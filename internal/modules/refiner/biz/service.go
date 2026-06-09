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
}

const refineMaxAttempts = 3

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

func NewService(workspace *models.Workspace, jobs *jobstore.Store, eventHub event.Hub, background task.BackgroundRoutine, provider ai.RefineProvider, fetcher *webfetch.Fetcher) *Service {
	return &Service{
		workspace:  workspace,
		jobs:       jobs,
		eventHub:   eventHub,
		background: background,
		provider:   provider,
		fetcher:    fetcher,
	}
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
	if strings.TrimSpace(thoughtID) == "" {
		return models.Job{}, errors.New("thought id is required")
	}
	job, err := s.jobs.CreateWithMaxAttempts(models.JobTypeRefine, models.ResourceTypeThought, thoughtID, "refine queued", refineMaxAttempts)
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.refineJob(job)
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
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.ThoughtRefinement{}, err
	}
	return s.refine(ctx, thought, content)
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

func (s *Service) refineJob(job models.Job) {
	ctx := context.Background()
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

		refinement, err := s.RefineNow(ctx, job.ResourceID)
		if err == nil {
			job, _ = s.jobs.MarkSucceeded(job, "refine succeeded")
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
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

func (s *Service) refine(ctx context.Context, thought models.Thought, content models.ThoughtContent) (models.ThoughtRefinement, error) {
	thought.RefineStatus = models.RefineStatusRunning
	if thought.Type == models.ThoughtTypeURL && thought.URL != "" {
		fetched, err := s.fetcher.Fetch(ctx, thought.URL)
		if err != nil {
			errRef := models.NewErrorRef("thoughtflow.refiner.fetch_failed", err.Error(), true)
			thought.RefineStatus = models.RefineStatusFailed
			thought.Errors = append(thought.Errors, errRef)
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
	}

	refinement, err := s.provider.Refine(ctx, ai.RefineRequest{Thought: thought, Content: content})
	if err != nil {
		return models.ThoughtRefinement{}, err
	}
	if strings.TrimSpace(refinement.ExtractedTitle) != "" {
		thought.ExtractedTitle = strings.TrimSpace(refinement.ExtractedTitle)
	}
	thought.Summary = refinement.Summary
	thought.KeyPoints = refinement.KeyPoints
	thought.AITags = refinement.AITags
	thought.RefineStatus = models.RefineStatusRefined
	thought.UpdatedAt = time.Now().UTC()
	content.AINotes = renderAINotes(refinement)
	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		return models.ThoughtRefinement{}, err
	}
	return refinement, nil
}

func isRetryableRefineError(err error) bool {
	var retryable retryableRefineError
	if errors.As(err, &retryable) {
		return true
	}
	var providerErr ai.ProviderError
	return errors.As(err, &providerErr) && providerErr.Retryable
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
