package faninmcp

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestReadRunEnv(t *testing.T) {
	dir := t.TempDir()
	write := func(name, body string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	good := write("good.json", `{"mcpServers":{"fanin":{"command":"fanin","args":["worker-mcp"],
		"env":{"FANIN_URL":"http://h","FANIN_RUN_ID":"r1","FANIN_RUN_TOKEN":"secret"}}}}`)
	got, err := ReadRunEnv(good)
	if err != nil {
		t.Fatal(err)
	}
	if diff := cmp.Diff(RunEnv{URL: "http://h", RunID: "r1", Token: "secret"}, got); diff != "" {
		t.Errorf("AW-14: run env (-want +got):\n%s", diff)
	}

	cases := []struct {
		name string
		path string
	}{
		{"AW-14_missing_file", filepath.Join(dir, "none.json")},
		{"AW-14_no_json", write("bad.json", "nope")},
		{"AW-14_no_fanin_server", write("other.json", `{"mcpServers":{"x":{"command":"x"}}}`)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := ReadRunEnv(tc.path); !errors.Is(err, ErrMCPConfig) {
				t.Errorf("ReadRunEnv = %v, want ErrMCPConfig", err)
			}
		})
	}
}
