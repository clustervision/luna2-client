package cleanup

import (
	"context"
	"strings"
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
)

func TestSplitDMName(t *testing.T) {
	cases := []struct {
		in, vg, lv string
		ok         bool
	}{
		{"data-root", "data", "root", true},
		{"data--2-root", "data-2", "root", true},
		{"my----vg-lv", "my--vg", "lv", true},
		{"my--vg-lv", "my-vg", "lv", true},
		{"os-swap--a", "os", "swap-a", true},
		{"os-root-real", "os", "root-real", true},
		{"mpatha", "", "", false},
		{"only--escaped", "", "", false},
		{"-x", "", "", false},
	}
	for _, c := range cases {
		vg, lv, ok := splitDMName(c.in)
		if ok != c.ok || vg != c.vg || lv != c.lv {
			t.Errorf("splitDMName(%q) = (%q, %q, %v), want (%q, %q, %v)", c.in, vg, lv, ok, c.vg, c.lv, c.ok)
		}
	}
}

// dmsetupMappersForVG must match a VG's mappings exactly: VG "data" must not
// catch "data--2-root" (VG "data-2", possibly persistent), and a VG with
// hyphens must find its own escaped mappings.
func TestDMSetupMappersForVGHonoursEscaping(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte(
			"data-root\t(253:0)\ndata--2-root\t(253:1)\ndata--2-log\t(253:2)\nmy----vg-lv\t(253:3)\nmpatha\t(253:4)\n")}})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	for vg, want := range map[string]string{
		"data":   "data-root",
		"data-2": "data--2-log,data--2-root",
		"my--vg": "my----vg-lv",
		"my":     "",
	} {
		got := c.dmsetupMappersForVG(context.Background(), vg)
		stringsSort(got)
		if strings.Join(got, ",") != want {
			t.Errorf("mappers for VG %q = %v, want %s", vg, got, want)
		}
	}
}

// Tearing down VG "data" must never dmsetup-remove VG "data-2"'s mappings.
func TestTeardownVGLeavesHyphenatedNeighbourAlone(t *testing.T) {
	f := executor.NewFakeRunner(executor.FakeResponse{})
	f.AddMatch(executor.FakeMatch{Path: "dmsetup", ArgsPrefix: []string{"ls"},
		Response: executor.FakeResponse{Stdout: []byte("data-root\t(253:0)\ndata--2-root\t(253:1)\n")}})
	f.AddMatch(executor.FakeMatch{Path: "vgs", ArgsPrefix: []string{"data"}, Response: executor.FakeResponse{Err: errFail()}})
	c := New(Options{Runner: f, CommandAvailable: alwaysAvailable, Sleep: noSleep})
	if err := c.teardownVG(context.Background(), "data", nil); err != nil {
		t.Fatalf("teardownVG: %v", err)
	}
	sawOwn := false
	for _, call := range f.Calls() {
		joined := call.Path + " " + strings.Join(call.Args, " ")
		if strings.Contains(joined, "data--2-root") && call.Path == "dmsetup" {
			t.Fatalf("VG data teardown touched VG data-2's mapping: %s", joined)
		}
		if joined == "dmsetup remove --retry -f data-root" {
			sawOwn = true
		}
	}
	if !sawOwn {
		t.Fatal("VG data's own mapping data-root was not removed")
	}
}
