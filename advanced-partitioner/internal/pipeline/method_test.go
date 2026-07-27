package pipeline

import (
	"os"
	"path/filepath"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
)

// TestConfigLayoutMethod locks in the terse Storage-panel Method line: it says
// only which selection the OS uses (manual/discover/mixed) and whether the node
// boots local (a disk OS was laid down) or PXE (the OS runs from RAM). The
// per-set raid/disk detail lives in the disk table, not this line.
func TestConfigLayoutMethod(t *testing.T) {
	write := func(t *testing.T, body string) string {
		t.Helper()
		p := filepath.Join(t.TempDir(), "storage.json")
		if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}

	cases := []struct {
		name, cfg string
		mode      installcontext.Mode
		want      string
	}{
		{
			name: "manual disk -> local",
			mode: installcontext.ModeAuto,
			cfg: `{"version":2,"sets":[{"name":"os","role":"os","selection":"manual","raid":"1",
				"devices":["/dev/disk/by-path/a","/dev/disk/by-path/b"],
				"volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},
				{"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			want: "manual · next boot: local",
		},
		{
			name: "mixed selection -> local",
			mode: installcontext.ModeAuto,
			cfg: `{"version":2,"sets":[
				{"name":"os","role":"os","selection":"manual","raid":"1","devices":["/dev/disk/by-path/a","/dev/disk/by-path/b"],
				 "volumes":[{"name":"uefi","mountpoint":"/boot/efi","fs":"vfat","provider":"partition","size":"600M"},
				 {"name":"root","mountpoint":"/","fs":"xfs","provider":"lvm","size":"100%"}]},
				{"name":"scratch","role":"data","selection":"discover","raid":"0","match":{"tags":["nvme"]},
				 "volumes":[{"name":"scratch","mountpoint":"/scratch","fs":"xfs","provider":"lvm","size":"100%"}]}]}`,
			want: "mixed · next boot: local",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			info, ok := configLayout(write(t, tc.cfg), tc.mode)
			if !ok {
				t.Fatal("expected ok")
			}
			if info.Method != tc.want {
				t.Errorf("Method = %q, want %q", info.Method, tc.want)
			}
		})
	}

	t.Run("memboot -> diskless / PXE", func(t *testing.T) {
		info, ok := configLayout(filepath.Join(t.TempDir(), "absent.json"), installcontext.ModeMemboot)
		if !ok || !info.MemOS {
			t.Fatalf("memboot: ok=%v memOS=%v", ok, info.MemOS)
		}
		if info.Method != "diskless (RAM) · next boot: PXE" {
			t.Errorf("Method = %q", info.Method)
		}
	})

	t.Run("auto with no config -> diskless / PXE", func(t *testing.T) {
		info, ok := configLayout(filepath.Join(t.TempDir(), "absent.json"), installcontext.ModeAuto)
		if !ok || !info.MemOS {
			t.Fatalf("auto-empty: ok=%v memOS=%v", ok, info.MemOS)
		}
	})
}
