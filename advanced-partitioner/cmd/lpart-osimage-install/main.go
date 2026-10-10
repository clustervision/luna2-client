// lpart-osimage-install downloads the Luna OS image and extracts it
// into /sysroot. Behavioural reference: /root/NEWTREE/bin/lpart-osimage-install.
//
// Flow:
//  1. Parse flags (or --config JSON), init logger, sysroot + LUNA_TOKEN
//     preflight.
//  2. Detect the image filename from /lunatmp/install.sh (or override).
//  3. Download via aria2c (torrent) when present, otherwise curl.
//  4. Resolve preserve / preserve_empty / no_new_files through
//     first-component sysroot symlinks.
//  5. PrepareSysroot: top-level wipe, sibling deletion, preserve-empty
//     content clear.
//  6. BuildTarExcludes from the resolved patterns.
//  7. Extract with --acls or --no-acls based on the sysroot fs type.
//  8. Remove the cached tarball.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/cli"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/executor"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/mount"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/osimage"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/sysenv"
)

const toolName = "lpart-osimage-install"

// stringList is a repeatable string flag.
type stringList []string

func (s *stringList) String() string     { return strings.Join(*s, ",") }
func (s *stringList) Set(v string) error { *s = append(*s, v); return nil }

func main() {
	cli.EnsureInstallerPath()
	os.Exit(run(os.Args, os.Stdout, os.Stderr))
}

func errln(w io.Writer, format string, args ...any) {
	_, _ = fmt.Fprintf(w, toolName+": "+format+"\n", args...)
}

type flags struct {
	cli.Common
	ConfigPath         string
	Sysroot            string
	LunaURL            string
	Details            bool
	InstallContextPath string
	IgnoreInstallCtx   bool
	StorageResultPath  string
	Preserve           stringList
	PreserveEmpty      stringList
	NoNewFiles         stringList
	// Test affordances; never set from production CLI.
	LunaInstallScript string
	LunaTokenOverride string
	OSImageOverride   string
	ProcMountsPath    string
	CmdlinePath       string
	SkipMountCheck    bool
	SkipExtract       bool
	SkipDownload      bool
	StreamExtract     string
}

//nolint:gocyclo // command flow is linear and matches the bash predecessor.
func run(argv []string, stdout, stderr io.Writer) int {
	if len(argv) == 0 {
		argv = []string{toolName}
	}
	var f flags
	fs := cli.NewToolFlagSet(toolName)
	f.Bind(fs)
	fs.StringVar(&f.ConfigPath, "config", "", "Path to a JSON filter config (exclusive with --preserve/--preserve-empty/--no-new-files)")
	fs.StringVar(&f.Sysroot, "sysroot", mount.EffectiveSysroot(), "Mount target root (defaults to $rootmnt, else /sysroot)")
	fs.StringVar(&f.LunaURL, "luna-url", "", "Base URL for Luna file downloads (default: derived from cmdline luna.url)")
	fs.BoolVar(&f.Details, "details", true, "Verbose: per-path keep/remove decisions + extract verification evidence (default on; --details=false to quieten)")
	fs.Var(&f.Preserve, "preserve", "Path to preserve (repeatable). Exclusive with --config.")
	fs.Var(&f.PreserveEmpty, "preserve-empty", "Path to keep but wipe contents of (repeatable). Exclusive with --config.")
	fs.Var(&f.NoNewFiles, "no-new-files", "Path to leave untouched and not create from image (repeatable). Exclusive with --config.")
	fs.StringVar(&f.InstallContextPath, "install-context", installcontext.DefaultJSONPath,
		"Path to install-context.json (install_mode + osimage_sync, written by the part phase (lpartinputs))")
	fs.BoolVar(&f.IgnoreInstallCtx, "ignore-install-context", false,
		"Run regardless of osimage_sync (operator-only escape hatch)")
	fs.StringVar(&f.StorageResultPath, "storage-result",
		"/tmp/install_files/runtime/storage-result.json",
		"Path to storage-result.json; if FirstInstall=true the Sync filter is skipped (behaves as Full)")
	// Hidden test affordances.
	fs.StringVar(&f.LunaInstallScript, "luna-install-script", "", "(test) override /lunatmp/install.sh path")
	fs.StringVar(&f.LunaTokenOverride, "luna-token", "", "(test) override LUNA_TOKEN value")
	fs.StringVar(&f.OSImageOverride, "osimage-file", "", "(test) override detected image filename")
	fs.StringVar(&f.ProcMountsPath, "proc-mounts", "", "(test) override /proc/mounts path")
	fs.StringVar(&f.CmdlinePath, "cmdline-path", "", "(test) override /proc/cmdline path")
	fs.BoolVar(&f.SkipMountCheck, "skip-sysroot-check", false, "(test) skip the 'sysroot is not mounted' guard")
	fs.BoolVar(&f.SkipExtract, "skip-extract", false, "(test) run prepare/download/build but skip the tar invocation")
	fs.BoolVar(&f.SkipDownload, "skip-download", false, "(test) halt after filter resolution, before download/prepare_sysroot/extract (no wipe without a tarball to install)")
	fs.StringVar(&f.StreamExtract, "stream-extract", "auto",
		"Streamed curl|tar extract (no tarball on disk, halves peak RAM): auto = only when the sysroot is tmpfs (memboot), always, never. Streaming falls back to the classic download+extract on failure.")

	if err := fs.Parse(argv[1:]); err != nil {
		return 2
	}
	if f.ShowVersion {
		_, _ = fmt.Fprintln(stdout, cli.Version())
		return 0
	}
	logger, err := f.NewLogger(toolName)
	if err != nil {
		errln(stderr, "log init: %v", err)
		return 1
	}
	defer func() { _ = logger.Close() }()
	logger.SetStage("startup")

	// Refuse to run outside the Luna installer environment: lpart-osimage-install
	// extracts the OS image into /sysroot and must never do so on a running
	// system. --allow-non-installer is the explicit escape hatch.
	logger.SetStage("installer_check")
	if err := sysenv.RequireInstallerEnv(sysenv.Options{
		CmdlinePath:       f.CmdlinePath,
		AllowNonInstaller: f.AllowNonInstaller,
	}); err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	ic, err := installcontext.LoadFile(f.InstallContextPath)
	if err != nil {
		errln(stderr, "install-context: %v", err)
		return 1
	}
	logger.KV("install_mode", string(ic.InstallMode))
	if !f.IgnoreInstallCtx && ic.InstallMode.IsLocal() {
		errln(stderr, "install_mode=local — lpart-osimage-install should be skipped (run lpart-storage-check instead). Pass --ignore-install-context to force.")
		return 1
	}

	// Path hygiene before any path is used in a deletion decision.
	f.Sysroot = filepath.Clean(f.Sysroot)

	// First-install promotion + persistent-set guard: read both signals
	// from storage-result.json in one pass. FAIL-CLOSED: an unreadable or
	// unparseable result file would silently erase the persistent-set
	// mountpoint list — the wipe/extract would then treat protected volumes
	// as fair game. Refuse instead. `--storage-result ''` is the explicit
	// opt-out for environments that genuinely have no result file.
	srSummary, err := readStorageResultSummary(f.StorageResultPath)
	if err != nil {
		return cli.ExitSafetyRefusal(stderr, toolName, "storage-result",
			"cannot read persistent-set information from %s (%v) — refusing to wipe/extract without it; pass --storage-result '' only if no storage result exists", f.StorageResultPath, err)
	}

	// Decide whether this is a full (no-preserve) extract. full extracts the
	// whole image with no filter; memboot's RAM root is always fresh so it is
	// also a full overlay; and a first install under auto/sync promotes to
	// full because a preserve list is meaningless with no prior on-disk state.
	fullExtract := ic.InstallMode == installcontext.ModeFull || ic.InstallMode.IsMemboot()
	if !fullExtract && srSummary.FirstInstall &&
		(ic.InstallMode == installcontext.ModeAuto || ic.InstallMode == installcontext.ModeSync) {
		logger.UserInfo("first_install detected from storage-result.json — preserve filter ignored, extracting as full")
		fullExtract = true
	}
	if fullExtract {
		if f.ConfigPath != "" {
			logger.UserInfo("install_mode=%s — extracting full, filter config ignored", ic.InstallMode)
		}
		f.ConfigPath = ""
		f.Preserve = nil
		f.PreserveEmpty = nil
		f.NoNewFiles = nil
	}
	logger.KV("first_install", srSummary.FirstInstall)
	logger.KV("persistent_mountpoints", strings.Join(srSummary.PersistentMountpoints, ","))

	// --config is exclusive with the per-path flags.
	if f.ConfigPath != "" && (len(f.Preserve) > 0 || len(f.PreserveEmpty) > 0 || len(f.NoNewFiles) > 0) {
		errln(stderr, "--config is exclusive: do not combine with --preserve, --preserve-empty, or --no-new-files.")
		return 1
	}
	if f.ConfigPath != "" {
		cfg, err := osimage.LoadFilterConfig(f.ConfigPath)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				errln(stderr, "--config file not found: %s", f.ConfigPath)
				return 1
			}
			errln(stderr, "%v", err)
			return 1
		}
		f.Preserve = append(f.Preserve, cfg.Preserve...)
		f.PreserveEmpty = append(f.PreserveEmpty, cfg.PreserveEmpty...)
		f.NoNewFiles = append(f.NoNewFiles, cfg.NoNewFiles...)
	}

	// Sysroot must be mounted (skip in tests).
	if !f.SkipMountCheck {
		procMounts := f.ProcMountsPath
		if procMounts == "" {
			procMounts = "/proc/mounts"
		}
		fstype, err := osimage.SysrootFSType(procMounts, f.Sysroot)
		if err != nil {
			errln(stderr, "%v", err)
			return 1
		}
		if fstype == "" {
			errln(stderr, "sysroot is not mounted: %s", f.Sysroot)
			return 1
		}
	}

	// LUNA_TOKEN preflight.
	tokenOverride := f.LunaTokenOverride
	if tokenOverride == "" {
		tokenOverride = os.Getenv("LUNA_TOKEN")
	}
	token, err := osimage.DetectLunaToken(osimage.DetectOptions{
		Override:          tokenOverride,
		LunaInstallScript: f.LunaInstallScript,
	})
	if err != nil {
		errln(stderr, "LUNA_TOKEN is not set: %v", err)
		return 1
	}

	// Image filename.
	override := f.OSImageOverride
	if override == "" {
		override = os.Getenv("OSIMAGE_FILE")
	}
	imageFile, err := osimage.DetectImageFilename(osimage.DetectOptions{
		Override:          override,
		LunaInstallScript: f.LunaInstallScript,
	})
	if err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	runner := executor.New(executor.Options{DryRun: f.DryRun, Logger: logger})

	// Resolve filter paths through first-component sysroot symlinks.
	// Done before download so tests can drive filter logic without the
	// 60-90s torrent fetch via --skip-download.
	logger.SetStage("filter_resolve")
	resolver := osimage.FirstSymlinkResolver{Sysroot: f.Sysroot}
	// Normalise first: "/var/log" and "var/log" mean the same path. Without
	// this an absolute path matched nothing and its data was deleted.
	norm := func(kind string, raw []string) []string {
		paths, dropped := osimage.NormalizeFilterPaths(raw)
		for _, d := range dropped {
			logger.Warn(fmt.Sprintf("%s entry %q ignored: empty, \"/\" or contains \"..\"", kind, d))
		}
		return paths
	}
	preserve := osimage.ParsePatterns(resolver.ResolveAll(norm("preserve", f.Preserve)))
	preserveEmpty := osimage.ParsePatterns(resolver.ResolveAll(norm("preserve_empty", f.PreserveEmpty)))
	noNew := osimage.ParsePatterns(resolver.ResolveAll(norm("no_new_files", f.NoNewFiles)))
	// Pre-compute tar excludes so the log captures them regardless of
	// whether --skip-extract / --skip-download short-circuit later.
	excludes := osimage.BuildTarExcludes(preserve, preserveEmpty, noNew)
	// Persistent-set mountpoints are auto-excluded in every mode (Full,
	// Sync, Local). This is a safety guarantee tied to the operator's
	// explicit safety.persistent flag, not a heuristic filter. Each
	// mountpoint becomes "./<mountpoint>" plus "./<mountpoint>/*" so
	// both the directory entry and its contents survive the extract.
	for _, mp := range srSummary.PersistentMountpoints {
		clean := strings.TrimPrefix(mp, "/")
		if clean == "" {
			continue
		}
		excludes = append(excludes,
			"--exclude=./"+clean,
			"--exclude=./"+clean+"/*",
		)
		logger.UserInfo("persistent-set-mountpoint excluded from extract: %s", mp)
	}
	logger.KV("tar_excludes", strings.Join(excludes, " "))

	// Defensive check: every persistent mountpoint must appear in the
	// tar excludes. Should never trip, but exits 2 if it does (safety
	// refusal) rather than continue with a broken exclude set.
	for _, mp := range srSummary.PersistentMountpoints {
		clean := strings.TrimPrefix(mp, "/")
		if clean == "" {
			continue
		}
		want := "--exclude=./" + clean
		found := false
		for _, ex := range excludes {
			if ex == want {
				found = true
				break
			}
		}
		if !found {
			return cli.ExitSafetyRefusal(stderr, toolName, "persistent",
				"persistent mountpoint %s not present in tar excludes", mp)
		}
	}

	// --skip-download halts HERE — before download, PrepareSysroot and
	// extract. There is no tarball to extract, so running the destructive
	// prepare would only expose the sysroot to a wipe with no install to
	// follow (the ancestor analysis's --skip-download hazard); it also keeps
	// the filter-only tests hermetic (no real /sysroot access).
	if f.SkipDownload {
		logger.SetStage("download")
		logger.UserInfo("(test) --skip-download set; halting before download/prepare/extract")
		return 0
	}

	// Sysroot filesystem type drives both the tar ACL mode and the streaming
	// decision, so probe it before the download strategy is chosen.
	fstype, _ := osimage.SysrootFSType(
		nonEmpty(f.ProcMountsPath, "/proc/mounts"),
		f.Sysroot,
	)
	acls := fstype != "tmpfs"
	logger.KV("sysroot_fstype", fstype)
	logger.KV("acls", acls)

	// Streaming decision (FTA v2 BE-G5, barrier NB-6). Streaming pipes
	// curl|tar with no tarball on disk — and the initramfs /tmp is tmpfs, so
	// the classic path's tarball costs RAM on every install; on memboot the
	// peak (tarball + extracted tree, both in RAM) stalled an 8GB node on a
	// 4.4GB image. Default "auto" streams ONLY tmpfs sysroots (memboot):
	// there a mid-stream failure costs one retry on a fresh RAM root, while
	// disk installs keep the safer payload-in-hand-before-extract ordering.
	// Streaming needs HTTP as the first usable provision method (torrent
	// requires the file); any stream failure falls back to the classic path.
	streamPolicy, policyNote := resolveStreamPolicy(f.StreamExtract, f.CmdlinePath)
	if policyNote != "" {
		logger.UserInfo("%s", policyNote)
	}
	streamWanted := false
	switch strings.ToLower(streamPolicy) {
	case "always":
		streamWanted = true
	case "never":
		streamWanted = false
	default:
		streamWanted = fstype == "tmpfs"
	}
	if streamWanted {
		firstUsable := ""
		for _, m := range osimage.ProvisionOrder(ic.ProvisionMethod, ic.ProvisionFallback) {
			if m == "torrent" {
				if _, aerr := exec.LookPath("aria2c"); aerr == nil {
					firstUsable = m
					break
				}
				continue // torrent preferred but unusable; next method
			}
			firstUsable = m
			break
		}
		if firstUsable != "http" && firstUsable != "https" {
			logger.UserInfo("stream-extract: first usable provision method is %q, not http; using the classic download", firstUsable)
			streamWanted = false
		}
	}

	// Derive the file-server URL, TLS policy and download method from the
	// same sources every other Luna caller uses — NOT hardcoded. The base
	// URL comes from --luna-url if given, else from the cmdline luna.url
	// host; TLS verification from luna.verifycert; and the method order
	// from the node's provision_method/provision_fallback (carried in the
	// install context). This restores the legacy install.sh contract.
	logger.SetStage("download")
	logger.UserInfo("preparing OS-image download: %s", imageFile)
	sopts := sysenv.Options{CmdlinePath: f.CmdlinePath}
	fileBase := strings.TrimRight(f.LunaURL, "/")
	if fileBase == "" {
		if lurl, ok := sysenv.KernelParam(sopts, "luna.url"); ok {
			fileBase = osimage.FileBaseFromLunaURL(lurl)
		}
	}
	if fileBase == "" {
		logger.Warn("could not derive file-server URL from cmdline luna.url; using last-resort default")
		fileBase = osimage.DefaultLunaFileBaseURL
	}
	insecure := true
	if vc, ok := sysenv.KernelParam(sopts, "luna.verifycert"); ok {
		switch strings.ToLower(vc) {
		case "yes", "true":
			insecure = false
		}
	}
	logger.KV("file_base_url", fileBase)
	logger.KV("provision_method", ic.ProvisionMethod)
	logger.KV("provision_fallback", ic.ProvisionFallback)
	logger.KV("stream_extract", fmt.Sprintf("%v", streamWanted))
	dopts := osimage.DownloadOptions{
		Runner:            runner,
		Logger:            logger,
		LunaFileBaseURL:   fileBase,
		LunaToken:         token,
		LunaInterface:     os.Getenv("LUNA_BOOTIF"),
		ImageFile:         imageFile,
		ProvisionMethod:   ic.ProvisionMethod,
		ProvisionFallback: ic.ProvisionFallback,
		Insecure:          insecure,
		Aria2cAvailable: func() bool {
			_, err := exec.LookPath("aria2c")
			return err == nil
		},
	}

	// Classic path downloads NOW — payload in hand before the sysroot wipe.
	// Streaming defers the transfer into the extract pipe below.
	var cachePath string
	if !streamWanted {
		cachePath, err = osimage.Download(context.Background(), dopts)
		if err != nil {
			errln(stderr, "%v", err)
			return 1
		}
	}

	// PrepareSysroot.
	logger.SetStage("prepare_sysroot")
	if err := osimage.PrepareSysroot(context.Background(), osimage.PrepareOptions{
		Sysroot:               f.Sysroot,
		Logger:                logger,
		DryRun:                f.DryRun,
		Resolver:              resolver,
		FindmntTargets:        findmntTargets(runner),
		PersistentMountpoints: srSummary.PersistentMountpoints,
		PreserveOnly:          preserve,
		PreserveEmpty:         preserveEmpty,
		NoNewFiles:            noNew,
		Details:               f.Details,
	}); err != nil {
		errln(stderr, "%v", err)
		return 1
	}

	// Extract. --skip-extract halts here even when the download did
	// happen, leaving the tarball at cachePath. (--skip-download halted
	// before the prepare above.)
	logger.SetStage("extract")
	if f.SkipExtract {
		logger.UserInfo("(test) --skip-extract set; not running tar")
		return 0
	}
	if streamWanted {
		serr := osimage.StreamHTTPExtract(context.Background(), dopts, osimage.ExtractOptions{
			Runner:      runner,
			Logger:      logger,
			Sysroot:     f.Sysroot,
			TarExcludes: excludes,
			ACLs:        acls,
		})
		if serr != nil {
			// Fall back to the classic path: PrepareSysroot is re-entrant and
			// tar overwrites the partial tree, so a failed stream costs one
			// full download, never a broken install.
			logger.Warn("streamed extract failed; falling back to classic download+extract: " + serr.Error())
			cachePath, err = osimage.Download(context.Background(), dopts)
			if err != nil {
				errln(stderr, "%v", err)
				return 1
			}
			streamWanted = false
		}
	}
	if !streamWanted {
		if err := osimage.Extract(context.Background(), osimage.ExtractOptions{
			Runner:      runner,
			Logger:      logger,
			Sysroot:     f.Sysroot,
			TarballPath: cachePath,
			TarExcludes: excludes,
			ACLs:        acls,
			RemoveAfter: true,
		}); err != nil {
			errln(stderr, "%v", err)
			return 1
		}
	}

	// Prove the extract: /etc/os-release present + sysroot populated, with the
	// evidence shown — the silent "extracted into the wrong dir, root empty"
	// failure (Ubuntu) is now caught and reported here.
	logger.SetStage("extract_verify")
	if err := osimage.VerifyExtract(logger, f.Sysroot); err != nil {
		errln(stderr, "%v", err)
		return 1
	}
	return 0
}

// findmntTargets returns a closure that runs `findmnt -rn -o TARGET`.
// FAIL-CLOSED: an error is returned to PrepareSysroot, which refuses to
// delete blind. (findmnt is a hard dependency of the installer initramfs —
// see 95luna/module-setup.sh — so this cannot mass-abort healthy installs.)
func findmntTargets(runner executor.Runner) func() ([]string, error) {
	return func() ([]string, error) {
		res, err := runner.Run(context.Background(), executor.CommandSpec{
			Path: "findmnt", Args: []string{"-rn", "-o", "TARGET"},
			CaptureStdout: true, CaptureStderr: true,
		})
		if err != nil {
			return nil, fmt.Errorf("findmnt -rn -o TARGET: %w", err)
		}
		var out []string
		for _, line := range strings.Split(string(res.Stdout), "\n") {
			line = strings.TrimSpace(line)
			if line != "" {
				out = append(out, line)
			}
		}
		return out, nil
	}
}

// resolveStreamPolicy picks the effective --stream-extract policy. The
// pipeline invokes this binary with a fixed argv, so the operator's only
// per-node lever is the kernel cmdline: lpart.stream=always|never|auto
// overrides the built-in default (an explicit --stream-extract flag value
// still wins, mirroring lpart.notui's flag-over-cmdline precedence). The
// returned note, when non-empty, is a human-readable line for the log.
func resolveStreamPolicy(flagVal, cmdlinePath string) (policy, note string) {
	if flagVal != "auto" {
		return flagVal, ""
	}
	v, ok := sysenv.KernelParam(sysenv.Options{CmdlinePath: cmdlinePath}, "lpart.stream")
	if !ok {
		return flagVal, ""
	}
	switch strings.ToLower(v) {
	case "always", "never", "auto":
		return strings.ToLower(v), "stream-extract policy from kernel cmdline: lpart.stream=" + v
	default:
		return flagVal, "ignoring invalid kernel cmdline lpart.stream=" + v + " (want always|never|auto)"
	}
}

func nonEmpty(s, fallback string) string {
	if s == "" {
		return fallback
	}
	return s
}

// storageResultSummary is the subset of storage-result.json
// lpart-osimage-install needs: the first-install flag (for Sync→Full
// promotion) and the union of every persistent set's mountpoints (for
// auto-excluding from the tar extract AND identity-preserving in
// PrepareSysroot). A missing or unparseable file is an ERROR (fail-closed):
// silently returning the zero value would erase the persistent-mountpoint
// list and let the wipe/extract treat protected volumes as fair game.
// An empty path ("" — explicit opt-out) returns the zero value.
type storageResultSummary struct {
	FirstInstall          bool
	PersistentMountpoints []string
}

func readStorageResultSummary(path string) (storageResultSummary, error) {
	out := storageResultSummary{}
	if path == "" {
		return out, nil
	}
	//nolint:gosec // operator-supplied path; binary already runs as root
	body, err := os.ReadFile(path)
	if err != nil {
		return out, err
	}
	type setProbe struct {
		FirstInstall bool `json:"first_install"`
		Persistent   bool `json:"persistent"`
		Filesystems  []struct {
			Mount string `json:"mount"`
		} `json:"filesystems"`
	}
	var probe struct {
		setProbe
		Sets []setProbe `json:"sets"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return out, fmt.Errorf("parse %s: %w", path, err)
	}
	collect := func(s setProbe) {
		if s.FirstInstall {
			out.FirstInstall = true
		}
		if s.Persistent {
			for _, f := range s.Filesystems {
				if f.Mount == "" || f.Mount == "swap" {
					continue
				}
				out.PersistentMountpoints = append(out.PersistentMountpoints, f.Mount)
			}
		}
	}
	if len(probe.Sets) == 0 {
		collect(probe.setProbe)
	} else {
		for _, s := range probe.Sets {
			collect(s)
		}
	}
	return out, nil
}
