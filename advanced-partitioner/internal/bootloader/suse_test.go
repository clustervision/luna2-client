package bootloader

// Tests for the SUSE family adapter (LOOSE-ENDS #2 / gaps G2 de-stub).
// The adapter composes the RHEL flow — these tests pin the SUSE-visible
// contract: family dispatch, preflight messages, the chrooted command set,
// and the grub2 artefact paths.

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// stageSUSERoot builds a minimal SUSE-shaped sysroot: dracut,
// grub2-mkconfig, grub2-install, a kernel modules dir, and /boot/efi.
func stageSUSERoot(t *testing.T) string {
	t.Helper()
	sysroot := t.TempDir()
	for _, d := range []string{
		"usr/bin", "usr/sbin", "boot/efi", "etc", "lib/modules/6.4.0-150600.1-default",
	} {
		if err := os.MkdirAll(filepath.Join(sysroot, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"usr/bin/dracut", "usr/sbin/grub2-mkconfig", "usr/sbin/grub2-install"} {
		if err := os.WriteFile(filepath.Join(sysroot, f), []byte("#!/bin/sh\n"), 0o750); err != nil { //nolint:gosec // test stub
			t.Fatal(err)
		}
	}
	return sysroot
}

//nolint:unparam // firmware stays a parameter for future UEFI-path tests (needs a staged ESP shim tree)
func suseDeps(t *testing.T, sysroot, firmware string, f *executor.FakeRunner) AdapterDeps {
	t.Helper()
	return AdapterDeps{
		Runner: f,
		BootContext: BootContext{
			Family:       FamilySUSE,
			Sysroot:      sysroot,
			FirmwareMode: firmware,
			Architecture: "x86_64",
			StorageResult: result.StorageResult{
				BootDevices:       []string{"/dev/sda"},
				MountedTargetRoot: sysroot,
			},
		},
	}
}

func TestSUSEDispatch(t *testing.T) {
	a, err := New(suseDeps(t, t.TempDir(), "bios", executor.NewFakeRunner(executor.FakeResponse{})))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if a.Family() != FamilySUSE {
		t.Fatalf("family = %s", a.Family())
	}
}

func TestSUSEPrepareHappyAndMissingTools(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	sysroot := stageSUSERoot(t)
	a, _ := New(suseDeps(t, sysroot, "bios", f))
	if err := a.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare on a complete SUSE root should pass: %v", err)
	}
	// Remove grub2-mkconfig: preflight must fail with a named tool.
	if err := os.Remove(filepath.Join(sysroot, "usr/sbin/grub2-mkconfig")); err != nil {
		t.Fatal(err)
	}
	err := a.Prepare(context.Background())
	if err == nil || !strings.Contains(err.Error(), "grub2-mkconfig") {
		t.Fatalf("expected grub2-mkconfig preflight failure, got %v", err)
	}
}

// BIOS/legacy boot is unsupported: SUSE Install (which delegates to the RHEL
// flow) must refuse a non-UEFI firmware mode and must NOT run grub2-install.
func TestSUSEInstallRefusesBIOS(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	sysroot := stageSUSERoot(t)
	a, _ := New(suseDeps(t, sysroot, "bios", f))
	err := a.Install(context.Background())
	if err == nil {
		t.Fatal("Install must refuse BIOS firmware; got nil error")
	}
	if !strings.Contains(err.Error(), "requires UEFI") {
		t.Fatalf("error should name the UEFI requirement; got: %v", err)
	}
	for _, c := range f.Calls() {
		if strings.Contains(strings.Join(c.Args, " "), "grub2-install") {
			t.Fatalf("BIOS refusal must not run grub2-install; calls: %+v", f.Calls())
		}
	}
}

func TestSUSERegenerateInitramfsUsesDracut(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	sysroot := stageSUSERoot(t)
	a, _ := New(suseDeps(t, sysroot, "bios", f))
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatalf("RegenerateInitramfs: %v", err)
	}
	var saw bool
	for _, c := range f.Calls() {
		if c.Chroot == sysroot && strings.Contains(strings.Join(c.Args, " "), "dracut -f --omit luna --kver 6.4.0-150600.1-default") {
			saw = true
		}
	}
	if !saw {
		t.Fatalf("expected chrooted dracut with the SUSE kernel version; calls: %+v", f.Calls())
	}
}

func TestSUSEValidateRequiresGrub2Cfg(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	sysroot := stageSUSERoot(t)
	a, _ := New(suseDeps(t, sysroot, "bios", f))
	if err := a.Validate(context.Background()); err == nil {
		t.Fatal("Validate must fail without /boot/grub2/grub.cfg")
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "boot/grub2"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "boot/grub2/grub.cfg"), []byte("menuentry x {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "etc/fstab"), []byte("UUID=x / xfs defaults 0 0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := a.Validate(context.Background()); err != nil {
		t.Fatalf("Validate should pass with grub.cfg + fstab: %v", err)
	}
}

// TestSUSEInstallUEFIDegradedSafeGrubCfg pins BE-W5 for SUSE: Install
// delegates to the RHEL flow, which ends with the degraded-safe grub.cfg
// rewrite — the mduuid set/hints a grub2-mkconfig run emits on a mirrored
// boot chain must be gone from /boot/grub2/grub.cfg after Install.
func TestSUSEInstallUEFIDegradedSafeGrubCfg(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	sysroot := stageSUSERoot(t)
	// Signed shim + grub staged under EFI/opensuse as an unpacked image would.
	suseDir := filepath.Join(sysroot, "boot", "efi", "EFI", "opensuse")
	if err := os.MkdirAll(suseDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shimx64.efi", "grubx64.efi"} {
		if err := os.WriteFile(filepath.Join(suseDir, name), []byte("EFI"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Pre-seed the grub.cfg grub2-mkconfig would have produced on a mirrored
	// /boot (the FakeRunner's grub2-mkconfig writes nothing, so this survives
	// to the rewrite step).
	grubDir := filepath.Join(sysroot, "boot", "grub2")
	if err := os.MkdirAll(grubDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "set root='mduuid/3dbc7f6c203c674664990e4cdd81136b'\n" +
		"search --no-floppy --fs-uuid --set=root --hint='mduuid/3dbc7f6c203c674664990e4cdd81136b' 4e345124-e054-47a8-8919-f040540db660\n" +
		"linux /vmlinuz-x root=/dev/mapper/os-root\n"
	if err := os.WriteFile(filepath.Join(grubDir, "grub.cfg"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	deps := suseDeps(t, sysroot, "uefi", f)
	deps.BootContext.RegisterNVRAM = "no"
	deps.BootContext.KernelArgs = "rd.auto"
	deps.BootContext.StorageResult.Filesystems = []result.Filesystem{
		{Mount: "/boot", UUID: "11111111-1111-1111-1111-111111111111"},
	}
	a, err := New(deps)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := readFile(t, filepath.Join(grubDir, "grub.cfg"))
	if strings.Contains(got, "mduuid/") {
		t.Fatalf("mduuid references survived the SUSE install:\n%s", got)
	}
	for _, want := range []string{
		"search --no-floppy --fs-uuid --set=root",
		"4e345124-e054-47a8-8919-f040540db660",
		"linux /vmlinuz-x root=/dev/mapper/os-root",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("grub.cfg lost %q:\n%s", want, got)
		}
	}
}
