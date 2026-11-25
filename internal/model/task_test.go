package model_test

import (
	"bytes"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/model"
)

func TestTaskModel(t *testing.T) {
	const opus, sonnet, haiku = "opus", "sonnet", "haiku"
	tests := []struct {
		name                   string
		owner, project, picked string
		want                   string
	}{
		{"owner_wins", opus, sonnet, haiku, opus},
		{"project_over_picked", "", sonnet, haiku, sonnet},
		{"picked_last", "", "", haiku, haiku},
		{"none", "", "", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			task := model.Task{OwnerModel: tt.owner, ProjectModel: tt.project, PickedModel: tt.picked}
			if got := task.Model(); got != tt.want {
				t.Errorf("Model() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestTokenNeverPrinted(t *testing.T) {
	const secret = "s3cret-run-token"
	run := model.Run{ID: "r1", Token: secret}
	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("run", "token", run.Token, "run", run)
	for name, out := range map[string]string{
		"fmt %v":  fmt.Sprintf("%v", run.Token),
		"fmt %+v": fmt.Sprintf("%+v", run),
		"slog":    buf.String(),
	} {
		if strings.Contains(out, secret) {
			t.Errorf("%s leaks the token: %s", name, out)
		}
	}
	if !run.Token.Equal(secret) || run.Token.Equal("other") {
		t.Error("Equal does not compare the raw token")
	}
}

func TestOwnerQuestionValidate(t *testing.T) {
	opts := func(labels ...string) []model.Option {
		out := make([]model.Option, len(labels))
		for i, l := range labels {
			out[i] = model.Option{Label: l, Description: "causes " + l}
		}
		return out
	}
	tests := []struct {
		name string
		in   model.OwnerQuestion
		ok   bool
	}{
		{"valid", model.OwnerQuestion{Question: "Which DB?", Options: opts("sqlite", "pg"), Recommended: 0}, true},
		{"four_options", model.OwnerQuestion{Question: "Q?", Options: opts("a", "b", "c", "d"), Recommended: 3}, true},
		{"no_question", model.OwnerQuestion{Options: opts("a", "b")}, false},
		{"one_option", model.OwnerQuestion{Question: "Q?", Options: opts("a")}, false},
		{"five_options", model.OwnerQuestion{Question: "Q?", Options: opts("a", "b", "c", "d", "e")}, false},
		{"empty_label", model.OwnerQuestion{Question: "Q?", Options: opts("a", " ")}, false},
		{"duplicate_label", model.OwnerQuestion{Question: "Q?", Options: opts("a", "a")}, false},
		{"recommendation_out_of_range", model.OwnerQuestion{Question: "Q?", Options: opts("a", "b"), Recommended: 2}, false},
		{"negative_recommendation", model.OwnerQuestion{Question: "Q?", Options: opts("a", "b"), Recommended: -1}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := tt.in.Validate()
			if tt.ok && err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !tt.ok && !errors.Is(err, model.ErrInvalidQuestion) {
				t.Fatalf("err = %v, want ErrInvalidQuestion", err)
			}
		})
	}
}
