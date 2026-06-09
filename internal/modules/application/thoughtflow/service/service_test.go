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
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
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
	if !strings.Contains(res.Body.String(), "weave-preview") || !strings.Contains(res.Body.String(), "weave-accept") {
		t.Fatalf("expected weave preview and accept API calls in embedded app script")
	}
	if !strings.Contains(res.Body.String(), "renderDiff") {
		t.Fatalf("expected diff renderer in embedded app script")
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
