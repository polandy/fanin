package api

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/intake"
	"github.com/polandy/fanin/internal/model"
)

func init() { RegisterBuiltin(registerTasks) }

// Intake queues task requests (internal/intake).
type Intake interface {
	Enqueue(ctx context.Context, r intake.TaskRequest) (model.Task, error)
}

func registerTasks(r *Registry) {
	r.Add(Route{Pattern: "POST /api/v1/tasks", Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if s.cfg.Intake == nil {
				WriteError(w, http.StatusInternalServerError, wire.CodeInternal,
					"fanind runs without intake; build api.Config with Intake")
				return
			}
			var req wire.TaskRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				var tooLarge *http.MaxBytesError
				if errors.As(err, &tooLarge) {
					writeTaskError(s, w, r, err)
					return
				}
				WriteError(w, http.StatusBadRequest, wire.CodeBadRequest,
					"the body is no task request JSON; see docs/dev/spec/api.md: "+err.Error())
				return
			}
			after := make([]model.TaskID, 0, len(req.After))
			for _, id := range req.After {
				after = append(after, model.TaskID(id))
			}
			t, err := s.cfg.Intake.Enqueue(r.Context(), intake.TaskRequest{
				ID: model.TaskID(req.ID), Project: req.Project, Task: req.Task, Analyze: req.Analyze,
				Model: req.Model, Worker: req.Worker, After: after, AllowedPaths: req.AllowedPaths,
			})
			if err != nil {
				writeTaskError(s, w, r, err)
				return
			}
			WriteJSON(w, http.StatusCreated, taskDTO(t))
		})
	}})
	r.Add(Route{Pattern: "GET /api/v1/tasks", Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			ts, err := s.Source().OpenTasks(r.Context())
			if err != nil {
				writeOwnerError(s, w, r, err)
				return
			}
			project := r.URL.Query().Get("project")
			out := wire.Tasks{Tasks: []wire.Task{}}
			for _, t := range ts {
				if project == "" || t.Project == project {
					out.Tasks = append(out.Tasks, taskDTO(t))
				}
			}
			WriteJSON(w, http.StatusOK, out)
		})
	}})
}

func writeTaskError(s *Server, w http.ResponseWriter, r *http.Request, err error) {
	var tooLarge *http.MaxBytesError
	switch {
	case errors.As(err, &tooLarge):
		writeOwnerError(s, w, r, err)
	case errors.Is(err, intake.ErrUnknownProject):
		WriteError(w, http.StatusNotFound, wire.CodeNotFound, err.Error())
	case errors.Is(err, intake.ErrExists):
		WriteError(w, http.StatusConflict, wire.CodeConflict, err.Error())
	case errors.Is(err, intake.ErrInvalid):
		WriteError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
	default:
		writeOwnerError(s, w, r, err)
	}
}
