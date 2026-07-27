package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

const sampleEFIOutput = `BootCurrent: 0001
Timeout: 0 seconds
BootOrder: 0001,0002,0003,0010
Boot0001* TrinityX sda
Boot0002* TrinityX sdb
Boot0003  Linux Boot Manager
Boot0010* UEFI: PXE IP4 Intel(R) Ethernet
Boot0011* UEFI: Network Boot
Boot0012  iPXE
`

func TestParseClearableBootNumsSkipsPXE(t *testing.T) {
	got := parseClearableBootNums(sampleEFIOutput)
	// 0001/0002 (TrinityX), 0003 (Linux Boot Manager) — none in
	// PXE/Network/Luna/iPXE allow list. 0010/0011/0012 stay.
	want := []string{"0001", "0002", "0003"}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestParseClearableBootNumsKeepsLunaToo(t *testing.T) {
	body := "Boot0099* Luna PXE\nBoot00FF  Anything else\n"
	got := parseClearableBootNums(body)
	if !equalSlices(got, []string{"00FF"}) {
		t.Fatalf("Luna should stay; got %v", got)
	}
}

func TestTrinityNumsFromOutput(t *testing.T) {
	got := trinityNumsFromOutput(sampleEFIOutput)
	want := []string{"0001", "0002"}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestBootOrderFromOutput(t *testing.T) {
	order, bn := bootOrderFromOutput(sampleEFIOutput)
	if !equalSlices(order, []string{"0001", "0002", "0003", "0010"}) {
		t.Fatalf("order = %v", order)
	}
	if bn != "" {
		t.Fatalf("BootNext = %q; want empty", bn)
	}
}

func TestBootOrderWithBootNext(t *testing.T) {
	body := "BootOrder: 0001,0002\nBootNext: 0002\n"
	order, bn := bootOrderFromOutput(body)
	if !equalSlices(order, []string{"0001", "0002"}) {
		t.Fatalf("order = %v", order)
	}
	if bn != "0002" {
		t.Fatalf("BootNext = %q", bn)
	}
}

func TestBootEntryRESupportsActiveStar(t *testing.T) {
	m := bootEntryRE.FindStringSubmatch("Boot0001* TrinityX sda")
	if m == nil || m[1] != "0001" || !strings.Contains(m[2], "TrinityX") {
		t.Fatalf("active-star line not parsed: %v", m)
	}
}

func TestBootEntryREHandlesNoStar(t *testing.T) {
	m := bootEntryRE.FindStringSubmatch("Boot0003  Linux Boot Manager")
	if m == nil || m[1] != "0003" || !strings.Contains(m[2], "Linux") {
		t.Fatalf("no-star line not parsed: %v", m)
	}
}

func equalSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] { //nolint:gosec // G602 false positive: i < len(a) == len(b) (checked above)
			return false
		}
	}
	return true
}

// efibootmgr lives in /usr/sbin on RHEL but /usr/bin on Ubuntu; the resolver
// must return whichever exists (HW regression: ubuntu disk install exit 127).
func TestEfibootmgrChrootPathResolvesPerDistro(t *testing.T) {
	mk := func(rel string) string {
		root := t.TempDir()
		p := filepath.Join(root, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o750); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
		return root
	}
	if got := efibootmgrChrootPath(mk("usr/sbin/efibootmgr")); got != "/usr/sbin/efibootmgr" {
		t.Fatalf("rhel layout: got %q", got)
	}
	if got := efibootmgrChrootPath(mk("usr/bin/efibootmgr")); got != "/usr/bin/efibootmgr" {
		t.Fatalf("ubuntu layout: got %q", got)
	}
	if got := efibootmgrChrootPath(t.TempDir()); got != "" {
		t.Fatalf("absent: got %q, want empty", got)
	}
}

// BE-W6: the stale-entry match must catch EVERY TrinityX-labelled entry —
// the exact "TrinityX <disk>" form, duplicates of the same label, and
// renamed/re-formatted variants — or a leftover entry keeps an obsolete
// boot path alive in NVRAM. The match is label-contains-BootloaderID,
// which subsumes the prefix requirement.
func TestTrinityNumsMatchesDuplicatesAndVariants(t *testing.T) {
	body := `BootOrder: 0001,0002,0003,0004,0010
Boot0001* TrinityX sda
Boot0002* TrinityX sda
Boot0003  TrinityX-old (sdb)
Boot0004* TrinityX
Boot0010* UEFI: PXE IP4 Intel(R) Ethernet
`
	got := trinityNumsFromOutput(body)
	want := []string{"0001", "0002", "0003", "0004"}
	if !equalSlices(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

// TestRegisterUEFIEntriesDeletesAllStaleTrinityX drives the full
// registration flow (BE-W6) and asserts the stale-delete step issues an
// efibootmgr --delete-bootnum for every TrinityX-labelled entry the
// listing reports — including a duplicate label and a suffix variant —
// while leaving the PXE entry alone.
func TestRegisterUEFIEntriesDeletesAllStaleTrinityX(t *testing.T) {
	sysroot := t.TempDir()
	for _, d := range []string{"usr/sbin", "sys/firmware/efi/efivars"} {
		if err := os.MkdirAll(filepath.Join(sysroot, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sysroot, "usr/sbin/efibootmgr"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	listing := `BootOrder: 0007,0001,0002,0003,0004,0010
Boot0001* TrinityX sda
Boot0002* TrinityX sda
Boot0003  TrinityX-old (sdb)
Boot0004* TrinityX
Boot0007* TrinityX sdb
Boot0010* UEFI: PXE IP4 Intel(R) Ethernet
`
	f := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(listing)})
	if err := RegisterUEFIEntries(context.Background(), RegisterUEFIEntriesOptions{
		Runner:        f,
		Sysroot:       sysroot,
		Arch:          "x86_64",
		BootDevices:   []string{"/dev/sda", "/dev/sdb"},
		RegisterNVRAM: "auto",
	}); err != nil {
		t.Fatalf("RegisterUEFIEntries: %v", err)
	}
	deleted := map[string]bool{}
	for _, c := range f.Calls() {
		if len(c.Args) == 3 && c.Args[0] == "--bootnum" && c.Args[2] == "--delete-bootnum" {
			deleted[c.Args[1]] = true
		}
	}
	for _, num := range []string{"0001", "0002", "0003", "0004", "0007"} {
		if !deleted[num] {
			t.Fatalf("stale TrinityX entry Boot%s not deleted; deleted=%v", num, deleted)
		}
	}
	if deleted["0010"] {
		t.Fatal("PXE entry Boot0010 must never be deleted by the stale-entry sweep")
	}
}
