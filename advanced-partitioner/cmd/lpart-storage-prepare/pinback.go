package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// Pinnable-layout: the resolved layout rendered as a complete, paste-ready
// selection=manual disklayout. It serves two consumers:
//
//   - The paste block (decisions/0007): always emitted to the part log and
//     forwarded to rsyslog, wrapped in greppable BEGIN/END markers, so an
//     operator can copy it into the node's (or group's) disklayout by hand.
//   - The write-back (decisions/0008): when a selection=discover set carries
//     save:true, the same document is POSTed to the node-level disklayout so
//     the node is pinned automatically for the next boot. See writeback.go.
//
// Writing a node-level disklayout shadows the group template in Luna's
// inheritance model, so the write-back is strictly opt-in (save:true); without
// it the layout is only ever emitted for a human to act on (0007's stance).

// pinBeginFmt / pinEndFmt are the copy-paste fences. %s is the host name.
const (
	pinBeginFmt = "----8<---- BEGIN %s disklayout (selection=manual, paste to pin) ----8<----"
	pinEndFmt   = "----8<---- END %s disklayout ----8<----"
)

// buildPinnableConfig turns the resolved sets back into a complete manual
// disklayout. Structural fields (role, raid, persistent, volumes) come from the
// original authored config so volumes are reproduced verbatim; the device list
// comes from what the resolver actually selected. selection becomes "manual" and
// the discover-only fields (save, count, spares, match) are dropped. A set that
// was selection=discover in the source carries the provenance stamp (origin,
// when non-nil); operator-authored manual sets do not. Returns nil when no set
// resolved to concrete disks (e.g. memboot/sanitize), so callers can skip the
// emit and the write-back.
func buildPinnableConfig(orig *v2.Config, resolved []result.ResolvedSet, origin *v2.Origin) *v2.Config {
	if orig == nil {
		return nil
	}
	out := &v2.Config{Version: v2.SchemaVersion}
	for _, rs := range resolved {
		if len(rs.SelectedDisks) == 0 {
			continue // tmpfs / memboot / unresolved — nothing to pin
		}
		src := findSet(orig, rs.Name)
		if src == nil {
			continue
		}
		devices := make([]string, 0, len(rs.SelectedDisks))
		for _, d := range rs.SelectedDisks {
			p := d.ConfiguredPath
			if p == "" {
				p = d.ResolvedPath
			}
			// Pin by STABLE identity when possible (FTA BR-9/MCS-6,
			// LOOSE-ENDS #8): a kernel name like /dev/sda drifts across
			// disk add/remove/reorder, silently repointing a pinned —
			// possibly persistent — set at a different physical disk. A
			// discovered set's kernel path is upgraded to its
			// /dev/disk/by-id link; operator-authored paths are kept
			// verbatim (their style is a choice).
			if src.SelectionMode() == "discover" {
				if stable := stableDevicePath(p); stable != "" {
					p = stable
				}
			}
			devices = append(devices, p)
		}
		pinned := v2.Set{
			Name:       src.Name,
			Role:       src.Role,
			Selection:  "manual",
			Raid:       src.Raid,
			Devices:    devices,
			Persistent: src.Persistent,
			Bootloader: src.Bootloader,
			Volumes:    src.Volumes,
		}
		// Stamp provenance only on sets the installer actually discovered;
		// operator-authored manual sets pass through unmarked.
		if origin != nil && src.SelectionMode() == "discover" {
			cp := *origin
			pinned.Origin = &cp
		}
		out.Sets = append(out.Sets, pinned)
	}
	if len(out.Sets) == 0 {
		return nil
	}
	return out
}

// byIDDir is the udev stable-identity directory; a var so tests can point
// it at a staged tempdir.
var byIDDir = "/dev/disk/by-id"

// stableDevicePath returns the best /dev/disk/by-id link that resolves to
// the given whole-disk kernel path, or "" when none exists (e.g. virtio
// disks without serials). Preference order: wwn-* (world-wide name, the
// most stable), then any other non-partition link. Partition links
// (*-part<N>) never match a whole-disk path, but are skipped defensively.
func stableDevicePath(kernelPath string) string {
	entries, err := os.ReadDir(byIDDir)
	if err != nil {
		return ""
	}
	best := ""
	for _, e := range entries {
		name := e.Name()
		if strings.Contains(name, "-part") {
			continue
		}
		full := filepath.Join(byIDDir, name)
		resolved, err := filepath.EvalSymlinks(full)
		if err != nil || resolved != kernelPath {
			continue
		}
		if strings.HasPrefix(name, "wwn-") {
			return full
		}
		if best == "" {
			best = full
		}
	}
	return best
}

// anySetSaves reports whether the config has at least one selection=discover
// set with save:true — the trigger for the daemon write-back (decisions/0008).
func anySetSaves(c *v2.Config) bool {
	if c == nil {
		return false
	}
	for i := range c.Sets {
		if c.Sets[i].Save && c.Sets[i].SelectionMode() == "discover" {
			return true
		}
	}
	return false
}

func findSet(c *v2.Config, name string) *v2.Set {
	for i := range c.Sets {
		if c.Sets[i].Name == name {
			return &c.Sets[i]
		}
	}
	return nil
}

// emitPinnableLayout renders the pinnable config as indented JSON and writes it
// to the operator log (and thus rsyslog) between the BEGIN/END fences. host
// labels the fences so a multi-node cluster-messages stream stays greppable.
func emitPinnableLayout(logger *txlog.Logger, host string, pin *v2.Config) {
	if pin == nil {
		return
	}
	if host == "" {
		host = "node"
	}
	body, err := json.MarshalIndent(pin, "", "  ")
	if err != nil {
		logger.Warn("pinnable layout marshal failed", "err", err.Error())
		return
	}
	logger.UserInfo("resolved layout (copy below to pin this node as selection=manual):")
	logger.UserInfo(pinBeginFmt, host)
	for _, line := range strings.Split(string(body), "\n") {
		logger.UserInfo("%s", line)
	}
	logger.UserInfo(pinEndFmt, host)
}
