package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	topicbiz "thoughtflow/internal/modules/topic/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/synthesisstore"
	"thoughtflow/internal/pkg/topicstore"
)

func TestHandleWebServesEmbeddedIndex(t *testing.T) {
	service := &Service{}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)

	service.handleWeb(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if contentType := res.Header().Get("Content-Type"); !strings.Contains(contentType, "text/html") {
		t.Fatalf("content type = %q", contentType)
	}
	if !strings.Contains(res.Body.String(), "ThoughtFlow") {
		t.Fatalf("expected embedded index body")
	}
	if !strings.Contains(res.Body.String(), `id="topic-edit-form"`) {
		t.Fatalf("expected topic rules editor in embedded index")
	}
	if !strings.Contains(res.Body.String(), `id="tab-review"`) {
		t.Fatalf("expected weave review panel in embedded index")
	}
}

func TestHandleWebServesEmbeddedScript(t *testing.T) {
	service := &Service{}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/app.js", nil)

	service.handleWeb(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if contentType := res.Header().Get("Content-Type"); !strings.Contains(contentType, "application/javascript") {
		t.Fatalf("content type = %q", contentType)
	}
	if !strings.Contains(res.Body.String(), "EventSource") {
		t.Fatalf("expected embedded app script")
	}
	if !strings.Contains(res.Body.String(), "saveTopicRules") {
		t.Fatalf("expected topic rules save handler in embedded app script")
	}
	if !strings.Contains(res.Body.String(), "renderMarkdown") {
		t.Fatalf("expected markdown renderer in embedded app script")
	}
	if !strings.Contains(res.Body.String(), "weave-preview") ||
		!strings.Contains(res.Body.String(), "weave-accept") ||
		!strings.Contains(res.Body.String(), "weave-proposals") {
		t.Fatalf("expected weave preview, accept, and proposal API calls in embedded app script")
	}
	if !strings.Contains(res.Body.String(), "renderDiff") {
		t.Fatalf("expected diff renderer in embedded app script")
	}
	if !strings.Contains(res.Body.String(), "patch hunks") {
		t.Fatalf("expected structured patch hunk indicator in embedded app script")
	}
}

func TestHandleWeaveProposalsListsAndReadsPersistentProposal(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	topicService := topicbiz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil)
	service := &Service{topicService: topicService}
	ctx := context.Background()
	topic, err := topicService.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Weave Queue",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	thought := models.Thought{
		ID:            "20260609-143010-queue",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     "Queue note",
		DisplayTitle:  "Queue note",
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath("20260609-143010-queue")),
		CreatedAt:     time.Now().UTC(),
		UpdatedAt:     time.Now().UTC(),
		ContentHash:   models.ContentHash("Queue note"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "DuckDB queue review note."}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	proposal, err := topicService.PreviewWeave(ctx, topic.ID, thought.ID)
	if err != nil {
		t.Fatalf("PreviewWeave() error = %v", err)
	}

	listRes := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/topics/"+topic.ID+"/weave-proposals", nil)
	service.handleListWeaveProposals(ctx, listRes, listReq)

	if listRes.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRes.Code, listRes.Body.String())
	}
	var listPayload struct {
		Data []models.TopicWeaveProposal `json:"data"`
	}
	if err := json.Unmarshal(listRes.Body.Bytes(), &listPayload); err != nil {
		t.Fatalf("Unmarshal(list) error = %v", err)
	}
	if len(listPayload.Data) != 1 || listPayload.Data[0].ID != proposal.ID {
		t.Fatalf("list payload = %#v", listPayload.Data)
	}

	getRes := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/topics/"+topic.ID+"/weave-proposals/"+proposal.ID, nil)
	service.handleGetWeaveProposal(ctx, getRes, getReq)

	if getRes.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRes.Code, getRes.Body.String())
	}
	var getPayload struct {
		Data models.TopicWeaveProposal `json:"data"`
	}
	if err := json.Unmarshal(getRes.Body.Bytes(), &getPayload); err != nil {
		t.Fatalf("Unmarshal(get) error = %v", err)
	}
	if getPayload.Data.ID != proposal.ID || getPayload.Data.Status != "pending" {
		t.Fatalf("get payload = %#v", getPayload.Data)
	}
}

func TestHandleSaveSynthesisCreatesSynthesisThought(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:       "test",
		RootPath: root,
		JobsPath: filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil)
	service := &Service{captureService: captureService}
	ctx := context.Background()

	left, err := captureService.Capture(ctx, models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Title:   "Vector notes",
		Content: "DuckDB vectors keep local search useful.",
	})
	if err != nil {
		t.Fatalf("Capture(left) error = %v", err)
	}
	right, err := captureService.Capture(ctx, models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Title:   "Topic notes",
		Content: "Topic synthesis keeps references visible.",
	})
	if err != nil {
		t.Fatalf("Capture(right) error = %v", err)
	}

	body := strings.NewReader(`{
		"draft_id":"job-synthesis-test",
		"thought_ids":["` + left.Thought.ID + `","` + right.Thought.ID + `"],
		"goal":"Research outline",
		"format":"outline",
		"content":"# Research outline\n\nCombine the selected notes."
	}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/synthesis/save", body)

	service.handleSaveSynthesis(ctx, res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data models.SynthesisSaveResult `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Data.Thought.Source != models.ThoughtSourceSynthesis {
		t.Fatalf("source = %q", payload.Data.Thought.Source)
	}
	if !containsString(payload.Data.Thought.UserTags, "synthesis") || !containsString(payload.Data.Thought.UserTags, "outline") {
		t.Fatalf("tags = %#v", payload.Data.Thought.UserTags)
	}
	savedThought, savedContent, err := markdown.ReadThought(root, payload.Data.Thought.ID)
	if err != nil {
		t.Fatalf("ReadThought(saved) error = %v", err)
	}
	if savedThought.Source != models.ThoughtSourceSynthesis {
		t.Fatalf("saved source = %q", savedThought.Source)
	}
	for _, sourcePath := range []string{left.Thought.Path, right.Thought.Path} {
		if !strings.Contains(savedContent.Original, sourcePath) {
			t.Fatalf("saved synthesis content should include source %s:\n%s", sourcePath, savedContent.Original)
		}
	}
}

func TestHandleSynthesisPersistsDraftAndSaveHistory(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:       "test",
		RootPath: root,
		JobsPath: filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil)
	drafts := synthesisstore.New(root)
	service := &Service{captureService: captureService, synthesisStore: drafts, workspace: ws, synthesisAI: fakeSynthesisProvider{}}
	ctx := context.Background()
	result, err := captureService.Capture(ctx, models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Title:   "Draft source",
		Content: "Synthesis drafts should keep a local approval history.",
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}

	createBody := strings.NewReader(`{
		"thought_ids":["` + result.Thought.ID + `"],
		"goal":"Draft goal",
		"format":"summary"
	}`)
	createRes := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/synthesis", createBody)
	service.handleSynthesis(ctx, createRes, createReq)

	if createRes.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
	var createPayload struct {
		Data models.SynthesisDraft `json:"data"`
	}
	if err := json.Unmarshal(createRes.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("Unmarshal(create) error = %v", err)
	}
	if createPayload.Data.ID == "" || createPayload.Data.Status != "draft" {
		t.Fatalf("created draft = %#v", createPayload.Data)
	}
	if createPayload.Data.Model != "fake-cloud" || !strings.Contains(createPayload.Data.Content, "Cloud synthesis draft") {
		t.Fatalf("draft should come from synthesis provider, got %#v", createPayload.Data)
	}
	loaded, err := drafts.GetDraft(ctx, createPayload.Data.ID)
	if err != nil {
		t.Fatalf("GetDraft() error = %v", err)
	}
	if loaded.ID != createPayload.Data.ID || len(loaded.History) != 1 {
		t.Fatalf("loaded draft = %#v", loaded)
	}

	listRes := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/synthesis", nil)
	service.handleListSynthesisDrafts(ctx, listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRes.Code, listRes.Body.String())
	}
	getRes := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/synthesis/"+createPayload.Data.ID, nil)
	service.handleGetSynthesisDraft(ctx, getRes, getReq)
	if getRes.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRes.Code, getRes.Body.String())
	}

	saveBody := strings.NewReader(`{
		"draft_id":"` + createPayload.Data.ID + `",
		"content":"# Draft goal\n\nAccepted synthesis."
	}`)
	saveRes := httptest.NewRecorder()
	saveReq := httptest.NewRequest(http.MethodPost, "/api/synthesis/save", saveBody)
	service.handleSaveSynthesis(ctx, saveRes, saveReq)

	if saveRes.Code != http.StatusAccepted {
		t.Fatalf("save status = %d, body = %s", saveRes.Code, saveRes.Body.String())
	}
	saved, err := drafts.GetDraft(ctx, createPayload.Data.ID)
	if err != nil {
		t.Fatalf("GetDraft(saved) error = %v", err)
	}
	if saved.Status != "saved" || saved.SavedThoughtID == "" || saved.SavedAt == nil {
		t.Fatalf("saved draft = %#v", saved)
	}
	if len(saved.History) != 2 || saved.History[1].Status != "saved" {
		t.Fatalf("saved history = %#v", saved.History)
	}
}

type fakeSynthesisProvider struct{}

func (fakeSynthesisProvider) Synthesize(ctx context.Context, req ai.SynthesisRequest) (models.SynthesisDraft, error) {
	_ = ctx
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          models.NewJobID("synthesis", now),
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        req.Goal,
		Format:      req.Format,
		Content:     "# Cloud synthesis draft\n\nGenerated from provider.\n\n### Sources\n\n- [[" + req.SourceLinks[0] + "]]",
		SourceLinks: req.SourceLinks,
		Model:       "fake-cloud",
		Status:      "draft",
		CreatedAt:   now,
		UpdatedAt:   now,
	}, nil
}

func TestHandleEventsHonorsLastEventIDAndTypeFilter(t *testing.T) {
	stream := eventstream.New(10)
	stream.Publish(models.DomainEvent{EventID: "evt-1", EventType: models.EventThoughtCaptured, ResourceID: "one"})
	stream.Publish(models.DomainEvent{EventID: "evt-2", EventType: models.EventJobUpdated, ResourceID: "two"})
	stream.Publish(models.DomainEvent{EventID: "evt-3", EventType: models.EventTopicUpdated, ResourceID: "three"})
	service := &Service{stream: stream}

	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest(http.MethodGet, "/api/events?types="+models.EventTopicUpdated, nil).WithContext(ctx)
	req.Header.Set("Last-Event-ID", "evt-1")
	res := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		service.handleEvents(ctx, res, req)
		close(done)
	}()
	time.Sleep(25 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("handleEvents did not stop after context cancellation")
	}

	body := res.Body.String()
	if !strings.Contains(body, "evt-3") || !strings.Contains(body, models.EventTopicUpdated) {
		t.Fatalf("expected replayed topic event in SSE body:\n%s", body)
	}
	if strings.Contains(body, "evt-1") || strings.Contains(body, "evt-2") {
		t.Fatalf("SSE body should honor Last-Event-ID and type filter:\n%s", body)
	}
}

func TestSystemStatusReportsRuntimeComponents(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
		GitEnabled:   false,
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.RuntimePath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	duckdbPath := filepath.Join(ws.RuntimePath, "thoughtflow.duckdb")
	if err := os.WriteFile(duckdbPath, []byte("duckdb"), 0o644); err != nil {
		t.Fatalf("WriteFile(duckdb) error = %v", err)
	}
	stream := eventstream.New(10)
	stream.Publish(models.DomainEvent{EventID: "evt-1", EventType: models.EventThoughtCaptured})
	service := &Service{workspace: ws, stream: stream}

	status := service.systemStatus(context.Background(), appconfig.Config{
		Search: appconfig.SearchConfig{DuckDBPath: filepath.ToSlash(filepath.Join(".thoughtflow", "thoughtflow.duckdb"))},
		AI: appconfig.AIConfig{
			APIKey:         "test-key",
			BaseURL:        "https://api.example.test",
			ChatModel:      "chat-test",
			EmbeddingModel: "embed-test",
		},
	})

	if !status.Ready || status.Status != "ready" {
		t.Fatalf("status = %#v", status)
	}
	if !status.Workspace.Writable || status.Workspace.Status != "ready" {
		t.Fatalf("workspace status = %#v", status.Workspace)
	}
	if !status.DuckDB.Exists || status.DuckDB.Status != "ready" {
		t.Fatalf("duckdb status = %#v", status.DuckDB)
	}
	if status.Git.Status != "disabled" || status.Git.Enabled {
		t.Fatalf("git status = %#v", status.Git)
	}
	if !status.Background.Writable || status.Background.Status != "ready" {
		t.Fatalf("background status = %#v", status.Background)
	}
	if status.Events.HistorySize != 1 || status.Events.Limit != 10 {
		t.Fatalf("events status = %#v", status.Events)
	}
}

func containsString(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
