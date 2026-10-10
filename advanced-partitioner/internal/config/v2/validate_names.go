package v2

import (
	"fmt"
	"regexp"
	"strings"
)

// Set and volume names are not labels: they become kernel and LVM object
// names, and one of them is handed to `rm -rf`.
//
//   - set name: the LVM VG name (/dev/<set>/<lv>, /dev/mapper/<set>-<lv>), the
//     md array name (/dev/md/<MDName>, -efi, -boot: non-alphanumerics folded
//     to '_'), and the cleanup path `rm -rf /dev/<set>`
//     (internal/cleanup.teardownVG).
//   - volume name: the LV name of an lvm volume and the GPT partition label of
//     a partition volume.
//
// A name the stack cannot carry used to pass validation and fail only after
// cleanup had wiped the disks (or, for a name like "disk", have cleanup delete
// /dev/disk itself). These rules refuse such names up front.

// nameRE is the character set for set and volume names: the intersection of
// what LVM accepts for VG/LV names and what survives the md-name fold
// unambiguously.
var nameRE = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

// reservedSetNames are entries that exist directly under /dev on RHEL (and the
// other supported installer initramfs). Cleanup runs `rm -rf /dev/<set name>`,
// so a set named after one of them deletes it: "disk" removes every by-id /
// by-path link the installer resolves devices through, "md" and "mapper" the
// arrays and LVs being built, "log" the journal socket. LVM would also place
// the VG directory on top of the entry.
var reservedSetNames = map[string]struct{}{
	// directories
	".": {}, "..": {}, "block": {}, "bsg": {}, "bus": {}, "char": {}, "cpu": {},
	"disk": {}, "dma_heap": {}, "dri": {}, "fd": {}, "hugepages": {},
	"infiniband": {}, "input": {}, "lvm": {}, "mapper": {}, "md": {},
	"mqueue": {}, "net": {}, "pts": {}, "raw": {}, "serial": {}, "shm": {},
	"snd": {}, "usb": {}, "vfio": {},
	// device nodes, sockets and links every installer environment carries
	"autofs": {}, "btrfs-control": {}, "console": {}, "core": {},
	"cpu_dma_latency": {}, "cuse": {}, "full": {}, "fuse": {}, "hpet": {},
	"initctl": {}, "kmsg": {}, "kvm": {}, "log": {}, "loop-control": {},
	"mcelog": {}, "mem": {}, "nvme-fabrics": {}, "nvram": {}, "null": {},
	"port": {}, "ppp": {}, "ptmx": {}, "random": {}, "rfkill": {}, "rtc": {},
	"snapshot": {}, "stderr": {}, "stdin": {}, "stdout": {}, "tty": {},
	"udmabuf": {}, "uhid": {}, "uinput": {}, "urandom": {}, "userfaultfd": {},
	"vga_arbiter": {}, "vhost-net": {}, "vhost-vsock": {}, "vsock": {},
	"watchdog": {}, "zero": {},
}

// kernelDevNameRE matches kernel block/char device node names (with an
// optional partition suffix). A set named "sda" would make cleanup run
// `rm -rf /dev/sda` — the node of a disk the installer is about to partition.
var kernelDevNameRE = regexp.MustCompile(
	`^((sd|vd|xvd)[a-z]{1,2}|nvme[0-9]+(n[0-9]+)?|md[0-9]+|dm-[0-9]+|loop[0-9]+|` +
		`sr[0-9]+|sg[0-9]+|nbd[0-9]+|ram[0-9]+|zram[0-9]+|mmcblk[0-9]+|` +
		`tty([A-Z]+[0-9]*|[0-9]+)|vcs[au]?[0-9]*|hidraw[0-9]+|rtc[0-9]+|watchdog[0-9]+|tpm(rm)?[0-9]+)` +
		`(p?[0-9]+)?$`)

// lvmReservedLVPrefixes and lvmReservedLVSubstrings are the LV names lvcreate
// refuses (lvm2 apply_lvname_restrictions): names starting with "snapshot" or
// "pvmove", and names containing an internal sub-LV component suffix.
var (
	lvmReservedLVPrefixes   = []string{"snapshot", "pvmove"}
	lvmReservedLVSubstrings = []string{
		"_cdata", "_cmeta", "_corig", "_cpool", "_cvol", "_imeta", "_iorig",
		"_mimage", "_mlog", "_pmspare", "_rimage", "_rmeta", "_tdata", "_tmeta",
		"_vdata", "_vorigin", "_wcorig",
	}
)

// setRef names a set in an operator-facing message: index and name.
func setRef(idx int, name string) string {
	return fmt.Sprintf("sets[%d] (set %q)", idx, name)
}

// volRef names a volume in an operator-facing message: indexes and names.
func volRef(setIdx int, setName string, volIdx int, volName string) string {
	return fmt.Sprintf("sets[%d].volumes[%d] (set %q, volume %q)", setIdx, volIdx, setName, volName)
}

// validateSetName checks one set name in isolation (the cross-set collisions
// are validateSetNameCollisions).
func validateSetName(idx int, name string) error {
	ref := setRef(idx, name)
	if !nameRE.MatchString(name) {
		return fmt.Errorf(
			"config_validation: %s: name may only contain letters, digits, '_' and '-' (it becomes the LVM volume group, the md array name and a path under /dev)",
			ref)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("config_validation: %s: name must not start with '-' (LVM refuses such a volume group name)", ref)
	}
	if (&Set{Name: name}).MDName() == "" {
		return fmt.Errorf(
			"config_validation: %s: name must contain a letter or digit (the md array name /dev/md/<name> is the name with every other character folded to '_' and trailing '_' trimmed, which leaves nothing)",
			ref)
	}
	if _, reserved := reservedSetNames[name]; reserved || kernelDevNameRE.MatchString(name) {
		return fmt.Errorf(
			"config_validation: %s: name is reserved: /dev/%s exists on the installer, and cleanup removes /dev/<set name> (rm -rf) while LVM puts the volume group there; choose another set name",
			ref, name)
	}
	return nil
}

// validateVolumeName checks one volume name in isolation; lvm reports whether
// the volume becomes a logical volume (LVM's reserved names apply only then).
func validateVolumeName(setIdx int, setName string, volIdx int, name string, lvm bool) error {
	ref := volRef(setIdx, setName, volIdx, name)
	if !nameRE.MatchString(name) {
		return fmt.Errorf(
			"config_validation: %s: name may only contain letters, digits, '_' and '-' (it becomes the LV name or the GPT partition label)",
			ref)
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("config_validation: %s: name must not start with '-' (lvcreate refuses such a logical volume name)", ref)
	}
	if !lvm {
		return nil
	}
	if strings.HasPrefix(name, "_") {
		return fmt.Errorf("config_validation: %s: an lvm volume name must not start with '_' (LVM reserves it for internal volumes)", ref)
	}
	for _, p := range lvmReservedLVPrefixes {
		if strings.HasPrefix(name, p) {
			return fmt.Errorf("config_validation: %s: lvm volume names starting with %q are reserved by LVM (lvcreate refuses them)", ref, p)
		}
	}
	for _, sub := range lvmReservedLVSubstrings {
		if strings.Contains(name, sub) {
			return fmt.Errorf("config_validation: %s: lvm volume names containing %q are reserved by LVM for internal sub-volumes (lvcreate refuses them)", ref, sub)
		}
	}
	return nil
}

// validateVolumeNamesUnique refuses two volumes of one set with the same name:
// lvcreate refuses a second LV of that name in the VG, and the partition-number
// predictor identifies a volume by its name.
func (s *Set) validateVolumeNamesUnique(idx int) error {
	seen := make(map[string]int, len(s.Volumes))
	for j, v := range s.Volumes {
		if prev, dup := seen[v.Name]; dup {
			return fmt.Errorf(
				"config_validation: %s: volume name %q is used by volumes[%d] and volumes[%d]; volume names must be unique within a set (they are the LV names of the volume group)",
				setRef(idx, s.Name), v.Name, prev, j)
		}
		seen[v.Name] = j
	}
	return nil
}

// validateSetNameCollisions refuses set names that end up naming the same
// kernel or LVM object:
//   - the same name twice (one VG, one md array, one cleanup target);
//   - two names that fold to the same md name (data-1 and data_1 both become
//     /dev/md/data_1);
//   - a name whose device-mapper prefix covers another set's LVs: cleanup
//     removes every /dev/mapper entry starting with "<set>-", and the mapper
//     name of LV "x" in VG "os-a" is "os--a-x", so cleaning set "os" would
//     remove set "os-a"'s volumes.
func (c *Config) validateSetNameCollisions() error {
	byName := make(map[string]int, len(c.Sets))
	byMD := make(map[string]int, len(c.Sets))
	for i := range c.Sets {
		s := &c.Sets[i]
		if prev, dup := byName[s.Name]; dup {
			return fmt.Errorf(
				"config_validation: sets[%d] and sets[%d] are both named %q; set names must be unique (each is a volume group, an md array and a cleanup target)",
				prev, i, s.Name)
		}
		byName[s.Name] = i
		md := s.MDName()
		if prev, dup := byMD[md]; dup {
			return fmt.Errorf(
				"config_validation: set names %q (sets[%d]) and %q (sets[%d]) both map to the md array /dev/md/%s (non-alphanumerics fold to '_'); rename one of them",
				c.Sets[prev].Name, prev, s.Name, i, md)
		}
		byMD[md] = i
	}
	for i := range c.Sets {
		a := &c.Sets[i]
		prefix := a.Name + "-"
		for j := range c.Sets {
			if i == j {
				continue
			}
			b := &c.Sets[j]
			for _, v := range b.Volumes {
				if v.Provider != "lvm" {
					continue
				}
				mapper := dmMapperName(b.Name, v.Name)
				if strings.HasPrefix(mapper, prefix) {
					return fmt.Errorf(
						"config_validation: set names %q (sets[%d]) and %q (sets[%d]) clash: cleanup of set %q removes every /dev/mapper/%s* entry, which includes /dev/mapper/%s (volume %q of set %q); rename one of the sets",
						a.Name, i, b.Name, j, a.Name, prefix, mapper, v.Name, b.Name)
				}
			}
		}
	}
	return nil
}

// dmMapperName is the device-mapper name of LV lv in VG vg (the entry under
// /dev/mapper): each '-' inside a name is doubled and a single '-' joins the
// two, exactly as internal/devices.VGLVPath builds it.
func dmMapperName(vg, lv string) string {
	return strings.ReplaceAll(vg, "-", "--") + "-" + strings.ReplaceAll(lv, "-", "--")
}
