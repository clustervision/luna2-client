package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// allCmds renders every recorded call, chroot calls by their argv and host
// calls by path + argv, so a UEFI comparison covers efibootmgr too.
func allCmds(f *executor.FakeRunner) []string {
	out := []string{}
	for _, c := range f.Calls() {
		if c.Path == "/usr/bin/env" {
			out = append(out, strings.Join(c.Args, " "))
			continue
		}
		out = append(out, c.Path+" "+strings.Join(c.Args, " "))
	}
	return out
}

// biosBootContext is the context a BIOS node whose os set asks for a
// bootloader arrives with: boot_method grub, boot_order first, firmware bios.
func biosBootContext(sysroot string, disks ...string) BootContext {
	return BootContext{
		Sysroot: sysroot, FirmwareMode: "bios", Architecture: "x86_64",
		BootMethod: "grub", BootOrder: "first", KernelArgs: "rd.auto",
		StorageResult: result.StorageResult{
			BootDevices: disks, BIOSBootPartition: 1,
		},
	}
}

func TestRHELInstallBIOSWritesGRUBPerDisk(t *testing.T) {
	sysroot := stageRHELSysroot(t, false)
	if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &rhelAdapter{deps: AdapterDeps{
		Runner:      f,
		BootContext: biosBootContext(sysroot, "/dev/sda", "/dev/sdb"),
	}}
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := allCmds(f)
	want := []string{
		"grub2-install --target=i386-pc --boot-directory=/boot /dev/sda",
		"grub2-install --target=i386-pc --boot-directory=/boot /dev/sdb",
		"grub2-mkconfig -o /boot/grub2/grub.cfg",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n got=%v\nwant=%v", got, want)
	}
	body, err := os.ReadFile(filepath.Join(sysroot, "etc", "default", "grub")) //nolint:gosec // test artefact
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), `GRUB_CMDLINE_LINUX="rd.auto"`) {
		t.Fatalf("/etc/default/grub not written for BIOS:\n%s", body)
	}
}

func TestDebianInstallBIOSWritesGRUBPerDisk(t *testing.T) {
	sysroot := stageDebianSysroot(t, false)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{
		Runner:      f,
		BootContext: biosBootContext(sysroot, "/dev/sda", "/dev/sdb"),
	}}
	if err := a.Install(context.Background()); err != nil {
		t.Fatalf("Install: %v", err)
	}
	got := allCmds(f)
	want := []string{
		"grub-install --target=i386-pc --boot-directory=/boot /dev/sda",
		"grub-install --target=i386-pc --boot-directory=/boot /dev/sdb",
		"update-grub",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands:\n got=%v\nwant=%v", got, want)
	}
}

// Nothing UEFI-only may run on a BIOS install: there is no NVRAM to register
// an entry in and no shim for firmware to verify.
func TestInstallBIOSStagesNoShimAndTouchesNoNVRAM(t *testing.T) {
	for _, tc := range []struct {
		name  string
		build func(string, *executor.FakeRunner) Adapter
	}{
		{"rhel", func(sysroot string, f *executor.FakeRunner) Adapter {
			return &rhelAdapter{deps: AdapterDeps{Runner: f, BootContext: biosBootContext(sysroot, "/dev/sda")}}
		}},
		{"debian", func(sysroot string, f *executor.FakeRunner) Adapter {
			return &debianAdapter{deps: AdapterDeps{Runner: f, BootContext: biosBootContext(sysroot, "/dev/sda")}}
		}},
	} {
		sysroot := stageRHELSysroot(t, false)
		if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
			t.Fatal(err)
		}
		f := executor.NewFakeRunner(executor.FakeResponse{})
		if err := tc.build(sysroot, f).Install(context.Background()); err != nil {
			t.Fatalf("%s: Install: %v", tc.name, err)
		}
		for _, c := range allCmds(f) {
			if strings.Contains(c, "efibootmgr") {
				t.Errorf("%s: BIOS install must not call efibootmgr: %s", tc.name, c)
			}
		}
		if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT")); err == nil {
			t.Errorf("%s: BIOS install must not stage the removable EFI fallback", tc.name)
		}
	}
}

// A BIOS node whose os set does not ask for a bootloader keeps today's
// refusal: "last" is a UEFI boot-order idea and there is nothing to arrange.
func TestInstallBIOSStillRefusesWithoutFirst(t *testing.T) {
	for _, order := range []string{"", "last"} {
		sysroot := stageRHELSysroot(t, false)
		if err := os.MkdirAll(filepath.Join(sysroot, "etc", "default"), 0o750); err != nil {
			t.Fatal(err)
		}
		f := executor.NewFakeRunner(executor.FakeResponse{})
		bctx := biosBootContext(sysroot, "/dev/sda")
		bctx.BootOrder = order
		a := &rhelAdapter{deps: AdapterDeps{Runner: f, BootContext: bctx}}
		err := a.Install(context.Background())
		if err == nil || !strings.Contains(err.Error(), "requires UEFI") {
			t.Fatalf("boot_order=%q: want the UEFI refusal; got %v", order, err)
		}
		for _, c := range allCmds(f) {
			if strings.Contains(c, "grub2-install") {
				t.Fatalf("boot_order=%q: refusal must not run grub2-install: %s", order, c)
			}
		}
	}
}

// Differential pin: on UEFI the issued commands are what they are today at
// every value of the bootloader key, so the BIOS branch changed nothing there.
func TestUEFIInstallCommandsUnchangedAtEveryBootloaderValue(t *testing.T) {
	for _, order := range []string{"", "last", "first"} {
		f := stageUEFIRunner()
		sysroot := stageUEFIInstallSysroot(t)
		a := &rhelAdapter{deps: AdapterDeps{
			Runner:      f,
			SnapshotDir: t.TempDir(),
			BootContext: BootContext{
				Sysroot: sysroot, FirmwareMode: "uefi", Architecture: "x86_64",
				BootMethod: "grub", BootOrder: order, RegisterNVRAM: "auto",
				KernelArgs: "rd.auto",
				StorageResult: result.StorageResult{
					BootDevices: []string{"/dev/sda"}, ESPPartition: 1,
					Filesystems: []result.Filesystem{
						{Mount: "/boot", UUID: "53261dca-33d4-4325-8200-4ff99b4a9b5a"},
					},
				},
			},
		}}
		if err := a.Install(context.Background()); err != nil {
			t.Fatalf("boot_order=%q: Install: %v", order, err)
		}
		got := allCmds(f)
		if strings.Join(got, "\n") != strings.Join(uefiCmdsToday, "\n") {
			t.Fatalf("boot_order=%q: UEFI commands changed:\n got=%#v\nwant=%#v",
				order, got, uefiCmdsToday)
		}
		// The signed shim is still what firmware loads, on every value.
		if _, err := os.Stat(filepath.Join(sysroot, "boot", "efi", "EFI", "BOOT", "BOOTX64.EFI")); err != nil {
			t.Fatalf("boot_order=%q: removable fallback not staged: %v", order, err)
		}
	}
}

// stageUEFIInstallSysroot stages a sysroot the UEFI install path runs through
// end to end: the signed pair under EFI/rocky and the efivars directory.
func stageUEFIInstallSysroot(t *testing.T) string {
	t.Helper()
	sysroot := stageRHELSysroot(t, true)
	for _, rel := range []string{
		"sys/firmware/efi/efivars",
		"etc/default",
		"boot/efi/EFI/rocky",
	} {
		if err := os.MkdirAll(filepath.Join(sysroot, rel), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, name := range []string{"shimx64.efi", "grubx64.efi"} {
		if err := os.WriteFile(filepath.Join(sysroot, "boot", "efi", "EFI", "rocky", name), []byte(name), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return sysroot
}

func stageUEFIRunner() *executor.FakeRunner {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{
		Path: "/usr/sbin/efibootmgr",
		Response: executor.FakeResponse{Stdout: []byte(
			"BootOrder: 0010,0020\nBoot0010* UEFI: PXE\nBoot0020* UEFI: Network\n")},
	})
	return f
}

// The commands a UEFI install issues today, captured from the unchanged
// adapter. One list for every value of the key: the NVRAM re-order that
// "first" asks for is driven by what efibootmgr reports back, which this
// fake holds constant, so the issued argv is the same at all three.
var uefiCmdsToday = []string{
	"/usr/sbin/efibootmgr ",
	`/usr/sbin/efibootmgr --create --disk /dev/sda --part 1 --label TrinityX sda --loader \EFI\rocky\shimx64.efi`,
	"/usr/sbin/efibootmgr ",
	"/usr/sbin/efibootmgr ",
	"/usr/sbin/efibootmgr ",
	"grub2-mkconfig -o /boot/grub2/grub.cfg",
}
