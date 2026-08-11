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

// TestRegisterUEFIEntriesOrdersTrinityAfterNetwork pins the boot-order policy:
// the local disk entries must land AFTER the network entry, so a provisioned
// node keeps asking the controller how to boot instead of booting itself for
// ever. Registration used to promote them to the front, which took that
// decision away from Luna and meant a re-provision needed someone at the BMC.
//
// The assertion is the relative order rather than a literal BootOrder string:
// what matters is "network before local", not the particular numbering the
// fixture happens to use.
func TestRegisterUEFIEntriesOrdersTrinityAfterNetwork(t *testing.T) {
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

	var order []string
	for _, c := range f.Calls() {
		if len(c.Args) == 2 && c.Args[0] == "--bootorder" {
			order = strings.Split(c.Args[1], ",")
		}
	}
	if order == nil {
		t.Fatal("registration never issued efibootmgr --bootorder")
	}
	pos := map[string]int{}
	for i, n := range order {
		pos[n] = i
	}
	pxe, ok := pos["0010"]
	if !ok {
		t.Fatalf("network entry Boot0010 dropped from BootOrder: %v", order)
	}
	for _, num := range []string{"0001", "0002", "0003", "0004", "0007"} {
		p, ok := pos[num]
		if !ok {
			t.Fatalf("TrinityX entry Boot%s missing from BootOrder %v", num, order)
		}
		if p < pxe {
			t.Fatalf("TrinityX Boot%s precedes the network entry (index %d < %d): %v -- "+
				"the node would stop netbooting after provisioning", num, p, pxe, order)
		}
	}
}

// mkEFISysroot builds the minimum sysroot RegisterUEFIEntries needs: an
// efibootmgr to find and an efivars dir to prove firmware is UEFI.
func mkEFISysroot(t *testing.T) string {
	t.Helper()
	sysroot := t.TempDir()
	for _, d := range []string{"usr/sbin", "sys/firmware/efi/efivars"} {
		if err := os.MkdirAll(filepath.Join(sysroot, d), 0o750); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(sysroot, "usr/sbin/efibootmgr"), []byte{}, 0o600); err != nil {
		t.Fatal(err)
	}
	return sysroot
}

// bootOrderWritten returns the boot-nums passed to `efibootmgr --bootorder`.
func bootOrderWritten(t *testing.T, f *executor.FakeRunner) []string {
	t.Helper()
	for _, c := range f.Calls() {
		if len(c.Args) == 2 && c.Args[0] == "--bootorder" {
			return strings.Split(c.Args[1], ",")
		}
	}
	t.Fatal("registration never issued efibootmgr --bootorder")
	return nil
}

// TestRegisterUEFIEntriesOrderingOnRealFirmware runs the reorder against boot
// listings shaped like the machines this actually ships to, rather than one
// tidy fixture. The vendors differ in how many network entries they expose
// (dual-stack, per-port), what else is bootable, and whether a shell or setup
// entry exists at all.
//
// Two invariants, and they are not the same strength:
//
//   - SAFETY: nothing that fails to fall through may precede our entry. A shell
//     or setup entry SUCCEEDS, so a node that reaches one stops there.
//   - PREFERENCE: network entries come first, so the controller decides before
//     any local media does.
//
// Note the safety invariant survives imperfect network detection: an unmatched
// NIC label lands in the device class, which still precedes our entry. Only a
// misclassification INTO the trailing class could hurt, which is why that regex
// is the conservative one.
func TestRegisterUEFIEntriesOrderingOnRealFirmware(t *testing.T) {
	cases := []struct {
		name     string
		listing  string
		trinity  []string
		network  []string
		trailing []string
	}{
		{
			name: "vmware/one nic, shell sits above the network entry",
			listing: `BootOrder: 0003,0000,0001,0002,0005
Boot0000* EFI VMware Virtual SATA CDROM Drive (0.0)
Boot0001* EFI Internal Shell (Unsupported option)
Boot0002* EFI Network
Boot0003* TrinityX sda
Boot0005* EFI Virtual disk (0.0)
`,
			trinity: []string{"0003"}, network: []string{"0002"}, trailing: []string{"0001"},
		},
		{
			name: "dell/dual-stack pxe, optical, boot menu and setup",
			listing: `BootOrder: 0000,0001,0002,0003,0004,0005
Boot0000* Integrated NIC 1 Port 1 Partition 1: IPv4 PXE
Boot0001* Integrated NIC 1 Port 1 Partition 1: IPv6 PXE
Boot0002* TrinityX sda
Boot0003* Optical Drive 1
Boot0004* UEFI Boot Menu
Boot0005* BIOS Setup
`,
			trinity: []string{"0002"}, network: []string{"0000", "0001"}, trailing: []string{"0004", "0005"},
		},
		{
			name: "raid mirror/two disks, stale foreign boot manager",
			listing: `BootOrder: 0010,0011,0012,0013,0014
Boot0010* UEFI: PXE IP4 Intel(R) Ethernet
Boot0011* TrinityX sda
Boot0012* TrinityX sdb
Boot0013* Windows Boot Manager
Boot0014* Embedded UEFI Shell
`,
			trinity: []string{"0011", "0012"}, network: []string{"0010"}, trailing: []string{"0013", "0014"},
		},
		{
			name: "no shell or menu at all -- ours must simply end up last",
			listing: `BootOrder: 0001,0002,0003
Boot0001* UEFI: PXE IP4
Boot0002* TrinityX nvme0n1
Boot0003* UEFI: USB Device
`,
			trinity: []string{"0002"}, network: []string{"0001"}, trailing: nil,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			sysroot := mkEFISysroot(t)
			f := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(tc.listing)})
			if err := RegisterUEFIEntries(context.Background(), RegisterUEFIEntriesOptions{
				Runner: f, Sysroot: sysroot, Arch: "x86_64",
				BootDevices: []string{"/dev/sda"}, RegisterNVRAM: "auto",
			}); err != nil {
				t.Fatalf("RegisterUEFIEntries: %v", err)
			}
			order := bootOrderWritten(t, f)

			// Nothing may be dropped or duplicated by the reorder.
			seen := map[string]int{}
			for _, n := range order {
				seen[n]++
			}
			for _, n := range strings.Split(strings.TrimPrefix(
				strings.SplitN(tc.listing, "\n", 2)[0], "BootOrder: "), ",") {
				if seen[n] != 1 {
					t.Fatalf("entry %s appears %d times in the rewritten order %v", n, seen[n], order)
				}
			}

			pos := map[string]int{}
			for i, n := range order {
				pos[n] = i
			}
			for _, tn := range tc.trinity {
				// SAFETY: every trailing entry must come after ours.
				for _, bad := range tc.trailing {
					if pos[bad] < pos[tn] {
						t.Fatalf("trailing entry %s precedes TrinityX %s in %v -- "+
							"a shell or menu there strands the node", bad, tn, order)
					}
				}
				// PREFERENCE: every network entry must come before ours.
				for _, net := range tc.network {
					if pos[net] > pos[tn] {
						t.Fatalf("network entry %s follows TrinityX %s in %v -- "+
							"the node would stop asking the controller", net, tn, order)
					}
				}
			}
		})
	}
}

// TestRegisterUEFIEntriesRegistersSignedShim pins WHICH loader goes into the
// NVRAM entry. Registering the removable path (\EFI\BOOT\BOOTX64.EFI) means
// booting it runs shim's fallback.efi, which reads the ESP's CSV, creates a
// SECOND TrinityX entry for the real shim and promotes it to the front of
// BootOrder -- observed on a node, where one local boot turned a single entry
// into two and undid the ordering. Pointing straight at the shim avoids both,
// and is also the binary Secure Boot expects to verify.
func TestRegisterUEFIEntriesRegistersSignedShim(t *testing.T) {
	sysroot := mkEFISysroot(t)
	shimDir := filepath.Join(sysroot, "boot", "efi", "EFI", "rocky")
	if err := os.MkdirAll(shimDir, 0o750); err != nil {
		t.Fatal(err)
	}
	for _, f := range []string{"shimx64.efi", "grubx64.efi"} {
		if err := os.WriteFile(filepath.Join(shimDir, f), []byte{}, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	f := executor.NewFakeRunner(executor.FakeResponse{
		Stdout: []byte("BootOrder: 0010\nBoot0010* UEFI: PXE IP4\n")})
	if err := RegisterUEFIEntries(context.Background(), RegisterUEFIEntriesOptions{
		Runner: f, Sysroot: sysroot, Arch: "x86_64",
		BootDevices: []string{"/dev/sda"}, RegisterNVRAM: "auto",
	}); err != nil {
		t.Fatalf("RegisterUEFIEntries: %v", err)
	}
	loader := createdLoader(t, f)
	if loader != `\EFI\rocky\shimx64.efi` {
		t.Fatalf("registered loader is %q, want the signed shim -- the removable path "+
			"lets fallback.efi add a competing entry ahead of ours", loader)
	}
}

// TestRegisterUEFIEntriesFallsBackWhenNoShim keeps the old behaviour for an
// image that ships no signed shim: a removable-path entry is worse than the
// shim, but far better than no bootable entry at all.
func TestRegisterUEFIEntriesFallsBackWhenNoShim(t *testing.T) {
	sysroot := mkEFISysroot(t)
	f := executor.NewFakeRunner(executor.FakeResponse{
		Stdout: []byte("BootOrder: 0010\nBoot0010* UEFI: PXE IP4\n")})
	if err := RegisterUEFIEntries(context.Background(), RegisterUEFIEntriesOptions{
		Runner: f, Sysroot: sysroot, Arch: "x86_64",
		BootDevices: []string{"/dev/sda"}, RegisterNVRAM: "auto",
	}); err != nil {
		t.Fatalf("RegisterUEFIEntries: %v", err)
	}
	if loader := createdLoader(t, f); loader != `\EFI\BOOT\BOOTX64.EFI` {
		t.Fatalf("registered loader is %q, want the removable fallback", loader)
	}
}

// createdLoader returns the --loader argument of the efibootmgr --create call.
func createdLoader(t *testing.T, f *executor.FakeRunner) string {
	t.Helper()
	for _, c := range f.Calls() {
		for i, a := range c.Args {
			if a == "--loader" && i+1 < len(c.Args) {
				return c.Args[i+1]
			}
		}
	}
	t.Fatal("registration never issued efibootmgr --create ... --loader")
	return ""
}
