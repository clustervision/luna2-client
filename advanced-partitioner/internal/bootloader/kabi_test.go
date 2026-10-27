package bootloader

import (
	"os"
	"path/filepath"
	"testing"
)

// kabiRoot builds a RHEL 9 sysroot with kernel-abi-stablelists installed:
// /lib/modules/kabi-rhel90 .. kabi-rhel98 plus the kabi-current link, next
// to one real kernel. withImage controls whether the kernel's vmlinuz is
// there (both /boot/vmlinuz-<v> and /lib/modules/<v>/vmlinuz).
func kabiRoot(t *testing.T, kver string, withImage bool) string {
	t.Helper()
	root := t.TempDir()
	mods := filepath.Join(root, "lib", "modules")
	for _, n := range []string{"kabi-rhel90", "kabi-rhel94", "kabi-rhel97", "kabi-rhel98", kver} {
		if err := os.MkdirAll(filepath.Join(mods, n), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("kabi-rhel98", filepath.Join(mods, "kabi-current")); err != nil {
		t.Fatal(err)
	}
	if withImage {
		_ = os.MkdirAll(filepath.Join(root, "boot"), 0o755)
		for _, p := range []string{filepath.Join(root, "boot", "vmlinuz-"+kver), filepath.Join(mods, kver, "vmlinuz")} {
			if err := os.WriteFile(p, []byte("k"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	return root
}

func noRunningKernel(t *testing.T) {
	t.Helper()
	orig := runningKernelVersion
	runningKernelVersion = func() string { return "" }
	t.Cleanup(func() { runningKernelVersion = orig })
}

// Ticket: "lpart post phase selects unowned kabi-rhel97 pseudo-module tree
// instead of a bootable kernel, causing UEFI/dracut finalisation failure".
func TestKabiTreesAreNeverAKernel(t *testing.T) {
	noRunningKernel(t)
	const kver = "5.14.0-611.5.1.el9_7.x86_64"

	root := kabiRoot(t, kver, true)
	if got, err := detectKernelVersion(root); err != nil || got != kver {
		t.Fatalf("detectKernelVersion = %q, %v; want %s", got, err, kver)
	}
	if got := bootableKernels(root); len(got) != 1 || got[0] != kver {
		t.Fatalf("bootableKernels = %v; want only %s", got, kver)
	}

	// No kernel image anywhere (the fallback path): still the real release,
	// never a kabi tree.
	root = kabiRoot(t, kver, false)
	if got, err := detectKernelVersion(root); err != nil || got != kver {
		t.Fatalf("fallback detectKernelVersion = %q, %v; want %s", got, err, kver)
	}
}
