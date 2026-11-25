package main

import (
	"context"
	"fmt"
	"strings"
	"text/tabwriter"

	"github.com/polandy/fanin/internal/cli"
	"github.com/polandy/fanin/internal/doctor"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/exec"
)

// Table layout of the doctor output.
const (
	doctorPad   = 2
	doctorEmpty = "-"
)

// doctorRunner runs the external programs doctor asks; tests replace it.
var doctorRunner runtime.Runner = exec.Runner{}

func init() {
	register(Command{
		Name:    "doctor",
		Summary: "check config, store, herdr, forge auth, branch protection and systemd; exit 1 on any failure",
		Run:     runDoctor,
	})
}

func runDoctor(ctx context.Context, g Globals, out cli.Output, _ []string) error {
	if g.Config == "" {
		return cli.Usagef("doctor needs the config directory; set --config or $%s", envConfig)
	}
	res := doctor.Run(ctx, doctor.Deps{ConfigDir: g.Config, Getenv: g.Getenv, Runner: doctorRunner})
	var sb strings.Builder
	tw := tabwriter.NewWriter(&sb, 0, 0, doctorPad, ' ', 0)
	fmt.Fprintln(tw, "CHECK\tSUBJECT\tSTATUS\tDETAIL")
	for _, r := range res {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\n", r.Check, orDash(r.Subject), r.Status, oneLine(r.Detail))
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("doctor: render the table: %w", err)
	}
	out.Printf("%s", sb.String())
	if n := doctor.Failed(res); n > 0 {
		return fmt.Errorf("doctor: %d of %d checks failed; fix the FAIL rows above", n, len(res))
	}
	return nil
}

func orDash(s string) string {
	if s == "" {
		return doctorEmpty
	}
	return s
}

// oneLine keeps a multi-line tool error in its table row.
func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
