package config

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

const triggersYAML = `webhooks:
  issues:
    kind: forge
    project: alpha
    secret_env: FANIN_HOOK_ISSUES
    label: fanin
  alerts:
    kind: alert
    project: alpha
    secret_env: FANIN_HOOK_ALERTS
    model: opus
  releases:
    kind: release
    project: alpha
    secret_env: FANIN_HOOK_RELEASES
cron:
  nightly:
    project: alpha
    every: 24h
    task: update the dependencies
release:
  project: alpha
`

func TestLoadTriggers(t *testing.T) {
	cfg, _ := mustLoad(t, with(map[string]string{fileTriggers: triggersYAML}))
	want := Triggers{
		Webhooks: map[string]Webhook{
			"issues":   {Kind: WebhookForge, Project: "alpha", SecretEnv: "FANIN_HOOK_ISSUES", Label: "fanin"},
			"alerts":   {Kind: WebhookAlert, Project: "alpha", SecretEnv: "FANIN_HOOK_ALERTS", Model: "opus"},
			"releases": {Kind: WebhookRelease, Project: "alpha", SecretEnv: "FANIN_HOOK_RELEASES"},
		},
		Cron: map[string]Cron{"nightly": {Project: "alpha", Every: Duration(24 * time.Hour),
			Task: "update the dependencies"}},
		Release: &Release{Project: "alpha", ComposeGlob: "modules/*/docker-compose.yml", Registry: "ghcr.io",
			Model: "sonnet"},
	}
	if d := cmp.Diff(want, cfg.Triggers); d != "" {
		t.Errorf("Triggers (-want +got):\n%s", d)
	}
	t.Run("IN-1_empty_file_has_no_triggers", func(t *testing.T) {
		cfg, _ := mustLoad(t, baseTree())
		if d := cmp.Diff(Triggers{}, cfg.Triggers); d != "" {
			t.Errorf("Triggers (-want +got):\n%s", d)
		}
	})
}

func TestLoadRefusesBadTriggers(t *testing.T) {
	hook := "webhooks:\n  h:\n    kind: forge\n    project: alpha\n    secret_env: FANIN_HOOK\n    label: fanin\n"
	cron := "cron:\n  n:\n    project: alpha\n    every: 1h\n    task: t\n"
	for _, tc := range []struct {
		name string
		body string
		want []string
	}{
		{"IN-1_webhook_unknown_project", strings.Replace(hook, "project: alpha", "project: gamma", 1),
			[]string{"/webhooks/h/project", `"gamma"`, ruleProjectKnown}},
		{"IN-1_webhook_model_the_kind_does_not_take", hook + "    model: gpt\n",
			[]string{"/webhooks/h/model", `"gpt"`, ruleModelKnown}},
		{"IN-1_secret_value_instead_of_name", strings.Replace(hook, "FANIN_HOOK", "s3cr3t-value", 1),
			[]string{"/webhooks/h/secret_env"}},
		{"IN-2_forge_webhook_without_label", strings.Replace(hook, "    label: fanin\n", "", 1),
			[]string{"/webhooks/h", "label"}},
		{"IN-3_label_on_an_alert_webhook", strings.Replace(hook, "kind: forge", "kind: alert", 1),
			[]string{"/webhooks/h"}},
		{"IN-5_release_webhook_without_release", strings.Replace(strings.Replace(hook, "kind: forge", "kind: release",
			1), "    label: fanin\n", "", 1), []string{"/webhooks/h/kind", ruleReleaseKnown}},
		{"IN-4_cron_unknown_project", strings.Replace(cron, "alpha", "gamma", 1),
			[]string{"/cron/n/project", ruleProjectKnown}},
		{"IN-4_cron_task_and_analyze", cron + "    analyze: q\n", []string{"/cron/n"}},
		{"IN-4_cron_bad_interval", strings.Replace(cron, "1h", "soon", 1), []string{"/cron/n/every", "soon"}},
		{"DR-1_release_unknown_project", "release:\n  project: gamma\n",
			[]string{"/release/project", ruleProjectKnown}},
		{"DR-1_release_default_model_the_kind_does_not_take", "release:\n  project: beta\n",
			[]string{"/release/model", `"sonnet"`, ruleModelKnown}},
		{"DR-1_release_unknown_key", "release:\n  project: alpha\n  colour: blue\n", []string{"/release", unknownKey}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Load(writeTree(t, with(map[string]string{fileTriggers: tc.body})))
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load: %v, want ErrInvalid", err)
			}
			for _, s := range append([]string{fileTriggers}, tc.want...) {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not name %q", err, s)
				}
			}
		})
	}
}
