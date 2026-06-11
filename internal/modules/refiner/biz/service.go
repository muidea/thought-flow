package biz

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/synthesisstore"
	"thoughtflow/internal/pkg/thoughtlock"
	"thoughtflow/internal/pkg/webfetch"
)

type Service struct {
	workspace         *models.Workspace
	jobs              *jobstore.Store
	eventHub          event.Hub
	background        task.BackgroundRoutine
	provider          ai.RefineProvider
	fetcher           *webfetch.Fetcher
	synthesisProvider ai.SynthesisProvider
	synthesisStore    *synthesisstore.Store
	locker            *thoughtlock.Locker
}

const refineMaxAttempts = 3
const skippedUnchangedModel = "cached-unchanged"
const refinerSessionID = "refiner"

type retryableRefineError struct {
	err error
}

type SynthesisDraftStoreError struct {
	Err error
}

func (e SynthesisDraftStoreError) Error() string {
	if e.Err == nil {
		return "synthesis draft store failed"
	}
	return e.Err.Error()
}

func (e SynthesisDraftStoreError) Unwrap() error {
	return e.Err
}

func (e retryableRefineError) Error() string {
	return e.err.Error()
}

func (e retryableRefineError) Unwrap() error {
	return e.err
}

type Option func(*Service)

// WithLocker attaches a thoughtlock.Locker. The same instance should be
// shared with the capture service so PATCH and refine serialize against
// each other.
func WithLocker(locker *thoughtlock.Locker) Option {
	return func(s *Service) {
		s.locker = locker
	}
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, eventHub event.Hub, background task.BackgroundRoutine, provider ai.RefineProvider, fetcher *webfetch.Fetcher, options ...Option) *Service {
	service := &Service{
		workspace:  workspace,
		jobs:       jobs,
		eventHub:   eventHub,
		background: background,
		provider:   provider,
		fetcher:    fetcher,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) ConfigureSynthesis(provider ai.SynthesisProvider, store *synthesisstore.Store) {
	s.synthesisProvider = provider
	s.synthesisStore = store
}

func (s *Service) ID() string {
	return "refiner.thought-captured-observer"
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if !ok {
		return
	}
	if domainEvent.ResourceID != "" {
		_, _ = s.RefineAsync(domainEvent.ResourceID)
	}
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Service) RefineAsync(thoughtID string) (models.Job, error) {
	return s.refineAsync(thoughtID, false)
}

func (s *Service) RetryRefineAsync(thoughtID string) (models.Job, error) {
	return s.refineAsync(thoughtID, true)
}

func (s *Service) refineAsync(thoughtID string, force bool) (models.Job, error) {
	if strings.TrimSpace(thoughtID) == "" {
		return models.Job{}, errors.New("thought id is required")
	}
	job, err := s.jobs.CreateWithMaxAttempts(models.JobTypeRefine, models.ResourceTypeThought, thoughtID, "refine queued", refineMaxAttempts)
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.refineJob(job, force)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) RefineNow(ctx context.Context, thoughtID string) (models.ThoughtRefinement, error) {
	return s.refineNow(ctx, thoughtID, false)
}

func (s *Service) Suggest(ctx context.Context, thoughtID string) (models.ThoughtSuggestion, error) {
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.ThoughtSuggestion{}, err
	}
	if thought.ExtractedTitle != "" {
		return models.ThoughtSuggestion{
			ThoughtID: thoughtID,
			Title:     thought.ExtractedTitle,
			Tags:      append([]string{}, thought.AITags...),
			Model:     "extracted",
		}, nil
	}
	if thought.RefineStatus == models.RefineStatusRefined {
		title := thought.UserTitle
		if title == "" {
			title = thought.ExtractedTitle
		}
		return models.ThoughtSuggestion{
			ThoughtID: thoughtID,
			Title:     title,
			Tags:      append([]string{}, thought.AITags...),
			Model:     "refined",
		}, nil
	}
	fallback := strings.SplitN(strings.TrimSpace(content.Original), "\n", 2)[0]
	if len(fallback) > 80 {
		fallback = fallback[:80]
	}
	return models.ThoughtSuggestion{
		ThoughtID: thoughtID,
		Title:     fallback,
		Tags:      []string{},
		Model:     "fallback",
	}, nil
}

func (s *Service) refineNow(ctx context.Context, thoughtID string, force bool) (models.ThoughtRefinement, error) {
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.ThoughtRefinement{}, err
	}
	return s.refine(ctx, thought, content, force)
}

func (s *Service) CreateSynthesisDraft(ctx context.Context, request models.SynthesisRequest, snapshots []models.ThoughtSnapshot, sourceLinks []string) (models.SynthesisDraft, error) {
	if s == nil || s.synthesisProvider == nil {
		return models.SynthesisDraft{}, errors.New("synthesis provider is not ready")
	}
	if s.synthesisStore == nil {
		return models.SynthesisDraft{}, errors.New("synthesis draft store is unavailable")
	}
	if len(snapshots) == 0 {
		return models.SynthesisDraft{}, errors.New("synthesis snapshots are required")
	}
	thoughtIDs := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		thoughtIDs = append(thoughtIDs, snapshot.Thought.ID)
	}
	draft, err := s.synthesisProvider.Synthesize(ctx, ai.SynthesisRequest{
		ThoughtIDs:  thoughtIDs,
		Goal:        request.Goal,
		Format:      request.Format,
		Snapshots:   snapshots,
		SourceLinks: sourceLinks,
	})
	if err != nil {
		return models.SynthesisDraft{}, err
	}
	draft, err = s.synthesisStore.SaveDraft(ctx, draft)
	if err != nil {
		return models.SynthesisDraft{}, SynthesisDraftStoreError{Err: err}
	}
	return draft, nil
}

func (s *Service) ListSynthesisDrafts(ctx context.Context) ([]models.SynthesisDraft, error) {
	if s == nil || s.synthesisStore == nil {
		return nil, errors.New("synthesis draft store is unavailable")
	}
	return s.synthesisStore.ListDrafts(ctx)
}

func (s *Service) GetSynthesisDraft(ctx context.Context, draftID string) (models.SynthesisDraft, error) {
	if s == nil || s.synthesisStore == nil {
		return models.SynthesisDraft{}, errors.New("synthesis draft store is unavailable")
	}
	return s.synthesisStore.GetDraft(ctx, draftID)
}

func (s *Service) MarkSynthesisSaved(ctx context.Context, draftID string, content string, thought models.Thought) (models.SynthesisDraft, error) {
	if s == nil || s.synthesisStore == nil {
		return models.SynthesisDraft{}, errors.New("synthesis draft store is unavailable")
	}
	return s.synthesisStore.MarkSaved(ctx, draftID, content, thought)
}

func (s *Service) refineJob(job models.Job, force bool) {
	ctx := context.Background()
	// Skip the job cleanly if a Capture session is currently editing the
	// thought. We don't queue or retry — the next background sweep will
	// pick it up once the session releases the lock or the TTL elapses.
	if s.locker != nil {
		if err := s.locker.Acquire(job.ResourceID, refinerSessionID); err != nil {
			skipped, _ := s.jobs.MarkSucceeded(job, "skipped: thought is locked by an active session")
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, skipped))
			return
		}
		defer s.locker.Release(job.ResourceID, refinerSessionID)
	}
	for {
		job, _ = s.jobs.MarkRunning(job)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      models.EventThoughtRefineStarted,
			SourceUnit:     "refiner",
			OccurredAt:     time.Now().UTC(),
			WorkspaceID:    s.workspace.ID,
			ResourceType:   models.ResourceTypeThought,
			ResourceID:     job.ResourceID,
			PayloadVersion: 1,
			Payload:        job,
		})

		refinement, err := s.refineNow(ctx, job.ResourceID, force)
		if err == nil {
			message := "refine succeeded"
			if refinementSkipped(refinement) {
				message = "refine skipped: input unchanged"
			}
			job, _ = s.jobs.MarkSucceeded(job, message)
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
			if refinementSkipped(refinement) {
				return
			}
			eventutil.Post(s.eventHub, models.DomainEvent{
				EventType:      models.EventThoughtRefined,
				SourceUnit:     "refiner",
				OccurredAt:     time.Now().UTC(),
				WorkspaceID:    s.workspace.ID,
				ResourceType:   models.ResourceTypeThought,
				ResourceID:     refinement.ThoughtID,
				PayloadVersion: 1,
				Payload:        refinement,
			})
			eventutil.Post(s.eventHub, models.DomainEvent{
				EventType:    models.EventGitCommitRequested,
				SourceUnit:   "refiner",
				OccurredAt:   time.Now().UTC(),
				WorkspaceID:  s.workspace.ID,
				ResourceType: models.ResourceTypeThought,
				ResourceID:   refinement.ThoughtID,
				Payload: models.GitCommitRequestedPayload{
					Paths:       []string{markdown.ThoughtRelativePath(refinement.ThoughtID)},
					Reason:      "refine",
					ResourceIDs: []string{refinement.ThoughtID},
				},
				PayloadVersion: 1,
			})
			return
		}

		errRef := models.NewErrorRef("thoughtflow.refiner.failed", err.Error(), isRetryableRefineError(err))
		if errRef.Retryable && job.Attempt < job.MaxAttempts {
			job, _ = s.jobs.MarkRetrying(job, errRef, "refine retrying")
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
			continue
		}
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      models.EventThoughtRefineFailed,
			SourceUnit:     "refiner",
			OccurredAt:     time.Now().UTC(),
			WorkspaceID:    s.workspace.ID,
			ResourceType:   models.ResourceTypeThought,
			ResourceID:     job.ResourceID,
			PayloadVersion: 1,
			Payload:        errRef,
		})
		return
	}
}

func (s *Service) refine(ctx context.Context, thought models.Thought, content models.ThoughtContent, force bool) (models.ThoughtRefinement, error) {
	if !force {
		if refinement, ok := unchangedRefinement(thought, content); ok {
			return refinement, nil
		}
	}
	thought.RefineStatus = models.RefineStatusRunning
	if thought.Type == models.ThoughtTypeURL && thought.URL != "" {
		fetched, err := s.fetcher.Fetch(ctx, thought.URL)
		if err != nil {
			errRef := models.NewErrorRef("thoughtflow.refiner.fetch_failed", err.Error(), true)
			thought.RefineStatus = models.RefineStatusFailed
			thought.Errors = replaceErrorRef(thought.Errors, errRef)
			thought.UpdatedAt = time.Now().UTC()
			_ = markdown.WriteThought(s.workspace.RootPath, thought, content)
			return models.ThoughtRefinement{
				ThoughtID:   thought.ID,
				Status:      models.RefineStatusFailed,
				GeneratedAt: time.Now().UTC(),
				Error:       &errRef,
			}, retryableRefineError{err: err}
		}
		if fetched.Title != "" {
			thought.ExtractedTitle = fetched.Title
		}
		if fetched.Content != "" {
			content.ExtractedContent = fetched.Content
		}
		thought.UpdatedAt = time.Now().UTC()
		_ = markdown.WriteThought(s.workspace.RootPath, thought, content)
	}

	refinement, err := s.provider.Refine(ctx, ai.RefineRequest{Thought: thought, Content: content})
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.refiner.provider_failed", err.Error(), isRetryableRefineError(err))
		thought.RefineStatus = models.RefineStatusFailed
		thought.Errors = replaceErrorRef(thought.Errors, errRef)
		thought.UpdatedAt = time.Now().UTC()
		_ = markdown.WriteThought(s.workspace.RootPath, thought, content)
		return models.ThoughtRefinement{}, err
	}
	if strings.TrimSpace(refinement.ExtractedTitle) != "" {
		thought.ExtractedTitle = strings.TrimSpace(refinement.ExtractedTitle)
	}
	thought.Summary = refinement.Summary
	thought.KeyPoints = refinement.KeyPoints
	thought.AITags = refinement.AITags
	thought.RefineStatus = models.RefineStatusRefined
	thought.Errors = nil
	thought.UpdatedAt = time.Now().UTC()
	content.AINotes = renderAINotes(refinement)
	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		return models.ThoughtRefinement{}, err
	}
	return refinement, nil
}

func unchangedRefinement(thought models.Thought, content models.ThoughtContent) (models.ThoughtRefinement, bool) {
	if thought.RefineStatus != models.RefineStatusRefined {
		return models.ThoughtRefinement{}, false
	}
	if strings.TrimSpace(content.AINotes) == "" {
		return models.ThoughtRefinement{}, false
	}
	inputHash := models.ContentHash(content.Original)
	if thought.ContentHash == "" || thought.ContentHash != inputHash {
		return models.ThoughtRefinement{}, false
	}
	return models.ThoughtRefinement{
		ThoughtID:      thought.ID,
		Status:         models.RefineStatusRefined,
		ExtractedTitle: thought.ExtractedTitle,
		Summary:        thought.Summary,
		KeyPoints:      thought.KeyPoints,
		AITags:         thought.AITags,
		Model:          skippedUnchangedModel,
		InputHash:      inputHash,
		GeneratedAt:    time.Now().UTC(),
	}, true
}

func refinementSkipped(refinement models.ThoughtRefinement) bool {
	return refinement.Model == skippedUnchangedModel
}

func isRetryableRefineError(err error) bool {
	var retryable retryableRefineError
	if errors.As(err, &retryable) {
		return true
	}
	var providerErr ai.ProviderError
	return errors.As(err, &providerErr) && providerErr.Retryable
}

func replaceErrorRef(errors []models.ErrorRef, next models.ErrorRef) []models.ErrorRef {
	ret := make([]models.ErrorRef, 0, len(errors)+1)
	for _, item := range errors {
		if item.Code == next.Code {
			continue
		}
		ret = append(ret, item)
	}
	return append(ret, next)
}

func renderAINotes(refinement models.ThoughtRefinement) string {
	var builder strings.Builder
	if refinement.Summary != "" {
		builder.WriteString("Summary: ")
		builder.WriteString(refinement.Summary)
		builder.WriteString("\n")
	}
	for _, point := range refinement.KeyPoints {
		builder.WriteString("- ")
		builder.WriteString(point)
		builder.WriteString("\n")
	}
	if len(refinement.AITags) > 0 {
		builder.WriteString("Tags: ")
		builder.WriteString(strings.Join(refinement.AITags, ", "))
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func jobEvent(workspaceID string, job models.Job) models.DomainEvent {
	return models.DomainEvent{
		EventType:      models.EventJobUpdated,
		SourceUnit:     "refiner",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   job.ResourceType,
		ResourceID:     job.ResourceID,
		PayloadVersion: 1,
		Payload:        job,
	}
}
