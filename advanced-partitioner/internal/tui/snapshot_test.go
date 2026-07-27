package tui

import (
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// Snapshot helper: build a populated model and dump View() so we can
// eyeball the layout at common terminal sizes.
func dumpView(t *testing.T, w, h int) string {
	t.Helper()
	m := newTestModel()
	m.Width, m.Height = w, h
	// Populate a representative state.
	m.State.Storage.NVMe = 2
	m.State.Storage.SSD = 1
	m.State.Storage.Spindles = 12
	m.State.Storage.Memory = "512GB"
	m.State.Storage.Candidates = []model.StorageCandidate{
		{Name: "sda", Kind: model.KindSSD, Size: "480G", Model: "Micron 5300", UID: "MSA22150ABCD", Set: "OS", RaidType: "RAID1", InUse: true},
		{Name: "sdb", Kind: model.KindSSD, Size: "480G", Model: "Micron 5300", UID: "MSA22150EFGH", Set: "OS", RaidType: "RAID1", InUse: true},
		{Name: "sdc", Kind: model.KindHDD, Size: "8.0T", Model: "Seagate Exos", UID: "ZL2A001", Set: "DATA", RaidType: "RAID5", InUse: true},
		{Name: "sdd", Kind: model.KindHDD, Size: "8.0T", Model: "Seagate Exos", UID: "ZL2A002", Set: "DATA", RaidType: "RAID5", InUse: true},
		{Name: "sde", Kind: model.KindHDD, Size: "8.0T", Model: "Seagate Exos", UID: "ZL2A003", Set: "DATA", RaidType: "RAID5", InUse: true},
		{Name: "nvme0n1", Kind: model.KindNVMe, Size: "1.92T", Model: "Samsung PM9A3", UID: "S6X1NE0R"},
	}
	m.State.Storage.Method = "RAID1 OS + RAID5 DATA · UEFI"
	m.State.Storage.Summary = "OS -> sda,sdb (RAID1)   DATA -> sdc,sdd,sde (RAID5)   Boot: UEFI/GRUB + NVRAM"
	m.State.Node = model.Node{
		Name: "node001", BootMode: "UEFI", Arch: "x86_64",
		Vendor: "Dell Inc.", Model: "PowerEdge R760",
		CPU: "Intel Xeon Gold 5218", CPUTopo: "32 vCPU · 2S/8C/2T",
		NICs: "2 interface(s)", GPU: "2 x NVIDIA",
	}
	m.State.Luna = model.Luna{
		Controller: "10.141.255.252:7050", TargetIP: "10.141.0.1/16",
		Hostname: "node001", Image: "compute-image",
		InstallMode: "auto",
	}
	m.State.MarkStage("preflight", model.StageDone, "")
	m.State.MarkStage("hwdiscover", model.StageDone, "")
	m.State.MarkStage("lpart-discover", model.StageRunning, "")
	for _, line := range []string{
		"$ /usr/local/bin/install.sh",
		"+ echo LPART_STAGE=preflight",
		"LPART_STAGE=preflight",
		"preflight: checking installer environment",
		"LPART_STAGE_DONE=preflight",
		"sgdisk --zap-all /dev/nvme1n1",
		"mounted tmpfs at /mnt/osimg",
		"warning: legacy LICENSE.txt skipped",
	} {
		m.appendOutput(line, "stdout", "")
	}
	return m.View()
}

func TestViewSnapshot100x30(t *testing.T) {
	out := dumpView(t, 100, 30)
	t.Logf("100x30 (full layout):\n%s", stripANSI(out))
}

func TestViewSnapshot120x50(t *testing.T) {
	// Tall console (like the node001 VM): verifies the storage panel is
	// capped to its content and the bottom panels + footer fill the space.
	out := dumpView(t, 120, 50)
	t.Logf("120x50 (tall full layout):\n%s", stripANSI(out))
}

func TestViewSnapshot80x24(t *testing.T) {
	out := dumpView(t, 80, 24)
	t.Logf("80x24 (compact layout):\n%s", stripANSI(out))
}

func TestViewSnapshot60x18(t *testing.T) {
	out := dumpView(t, 60, 18)
	t.Logf("60x18 (compact lower bound):\n%s", stripANSI(out))
}

// stripANSI removes CSI escapes so the snapshot is readable in test logs.
func stripANSI(s string) string {
	var b strings.Builder
	inEsc := false
	for _, r := range s {
		if inEsc {
			if r >= 0x40 && r <= 0x7e {
				inEsc = false
			}
			continue
		}
		if r == 0x1b {
			inEsc = true
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
