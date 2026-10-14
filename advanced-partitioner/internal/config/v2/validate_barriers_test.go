package v2

// Barrier-pinning tests for the config-level protection rules (FTA BR-1a):
// cross-set device exclusivity and persistent-requires-manual. Removing
// either rule fails these tests.

import (
	"strings"
	"testing"
)

func validateJSON(t *testing.T, body string) error {
	t.Helper()
	cfg, err := Parse([]byte(body))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	return cfg.ValidateRaw([]byte(body))
}

func TestDuplicateDeviceAcrossSetsRejected(t *testing.T) {
	err := validateJSON(t, `{
  "version": 2,
  "sets": [
    {"name": "os", "role": "os", "selection": "manual", "raid": "none",
     "devices": ["/dev/sda"],
     "volumes": [
       {"name": "efi", "mountpoint": "/boot/efi", "fs": "vfat", "provider": "partition", "size": "512M"},
       {"name": "root", "mountpoint": "/", "fs": "xfs", "provider": "lvm", "size": "100%"}
     ]},
    {"name": "data", "role": "data", "selection": "manual", "raid": "none",
     "devices": ["/dev/sda"],
     "volumes": [
       {"name": "d", "mountpoint": "/data", "fs": "xfs", "provider": "lvm", "size": "100%"}
     ]}
  ]
}`)
	if err == nil {
		t.Fatal("a device shared by two sets must be rejected")
	}
	if !strings.Contains(err.Error(), "exactly one set") {
		t.Fatalf("wording: %v", err)
	}
}

func TestDuplicateDeviceNormalisedFormsRejected(t *testing.T) {
	// Same disk written with a redundant path form must still collide.
	err := validateJSON(t, `{
  "version": 2,
  "sets": [
    {"name": "os", "role": "os", "selection": "manual", "raid": "none",
     "devices": ["/dev/sda"],
     "volumes": [
       {"name": "efi", "mountpoint": "/boot/efi", "fs": "vfat", "provider": "partition", "size": "512M"},
       {"name": "root", "mountpoint": "/", "fs": "xfs", "provider": "lvm", "size": "100%"}
     ]},
    {"name": "data", "role": "data", "selection": "manual", "raid": "none",
     "devices": ["/dev//sda"],
     "volumes": [
       {"name": "d", "mountpoint": "/data", "fs": "xfs", "provider": "lvm", "size": "100%"}
     ]}
  ]
}`)
	if err == nil {
		t.Fatal("normalised duplicate (/dev//sda vs /dev/sda) must be rejected")
	}
}

func TestPersistentRequiresManual(t *testing.T) {
	err := validateJSON(t, `{
  "version": 2,
  "sets": [
    {"name": "data", "role": "data", "selection": "discover", "raid": "none", "count": 1,
     "persistent": true,
     "volumes": [
       {"name": "d", "mountpoint": "/data", "fs": "xfs", "provider": "lvm", "size": "100%"}
     ]}
  ]
}`)
	if err == nil {
		t.Fatal("persistent=true with selection=discover must be rejected")
	}
	if !strings.Contains(err.Error(), "selection=manual") {
		t.Fatalf("wording: %v", err)
	}
}

// Device-mapper / multipath paths are refused as set devices (FTA v2 BE-N3 /
// CCF-8, barrier NB-4): dm partition naming has no reliable convention, so
// the partition-path chokepoint cannot derive member partitions safely.
// Also pins that SINGLE-device sets get style-classified at all (they used
// to skip the check entirely).
func TestDeviceMapperPathsRejected(t *testing.T) {
	mk := func(dev string) string {
		return `{
  "version": 2,
  "sets": [
    {"name": "os", "role": "os", "selection": "manual", "raid": "none",
     "devices": ["` + dev + `"],
     "volumes": [
       {"name": "efi", "mountpoint": "/boot/efi", "fs": "vfat", "provider": "partition", "size": "512M"},
       {"name": "root", "mountpoint": "/", "fs": "xfs", "provider": "lvm", "size": "100%"}
     ]}
  ]
}`
	}
	for _, dev := range []string{"/dev/mapper/mpatha", "/dev/dm-0"} {
		err := validateJSON(t, mk(dev))
		if err == nil {
			t.Fatalf("%s must be rejected as a set device", dev)
		}
		if !strings.Contains(err.Error(), "device-mapper/multipath") {
			t.Fatalf("%s: error must name the dm refusal, got: %v", dev, err)
		}
	}
	// Single-device NON-/dev path is now caught too (used to slip through).
	if err := validateJSON(t, mk("sda-without-dev-prefix")); err == nil {
		t.Fatal("a single non-/dev device path must be rejected")
	}
	// Sanity: a plain kernel path still validates.
	if err := validateJSON(t, mk("/dev/sda")); err != nil {
		t.Fatalf("plain /dev/sda must stay valid: %v", err)
	}
}

// allow_degraded is a boolean, and only means something on a set that has a
// member to lose: raid 1, 5, 6 or 10.
func TestAllowDegradedOnlyOnRedundantRaid(t *testing.T) {
	layout := func(raid, value string) string {
		return `{"version": 2, "sets": [{"name": "data", "role": "data", "selection": "manual", "raid": "` + raid + `",
  "allow_degraded": ` + value + `, "devices": ["/dev/sda", "/dev/sdb"],
  "volumes": [{"name": "d", "mountpoint": "/data", "fs": "xfs", "provider": "lvm", "size": "100%"}]}]}`
	}
	if err := validateJSON(t, layout("1", "true")); err != nil {
		t.Fatalf("raid1 with allow_degraded: %v", err)
	}
	err := validateJSON(t, layout("0", "true"))
	if err == nil || !strings.Contains(err.Error(), "allow_degraded is valid on a redundant raid set only") {
		t.Fatalf("raid0 with allow_degraded must be refused, got %v", err)
	}
	if _, err := Parse([]byte(layout("1", `"yes"`))); err == nil {
		t.Fatal("a non-boolean allow_degraded must not parse")
	}
}
