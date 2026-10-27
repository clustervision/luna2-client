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

// espTree writes files under <sysroot>/boot/efi/EFI: map of "dir/name" -> body.
func espTree(t *testing.T, sysroot string, files map[string]string) {
	t.Helper()
	for rel, body := range files {
		p := filepath.Join(sysroot, "boot", "efi", "EFI", rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
}

func osRelease(t *testing.T, sysroot, body string) {
	t.Helper()
	p := filepath.Join(sysroot, "etc", "os-release")
	if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func espFile(t *testing.T, sysroot, rel string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(sysroot, "boot", "efi", "EFI", rel)) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("EFI/%s: %v", rel, err)
	}
	return string(b)
}

const testBootUUID = "abcdef00-0000-0000-0000-000000000001"

func stageOpts(sysroot string) StageSignedShimOpts {
	return StageSignedShimOpts{
		Sysroot: sysroot, Arch: "x86_64", GrubDirName: "grub2", Family: FamilyRHEL,
		StorageResult: result.StorageResult{Filesystems: []result.Filesystem{{Mount: "/boot", UUID: testBootUUID}}},
	}
}

// Review finding 1: a RHEL 8 image whose shim and grub sit in different
// vendor directories used to fail with "no signed shim+grub found". The shim's
// directory wins, grub (and MokManager) are copied beside it, and both
// directories get the stub because the grub's baked prefix names its own.
func TestStageSignedShimSplitPair(t *testing.T) {
	for _, tc := range []struct {
		name, osrel, shimDir, grubDir string
	}{
		{"shim in redhat, grub in rocky", "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n", "redhat", "rocky"},
		{"shim in rocky, grub in redhat", "ID=\"rhel\"\nID_LIKE=\"fedora\"\n", "rocky", "redhat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sysroot := t.TempDir()
			osRelease(t, sysroot, tc.osrel)
			espTree(t, sysroot, map[string]string{
				tc.shimDir + "/shimx64.efi": "SHIM",
				tc.grubDir + "/grubx64.efi": "GRUB",
				tc.grubDir + "/mmx64.efi":   "MOK",
			})
			if err := StageSignedShim(stageOpts(sysroot)); err != nil {
				t.Fatalf("StageSignedShim: %v", err)
			}
			for rel, want := range map[string]string{
				tc.shimDir + "/grubx64.efi": "GRUB",
				tc.shimDir + "/mmx64.efi":   "MOK",
				"BOOT/BOOTX64.EFI":          "SHIM",
				"BOOT/grubx64.efi":          "GRUB",
			} {
				if got := espFile(t, sysroot, rel); got != want {
					t.Errorf("EFI/%s = %q, want %q", rel, got, want)
				}
			}
			for _, d := range []string{tc.shimDir, tc.grubDir} {
				if !strings.Contains(espFile(t, sysroot, d+"/grub.cfg"), testBootUUID) {
					t.Errorf("EFI/%s/grub.cfg stub missing", d)
				}
			}
			loader, ok := signedShimLoaderPath(sysroot, "x86_64")
			if want := `\EFI\` + tc.shimDir + `\shimx64.efi`; !ok || loader != want {
				t.Errorf("NVRAM loader = %q (%v), want %q", loader, ok, want)
			}
		})
	}
}

// A split pair in dry-run is reported but nothing is copied.
func TestStageSignedShimSplitPairDryRun(t *testing.T) {
	sysroot := t.TempDir()
	osRelease(t, sysroot, "ID=rocky\nID_LIKE=\"rhel centos fedora\"\n")
	espTree(t, sysroot, map[string]string{"redhat/shimx64.efi": "SHIM", "rocky/grubx64.efi": "GRUB"})
	opts := stageOpts(sysroot)
	opts.FS = fsops.FS{DryRun: true}
	if err := StageSignedShim(opts); err != nil {
		t.Fatalf("StageSignedShim: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi", "EFI", "redhat", "grubx64.efi")); err == nil {
		t.Fatal("dry-run copied grub")
	}
}

// Review finding 2: the image's own vendor directory beats a stale one from an
// earlier image, whatever the alphabet says; with no usable os-release the old
// alphabetical choice stands.
func TestFindSignedShimPrefersImageDistro(t *testing.T) {
	stale := map[string]string{
		"redhat/shimx64.efi": "OLD-SHIM", "redhat/grubx64.efi": "OLD-GRUB",
		"rocky/shimx64.efi": "NEW-SHIM", "rocky/grubx64.efi": "NEW-GRUB",
	}
	for _, tc := range []struct{ name, osrel, wantDir string }{
		{"rocky image, stale redhat dir", "ID=\"rocky\"\nID_LIKE=\"rhel centos fedora\"\n", "rocky"},
		{"rhel image, stale rocky dir", "ID=\"rhel\"\nID_LIKE=\"fedora\"\n", "redhat"},
		{"oracle linux uses EFI/redhat", "ID=\"ol\"\nID_LIKE=\"fedora\"\n", "redhat"},
		{"no os-release: alphabetical as before", "", "redhat"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sysroot := t.TempDir()
			if tc.osrel != "" {
				osRelease(t, sysroot, tc.osrel)
			}
			espTree(t, sysroot, stale)
			sel, err := findSignedShim(sysroot, "x86_64")
			if err != nil {
				t.Fatal(err)
			}
			if filepath.Base(sel.dir) != tc.wantDir || sel.split() {
				t.Fatalf("picked %s (split=%v), want %s", sel.dir, sel.split(), tc.wantDir)
			}
			if err := StageSignedShim(stageOpts(sysroot)); err != nil {
				t.Fatal(err)
			}
			want := "NEW-SHIM"
			if tc.wantDir == "redhat" {
				want = "OLD-SHIM"
			}
			if got := espFile(t, sysroot, "BOOT/BOOTX64.EFI"); got != want {
				t.Errorf("removable fallback = %q, want %q", got, want)
			}
		})
	}
}

// Review finding 2, staging side: an Ubuntu image on an ESP that still holds
// an earlier Rocky install's pair must stage its own pair, not call the Rocky
// one "already staged" -- and the staged pair is the one used.
func TestEnsureSignedPairStagedIgnoresOtherDistroPair(t *testing.T) {
	sysroot := t.TempDir()
	osRelease(t, sysroot, "ID=ubuntu\nID_LIKE=debian\n")
	espTree(t, sysroot, map[string]string{"rocky/shimx64.efi": "ROCKY-SHIM", "rocky/grubx64.efi": "ROCKY-GRUB"})
	mustWriteFile(t, filepath.Join(sysroot, "usr/lib/shim/shimx64.efi.signed"), "UBUNTU-SHIM")
	mustWriteFile(t, filepath.Join(sysroot, "usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed"), "UBUNTU-GRUB")
	opts := stageOpts(sysroot)
	opts.Family, opts.GrubDirName = FamilyDebian, "grub"
	if err := StageSignedShim(opts); err != nil {
		t.Fatalf("StageSignedShim: %v", err)
	}
	if got := espFile(t, sysroot, "ubuntu/shimx64.efi"); got != "UBUNTU-SHIM" {
		t.Fatalf("image pair not staged: %q", got)
	}
	if got := espFile(t, sysroot, "BOOT/BOOTX64.EFI"); got != "UBUNTU-SHIM" {
		t.Fatalf("removable fallback = %q, want the image's own shim", got)
	}
	if loader, _ := signedShimLoaderPath(sysroot, "x86_64"); loader != `\EFI\ubuntu\shimx64.efi` {
		t.Fatalf("NVRAM loader = %q", loader)
	}
}

// ...but an image with no package copy to stage from keeps working with the
// pair the ESP has, as it did before.
func TestEnsureSignedPairStagedFallsBackToESPPair(t *testing.T) {
	sysroot := t.TempDir()
	osRelease(t, sysroot, "ID=ubuntu\nID_LIKE=debian\n")
	espTree(t, sysroot, map[string]string{"rocky/shimx64.efi": "S", "rocky/grubx64.efi": "G"})
	if err := ensureSignedPairStaged(StageSignedShimOpts{Sysroot: sysroot, Arch: "x86_64", Family: FamilyDebian}); err != nil {
		t.Fatalf("must fall back to the ESP pair, got %v", err)
	}
}

// No shim anywhere is an image problem: typed, so finalise parks with exit 3.
func TestFindSignedShimMissingIsImageError(t *testing.T) {
	sysroot := t.TempDir()
	espTree(t, sysroot, map[string]string{"rocky/grubx64.efi": "G"})
	err := StageSignedShim(stageOpts(sysroot))
	fe, ok := AsFatal(err)
	if !ok || fe.Kind != "image" || fe.Reason != "image-no-signed-shim" {
		t.Fatalf("want an image FatalError, got %v", err)
	}
}
