package bootloader

import (
	"strings"
	"testing"
)

// The installed system's initramfs must declare the storage stacks lpart can
// create and stay generic; without lvm/dm/mdraid an LVM or md root cannot
// assemble and the node stops in the dracut emergency shell.
func TestDracutArgsDeclareStorageModules(t *testing.T) {
	joined := strings.Join(dracutArgs("5.14.0-570.58.1.el9_6.x86_64"), " ")
	for _, want := range []string{
		"--omit luna",
		"--add lvm mdraid dm",
		"--no-hostonly",
		"--no-hostonly-cmdline",
	} {
		if !strings.Contains(joined, want) {
			t.Fatalf("%q missing: %s", want, joined)
		}
	}
	if !strings.HasSuffix(joined, "--kver 5.14.0-570.58.1.el9_6.x86_64") {
		t.Fatalf("--kver must carry the version last: %s", joined)
	}
}
