package api

import (
	"fmt"
	"net/http"
	"strconv"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/owner"
)

func init() { RegisterBuiltin(registerWait) }

func registerWait(r *Registry) {
	r.Add(Route{Pattern: "GET /api/v1/wait", Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { wait(s, w, r) })
	}})
}

// wait answers the first event after the cursor (default: the newest event now), only an owner-attention one with
// owner=true. It blocks until one is logged; a cancelled request ends without an answer (see SCENARIOS WA-2).
func wait(s *Server, w http.ResponseWriter, r *http.Request) {
	ctx, src := r.Context(), s.Source()
	q := r.URL.Query()
	ownerOnly := false
	if raw := q.Get("owner"); raw != "" {
		v, err := strconv.ParseBool(raw)
		if err != nil {
			WriteError(w, http.StatusBadRequest, wire.CodeBadRequest,
				fmt.Sprintf("owner=%q is no boolean; pass owner=true or leave it out", raw))
			return
		}
		ownerOnly = v
	}
	changed := src.Changed()
	last, err := cursor(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
		return
	}
	if q.Get("after") == "" && r.Header.Get("Last-Event-ID") == "" {
		if last, err = src.LastEventSeq(ctx); err != nil {
			writeOwnerError(s, w, r, fmt.Errorf("read the last event: %w", err))
			return
		}
	}
	for {
		events, err := src.EventsAfter(ctx, last, eventBatch)
		if err != nil {
			if ctx.Err() == nil {
				writeOwnerError(s, w, r, fmt.Errorf("read events after %d: %w", last, err))
			}
			return
		}
		for _, ev := range events {
			if !ownerOnly || owner.NeedsAttention(ev) {
				WriteJSON(w, http.StatusOK, eventDTO(ev))
				return
			}
			last = ev.Seq
		}
		if len(events) == eventBatch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
			changed = src.Changed()
		}
	}
}
