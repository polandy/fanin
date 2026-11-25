package model_test

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/polandy/fanin/internal/model"
)

// wireValues are literals that spell a state but are a foreign API's value, by directory below the repo root.
var wireValues = map[string][]string{
	filepath.Join("internal", "forge", "gitea"): {"running"}, // a Gitea commit status state
}

// TestNoStateLiteralsOutsideModel fails when a Go file outside internal/model spells a task or run state as a
// string literal instead of using the constant.
func TestNoStateLiteralsOutsideModel(t *testing.T) {
	states := map[string]bool{}
	for _, s := range model.AllTaskStates {
		states[string(s)] = true
	}
	for _, s := range model.AllRunStates {
		states[string(s)] = true
	}
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	self, err := filepath.Abs(".")
	if err != nil {
		t.Fatal(err)
	}
	scanned := 0
	for _, dir := range []string{"cmd", "internal", "pkg"} {
		base := filepath.Join(root, dir)
		if _, err := os.Stat(base); os.IsNotExist(err) {
			continue
		}
		err := filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if path == self || d.Name() == "testdata" || d.Name() == "vendor" {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") {
				return nil
			}
			scanned++
			rel, err := filepath.Rel(root, filepath.Dir(path))
			if err != nil {
				return err
			}
			return checkFile(t, path, states, wireValues[rel])
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	if scanned == 0 {
		t.Fatal("no Go files scanned outside internal/model; the walk is broken")
	}
}

func checkFile(t *testing.T, path string, states map[string]bool, allowed []string) error {
	t.Helper()
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
	if err != nil {
		return err
	}
	ast.Inspect(f, func(n ast.Node) bool {
		lit, ok := n.(*ast.BasicLit)
		if !ok || lit.Kind != token.STRING {
			return true
		}
		if v, err := strconv.Unquote(lit.Value); err == nil && states[v] && !slices.Contains(allowed, v) {
			t.Errorf("%s: state literal %s; use the internal/model constant", fset.Position(lit.Pos()), lit.Value)
		}
		return true
	})
	return nil
}
