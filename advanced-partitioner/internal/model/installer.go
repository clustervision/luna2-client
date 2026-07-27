// Package model holds typed state structs the TUI renders. The TUI never
// mutates these directly: the engine produces them, and the TUI reads.
package model

import "time"

// StageStatus is the lifecycle marker for a pipeline stage.
type StageStatus string

const (
	StagePending StageStatus = "pending"
	StageRunning StageStatus = "running"
	StageDone    StageStatus = "done"
	StageWarn    StageStatus = "warning"
	StageFailed  StageStatus = "failed"
)

// Stage is a logical phase of the installer.
type Stage struct {
	ID     string
	Title  string
	Status StageStatus
	Note   string
}

// Installer is the top-level state aggregated from engine events.
type Installer struct {
	StartedAt time.Time
	Stages    []Stage
	Active    string
	Luna      Luna
	Node      Node
	Storage   Storage
	Output    Output
	Done      bool
	ExitCode  int
	Err       string
}

// EnsureStage appends a pending stage with the given ID/title if one is not
// already present, and reports whether it added it. It lets a bus-driven
// renderer (lpart-tui) that has no pre-seeded plan grow its stage list in the
// order stages first start — which, for a top-to-bottom install, is the right
// order. The in-process pipeline pre-seeds every stage, so EnsureStage is a
// no-op there. Title falls back to ID when empty.
func (i *Installer) EnsureStage(id, title string) bool {
	if id == "" {
		return false
	}
	for idx := range i.Stages {
		if i.Stages[idx].ID == id {
			return false
		}
	}
	if title == "" {
		title = id
	}
	i.Stages = append(i.Stages, Stage{ID: id, Title: title, Status: StagePending})
	return true
}

// MarkStage updates a stage by ID. Unknown IDs are ignored so the
// installer is free to emit markers for stages we don't know about
// without crashing the renderer.
func (i *Installer) MarkStage(id string, st StageStatus, note string) {
	for idx := range i.Stages {
		if i.Stages[idx].ID == id {
			i.Stages[idx].Status = st
			i.Stages[idx].Note = note
			if st == StageRunning {
				i.Active = id
			}
			return
		}
	}
}
