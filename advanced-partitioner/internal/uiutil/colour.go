package uiutil

import (
	"github.com/charmbracelet/lipgloss"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// Palette is the calm sysadmin palette from tui-design.plan §"Colour
// intent". Keep it boring — no rainbow logs.
var Palette = struct {
	Border      lipgloss.Color
	Dim         lipgloss.Color
	Normal      lipgloss.Color
	Accent      lipgloss.Color
	Stage       lipgloss.Color
	Command     lipgloss.Color
	Destructive lipgloss.Color
	Warning     lipgloss.Color
	Error       lipgloss.Color
	Success     lipgloss.Color
	Device      lipgloss.Color
}{
	Border:      lipgloss.Color("240"),
	Dim:         lipgloss.Color("245"),
	Normal:      lipgloss.Color("252"),
	Accent:      lipgloss.Color("75"),
	Stage:       lipgloss.Color("141"),
	Command:     lipgloss.Color("39"),
	Destructive: lipgloss.Color("208"),
	Warning:     lipgloss.Color("214"),
	Error:       lipgloss.Color("160"),
	Success:     lipgloss.Color("114"),
	Device:      lipgloss.Color("117"),
}

// StyleFor picks the foreground style for a classified line. Pure
// function so the renderer can call it per-line without ceremony.
func StyleFor(class model.LineClass) lipgloss.Style {
	s := lipgloss.NewStyle()
	switch class {
	case model.ClassError:
		return s.Foreground(Palette.Error).Bold(true)
	case model.ClassDestructive:
		return s.Foreground(Palette.Destructive).Bold(true)
	case model.ClassWarning:
		return s.Foreground(Palette.Warning)
	case model.ClassCommand:
		return s.Foreground(Palette.Command)
	case model.ClassStage, model.ClassMarker:
		return s.Foreground(Palette.Stage)
	case model.ClassSuccess:
		return s.Foreground(Palette.Success)
	case model.ClassStderr:
		return s.Foreground(Palette.Warning)
	case model.ClassDevice:
		return s.Foreground(Palette.Device).Bold(true)
	default:
		return s.Foreground(Palette.Normal)
	}
}
