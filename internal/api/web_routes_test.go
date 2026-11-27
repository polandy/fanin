package api

import (
	"io/fs"
	"net/http"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

func TestWebUIIsServed(t *testing.T) {
	e := newEnv(t)
	for path, wantType := range map[string]string{
		"/ui/":          "text/html",
		"/ui/app.js":    "text/javascript",
		"/ui/style.css": "text/css",
	} {
		resp := e.do(t, http.MethodGet, path, "", nil)
		if resp.StatusCode != http.StatusOK || !strings.HasPrefix(resp.Header.Get("Content-Type"), wantType) {
			t.Errorf("GET %s = %d %q, want 200 %s", path, resp.StatusCode, resp.Header.Get("Content-Type"), wantType)
		}
		if len(resp.Body) == 0 {
			t.Errorf("GET %s: empty body", path)
		}
	}
	noFollow := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, e.srv.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	root, err := noFollow.Do(req)
	if err != nil {
		t.Fatalf("GET /: %v", err)
	}
	_ = root.Body.Close()
	if root.StatusCode != http.StatusFound || root.Header.Get("Location") != "/ui/" {
		t.Errorf("GET / = %d %q, want a redirect to /ui/", root.StatusCode, root.Header.Get("Location"))
	}
	if resp := e.do(t, http.MethodGet, "/ui/nope.js", "", nil); resp.StatusCode != http.StatusNotFound {
		t.Errorf("GET /ui/nope.js = %d, want 404", resp.StatusCode)
	}
}

// TestWebUIListensToEveryEventType keeps the board's EVENT_TYPES in step with model.AllEventTypes (SCENARIOS OA-8).
func TestWebUIListensToEveryEventType(t *testing.T) {
	src, err := fs.ReadFile(webFS, "web/app.js")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range model.AllEventTypes {
		if !strings.Contains(string(src), "'"+string(typ)+"'") {
			t.Errorf("app.js EVENT_TYPES lacks %s; the board would miss it", typ)
		}
	}
}

// TestWebUIHasNoGoFiles keeps internal/api/web a plain static tree: no build step, no Go package.
func TestWebUIHasNoGoFiles(t *testing.T) {
	err := fs.WalkDir(webFS, ".", func(path string, _ fs.DirEntry, err error) error {
		if err == nil && strings.HasSuffix(path, ".go") {
			t.Errorf("embedded web tree holds %s; it must contain no .go files", path)
		}
		return err
	})
	if err != nil {
		t.Fatalf("walk web tree: %v", err)
	}
}

// TestUIAnswerEqualsCLIAnswer posts the exact request app.js sends and compares the logged event with the CLI's
// (see SCENARIOS ST-2, OA-1).
func TestUIAnswerEqualsCLIAnswer(t *testing.T) {
	cli := newOwnerEnv(t)
	cli.ask(t, model.QuestionDialog, false)
	if _, err := cli.c.Answer(t.Context(), "t1", "2"); err != nil {
		t.Fatalf("CLI answer: %v", err)
	}
	ui := newOwnerEnv(t)
	ui.ask(t, model.QuestionDialog, false)
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, ui.url+"/api/v1/tasks/t1/answer",
		strings.NewReader(`{"answer":"2"}`))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+ownerToken)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("UI answer: %v", err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("UI answer status = %d, want 200", resp.StatusCode)
	}
	want, got := cli.events(t, model.EventQuestionAnswered), ui.events(t, model.EventQuestionAnswered)
	if len(want) != 1 {
		t.Fatalf("CLI logged %d answer events, want 1", len(want))
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("answer event, UI vs CLI (-cli +ui):\n%s", diff)
	}
	if len(cli.del.got) != 1 {
		t.Fatalf("CLI delivered %d answers, want 1", len(cli.del.got))
	}
	if diff := cmp.Diff(cli.del.got, ui.del.got); diff != "" {
		t.Errorf("delivery, UI vs CLI (-cli +ui):\n%s", diff)
	}
}
