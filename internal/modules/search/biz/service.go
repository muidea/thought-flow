package biz

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/searchdb"
)

type Service struct {
	workspace  *models.Workspace
	jobs       *jobstore.Store
	store      *searchdb.Store
	eventHub   event.Hub
	background task.BackgroundRoutine
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, store *searchdb.Store, eventHub event.Hub, background task.BackgroundRoutine) *Service {
	return &Service{
		workspace:  workspace,
		jobs:       jobs,
		store:      store,
		eventHub:   eventHub,
		background: background,
	}
}

func (s *Service) ID() string {
	return "search.thought-index-observer"
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if ok && domainEvent.ResourceID != "" {
		_, _ = s.IndexAsync(domainEvent.ResourceID)
	}
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Service) IndexAsync(thoughtID string) (models.Job, error) {
	if strings.TrimSpace(thoughtID) == "" {
		return models.Job{}, errors.New("thought id is required")
	}
	job, err := s.jobs.Create(models.JobTypeIndex, models.ResourceTypeThought, thoughtID, "index queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.indexJob(job)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) IndexThought(ctx context.Context, thoughtID string) error {
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return err
	}
	thought.IndexStatus = models.IndexStatusIndexed
	thought.UpdatedAt = time.Now().UTC()
	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		return err
	}
	return s.store.IndexThought(ctx, thought, content)
}

func (s *Service) ReindexWorkspace(ctx context.Context) (models.Job, error) {
	job, err := s.jobs.Create(models.JobTypeReindex, models.ResourceTypeWorkspace, s.workspace.ID, "reindex queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.reindexJob(job)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) Search(ctx context.Context, query models.SearchQuery) (models.SearchResponse, error) {
	return s.store.Search(ctx, query)
}

func (s *Service) indexJob(job models.Job) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	err := s.IndexThought(context.Background(), job.ResourceID)
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.search.index_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, searchEvent(models.EventSearchIndexFailed, s.workspace.ID, job.ResourceID, errRef))
		return
	}
	job, _ = s.jobs.MarkSucceeded(job, "index succeeded")
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, searchEvent(models.EventSearchIndexUpdated, s.workspace.ID, job.ResourceID, job))
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:    models.EventGitCommitRequested,
		SourceUnit:   "search",
		OccurredAt:   time.Now().UTC(),
		WorkspaceID:  s.workspace.ID,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   job.ResourceID,
		Payload: models.GitCommitRequestedPayload{
			Paths:       []string{markdown.ThoughtRelativePath(job.ResourceID)},
			Reason:      "index",
			ResourceIDs: []string{job.ResourceID},
		},
		PayloadVersion: 1,
	})
}

func (s *Service) reindexJob(job models.Job) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, searchEvent(models.EventSearchReindexStarted, s.workspace.ID, s.workspace.ID, job))
	count, err := s.store.ReindexWorkspace(context.Background(), s.workspace.RootPath)
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.search.reindex_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		eventutil.Post(s.eventHub, searchEvent(models.EventSearchIndexFailed, s.workspace.ID, s.workspace.ID, errRef))
		return
	}
	job.Message = "reindexed workspace"
	job, _ = s.jobs.MarkSucceeded(job, "reindexed workspace")
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, searchEvent(models.EventSearchReindexFinished, s.workspace.ID, s.workspace.ID, map[string]any{"count": count, "job": job}))
}

func jobEvent(workspaceID string, job models.Job) models.DomainEvent {
	return models.DomainEvent{
		EventType:      models.EventJobUpdated,
		SourceUnit:     "search",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   job.ResourceType,
		ResourceID:     job.ResourceID,
		PayloadVersion: 1,
		Payload:        job,
	}
}

func searchEvent(eventType string, workspaceID string, resourceID string, payload any) models.DomainEvent {
	resourceType := models.ResourceTypeThought
	if resourceID == workspaceID {
		resourceType = models.ResourceTypeWorkspace
	}
	return models.DomainEvent{
		EventType:      eventType,
		SourceUnit:     "search",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   resourceType,
		ResourceID:     resourceID,
		PayloadVersion: 1,
		Payload:        payload,
	}
}
