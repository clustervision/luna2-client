package cleanup

import "testing"

// On initramfs-tools (Ubuntu) the install root is $rootmnt (/root). Cleanup
// used a fixed /sysroot, so it never unmounted leftovers under /root.
func TestNewDefaultsToRootmnt(t *testing.T) {
	t.Setenv("rootmnt", "/root")
	if got := New(Options{}).o.Sysroot; got != "/root" {
		t.Fatalf("Sysroot = %q with rootmnt=/root, want /root", got)
	}
	t.Setenv("rootmnt", "")
	if got := New(Options{}).o.Sysroot; got != "/sysroot" {
		t.Fatalf("Sysroot = %q without rootmnt, want /sysroot", got)
	}
}
