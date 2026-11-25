// Package api serves the owner-facing HTTP API and the server-sent event stream (concept: owner UI; spec in
// docs/dev/spec/api.md).
package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/owner"
)

// MaxBodyBytes caps the body of a command request.
const MaxBodyBytes = 1 << 20

// ErrNoOwnerToken is returned by New when no owner token is configured.
var ErrNoOwnerToken = errors.New("api: no owner token")

const (
	bearerPrefix = "Bearer "
)

// webhookHeaderPrefixes mark forge webhook deliveries; a webhook authenticates with its own signature on its own route.
var webhookHeaderPrefixes = []string{"x-github-", "x-gitea-", "x-gogs-", "x-gitlab-", "x-hub-signature"}

// Source is what the API reads: the store, or a fake in tests.
type Source interface {
	OpenTasks(ctx context.Context) ([]model.Task, error)
	Runs(ctx context.Context, task model.TaskID) ([]model.Run, error)
	OpenQuestions(ctx context.Context) ([]model.OwnerQuestion, error)
	DoneTasks(ctx context.Context) ([]model.Task, error)
	AllHolds(ctx context.Context) ([]model.Hold, error)
	Locks(ctx context.Context) ([]model.Lock, error)
	LatestEvents(ctx context.Context) ([]model.Event, error)
	EventsAfter(ctx context.Context, seq int64, limit int) ([]model.Event, error)
	LastEventSeq(ctx context.Context) (int64, error)
	// Changed returns a channel closed after the next committed change.
	Changed() <-chan struct{}
}

// Fleet lists the worker hosts and their agents. A host it cannot read comes back with Warning set; an error means
// the whole view is unreadable.
type Fleet interface {
	Hosts(ctx context.Context) ([]wire.Host, error)
}

// Config is what New needs.
type Config struct {
	Source Source
	// OwnerToken guards every command endpoint; it is never logged.
	OwnerToken string
	Clock      clock.Clock
	Logger     *slog.Logger
	Registry   *Registry
	// Projects are the configured project names in config order.
	Projects []string
	// Owner runs the owner commands; without it they answer 500 and change nothing.
	Owner Owner
	// Fleet feeds the hosts view; without it the snapshot lists no hosts.
	Fleet Fleet
}

// Owner is the owner channel the command endpoints call (internal/owner).
type Owner interface {
	Answer(ctx context.Context, task model.TaskID, raw string) (owner.Delivery, error)
	Go(ctx context.Context, task model.TaskID) error
	LiftHold(ctx context.Context, id string) error
	Pause(ctx context.Context, project string) (bool, error)
	Resume(ctx context.Context, project string) error
}

// Server is the state the route handlers share.
type Server struct {
	cfg       Config
	tokenHash [sha256.Size]byte
}

// Source returns the read side of the store.
func (s *Server) Source() Source { return s.cfg.Source }

// Clock returns the injected clock.
func (s *Server) Clock() clock.Clock { return s.cfg.Clock }

// Projects returns the configured project names in config order.
func (s *Server) Projects() []string { return s.cfg.Projects }

// Logger returns the server's logger.
func (s *Server) Logger() *slog.Logger { return s.cfg.Logger }

// New builds the handler for cfg.Registry. An empty owner token is an error: command endpoints fail closed.
func New(cfg Config) (http.Handler, error) {
	if cfg.OwnerToken == "" {
		return nil, fmt.Errorf("%w: set the owner token in the config; command endpoints stay shut without it",
			ErrNoOwnerToken)
	}
	if cfg.Logger == nil {
		cfg.Logger = slog.Default()
	}
	s := &Server{cfg: cfg, tokenHash: sha256.Sum256([]byte(cfg.OwnerToken))}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		WriteError(w, http.StatusNotFound, wire.CodeNotFound,
			fmt.Sprintf("no route %s %s; see docs/dev/spec/api.md for the endpoints", r.Method, r.URL.Path))
	})
	for _, rt := range cfg.Registry.Routes() {
		h := rt.Handler(s)
		if isCommand(rt.Pattern) {
			h = s.ownerOnly(h)
		}
		mux.Handle(rt.Pattern, h)
	}
	return mux, nil
}

// isCommand reports whether the pattern's method changes state; a pattern without a method serves every method.
func isCommand(pattern string) bool {
	method, _, found := strings.Cut(pattern, " ")
	if !found {
		return true
	}
	return method != http.MethodGet && method != http.MethodHead
}

func (s *Server) ownerOnly(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got, ok := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
		sum := sha256.Sum256([]byte(got))
		if !ok || subtle.ConstantTimeCompare(sum[:], s.tokenHash[:]) != 1 {
			WriteError(w, http.StatusUnauthorized, wire.CodeUnauthorized,
				"command endpoints need the owner token; send it as 'Authorization: Bearer <token>'")
			return
		}
		if hasWebhookHeader(r.Header) {
			WriteError(w, http.StatusForbidden, wire.CodeForbidden,
				"a webhook delivery cannot call a command endpoint; send webhooks to their own intake route")
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, MaxBodyBytes)
		next.ServeHTTP(w, r)
	})
}

func hasWebhookHeader(h http.Header) bool {
	for name := range h {
		lower := strings.ToLower(name)
		for _, p := range webhookHeaderPrefixes {
			if strings.HasPrefix(lower, p) {
				return true
			}
		}
	}
	return false
}

// WriteJSON writes v as the JSON body with status.
func WriteJSON(w http.ResponseWriter, status int, v any) {
	body, err := json.Marshal(v)
	if err != nil {
		WriteError(w, http.StatusInternalServerError, wire.CodeInternal, "encode the answer: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(append(body, '\n'))
}

// WriteError writes the JSON error format. The message names what was wrong and what to do.
func WriteError(w http.ResponseWriter, status int, code, message string) {
	WriteJSON(w, status, wire.ErrorBody{Error: wire.Error{Code: code, Message: message}})
}
