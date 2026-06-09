package service

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	engine "github.com/muidea/magicEngine/http"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	refinerbiz "thoughtflow/internal/modules/refiner/biz"
	searchbiz "thoughtflow/internal/modules/search/biz"
	topicbiz "thoughtflow/internal/modules/topic/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/observability"
	"thoughtflow/internal/pkg/synthesisstore"
)

//go:embed web/*
var webAssets embed.FS

var errSynthesisThoughtIDsRequired = errors.New("thought_ids is required")

type Service struct {
	registry       engine.RouteRegistry
	captureService *capturebiz.Service
	refinerService *refinerbiz.Service
	searchService  *searchbiz.Service
	topicService   *topicbiz.Service
	gitQueries     gitQueryReader
	jobs           *jobstore.Store
	stream         *eventstream.Stream
	workspace      *models.Workspace
	synthesisStore *synthesisstore.Store
	synthesisAI    ai.SynthesisProvider
}

type gitQueryReader interface {
	RecentCommits(ctx context.Context, relativePath string, resourceID string, limit int) []models.GitCommitRecord
	RuntimeStatus(ctx context.Context) models.GitRuntimeStatus
}

func New(registry engine.RouteRegistry, captureService *capturebiz.Service, refinerService *refinerbiz.Service, searchService *searchbiz.Service, topicService *topicbiz.Service, gitQueries gitQueryReader, jobs *jobstore.Store, stream *eventstream.Stream, workspace *models.Workspace) *Service {
	return &Service{
		registry:       registry,
		captureService: captureService,
		refinerService: refinerService,
		searchService:  searchService,
		topicService:   topicService,
		gitQueries:     gitQueries,
		jobs:           jobs,
		stream:         stream,
		workspace:      workspace,
		synthesisStore: newSynthesisStore(workspace),
		synthesisAI:    ai.NewSynthesisProvider(appconfig.Load().AI),
	}
}

func (s *Service) RegisterRoutes() {
	s.registry.AddHandler("/", engine.GET, s.handleWeb)
	s.registry.AddHandler("/index.html", engine.GET, s.handleWeb)
	s.registry.AddHandler("/styles.css", engine.GET, s.handleWeb)
	s.registry.AddHandler("/app.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/vendor/markdown-it.min.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/vendor/markdown-it.LICENSE", engine.GET, s.handleWeb)
	s.registry.AddHandler("/api/thoughts", engine.POST, s.handleCreateThought)
	s.registry.AddHandler("/api/thoughts/:id/retry-refine", engine.POST, s.handleRetryRefine)
	s.registry.AddHandler("/api/thoughts/:id", engine.GET, s.handleGetThought)
	s.registry.AddHandler("/api/search", engine.GET, s.handleSearch)
	s.registry.AddHandler("/api/synthesis/save", engine.POST, s.handleSaveSynthesis)
	s.registry.AddHandler("/api/synthesis/:id", engine.GET, s.handleGetSynthesisDraft)
	s.registry.AddHandler("/api/synthesis", engine.GET, s.handleListSynthesisDrafts)
	s.registry.AddHandler("/api/synthesis", engine.POST, s.handleSynthesis)
	s.registry.AddHandler("/api/topics", engine.GET, s.handleListTopics)
	s.registry.AddHandler("/api/topics", engine.POST, s.handleCreateTopic)
	s.registry.AddHandler("/api/topics/:id/rebuild", engine.POST, s.handleRebuildTopic)
	s.registry.AddHandler("/api/topics/:id/weave-preview", engine.POST, s.handlePreviewWeave)
	s.registry.AddHandler("/api/topics/:id/weave-accept", engine.POST, s.handleAcceptWeave)
	s.registry.AddHandler("/api/topics/:id/weave-proposals/:proposal_id", engine.GET, s.handleGetWeaveProposal)
	s.registry.AddHandler("/api/topics/:id/weave-proposals", engine.GET, s.handleListWeaveProposals)
	s.registry.AddHandler("/api/topics/:id", engine.GET, s.handleGetTopic)
	s.registry.AddHandler("/api/topics/:id", engine.PUT, s.handleUpdateTopic)
	s.registry.AddHandler("/api/jobs/:id", engine.GET, s.handleGetJob)
	s.registry.AddHandler("/api/events", engine.GET, s.handleEvents)
	s.registry.AddHandler("/api/system/status", engine.GET, s.handleSystemStatus)
	s.registry.AddHandler("/api/system/metrics", engine.GET, s.handleSystemMetrics)
	s.registry.AddHandler("/api/system/reindex", engine.POST, s.handleReindex)
	s.registry.AddHandler("/metrics", engine.GET, s.handlePrometheusMetrics)
	s.registry.AddHandler("/health/live", engine.GET, s.handleLive)
	s.registry.AddHandler("/health/ready", engine.GET, s.handleReady)
}

func (s *Service) handleWeb(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	filePath := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
	if filePath == "." || filePath == "" {
		filePath = "index.html"
	}
	if strings.Contains(filePath, "..") {
		http.NotFound(res, req)
		return
	}
	raw, err := webAssets.ReadFile(path.Join("web", filePath))
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(res, req)
		return
	}
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.ui.asset_failed", err.Error())
		return
	}
	switch path.Ext(filePath) {
	case ".css":
		res.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		res.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case ".LICENSE":
		res.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		res.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	res.WriteHeader(http.StatusOK)
	_, _ = res.Write(raw)
}

func (s *Service) handleCreateThought(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var cmd models.CaptureCommand
	if err := json.NewDecoder(req.Body).Decode(&cmd); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
		return
	}
	result, err := s.captureService.Capture(ctx, cmd)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, result)
}

func (s *Service) handleGetThought(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	thoughtID := pathID(req.URL.Path, "/api/thoughts/")
	if thoughtID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", "thought id is required")
		return
	}
	snapshot, err := s.captureService.GetThought(ctx, thoughtID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			status = http.StatusNotFound
		}
		writeError(res, req, status, "thoughtflow.capture.not_found", err.Error())
		return
	}
	snapshot.Jobs = thoughtJobs(s.jobs, thoughtID, 20)
	if s.gitQueries != nil {
		snapshot.GitCommits = s.gitQueries.RecentCommits(ctx, snapshot.Thought.Path, thoughtID, 5)
	}
	writeJSON(res, req, http.StatusOK, snapshot)
}

func (s *Service) handleRetryRefine(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	thoughtID := strings.TrimSuffix(pathID(req.URL.Path, "/api/thoughts/"), "/retry-refine")
	if thoughtID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.refiner.invalid_request", "thought id is required")
		return
	}
	job, err := s.refinerService.RefineAsync(thoughtID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.refiner.invalid_request", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, job)
}

func thoughtJobs(store *jobstore.Store, thoughtID string, limit int) []models.Job {
	if store == nil || strings.TrimSpace(thoughtID) == "" {
		return nil
	}
	jobs, err := store.List()
	if err != nil {
		return nil
	}
	ret := []models.Job{}
	for _, job := range jobs {
		if job.ResourceID == thoughtID {
			ret = append(ret, job)
		}
	}
	sort.Slice(ret, func(left, right int) bool {
		if ret[left].CreatedAt.Equal(ret[right].CreatedAt) {
			return ret[left].ID > ret[right].ID
		}
		return ret[left].CreatedAt.After(ret[right].CreatedAt)
	})
	if limit > 0 && len(ret) > limit {
		ret = ret[:limit]
	}
	return ret
}

func (s *Service) handleSearch(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	observability.IncrementSearchQuery()
	query := req.URL.Query()
	from, err := timeQuery(query.Get("from"), false)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.search.invalid_request", "from must be RFC3339 or YYYY-MM-DD")
		return
	}
	to, err := timeQuery(query.Get("to"), true)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.search.invalid_request", "to must be RFC3339 or YYYY-MM-DD")
		return
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.search.invalid_request", "from must be before to")
		return
	}
	searchQuery := models.SearchQuery{
		Query:    query.Get("q"),
		Mode:     firstNonEmpty(query.Get("mode"), "hybrid"),
		Sort:     query.Get("sort"),
		TopicID:  query.Get("topic_id"),
		Tags:     splitCSV(query.Get("tags")),
		From:     from,
		To:       to,
		Page:     intQuery(query.Get("page"), 1),
		PageSize: intQuery(query.Get("page_size"), 20),
		Explain:  boolQuery(query.Get("explain")),
		Weights: models.SearchWeights{
			Keyword:  floatQuery(firstNonEmpty(query.Get("keyword_weight"), query.Get("weight_keyword")), 0),
			Semantic: floatQuery(firstNonEmpty(query.Get("semantic_weight"), query.Get("weight_semantic")), 0),
			Recency:  floatQuery(firstNonEmpty(query.Get("recency_weight"), query.Get("weight_recency")), 0),
		},
	}
	result, err := s.searchService.Search(ctx, searchQuery)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.search.query_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, result)
}

func (s *Service) handleSynthesis(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var request models.SynthesisRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_json", err.Error())
		return
	}
	if len(request.ThoughtIDs) == 0 {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "thought_ids is required")
		return
	}
	snapshots, sourceLinks, err := s.synthesisSources(ctx, request.ThoughtIDs)
	if err != nil {
		writeSynthesisSourceError(res, req, err)
		return
	}
	thoughtIDs := make([]string, 0, len(snapshots))
	for _, snapshot := range snapshots {
		thoughtIDs = append(thoughtIDs, snapshot.Thought.ID)
	}
	provider := s.getSynthesisProvider()
	draft, err := provider.Synthesize(ctx, ai.SynthesisRequest{
		ThoughtIDs:  thoughtIDs,
		Goal:        request.Goal,
		Format:      request.Format,
		Snapshots:   snapshots,
		SourceLinks: sourceLinks,
	})
	if err != nil {
		writeError(res, req, http.StatusBadGateway, "thoughtflow.synthesis.generate_failed", err.Error())
		return
	}
	if store := s.getSynthesisStore(); store != nil {
		draft, err = store.SaveDraft(ctx, draft)
		if err != nil {
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.draft_failed", err.Error())
			return
		}
	}
	writeJSON(res, req, http.StatusOK, draft)
}

func (s *Service) handleListSynthesisDrafts(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	store := s.getSynthesisStore()
	if store == nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.store_unavailable", "synthesis draft store is unavailable")
		return
	}
	drafts, err := store.ListDrafts(ctx)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.list_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, drafts)
}

func (s *Service) handleGetSynthesisDraft(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	draftID := pathID(req.URL.Path, "/api/synthesis/")
	if draftID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "draft id is required")
		return
	}
	store := s.getSynthesisStore()
	if store == nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.store_unavailable", "synthesis draft store is unavailable")
		return
	}
	draft, err := store.GetDraft(ctx, draftID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.synthesis.draft_not_found", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, draft)
}

func (s *Service) handleSaveSynthesis(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var request models.SynthesisSaveRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_json", err.Error())
		return
	}
	store := s.getSynthesisStore()
	if store != nil && strings.TrimSpace(request.DraftID) != "" {
		draft, err := store.GetDraft(ctx, request.DraftID)
		if err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.draft_not_found", err.Error())
			return
		}
		if len(request.ThoughtIDs) == 0 {
			request.ThoughtIDs = draft.ThoughtIDs
		}
		if strings.TrimSpace(request.Goal) == "" {
			request.Goal = draft.Goal
		}
		if strings.TrimSpace(request.Format) == "" {
			request.Format = draft.Format
		}
		if strings.TrimSpace(request.Content) == "" {
			request.Content = draft.Content
		}
	}
	if len(request.ThoughtIDs) == 0 {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "thought_ids is required")
		return
	}
	if strings.TrimSpace(request.Content) == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "content is required")
		return
	}
	_, sourceLinks, err := s.synthesisSources(ctx, request.ThoughtIDs)
	if err != nil {
		writeSynthesisSourceError(res, req, err)
		return
	}
	format := firstNonEmpty(request.Format, "summary")
	title := firstNonEmpty(request.Title, request.Goal, "Synthesis draft")
	content := appendSynthesisSourceLinks(request.Content, sourceLinks)
	tags := []string{"synthesis"}
	if format != "" {
		tags = append(tags, format)
	}
	result, err := s.captureService.Capture(ctx, models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Source:  models.ThoughtSourceSynthesis,
		Title:   title,
		Content: content,
		Tags:    tags,
	})
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.save_failed", err.Error())
		return
	}
	if store != nil && strings.TrimSpace(request.DraftID) != "" {
		if _, err := store.MarkSaved(ctx, request.DraftID, content, result.Thought); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.draft_save_failed", err.Error())
			return
		}
	}
	writeJSON(res, req, http.StatusAccepted, models.SynthesisSaveResult{
		Thought:     result.Thought,
		Jobs:        result.Jobs,
		SourceLinks: sourceLinks,
	})
}

func (s *Service) getSynthesisStore() *synthesisstore.Store {
	if s.synthesisStore != nil {
		return s.synthesisStore
	}
	s.synthesisStore = newSynthesisStore(s.workspace)
	return s.synthesisStore
}

func newSynthesisStore(workspace *models.Workspace) *synthesisstore.Store {
	if workspace == nil || strings.TrimSpace(workspace.RootPath) == "" {
		return nil
	}
	return synthesisstore.New(workspace.RootPath)
}

func (s *Service) getSynthesisProvider() ai.SynthesisProvider {
	if s.synthesisAI != nil {
		return s.synthesisAI
	}
	s.synthesisAI = ai.NewSynthesisProvider(appconfig.Load().AI)
	return s.synthesisAI
}

func (s *Service) synthesisSources(ctx context.Context, thoughtIDs []string) ([]models.ThoughtSnapshot, []string, error) {
	snapshots := []models.ThoughtSnapshot{}
	sourceLinks := []string{}
	seen := map[string]struct{}{}
	for _, thoughtID := range thoughtIDs {
		thoughtID = strings.TrimSpace(thoughtID)
		if thoughtID == "" {
			continue
		}
		if _, ok := seen[thoughtID]; ok {
			continue
		}
		seen[thoughtID] = struct{}{}
		snapshot, err := s.captureService.GetThought(ctx, thoughtID)
		if err != nil {
			return nil, nil, err
		}
		snapshots = append(snapshots, snapshot)
		sourceLinks = append(sourceLinks, snapshot.Thought.Path)
	}
	if len(snapshots) == 0 {
		return nil, nil, errSynthesisThoughtIDsRequired
	}
	return snapshots, sourceLinks, nil
}

func writeSynthesisSourceError(res http.ResponseWriter, req *http.Request, err error) {
	if errors.Is(err, errSynthesisThoughtIDsRequired) {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", err.Error())
		return
	}
	writeError(res, req, http.StatusNotFound, "thoughtflow.synthesis.thought_not_found", err.Error())
}

func appendSynthesisSourceLinks(content string, sourceLinks []string) string {
	content = strings.TrimSpace(content)
	missing := []string{}
	for _, link := range sourceLinks {
		link = strings.TrimSpace(link)
		if link == "" || strings.Contains(content, link) {
			continue
		}
		missing = append(missing, link)
	}
	if len(missing) == 0 {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	if strings.Contains(strings.ToLower(content), "### sources") {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n### Sources\n\n")
	}
	for _, link := range missing {
		builder.WriteString("- [[")
		builder.WriteString(link)
		builder.WriteString("]]\n")
	}
	return strings.TrimSpace(builder.String())
}

func (s *Service) handleListTopics(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topics, err := s.topicService.ListTopics(ctx)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.topic.list_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, topics)
}

func (s *Service) handleCreateTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var request models.TopicCreateRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	topic, err := s.topicService.CreateTopic(ctx, request)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", err.Error())
		return
	}
	writeJSON(res, req, http.StatusCreated, topic)
}

func (s *Service) handleGetTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := pathID(req.URL.Path, "/api/topics/")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	detail, err := s.topicService.GetTopic(ctx, topicID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.topic.not_found", err.Error())
		return
	}
	detail.Activities = topicActivities(s.stream, topicID, 20)
	writeJSON(res, req, http.StatusOK, detail)
}

func topicActivities(stream *eventstream.Stream, topicID string, limit int) []models.DomainEvent {
	if stream == nil || strings.TrimSpace(topicID) == "" {
		return nil
	}
	history := stream.History()
	activities := []models.DomainEvent{}
	for idx := len(history) - 1; idx >= 0; idx-- {
		item := history[idx]
		if !isTopicActivity(item, topicID) {
			continue
		}
		activities = append(activities, item)
		if limit > 0 && len(activities) >= limit {
			break
		}
	}
	for left, right := 0, len(activities)-1; left < right; left, right = left+1, right-1 {
		activities[left], activities[right] = activities[right], activities[left]
	}
	return activities
}

func isTopicActivity(event models.DomainEvent, topicID string) bool {
	if event.ResourceType == models.ResourceTypeTopic && event.ResourceID == topicID {
		return true
	}
	if strings.HasPrefix(event.EventType, "topic.") && payloadReferencesTopic(event.Payload, topicID) {
		return true
	}
	return false
}

func payloadReferencesTopic(payload any, topicID string) bool {
	switch value := payload.(type) {
	case models.Topic:
		return value.ID == topicID
	case *models.Topic:
		return value != nil && value.ID == topicID
	case models.TopicMembership:
		return value.TopicID == topicID
	case *models.TopicMembership:
		return value != nil && value.TopicID == topicID
	case []models.TopicMembership:
		for _, membership := range value {
			if membership.TopicID == topicID {
				return true
			}
		}
	case []*models.TopicMembership:
		for _, membership := range value {
			if membership != nil && membership.TopicID == topicID {
				return true
			}
		}
	case map[string]any:
		if topicValue, ok := value["topic"]; ok && payloadReferencesTopic(topicValue, topicID) {
			return true
		}
		if topicIDValue, ok := value["topic_id"].(string); ok && topicIDValue == topicID {
			return true
		}
	}
	return false
}

func (s *Service) handleUpdateTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := pathID(req.URL.Path, "/api/topics/")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	var request models.TopicUpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	topic, err := s.topicService.UpdateTopic(ctx, topicID, request)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.update_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, topic)
}

func (s *Service) handleRebuildTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/rebuild")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	job, err := s.topicService.RebuildTopic(req.Context(), topicID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.rebuild_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, job)
}

func (s *Service) handlePreviewWeave(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/weave-preview")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	var request models.TopicWeavePreviewRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	proposal, err := s.topicService.PreviewWeave(ctx, topicID, request.ThoughtID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.weave_preview_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, proposal)
}

func (s *Service) handleAcceptWeave(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/weave-accept")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	var request models.TopicWeaveAcceptRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	detail, err := s.topicService.AcceptWeave(ctx, topicID, request)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.weave_accept_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, detail)
}

func (s *Service) handleListWeaveProposals(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/weave-proposals")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	proposals, err := s.topicService.ListWeaveProposals(ctx, topicID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.weave_proposals_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, proposals)
}

func (s *Service) handleGetWeaveProposal(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID, proposalID := topicWeaveProposalPath(req.URL.Path)
	if topicID == "" || proposalID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id and proposal id are required")
		return
	}
	proposal, err := s.topicService.GetWeaveProposal(ctx, topicID, proposalID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.topic.weave_proposal_not_found", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, proposal)
}

func (s *Service) handleGetJob(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	jobID := pathID(req.URL.Path, "/api/jobs/")
	if jobID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.system.invalid_request", "job id is required")
		return
	}
	job, err := s.jobs.Get(jobID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.system.job_not_found", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, job)
}

func (s *Service) handleEvents(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	res.Header().Set("Content-Type", "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("Connection", "keep-alive")

	flusher, ok := res.(http.Flusher)
	if !ok {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.system.sse_unavailable", "streaming is not supported")
		return
	}
	events := s.stream.SubscribeWithOptions(req.Context(), eventstream.SubscribeOptions{
		LastEventID: req.Header.Get("Last-Event-ID"),
		Types:       splitCSV(req.URL.Query().Get("types")),
	})
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-req.Context().Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			raw, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(res, "event: %s\nid: %s\ndata: %s\n\n", ev.EventType, ev.EventID, raw)
			flusher.Flush()
		case <-ticker.C:
			_, _ = fmt.Fprint(res, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Service) handleSystemStatus(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	cfg := appconfig.Load()
	status := s.systemStatus(ctx, cfg)
	writeJSON(res, req, http.StatusOK, status)
}

func (s *Service) handleSystemMetrics(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	metrics, err := s.systemMetrics(ctx, time.Now().UTC())
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.system.metrics_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, metrics)
}

func (s *Service) handlePrometheusMetrics(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	metrics, err := s.systemMetrics(ctx, time.Now().UTC())
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.system.metrics_failed", err.Error())
		return
	}
	_ = req
	res.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	res.WriteHeader(http.StatusOK)
	_, _ = res.Write([]byte(renderPrometheusMetrics(metrics)))
}

func (s *Service) systemMetrics(ctx context.Context, now time.Time) (models.SystemMetrics, error) {
	_ = ctx
	counters := observability.Snapshot()
	jobs := []models.Job{}
	if s.jobs != nil {
		listed, err := s.jobs.List()
		if err != nil {
			return models.SystemMetrics{}, err
		}
		jobs = listed
	}
	thoughts, err := listWorkspaceThoughts(s.workspace)
	if err != nil {
		return models.SystemMetrics{}, err
	}

	captureTotal := len(thoughts)
	gitCommitTotal := 0
	for _, job := range jobs {
		if job.Type == models.JobTypeGitCommit && job.Status == models.JobStatusSucceeded {
			gitCommitTotal++
		}
	}
	refineDuration := refineDurationMetric(jobs)
	backgroundJobs := backgroundJobsMetric(jobs)
	indexLag := thoughtIndexLagMetric(thoughts, now)
	values := map[string]float64{
		"thoughtflow_capture_total":           float64(captureTotal),
		"thoughtflow_refine_duration_seconds": refineDuration.Average,
		"thoughtflow_ai_request_total":        float64(counters.AIRequestTotal),
		"thoughtflow_search_query_total":      float64(counters.SearchQueryTotal),
		"thoughtflow_index_lag_seconds":       indexLag.Seconds,
		"thoughtflow_topic_weave_total":       float64(counters.TopicWeaveTotal),
		"thoughtflow_git_commit_total":        float64(gitCommitTotal),
		"thoughtflow_background_jobs":         float64(backgroundJobs.Total),
	}
	return models.SystemMetrics{
		GeneratedAt:           now,
		Values:                values,
		RefineDurationSeconds: refineDuration,
		BackgroundJobs:        backgroundJobs,
		ThoughtIndexLag:       indexLag,
	}, nil
}

func listWorkspaceThoughts(ws *models.Workspace) ([]models.Thought, error) {
	if ws == nil || strings.TrimSpace(ws.RootPath) == "" {
		return []models.Thought{}, nil
	}
	thoughtsPath := filepath.Join(ws.RootPath, "thoughts")
	thoughts := []models.Thought{}
	err := filepath.WalkDir(thoughtsPath, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(filePath) != ".md" {
			return nil
		}
		thoughtID := strings.TrimSuffix(filepath.Base(filePath), ".md")
		thought, _, err := markdown.ReadThought(ws.RootPath, thoughtID)
		if err != nil {
			return err
		}
		thoughts = append(thoughts, thought)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return []models.Thought{}, nil
	}
	return thoughts, err
}

func refineDurationMetric(jobs []models.Job) models.DurationMetric {
	metric := models.DurationMetric{}
	for _, job := range jobs {
		if job.Type != models.JobTypeRefine || job.StartedAt == nil || job.FinishedAt == nil {
			continue
		}
		duration := job.FinishedAt.Sub(*job.StartedAt).Seconds()
		if duration < 0 {
			continue
		}
		metric.Count++
		metric.Sum += duration
		metric.Latest = duration
	}
	if metric.Count > 0 {
		metric.Average = metric.Sum / float64(metric.Count)
	}
	return metric
}

func backgroundJobsMetric(jobs []models.Job) models.BackgroundJobsMetric {
	metric := models.BackgroundJobsMetric{
		Total:    len(jobs),
		ByStatus: map[string]int{},
		ByType:   map[string]int{},
	}
	for _, job := range jobs {
		if job.Status != "" {
			metric.ByStatus[job.Status]++
		}
		if job.Type != "" {
			metric.ByType[job.Type]++
		}
	}
	return metric
}

func thoughtIndexLagMetric(thoughts []models.Thought, now time.Time) models.ThoughtIndexLagMetric {
	metric := models.ThoughtIndexLagMetric{}
	for _, thought := range thoughts {
		switch thought.IndexStatus {
		case models.IndexStatusIndexed:
			continue
		case models.IndexStatusFailed:
			metric.FailedThoughts++
		default:
			metric.PendingThoughts++
		}
		updatedAt := thought.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = thought.CreatedAt
		}
		if updatedAt.IsZero() {
			continue
		}
		lag := now.Sub(updatedAt).Seconds()
		if lag > metric.Seconds {
			metric.Seconds = lag
		}
	}
	return metric
}

func renderPrometheusMetrics(metrics models.SystemMetrics) string {
	var builder strings.Builder
	writePrometheusSample(&builder, "thoughtflow_capture_total", "counter", "Total captured thoughts.", metrics.Values["thoughtflow_capture_total"])
	writePrometheusSample(&builder, "thoughtflow_refine_duration_seconds", "gauge", "Average thought refinement duration in seconds.", metrics.Values["thoughtflow_refine_duration_seconds"])
	writePrometheusSample(&builder, "thoughtflow_ai_request_total", "counter", "Total AI provider requests.", metrics.Values["thoughtflow_ai_request_total"])
	writePrometheusSample(&builder, "thoughtflow_search_query_total", "counter", "Total search queries.", metrics.Values["thoughtflow_search_query_total"])
	writePrometheusSample(&builder, "thoughtflow_index_lag_seconds", "gauge", "Maximum lag for non-indexed thoughts in seconds.", metrics.Values["thoughtflow_index_lag_seconds"])
	writePrometheusSample(&builder, "thoughtflow_topic_weave_total", "counter", "Total topic weave operations.", metrics.Values["thoughtflow_topic_weave_total"])
	writePrometheusSample(&builder, "thoughtflow_git_commit_total", "counter", "Total successful git commit jobs.", metrics.Values["thoughtflow_git_commit_total"])
	writePrometheusSample(&builder, "thoughtflow_background_jobs", "gauge", "Total persisted background jobs.", metrics.Values["thoughtflow_background_jobs"])
	writePrometheusSample(&builder, "thoughtflow_refine_duration_seconds_sum", "counter", "Total refinement duration in seconds.", metrics.RefineDurationSeconds.Sum)
	writePrometheusSample(&builder, "thoughtflow_refine_duration_seconds_count", "counter", "Total completed refinement jobs.", float64(metrics.RefineDurationSeconds.Count))
	for status, count := range metrics.BackgroundJobs.ByStatus {
		writePrometheusLabeledSample(&builder, "thoughtflow_background_jobs", map[string]string{"status": status}, float64(count))
	}
	for jobType, count := range metrics.BackgroundJobs.ByType {
		writePrometheusLabeledSample(&builder, "thoughtflow_background_jobs", map[string]string{"type": jobType}, float64(count))
	}
	return builder.String()
}

func writePrometheusSample(builder *strings.Builder, name string, metricType string, help string, value float64) {
	_, _ = fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
	_, _ = fmt.Fprintf(builder, "# TYPE %s %s\n", name, metricType)
	_, _ = fmt.Fprintf(builder, "%s %.6f\n", name, value)
}

func writePrometheusLabeledSample(builder *strings.Builder, name string, labels map[string]string, value float64) {
	parts := []string{}
	for key, value := range labels {
		parts = append(parts, fmt.Sprintf("%s=%q", key, sanitizePrometheusLabel(value)))
	}
	_, _ = fmt.Fprintf(builder, "%s{%s} %.6f\n", name, strings.Join(parts, ","), value)
}

func sanitizePrometheusLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func (s *Service) systemStatus(ctx context.Context, cfg appconfig.Config) models.SystemStatus {
	workspaceStatus := workspaceRuntimeStatus(s.workspace)
	duckdbStatus := duckDBRuntimeStatus(s.workspace, cfg)
	aiStatus := aiRuntimeStatus(cfg)
	gitStatus := models.GitRuntimeStatus{Status: "disabled"}
	if s.gitQueries != nil {
		gitStatus = s.gitQueries.RuntimeStatus(ctx)
	}
	backgroundStatus := backgroundRuntimeStatus(s.workspace)
	eventsStatus := eventsRuntimeStatus(s.stream)

	ready := workspaceStatus.Status == "ready" &&
		duckdbStatus.Status == "ready" &&
		backgroundStatus.Status == "ready" &&
		eventsStatus.Status == "ready"
	status := "ready"
	if !ready || aiStatus.Status != "ready" || gitStatus.Status == "degraded" {
		status = "degraded"
	}
	return models.SystemStatus{
		Status:     status,
		Ready:      ready,
		Workspace:  workspaceStatus,
		DuckDB:     duckdbStatus,
		AI:         aiStatus,
		Git:        gitStatus,
		Background: backgroundStatus,
		Events:     eventsStatus,
	}
}

func workspaceRuntimeStatus(ws *models.Workspace) models.WorkspaceRuntimeStatus {
	status := models.WorkspaceRuntimeStatus{Status: "degraded"}
	if ws == nil {
		status.Error = "workspace is not ready"
		return status
	}
	status.ID = ws.ID
	status.RootPath = ws.RootPath
	status.ThoughtsPath = ws.ThoughtsPath
	status.TopicsPath = ws.TopicsPath
	status.AttachmentsPath = ws.AttachmentsPath
	status.RuntimePath = ws.RuntimePath
	status.JobsPath = ws.JobsPath
	status.GitEnabled = ws.GitEnabled
	if err := os.MkdirAll(ws.RuntimePath, 0o755); err != nil {
		status.Error = err.Error()
		return status
	}
	tmp, err := os.CreateTemp(ws.RuntimePath, ".status-*.tmp")
	if err != nil {
		status.Error = err.Error()
		return status
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	status.Writable = true
	status.Status = "ready"
	return status
}

func duckDBRuntimeStatus(ws *models.Workspace, cfg appconfig.Config) models.DuckDBRuntimeStatus {
	status := models.DuckDBRuntimeStatus{Status: "ready"}
	if ws == nil {
		status.Status = "degraded"
		status.Error = "workspace is not ready"
		return status
	}
	pathValue := strings.TrimSpace(cfg.Search.DuckDBPath)
	if pathValue == "" {
		pathValue = ".thoughtflow/thoughtflow.duckdb"
	}
	if filepath.IsAbs(pathValue) {
		status.Path = pathValue
	} else {
		status.Path = filepath.Join(ws.RootPath, filepath.FromSlash(pathValue))
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

func aiRuntimeStatus(cfg appconfig.Config) models.AIRuntimeStatus {
	status := "not_configured"
	configured := strings.TrimSpace(cfg.AI.APIKey) != ""
	if configured {
		status = "ready"
	}
	return models.AIRuntimeStatus{
		Status:         status,
		Configured:     configured,
		BaseURL:        cfg.AI.BaseURL,
		ChatModel:      cfg.AI.ChatModel,
		EmbeddingModel: cfg.AI.EmbeddingModel,
	}
}

func backgroundRuntimeStatus(ws *models.Workspace) models.BackgroundRuntimeStatus {
	status := models.BackgroundRuntimeStatus{Status: "degraded"}
	if ws == nil {
		status.Error = "workspace is not ready"
		return status
	}
	status.JobsPath = ws.JobsPath
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		status.Error = err.Error()
		return status
	}
	tmp, err := os.CreateTemp(ws.JobsPath, ".status-*.tmp")
	if err != nil {
		status.Error = err.Error()
		return status
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	status.Writable = true
	status.Status = "ready"
	return status
}

func eventsRuntimeStatus(stream *eventstream.Stream) models.EventsRuntimeStatus {
	if stream == nil {
		return models.EventsRuntimeStatus{Status: "degraded"}
	}
	stats := stream.Stats()
	return models.EventsRuntimeStatus{
		Status:      "ready",
		HistorySize: stats.HistorySize,
		Limit:       stats.Limit,
		Subscribers: stats.Subscribers,
	}
}

func (s *Service) handleReindex(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	job, err := s.searchService.ReindexWorkspace(req.Context())
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.search.reindex_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, job)
}

func (s *Service) handleLive(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	writeJSON(res, req, http.StatusOK, map[string]string{"status": "live"})
}

func (s *Service) handleReady(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	writeJSON(res, req, http.StatusOK, map[string]string{"status": "ready"})
}

func writeJSON(res http.ResponseWriter, req *http.Request, status int, data any) {
	res.Header().Set("Content-Type", "application/json; charset=utf-8")
	res.WriteHeader(status)
	_ = json.NewEncoder(res).Encode(models.APIResponse{
		RequestID: requestID(req),
		Data:      data,
		Error:     nil,
	})
}

func writeError(res http.ResponseWriter, req *http.Request, status int, code string, message string) {
	res.Header().Set("Content-Type", "application/json; charset=utf-8")
	res.WriteHeader(status)
	_ = json.NewEncoder(res).Encode(models.APIResponse{
		RequestID: requestID(req),
		Data:      nil,
		Error: map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func requestID(req *http.Request) string {
	if value := req.Header.Get("X-Request-ID"); value != "" {
		return value
	}
	return models.NewEventID(time.Now().UTC())
}

func pathID(path string, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	return strings.Trim(strings.TrimPrefix(path, prefix), "/")
}

func topicWeaveProposalPath(value string) (string, string) {
	tail := pathID(value, "/api/topics/")
	parts := strings.Split(tail, "/")
	if len(parts) != 3 || parts[1] != "weave-proposals" {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[2])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func intQuery(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	var ret int
	_, err := fmt.Sscanf(value, "%d", &ret)
	if err != nil || ret <= 0 {
		return fallback
	}
	return ret
}

func boolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func floatQuery(value string, fallback float64) float64 {
	if value == "" {
		return fallback
	}
	ret, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return ret
}

func timeQuery(value string, endOfDay bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	parsed = parsed.UTC()
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return parsed, nil
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	items := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}
