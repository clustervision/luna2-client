package bootloader

import (
	"os"
	"path/filepath"
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
