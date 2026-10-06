package tui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/uiutil"
)

// Layout thresholds. The full dashboard needs room for a 3-line header,
// a footer, and three vertically-stacked bordered panel rows. On a
// booting server the TTY size comes from the firmware console driver
// (efifb / vesafb / VGA / serial / IPMI SOL) and the OS cannot grow it
// past what the driver exposes - so we keep a compact fallback that
// works on the universal 80x24 minimum.
const (
	fullMinWidth  = 100
	fullMinHeight = 30
	hardMinWidth  = 60
	hardMinHeight = 18
)

// View renders the single-screen dashboard. Two layouts:
//
//	full   (≥100x30): header / Luna|Node / storage / progress|output / footer
//	compact (80x24+): header / storage / output / footer
//
// Below 60x18 we fail with a clear message rather than render garbage.
func (m Model) View() string {
	w := m.Width
	h := m.Height
	if w < hardMinWidth || h < hardMinHeight {
		return fmt.Sprintf("terminal too small (%dx%d) - need at least %dx%d\n",
			w, h, hardMinWidth, hardMinHeight)
	}
	if w < fullMinWidth || h < fullMinHeight {
		return m.viewCompact(w, h)
	}
	return m.viewFull(w, h)
}

func (m Model) viewFull(w, h int) string {
	header := m.renderHeader(w)
	footer := m.renderFooter(w)

	// Reserved heights.
	const headerH = 2 // banner row + tagline (the Partition/Image/Boot line is gone)
	const footerH = 2 // blank spacer line + the keybinding row
	const sepH = 0    // borders inside panels handle separators

	usable := h - headerH - footerH - sepH
	if usable < 6 {
		usable = 6
	}

	// Three rows:
	//   row1 (top):    Luna (identity + arch/vendor)  |  Discovery (hardware)
	//   row2 (middle): Progress (stages)              |  Storage (disk table)
	//   row3 (bottom): Run output, full width (the most space)
	// The Node panel is gone - its arch/vendor/model folded into Luna.
	topH := 9
	// Middle row sized to the larger of the stage list and the disk table,
	// then capped so the rest goes to the full-width Run output below.
	midNeed := len(m.State.Storage.Candidates) + 7
	if n := len(m.State.Stages) + 4; n > midNeed {
		midNeed = n
	}
	if midNeed < 9 {
		midNeed = 9
	}
	midH := midNeed
	if midH > usable-topH-5 {
		midH = usable - topH - 5
	}
	if midH < 6 {
		midH = 6
	}
	outputH := usable - topH - midH
	if outputH < 5 {
		outputH = 5
		if topH+midH+outputH > usable && topH > 6 {
			topH = usable - midH - outputH
		}
	}

	leftW, rightW := uiutil.SplitHorizontal(w, 0.5, 20)
	top := lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderLuna(leftW, topH),
		m.renderDiscovery(rightW, topH))

	progW, storeW := uiutil.SplitHorizontal(w, 0.42, 20)
	mid := lipgloss.JoinHorizontal(lipgloss.Top,
		m.renderProgress(progW, midH),
		m.renderStorage(storeW, midH))

	output := m.renderOutput(w, outputH)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		top,
		mid,
		output,
		"",
		footer,
	)
}

// viewCompact is the narrow-terminal layout: header -> storage candidate
// list -> output panel -> footer. Drops the Luna/Node + progress panels;
// stage status moves into the header line.
func (m Model) viewCompact(w, h int) string {
	header := m.renderCompactHeader(w)
	footer := m.renderFooter(w)
	const compactHeaderH = 2
	const footerH = 2 // blank spacer line + the keybinding row
	usable := h - compactHeaderH - footerH
	if usable < 6 {
		usable = 6
	}
	// Split between storage (top) and output (bottom). Storage gets
	// slightly less since the table + counts + details want fewer rows
	// than a flowing log.
	storageH := usable * 4 / 10
	if storageH < 6 {
		storageH = 6
	}
	outputH := usable - storageH
	if outputH < 3 {
		outputH = 3
	}
	storage := m.renderStorage(w, storageH)
	output := m.renderOutput(w, outputH)
	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		storage,
		output,
		"",
		footer,
	)
}

// renderCompactHeader merges the 3-line banner into 2 lines + a status
// line summarising stage progress, so vertical real estate is spent on
// the panels.
func (m Model) renderCompactHeader(w int) string {
	clock := m.now.Format("15:04:05")
	title := "◆ TrinityX Compute Node Installer"
	leftW := w - len(clock) - 1
	if leftW < 1 {
		leftW = 1
	}
	line1 := lipgloss.JoinHorizontal(lipgloss.Top,
		m.styles.HeaderBanner.Render(uiutil.Truncate(title, leftW)),
		strings.Repeat(" ", maxInt(1, w-lipgloss.Width(uiutil.Truncate(title, leftW))-lipgloss.Width(clock))),
		m.styles.HeaderTime.Render(clock),
	)
	// Stage line: ✓ done / > running / ○ pending, abbreviated titles.
	var parts []string
	for _, st := range m.State.Stages {
		parts = append(parts, fmt.Sprintf("%s %s", stageGlyph(st.Status), shortStage(st.Title)))
	}
	stageLine := m.styles.Dim.Render(uiutil.Truncate(strings.Join(parts, " · "), w))
	return lipgloss.JoinVertical(lipgloss.Left, line1, stageLine)
}

// shortStage trims stage titles for compact layouts.
func shortStage(s string) string {
	switch s {
	case "Prepare storage":
		return "stor"
	case "Resolve install context":
		return "ctx"
	case "Partitioning":
		return "part"
	case "OS image", "OS image install":
		return "img"
	case "Finalise":
		return "fin"
	case "Preflight":
		return "pre"
	case "Post":
		return "post"
	}
	if len(s) > 5 {
		return s[:5]
	}
	return s
}

// renderHeader is the 3-line banner with a clock on the right.
func (m Model) renderHeader(w int) string {
	clock := m.now.Format("15:04:05")
	line1 := "◆ TrinityX Compute Node Installer"
	line3 := "Target-aware installation for HPC and AI compute nodes"

	leftW := w - len(clock) - 1
	if leftW < 1 {
		leftW = 1
	}
	l1 := m.styles.HeaderBanner.Render(uiutil.Truncate(line1, leftW))
	t := m.styles.HeaderTime.Render(clock)
	header1 := lipgloss.JoinHorizontal(lipgloss.Top,
		l1,
		strings.Repeat(" ", maxInt(1, w-lipgloss.Width(l1)-lipgloss.Width(t))),
		t,
	)
	header3 := m.styles.Dim.Render(uiutil.Truncate(line3, w))
	return lipgloss.JoinVertical(lipgloss.Left, header1, header3)
}

// renderFooter prints the keymap hint row.
func (m Model) renderFooter(w int) string {
	parts := []string{
		m.styles.FooterKey.Render("Up/Dn") + m.styles.FooterText.Render(" Scroll"),
		m.styles.FooterKey.Render("F2") + m.styles.FooterText.Render(" Shell"),
		m.styles.FooterKey.Render("F10") + m.styles.FooterText.Render(" Reboot"),
		m.styles.FooterKey.Render("^L") + m.styles.FooterText.Render(" Redraw"),
		m.styles.FooterKey.Render("q") + m.styles.FooterText.Render(" Quit"),
	}
	row := strings.Join(parts, "   ")
	if lipgloss.Width(row) > w {
		row = uiutil.Truncate(row, w)
	}
	return row
}

// renderLuna draws the controller + node-identity panel. The old separate
// Node panel is folded in here (Arch + Vendor/Model); CPU/GPU/etc. live in
// the Discovery panel. Install-mode/osimage-sync are omitted (shown via the
// Storage Method line + Run output) to keep this to identity.
func (m Model) renderLuna(w, h int) string {
	l := m.State.Luna
	n := m.State.Node
	vendorModel := strOr(joinNonEmpty(n.Vendor, n.Model), "-")
	rows := []string{
		labelValue(m.styles, "Controller", strOr(l.Controller, "-")),
		labelValue(m.styles, "Target IP", strOr(l.TargetIP, "-")),
		labelValue(m.styles, "Hostname", strOr(l.Hostname, "-")),
		labelValue(m.styles, "Image", strOr(l.Image, "-")),
		labelValue(m.styles, "Arch", strOr(n.Arch, "-")),
		labelValue(m.styles, "Vendor/Model", vendorModel),
	}
	return panelBox(m.styles, "Luna", rows, w, h)
}

// renderStorage draws the middle target/storage panel. Includes the
// counts line, the candidate table, scroll markers, and the details
// block for the highlighted row.
func (m Model) renderStorage(w, h int) string {
	s := m.State.Storage
	method := strOr(s.Method, "(resolving...)")

	cands := s.Candidates
	// Rows the chrome consumes: Method + blank + header (+ a possible "v more");
	// the rest is the candidate window. The table is informational (no cursor /
	// no key scroll — Up/Dn drive the Run-output panel); it shows from the top
	// and marks "v more" if it overflows, which is rare.
	tableH := h - 5
	if tableH < 3 {
		tableH = 3
	}
	end := tableH
	if end > len(cands) {
		end = len(cands)
	}

	// Columns: Target Type Size [ID] Set RAID. No Use column - a non-empty
	// Set already means the disk is part of the install.
	// Narrow consoles drop the ID column.
	narrow := w-4 < 56

	var hdr string
	if narrow {
		hdr = m.styles.TableHeader.Render(
			fmt.Sprintf(" %-9s %-5s %-8s %-6s %-6s", "Target", "Type", "Size", "Set", "RAID"))
	} else {
		hdr = m.styles.TableHeader.Render(
			fmt.Sprintf(" %-9s %-5s %-8s %-20s %-6s %-6s", "Target", "Type", "Size", "ID", "Set", "RAID"))
	}

	lines := []string{m.styles.TitleAccent.Render("Method: " + method), "", hdr}
	for i := 0; i < end; i++ {
		c := cands[i]
		var row string
		if narrow {
			row = fmt.Sprintf(" %-9s %-5s %-8s %-6s %-6s",
				uiutil.Truncate(c.Name, 9), uiutil.Truncate(string(c.Kind), 5),
				uiutil.Truncate(c.Size, 8), uiutil.Truncate(c.Set, 6), uiutil.Truncate(c.RaidType, 6))
		} else {
			row = fmt.Sprintf(" %-9s %-5s %-8s %-20s %-6s %-6s",
				uiutil.Truncate(c.Name, 9), uiutil.Truncate(string(c.Kind), 5),
				uiutil.Truncate(c.Size, 8), uiutil.Truncate(strOr(c.UID, "-"), 20),
				uiutil.Truncate(c.Set, 6), uiutil.Truncate(c.RaidType, 6))
		}
		lines = append(lines, row)
	}
	if end < len(cands) {
		lines = append(lines, m.styles.Dim.Render(fmt.Sprintf("  v %d more", len(cands)-end)))
	}
	if len(cands) == 0 {
		lines = append(lines, m.styles.Dim.Render("(discovery in progress...)"))
	}

	body := strings.Join(lines, "\n")
	return panelBox(m.styles, "Storage", []string{body}, w, h)
}

// renderDiscovery draws the hardware panel beside the disk table: CPU, GPU,
// memory, NICs, disk-type counts, and a one-line detail for the highlighted
// disk row (so the storage table itself stays lean).
func (m Model) renderDiscovery(w, h int) string {
	n := m.State.Node
	s := m.State.Storage
	rows := []string{labelValue(m.styles, "CPU", strOr(n.CPU, "-"))}
	if n.CPUTopo != "" {
		rows = append(rows, m.styles.Dim.Render("    "+n.CPUTopo))
	}
	rows = append(rows,
		labelValue(m.styles, "Memory", strOr(s.Memory, "-")),
		labelValue(m.styles, "GPU", strOr(n.GPU, "-")),
		labelValue(m.styles, "NIC / IB", strOr(joinNonEmpty(n.NICs, n.IB), "-")),
		labelValue(m.styles, "Disks", diskCounts(s)),
	)
	for i, r := range rows {
		rows[i] = uiutil.Truncate(r, w-4)
	}
	return panelBox(m.styles, "Discovery", rows, w, h)
}

// diskCounts summarises the discovered disk inventory by media type.
func diskCounts(s model.Storage) string {
	var parts []string
	if s.NVMe > 0 {
		parts = append(parts, fmt.Sprintf("%d NVMe", s.NVMe))
	}
	if s.SSD > 0 {
		parts = append(parts, fmt.Sprintf("%d SSD", s.SSD))
	}
	if s.Spindles > 0 {
		parts = append(parts, fmt.Sprintf("%d HDD", s.Spindles))
	}
	if len(parts) == 0 {
		return "-"
	}
	return strings.Join(parts, " · ")
}

// renderProgress draws the stage list. The list scrolls so the
// currently-running stage (or the last failed one) stays visible -
// without this, long stage lists pushed the active line off-panel and
// the operator couldn't tell what was happening.
func (m Model) renderProgress(w, h int) string {
	stages := m.State.Stages
	stageH := h - 2 - 1 // borders + title
	if stageH < 1 {
		stageH = 1
	}

	// Decide which row should be on-screen as a priority.
	focus := -1
	for i, st := range stages {
		if st.Status == model.StageRunning || st.Status == model.StageFailed {
			focus = i
		}
	}
	if focus < 0 {
		// no running/failed -> keep the first-pending stage in view
		for i, st := range stages {
			if st.Status == model.StagePending {
				focus = i
				break
			}
		}
	}

	start := 0
	if len(stages) > stageH && focus >= 0 {
		// Centre-ish: keep focus 1 row from the bottom so upcoming
		// stages remain visible too.
		start = focus - (stageH - 2)
		if start < 0 {
			start = 0
		}
		if start > len(stages)-stageH {
			start = len(stages) - stageH
		}
	}
	end := start + stageH
	if end > len(stages) {
		end = len(stages)
	}

	var lines []string
	for i := start; i < end; i++ {
		st := stages[i]
		glyph := stageGlyph(st.Status)
		style := m.styles.StagePending
		switch st.Status {
		case model.StageDone:
			style = m.styles.StageDone
		case model.StageRunning:
			style = m.styles.StageRunning
		case model.StageFailed:
			style = m.styles.StageFailed
		case model.StageWarn:
			style = m.styles.StageWarn
		}
		row := fmt.Sprintf("%s  %s", glyph, st.Title)
		// Show a note only on a FINISHED stage, and only when it adds info
		// beyond the title. A running/pending stage's note is just the
		// stageStart message restating the title ("Prepare storage  Prepare
		// storage", "Bootloader & GRUB finalise  Bootloader finalise"); the
		// stageDone note ("source=daemon ...") and a failure reason still show.
		terminal := st.Status == model.StageDone || st.Status == model.StageFailed || st.Status == model.StageWarn
		if terminal && st.Note != "" && st.Note != st.Title {
			row += "  " + st.Note
		}
		lines = append(lines, style.Render(row))
	}
	// Tiny scroll markers when content is cropped (ASCII for the VGA console).
	if len(lines) > 0 {
		if start > 0 {
			lines[0] = m.styles.Dim.Render("^ ") + lines[0]
		}
		if end < len(stages) {
			lines[len(lines)-1] = lines[len(lines)-1] + m.styles.Dim.Render(" v")
		}
	}
	body := strings.Join(lines, "\n")
	return panelBox(m.styles, "Progress", []string{body}, w, h)
}

// renderOutput draws the bottom-right run-output panel. Lines are
// rendered via uiutil.StyleFor - raw text untouched, render-only colour.
// An error line is soft-wrapped so the whole reason reaches the console (a
// failed install reboots in ~30 s: "stderr=...~" is the one thing a reader
// cannot do without); every other class keeps the cut, or ordinary command
// chatter would scroll the panel's history away.
func (m Model) renderOutput(w, h int) string {
	inner := h - 2 // panel border eats two rows
	if inner < 1 {
		inner = 1
	}
	lines := m.State.Output.Lines
	// Newest visible line, OutputScroll lines back from the tail; the window
	// always fills the panel when there is that much history.
	end := len(lines) - m.OutputScroll
	if end < inner {
		end = inner
	}
	if end > len(lines) {
		end = len(lines)
	}
	// Fill from the bottom: a wrapped line occupies several rows, so how many
	// lines fit is only known as they are rendered.
	var rendered []string
	for i := end - 1; i >= 0 && len(rendered) < inner; i-- {
		line := lines[i]
		var rows []string
		if line.Class == model.ClassError {
			rows = uiutil.Wrap(line.Raw, w-4)
		} else {
			rows = []string{uiutil.Truncate(line.Raw, w-4)}
		}
		// The topmost line is scrolling off: keep the rows that still fit.
		if room := inner - len(rendered); len(rows) > room {
			rows = rows[len(rows)-room:]
		}
		style := uiutil.StyleFor(line.Class)
		for j := range rows {
			rows[j] = style.Render(rows[j])
		}
		rendered = append(rows, rendered...)
	}
	// Pad to fill the panel so the bottom border lands consistently.
	for len(rendered) < inner {
		rendered = append(rendered, "")
	}
	title := "Run output"
	if m.OutputScroll > 0 && end < len(lines) {
		title = fmt.Sprintf("Run output [scrolled +%d  -  Dn to follow]", m.OutputScroll)
	}
	body := strings.Join(rendered, "\n")
	return panelBox(m.styles, title, []string{body}, w, h)
}

// --- helpers ---

// panelBox wraps title + body into a bordered box of exact outer
// dimensions w x h. Two hard guarantees:
//
//  1. Vertical clip: body is capped to h-3 lines (1 title + 2 border).
//  2. Horizontal clip: every line is truncated to the panel's inner
//     width. Without this, lipgloss would wrap long content onto
//     extra visual rows and the panel would push its sibling panels
//     down - the root cause of the "stacked Progress" glitch.
func panelBox(s Styles, title string, body []string, w, h int) string {
	innerW := w - 4 // borders (2) + lipgloss left/right padding (2)
	if innerW < 1 {
		innerW = 1
	}
	innerH := h - 2 - 1
	if innerH < 1 {
		innerH = 1
	}
	header := s.TitleAccent.Render(uiutil.Truncate(title, innerW))
	clipped := clipBody(body, innerH, innerW)
	content := header + "\n" + strings.Join(clipped, "\n")
	return s.Border.
		Width(w - 2).
		Height(h - 2).
		Render(content)
}

// clipBody expands every body block on \n, truncates each line to
// maxWidth visible cells, then caps at maxLines. Caller is responsible
// for the final \n joins.
func clipBody(body []string, maxLines, maxWidth int) []string {
	var flat []string
	for _, b := range body {
		for _, line := range strings.Split(b, "\n") {
			flat = append(flat, uiutil.Truncate(line, maxWidth))
		}
	}
	if len(flat) > maxLines {
		flat = flat[:maxLines]
	}
	return flat
}

func labelValue(s Styles, label, value string) string {
	return s.Label.Render(fmt.Sprintf("%-14s", label+":")) + " " + s.Value.Render(value)
}

func strOr(a, fallback string) string {
	a = strings.TrimSpace(a)
	if a == "" {
		return fallback
	}
	return a
}

func joinNonEmpty(parts ...string) string {
	var out []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			out = append(out, p)
		}
	}
	return strings.Join(out, " / ")
}

// stageGlyph uses ASCII only - the node's VGA console renders ✓/✗/○ as a
// solid ■ block (see uiutil.StageMarker). Colour still distinguishes states.
func stageGlyph(status model.StageStatus) string {
	switch status {
	case model.StageDone:
		return "+"
	case model.StageRunning:
		return ">"
	case model.StageFailed:
		return "x"
	case model.StageWarn:
		return "!"
	default:
		return "-"
	}
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// staleness compile-tag; not invoked.
var _ = time.Time{}
