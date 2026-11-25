package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/owner"
	"github.com/polandy/fanin/internal/store"
)

// Details of the owner commands' Ack.
const (
	detailGo            = "released"
	detailLifted        = "lifted"
	detailPaused        = "paused before the next worker"
	detailAlreadyPaused = "already paused"
	detailResumed       = "resumed"
)

func init() { RegisterBuiltin(registerOwner) }

func registerOwner(r *Registry) {
	cmd := func(pattern string, fn func(s *Server, r *http.Request) (wire.Ack, error)) {
		r.Add(Route{Pattern: pattern, Handler: func(s *Server) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.cfg.Owner == nil {
					WriteError(w, http.StatusInternalServerError, wire.CodeInternal,
						"fanind runs without the owner channel; build api.Config with Owner")
					return
				}
				ack, err := fn(s, r)
				if err != nil {
					writeOwnerError(s, w, r, err)
					return
				}
				ack.Status = wire.StatusOK
				WriteJSON(w, http.StatusOK, ack)
			})
		}})
	}
	cmd("POST /api/v1/tasks/{id}/answer", answer)
	cmd("POST /api/v1/tasks/{id}/go", func(s *Server, r *http.Request) (wire.Ack, error) {
		return wire.Ack{Detail: detailGo}, s.cfg.Owner.Go(r.Context(), model.TaskID(r.PathValue("id")))
	})
	cmd("POST /api/v1/holds/{id}/lift", func(s *Server, r *http.Request) (wire.Ack, error) {
		return wire.Ack{Detail: detailLifted}, s.cfg.Owner.LiftHold(r.Context(), r.PathValue("id"))
	})
	cmd("POST /api/v1/projects/{p}/pause", func(s *Server, r *http.Request) (wire.Ack, error) {
		p, err := s.project(r)
		if err != nil {
			return wire.Ack{}, err
		}
		ok, err := s.cfg.Owner.Pause(r.Context(), p)
		if !ok {
			return wire.Ack{Detail: detailAlreadyPaused}, err
		}
		return wire.Ack{Detail: detailPaused}, err
	})
	cmd("POST /api/v1/projects/{p}/resume", func(s *Server, r *http.Request) (wire.Ack, error) {
		p, err := s.project(r)
		if err != nil {
			return wire.Ack{}, err
		}
		return wire.Ack{Detail: detailResumed}, s.cfg.Owner.Resume(r.Context(), p)
	})
	r.Add(Route{Pattern: "GET /api/v1/questions", Handler: func(s *Server) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			qs, err := s.Source().OpenQuestions(r.Context())
			if err != nil {
				writeOwnerError(s, w, r, err)
				return
			}
			out := wire.Questions{Questions: []wire.Question{}}
			for _, q := range qs {
				out.Questions = append(out.Questions, questionDTO(q))
			}
			WriteJSON(w, http.StatusOK, out)
		})
	}})
}

// errUnknownProject is a project the config does not name.
var errUnknownProject = errors.New("api: unknown project")

func (s *Server) project(r *http.Request) (string, error) {
	p := r.PathValue("p")
	if !slices.Contains(s.Projects(), p) {
		return "", fmt.Errorf("%w: %q; configured: %s", errUnknownProject, p, strings.Join(s.Projects(), ", "))
	}
	return p, nil
}

func answer(s *Server, r *http.Request) (wire.Ack, error) {
	var req wire.AnswerRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		return wire.Ack{}, &bodyError{err: err}
	}
	d, err := s.cfg.Owner.Answer(r.Context(), model.TaskID(r.PathValue("id")), req.Answer)
	if err != nil {
		return wire.Ack{}, err
	}
	if d.Option > 0 {
		return wire.Ack{Detail: fmt.Sprintf("option %d: %s", d.Option, d.Text)}, nil
	}
	return wire.Ack{Detail: d.Text}, nil
}

// bodyError is a request body that is no JSON of the expected shape.
type bodyError struct{ err error }

func (e *bodyError) Error() string {
	return fmt.Sprintf("the body is no JSON {\"answer\": \"<option number or text>\"}: %v", e.err)
}

func (e *bodyError) Unwrap() error { return e.err }

// writeOwnerError maps an owner command's error to its status; anything unknown is a 500, never a success.
func writeOwnerError(s *Server, w http.ResponseWriter, r *http.Request, err error) {
	var (
		tooLarge *http.MaxBytesError
		badBody  *bodyError
	)
	switch {
	case errors.As(err, &tooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, wire.CodeTooLarge,
			fmt.Sprintf("the body is over %d bytes; send a shorter answer", MaxBodyBytes))
	case errors.As(err, &badBody), errors.Is(err, owner.ErrInvalidAnswer), errors.Is(err, store.ErrInvalid),
		errors.Is(err, model.ErrInvalidQuestion):
		WriteError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
	case errors.Is(err, store.ErrNotFound), errors.Is(err, errUnknownProject):
		WriteError(w, http.StatusNotFound, wire.CodeNotFound, err.Error())
	case errors.Is(err, owner.ErrNotReady), errors.Is(err, model.ErrTransition):
		WriteError(w, http.StatusConflict, wire.CodeConflict, err.Error())
	case errors.Is(err, owner.ErrDelivery):
		WriteError(w, http.StatusBadGateway, wire.CodeDeliveryFailed, err.Error())
	default:
		s.Logger().ErrorContext(r.Context(), "api: owner command", "path", r.URL.Path, "err", err)
		WriteError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
	}
}
