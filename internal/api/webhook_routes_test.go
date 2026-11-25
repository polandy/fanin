package api

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/store"
)

const (
	hookName   = "issues"
	hookSecret = "s3cret-for-tests"
	hookEnv    = "FANIN_HOOK_SECRET"
	hookPath   = "/api/v1/webhooks/" + hookName
	hookBody   = `{"action":"labeled","issue":{"number":7,"title":"Crash","body":"b",` +
		`"html_url":"https://example.com/acme/alpha/issues/7","labels":[{"name":"fanin"}]}}`
)

// busyReleases refuses every release.
type busyReleases struct{}

func (busyReleases) Submit(intake.ReleaseEvent) error { return intake.ErrBusy }

func startWebhookAPI(t *testing.T, in *fakeIntake) *httptest.Server {
	t.Helper()
	st, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "fanin.db"), clock.NewManual(t0))
	if err != nil {
		t.Fatalf("store.Open: %v", err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	cfg := config.Config{Triggers: config.Triggers{Webhooks: map[string]config.Webhook{
		hookName:  {Kind: config.WebhookForge, Project: projAlpha, SecretEnv: hookEnv, Label: "fanin"},
		"release": {Kind: config.WebhookRelease, Project: projAlpha, SecretEnv: hookEnv},
	}}}
	hooks, err := intake.NewWebhooks(cfg, func(string) string { return hookSecret }, in, busyReleases{})
	if err != nil {
		t.Fatal(err)
	}
	reg := NewRegistry()
	RegisterBuiltins(reg)
	h, err := New(Config{Source: st, OwnerToken: ownerToken, Clock: clock.NewManual(t0), Registry: reg,
		Projects: configured, Intake: in, Webhooks: hooks})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return srv
}

func hubSignature(body string) string {
	m := hmac.New(sha256.New, []byte(hookSecret))
	m.Write([]byte(body))
	return intake.SignaturePrefix + hex.EncodeToString(m.Sum(nil))
}

// post sends body to path as a GitHub delivery of event, signed with sig and with token as the bearer when set.
func post(t *testing.T, srv *httptest.Server, path, event, sig, token, body string) (int, wire.WebhookResult) {
	t.Helper()
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, srv.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set(intake.HeaderGitHubEvent, event)
	if sig != "" {
		req.Header.Set(intake.HeaderHubSignature, sig)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	var res wire.WebhookResult
	if resp.StatusCode < http.StatusBadRequest {
		if err := json.NewDecoder(resp.Body).Decode(&res); err != nil {
			t.Fatalf("decode: %v", err)
		}
	}
	return resp.StatusCode, res
}

func TestWebhookRoute(t *testing.T) {
	t.Run("IN-1_wrong_signature_is_401_and_queues_nothing", func(t *testing.T) {
		in := &fakeIntake{}
		srv := startWebhookAPI(t, in)
		for _, sig := range []string{"", hubSignature("other"), "sha256=zz"} {
			if code, _ := post(t, srv, hookPath, "issues", sig, "", hookBody); code != http.StatusUnauthorized {
				t.Errorf("signature %q: status %d, want 401", sig, code)
			}
		}
		if len(in.got) != 0 {
			t.Errorf("queued %+v on bad signatures", in.got)
		}
	})

	t.Run("IN-1_signed_delivery_needs_no_owner_token", func(t *testing.T) {
		in := &fakeIntake{}
		srv := startWebhookAPI(t, in)
		code, res := post(t, srv, hookPath, "issues", hubSignature(hookBody), "", hookBody)
		if code != http.StatusCreated || res.Outcome != string(intake.OutcomeQueued) || res.Task != "alpha-issue-7" {
			t.Fatalf("status %d, %+v; want 201 queued alpha-issue-7", code, res)
		}
		if len(in.got) != 1 || in.got[0].Project != projAlpha {
			t.Errorf("intake saw %+v", in.got)
		}
	})

	t.Run("IN-1_unknown_webhook_is_404", func(t *testing.T) {
		srv := startWebhookAPI(t, &fakeIntake{})
		if code, _ := post(t, srv, "/api/v1/webhooks/nope", "issues", hubSignature(hookBody), "",
			hookBody); code != http.StatusNotFound {
			t.Errorf("status %d, want 404", code)
		}
	})

	t.Run("IN-1_webhook_on_a_command_endpoint_is_refused", func(t *testing.T) {
		in := &fakeIntake{}
		srv := startWebhookAPI(t, in)
		body := `{"project":"alpha","task":"x"}`
		if code, _ := post(t, srv, "/api/v1/tasks", "issues", hubSignature(body), "", body); code != http.StatusUnauthorized {
			t.Errorf("signed delivery without the owner token: status %d, want 401", code)
		}
		if code, _ := post(t, srv, "/api/v1/tasks", "issues", hubSignature(body), ownerToken,
			body); code != http.StatusForbidden {
			t.Errorf("signed delivery with the owner token: status %d, want 403", code)
		}
		if len(in.got) != 0 {
			t.Errorf("queued %+v through a command endpoint", in.got)
		}
	})

	t.Run("IN-2_ignored_and_duplicate_are_200", func(t *testing.T) {
		in := &fakeIntake{}
		srv := startWebhookAPI(t, in)
		if code, res := post(t, srv, hookPath, "ping", hubSignature("{}"), "", "{}"); code != http.StatusOK ||
			res.Outcome != string(intake.OutcomeIgnored) {
			t.Errorf("ping: status %d, %+v; want 200 ignored", code, res)
		}
		in.err = intake.ErrExists
		if code, res := post(t, srv, hookPath, "issues", hubSignature(hookBody), "", hookBody); code != http.StatusOK ||
			res.Outcome != string(intake.OutcomeDuplicate) {
			t.Errorf("redelivery: status %d, %+v; want 200 duplicate", code, res)
		}
	})

	t.Run("IN-2_unreadable_payload_is_400", func(t *testing.T) {
		srv := startWebhookAPI(t, &fakeIntake{})
		if code, _ := post(t, srv, hookPath, "issues", hubSignature("{"), "", "{"); code != http.StatusBadRequest {
			t.Errorf("status %d, want 400", code)
		}
	})

	t.Run("IN-5_full_release_queue_is_503", func(t *testing.T) {
		srv := startWebhookAPI(t, &fakeIntake{})
		body := `{"action":"published","release":{"tag_name":"v1"},"repository":{"full_name":"acme/alpha"}}`
		if code, _ := post(t, srv, "/api/v1/webhooks/release", "release", hubSignature(body), "",
			body); code != http.StatusServiceUnavailable {
			t.Errorf("status %d, want 503", code)
		}
	})

	t.Run("IN-1_oversized_body_is_413", func(t *testing.T) {
		srv := startWebhookAPI(t, &fakeIntake{})
		big := string(bytes.Repeat([]byte("x"), MaxBodyBytes+1))
		if code, _ := post(t, srv, hookPath, "issues", hubSignature(big), "", big); code != http.StatusRequestEntityTooLarge {
			t.Errorf("status %d, want 413", code)
		}
	})
}
