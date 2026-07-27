package pipeline

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	v2 "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/config/v2"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/engine"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/installcontext"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// emitLuna fills the Luna (controller-side) dashboard panel from the kernel
// cmdline + the resolved install context. The old bash partscript emitted
// LPART_CONTROLLER/LPART_HOSTNAME markers for this; the Go pipeline emits the
// typed event directly. Best-effort: blank fields just render empty.
func (p *Pipeline) emitLuna(mode string) {
	cl := readCmdline()
	l := model.Luna{
		Controller:  hostFromURL(cl["luna.url"]),
		TargetIP:    cl["luna.ip"],
		Hostname:    firstNonEmpty(cl["luna.hostname"], p.opts.NodeName),
		Image:       osimageName(),
		InstallMode: mode,
	}
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvLunaUpdated, Data: engine.LunaUpdate(l)})
}

func readCmdline() map[string]string {
	out := map[string]string{}
	data, err := os.ReadFile("/proc/cmdline")
	if err != nil {
		return out
	}
	for _, tok := range strings.Fields(string(data)) {
		if eq := strings.IndexByte(tok, '='); eq > 0 {
			out[tok[:eq]] = tok[eq+1:]
		}
	}
	return out
}

// hostFromURL strips the scheme from a luna.url, leaving host[:port].
func hostFromURL(u string) string {
	u = strings.TrimPrefix(u, "https://")
	u = strings.TrimPrefix(u, "http://")
	if i := strings.IndexByte(u, '/'); i >= 0 {
		u = u[:i]
	}
	return u
}

var osimageRe = regexp.MustCompile(`/files/([A-Za-z0-9._-]+?)-\d+\.tar\.bz2`)

// osimageName extracts the osimage base name (e.g. "compute-image") from the
// Luna-written /lunatmp/install.sh. Best-effort; returns "" if unavailable.
func osimageName() string {
	data, err := os.ReadFile("/lunatmp/install.sh")
	if err != nil {
		return ""
	}
	if m := osimageRe.FindStringSubmatch(string(data)); len(m) > 1 {
		return m[1]
	}
	return ""
}

func firstNonEmpty(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// announce emits the install banner to the log/forward stream (and the TUI run
// output): who we are, what we see (node + hardware facts), and what we are
// about to do. Lines go via p.info so they reach rsyslog (forwarded) and the
// local file. Call once per phase.
func (p *Pipeline) announce(ctx context.Context, phase, mode, source string) {
	cl := readCmdline()
	bar := "================================================================"
	p.info(bar)
	p.info(fmt.Sprintf("TrinityX node installer (v1.4) -- phase %s on %s", phase, p.opts.NodeName))
	p.info(fmt.Sprintf("install_mode=%s  source=%s  controller=%s  osimage=%s",
		mode, source, hostFromURL(cl["luna.url"]), osimageName()))
	p.info(fmt.Sprintf("arch=%s  kernel=%s", unameArch(), readTrim("/proc/sys/kernel/osrelease")))
	p.info(fmt.Sprintf("cpu=%s (%s)  mem=%s  gpu=%s  nics=%s",
		shortCPUModel(cpuModel()), cpuTopo(), memTotal(), gpuSummary(), nicSummary()))
	p.info("disks seen: " + disksSummary(ctx))
	p.info("plan: " + modeDescription(mode))
	p.info(bar)
}

// disksSummary is a one-line "sda(500G,ssd) nvme0n1(2T,nvme) ..." for the banner.
func disksSummary(ctx context.Context) string {
	ds := lsblkDisks(ctx)
	if len(ds) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(ds))
	for _, d := range ds {
		tag := "rotary"
		if !d.Rota {
			tag = "ssd"
			if d.Tran == "nvme" || strings.HasPrefix(d.Name, "nvme") {
				tag = "nvme"
			}
		}
		parts = append(parts, fmt.Sprintf("%s(%s,%s)", d.Name, humanSize(d.Size), tag))
	}
	return strings.Join(parts, " ")
}

// modeDescription is the one-line "what will happen" for the banner.
func modeDescription(mode string) string {
	switch mode {
	case "memboot":
		return "OS runs from RAM (tmpfs); no disk OS; persistent data sets are mounted, never wiped"
	case "auto":
		return "install if blank, refresh if healthy, repair a broken OS set; fall back to memboot if no layout"
	case "sync":
		return "converge to the golden image (preserve list kept, cruft deleted); HALT on a broken disk, never wipe"
	case "full":
		return "wipe and reinstall from scratch (non-persistent sets)"
	case "local":
		return "boot the on-disk OS, fetch nothing; verify + mount"
	case "sanitize":
		return "securely erase ALL local disks for tenant turnover, then stop"
	default:
		return "install_mode=" + mode
	}
}

// dumpKernel sends a one-shot snapshot of the existing kernel ring buffer
// (boot + dracut + early install) to the stream, then a live kernel/install
// progress follows from PollKmsg. So the controller log reads: banner ->
// "what the kernel/dracut did" -> live install.
func (p *Pipeline) dumpKernel() {
	// The backlog is captured for the controller's rsyslog (cluster-messages)
	// only: the node console already printed it before the TUI started, so each
	// record is flagged SyslogOnly to keep it out of the live TUI/plain output.
	evs := engine.ReadKmsgBacklog(800)
	p.infoSyslog(fmt.Sprintf("---- kernel ring buffer (dmesg) snapshot: %d record(s) ----", len(evs)))
	for _, ev := range evs {
		ev.SyslogOnly = true
		p.emit(ev)
	}
	p.infoSyslog("---- end kernel snapshot; live kernel + install progress follows ----")
}

// emitNode probes the host for dashboard facts (vendor/model/cpu/mem/nics)
// and pushes them as a node.updated event. This replaces the bash
// emit_node_facts that shelled out to dmidecode/lscpu and printf'd LPART_*
// markers. Everything is best-effort: a missing source just leaves a field
// blank.
func (p *Pipeline) emitNode() {
	n := model.Node{
		Name:     p.opts.NodeName,
		Arch:     unameArch(),
		BootMode: bootMode(),
		Vendor:   readTrim("/sys/devices/virtual/dmi/id/sys_vendor"),
		Model:    readTrim("/sys/devices/virtual/dmi/id/product_name"),
		CPU:      shortCPUModel(cpuModel()),
		CPUTopo:  cpuTopo(),
		Memory:   memTotal(),
		NICs:     nicSummary(),
		GPU:      gpuSummary(),
	}
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvNodeUpdated, Data: engine.NodeUpdate(n)})
}

// emitStorage enumerates whole disks and pushes a storage.updated event so
// the candidate table reflects the real hardware. Only real install targets
// are listed — physical disks here, plus a tmpfs row added later by
// markLayout *only* when the OS actually targets RAM. The installer's own
// memory staging area is never shown. Replaces emit_storage_facts (lsblk|jq).
func (p *Pipeline) emitStorage() {
	s := model.Storage{
		Memory:     memTotal(),
		TmpfsAvail: true,
	}
	p.disks = nil
	for _, d := range lsblkDisks(p.ctx) {
		var kind model.DeviceKind
		switch {
		case d.Tran == "nvme":
			kind, s.NVMe = model.KindNVMe, s.NVMe+1
		case !d.Rota:
			kind, s.SSD = model.KindSSD, s.SSD+1
		default:
			kind, s.Spindles = model.KindHDD, s.Spindles+1
		}
		p.disks = append(p.disks, d.Name)
		s.Candidates = append(s.Candidates, model.StorageCandidate{
			Name:  d.Name,
			Kind:  kind,
			Size:  humanSize(d.Size),
			Model: strings.TrimSpace(d.Model),
			UID:   diskUID(d.Serial, d.WWN, d.Model, d.Name),
		})
	}
	p.storageState = s
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvStorageUpdated, Data: engine.StorageUpdate(s)})
}

// markLayout re-emits the storage panel from the resolved layout: each disk
// the layout writes to is flagged InUse and tagged with its set (OS/DATA/...)
// and RAID level; a tmpfs row is added only when the OS targets RAM. Disks
// not in the layout are shown but left untagged. The header Method + the
// under-table Summary come from the same parse.
func (p *Pipeline) markLayout(info layoutInfo) {
	s := p.storageState
	cands := make([]model.StorageCandidate, 0, len(s.Candidates)+1)
	for _, c := range s.Candidates {
		if c.Kind == model.KindTmpfs {
			continue // drop any stale tmpfs row; re-added below if mem OS
		}
		if di, ok := info.Disks[c.Name]; ok {
			c.InUse = true
			c.Set = di.Set
			c.RaidType = di.Raid
			if di.UID != "" {
				c.UID = di.UID
			}
		} else {
			c.InUse, c.Set, c.RaidType = false, "", ""
		}
		cands = append(cands, c)
	}
	if info.MemOS {
		// tmpfs OS row at the top — only when the OS actually targets RAM.
		cands = append([]model.StorageCandidate{{
			Name: "tmpfs", Kind: model.KindTmpfs, Size: memTotal(),
			Model: "OS in RAM", Set: "OS", InUse: true,
		}}, cands...)
	}
	s.Candidates = cands
	s.Method = info.Method
	s.Summary = info.Summary
	p.storageState = s
	p.emit(engine.InstallerEvent{Time: time.Now(), Type: engine.EvStorageUpdated, Data: engine.StorageUpdate(s)})
}

// diskInfo is the per-disk layout assignment derived from storage-result.json.
type diskInfo struct {
	Set  string // "OS", "DATA", "DATA1", ...
	Raid string // "RAID1"/"RAID5"/... or "" when not RAID
	UID  string // serial / by-path tail / model
}

// layoutInfo is the resolved-layout view markLayout renders.
type layoutInfo struct {
	Disks   map[string]diskInfo // disk basename -> assignment
	MemOS   bool                // OS targets tmpfs (RAM)
	Method  string              // header summary, e.g. "RAID1 OS + RAID5 DATA · UEFI"
	Summary string              // under-table line, e.g. "OS -> sda,sdb (RAID1)   ..."
}

// setProbe is one set as it appears in storage-result.json: the top-level
// object for a single-set result, or each element of "sets" for multi-set.
type setProbe struct {
	Role          string `json:"role"`
	RaidMode      string `json:"raid_mode"`
	RegisterNVRAM string `json:"register_nvram"`
	SelectedDisks []struct {
		ResolvedPath   string `json:"resolved_path"`
		ConfiguredPath string `json:"configured_path"`
		Serial         string `json:"serial"`
		Model          string `json:"model"`
	} `json:"selected_disks"`
}

// parseLayout reads storage-result.json and maps each physical disk to its
// set + RAID level, detects an OS-in-RAM layout, and builds the header
// Method + under-table Summary. Handles single-set and multi-set shapes.
func parseLayout(path string) layoutInfo {
	info := layoutInfo{Disks: map[string]diskInfo{}}
	data, err := os.ReadFile(path) //nolint:gosec // G304: runtimeDir path; binary runs as root
	if err != nil {
		return info
	}
	var top struct {
		setProbe
		Sets []setProbe `json:"sets"`
	}
	_ = json.Unmarshal(data, &top)
	sets := top.Sets
	if len(sets) == 0 {
		sets = []setProbe{top.setProbe}
	}

	dataTotal := 0
	for _, s := range sets {
		if strings.EqualFold(s.Role, "data") {
			dataTotal++
		}
	}

	firmware := bootMode()
	var methodParts, summaryParts []string
	nvram := false
	dataIdx := 0
	for _, s := range sets {
		isData := strings.EqualFold(s.Role, "data")
		label := "OS"
		if isData {
			dataIdx++
			label = "DATA"
			if dataTotal > 1 {
				label = fmt.Sprintf("DATA%d", dataIdx)
			}
		}
		raid := raidLabel(s.RaidMode)

		// A set with no disks and a non-data role is the memory-root OS.
		if len(s.SelectedDisks) == 0 {
			if !isData {
				info.MemOS = true
				methodParts = append(methodParts, "tmpfs OS")
				summaryParts = append(summaryParts, "OS -> tmpfs (RAM)")
			}
			continue
		}

		var names []string
		for _, d := range s.SelectedDisks {
			name := diskBasename(d.ResolvedPath)
			if name == "" {
				continue
			}
			uid := firstNonEmpty(firstNonEmpty(strings.TrimSpace(d.Serial), byPathTail(d.ConfiguredPath)), strings.TrimSpace(d.Model))
			info.Disks[name] = diskInfo{Set: label, Raid: raid, UID: uid}
			names = append(names, name)
		}
		mp := raid
		if mp == "" {
			mp = "disk"
		}
		methodParts = append(methodParts, mp+" "+label)
		seg := label + " -> " + strings.Join(names, ",")
		if raid != "" {
			seg += " (" + raid + ")"
		}
		summaryParts = append(summaryParts, seg)
		if !strings.EqualFold(s.RegisterNVRAM, "no") && !strings.EqualFold(s.RegisterNVRAM, "false") {
			nvram = true
		}
	}

	boot := "Boot: " + firmware + "/GRUB"
	if info.MemOS && len(info.Disks) == 0 {
		boot = "Boot: none (memory-root)"
	} else if strings.EqualFold(firmware, "UEFI") && nvram {
		boot += " + NVRAM"
	}
	summaryParts = append(summaryParts, boot)
	info.Summary = strings.Join(summaryParts, "   ")
	info.Method = strings.Join(methodParts, " + ")
	if info.Method != "" {
		info.Method += " · " + firmware
	}
	return info
}

// configLayout builds the Storage panel's Method line from the disklayout
// config (storage.json), known before any disk is touched. It is deliberately
// terse — just the two things an operator wants at a glance: which selection
// the OS uses (manual vs discover) and what the node boots next (local once a
// disk OS is laid down, or PXE when the OS runs from RAM). The per-set raid and
// disk detail lives in the disk table below, not in this line. It tags no disks
// (not resolved yet); markLayout(parseLayout(...)) tags the table after prepare.
// ok is false when there is nothing useful to show yet.
func configLayout(cfgPath string, mode installcontext.Mode) (info layoutInfo, ok bool) {
	info = layoutInfo{Disks: map[string]diskInfo{}}
	raw, _ := os.ReadFile(cfgPath) //nolint:gosec // G304: runtimeDir path; binary runs as root
	hasCfg := len(strings.TrimSpace(string(raw))) > 0

	// memboot, or auto with no declared layout, runs the OS from RAM -> the node
	// must come back to PXE next boot (there is no local OS to start).
	if mode.IsMemboot() || (mode == installcontext.ModeAuto && !hasCfg) {
		info.MemOS = true
		info.Method = "diskless (RAM) · next boot: PXE"
		return info, true
	}
	if !hasCfg {
		return info, false
	}
	cfg, err := v2.Parse(raw)
	if err != nil || cfg == nil || len(cfg.Sets) == 0 {
		return info, false
	}
	sels := map[string]bool{}
	for i := range cfg.Sets {
		if sel := strings.ToLower(strings.TrimSpace(cfg.Sets[i].SelectionMode())); sel != "" {
			sels[sel] = true
		}
	}
	// A disk OS install leaves a locally-bootable system.
	info.Method = selectionWord(sels) + " · next boot: local"
	return info, true
}

// selectionWord collapses the per-set selection modes into one headline word
// for the Method line: "manual", "discover", or "mixed" when a layout uses both.
func selectionWord(sels map[string]bool) string {
	switch {
	case len(sels) > 1:
		return "mixed"
	case sels["discover"]:
		return "discover"
	default:
		return "manual"
	}
}

// diskBasename returns the whole-disk name for a /dev path
// (/dev/sda -> sda, /dev/nvme0n1 -> nvme0n1). Returns "" if it is not a
// recognised whole-disk path.
func diskBasename(p string) string {
	m := resultDiskRe.FindStringSubmatch(p)
	if m == nil {
		return ""
	}
	return m[1]
}

var resultDiskRe = regexp.MustCompile(`(sd[a-z]+|nvme\d+n\d+|vd[a-z]+|mmcblk\d+)`)

// byPathTail returns a compact-but-stable disk identifier from a
// /dev/disk/by-path value when no serial/WWN is available (the common case
// for virtual disks): the last path component with the redundant
// "pci-0000:" domain prefix stripped, e.g.
// "pci-0000:03:00.0-scsi-0:0:0:0" -> "03:00.0-scsi-0:0:0:0".
func byPathTail(p string) string {
	p = strings.TrimSpace(p)
	if i := strings.LastIndex(p, "/"); i >= 0 {
		p = p[i+1:]
	}
	return strings.TrimPrefix(p, "pci-0000:")
}

// diskUID picks the most useful stable identifier for the ID column:
// serial, else WWN, else model, else the kernel name. On real hardware this
// is the disk serial; virtual disks (no serial/WWN) fall back to the model
// here and to the by-path in the resolved layout.
func diskUID(serial, wwn, model, name string) string {
	return firstNonEmpty(
		firstNonEmpty(strings.TrimSpace(serial), strings.TrimSpace(wwn)),
		firstNonEmpty(strings.TrimSpace(model), name))
}

// raidLabel maps a raid_mode value to its display form ("" when not RAID).
func raidLabel(rm string) string {
	if rm == "" || strings.EqualFold(rm, "none") {
		return ""
	}
	return strings.ToUpper(rm)
}

// --- lsblk (whole disks) --------------------------------------------------

type disk struct {
	Name   string
	Size   int64
	Model  string
	Serial string
	WWN    string
	Rota   bool
	Tran   string
}

func lsblkDisks(ctx context.Context) []disk {
	cctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	out, err := exec.CommandContext(cctx, "lsblk", "-dn", "-J", "-b", "-o", "KNAME,SIZE,MODEL,SERIAL,WWN,ROTA,TRAN,TYPE").Output()
	if err != nil {
		return nil
	}
	var root struct {
		Blockdevices []struct {
			KName  json.RawMessage `json:"kname"`
			Size   json.RawMessage `json:"size"`
			Model  json.RawMessage `json:"model"`
			Serial json.RawMessage `json:"serial"`
			WWN    json.RawMessage `json:"wwn"`
			Rota   json.RawMessage `json:"rota"`
			Tran   json.RawMessage `json:"tran"`
			Type   json.RawMessage `json:"type"`
		} `json:"blockdevices"`
	}
	if json.Unmarshal(out, &root) != nil {
		return nil
	}
	var disks []disk
	for _, e := range root.Blockdevices {
		if rawString(e.Type) != "disk" {
			continue
		}
		name := rawString(e.KName)
		switch {
		case strings.HasPrefix(name, "loop"), strings.HasPrefix(name, "ram"),
			strings.HasPrefix(name, "zram"), strings.HasPrefix(name, "sr"),
			strings.HasPrefix(name, "fd"), strings.HasPrefix(name, "dm-"):
			continue
		}
		disks = append(disks, disk{
			Name:   name,
			Size:   rawInt(e.Size),
			Model:  rawString(e.Model),
			Serial: rawString(e.Serial),
			WWN:    rawString(e.WWN),
			Rota:   rawBool(e.Rota),
			Tran:   rawString(e.Tran),
		})
	}
	return disks
}

// rawString/rawInt/rawBool absorb lsblk's scalar drift (bool vs "1"/"0",
// number vs "<digits>") across util-linux versions.
func rawString(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return strings.Trim(string(raw), "\"")
}

func rawInt(raw json.RawMessage) int64 {
	if len(raw) == 0 {
		return 0
	}
	var i int64
	if json.Unmarshal(raw, &i) == nil {
		return i
	}
	if v, err := strconv.ParseInt(rawString(raw), 10, 64); err == nil {
		return v
	}
	return 0
}

func rawBool(raw json.RawMessage) bool {
	if len(raw) == 0 {
		return false
	}
	var b bool
	if json.Unmarshal(raw, &b) == nil {
		return b
	}
	switch strings.ToLower(rawString(raw)) {
	case "1", "true", "yes":
		return true
	}
	return false
}

// --- host facts -----------------------------------------------------------

func bootMode() string {
	if _, err := os.Stat("/sys/firmware/efi"); err == nil {
		return "UEFI"
	}
	return "BIOS"
}

func unameArch() string {
	if out, err := exec.Command("uname", "-m").Output(); err == nil {
		if s := strings.TrimSpace(string(out)); s != "" {
			return s
		}
	}
	return ""
}

func memTotal() string {
	data, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "MemTotal:") {
			f := strings.Fields(line)
			if len(f) >= 2 {
				if kb, err := strconv.ParseFloat(f[1], 64); err == nil {
					return fmt.Sprintf("%.0fGB", kb/1024/1024)
				}
			}
		}
	}
	return ""
}

func cpuModel() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(data), "\n") {
		// x86 reports "model name"; arm64 has no such field (left blank).
		if strings.HasPrefix(line, "model name") {
			if i := strings.IndexByte(line, ':'); i >= 0 {
				return strings.TrimSpace(line[i+1:])
			}
		}
	}
	return ""
}

// shortCPUModel trims marketing noise from a CPU model name:
// "Intel(R) Xeon(R) Gold 5218 CPU @ 2.30GHz" -> "Intel Xeon Gold 5218".
func shortCPUModel(s string) string {
	s = strings.NewReplacer("(R)", "", "(TM)", "", "(tm)", "").Replace(s)
	if i := strings.Index(s, " CPU @"); i >= 0 {
		s = s[:i]
	}
	if i := strings.Index(s, " @ "); i >= 0 {
		s = s[:i]
	}
	return strings.Join(strings.Fields(s), " ")
}

// cpuTopo summarises CPU topology from /proc/cpuinfo without lscpu (absent in
// the install env): "4 vCPU · 4S/1C/1T" = 4 logical, 4 sockets, 1 core/socket,
// 1 thread/core.
func cpuTopo() string {
	data, err := os.ReadFile("/proc/cpuinfo")
	if err != nil {
		return ""
	}
	logical := 0
	sockets := map[string]struct{}{}
	cores := 0
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, ":")
		if !ok {
			continue
		}
		k, v = strings.TrimSpace(k), strings.TrimSpace(v)
		switch k {
		case "processor":
			logical++
		case "physical id":
			sockets[v] = struct{}{}
		case "cpu cores":
			if n, e := strconv.Atoi(v); e == nil {
				cores = n
			}
		}
	}
	if logical == 0 {
		return ""
	}
	ns := len(sockets)
	if ns == 0 {
		ns = 1
	}
	if cores == 0 {
		cores = logical / ns
	}
	threads := 1
	if ns*cores > 0 {
		threads = logical / (ns * cores)
	}
	if threads < 1 {
		threads = 1
	}
	return fmt.Sprintf("%d vCPU · %dS/%dC/%dT", logical, ns, cores, threads)
}

// gpuSummary tallies discrete GPUs from PCI class+vendor in /sys (no lspci
// dependency). Class 0x0300 (VGA) / 0x0302 (3D) with vendor 0x10de (NVIDIA)
// or 0x1002 (AMD) count as accelerators; VMware/Intel display adapters do
// not. Returns e.g. "2 x NVIDIA", "1 x AMD", or "none".
func gpuSummary() string {
	devs, err := os.ReadDir("/sys/bus/pci/devices")
	if err != nil {
		return ""
	}
	nv, amd := 0, 0
	for _, d := range devs {
		base := filepath.Join("/sys/bus/pci/devices", d.Name())
		class := readTrim(filepath.Join(base, "class"))
		if !strings.HasPrefix(class, "0x0300") && !strings.HasPrefix(class, "0x0302") {
			continue
		}
		switch strings.ToLower(readTrim(filepath.Join(base, "vendor"))) {
		case "0x10de":
			nv++
		case "0x1002":
			amd++
		}
	}
	var parts []string
	if nv > 0 {
		parts = append(parts, fmt.Sprintf("%d x NVIDIA", nv))
	}
	if amd > 0 {
		parts = append(parts, fmt.Sprintf("%d x AMD", amd))
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}

func nicSummary() string {
	entries, err := os.ReadDir("/sys/class/net")
	if err != nil {
		return ""
	}
	n := 0
	for _, e := range entries {
		name := e.Name()
		switch {
		case name == "lo",
			strings.HasPrefix(name, "docker"), strings.HasPrefix(name, "virbr"),
			strings.HasPrefix(name, "veth"), strings.HasPrefix(name, "tap"),
			strings.HasPrefix(name, "br-"):
			continue
		}
		n++
	}
	if n == 0 {
		return ""
	}
	return fmt.Sprintf("%d interface(s)", n)
}

func readTrim(path string) string {
	data, err := os.ReadFile(path) //nolint:gosec // G304: well-known /sys + /proc fact paths; binary runs as root

	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}

func humanSize(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%dB", b)
	}
	units := []string{"KB", "MB", "GB", "TB", "PB"}
	v := float64(b)
	i := -1
	for v >= unit && i < len(units)-1 {
		v /= unit
		i++
	}
	return fmt.Sprintf("%.1f%s", v, units[i])
}
