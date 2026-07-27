package bootloader

import (
	"os"
	"path/filepath"
	"testing"
)

// TestDetectKernelVersion pins the el10 debug-kernel behaviour.
//
// Regression: el10 installs kernel-debug-core + kernel-debug-modules* alongside the
// real kernel, so the installed rootfs holds BOTH <ver>.x86_64 and <ver>.x86_64+debug.
// detectKernelVersion used "last wins" over os.ReadDir (which returns sorted names), so
// the DEBUG tree won and bootloader-finalise ran `dracut --kver <...+debug>`, which
// exits 1 -> the node install dies at postscript and luna retries forever. rocky8/rocky9
// ship one kernel each, which is why only el10 ever showed it.
func TestDetectKernelVersion(t *testing.T) {
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
	sysroot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(sysroot, "lib", "modules"), 0o750); err != nil {
		t.Fatal(err)
	}
	if _, err := detectKernelVersion(sysroot); err == nil {
		t.Error("expected an error when lib/modules is empty, got nil")
	}
}
