package service

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	mcevent "github.com/muidea/magicCommon/event"
	engine "github.com/muidea/magicEngine/http"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func (s *Service) configureDocuments() {
	if s.workspace == nil {
		return
	}
	cfg := s.config.Documents
	opts := documentflow.Options{RuntimePath: s.workspace.RuntimePath, Jobs: jobstore.New(s.workspace.JobsPath), Enabled: cfg.Enabled, Families: cfg.Families, AllowManual: cfg.AllowManual, SemanticRequired: true, ReviewTimeout: time.Duration(cfg.ReviewTimeoutSeconds) * time.Second, MaxRepairAttempts: cfg.MaxRepairAttempts, Concurrency: cfg.Concurrency}
	if cfg.SemanticReview && s.config.LLM.APIKey != "" {
		provider := ai.NewOpenAICompatibleProvider(s.config.LLM, appconfig.EmbeddingConfig{})
		opts.Reviewer = provider
		opts.Repairer = provider
	}
	opts.Projected = func(id string) {
		if s.events == nil {
			return
		}
		domain := models.DomainEvent{EventType: models.EventThoughtPatched, SourceUnit: "document", OccurredAt: time.Now().UTC(), WorkspaceID: s.workspace.ID, ResourceType: models.ResourceTypeThought, ResourceID: id, PayloadVersion: 1, Payload: map[string]any{"thought_id": id}}
		s.events.Post(mcevent.NewEvent(domain.EventType, "document", "#", mcevent.NewHeader(), domain))
	}
	opts.Commit = func(paths []string) {
		if s.events == nil {
			return
		}
		domain := models.DomainEvent{EventType: models.EventGitCommitRequested, SourceUnit: "document", OccurredAt: time.Now().UTC(), WorkspaceID: s.workspace.ID, ResourceType: "document", PayloadVersion: 1, Payload: models.GitCommitRequestedPayload{Paths: paths, Reason: "document"}}
		s.events.Post(mcevent.NewEvent(domain.EventType, "document", "#", mcevent.NewHeader(), domain))
	}
	opts.Emit = func(kind string, payload map[string]any) {
		if s.events == nil {
			return
		}
		domain := models.DomainEvent{EventType: kind, SourceUnit: "document", OccurredAt: time.Now().UTC(), WorkspaceID: s.workspace.ID, ResourceType: "document", ResourceID: payload["document_id"].(string), PayloadVersion: 1, Payload: payload}
		s.events.Post(mcevent.NewEvent(kind, "document", "#", mcevent.NewHeader(), domain))
	}
	opts.SourceHash = func(source models.DocumentSource) (string, error) {
		if source.Type != "thought" {
			return source.Hash, nil
		}
		_, content, e := markdown.ReadThought(s.workspace.RootPath, source.ResourceID)
		if e != nil {
			return "", e
		}
		return documentstore.HashJSON(content), nil
	}
	opts.SourceHashUnderLease = func(source models.DocumentSource) (string, error) {
		if source.Type != "thought" {
			return source.Hash, nil
		}
		raw, err := markdown.ReadThoughtRawUnderLease(s.workspace.RootPath, source.ResourceID)
		if err != nil {
			return "", err
		}
		_, content := markdown.ParseThought(raw)
		return documentstore.HashJSON(content), nil
	}
	s.documents = documentflow.Configure(s.workspace.RootPath, s.documentProfiles, opts)
}
func (s *Service) registerDocumentRoutes() {
	s.registry.AddHandler("/api/documents/:id", engine.GET, s.handleDocument)
	s.registry.AddHandler("/api/documents/:id/revisions", engine.POST, s.handleDocument)
	s.registry.AddHandler("/api/documents/:id/validate", engine.POST, s.handleDocument)
	s.registry.AddHandler("/api/documents/:id/validation-reports/:report_id", engine.GET, s.handleDocument)
	s.registry.AddHandler("/api/documents/:id/repair", engine.POST, s.handleDocument)
	s.registry.AddHandler("/api/documents/:id/finalize", engine.POST, s.handleDocument)
	s.registry.AddHandler("/api/documents/:id/revisions/:revision_id/export", engine.GET, s.handleDocument)
	s.registry.AddHandler("/p/documents/:id/revisions/:revision_id", engine.GET, s.handlePublicDocument)
}
func documentError(w http.ResponseWriter, r *http.Request, e error) {
	status := http.StatusInternalServerError
	code := "thoughtflow.document.failed"
	switch {
	case errors.Is(e, os.ErrNotExist):
		status = 404
		code = "thoughtflow.document.not_found"
	case errors.Is(e, documentstore.ErrConflict):
		status = 409
		code = "thoughtflow.document.conflict"
	case errors.Is(e, documentstore.ErrInvalid):
		status = 400
		code = "thoughtflow.document.invalid"
	case errors.Is(e, documentflow.ErrRules):
		status = 422
		code = "thoughtflow.document.rules"
	case errors.Is(e, documentflow.ErrUnavailable):
		status = 503
		code = "thoughtflow.document.unavailable"
	case errors.Is(e, context.Canceled) || errors.Is(e, context.DeadlineExceeded):
		status = 503
		code = "thoughtflow.document.unavailable"
	}
	writeError(w, r, status, code, e.Error())
}
func documentGeneration(w http.ResponseWriter, r *http.Request) (uint64, bool) {
	value := strings.Trim(r.Header.Get("If-Match"), `"`)
	if value == "" {
		writeError(w, r, 428, "thoughtflow.document.precondition", "If-Match generation is required")
		return 0, false
	}
	v, e := strconv.ParseUint(value, 10, 64)
	if e != nil {
		documentError(w, r, documentstore.ErrInvalid)
		return 0, false
	}
	return v, true
}
func (s *Service) ensureDocument(ctx context.Context, id string) error {
	snapshot, e := s.captureService.GetThought(ctx, id)
	if e != nil {
		return e
	}
	if snapshot.Thought.DocumentProfile == nil && snapshot.Thought.Document == nil {
		return os.ErrNotExist
	}
	_, e = s.documents.Store.Head(id)
	if os.IsNotExist(e) {
		e = markdown.WriteThought(s.workspace.RootPath, snapshot.Thought, snapshot.Content)
	}
	return e
}
func (s *Service) syncDocumentProjection(ctx context.Context, id string) error {
	_, e := s.captureService.GetThought(ctx, id)
	return e
}
func (s *Service) handleDocument(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.documents == nil {
		documentError(w, r, documentflow.ErrUnavailable)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/api/documents/"), "/")
	id := parts[0]
	if !documentstore.ValidID(id) {
		documentError(w, r, documentstore.ErrInvalid)
		return
	}
	if e := s.documents.Store.CheckActive(id); e != nil {
		documentError(w, r, e)
		return
	}
	// Immutable exports and reports remain readable if the compatibility projection failed.
	if len(parts) == 4 && parts[1] == "revisions" && parts[3] == "export" {
		s.writeDocumentExport(w, r, id, parts[2], r.URL.Query().Get("mode"))
		return
	}
	if len(parts) == 3 && parts[1] == "validation-reports" {
		out, e := s.documents.Store.Report(id, parts[2])
		if e != nil {
			documentError(w, r, e)
			return
		}
		writeJSON(w, r, 200, out)
		return
	}
	if e := s.ensureDocument(ctx, id); e != nil {
		if _, headErr := s.documents.Store.Head(id); headErr != nil || !((len(parts) == 1 && r.Method == http.MethodGet) || (len(parts) == 2 && (parts[1] == "validate" || parts[1] == "finalize"))) {
			documentError(w, r, e)
			return
		}
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		out, e := s.documents.Get(id)
		if e != nil {
			documentError(w, r, e)
			return
		}
		w.Header().Set("ETag", `"`+strconv.FormatUint(out.Head.Generation, 10)+`"`)
		writeJSON(w, r, 200, out)
		return
	}
	generation, ok := documentGeneration(w, r)
	if !ok {
		return
	}
	if len(parts) != 2 {
		documentError(w, r, documentstore.ErrInvalid)
		return
	}
	switch parts[1] {
	case "revisions":
		var req struct {
			Body     *string                  `json:"body"`
			Contract *models.DocumentContract `json:"contract"`
			Sources  *models.SourceManifest   `json:"source_manifest"`
			Evidence map[string]string        `json:"evidence"`
		}
		if e := decodeJSONBody(r, &req); e != nil {
			documentError(w, r, documentstore.ErrInvalid)
			return
		}
		h, e := s.documents.Store.Head(id)
		if e != nil {
			documentError(w, r, e)
			return
		}
		d, e := s.documents.Store.Revision(id, h.WorkingRevisionID)
		if e != nil {
			documentError(w, r, e)
			return
		}
		if req.Body != nil {
			d.Body = *req.Body
		}
		if req.Contract != nil {
			d.Contract = *req.Contract
			if d.Contract.ProfileRef != hProfile(s, id, h.WorkingRevisionID) {
				if s.documentProfiles == nil {
					documentError(w, r, documentflow.ErrUnavailable)
					return
				}
				p, e := s.documentProfiles.Resolve(d.Contract.ProfileRef)
				if e != nil {
					documentError(w, r, e)
					return
				}
				d.Profile = p.Snapshot
			}
		}
		if req.Sources != nil {
			d.Sources = *req.Sources
		}
		if e = s.documents.SaveEvidence(id, &d.Sources, req.Evidence); e != nil {
			documentError(w, r, e)
			return
		}
		d.Revision.Origin = "editor"
		d.Revision.RepairCount = 0
		out, e := s.documents.CreateRevision(id, generation, d)
		if e != nil {
			documentError(w, r, e)
			return
		}
		if e = s.syncDocumentProjection(ctx, id); e != nil {
			documentError(w, r, e)
			return
		}
		out, e = s.documents.Get(id)
		if e != nil {
			documentError(w, r, e)
			return
		}
		writeJSON(w, r, 201, out)
	case "validate":
		var req struct {
			RevisionID  string `json:"revision_id"`
			ReviewMode  string `json:"review_mode"`
			OperationID string `json:"operation_id"`
		}
		if e := decodeJSONBody(r, &req); e != nil {
			documentError(w, r, documentstore.ErrInvalid)
			return
		}
		out, e := s.documents.Validate(ctx, id, req.RevisionID, generation, req.ReviewMode, req.OperationID)
		if e != nil {
			documentError(w, r, e)
			return
		}
		writeJSON(w, r, 200, out)
	case "finalize":
		var req documentflow.FinalizeRequest
		if e := decodeJSONBody(r, &req); e != nil {
			documentError(w, r, documentstore.ErrInvalid)
			return
		}
		out, e := s.documents.Finalize(id, generation, req)
		if e != nil {
			documentError(w, r, e)
			return
		}
		writeJSON(w, r, 200, out)
	case "repair":
		var req struct {
			RevisionID string   `json:"revision_id"`
			ReportID   string   `json:"report_id"`
			IssueIDs   []string `json:"issue_ids"`
			Sections   []string `json:"sections"`
		}
		if e := decodeJSONBody(r, &req); e != nil {
			documentError(w, r, documentstore.ErrInvalid)
			return
		}
		out, e := s.documents.Repair(ctx, id, req.RevisionID, req.ReportID, generation, req.IssueIDs, req.Sections)
		if e != nil {
			documentError(w, r, e)
			return
		}
		if e = s.syncDocumentProjection(ctx, id); e != nil {
			documentError(w, r, e)
			return
		}
		out, e = s.documents.Get(id)
		if e != nil {
			documentError(w, r, e)
			return
		}
		writeJSON(w, r, 201, out)
	default:
		documentError(w, r, documentstore.ErrInvalid)
	}
}
func hProfile(s *Service, id, rev string) models.DocumentProfileRef {
	d, _ := s.documents.Store.Revision(id, rev)
	return d.Contract.ProfileRef
}
func (s *Service) writeDocumentExport(w http.ResponseWriter, r *http.Request, id, rev, mode string) {
	body, e := s.documents.Export(id, rev, mode)
	if e != nil {
		documentError(w, r, e)
		return
	}
	w.Header().Set("Content-Type", "text/markdown; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+id+"-"+rev+"-"+mode+`.md"`)
	w.Header().Set("X-Document-Revision", rev)
	w.Header().Set("X-Document-Mode", mode)
	w.Write([]byte(body))
}
func (s *Service) handlePublicDocument(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if !s.runtimeConfig().Server.PublicThoughtsEnabled || s.documents == nil {
		http.NotFound(w, r)
		return
	}
	parts := strings.Split(strings.TrimPrefix(r.URL.Path, "/p/documents/"), "/")
	if len(parts) != 3 || parts[1] != "revisions" {
		http.NotFound(w, r)
		return
	}
	s.writeDocumentExport(w, r, parts[0], parts[2], "final")
}
