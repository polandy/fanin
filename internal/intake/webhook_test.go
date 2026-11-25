package intake

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/safego"
)

const (
	hookIssues  = "issues"
	hookAlerts  = "alerts"
	hookRelease = "release"
	hookSecret  = "s3cret-for-tests"
	secretEnv   = "FANIN_HOOK_SECRET"
	issueBody   = `{"action":"labeled","label":{"name":"fanin"},"issue":{"number":7,"title":"Crash on start",` +
		`"body":"It panics.","html_url":"https://example.com/acme/alpha/issues/7","labels":[{"name":"bug"},` +
		`{"name":"fanin"}]}}`
	alertBody = `{"status":"firing","groupKey":"{}:{alertname=\"DiskFull\"}","commonLabels":{"alertname":"DiskFull"},` +
		`"alerts":[{"status":"firing","labels":{"alertname":"DiskFull","instance":"host-a"},` +
		`"annotations":{"summary":"disk 95% full"},"startsAt":"2026-11-25T10:00:00Z"}]}`
	releaseBody = `{"action":"published","release":{"tag_name":"v1.2.3"},"repository":{"full_name":"acme/Alpha"}}`
)

// fakeReleases records the submitted releases.
type fakeReleases struct {
	got []ReleaseEvent
	err error
}

func (f *fakeReleases) Submit(ev ReleaseEvent) error {
	if f.err != nil {
		return f.err
	}
	f.got = append(f.got, ev)
	return nil
}

func hookConfig() config.Config {
	cfg := releaseConfig()
	cfg.Triggers.Webhooks = map[string]config.Webhook{
		hookIssues:  {Kind: config.WebhookForge, Project: projGH, SecretEnv: secretEnv, Label: "fanin", Model: modelOpus},
		hookAlerts:  {Kind: config.WebhookAlert, Project: projGitea, SecretEnv: secretEnv},
		hookRelease: {Kind: config.WebhookRelease, Project: projGH, SecretEnv: secretEnv},
	}
	return cfg
}

func getenv(name string) string {
	if name == secretEnv {
		return hookSecret
	}
	return ""
}

func newWebhooks(t *testing.T) (*Webhooks, *fakeReleases, func() []model.Task) {
	t.Helper()
	_, st := newService(t)
	cfg := hookConfig()
	rel := &fakeReleases{}
	w, err := NewWebhooks(cfg, getenv, New(cfg, st), rel)
	if err != nil {
		t.Fatal(err)
	}
	return w, rel, func() []model.Task { return openTasks(t, st) }
}

func sign(body string) string {
	m := hmac.New(sha256.New, []byte(hookSecret))
	m.Write([]byte(body))
	return hex.EncodeToString(m.Sum(nil))
}

// signed is a GitHub-style delivery header of event for body.
func signed(event, body string) http.Header {
	h := http.Header{}
	h.Set(HeaderGitHubEvent, event)
	h.Set(HeaderHubSignature, SignaturePrefix+sign(body))
	return h
}

func receive(t *testing.T, w *Webhooks, name string, h http.Header, body string) Delivery {
	t.Helper()
	d, err := w.Receive(context.Background(), name, h, []byte(body))
	if err != nil {
		t.Fatalf("Receive %s: %v", name, err)
	}
	return d
}

func TestWebhookSignature(t *testing.T) {
	bad := http.Header{}
	bad.Set(HeaderGitHubEvent, hookIssues)
	bad.Set(HeaderHubSignature, SignaturePrefix+strings.Repeat("0", sha256.Size*2))
	tampered := signed(hookIssues, issueBody)
	plainHex := http.Header{}
	plainHex.Set(HeaderHubSignature, sign(issueBody))
	for _, tc := range []struct {
		name string
		hdr  http.Header
		body string
	}{
		{"IN-1_wrong_signature", bad, issueBody},
		{"IN-1_no_signature", http.Header{HeaderGitHubEvent: {hookIssues}}, issueBody},
		{"IN-1_signature_of_another_body", tampered, strings.Replace(issueBody, "Crash", "Wipe", 1)},
		{"IN-1_hub_signature_without_prefix", plainHex, issueBody},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w, _, tasks := newWebhooks(t)
			if _, err := w.Receive(context.Background(), hookIssues, tc.hdr, []byte(tc.body)); !errors.Is(err,
				ErrSignature) {
				t.Fatalf("Receive = %v; want ErrSignature", err)
			}
			if got := tasks(); len(got) != 0 {
				t.Errorf("queued %d tasks on a bad signature, want none", len(got))
			}
		})
	}

	t.Run("IN-1_gitea_signature_is_accepted", func(t *testing.T) {
		w, _, tasks := newWebhooks(t)
		h := http.Header{}
		h.Set(HeaderGiteaEvent, hookIssues)
		h.Set(HeaderGiteaSignature, sign(issueBody))
		if d := receive(t, w, hookIssues, h, issueBody); d.Outcome != OutcomeQueued || len(tasks()) != 1 {
			t.Errorf("Receive = %+v with %d tasks; want one queued", d, len(tasks()))
		}
	})

	t.Run("IN-1_unknown_webhook", func(t *testing.T) {
		w, _, _ := newWebhooks(t)
		if _, err := w.Receive(context.Background(), "nope", signed(hookIssues, issueBody),
			[]byte(issueBody)); !errors.Is(err, ErrUnknownWebhook) {
			t.Errorf("Receive = %v; want ErrUnknownWebhook", err)
		}
	})

	t.Run("IN-1_unset_secret_refuses_to_start", func(t *testing.T) {
		_, st := newService(t)
		cfg := hookConfig()
		_, err := NewWebhooks(cfg, func(string) string { return "" }, New(cfg, st), &fakeReleases{})
		if err == nil || !strings.Contains(err.Error(), secretEnv) {
			t.Errorf("NewWebhooks without the secret = %v; want an error naming %s", err, secretEnv)
		}
	})
}

func TestForgeWebhook(t *testing.T) {
	t.Run("IN-2_labelled_issue_queues_one_task", func(t *testing.T) {
		w, _, tasks := newWebhooks(t)
		d := receive(t, w, hookIssues, signed(hookIssues, issueBody), issueBody)
		if d.Outcome != OutcomeQueued || d.TaskID != "alpha-issue-7" {
			t.Fatalf("Receive = %+v; want alpha-issue-7 queued", d)
		}
		got := tasks()
		if len(got) != 1 {
			t.Fatalf("%d tasks, want 1", len(got))
		}
		tk := got[0]
		if tk.Project != projGH || tk.Shape != model.ShapePR || tk.OwnerModel != modelOpus || tk.Title != "Crash on start" {
			t.Errorf("task = %+v", tk)
		}
		for _, want := range []string{"It panics.", "https://example.com/acme/alpha/issues/7"} {
			if !strings.Contains(tk.Prompt, want) {
				t.Errorf("prompt %q does not name %q", tk.Prompt, want)
			}
		}
	})

	t.Run("IN-2_redelivery_adds_nothing", func(t *testing.T) {
		w, _, tasks := newWebhooks(t)
		receive(t, w, hookIssues, signed(hookIssues, issueBody), issueBody)
		if d := receive(t, w, hookIssues, signed(hookIssues, issueBody), issueBody); d.Outcome != OutcomeDuplicate {
			t.Errorf("redelivery = %+v; want duplicate", d)
		}
		if got := tasks(); len(got) != 1 {
			t.Errorf("%d tasks after a redelivery, want 1", len(got))
		}
	})

	for _, tc := range []struct {
		name, event, body string
	}{
		{"IN-2_issue_without_the_label", hookIssues, strings.Replace(issueBody, `{"name":"fanin"}]`, `{"name":"x"}]`, 1)},
		{"IN-2_closed_issue", hookIssues, strings.Replace(issueBody, `"labeled"`, `"closed"`, 1)},
		{"IN-2_ping", "ping", `{"zen":"hi"}`},
		{"IN-2_pull_request", hookIssues, strings.Replace(issueBody, `"number":7,`, `"number":7,"pull_request":{},`, 1)},
	} {
		t.Run(tc.name+"_is_ignored", func(t *testing.T) {
			w, _, tasks := newWebhooks(t)
			if d := receive(t, w, hookIssues, signed(tc.event, tc.body), tc.body); d.Outcome != OutcomeIgnored {
				t.Errorf("Receive = %+v; want ignored", d)
			}
			if got := tasks(); len(got) != 0 {
				t.Errorf("%d tasks, want none", len(got))
			}
		})
	}

	t.Run("IN-2_unreadable_payload_is_invalid", func(t *testing.T) {
		w, _, tasks := newWebhooks(t)
		if _, err := w.Receive(context.Background(), hookIssues, signed(hookIssues, "{"), []byte("{")); !errors.Is(err,
			ErrInvalid) {
			t.Errorf("Receive = %v; want ErrInvalid", err)
		}
		if got := tasks(); len(got) != 0 {
			t.Errorf("%d tasks, want none", len(got))
		}
	})
}

func TestAlertWebhook(t *testing.T) {
	t.Run("IN-3_firing_alert_queues_one_analysis", func(t *testing.T) {
		w, _, tasks := newWebhooks(t)
		d := receive(t, w, hookAlerts, signed("", alertBody), alertBody)
		if d.Outcome != OutcomeQueued || !strings.HasPrefix(string(d.TaskID), "beta-alert-") {
			t.Fatalf("Receive = %+v; want a beta-alert-… analysis queued", d)
		}
		got := tasks()
		if len(got) != 1 || got[0].Shape != model.ShapeAnalysis || got[0].Project != projGitea {
			t.Fatalf("tasks = %+v; want one analysis on %s", got, projGitea)
		}
		for _, want := range []string{"DiskFull", "disk 95% full", "host-a"} {
			if !strings.Contains(got[0].Prompt, want) {
				t.Errorf("prompt %q does not name %q", got[0].Prompt, want)
			}
		}
	})

	t.Run("IN-3_repeated_notification_adds_nothing", func(t *testing.T) {
		w, _, tasks := newWebhooks(t)
		receive(t, w, hookAlerts, signed("", alertBody), alertBody)
		if d := receive(t, w, hookAlerts, signed("", alertBody), alertBody); d.Outcome != OutcomeDuplicate {
			t.Errorf("repeat = %+v; want duplicate", d)
		}
		if got := tasks(); len(got) != 1 {
			t.Errorf("%d tasks, want 1", len(got))
		}
	})

	t.Run("IN-3_resolved_alert_is_ignored", func(t *testing.T) {
		w, _, tasks := newWebhooks(t)
		body := strings.ReplaceAll(alertBody, `"firing"`, `"resolved"`)
		if d := receive(t, w, hookAlerts, signed("", body), body); d.Outcome != OutcomeIgnored {
			t.Errorf("Receive = %+v; want ignored", d)
		}
		if got := tasks(); len(got) != 0 {
			t.Errorf("%d tasks, want none", len(got))
		}
	})
}

func TestReleaseWebhook(t *testing.T) {
	t.Run("IN-5_published_release_starts_the_deploy", func(t *testing.T) {
		w, rel, tasks := newWebhooks(t)
		if d := receive(t, w, hookRelease, signed(hookRelease, releaseBody), releaseBody); d.Outcome != OutcomeDeploying {
			t.Fatalf("Receive = %+v; want deploying", d)
		}
		want := []ReleaseEvent{{Project: projGH, Repo: relRepo, Version: relVersion}}
		if diff := cmp.Diff(want, rel.got); diff != "" {
			t.Errorf("submitted (-want +got):\n%s", diff)
		}
		if got := tasks(); len(got) != 0 {
			t.Errorf("the webhook queued %d tasks itself, want none", len(got))
		}
	})

	for _, tc := range []struct{ name, event, body string }{
		{"IN-5_draft_release", hookRelease, strings.Replace(releaseBody, "published", "created", 1)},
		{"IN-5_other_event", "push", releaseBody},
	} {
		t.Run(tc.name+"_is_ignored", func(t *testing.T) {
			w, rel, _ := newWebhooks(t)
			if d := receive(t, w, hookRelease, signed(tc.event, tc.body), tc.body); d.Outcome != OutcomeIgnored {
				t.Errorf("Receive = %+v; want ignored", d)
			}
			if len(rel.got) != 0 {
				t.Errorf("submitted %+v, want nothing", rel.got)
			}
		})
	}

	t.Run("IN-5_full_queue_is_busy", func(t *testing.T) {
		w, rel, _ := newWebhooks(t)
		rel.err = ErrBusy
		if _, err := w.Receive(context.Background(), hookRelease, signed(hookRelease, releaseBody),
			[]byte(releaseBody)); !errors.Is(err, ErrBusy) {
			t.Errorf("Receive = %v; want ErrBusy", err)
		}
	})
}

// signalEnqueuer reports every enqueued request.
type signalEnqueuer struct {
	Enqueuer
	done chan model.TaskID
}

func (s signalEnqueuer) Enqueue(ctx context.Context, r TaskRequest) (model.Task, error) {
	t, err := s.Enqueuer.Enqueue(ctx, r)
	s.done <- r.ID
	return t, err
}

func TestReleaseQueue(t *testing.T) {
	t.Run("IN-5_full_queue_refuses_the_release", func(t *testing.T) {
		p, wf := goodRelease()
		d, _, _ := newDeployer(t, p, wf)
		q := NewReleaseQueue(d, 1)
		if err := q.Submit(relEvent); err != nil {
			t.Fatal(err)
		}
		if err := q.Submit(relEvent); !errors.Is(err, ErrBusy) {
			t.Errorf("second Submit = %v; want ErrBusy", err)
		}
	})

	t.Run("IN-5_queued_release_is_deployed", func(t *testing.T) {
		p, wf := goodRelease()
		d, _, tasks := newDeployer(t, p, wf)
		done := make(chan model.TaskID, 1)
		d.Intake = signalEnqueuer{Enqueuer: d.Intake, done: done}
		q := NewReleaseQueue(d, 1)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		safego.Go(ctx, slog.New(slog.DiscardHandler), q.Run)
		if err := q.Submit(relEvent); err != nil {
			t.Fatal(err)
		}
		if id := <-done; id != "nixos-release-alpha-1.2.3" {
			t.Errorf("enqueued %s; want nixos-release-alpha-1.2.3", id)
		}
		if got := tasks(); len(got) != 1 {
			t.Errorf("%d tasks, want 1", len(got))
		}
	})
}
