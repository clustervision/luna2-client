package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lunaclient"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
)

// fixedNow gives the write-back a deterministic origin timestamp.
func fixedNow() time.Time { return time.Date(2026, 6, 7, 12, 0, 0, 0, time.UTC) }

// discoverSaveCfg is a single os set, selection=discover, save=true.
func discoverSaveCfg() *v2.Config {
	return &v2.Config{Version: v2.SchemaVersion, Sets: []v2.Set{{
		Name: "os", Role: "os", Selection: "discover", Raid: "1", Count: 2, Save: true,
		Volumes: []v2.Volume{
			{Name: "uefi", Mountpoint: "/boot/efi", FS: "vfat", Size: "600M", Provider: "partition"},
			{Name: "root", Mountpoint: "/", FS: "xfs", Size: "100%", Provider: "lvm"},
		},
	}}}
}

func resolvedOS() []result.ResolvedSet {
	return []result.ResolvedSet{{
		Name: "os", Role: "os",
		SelectedDisks: []selector.Resolved{
			{ConfiguredPath: "/dev/sda", ResolvedPath: "/dev/sda"},
			{ConfiguredPath: "/dev/sdb", ResolvedPath: "/dev/sdb"},
		},
	}}
}

// cmdlineFile writes a fake /proc/cmdline with luna.url / luna.node.
func cmdlineFile(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, "cmdline")
	if err := os.WriteFile(p, []byte("root=luna luna.url=https://ctrl:7050 luna.node=node001\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

// recorder is an injectable pin func that records what it was called with.
type recorder struct {
	calls  int
	params lunaclient.PinParams
}

func (r *recorder) pin(_ context.Context, p lunaclient.PinParams) error {
	r.calls++
	r.params = p
	return nil
}

func baseParams(t *testing.T, cfg *v2.Config, rec *recorder) writeBackParams {
	return writeBackParams{
		cfg: cfg, resolved: resolvedOS(), dryRun: false,
		cmdlinePath: cmdlineFile(t), tokenOverride: "tok-abc", insecure: true,
		pin: rec.pin, now: fixedNow, logger: silentLogger(t),
	}
}

func TestWriteBackHappyPath(t *testing.T) {
	rec := &recorder{}
	p := baseParams(t, discoverSaveCfg(), rec)
	if err := maybeWriteBack(context.Background(), p); err != nil {
		t.Fatalf("maybeWriteBack: %v", err)
	}
	if rec.calls != 1 {
		t.Fatalf("expected exactly one pin call, got %d", rec.calls)
	}
	if rec.params.Node != "node001" || rec.params.Token != "tok-abc" {
		t.Errorf("pin params: node=%q token=%q", rec.params.Node, rec.params.Token)
	}
	if rec.params.Endpoint() != "https://ctrl:7050/config/node/node001" {
		t.Errorf("endpoint: %s", rec.params.Endpoint())
	}
	// The body must be the resolved manual layout with an origin stamp.
	if !strings.Contains(string(rec.params.Disklayout), `"selection":"manual"`) ||
		!strings.Contains(string(rec.params.Disklayout), `"origin"`) ||
		!strings.Contains(string(rec.params.Disklayout), `"host":"node001"`) {
		t.Errorf("pinned body missing manual/origin/host: %s", rec.params.Disklayout)
	}
}

func TestWriteBackNoSaveIsNoop(t *testing.T) {
	rec := &recorder{}
	cfg := discoverSaveCfg()
	cfg.Sets[0].Save = false // discover, but not asked to be saved
	if err := maybeWriteBack(context.Background(), baseParams(t, cfg, rec)); err != nil {
		t.Fatalf("maybeWriteBack: %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("save=false must not pin; got %d calls", rec.calls)
	}
}

// A pinned node reads back a selection=manual layout. manual sets never carry
// save, so the write-back must not fire again — re-entrancy by construction.
func TestWriteBackManualIsReentrant(t *testing.T) {
	rec := &recorder{}
	cfg := &v2.Config{Version: v2.SchemaVersion, Sets: []v2.Set{{
		Name: "os", Role: "os", Selection: "manual", Raid: "1",
		Devices: []string{"/dev/sda", "/dev/sdb"},
		Origin:  &v2.Origin{From: "discover", At: "2026-06-07T00:00:00Z", By: "x", Host: "node001"},
		Volumes: discoverSaveCfg().Sets[0].Volumes,
	}}}
	if err := maybeWriteBack(context.Background(), baseParams(t, cfg, rec)); err != nil {
		t.Fatalf("maybeWriteBack: %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("a pinned manual layout must not re-write; got %d calls", rec.calls)
	}
}

func TestWriteBackDryRunSendsNothing(t *testing.T) {
	rec := &recorder{}
	p := baseParams(t, discoverSaveCfg(), rec)
	p.dryRun = true
	if err := maybeWriteBack(context.Background(), p); err != nil {
		t.Fatalf("dry-run must not error: %v", err)
	}
	if rec.calls != 0 {
		t.Fatalf("dry-run must not pin; got %d calls", rec.calls)
	}
}

func TestWriteBackMissingCmdlineIsError(t *testing.T) {
	rec := &recorder{}
	p := baseParams(t, discoverSaveCfg(), rec)
	p.cmdlinePath = filepath.Join(t.TempDir(), "absent") // no luna.url/luna.node
	if err := maybeWriteBack(context.Background(), p); err == nil {
		t.Fatal("missing luna.url must return an error (caller logs it non-fatally)")
	}
	if rec.calls != 0 {
		t.Errorf("must not pin when cmdline is missing")
	}
}

func TestWriteBackMissingTokenIsError(t *testing.T) {
	rec := &recorder{}
	p := baseParams(t, discoverSaveCfg(), rec)
	p.tokenOverride = ""                                        // force a token lookup
	p.tokenSource = filepath.Join(t.TempDir(), "no-install-sh") // which will fail
	if err := maybeWriteBack(context.Background(), p); err == nil {
		t.Fatal("missing token must return an error")
	}
}

// A pin failure (daemon/transport) is surfaced for main to log; non-fatal there.
func TestWriteBackPinErrorIsReturned(t *testing.T) {
	p := baseParams(t, discoverSaveCfg(), &recorder{})
	p.pin = func(context.Context, lunaclient.PinParams) error {
		return errForcedPinFailure
	}
	if err := maybeWriteBack(context.Background(), p); err == nil {
		t.Fatal("a pin failure must surface as an error")
	}
}

var errForcedPinFailure = errPin("forced")

type errPin string

func (e errPin) Error() string { return string(e) }
