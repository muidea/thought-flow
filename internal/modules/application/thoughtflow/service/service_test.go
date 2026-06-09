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
	"thoughtflow/internal/pkg/observability"
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
	if !strings.Contains(res.Body.String(), `/vendor/markdown-it.min.js`) {
		t.Fatalf("expected markdown parser script in embedded index")
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

func TestHandleWebServesMarkdownParserVendorScript(t *testing.T) {
	service := &Service{}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/vendor/markdown-it.min.js", nil)

	service.handleWeb(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d", res.Code)
	}
	if contentType := res.Header().Get("Content-Type"); !strings.Contains(contentType, "application/javascript") {
		t.Fatalf("content type = %q", contentType)
	}
	if !strings.Contains(res.Body.String(), "markdown-it 14.2.0") {
		t.Fatalf("expected vendored markdown-it script")
	}
}

func TestTimeQueryParsesSearchRangeParameters(t *testing.T) {
	from, err := timeQuery("2026-06-09T10:30:00+08:00", false)
	if err != nil {
		t.Fatalf("timeQuery(RFC3339) error = %v", err)
	}
	if want := time.Date(2026, 6, 9, 2, 30, 0, 0, time.UTC); !from.Equal(want) {
		t.Fatalf("from = %s, want %s", from, want)
	}

	to, err := timeQuery("2026-06-09", true)
	if err != nil {
		t.Fatalf("timeQuery(date) error = %v", err)
	}
	if want := time.Date(2026, 6, 9, 23, 59, 59, int(time.Second-time.Nanosecond), time.UTC); !to.Equal(want) {
		t.Fatalf("to = %s, want %s", to, want)
	}

	if _, err := timeQuery("not-a-date", false); err == nil {
		t.Fatalf("expected invalid date error")
	}
}

func TestHandleGetThoughtIncludesJobsAndGitCommits(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		TopicsPath:   filepath.Join(root, "topics"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
		GitEnabled:   true,
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.TopicsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	thought := models.Thought{
		ID:            "20260609-143010-detail",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     "Detail thought",
		DisplayTitle:  "Detail thought",
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath("20260609-143010-detail")),
		CreatedAt:     time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC),
		ContentHash:   models.ContentHash("detail thought"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "Thought detail body."}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	jobs := jobstore.New(ws.JobsPath)
	if err := jobs.Save(models.Job{
		ID:           "index-detail",
		Type:         models.JobTypeIndex,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   thought.ID,
		Status:       models.JobStatusSucceeded,
		CreatedAt:    thought.CreatedAt.Add(time.Minute),
	}); err != nil {
		t.Fatalf("Save(job) error = %v", err)
	}
	captureService := capturebiz.NewService(ws, jobs, nil)
	service := &Service{
		captureService: captureService,
		jobs:           jobs,
		workspace:      ws,
		gitQueries: fakeGitQueryReader{records: []models.GitCommitRecord{{
			CommitHash:  "abc123",
			Message:     "thoughtflow: add detail thought",
			Paths:       []string{thought.Path},
			ResourceIDs: []string{thought.ID},
			CommittedAt: thought.CreatedAt.Add(2 * time.Minute),
		}}},
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/thoughts/"+thought.ID, nil)
	service.handleGetThought(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data models.ThoughtSnapshot `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Data.Thought.ID != thought.ID {
		t.Fatalf("thought = %#v", payload.Data.Thought)
	}
	if len(payload.Data.Jobs) != 1 || payload.Data.Jobs[0].ID != "index-detail" {
		t.Fatalf("jobs = %#v", payload.Data.Jobs)
	}
	if len(payload.Data.GitCommits) != 1 {
		t.Fatalf("git commits = %#v", payload.Data.GitCommits)
	}
	if payload.Data.GitCommits[0].Message != "thoughtflow: add detail thought" ||
		payload.Data.GitCommits[0].CommitHash != "abc123" ||
		len(payload.Data.GitCommits[0].Paths) != 1 ||
		payload.Data.GitCommits[0].Paths[0] != thought.Path {
		t.Fatalf("git commit = %#v", payload.Data.GitCommits[0])
	}
}

type fakeGitQueryReader struct {
	records []models.GitCommitRecord
}

func (reader fakeGitQueryReader) RecentCommits(ctx context.Context, relativePath string, resourceID string, limit int) []models.GitCommitRecord {
	_ = ctx
	_ = relativePath
	_ = resourceID
	_ = limit
	return reader.records
}

func (reader fakeGitQueryReader) RuntimeStatus(ctx context.Context) models.GitRuntimeStatus {
	_ = ctx
	return models.GitRuntimeStatus{Status: "disabled"}
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

func TestHandleGetTopicIncludesRecentActivities(t *testing.T) {
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
	ctx := context.Background()
	topic, err := topicService.CreateTopic(ctx, models.TopicCreateRequest{Name: "Activity Topic"})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	stream := eventstream.New(10)
	stream.Publish(models.DomainEvent{EventID: "evt-unrelated", EventType: models.EventTopicUpdated, ResourceType: models.ResourceTypeTopic, ResourceID: "other-topic"})
	stream.Publish(models.DomainEvent{EventID: "evt-topic", EventType: models.EventTopicUpdated, ResourceType: models.ResourceTypeTopic, ResourceID: topic.ID})
	stream.Publish(models.DomainEvent{
		EventID:      "evt-membership",
		EventType:    models.EventTopicMatched,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   "thought-1",
		Payload: []models.TopicMembership{
			{TopicID: topic.ID, ThoughtID: "thought-1"},
		},
	})
	service := &Service{topicService: topicService, stream: stream}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/topics/"+topic.ID, nil)
	service.handleGetTopic(ctx, res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data models.TopicDetail `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Data.Topic.ID != topic.ID {
		t.Fatalf("topic = %#v", payload.Data.Topic)
	}
	if len(payload.Data.Activities) != 2 {
		t.Fatalf("activities = %#v", payload.Data.Activities)
	}
	if payload.Data.Activities[0].EventID != "evt-topic" || payload.Data.Activities[1].EventID != "evt-membership" {
		t.Fatalf("activities order = %#v", payload.Data.Activities)
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

func TestSystemMetricsReportsDesignedMetricNames(t *testing.T) {
	observability.ResetForTest()
	defer observability.ResetForTest()

	root := t.TempDir()
	now := time.Date(2026, 6, 9, 12, 0, 0, 0, time.UTC)
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	for _, dir := range []string{ws.ThoughtsPath, ws.JobsPath} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}
	indexed := models.Thought{
		ID:            "20260609-100000-indexed",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath("20260609-100000-indexed")),
		CreatedAt:     now.Add(-10 * time.Minute),
		UpdatedAt:     now.Add(-9 * time.Minute),
		ContentHash:   models.ContentHash("indexed"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusMatched,
	}
	pending := indexed
	pending.ID = "20260609-100500-pending"
	pending.Path = filepath.ToSlash(markdown.ThoughtRelativePath(pending.ID))
	pending.UpdatedAt = now.Add(-5 * time.Second)
	pending.IndexStatus = models.IndexStatusPending
	for _, thought := range []models.Thought{indexed, pending} {
		if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: thought.ID}); err != nil {
			t.Fatalf("WriteThought(%s) error = %v", thought.ID, err)
		}
	}

	jobs := jobstore.New(ws.JobsPath)
	started := now.Add(-4 * time.Second)
	finished := now.Add(-1 * time.Second)
	if err := jobs.Save(models.Job{
		ID:           "refine-job",
		Type:         models.JobTypeRefine,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   indexed.ID,
		Status:       models.JobStatusSucceeded,
		CreatedAt:    now.Add(-5 * time.Second),
		StartedAt:    &started,
		FinishedAt:   &finished,
	}); err != nil {
		t.Fatalf("Save(refine) error = %v", err)
	}
	if err := jobs.Save(models.Job{
		ID:           "git-job",
		Type:         models.JobTypeGitCommit,
		ResourceType: models.ResourceTypeWorkspace,
		ResourceID:   ws.ID,
		Status:       models.JobStatusSucceeded,
		CreatedAt:    now.Add(-3 * time.Second),
	}); err != nil {
		t.Fatalf("Save(git) error = %v", err)
	}
	if err := jobs.Save(models.Job{
		ID:           "index-job",
		Type:         models.JobTypeIndex,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   pending.ID,
		Status:       models.JobStatusRunning,
		CreatedAt:    now.Add(-2 * time.Second),
	}); err != nil {
		t.Fatalf("Save(index) error = %v", err)
	}
	observability.IncrementAIRequest()
	observability.IncrementAIRequest()
	observability.IncrementSearchQuery()
	observability.AddTopicWeave(3)

	service := &Service{workspace: ws, jobs: jobs}
	metrics, err := service.systemMetrics(context.Background(), now)
	if err != nil {
		t.Fatalf("systemMetrics() error = %v", err)
	}
	expected := map[string]float64{
		"thoughtflow_capture_total":           2,
		"thoughtflow_refine_duration_seconds": 3,
		"thoughtflow_ai_request_total":        2,
		"thoughtflow_search_query_total":      1,
		"thoughtflow_index_lag_seconds":       5,
		"thoughtflow_topic_weave_total":       3,
		"thoughtflow_git_commit_total":        1,
		"thoughtflow_background_jobs":         3,
	}
	for name, value := range expected {
		if metrics.Values[name] != value {
			t.Fatalf("%s = %v, want %v in %#v", name, metrics.Values[name], value, metrics.Values)
		}
	}
	if metrics.RefineDurationSeconds.Count != 1 || metrics.RefineDurationSeconds.Sum != 3 {
		t.Fatalf("refine duration = %#v", metrics.RefineDurationSeconds)
	}
	if metrics.BackgroundJobs.ByStatus[models.JobStatusSucceeded] != 2 ||
		metrics.BackgroundJobs.ByType[models.JobTypeIndex] != 1 {
		t.Fatalf("background jobs = %#v", metrics.BackgroundJobs)
	}
	if metrics.ThoughtIndexLag.PendingThoughts != 1 || metrics.ThoughtIndexLag.FailedThoughts != 0 {
		t.Fatalf("index lag = %#v", metrics.ThoughtIndexLag)
	}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	service.handlePrometheusMetrics(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("prometheus status = %d, body = %s", res.Code, res.Body.String())
	}
	body := res.Body.String()
	for name := range expected {
		if !strings.Contains(body, name) {
			t.Fatalf("prometheus body missing %s:\n%s", name, body)
		}
	}
	if !strings.Contains(body, `thoughtflow_background_jobs{status="running"} 1.000000`) {
		t.Fatalf("prometheus body missing status label:\n%s", body)
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
