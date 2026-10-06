package pipeline

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/bootloader"
)

func writeResult(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "storage-result.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// Single-set RAID1 OS: both members tagged OS/RAID1, UID from serial.
func TestParseLayoutRAID1OS(t *testing.T) {
	info := parseLayout(writeResult(t, `{
	  "role": "os", "raid_mode": "raid1", "register_nvram": "auto",
	  "selected_disks": [
	    {"resolved_path": "/dev/sda", "serial": "VMW-SA-001"},
	    {"resolved_path": "/dev/sdb", "serial": "VMW-SA-002"}
	  ]}`))
	for _, d := range []string{"sda", "sdb"} {
		di, ok := info.Disks[d]
		if !ok {
			t.Fatalf("expected %s in layout, got %v", d, info.Disks)
		}
		if di.Set != "OS" || di.Raid != "RAID1" {
			t.Errorf("%s = {Set:%q Raid:%q}, want {OS RAID1}", d, di.Set, di.Raid)
		}
	}
	if info.Disks["sda"].UID != "VMW-SA-001" {
		t.Errorf("sda UID = %q, want serial", info.Disks["sda"].UID)
	}
	if _, ok := info.Disks["sdc"]; ok {
		t.Errorf("sdc should not be in layout: %v", info.Disks)
	}
	if info.MemOS {
		t.Errorf("MemOS should be false for a disk install")
	}
	if !strings.HasPrefix(info.Method, "RAID1 OS · ") {
		t.Errorf("method = %q, want 'RAID1 OS · <fw>'", info.Method)
	}
	if !strings.Contains(info.Summary, "OS -> sda,sdb (RAID1)") {
		t.Errorf("summary = %q, want OS segment", info.Summary)
	}
}

// Multi-set OS RAID1 + one DATA RAID5: single data set labelled DATA.
func TestParseLayoutOSPlusData(t *testing.T) {
	info := parseLayout(writeResult(t, `{"version":2,"sets":[
	  {"role":"os","raid_mode":"raid1","selected_disks":[{"resolved_path":"/dev/sda"},{"resolved_path":"/dev/sdb"}]},
	  {"role":"data","raid_mode":"raid5","selected_disks":[{"resolved_path":"/dev/sdc"},{"resolved_path":"/dev/sdd"},{"resolved_path":"/dev/sde"}]}
	]}`))
	if di := info.Disks["sdc"]; di.Set != "DATA" || di.Raid != "RAID5" {
		t.Errorf("sdc = {Set:%q Raid:%q}, want {DATA RAID5}", di.Set, di.Raid)
	}
	if di := info.Disks["sda"]; di.Set != "OS" {
		t.Errorf("sda Set = %q, want OS", di.Set)
	}
	if !strings.HasPrefix(info.Method, "RAID1 OS + RAID5 DATA · ") {
		t.Errorf("method = %q", info.Method)
	}
}

// Two DATA sets get numbered DATA1/DATA2; a non-RAID set has blank RAID.
func TestParseLayoutTwoDataSets(t *testing.T) {
	info := parseLayout(writeResult(t, `{"version":2,"sets":[
	  {"role":"os","raid_mode":"raid1","selected_disks":[{"resolved_path":"/dev/sda"},{"resolved_path":"/dev/sdb"}]},
	  {"role":"data","raid_mode":"raid5","selected_disks":[{"resolved_path":"/dev/sdc"},{"resolved_path":"/dev/sdd"},{"resolved_path":"/dev/sde"}]},
	  {"role":"data","raid_mode":"none","selected_disks":[{"resolved_path":"/dev/sdf"}]}
	]}`))
	if di := info.Disks["sdc"]; di.Set != "DATA1" {
		t.Errorf("sdc Set = %q, want DATA1", di.Set)
	}
	if di := info.Disks["sdf"]; di.Set != "DATA2" || di.Raid != "" {
		t.Errorf("sdf = {Set:%q Raid:%q}, want {DATA2 <blank>}", di.Set, di.Raid)
	}
}

// Single disk, no RAID: tagged OS with blank RAID.
func TestParseLayoutSingleDisk(t *testing.T) {
	info := parseLayout(writeResult(t, `{"role":"os","raid_mode":"none",
	  "selected_disks":[{"resolved_path":"/dev/nvme0n1","serial":"S1"}]}`))
	if di := info.Disks["nvme0n1"]; di.Set != "OS" || di.Raid != "" {
		t.Errorf("nvme0n1 = {Set:%q Raid:%q}, want {OS <blank>}", di.Set, di.Raid)
	}
	if !strings.HasPrefix(info.Method, "disk OS · ") {
		t.Errorf("method = %q, want 'disk OS · <fw>'", info.Method)
	}
}

// OS-in-memory: no disks, MemOS flagged.
func TestParseLayoutMemOS(t *testing.T) {
	info := parseLayout(writeResult(t, `{"role":"os","raid_mode":"none","storage_type":"memory","selected_disks":[]}`))
	if !info.MemOS {
		t.Errorf("MemOS should be true for a memory-root OS")
	}
	if len(info.Disks) != 0 {
		t.Errorf("expected no disks, got %v", info.Disks)
	}
}

// A disk OS with boot_method=none (BIOS firmware) must not claim GRUB in the
// summary: the node PXE-boots every time. bootMode() reads the host's /sys, so
// only the boot segment is asserted.
func TestParseLayoutDiskOSWithoutBootloader(t *testing.T) {
	info := parseLayout(writeResult(t, `{"role":"os","raid_mode":"none","boot_method":"none",
	  "selected_disks":[{"resolved_path":"/dev/sda","serial":"S1"}]}`))
	want := "Boot: none (BIOS firmware: GRUB, shim and boot order skipped; node PXE-boots every time)"
	if !strings.Contains(info.Summary, want) {
		t.Errorf("summary = %q, want it to contain %q", info.Summary, want)
	}
	if strings.Contains(info.Summary, "/GRUB") {
		t.Errorf("summary must not claim GRUB: %q", info.Summary)
	}
}

// A disk OS with a bootloader still reports GRUB.
func TestParseLayoutDiskOSWithGRUB(t *testing.T) {
	info := parseLayout(writeResult(t, `{"role":"os","raid_mode":"none","boot_method":"grub",
	  "selected_disks":[{"resolved_path":"/dev/sda","serial":"S1"}]}`))
	if !strings.Contains(info.Summary, "/GRUB") {
		t.Errorf("summary = %q, want it to contain /GRUB", info.Summary)
	}
}

func TestHostFromURL(t *testing.T) {
	cases := map[string]string{
		"https://10.141.255.252:7050": "10.141.255.252:7050",
		"http://controller/api":       "controller",
		"controller:7050":             "controller:7050",
		"":                            "",
	}
	for in, want := range cases {
		if got := hostFromURL(in); got != want {
			t.Errorf("hostFromURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestHumanSize(t *testing.T) {
	cases := map[int64]string{
		512:                     "512B",
		1024:                    "1.0KB",
		25 * 1024 * 1024 * 1024: "25.0GB",
	}
	for in, want := range cases {
		if got := humanSize(in); got != want {
			t.Errorf("humanSize(%d) = %q, want %q", in, got, want)
		}
	}
}

func TestShortCPUModel(t *testing.T) {
	cases := map[string]string{
		"Intel(R) Xeon(R) Gold 5218 CPU @ 2.30GHz": "Intel Xeon Gold 5218",
		"AMD EPYC 7763 64-Core Processor":          "AMD EPYC 7763 64-Core Processor",
		"":                                         "",
	}
	for in, want := range cases {
		if got := shortCPUModel(in); got != want {
			t.Errorf("shortCPUModel(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestByPathTail(t *testing.T) {
	cases := map[string]string{
		"/dev/disk/by-path/pci-0000:03:00.0-scsi-0:0:0:0": "03:00.0-scsi-0:0:0:0",
		"pci-0000:18:00.0-nvme-1":                         "18:00.0-nvme-1",
		"":                                                "",
	}
	for in, want := range cases {
		if got := byPathTail(in); got != want {
			t.Errorf("byPathTail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDiskUID(t *testing.T) {
	if got := diskUID("SN123", "0xabc", "Model", "sda"); got != "SN123" {
		t.Errorf("serial should win: %q", got)
	}
	if got := diskUID("", "0xabc", "Model", "sda"); got != "0xabc" {
		t.Errorf("wwn should be next: %q", got)
	}
	if got := diskUID("", "", "Virtual disk", "sda"); got != "Virtual disk" {
		t.Errorf("model fallback: %q", got)
	}
	if got := diskUID("", "", "", "sda"); got != "sda" {
		t.Errorf("name last: %q", got)
	}
}

// bootloader: none on a UEFI node must not be reported as the BIOS case: the
// operator chose it, and the remedy is the key, not the firmware.
func TestParseLayoutBootloaderNoneOnUEFI(t *testing.T) {
	info := parseLayout(writeResult(t, `{"role":"os","raid_mode":"none","firmware_mode":"uefi",
	  "boot_method":"none","boot_order":"last",
	  "selected_disks":[{"resolved_path":"/dev/sda","serial":"S1"}]}`))
	want := "Boot: none (bootloader: none; node PXE-boots every time)"
	if !strings.Contains(info.Summary, want) {
		t.Errorf("summary = %q, want it to contain %q", info.Summary, want)
	}
}

// pinBootMode fixes the firmware the summary reports, so a test asserting the
// UEFI boot line does not depend on the firmware of the machine running it.
func pinBootMode(t *testing.T, mode string) {
	t.Helper()
	orig := bootMode
	bootMode = func() string { return mode }
	t.Cleanup(func() { bootMode = orig })
}

// bootloader: first, and the firmware kept it: the summary says the disk is
// first so the operator can see the request landed.
func TestParseLayoutBootOrderFirstHonoured(t *testing.T) {
	pinBootMode(t, "UEFI")
	info := parseLayout(writeResult(t, `{"role":"os","raid_mode":"none","firmware_mode":"uefi",
	  "boot_method":"grub","boot_order":"first","register_nvram":"auto",
	  "selected_disks":[{"resolved_path":"/dev/sda","serial":"S1"}]}`))
	if !strings.Contains(info.Summary, "(disk first)") {
		t.Errorf("summary = %q, want it to report the disk first", info.Summary)
	}
}

// Same request, but the finalise's read-back found the firmware had re-sorted
// the order. The summary must say what actually happened, not what was asked.
func TestParseLayoutBootOrderFirstNotHonoured(t *testing.T) {
	pinBootMode(t, "UEFI")
	path := writeResult(t, `{"role":"os","raid_mode":"none","firmware_mode":"uefi",
	  "boot_method":"grub","boot_order":"first","register_nvram":"auto",
	  "selected_disks":[{"resolved_path":"/dev/sda","serial":"S1"}]}`)
	marker := filepath.Join(filepath.Dir(path), bootloader.BootOrderNotHonouredMarker)
	if err := os.WriteFile(marker, []byte("0010,0001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	info := parseLayout(path)
	want := "(disk first requested, firmware kept network first)"
	if !strings.Contains(info.Summary, want) {
		t.Errorf("summary = %q, want it to contain %q", info.Summary, want)
	}
}
