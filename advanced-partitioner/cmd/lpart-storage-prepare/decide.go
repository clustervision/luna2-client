package main

import (
	"context"
	"fmt"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cleanup"
	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/diskresolve"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
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

	// auto/sync/full/local. With no declared disk layout, auto falls back to
	// memboot; the other modes require a layout.
	if cfg == nil || len(cfg.Sets) == 0 {
		if mode == installcontext.ModeAuto {
			sp.logger.UserInfo("auto: no disk layout declared -- falling back to memboot (RAM root)")
			return sp.runMemboot(ctx, nil, firmware, dryRun, wipe)
		}
		return nil, nil, fmt.Errorf("install_mode=%s requires a disklayout", mode)
	}

	order := cfg.ExecutionOrder()
	results := make([]result.StorageResult, 0, len(order))
	resolved := make([]result.ResolvedSet, 0, len(order))
	for _, idx := range order {
		set := &cfg.Sets[idx]
		sp.logger.StageBanner(fmt.Sprintf("Processing set '%s' (install_mode=%s)", set.Name, mode))
		res, rec, err := sp.applySet(ctx, mode, set, firmware, dryRun, wipe)
		if err != nil {
			return nil, nil, err
		}
		results = append(results, res)
		resolved = append(resolved, rec)
	}
	return results, resolved, nil
}

// applySet enforces the data fence for one set under auto/sync/full/local and
// then runs process() with the decided action.
func (sp *setProcessor) applySet(ctx context.Context, mode installcontext.Mode, set *v2.Set, firmware string, dryRun, wipe bool) (result.StorageResult, result.ResolvedSet, error) {
	// Persistent sets are hands-off in every normal mode: mount only.
	if set.Persistent {
		sp.logger.UserInfo("set %q is persistent -- mount only (never partitioned/formatted)", set.Name)
		return sp.process(ctx, set, firmware, actMount, dryRun, false)
	}

	// Resolve selection=discover disks from the discovered pool (mutates
	// set.Devices so process() resolves the same disks). Failure to meet the
	// layout is a halt, never a silent fallback.
	if set.SelectionMode() == "discover" && set.StorageType() != v2.StorageMemory {
		if err := sp.resolveDiscover(set); err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("set %q: %w", set.Name, err)
		}
	}

	firstInstall := wipe && !dryRun

	// full and local need no health probe; sync/auto do.
	var verdict storagehealth.Verdict
	if mode == installcontext.ModeSync || mode == installcontext.ModeAuto {
		resolvedDisks, err := sp.selector.ResolveAll(ctx, set.EffectiveDevices())
		if err != nil {
			return result.StorageResult{}, result.ResolvedSet{}, fmt.Errorf("set %q: resolve: %w", set.Name, err)
		}
		verdict, _ = storagehealth.Classify(ctx, sp.runner, sp.checker, set, resolvedDisks, sp.logger)
		sp.logger.KV("verdict", set.Name+"="+string(verdict))
	}

	act, halt := decideAction(mode, set.Role, false, verdict)
	if halt {
		return result.StorageResult{}, result.ResolvedSet{}, safetyRefusalf("data-fence",
			"set %q is %s and install_mode=%s will not wipe it (data fence); halting -- operator review required",
			set.Name, verdict, mode)
	}
	sp.logger.UserInfo("set %q: mode=%s verdict=%s -> %s", set.Name, mode, verdict, actionName(act))
	if act == actMount {
		firstInstall = false
	}
	return sp.process(ctx, set, firmware, act, dryRun, firstInstall)
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
// means we cannot know which physical disk to protect.
func (sp *setProcessor) reserveManualSets(ctx context.Context, cfg *v2.Config) error {
	sp.protected = map[string]struct{}{}
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
		resolved, err := sp.resolveForReservation(ctx, set.EffectiveDevices())
		if err != nil {
			return fmt.Errorf("set %q: resolving devices for reservation: %w", set.Name, err)
		}
		for _, r := range resolved {
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
// raid count, removes them from the pool, and fills set.Devices.
func (sp *setProcessor) resolveDiscover(set *v2.Set) error {
	mf := set.MatchFilter()
	m, err := diskresolve.ParseMatch(mf.Tags, mf.MinSize, mf.MaxSize, mf.Model)
	if err != nil {
		return err
	}
	selected, err := diskresolve.Resolve(sp.pool, m, set.MemberCount(), set.SpareCount(), 0)
	if err != nil {
		return err
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
		sp.logger.UserInfo("sanitize: no local disks discovered; nothing to erase")
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
		return nil, nil, fmt.Errorf("sanitize: pre-erase stack teardown: %w", err)
	}
	s := sanitize.New(sanitize.Options{Runner: sp.runner, Logger: sp.logger})
	if _, err := s.EraseAll(ctx, disks); err != nil {
		return nil, nil, err
	}
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
// a data set).
func (sp *setProcessor) runMemboot(ctx context.Context, cfg *v2.Config, firmware string, dryRun, wipe bool) ([]result.StorageResult, []result.ResolvedSet, error) {
	results := make([]result.StorageResult, 0, 2)
	resolved := make([]result.ResolvedSet, 0, 2)

	sp.logger.StageBanner("Processing memboot OS (tmpfs RAM root)")
	res, rec, err := sp.process(ctx, membootSet(), firmware, actMount, dryRun, true)
	if err != nil {
		return nil, nil, fmt.Errorf("memboot tmpfs: %w", err)
	}
	results = append(results, res)
	resolved = append(resolved, rec)

	if cfg != nil {
		for i := range cfg.Sets {
			set := &cfg.Sets[i]
			if set.Role == "os" {
				sp.logger.UserInfo("memboot: ignoring declared os set %q (OS runs from RAM)", set.Name)
				continue
			}
			sp.logger.StageBanner(fmt.Sprintf("Processing memboot data set '%s'", set.Name))
			// Data sets follow the auto policy: persistent -> mount, blank ->
			// create, broken -> halt. The data fence guarantees no wipe.
			dres, drec, derr := sp.applySet(ctx, installcontext.ModeAuto, set, firmware, dryRun, wipe)
			if derr != nil {
				return nil, nil, derr
			}
			results = append(results, dres)
			resolved = append(resolved, drec)
		}
	}
	return results, resolved, nil
}
