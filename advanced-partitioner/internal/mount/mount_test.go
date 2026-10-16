package mount

import (
	"context"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
)

func TestMountSysrootTreeShallowFirst(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// findmnt -rn -M <target> for each new mount: not mounted (default
	// FakeResponse returns nil error which would mean mounted; override).
	for _, target := range []string{"/sysroot", "/sysroot/boot", "/sysroot/boot/efi"} {
		f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", target, "-o", "SOURCE"},
			Response: executor.FakeResponse{Stdout: []byte("")}})
		f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", target},
			Response: executor.FakeResponse{Stdout: []byte("")}})
	}
	// ensureNotMountedElsewhere: findmnt -rn -S <source> -o TARGET returns nothing.
	for _, source := range []string{"/dev/mapper/os-root", "/dev/sda2", "/dev/sda1"} {
		f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", source, "-o", "TARGET"},
			Response: executor.FakeResponse{Stdout: []byte("")}})
	}

	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	res, err := m.MountSysrootTree(context.Background(), fs.Filesystems{
		Filesystems: []fs.Filesystem{
			// Order is intentionally shuffled to exercise the sort.
			{Mount: "/boot/efi", Device: "/dev/sda1", FS: "vfat"},
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs"},
			{Mount: "/boot", Device: "/dev/sda2", FS: "xfs"},
		},
	})
	if err != nil {
		t.Fatalf("MountSysrootTree: %v", err)
	}
	if res.RootDevice != "/dev/mapper/os-root" {
		t.Fatalf("RootDevice = %q", res.RootDevice)
	}

	// Verify call order: root mount -> /boot -> /boot/efi.
	mounts := []string{}
	for _, c := range f.Calls() {
		if c.Path == "mount" {
			mounts = append(mounts, strings.Join(c.Args, " "))
		}
	}
	wantOrder := []string{
		// propagation firewall first (private layer under the root fs)
		"-t tmpfs -o size=64k,mode=0755 lpart-propagation-firewall /sysroot",
		"--make-private /sysroot",
		"-t xfs -o defaults /dev/mapper/os-root /sysroot",
		"-t xfs -o defaults /dev/sda2 /sysroot/boot",
		"-t vfat -o defaults /dev/sda1 /sysroot/boot/efi",
	}
	if len(mounts) != len(wantOrder) {
		t.Fatalf("mount count = %d, want %d:\n  %s", len(mounts), len(wantOrder), strings.Join(mounts, "\n  "))
	}
	for i, w := range wantOrder {
		if mounts[i] != w {
			t.Fatalf("[%d] got %q, want %q", i, mounts[i], w)
		}
	}
}

func TestMountSysrootTreeSkipsSwap(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/mapper/os-root", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot", "-o", "SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("")}})

	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	_, err := m.MountSysrootTree(context.Background(), fs.Filesystems{
		Filesystems: []fs.Filesystem{
			{Mount: "/", Device: "/dev/mapper/os-root", FS: "xfs"},
			{Mount: "swap", Device: "/dev/sda3", FS: "swap"},
		},
	})
	if err != nil {
		t.Fatalf("MountSysrootTree: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Path == "mount" {
			for _, a := range c.Args {
				if a == "/sysroot/swap" || a == "swap" {
					t.Fatalf("unexpected swap mount: %v", c.Args)
				}
			}
		}
	}
}

func TestMountSysrootTreeRejectsDifferentSourceAtSameTarget(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// ensureNotMountedElsewhere: source not mounted elsewhere.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/sda1", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	// mountedSource: target is already mounted with a different source.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot", "-o", "SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/dev/sdb1\n")}})

	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	_, err := m.MountSysrootTree(context.Background(), fs.Filesystems{
		Filesystems: []fs.Filesystem{
			{Mount: "/", Device: "/dev/sda1", FS: "xfs"},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "already used by") {
		t.Fatalf("expected 'already used by' error; got %v", err)
	}
}

func TestUnmountSysrootTreeDeepestFirst(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	// findmnt -rn -o TARGET listing the live mounts.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte(
			"/proc\n/sysroot\n/sysroot/boot\n/sysroot/boot/efi\n/somewhere/else\n")}})
	for _, t := range []string{"/sysroot", "/sysroot/boot", "/sysroot/boot/efi"} {
		f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", t},
			Response: executor.FakeResponse{Stdout: []byte(t + " /dev/x ext4\n")}})
		f.AddMatch(executor.FakeMatch{Path: "umount", ArgsPrefix: []string{t},
			Response: executor.FakeResponse{}})
	}
	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	if err := m.UnmountSysrootTree(context.Background()); err != nil {
		t.Fatalf("UnmountSysrootTree: %v", err)
	}

	// Capture umount order: should be deepest first.
	order := []string{}
	for _, c := range f.Calls() {
		if c.Path == "umount" && len(c.Args) == 1 {
			order = append(order, c.Args[0])
		}
	}
	want := []string{"/sysroot/boot/efi", "/sysroot/boot", "/sysroot"}
	if len(order) != len(want) {
		t.Fatalf("umount order = %v, want %v", order, want)
	}
	for i, w := range want {
		if order[i] != w {
			t.Fatalf("[%d] got %q, want %q", i, order[i], w)
		}
	}
}

func TestUnmountFromResult(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	for _, target := range []string{"/sysroot", "/sysroot/boot"} {
		f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", target},
			Response: executor.FakeResponse{Stdout: []byte("mounted\n")}})
		f.AddMatch(executor.FakeMatch{Path: "umount", ArgsPrefix: []string{target}, Response: executor.FakeResponse{}})
	}
	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	if err := m.UnmountFromResult(context.Background(), fs.Filesystems{
		Filesystems: []fs.Filesystem{
			{Mount: "/", Device: "/dev/sda1", FS: "xfs"},
			{Mount: "/boot", Device: "/dev/sda2", FS: "xfs"},
			{Mount: "swap", Device: "/dev/sda3", FS: "swap"},
		},
	}); err != nil {
		t.Fatalf("UnmountFromResult: %v", err)
	}
	order := []string{}
	for _, c := range f.Calls() {
		if c.Path == "umount" && len(c.Args) == 1 {
			order = append(order, c.Args[0])
		}
	}
	if !equalSlices(order, []string{"/sysroot/boot", "/sysroot"}) {
		t.Fatalf("umount order = %v, want [/sysroot/boot /sysroot]", order)
	}
}

func TestChildMountPointsInOrder(t *testing.T) {
	got := childMountPointsInOrder(fs.Filesystems{
		Filesystems: []fs.Filesystem{
			{Mount: "/boot/efi"},
			{Mount: "/"},
			{Mount: "/var/log"},
			{Mount: "/boot"},
			{Mount: "swap"},
			{Mount: ""},
		},
	})
	want := []string{"/boot", "/boot/efi", "/var/log"} // /boot has 1 slash, /boot/efi+/var/log have 2; lex within depth
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// The propagation firewall keeps the sysroot tree out of other mount
// namespaces (udevd/haveged sandboxes) so teardown can always release the
// devices — see firewallSource in mount.go. Two pins: (1) when the firewall
// is already the top mount at the sysroot, the root fs mounts OVER it (no
// "already used by" refusal, no second firewall layer); (2) when the target
// fs itself is already mounted (re-entrant pass), no firewall is stacked on
// top of it either.
func TestMountSysrootTreeMountsOverExistingFirewall(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/sda1", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("")}})
	// The firewall from an earlier pass is the top mount at /sysroot.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot", "-o", "SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("lpart-propagation-firewall\n")}})

	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	if _, err := m.MountSysrootTree(context.Background(), fs.Filesystems{
		Filesystems: []fs.Filesystem{{Mount: "/", Device: "/dev/sda1", FS: "xfs"}},
	}); err != nil {
		t.Fatalf("MountSysrootTree over existing firewall: %v", err)
	}
	fsMounted, firewalls := false, 0
	for _, c := range f.Calls() {
		if c.Path != "mount" {
			continue
		}
		args := strings.Join(c.Args, " ")
		if strings.Contains(args, "lpart-propagation-firewall") {
			firewalls++
		}
		if args == "-t xfs -o defaults /dev/sda1 /sysroot" {
			fsMounted = true
		}
	}
	if !fsMounted {
		t.Fatal("root fs was not mounted over the existing firewall")
	}
	if firewalls != 0 {
		t.Fatalf("firewall stacked again (%d extra layers)", firewalls)
	}
}

func TestMountSysrootTreeNoFirewallOverMountedRoot(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/sda1", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("/sysroot\n")}})
	// Re-entrant pass: the target fs is already mounted at /sysroot.
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot", "-o", "SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/dev/sda1\n")}})

	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	if _, err := m.MountSysrootTree(context.Background(), fs.Filesystems{
		Filesystems: []fs.Filesystem{{Mount: "/", Device: "/dev/sda1", FS: "xfs"}},
	}); err != nil {
		t.Fatalf("re-entrant MountSysrootTree: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Path == "mount" && strings.Contains(strings.Join(c.Args, " "), "lpart-propagation-firewall") {
			t.Fatalf("firewall stacked on top of an already-mounted root: %v", c.Args)
		}
	}
}

func TestPreflightRequiresRunner(t *testing.T) {
	m := New(Options{})
	if _, err := m.MountSysrootTree(context.Background(), fs.Filesystems{}); err == nil {
		t.Fatal("expected Runner-required error")
	}
	if err := m.UnmountSysrootTree(context.Background()); err == nil {
		t.Fatal("expected Runner-required error")
	}
	if err := m.UnmountFromResult(context.Background(), fs.Filesystems{}); err == nil {
		t.Fatal("expected Runner-required error")
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// findmnt -r escapes a space as \x20: a sysroot whose path has one must still
// be recognised in the live mount list and as the place a source is mounted.
func TestFindmntTargetsAreDecoded(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-o", "TARGET"},
		Response: executor.FakeResponse{Stdout: []byte("/proc\n/sys\\x20root\n/sys\\x20root/boot\n")}})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-S", "/dev/sdb1"},
		Response: executor.FakeResponse{Stdout: []byte("/sys\\x20root/boot\n")}})
	m := New(Options{Runner: f, Sysroot: "/sys root"})
	if got := m.liveSysrootTargets(context.Background()); len(got) != 2 || got[1] != "/sys root/boot" {
		t.Errorf("liveSysrootTargets = %q, want [/sys root /sys root/boot]", got)
	}
	if err := m.ensureNotMountedElsewhere(context.Background(), "/dev/sdb1"); err != nil {
		t.Errorf("a source mounted below the sysroot is not mounted elsewhere: %v", err)
	}
}

// The SOURCE column is escaped too: the device mounted at a target must come
// back as its real path, or the re-entrancy check sees a different source.
func TestFindmntSourceIsDecoded(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "findmnt", ArgsPrefix: []string{"-rn", "-M", "/sysroot/data", "-o", "SOURCE"},
		Response: executor.FakeResponse{Stdout: []byte("/dev/disk/by-label/my\\x20data\n")}})
	m := New(Options{Runner: f, Sysroot: "/sysroot"})
	got, mounted := m.mountedSource(context.Background(), "/sysroot/data")
	if !mounted || got != "/dev/disk/by-label/my data" {
		t.Errorf("mountedSource = %q, %v, want /dev/disk/by-label/my data, true", got, mounted)
	}
}
