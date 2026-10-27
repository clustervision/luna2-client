package result

import (
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	fsmod "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/fs"
)

// The fallback install root follows $rootmnt (Ubuntu: /root), like the mounter.
func TestAssembleFallbackRootFollowsRootmnt(t *testing.T) {
	t.Setenv("rootmnt", "/root")
	set := &v2.Set{Name: "os", Role: "os", Volumes: []v2.Volume{
		{Name: "root", Mountpoint: "/", FS: "tmpfs", Provider: "memory", Options: "defaults"},
	}}
	got := Assemble(AssembleInput{Set: set, Filesystems: fsmod.MemoryFilesystems(set)})
	if got.MountedTargetRoot != "/root" {
		t.Fatalf("MountedTargetRoot = %q, want /root", got.MountedTargetRoot)
	}
}
