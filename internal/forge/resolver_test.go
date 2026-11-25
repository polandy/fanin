package forge_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/forge"
)

const hub, alpha = "hub", "alpha"

// stubForge is a Forge that is never called; the resolver only hands it out.
type stubForge struct {
	forge.Forge
	name string
}

func newResolver(t *testing.T) (*forge.Resolver, *stubForge, *stubForge) {
	t.Helper()
	gh, gt := &stubForge{name: "github"}, &stubForge{name: "gitea"}
	r, err := forge.NewResolver(
		map[string]forge.Named{hub: {Kind: forge.KindGitHub, Client: gh}, "tea": {Kind: forge.KindGitea, Client: gt}},
		map[string]string{alpha: hub, "beta": "tea"})
	if err != nil {
		t.Fatal(err)
	}
	return r, gh, gt
}

func TestResolver(t *testing.T) {
	t.Run("GT-2_each_project_gets_its_forge_client", func(t *testing.T) {
		r, gh, gt := newResolver(t)
		for project, want := range map[string]*stubForge{alpha: gh, "beta": gt} {
			got, _, err := r.For(project)
			if err != nil {
				t.Fatal(err)
			}
			if got != want {
				t.Errorf("For(%s) = %v, want %s", project, got, want.name)
			}
		}
		got, err := r.Require("beta", forge.KindGitea)
		if err != nil || got != gt {
			t.Fatalf("Require(beta, gitea) = %v, %v; want the gitea client", got, err)
		}
	})

	t.Run("GT-2_gitea_operation_on_a_github_project_is_refused", func(t *testing.T) {
		r, _, _ := newResolver(t)
		got, err := r.Require(alpha, forge.KindGitea)
		if !errors.Is(err, forge.ErrWrongForge) || got != nil {
			t.Fatalf("Require(alpha, gitea) = %v, %v; want ErrWrongForge", got, err)
		}
		if !strings.Contains(err.Error(), "alpha is not a gitea project") {
			t.Errorf("error %q does not say the project is not a gitea project", err)
		}
	})

	t.Run("unknown_project_is_an_error", func(t *testing.T) {
		r, _, _ := newResolver(t)
		if _, _, err := r.For("gamma"); !errors.Is(err, forge.ErrUnknownProject) {
			t.Fatalf("For(gamma) err = %v, want ErrUnknownProject", err)
		}
		if _, err := r.Require("gamma", forge.KindGitHub); !errors.Is(err, forge.ErrUnknownProject) {
			t.Fatalf("Require(gamma) err = %v, want ErrUnknownProject", err)
		}
	})

	for _, tc := range []struct {
		name     string
		forges   map[string]forge.Named
		projects map[string]string
	}{
		{"missing_forge", map[string]forge.Named{}, map[string]string{alpha: hub}},
		{"unknown_kind", map[string]forge.Named{hub: {Kind: "gitlab", Client: &stubForge{}}}, nil},
		{"nil_client", map[string]forge.Named{hub: {Kind: forge.KindGitHub}}, nil},
	} {
		t.Run("bad_config_is_refused_"+tc.name, func(t *testing.T) {
			if _, err := forge.NewResolver(tc.forges, tc.projects); !errors.Is(err, forge.ErrInvalidRequest) {
				t.Fatalf("NewResolver err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestParseNumber(t *testing.T) {
	for _, tc := range []struct {
		in   string
		want int
	}{{"42", 42}, {"#7", 7}} {
		t.Run("reads_"+tc.in, func(t *testing.T) {
			got, err := forge.ParseNumber(tc.in)
			if err != nil || got != tc.want {
				t.Fatalf("ParseNumber(%q) = %d, %v; want %d", tc.in, got, err, tc.want)
			}
		})
	}
	for _, in := range []string{"", "#", "abc", "4x2", "-3", "0", "+5", " 42", "1e3", "12345678901"} {
		t.Run("GT-6_non_numeric_"+in+"_is_refused", func(t *testing.T) {
			if got, err := forge.ParseNumber(in); !errors.Is(err, forge.ErrInvalidRequest) || got != 0 {
				t.Fatalf("ParseNumber(%q) = %d, %v; want ErrInvalidRequest", in, got, err)
			}
		})
	}
}
