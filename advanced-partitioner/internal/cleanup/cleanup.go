package cleanup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	txlog "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/log"
)

// Set is the subset of v2.Set the cleanup cascade needs. lpart-storage-prepare
// builds one of these per selected set from the typed v2 config + the
// disk/selector.Resolved records.
type Set struct {
	// Name is the set name; also the LVM VG name (unless provider.vg_name
	// overrides it, in which case the override goes here too).
	Name string
	// SelectedDisks is the list of resolved whole-disk paths, e.g.
	// "/dev/sda", "/dev/disk/by-path/pci-0000:00:1f.2-ata-1.0" (the
	// selector resolves both to the kernel path before cleanup runs).
	SelectedDisks []string
	// OwnsSysroot is true when this set provides the root filesystem
	// (i.e. has a volume with mountpoint "/"). When false, the per-set
	// cleanup does not unmount /sysroot itself — only targets strictly
	// under /sysroot/ — so a data set cannot tear down an OS set's root
	// mount during a multi-set run. The orchestrator-level
	// `unmount_sysroot` stage in lpart-storage-prepare handles stale
	// /sysroot state from prior full runs before any per-set cleanup
	// fires, so leaving /sysroot alone here is safe.
	OwnsSysroot bool
	// Persistent is the operator-pinned hands-off flag
	// (v2.Safety.Persistent). When true, CleanupSelectedSet logs and
	// returns immediately: no probes, no lsblk, no findmnt, nothing
	// reaches the set's devices. This is a hard safety property; tests
	// assert zero commands run against a persistent set's devices.
	Persistent bool
	// Protected is the set of disk basenames belonging to PERSISTENT sets
	// (built by the orchestrator from the resolved config). It is an
	// independent barrier under every destructive step: cleanup REFUSES
	// (hard error → install halts) when the selected disks intersect it,
	// or when a VG/md array under teardown has any member on a protected
	// disk — whatever the selection logic upstream decided. Tests pin this.
	Protected map[string]struct{}
}

// ErrProtected is matched (errors.Is) by every refusal of the protected-disk
// barrier: the work would touch a persistent set's disk. Such a refusal is
// deterministic -- the same layout refuses identically on every retry -- so
// callers map it to the exit-2 safety refusal, never to a retryable failure.
var ErrProtected = errors.New("protected-disk barrier")

// ProtectedError is one protected-disk barrier refusal; it matches
// ErrProtected.
type ProtectedError struct{ Msg string }

func (e *ProtectedError) Error() string { return e.Msg }

// Is makes errors.Is(err, ErrProtected) true for every barrier refusal.
func (e *ProtectedError) Is(target error) bool { return target == ErrProtected }

func protectedf(format string, args ...any) error {
	return &ProtectedError{Msg: fmt.Sprintf(format, args...)}
}

// Result is the typed equivalent of the {"status":"ok"} JSON the Bash
// cleanup_selected_set wrote to its output file.
type Result struct {
	Status string `json:"status"`
}

// Options configures a Cleaner.
type Options struct {
	Runner executor.Runner
	Logger *txlog.Logger

	// Sysroot is the install target root; defaults to /sysroot. Live
	// mount targets under this path are unmounted in step 1.
	Sysroot string

	// CommandAvailable answers "is <cmd> on PATH?". Defaults to
	// exec.LookPath. dmsetup, partprobe, and mdadm are probed at run
	// time because not every installer image carries all three.
	CommandAvailable func(string) bool

	// Sleep is invoked in the partition-table-settled retry loop and
	// in the inter-step udevadm-settle waits. Defaults to time.Sleep;
	// tests inject a no-op.
	Sleep func(time.Duration)

	// MDStatPath defaults to /proc/mdstat. Tests point this at a
	// temporary file with a synthetic body.
	MDStatPath string

	// DevDir defaults to /dev. Used only for the `rm -rf /dev/<vg>`
	// cleanup at the end of TeardownVG so tests can verify the
	// invocation against a temp path without ever touching real /dev.
	DevDir string

	// MDAdmDetail is an optional override for "given an md device path,
	// return its member device list". Defaults to running
	// `mdadm --detail <md>` via the Runner. Tests inject a stub.
	MDAdmDetail func(ctx context.Context, mdDevice string) ([]string, error)

	// Stat is the /dev existence probe used by stopMDIfExists. Defaults to
	// os.Stat. Tests MUST inject one: with the real os.Stat a unit test's
	// behaviour depends on the HOST's /dev/md/* — a leftover forensics
	// array on the dev box made the already-clean re-entrancy test issue a
	// real `mdadm --stop` for days before anyone noticed.
	Stat func(string) (os.FileInfo, error)
}

// Cleaner is the public type. Construct via New.
type Cleaner struct {
	o Options
}

// New returns a Cleaner with sensible defaults filled in.
func New(o Options) *Cleaner {
	if o.Sysroot == "" {
		o.Sysroot = "/sysroot"
	}
	if o.CommandAvailable == nil {
		o.CommandAvailable = defaultCommandAvailable
	}
	if o.Sleep == nil {
		o.Sleep = time.Sleep
	}
	if o.MDStatPath == "" {
		o.MDStatPath = "/proc/mdstat"
	}
	if o.Stat == nil {
		o.Stat = os.Stat
	}
	if o.DevDir == "" {
		o.DevDir = "/dev"
	}
	c := &Cleaner{o: o}
	if o.MDAdmDetail == nil {
		c.o.MDAdmDetail = c.defaultMDAdmDetail
	}
	return c
}

func defaultCommandAvailable(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

// CleanupSelectedSet runs the 5-step cascade in order. Errors from
// individual steps are logged via the supplied Logger but do not stop
// the cascade; the only error CleanupSelectedSet returns is a
// preflight failure (Runner missing, etc.).
func (c *Cleaner) CleanupSelectedSet(ctx context.Context, set Set) (Result, error) {
	if c.o.Runner == nil {
		return Result{}, fmt.Errorf("cleanup: Runner is required")
	}
	if set.Persistent {
		if c.o.Logger != nil {
			c.o.Logger.Info("persistent set: cleanup cascade skipped entirely",
				"set", set.Name,
				"disk_count", len(set.SelectedDisks))
		}
		return Result{Status: "skipped_persistent"}, nil
	}
	if c.o.Logger != nil {
		c.o.Logger.Info("cleanup: starting",
			"set", set.Name,
			"disk_count", len(set.SelectedDisks))
	}

	selected := SelectedDisksSet(set.SelectedDisks)

	// Protected-disk barrier (FTA BR-2): refuse outright if any selected
	// disk is a persistent set's disk. This is deliberately independent of
	// the selection/reservation logic upstream — if that logic is ever
	// wrong, this is the last barrier before irreversible destruction.
	for disk := range selected {
		if _, hit := set.Protected[disk]; hit {
			return Result{}, protectedf(
				"cleanup: selected disk %s belongs to a persistent set — refusing to touch it (protected-disk barrier)", disk)
		}
	}

	c.unmountUnderSysroot(ctx, set.Name, set.OwnsSysroot, selected)
	if err := c.teardownVG(ctx, set.Name, selected, set.Protected); err != nil {
		return Result{}, err
	}
	if err := c.teardownSameDiskVGs(ctx, selected, set.Protected); err != nil {
		return Result{}, err
	}
	if err := c.stopMDArrays(ctx, set.Name, selected, set.Protected); err != nil {
		return Result{}, err
	}
	c.clearZFSLabels(ctx, set.SelectedDisks)
	c.wipePartitionsAndDisks(ctx, set.SelectedDisks)

	return Result{Status: "ok"}, nil
}

// runBest executes a command via the runner; failure is logged but
// nothing is propagated. Mirrors _run_cleanup_cmd: every cleanup step
// is best-effort because we're tearing down possibly-broken state.
// Callers that need the success/failure outcome use runOK instead.
func (c *Cleaner) runBest(ctx context.Context, path string, args ...string) {
	if _, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          path,
		Args:          args,
		CaptureStdout: true,
		CaptureStderr: true,
	}); err != nil {
		c.warn("cleanup cmd failed: %s %v: %v", path, args, err)
	}
}

// runOK is runBest's value-returning sibling for callers that need
// stdout (e.g. findmnt's mount-target list).
func (c *Cleaner) runOK(ctx context.Context, path string, args ...string) (executor.CommandResult, bool) {
	res, err := c.o.Runner.Run(ctx, executor.CommandSpec{
		Path:          path,
		Args:          args,
		CaptureStdout: true,
		CaptureStderr: true,
	})
	return res, err == nil
}

func (c *Cleaner) log(format string, args ...any) {
	if c.o.Logger != nil {
		c.o.Logger.Info(fmt.Sprintf(format, args...))
	}
}

func (c *Cleaner) warn(format string, args ...any) {
	if c.o.Logger != nil {
		c.o.Logger.Warn(fmt.Sprintf(format, args...))
	}
}
