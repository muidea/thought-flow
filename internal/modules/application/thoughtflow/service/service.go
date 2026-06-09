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
	"strconv"
	"strings"
	"time"

	engine "github.com/muidea/magicEngine/http"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	refinerbiz "thoughtflow/internal/modules/refiner/biz"
	searchbiz "thoughtflow/internal/modules/search/biz"
	topicbiz "thoughtflow/internal/modules/topic/biz"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
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
	jobs           *jobstore.Store
	stream         *eventstream.Stream
	workspace      *models.Workspace
}

func New(registry engine.RouteRegistry, captureService *capturebiz.Service, refinerService *refinerbiz.Service, searchService *searchbiz.Service, topicService *topicbiz.Service, jobs *jobstore.Store, stream *eventstream.Stream, workspace *models.Workspace) *Service {
	return &Service{
		registry:       registry,
		captureService: captureService,
		refinerService: refinerService,
		searchService:  searchService,
		topicService:   topicService,
		jobs:           jobs,
		stream:         stream,
		workspace:      workspace,
	}
}

func (s *Service) RegisterRoutes() {
	s.registry.AddHandler("/", engine.GET, s.handleWeb)
	s.registry.AddHandler("/index.html", engine.GET, s.handleWeb)
	s.registry.AddHandler("/styles.css", engine.GET, s.handleWeb)
	s.registry.AddHandler("/app.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/api/thoughts", engine.POST, s.handleCreateThought)
	s.registry.AddHandler("/api/thoughts/:id/retry-refine", engine.POST, s.handleRetryRefine)
	s.registry.AddHandler("/api/thoughts/:id", engine.GET, s.handleGetThought)
	s.registry.AddHandler("/api/search", engine.GET, s.handleSearch)
	s.registry.AddHandler("/api/synthesis/save", engine.POST, s.handleSaveSynthesis)
	s.registry.AddHandler("/api/synthesis", engine.POST, s.handleSynthesis)
	s.registry.AddHandler("/api/topics", engine.GET, s.handleListTopics)
	s.registry.AddHandler("/api/topics", engine.POST, s.handleCreateTopic)
	s.registry.AddHandler("/api/topics/:id/rebuild", engine.POST, s.handleRebuildTopic)
	s.registry.AddHandler("/api/topics/:id/weave-preview", engine.POST, s.handlePreviewWeave)
	s.registry.AddHandler("/api/topics/:id/weave-accept", engine.POST, s.handleAcceptWeave)
	s.registry.AddHandler("/api/topics/:id", engine.GET, s.handleGetTopic)
	s.registry.AddHandler("/api/topics/:id", engine.PUT, s.handleUpdateTopic)
	s.registry.AddHandler("/api/jobs/:id", engine.GET, s.handleGetJob)
	s.registry.AddHandler("/api/events", engine.GET, s.handleEvents)
	s.registry.AddHandler("/api/system/status", engine.GET, s.handleSystemStatus)
	s.registry.AddHandler("/api/system/reindex", engine.POST, s.handleReindex)
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

func (s *Service) handleSearch(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	query := req.URL.Query()
	searchQuery := models.SearchQuery{
		Query:    query.Get("q"),
		Mode:     firstNonEmpty(query.Get("mode"), "hybrid"),
		Sort:     query.Get("sort"),
		TopicID:  query.Get("topic_id"),
		Tags:     splitCSV(query.Get("tags")),
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
	contentParts := []string{}
	snapshots, sourceLinks, err := s.synthesisSources(ctx, request.ThoughtIDs)
	if err != nil {
		writeSynthesisSourceError(res, req, err)
		return
	}
	for _, snapshot := range snapshots {
		title := firstNonEmpty(snapshot.Thought.DisplayTitle, snapshot.Thought.UserTitle, snapshot.Thought.ExtractedTitle, snapshot.Thought.ID)
		body := firstNonEmpty(snapshot.Thought.Summary, snapshot.Content.AINotes, snapshot.Content.ExtractedContent, snapshot.Content.Original)
		contentParts = append(contentParts, "## "+title+"\n\n"+body)
	}
	format := firstNonEmpty(request.Format, "summary")
	goal := firstNonEmpty(request.Goal, "Synthesize selected thoughts")
	draft := models.SynthesisDraft{
		ID:          models.NewJobID("synthesis", time.Now().UTC()),
		ThoughtIDs:  request.ThoughtIDs,
		Goal:        goal,
		Format:      format,
		Content:     "# " + goal + "\n\n" + strings.Join(contentParts, "\n\n"),
		SourceLinks: sourceLinks,
		Model:       "local-rule",
		CreatedAt:   time.Now().UTC(),
	}
	writeJSON(res, req, http.StatusOK, draft)
}

func (s *Service) handleSaveSynthesis(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var request models.SynthesisSaveRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_json", err.Error())
		return
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
	writeJSON(res, req, http.StatusAccepted, models.SynthesisSaveResult{
		Thought:     result.Thought,
		Jobs:        result.Jobs,
		SourceLinks: sourceLinks,
	})
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
	writeJSON(res, req, http.StatusOK, detail)
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
	events := s.stream.Subscribe(req.Context())
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
	_ = ctx
	cfg := appconfig.Load()
	aiStatus := "not_configured"
	if strings.TrimSpace(cfg.AI.APIKey) != "" {
		aiStatus = "ready"
	}
	status := map[string]any{
		"workspace": map[string]any{
			"id":               s.workspace.ID,
			"root_path":        s.workspace.RootPath,
			"thoughts_path":    s.workspace.ThoughtsPath,
			"topics_path":      s.workspace.TopicsPath,
			"attachments_path": s.workspace.AttachmentsPath,
			"runtime_path":     s.workspace.RuntimePath,
			"git_enabled":      s.workspace.GitEnabled,
		},
		"duckdb": map[string]any{
			"status": "ready",
		},
		"ai": map[string]any{
			"status":          aiStatus,
			"base_url":        cfg.AI.BaseURL,
			"chat_model":      cfg.AI.ChatModel,
			"embedding_model": cfg.AI.EmbeddingModel,
		},
		"events": map[string]any{
			"status": "ready",
		},
	}
	writeJSON(res, req, http.StatusOK, status)
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
