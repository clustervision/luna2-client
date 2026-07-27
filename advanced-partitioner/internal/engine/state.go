package engine

import (
	"sync"
	"time"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// State is the engine-owned aggregate; the TUI receives a copy via
// events and never mutates this directly.
type State struct {
	mu        sync.Mutex
	installer model.Installer
}

// NewState returns a State pre-populated with the default stage list. The
// storage panel starts empty ("discovery in progress"); real targets — the
// physical disks, and a tmpfs row only when the OS actually targets RAM —
// are filled in by discovery + the resolved layout. The installer's own
// memory staging area is never shown as a target.
func NewState() *State {
	return &State{
		installer: model.Installer{
			StartedAt: time.Now(),
			Stages:    DefaultStages(),
			Storage: model.Storage{
				Memory:     "unknown",
				TmpfsAvail: true,
			},
		},
	}
}

// Snapshot returns a deep-enough copy of the current installer state.
// Slices are copied so the TUI can render without holding the lock.
func (s *State) Snapshot() model.Installer {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.installer
	out.Stages = append([]model.Stage(nil), s.installer.Stages...)
	out.Storage.Candidates = append([]model.StorageCandidate(nil), s.installer.Storage.Candidates...)
	out.Output.Lines = append([]model.OutputLine(nil), s.installer.Output.Lines...)
	return out
}
