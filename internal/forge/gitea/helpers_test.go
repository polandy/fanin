package gitea_test

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/forge/gitea"
)

const (
	repo       = "acme/widget"
	number     = 42
	headSHA    = "abc123def456"
	headBranch = "feat/widget"
	baseBranch = "main"
	title      = "feat: add the widget"
	token      = "0123456789abcdef0123456789abcdef01234567"
	doneAt     = "2026-11-25T09:05:00Z"
	review     = "fanin/review"

	pullPath   = "GET /api/v1/repos/acme/widget/pulls/42"
	statusPath = "GET /api/v1/repos/acme/widget/commits/abc123def456/status"
	listPath   = "GET /api/v1/repos/acme/widget/pulls"
	filesPath  = "GET /api/v1/repos/acme/widget/pulls/42/files"
	mergePath  = "POST /api/v1/repos/acme/widget/pulls/42/merge"
	openPath   = "POST /api/v1/repos/acme/widget/pulls"
	keyState   = "state"
	keyMerged  = "merged"
	keyStatus  = "status"

	keyRef          = "ref"
	keySHA          = "sha"
	keyContext      = "context"
	keyFilename     = "filename"
	valOpen         = "open"
	valClosed       = "closed"
	valSuccess      = "success"
	caseServerError = "server_error"
	caseBrokenJSON  = "broken_json"
)

// fake is a Gitea API on httptest: routes by "METHOD path", records every call and request body.
type fake struct {
	t      *testing.T
	srv    *httptest.Server
	mu     sync.Mutex
	routes map[string]http.HandlerFunc
	calls  []string
	bodies map[string]map[string]any
	auth   []string
}

func newFake(t *testing.T) *fake {
	t.Helper()
	f := &fake{t: t, routes: map[string]http.HandlerFunc{}, bodies: map[string]map[string]any{}}
	f.srv = httptest.NewServer(http.HandlerFunc(f.serve))
	t.Cleanup(f.srv.Close)
	return f
}

func (f *fake) serve(w http.ResponseWriter, r *http.Request) {
	key := r.Method + " " + r.URL.EscapedPath()
	f.mu.Lock()
	f.calls = append(f.calls, key)
	f.auth = append(f.auth, r.Header.Get("Authorization"))
	if data, _ := io.ReadAll(r.Body); len(data) > 0 {
		body := map[string]any{}
		if err := json.Unmarshal(data, &body); err != nil {
			f.t.Errorf("%s: body %q is not a JSON object: %v", key, data, err)
		}
		f.bodies[key] = body
	}
	h, ok := f.routes[key]
	f.mu.Unlock()
	if !ok {
		f.t.Errorf("unexpected call %s", key)
		http.Error(w, "no route", http.StatusTeapot)
		return
	}
	h(w, r)
}

func (f *fake) on(key string, h http.HandlerFunc) { f.routes[key] = h }

func (f *fake) callList() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.calls...)
}

func (f *fake) body(key string) map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.bodies[key]
}

func (f *fake) client(t *testing.T) *gitea.Client {
	t.Helper()
	c, err := gitea.New(f.srv.Client(), f.srv.URL, forge.NewSecret(token), nil)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// reply answers code with v as JSON.
func reply(code int, v any) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(code)
		if err := json.NewEncoder(w).Encode(v); err != nil {
			panic(err)
		}
	}
}

// raw answers code with body as is.
func raw(code int, body string) http.HandlerFunc {
	return func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(code)
		if _, err := io.WriteString(w, body); err != nil {
			panic(err)
		}
	}
}

// prDoc is the Gitea answer for PR 42; tests change single fields to break it.
func prDoc() map[string]any {
	return map[string]any{
		"number": number, "title": title, "html_url": "https://git.example.com/acme/widget/pulls/42",
		keyState: valOpen, keyMerged: false, "mergeable": true,
		"head": map[string]any{keyRef: headBranch, keySHA: headSHA}, "base": map[string]any{keyRef: baseBranch, keySHA: "000"},
	}
}

func prWith(key string, v any) map[string]any {
	d := prDoc()
	d[key] = v
	return d
}

func status(context, state string) map[string]any {
	return map[string]any{keyContext: context, keyStatus: state, "created_at": "2026-11-25T09:00:00Z", "updated_at": doneAt}
}

func combined(statuses ...map[string]any) map[string]any {
	return map[string]any{keyState: valSuccess, "total_count": len(statuses), "statuses": statuses}
}

var _ forge.Forge = (*gitea.Client)(nil)
