package cleanup

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// stopMDArrays is _cleanup_mdraid:
//
//  1. Stop the named arrays this set would create (/dev/md/<name> and the
//     per-set efi/boot mirrored arrays).
//  2. Then, in a retry loop, walk /proc/mdstat for any array whose members
//     intersect the selected disks, stop it, and zero its members and the
//     whole disk + partitions — repeating until no such array remains, so a
//     udev-re-assembled stale array cannot survive to block re-partitioning.
func (c *Cleaner) stopMDArrays(ctx context.Context, setName string, selected, protected map[string]struct{}) error {
	if !c.o.CommandAvailable("mdadm") {
		return nil
	}
	c.log("cleanup: stopping md arrays related to %s", setName)

	// Protected-disk barrier: an array whose members intersect the selected
	// disks would be stopped and have ALL its member superblocks zeroed —
	// including members living on a persistent set's disk. That destroys
	// the persistent array. Refuse the whole cleanup instead (hard error);
	// the layout conflict needs an operator, not a guess.
	if len(protected) > 0 {
		if mdstat, err := os.ReadFile(c.o.MDStatPath); err == nil {
			for _, arr := range parseMDStatArrays(string(mdstat)) {
				if !c.arrayTouchesSelected(ctx, arr, selected) {
					continue
				}
				if c.arrayTouchesSelected(ctx, arr, protected) {
					return protectedf(
						"cleanup: md array %s spans a selected disk and a persistent set's disk — refusing to stop/zero it (protected-disk barrier)",
						arr.Device)
				}
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			c.warn("cleanup: read %s: %v", c.o.MDStatPath, err)
		}
		// DORMANT arrays: a STOPPED spanning array is invisible to /proc/mdstat,
		// so the barrier above never saw it and the whole-disk zero sweep below
		// destroyed the selected member without any refusal (found live by
		// regression 3.47: `mdadm --zero-superblock /dev/vdb1` ran while the
		// array's OTHER member sat on a persistent set's disk — a raid1 sibling
		// survives that, a spanning raid0/5 is silently destroyed). Examine the
		// superblocks directly: any Array UUID present on BOTH a selected and a
		// protected disk is the same conflict, running or not.
		selUUIDs := c.dormantArrayUUIDs(ctx, selected)
		if len(selUUIDs) > 0 {
			protUUIDs := c.dormantArrayUUIDs(ctx, protected)
			for uuid, part := range selUUIDs {
				if pp, ok := protUUIDs[uuid]; ok {
					return protectedf(
						"cleanup: dormant md array %s spans a selected disk (member %s) and a persistent set's disk (member %s) — refusing to zero it (protected-disk barrier)",
						uuid, part, pp)
				}
			}
		}
	}

	// Step 1: named arrays.
	mdName := mdNameForSet(setName)
	c.stopMDIfExists(ctx, "/dev/md/"+mdName+"-efi")
	c.stopMDIfExists(ctx, "/dev/md/"+mdName+"-boot")
	c.stopMDIfExists(ctx, "/dev/md/"+mdName)

	// Steps 2+3, in a retry loop that defeats udev auto-re-assembly. Stopping
	// an array leaves the on-member superblocks intact, so udev's incremental
	// assembler can re-create the array (busy device) before we zero them — a
	// stale array then blocks the re-partition (partprobe EBUSY). Each pass
	// therefore: (a) stops every array whose members are on the selected disks
	// and zeroes that array's members *immediately* (no settle in between, to
	// beat the re-assembler); then (b) zeroes the whole disk + every partition.
	// Repeat until a pass finds no array touching the selected disks (its
	// superblocks are gone, so it can no longer reassemble), or the cap.
	const maxPasses = 8
	for pass := 0; pass < maxPasses; pass++ {
		active := false
		if mdstat, err := os.ReadFile(c.o.MDStatPath); err != nil {
			if !errors.Is(err, os.ErrNotExist) {
				c.warn("cleanup: read %s: %v", c.o.MDStatPath, err)
			}
		} else {
			for _, arr := range parseMDStatArrays(string(mdstat)) {
				if !c.arrayTouchesSelected(ctx, arr, selected) {
					continue
				}
				active = true
				c.runBest(ctx, "mdadm", "--stop", arr.Device)
				// Zero this array's members now, before udev re-assembles them.
				for _, m := range arr.Members {
					c.runBest(ctx, "mdadm", "--zero-superblock", "/dev/"+m)
				}
			}
		}
		// Whole-disk + partition superblocks (catches 1.0/0.90 metadata at the
		// disk end and any member the line did not enumerate). wipefs -a too:
		// it strips the md signature even when --zero-superblock lost the race,
		// so the partition cannot be re-assembled.
		for _, disk := range selectedKeys(selected, true) {
			c.runBest(ctx, "mdadm", "--zero-superblock", "/dev/"+disk)
			for _, part := range c.partitionsOf(ctx, "/dev/"+disk) {
				c.runBest(ctx, "mdadm", "--zero-superblock", part)
				c.runBest(ctx, "wipefs", "-a", part)
			}
		}
		// No udevadm settle inside the loop: settle blocks until udev has run
		// its incremental assembler, which re-creates the very array we just
		// stopped. Looping stop+zero without settling starves the re-assembler;
		// a single settle after convergence lets the now-clean state quiesce.
		if !active {
			break
		}
	}
	c.runBest(ctx, "udevadm", "settle")
	return nil
}

// EnsureNoAutoAssemble appends "AUTO -all" to each mdadm config path that does
// not already contain it (creating the file and parent dir when absent), so
// udev's incremental assembler (mdadm -I, run on every block uevent) will NOT
// re-create md arrays from surviving superblocks while the installer is
// cleaning, partitioning and (re)creating arrays.
//
// This is the root-cause fix for the re-assembly race: stopping an array leaves
// the on-member superblocks intact, and udev re-assembles the array onto the
// just-(re)partitioned member before it can be zeroed — leaving the member busy
// and breaking re-partition / mdadm --create. Site-specific stop+zero loops
// only relocate the race; disabling auto-assembly removes it. It does not
// disturb arrays already assembled at boot (AUTO -all governs future
// incremental assembly only), so healthy/persistent mount-existing paths are
// unaffected. Best-effort: returns the first error; callers log and continue.
func EnsureNoAutoAssemble(paths ...string) error {
	for _, p := range paths {
		if b, err := os.ReadFile(p); err == nil { //nolint:gosec // mdadm.conf path is a fixed installer config location, not user input
			if strings.Contains(string(b), "AUTO -all") {
				continue
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil { //nolint:gosec // installer RAM root; dir must be traversable
			return err
		}
		f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644) //nolint:gosec // mdadm.conf is world-readable by design
		if err != nil {
			return err
		}
		if _, err := f.WriteString("\n# disabled by lpart-storage-prepare during install\nAUTO -all\n"); err != nil {
			_ = f.Close()
			return err
		}
		if err := f.Close(); err != nil {
			return err
		}
	}
	return nil
}

// stopMDIfExists tries to stop both the symlink and its target. Mirrors
// the _stop_md_if_exists helper that follows a level of indirection
// because /dev/md/<name> can be a symlink to /dev/md0.
func (c *Cleaner) stopMDIfExists(ctx context.Context, mdDevice string) {
	if _, err := c.o.Stat(mdDevice); err != nil {
		return
	}
	c.runBest(ctx, "mdadm", "--stop", mdDevice)
	resolved, err := filepath.EvalSymlinks(mdDevice)
	if err == nil && resolved != mdDevice {
		c.runBest(ctx, "mdadm", "--stop", resolved)
	}
	c.runBest(ctx, "udevadm", "settle")
}

// partitionsOf returns the partition device paths of a whole disk by
// asking lsblk. KNAME, not PATH: the PATH column arrived in util-linux 2.33,
// and el8 ships 2.32 — with PATH the lsblk call fails there, partitionsOf
// returns nil, and the dormant protected-disk barrier silently never sees
// the partition superblocks (found live: battery S24 + regression 3.47
// refused=0 on rocky8). KNAME exists everywhere and for a disk's partitions
// "/dev/"+KNAME is exactly what PATH printed.
// dormantArrayUUIDs maps md Array UUIDs found in the superblocks on the given
// disks (whole disk + every partition) to one carrying device path. Running
// state is irrelevant: --examine reads the on-disk metadata.
func (c *Cleaner) dormantArrayUUIDs(ctx context.Context, disks map[string]struct{}) map[string]string {
	out := map[string]string{}
	for _, disk := range selectedKeys(disks, true) {
		devs := append([]string{"/dev/" + disk}, c.partitionsOf(ctx, "/dev/"+disk)...)
		for _, dev := range devs {
			if uuid := c.mdArrayUUID(ctx, dev); uuid != "" {
				out[uuid] = dev
			}
		}
	}
	return out
}

// mdArrayUUID returns the Array UUID from a device's md superblock, or "".
func (c *Cleaner) mdArrayUUID(ctx context.Context, dev string) string {
	res, ok := c.runOK(ctx, "mdadm", "--examine", dev)
	if !ok {
		return ""
	}
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		trimmed := strings.TrimSpace(line)
		if v, found := strings.CutPrefix(trimmed, "Array UUID :"); found {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func (c *Cleaner) partitionsOf(ctx context.Context, disk string) []string {
	res, ok := c.runOK(ctx, "lsblk", "-ln", "-o", "KNAME,TYPE", disk)
	if !ok {
		return nil
	}
	var parts []string
	for _, line := range strings.Split(string(res.Stdout), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && fields[1] == "part" {
			parts = append(parts, "/dev/"+fields[0])
		}
	}
	return parts
}

// mdNameForSet mirrors _set_md_name: strip non-alnum, trim trailing
// underscore.
func mdNameForSet(setName string) string {
	var b strings.Builder
	for _, r := range setName {
		if isAlnum(r) {
			b.WriteRune(r)
		} else {
			b.WriteRune('_')
		}
	}
	out := b.String()
	return strings.TrimRight(out, "_")
}

func isAlnum(r rune) bool {
	return ('a' <= r && r <= 'z') || ('A' <= r && r <= 'Z') || ('0' <= r && r <= '9')
}

// mdStatLineRE matches the leading "mdN" device name on each /proc/mdstat
// line. Anchored by the start-of-line marker in the regex.
var mdStatLineRE = regexp.MustCompile(`^(md\d+)\s*:`)

// parseMDStat returns the /dev paths of every active md device listed in
// /proc/mdstat. Mirrors the `awk '/^md[0-9]+/ {print "/dev/" $1}'` call.
func parseMDStat(body string) []string {
	var out []string
	for _, line := range strings.Split(body, "\n") {
		if m := mdStatLineRE.FindStringSubmatch(line); m != nil {
			out = append(out, "/dev/"+m[1])
		}
	}
	return out
}

// mdArray is one /proc/mdstat array: its device path and the member basenames
// listed on its line.
type mdArray struct {
	Device  string
	Members []string
}

// mdMemberRE pulls the member basename out of an mdstat member token like
// "sdc1[2](S)" or "nvme0n1p3[1]" -> "sdc1" / "nvme0n1p3".
var mdMemberRE = regexp.MustCompile(`^([a-zA-Z][a-zA-Z0-9]*)\[\d+\]`)

// parseMDStatArrays parses each "mdN : <state> member[i](flags) ..." line into
// the md device and its member basenames. Crucially it reads members straight
// from the line, so it also enumerates INACTIVE arrays — which mdadm --detail
// cannot, and which (left running) hold a selected disk busy and make a
// re-partition time out. Mirrors awk over /proc/mdstat.
func parseMDStatArrays(body string) []mdArray {
	var out []mdArray
	for _, line := range strings.Split(body, "\n") {
		m := mdStatLineRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		arr := mdArray{Device: "/dev/" + m[1]}
		rest := line[strings.Index(line, ":")+1:]
		for _, tok := range strings.Fields(rest) {
			if mm := mdMemberRE.FindStringSubmatch(tok); mm != nil {
				arr.Members = append(arr.Members, mm[1])
			}
		}
		out = append(out, arr)
	}
	return out
}

// arrayTouchesSelected reports whether an md array has any member on the
// selected disks. It checks the members parsed from the /proc/mdstat line
// first (present even for inactive arrays), then falls back to mdadm --detail
// for active arrays whose line did not enumerate members.
func (c *Cleaner) arrayTouchesSelected(ctx context.Context, arr mdArray, selected map[string]struct{}) bool {
	for _, m := range arr.Members {
		if PartitionOnSelectedDisk(m, selected) {
			return true
		}
	}
	members, err := c.o.MDAdmDetail(ctx, arr.Device)
	if err != nil {
		return false
	}
	for _, m := range members {
		if PartitionOnSelectedDisk(m, selected) {
			return true
		}
	}
	return false
}

// defaultMDAdmDetail returns the member device paths reported by
// `mdadm --detail <md>` — every line whose first non-blank character is
// a digit, taking the last whitespace-separated field as the device path.
func (c *Cleaner) defaultMDAdmDetail(ctx context.Context, mdDevice string) ([]string, error) {
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          "mdadm",
		Args:          []string{"--detail", mdDevice},
		CaptureStdout: true,
		CaptureStderr: true,
	})
	if err != nil {
		return nil, err
	}
	return parseMDAdmDetailMembers(string(res.Stdout)), nil
}

// parseMDAdmDetailMembers extracts the trailing device-path column from
// every data row of `mdadm --detail`. Example row:
//
//	"      0       8        1        0      active sync   /dev/sda1"
//
// We pick the last whitespace-separated field on each line whose first
// non-blank character is a digit.
func parseMDAdmDetailMembers(out string) []string {
	var members []string
	for _, line := range strings.Split(out, "\n") {
		trimmed := strings.TrimLeft(line, " \t")
		if trimmed == "" {
			continue
		}
		if !unicode.IsDigit(rune(trimmed[0])) {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		last := fields[len(fields)-1]
		// mdadm marks faulty/spare devices with descriptive tokens
		// instead of paths in some versions ("removed", "failed"); skip
		// anything that doesn't look like a /dev path.
		if strings.HasPrefix(last, "/dev/") {
			members = append(members, last)
		}
	}
	return members
}

// selectedKeys returns the basenames in a deterministic order so cleanup
// produces reproducible logs. sortAsc=true sorts ascending; false leaves
// map-iteration order (debugging convenience).
func selectedKeys(set map[string]struct{}, sortAsc bool) []string {
	keys := make([]string, 0, len(set))
	for k := range set {
		keys = append(keys, k)
	}
	if sortAsc {
		stringsSort(keys)
	}
	return keys
}

// stringsSort exists because cleanup.go imports many packages already
// and sort.Strings would force one more line at the top of every file
// importing this package. Centralising it here keeps imports tidy.
func stringsSort(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}
