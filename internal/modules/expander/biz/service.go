package biz

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"
	"github.com/muidea/magicCommon/task"
	"golang.org/x/sync/errgroup"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/thoughtlock"
	"thoughtflow/internal/pkg/webfetch"
)

// Searcher is the surface the expander needs from the search
// service. The production binding calls search.Current().Search();
// tests substitute a mock to drive the related-thought stage
// deterministically.
type Searcher interface {
	Search(ctx context.Context, query models.SearchQuery) (models.SearchResponse, error)
}

// TopicSuggester is the surface the expander needs from the topic
// service. The production binding calls topic.Current().NearMissTopics();
// tests substitute a mock.
type TopicSuggester interface {
	NearMissTopics(ctx context.Context, thoughtID string, topK int, minScore float64) ([]models.TopicMatchSuggestion, error)
}

// Service runs the post-refine expansion: 4-way parallel pipeline
// that augments a thought with related-thought links, an LLM
// expansion plan, near-miss topic suggestions, and URL followup
// links. The pipeline is best-effort — any sub-stage failure is
// recorded as an ErrorRef but does not block the others.
type Service struct {
	workspace  *models.Workspace
	jobs       *jobstore.Store
	eventHub   event.Hub
	background task.BackgroundRoutine
	provider   ai.ExpandProvider
	fetcher    *webfetch.Fetcher
	locker     *thoughtlock.Locker

	// searcher and topicSuggester are looked up lazily on the first
	// job so the expander can be constructed before its peers
	// (search.weight=300, topic.weight=400) have finished Setup.
	searcher       Searcher
	topicSuggester TopicSuggester
	searcherFn     func() Searcher
	topicFn        func() TopicSuggester
}

// expanderSessionID is the well-known lock holder for the expansion
// pipeline. Sharing the same identity as the refiner signals to the
// capture module that "the AI is processing" — the user can still
// see the lock state but the lock TTL applies uniformly.
const expanderSessionID = "expander"

const expandTopKRelated = 3
const expandTopKNearTopics = 3
const expandTimeout = 30 * time.Second

func NewService(workspace *models.Workspace, jobs *jobstore.Store, eventHub event.Hub, background task.BackgroundRoutine, provider ai.ExpandProvider, fetcher *webfetch.Fetcher, locker *thoughtlock.Locker) *Service {
	return &Service{
		workspace:  workspace,
		jobs:       jobs,
		eventHub:   eventHub,
		background: background,
		provider:   provider,
		fetcher:    fetcher,
		locker:     locker,
	}
}

// SetSearcherLookup registers a function that returns the search
// service. The lookup is called lazily on the first job so it can
// resolve to a peer module that has not yet been wired up.
func (s *Service) SetSearcherLookup(fn func() Searcher) {
	s.searcherFn = fn
}

// SetTopicSuggesterLookup registers a function that returns the
// topic suggester. See SetSearcherLookup for the lazy-resolution
// rationale.
func (s *Service) SetTopicSuggesterLookup(fn func() TopicSuggester) {
	s.topicFn = fn
}

func (s *Service) resolveSearcher() Searcher {
	if s.searcher != nil {
		return s.searcher
	}
	if s.searcherFn != nil {
		s.searcher = s.searcherFn()
	}
	return s.searcher
}

func (s *Service) resolveTopicSuggester() TopicSuggester {
	if s.topicSuggester != nil {
		return s.topicSuggester
	}
	if s.topicFn != nil {
		s.topicSuggester = s.topicFn()
	}
	return s.topicSuggester
}

func (s *Service) ID() string {
	return "expander.thought-refined-observer"
}

func (s *Service) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if !ok {
		if result != nil {
			result.Set(nil, nil)
		}
		return
	}
	if domainEvent.ResourceID != "" {
		if _, err := s.ExpandAsync(domainEvent.ResourceID); err != nil {
			// Best-effort: a failed dispatch is logged via the job
			// failure path, no need to spam the result.
			_ = err
		}
	}
	if result != nil {
		result.Set(nil, nil)
	}
}

// ExpandAsync creates a `expand` job and dispatches the background
// expansion. It returns the created job so the caller can surface
// the job ID to clients that want to track the pipeline.
func (s *Service) ExpandAsync(thoughtID string) (models.Job, error) {
	if strings.TrimSpace(thoughtID) == "" {
		return models.Job{}, errors.New("thought id is required")
	}
	job, err := s.jobs.Create(models.JobTypeExpand, models.ResourceTypeThought, thoughtID, "expand queued")
	if err != nil {
		return models.Job{}, err
	}
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))
	run := func() {
		s.expandJob(job)
	}
	if s.background != nil {
		if err := s.background.AsyncFunction(run); err == nil {
			return job, nil
		}
	}
	go run()
	return job, nil
}

func (s *Service) expandJob(job models.Job) {
	if s.locker != nil {
		if err := s.locker.Acquire(job.ResourceID, expanderSessionID); err != nil {
			skipped, _ := s.jobs.MarkSucceeded(job, "skipped: thought is locked by an active session")
			eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, skipped))
			return
		}
		defer s.locker.Release(job.ResourceID, expanderSessionID)
	}

	thought, content, err := markdown.ReadThought(s.workspace.RootPath, job.ResourceID)
	if err != nil {
		errRef := models.NewErrorRef("thoughtflow.expand.read_failed", err.Error(), true)
		failed, _ := s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, failed))
		return
	}

	job, _ = s.jobs.MarkRunning(job)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, job))

	related, plan, nearTopics, followups, groupErr := s.runPipeline(context.Background(), thought, content)
	if groupErr != nil {
		// Soft failure: log to the thought's error list and continue
		// persisting whatever partial result we have.
		thought.Errors = replaceErrorRef(thought.Errors, models.NewErrorRef("thoughtflow.expand.partial_failed", groupErr.Error(), false))
	}

	thought.RelatedThoughtIDs = related
	thought.ExpansionPlan = plan
	thought.SuggestedTopicIDs = topicIDsFromSuggestions(nearTopics)
	thought.URLFollowups = followups
	thought.UpdatedAt = time.Now().UTC()

	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		errRef := models.NewErrorRef("thoughtflow.expand.write_failed", err.Error(), true)
		failed, _ := s.jobs.MarkFailed(job, errRef)
		eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, failed))
		return
	}

	message := "expand succeeded"
	if groupErr != nil {
		message = "expand partial: " + groupErr.Error()
	}
	succeeded, _ := s.jobs.MarkSucceeded(job, message)
	eventutil.Post(s.eventHub, jobEvent(s.workspace.ID, succeeded))
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:      models.EventThoughtExpanded,
		SourceUnit:     "expander",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    s.workspace.ID,
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thought.ID,
		PayloadVersion: 1,
		Payload:        expansionPayload(thought),
	})
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:    models.EventGitCommitRequested,
		SourceUnit:   "expander",
		OccurredAt:   time.Now().UTC(),
		WorkspaceID:  s.workspace.ID,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   thought.ID,
		Payload: models.GitCommitRequestedPayload{
			Paths:       []string{thought.Path},
			Reason:      "expand",
			ResourceIDs: []string{thought.ID},
		},
		PayloadVersion: 1,
	})
}

// runPipeline runs the 4 sub-stages in parallel under a shared
// timeout. The first error from any stage is reported; the others
// continue so the partial result can be persisted. The returned
// values are always safe to use (defaults are nil/empty).
func (s *Service) runPipeline(ctx context.Context, thought models.Thought, content models.ThoughtContent) ([]string, string, []models.TopicMatchSuggestion, []models.URLFollowup, error) {
	ctx, cancel := context.WithTimeout(ctx, expandTimeout)
	defer cancel()

	g, gctx := errgroup.WithContext(ctx)

	var related []string
	var plan string
	var nearTopics []models.TopicMatchSuggestion
	var followups []models.URLFollowup

	g.Go(func() error {
		r, err := s.expandRelated(gctx, thought, content)
		related = r
		if err != nil {
			return fmt.Errorf("related: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		p, err := s.expandLLM(gctx, thought, content)
		plan = p
		if err != nil {
			return fmt.Errorf("llm: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		n, err := s.expandNearTopics(gctx, thought.ID)
		nearTopics = n
		if err != nil {
			return fmt.Errorf("near_topics: %w", err)
		}
		return nil
	})
	g.Go(func() error {
		f, err := s.expandURLFollowups(gctx, thought)
		followups = f
		if err != nil {
			return fmt.Errorf("url_followups: %w", err)
		}
		return nil
	})

	groupErr := g.Wait()
	return related, plan, nearTopics, followups, groupErr
}

func (s *Service) expandRelated(ctx context.Context, thought models.Thought, content models.ThoughtContent) ([]string, error) {
	searchSvc := s.resolveSearcher()
	if searchSvc == nil {
		return nil, nil
	}
	query := strings.TrimSpace(thought.UserTitle + " " + thought.Summary + " " + firstLine(content.Original))
	if query == "" {
		return nil, nil
	}
	resp, err := searchSvc.Search(ctx, models.SearchQuery{
		Query:    query,
		Mode:     "hybrid",
		PageSize: expandTopKRelated + 1,
		Page:     1,
	})
	if err != nil {
		return nil, err
	}
	ids := []string{}
	seen := map[string]bool{thought.ID: true}
	for _, item := range resp.Items {
		if item.ThoughtID == "" || seen[item.ThoughtID] {
			continue
		}
		seen[item.ThoughtID] = true
		ids = append(ids, item.ThoughtID)
		if len(ids) >= expandTopKRelated {
			break
		}
	}
	return ids, nil
}

func (s *Service) expandLLM(ctx context.Context, thought models.Thought, content models.ThoughtContent) (string, error) {
	if s.provider == nil {
		return "", nil
	}
	result, err := s.provider.Expand(ctx, ai.ExpandRequest{
		Thought: thought,
		Content: content,
		Summary: thought.Summary,
		Tags:    append([]string{}, thought.AITags...),
	})
	if err != nil {
		return "", err
	}
	return result.Plan, nil
}

func (s *Service) expandNearTopics(ctx context.Context, thoughtID string) ([]models.TopicMatchSuggestion, error) {
	topicSvc := s.resolveTopicSuggester()
	if topicSvc == nil {
		return nil, nil
	}
	return topicSvc.NearMissTopics(ctx, thoughtID, expandTopKNearTopics, 0.4)
}

func (s *Service) expandURLFollowups(ctx context.Context, thought models.Thought) ([]models.URLFollowup, error) {
	if thought.Type != models.ThoughtTypeURL || strings.TrimSpace(thought.URL) == "" {
		return nil, nil
	}
	if s.fetcher == nil {
		return nil, nil
	}
	result, err := s.fetcher.Fetch(ctx, thought.URL)
	if err != nil {
		return nil, err
	}
	return pickFollowups(result.Links, 2), nil
}

func pickFollowups(links []webfetch.Link, topK int) []models.URLFollowup {
	if len(links) == 0 {
		return nil
	}
	limit := topK
	if limit > len(links) {
		limit = len(links)
	}
	out := make([]models.URLFollowup, 0, limit)
	for _, link := range links[:limit] {
		out = append(out, models.URLFollowup{URL: link.URL, Title: link.Title})
	}
	return out
}

func topicIDsFromSuggestions(suggestions []models.TopicMatchSuggestion) []string {
	if len(suggestions) == 0 {
		return nil
	}
	ids := make([]string, 0, len(suggestions))
	for _, suggestion := range suggestions {
		if strings.TrimSpace(suggestion.TopicID) == "" {
			continue
		}
		ids = append(ids, suggestion.TopicID)
	}
	return ids
}

func expansionPayload(thought models.Thought) map[string]any {
	return map[string]any{
		"thought_id":       thought.ID,
		"related_thoughts": thought.RelatedThoughtIDs,
		"expansion_plan":   thought.ExpansionPlan,
		"suggested_topics": thought.SuggestedTopicIDs,
		"url_followups":    thought.URLFollowups,
	}
}

func firstLine(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if idx := strings.Index(value, "\n"); idx >= 0 {
		return strings.TrimSpace(value[:idx])
	}
	return value
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

func jobEvent(workspaceID string, job models.Job) models.DomainEvent {
	return models.DomainEvent{
		EventType:      models.EventJobUpdated,
		SourceUnit:     "expander",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   job.ResourceType,
		ResourceID:     job.ResourceID,
		PayloadVersion: 1,
		Payload:        job,
	}
}
