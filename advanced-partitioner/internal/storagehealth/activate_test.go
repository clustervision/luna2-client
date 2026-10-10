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
	assembleExplicit(context.Background(), fake, "data", []string{"/dev/vdb", "/dev/vdc"}, run, logger)
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
	assembleExplicit(context.Background(), fake, "data", []string{"/dev/vdb"}, run, logger)
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
