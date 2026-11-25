package config

import (
	"bytes"
	"errors"
	"strings"
	"testing"
)

const fakeSecret = "fake-secret-value-42"

// fakeSecrets knows every secret name and answers the same value for each.
type fakeSecrets struct{ missing string }

func (f fakeSecrets) Lookup(name string) (string, bool) {
	if name == f.missing {
		return "", false
	}
	return fakeSecret, true
}

// TestEffectiveNeverDumpsASecret covers SCENARIOS GT-10: a config dump names secrets, never their values.
func TestEffectiveNeverDumpsASecret(t *testing.T) {
	cfg, _ := mustLoad(t, baseTree())
	for _, name := range cfg.ProjectNames() {
		t.Run("GT-10_"+name, func(t *testing.T) {
			eff, err := cfg.Effective(name, fakeSecrets{missing: "GITEA_TOKEN"})
			if err != nil {
				t.Fatalf("Effective: %v", err)
			}
			var buf bytes.Buffer
			if err := eff.WriteYAML(&buf); err != nil {
				t.Fatalf("WriteYAML: %v", err)
			}
			out := buf.String()
			if strings.Contains(out, fakeSecret) {
				t.Fatalf("dump holds the secret value:\n%s", out)
			}
			for _, s := range []string{"FANIN_OWNER_TOKEN", "max_parallel:", "fix_rounds_per_run:"} {
				if !strings.Contains(out, s) {
					t.Errorf("dump lacks %q:\n%s", s, out)
				}
			}
		})
	}
	eff, err := cfg.Effective("beta", fakeSecrets{missing: "GITEA_TOKEN"})
	if err != nil {
		t.Fatal(err)
	}
	if eff.Forge.Token != (SecretRef{Env: "GITEA_TOKEN", Set: false}) || !eff.Daemon.OwnerToken.Set {
		t.Errorf("secret refs = %+v / %+v, want GITEA_TOKEN unset and the owner token set", eff.Forge.Token,
			eff.Daemon.OwnerToken)
	}
	if eff.Host.Address != "host-a.example.com" || eff.Kind.Runtime != RuntimeHerdr {
		t.Errorf("effective beta = %+v", eff)
	}
}

func TestEffectiveUnknownProject(t *testing.T) {
	cfg, _ := mustLoad(t, baseTree())
	if _, err := cfg.Effective("gamma", fakeSecrets{}); !errors.Is(err, ErrUnknownProject) {
		t.Fatalf("Effective: %v, want ErrUnknownProject", err)
	}
}
