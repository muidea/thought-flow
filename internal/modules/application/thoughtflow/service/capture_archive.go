package service

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"thoughtflow/internal/pkg/ai"
	"time"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/scratchpad"
)

func captureArchiveError(w http.ResponseWriter, r *http.Request, err error) {
	status, code := 500, "thoughtflow.archive.failed"
	if errors.Is(err, capturebiz.ErrCaptureParameters) || errors.Is(err, documentstore.ErrInvalid) {
		status, code = 400, "thoughtflow.capture.invalid_request"
	}
	if errors.Is(err, scratchpad.ErrConflict) || errors.Is(err, capturebiz.ErrArchivePreviewStale) || errors.Is(err, documentstore.ErrConflict) {
		status = 409
		code = "thoughtflow.archive.preview_stale"
	}
	if errors.Is(err, capturebiz.ErrArchiveFormatInvalid) {
		status = 422
		code = "thoughtflow.archive.format_invalid"
	}
	if errors.Is(err, capturebiz.ErrArchivePreviewRequired) {
		status = 409
		code = "thoughtflow.archive.preview_required"
	}
	writeError(w, r, status, code, err.Error())
}

func (s *Service) handleCaptureParameters(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.scratchpadSvc == nil {
		writeError(w, r, 503, "thoughtflow.capture.unavailable", "capture unavailable")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/capture/sessions/"), "/parameters")
	generation, ok := documentGeneration(w, r)
	if !ok {
		return
	}
	var body struct {
		SourceLinks *[]string         `json:"source_links"`
		Values      map[string]string `json:"values"`
		Resolutions map[string]string `json:"resolutions"`
	}
	if err := decodeJSONBody(r, &body); err != nil {
		writeError(w, r, 400, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var links [][]string
	if body.SourceLinks != nil {
		links = append(links, *body.SourceLinks)
	}
	sp, err := s.scratchpadSvc.PatchParameters(id, generation, body.Values, body.Resolutions, links...)
	if err != nil {
		captureArchiveError(w, r, err)
		return
	}
	writeJSON(w, r, 200, sp)
}

func (s *Service) handleCaptureDraft(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.scratchpadSvc == nil {
		writeError(w, r, 503, "thoughtflow.capture.unavailable", "capture unavailable")
		return
	}
	id := strings.TrimSuffix(strings.TrimPrefix(r.URL.Path, "/api/capture/sessions/"), "/archive/draft")
	result, err := s.scratchpadSvc.SaveDraft(ctx, id)
	if err != nil {
		captureArchiveError(w, r, err)
		return
	}
	sp, err := s.scratchpad.Get(id)
	if err != nil {
		captureArchiveError(w, r, err)
		return
	}
	writeJSON(w, r, 200, map[string]any{"draft_thought_id": result.Thought.ID, "scratchpad": sp})
}

func (s *Service) startCaptureArchive(sp scratchpad.Scratchpad) error {
	if sp.Operation == nil {
		return capturebiz.ErrArchivePreviewRequired
	}
	key := sp.SessionID + ":" + sp.Operation.ID
	s.archiveMu.Lock()
	if s.archiveClosing {
		s.archiveMu.Unlock()
		return context.Canceled
	}
	if s.archiveRunning == nil {
		s.archiveRunning = map[string]bool{}
	}
	if s.archiveRunning[key] {
		s.archiveMu.Unlock()
		return nil
	}
	s.archiveRunning[key] = true
	s.archiveWG.Add(1)
	s.archiveMu.Unlock()
	finish := func() { s.archiveMu.Lock(); delete(s.archiveRunning, key); s.archiveMu.Unlock(); s.archiveWG.Done() }
	work := func() {
		defer finish()
		defer func() {
			if problem := recover(); problem != nil {
				s.scratchpadSvc.FailOperation(sp.SessionID, sp.Operation.ID, fmt.Errorf("capture archive worker panic: %v", problem))
			}
		}()
		base := s.shutdownCtx
		if base == nil {
			base = context.Background()
		}
		timeout := s.config.LLM.Timeout
		if timeout <= 0 {
			timeout = 2 * time.Minute
		}
		ctx, cancel := context.WithTimeout(base, timeout)
		ctx = ai.WithGenerationScope(ctx, sp.Operation.ID)
		defer cancel()
		var err error
		if sp.SaveCommand != nil && sp.SaveCommand.State != "completed" {
			if sp.SaveCommand.Mode == "draft" {
				_, err = s.scratchpadSvc.SaveDraft(ctx, sp.SessionID)
			} else {
				_, err = s.scratchpadSvc.Commit(ctx, sp.SessionID)
			}
		} else if sp.Operation.State == "ready" && sp.Operation.AutoCommit {
			_, err = s.scratchpadSvc.Commit(ctx, sp.SessionID)
		} else {
			err = s.scratchpadSvc.PreviewAndArchive(ctx, sp)
		}
		if err != nil {
			s.scratchpadSvc.FailOperation(sp.SessionID, sp.Operation.ID, err)
		}
	}
	if s.background == nil {
		work()
		return nil
	} // deterministic embedded/test use
	if err := s.background.AsyncFunction(work); err != nil {
		finish()
		s.scratchpadSvc.FailOperation(sp.SessionID, sp.Operation.ID, err)
		return err
	}
	return nil
}

func (s *Service) resumeCaptureArchives() {
	if s.scratchpad == nil || s.scratchpadSvc == nil {
		return
	}
	for _, summary := range s.scratchpad.List() {
		sp, err := s.scratchpad.Get(summary.SessionID)
		if err != nil {
			continue
		}
		if sp.Operation == nil && sp.ArchiveIntent == scratchpad.ArchiveIntentLLM && strings.TrimSpace(sp.Content) != "" {
			sp, err = s.scratchpadSvc.BeginPreview(sp.SessionID, true)
		}
		if err != nil || sp.Operation == nil {
			continue
		}
		if sp.Operation.GenerationStarted && (sp.SaveCommand == nil || sp.SaveCommand.State == "completed" || sp.SaveCommand.OperationID != sp.Operation.ID) && (sp.Operation.State == "preparing" || sp.Operation.State == "interrupted" || sp.Operation.State == "saving") {
			s.scratchpadSvc.FailOperation(sp.SessionID, sp.Operation.ID, fmt.Errorf("model execution completion is unknown; explicit retry required"))
			continue
		}
		if (sp.SaveCommand != nil && sp.SaveCommand.State != "completed" && sp.Operation.State != "conflicted") || sp.Operation.State == "interrupted" || sp.Operation.State == "preparing" || sp.Operation.State == "saving" || (sp.Operation.State == "ready" && sp.Operation.AutoCommit) {
			_ = s.startCaptureArchive(sp)
		}
	}
}
