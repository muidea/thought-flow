package service

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"fmt"
	mcevent "github.com/muidea/magicCommon/event"
	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetflow"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/scratchpad"
)

func (s *Service) configureDocumentSets() {
	if s.workspace == nil {
		return
	}
	opts := documentsetflow.Options{Enabled: s.config.DocumentProfiles.Enabled && s.config.Documents.Enabled, Generator: ai.NewDocumentGenerationProvider(s.config.LLM), ModelHash: documentstore.HashJSON([]string{s.config.LLM.BaseURL, s.config.LLM.ChatModel}), Snapshot: s.captureSetSnapshot, SnapshotUnderLease: func(id string) (ds.CaptureSnapshot, error) { return s.captureSetSnapshotUnderLease(id, true) }}
	if s.config.LLM.APIKey != "" {
		provider := ai.NewOpenAICompatibleProvider(s.config.LLM, s.config.Embedding)
		opts.Planner = provider
		opts.Reviewer = provider
	}
	opts.Emit = func(kind string, payload map[string]any) error {
		if s.events != nil {
			event := models.DomainEvent{EventType: kind, SourceUnit: "document-set", OccurredAt: time.Now().UTC(), WorkspaceID: s.workspace.ID, ResourceType: "document-set", ResourceID: payload["set_id"].(string), PayloadVersion: 1, Payload: payload}
			if thoughtID, ok := payload["thought_id"].(string); ok {
				event.ResourceType = models.ResourceTypeThought
				event.ResourceID = thoughtID
			}
			s.events.Post(mcevent.NewEvent(kind, "document-set", "#", mcevent.NewHeader(), event))
		}
		return nil
	}
	opts.Commit = func(paths []string) error {
		if s.events != nil {
			event := models.DomainEvent{EventType: models.EventGitCommitRequested, SourceUnit: "document-set", OccurredAt: time.Now().UTC(), WorkspaceID: s.workspace.ID, ResourceType: "document-set", PayloadVersion: 1, Payload: models.GitCommitRequestedPayload{Paths: paths, Reason: "document-set"}}
			s.events.Post(mcevent.NewEvent(event.EventType, "document-set", "#", mcevent.NewHeader(), event))
		}
		return nil
	}
	s.documentSets = documentsetflow.New(s.workspace.RootPath, opts)
	for _, e := range s.documentSets.Recover() {
		slog.Warn("document set recovery needs attention", "error", e)
	}
}
func (s *Service) captureSetSnapshot(id string) (ds.CaptureSnapshot, error) {
	return s.captureSetSnapshotUnderLease(id, false)
}
func (s *Service) captureSetSnapshotUnderLease(id string, underLease bool) (ds.CaptureSnapshot, error) {
	if s.scratchpad == nil {
		return ds.CaptureSnapshot{}, ds.ErrUnavailable
	}
	get := s.scratchpad.Get
	if existing, ok := s.scratchpad.(interface {
		GetExisting(string) (scratchpad.Scratchpad, error)
	}); ok {
		get = existing.GetExisting
	}
	sp, e := get(id)
	if e != nil {
		if os.IsNotExist(e) {
			return ds.CaptureSnapshot{}, fmt.Errorf("%w: %v", ds.ErrCaptureMissing, e)
		}
		return ds.CaptureSnapshot{}, e
	}
	if sp.SessionID == "" {
		return ds.CaptureSnapshot{}, ds.ErrCaptureMissing
	}
	c := ds.CaptureSnapshot{SessionID: id, Generation: sp.InputGeneration, ConfirmedFacts: sp.SessionContext.ConfirmedFacts, Sources: models.SourceManifest{SchemaVersion: 1}}
	for _, m := range sp.Messages {
		c.Messages = append(c.Messages, ds.Message{Role: m.Role, Text: m.Text})
	}
	c.ContextHash = documentstore.HashJSON(struct {
		Content  string
		Messages []ds.Message
		Context  any
	}{sp.Content, c.Messages, sp.SessionContext})
	// Freeze approved links without fetching or disclosing private evidence.
	for i, link := range sp.SessionContext.SourceLinks {
		if documentvalidation.SensitiveLocator(link) {
			return c, ds.ErrInvalid
		}
		c.Sources.Sources = append(c.Sources.Sources, models.DocumentSource{ID: fmt.Sprintf("S%d", i+1), Type: "url", Locator: link, Hash: documentstore.Hash([]byte(link)), VerificationStatus: "unverified"})
	}
	for _, thoughtID := range sp.SessionContext.RelatedThoughtIDs {
		var thought models.Thought
		var content models.ThoughtContent
		var err error
		if underLease {
			thought, content, err = markdown.ReadThoughtUnderNamespace(s.workspace.RootPath, thoughtID)
		} else {
			thought, content, err = markdown.ReadThought(s.workspace.RootPath, thoughtID)
		}
		if err != nil {
			if os.IsNotExist(err) {
				return c, fmt.Errorf("%w: %s", ds.ErrSourceMissing, thoughtID)
			}
			return c, err
		}
		c.Sources.Sources = append(c.Sources.Sources, models.DocumentSource{ID: fmt.Sprintf("S%d", len(c.Sources.Sources)+1), Type: "thought", ResourceID: thoughtID, Locator: "thought://" + thoughtID, Hash: documentstore.HashJSON(content), Version: fmt.Sprint(thought.UpdatedAt.UnixNano()), VerificationStatus: "frozen"})
	}
	c.ScopeHash = documentstore.HashJSON([]any{sp.SessionContext.Goal, sp.SessionContext.ConfirmedFacts, sp.SessionContext.DocumentParameters})
	return c, nil
}
func (s *Service) registerDocumentSetRoutes() {
	s.registry.AddHandler("/api/thoughts/:id/document-conversions", "POST", s.handleDocumentConversion)
	s.registry.AddHandler("/api/thoughts/:id/document-conversions", "GET", s.handleDocumentConversion)
	for _, route := range [][2]string{{"GET", "/api/document-set-profiles"}, {"POST", "/api/document-set-profiles"}, {"POST", "/api/capture/sessions/:id/document-set-plans"}, {"POST", "/api/capture/sessions/:id/document-set-markdown-previews"}, {"POST", "/api/capture/sessions/:id/document-set-runs"}, {"GET", "/api/document-set-runs"}, {"GET", "/api/document-set-runs/:id"}, {"POST", "/api/document-set-runs/:id/cancel"}, {"POST", "/api/document-set-runs/:id/retry"}, {"POST", "/api/document-set-runs/:id/recover"}, {"GET", "/api/document-sets/:id"}, {"GET", "/api/document-sets/:id/revisions/:revision"}, {"GET", "/api/document-sets/:id/members/:key/content"}, {"GET", "/api/document-sets/:id/search"}, {"POST", "/api/document-sets/:id/reviews"}, {"POST", "/api/document-sets/:id/finalizations"}, {"POST", "/api/document-sets/:id/exports"}, {"POST", "/api/document-set-imports"}} {
		s.registry.AddHandler(route[1], route[0], s.handleDocumentSet)
	}
}
func setError(w http.ResponseWriter, r *http.Request, e error) {
	status, code := 500, "failed"
	switch {
	case errors.Is(e, ds.ErrPrecondition):
		status, code = 428, "precondition_required"
	case errors.Is(e, ds.ErrConflict), errors.Is(e, ds.ErrSourceMissing), errors.Is(e, objectlocation.ErrConflict), errors.Is(e, documentstore.ErrConflict):
		status, code = 409, "conflict"
	case errors.Is(e, ds.ErrInvalid), errors.Is(e, ds.ErrRules):
		status, code = 422, "validation_blocked"
	case errors.Is(e, ds.ErrBudget):
		status, code = 422, "budget_exceeded"
	case errors.Is(e, ds.ErrUnavailable):
		status, code = 503, "unavailable"
	case errors.Is(e, os.ErrNotExist), errors.Is(e, ds.ErrCaptureMissing):
		status, code = 404, "not_found"
	case errors.Is(e, context.Canceled), errors.Is(e, context.DeadlineExceeded):
		status, code = 503, "interrupted"
	}
	writeError(w, r, status, "thoughtflow.document_set."+code, e.Error())
}
func (s *Service) handleDocumentSet(ctx context.Context, w http.ResponseWriter, r *http.Request) {
	if s.documentSets == nil {
		setError(w, r, ds.ErrUnavailable)
		return
	}
	flow := s.documentSets
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	var result any
	var e error
	status := 200
	if len(parts) < 2 {
		setError(w, r, ds.ErrInvalid)
		return
	}
	switch parts[1] {
	case "document-set-profiles":
		if r.Method == "GET" {
			result = flow.Profiles()
		} else {
			var p ds.Profile
			raw, err := io.ReadAll(io.LimitReader(r.Body, (256<<10)+1))
			e = err
			if e == nil {
				p, e = ds.ParseProfile(raw, s.documentProfiles)
			}
			if e == nil {
				e = flow.PublishProfile(p)
				result = p
				status = 201
			}
		}
	case "capture":
		if len(parts) != 5 {
			e = ds.ErrInvalid
			break
		}
		session := parts[3]
		if parts[4] == "document-set-markdown-previews" {
			var req documentsetflow.MarkdownPreviewRequest
			if e = decodeWorkspace(r, &req); e == nil {
				var plan ds.Plan
				plan, e = flow.Store.Plan(req.SetID, req.PlanID)
				if e == nil && plan.Capture.SessionID != session {
					e = ds.ErrConflict
				}
				if e == nil {
					result, e = flow.PreviewMarkdown(req)
				}
			}
		} else if parts[4] == "document-set-plans" {
			var req documentsetflow.PlanRequest
			if e = decodeWorkspace(r, &req); e == nil {
				result, e = flow.Plan(ctx, session, req)
				if e == nil {
					plan := result.(ds.Plan)
					_, e = s.scratchpad.Update(session, func(sp *scratchpad.Scratchpad) error {
						sp.DocumentSetID = plan.SetID
						sp.DocumentSetPlanID = plan.ID
						return nil
					})
				}
			}
		} else {
			var req documentsetflow.RunRequest
			if e = decodeWorkspace(r, &req); e == nil {
				plan, err := flow.Store.Plan(req.SetID, req.PlanID)
				if err != nil {
					e = err
				} else if plan.Capture.SessionID != session {
					e = ds.ErrConflict
				} else {
					result, e = flow.Start(req)
					status = 202
					if e == nil {
						run := result.(ds.Run)
						_, e = s.scratchpad.Update(session, func(sp *scratchpad.Scratchpad) error {
							if sp.InputGeneration != run.Plan.Capture.Generation || sp.DocumentSetPlanID != run.Plan.ID {
								return ds.ErrConflict
							}
							sp.DocumentSetRunID = run.ID
							return nil
						})
					}
				}
			}
		}
	case "document-set-runs":
		if len(parts) == 2 {
			result, e = flow.Store.Runs()
			break
		}
		id := parts[2]
		if r.Method == "GET" {
			result, e = flow.Store.Run(id)
		} else if len(parts) == 4 && parts[3] == "cancel" {
			result, e = flow.Cancel(id)
		} else if len(parts) == 4 && parts[3] == "recover" {
			result, e = flow.RecoverRun(id)
		} else if len(parts) == 4 && parts[3] == "retry" {
			var req documentsetflow.RetryRequest
			if e = decodeWorkspace(r, &req); e == nil {
				result, e = flow.RetryWithBinding(id, req, func(run ds.Run) error {
					if s.scratchpad == nil {
						return ds.ErrUnavailable
					}
					_, err := s.scratchpad.Update(run.Plan.Capture.SessionID, func(sp *scratchpad.Scratchpad) error {
						if sp.InputGeneration != run.Plan.Capture.Generation || sp.DocumentSetID != run.Plan.SetID || (sp.DocumentSetRunID != id && sp.DocumentSetRunID != run.ID) {
							return ds.ErrConflict
						}
						sp.DocumentSetRunID = run.ID
						return nil
					})
					return err
				})
				status = 202
			}
		} else {
			e = ds.ErrInvalid
		}
	case "document-sets":
		if len(parts) < 3 {
			e = ds.ErrInvalid
			break
		}
		id := parts[2]
		if len(parts) == 3 {
			head, err := flow.Store.Head(id)
			e = err
			if e == nil {
				revisions := []ds.Revision{}
				seen := map[string]bool{}
				for _, start := range []string{head.Working, head.Finalized} {
					for rev := start; rev != "" && !seen[rev]; {
						seen[rev] = true
						v, err := flow.Store.Read(id, rev)
						if err != nil {
							e = err
							break
						}
						revisions = append(revisions, v)
						rev = v.Parent
					}
				}
				result = map[string]any{"head": head, "revisions": revisions}
			}
			break
		}
		switch parts[3] {
		case "revisions":
			if len(parts) == 5 {
				result, e = flow.Store.Read(id, parts[4])
			} else {
				e = ds.ErrInvalid
			}
		case "members":
			if len(parts) == 6 && parts[5] == "content" {
				rev := r.URL.Query().Get("set_revision")
				if rev == "" {
					e = ds.ErrPrecondition
				} else {
					key, err := url.PathUnescape(parts[4])
					if err != nil {
						e = ds.ErrInvalid
					} else {
						result, e = flow.Store.Member(id, rev, key)
					}
				}
			} else {
				e = ds.ErrInvalid
			}
		case "search":
			rev := r.URL.Query().Get("set_revision")
			if rev == "" {
				e = ds.ErrPrecondition
			} else {
				result, e = flow.Search(id, rev, r.URL.Query().Get("q"))
			}
		case "reviews":
			var req documentsetflow.ReviewRequest
			if e = decodeWorkspace(r, &req); e == nil {
				run, err := flow.Store.Run(req.RunID)
				if err != nil {
					e = err
				} else if run.Plan.SetID != id {
					e = ds.ErrConflict
				} else {
					result, e = flow.Review(req)
				}
			}
		case "finalizations":
			var req documentsetflow.FinalizeRequest
			if e = decodeWorkspace(r, &req); e == nil {
				result, e = flow.Finalize(id, req)
			}
		case "exports":
			var req documentsetstore.ExportRequest
			if e = decodeWorkspace(r, &req); e == nil {
				var raw []byte
				raw, e = flow.Store.Export(id, req)
				if e == nil {
					w.Header().Set("Content-Type", "application/zip")
					w.Header().Set("Content-Disposition", `attachment; filename="document-set.zip"`)
					w.Header().Set("Cache-Control", "no-store")
					w.Write(raw)
					return
				}
			}
		default:
			e = ds.ErrInvalid
		}
	case "document-set-imports":
		operation := r.URL.Query().Get("operation_id")
		r.Body = http.MaxBytesReader(w, r.Body, documentsetstore.MaxPackageBytes+1)
		var raw []byte
		raw, e = io.ReadAll(r.Body)
		if e == nil {
			result, e = flow.Store.Import(raw, operation)
			status = 201
		}
	default:
		e = ds.ErrInvalid
	}
	if e != nil {
		setError(w, r, e)
		return
	}
	writeJSON(w, r, status, result)
}

// A session already bound to a set keeps the set archive path. An archive
// intent authorizes generation of working drafts, never automatic finalization.
func (s *Service) archiveDocumentSet(ctx context.Context, sp scratchpad.Scratchpad) error {
	if s.documentSets == nil {
		return ds.ErrUnavailable
	}
	old, e := s.documentSets.Store.Plan(sp.DocumentSetID, sp.DocumentSetPlanID)
	if e != nil {
		return e
	}
	head, e := s.documentSets.Store.HeadOrEmpty(sp.DocumentSetID)
	if e != nil {
		return e
	}
	plan := old
	if old.Capture.Generation != sp.InputGeneration {
		plan, e = s.documentSets.Plan(ctx, sp.SessionID, documentsetflow.PlanRequest{OperationID: sp.Operation.ID, SetID: sp.DocumentSetID, Name: old.Profile.Name, ExpectedGeneration: &head.Generation, CaptureGeneration: &sp.InputGeneration, ProfileID: old.Profile.ID, ProfileVersion: old.Profile.Version, ProfileHash: old.Profile.Hash})
		if e != nil {
			return e
		}
	}
	run, e := s.documentSets.Start(documentsetflow.RunRequest{OperationID: sp.Operation.ID, SetID: plan.SetID, PlanID: plan.ID, PlanHash: plan.Hash, ExpectedGeneration: &plan.BaseGeneration, CaptureGeneration: &plan.Capture.Generation})
	if e != nil {
		return e
	}
	_, e = s.scratchpad.Update(sp.SessionID, func(current *scratchpad.Scratchpad) error {
		if current.InputGeneration != sp.InputGeneration {
			return ds.ErrConflict
		}
		current.DocumentSetID = plan.SetID
		current.DocumentSetPlanID = plan.ID
		current.DocumentSetRunID = run.ID
		if current.Operation != nil && current.Operation.ID == sp.Operation.ID {
			current.Operation.State = "document_set"
		}
		return nil
	})
	return e
}
