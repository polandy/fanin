package shadow

import (
	"errors"
	"io/fs"
	"os"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/google/go-cmp/cmp"

	"github.com/polandy/fanin/internal/model"
)

const fixtures = "testdata/state"

// fakeProbe reports the lock files in held as flock-held; err fails every check.
type fakeProbe struct {
	held map[string]bool
	err  error
}

func (p fakeProbe) Held(name string) (bool, error) { return p.held[name], p.err }

// failFS fails to open one file of an otherwise working fs.FS.
type failFS struct {
	fs.FS
	name string
}

func (f failFS) Open(name string) (fs.File, error) {
	if name == f.name {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrPermission}
	}
	return f.FS.Open(name)
}

func byLabel(t *testing.T, projects []Project) map[string]Project {
	t.Helper()
	out := map[string]Project{}
	for _, p := range projects {
		out[p.Label] = p
	}
	return out
}

func TestReadFixtures(t *testing.T) {
	projects, err := Read(os.DirFS(fixtures), fakeProbe{held: map[string]bool{".delta.merge.lock": true}})
	if err != nil {
		t.Fatal(err)
	}
	labels := make([]string, 0, len(projects))
	for _, p := range projects {
		labels = append(labels, p.Label)
	}
	if diff := cmp.Diff([]string{"alpha", "beta", "delta", "gamma"}, labels); diff != "" {
		t.Errorf("labels (-want +got), the archive and other files are not projects:\n%s", diff)
	}
	got := byLabel(t, projects)

	t.Run("SH-2_holds", func(t *testing.T) {
		want := map[string][]model.Hold{
			"alpha": nil,
			"beta":  {{Project: "beta", Kind: model.HoldMerge, Reason: "post-merge of beta #433 failed: note 1"}},
			"delta": nil,
			"gamma": {{Project: "gamma", Kind: model.HoldCI, FixTask: "shadow-gamma-f-7",
				Reason: "gamma/f task 2 (gamma-f-6): red CI not caused by this diff: note 2"}},
		}
		for label, holds := range want {
			if diff := cmp.Diff(holds, got[label].Holds); diff != "" {
				t.Errorf("%s holds (-want +got):\n%s", label, diff)
			}
		}
	})
	t.Run("SH-3_merge_lock_held_by_the_PR's_task", func(t *testing.T) {
		want := &Lock{Lane: "a", PR: 1349, Since: "2026-11-25 08:50", Holder: "shadow-delta-183"}
		if diff := cmp.Diff(want, got["delta"].Lock); diff != "" {
			t.Errorf("delta lock (-want +got):\n%s", diff)
		}
		for _, label := range []string{"alpha", "beta", "gamma"} {
			if got[label].Lock != nil {
				t.Errorf("%s lock = %+v, want none", label, got[label].Lock)
			}
		}
	})
}

func TestReadHoldsAndLocks(t *testing.T) {
	log := []byte("2026-01-02 03:04 spawned alpha-1 (sonnet): p\n2026-01-02 03:05 pr #7 alpha-1 feat/x\n")
	cases := []struct {
		name  string
		files fstest.MapFS
		held  bool
		holds []model.Hold
		lock  *Lock
	}{
		{"SH-2_ci_hold_without_a_fix_has_the_pending_fix_task",
			fstest.MapFS{".alpha.ci-hold": {Data: []byte("red CI\n")}}, false,
			[]model.Hold{{Project: "alpha", Kind: model.HoldCI, Reason: "red CI", FixTask: PendingFix}}, nil},
		{"SH-2_ci_hold_takes_the_last_fix_line",
			fstest.MapFS{".alpha.ci-hold": {Data: []byte("red CI\nci-fix: alpha-2\nci-fix: alpha-3\n")}}, false,
			[]model.Hold{{Project: "alpha", Kind: model.HoldCI, Reason: "red CI", FixTask: "shadow-alpha-3"}}, nil},
		{"SH-2_empty_merge_hold_still_holds",
			fstest.MapFS{".alpha.merge-hold": {Data: nil}}, false,
			[]model.Hold{{Project: "alpha", Kind: model.HoldMerge}}, nil},
		{"SH-3_lock_file_without_a_flock_is_free",
			fstest.MapFS{".alpha.merge.lock": {Data: []byte("a 1 7 2026-01-02 03:06\n")}}, false, nil, nil},
		{"SH-3_lock_of_an_unknown_PR_names_the_lane",
			fstest.MapFS{".alpha.merge.lock": {Data: []byte("b 1 77 2026-01-02 03:06\n")}}, true, nil,
			&Lock{Lane: "b", PR: 77, Since: "2026-01-02 03:06", Holder: "lane b #77"}},
		{"SH-3_lock_of_a_known_PR_names_its_task",
			fstest.MapFS{".alpha.merge.lock": {Data: []byte("a 1 7 2026-01-02 03:06\n")}}, true, nil,
			&Lock{Lane: "a", PR: 7, Since: "2026-01-02 03:06", Holder: "shadow-alpha-1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			tc.files["alpha.md"] = &fstest.MapFile{Data: log}
			projects, err := Read(tc.files, fakeProbe{held: map[string]bool{".alpha.merge.lock": tc.held}})
			if err != nil {
				t.Fatal(err)
			}
			if len(projects) != 1 {
				t.Fatalf("projects = %+v, want alpha only", projects)
			}
			if diff := cmp.Diff(tc.holds, projects[0].Holds); diff != "" {
				t.Errorf("holds (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.lock, projects[0].Lock); diff != "" {
				t.Errorf("lock (-want +got):\n%s", diff)
			}
		})
	}
}

func TestReadUnreadable(t *testing.T) {
	ok := []byte("2026-01-02 03:04 spawned alpha-1 (sonnet): p\n")
	cases := []struct {
		name  string
		fsys  fs.FS
		probe Probe
		in    string
	}{
		{"SH-4_missing_directory", os.DirFS("testdata/none"), fakeProbe{}, "."},
		{"SH-4_unreadable_state_file", failFS{fstest.MapFS{"alpha.md": {Data: ok}}, "alpha.md"}, fakeProbe{},
			"alpha.md"},
		{"SH-4_unreadable_hold_file", failFS{fstest.MapFS{"alpha.md": {Data: ok}, ".alpha.ci-hold": {Data: ok}},
			".alpha.ci-hold"}, fakeProbe{}, ".alpha.ci-hold"},
		{"SH-4_non_UTF-8_state_file", fstest.MapFS{"alpha.md": {Data: []byte("\xfe\xff")}}, fakeProbe{}, "alpha"},
		{"SH-4_held_lock_with_bad_content", fstest.MapFS{"alpha.md": {Data: ok},
			".alpha.merge.lock": {Data: []byte("garbage\n")}},
			fakeProbe{held: map[string]bool{".alpha.merge.lock": true}}, ".alpha.merge.lock"},
		{"SH-4_lock_check_fails", fstest.MapFS{"alpha.md": {Data: ok}, ".alpha.merge.lock": {Data: ok}},
			fakeProbe{err: errors.New("no /proc/locks")}, "no /proc/locks"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			projects, err := Read(tc.fsys, tc.probe)
			if !errors.Is(err, ErrUnreadable) || !strings.Contains(err.Error(), tc.in) {
				t.Fatalf("Read = %+v, %v; want ErrUnreadable naming %q", projects, err, tc.in)
			}
			if projects != nil {
				t.Errorf("projects = %+v, want none next to the error", projects)
			}
		})
	}
}
