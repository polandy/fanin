package api

import (
	"net/http"

	"github.com/polandy/fanin/internal/api/wire"
)

func init() { RegisterBuiltin(registerMetrics) }

func registerMetrics(r *Registry) {
	r.Add(Route{Pattern: "GET /metrics", Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			h := s.Metrics()
			if h == nil {
				WriteError(w, http.StatusInternalServerError, wire.CodeInternal,
					"no metrics source is configured; set api.Config.Metrics")
				return
			}
			h.ServeHTTP(w, r)
		})
	}})
}
