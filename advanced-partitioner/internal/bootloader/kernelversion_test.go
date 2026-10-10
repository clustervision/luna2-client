package bootloader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pinRunningKernel makes the "what did the installer boot" answer deterministic for a
// test, instead of whatever kernel the suite happens to run on.
func pinRunningKernel(t *testing.T, kver string) {
	t.Helper()
	prev := runningKernelVersion
	runningKernelVersion = func() string { return kver }
	t.Cleanup(func() { runningKernelVersion = prev })
}

// TestDetectKernelVersion pins the el10 debug-kernel behaviour.
//
// Regression: el10 installs kernel-debug-core + kernel-debug-modules* alongside the
// real kernel, so the installed rootfs holds BOTH <ver>.x86_64 and <ver>.x86_64+debug.
// detectKernelVersion used "last wins" over os.ReadDir (which returns sorted names), so
// the DEBUG tree won and bootloader-finalise ran `dracut --kver <...+debug>`, which
// exits 1 -> the node install dies at postscript and luna retries forever. rocky8/rocky9
// ship one kernel each, which is why only el10 ever showed it.
func TestDetectKernelVersion(t *testing.T) {
	pinRunningKernel(t, "")
	cases := []struct {
		name string
		dirs []string
		want string
	}{
		{
			name: "el10 ships a debug kernel alongside the real one -> pick the real one",
			dirs: []string{"6.12.0-211.34.1.el10_2.x86_64", "6.12.0-211.34.1.el10_2.x86_64+debug"},
			want: "6.12.0-211.34.1.el10_2.x86_64",
		},
		{
			name: "single kernel (rocky9) -> unchanged",
			dirs: []string{"5.14.0-687.26.1.el9_8.x86_64"},
			want: "5.14.0-687.26.1.el9_8.x86_64",
		},
		{
			name: "debug-only image still installs rather than failing outright",
			dirs: []string{"6.12.0-211.34.1.el10_2.x86_64+debug"},
			want: "6.12.0-211.34.1.el10_2.x86_64+debug",
		},
		{
			name: "several real kernels -> last (highest) still wins",
			dirs: []string{"6.12.0-100.el10_2.x86_64", "6.12.0-211.34.1.el10_2.x86_64", "6.12.0-211.34.1.el10_2.x86_64+debug"},
			want: "6.12.0-211.34.1.el10_2.x86_64",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sysroot := t.TempDir()
			mods := filepath.Join(sysroot, "lib", "modules")
			if err := os.MkdirAll(mods, 0o750); err != nil {
				t.Fatal(err)
			}
			for _, d := range tc.dirs {
				if err := os.Mkdir(filepath.Join(mods, d), 0o750); err != nil {
					t.Fatal(err)
				}
			}
			got, err := detectKernelVersion(sysroot)
			if err != nil {
				t.Fatalf("detectKernelVersion: %v", err)
			}
			if got != tc.want {
				t.Errorf("picked %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDetectKernelVersionEmpty(t *testing.T) {
	pinRunningKernel(t, "")
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := detectKernelVersion(sysroot); err == nil {
		t.Error("expected an error when lib/modules is empty, got nil")
	}
}

// TestDetectKernelVersionSkipsLeftovers pins the field failure: a converted image
// carries eleven module directories and only one installed kernel. The other ten are
// what an erased kernel package leaves behind -- depmod output and empty extra/
// directories, owned by nothing. Selecting one of them hands dracut a version with no
// modules, and the install dies at the bootloader phase.
//
// The directory names are the ones taken from the customer image. They matter: a
// byte-wise sort puts 553.97.1 and 553.8.1 after 553.123.1, so the leftover does not
// merely compete for the choice, it wins it.
func TestDetectKernelVersionSkipsLeftovers(t *testing.T) {
	pinRunningKernel(t, "")
	const real = "4.18.0-553.123.1.el8_10.x86_64"
	leftovers := []string{
		"4.18.0-477.10.1.el8_8.x86_64",
		"4.18.0-513.5.1.el8_9.x86_64",
		"4.18.0-553.115.1.el8_10.x86_64",
		"4.18.0-553.22.1.el8_10.x86_64",
		"4.18.0-553.33.1.el8_10.x86_64",
		"4.18.0-553.40.1.el8_10.x86_64",
		"4.18.0-553.51.1.el8_10.x86_64",
		"4.18.0-553.64.1.el8_10.x86_64",
		"4.18.0-553.71.1.el8_10.x86_64",
		"4.18.0-553.8.1.el8_10.x86_64",
		"4.18.0-553.97.1.el8_10.x86_64",
	}

	sysroot := t.TempDir()
	mods := filepath.Join(sysroot, "lib", "modules")
	if err := os.MkdirAll(filepath.Join(sysroot, "boot"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, d := range append([]string{real}, leftovers...) {
		if err := os.MkdirAll(filepath.Join(mods, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	// Only the installed kernel owns a kernel image.
	if err := os.WriteFile(filepath.Join(sysroot, "boot", "vmlinuz-"+real), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := detectKernelVersion(sysroot)
	if err != nil {
		t.Fatalf("detectKernelVersion: %v", err)
	}
	if got != real {
		t.Errorf("picked %q, want the only installed kernel %q", got, real)
	}
}

// TestDetectKernelVersionOrdersNumerically pins the ordering on its own, with no
// kernel images present at all, so it fails if the fallback path ever goes back to
// comparing version strings bytewise.
func TestDetectKernelVersionOrdersNumerically(t *testing.T) {
	pinRunningKernel(t, "")
	sysroot := t.TempDir()
	mods := filepath.Join(sysroot, "lib", "modules")
	if err := os.MkdirAll(mods, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{
		"4.18.0-553.97.1.el8_10.x86_64",
		"4.18.0-553.123.1.el8_10.x86_64",
		"4.18.0-553.8.1.el8_10.x86_64",
	} {
		if err := os.Mkdir(filepath.Join(mods, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	got, err := detectKernelVersion(sysroot)
	if err != nil {
		t.Fatalf("detectKernelVersion: %v", err)
	}
	if want := "4.18.0-553.123.1.el8_10.x86_64"; got != want {
		t.Errorf("picked %q, want %q", got, want)
	}
}

func TestCompareKernelVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"4.18.0-553.123.1.el8_10.x86_64", "4.18.0-553.97.1.el8_10.x86_64", 1},
		{"4.18.0-553.97.1.el8_10.x86_64", "4.18.0-553.8.1.el8_10.x86_64", 1},
		{"4.18.0-477.10.1.el8_8.x86_64", "4.18.0-553.123.1.el8_10.x86_64", -1},
		{"6.12.0-211.34.1.el10_2.x86_64", "6.12.0-100.el10_2.x86_64", 1},
		{"5.14.0-687.26.1.el9_8.x86_64", "5.14.0-687.26.1.el9_8.x86_64", 0},
		// leading zeros carry no value: 007 ranks as 7, so it beats 6
		{"4.18.0-553.007.1.el8_10.x86_64", "4.18.0-553.6.1.el8_10.x86_64", 1},
		// two names with identical numbers still order, so the choice is never
		// left to map or readdir order
		{"4.18.0-553.007.1.el8_10.x86_64", "4.18.0-553.7.1.el8_10.x86_64", -1},
		// the review finding: a z-stream respin adds a numeric run where the
		// GA build has "el"; rpm (numeric beats alpha) calls the respin newer
		{"4.18.0-553.el8_10.x86_64", "4.18.0-553.5.1.el8_10.x86_64", -1},
		{"4.18.0-553.5.1.el8_10.x86_64", "4.18.0-553.el8_10.x86_64", 1},
		{"5.14.0-503.el9.x86_64", "5.14.0-503.11.1.el9_5.x86_64", -1},
		{"5.14.0-427.13.1.el9_4.x86_64", "5.14.0-503.11.1.el9_5.x86_64", -1},
		{"5.14.0-70.13.1.el9_0.x86_64", "5.14.0-70.13.1.el9_0.x86_64+debug", -1},
		{"6.12.0-55.el10.x86_64", "6.12.0-55.9.1.el10_0.x86_64", -1},
		// the version decides before the release is looked at
		{"5.14.0-1.el9.x86_64", "4.18.0-553.123.1.el8_10.x86_64", 1},
		// Ubuntu: <version>-<abi>-<flavour>
		{"5.15.0-91-generic", "5.15.0-101-generic", -1},
		{"6.8.0-48-generic", "6.8.0-45-generic", 1},
		{"6.8.0-1015-aws", "6.8.0-1009-aws", 1},
		{"6.8.0-45-generic", "6.8.0-45-generic", 0},
	}
	for _, tc := range cases {
		if got := compareKernelVersions(tc.a, tc.b); got != tc.want {
			t.Errorf("compare(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// TestDetectKernelVersionPrefersRunningKernel pins the primary rule: Luna builds the
// node's boot kernel from the osimage, so the version the installer is running is that
// image's kernel. It is taken even when another installed tree carries a higher
// version, because the question is which kernel this image uses, not which is newest.
func TestDetectKernelVersionPrefersRunningKernel(t *testing.T) {
	const running = "4.18.0-553.97.1.el8_10.x86_64"
	pinRunningKernel(t, running)

	sysroot := t.TempDir()
	mods := filepath.Join(sysroot, "lib", "modules")
	if err := os.MkdirAll(filepath.Join(sysroot, "boot"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{running, "4.18.0-553.123.1.el8_10.x86_64"} {
		if err := os.MkdirAll(filepath.Join(mods, d), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sysroot, "boot", "vmlinuz-"+d), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	got, err := detectKernelVersion(sysroot)
	if err != nil {
		t.Fatalf("detectKernelVersion: %v", err)
	}
	if got != running {
		t.Errorf("picked %q, want the running kernel %q", got, running)
	}
}

// TestDetectKernelVersionIgnoresRunningKernelNotOnDisk covers install_mode=local,
// which writes a bootloader over an on-disk OS it did not lay down. The booted kernel
// then need not exist in the target root, so it must be checked rather than trusted,
// and the choice falls back to what is actually installed there.
func TestDetectKernelVersionIgnoresRunningKernelNotOnDisk(t *testing.T) {
	pinRunningKernel(t, "6.12.0-999.el10_2.x86_64")

	const onDisk = "4.18.0-553.123.1.el8_10.x86_64"
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules", onDisk), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "boot"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "boot", "vmlinuz-"+onDisk), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := detectKernelVersion(sysroot)
	if err != nil {
		t.Fatalf("detectKernelVersion: %v", err)
	}
	if got != onDisk {
		t.Errorf("picked %q, want the kernel actually on disk %q", got, onDisk)
	}
}

// TestDetectKernelVersionRunningKernelNeedsBothHalves: a version with a module tree
// but no kernel image, or an image with no module tree, is not a usable dracut target
// even when it is the running one.
func TestDetectKernelVersionRunningKernelNeedsBothHalves(t *testing.T) {
	const running = "4.18.0-553.97.1.el8_10.x86_64"
	const good = "4.18.0-553.123.1.el8_10.x86_64"
	pinRunningKernel(t, running)

	sysroot := t.TempDir()
	// running: module tree only, exactly the leftover shape seen in the field
	if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules", running, "extra"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules", good), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sysroot, "boot"), 0o750); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sysroot, "boot", "vmlinuz-"+good), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}

	got, err := detectKernelVersion(sysroot)
	if err != nil {
		t.Fatalf("detectKernelVersion: %v", err)
	}
	if got != good {
		t.Errorf("picked %q, want %q", got, good)
	}
}

// TestKernelNotNewestWarning pins when the operator gets told that the boot loader
// may start a different kernel from the one the initramfs was built for.
//
// The case that matters is a pinned kernel: setting an older kernelversion on the
// osimage is a supported thing to do, and the node then netboots that kernel and the
// initramfs is rebuilt for it -- but the boot loader orders its entries by version and
// may start a newer one, whose initramfs still carries the luna module. Silence is
// just as important: warning on every ordinary multi-kernel image would train people
// to ignore it.
func TestKernelNotNewestWarning(t *testing.T) {
	const older = "4.18.0-553.97.1.el8_10.x86_64"
	const newer = "4.18.0-553.123.1.el8_10.x86_64"

	// build makes a sysroot where every name in bootable has a kernel image and
	// every name in modulesOnly has a module directory but no kernel.
	build := func(t *testing.T, bootable, modulesOnly []string) string {
		t.Helper()
		sysroot := t.TempDir()
		if err := os.MkdirAll(filepath.Join(sysroot, "boot"), 0o750); err != nil {
			t.Fatal(err)
		}
		for _, k := range append(append([]string{}, bootable...), modulesOnly...) {
			if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules", k), 0o750); err != nil {
				t.Fatal(err)
			}
		}
		for _, k := range bootable {
			if err := os.WriteFile(filepath.Join(sysroot, "boot", "vmlinuz-"+k), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		return sysroot
	}

	t.Run("pinned to an older kernel -> warn, naming both", func(t *testing.T) {
		got := kernelNotNewestWarning(build(t, []string{older, newer}, nil), older)
		if got == "" {
			t.Fatal("expected a warning when the target is not the newest installed kernel")
		}
		for _, want := range []string{older, newer} {
			if !strings.Contains(got, want) {
				t.Errorf("warning does not name %q: %s", want, got)
			}
		}
	})

	t.Run("target is the newest -> silent", func(t *testing.T) {
		if got := kernelNotNewestWarning(build(t, []string{older, newer}, nil), newer); got != "" {
			t.Errorf("expected silence, got: %s", got)
		}
	})

	t.Run("single kernel -> silent", func(t *testing.T) {
		if got := kernelNotNewestWarning(build(t, []string{newer}, nil), newer); got != "" {
			t.Errorf("expected silence, got: %s", got)
		}
	})

	// The field image: eleven leftovers and one real kernel. Leftovers are not
	// installed kernels, so they must not provoke a warning.
	t.Run("leftover directories are not installed kernels -> silent", func(t *testing.T) {
		leftovers := []string{older, "4.18.0-553.8.1.el8_10.x86_64", "4.18.0-477.10.1.el8_8.x86_64"}
		if got := kernelNotNewestWarning(build(t, []string{newer}, leftovers), newer); got != "" {
			t.Errorf("expected silence, got: %s", got)
		}
	})

	// el10 ships a debug tree beside the real kernel; comparing within the same
	// flavour keeps that ordinary case quiet.
	t.Run("debug kernel alongside the real one -> silent", func(t *testing.T) {
		const real = "6.12.0-211.34.1.el10_2.x86_64"
		sysroot := build(t, []string{real, real + "+debug"}, nil)
		if got := kernelNotNewestWarning(sysroot, real); got != "" {
			t.Errorf("expected silence, got: %s", got)
		}
	})
}

// TestRPMVerCmp pins the port against rpm's own test vectors
// (tests/rpmvercmp.at), including the tilde and caret markers.
func TestRPMVerCmp(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"1.0", "1.0", 0}, {"1.0", "2.0", -1}, {"2.0", "1.0", 1},
		{"2.0.1", "2.0.1", 0}, {"2.0", "2.0.1", -1}, {"2.0.1", "2.0", 1},
		{"2.0.1a", "2.0.1a", 0}, {"2.0.1a", "2.0.1", 1}, {"2.0.1", "2.0.1a", -1},
		{"5.5p1", "5.5p1", 0}, {"5.5p1", "5.5p2", -1}, {"5.5p2", "5.5p1", 1},
		{"5.5p10", "5.5p10", 0}, {"5.5p1", "5.5p10", -1}, {"5.5p10", "5.5p1", 1},
		{"10xyz", "10.1xyz", -1}, {"10.1xyz", "10xyz", 1},
		{"xyz10", "xyz10", 0}, {"xyz10", "xyz10.1", -1}, {"xyz10.1", "xyz10", 1},
		{"xyz.4", "xyz.4", 0}, {"xyz.4", "8", -1}, {"8", "xyz.4", 1},
		{"xyz.4", "2", -1}, {"2", "xyz.4", 1},
		{"5.5p2", "5.6p1", -1}, {"5.6p1", "5.5p2", 1},
		{"5.6p1", "6.5p1", -1}, {"6.5p1", "5.6p1", 1},
		{"6.0.rc1", "6.0", 1}, {"6.0", "6.0.rc1", -1},
		{"10b2", "10a1", 1}, {"10a2", "10b2", -1},
		{"1.0aa", "1.0aa", 0}, {"1.0a", "1.0aa", -1}, {"1.0aa", "1.0a", 1},
		{"10.0001", "10.0001", 0}, {"10.0001", "10.1", 0}, {"10.1", "10.0001", 0},
		{"10.0001", "10.0039", -1}, {"10.0039", "10.0001", 1},
		{"4.999.9", "5.0", -1}, {"5.0", "4.999.9", 1},
		{"20101121", "20101121", 0}, {"20101121", "20101122", -1}, {"20101122", "20101121", 1},
		{"2_0", "2_0", 0}, {"2.0", "2_0", 0}, {"2_0", "2.0", 0},
		{"a", "a", 0}, {"a+", "a+", 0}, {"a+", "a_", 0}, {"a_", "a+", 0},
		{"+a", "+a", 0}, {"+a", "_a", 0}, {"_a", "+a", 0},
		{"+_", "+_", 0}, {"_+", "+_", 0}, {"_+", "_+", 0}, {"+", "_", 0}, {"_", "+", 0},
		{"1b.fc17", "1b.fc17", 0}, {"1b.fc17", "1.fc17", -1}, {"1.fc17", "1b.fc17", 1},
		{"1g.fc17", "1g.fc17", 0}, {"1g.fc17", "1.fc17", 1}, {"1.fc17", "1g.fc17", -1},
		{"1.0~rc1", "1.0~rc1", 0}, {"1.0~rc1", "1.0", -1}, {"1.0", "1.0~rc1", 1},
		{"1.0~rc1", "1.0~rc2", -1}, {"1.0~rc2", "1.0~rc1", 1},
		{"1.0~rc1~git123", "1.0~rc1~git123", 0}, {"1.0~rc1~git123", "1.0~rc1", -1},
		{"1.0~rc1", "1.0~rc1~git123", 1},
		{"1.0^", "1.0^", 0}, {"1.0^", "1.0", 1}, {"1.0", "1.0^", -1},
		{"1.0^git1", "1.0^git1", 0}, {"1.0^git1", "1.0", 1}, {"1.0", "1.0^git1", -1},
		{"1.0^git1", "1.0^git2", -1}, {"1.0^git2", "1.0^git1", 1},
		{"1.0^git1", "1.01", -1}, {"1.01", "1.0^git1", 1},
		{"1.0^20160101", "1.0^20160101", 0}, {"1.0^20160101", "1.0.1", -1}, {"1.0.1", "1.0^20160101", 1},
		{"1.0^20160101^git1", "1.0^20160101^git1", 0},
		{"1.0^20160102", "1.0^20160101^git1", 1}, {"1.0^20160101^git1", "1.0^20160102", -1},
		{"1.0~rc1^git1", "1.0~rc1^git1", 0}, {"1.0~rc1^git1", "1.0~rc1", 1}, {"1.0~rc1", "1.0~rc1^git1", -1},
		{"1.0^git1~pre", "1.0^git1~pre", 0}, {"1.0^git1", "1.0^git1~pre", 1}, {"1.0^git1~pre", "1.0^git1", -1},
	}
	for _, tc := range cases {
		if got := rpmvercmp(tc.a, tc.b); got != tc.want {
			t.Errorf("rpmvercmp(%q, %q) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// The GA kernel and its z-stream respin side by side, both installed: the
// respin is the newer one, so it is the one picked and the one the warning
// compares against.
func TestDetectKernelVersionPrefersZStreamOverGA(t *testing.T) {
	pinRunningKernel(t, "")
	const ga, respin = "4.18.0-553.el8_10.x86_64", "4.18.0-553.5.1.el8_10.x86_64"
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "boot"), 0o750); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{ga, respin} {
		if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules", k), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(sysroot, "boot", "vmlinuz-"+k), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	got, err := detectKernelVersion(sysroot)
	if err != nil {
		t.Fatal(err)
	}
	if got != respin {
		t.Errorf("picked %q, want the z-stream respin %q", got, respin)
	}
	if msg := kernelNotNewestWarning(sysroot, ga); !strings.Contains(msg, respin) {
		t.Errorf("GA target must warn about the newer respin; got %q", msg)
	}
	if msg := kernelNotNewestWarning(sysroot, respin); msg != "" {
		t.Errorf("respin is the newest; expected silence, got %q", msg)
	}
}
