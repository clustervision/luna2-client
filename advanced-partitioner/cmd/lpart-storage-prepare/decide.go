package main

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/disk/selector"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/diskresolve"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/lvm/vgselect"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/partition"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/result"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sanitize"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/storagehealth"
)

// safetyError marks a data-fence / persistent-protection halt. main.run()
// maps it to the exit-code-2 safety_refusal contract (cli.ExitSafetyRefusal)
// so the wrapper can distinguish "do NOT continue without operator review"
// from a retryable failure.
type safetyError struct {
	reason string // short machine-readable class, e.g. "data-fence"
	msg    string
}

func (e *safetyError) Error() string { return e.msg }

func safetyRefusalf(reason, format string, args ...any) error {
	return &safetyError{reason: reason, msg: fmt.Sprintf(format, args...)}
}

// fatalConfigError marks a deterministic configuration error: the same layout
// fails identically on every retry (a missing manual device, a disk too small,
// a raid count the disks cannot meet). main.run() maps it to the exit-3
// fatal_config contract (cli.ExitFatalConfig) so the wrapper parks instead of
// retrying every 30 s -- often re-wiping disks -- for ever.
type fatalConfigError struct {
	reason string // short machine-readable class, e.g. "device-missing"
	msg    string
}

func (e *fatalConfigError) Error() string { return e.msg }

func fatalConfigf(reason, format string, args ...any) error {
	return &fatalConfigError{reason: reason, msg: fmt.Sprintf(format, args...)}
}

// plannedSet is the decided work for one set. Every set is planned -- devices
// resolved, health probed, data fence applied, deterministic preflights run --
// BEFORE any set is touched, so a refusal for the second set can no longer
// come after the first set's disks were wiped.
type plannedSet struct {
	set          *v2.Set
	act          setAction
	firstInstall bool
	// disks are the set's devices present on the node, in configured order.
	disks []selector.Resolved
	// missing are configured devices absent from the node. Only a redundant
	// raid set that is mounted (not created) may have any: it runs degraded.
	missing []string
}

// runDecision implements the spec section-5 dispatch on install_mode and
// returns the per-set results. cfg may be nil/empty for memboot or for auto
// with no declared disk layout.
func (sp *setProcessor) runDecision(ctx context.Context, mode installcontext.Mode, cfg *v2.Config, firmware string, dryRun, wipe bool) ([]result.StorageResult, []result.ResolvedSet, error) {
	switch {
	case mode.IsLegacy():
		// A legacy misroute fails identically on every retry (the daemon
		// rendered the wrong template) — safety-refusal class so the
		// wrapper parks instead of looping (FTA D4).
		return nil, nil, safetyRefusalf("legacy-misroute",
			"install_mode=legacy is daemon-only and must not reach the client")
	case mode.IsSanitize():
		// Sanitize erases EVERY discovered disk. A disklayout that declares
		// a persistent set is a direct contradiction: the operator both
		// pinned data as hands-off and asked for a full erase. Refuse and
		// make a human resolve it (remove the persistent set from the
		// layout — or the layout from the node — before sanitizing).
		if cfg != nil {
			for i := range cfg.Sets {
				if cfg.Sets[i].Persistent {
					return nil, nil, safetyRefusalf("sanitize-persistent",
						"install_mode=sanitize would erase every disk, but set %q is marked persistent; refusing — remove the persistent set from the disklayout before sanitizing",
						cfg.Sets[i].Name)
				}
			}
		}
		return sp.runSanitize(ctx, dryRun)
	}

	// auto/sync/full/local/memboot. With no declared disk layout, auto falls
	// back to memboot; the other diskful modes require a layout.
	if !mode.IsMemboot() && (cfg == nil || len(cfg.Sets) == 0) {
		if mode == installcontext.ModeAuto {
			sp.logger.UserInfo("auto: no disk layout declared -- falling back to memboot (RAM root)")
			return sp.runMemboot(ctx, nil, firmware, dryRun, wipe)
		}
		return nil, nil, fatalConfigf("no-disklayout", "install_mode=%s requires a disklayout", mode)
	}
	// A diskful mode installs the OS onto the os set: without one, every data
	// set used to be created (wiped) first and then lpart-osimage-install
	// failed on an unmounted /sysroot, for ever. Refuse before anything.
	hasOS := cfg != nil && cfg.OSSet() != nil
	if !hasOS && !mode.IsMemboot() && mode != installcontext.ModeAuto {
		return nil, nil, fatalConfigf("no-os-set",
			"install_mode=%s installs the OS to disk, but the disklayout declares no os set (role=os) -- add one, or use install_mode auto or memboot for a RAM OS with these data sets; no disk was touched", mode)
	}

	// Reserve every manual set's disks up front (persistent sets are
	// manual-only, enforced by the validator): resolve them to canonical
	// kernel paths, refuse cross-set aliases, and remove them from the
	// discover pool so selection=discover can NEVER draw a disk that
	// belongs to a declared set. (FTA BR-1b, closes MCS-1/MCS-12.)
	if err := sp.reserveManualSets(ctx, cfg); err != nil {
		return nil, nil, err
	}

	if mode.IsMemboot() {
		return sp.runMemboot(ctx, cfg, firmware, dryRun, wipe)
	}
	if !hasOS {
		// auto with a data-only layout: the OS runs from RAM, exactly as with
		// no layout at all, and the declared data sets follow the auto data
		// policy (persistent -> mount, blank -> create, broken -> halt).
		sp.logger.UserInfo("auto: the disklayout declares no os set -- memboot (RAM root) plus the declared data sets")
		return sp.runMemboot(ctx, cfg, firmware, dryRun, wipe)
	}

	plans, err := sp.planSets(ctx, mode, cfg, false, firmware, dryRun, wipe)
	if err != nil {
		return nil, nil, err
	}
	return sp.executePlans(ctx, plans, firmware, dryRun)
}

// planSets plans every set (skipping the os set when skipOS) in execution
// order, then cross-checks the plans; nothing is touched.
func (sp *setProcessor) planSets(ctx context.Context, mode installcontext.Mode, cfg *v2.Config, skipOS bool, firmware string, dryRun, wipe bool) ([]plannedSet, error) {
	var plans []plannedSet
	for _, idx := range cfg.ExecutionOrder() {
		set := &cfg.Sets[idx]
		if skipOS && set.Role == "os" {
			sp.logger.UserInfo("memboot: ignoring declared os set %q (OS runs from RAM)", set.Name)
			continue
		}
		sp.logger.StageBanner(fmt.Sprintf("Planning set '%s' (install_mode=%s)", set.Name, mode))
		p, err := sp.planSet(ctx, mode, cfg, set, firmware, dryRun, wipe)
		if err != nil {
			return nil, err
		}
		plans = append(plans, p)
	}
	if err := sp.checkForeignVGs(ctx, plans); err != nil {
		return nil, err
	}
	return plans, nil
}

// executePlans runs the plans: first the identity re-check and the cleanup of
// every set that is (re)created, then each set's build or mount in execution
// order. Cleaning every create set first lets a stale VG named like one set
// but sitting on another create set's disks be wiped before vgcreate runs.
func (sp *setProcessor) executePlans(ctx context.Context, plans []plannedSet, firmware string, dryRun bool) ([]result.StorageResult, []result.ResolvedSet, error) {
	for _, p := range plans {
		if p.act != actCreate {
			continue
		}
		sp.logger.StageBanner(fmt.Sprintf("Cleaning set '%s'", p.set.Name))
		if err := sp.prepareCreate(ctx, p, dryRun); err != nil {
			return nil, nil, err
		}
	}
	results := make([]result.StorageResult, 0, len(plans))
	resolved := make([]result.ResolvedSet, 0, len(plans))
	for _, p := range plans {
		sp.logger.StageBanner(fmt.Sprintf("Processing set '%s' (%s)", p.set.Name, actionName(p.act)))
		res, rec, err := sp.process(ctx, p, firmware, dryRun)
		if err != nil {
			return nil, nil, err
		}
		results = append(results, res)
		resolved = append(resolved, rec)
	}
	return results, resolved, nil
}

// planSet decides one set's action under auto/sync/full/local: the data
// fence, the missing-device rule and the deterministic create preflights.
func (sp *setProcessor) planSet(ctx context.Context, mode installcontext.Mode, cfg *v2.Config, set *v2.Set, firmware string, dryRun, wipe bool) (plannedSet, error) {
	p := plannedSet{set: set, firstInstall: wipe && !dryRun}

	if err := sp.missingDeviceError(set); err != nil {
		return p, err
	}

	// Persistent sets are hands-off in every normal mode: mount only.
	if set.Persistent {
		sp.logger.UserInfo("set %q is persistent -- mount only (never partitioned/formatted)", set.Name)
		p.act, p.firstInstall = actMount, false
		p.disks, p.missing = sp.manualDisks[set.Name], sp.missing[set.Name]
		if err := validateRAIDCount(set); err != nil {
			return p, err
		}
		sp.warnDegraded(set, p.missing)
		return p, nil
	}

	// Resolve selection=discover disks from the discovered pool (mutates
	// set.Devices so the record shows the same disks). Failure to meet the
	// layout is a halt, never a silent fallback.
	if set.SelectionMode() == "discover" && set.StorageType() != v2.StorageMemory {
		if err := sp.resolveDiscover(set); err != nil {
			return p, err
		}
	}
	disks, missing, err := sp.setDisks(ctx, set)
	if err != nil {
		return p, err
	}
	if err := validateRAIDCount(set); err != nil {
		return p, err
	}

	// full and local need no health probe; sync/auto do.
	var verdict storagehealth.Verdict
	if mode == installcontext.ModeSync || mode == installcontext.ModeAuto {
		verdict = sp.classify(ctx, set, disks)
		sp.logger.KV("verdict", set.Name+"="+string(verdict))
	}

	if verdict == storagehealth.VerdictDegraded {
		return p, safetyRefusalf("degraded",
			"set %q is degraded: an array of the set misses a member; replace the disk and reinstall with install_mode full, or set allow_degraded on the set to install on it degraded; no disk was touched",
			set.Name)
	}
	act, halt := decideAction(mode, set.Role, false, verdict)
	// A set with absent members may only be MOUNTED, degraded, and only when
	// the array actually came up (healthy verdict, or local/full which do
	// not probe). Creating needs every device; a non-healthy degraded set
	// is the missing disk's fault, so name it: fatal config, exit 3.
	if len(missing) > 0 && (act != actMount || halt) {
		return p, fatalConfigf("device-missing",
			"set %q: device(s) %s not present on this node, and install_mode=%s (verdict %s) cannot mount the set degraded -- it would have to (re)create it, which needs every device; replace the disk or correct the disklayout; no disk was touched",
			set.Name, strings.Join(missing, ", "), mode, verdictOrNone(verdict))
	}
	if halt && verdict == storagehealth.VerdictUnknown {
		return p, safetyRefusalf("health-unknown",
			"set %q: the health probe could not decide (a filesystem checker did not run) and install_mode=%s neither mounts nor wipes an unverified set; halting -- operator review required",
			set.Name, mode)
	}
	if halt {
		return p, safetyRefusalf("data-fence",
			"set %q is %s and install_mode=%s will not wipe it (data fence); halting -- operator review required",
			set.Name, verdict, mode)
	}
	sp.logger.UserInfo("set %q: mode=%s verdict=%s -> %s", set.Name, mode, verdictOrNone(verdict), actionName(act))
	if act == actMount {
		p.firstInstall = false
		sp.warnDegraded(set, missing)
	} else if err := sp.preflightCreate(ctx, mode, cfg, set, disks, verdict, firmware); err != nil {
		return p, err
	}
	p.act, p.disks, p.missing = act, disks, missing
	return p, nil
}

func verdictOrNone(v storagehealth.Verdict) string {
	if v == "" {
		return "none"
	}
	return string(v)
}

// classify runs the health probe (test seam: classifyFn).
func (sp *setProcessor) classify(ctx context.Context, set *v2.Set, disks []selector.Resolved) storagehealth.Verdict {
	if sp.classifyFn != nil {
		return sp.classifyFn(ctx, set, disks)
	}
	v, _ := storagehealth.Classify(ctx, sp.runner, sp.checker, set, disks, sp.logger)
	return v
}

// warnDegraded logs, loudly, every member a mounted raid set runs without.
func (sp *setProcessor) warnDegraded(set *v2.Set, missing []string) {
	for _, m := range missing {
		sp.logger.Warn(fmt.Sprintf("set %s: running degraded, member %s missing; replace the disk and reinstall with install_mode full to rebuild", set.Name, m))
	}
}

// validateRAIDCount checks the set's configured member count (spares
// excluded) against its raid level. Deterministic: exit 3.
func validateRAIDCount(set *v2.Set) error {
	if set.StorageType() == v2.StorageMemory {
		return nil
	}
	if err := partition.ValidateRAID(set.RAIDMode(), len(set.EffectiveDevices())-set.SpareCount()); err != nil {
		return fatalConfigf("raid-count", "set %q: %v (%d device(s), %d spare(s))", set.Name, err, len(set.EffectiveDevices()), set.SpareCount())
	}
	return nil
}

// setDisks returns the set's present devices and the configured ones that
// are missing. Manual sets were resolved (and their missing devices judged)
// by reserveManualSets; discover sets resolve the disks the pool gave them.
func (sp *setProcessor) setDisks(ctx context.Context, set *v2.Set) ([]selector.Resolved, []string, error) {
	if set.SelectionMode() == "manual" {
		if d, ok := sp.manualDisks[set.Name]; ok {
			return d, sp.missing[set.Name], nil
		}
	}
	var disks []selector.Resolved
	for _, dev := range set.EffectiveDevices() {
		r, err := sp.resolveForReservation(ctx, []string{dev})
		if err != nil {
			if errors.Is(err, selector.ErrNotFound) && set.SelectionMode() == "discover" {
				// The pool listed it a moment ago: environmental, retry.
				return nil, nil, fmt.Errorf("set %q: discovered disk %s vanished: %w", set.Name, dev, err)
			}
			return nil, nil, classifyResolveErr(set.Name, err)
		}
		disks = append(disks, r...)
	}
	return disks, nil, nil
}

// classifyResolveErr maps a device-resolution failure onto the exit contract:
// a device absent from the node, or one that is not a usable whole disk
// (read-only, a partition, a bad path), fails identically on every retry --
// fatal config, exit 3; a mounted disk is a safety refusal, exit 2; a probe
// error may clear and stays retryable, exit 1.
func classifyResolveErr(setName string, err error) error {
	var re *selector.ResolveError
	if errors.As(err, &re) {
		switch re.Kind {
		case selector.KindNotFound:
			return fatalConfigf("device-missing", "set %q: device %s is not present on this node: %v", setName, re.Device, err)
		case selector.KindInvalid:
			return fatalConfigf("device-invalid", "set %q: device %s: %v", setName, re.Device, err)
		case selector.KindInUse:
			return safetyRefusalf("device-in-use", "set %q: device %s: %v", setName, re.Device, err)
		}
	}
	return fmt.Errorf("set %q: %w", setName, err)
}

// missingDeviceError is the error for a set with absent devices: a safety
// refusal (reason degraded) when the redundant raid set could run without them
// but the layout does not allow it, the fatal config error when it cannot run
// at all, nil when nothing is missing or allow_degraded is set (planSet then
// still refuses a create).
func (sp *setProcessor) missingDeviceError(set *v2.Set) error {
	missing := sp.missing[set.Name]
	if len(missing) == 0 {
		return nil
	}
	present := len(sp.manualDisks[set.Name])
	if set.RunsDegradedWith(present) {
		if set.AllowDegraded {
			return nil
		}
		return safetyRefusalf("degraded",
			"set %q is degraded: device(s) %s not present on this node (%d of %d present); replace the disk and reinstall with install_mode full, or set allow_degraded on the set to install on it degraded; no disk was touched",
			set.Name, strings.Join(missing, ", "), present, len(set.EffectiveDevices()))
	}
	return fatalConfigf("device-missing",
		"set %q: device(s) %s not present on this node, and raid %s cannot run without them (%d of %d device(s) present) -- replace the disk or correct the disklayout; no disk was touched",
		set.Name, strings.Join(missing, ", "), set.Raid, present, len(set.EffectiveDevices()))
}

// preflightCreate runs the deterministic checks of a set about to be
// (re)created, before any disk is touched: disk fit, and (auto/sync, on
// non-blank discovered disks) that the disks do not carry another declared
// set's array or volume group.
func (sp *setProcessor) preflightCreate(ctx context.Context, mode installcontext.Mode, cfg *v2.Config, set *v2.Set, disks []selector.Resolved, verdict storagehealth.Verdict, firmware string) error {
	if err := partition.CheckDiskFit(set, disks, firmware); err != nil {
		return fatalConfigf("disk-too-small", "set %q: %v -- no disk was touched", set.Name, err)
	}
	if set.SelectionMode() == "discover" && verdict != storagehealth.VerdictBlank &&
		(mode == installcontext.ModeAuto || mode == installcontext.ModeSync) {
		if err := sp.refuseForeignSetSignatures(ctx, cfg, set, disks); err != nil {
			return err
		}
	}
	return nil
}

// refuseForeignSetSignatures refuses to create a discover-selected set on
// disks that carry an md array or LVM volume group named after ANOTHER
// declared set: discover picked another set's disks (enumeration changed),
// and auto would wipe that set's data. A persistent set's signature gets its
// own reason.
func (sp *setProcessor) refuseForeignSetSignatures(ctx context.Context, cfg *v2.Config, set *v2.Set, disks []selector.Resolved) error {
	if cfg == nil {
		return nil
	}
	paths := diskPaths(disks)
	mdNames := storagehealth.MDNamesOnDisks(ctx, sp.runner, paths)
	vgs, err := vgselect.OnDisks(ctx, sp.runner, paths)
	if err != nil {
		// Cannot tell: never create on uncertainty over disks that are not blank.
		return fmt.Errorf("set %q: cannot list the volume groups on %v before creating: %w", set.Name, paths, err)
	}
	for i := range cfg.Sets {
		other := &cfg.Sets[i]
		if other.Name == set.Name {
			continue
		}
		reason := "foreign-set-signature"
		if other.Persistent {
			reason = "persistent-signature"
		}
		for _, v := range vgs {
			if v.Name == other.VGName() {
				return safetyRefusalf(reason,
					"set %q (selection=discover) picked disk(s) %v that carry volume group %q of set %q -- discover chose another set's disks; refusing to wipe them -- operator review required (pin the sets with selection=manual)",
					set.Name, paths, v.Name, other.Name)
			}
		}
		names := []string{other.MDName()}
		if other.Role == "os" {
			names = append(names, other.MDName()+"-efi", other.MDName()+"-boot")
		}
		for _, n := range names {
			if dev, ok := mdNames[n]; ok {
				return safetyRefusalf(reason,
					"set %q (selection=discover) picked disk(s) %v, and %s carries md array %q of set %q -- discover chose another set's disks; refusing to wipe them -- operator review required (pin the sets with selection=manual)",
					set.Name, paths, dev, n, other.Name)
			}
		}
	}
	return nil
}

// checkForeignVGs refuses, before anything is touched, a create whose VG name
// is taken by a volume group outside the set: vgcreate would fail on it after
// the set's disks were wiped, and the installer must not remove a VG it does
// not own. A stale VG that sits entirely on disks of sets this run recreates
// is fine: their cleanup wipes it before vgcreate (executePlans cleans every
// create set first).
func (sp *setProcessor) checkForeignVGs(ctx context.Context, plans []plannedSet) error {
	var createDisks []string
	for _, p := range plans {
		if p.act == actCreate {
			createDisks = append(createDisks, diskPaths(p.disks)...)
		}
	}
	for _, p := range plans {
		if p.act != actCreate || p.set.StorageType() != v2.StorageLVM {
			continue
		}
		loc, err := vgselect.Locate(ctx, sp.runner, p.set.VGName(), diskPaths(p.disks))
		if err != nil || len(loc.Foreign) == 0 {
			continue // the lvm builder re-checks right before vgcreate
		}
		wiped, err := vgselect.StackedKernelNames(ctx, sp.runner, createDisks)
		if err != nil {
			wiped = nil
		}
		for _, f := range loc.Foreign {
			if f.OnlyOn(wiped) {
				continue
			}
			return safetyRefusalf("foreign-vg", "set %q: %v; no disk was touched", p.set.Name,
				&lvm.ForeignVGError{VG: f.Name, UUID: f.UUID, PVs: f.PVs})
		}
	}
	return nil
}

func diskPaths(disks []selector.Resolved) []string {
	out := make([]string, 0, len(disks))
	for _, d := range disks {
		out = append(out, d.ResolvedPath)
	}
	return out
}

// decideAction is the data fence as a pure function: given the mode, the set's
// role, whether it is persistent, and (for sync/auto) the health verdict, it
// returns the action to take or halt=true when the fence forbids wiping.
//
//	persistent          -> mount (hands-off, any mode)
//	full                -> create (wipe; verdict ignored)
//	local               -> mount (verify/boot existing; verdict ignored)
//	sync,  healthy       -> mount (refresh + preserve, no wipe)
//	sync,  blank         -> create
//	sync,  broken        -> HALT (never wipe)
//	auto,  healthy       -> mount (refresh OS set)
//	auto,  blank         -> create
//	auto,  broken, os    -> create (recreate the OS set only)
//	auto,  broken, data  -> HALT (data fence)
//	sync/auto, unknown   -> HALT (a checker did not run: no verdict, no wipe)
func decideAction(mode installcontext.Mode, role string, persistent bool, verdict storagehealth.Verdict) (act setAction, halt bool) {
	if persistent {
		return actMount, false
	}
	switch mode {
	case installcontext.ModeFull:
		return actCreate, false
	case installcontext.ModeLocal:
		return actMount, false
	}
	// sync / auto
	switch verdict {
	case storagehealth.VerdictHealthy:
		return actMount, false
	case storagehealth.VerdictBlank:
		return actCreate, false
	case storagehealth.VerdictUnknown:
		// The probe never ran: mounting an unverified set and wiping a
		// possibly healthy one are both wrong. Halt for an operator.
		return actMount, true
	default: // broken
		if mode == installcontext.ModeAuto && role == "os" {
			return actCreate, false
		}
		return actMount, true // halt (act unused)
	}
}

func actionName(a setAction) string {
	if a == actMount {
		return "mount (no wipe)"
	}
	return "create"
}

// reserveManualSets resolves every manual set's devices to canonical kernel
// paths BEFORE any set is processed, and:
//
//  1. refuses when two sets' devices resolve to the same disk (an alias a
//     literal-level validator cannot see: /dev/disk/by-id/X == /dev/sdb);
//  2. records the canonical paths of PERSISTENT sets' disks in sp.protected —
//     the protected-disk barrier that cleanup enforces independently;
//  3. removes every reserved disk from the discover pool, so a
//     selection=discover set can never draw a disk that belongs to a
//     declared manual (and in particular persistent) set.
//
// Resolution failure is a halt (fail-closed): an unresolvable manual device
// means we cannot know which physical disk to protect. A device that is
// simply ABSENT from the node is recorded in sp.missing; planSet tolerates it
// only for a redundant raid set that can still run without it AND is mounted,
// not (re)created (a dead mirror disk must not stop the node from ever
// booting again). Any other absent device is a fatal config error (exit 3)
// naming the device, raised while planning -- before any disk is touched.
// Present disks of a degraded persistent set stay protected.
func (sp *setProcessor) reserveManualSets(ctx context.Context, cfg *v2.Config) error {
	sp.protected = map[string]struct{}{}
	sp.manualDisks = map[string][]selector.Resolved{}
	sp.missing = map[string][]string{}
	if cfg == nil {
		return nil
	}
	reserved := map[string]struct{}{}
	owner := map[string]string{} // canonical path -> set name
	for i := range cfg.Sets {
		set := &cfg.Sets[i]
		if set.SelectionMode() != "manual" || set.StorageType() == v2.StorageMemory {
			continue
		}
		var present []selector.Resolved
		var missing []string
		resolve := func() (int, error) {
			present, missing = nil, nil
			for _, dev := range set.EffectiveDevices() {
				r, err := sp.resolveForReservation(ctx, []string{dev})
				if err != nil {
					if errors.Is(err, selector.ErrNotFound) {
						missing = append(missing, dev)
						continue
					}
					return 0, err
				}
				present = append(present, r...)
			}
			return len(missing), nil
		}
		if err := selector.WaitWhileMissing(ctx, sp.runner, sp.lateWait, resolve); err != nil {
			return classifyResolveErr(set.Name, fmt.Errorf("resolving devices for reservation: %w", err))
		}
		if len(missing) > 0 {
			// Judged by planSet (missingDeviceError): a set this run ignores
			// (the os set under memboot) must not fail on its devices.
			sp.missing[set.Name] = missing
			sp.logger.Warn(fmt.Sprintf("set %s: device(s) %s not present on this node", set.Name, strings.Join(missing, ", ")))
		}
		sp.manualDisks[set.Name] = present
		for _, r := range present {
			p := r.ResolvedPath
			if prev, dup := owner[p]; dup && prev != set.Name {
				return safetyRefusalf("device-overlap",
					"device %s (%s) resolves into both set %q and set %q — a disk may belong to exactly one set",
					r.ConfiguredPath, p, prev, set.Name)
			}
			owner[p] = set.Name
			reserved[p] = struct{}{}
			if set.Persistent {
				sp.protected[p] = struct{}{}
			}
		}
	}
	if len(reserved) == 0 {
		return nil
	}
	// Subtract reserved disks from the discover pool (match on canonical
	// kernel path — the pool is enumerated with kernel names).
	remaining := sp.pool[:0]
	for _, d := range sp.pool {
		if _, taken := reserved[d.KernelPath]; taken {
			sp.logger.UserInfo("disk %s is reserved by set %q -- removed from the discover pool", d.KernelPath, owner[d.KernelPath])
			continue
		}
		remaining = append(remaining, d)
	}
	sp.pool = remaining
	return nil
}

// resolveDiscover picks the set's disks from the pool by its match filter +
// raid count, removes them from the pool, and fills set.Devices. A match the
// pool cannot meet is a fatal config error (exit 3) -- unless the pool itself
// could not be listed, which may clear on retry (exit 1).
func (sp *setProcessor) resolveDiscover(set *v2.Set) error {
	mf := set.MatchFilter()
	m, err := diskresolve.ParseMatch(mf.Tags, mf.MinSize, mf.MaxSize, mf.Model)
	if err != nil {
		return fatalConfigf("match-invalid", "set %q: match: %v", set.Name, err)
	}
	selected, err := diskresolve.Resolve(sp.pool, m, set.MemberCount(), set.SpareCount(), 0)
	if err != nil {
		if sp.poolErr != nil {
			return fmt.Errorf("set %q: %w (device discovery failed: %v)", set.Name, err, sp.poolErr)
		}
		return fatalConfigf("discover-unmet", "set %q: %v", set.Name, err)
	}
	paths := make([]string, 0, len(selected))
	chosen := make(map[string]struct{}, len(selected))
	for _, d := range selected {
		paths = append(paths, d.Path)
		chosen[d.Path] = struct{}{}
	}
	set.Devices = paths
	// Remove the chosen disks from the pool so a later auto set can't pick them.
	remaining := sp.pool[:0]
	for _, d := range sp.pool {
		if _, taken := chosen[d.Path]; !taken {
			remaining = append(remaining, d)
		}
	}
	sp.pool = remaining
	sp.logger.UserInfo("set %q selection=discover -> %v", set.Name, paths)
	return nil
}

// runSanitize securely erases every discovered local disk, then stops.
func (sp *setProcessor) runSanitize(ctx context.Context, dryRun bool) ([]result.StorageResult, []result.ResolvedSet, error) {
	disks := make([]string, 0, len(sp.pool))
	for _, d := range sp.pool {
		disks = append(disks, d.KernelPath)
	}
	if len(disks) == 0 {
		// A diskless node is legitimate (exit 0), but say plainly that
		// nothing was erased: the node powers off after sanitize.
		sp.logger.UserInfo("sanitize: no local disks found -- nothing was erased (USB, FC/iSCSI and multipath disks are excluded from sanitize by design)")
		return nil, nil, nil
	}
	sp.logger.UserInfo("sanitize: erasing %d local disk(s): %v", len(disks), disks)
	if dryRun {
		sp.logger.UserInfo("sanitize: dry-run, not erasing")
	}
	// Release every storage stack on the target disks BEFORE erasing: the
	// same unmount → VG teardown (any VG with a PV on these disks) → md stop
	// cascade the install modes run. Without it the erase leaves live LVM/md
	// mappings behind — and on the advisory-discard fallback (wipe+zap) the
	// tenant filesystem stays fully reachable through the surviving
	// /dev/mapper node, while the held partition device blocks the zapped
	// table from ever re-reading (measured live on the 3.0 battery, S26).
	// Persistent sets were refused earlier (sanitize × persistent = safety
	// halt), so nothing here is protected — every disk is being erased.
	if _, err := sp.cleaner.CleanupSelectedSet(ctx, cleanup.Set{
		Name:          "sanitize",
		SelectedDisks: disks,
		OwnsSysroot:   true,
	}); err != nil {
		if errors.Is(err, cleanup.ErrProtected) {
			return nil, nil, cleanupError("sanitize", err)
		}
		return nil, nil, fmt.Errorf("sanitize: pre-erase stack teardown: %w", err)
	}
	s := sanitize.New(sanitize.Options{Runner: sp.runner, Logger: sp.logger, DryRun: dryRun, ReadBack: sp.sanitizeReadBack})
	if _, err := s.EraseAll(ctx, disks); err != nil {
		// Deterministic: the same disk fails the same way on every retry, so
		// park for an operator (safety refusal, exit 2) instead of looping.
		return nil, nil, safetyRefusalf("sanitize-incomplete", "%v", err)
	}
	sp.sanitized = len(disks)
	return nil, nil, nil
}

// membootSet is the synthesized tmpfs OS set used by memboot (and the auto
// fallback). It is constructed in-code, not from operator config, so it is
// not subject to the disklayout validator.
func membootSet() *v2.Set {
	return &v2.Set{
		Name: "os", Role: "os",
		Volumes: []v2.Volume{
			// size=80%: the RAM root must hold the ENTIRE extracted OS image,
			// and tmpfs's 50% default stalls smaller nodes (a 4.4GB image on
			// an 8GB node = 4GB tmpfs -> ENOSPC mid-extract; observed live).
			// 80% leaves ~20% for kernel + installer userspace.
			// FTA v2 BE-G4/BE-G5, barrier NB-8.
			{Name: "root", Mountpoint: "/", FS: "tmpfs", Provider: "memory", Options: "defaults,size=80%"},
		},
	}
}

// runMemboot synthesizes a tmpfs OS root and applies any declared DATA sets
// (persistent -> mount; otherwise the auto data-set policy, which never wipes
// a data set). The data sets are planned -- and refused -- before anything is
// mounted or touched.
func (sp *setProcessor) runMemboot(ctx context.Context, cfg *v2.Config, firmware string, dryRun, wipe bool) ([]result.StorageResult, []result.ResolvedSet, error) {
	var plans []plannedSet
	if cfg != nil {
		var err error
		// Data sets follow the auto policy: persistent -> mount, blank ->
		// create, broken -> halt. The data fence guarantees no wipe.
		plans, err = sp.planSets(ctx, installcontext.ModeAuto, cfg, true, firmware, dryRun, wipe)
		if err != nil {
			return nil, nil, err
		}
	}

	sp.logger.StageBanner("Processing memboot OS (tmpfs RAM root)")
	res, rec, err := sp.process(ctx, plannedSet{set: membootSet(), act: actMount, firstInstall: true}, firmware, dryRun)
	if err != nil {
		return nil, nil, fmt.Errorf("memboot tmpfs: %w", err)
	}
	dres, drec, err := sp.executePlans(ctx, plans, firmware, dryRun)
	if err != nil {
		return nil, nil, err
	}
	return append([]result.StorageResult{res}, dres...), append([]result.ResolvedSet{rec}, drec...), nil
}
