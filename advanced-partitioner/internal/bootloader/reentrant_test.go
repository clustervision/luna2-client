package bootloader

import (
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// Phase 11 re-entrant scenarios for bootloader.WriteFstab. The fstab
// is operator-editable, so finalise must never duplicate entries or
// overwrite manual edits when called a second time.

func TestReentrantFstabSecondCallDoesNotDuplicate(t *testing.T) {
	sysroot := t.TempDir()
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", Device: "/dev/mapper/os-root", UUID: "root-uuid"},
			{Mount: "/boot", FS: "xfs", Device: "/dev/sda2", UUID: "boot-uuid"},
			{Mount: "/boot/efi", FS: "vfat", Device: "/dev/sda1", UUID: "efi-uuid"},
			{Mount: "swap", FS: "swap", Device: "/dev/mapper/os-swap", UUID: "swap-uuid"},
		},
	}
	for i := 0; i < 3; i++ {
		if err := WriteFstab(fsops.FS{}, sysroot, sr); err != nil {
			t.Fatalf("pass %d: %v", i+1, err)
		}
	}
	body, err := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	s := string(body)
	for _, mp := range []string{"/", "/boot", "/boot/efi", "swap"} {
		count := countMountLines(s, mp)
		if count != 1 {
			t.Fatalf("mount %s appeared %d times after 3 WriteFstab calls; want 1\nfstab:\n%s",
				mp, count, s)
		}
	}
}

func TestReentrantFstabPreservesManualEdits(t *testing.T) {
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "etc"), 0o750); err != nil {
		t.Fatal(err)
	}
	// The operator hand-added TWO lines: one for a mount OUTSIDE the
	// layout (/backup — must survive verbatim) and one for /data, which
	// THIS install just reformatted. The /data line's UUID is dangling by
	// construction (mkfs minted a new one), so the installer-owned entry
	// replaces it — the same replace-by-mountpoint rule that fixes the
	// stale golden-image root entry (ancestor FTA MCS-6). Installer-owned
	// mounts are authoritative, like grub.cfg; everything else survives.
	manual := "" +
		"# Manual edit by operator\n" +
		"UUID=backup-uuid /backup ext4 defaults,nofail 0 2\n" +
		"UUID=manual-data /data ext4 defaults,nofail 0 2\n"
	if err := os.WriteFile(filepath.Join(sysroot, "etc", "fstab"), []byte(manual), 0o600); err != nil {
		t.Fatal(err)
	}
	sr := result.StorageResult{
		Filesystems: []result.Filesystem{
			{Mount: "/", FS: "xfs", Device: "/dev/sda1", UUID: "auto-root"},
			{Mount: "/data", FS: "ext4", Device: "/dev/sda2", UUID: "auto-data"},
		},
	}
	if err := WriteFstab(fsops.FS{}, sysroot, sr); err != nil {
		t.Fatalf("WriteFstab: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(sysroot, "etc", "fstab")) //nolint:gosec // test artefact
	s := string(body)
	if !strings.Contains(s, "# Manual edit by operator") {
		t.Fatalf("manual comment lost:\n%s", s)
	}
	if !strings.Contains(s, "UUID=backup-uuid /backup ext4 defaults,nofail 0 2") {
		t.Fatalf("non-owned manual /backup line must survive verbatim:\n%s", s)
	}
	if strings.Contains(s, "UUID=manual-data") {
		t.Fatalf("dangling manual /data entry (fs was just reformatted) must be replaced:\n%s", s)
	}
	if !strings.Contains(s, "UUID=auto-data /data ext4 defaults 1 2") {
		t.Fatalf("installer-owned /data entry missing:\n%s", s)
	}
}

// countMountLines returns the number of fstab data lines that mention
// `mp` as a whitespace-bounded token. Comments and empty lines are
// ignored.
func countMountLines(s, mp string) int {
	count := 0
	for _, line := range strings.Split(s, "\n") {
		trim := strings.TrimSpace(line)
		if trim == "" || strings.HasPrefix(trim, "#") {
			continue
		}
		if hasToken(trim, mp) {
			count++
		}
	}
	return count
}

func hasToken(line, tok string) bool {
	for _, f := range strings.Fields(line) {
		if f == tok {
			return true
		}
	}
	return false
}
