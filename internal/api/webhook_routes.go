package api

import (
	"context"
	"errors"
	"io"
	"net/http"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/intake"
)

func init() { RegisterBuiltin(registerWebhooks) }

// Webhooks checks a delivery's signature and queues what it asks for (internal/intake).
type Webhooks interface {
	Receive(ctx context.Context, name string, h http.Header, body []byte) (intake.Delivery, error)
}

// webhookStatus is the status of each delivery outcome.
var webhookStatus = map[intake.Outcome]int{
	intake.OutcomeQueued:    http.StatusCreated,
	intake.OutcomeDuplicate: http.StatusOK,
	intake.OutcomeIgnored:   http.StatusOK,
	intake.OutcomeDeploying: http.StatusAccepted,
}

func registerWebhooks(r *Registry) {
	r.Add(Route{Pattern: "POST /api/v1/webhooks/{name}", Auth: AuthWebhook, Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			name := r.PathValue("name")
			if s.cfg.Webhooks == nil {
				WriteError(w, http.StatusNotFound, wire.CodeNotFound,
					"no webhook "+name+"; configure webhooks in triggers.yaml")
				return
			}
			body, err := io.ReadAll(r.Body)
			if err != nil {
				writeTaskError(s, w, r, err)
				return
			}
			d, err := s.cfg.Webhooks.Receive(r.Context(), name, r.Header, body)
			switch {
			case errors.Is(err, intake.ErrUnknownWebhook):
				WriteError(w, http.StatusNotFound, wire.CodeNotFound, err.Error())
			case errors.Is(err, intake.ErrSignature):
				WriteError(w, http.StatusUnauthorized, wire.CodeUnauthorized, err.Error())
			case errors.Is(err, intake.ErrBusy):
				WriteError(w, http.StatusServiceUnavailable, wire.CodeBusy, err.Error())
			case err != nil:
				writeTaskError(s, w, r, err)
			default:
				status, ok := webhookStatus[d.Outcome]
				if !ok {
					WriteError(w, http.StatusInternalServerError, wire.CodeInternal,
						"webhook "+name+" answered outcome "+string(d.Outcome)+"; this is a fanind bug")
					return
				}
				WriteJSON(w, status, wire.WebhookResult{Outcome: string(d.Outcome),
					Task: string(d.TaskID)})
			}
		})
	}})
}
