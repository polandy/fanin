package config

import (
	"errors"
	"testing"
)

// The models of the base tree.
const (
	modelHaiku  = "haiku"
	modelSonnet = "sonnet"
	modelOpus   = "opus"
)

// TestWorkerModel covers SCENARIOS PK-5: the owner model wins over the project model, which wins over the picked
// one; opencode kinds take no model.
func TestWorkerModel(t *testing.T) {
	cfg, _ := mustLoad(t, baseTree())
	alpha, _ := cfg.Project("alpha")
	noModel := alpha
	noModel.Model = ""
	withModel := alpha
	withModel.Model = modelSonnet
	beta, _ := cfg.Project("beta")
	for _, tc := range []struct {
		name          string
		p             Project
		owner, picked string
		want          string
	}{
		{"PK-5_owner_wins", withModel, modelOpus, modelHaiku, modelOpus},
		{"PK-5_project_over_picked", withModel, "", modelHaiku, modelSonnet},
		{"PK-5_picked_without_project_model", noModel, "", modelHaiku, modelHaiku},
		{"PK-5_kind_default_last", noModel, "", "", modelSonnet},
		{"PK-5_opencode_takes_no_model", beta, modelOpus, modelHaiku, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := cfg.WorkerModel(tc.p, tc.owner, tc.picked)
			if err != nil || got != tc.want {
				t.Errorf("WorkerModel = %q, %v; want %q", got, err, tc.want)
			}
		})
	}
	t.Run("PK-5_unknown_owner_model", func(t *testing.T) {
		if _, err := cfg.WorkerModel(noModel, "gpt", ""); !errors.Is(err, ErrModel) {
			t.Errorf("WorkerModel: %v, want ErrModel", err)
		}
	})
	t.Run("PK-5_picker_model_is_data", func(t *testing.T) {
		if cfg.Picker != (Picker{Kind: "headless", Model: modelHaiku}) {
			t.Errorf("Picker = %+v", cfg.Picker)
		}
	})
}
