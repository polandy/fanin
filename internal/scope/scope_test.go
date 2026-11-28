package scope_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/scope"
)

const (
	repo   = "acme/widget"
	number = 42
	head   = "abc123def456"

	readme      = "README.md"
	aGo         = "a.go"
	internalDir = "internal/"
)

var errForge = errors.New("forge down")

func TestValidateSC1(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		ok    bool
	}{
		{"none", nil, true},
		{"files, dirs and globs", []string{"internal/scope/", "docs/**", "cmd/fanin/*.go", readme}, true},
		{"twenty", strings.Split(strings.Repeat("a,", scope.MaxPaths-1)+"a", ","), true},
		{"twenty-one", strings.Split(strings.Repeat("a,", scope.MaxPaths)+"a", ","), false},
		{"empty", []string{""}, false},
		{"blank", []string{"  "}, false},
		{"absolute", []string{"/etc/passwd"}, false},
		{"drive", []string{"C:/repo/a.go"}, false},
		{"drive relative", []string{"c:a.go"}, false},
		{"unc", []string{`\\host\share\a.go`}, false},
		{"backslash", []string{`internal\a.go`}, false},
		{"dot dot", []string{"../other/a.go"}, false},
		{"dot dot inside", []string{"internal/../../a.go"}, false},
		{"dot dot last", []string{"internal/.."}, false},
		{"newline", []string{"a.go\nb.go"}, false},
		{"nul", []string{"a\x00.go"}, false},
		{"del", []string{"a\x7f.go"}, false},
		{"c1 control", []string{"a\u0085.go"}, false},
		{"bad glob", []string{"internal/[a.go"}, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := scope.Validate(tt.paths)
			if tt.ok && err != nil {
				t.Fatalf("Validate(%q) = %v, want nil", tt.paths, err)
			}
			if !tt.ok && !errors.Is(err, scope.ErrInvalid) {
				t.Fatalf("Validate(%q) = %v, want ErrInvalid", tt.paths, err)
			}
		})
	}
}

func TestOutsideSC2(t *testing.T) {
	tests := []struct {
		name  string
		paths []string
		files []string
		want  []string
	}{
		{"no scope", nil, []string{aGo}, nil},
		{"exact file", []string{readme}, []string{readme, "docs/README.md"}, []string{"docs/README.md"}},
		{"dir prefix", []string{"internal/scope/"}, []string{"internal/scope/a.go", "internal/scope/x/b.go",
			"internal/scopes/c.go"}, []string{"internal/scopes/c.go"}},
		{"star stays in its dir", []string{"cmd/*.go"}, []string{"cmd/a.go", "cmd/x/b.go"}, []string{"cmd/x/b.go"}},
		{"double star", []string{"docs/**"}, []string{"docs/a.md", "docs/x/y/b.md", "docsx/c.md"},
			[]string{"docsx/c.md"}},
		{"double star middle", []string{"internal/**/*_test.go"}, []string{"internal/a_test.go",
			"internal/x/y/b_test.go", "internal/x/c.go"}, []string{"internal/x/c.go"}},
		{"any of several", []string{aGo, "b/"}, []string{aGo, "b/c.go", "d.go"}, []string{"d.go"}},
		{"all inside", []string{"**"}, []string{aGo, "x/y.go"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if diff := cmp.Diff(tt.want, scope.Outside(tt.paths, tt.files)); diff != "" {
				t.Fatalf("Outside(%q, %q) -want +got:\n%s", tt.paths, tt.files, diff)
			}
		})
	}
}

// fakeForge answers Files from fixed values and records the statuses posted.
type fakeForge struct {
	files     []string
	filesErr  error
	postErr   error
	fileCalls int
	posted    []forge.Status
}

func (f *fakeForge) Files(_ context.Context, gotRepo string, gotNumber int) ([]string, error) {
	f.fileCalls++
	if gotRepo != repo || gotNumber != number {
		return nil, forge.ErrNotFound
	}
	return f.files, f.filesErr
}

func (f *fakeForge) PostStatus(_ context.Context, st forge.Status) error {
	if f.postErr != nil {
		return f.postErr
	}
	f.posted = append(f.posted, st)
	return nil
}

func TestPostSC2(t *testing.T) {
	tests := []struct {
		name      string
		paths     []string
		files     []string
		want      forge.StatusState
		desc      string
		fileCalls int
	}{
		{"no scope posts success unread", nil, nil, forge.StatusSuccess, scope.DescriptionNone, 0},
		{"inside", []string{internalDir}, []string{"internal/a.go"}, forge.StatusSuccess, scope.DescriptionInside, 1},
		{"outside", []string{internalDir}, []string{"internal/a.go", "go.mod", "cmd/b.go"}, forge.StatusFailure,
			scope.DescriptionOutside + "go.mod, cmd/b.go", 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeForge{files: tt.files}
			got, err := scope.Post(context.Background(), f, repo, number, head, tt.paths)
			if err != nil {
				t.Fatalf("Post = %v", err)
			}
			want := []forge.Status{{Repo: repo, SHA: head, Context: scope.Context, State: tt.want, Description: tt.desc}}
			if diff := cmp.Diff(want, f.posted); diff != "" {
				t.Fatalf("posted -want +got:\n%s", diff)
			}
			if got != tt.want || f.fileCalls != tt.fileCalls {
				t.Fatalf("Post = %s with %d file reads, want %s with %d", got, f.fileCalls, tt.want, tt.fileCalls)
			}
		})
	}
}

func TestPostLongDescriptionSC2(t *testing.T) {
	files := make([]string, 0, 30)
	for range 30 {
		files = append(files, "outside/some/long/path/file.go")
	}
	f := &fakeForge{files: files}
	if _, err := scope.Post(context.Background(), f, repo, number, head, []string{internalDir}); err != nil {
		t.Fatalf("Post = %v", err)
	}
	if n := len([]rune(f.posted[0].Description)); n > scope.MaxDescription {
		t.Fatalf("description has %d runes, want at most %d", n, scope.MaxDescription)
	}
}

func TestPostFailuresSC2(t *testing.T) {
	tests := []struct {
		name  string
		f     *fakeForge
		head  string
		paths []string
		is    error
	}{
		{"files unreadable", &fakeForge{filesErr: errForge}, head, []string{"a/"}, errForge},
		{"post fails", &fakeForge{files: []string{"a/b.go"}, postErr: errForge}, head, []string{"a/"}, errForge},
		{"post fails without scope", &fakeForge{postErr: errForge}, head, nil, errForge},
		{"no head", &fakeForge{}, "", nil, forge.ErrInvalidRequest},
		{"invalid scope", &fakeForge{files: []string{aGo}}, head, []string{"/abs"}, scope.ErrInvalid},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := scope.Post(context.Background(), tt.f, repo, number, tt.head, tt.paths)
			if !errors.Is(err, tt.is) {
				t.Fatalf("Post = %v, want %v", err, tt.is)
			}
			if len(tt.f.posted) != 0 {
				t.Fatalf("posted %v on a failure, want nothing", tt.f.posted)
			}
		})
	}
}
