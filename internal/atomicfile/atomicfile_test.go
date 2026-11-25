package atomicfile

import (
	"os"
	"path/filepath"
	"testing"
)

func TestWriteReplacesAndLeavesNoTemp(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "a.md")
	for _, want := range []string{"first", "second"} {
		if err := Write(path, []byte(want), 0o644); err != nil {
			t.Fatalf("Write: %v", err)
		}
		got, err := os.ReadFile(path)
		if err != nil || string(got) != want {
			t.Fatalf("ReadFile = %q, %v; want %q", got, err, want)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil || len(entries) != 1 {
		t.Fatalf("dir = %v, %v; want only a.md", entries, err)
	}
}

func TestWriteMissingDirFails(t *testing.T) {
	if err := Write(filepath.Join(t.TempDir(), "no", "a.md"), []byte("x"), 0o644); err == nil {
		t.Fatal("Write into a missing directory returned nil")
	}
}
