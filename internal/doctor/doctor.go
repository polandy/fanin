// Package doctor checks that every component fanin depends on answers: config, store, herdr, forge auth, branch
// protection and systemd. A component that does not answer is a failed row, never an ok one.
package doctor

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"slices"
	"strings"
	"time"

	"github.com/polandy/fanin/internal/clock"
	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/forge"
	"github.com/polandy/fanin/internal/gate"
	"github.com/polandy/fanin/internal/runtime"
	"github.com/polandy/fanin/internal/runtime/herdr"
	"github.com/polandy/fanin/internal/store"
)

// Status is the verdict of one row.
type Status string

// The row verdicts. Skip is a check that cannot run here and says why; it is not a pass.
const (
	OK   Status = "ok"
	Fail Status = "FAIL"
	Skip Status = "skip"
)

// The checks, by name.
const (
	CheckConfig     = "config"
	CheckStore      = "store"
	CheckHerdr      = "herdr"
	CheckForgeAuth  = "forge-auth"
	CheckProtection = "branch-protection"
	CheckSystemd    = "systemd"
)

// Unit is the systemd unit of the daemon.
const Unit = "fanind"

// activeState is what systemctl is-active prints for a running unit.
const activeState = "active"

// CallTimeout bounds every external call.
const CallTimeout = 20 * time.Second

// Deps is what Run needs.
type Deps struct {
	ConfigDir string
	Getenv    func(string) string
	Runner    runtime.Runner
}

// Result is one row of the table.
type Result struct {
	Check   string
	Subject string
	Status  Status
	Detail  string
}

// Failed counts the failed rows.
func Failed(res []Result) int {
	n := 0
	for _, r := range res {
		if r.Status == Fail {
			n++
		}
	}
	return n
}

type doctor struct {
	Deps
	cfg     config.Config
	secrets []string
	res     []Result
}

// Run checks every component. An invalid config fails its row and every row that depends on it.
func Run(ctx context.Context, d Deps) []Result {
	dr := &doctor{Deps: d}
	cfg, err := config.Load(d.ConfigDir)
	if err != nil {
		dr.add(CheckConfig, "", err)
		dr.add(CheckStore, "", errors.New("not checked: the config is invalid; fix it first"))
		dr.add(CheckHerdr, "", errors.New("not checked: the config is invalid; fix it first"))
		dr.add(CheckSystemd, "", errors.New("not checked: the config is invalid; fix it first"))
		return dr.res
	}
	dr.cfg = cfg
	dr.add(CheckConfig, "", nil, "valid, "+fmt.Sprint(len(cfg.Projects))+" projects")
	for _, f := range cfg.Forges {
		if v := d.Getenv(f.TokenEnv); v != "" {
			dr.secrets = append(dr.secrets, v)
		}
	}
	if v := d.Getenv(cfg.OwnerTokenEnv); v != "" {
		dr.secrets = append(dr.secrets, v)
	}
	dr.store()
	dr.herdr(ctx)
	for _, p := range cfg.Projects {
		dr.forgeAuth(ctx, p)
		dr.protection(ctx, p)
	}
	dr.systemd(ctx)
	return dr.res
}

// add records a row: a nil err is ok with the first detail, an error fails with its scrubbed text.
func (d *doctor) add(check, subject string, err error, detail ...string) {
	r := Result{Check: check, Subject: subject, Status: OK, Detail: strings.Join(detail, " ")}
	if err != nil {
		r.Status, r.Detail = Fail, d.scrub(err.Error())
	}
	d.res = append(d.res, r)
}

func (d *doctor) skip(check, subject, why string) {
	d.res = append(d.res, Result{Check: check, Subject: subject, Status: Skip, Detail: why})
}

// scrub removes every known token value and anything that looks like a credential.
func (d *doctor) scrub(s string) string {
	for _, v := range d.secrets {
		s = strings.ReplaceAll(s, v, forge.Redacted)
	}
	return forge.Redact(s)
}

func (d *doctor) store() {
	path := d.cfg.Store
	if _, err := os.Stat(path); err != nil {
		d.add(CheckStore, "", fmt.Errorf("no store file at %s: %w; start fanind once or fix store in fanin.yaml", path, err))
		return
	}
	st, err := store.Open(context.Background(), path, clock.Real{})
	if err != nil {
		d.add(CheckStore, "", fmt.Errorf("open %s: %w", path, err))
		return
	}
	if err := st.Close(); err != nil {
		d.add(CheckStore, "", fmt.Errorf("close %s: %w", path, err))
		return
	}
	d.add(CheckStore, "", nil, path)
}

func (d *doctor) herdr(ctx context.Context) {
	uses := false
	for _, k := range d.cfg.WorkerKinds {
		uses = uses || k.Runtime == config.RuntimeHerdr
	}
	if !uses {
		d.skip(CheckHerdr, "", "no worker kind runs in herdr")
		return
	}
	_, err := d.run(ctx, nil, herdr.Program, "workspace", "list")
	d.add(CheckHerdr, "", err, "answers")
}

func (d *doctor) systemd(ctx context.Context) {
	out, err := d.run(ctx, nil, "systemctl", "is-active", Unit)
	state := strings.TrimSpace(string(out))
	switch {
	case err != nil && state != "":
		d.add(CheckSystemd, Unit, fmt.Errorf("unit %s is %s; start it with: sudo systemctl start %s", Unit, state, Unit))
	case err != nil:
		d.add(CheckSystemd, Unit, err)
	case state != activeState:
		d.add(CheckSystemd, Unit, fmt.Errorf("unit %s is %q, want %s; start it with: sudo systemctl start %s",
			Unit, state, activeState, Unit))
	default:
		d.add(CheckSystemd, Unit, nil, activeState)
	}
}

func (d *doctor) forgeAuth(ctx context.Context, p config.Project) {
	f := d.cfg.Forges[p.Forge]
	var err error
	switch f.Kind {
	case config.ForgeGitHub:
		_, err = d.run(ctx, nil, "gh", "auth", "status")
	case config.ForgeGitea:
		_, err = d.gitea(ctx, f, "/api/v1/user")
	default:
		err = fmt.Errorf("forge %q has kind %q; use github or gitea", p.Forge, f.Kind)
	}
	d.add(CheckForgeAuth, p.Name, err, p.Forge+" accepts the token")
}

// protection checks that the base branch requires the fanin/review status.
func (d *doctor) protection(ctx context.Context, p config.Project) {
	if p.Host != config.HostLocal {
		d.skip(CheckProtection, p.Name, "repo lives on host "+p.Host+"; run fanin doctor there")
		return
	}
	f := d.cfg.Forges[p.Forge]
	repo, err := d.repo(ctx, p)
	if err != nil {
		d.add(CheckProtection, p.Name, err)
		return
	}
	var contexts []string
	switch f.Kind {
	case config.ForgeGitHub:
		contexts, err = d.githubContexts(ctx, repo, p.Base)
	case config.ForgeGitea:
		contexts, err = d.giteaContexts(ctx, f, repo, p.Base)
	default:
		err = fmt.Errorf("forge %q has kind %q; use github or gitea", p.Forge, f.Kind)
	}
	switch {
	case err != nil:
		d.add(CheckProtection, p.Name, err)
	case !slices.Contains(contexts, gate.ReviewContext):
		d.add(CheckProtection, p.Name, fmt.Errorf("%s on %s requires %v but not %q; add it to the branch protection rule",
			p.Base, repo, contexts, gate.ReviewContext))
	default:
		d.add(CheckProtection, p.Name, nil, repo+"@"+p.Base+" requires "+gate.ReviewContext)
	}
}

func (d *doctor) githubContexts(ctx context.Context, repo, base string) ([]string, error) {
	out, err := d.run(ctx, nil, "gh", "api", "repos/"+repo+"/branches/"+base+"/protection/required_status_checks",
		"--jq", ".contexts")
	if err != nil {
		return nil, fmt.Errorf("read the branch protection of %s@%s: %w", repo, base, err)
	}
	var contexts []string
	if err := json.Unmarshal(out, &contexts); err != nil {
		return nil, fmt.Errorf("branch protection of %s@%s: unreadable answer: %w", repo, base, err)
	}
	return contexts, nil
}

func (d *doctor) giteaContexts(ctx context.Context, f config.Forge, repo, base string) ([]string, error) {
	out, err := d.gitea(ctx, f, "/api/v1/repos/"+repo+"/branch_protections/"+base)
	if err != nil {
		return nil, fmt.Errorf("read the branch protection of %s@%s: %w", repo, base, err)
	}
	var body struct {
		Contexts []string `json:"status_check_contexts"`
	}
	if err := json.Unmarshal(out, &body); err != nil {
		return nil, fmt.Errorf("branch protection of %s@%s: unreadable answer: %w", repo, base, err)
	}
	return body.Contexts, nil
}

// gitea GETs path with the forge token; curl reads its config, token included, from stdin so it stays out of argv.
func (d *doctor) gitea(ctx context.Context, f config.Forge, path string) ([]byte, error) {
	token := d.Getenv(f.TokenEnv)
	if token == "" {
		return nil, fmt.Errorf("$%s is empty; export the forge token there (token_env in forges.yaml)", f.TokenEnv)
	}
	base := strings.TrimSuffix(f.URL, "/")
	cfg := fmt.Sprintf("url = %q\nheader = %q\n", base+path, "Authorization: token "+token)
	return d.run(ctx, []byte(cfg), "curl", "-fsS", "-K", "-")
}

// repo reads owner/name from the origin remote of the project's checkout.
func (d *doctor) repo(ctx context.Context, p config.Project) (string, error) {
	out, err := d.run(ctx, nil, "git", "-C", p.Path, "remote", "get-url", "origin")
	if err != nil {
		return "", fmt.Errorf("read the origin of %s: %w", p.Path, err)
	}
	return ParseRepo(strings.TrimSpace(string(out)))
}

// ParseRepo returns owner/name from a git remote URL, ssh or http(s).
func ParseRepo(remote string) (string, error) {
	path := remote
	if u, err := url.Parse(remote); err == nil && u.Host != "" {
		path = u.Path
	} else if _, after, ok := strings.Cut(remote, ":"); ok {
		path = after
	}
	parts := strings.Split(strings.Trim(strings.TrimSuffix(path, ".git"), "/"), "/")
	if len(parts) < 2 || parts[len(parts)-1] == "" || parts[len(parts)-2] == "" {
		return "", fmt.Errorf("remote %q has no owner/name; set origin to the forge URL of the repo", forge.Redact(remote))
	}
	return strings.Join(parts[len(parts)-2:], "/"), nil
}

func (d *doctor) run(ctx context.Context, stdin []byte, argv ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, CallTimeout)
	defer cancel()
	res, err := d.Runner.Run(ctx, runtime.Cmd{Argv: argv, Stdin: stdin})
	if err != nil {
		return res.Stdout, fmt.Errorf("%s: %w", argv[0], err)
	}
	return res.Stdout, nil
}
