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
	return f
}

// Ubuntu lab node, raid1 os set with the second disk removed: udev had
// assembled os-efi from vda1 but left it inactive, without /dev/md/os-efi.
// lpart must start it and name it, not run a second --assemble ("busy").
func TestAssembleAdoptsInactiveAutoAssembledArray(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	assembleExplicit(context.Background(), heldBy("os-efi", "md125", "inactive"), "os-efi", []string{"/dev/vda"}, run, nil)
	want := []string{"mdadm --run /dev/md125", "mkdir -p /dev/md", "ln -sfn ../md125 /dev/md/os-efi"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}

// An array that already runs is only given its name.
func TestAssembleAdoptsRunningArrayWithoutRun(t *testing.T) {
	var got []string
	run := func(path string, args ...string) { got = append(got, path+" "+strings.Join(args, " ")) }
	assembleExplicit(context.Background(), heldBy("os-boot", "md126", "clean"), "os-boot", []string{"/dev/vda"}, run, nil)
	want := []string{"mkdir -p /dev/md", "ln -sfn ../md126 /dev/md/os-boot"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("calls = %q, want %q", got, want)
	}
}
