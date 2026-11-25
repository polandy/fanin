package shadow

import (
	"fmt"
	"io/fs"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/polandy/fanin/internal/model"
)

// PendingFix is the fix task of a ci hold whose file names no `ci-fix:` agent yet.
const PendingFix model.TaskID = IDPrefix + "pending-fix"

// ciFixPrefix starts the line of a ci hold file that names the fixing agent.
const ciFixPrefix = "ci-fix:"

// The file names of the state directory; group 1 is the project label.
var (
	stateFile = regexp.MustCompile(`^([^.].*)\.md$`)
	mergeHold = regexp.MustCompile(`^\.(.+)\.merge-hold$`)
	ciHold    = regexp.MustCompile(`^\.(.+)\.ci-hold$`)
	mergeLock = regexp.MustCompile(`^\.(.+)\.merge\.lock$`)
	lockBody  = regexp.MustCompile(`^([a-z]) [0-9]+ ([0-9]+) ([0-9]{4}-[0-9]{2}-[0-9]{2} [0-9]{2}:[0-9]{2})$`)
)

// Project is the imported state of one project label.
type Project struct {
	Label string
	Tasks []Task
	// Holds are the active holds, without IDs: merge, then ci.
	Holds []model.Hold
	// Lock is the merge lock while a process holds its flock, else nil.
	Lock *Lock
}

// Lock is a held merge lock file.
type Lock struct {
	Lane  string
	PR    int
	Since string
	// Holder is the task that owns PR, else "lane <x> #<pr>".
	Holder string
}

// Probe reports whether a process holds a flock on a file of the state directory.
type Probe interface {
	Held(name string) (bool, error)
}

// files are the state directory files of one label.
type files struct{ state, mergeHold, ciHold, mergeLock string }

// Read reads the state directory fsys into its projects, sorted by label; it only reads (see SCENARIOS SH-5).
func Read(fsys fs.FS, probe Probe) ([]Project, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("%w: read the state directory .: %w; point --shadow at the bash state directory",
			ErrUnreadable, err)
	}
	byLabel := map[string]*files{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		for _, f := range []struct {
			re   *regexp.Regexp
			slot func(*files) *string
		}{
			{stateFile, func(f *files) *string { return &f.state }},
			{mergeHold, func(f *files) *string { return &f.mergeHold }},
			{ciHold, func(f *files) *string { return &f.ciHold }},
			{mergeLock, func(f *files) *string { return &f.mergeLock }},
		} {
			if m := f.re.FindStringSubmatch(name); m != nil {
				if byLabel[m[1]] == nil {
					byLabel[m[1]] = &files{}
				}
				*f.slot(byLabel[m[1]]) = name
				break
			}
		}
	}
	labels := make([]string, 0, len(byLabel))
	for label := range byLabel {
		labels = append(labels, label)
	}
	slices.Sort(labels)
	out := make([]Project, 0, len(labels))
	for _, label := range labels {
		p, err := readProject(fsys, probe, label, byLabel[label])
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func readProject(fsys fs.FS, probe Probe, label string, f *files) (Project, error) {
	p := Project{Label: label}
	if f.state != "" {
		data, err := readText(fsys, f.state)
		if err != nil {
			return Project{}, err
		}
		if p.Tasks, err = ParseLog(label, []byte(data)); err != nil {
			return Project{}, err
		}
	}
	if f.mergeHold != "" {
		data, err := readText(fsys, f.mergeHold)
		if err != nil {
			return Project{}, err
		}
		p.Holds = append(p.Holds, model.Hold{Project: label, Kind: model.HoldMerge, Reason: firstLine(data)})
	}
	if f.ciHold != "" {
		data, err := readText(fsys, f.ciHold)
		if err != nil {
			return Project{}, err
		}
		fix := PendingFix
		for line := range strings.Lines(data) {
			if agent, ok := strings.CutPrefix(strings.TrimSpace(line), ciFixPrefix); ok {
				fix = TaskID(strings.TrimSpace(agent))
			}
		}
		p.Holds = append(p.Holds, model.Hold{Project: label, Kind: model.HoldCI, Reason: firstLine(data), FixTask: fix})
	}
	if f.mergeLock != "" {
		lock, err := readLock(fsys, probe, f.mergeLock, p.Tasks)
		if err != nil {
			return Project{}, err
		}
		p.Lock = lock
	}
	return p, nil
}

// readLock returns the lock of a held merge lock file, nil when no process holds its flock.
func readLock(fsys fs.FS, probe Probe, name string, tasks []Task) (*Lock, error) {
	held, err := probe.Held(name)
	if err != nil {
		return nil, fmt.Errorf("%w: check the flock of %s: %w", ErrUnreadable, name, err)
	}
	if !held {
		return nil, nil
	}
	data, err := readText(fsys, name)
	if err != nil {
		return nil, err
	}
	m := lockBody.FindStringSubmatch(firstLine(data))
	if m == nil {
		return nil, fmt.Errorf("%w: held merge lock %s has %q, want \"<lane> <pid> <pr> <YYYY-MM-DD HH:MM>\"; "+
			"check the bash orchestrator that holds it", ErrUnreadable, name, firstLine(data))
	}
	pr, err := strconv.Atoi(m[2])
	if err != nil {
		return nil, fmt.Errorf("%w: held merge lock %s: PR %q: %w", ErrUnreadable, name, m[2], err)
	}
	lock := &Lock{Lane: m[1], PR: pr, Since: m[3], Holder: fmt.Sprintf("lane %s #%d", m[1], pr)}
	for _, t := range tasks {
		if t.PR == pr {
			lock.Holder = string(t.ID)
		}
	}
	return lock, nil
}

func readText(fsys fs.FS, name string) (string, error) {
	data, err := fs.ReadFile(fsys, name)
	if err != nil {
		return "", fmt.Errorf("%w: read %s: %w; make it readable for fanind", ErrUnreadable, name, err)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%w: %s is not UTF-8; repair or move it out of the state directory", ErrUnreadable, name)
	}
	return string(data), nil
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(s, "\n")
	return strings.TrimRight(line, "\r")
}
