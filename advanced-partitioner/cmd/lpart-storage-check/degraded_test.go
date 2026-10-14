package main

import (
	"context"
	"strings"
	"testing"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

// install_mode=local with one member of a mirror gone: the missing member is
// reported separately (the set may run degraded, as lpart-storage-prepare
// allows) instead of failing the whole set's device resolution.
func TestResolvePresentSeparatesMissingMembers(t *testing.T) {
	const present = "/dev/null"
	const absent = "/dev/lpart-test-absent-member"
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "TYPE", present},
		Response: executor.FakeResponse{Stdout: []byte("disk")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-o", "RO", present},
		Response: executor.FakeResponse{Stdout: []byte("0")}})
	f.AddMatch(executor.FakeMatch{Path: "lsblk", ArgsPrefix: []string{"-dn", "-b", "-o", "SIZE", present},
		Response: executor.FakeResponse{Stdout: []byte("17179869184")}})
	r := selector.New(selector.Options{Runner: f, IsBlockDevice: func(p string) bool { return p == present }})

	got, missing, err := resolvePresent(context.Background(), r, []string{absent, present})
	if err != nil {
		t.Fatalf("resolvePresent: %v", err)
	}
	if len(got) != 1 || got[0].ConfiguredPath != present {
		t.Fatalf("present = %+v, want only %s", got, present)
	}
	if len(missing) != 1 || missing[0] != absent {
		t.Fatalf("missing = %v, want [%s]", missing, absent)
	}

	if _, _, err := resolvePresent(context.Background(), r, []string{absent}); err == nil {
		t.Fatal("a set with none of its devices present must fail")
	}
}

// install_mode=local parks on a degraded set unless the set allows it.
func TestDegradedRefusalFollowsAllowDegraded(t *testing.T) {
	set := &v2.Set{Name: "os", Raid: "1", Devices: []string{"/dev/vda", "/dev/vdb"}}
	if msg := degradedRefusal(set, 1, []string{"/dev/vdb"}); !strings.Contains(msg, "is degraded") {
		t.Fatalf("missing member without allow_degraded must refuse, got %q", msg)
	}
	set.AllowDegraded = true
	if msg := degradedRefusal(set, 1, []string{"/dev/vdb"}); msg != "" {
		t.Fatalf("allow_degraded must let the set run degraded, got %q", msg)
	}
	set.AllowDegraded = false
	if msg := degradedRefusal(set, 0, []string{"/dev/vda", "/dev/vdb"}); msg != "" {
		t.Fatalf("a set that cannot run at all is not a degraded refusal, got %q", msg)
	}
}
