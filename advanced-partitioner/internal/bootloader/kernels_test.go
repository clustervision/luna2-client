package bootloader

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fsops"
)

const (
	kOld      = "4.18.0-553.el8_10.x86_64"
	kNew      = "4.18.0-553.5.1.el8_10.x86_64"
	kLeftover = "4.18.0-477.10.1.el8_8.x86_64"
)

// stageKernels gives sysroot a module tree for every kernel, a /boot/vmlinuz
// for the bootable ones, and a BLS entry per bootable kernel.
func stageKernels(t *testing.T, sysroot string, bootable []string, leftovers ...string) {
	t.Helper()
	for _, k := range append(append([]string{}, bootable...), leftovers...) {
		if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules", k), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	for _, k := range bootable {
		mustWriteFile(t, filepath.Join(sysroot, "boot", "vmlinuz-"+k), "x")
		writeBLSEntry(t, sysroot, "abc123-"+k+".conf",
			"title k\nversion "+k+"\nlinux /vmlinuz-"+k+"\ninitrd /initramfs-"+k+".img\noptions $kernelopts\n")
	}
}

func dracutKvers(f *executor.FakeRunner) []string {
	var out []string
	for _, c := range f.Calls() {
		if len(c.Args) >= 6 && c.Args[0] == "dracut" {
			out = append(out, c.Args[len(c.Args)-1])
		}
	}
	return out
}

func editenvCalls(f *executor.FakeRunner) []string {
	var out []string
	for _, c := range f.Calls() {
		if len(c.Args) > 0 && c.Args[0] == "grub2-editenv" {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

// Review finding 5: every bootable kernel gets a node-built initramfs (the
// default first), leftovers do not, and saved_entry names the default.
func TestRHELRebuildsEveryKernelAndSetsDefault(t *testing.T) {
	for _, tc := range []struct {
		name, running, wantDefault string
	}{
		{"osimage boots the newest", kNew, kNew},
		{"osimage pins the older kernel", kOld, kOld},
		{"running kernel not in the image (local mode)", "6.12.0-1.el10.x86_64", kNew},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pinRunningKernel(t, tc.running)
			sysroot := stageRHELSysroot(t, false)
			if err := os.RemoveAll(filepath.Join(sysroot, "lib", "modules", "5.14.0-rocky")); err != nil {
				t.Fatal(err)
			}
			stageKernels(t, sysroot, []string{kOld, kNew}, kLeftover)
			mustWriteFile(t, filepath.Join(sysroot, "etc", "default", "grub"), "GRUB_DEFAULT=saved\n")
			mustWriteFile(t, filepath.Join(sysroot, "usr", "bin", "grub2-editenv"), "")
			f := executor.NewFakeRunner(executor.FakeResponse{})
			a := &rhelAdapter{deps: AdapterDeps{Runner: f, BootContext: biosBootContext(sysroot, "/dev/sda")}}
			if err := a.RegenerateInitramfs(context.Background()); err != nil {
				t.Fatalf("RegenerateInitramfs: %v", err)
			}
			got := dracutKvers(f)
			other := kOld
			if tc.wantDefault == kOld {
				other = kNew
			}
			if strings.Join(got, ",") != tc.wantDefault+","+other {
				t.Fatalf("dracut --kver order = %v, want default %s first then %s (and no leftover)", got, tc.wantDefault, other)
			}
			if err := a.Install(context.Background()); err != nil {
				t.Fatalf("Install: %v", err)
			}
			want := "grub2-editenv - set saved_entry=abc123-" + tc.wantDefault
			if e := editenvCalls(f); len(e) != 1 || e[0] != want {
				t.Fatalf("editenv calls = %v, want [%s]", e, want)
			}
		})
	}
}

// A dracut failure on a kernel that is not the default warns; on the default
// it fails the step.
func TestDracutAllKernelsFailurePolicy(t *testing.T) {
	pinRunningKernel(t, "")
	for _, tc := range []struct {
		failing string
		wantErr bool
	}{{kOld, false}, {kNew, true}} {
		sysroot := t.TempDir()
		stageKernels(t, sysroot, []string{kOld, kNew})
		f := executor.NewFakeRunner(executor.FakeResponse{})
		f.AddMatch(executor.FakeMatch{
			Path:       "chroot",
			ArgsPrefix: append([]string{sysroot, "/usr/bin/env"}, dracutArgs(tc.failing)...),
			Response:   executor.FakeResponse{ExitCode: 1, Err: errors.New("exit 1")},
		})
		def, err := dracutAllKernels(context.Background(), AdapterDeps{Runner: f, BootContext: BootContext{Sysroot: sysroot}}, true)
		if (err != nil) != tc.wantErr {
			t.Fatalf("failing %s: err=%v, wantErr=%v", tc.failing, err, tc.wantErr)
		}
		if !tc.wantErr && def != kNew {
			t.Fatalf("default = %s, want %s", def, kNew)
		}
		if got := dracutKvers(f); len(got) != 2 && !tc.wantErr {
			t.Fatalf("both kernels must be attempted: %v", got)
		}
	}
}

// No kernel at all is an image problem (exit 3), not a retry.
func TestDracutAllKernelsNoKernelIsImageError(t *testing.T) {
	pinRunningKernel(t, "")
	sysroot := t.TempDir()
	_, err := dracutAllKernels(context.Background(),
		AdapterDeps{Runner: executor.NewFakeRunner(executor.FakeResponse{}), BootContext: BootContext{Sysroot: sysroot}}, true)
	if fe, ok := AsFatal(err); !ok || fe.Reason != "image-no-kernel" {
		t.Fatalf("want image-no-kernel FatalError, got %v", err)
	}
}

// Debian dracut-only images rebuild every kernel too, newest first.
func TestDebianDracutRebuildsEveryKernel(t *testing.T) {
	pinRunningKernel(t, "6.8.0-45-generic")
	sysroot := stageDebianSysroot(t, false)
	if err := os.Remove(filepath.Join(sysroot, "usr/sbin/update-initramfs")); err != nil {
		t.Fatal(err)
	}
	mustWriteFile(t, filepath.Join(sysroot, "usr/bin/dracut"), "")
	stageKernels(t, sysroot, []string{"6.8.0-45-generic", "6.8.0-48-generic"}, "6.8.0-31-generic")
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &debianAdapter{deps: AdapterDeps{Runner: f, BootContext: BootContext{Sysroot: sysroot}}}
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(dracutKvers(f), ","); got != "6.8.0-48-generic,6.8.0-45-generic" {
		t.Fatalf("dracut --kver order = %s", got)
	}
}

// A classic (non-BLS) grub.cfg names the kernel's entry by 10_linux's id.
func TestGrubCfgEntryIDFor(t *testing.T) {
	cfg := filepath.Join(t.TempDir(), "grub.cfg")
	mustWriteFile(t, cfg, "menuentry 'SLES' --class sles $menuentry_id_option 'gnulinux-6.4.0-150600.23.25-default-advanced-1234-abcd' {\n"+
		"\tlinux /vmlinuz-6.4.0-150600.23.25-default root=UUID=1234-abcd\n}\n")
	if got := grubCfgEntryIDFor(cfg, "6.4.0-150600.23.25-default"); got != "gnulinux-6.4.0-150600.23.25-default-advanced-1234-abcd" {
		t.Fatalf("id = %q", got)
	}
	if got := grubCfgEntryIDFor(cfg, "6.4.0-150600.23.25"); got != "" {
		t.Fatalf("a prefix of the version must not match: %q", got)
	}
}

// Review finding 7: the image's LVM devices file would hide this node's root
// PV inside the initramfs, so it is removed before dracut runs -- except in
// dry-run.
func TestRegenerateInitramfsRemovesImageLVMDevicesFile(t *testing.T) {
	pinRunningKernel(t, "")
	for _, dry := range []bool{false, true} {
		for _, fam := range []string{"rhel", "debian"} {
			var sysroot string
			var a Adapter
			f := executor.NewFakeRunner(executor.FakeResponse{})
			deps := func(s string) AdapterDeps {
				return AdapterDeps{Runner: f, FS: fsops.FS{DryRun: dry}, BootContext: BootContext{Sysroot: s}}
			}
			if fam == "rhel" {
				sysroot = stageRHELSysroot(t, false)
				a = &rhelAdapter{deps: deps(sysroot)}
			} else {
				sysroot = stageDebianSysroot(t, false)
				a = &debianAdapter{deps: deps(sysroot)}
			}
			devices := filepath.Join(sysroot, lvmDevicesFile)
			mustWriteFile(t, devices, "VERSION=1.1.2\nIDTYPE=sys_wwid IDNAME=naa.6000c29 DEVNAME=/dev/sda3 PVID=abc PART=3\n")
			if err := a.RegenerateInitramfs(context.Background()); err != nil {
				t.Fatalf("%s: %v", fam, err)
			}
			_, err := os.Stat(devices)
			if !dry && err == nil {
				t.Errorf("%s: %s still present", fam, lvmDevicesFile)
			}
			if dry && err != nil {
				t.Errorf("%s: dry-run removed %s", fam, lvmDevicesFile)
			}
		}
	}
}

// A reused root (install_mode local) keeps its LVM devices file: it was set up
// for this node, by lpart or by the site, and is not the image host's.
func TestRegenerateInitramfsKeepsDevicesFileOnReusedRoot(t *testing.T) {
	pinRunningKernel(t, "")
	sysroot := stageRHELSysroot(t, false)
	f := executor.NewFakeRunner(executor.FakeResponse{})
	a := &rhelAdapter{deps: AdapterDeps{Runner: f, BootContext: BootContext{Sysroot: sysroot, ReusesRoot: true}}}
	devices := filepath.Join(sysroot, lvmDevicesFile)
	mustWriteFile(t, devices, "VERSION=1.1.2\n")
	if err := a.RegenerateInitramfs(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(devices); err != nil {
		t.Fatalf("%s removed on a reused root: %v", lvmDevicesFile, err)
	}
}

// An image without grub2-editenv keeps its own saved_entry rather than failing
// the install on a command that can never succeed.
func TestSetDefaultBootEntryWithoutEditenv(t *testing.T) {
	sysroot := t.TempDir()
	stageKernels(t, sysroot, []string{kNew})
	f := executor.NewFakeRunner(executor.FakeResponse{})
	if err := setDefaultBootEntry(context.Background(), AdapterDeps{Runner: f, BootContext: BootContext{Sysroot: sysroot}}, kNew); err != nil {
		t.Fatal(err)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("no command expected, got %v", f.Calls())
	}
}
