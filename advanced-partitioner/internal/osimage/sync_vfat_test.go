package osimage

import (
	"archive/tar"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Found on the lab node (UEFI, sync with verify=content): every member on
// the ESP failed verification -- "mode 755, image 700" and a 1-second mtime
// difference -- because vfat stores no Unix mode/owner and keeps mtimes in
// 2-second steps, so the healthy node parked. On a vfat filesystem only
// type, size, mtime (2 s) and content are compared; content is still exact.
func TestVerifyIgnoresModeOwnerOnVfat(t *testing.T) {
	root := t.TempDir()
	esp := filepath.Join(root, "boot", "efi", "EFI", "BOOT")
	etc := filepath.Join(root, "etc")
	for _, d := range []string{esp, etc} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	body := []byte("loader")
	sum := sha256.Sum256(body)
	img := time.Date(2026, 8, 3, 16, 30, 21, 0, time.UTC)
	write := func(p string, mode os.FileMode, mt time.Time) {
		if err := os.WriteFile(p, body, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(p, mode); err != nil {
			t.Fatal(err)
		}
		if err := os.Chtimes(p, mt, mt); err != nil {
			t.Fatal(err)
		}
	}
	// what vfat makes of it: mode from the mount options, mtime rounded down
	write(filepath.Join(esp, "BOOTX64.EFI"), 0o755, img.Add(-time.Second))
	// the same differences on a POSIX filesystem must still be reported
	write(filepath.Join(etc, "conf"), 0o755, img.Add(-time.Second))

	orig := statfsType
	statfsType = func(p string) (int64, error) {
		if strings.Contains(p, "/boot/efi") {
			return 0x4d44, nil
		}
		return 0xef53, nil
	}
	t.Cleanup(func() { statfsType = orig })

	uid, gid := os.Getuid(), os.Getgid()
	mem := func(mode int64) memberInfo {
		return memberInfo{typ: tar.TypeReg, mode: mode, uid: uid, gid: gid, size: int64(len(body)), mtime: img, sum: hex.EncodeToString(sum[:])}
	}
	o := SyncOptions{Sysroot: root}

	// Separate devices are needed for the per-device cache: the temp dir is one
	// filesystem, so check each file on its own.
	_, bad := o.verify(map[string]memberInfo{"boot/efi/EFI/BOOT/BOOTX64.EFI": mem(0o700)})
	if len(bad) != 0 {
		t.Fatalf("vfat member flagged for mode/mtime it cannot store: %v", bad)
	}
	_, bad = o.verify(map[string]memberInfo{"etc/conf": mem(0o700)})
	if len(bad) != 2 {
		t.Fatalf("POSIX member: want mode and mtime mismatches, got %v", bad)
	}

	// content stays exact on vfat
	if err := os.WriteFile(filepath.Join(esp, "BOOTX64.EFI"), []byte("LOADER"), 0o755); err != nil {
		t.Fatal(err)
	}
	_ = os.Chtimes(filepath.Join(esp, "BOOTX64.EFI"), img, img)
	_, bad = o.verify(map[string]memberInfo{"boot/efi/EFI/BOOT/BOOTX64.EFI": mem(0o700)})
	if len(bad) != 1 || !strings.Contains(bad[0], "content differs") {
		t.Fatalf("changed content on vfat must still be reported, got %v", bad)
	}
}
