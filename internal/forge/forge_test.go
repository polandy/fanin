package forge_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
)

const (
	repo  = "acme/widget"
	token = "ghp_0123456789abcdefghijABCDEFGHIJ012345"
)

func openPR() forge.PR {
	return forge.PR{Repo: repo, Number: 42, Title: "feat: add the widget", HeadBranch: "feat/widget", HeadSHA: "abc123"}
}

func TestNewSquash(t *testing.T) {
	t.Run("MG-6_subject_is_title_and_number_body_only_trailers", func(t *testing.T) {
		got, err := forge.NewSquash(openPR(), []forge.Trailer{{Key: "Task-Id", Value: "t-1"}, {Key: "Run-Id", Value: "r-2"}})
		if err != nil {
			t.Fatal(err)
		}
		want := forge.MergeRequest{Repo: repo, Number: 42, Subject: "feat: add the widget (#42)",
			Body: "Task-Id: t-1\nRun-Id: r-2", HeadSHA: "abc123"}
		if diff := cmp.Diff(want, got); diff != "" {
			t.Errorf("NewSquash (-want +got):\n%s", diff)
		}
	})
	t.Run("MG-6_no_trailers_is_an_empty_body", func(t *testing.T) {
		got, err := forge.NewSquash(openPR(), nil)
		if err != nil || got.Body != "" {
			t.Fatalf("body %q err %v", got.Body, err)
		}
	})
	bad := []struct {
		name     string
		pr       func(*forge.PR)
		trailers []forge.Trailer
	}{
		{"MG-6_view_without_head_branch", func(p *forge.PR) { p.HeadBranch = "" }, nil},
		{"no_head_sha", func(p *forge.PR) { p.HeadSHA = "" }, nil},
		{"empty_title", func(p *forge.PR) { p.Title = " " }, nil},
		{"no_number", func(p *forge.PR) { p.Number = 0 }, nil},
		{"multi_line_trailer", nil, []forge.Trailer{{Key: "Task-Id", Value: "a\nb"}}},
		{"trailer_key_with_colon", nil, []forge.Trailer{{Key: "A:B", Value: "x"}}},
	}
	for _, tc := range bad {
		t.Run(tc.name, func(t *testing.T) {
			pr := openPR()
			if tc.pr != nil {
				tc.pr(&pr)
			}
			if _, err := forge.NewSquash(pr, tc.trailers); !errors.Is(err, forge.ErrInvalidRequest) {
				t.Errorf("err = %v, want ErrInvalidRequest", err)
			}
		})
	}
}

func TestRedact(t *testing.T) {
	cases := []struct{ name, in string }{
		{"classic_token", "bad credentials for " + token},
		{"fine_grained_token", "token github_pat_11ABCDEFG0123456789_abcdefghijklmnopqrstuvwxyz rejected"},
		{"authorization_header", "Authorization: token s3cr3tvalue"},
		{"url_credentials", "fetch https://x-access-token:s3cr3tvalue@example.com/acme/widget.git"},
		{"query_token", "GET /x?access_token=s3cr3tvalue&page=2"},
	}
	for _, tc := range cases {
		t.Run("GT-10_"+tc.name, func(t *testing.T) {
			got := forge.Redact(tc.in)
			for _, secret := range []string{token, "s3cr3tvalue", "github_pat_11ABCDEFG"} {
				if strings.Contains(got, secret) {
					t.Errorf("Redact(%q) = %q still holds %q", tc.in, got, secret)
				}
			}
			if !strings.Contains(got, forge.Redacted) {
				t.Errorf("Redact(%q) = %q has no %s marker", tc.in, got, forge.Redacted)
			}
		})
	}
	t.Run("plain_text_is_unchanged", func(t *testing.T) {
		in := "HTTP 404: Not Found (https://api.github.com/repos/acme/widget)"
		if got := forge.Redact(in); got != in {
			t.Errorf("Redact = %q, want %q", got, in)
		}
	})
}

func TestRedactError(t *testing.T) {
	t.Run("GT-10_text_redacted_chain_kept", func(t *testing.T) {
		err := forge.RedactError(fmt.Errorf("%w: %s", forge.ErrNetwork, token))
		if strings.Contains(err.Error(), token) || !errors.Is(err, forge.ErrNetwork) {
			t.Errorf("err = %q, Is(ErrNetwork) = %v", err, errors.Is(err, forge.ErrNetwork))
		}
	})
	t.Run("nil_stays_nil", func(t *testing.T) {
		if err := forge.RedactError(nil); err != nil {
			t.Errorf("RedactError(nil) = %v", err)
		}
	})
}
