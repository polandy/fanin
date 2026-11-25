package intake

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"slices"
	"strings"

	"github.com/polandy/fanin/internal/config"
	"github.com/polandy/fanin/internal/model"
)

// Delivery headers of GitHub, Gitea and HMAC-signing alert senders.
const (
	HeaderGitHubEvent    = "X-GitHub-Event"
	HeaderGiteaEvent     = "X-Gitea-Event"
	HeaderHubSignature   = "X-Hub-Signature-256"
	HeaderGiteaSignature = "X-Gitea-Signature"
	// SignaturePrefix starts the hex HMAC-SHA256 in HeaderHubSignature.
	SignaturePrefix = "sha256="
)

// Webhook events and actions fanin acts on.
const (
	eventIssues     = "issues"
	eventIssueLabel = "issue_label"
	eventRelease    = "release"
	alertFiring     = "firing"
	actionPublished = "published"
	// alertIDHex is the hex length of the alert hash in an alert task ID.
	alertIDHex = 12
)

// issueActions are the issue actions that can carry the label.
var issueActions = []string{"labeled", "label_updated", "opened", "edited"}

// The webhook errors; the API maps them to status codes.
var (
	// ErrSignature is a delivery without a valid HMAC signature; nothing was queued.
	ErrSignature = errors.New("intake: webhook signature invalid")
	// ErrUnknownWebhook is a name triggers.yaml does not configure.
	ErrUnknownWebhook = errors.New("intake: unknown webhook")
	// ErrBusy is a release queue that is full; the sender retries.
	ErrBusy = errors.New("intake: release queue full")
	// ErrNoSecret is a webhook whose secret variable is unset; fanind does not start.
	ErrNoSecret = errors.New("intake: webhook secret unset")
)

// Releaser starts the deploy of a release in the background.
type Releaser interface {
	// Submit queues ev; ErrBusy when the queue is full.
	Submit(ev ReleaseEvent) error
}

// Delivery is what a webhook delivery did.
type Delivery struct {
	Outcome Outcome
	// TaskID is the queued or already queued task.
	TaskID model.TaskID
}

// Webhooks checks and maps the deliveries of triggers.yaml's webhooks (see SCENARIOS IN-1 to IN-3, IN-5).
type Webhooks struct {
	hooks    map[string]config.Webhook
	secrets  map[string][]byte
	intake   Enqueuer
	releases Releaser
}

// NewWebhooks reads each webhook's secret through getenv; an unset secret is an error, so fanind does not start.
func NewWebhooks(cfg config.Config, getenv func(string) string, e Enqueuer, r Releaser) (*Webhooks, error) {
	w := &Webhooks{hooks: cfg.Triggers.Webhooks, secrets: map[string][]byte{}, intake: e, releases: r}
	for _, name := range slices.Sorted(maps.Keys(w.hooks)) {
		h := w.hooks[name]
		secret := getenv(h.SecretEnv)
		if secret == "" {
			return nil, fmt.Errorf("%w: webhook %s: %s is unset; export the HMAC secret the sender signs with",
				ErrNoSecret, name, h.SecretEnv)
		}
		if h.Kind == config.WebhookRelease && r == nil {
			return nil, fmt.Errorf("intake: webhook %s: no release queue; add the release section to triggers.yaml",
				name)
		}
		w.secrets[name] = []byte(secret)
	}
	return w, nil
}

// Receive checks the signature of body and queues what the delivery asks for; on an error nothing is queued.
func (w *Webhooks) Receive(ctx context.Context, name string, h http.Header, body []byte) (Delivery, error) {
	hook, ok := w.hooks[name]
	if !ok {
		return Delivery{}, fmt.Errorf("%w: %q; configure it under webhooks in triggers.yaml", ErrUnknownWebhook, name)
	}
	if !validSignature(w.secrets[name], h, body) {
		return Delivery{}, fmt.Errorf("%w: webhook %s; sign the body with the secret of %s", ErrSignature, name,
			hook.SecretEnv)
	}
	event := h.Get(HeaderGitHubEvent)
	if event == "" {
		event = h.Get(HeaderGiteaEvent)
	}
	switch hook.Kind {
	case config.WebhookForge:
		return w.issue(ctx, hook, event, body)
	case config.WebhookAlert:
		return w.alert(ctx, hook, body)
	case config.WebhookRelease:
		return w.release(hook, event, body)
	}
	return Delivery{}, fmt.Errorf("intake: webhook %s has kind %q; use forge, alert or release", name, hook.Kind)
}

// validSignature checks the GitHub "sha256=<hex>" or the Gitea "<hex>" HMAC-SHA256 of body.
func validSignature(secret []byte, h http.Header, body []byte) bool {
	got, ok := strings.CutPrefix(h.Get(HeaderHubSignature), SignaturePrefix)
	if !ok {
		got = h.Get(HeaderGiteaSignature)
	}
	sig, err := hex.DecodeString(got)
	if err != nil || len(sig) != sha256.Size {
		return false
	}
	m := hmac.New(sha256.New, secret)
	m.Write(body)
	return hmac.Equal(sig, m.Sum(nil))
}

func decode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return invalidf("webhook payload: %v", err)
	}
	return nil
}

type issueLabel struct {
	Name string `json:"name"`
}

// issue queues an issue that carries the webhook's label as task <project>-issue-<n> (see SCENARIOS IN-2).
func (w *Webhooks) issue(ctx context.Context, hook config.Webhook, event string, body []byte) (Delivery, error) {
	if event != eventIssues && event != eventIssueLabel {
		return Delivery{Outcome: OutcomeIgnored}, nil
	}
	var p struct {
		Action string `json:"action"`
		Issue  struct {
			Number      int             `json:"number"`
			Title       string          `json:"title"`
			Body        string          `json:"body"`
			URL         string          `json:"html_url"`
			PullRequest json.RawMessage `json:"pull_request"`
			Labels      []issueLabel    `json:"labels"`
		} `json:"issue"`
	}
	if err := decode(body, &p); err != nil {
		return Delivery{}, err
	}
	labelled := slices.ContainsFunc(p.Issue.Labels, func(l issueLabel) bool { return l.Name == hook.Label })
	if !slices.Contains(issueActions, p.Action) || !labelled || p.Issue.PullRequest != nil {
		return Delivery{Outcome: OutcomeIgnored}, nil
	}
	if p.Issue.Number <= 0 || strings.TrimSpace(p.Issue.Title) == "" {
		return Delivery{}, invalidf("issue payload without number or title")
	}
	text := strings.Join([]string{p.Issue.Title, p.Issue.Body, p.Issue.URL}, "\n\n")
	return w.enqueue(ctx, TaskRequest{ID: model.TaskID(fmt.Sprintf("%s-issue-%d", hook.Project, p.Issue.Number)),
		Project: hook.Project, Task: text, Model: hook.Model})
}

// alert queues one analysis per firing alert group and start (see SCENARIOS IN-3).
func (w *Webhooks) alert(ctx context.Context, hook config.Webhook, body []byte) (Delivery, error) {
	var p struct {
		Status       string            `json:"status"`
		GroupKey     string            `json:"groupKey"`
		CommonLabels map[string]string `json:"commonLabels"`
		Alerts       []struct {
			Status      string            `json:"status"`
			Labels      map[string]string `json:"labels"`
			Annotations map[string]string `json:"annotations"`
			StartsAt    string            `json:"startsAt"`
		} `json:"alerts"`
	}
	if err := decode(body, &p); err != nil {
		return Delivery{}, err
	}
	if p.Status != alertFiring {
		return Delivery{Outcome: OutcomeIgnored}, nil
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Alert %s is firing. Find the cause and propose the fix; change nothing.\n",
		p.CommonLabels["alertname"])
	var starts []string
	for _, a := range p.Alerts {
		if a.Status != alertFiring {
			continue
		}
		starts = append(starts, a.StartsAt)
		fmt.Fprintf(&b, "\n- since %s: %s", a.StartsAt, pairs(a.Labels))
		if len(a.Annotations) > 0 {
			fmt.Fprintf(&b, "; %s", pairs(a.Annotations))
		}
	}
	if len(starts) == 0 {
		return Delivery{Outcome: OutcomeIgnored}, nil
	}
	sum := sha256.Sum256([]byte(p.GroupKey + "\n" + slices.Min(starts)))
	id := model.TaskID(fmt.Sprintf("%s-alert-%s", hook.Project, hex.EncodeToString(sum[:])[:alertIDHex]))
	return w.enqueue(ctx, TaskRequest{ID: id, Project: hook.Project, Analyze: b.String(), Model: hook.Model})
}

// pairs is m as sorted "k=v" pairs.
func pairs(m map[string]string) string {
	out := make([]string, 0, len(m))
	for _, k := range slices.Sorted(maps.Keys(m)) {
		out = append(out, k+"="+m[k])
	}
	return strings.Join(out, " ")
}

// release hands a published release to the deploy queue (see SCENARIOS IN-5, DR-1).
func (w *Webhooks) release(hook config.Webhook, event string, body []byte) (Delivery, error) {
	if event != eventRelease {
		return Delivery{Outcome: OutcomeIgnored}, nil
	}
	var p struct {
		Action  string `json:"action"`
		Release struct {
			Tag string `json:"tag_name"`
		} `json:"release"`
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := decode(body, &p); err != nil {
		return Delivery{}, err
	}
	if p.Action != actionPublished {
		return Delivery{Outcome: OutcomeIgnored}, nil
	}
	if p.Release.Tag == "" || !strings.Contains(p.Repository.FullName, "/") {
		return Delivery{}, invalidf("release payload without tag_name or repository.full_name")
	}
	ev := ReleaseEvent{Project: hook.Project, Repo: p.Repository.FullName, Version: p.Release.Tag}
	if err := w.releases.Submit(ev); err != nil {
		return Delivery{}, err
	}
	return Delivery{Outcome: OutcomeDeploying}, nil
}

func (w *Webhooks) enqueue(ctx context.Context, r TaskRequest) (Delivery, error) {
	switch _, err := w.intake.Enqueue(ctx, r); {
	case errors.Is(err, ErrExists):
		return Delivery{Outcome: OutcomeDuplicate, TaskID: r.ID}, nil
	case err != nil:
		return Delivery{}, err
	}
	return Delivery{Outcome: OutcomeQueued, TaskID: r.ID}, nil
}
