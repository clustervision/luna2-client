package engine

import "gitlab.taurusgroup.one/clustervision/luna2-client/advanced-partitioner/internal/model"

// DefaultStages returns the placeholder stage list from tui-design.plan
// §"Progress". Stages are configurable so a future migration that
// rearranges the pipeline can swap this list without touching the TUI.
func DefaultStages() []model.Stage {
	return []model.Stage{
		{ID: "preflight", Title: "Preflight", Status: model.StagePending},
		{ID: "storage-prepare", Title: "Prepare storage", Status: model.StagePending},
		{ID: "osimage", Title: "OS image", Status: model.StagePending},
		{ID: "finalise", Title: "Finalise", Status: model.StagePending},
		{ID: "post", Title: "Post", Status: model.StagePending},
	}
}
