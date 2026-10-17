package storagehealth

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

func TestAssembleExplicitUsesExaminedMembers(t *testing.T) {
	logger, err := txlog.Init(txlog.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()
	fake := executor.NewFakeRunner(executor.FakeResponse{})
	// two disks, one partition each; both partitions carry the "data" array
	fake.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vdb"},
		Response: executor.FakeResponse{Stdout: []byte("vdb disk\nvdb1 part\n")}})
	fake.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vdc"},
		Response: executor.FakeResponse{Stdout: []byte("vdc disk\nvdc1 part\n")}})
	fake.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
		Response: executor.FakeResponse{Stdout: []byte("           Name : any:data  (local to host any)\n")}})
	var got [][]string
	run := func(path string, args ...string) { got = append(got, append([]string{path}, args...)) }
	assembleExplicit(context.Background(), fake, "data", []string{"/dev/vdb", "/dev/vdc"}, run, logger, false)
	if len(got) != 1 {
		t.Fatalf("want 1 assemble call, got %v", got)
	}
	want := []string{"mdadm", "--assemble", "/dev/md/data", "/dev/vdb1", "/dev/vdc1"}
	if strings.Join(got[0], " ") != strings.Join(want, " ") {
		t.Fatalf("assemble argv = %v, want %v", got[0], want)
	}
}

func TestAssembleExplicitNoMembersIsNoop(t *testing.T) {
	logger, err := txlog.Init(txlog.Options{LogDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = logger.Close() }()
	fake := executor.NewFakeRunner(executor.FakeResponse{ExitCode: 1, Err: fmt.Errorf("no superblock")})
	var got [][]string
	run := func(path string, args ...string) { got = append(got, append([]string{path}, args...)) }
	assembleExplicit(context.Background(), fake, "data", []string{"/dev/vdb"}, run, logger, false)
	if len(got) != 0 {
		t.Fatalf("want no assemble call without members, got %v", got)
	}
}

func TestMDSuperblockNameParsing(t *testing.T) {
	for _, tc := range []struct{ out, want string }{
		{"           Name : any:data  (local to host any)\n", "data"},
		{"           Name : node002:os-boot\n", "os-boot"},
		{"           Name : bare\n", "bare"},
		{"no superblock here\n", ""},
	} {
		fake := executor.NewFakeRunner(executor.FakeResponse{Stdout: []byte(tc.out)})
		if got := mdSuperblockName(context.Background(), fake, "/dev/vdb1"); got != tc.want {
			t.Fatalf("mdSuperblockName(%q) = %q, want %q", tc.out, got, tc.want)
		}
	}
}

// survivingMember is one mirror member that is free until mdadm --assemble
// runs, and held by md127 (in the given state) after it.
type survivingMember struct {
	*executor.FakeRunner
	assembled bool
}

func newSurvivingMember(state string) *survivingMember {
	fake := executor.NewFakeRunner(executor.FakeResponse{})
	fake.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vdc"},
		Response: executor.FakeResponse{Stdout: []byte("vdc disk\nvdc1 part\n")}})
	fake.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine"},
		Response: executor.FakeResponse{Stdout: []byte("           Name : any:os  (local to host any)\n")}})
	fake.AddMatch(executor.FakeMatch{Path: "cat", ArgsPrefix: []string{"/sys/block/md127/md/array_state"},
		Response: executor.FakeResponse{Stdout: []byte(state + "\n")}})
	return &survivingMember{FakeRunner: fake}
}

func (m *survivingMember) Run(ctx context.Context, spec executor.CommandSpec) (executor.CommandResult, error) {
	if spec.Path == "ls" && len(spec.Args) == 2 && spec.Args[1] == "/sys/class/block/vdc1/holders" {
		if m.assembled {
			return executor.CommandResult{Stdout: []byte("md127\n")}, nil
		}
		return executor.CommandResult{}, nil
	}
	return m.FakeRunner.Run(ctx, spec)
}

// One mirror disk dead, the set allows running degraded: the surviving member
// alone is assembled with --run so the array starts.
func TestAssembleExplicitStartsDegradedArray(t *testing.T) {
	m := newSurvivingMember("clean")
	var got [][]string
	run := func(path string, args ...string) {
		got = append(got, append([]string{path}, args...))
		m.assembled = true
	}
	_, degraded := assembleExplicit(context.Background(), m, "os", []string{"/dev/vdc"}, run, nil, true)
	if degraded || len(got) != 1 || strings.Join(got[0], " ") != "mdadm --assemble --run /dev/md/os /dev/vdc1" {
		t.Fatalf("degraded assemble = %v (degraded=%v), want mdadm --assemble --run /dev/md/os /dev/vdc1", got, degraded)
	}
}

// The same dead disk under the health probe of a set that does not allow it:
// no --run, the array stays inactive and is reported degraded.
func TestProbeLeavesDegradedArrayInactive(t *testing.T) {
	m := newSurvivingMember("inactive")
	var got [][]string
	run := func(path string, args ...string) {
		got = append(got, append([]string{path}, args...))
		m.assembled = true
	}
	assembled, degraded := assembleExplicit(context.Background(), m, "os", []string{"/dev/vdc"}, run, nil, false)
	if !assembled || !degraded || len(got) != 1 || strings.Join(got[0], " ") != "mdadm --assemble /dev/md/os /dev/vdc1" {
		t.Fatalf("probe assemble = %v (assembled=%v degraded=%v), want mdadm --assemble /dev/md/os /dev/vdc1, degraded", got, assembled, degraded)
	}
}

// Release takes the probe's volume group down first, then the arrays it assembled.
func TestReleaseStopsWhatTheProbeStarted(t *testing.T) {
	fake := executor.NewFakeRunner(executor.FakeResponse{})
	Release(context.Background(), fake, Activation{Assembled: []string{"os", "os-efi"}}, "vg_uuid=abc", nil)
	var got []string
	for _, c := range fake.Calls() {
		got = append(got, c.Path+" "+strings.Join(c.Args, " "))
	}
	want := "vgchange -an --select vg_uuid=abc|mdadm --stop /dev/md/os|mdadm --stop /dev/md/os-efi"
	if strings.Join(got, "|") != want {
		t.Fatalf("release = %q, want %q", strings.Join(got, "|"), want)
	}
}
