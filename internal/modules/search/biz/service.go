package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/ai"
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
	indexPath  string
	eventHub   event.Hub
	background task.BackgroundRoutine
	embedder   ai.EmbeddingProvider
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, store *searchdb.Store, eventHub event.Hub, background task.BackgroundRoutine, embedder ai.EmbeddingProvider, indexPath string) *Service {
	return &Service{
		workspace:  workspace,
		jobs:       jobs,
		store:      store,
		indexPath:  normalizeIndexPath(workspace, indexPath),
		eventHub:   eventHub,
		background: background,
		embedder:   embedder,
	}
}

func (s *Service) ID() string {
	return "search.thought-index-observer"
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if ok {
		switch domainEvent.ResourceType {
		case models.ResourceTypeThought:
			if domainEvent.ResourceID != "" {
				_, _ = s.IndexAsyncWithEmbedding(domainEvent.ResourceID, eventEmbedding(domainEvent.Payload))
			}
		case models.ResourceTypeTopic:
			_, _ = s.ReindexWorkspace(context.Background())
		}
	}
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Service) IndexAsync(thoughtID string) (models.Job, error) {
	return s.IndexAsyncWithEmbedding(thoughtID, nil)
}

func (s *Service) IndexAsyncWithEmbedding(thoughtID string, embedding *models.EmbeddingRecord) (models.Job, error) {
	if strings.TrimSpace(thoughtID) == "" {
		return models.Job{}, errors.New("thought id is required")
	}
	job, err := s.jobs.Create(models.JobTypeIndex, models.ResourceTypeThought, thoughtID, "index queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.indexJob(job, embedding)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) IndexThought(ctx context.Context, thoughtID string, embedding *models.EmbeddingRecord) error {
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return err
	}
	thought.IndexStatus = models.IndexStatusIndexed
	thought.UpdatedAt = time.Now().UTC()
	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		return err
	}
	if err := s.store.IndexThought(ctx, thought, content); err != nil {
		return err
	}
	if embedding != nil && len(embedding.Vector) > 0 {
		embedding.ThoughtID = thought.ID
		if embedding.ContentHash == "" {
			embedding.ContentHash = models.ContentHash(buildEmbeddingText(thought, content))
		}
		if embedding.CreatedAt.IsZero() {
			embedding.CreatedAt = time.Now().UTC()
		}
		return s.store.IndexEmbedding(ctx, *embedding)
	}
	return nil
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
	mode := strings.ToLower(strings.TrimSpace(query.Mode))
	if mode == "" {
		mode = "hybrid"
	}
	query.Mode = mode
	if strings.TrimSpace(query.Query) != "" && (mode == "semantic" || mode == "hybrid") && s.embedder != nil {
		embedding, err := s.embedder.Embed(ctx, ai.EmbedRequest{Text: query.Query})
		if err != nil {
			if mode == "semantic" {
				return models.SearchResponse{}, err
			}
		} else {
			query.QueryVector = embedding.Vector
			query.EmbeddingModel = embedding.Model
		}
	}
	return s.store.Search(ctx, query)
}

func (s *Service) GetSearchPreview(ctx context.Context, thoughtID string) (models.SearchResult, error) {
	if s == nil || s.store == nil {
		return models.SearchResult{}, errors.New("search service is not ready")
	}
	return s.store.GetSearchPreview(ctx, thoughtID)
}

func (s *Service) RuntimeStatus(ctx context.Context) models.DuckDBRuntimeStatus {
	_ = ctx
	status := models.DuckDBRuntimeStatus{Status: "ready"}
	if s == nil || s.workspace == nil {
		status.Status = "degraded"
		status.Error = "search service is not ready"
		return status
	}
	status.Path = s.indexPath
	if status.Path == "" {
		status.Status = "degraded"
		status.Error = "search index path is not configured"
		return status
	}
	if s.store == nil {
		status.Status = "degraded"
		status.Error = "search index store is not ready"
		return status
	}
	if _, err := os.Stat(status.Path); err == nil {
		status.Exists = true
		return status
	} else if errors.Is(err, os.ErrNotExist) {
		return status
	} else {
		status.Status = "degraded"
		status.Error = err.Error()
		return status
	}
}

func normalizeIndexPath(workspace *models.Workspace, indexPath string) string {
	indexPath = strings.TrimSpace(indexPath)
	if indexPath == "" {
		indexPath = filepath.ToSlash(filepath.Join(".thoughtflow", "thoughtflow.duckdb"))
	}
	if filepath.IsAbs(indexPath) || workspace == nil || strings.TrimSpace(workspace.RootPath) == "" {
		return indexPath
	}
	return filepath.Join(workspace.RootPath, filepath.FromSlash(indexPath))
}

func (s *Service) CachedEmbedding(ctx context.Context, thoughtID string, model string) (models.EmbeddingRecord, bool) {
	if s == nil || s.store == nil || strings.TrimSpace(thoughtID) == "" {
		return models.EmbeddingRecord{}, false
	}
	return s.store.GetEmbedding(ctx, thoughtID, model)
}

func (s *Service) CachedSemanticScores(ctx context.Context, queryVector []float64, model string, limit int) (map[string]float64, string, bool) {
	if s == nil || s.store == nil || len(queryVector) == 0 {
		return nil, "", false
	}
	return s.store.SemanticScores(ctx, queryVector, model, limit)
}

func (s *Service) indexJob(job models.Job, embedding *models.EmbeddingRecord) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	err := s.IndexThought(context.Background(), job.ResourceID, embedding)
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

func eventEmbedding(payload any) *models.EmbeddingRecord {
	switch value := payload.(type) {
	case models.ThoughtRefinement:
		return value.Embedding
	case *models.ThoughtRefinement:
		if value == nil {
			return nil
		}
		return value.Embedding
	default:
		return nil
	}
}

func buildEmbeddingText(thought models.Thought, content models.ThoughtContent) string {
	return strings.Join([]string{
		thought.UserTitle,
		thought.ExtractedTitle,
		thought.Summary,
		content.Original,
		content.ExtractedContent,
		content.AINotes,
	}, "\n")
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
