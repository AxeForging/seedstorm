package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
)

// maxRunBody bounds a job request: room for an export document as large as
// the output limit, plus its JSON framing.
func maxRunBody() int { return webOutputLimit + 1<<20 }

// startRun is a helper that decodes a JSON body into req, requires an active
// session, kicks off a job that delegates to runner, and returns the job ID.
func startRun[T any](
	s *Server,
	w http.ResponseWriter,
	r *http.Request,
	jobName string,
	runner func(ctx context.Context, sess *Session, req T, jc JobControl) (map[string]any, error),
) {
	startGuardedRun(s, w, r, jobName, runner, nil)
}

// startGuardedRun is startRun with a check that runs before the job exists:
// a production refusal answers 409 and nothing starts.
func startGuardedRun[T any](
	s *Server,
	w http.ResponseWriter,
	r *http.Request,
	jobName string,
	runner func(ctx context.Context, sess *Session, req T, jc JobControl) (map[string]any, error),
	guard func(req T, sess *Session) *productionRefusal,
) {
	if r.Method != http.MethodPost {
		writeError(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req T
	if r.ContentLength != 0 {
		// Bound what one request may hold in memory (export sends its data inline).
		body := http.MaxBytesReader(w, r.Body, int64(maxRunBody()))
		if err := json.NewDecoder(body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			var tooLarge *http.MaxBytesError
			if errors.As(err, &tooLarge) {
				writeError(w, http.StatusRequestEntityTooLarge, fmt.Sprintf("request larger than %dMB", maxRunBody()>>20))
				return
			}
			writeError(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
			return
		}
	}
	sess, err := s.sessions.fromRequest(r)
	if err != nil {
		writeError(w, http.StatusUnauthorized, err.Error())
		return
	}
	if guard != nil {
		if refusal := guard(req, sess); refusal != nil {
			writeProductionRefusal(w, refusal)
			return
		}
	}
	job := s.jobs.StartFor(context.Background(), sess.ID, jobName, func(ctx context.Context, jc JobControl) (map[string]any, error) {
		return runner(ctx, sess, req, jc)
	})
	view := jobView(job)
	view["bootId"] = s.bootID
	writeJSON(w, http.StatusAccepted, view)
}

func (s *Server) handleSeedRun(w http.ResponseWriter, r *http.Request) {
	startGuardedRun(s, w, r, "seed", s.runSeed, func(req SeedRequest, sess *Session) *productionRefusal {
		if req.DryRun {
			return nil
		}
		return s.guardProduction(sessionTarget(sess), req.ConfirmProduction, "seed it")
	})
}

func (s *Server) handleGapsRun(w http.ResponseWriter, r *http.Request) {
	startGuardedRun(s, w, r, "gaps", s.runGaps, func(req GapsRequest, sess *Session) *productionRefusal {
		if !req.Fill || req.DryRun {
			return nil
		}
		return s.guardProduction(sessionTarget(sess), req.ConfirmProduction, "fill its empty tables")
	})
}

func (s *Server) handleGenerateRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "generate", s.runGenerate)
}

func (s *Server) handleEnrichRun(w http.ResponseWriter, r *http.Request) {
	if os.Getenv("GEMINI_API_KEY") == "" {
		writeError(w, http.StatusBadRequest, "GEMINI_API_KEY env var must be set on the seedstorm server")
		return
	}
	startRun(s, w, r, "ai-enrich", s.runEnrich)
}

func (s *Server) handleExportRun(w http.ResponseWriter, r *http.Request) {
	startRun(s, w, r, "export", s.runExport)
}

func (s *Server) handleCloneSchemaRun(w http.ResponseWriter, r *http.Request) {
	startGuardedRun(s, w, r, "clone-schema", s.runCloneSchema, func(req CloneSchemaRequest, sess *Session) *productionRefusal {
		if req.DryRun {
			return nil
		}
		target := s.refTarget(ConnRef{ID: req.TargetID, SavedID: req.TargetSavedID})
		if req.TargetID == "" && req.TargetSavedID == "" {
			target = connectionTarget{Info: req.Target}
			if strings.TrimSpace(req.TargetDSN) != "" {
				if _, _, info, err := buildRawDSN(req.Target.DBType, req.TargetDSN, req.Target.Params); err == nil {
					target.Info = info
				}
			}
		}
		return s.guardProduction(target, req.ConfirmProduction, "clone a schema into it")
	})
}
