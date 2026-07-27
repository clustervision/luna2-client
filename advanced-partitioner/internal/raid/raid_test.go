package raid

import (
	"context"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
)

func TestBuildNoneIsNoOp(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	b := New(Options{Runner: f})
	state, err := b.Build(context.Background(), &v2.Set{Name: "os", Role: "os"},
		partition.Layout{RAIDMode: "none"})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if state.RAIDMode != "none" {
		t.Fatalf("state.RAIDMode = %q", state.RAIDMode)
	}
	if len(f.Calls()) != 0 {
		t.Fatalf("raid=none should not call any commands; got %v", f.Calls())
	}
}

func TestBuildRAID1StorageOnly(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	layout := partition.Layout{
		RAIDMode:          "raid1",
		StoragePartitions: []string{"/dev/sda3", "/dev/sdb3"},
	}
	state, err := New(Options{Runner: f}).Build(context.Background(),
		&v2.Set{Name: "data", Role: "data"}, layout)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if state.MDDevice != "/dev/md/data" {
		t.Fatalf("MDDevice = %q", state.MDDevice)
	}
	if state.MetadataVersion != "1.2" {
		t.Fatalf("MetadataVersion = %q", state.MetadataVersion)
	}
	mustHaveCalls(t, f, []string{
		// Defensive member clear before create (so a stale superblock surviving
		// a re-partition cannot make mdadm --create prompt + abort).
		"mdadm --zero-superblock /dev/sda3",
		"mdadm --zero-superblock /dev/sdb3",
		"wipefs -a /dev/sda3",
		"wipefs -a /dev/sdb3",
		"mdadm --create /dev/md/data --run --metadata 1.2 --level 1 --raid-devices 2 --force --homehost=any --assume-clean /dev/sda3 /dev/sdb3",
		"wipefs -af /dev/md/data",
	})
	// No mirrored EFI/boot arrays for data role.
	if state.EFIMDDevice != "" || state.BootMDDevice != "" {
		t.Fatalf("non-OS set should not produce EFI/boot arrays: %+v", state)
	}
}

func TestBuildRAID1OSWithMirroredEFIAndBoot(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	layout := partition.Layout{
		Role:              "os",
		FirmwareMode:      "uefi",
		RAIDMode:          "raid1",
		MirrorBoot:        true,
		EFIPartitions:     []string{"/dev/sda1", "/dev/sdb1"},
		BootPartitions:    []string{"/dev/sda2", "/dev/sdb2"},
		StoragePartitions: []string{"/dev/sda3", "/dev/sdb3"},
	}
	state, err := New(Options{Runner: f}).Build(context.Background(),
		&v2.Set{Name: "os", Role: "os"}, layout)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if state.EFIMDDevice != "/dev/md/os-efi" || state.EFIMetadataVersion != "1.0" {
		t.Fatalf("EFI state wrong: %+v", state)
	}
	if state.BootMDDevice != "/dev/md/os-boot" || state.BootMetadataVersion != "1.0" {
		t.Fatalf("Boot state wrong: %+v", state)
	}
	mustHaveCalls(t, f, []string{
		"mdadm --create /dev/md/os --run --metadata 1.2 --level 1 --raid-devices 2 --force --homehost=any --assume-clean /dev/sda3 /dev/sdb3",
		"mdadm --create /dev/md/os-efi --run --metadata 1.0 --level 1 --raid-devices 2 --force --homehost=any --assume-clean /dev/sda1 /dev/sdb1",
		"mdadm --create /dev/md/os-boot --run --metadata 1.0 --level 1 --raid-devices 2 --force --homehost=any --assume-clean /dev/sda2 /dev/sdb2",
		"wipefs -af /dev/md/os",
		"wipefs -af /dev/md/os-efi",
		"wipefs -af /dev/md/os-boot",
	})
}

func TestBuildRAID5(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	layout := partition.Layout{
		RAIDMode:          "raid5",
		StoragePartitions: []string{"/dev/sda1", "/dev/sdb1", "/dev/sdc1"},
	}
	_, err := New(Options{Runner: f}).Build(context.Background(),
		&v2.Set{Name: "data", Role: "data"}, layout)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	// Barrier pin (FTA C1): parity levels must NOT get --assume-clean — it
	// marks the array in-sync with uninitialised parity, and a later
	// rebuild/degraded read reconstructs garbage. The initial resync is
	// the price of correctness. Removing this distinction fails here.
	mustHaveCalls(t, f, []string{
		"mdadm --create /dev/md/data --run --metadata 1.2 --level 5 --raid-devices 3 --force --homehost=any /dev/sda1 /dev/sdb1 /dev/sdc1",
	})
	for _, c := range f.Calls() {
		if c.Path != "mdadm" {
			continue
		}
		for i, a := range c.Args {
			if a == "--assume-clean" {
				t.Fatalf("parity array must not be created --assume-clean: %v", c.Args)
			}
			_ = i
		}
	}
}

// TestBuildRaid6NoAssumeClean pins the same invariant for raid6.
func TestBuildRaid6NoAssumeClean(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	layout := partition.Layout{
		RAIDMode:          "raid6",
		StoragePartitions: []string{"/dev/sda1", "/dev/sdb1", "/dev/sdc1", "/dev/sdd1"},
	}
	_, err := New(Options{Runner: f}).Build(context.Background(),
		&v2.Set{Name: "data", Role: "data"}, layout)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	for _, c := range f.Calls() {
		if c.Path != "mdadm" {
			continue
		}
		for _, a := range c.Args {
			if a == "--assume-clean" {
				t.Fatalf("raid6 must not be created --assume-clean: %v", c.Args)
			}
		}
	}
}

// TestBuildRaid5OSMirroredBootChain pins FTA C2: a redundant non-raid1 OS
// set gets its ESP and /boot as md mirrors across ALL members, so the node
// still boots after losing any one disk.
func TestBuildRaid5OSMirroredBootChain(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	layout := partition.Layout{
		RAIDMode:          "raid5",
		MirrorBoot:        true,
		EFIPartitions:     []string{"/dev/sda1", "/dev/sdb1", "/dev/sdc1"},
		BootPartitions:    []string{"/dev/sda2", "/dev/sdb2", "/dev/sdc2"},
		StoragePartitions: []string{"/dev/sda3", "/dev/sdb3", "/dev/sdc3"},
	}
	st, err := New(Options{Runner: f}).Build(context.Background(),
		&v2.Set{Name: "os", Role: "os"}, layout)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	if st.EFIMDDevice == "" || st.BootMDDevice == "" {
		t.Fatalf("raid5 OS must build md efi+boot mirrors, got %+v", st)
	}
	if len(st.EFIMembers) != 3 || len(st.BootMembers) != 3 {
		t.Fatalf("boot chain must span ALL members: %+v", st)
	}
	mustHaveCalls(t, f, []string{
		"mdadm --create /dev/md/os-efi --run --metadata 1.0 --level 1 --raid-devices 3 --force --homehost=any --assume-clean /dev/sda1 /dev/sdb1 /dev/sdc1",
		"mdadm --create /dev/md/os-boot --run --metadata 1.0 --level 1 --raid-devices 3 --force --homehost=any --assume-clean /dev/sda2 /dev/sdb2 /dev/sdc2",
	})
}

// TestBuildCreateSetsHomehostAny pins BE-N4: every mdadm --create must carry
// --homehost=any before the member devices. Metadata 1.x otherwise records the
// installer initramfs hostname, making the array "foreign" on the installed OS
// and letting it assemble as /dev/md127 instead of its deterministic name.
func TestBuildCreateSetsHomehostAny(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	layout := partition.Layout{
		Role:              "os",
		RAIDMode:          "raid1",
		MirrorBoot:        true,
		EFIPartitions:     []string{"/dev/sda1", "/dev/sdb1"},
		BootPartitions:    []string{"/dev/sda2", "/dev/sdb2"},
		StoragePartitions: []string{"/dev/sda3", "/dev/sdb3"},
	}
	if _, err := New(Options{Runner: f}).Build(context.Background(),
		&v2.Set{Name: "os", Role: "os"}, layout); err != nil {
		t.Fatalf("Build: %v", err)
	}
	creates := 0
	for _, c := range f.Calls() {
		if c.Path != "mdadm" || len(c.Args) == 0 || c.Args[0] != "--create" {
			continue
		}
		creates++
		found := -1
		firstMember := -1
		for i, a := range c.Args {
			if a == "--homehost=any" {
				found = i
			}
			if firstMember < 0 && strings.HasPrefix(a, "/dev/sd") {
				firstMember = i
			}
		}
		if found < 0 {
			t.Fatalf("mdadm --create missing --homehost=any: %v", c.Args)
		}
		if firstMember >= 0 && found > firstMember {
			t.Fatalf("--homehost=any must precede the member devices: %v", c.Args)
		}
	}
	if creates != 3 {
		t.Fatalf("expected 3 mdadm --create calls, got %d", creates)
	}
}

func TestMDNameForSet(t *testing.T) {
	for in, want := range map[string]string{
		"os":       "os",
		"my-data":  "my_data",
		"data 1":   "data_1",
		"a:b:c":    "a_b_c",
		"trail_":   "trail",
		"trail___": "trail",
	} {
		if got := mdNameForSet(in); got != want {
			t.Fatalf("mdNameForSet(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestBuildPreflightRequiresRunner(t *testing.T) {
	_, err := New(Options{}).Build(context.Background(), &v2.Set{Name: "x"},
		partition.Layout{RAIDMode: "raid1"})
	if err == nil {
		t.Fatal("expected Runner-required preflight error")
	}
}

func TestBuildBadRAIDMode(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	_, err := New(Options{Runner: f}).Build(context.Background(), &v2.Set{Name: "x"},
		partition.Layout{RAIDMode: "raid42"})
	if err == nil || !strings.Contains(err.Error(), "unsupported RAID mode") {
		t.Fatalf("expected unsupported-raid error; got %v", err)
	}
}

func mustHaveCalls(t *testing.T, f *executor.FakeRunner, want []string) {
	t.Helper()
	got := make([]string, 0, len(f.Calls()))
	for _, c := range f.Calls() {
		got = append(got, c.Path+" "+strings.Join(c.Args, " "))
	}
	for _, w := range want {
		found := false
		for _, g := range got {
			if g == w {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("expected %q in:\n  %s", w, strings.Join(got, "\n  "))
		}
	}
}
