package storagehealth

import (
	"context"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func heldBy(name, md, state string) *executor.FakeRunner {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vda"},
		Response: executor.FakeResponse{Stdout: []byte("vda disk\nvda1 part\n")}})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
		Response: executor.FakeResponse{Stdout: []byte("           Name : any:" + name + "  (local to host any)\n")}})
	f.AddMatch(executor.FakeMatch{Path: "ls", ArgsPrefix: []string{"-1", "/sys/class/block/vda1/holders"},
		Response: executor.FakeResponse{Stdout: []byte(md + "\n")}})
	f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/" + md + "/md/array_state"},
		Response: executor.FakeResponse{Stdout: []byte(state + "\n")}})
	f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/" + md + "/md/degraded"},
		Response: executor.FakeResponse{Stdout: []byte("0\n")}})
	f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/" + md + "/md/dev-vda1/state"},
		Response: executor.FakeResponse{Stdout: []byte("in_sync\n")}})
	return f
}

// Ubuntu, raid1 os set with the second disk removed: udev had assembled
// os-efi from vda1 but left it inactive, without /dev/md/os-efi. With
// allow_degraded lpart starts it and names it, not a second --assemble ("busy").
func TestAssembleAdoptsInactiveAutoAssembledArray(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	_, degraded := assembleExplicit(context.Background(), heldBy("os-efi", "md125", "inactive"), "os-efi", []string{"/dev/vda"}, run, nil, true)
	want := []string{"mdadm --run /dev/md125", "mkdir -p /dev/md", "ln -sfn ../md125 /dev/md/os-efi"}
	if degraded || strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q degraded=%v, want %q", got, degraded, want)
	}
}

// Without allow_degraded the inactive array is left as it is and reported.
func TestAssembleLeavesInactiveAutoAssembledArrayDegraded(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	assembled, degraded := assembleExplicit(context.Background(), heldBy("os-efi", "md125", "inactive"), "os-efi", []string{"/dev/vda"}, run, nil, false)
	if assembled || !degraded || len(got) != 0 {
		t.Fatalf("calls = %q assembled=%v degraded=%v, want nothing run and degraded", got, assembled, degraded)
	}
}

// An array that already runs is only given its name.
func TestAssembleAdoptsRunningArrayWithoutRun(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	assembleExplicit(context.Background(), heldBy("os-boot", "md126", "clean"), "os-boot", []string{"/dev/vda"}, run, nil, false)
	want := []string{"mkdir -p /dev/md", "ln -sfn ../md126 /dev/md/os-boot"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// mirrorHeldBy is a raid1 over vda1 and vdb1, both held by md125 in the
// given state, with each member's superblock event count and kernel state.
func mirrorHeldBy(state, degraded string, members map[string][2]string) *executor.FakeRunner {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	for _, d := range []string{"vda", "vdb"} {
		f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/" + d},
			Response: executor.FakeResponse{Stdout: []byte(d + " disk\n" + d + "1 part\n")}})
		f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/" + d + "1"},
			Response: executor.FakeResponse{Stdout: []byte("           Name : any:os  (local to host any)\n         Events : " + members[d][0] + "\n")}})
		f.AddMatch(executor.FakeMatch{Path: "ls", ArgsPrefix: []string{"-1", "/sys/class/block/" + d + "1/holders"},
			Response: executor.FakeResponse{Stdout: []byte("md125\n")}})
		f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/md125/md/dev-" + d + "1/state"},
			Response: executor.FakeResponse{Stdout: []byte(members[d][1] + "\n")}})
	}
	f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/md125/md/array_state"},
		Response: executor.FakeResponse{Stdout: []byte(state + "\n")}})
	f.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/md125/md/degraded"},
		Response: executor.FakeResponse{Stdout: []byte(degraded + "\n")}})
	return f
}

// After a hard reset a healthy mirror's superblocks can differ by one event;
// the kernel runs both members in sync, so the array is taken over.
func TestAssembleTakesOverRunningMirrorAfterHardReset(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	f := mirrorHeldBy("clean", "0", map[string][2]string{"vda": {"3", "in_sync"}, "vdb": {"2", "in_sync"}})
	_, degraded := assembleExplicit(context.Background(), f, "os", []string{"/dev/vda", "/dev/vdb"}, run, nil, false)
	if degraded || strings.Join(got, "|") != "mkdir -p /dev/md|ln -sfn ../md125 /dev/md/os" {
		t.Fatalf("calls = %q degraded=%v, want the running mirror taken over", got, degraded)
	}
}

// A running array with a member the kernel does not hold in sync is not
// taken over, even with allow_degraded: it counts as degraded.
func TestAssembleRefusesRunningArrayWithMemberOutOfSync(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	f := mirrorHeldBy("clean", "1", map[string][2]string{"vda": {"100", "in_sync"}, "vdb": {"90", "faulty"}})
	_, degraded := assembleExplicit(context.Background(), f, "os", []string{"/dev/vda", "/dev/vdb"}, run, nil, true)
	if !degraded || len(got) != 0 {
		t.Fatalf("calls = %q degraded=%v, want the array left alone and degraded", got, degraded)
	}
}

// An array that is not running yet and whose members disagree on their event
// count (a stale disk put back) is not started, even with allow_degraded.
func TestAssembleDoesNotStartInactiveArrayWithStaleMember(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	f := mirrorHeldBy("inactive", "0", map[string][2]string{"vda": {"100", ""}, "vdb": {"90", ""}})
	_, degraded := assembleExplicit(context.Background(), f, "os", []string{"/dev/vda", "/dev/vdb"}, run, nil, true)
	if !degraded || len(got) != 0 {
		t.Fatalf("calls = %q degraded=%v, want nothing started and degraded", got, degraded)
	}
}
