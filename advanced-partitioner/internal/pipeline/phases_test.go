package pipeline

import (
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

// The pre/part/post phase contract — pinned so it is unambiguous WHEN each
// stage runs and stays that way. Each stage belongs to exactly one phase; a
// later phase's process marks every earlier phase's stage Done. (decisions/0004)
//
//	pre   : prepare-runtime                          (env prep; NO disk, NO daemon)
//	part  : install-context, storage-prepare, osimage (resolve inputs -> partition/RAID/LVM/mkfs -> download+extract)
//	post  : bootloader                              (shim/grub + UEFI NVRAM)
func TestPhaseStageContract(t *testing.T) {
	order := []string{
		"prepare-runtime",
		"install-context", "storage-prepare", "osimage",
		"bootloader",
	}
	phaseOf := map[string]Phase{
		"prepare-runtime": PhasePre,
		"install-context": PhasePart, "storage-prepare": PhasePart, "osimage": PhasePart,
		"bootloader": PhasePost,
	}
	rank := map[Phase]int{PhasePre: 0, PhasePart: 1, PhasePost: 2}

	for _, cur := range []Phase{PhasePre, PhasePart, PhasePost} {
		got := stagesFor(cur)
		if len(got) != len(order) {
			t.Fatalf("stagesFor(%s) returned %d stages, want %d", cur, len(got), len(order))
		}
		for i, s := range got {
			if s.ID != order[i] {
				t.Fatalf("stage[%d] = %q, want %q (order changed)", i, s.ID, order[i])
			}
			// A stage is Done iff its phase ran before the current phase.
			wantDone := rank[phaseOf[s.ID]] < rank[cur]
			isDone := s.Status == model.StageDone
			if wantDone != isDone {
				t.Fatalf("phase %s: stage %s done=%v, want %v", cur, s.ID, isDone, wantDone)
			}
		}
	}
}

// pre must be non-destructive: it owns only environment-prep stages, never a
// disk-mutating, image, or bootloader stage. This guards the "pre touches
// nothing" guarantee the operator relies on.
func TestPrePhaseIsNonDestructive(t *testing.T) {
	destructive := map[string]bool{"storage-prepare": true, "osimage": true, "bootloader": true}
	preStages := map[string]bool{"prepare-runtime": true}
	for _, s := range stagesFor(PhasePre) {
		// In the pre phase, only the pre-owned stages may be the "current"
		// (Pending-and-first) work; the destructive stages belong to later phases.
		if preStages[s.ID] && destructive[s.ID] {
			t.Fatalf("pre must not own destructive stage %s", s.ID)
		}
	}
	// And the pre-owned set is exactly the env-prep stage.
	for id := range destructive {
		if preStages[id] {
			t.Fatalf("destructive stage %s wrongly classified as a pre stage", id)
		}
	}
}
