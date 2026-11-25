// Package fakebin builds a per-test PATH directory of script fakes for herdr, gh, claude and git.
// A fake logs every call and answers only argv that the test scripted; any other argv exits with ExitUnexpected.
package fakebin

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// ExitUnexpected is the exit code of a fake that was called with argv nobody scripted.
const ExitUnexpected = 97

// Answer is one scripted reply of a fake.
type Answer struct {
	Stdout string
	Stderr string
	Exit   int
}

// Call is one logged call of a fake.
type Call struct {
	Argv []string
	// Dir is the working directory the fake ran in.
	Dir string
}

// Bin is the set of fakes of one test.
type Bin struct {
	t     *testing.T
	dir   string
	fakes map[string]*Fake
}

// Fake is one scripted binary.
type Fake struct {
	name  string
	rules []rule
}

type rule struct {
	argv    []string
	answers []Answer
	// prefix matches every argv that starts with argv.
	prefix bool
}

// New returns an empty Bin in a fresh temp dir; Install puts it on PATH.
func New(t *testing.T) *Bin {
	t.Helper()
	return &Bin{t: t, dir: t.TempDir(), fakes: map[string]*Fake{}}
}

// Dir is the directory that holds the fake binaries.
func (b *Bin) Dir() string { return b.dir }

// Fake returns the fake called name, creating it on first use.
func (b *Bin) Fake(name string) *Fake {
	if f, ok := b.fakes[name]; ok {
		return f
	}
	f := &Fake{name: name}
	b.fakes[name] = f
	return f
}

// On scripts the answers for exactly argv: the nth call gets the nth answer and the last answer repeats.
func (f *Fake) On(argv []string, answers ...Answer) *Fake {
	if len(answers) == 0 {
		answers = []Answer{{}}
	}
	f.rules = append(f.rules, rule{argv: argv, answers: answers})
	return f
}

// OnPrefix scripts the answers for every argv that starts with prefix, as On does for one exact argv.
func (f *Fake) OnPrefix(prefix []string, answers ...Answer) *Fake {
	f.On(prefix, answers...)
	f.rules[len(f.rules)-1].prefix = true
	return f
}

// Install writes the scripts and makes the fake dir the whole PATH of this test; an unscripted call fails the test.
func (b *Bin) Install() {
	b.t.Helper()
	for name, f := range b.fakes {
		if err := os.WriteFile(filepath.Join(b.dir, name), []byte(f.script(b.dir)), 0o755); err != nil { //nolint:gosec // fakes must be executable
			b.t.Fatalf("fakebin: cannot write the fake %s in %s: %v", name, b.dir, err)
		}
	}
	b.t.Setenv("PATH", b.dir)
	b.t.Cleanup(func() {
		for name, f := range b.fakes {
			for _, c := range b.read(name, unexpectedLog) {
				b.t.Errorf("fakebin: %s was called with unscripted argv %q; script it with On (%d rules)", f.name, c.Argv, len(f.rules))
			}
		}
	})
}

// Calls returns the logged calls of the fake name in order, including unscripted ones.
func (b *Bin) Calls(name string) []Call { return b.read(name, callLog) }

const (
	callLog       = "calls"
	unexpectedLog = "unexpected"
)

func (b *Bin) read(name, kind string) []Call {
	data, err := os.ReadFile(filepath.Join(b.dir, name+"."+kind))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		b.t.Fatalf("fakebin: cannot read the %s log of %s: %v", kind, name, err)
	}
	calls, err := parseLog(data)
	if err != nil {
		b.t.Fatalf("fakebin: bad %s log of %s: %v", kind, name, err)
	}
	return calls
}

// parseLog reads records of `<dir>\n<argc>\n` followed by argc NUL-terminated args.
func parseLog(data []byte) ([]Call, error) {
	var calls []Call
	rest := string(data)
	line := func() (string, error) {
		dir, tail, ok := strings.Cut(rest, "\n")
		if !ok {
			return "", fmt.Errorf("truncated record near %q", rest)
		}
		rest = tail
		return dir, nil
	}
	for rest != "" {
		dir, err := line()
		if err != nil {
			return nil, err
		}
		count, err := line()
		if err != nil {
			return nil, err
		}
		n, err := strconv.Atoi(count)
		if err != nil {
			return nil, fmt.Errorf("bad argument count %q: %w", count, err)
		}
		c := Call{Dir: dir, Argv: []string{}}
		for range n {
			arg, tail, ok := strings.Cut(rest, "\x00")
			if !ok {
				return nil, fmt.Errorf("truncated argument near %q", rest)
			}
			c.Argv = append(c.Argv, arg)
			rest = tail
		}
		calls = append(calls, c)
	}
	return calls, nil
}

// quote is a POSIX single-quoted string.
func quote(s string) string { return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'" }

// key is the shell expression a rule's argv is matched as: each arg followed by the unit separator in $s.
func key(argv []string) string {
	var b strings.Builder
	b.WriteString(`""`)
	for _, a := range argv {
		b.WriteString(quote(a) + `"$s"`)
	}
	return b.String()
}

func (f *Fake) script(dir string) string {
	var b strings.Builder
	b.WriteString("#!/bin/sh\n")
	b.WriteString("s=$(printf '\\037')\n")
	// Log the call before matching, so an unscripted call is recorded too.
	logCall := func(file string) string {
		return fmt.Sprintf("{ pwd -P; echo $#; printf '%%s\\0' \"$@\"; } >> %s\n", quote(filepath.Join(dir, f.name+"."+file)))
	}
	b.WriteString(logCall(callLog))
	// Drain stdin so a writer never blocks or breaks on a closed pipe.
	b.WriteString("while IFS= read -r l || [ -n \"$l\" ]; do :; done\n")
	b.WriteString("k=\"\"; for a in \"$@\"; do k=\"$k$a$s\"; done\n")
	b.WriteString("case \"$k\" in\n")
	for i, r := range f.rules {
		counter := quote(filepath.Join(dir, fmt.Sprintf("%s.n%d", f.name, i)))
		pat := key(r.argv)
		if r.prefix {
			pat += "*"
		}
		fmt.Fprintf(&b, "%s)\n", pat)
		fmt.Fprintf(&b, "  n=0; [ -f %s ] && read -r n < %s\n  echo $((n + 1)) > %s\n", counter, counter, counter)
		b.WriteString("  case \"$n\" in\n")
		for j, a := range r.answers {
			pat := strconv.Itoa(j)
			if j == len(r.answers)-1 {
				pat = "*"
			}
			fmt.Fprintf(&b, "  %s) printf '%%s' %s; printf '%%s' %s >&2; exit %d;;\n", pat, quote(a.Stdout), quote(a.Stderr), a.Exit)
		}
		b.WriteString("  esac;;\n")
	}
	b.WriteString("esac\n")
	b.WriteString(logCall(unexpectedLog))
	fmt.Fprintf(&b, "echo \"fakebin: unscripted argv for %s: $*\" >&2\nexit %d\n", f.name, ExitUnexpected)
	return b.String()
}
