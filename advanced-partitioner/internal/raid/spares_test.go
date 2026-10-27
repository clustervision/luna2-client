package raid

import (
	"context"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
)

func createCalls(f *executor.FakeRunner) []string {
	var out []string
	for _, c := range f.Calls() {
		if c.Path == "mdadm" && len(c.Args) > 0 && c.Args[0] == "--create" {
			out = append(out, strings.Join(c.Args, " "))
		}
	}
	return out
}

// A raid1/raid10 os set whose /boot lives on the root LV has no /boot
// partitions: Build used to create /dev/md/<set>-boot anyway and fail
// "raid.create: no members". Only the arrays the layout has partitions for
// may be created.
func TestBuildMirroredOSWithoutBootVolume(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	layout := partition.Layout{
		RAIDMode: "raid1", MirrorBoot: true,
		EFIPartitions:     []string{"/dev/sda1", "/dev/sdb1"},
		StoragePartitions: []string{"/dev/sda2", "/dev/sdb2"},
	}
	state, err := New(Options{Runner: f}).Build(context.Background(), &v2.Set{Name: "os", Role: "os"}, layout)
	if err != nil {
		t.Fatalf("raid1 os without /boot must build: %v", err)
	}
	if state.BootMDDevice != "" || len(state.BootMembers) != 0 {
		t.Fatalf("no /boot partitions -> no -boot array, got %+v", state)
	}
	if state.EFIMDDevice != "/dev/md/os-efi" {
		t.Fatalf("ESP mirror missing: %+v", state)
	}
	for _, c := range createCalls(f) {
		if strings.Contains(c, "/dev/md/os-boot") {
			t.Fatalf("must not create the -boot array: %s", c)
		}
	}

	// And without ESP partitions either (data-like os layout): no -efi array.
	g := executor.NewFakeRunner(executor.FakeResponse{})
	layout.EFIPartitions = nil
	state, err = New(Options{Runner: g}).Build(context.Background(), &v2.Set{Name: "os", Role: "os"}, layout)
	if err != nil || state.EFIMDDevice != "" {
		t.Fatalf("no ESP partitions -> no -efi array; state=%+v err=%v", state, err)
	}
	if got := createCalls(g); len(got) != 1 {
		t.Fatalf("only the root array may be created, got %v", got)
	}
}

// Hot spares: the last <spares> members are created as spares, never as
// active members (raid1+1 spare used to fail, raid5+1 spare silently became a
// 5-member array). The boot-chain mirrors span every disk.
func TestBuildCreatesHotSpares(t *testing.T) {
	cases := []struct {
		name   string
		mode   string
		parts  []string
		spares int
		want   string
	}{
		{"raid1+1", "raid1", []string{"/dev/sda3", "/dev/sdb3", "/dev/sdc3"}, 1,
			"--create /dev/md/os --run --metadata 1.2 --level 1 --raid-devices 2 --spare-devices 1 --force --homehost=any --assume-clean /dev/sda3 /dev/sdb3 /dev/sdc3"},
		{"raid5+1", "raid5", []string{"/dev/sda3", "/dev/sdb3", "/dev/sdc3", "/dev/sdd3", "/dev/sde3"}, 1,
			"--create /dev/md/os --run --metadata 1.2 --level 5 --raid-devices 4 --spare-devices 1 --force --homehost=any /dev/sda3 /dev/sdb3 /dev/sdc3 /dev/sdd3 /dev/sde3"},
		{"raid10+2", "raid10", []string{"/dev/sda3", "/dev/sdb3", "/dev/sdc3", "/dev/sdd3", "/dev/sde3", "/dev/sdf3"}, 2,
			"--create /dev/md/os --run --metadata 1.2 --level 10 --raid-devices 4 --spare-devices 2 --force --homehost=any --assume-clean /dev/sda3 /dev/sdb3 /dev/sdc3 /dev/sdd3 /dev/sde3 /dev/sdf3"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := executor.NewFakeRunner(executor.FakeResponse{})
			var efi []string
			for _, p := range c.parts {
				efi = append(efi, strings.TrimSuffix(p, "3")+"1")
			}
			layout := partition.Layout{RAIDMode: c.mode, MirrorBoot: true, EFIPartitions: efi, StoragePartitions: c.parts}
			if _, err := New(Options{Runner: f}).Build(context.Background(),
				&v2.Set{Name: "os", Role: "os", Spares: c.spares}, layout); err != nil {
				t.Fatalf("Build: %v", err)
			}
			got := createCalls(f)
			if len(got) != 2 || got[0] != c.want {
				t.Fatalf("root create:\n got %v\nwant %s", got, c.want)
			}
			if !strings.Contains(got[1], "--raid-devices "+fmtInt(len(c.parts))+" ") || strings.Contains(got[1], "--spare-devices") {
				t.Fatalf("the ESP mirror spans every disk with no spares: %s", got[1])
			}
		})
	}
}

// No active member left once the spares are taken off is an error, not an
// array of spares.
func TestBuildRefusesAllSpares(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	_, err := New(Options{Runner: f}).Build(context.Background(), &v2.Set{Name: "d", Role: "data", Spares: 2},
		partition.Layout{RAIDMode: "raid1", StoragePartitions: []string{"/dev/sda1", "/dev/sdb1"}})
	if err == nil {
		t.Fatal("2 partitions with 2 spares must fail")
	}
	if len(createCalls(f)) != 0 {
		t.Fatal("no array may be created")
	}
}
