package forge_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/forge"
)

const giteaToken = "0123456789abcdef0123456789abcdef01234567"

// dump is a config value as it would be logged or dumped: exported and unexported secret fields.
type dump struct {
	URL    string       `json:"url"`
	Token  forge.Secret `json:"token"`
	hidden forge.Secret
}

func TestSecret(t *testing.T) {
	d := dump{URL: "https://git.example.com", Token: forge.NewSecret(giteaToken), hidden: forge.NewSecret(giteaToken)}

	t.Run("GT-10_token_is_in_no_print_log_or_dump", func(t *testing.T) {
		js, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		var logs bytes.Buffer
		log := slog.New(slog.NewJSONHandler(&logs, nil))
		log.Info("config", "dump", d, "token", d.Token)
		log.Info("text", "dump", fmt.Sprintf("%+v", d))
		outs := map[string]string{
			"%v": fmt.Sprintf("%v", d), "%+v": fmt.Sprintf("%+v", d), "%#v": fmt.Sprintf("%#v", d),
			"%s": fmt.Sprintf("%s", d.Token), "%q": fmt.Sprintf("%q", d.Token), "json": string(js), "slog": logs.String(),
		}
		for form, out := range outs {
			if strings.Contains(out, giteaToken) {
				t.Errorf("%s leaks the token: %s", form, out)
			}
		}
		if !strings.Contains(string(js), forge.Redacted) || !strings.Contains(logs.String(), forge.Redacted) {
			t.Errorf("json %s or logs %s do not show %s", js, logs.String(), forge.Redacted)
		}
		if got := d.Token.Reveal(); got != giteaToken {
			t.Errorf("Reveal = %q, want the token", got)
		}
	})

	t.Run("GT-10_scrub_removes_the_literal_token", func(t *testing.T) {
		got := d.Token.Scrub("bad credentials " + giteaToken + " and " + token)
		if strings.Contains(got, giteaToken) || strings.Contains(got, token) {
			t.Errorf("Scrub = %q, still has a token", got)
		}
	})

	t.Run("from_env_reads_the_named_variable", func(t *testing.T) {
		lookup := func(name string) (string, bool) { return giteaToken, name == "GITEA_TOKEN" }
		s, err := forge.SecretFromEnv("GITEA_TOKEN", lookup)
		if err != nil || s.Reveal() != giteaToken {
			t.Fatalf("SecretFromEnv = %v, %v; want the token", s, err)
		}
		for _, v := range []string{"", "  "} {
			_, err := forge.SecretFromEnv("GITEA_TOKEN", func(string) (string, bool) { return v, true })
			if !errors.Is(err, forge.ErrNoSecret) || !strings.Contains(err.Error(), "GITEA_TOKEN") {
				t.Errorf("SecretFromEnv(%q) err = %v, want ErrNoSecret naming GITEA_TOKEN", v, err)
			}
		}
		if _, err := forge.SecretFromEnv("GITEA_TOKEN", func(string) (string, bool) { return "", false }); !errors.Is(err, forge.ErrNoSecret) {
			t.Errorf("unset variable err = %v, want ErrNoSecret", err)
		}
	})
}
