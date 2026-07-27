package bootloader

import (
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// TestStageSignedShimARM64 covers the arm64 path: shim/grub binaries
// have aa64 suffixes, the removable fallback is BOOTAA64.EFI, and
// the override CSV is BOOTAA64.CSV (not BOOTX64.CSV). The amd64
// equivalent is exercised by TestRHELInstallUEFI in rhel_test.go.
func TestStageSignedShimARM64(t *testing.T) {
	sysroot := t.TempDir()
	// Stage signed shimaa64.efi + grubaa64.efi under
	// /boot/efi/EFI/rocky/ as if the OS image had been unpacked.
	rocky := filepath.Join(sysroot, "boot", "efi", "EFI", "rocky")
	if err := os.MkdirAll(rocky, 0o750); err != nil {
		t.Fatal(err)
	}
	shim := []byte("SIGNED-SHIM-AARCH64")
	grub := []byte("SIGNED-GRUB-AARCH64")
	if err := os.WriteFile(filepath.Join(rocky, "shimaa64.efi"), shim, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(rocky, "grubaa64.efi"), grub, 0o600); err != nil {
		t.Fatal(err)
	}

	err := StageSignedShim(StageSignedShimOpts{
		FS:      fsops.FS{},
		Sysroot: sysroot,
		Arch:    "aarch64",
		StorageResult: result.StorageResult{
			Filesystems: []result.Filesystem{
				{Mount: "/boot", UUID: "abcdef00-0000-0000-0000-000000000001"},
			},
		},
		GrubDirName: "grub2",
	})
	if err != nil {
		t.Fatalf("StageSignedShim aarch64: %v", err)
	}

	// Removable fallback lives at /EFI/BOOT/BOOTAA64.EFI on arm64.
	bootDir := filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT")
	if got, _ := os.ReadFile(filepath.Join(bootDir, "BOOTAA64.EFI")); string(got) != string(shim) { //nolint:gosec // test artefact
		t.Fatalf("BOOTAA64.EFI != signed shim:\n  got=%q want=%q", got, shim)
	}
	if got, _ := os.ReadFile(filepath.Join(bootDir, "grubaa64.efi")); string(got) != string(grub) { //nolint:gosec // test artefact
		t.Fatalf("grubaa64.efi in /EFI/BOOT != signed grub:\n  got=%q want=%q", got, grub)
	}
	// The plain-name alias must sit beside it: SUSE shim builds chain to
	// grub.efi (not grub<arch>.efi) — a missing alias killed the surviving
	// raid1 leg's boot on opensuse (regression 3.28, silent PXE re-provision).
	if got, _ := os.ReadFile(filepath.Join(bootDir, "grub.efi")); string(got) != string(grub) { //nolint:gosec // test artefact
		t.Fatalf("grub.efi alias in /EFI/BOOT != signed grub:\n  got=%q want=%q", got, grub)
	}
	// And the stub grub.cfg: SUSE grub's cmdpath-derived $prefix reads its
	// config from the LOAD directory (EFI/BOOT), not a baked /EFI/<distro>.
	if got, _ := os.ReadFile(filepath.Join(bootDir, "grub.cfg")); !strings.Contains(string(got), "abcdef00-0000-0000-0000-000000000001") { //nolint:gosec // test artefact
		t.Fatalf("EFI/BOOT/grub.cfg stub missing or lacks the /boot UUID:\n  got=%q", got)
	}
	// x64 names must NOT appear in /EFI/BOOT on arm64.
	if _, err := os.Stat(filepath.Join(bootDir, "BOOTX64.EFI")); err == nil {
		t.Fatalf("BOOTX64.EFI must not be created on arm64")
	}

	// Override CSV is BOOTAA64.CSV, not BOOTX64.CSV.
	csv, err := os.ReadFile(filepath.Join(rocky, "BOOTAA64.CSV")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("BOOTAA64.CSV: %v", err)
	}
	if _, err := os.Stat(filepath.Join(rocky, "BOOTX64.CSV")); err == nil {
		t.Fatalf("BOOTX64.CSV must not be created on arm64")
	}
	// CSV body starts with the per-arch shim filename and the
	// TrinityX vendor token (UCS-2 LE).
	if len(csv)%2 != 0 {
		t.Fatalf("BOOTAA64.CSV byte length is odd, not UCS-2 LE: %d", len(csv))
	}
	u16 := make([]uint16, len(csv)/2)
	for i := 0; i < len(csv); i += 2 {
		u16[i/2] = uint16(csv[i]) | uint16(csv[i+1])<<8
	}
	got := utf16ToString(u16)
	if !strings.HasPrefix(got, "shimaa64.efi,TrinityX,,") {
		t.Fatalf("BOOTAA64.CSV does not start with shimaa64.efi,TrinityX,,...:\n  %q", got)
	}

	// Stub grub.cfg landed in /EFI/rocky/.
	stub, err := os.ReadFile(filepath.Join(rocky, "grub.cfg")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("stub grub.cfg: %v", err)
	}
	for _, want := range []string{
		"search --no-floppy --fs-uuid --set=dev abcdef00-0000-0000-0000-000000000001",
		"set prefix=($dev)/grub2",
		"configfile $prefix/grub.cfg",
	} {
		if !strings.Contains(string(stub), want) {
			t.Fatalf("stub grub.cfg missing %q:\n%s", want, stub)
		}
	}
}

// utf16ToString decodes a UCS-2 LE buffer (already unpacked into
// []uint16) into a Go string. Avoids pulling in unicode/utf16 here
// since the rhel_test.go already imports it for the amd64 case.
func utf16ToString(u []uint16) string {
	var b strings.Builder
	for _, r := range u {
		b.WriteRune(rune(r))
	}
	return b.String()
}
