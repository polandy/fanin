package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/polandy/fanin/internal/api/wire"
	"github.com/polandy/fanin/internal/model"
	"github.com/polandy/fanin/internal/report"
	"github.com/polandy/fanin/internal/store"
	"github.com/polandy/fanin/pkg/faninmcp"
)

// Details of the worker routes' Ack.
const (
	detailReported = "recorded"
	detailProgress = "noted"
)

// Reports records worker reports (internal/report); it checks the run token.
type Reports interface {
	Report(ctx context.Context, run model.RunID, token string, res model.Result) error
	Progress(ctx context.Context, run model.RunID, token, text string) error
}

func init() { RegisterBuiltin(registerReport) }

func registerReport(r *Registry) {
	worker := func(pattern string, fn func(s *Server, r *http.Request, token string) (wire.Ack, error)) {
		r.Add(Route{Pattern: pattern, Auth: AuthRun, Handler: func(s *Server) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if s.cfg.Reports == nil {
					WriteError(w, http.StatusInternalServerError, wire.CodeInternal,
						"fanind runs without the report service; build api.Config with Reports")
					return
				}
				ack, err := fn(s, r, bearer(r))
				if err != nil {
					writeReportError(s, w, r, err)
					return
				}
				ack.Status = wire.StatusOK
				WriteJSON(w, http.StatusOK, ack)
			})
		}})
	}
	worker("POST /api/v1/runs/{id}/report", func(s *Server, r *http.Request, token string) (wire.Ack, error) {
		var in faninmcp.Report
		if err := decodeStrict(r.Body, &in); err != nil {
			return wire.Ack{}, &reportBodyError{err: err}
		}
		res, err := report.FromWire(in)
		if err != nil {
			return wire.Ack{}, err
		}
		if err := s.cfg.Reports.Report(r.Context(), model.RunID(r.PathValue("id")), token, res); err != nil {
			return wire.Ack{}, err
		}
		return wire.Ack{Detail: detailReported + " " + string(res.Kind)}, nil
	})
	worker("POST /api/v1/runs/{id}/progress", func(s *Server, r *http.Request, token string) (wire.Ack, error) {
		var in faninmcp.Progress
		if err := decodeStrict(r.Body, &in); err != nil {
			return wire.Ack{}, &reportBodyError{err: err}
		}
		return wire.Ack{Detail: detailProgress},
			s.cfg.Reports.Progress(r.Context(), model.RunID(r.PathValue("id")), token, in.Text)
	})
}

func bearer(r *http.Request) string {
	tok, _ := strings.CutPrefix(r.Header.Get("Authorization"), bearerPrefix)
	return tok
}

// decodeStrict reads exactly one JSON object of known fields.
func decodeStrict(body io.Reader, v any) error {
	raw, err := io.ReadAll(body)
	if err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return errors.New("data after the JSON object")
	}
	return nil
}

// reportBodyError is a worker request body that is no JSON of the contract's shape.
type reportBodyError struct{ err error }

func (e *reportBodyError) Error() string {
	return fmt.Sprintf("the body is no JSON object of the worker contract (docs/dev/spec/worker-contract.md): %v", e.err)
}

func (e *reportBodyError) Unwrap() error { return e.err }

// writeReportError maps a worker route's error to its status; anything unknown is a 500, never a success.
func writeReportError(s *Server, w http.ResponseWriter, r *http.Request, err error) {
	var (
		tooLarge *http.MaxBytesError
		badBody  *reportBodyError
	)
	switch {
	case errors.As(err, &tooLarge):
		WriteError(w, http.StatusRequestEntityTooLarge, wire.CodeTooLarge,
			fmt.Sprintf("the body is over %d bytes; send a shorter report", MaxBodyBytes))
	case errors.As(err, &badBody), errors.Is(err, model.ErrInvalidResult), errors.Is(err, model.ErrUnknownValue):
		WriteError(w, http.StatusBadRequest, wire.CodeBadRequest, err.Error())
	case errors.Is(err, report.ErrToken):
		WriteError(w, http.StatusUnauthorized, wire.CodeUnauthorized, err.Error())
	case errors.Is(err, report.ErrStaleRun):
		WriteError(w, http.StatusConflict, wire.CodeConflict, err.Error())
	case errors.Is(err, store.ErrNotFound):
		WriteError(w, http.StatusNotFound, wire.CodeNotFound, err.Error())
	default:
		s.Logger().ErrorContext(r.Context(), "api: worker report", "path", r.URL.Path, "err", err)
		WriteError(w, http.StatusInternalServerError, wire.CodeInternal, err.Error())
	}
}
