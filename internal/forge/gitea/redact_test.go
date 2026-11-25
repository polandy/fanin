package gitea_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/gitea"
)

// echoAuth answers code with the request's Authorization header in the body, as a careless proxy might.
func echoAuth(code int) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		raw(code, `{"message":"bad credentials: `+r.Header.Get("Authorization")+`"}`)(w, r)
	}
}

func TestTokenNeverLeaks(t *testing.T) {
	for _, code := range []int{http.StatusUnauthorized, http.StatusInternalServerError, http.StatusNotFound, http.StatusBadGateway} {
		t.Run(fmt.Sprintf("GT-10_token_echoed_with_HTTP_%d_is_in_no_error_and_no_log_line", code), func(t *testing.T) {
			f := newFake(t)
			f.on(pullPath, echoAuth(code))
			var logs bytes.Buffer
			c, err := gitea.New(f.srv.Client(), f.srv.URL, forge.NewSecret(token),
				slog.New(slog.NewJSONHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.PR(context.Background(), repo, number)
			if err == nil {
				t.Fatal("err = nil, want the HTTP error")
			}
			if !strings.Contains(logs.String(), "gitea call failed") {
				t.Fatalf("no log line for the failed call: %q", logs.String())
			}
			for name, out := range map[string]string{"error": err.Error(), "logs": logs.String(),
				"client": fmt.Sprintf("%+v %#v", c, c)} {
				if strings.Contains(out, token) {
					t.Errorf("%s leaks the token: %s", name, out)
				}
			}
		})
	}

	t.Run("GT-10_credentials_in_the_url_are_refused_and_redacted", func(t *testing.T) {
		_, err := gitea.New(http.DefaultClient, "https://user:"+token+"@git.example.com", forge.NewSecret(token), nil)
		if !errors.Is(err, forge.ErrInvalidRequest) || strings.Contains(err.Error(), token) {
			t.Fatalf("err = %v, want ErrInvalidRequest without the token", err)
		}
	})

	t.Run("empty_token_is_refused", func(t *testing.T) {
		if _, err := gitea.New(http.DefaultClient, "https://git.example.com", forge.Secret{}, nil); !errors.Is(err, forge.ErrNoSecret) {
			t.Fatalf("err = %v, want ErrNoSecret", err)
		}
	})
}
