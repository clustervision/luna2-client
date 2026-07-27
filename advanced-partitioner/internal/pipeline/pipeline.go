// Package pipeline runs a built-in install phase (pre/part/post) directly in
// the lpart-node-installer process, driving the lpart-* binaries via
// engine.RunCommand and emitting the same typed events the TUI already
// renders. It replaces the bash orchestration that used to live in the Luna
// pre/part/post scripts, so those scripts collapse to a one-line exec.
//
// Under the v1.4 model the single install_mode enum drives everything
// (decisions/0004): lpart-storage-prepare always runs and dispatches on the mode
// internally (the decision engine), then the OS image is extracted
// (lpart-osimage-install) for the image modes, verified in place
// (lpart-storage-check) for local, or skipped for sanitize; the bootloader
// finalise runs in the post phase only for the disk-based OS modes.
package pipeline

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lpartinputs"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// Phase selects which built-in sequence to run.
type Phase string

const (
	PhasePre  Phase = "pre"
	PhasePart Phase = "part"
	PhasePost Phase = "post"
)

// Options configures a Pipeline. Zero values fall back to the install-env
// defaults.
type Options struct {
	Phase      Phase
	BinDir     string // where the lpart-* binaries live (default /usr/bin)
	RuntimeDir string // tx_* JSON + results (default /tmp/install_files/runtime)
	LogDir     string // per-binary logs (default /tmp/install_files/logs)
	NodeName   string // --expect-host (default $_HOSTNAME or short hostname)
	// StepTimeout bounds each lpart-* sub-tool invocation. A wedged tool
	// (kernel I/O stall, hung mount) previously hung the phase forever
	// (FTA D9); with the bound it becomes a normal, retryable failure.
	// Defaults to 2h — generous enough for a slow image extract plus
	// filesystem work, far below "operator finds the node next morning".
	StepTimeout time.Duration
}

// FailureClass classifies a phase failure for the retry-vs-park decision
// in the boot wrapper (see luna2-start.sh LUNA_FATAL_RC).
type FailureClass int

const (
	// FailRetryable is a transient/operational failure — worth a retry.
	FailRetryable FailureClass = iota
	// FailFatal will fail identically on every retry: a safety refusal
	// (exit 2 from a sub-tool — data fence, persistent protection) or a
	// non-retryable configuration error (exit 3). The wrapper must PARK
	// for an operator instead of retrying forever.
	FailFatal
)

// Fatal reports whether the class is non-retryable. The renderers read it
// through a small interface assertion (no package coupling) to map
// EvInstallerFailed to exit 3 vs 1.
func (c FailureClass) Fatal() bool { return c == FailFatal }

// Result is the pipeline's authoritative terminal outcome. It is recorded
// on the Pipeline itself BEFORE the event channel closes, so a dropped or
// unread terminal event can never change the process exit code (the event
// stream is display-only). FTA Cluster B (B2): the previous design derived
// the exit code from the lossy event bus.
type Result struct {
	// Done is true when the phase reached a terminal state (success or
	// failure). A false Done after the channel closed means the pipeline
	// was interrupted — callers must treat that as failure.
	Done    bool
	Failed  bool
	Class   FailureClass
	Message string
}

// Pipeline drives one phase and exposes an engine event channel the TUI
// consumes (the TUI drains it just like any engine event source).
type Pipeline struct {
	opts         Options
	ctx          context.Context
	events       chan engine.InstallerEvent
	disks        []string      // physical disk names from emitStorage
	storageState model.Storage // last emitted storage, for re-emit with InUse/Method
	methodLine   string        // config-derived Method (selection + counts), shown from resolve onward

	resultMu sync.Mutex
	result   Result
}

// Result returns the terminal outcome recorded by run(). Callers decide the
// process exit code from THIS, never from the rendered event stream.
func (p *Pipeline) Result() Result {
	p.resultMu.Lock()
	defer p.resultMu.Unlock()
	return p.result
}

func (p *Pipeline) setResult(r Result) {
	p.resultMu.Lock()
	p.result = r
	p.resultMu.Unlock()
}

// stepError carries a sub-tool's numeric exit code through the pipeline so
// the failure class survives to the process exit (FTA Cluster B, B1: the
// code used to be flattened into a string).
type stepError struct {
	Bin   string
	Stage string
	Code  int
}

func (e *stepError) Error() string {
	return fmt.Sprintf("%s exited %d", e.Bin, e.Code)
}

// classify maps a phase error to its FailureClass. Sub-tool exit 2 is the
// repo-wide safety_refusal contract (cli.ExitSafetyRefusal: data fence,
// persistent protection, device overlap) and exit 3 a non-retryable config
// error — both fail identically on every retry, so both are fatal.
func classify(err error) FailureClass {
	var se *stepError
	if errors.As(err, &se) && (se.Code == 2 || se.Code == 3) {
		return FailFatal
	}
	return FailRetryable
}

// New builds a Pipeline with defaults filled in.
func New(ctx context.Context, opts Options) *Pipeline {
	if opts.BinDir == "" {
		opts.BinDir = "/usr/bin"
	}
	if opts.RuntimeDir == "" {
		opts.RuntimeDir = "/tmp/install_files/runtime"
	}
	if opts.LogDir == "" {
		opts.LogDir = "/tmp/install_files/logs"
	}
	if opts.NodeName == "" {
		opts.NodeName = defaultNodeName()
	}
	if opts.StepTimeout <= 0 {
		opts.StepTimeout = 2 * time.Hour
	}
	return &Pipeline{opts: opts, ctx: ctx, events: make(chan engine.InstallerEvent, 1024)}
}

// Events returns the read-only event channel, closed when the phase ends.
func (p *Pipeline) Events() <-chan engine.InstallerEvent { return p.events }

// Stages returns the progress list for the configured phase, seeded into the
// initial TUI snapshot so the panel shows the steps up front.
func (p *Pipeline) Stages() []model.Stage { return stagesFor(p.opts.Phase) }

// Start launches the phase goroutine. It returns immediately.
func (p *Pipeline) Start() error {
	go p.run()
	return nil
}

func (p *Pipeline) run() {
	defer close(p.events)
	var err error
	switch p.opts.Phase {
	case PhasePre:
		err = p.runPre()
	case PhasePart:
		err = p.runPart()
	case PhasePost:
		err = p.runPost()
	default:
		err = fmt.Errorf("unknown phase %q", p.opts.Phase)
	}
	if err != nil {
		class := classify(err)
		// Record the authoritative outcome FIRST — the emit below is
		// display-only and rides a lossy drop-oldest bus.
		p.setResult(Result{Done: true, Failed: true, Class: class, Message: err.Error()})
		p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvInstallerFailed, Stream: engine.StreamSystem, Message: err.Error(), Data: class})
		return
	}
	p.setResult(Result{Done: true})
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvInstallerDone, Stream: engine.StreamSystem, Message: string(p.opts.Phase) + " phase complete"})
}

// --- phases ---------------------------------------------------------------

func (p *Pipeline) runPre() error {
	// First installer phase: announce ourselves and dump the kernel ring
	// buffer (boot + dracut) so the controller log opens with what happened
	// before lpart-installer took over, then live progress follows.
	p.info("================================================================")
	p.info(fmt.Sprintf("TrinityX node installer (v1.4) -- phase pre on %s", p.opts.NodeName))
	p.info("capturing boot/dracut kernel log, then preparing the install env")
	p.info("================================================================")
	p.dumpKernel()

	p.stageStart("prepare-runtime", "Prepare runtime")
	if err := os.MkdirAll(p.opts.RuntimeDir, 0o750); err != nil {
		p.stageFail("prepare-runtime", err.Error())
		return err
	}
	if err := os.MkdirAll(p.opts.LogDir, 0o750); err != nil {
		p.stageFail("prepare-runtime", err.Error())
		return err
	}
	p.stageDone("prepare-runtime", "")
	return nil
}

func (p *Pipeline) runPart() error {
	// Populate the Node + Storage dashboard panels.
	p.emitNode()
	p.emitStorage()

	// 1. Resolve tx_* inputs (daemon -> files) and validate the context file.
	p.stageStart("install-context", "Resolve install context")
	in, err := lpartinputs.Resolve(p.opts.RuntimeDir)
	if err != nil {
		p.stageFail("install-context", err.Error())
		return err
	}
	// lpartinputs.Resolve already wrote the canonical install-context.json; every
	// downstream lpart-* binary reads + validates it via installcontext.LoadFile.
	mode := installcontext.Mode(in.InstallMode)
	if err := mode.Validate(); err != nil {
		p.stageFail("install-context", err.Error())
		return err
	}
	p.emitLuna(in.InstallMode)
	p.stageDone("install-context", "source="+in.Source+" mode="+in.InstallMode)

	// Banner: who we are, what we see, and what we are about to do — so the
	// rsyslog stream reads as a self-describing install narrative.
	p.announce(p.ctx, "part", in.InstallMode, in.Source)

	// Show the intended method up front (selection discover/manual + per-set
	// raid and disk counts) from the declared layout, so the Storage panel says
	// what it is about to do while storage-prepare resolves, not "(resolving...)".
	if pre, ok := configLayout(p.rt("storage.json"), mode); ok {
		p.methodLine = pre.Method
		p.markLayout(pre)
	}

	// 2. Storage prepare. Always runs; the decision engine inside
	// lpart-storage-prepare dispatches on install_mode (sanitize/memboot/local/
	// full/sync/auto), resolves selection=discover disks, and enforces the data
	// fence. A sanitize run stops here (no image, no bootloader).
	p.stageStart("storage-prepare", "Prepare storage")
	args := []string{
		// --wipe authorises destructive work; the decision engine inside
		// lpart-storage-prepare still enforces the per-mode data fence on top.
		"--wipe", "--details",
		"--expect-host", p.opts.NodeName,
		"--install-context", p.rt("install-context.json"),
		"--result-out", p.rt("storage-result.json"),
		"--resolved-out", p.rt("storage-resolved.json"),
	}
	if fileHasContent(p.rt("storage.json")) {
		args = append(args, "--config", p.rt("storage.json"))
	}
	if err := p.runStep("storage-prepare", p.bin("lpart-storage-prepare"), args...); err != nil {
		p.stageFail("storage-prepare", err.Error())
		return err
	}
	// NOTE: storage.json is the INPUT config and must stay that way. An
	// earlier version copied storage-resolved.json over it "to keep the
	// resolved layout" — but storage-resolved.json is RESULT-shaped
	// (firmware_mode/selector_type/storage_type/selected_disks), not a valid
	// v2 config, so on a retry (the install tmpfs persists across
	// luna-installer.sh's 30s retry loop) the pipeline re-fed it as --config
	// and storage-prepare rejected it: "config_validation: unknown field
	// 'firmware_mode'". Found on node001 via the real PXE regression. The
	// resolved layout already lives in storage-result.json / storage-resolved
	// .json for downstream steps; storage.json is left untouched so a retry
	// re-runs from the same input config (idempotent).
	info := parseLayout(p.rt("storage-result.json"))
	if mode.IsMemboot() {
		info.MemOS = true
		if info.Method == "" {
			info.Method = "tmpfs OS · " + bootMode()
		}
	}
	// Keep the config-derived Method (selection + counts) over the result-only
	// raid summary, so the panel still says manual/discover after resolving.
	if p.methodLine != "" {
		info.Method = p.methodLine
	}
	if info.Method == "" {
		info.Method = "disk install · " + bootMode()
	}
	p.markLayout(info)
	p.stageDone("storage-prepare", "")

	if mode.IsSanitize() {
		p.info("sanitize complete; no OS image or bootloader for an erase run")
		return nil
	}

	// 3. OS image: extract (image modes), verify in place (local), or skip.
	p.stageStart("osimage", "OS image install")
	if err := p.runOSImage(mode); err != nil {
		p.stageFail("osimage", err.Error())
		return err
	}
	p.stageDone("osimage", "")
	return nil
}

func (p *Pipeline) runOSImage(mode installcontext.Mode) error {
	switch {
	case mode.IsLocal():
		return p.runStep("osimage", p.bin("lpart-storage-check"),
			"--config", p.rt("storage.json"),
			"--install-context", p.rt("install-context.json"),
			"--log-dir", p.opts.LogDir, "--details")
	case mode.ExtractsImage():
		argv := []string{p.bin("lpart-osimage-install")}
		// full (and memboot, whose RAM root is always fresh) extract with no
		// preserve filter; auto/sync honour the operator filter when present.
		if mode != installcontext.ModeFull && !mode.IsMemboot() && fileHasContent(p.rt("osimage-filter.json")) {
			argv = append(argv, "--config", p.rt("osimage-filter.json"))
		}
		argv = append(argv,
			"--install-context", p.rt("install-context.json"),
			"--storage-result", p.rt("storage-result.json"),
			"--log-dir", p.opts.LogDir, "--details")
		return p.runStepArgv("osimage", argv)
	default:
		p.info("no OS image step for install_mode=" + string(mode))
		return nil
	}
}

func (p *Pipeline) runPost() error {
	p.info("================================================================")
	p.info(fmt.Sprintf("TrinityX node installer (v1.4) -- phase post on %s (bootloader finalise)", p.opts.NodeName))
	p.info("================================================================")
	// Resolve the mode BEFORE requiring storage-result.json: sanitize (and
	// any mode that installs no bootloader) produces NO storage results by
	// design, so demanding the file first turned a successful erase into a
	// post-phase failure -> 30 s retry loop. Found live on node001 the moment
	// the sanitize exit-0 fix (NB-1) let the post phase run at all -- the
	// CCF-7 class again: a contract only exercised end-to-end.
	mode := installcontext.ModeAuto
	if ic, err := installcontext.LoadFile(p.rt("install-context.json")); err == nil {
		mode = ic.InstallMode
		p.emitLuna(string(ic.InstallMode))
	}
	p.emitNode()
	if !mode.InstallsBootloader() {
		p.stageStart("bootloader", "Bootloader finalise")
		p.info("no bootloader finalise for install_mode=" + string(mode))
		p.stageDone("bootloader", "skipped")
		return nil
	}

	result := p.rt("storage-result.json")
	if !fileHasContent(result) {
		err := fmt.Errorf("storage-result.json not found: %s", result)
		p.stageStart("bootloader", "Bootloader finalise")
		p.stageFail("bootloader", err.Error())
		return err
	}
	// Repopulate the dashboard — post is a separate process from part, so
	// Node/Luna/Storage would otherwise render empty during finalise.
	p.emitStorage()
	info := parseLayout(result)
	if pre, ok := configLayout(p.rt("storage.json"), mode); ok {
		info.Method = pre.Method
	}
	p.markLayout(info)

	p.stageStart("bootloader", "Bootloader finalise")
	if err := p.runStep("bootloader", p.bin("lpart-bootloader-finalise"),
		"--result", result,
		"--boot-context-out", p.rt("boot-context.json"),
		"--install-context", p.rt("install-context.json"),
		"--details"); err != nil {
		p.stageFail("bootloader", err.Error())
		return err
	}
	p.stageDone("bootloader", "")
	return nil
}

// --- event helpers --------------------------------------------------------

// runStep runs a lpart-* binary via engine.RunCommand under the given stage,
// streaming its output into the TUI. A non-zero exit is an error.
func (p *Pipeline) runStep(stage, path string, args ...string) error {
	return p.runStepArgv(stage, append([]string{path}, args...))
}

// runStepArgv is runStep with a pre-built argv (argv[0] is the binary). A
// non-zero exit is returned as a typed stepError so the numeric code (and
// with it the retry-vs-park class) survives to the process exit.
func (p *Pipeline) runStepArgv(stage string, argv []string) error {
	ctx, cancel := context.WithTimeout(p.ctx, p.opts.StepTimeout)
	defer cancel()
	code, err := engine.RunCommand(ctx, engine.Command{Stage: stage, Argv: argv}, p.events)
	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Errorf("%s: step exceeded the %s timeout and was killed", filepath.Base(argv[0]), p.opts.StepTimeout)
	}
	// A non-zero child exit is reported by RunCommand as BOTH a numeric code
	// AND a non-nil err (the exec.ExitError echoing that code). Build the
	// typed stepError from the CODE first, so the fatal/retry class survives.
	// Returning the raw err before this check discarded the code, so a
	// safety-refusal (exit 2 -> should map to fatal exit 3) was misclassified
	// retryable and the node retry-looped forever instead of parking — the FTA
	// Cluster B catastrophe, found by the real PXE regression (unit tests
	// exercised stepError classification but not this RunCommand seam).
	if code != 0 {
		return &stepError{Bin: filepath.Base(argv[0]), Stage: stage, Code: code}
	}
	if err != nil {
		return err // code==0 but a genuine non-exit failure
	}
	return nil
}

func (p *Pipeline) emit(ev engine.InstallerEvent) {
	select {
	case p.events <- ev:
	default:
		select {
		case <-p.events:
		default:
		}
		select {
		case p.events <- ev:
		default:
		}
	}
}

func (p *Pipeline) info(msg string) {
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvRawStdout, Stream: engine.StreamSystem, Message: msg})
}

// infoSyslog is info() for the rsyslog forward + log file only — kept out of
// the live TUI/plain console (used to frame the boot dmesg backlog).
func (p *Pipeline) infoSyslog(msg string) {
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvRawStdout, Stream: engine.StreamSystem, Message: msg, SyslogOnly: true})
}

func (p *Pipeline) stageStart(id, title string) {
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvStageStarted, Stage: id, Message: title})
}

func (p *Pipeline) stageDone(id, note string) {
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvStageDone, Stage: id, Message: note})
}

func (p *Pipeline) stageFail(id, note string) {
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvStageFailed, Stage: id, Message: note})
}

func (p *Pipeline) bin(name string) string { return filepath.Join(p.opts.BinDir, name) }
func (p *Pipeline) rt(name string) string  { return filepath.Join(p.opts.RuntimeDir, name) }

func fileHasContent(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.Size() > 0
}

// --- stage lists ----------------------------------------------------------

// stagesFor returns the WHOLE install pipeline (pre + part + post), so every
// phase's dashboard shows all the steps up front — the operator sees that the
// bootloader/GRUB finalise still comes after the OS image, not a list that
// stops at "OS image install". Each phase is its own process, so the stages of
// already-completed phases are pre-marked done; the current phase's stages
// animate live via MarkStage (matched by ID); later phases stay pending until
// their own process runs.
func stagesFor(ph Phase) []model.Stage {
	type st struct {
		phase     Phase
		id, title string
	}
	all := []st{
		{PhasePre, "prepare-runtime", "Prepare runtime"},
		{PhasePart, "install-context", "Resolve install context"},
		{PhasePart, "storage-prepare", "Prepare storage"},
		{PhasePart, "osimage", "OS image install"},
		{PhasePost, "bootloader", "Bootloader & GRUB finalise"},
	}
	rank := map[Phase]int{PhasePre: 0, PhasePart: 1, PhasePost: 2}
	out := make([]model.Stage, 0, len(all))
	for _, s := range all {
		status := model.StagePending
		if rank[s.phase] < rank[ph] {
			status = model.StageDone
		}
		out = append(out, model.Stage{ID: s.id, Title: s.title, Status: status})
	}
	return out
}

func defaultNodeName() string {
	if h := strings.TrimSpace(os.Getenv("_HOSTNAME")); h != "" {
		return h
	}
	if h, err := os.Hostname(); err == nil {
		if i := strings.IndexByte(h, '.'); i > 0 {
			return h[:i]
		}
		return h
	}
	return "unknown"
}
