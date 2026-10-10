package storagehealth

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

const mdDetailDegraded = `/dev/md/os-boot:
           Version : 1.0
        Raid Level : raid1
      Raid Devices : 2
     Total Devices : 1
             State : clean, degraded
    Active Devices : 1
   Working Devices : 1
`

const mdDetailClean = `/dev/md/os-boot:
      Raid Devices : 2
             State : clean
    Active Devices : 2
`

func logText(t *testing.T, detail string) string {
	t.Helper()
	logFile := filepath.Join(t.TempDir(), "lpart.log")
	logger, err := txlog.Init(txlog.Options{LogFile: logFile})
	if err != nil {
		t.Fatal(err)
	}
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "mdadm", ArgsPrefix: []string{"--detail", "/dev/md/os-boot"},
		Response: executor.FakeResponse{Stdout: []byte(detail)}})
	warnIfDegraded(context.Background(), f, "os", "os-boot", logger)
	_ = logger.Close()
	b, _ := os.ReadFile(logFile)
	return string(b)
}

// Lab, RHEL 8 raid1 os set: one /boot member lost its md superblock; the
// array ran on the other member and sync went on without a word.
func TestWarnIfDegraded(t *testing.T) {
	if got := logText(t, mdDetailDegraded); !strings.Contains(got, "array os-boot runs DEGRADED (1 of 2 members active") {
		t.Fatalf("no degraded warning in the log:\n%s", got)
	}
	if got := logText(t, mdDetailClean); strings.Contains(got, "DEGRADED") {
		t.Fatalf("warning for a clean array:\n%s", got)
	}
}
