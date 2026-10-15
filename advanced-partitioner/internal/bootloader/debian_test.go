package bootloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

func stageDebianSysroot(t *testing.T, withEFI bool) string {
	t.Helper()
	sysroot := t.TempDir()
	for _, rel := range []string{
		"usr/sbin/update-initramfs",
		"usr/sbin/update-grub",
		"usr/sbin/grub-install",
		"usr/sbin/efibootmgr",
	} {
		path := filepath.Join(sysroot, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if withEFI {
		if err := os.MkdirAll(filepath.Join(sysroot, "boot", "efi"), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	return sysroot
}

func TestDebianPrepareHappy(t *testing.T) {
	sysroot := stageDebianSysroot(t, true)
	a := &debianAdapter{deps: AdapterDeps{BootContext: BootContext{
		Sysroot: sysroot, FirmwareMode: "uefi",
		StorageResult: result.StorageResult{BootDevices: []string{"/dev/sda"}},
	}}}
	if err := a.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare: %v", err)
	}
}

func TestDebianRegenerateInitramfs(t *testing.T) {
	sysroot := stageDebianSysroot(t, false)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{
		Runner:      f,
		BootContext: BootContext{Sysroot: sysroot},
	}}
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatalf("RegenerateInitramfs: %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 || calls[0].Path != "/usr/bin/env" {
		t.Fatalf("call shape wrong: %+v", calls)
	}
	args := strings.Join(calls[0].Args, " ")
	if args != "update-initramfs -u -k all" {
		t.Fatalf("args = %q", args)
	}
}

// Dracut-only Ubuntu image (e.g. 24.04/noble, where the dracut package
// displaced initramfs-tools): RegenerateInitramfs falls back to `dracut -f`
// with the luna module omitted, exactly like the RHEL/SUSE path.
func TestDebianRegenerateInitramfsDracutFallback(t *testing.T) {
	sysroot := stageDebianSysroot(t, false)
	if err := os.Remove(filepath.Join(sysroot, "usr/sbin/update-initramfs")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(sysroot, "usr/bin/dracut"), "")
	if err := os.MkdirAll(filepath.Join(sysroot, "lib/modules/6.8.0-31-generic"), 0o750); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{
		Runner:      f,
		BootContext: BootContext{Sysroot: sysroot},
	}}
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatalf("RegenerateInitramfs (dracut fallback): %v", err)
	}
	calls := f.Calls()
	if len(calls) != 1 {
		t.Fatalf("call shape wrong: %+v", calls)
	}
	args := strings.Join(calls[0].Args, " ")
	if args != strings.Join(dracutArgs("6.8.0-31-generic"), " ") {
		t.Fatalf("args = %q", args)
	}
}

// Prepare accepts a dracut-only image (no update-initramfs) as long as dracut
// and update-grub are present.
func TestDebianPrepareDracutOnly(t *testing.T) {
	sysroot := stageDebianSysroot(t, true)
	if err := os.Remove(filepath.Join(sysroot, "usr/sbin/update-initramfs")); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(sysroot, "usr/bin/dracut"), "")
	a := &debianAdapter{deps: AdapterDeps{BootContext: BootContext{
		Sysroot: sysroot, FirmwareMode: "uefi",
		StorageResult: result.StorageResult{BootDevices: []string{"/dev/sda"}},
	}}}
	if err := a.Prepare(context.Background()); err != nil {
		t.Fatalf("Prepare (dracut-only): %v", err)
	}
}

func TestDebianInstallUEFI(t *testing.T) {
	sysroot := stageDebianSysroot(t, true)
	if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
		t.Fatal(err)
	}
	// Stage the signed shim + grub binaries under /boot/efi/EFI/debian
	// as if the OS image had been unpacked.
	debDir := filepath.Join(sysroot, "boot", "efi", "EFI", "debian")
	if err := os.MkdirAll(debDir, 0o750); err != nil {
		t.Fatal(err)
	}
	shimBody := []byte("SIGNED-SHIM-DEB")
	grubBody := []byte("SIGNED-GRUB-DEB")
	if err := os.WriteFile(filepath.Join(debDir, "shimx64.efi"), shimBody, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(debDir, "grubx64.efi"), grubBody, 0o600); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{
		Runner: f,
		BootContext: BootContext{
			Sysroot: sysroot, FirmwareMode: "uefi", Architecture: "x86_64",
			KernelArgs: "rd.auto", RegisterNVRAM: "no",
			StorageResult: result.StorageResult{
				BootDevices: []string{"/dev/sda"},
				Filesystems: []result.Filesystem{
					{Mount: "/boot", UUID: "11111111-1111-1111-1111-111111111111"},
				},
			},
		},
	}}
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	body, _ := os.ReadFile(filepath.Join(sysroot, "etc", "default", "grub")) //nolint:gosec // test artefact
	for _, want := range []string{
		"GRUB_TIMEOUT=5",
		"GRUB_DEFAULT=0",
		"GRUB_DISABLE_OS_PROBER=true",
	} {
		if !strings.Contains(string(body), want) {
			t.Fatalf("/etc/default/grub missing %q:\n%s", want, body)
		}
	}
	// The submenu is off on every family.
	if !strings.Contains(string(body), "GRUB_DISABLE_SUBMENU=true") {
		t.Fatalf("Debian grub default must disable the submenu:\n%s", body)
	}

	// Stub grub.cfg landed under /EFI/debian/ pointing at /boot/grub/
	// (Debian's grub.cfg dir) — NOT /boot/grub2/ (RHEL's).
	stub, err := os.ReadFile(filepath.Join(debDir, "grub.cfg")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatalf("stub grub.cfg: %v", err)
	}
	for _, want := range []string{
		"search --no-floppy --fs-uuid --set=dev 11111111-1111-1111-1111-111111111111",
		"set prefix=($dev)/grub\n",
		"configfile $prefix/grub.cfg",
	} {
		if !strings.Contains(string(stub), want) {
			t.Fatalf("Debian stub grub.cfg missing %q:\n%s", want, stub)
		}
	}

	// Removable fallback populated with the signed shim + grub.
	bootDir := filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT")
	if got, _ := os.ReadFile(filepath.Join(bootDir, "BOOTX64.EFI")); string(got) != string(shimBody) { //nolint:gosec // test artefact
		t.Fatalf("BOOTX64.EFI != signed shim: got=%q want=%q", got, shimBody)
	}
	if got, _ := os.ReadFile(filepath.Join(bootDir, "grubx64.efi")); string(got) != string(grubBody) { //nolint:gosec // test artefact
		t.Fatalf("grubx64.efi in /EFI/BOOT != signed grub: got=%q want=%q", got, grubBody)
	}

	// grub-install --target=x86_64-efi must NOT be called any more.
	gotCmds := envCmds(f)
	for _, g := range gotCmds {
		if strings.Contains(g, "grub-install") && strings.Contains(g, "--target=") {
			t.Fatalf("unexpected grub-install UEFI invocation: %s", g)
		}
	}
	// update-grub still required.
	want := "update-grub"
	found := false
	for _, g := range gotCmds {
		if g == want {
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("expected %q in:\n  %s", want, strings.Join(gotCmds, "\n  "))
	}
}

// TestDebianInstallUEFIDegradedSafeGrubCfg pins BE-W5: the Debian install
// must apply the same degraded-safe rewrite as RHEL — update-grub output on a
// mirrored boot chain locates /boot by md-array UUID, which GRUB resolves
// non-deterministically when a mirror leg is missing. The mduuid set/hints
// must be gone from grub.cfg after Install.
func TestDebianInstallUEFIDegradedSafeGrubCfg(t *testing.T) {
	sysroot := stageDebianSysroot(t, true)
	if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
		t.Fatal(err)
	}
	debDir := filepath.Join(sysroot, "boot", "efi", "EFI", "debian")
	if err := os.MkdirAll(debDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"shimx64.efi", "grubx64.efi"} {
		if err := os.WriteFile(filepath.Join(debDir, name), []byte("EFI"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	// Pre-seed the grub.cfg update-grub would have produced on a mirrored
	// /boot (the FakeRunner's update-grub writes nothing, so this survives
	// to the rewrite step).
	grubDir := filepath.Join(sysroot, "boot", "grub")
	if err := os.MkdirAll(grubDir, 0o750); err != nil {
		t.Fatal(err)
	}
	cfg := "set root='mduuid/3dbc7f6c203c674664990e4cdd81136b'\n" +
		"search --no-floppy --fs-uuid --set=root --hint='mduuid/3dbc7f6c203c674664990e4cdd81136b' 4e345124-e054-47a8-8919-f040540db660\n" +
		"linux /vmlinuz-x root=/dev/mapper/os-root\n"
	if err := os.WriteFile(filepath.Join(grubDir, "grub.cfg"), []byte(cfg), 0o600); err != nil {
		t.Fatal(err)
	}

	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{
		Runner: f,
		BootContext: BootContext{
			Sysroot: sysroot, FirmwareMode: "uefi", Architecture: "x86_64",
			KernelArgs: "rd.auto", RegisterNVRAM: "no",
			StorageResult: result.StorageResult{
				BootDevices: []string{"/dev/sda", "/dev/sdb"},
				Filesystems: []result.Filesystem{
					{Mount: "/boot", UUID: "11111111-1111-1111-1111-111111111111"},
				},
			},
		},
	}}
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := readFile(t, filepath.Join(grubDir, "grub.cfg"))
	if strings.Contains(got, "mduuid/") {
		t.Fatalf("mduuid references survived the Debian install:\n%s", got)
	}
	// The fs-uuid search and the kernel line stay.
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

// BIOS/legacy boot is unsupported: Install must refuse a non-UEFI firmware
// mode outright and must NOT run grub-install (see the storage-prepare gate).
func TestDebianInstallRefusesBIOS(t *testing.T) {
	sysroot := stageDebianSysroot(t, false)
	if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{
		Runner: f,
		BootContext: BootContext{
			Sysroot: sysroot, FirmwareMode: "bios", Architecture: "x86_64",
			KernelArgs:    "rd.auto",
			StorageResult: result.StorageResult{BootDevices: []string{"/dev/sda"}},
		},
	}}
	err := a.Install(context.Background())
	if err == nil {
		t.Fatal("Install must refuse BIOS firmware; got nil error")
	}
	if !strings.Contains(err.Error(), "requires UEFI") {
		t.Fatalf("error should name the UEFI requirement; got: %v", err)
	}
	for _, g := range envCmds(f) {
		if strings.Contains(g, "grub-install") {
			t.Fatalf("BIOS refusal must not run grub-install; saw: %s", g)
		}
	}
}

func TestSUSEStubRefuses(t *testing.T) {
	a := &suseAdapter{}
	if err := a.RegenerateInitramfs(context.Background()); err == nil {
		t.Fatal("expected refusal")
	}
	if err := a.Install(context.Background()); err == nil {
		t.Fatal("expected refusal")
	}
	if err := a.Validate(context.Background()); err == nil {
		t.Fatal("expected refusal")
	}
}

func TestAdapterDispatch(t *testing.T) {
	for fam := range map[Family]Family{FamilyRHEL: FamilyRHEL, FamilyDebian: FamilyDebian, FamilySUSE: FamilySUSE, FamilyNone: FamilyNone} {
		a, err := New(AdapterDeps{BootContext: BootContext{Family: fam}})
		if err != nil {
			t.Fatalf("family %s: New: %v", fam, err)
		}
		if a.Family() != fam {
			t.Fatalf("family %s: adapter says %s", fam, a.Family())
		}
	}
	if _, err := New(AdapterDeps{BootContext: BootContext{Family: "openbsd"}}); err == nil {
		t.Fatal("expected unsupported-family error")
	}
}

func TestNoopAdapterAllNil(t *testing.T) {
	a := &noopAdapter{}
	for _, fn := range []func(context.Context) error{
		a.Prepare, a.RegenerateInitramfs, a.Install, a.Validate,
	} {
		if err := fn(context.Background()); err != nil {
			t.Fatalf("noop returned err: %v", err)
		}
	}
}

func mustWriteFile(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

// One unified pre-stage body for every family (ensureSignedPairStaged);
// the per-family difference is the source table (signedPairSources).
// Debian row: empty ESP, signed pair only under /usr/lib.
func TestEnsureSignedPairStagedDebianFromUsrLib(t *testing.T) {
	sysroot := stageDebianSysroot(t, true)
	mustWriteFile(t, filepath.Join(sysroot, "etc/os-release"), "ID=ubuntu\nPRETTY_NAME=\"Ubuntu 26.04\"\n")
	mustWriteFile(t, filepath.Join(sysroot, "usr/lib/shim/shimx64.efi.signed"), "SHIM")
	mustWriteFile(t, filepath.Join(sysroot, "usr/lib/grub/x86_64-efi-signed/grubx64.efi.signed"), "GRUB")

	err := ensureSignedPairStaged(StageSignedShimOpts{
		Sysroot: sysroot, Arch: "x86_64", Family: FamilyDebian,
	})
	if err != nil {
		t.Fatalf("ensureSignedPairStaged(debian): %v", err)
	}
	for name, want := range map[string]string{
		filepath.Join(sysroot, "boot/efi/EFI/ubuntu/shimx64.efi"): "SHIM",
		filepath.Join(sysroot, "boot/efi/EFI/ubuntu/grubx64.efi"): "GRUB",
	} {
		b, err := os.ReadFile(name) //nolint:gosec // test artefact
		if err != nil {
			t.Fatalf("staged file %s missing: %v", name, err)
		}
		if string(b) != want {
			t.Fatalf("%s = %q, want %q", name, b, want)
		}
	}
	if _, err := findSignedShim(sysroot, "x86_64"); err != nil {
		t.Fatalf("findSignedShim after staging: %v", err)
	}
}

// SUSE row: pair under /usr/lib64/efi, generic shim.efi/grub.efi names
// staged as the arch-canonical shimx64.efi/grubx64.efi.
func TestEnsureSignedPairStagedSUSEFromUsrLib64(t *testing.T) {
	sysroot := stageDebianSysroot(t, true)
	mustWriteFile(t, filepath.Join(sysroot, "etc/os-release"), "ID=\"opensuse-leap\"\n")
	mustWriteFile(t, filepath.Join(sysroot, "usr/lib64/efi/shim.efi"), "SHIM")
	mustWriteFile(t, filepath.Join(sysroot, "usr/lib64/efi/grub.efi"), "GRUB")

	err := ensureSignedPairStaged(StageSignedShimOpts{
		Sysroot: sysroot, Arch: "x86_64", Family: FamilySUSE,
	})
	if err != nil {
		t.Fatalf("ensureSignedPairStaged(suse): %v", err)
	}
	for name, want := range map[string]string{
		filepath.Join(sysroot, "boot/efi/EFI/opensuse-leap/shimx64.efi"): "SHIM",
		filepath.Join(sysroot, "boot/efi/EFI/opensuse-leap/grubx64.efi"): "GRUB",
	} {
		b, err := os.ReadFile(name) //nolint:gosec // test artefact
		if err != nil {
			t.Fatalf("staged file %s missing: %v", name, err)
		}
		if string(b) != want {
			t.Fatalf("%s = %q, want %q", name, b, want)
		}
	}
}

// Pre-populated ESP (RHEL-style image, or an earlier run) -> no-op for
// every family.
func TestEnsureSignedPairStagedSkipsWhenAlreadyStaged(t *testing.T) {
	for _, fam := range []Family{FamilyRHEL, FamilyDebian, FamilySUSE} {
		sysroot := stageDebianSysroot(t, true)
		mustWriteFile(t, filepath.Join(sysroot, "boot/efi/EFI/rocky/shimx64.efi"), "x")
		mustWriteFile(t, filepath.Join(sysroot, "boot/efi/EFI/rocky/grubx64.efi"), "x")
		err := ensureSignedPairStaged(StageSignedShimOpts{
			Sysroot: sysroot, Arch: "x86_64", Family: fam,
		})
		if err != nil {
			t.Fatalf("family %s: should be a no-op, got: %v", fam, err)
		}
	}
}

// Missing sources -> a clear error naming the needed packages (families
// with a source row); RHEL has no row so the miss is reported later by
// findSignedShim instead.
func TestEnsureSignedPairStagedMissingSources(t *testing.T) {
	for _, fam := range []Family{FamilyDebian, FamilySUSE} {
		sysroot := stageDebianSysroot(t, true)
		err := ensureSignedPairStaged(StageSignedShimOpts{
			Sysroot: sysroot, Arch: "x86_64", Family: fam,
		})
		if err == nil {
			t.Fatalf("family %s: expected error when signed shim/grub are absent", fam)
		}
	}
	sysroot := stageDebianSysroot(t, true)
	if err := ensureSignedPairStaged(StageSignedShimOpts{
		Sysroot: sysroot, Arch: "x86_64", Family: FamilyRHEL,
	}); err != nil {
		t.Fatalf("rhel: pre-stage must not error (empty source row): %v", err)
	}
}

// Ubuntu 26.04: dracut is the generator; initramfs-tools-core (hook-functions)
// stays installed, but update-initramfs is the dracut package's wrapper, which
// builds without --omit luna, so the installed system's initramfs carried the
// Luna install module. lpart now calls dracut itself, with --omit luna.
func TestDebianRegenerateInitramfsDracutWrapper(t *testing.T) {
	sysroot := stageDebianSysroot(t, false)
	mustWriteFile(t, filepath.Join(sysroot, "usr/sbin/update-initramfs"),
		"#!/bin/sh\n# Taken from initramfs-tools and adapted for dracut by Benjamin Drung.\ndracut ${OPTS} --force \"${initramfs}\" \"${version}\"\n")
	mustWriteFile(t, filepath.Join(sysroot, "usr/bin/dracut"), "")
	mustWriteFile(t, filepath.Join(sysroot, "usr/share/initramfs-tools/hook-functions"), "")
	if err := os.MkdirAll(filepath.Join(sysroot, "lib/modules/7.0.0-14-generic"), 0o750); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{Runner: f, BootContext: BootContext{Sysroot: sysroot}}}
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 || strings.Join(calls[0].Args, " ") != strings.Join(dracutArgs("7.0.0-14-generic"), " ") {
		t.Fatalf("calls = %+v", calls)
	}
}

// initramfs-tools' own update-initramfs keeps being used, also when dracut
// (or only dracut-install, as on the 24.04 lab image) is present too.
func TestDebianRegenerateInitramfsPrefersInitramfsTools(t *testing.T) {
	sysroot := stageDebianSysroot(t, false)
	mustWriteFile(t, filepath.Join(sysroot, "usr/sbin/update-initramfs"),
		"#!/bin/sh\n# Copyright (C) 2005-2011 maximilian attems\nmkinitramfs -o \"${initramfs}\" \"${version}\"\n")
	mustWriteFile(t, filepath.Join(sysroot, "usr/bin/dracut"), "")
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{Runner: f, BootContext: BootContext{Sysroot: sysroot}}}
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatal(err)
	}
	calls := f.Calls()
	if len(calls) != 1 || strings.Join(calls[0].Args, " ") != "update-initramfs -u -k all" {
		t.Fatalf("calls = %+v", calls)
	}
}

// A grub.cfg repair that cannot be written fails finalise, as on RHEL: the
// unrepaired file leaves the node unbootable or at a GRUB prompt.
func TestDebianGenerateGrubConfigFailsOnUnwritableRepair(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root writes a read-only file")
	}
	for _, c := range []struct {
		name, mount, cfg string
	}{
		{"separate /boot", "/boot", "linux /boot/vmlinuz-x root=/dev/sda2\n"},
		{"degraded-safe", "/", "set root='mduuid/3dbc7f6c203c674664990e4cdd81136b'\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			sysroot := stageDebianSysroot(t, false)
			p := filepath.Join(sysroot, "boot", "grub", "grub.cfg")
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(c.cfg), 0o400); err != nil {
				t.Fatal(err)
			}
			a := &debianAdapter{deps: AdapterDeps{
				Runner: executor.NewFakeRunner(executor.FakeResponse{}),
				BootContext: BootContext{
					Sysroot:       sysroot,
					StorageResult: result.StorageResult{Filesystems: []result.Filesystem{{Mount: c.mount}}},
				},
			}}
			if err := a.generateGrubConfig(context.Background()); err == nil || !errors.Is(err, os.ErrPermission) {
				t.Fatalf("an unwritable grub.cfg (%s) must fail generateGrubConfig, got %v", c.name, err)
			}
		})
	}
}
