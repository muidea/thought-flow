// Package compose implements the Compose running unit. It owns
// the compose draft lifecycle (create → list/get → save-as-thought)
// and is the successor to the legacy synthesis flow. The HTTP
// surface is /api/compose/drafts*; the on-disk store lives in
// internal/pkg/composedraft (workspace/compose/drafts/{id}.yaml).
//
// compose.Service depends on the LLM synthesis provider (the same
// Provider interface used by the refiner module) and on a small
// CaptureSink interface for save-as-thought. The sink indirection
// avoids a circular import with internal/modules/capture; the
// application module wires the concrete capture.Capture sink in at
// startup.
package biz

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/composedraft"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

var errComposeDraftDeleted = errors.New("compose draft was deleted")

const (
	composeSessionID = "compose-session"

	EventComposeDraftCreated = "compose.draft_created"
	EventComposeDraftSaved   = "compose.draft_saved"
	EventComposeDraftDeleted = "compose.draft_deleted"

	// composeJobType is the legacy save-as-thought job label used by
	// SaveDraft. Async generation uses models.JobTypeComposeGenerate.
	composeJobType = "compose_save"
)

// CaptureSink is the small subset of capture.Service that compose
// needs to materialise a draft as a Thought. The interface lives
// here so the compose module can stay decoupled from the capture
// module; the application module wires the concrete implementation
// at startup.
type CaptureSink interface {
	Capture(ctx context.Context, cmd models.CaptureCommand) (models.CaptureResult, error)
}

type Service struct {
	workspace         *models.Workspace
	draftStore        *composedraft.Store
	jobs              *jobstore.Store
	eventHub          event.Hub
	background        task.BackgroundRoutine
	synthesis         ai.SynthesisProvider
	capture           CaptureSink
	now               func() time.Time
	model             string
	profiles          *documentprofile.Registry
	documentGenerator ai.DocumentGenerationProvider
	maxRepairAttempts int

	// inflight guards concurrent CreateDraftAsync calls that share the
	// same request fingerprint so double-clicks do not mint two jobs.
	inflightMu sync.Mutex
	inflight   map[string]models.Job
}

func (s *Service) SetDocumentProfiles(registry *documentprofile.Registry, generator ai.DocumentGenerationProvider, maxRepairAttempts int) {
	s.profiles = registry
	s.documentGenerator = generator
	if maxRepairAttempts >= 0 {
		s.maxRepairAttempts = maxRepairAttempts
	}
}

func NewService(
	workspace *models.Workspace,
	draftStore *composedraft.Store,
	jobs *jobstore.Store,
	eventHub event.Hub,
	background task.BackgroundRoutine,
	synthesis ai.SynthesisProvider,
	capture CaptureSink,
) *Service {
	return &Service{
		workspace:  workspace,
		draftStore: draftStore,
		jobs:       jobs,
		eventHub:   eventHub,
		background: background,
		synthesis:  synthesis,
		capture:    capture,
		now:        func() time.Time { return time.Now().UTC() },
		model:      "local-rule",
		inflight:   map[string]models.Job{},
	}
}

// SetModel lets the application module override the reported model
// string (typically the chat model name from LLM config) so the
// persisted ComposeDraft carries the model that actually generated
// the content.
func (s *Service) SetModel(model string) {
	if model = strings.TrimSpace(model); model != "" {
		s.model = model
	}
}

// CreateDraftAsync queues LLM generation as a background job and returns
// immediately. Identical in-flight requests (same sources/goal/profile/
// format/parameters fingerprint) reuse the existing job so rapid re-clicks
// do not produce duplicate drafts.
func (s *Service) CreateDraftAsync(ctx context.Context, req models.ComposeRequest) (models.Job, error) {
	if s == nil || s.draftStore == nil {
		return models.Job{}, errors.New("compose service is not ready")
	}
	if s.jobs == nil {
		return models.Job{}, errors.New("compose jobstore is not ready")
	}
	if s.synthesis == nil && (s.profiles == nil || s.documentGenerator == nil) {
		return models.Job{}, errors.New("compose synthesis provider is not ready")
	}
	if len(req.Sources) == 0 {
		return models.Job{}, errors.New("sources are required")
	}

	deduped, sourceLinks := dedupeSources(req.Sources)
	fingerprint := composeRequestFingerprint(deduped, req)

	s.inflightMu.Lock()
	if cached, ok := s.inflight[fingerprint]; ok {
		// The map may hold a stale snapshot (status frozen at queue time).
		// Re-read the job store so a just-finished job is not reused.
		if live, err := s.jobs.Get(cached.ID); err == nil && jobActive(live) {
			s.inflight[fingerprint] = live
			s.inflightMu.Unlock()
			return live, nil
		}
		delete(s.inflight, fingerprint)
	}
	if existing, ok := s.findInflightGenerateJob(fingerprint); ok {
		s.inflight[fingerprint] = existing
		s.inflightMu.Unlock()
		return existing, nil
	}

	now := s.now()
	draftID := models.NewJobID("compose", now)
	placeholder := models.ComposeDraft{
		ID:                 draftID,
		Sources:            deduped,
		Goal:               firstNonEmpty(req.Goal, "Compose selected sources"),
		Format:             firstNonEmpty(req.Format, models.ComposeFormatSummary),
		Parameters:         cloneStringMap(req.Parameters),
		Content:            "",
		SourceLinks:        sourceLinks,
		Model:              s.model,
		Status:             models.ComposeStatusGenerating,
		RequestFingerprint: fingerprint,
		GenerationPrompt:   req.Prompt,
		History: []models.ComposeDraftHistory{{
			Status:  models.ComposeStatusGenerating,
			Message: "compose generation queued",
			At:      now,
		}},
		CreatedAt: now,
		UpdatedAt: now,
	}
	if s.profiles != nil {
		if profile, err := s.resolveComposeProfile(req); err == nil {
			ref := profile.Ref
			placeholder.DocumentProfile = &ref
		}
	}
	savedPlaceholder, err := s.draftStore.SaveDraft(ctx, placeholder)
	if err != nil {
		s.inflightMu.Unlock()
		return models.Job{}, err
	}

	job, err := s.jobs.Create(models.JobTypeComposeGenerate, models.ResourceTypeComposeDraft, savedPlaceholder.ID, "compose generation queued")
	if err != nil {
		_ = s.draftStore.Delete(ctx, savedPlaceholder.ID)
		s.inflightMu.Unlock()
		return models.Job{}, err
	}
	savedPlaceholder.JobID = job.ID
	if _, saveErr := s.draftStore.SaveDraft(ctx, savedPlaceholder); saveErr != nil {
		// Job is already queued; keep going so the worker can still finish.
	}
	s.inflight[fingerprint] = job
	s.inflightMu.Unlock()

	eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
	// Surface the generating placeholder so the writing list updates before
	// the LLM round-trip completes.
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      EventComposeDraftCreated,
			SourceUnit:     "compose",
			OccurredAt:     s.now(),
			WorkspaceID:    s.workspaceID(),
			ResourceType:   models.ResourceTypeComposeDraft,
			ResourceID:     savedPlaceholder.ID,
			PayloadVersion: 1,
			Payload:        savedPlaceholder,
		})
	}

	s.scheduleGenerateDraftJob(job, req, fingerprint)
	return job, nil
}

// RecoverGeneratingDrafts re-schedules active compose jobs found on disk.
// Jobs are persisted, but their goroutines are not, so this must run after a
// process restart before a matching request can safely be reused.
func (s *Service) RecoverGeneratingDrafts() (int, error) {
	if s == nil || s.jobs == nil || s.draftStore == nil {
		return 0, errors.New("compose recovery dependencies are not ready")
	}
	jobs, err := s.jobs.List()
	if err != nil {
		return 0, err
	}
	recovered := 0
	for _, job := range jobs {
		if job.Type != models.JobTypeComposeGenerate || !jobActive(job) {
			continue
		}
		draft, err := s.draftStore.GetDraft(context.Background(), job.ResourceID)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return recovered, err
		}
		if draft.Status != models.ComposeStatusGenerating || (draft.JobID != "" && draft.JobID != job.ID) {
			continue
		}
		req := composeRequestFromDraft(draft)
		fingerprint := firstNonEmpty(draft.RequestFingerprint, composeRequestFingerprint(draft.Sources, req))
		s.inflightMu.Lock()
		if current, ok := s.inflight[fingerprint]; ok && current.ID == job.ID {
			s.inflightMu.Unlock()
			continue
		}
		s.inflight[fingerprint] = job
		s.inflightMu.Unlock()
		s.scheduleGenerateDraftJob(job, req, fingerprint)
		recovered++
	}
	return recovered, nil
}

func (s *Service) scheduleGenerateDraftJob(job models.Job, req models.ComposeRequest, fingerprint string) {
	run := func() { s.generateDraftJob(job, req, fingerprint) }
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return
		}
	}
	go run()
}

func (s *Service) generateDraftJob(job models.Job, req models.ComposeRequest, fingerprint string) {
	defer func() {
		s.inflightMu.Lock()
		if current, ok := s.inflight[fingerprint]; ok && current.ID == job.ID {
			delete(s.inflight, fingerprint)
		}
		s.inflightMu.Unlock()
	}()

	// Re-read persisted state before changing it: a delete may have canceled
	// the job after it was queued but before this goroutine was scheduled.
	if live, err := s.jobs.Get(job.ID); err == nil {
		job = live
	}
	if !jobActive(job) {
		return
	}
	if job.Status != models.JobStatusRunning {
		job, _ = s.jobs.MarkRunning(job)
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))

	// Force CreateDraft to update the existing placeholder rather than
	// minting a second draft id for the same request.
	_, err := s.CreateDraft(context.Background(), req, createDraftOptions{
		DraftID:            job.ResourceID,
		RequestFingerprint: fingerprint,
		JobID:              job.ID,
	})
	if err != nil {
		// Draft deleted while generating: cancel the job and stop. Do not
		// recreate a failed placeholder on disk.
		if errors.Is(err, errComposeDraftDeleted) {
			job, _ = s.jobs.MarkCanceled(job, "compose draft deleted")
			eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
			return
		}
		errRef := models.NewErrorRef("thoughtflow.compose.generate_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
		if s.draftStore != nil {
			if existing, loadErr := s.draftStore.GetDraft(context.Background(), job.ResourceID); loadErr == nil {
				now := s.now()
				existing.Status = models.ComposeStatusFailed
				existing.UpdatedAt = now
				existing.JobID = job.ID
				existing.RequestFingerprint = fingerprint
				existing.History = append(existing.History, models.ComposeDraftHistory{
					Status:  models.ComposeStatusFailed,
					Message: err.Error(),
					At:      now,
				})
				// Persist a non-empty body so SaveDraft's content guard
				// still accepts the failed placeholder.
				if strings.TrimSpace(existing.Content) == "" {
					existing.Content = "_Generation failed._\n\n" + err.Error()
				}
				if _, saveErr := s.draftStore.SaveDraft(context.Background(), existing); saveErr == nil && s.eventHub != nil {
					eventutil.Post(s.eventHub, models.DomainEvent{
						EventType:      EventComposeDraftCreated,
						SourceUnit:     "compose",
						OccurredAt:     now,
						WorkspaceID:    s.workspaceID(),
						ResourceType:   models.ResourceTypeComposeDraft,
						ResourceID:     existing.ID,
						PayloadVersion: 1,
						Payload:        existing,
					})
				}
			}
		}
		return
	}
	// A successful draft consumes only the sources that formed this request.
	// Sources queued after submit remain available for the next draft.
	_ = s.removeGeneratedSources(context.Background(), req.Sources)
	job, _ = s.jobs.MarkSucceeded(job, "compose generation succeeded")
	eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
}

func (s *Service) removeGeneratedSources(ctx context.Context, used []models.ComposeSource) error {
	if s == nil || s.draftStore == nil || len(used) == 0 {
		return nil
	}
	basket, err := s.draftStore.GetBasket(ctx)
	if err != nil {
		return err
	}
	usedKeys := make(map[string]struct{}, len(used))
	for _, source := range used {
		if key := composeSourceKey(source); key != "" {
			usedKeys[key] = struct{}{}
		}
	}
	if len(usedKeys) == 0 {
		return nil
	}
	remaining := make([]models.ComposeSource, 0, len(basket.Sources))
	for _, source := range basket.Sources {
		if _, consumed := usedKeys[composeSourceKey(source)]; !consumed {
			remaining = append(remaining, source)
		}
	}
	if len(remaining) == len(basket.Sources) {
		return nil
	}
	_, err = s.draftStore.SaveBasket(ctx, remaining)
	return err
}

type createDraftOptions struct {
	DraftID            string
	RequestFingerprint string
	JobID              string
}

// CreateDraft hydrates the incoming sources into ThoughtSnapshots
// (for thought sources) and context blocks (for search/topic/
// capture sources), calls the LLM synthesis provider, and persists
// the resulting draft to compose/drafts/{id}.yaml.
//
// Sources are deduplicated by (source_type, source_id) and the
// source_links list is unioned across all sources so the saved
// Thought can carry a complete provenance trail.
func (s *Service) CreateDraft(ctx context.Context, req models.ComposeRequest, opts ...createDraftOptions) (models.ComposeDraft, error) {
	if s == nil || s.draftStore == nil {
		return models.ComposeDraft{}, errors.New("compose service is not ready")
	}
	// Document-profile generation can stand in for the free-form
	// synthesis provider; only fail hard when neither path is ready.
	if s.synthesis == nil && (s.profiles == nil || s.documentGenerator == nil) {
		return models.ComposeDraft{}, errors.New("compose synthesis provider is not ready")
	}
	if len(req.Sources) == 0 {
		return models.ComposeDraft{}, errors.New("sources are required")
	}
	var opt createDraftOptions
	if len(opts) > 0 {
		opt = opts[0]
	}

	deduped, sourceLinks := dedupeSources(req.Sources)
	snapshots, hydrateErrs := s.hydrateSnapshots(ctx, deduped)
	if len(snapshots) == 0 {
		// We tolerate partial hydration: a missing thought file is
		// skipped when other source types can still provide context.
		// Only a purely-broken thought-only request should fail.
		if len(hydrateErrs) > 0 {
			return models.ComposeDraft{}, hydrateErrs[0]
		}
		return models.ComposeDraft{}, errors.New("no compose sources could be loaded")
	}

	thoughtIDs := make([]string, 0, len(snapshots))
	for _, snap := range snapshots {
		thoughtIDs = append(thoughtIDs, snap.Thought.ID)
	}

	now := s.now()
	synthReq := ai.SynthesisRequest{
		ThoughtIDs:  thoughtIDs,
		Goal:        firstNonEmpty(req.Goal, "Compose selected sources"),
		Format:      firstNonEmpty(req.Format, models.ComposeFormatSummary),
		Snapshots:   snapshots,
		SourceLinks: sourceLinks,
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		// Selected_thought_ids and prompt are appended to the goal
		// line so the LLM gets a single instruction block; the
		// prompt is otherwise opaque to the wire shape.
		synthReq.Goal = synthReq.Goal + "\n\n" + prompt
	}

	var synthDraft models.SynthesisDraft
	var profileRef *models.DocumentProfileRef
	var structuredDraft *models.DocumentDraft
	var validation *models.ArchiveValidation
	var err error
	if s.profiles != nil && s.documentGenerator != nil {
		profile, profileErr := s.resolveComposeProfile(req)
		if profileErr != nil {
			return models.ComposeDraft{}, profileErr
		}
		docReq := ai.DocumentGenerationRequest{
			Profile:    profile,
			Parameters: req.Parameters,
			Context: ai.DocumentSourceContext{
				Title:       synthReq.Goal,
				Summary:     synthReq.Goal,
				Body:        composeSnapshotText(snapshots),
				SourceLinks: sourceLinks,
			},
		}
		var doc models.DocumentDraft
		var rendered documentprofile.RenderResult
		for attempt := 0; attempt <= s.maxRepairAttempts; attempt++ {
			doc, err = s.documentGenerator.GenerateDocument(ctx, docReq)
			if err != nil {
				return models.ComposeDraft{}, fmt.Errorf("compose generate document: %w", err)
			}
			rendered = documentprofile.Render(profile, doc, req.Parameters)
			rendered.Validation.RepairCount = attempt
			if rendered.Validation.Status == models.ArchiveValidationValid {
				break
			}
			docReq.PreviousDraft = &doc
			docReq.RepairIssues = rendered.Validation.Issues
		}
		if rendered.Validation.Status != models.ArchiveValidationValid {
			return models.ComposeDraft{}, errors.New("compose document format validation failed")
		}
		now := s.now()
		synthDraft = models.SynthesisDraft{ID: models.NewJobID("compose", now), Goal: synthReq.Goal, Format: req.Format, Content: rendered.Content, SourceLinks: sourceLinks, Model: s.model, Status: models.ComposeStatusDraft, CreatedAt: now, UpdatedAt: now}
		ref := profile.Ref
		profileRef = &ref
		structuredDraft = &doc
		valid := rendered.Validation
		validation = &valid
	} else {
		synthDraft, err = s.synthesis.Synthesize(ctx, synthReq)
		if err != nil {
			return models.ComposeDraft{}, fmt.Errorf("compose synthesize: %w", err)
		}
	}

	draftID := firstNonEmpty(opt.DraftID, synthDraft.ID, models.NewJobID("compose", now))
	createdAt := now
	if opt.DraftID != "" && s.draftStore != nil {
		if existing, err := s.draftStore.GetDraft(ctx, opt.DraftID); err == nil && !existing.CreatedAt.IsZero() {
			createdAt = existing.CreatedAt
		}
	}
	fingerprint := firstNonEmpty(opt.RequestFingerprint, composeRequestFingerprint(deduped, req))
	draft := models.ComposeDraft{
		ID:                 draftID,
		Sources:            deduped,
		Goal:               synthReq.Goal,
		Format:             firstNonEmpty(synthReq.Format, models.ComposeFormatSummary),
		DocumentProfile:    profileRef,
		Parameters:         cloneStringMap(req.Parameters),
		DocumentDraft:      structuredDraft,
		Validation:         validation,
		Content:            synthDraft.Content,
		SourceLinks:        sourceLinks,
		Model:              firstNonEmpty(synthDraft.Model, s.model),
		Status:             models.ComposeStatusDraft,
		RequestFingerprint: fingerprint,
		JobID:              opt.JobID,
		GenerationPrompt:   req.Prompt,
		CreatedAt:          createdAt,
		UpdatedAt:          now,
	}
	if len(synthDraft.History) > 0 {
		draft.History = convertHistory(synthDraft.History)
	}
	// Selected_thought_ids travels on the prompt line above; the
	// source list itself is the authoritative record of which
	// thoughts the user picked, so we do not duplicate it here.

	// If this CreateDraft is filling an async placeholder and the
	// user deleted that placeholder mid-run, do not recreate it.
	if opt.DraftID != "" {
		if _, loadErr := s.draftStore.GetDraft(ctx, opt.DraftID); loadErr != nil {
			return models.ComposeDraft{}, errComposeDraftDeleted
		}
	}

	saved, err := s.draftStore.SaveDraft(ctx, draft)
	if err != nil {
		return models.ComposeDraft{}, err
	}
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      EventComposeDraftCreated,
			SourceUnit:     "compose",
			OccurredAt:     s.now(),
			WorkspaceID:    s.workspaceID(),
			ResourceType:   models.ResourceTypeComposeDraft,
			ResourceID:     saved.ID,
			PayloadVersion: 1,
			Payload:        saved,
		})
	}
	return saved, nil
}

func (s *Service) ListDrafts(ctx context.Context) ([]models.ComposeDraft, error) {
	if s == nil || s.draftStore == nil {
		return nil, errors.New("compose draft store is not ready")
	}
	return s.draftStore.ListDrafts(ctx)
}

func (s *Service) GetDraft(ctx context.Context, draftID string) (models.ComposeDraft, error) {
	if s == nil || s.draftStore == nil {
		return models.ComposeDraft{}, errors.New("compose draft store is not ready")
	}
	return s.draftStore.GetDraft(ctx, draftID)
}

// DeleteDraft removes a compose draft file and best-effort cancels any
// in-flight generate job tied to it. Missing drafts are treated as
// already-deleted so the UI can stay idempotent.
func (s *Service) DeleteDraft(ctx context.Context, draftID string) error {
	if s == nil || s.draftStore == nil {
		return errors.New("compose draft store is not ready")
	}
	draftID = strings.TrimSpace(draftID)
	if draftID == "" {
		return errors.New("draft id is required")
	}
	draft, err := s.draftStore.GetDraft(ctx, draftID)
	if err != nil {
		// Idempotent delete: missing file is success.
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			return nil
		}
		return err
	}
	if err := s.draftStore.Delete(ctx, draftID); err != nil {
		return err
	}
	// Drop any in-memory inflight slot for this fingerprint so a
	// subsequent identical generate request can start fresh.
	if fp := strings.TrimSpace(draft.RequestFingerprint); fp != "" {
		s.inflightMu.Lock()
		if current, ok := s.inflight[fp]; ok {
			if current.ID == draft.JobID || current.ResourceID == draftID {
				delete(s.inflight, fp)
			}
		}
		s.inflightMu.Unlock()
	}
	if s.jobs != nil && strings.TrimSpace(draft.JobID) != "" {
		if job, jobErr := s.jobs.Get(draft.JobID); jobErr == nil && jobActive(job) {
			if canceled, cancelErr := s.jobs.MarkCanceled(job, "compose draft deleted"); cancelErr == nil {
				eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), canceled))
			}
		}
	}
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      EventComposeDraftDeleted,
			SourceUnit:     "compose",
			OccurredAt:     s.now(),
			WorkspaceID:    s.workspaceID(),
			ResourceType:   models.ResourceTypeComposeDraft,
			ResourceID:     draftID,
			PayloadVersion: 1,
			Payload: map[string]any{
				"draft_id": draftID,
				"job_id":   draft.JobID,
				"status":   draft.Status,
			},
		})
	}
	return nil
}

func (s *Service) GetBasket(ctx context.Context) (models.ComposeBasket, error) {
	if s == nil || s.draftStore == nil {
		return models.ComposeBasket{}, errors.New("compose draft store is not ready")
	}
	return s.draftStore.GetBasket(ctx)
}

func (s *Service) SaveBasket(ctx context.Context, sources []models.ComposeSource) (models.ComposeBasket, error) {
	if s == nil || s.draftStore == nil {
		return models.ComposeBasket{}, errors.New("compose draft store is not ready")
	}
	return s.draftStore.SaveBasket(ctx, sources)
}

func (s *Service) ClearBasket(ctx context.Context) (models.ComposeBasket, error) {
	if s == nil || s.draftStore == nil {
		return models.ComposeBasket{}, errors.New("compose draft store is not ready")
	}
	return s.draftStore.ClearBasket(ctx)
}

// SaveDraft materialises a stored draft as a Thought via the
// capture sink. The Thought's source is set to "compose" and the
// user-supplied title/tags override the defaults the LLM produced.
// The original draft file is updated to record the saved_thought_id
// and a history event.
func (s *Service) SaveDraft(ctx context.Context, draftID string, req models.ComposeSaveRequest) (models.ComposeSaveResult, error) {
	if s == nil || s.capture == nil {
		return models.ComposeSaveResult{}, errors.New("compose capture sink is not ready")
	}
	if s.draftStore == nil {
		return models.ComposeSaveResult{}, errors.New("compose draft store is not ready")
	}
	draft, err := s.draftStore.GetDraft(ctx, draftID)
	if err != nil {
		return models.ComposeSaveResult{}, err
	}
	if draft.Status == models.ComposeStatusSaved {
		return models.ComposeSaveResult{}, fmt.Errorf("compose draft %q already saved as thought %q", draftID, draft.SavedThoughtID)
	}
	if draft.Status == models.ComposeStatusGenerating {
		return models.ComposeSaveResult{}, fmt.Errorf("compose draft %q is still generating", draftID)
	}
	if draft.Status == models.ComposeStatusFailed {
		return models.ComposeSaveResult{}, fmt.Errorf("compose draft %q failed generation and cannot be saved", draftID)
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		content = draft.Content
	}
	content = stripComposeSourceAppendix(content, draft.SourceLinks)
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = firstNonEmpty(firstMarkdownHeading(content), deriveComposeTitle(draft))
	}
	tags := req.Tags
	if len(tags) == 0 {
		tags = deriveComposeTags(draft)
	}

	cmd := models.CaptureCommand{
		Type:            models.ThoughtTypeText,
		Content:         content,
		Title:           title,
		Tags:            tags,
		Source:          models.ThoughtSourceCompose,
		Links:           dedupeStrings(append([]string{}, draft.SourceLinks...)),
		DocumentProfile: cloneProfileRef(draft.DocumentProfile),
	}
	job, jobErr := s.recordJob(draftID)
	if jobErr != nil && s.jobs == nil {
		return models.ComposeSaveResult{}, jobErr
	}

	result, err := s.capture.Capture(ctx, cmd)
	if err != nil {
		if jobErr == nil {
			_, _ = s.jobs.MarkFailed(job, models.NewErrorRef("thoughtflow.compose.save_failed", err.Error(), true))
		}
		return models.ComposeSaveResult{}, fmt.Errorf("compose capture: %w", err)
	}
	if jobErr == nil {
		job, _ = s.jobs.MarkSucceeded(job, "compose draft saved")
		eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
	}

	saved, err := s.draftStore.MarkSaved(ctx, draftID, content, result.Thought)
	if err != nil {
		return models.ComposeSaveResult{}, err
	}
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      EventComposeDraftSaved,
			SourceUnit:     "compose",
			OccurredAt:     s.now(),
			WorkspaceID:    s.workspaceID(),
			ResourceType:   models.ResourceTypeThought,
			ResourceID:     result.Thought.ID,
			PayloadVersion: 1,
			Payload:        saved,
		})
	}
	return models.ComposeSaveResult{
		Thought:     result.Thought,
		Jobs:        result.Jobs,
		SourceLinks: dedupeStrings(append([]string{}, draft.SourceLinks...)),
	}, nil
}

// hydrateSnapshots turns a ComposeSource list into the ThoughtSnapshot
// list the synthesis provider needs. Thought sources are loaded from
// disk. Search result, topic section, and capture session sources are
// represented as minimal context snapshots so a draft can be generated
// from any supported source type while preserving source_links.
//
// Partial hydration is allowed: missing thought files are recorded
// in the returned error list and skipped, so a single bad source ID
// in a multi-source request does not abort the whole compose. The
// caller only receives a non-nil error when no source of any type
// survived hydration, because then the LLM has nothing to draw on.
func (s *Service) hydrateSnapshots(_ context.Context, sources []models.ComposeSource) ([]models.ThoughtSnapshot, []error) {
	if s == nil || s.workspace == nil {
		return nil, []error{errors.New("compose workspace is not ready")}
	}
	snapshots := []models.ThoughtSnapshot{}
	errs := []error{}
	for _, src := range sources {
		switch src.SourceType {
		case models.ComposeSourceTypeThought, "":
			thought, content, err := markdown.ReadThought(s.workspace.RootPath, src.SourceID)
			if err != nil {
				errs = append(errs, fmt.Errorf("hydrate thought %q: %w", src.SourceID, err))
				continue
			}
			snapshots = append(snapshots, models.ThoughtSnapshot{Thought: thought, Content: content})
		case models.ComposeSourceTypeSearchResult, models.ComposeSourceTypeTopicSection, models.ComposeSourceTypeCaptureSession:
			snapshots = append(snapshots, composeSourceSnapshot(src))
		default:
			continue
		}
	}
	if len(snapshots) > 0 {
		return snapshots, nil
	}
	return snapshots, errs
}

func composeSourceSnapshot(src models.ComposeSource) models.ThoughtSnapshot {
	title := strings.TrimSpace(src.Title)
	if title == "" {
		title = strings.TrimSpace(src.SourceID)
	}
	original := strings.Join([]string{
		"Compose source type: " + strings.TrimSpace(src.SourceType),
		"Compose source id: " + strings.TrimSpace(src.SourceID),
	}, "\n")
	if link := strings.TrimSpace(src.SourceLink); link != "" {
		original += "\nSource link: " + link
	}
	return models.ThoughtSnapshot{
		Thought: models.Thought{
			ID:           strings.TrimSpace(src.SourceID),
			Type:         models.ThoughtTypeText,
			Source:       strings.TrimSpace(src.SourceType),
			UserTitle:    title,
			DisplayTitle: title,
			Path:         strings.TrimSpace(src.SourceLink),
		},
		Content: models.ThoughtContent{
			Original: original,
			Links:    strings.TrimSpace(src.SourceLink),
		},
	}
}

func (s *Service) recordJob(draftID string) (models.Job, error) {
	if s == nil || s.jobs == nil {
		return models.Job{}, errors.New("jobstore is not ready")
	}
	job, err := s.jobs.Create(composeJobType, models.ResourceTypeWorkspace, s.workspaceID(), "compose draft "+draftID+" save queued")
	if err != nil {
		return models.Job{}, err
	}
	job, _ = s.jobs.MarkRunning(job)
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
	}
	return job, nil
}

func (s *Service) workspaceID() string {
	if s == nil || s.workspace == nil {
		return ""
	}
	return s.workspace.ID
}

func dedupeSources(sources []models.ComposeSource) ([]models.ComposeSource, []string) {
	seen := map[string]struct{}{}
	out := []models.ComposeSource{}
	links := []string{}
	for _, src := range sources {
		key := strings.TrimSpace(src.SourceType) + "\x00" + strings.TrimSpace(src.SourceID)
		if key == "\x00" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, src)
		if link := strings.TrimSpace(src.SourceLink); link != "" {
			links = append(links, link)
		}
	}
	// Stable, sorted source_links for deterministic YAML output and
	// for tests that assert the list of links.
	sort.Strings(links)
	links = dedupeStrings(links)
	return out, links
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func deriveComposeTitle(draft models.ComposeDraft) string {
	if heading := firstMarkdownHeading(draft.Content); heading != "" {
		return heading
	}
	goal := strings.TrimSpace(draft.Goal)
	if goal != "" {
		first := strings.SplitN(goal, "\n", 2)[0]
		first = strings.TrimSpace(first)
		if first != "" {
			return first
		}
	}
	for _, src := range draft.Sources {
		if strings.TrimSpace(src.Title) != "" {
			return strings.TrimSpace(src.Title)
		}
	}
	if len(draft.Sources) > 0 {
		return strings.TrimSpace(draft.Sources[0].SourceID)
	}
	return "Untitled compose"
}

func firstMarkdownHeading(content string) string {
	for _, line := range strings.Split(strings.TrimSpace(content), "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "#") {
			continue
		}
		title := strings.TrimSpace(strings.TrimLeft(line, "#"))
		if title != "" {
			return title
		}
	}
	return ""
}

func stripComposeSourceAppendix(content string, sourceLinks []string) string {
	content = strings.TrimSpace(content)
	if content == "" || len(sourceLinks) == 0 {
		return content
	}
	markers := []string{"\n### Sources\n", "\n## Sources\n", "\n### 来源\n", "\n## 来源\n"}
	for _, marker := range markers {
		idx := strings.LastIndex(content, marker)
		if idx < 0 {
			continue
		}
		tail := strings.TrimSpace(content[idx+len(marker):])
		if sourceAppendixContainsOnlyLinks(tail, sourceLinks) {
			return strings.TrimSpace(content[:idx])
		}
	}
	return content
}

func sourceAppendixContainsOnlyLinks(tail string, sourceLinks []string) bool {
	if strings.TrimSpace(tail) == "" {
		return false
	}
	allowed := map[string]struct{}{}
	for _, link := range dedupeStrings(sourceLinks) {
		allowed[link] = struct{}{}
		allowed[("[[" + link + "]]")] = struct{}{}
	}
	for _, line := range strings.Split(tail, "\n") {
		line = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "-"))
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if _, ok := allowed[line]; !ok {
			return false
		}
	}
	return true
}

func deriveComposeTags(draft models.ComposeDraft) []string {
	tags := []string{"compose"}
	for _, src := range draft.Sources {
		switch src.SourceType {
		case models.ComposeSourceTypeTopicSection:
			tags = append(tags, "topic")
		case models.ComposeSourceTypeSearchResult:
			tags = append(tags, "search")
		case models.ComposeSourceTypeCaptureSession:
			tags = append(tags, "capture")
		}
	}
	return dedupeStrings(tags)
}

func jobEvent(workspaceID string, job models.Job) models.DomainEvent {
	return models.DomainEvent{
		EventType:      models.EventJobUpdated,
		SourceUnit:     "compose",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   job.ResourceType,
		ResourceID:     job.ResourceID,
		PayloadVersion: 1,
		Payload:        job,
	}
}

func convertHistory(in []models.SynthesisDraftHistory) []models.ComposeDraftHistory {
	if len(in) == 0 {
		return nil
	}
	out := make([]models.ComposeDraftHistory, 0, len(in))
	for _, h := range in {
		out = append(out, models.ComposeDraftHistory{
			Status:    h.Status,
			Message:   h.Message,
			ThoughtID: h.ThoughtID,
			At:        h.At,
		})
	}
	return out
}

func (s *Service) resolveComposeProfile(req models.ComposeRequest) (documentprofile.DocumentProfile, error) {
	profileID := strings.TrimSpace(req.ProfileID)
	if profileID == "" {
		switch strings.TrimSpace(req.Format) {
		case models.ComposeFormatReport:
			profileID = models.DocumentProfileBuiltinResearchReport
		default:
			profileID = models.DocumentProfileBuiltinNote
		}
	}
	if req.ProfileVersion > 0 {
		return s.profiles.Resolve(models.DocumentProfileRef{ProfileID: profileID, Version: req.ProfileVersion})
	}
	return s.profiles.ResolveLatest(profileID)
}

func composeSnapshotText(snapshots []models.ThoughtSnapshot) string {
	parts := []string{}
	for _, snapshot := range snapshots {
		title := firstNonEmpty(snapshot.Thought.DisplayTitle, snapshot.Thought.UserTitle, snapshot.Thought.ExtractedTitle, snapshot.Thought.ID)
		body := firstNonEmpty(snapshot.Thought.Summary, snapshot.Content.AINotes, snapshot.Content.ExtractedContent, snapshot.Content.Original)
		if strings.TrimSpace(body) == "" {
			continue
		}
		parts = append(parts, "## "+title+"\n\n"+body)
	}
	return strings.Join(parts, "\n\n")
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func cloneProfileRef(ref *models.DocumentProfileRef) *models.DocumentProfileRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
}

func composeRequestFromDraft(draft models.ComposeDraft) models.ComposeRequest {
	req := models.ComposeRequest{
		Sources:    append([]models.ComposeSource{}, draft.Sources...),
		Prompt:     draft.GenerationPrompt,
		Goal:       draft.Goal,
		Format:     draft.Format,
		Parameters: cloneStringMap(draft.Parameters),
	}
	if draft.DocumentProfile != nil {
		req.ProfileID = draft.DocumentProfile.ProfileID
		req.ProfileVersion = draft.DocumentProfile.Version
	}
	return req
}

func composeRequestFingerprint(sources []models.ComposeSource, req models.ComposeRequest) string {
	parts := make([]string, 0, len(sources)+8)
	for _, source := range sources {
		parts = append(parts, composeSourceFingerprintPart(source))
	}
	sort.Strings(parts)
	parts = append(parts,
		"goal="+strings.TrimSpace(req.Goal),
		"format="+strings.TrimSpace(req.Format),
		"profile="+strings.TrimSpace(req.ProfileID),
		fmt.Sprintf("profile_version=%d", req.ProfileVersion),
		"prompt="+strings.TrimSpace(req.Prompt),
	)
	if len(req.Parameters) > 0 {
		keys := make([]string, 0, len(req.Parameters))
		for key := range req.Parameters {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, key+"="+req.Parameters[key])
		}
	}
	sum := sha1.Sum([]byte(strings.Join(parts, "\n")))
	return hex.EncodeToString(sum[:])
}

func composeSourceFingerprintPart(source models.ComposeSource) string {
	parts := []string{
		"type=" + strings.TrimSpace(source.SourceType),
		"id=" + strings.TrimSpace(source.SourceID),
		"title=" + strings.TrimSpace(source.Title),
		"snippet=" + strings.TrimSpace(source.Snippet),
		"link=" + strings.TrimSpace(source.SourceLink),
	}
	if len(source.Metadata) > 0 {
		keys := make([]string, 0, len(source.Metadata))
		for key := range source.Metadata {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			parts = append(parts, "metadata."+key+"="+source.Metadata[key])
		}
	}
	return strings.Join(parts, "\x1f")
}

func composeSourceKey(source models.ComposeSource) string {
	sourceType := strings.TrimSpace(source.SourceType)
	sourceID := strings.TrimSpace(source.SourceID)
	if sourceType == "" || sourceID == "" {
		return ""
	}
	return sourceType + "\x00" + sourceID
}

func jobActive(job models.Job) bool {
	switch job.Status {
	case models.JobStatusQueued, models.JobStatusRunning, models.JobStatusRetrying:
		return true
	default:
		return false
	}
}

// findInflightGenerateJob scans persisted jobs for an active generate job
// whose draft still carries the same request fingerprint. This covers the
// process-restart case where the in-memory map is empty but a prior click
// is still running.
func (s *Service) findInflightGenerateJob(fingerprint string) (models.Job, bool) {
	if s == nil || s.jobs == nil || s.draftStore == nil || strings.TrimSpace(fingerprint) == "" {
		return models.Job{}, false
	}
	jobs, err := s.jobs.List()
	if err != nil {
		return models.Job{}, false
	}
	for _, job := range jobs {
		if job.Type != models.JobTypeComposeGenerate || !jobActive(job) {
			continue
		}
		draft, err := s.draftStore.GetDraft(context.Background(), job.ResourceID)
		if err != nil {
			continue
		}
		if draft.RequestFingerprint == fingerprint && draft.Status == models.ComposeStatusGenerating {
			return job, true
		}
	}
	return models.Job{}, false
}
