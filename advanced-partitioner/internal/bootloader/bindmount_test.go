package bootloader

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func TestBindMountSysrootDryRunCreatesNothing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sysroot")
	r := executor.New(executor.Options{DryRun: true})
	if err := BindMountSysroot(context.Background(), r, root, func(string) bool { return false }); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(root); err == nil {
		t.Fatal("dry-run created directories under the sysroot")
	}
}
