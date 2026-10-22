package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/searchdb"
	"thoughtflow/internal/pkg/thoughtlock"
)

type Service struct {
	runner       *jobstore.Runner
	mutationMu   sync.Mutex
	stateMu      sync.Mutex
	rebuilding   bool
	rebuildError string
	workspace    *models.Workspace
	jobs         *jobstore.Store
	store        *searchdb.Store
	indexPath    string
	eventHub     event.Hub
	background   task.BackgroundRoutine
	embedder     ai.EmbeddingProvider
	locker       *thoughtlock.Locker
}

const indexerSessionID = thoughtlock.IndexerSessionID
const indexLockWait = time.Second

type Option func(*Service)

// WithLocker attaches the shared thought lock so indexing cannot write a
// thought after another workflow has deleted it.
func WithLocker(locker *thoughtlock.Locker) Option {
	return func(s *Service) {
		s.locker = locker
	}
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, store *searchdb.Store, eventHub event.Hub, background task.BackgroundRoutine, embedder ai.EmbeddingProvider, indexPath string, options ...Option) *Service {
	service := &Service{
		runner:     jobstore.NewRunner(jobs),
		workspace:  workspace,
		jobs:       jobs,
		store:      store,
		indexPath:  normalizeIndexPath(workspace, indexPath),
		eventHub:   eventHub,
		background: background,
		embedder:   embedder,
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) ID() string {
	return "search.thought-index-observer"
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if ok {
		switch domainEvent.ResourceType {
		case models.ResourceTypeThought:
			switch domainEvent.EventType {
			case models.EventThoughtDeleted:
				_ = s.DeleteThought(context.Background(), domainEvent.ResourceID)
			case models.EventThoughtCaptured, models.EventThoughtRefined, models.EventThoughtPatched, models.EventThoughtExpanded:
				_, _ = s.IndexAsyncWithEmbedding(domainEvent.ResourceID, eventEmbedding(domainEvent.Payload))
			}
		case models.ResourceTypeTopic:
			if topic, ok := domainEvent.Payload.(models.Topic); ok {
				s.mutationMu.Lock()
				_ = s.store.UpdateTopicMembers(context.Background(), topic)
				s.mutationMu.Unlock()
			}
		}
	}
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Service) DeleteThought(ctx context.Context, thoughtID string) error {
	if s == nil || s.store == nil {
		return errors.New("search service is not ready")
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	return s.store.DeleteThought(ctx, thoughtID)
}

func (s *Service) IndexAsync(thoughtID string) (models.Job, error) {
	return s.IndexAsyncWithEmbedding(thoughtID, nil)
}

func (s *Service) IndexAsyncWithEmbedding(thoughtID string, embedding *models.EmbeddingRecord) (models.Job, error) {
	if strings.TrimSpace(thoughtID) == "" {
		return models.Job{}, errors.New("thought id is required")
	}
	return s.runner.StartLatest(models.JobTypeIndex, thoughtID, func(ctx context.Context, job models.Job) { s.indexJobContext(ctx, job, nil) })
}

func (s *Service) IndexThought(ctx context.Context, thoughtID string, embedding *models.EmbeddingRecord) error {
	if s.locker != nil {
		release, err := s.locker.Lease(ctx, thoughtID, indexerSessionID+":"+models.NewJobID("lease", time.Now()))
		if err != nil {
			return err
		}
		defer release()
	}
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()

	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return err
	}
	// Provider input and cache identity are independent of refinement payloads.
	if err := s.store.IndexThought(ctx, thought, content); err != nil {
		return err
	}
	record, err := s.embedding(ctx, thought, content)
	if err != nil {
		return err
	}
	if len(record.Vector) > 0 {
		if err := s.store.IndexEmbedding(ctx, record); err != nil {
			return err
		}
	}
	return markdown.UpdateThought(s.workspace.RootPath, thought.ID, func(current *models.Thought, _ *models.ThoughtContent) error {
		current.IndexStatus = models.IndexStatusIndexed
		return nil
	})
}

func (s *Service) ReindexWorkspace(ctx context.Context) (models.Job, error) {
	return s.runner.StartResource(models.JobTypeReindex, models.ResourceTypeWorkspace, s.workspace.ID, 1, func(ctx context.Context, job models.Job) { s.reindexJobContext(ctx, job) })
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
	s.stateMu.Lock()
	if s.rebuilding {
		status.Status = "rebuilding"
	}
	if s.rebuildError != "" {
		status.Status = "degraded"
		status.Error = s.rebuildError
	}
	s.stateMu.Unlock()
	status.Vector = s.store.VectorRuntimeStatus(ctx)
	if status.Vector.Status == "degraded" {
		status.Status = "degraded"
		status.Error = status.Vector.Error
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
		indexPath = "thoughtflow.duckdb"
	}
	if filepath.IsAbs(indexPath) || workspace == nil || strings.TrimSpace(workspace.RuntimePath) == "" {
		return indexPath
	}
	return filepath.Join(workspace.RuntimePath, filepath.FromSlash(indexPath))
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
	s.indexJobContext(context.Background(), job, embedding)
}
func (s *Service) indexJobContext(ctx context.Context, job models.Job, embedding *models.EmbeddingRecord) {
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	err := s.IndexThought(ctx, job.ResourceID, embedding)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		if errors.Is(err, os.ErrNotExist) {
			skipped, _ := s.jobs.MarkSucceeded(job, "skipped: thought is locked or deleted")
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, skipped))
			return
		}
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

func buildEmbeddingText(t models.Thought, c models.ThoughtContent) string {
	return models.EmbeddingText(t, c)
}

func (s *Service) embedding(ctx context.Context, t models.Thought, c models.ThoughtContent) (models.EmbeddingRecord, error) {
	if s.embedder == nil {
		return models.EmbeddingRecord{}, nil
	}
	text := buildEmbeddingText(t, c)
	if text == "" {
		return models.EmbeddingRecord{}, nil
	}
	hash := models.ContentHash(text)
	if spec, ok := s.embedder.(interface{ EmbeddingSpec() (string, int) }); ok {
		model, dimension := spec.EmbeddingSpec()
		if record, ok := s.store.GetEmbedding(ctx, t.ID, model); ok && model != "" && record.ContentHash == hash && record.Dimension == len(record.Vector) && len(record.Vector) > 0 && (dimension == 0 || dimension == record.Dimension) {
			return record, nil
		}
	}
	record, err := s.embedder.Embed(ctx, ai.EmbedRequest{ThoughtID: t.ID, Text: text})
	if err != nil {
		return record, err
	}
	record.ThoughtID = t.ID
	record.ContentHash = hash
	record.Dimension = len(record.Vector)
	if record.CreatedAt.IsZero() {
		record.CreatedAt = time.Now().UTC()
	}
	return record, nil
}

func (s *Service) reindexJob(job models.Job) { s.reindexJobContext(context.Background(), job) }
func (s *Service) reindexJobContext(ctx context.Context, job models.Job) {
	s.stateMu.Lock()
	s.rebuilding = true
	s.rebuildError = ""
	s.stateMu.Unlock()
	defer func() { s.stateMu.Lock(); s.rebuilding = false; s.stateMu.Unlock() }()
	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, searchEvent(models.EventSearchReindexStarted, s.workspace.ID, s.workspace.ID, job))
	count, embeddingCount, err := s.rebuild(ctx)
	if err != nil {
		if ctx.Err() != nil {
			return
		}
		s.stateMu.Lock()
		s.rebuildError = err.Error()
		s.stateMu.Unlock()
		ref := models.NewErrorRef("thoughtflow.search.reindex_failed", err.Error(), true)
		job, _ = s.jobs.MarkFailed(job, ref)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
		return
	}

	job.Message = "reindexed workspace"
	job, _ = s.jobs.MarkSucceeded(job, "reindexed workspace")
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	eventutil.Post(s.eventHub, searchEvent(models.EventSearchReindexFinished, s.workspace.ID, s.workspace.ID, map[string]any{"count": count, "embedding_count": embeddingCount, "job": job}))
}

func (s *Service) rebuild(ctx context.Context) (int, int, error) {
	s.mutationMu.Lock()
	defer s.mutationMu.Unlock()
	snapshots, err := searchdb.ReadWorkspace(s.workspace.RootPath)
	if err != nil {
		return 0, 0, err
	}
	records := []models.EmbeddingRecord{}
	for _, snapshot := range snapshots {
		record, err := s.embedding(ctx, snapshot.Thought, snapshot.Content)
		if err != nil {
			return 0, 0, err
		}
		if len(record.Vector) > 0 {
			records = append(records, record)
		}
	}
	return len(snapshots), len(records), s.store.ReplaceWorkspace(ctx, snapshots, records)
}

func (s *Service) Recover() error {
	if err := s.runner.Recover(models.JobTypeIndex, func(ctx context.Context, job models.Job) { s.indexJobContext(ctx, job, nil) }); err != nil {
		return err
	}
	if err := s.runner.Recover(models.JobTypeReindex, func(ctx context.Context, job models.Job) { s.reindexJobContext(ctx, job) }); err != nil {
		return err
	}
	_, err := s.ReindexWorkspace(context.Background())
	return err
}
func (s *Service) Close() { s.runner.Close() }

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

func (s *Service) Bind(ctx context.Context) { s.runner.Bind(ctx) }
