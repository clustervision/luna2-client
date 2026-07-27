package tui

import (
	"github.com/charmbracelet/lipgloss"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/uiutil"
)

// Styles bundles the recurring lipgloss styles into one struct so they
// can be cheaply re-rendered each frame.
type Styles struct {
	Border       lipgloss.Style
	TitleAccent  lipgloss.Style
	HeaderBanner lipgloss.Style
	HeaderTime   lipgloss.Style
	Dim          lipgloss.Style
	Label        lipgloss.Style
	Value        lipgloss.Style
	TableHeader  lipgloss.Style
	CursorRow    lipgloss.Style
	Selected     lipgloss.Style
	FooterKey    lipgloss.Style
	FooterText   lipgloss.Style
	StageDone    lipgloss.Style
	StageRunning lipgloss.Style
	StagePending lipgloss.Style
	StageFailed  lipgloss.Style
	StageWarn    lipgloss.Style
	BannerSep    lipgloss.Style
}

// NewStyles constructs a fresh palette-bound Styles bundle.
func NewStyles() Styles {
	p := uiutil.Palette
	return Styles{
		Border: lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(p.Border),
		TitleAccent: lipgloss.NewStyle().
			Foreground(p.Accent).Bold(true),
		HeaderBanner: lipgloss.NewStyle().
			Foreground(p.Normal).Bold(true),
		HeaderTime: lipgloss.NewStyle().
			Foreground(p.Dim),
		Dim:   lipgloss.NewStyle().Foreground(p.Dim),
		Label: lipgloss.NewStyle().Foreground(p.Dim),
		Value: lipgloss.NewStyle().Foreground(p.Normal),
		TableHeader: lipgloss.NewStyle().
			Foreground(p.Dim).Bold(true),
		CursorRow: lipgloss.NewStyle().
			Foreground(p.Accent).Bold(true),
		Selected: lipgloss.NewStyle().
			Foreground(p.Success).Bold(true),
		FooterKey:  lipgloss.NewStyle().Foreground(p.Accent).Bold(true),
		FooterText: lipgloss.NewStyle().Foreground(p.Dim),
		StageDone: lipgloss.NewStyle().
			Foreground(p.Success).Bold(true),
		StageRunning: lipgloss.NewStyle().
			Foreground(p.Accent).Bold(true),
		StagePending: lipgloss.NewStyle().Foreground(p.Dim),
		StageFailed: lipgloss.NewStyle().
			Foreground(p.Error).Bold(true),
		StageWarn: lipgloss.NewStyle().
			Foreground(p.Warning).Bold(true),
		BannerSep: lipgloss.NewStyle().Foreground(p.Border),
	}
}
