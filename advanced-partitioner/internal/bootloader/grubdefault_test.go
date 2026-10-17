package bootloader

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// imageGrubDefault is an /etc/default/grub as an image builder ships it: a
// serial console, a crashkernel reservation, its build host's root storage,
// and the installer arguments an image captured from a netbooted node drags
// along.
const imageGrubDefault = `# image builder settings
GRUB_TIMEOUT=10
GRUB_DISTRIBUTOR="$(sed 's, release .*$,,g' /etc/system-release)"
GRUB_DEFAULT=0
GRUB_DISABLE_SUBMENU=false
GRUB_TERMINAL="serial console"
GRUB_SERIAL_COMMAND="serial --speed=115200 --unit=0 --word=8 --parity=no --stop=1"
GRUB_CMDLINE_LINUX="crashkernel=1G-4G:192M console=ttyS0,115200 rd.lvm.lv=old/root quiet"
GRUB_CMDLINE_LINUX_DEFAULT="console=tty0 console=ttyS1,9600 net.ifnames=0 root=luna luna.node=node001"
GRUB_ENABLE_BLSCFG=true
`

// labInstallerCmdline is an installer command line as the controller's iPXE
// template builds it, with two osimage kernel options.
const labInstallerCmdline = "root=luna luna.mac=00:50:56:b3:6b:d9 luna.ip=10.141.0.2/16 luna.gw=10.141.255.254 " +
	"luna.url=https://10.141.255.252:7050 luna.verifycert=False luna.node=node002 luna.hostname=node002 " +
	"luna.loghost=10.141.255.254 luna.service=0 _net.ifnames=0 _biosdevname=0 initrd=initrd.img boot=ramdisk\n"

func lunaOptionsOf(t *testing.T, cmdline string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "cmdline")
	if err := os.WriteFile(p, []byte(cmdline), 0o600); err != nil {
		t.Fatal(err)
	}
	got, err := LunaKernelOptions(p)
	if err != nil {
		t.Fatalf("LunaKernelOptions: %v", err)
	}
	return got
}

// The installer's own arguments stay behind; the osimage options pass
// through exactly as written, underscore placeholders included.
func TestLunaKernelOptions(t *testing.T) {
	if got := lunaOptionsOf(t, labInstallerCmdline); got != "_net.ifnames=0 _biosdevname=0" {
		t.Fatalf("Luna options = %q", got)
	}
	// iPXE's image name, the dracut network arguments and lpart's own
	// switches are the installer's too; lpart's root arguments are its own.
	withMore := "kernel " + labInstallerCmdline + " BOOTIF=01-aa rd.neednet=1 ip=dhcp lpart.notui=1 rd.luna rootfstype=xfs rd.md=1 console=ttyS0 rd.break"
	if got := lunaOptionsOf(t, withMore); got != "_net.ifnames=0 _biosdevname=0 console=ttyS0 rd.break" {
		t.Fatalf("Luna options = %q", got)
	}
	if _, err := LunaKernelOptions(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing command line must be an error")
	}
}

// An image without /etc/default/grub gets lpart's file, with Luna's options
// after lpart's arguments.
func TestGrubDefaultFileNoImageFile(t *testing.T) {
	got := grubDefaultFile("", "rd.auto rd.lvm=1 rootfstype=xfs", "_net.ifnames=0 _biosdevname=0", "saved")
	want := "GRUB_TIMEOUT=5\nGRUB_DEFAULT=saved\nGRUB_DISABLE_SUBMENU=true\nGRUB_DISABLE_OS_PROBER=true\n" +
		"GRUB_DISABLE_RECOVERY=true\nGRUB_TERMINAL_OUTPUT=\"console\"\n" +
		"GRUB_CMDLINE_LINUX=\"rd.auto rd.lvm=1 rootfstype=xfs _net.ifnames=0 _biosdevname=0\"\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Only the allow-list survives from the image's file: its serial console
// keys verbatim (replacing GRUB_TERMINAL_OUTPUT), console=ttyS* and
// crashkernel= from both command lines; every other key and argument is
// lpart's or Luna's.
func TestGrubDefaultFileAllowList(t *testing.T) {
	got := grubDefaultFile(imageGrubDefault, "rd.auto rd.lvm=1 rootfstype=xfs", "_net.ifnames=0 _biosdevname=0", "saved")
	want := "GRUB_TIMEOUT=5\nGRUB_DEFAULT=saved\nGRUB_DISABLE_SUBMENU=true\nGRUB_DISABLE_OS_PROBER=true\n" +
		"GRUB_DISABLE_RECOVERY=true\n" +
		"GRUB_TERMINAL=\"serial console\"\n" +
		"GRUB_SERIAL_COMMAND=\"serial --speed=115200 --unit=0 --word=8 --parity=no --stop=1\"\n" +
		"GRUB_ENABLE_BLSCFG=true\n" +
		"GRUB_CMDLINE_LINUX=\"rd.auto rd.lvm=1 rootfstype=xfs crashkernel=1G-4G:192M console=ttyS0,115200 console=ttyS1,9600 _net.ifnames=0 _biosdevname=0\"\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// The image's GRUB_ENABLE_BLSCFG decides how later kernel updates reach the
// menu, so it is carried over; without one in the image none is written.
func TestGrubDefaultFileKeepsTheImagesBLSSetting(t *testing.T) {
	if got := grubDefaultFile("GRUB_ENABLE_BLSCFG=true\n", "rd.auto", "", "saved"); !strings.Contains(got, "GRUB_ENABLE_BLSCFG=true\n") {
		t.Fatalf("GRUB_ENABLE_BLSCFG lost:\n%s", got)
	}
	if got := grubDefaultFile("", "rd.auto", "", "saved"); strings.Contains(got, "GRUB_ENABLE_BLSCFG") {
		t.Fatalf("GRUB_ENABLE_BLSCFG invented:\n%s", got)
	}
}

// Debian keeps GRUB_DEFAULT=0 but the submenu is off on every family.
func TestGrubDefaultFileDebian(t *testing.T) {
	got := grubDefaultFile("GRUB_DISABLE_SUBMENU=false\n", "rd.auto", "", "0")
	if !strings.Contains(got, "GRUB_DEFAULT=0\n") || !strings.Contains(got, "GRUB_DISABLE_SUBMENU=true\n") {
		t.Fatalf("got:\n%s", got)
	}
}

// Single quotes, a trailing comment, a commented-out assignment, an exported
// one and an argument present in several places.
func TestGrubDefaultFileShellForms(t *testing.T) {
	image := "#GRUB_TERMINAL=\"serial\"\n" +
		"export GRUB_CMDLINE_LINUX_DEFAULT='quiet crashkernel=auto' # site\n" +
		"GRUB_CMDLINE_LINUX=\"$GRUB_CMDLINE_LINUX console=ttyS0,115200\" # sol\n" +
		"GRUB_CMDLINE_LINUX=crashkernel=auto\n"
	got := grubDefaultFile(image, "rd.auto", "console=ttyS0,115200", "0")
	if strings.Contains(got, "GRUB_TERMINAL=") || !strings.Contains(got, "GRUB_TERMINAL_OUTPUT=\"console\"\n") {
		t.Errorf("commented-out GRUB_TERMINAL must not count:\n%s", got)
	}
	if !strings.Contains(got, "GRUB_CMDLINE_LINUX=\"rd.auto crashkernel=auto console=ttyS0,115200\"\n") {
		t.Errorf("command line:\n%s", got)
	}
}

// grub2-mkconfig sources the file with set -e: Luna options carrying shell
// syntax must reach GRUB_CMDLINE_LINUX unchanged, not break or run in it.
func TestGrubDefaultFileSourcesLunaOptionsVerbatim(t *testing.T) {
	luna := lunaOptionsOf(t, labInstallerCmdline+` dyndbg="file foo.c +p" a=$HOME b=`+"`id`"+` c=x\y d='q' quiet`)
	file := filepath.Join(t.TempDir(), "grub")
	if err := os.WriteFile(file, []byte(grubDefaultFile("", "rd.auto", luna, "saved")), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-ec", `. "$1"; printf %s "$GRUB_CMDLINE_LINUX"`, "sh", file).CombinedOutput() //nolint:gosec // test-built path
	if err != nil {
		t.Fatalf("sourcing the file failed: %v: %s", err, out)
	}
	if want := "rd.auto " + luna; string(out) != want {
		t.Fatalf("GRUB_CMDLINE_LINUX = %q, want %q", out, want)
	}
	once := grubDefaultFile("", "rd.auto", luna, "saved")
	if twice := grubDefaultFile(once, "rd.auto", luna, "saved"); twice != once {
		t.Fatalf("second run changed the file:\n%s\n%s", once, twice)
	}
}

// A quoted Luna option is one argument: a word it shares with another quoted
// value is no duplicate, and dropping it would leave a quote open in grub.cfg.
func TestGrubDefaultFileKeepsQuotedLunaOptionsWhole(t *testing.T) {
	luna := `a="x y" b="z y" quiet rd.auto quiet`
	file := filepath.Join(t.TempDir(), "grub")
	if err := os.WriteFile(file, []byte(grubDefaultFile("", "rd.auto", luna, "saved")), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("sh", "-ec", `. "$1"; printf %s "$GRUB_CMDLINE_LINUX"`, "sh", file).CombinedOutput() //nolint:gosec // test-built path
	if err != nil {
		t.Fatalf("sourcing the file failed: %v: %s", err, out)
	}
	if want := `rd.auto a="x y" b="z y" quiet`; string(out) != want {
		t.Fatalf("GRUB_CMDLINE_LINUX = %q, want %q", out, want)
	}
}

// The last console= is /dev/console: a repeat in the Luna options must not be
// dropped as a duplicate of the image's, and a re-run must not grow the line.
func TestGrubDefaultFileKeepsTheLastConsole(t *testing.T) {
	image := `GRUB_CMDLINE_LINUX="console=ttyS0,115200"`
	luna := "console=tty0 console=ttyS0,115200 quiet quiet"
	once := grubDefaultFile(image, "rd.auto", luna, "saved")
	if want := `GRUB_CMDLINE_LINUX="rd.auto console=tty0 console=ttyS0,115200 quiet"`; !strings.Contains(once, want+"\n") {
		t.Fatalf("want %s in:\n%s", want, once)
	}
	if twice := grubDefaultFile(once, "rd.auto", luna, "saved"); twice != once {
		t.Fatalf("second run changed the file:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// Finalise is re-run on every sync: the file it wrote is a valid input and
// produces itself.
func TestGrubDefaultFileIdempotent(t *testing.T) {
	once := grubDefaultFile(imageGrubDefault, "rd.auto rootfstype=xfs", "_net.ifnames=0", "saved")
	if twice := grubDefaultFile(once, "rd.auto rootfstype=xfs", "_net.ifnames=0", "saved"); twice != once {
		t.Fatalf("second run changed the file:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// End to end through both adapters: the file on disk is replaced, not merged,
// and carries the Luna options from the boot context.
func TestAdaptersWriteGrubDefault(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sysroot func(*testing.T) string
		build   func(AdapterDeps) Adapter
		def     string
	}{
		{"rhel", func(t *testing.T) string { return stageRHELSysroot(t, false) },
			func(d AdapterDeps) Adapter { return &rhelAdapter{deps: d} }, "GRUB_DEFAULT=saved"},
		{"debian", func(t *testing.T) string { return stageDebianSysroot(t, false) },
			func(d AdapterDeps) Adapter { return &debianAdapter{deps: d} }, "GRUB_DEFAULT=0"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sysroot := tc.sysroot(t)
			p := filepath.Join(sysroot, "etc", "default", "grub")
			if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(p, []byte(imageGrubDefault), 0o600); err != nil {
				t.Fatal(err)
			}
			bctx := biosBootContext(sysroot, "/dev/sda")
			bctx.KernelArgs = "rd.auto rootfstype=xfs"
			bctx.LunaKernelOptions = "_net.ifnames=0"
			a := tc.build(AdapterDeps{Runner: executor.NewFakeRunner(executor.FakeResponse{}), BootContext: bctx})
			if err := a.Install(context.Background()); err != nil {
				t.Fatalf("Install: %v", err)
			}
			got := readFile(t, p)
			for _, want := range []string{`GRUB_TERMINAL="serial console"`, "GRUB_SERIAL_COMMAND=", "GRUB_ENABLE_BLSCFG=", "GRUB_DISABLE_SUBMENU=true\n",
				`GRUB_CMDLINE_LINUX="rd.auto rootfstype=xfs crashkernel=1G-4G:192M console=ttyS0,115200 console=ttyS1,9600 _net.ifnames=0"`,
				tc.def + "\n"} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			for _, unwanted := range []string{"GRUB_TIMEOUT=10", "GRUB_DISTRIBUTOR", "rd.lvm.lv=old", "root=luna", "quiet"} {
				if strings.Contains(got, unwanted) {
					t.Errorf("image setting %q survived:\n%s", unwanted, got)
				}
			}
		})
	}
}
