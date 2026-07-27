package pipeline

import (
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

func TestStagesFor(t *testing.T) {
	// Every phase now shows the WHOLE pipeline (5 stages) so the operator sees
	// all the steps up front; the difference is which stages are pre-marked
	// done. pre=0 done, part=1 done (the pre stage), post=4 done (pre+part).
	for _, ph := range []Phase{PhasePre, PhasePart, PhasePost} {
		if got := len(stagesFor(ph)); got != 5 {
			t.Fatalf("%s: stages = %d, want 5 (full pipeline)", ph, got)
		}
	}
	countDone := func(ph Phase) int {
		n := 0
		for _, s := range stagesFor(ph) {
			if s.Status == model.StageDone {
				n++
			}
		}
		return n
	}
	for ph, want := range map[Phase]int{PhasePre: 0, PhasePart: 1, PhasePost: 4} {
		if got := countDone(ph); got != want {
			t.Fatalf("%s: done stages = %d, want %d", ph, got, want)
		}
	}
	// The last stage is the post-phase bootloader/GRUB finalise — the step the
	// part-phase list used to omit.
	all := stagesFor(PhasePart)
	if all[len(all)-1].ID != "bootloader" {
		t.Fatalf("last stage = %q, want bootloader", all[len(all)-1].ID)
	}
}

func TestDefaultNodeNameFromEnv(t *testing.T) {
	t.Setenv("_HOSTNAME", "node042")
	if got := defaultNodeName(); got != "node042" {
		t.Fatalf("defaultNodeName = %q, want node042", got)
	}
}
