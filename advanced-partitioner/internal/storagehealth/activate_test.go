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
	assembleExplicit(context.Background(), fake, "data", []string{"/dev/vdb", "/dev/vdc"}, func(string, ...string) {}, logger, false)
	var got [][]string
	for _, c := range fake.Calls() {
		if c.Path == "mdadm" && len(c.Args) > 0 && c.Args[0] == "--assemble" {
			got = append(got, append([]string{c.Path}, c.Args...))
		}
	}
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
	got       [][]string
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
	if spec.Path == "mdadm" && len(spec.Args) > 0 && spec.Args[0] == "--assemble" {
		m.got = append(m.got, append([]string{spec.Path}, spec.Args...))
		m.assembled = true
		return executor.CommandResult{}, nil
	}
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
	_, degraded := assembleExplicit(context.Background(), m, "os", []string{"/dev/vdc"}, func(string, ...string) {}, nil, true)
	got := m.got
	if degraded || len(got) != 1 || strings.Join(got[0], " ") != "mdadm --assemble --run /dev/md/os /dev/vdc1" {
		t.Fatalf("degraded assemble = %v (degraded=%v), want mdadm --assemble --run /dev/md/os /dev/vdc1", got, degraded)
	}
}

// The same dead disk under the health probe of a set that does not allow it:
// no --run, the array stays inactive and is reported degraded.
func TestProbeLeavesDegradedArrayInactive(t *testing.T) {
	m := newSurvivingMember("inactive")
	assembled, degraded := assembleExplicit(context.Background(), m, "os", []string{"/dev/vdc"}, func(string, ...string) {}, nil, false)
	got := m.got
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

// lostSuperblock is a raid1 whose second member lost its md superblock: only
// vdb2 is found, mdadm will not start the array without --run, and nothing
// holds the member afterwards.
func lostSuperblock(examine, stderr string) *executor.FakeRunner {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vda"},
		Response: executor.FakeResponse{Stdout: []byte("vda disk\nvda2 part\n")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/vdb"},
		Response: executor.FakeResponse{Stdout: []byte("vdb disk\nvdb2 part\n")}})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/vda2"},
		Response: executor.FakeResponse{ExitCode: 1, Err: fmt.Errorf("exit status 1")}})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/vdb2"},
		Response: executor.FakeResponse{Stdout: []byte("           Name : any:os-boot  (local to host any)\n" + examine)}})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--assemble"},
		Response: executor.FakeResponse{ExitCode: 1, Err: fmt.Errorf("exit status 1"), Stderr: []byte(stderr)}})
	return f
}

// A member whose superblock is gone makes the set degraded, not broken: mdadm
// finds one member of two and will not start the array.
func TestAssembleLostSuperblockIsDegraded(t *testing.T) {
	for _, c := range []struct{ name, examine, stderr string }{
		{"fewer members than raid devices", "   Raid Devices : 2\n", ""},
		{"mdadm says it needs all", "", "mdadm: /dev/md/os-boot assembled from 1 drive - need all 2 to start it (use --run to insist).\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, degraded := assembleExplicit(context.Background(), lostSuperblock(c.examine, c.stderr), "os-boot",
				[]string{"/dev/vda", "/dev/vdb"}, func(string, ...string) {}, nil, false)
			if !degraded {
				t.Fatal("a mirror missing one member's superblock was not reported degraded")
			}
		})
	}
}

// dirtyRaid5WithSpare is a raid5 of four plus a hot spare after an unclean
// shutdown, with one active member's disk gone: three actives and the spare
// are found, so the members match Raid Devices and only mdadm's refusal says
// the array is short.
func dirtyRaid5WithSpare(stderr string) *executor.FakeRunner {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	for _, d := range []string{"vda", "vdb", "vdc", "vdd"} {
		f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-ln", "-o", "KNAME,TYPE", "/dev/" + d},
			Response: executor.FakeResponse{Stdout: []byte(d + " disk\n" + d + "2 part\n")}})
		f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--examine", "/dev/" + d + "2"},
			Response: executor.FakeResponse{Stdout: []byte("           Name : any:os-root  (local to host any)\n   Raid Devices : 4\n")}})
	}
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--assemble"},
		Response: executor.FakeResponse{ExitCode: 1, Err: fmt.Errorf("exit status 1"), Stderr: []byte(stderr)}})
	return f
}

// A present spare must not make a dirty, member-short raid5 look complete:
// mdadm refuses it with "not clean", never "need all", and the set is
// degraded, not broken.
func TestAssembleDirtyRaid5WithSpareIsDegraded(t *testing.T) {
	for _, c := range []struct {
		name   string
		start  bool
		stderr string
	}{
		{"without --run", false, "mdadm: /dev/md/os-root assembled from 3 drives and 1 spare - not enough to start the array while not clean - consider --force.\n"},
		{"with --run", true, "mdadm: Not enough devices to start the array while not clean - consider --force.\n"},
	} {
		t.Run(c.name, func(t *testing.T) {
			_, degraded := assembleExplicit(context.Background(), dirtyRaid5WithSpare(c.stderr), "os-root",
				[]string{"/dev/vda", "/dev/vdb", "/dev/vdc", "/dev/vdd"}, func(string, ...string) {}, nil, c.start)
			if !degraded {
				t.Fatal("a dirty raid5 one active member short, spare present, was not reported degraded")
			}
		})
	}
}
