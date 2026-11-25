package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/api/client"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/intake"
)

const (
	hookEnv    = "TEST_FANIN_HOOK_SECRET"
	hookSecret = "hook-secret"
	hookBody   = `{"action":"labeled","issue":{"number":3,"title":"Fix the parser","body":"b",` +
		`"html_url":"https://example.com/acme/alpha/issues/3","labels":[{"name":"fanin"}]}}`
)

// writeTriggers adds a triggers.yaml with a forge webhook on alpha to the config in dir.
func writeTriggers(t *testing.T, dir string) {
	t.Helper()
	body := "webhooks:\n  issues:\n    kind: forge\n    project: " + project + "\n    secret_env: " + hookEnv +
		"\n    label: fanin\n"
	if err := os.WriteFile(filepath.Join(dir, "triggers.yaml"), []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestDaemonWebhook(t *testing.T) {
	t.Run("IN-2_signed_issue_delivery_queues_the_task", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := writeConfig(t, dir, "127.0.0.1:0")
		writeTriggers(t, cfgPath)
		d := startEnv(t, cfgPath, map[string]string{tokenEnv: token, hookEnv: hookSecret})
		m := hmac.New(sha256.New, []byte(hookSecret))
		m.Write([]byte(hookBody))
		req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, d.url+"/api/v1/webhooks/issues",
			strings.NewReader(hookBody))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set(intake.HeaderGitHubEvent, "issues")
		req.Header.Set(intake.HeaderHubSignature, intake.SignaturePrefix+hex.EncodeToString(m.Sum(nil)))
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		if err := resp.Body.Close(); err != nil {
			t.Fatal(err)
		}
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("status %d, want 201", resp.StatusCode)
		}
		tasks, err := client.New(d.url, token, nil).Tasks(context.Background(), project)
		if err != nil {
			t.Fatal(err)
		}
		if len(tasks.Tasks) != 1 || tasks.Tasks[0].ID != "alpha-issue-3" {
			t.Errorf("tasks = %+v; want alpha-issue-3", tasks.Tasks)
		}
		if strings.Contains(d.logs.String(), hookSecret) {
			t.Error("the log carries the webhook secret")
		}
	})

	t.Run("IN-1_unset_webhook_secret_refuses_to_start", func(t *testing.T) {
		dir := t.TempDir()
		cfgPath := writeConfig(t, dir, "127.0.0.1:0")
		writeTriggers(t, cfgPath)
		env := map[string]string{tokenEnv: token}
		deps := deps{getenv: func(k string) string { return env[k] }, logOut: &bytes.Buffer{},
			clock: clock.NewManual(t0), ready: func(string) { t.Error("ready called for a refused start") }}
		err := run(context.Background(), []string{flagConfig, cfgPath}, deps)
		if !errors.Is(err, intake.ErrNoSecret) || !strings.Contains(err.Error(), hookEnv) {
			t.Errorf("run: %v; want ErrNoSecret naming %s", err, hookEnv)
		}
	})
}
