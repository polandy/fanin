package api

import (
	"net/http"

	"github.com/polandy/fanin/internal/api/wire"
)

func init() { RegisterBuiltin(registerHealth) }

func registerHealth(r *Registry) {
	r.Add(Route{Pattern: "GET /api/v1/health", Handler: func(*Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			WriteJSON(w, http.StatusOK, wire.Health{Status: wire.StatusOK})
		})
	}})
}
