package main

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"
)

// Found on the Ubuntu 22.04 lab node: install_mode=local parked with
// "selected disk /dev/vda ... is mounted at /root/boot/efi - refusing",
// because storage-check unmounted the stale tree under a fixed /sysroot while
// initramfs-tools mounts it under $rootmnt (/root).
func TestDefaultSysrootFollowsRootmnt(t *testing.T) {
	t.Setenv("rootmnt", "/root")
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stderr
	os.Stderr = w
	var out, errb bytes.Buffer
	_ = run([]string{toolName, "--help"}, &out, &errb)
	os.Stderr = orig
	_ = w.Close()
	help, _ := io.ReadAll(r)
	all := out.String() + errb.String() + string(help)
	if !strings.Contains(all, `(default "/root")`) {
		t.Fatalf("--sysroot default does not follow rootmnt:\n%s", all)
	}
}
