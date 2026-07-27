package model_test

import (
	"testing"

	"gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"
)

func TestStorageSelection(t *testing.T) {
	tmpfs := model.StorageCandidate{Name: "tmpfs", Kind: model.KindTmpfs}
	nvme0 := model.StorageCandidate{Name: "nvme0n1", Kind: model.KindNVMe}
	nvme1 := model.StorageCandidate{Name: "nvme1n1", Kind: model.KindNVMe}
	sda := model.StorageCandidate{Name: "sda", Kind: model.KindSSD}

	s := model.Storage{Candidates: []model.StorageCandidate{tmpfs, nvme0, nvme1, sda}}

	// No selection -> every row unchecked.
	for _, c := range s.Candidates {
		if s.IsSelected(c) {
			t.Errorf("nothing selected; row %q should be unchecked", c.Name)
		}
	}

	// tmpfs selected -> only tmpfs ticked.
	s.SelectTarget("tmpfs")
	if !s.IsSelected(tmpfs) {
		t.Error("tmpfs should be selected")
	}
	for _, c := range []model.StorageCandidate{nvme0, nvme1, sda} {
		if s.IsSelected(c) {
			t.Errorf("%q should be unchecked when tmpfs is selected", c.Name)
		}
	}

	// Block device selected -> tmpfs unchecked, that device ticked.
	s.SelectTarget("nvme1n1")
	if s.IsSelected(tmpfs) {
		t.Error("tmpfs should be unchecked when block device is selected")
	}
	if !s.IsSelected(nvme1) {
		t.Error("nvme1n1 should be selected")
	}
	if s.IsSelected(nvme0) {
		t.Error("nvme0n1 should not be selected")
	}
}

func TestMarkStageUnknownIsNoOp(t *testing.T) {
	i := model.Installer{
		Stages: []model.Stage{
			{ID: "preflight", Status: model.StagePending},
		},
	}
	i.MarkStage("unknown", model.StageRunning, "")
	if i.Stages[0].Status != model.StagePending {
		t.Errorf("unknown stage update mutated known stage: %+v", i.Stages[0])
	}
	i.MarkStage("preflight", model.StageDone, "ok")
	if i.Stages[0].Status != model.StageDone {
		t.Errorf("preflight should be done: %+v", i.Stages[0])
	}
	if i.Stages[0].Note != "ok" {
		t.Errorf("preflight note: got %q want ok", i.Stages[0].Note)
	}
}

func TestEnsureStageGrowsList(t *testing.T) {
	i := model.Installer{}
	if !i.EnsureStage("storage-prepare", "Prepare storage") {
		t.Fatal("EnsureStage should append a new stage and return true")
	}
	if len(i.Stages) != 1 || i.Stages[0].ID != "storage-prepare" || i.Stages[0].Status != model.StagePending {
		t.Fatalf("appended stage wrong: %+v", i.Stages)
	}
	if i.EnsureStage("storage-prepare", "again") {
		t.Fatal("EnsureStage should be a no-op (false) for an existing stage")
	}
	if len(i.Stages) != 1 {
		t.Fatalf("duplicate stage appended: %+v", i.Stages)
	}
	// Title falls back to ID.
	i.EnsureStage("osimage", "")
	if i.Stages[1].Title != "osimage" {
		t.Fatalf("empty title should fall back to ID, got %q", i.Stages[1].Title)
	}
	// Empty ID is rejected.
	if i.EnsureStage("", "x") {
		t.Fatal("empty ID should not be appended")
	}
}
