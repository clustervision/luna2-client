package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// imageGrubDefault is an /etc/default/grub as an anaconda-built el8 image
// ships it, plus what an image builder adds for serial-over-LAN and what an
// image captured from a netbooted node drags along.
const imageGrubDefault = `# image builder settings
GRUB_TIMEOUT=10
GRUB_DISTRIBUTOR="$(sed 's, release .*$,,g' /etc/system-release)"
GRUB_DEFAULT=0
GRUB_DISABLE_SUBMENU=true
GRUB_TERMINAL="serial console"
GRUB_SERIAL_COMMAND="serial --speed=115200 --unit=0 --word=8 --parity=no --stop=1"
GRUB_CMDLINE_LINUX="console=tty0 console=ttyS0,115200 crashkernel=auto resume=/dev/mapper/rhel-swap rd.lvm.lv=rhel/root rd.lvm.lv=rhel/swap root=/dev/mapper/rhel-root net.ifnames=0 biosdevname=0 hugepagesz=1G hugepages=4 rd.md.uuid=0a1b:2c3d root=luna luna.url=https://ctl:7050 luna.node=node001 rd.luna BOOTIF=01-aa-bb rd.neednet=1 ip=dhcp rhgb quiet"
GRUB_DISABLE_RECOVERY="true"
GRUB_ENABLE_BLSCFG=true
`

func mergedRHEL(t *testing.T, existing, kernelArgs string) string {
	t.Helper()
	return mergeGrubDefault(existing, rhelGrubDefaultKeys, kernelArgs)
}

func cmdlineOf(t *testing.T, body, key string) []string {
	t.Helper()
	var last string
	found := false
	for _, ln := range strings.Split(body, "\n") {
		if strings.HasPrefix(ln, key+"=") {
			last = strings.TrimPrefix(ln, key+"=")
			found = true
		}
	}
	if !found {
		t.Fatalf("%s not in:\n%s", key, body)
	}
	return strings.Fields(strings.Trim(last, `"'`))
}

func TestMergeGrubDefaultKeepsImageSettings(t *testing.T) {
	const lpart = "rd.auto rd.md=1 rd.lvm=1 rootfstype=xfs"
	got := mergedRHEL(t, imageGrubDefault, lpart)

	// Every key the image set survives, values intact except lpart's two.
	for _, want := range []string{
		"# image builder settings",
		"GRUB_TIMEOUT=10",
		`GRUB_DISTRIBUTOR="$(sed 's, release .*$,,g' /etc/system-release)"`,
		`GRUB_TERMINAL="serial console"`,
		`GRUB_SERIAL_COMMAND="serial --speed=115200 --unit=0 --word=8 --parity=no --stop=1"`,
		`GRUB_DISABLE_RECOVERY="true"`,
		"GRUB_ENABLE_BLSCFG=true",
		"GRUB_DEFAULT=saved",
		"GRUB_DISABLE_OS_PROBER=true",
	} {
		if !strings.Contains(got, want+"\n") {
			t.Errorf("merged file lost %q:\n%s", want, got)
		}
	}
	for _, unwanted := range []string{"GRUB_DEFAULT=0", "GRUB_TIMEOUT=5", "GRUB_TERMINAL_OUTPUT"} {
		if strings.Contains(got, unwanted) {
			t.Errorf("merged file has %q (image value overridden or console forced over serial):\n%s", unwanted, got)
		}
	}
	if n := strings.Count(got, "GRUB_DISABLE_SUBMENU="); n != 1 {
		t.Errorf("GRUB_DISABLE_SUBMENU appears %d times:\n%s", n, got)
	}

	args := cmdlineOf(t, got, "GRUB_CMDLINE_LINUX")
	joined := " " + strings.Join(args, " ") + " "
	for _, keep := range []string{"console=tty0", "console=ttyS0,115200", "crashkernel=auto",
		"net.ifnames=0", "biosdevname=0", "hugepagesz=1G", "hugepages=4", "rhgb", "quiet"} {
		if !strings.Contains(joined, " "+keep+" ") {
			t.Errorf("image argument %q lost: %v", keep, args)
		}
	}
	for _, need := range strings.Fields(lpart) {
		if !strings.Contains(joined, " "+need+" ") {
			t.Errorf("lpart argument %q missing: %v", need, args)
		}
	}
	for _, a := range args {
		if strings.HasPrefix(a, "root=") || strings.HasPrefix(a, "resume=") ||
			strings.HasPrefix(a, "rd.lvm.lv=") || strings.HasPrefix(a, "rd.md.uuid=") ||
			strings.HasPrefix(a, "luna") || strings.HasPrefix(a, "rd.luna") ||
			strings.HasPrefix(a, "BOOTIF=") || strings.HasPrefix(a, "rd.neednet") || strings.HasPrefix(a, "ip=") {
			t.Errorf("build-host or installer-only argument %q leaked: %v", a, args)
		}
	}
	// image order first, lpart's arguments after
	if args[0] != "console=tty0" || args[len(args)-1] != "rootfstype=xfs" {
		t.Errorf("argument order: %v", args)
	}
}

// A missing /etc/default/grub produces exactly what the old overwriting
// writer produced.
func TestMergeGrubDefaultEmptyMatchesOldOutput(t *testing.T) {
	got := mergedRHEL(t, "", "rd.auto")
	want := "GRUB_TIMEOUT=5\nGRUB_DEFAULT=saved\nGRUB_DISABLE_SUBMENU=true\nGRUB_DISABLE_OS_PROBER=true\n" +
		"GRUB_DISABLE_RECOVERY=true\nGRUB_TERMINAL_OUTPUT=\"console\"\nGRUB_CMDLINE_LINUX=\"rd.auto\"\n"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

// Re-running the merge on its own output changes nothing (finalise is retried
// and re-run on every sync).
func TestMergeGrubDefaultIdempotent(t *testing.T) {
	const lpart = "rd.auto rd.md=1 rootfstype=xfs"
	once := mergedRHEL(t, imageGrubDefault, lpart)
	if twice := mergedRHEL(t, once, lpart); twice != once {
		t.Fatalf("second merge changed the file:\n--- once ---\n%s\n--- twice ---\n%s", once, twice)
	}
}

// Quoting is preserved, a "$GRUB_CMDLINE_LINUX ..." continuation gets lpart's
// arguments on the last assignment, and GRUB_CMDLINE_LINUX_DEFAULT is scrubbed
// but otherwise kept.
func TestMergeGrubDefaultQuotingAndContinuation(t *testing.T) {
	in := "GRUB_CMDLINE_LINUX='console=ttyS1,115200 root=/dev/sda2'\n" +
		"GRUB_CMDLINE_LINUX=\"$GRUB_CMDLINE_LINUX crashkernel=512M\" # site\n" +
		"export GRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash luna.node=n1 rd.lvm.vg=vg0\"\n"
	got := mergeGrubDefault(in, debianGrubDefaultKeys, "rd.auto rootfstype=ext4")
	for _, want := range []string{
		"GRUB_CMDLINE_LINUX='console=ttyS1,115200'\n",
		"GRUB_CMDLINE_LINUX=\"$GRUB_CMDLINE_LINUX crashkernel=512M rd.auto rootfstype=ext4\" # site\n",
		"export GRUB_CMDLINE_LINUX_DEFAULT=\"quiet splash\"\n",
		"GRUB_DEFAULT=0\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
}

// End to end through both adapters: the file on disk is merged, not replaced.
func TestAdaptersMergeImageGrubDefault(t *testing.T) {
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
			a := tc.build(AdapterDeps{Runner: executor.NewFakeRunner(executor.FakeResponse{}), BootContext: bctx})
			if err := a.Install(context.Background()); err != nil {
				t.Fatalf("Install: %v", err)
			}
			got := readFile(t, p)
			for _, want := range []string{"console=ttyS0,115200", "crashkernel=auto", `GRUB_TERMINAL="serial console"`,
				"GRUB_SERIAL_COMMAND=", "rd.auto rootfstype=xfs\"", tc.def + "\n"} {
				if !strings.Contains(got, want) {
					t.Errorf("missing %q in:\n%s", want, got)
				}
			}
			if strings.Contains(got, "root=luna") || strings.Contains(got, "rd.lvm.lv=rhel") {
				t.Errorf("installer or build-host argument survived:\n%s", got)
			}
		})
	}
}
