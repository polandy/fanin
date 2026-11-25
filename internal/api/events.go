package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/model"
)

// KeepAliveEvery is how often an idle stream gets a comment line, so proxies keep the connection open.
const KeepAliveEvery = 25 * time.Second

// eventBatch is how many events one store read returns.
const eventBatch = 100

func init() { RegisterBuiltin(registerEvents) }

func registerEvents(r *Registry) {
	r.Add(Route{Pattern: "GET /api/v1/events", Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { streamEvents(s, w, r) })
	}})
}

// cursor reads the resume point: ?after=N, else the Last-Event-ID header, else 0 (the whole log).
func cursor(r *http.Request) (int64, error) {
	raw := r.URL.Query().Get("after")
	if raw == "" {
		raw = r.Header.Get("Last-Event-ID")
	}
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("cursor %q is not an event number; pass after=<seq> with seq >= 0", raw)
	}
	return n, nil
}

func streamEvents(s *Server, w http.ResponseWriter, r *http.Request) {
	last, err := cursor(r)
	if err != nil {
		WriteError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
		return
	}
	flusher, ok := w.(http.Flusher)
	if !ok {
		WriteError(w, http.StatusInternalServerError, wire.CodeInternal, "the connection cannot stream; serve the API over net/http")
		return
	}
	ctx, src := r.Context(), s.Source()
	changed := src.Changed()
	keepAlive := s.Clock().After(KeepAliveEvery)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flusher.Flush()
	for {
		events, err := src.EventsAfter(ctx, last, eventBatch)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			s.Logger().ErrorContext(ctx, "api: read events", "after", last, "err", err)
			return
		}
		for _, ev := range events {
			if err := writeEvent(w, ev); err != nil {
				return
			}
			last = ev.Seq
		}
		if len(events) > 0 {
			flusher.Flush()
		}
		if len(events) == eventBatch {
			continue
		}
		select {
		case <-ctx.Done():
			return
		case <-changed:
			changed = src.Changed()
		case <-keepAlive:
			if _, err := fmt.Fprint(w, ": keepalive\n\n"); err != nil {
				return
			}
			flusher.Flush()
			keepAlive = s.Clock().After(KeepAliveEvery)
		}
	}
}

func writeEvent(w http.ResponseWriter, ev model.Event) error {
	data, err := json.Marshal(eventDTO(ev))
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "id: %d\nevent: %s\ndata: %s\n\n", ev.Seq, ev.Type, data)
	if err != nil {
		return fmt.Errorf("write event %d: %w", ev.Seq, err)
	}
	return nil
}
