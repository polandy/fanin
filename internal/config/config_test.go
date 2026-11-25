package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const valid = `listen: 127.0.0.1:7420
store: data/fanin.db
log:
  format: text
owner_token_env: FANIN_OWNER_TOKEN
projects:
  - name: beta
  - name: alpha
`

func write(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "fanin.yaml")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLoad(t *testing.T) {
	path := write(t, valid)
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := Config{
		Listen:        "127.0.0.1:7420",
		Store:         filepath.Join(filepath.Dir(path), "data", "fanin.db"),
		Log:           Log{Format: LogText, Level: LevelInfo},
		OwnerTokenEnv: "FANIN_OWNER_TOKEN",
		Projects:      []Project{{Name: "beta"}, {Name: "alpha"}},
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("Load (-want +got):\n%s", diff)
	}
	// ST-1: status lists the projects in config order.
	if names := got.ProjectNames(); !cmp.Equal(names, []string{"beta", "alpha"}) {
		t.Errorf("ProjectNames = %v, want config order", names)
	}
}

func TestLoadDefaultsToJSONLogs(t *testing.T) {
	got, err := Load(write(t, strings.Replace(valid, "log:\n  format: text\n", "", 1)))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.Log != (Log{Format: LogJSON, Level: LevelInfo}) {
		t.Errorf("Log = %+v, want json at info", got.Log)
	}
}

// TestLoadRefusesABadConfig covers SCENARIOS ST-4: a bad config refuses to start and names the file and the path.
func TestLoadRefusesABadConfig(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		wantIn     []string
	}{
		{"ST-4_unknown_key", valid + "colour: blue\n", []string{"colour"}},
		{"ST-4_unknown_nested_key", strings.Replace(valid, "format: text", "format: text\n  colour: blue", 1),
			[]string{"/log", "colour"}},
		{"ST-4_bad_log_format", strings.Replace(valid, "format: text", "format: xml", 1),
			[]string{"/log/format", "xml"}},
		{"ST-4_missing_store", strings.Replace(valid, "store: data/fanin.db\n", "", 1), []string{"store"}},
		{"ST-4_bad_project_name", strings.Replace(valid, "name: beta", "name: Beta Project", 1),
			[]string{"/projects/0/name"}},
		{"ST-4_duplicate_project", strings.Replace(valid, "name: beta", "name: alpha", 1),
			[]string{"projects", `"alpha"`}},
		{"ST-4_not_yaml", "listen: [", []string{"yaml"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := write(t, tc.body)
			_, err := Load(path)
			if !errors.Is(err, ErrInvalid) {
				t.Fatalf("Load: %v, want ErrInvalid", err)
			}
			for _, s := range append([]string{path}, tc.wantIn...) {
				if !strings.Contains(err.Error(), s) {
					t.Errorf("error %q does not name %q", err, s)
				}
			}
		})
	}
}

func TestLoadUnreadableFileIsErrRead(t *testing.T) {
	_, err := Load(filepath.Join(t.TempDir(), "missing.yaml"))
	if !errors.Is(err, ErrRead) {
		t.Fatalf("Load: %v, want ErrRead", err)
	}
}
