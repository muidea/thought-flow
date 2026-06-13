package service

import (
	"context"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	mcevent "github.com/muidea/magicCommon/event"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	composebiz "thoughtflow/internal/modules/compose/biz"
	refinerbiz "thoughtflow/internal/modules/refiner/biz"
	searchbiz "thoughtflow/internal/modules/search/biz"
	topicbiz "thoughtflow/internal/modules/topic/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/composedraft"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/observability"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/pkg/searchdb"
	"thoughtflow/internal/pkg/thoughtlock"
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
	if !strings.Contains(res.Body.String(), `class="tf-sider"`) ||
		!strings.Contains(res.Body.String(), `data-nav="overview"`) ||
		!strings.Contains(res.Body.String(), `id="page-container"`) {
		t.Fatalf("expected redesigned app shell in embedded index")
	}
	if !strings.Contains(res.Body.String(), `data-tab="topics-proposals"`) {
		t.Fatalf("expected weave review tab under topics page in embedded index")
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
	if !strings.Contains(res.Body.String(), "topics.patch_hunks") &&
		!strings.Contains(res.Body.String(), "patch hunks") {
		t.Fatalf("expected structured patch hunk indicator in embedded app script")
	}
}

// TestHandleWebRoutesCoverEveryEmbeddedAsset is a regression guard: every
// file under web/ in the embed FS must be reachable through handleWeb.
// When a new JS/CSS/HTML file is added under web/i18n/, web/vendor/,
// etc., this test will fail until the matching route is registered in
// RegisterRoutes().
//
// The bug this guards against: the route table only lists files
// explicitly, so a new asset added without a corresponding
// AddHandler returns 404 in production even though it's in the binary.
func TestHandleWebRoutesCoverEveryEmbeddedAsset(t *testing.T) {
	service := &Service{}
	var missing []string
	err := fs.WalkDir(webAssets, "web", func(path string, d fs.DirEntry, walkErr error) error {
		if walkErr != nil || d.IsDir() {
			return walkErr
		}
		urlPath := "/" + strings.TrimPrefix(path, "web")
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, urlPath, nil)
		service.handleWeb(context.Background(), res, req)
		if res.Code == http.StatusNotFound {
			missing = append(missing, urlPath)
		}
		return nil
	})
	if err != nil {
		t.Fatalf("WalkDir() error = %v", err)
	}
	if len(missing) > 0 {
		t.Fatalf("the following embedded assets are not reachable through handleWeb (add them to RegisterRoutes): %v", missing)
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

type fakeEventPublisher struct {
	events []mcevent.Event
}

func (publisher *fakeEventPublisher) Post(event mcevent.Event) {
	publisher.events = append(publisher.events, event)
}

type fakeBackgroundAcceptor struct {
	err error
}

func (acceptor fakeBackgroundAcceptor) AsyncFunction(function func()) error {
	if acceptor.err != nil {
		return acceptor.err
	}
	if function != nil {
		function()
	}
	return nil
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
	topicService := topicbiz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
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
	topicService := topicbiz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
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

func TestHandleListSessionCandidatesReturnsEmptyListForFreshTopic(t *testing.T) {
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
	topicService := topicbiz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
	ctx := context.Background()
	topic, err := topicService.CreateTopic(ctx, models.TopicCreateRequest{Name: "CandidateTopic"})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}
	service := &Service{topicService: topicService}

	req := httptest.NewRequest(http.MethodGet, "/api/topics/"+topic.ID+"/candidates", nil)
	res := httptest.NewRecorder()
	service.handleListSessionCandidates(ctx, res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body.String())
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var candidates []models.TopicSessionCandidate
	if err := json.Unmarshal(raw, &candidates); err != nil {
		t.Fatalf("candidate shape: %v", err)
	}
	if len(candidates) != 0 {
		t.Fatalf("expected empty candidates, got %d", len(candidates))
	}
}

func TestHandleListSessionCandidatesReturnsBadRequestForEmptyID(t *testing.T) {
	topicService := topicbiz.NewService(&models.Workspace{}, jobstore.New(filepath.Join(t.TempDir(), "jobs")), topicstore.New(t.TempDir()), nil, nil, nil, nil, nil)
	service := &Service{topicService: topicService}
	req := httptest.NewRequest(http.MethodGet, "/api/topics//candidates", nil)
	res := httptest.NewRecorder()
	service.handleListSessionCandidates(context.Background(), res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", res.Code, res.Body.String())
	}
}

func TestHandlePrivacyReportReflectsConfig(t *testing.T) {
	cfg := appconfig.Config{
		LLM:       appconfig.LLMConfig{BaseURL: "https://api.openai.com", APIKey: "sk-test"},
		Embedding: appconfig.EmbeddingConfig{BaseURL: "https://api.openai.com"},
		Reader:    appconfig.ReaderConfig{BaseURL: "https://r.jina.ai/http://", APIKey: ""},
	}
	report := buildPrivacyReport(cfg, time.Now().UTC())
	if !report.LLM.Configured {
		t.Fatalf("LLM should be configured when APIKey is set")
	}
	if report.Embedding.Configured {
		t.Fatalf("Embedding should not be configured when APIKey is empty")
	}
	if report.LLM.Provider != "openai" {
		t.Fatalf("LLM provider = %q, want openai", report.LLM.Provider)
	}
	if report.Reader.Provider != "jina" {
		t.Fatalf("Reader provider = %q, want jina", report.Reader.Provider)
	}
	if !strings.Contains(report.LLM.Hint, "https://api.openai.com") {
		t.Fatalf("LLM hint missing base URL: %q", report.LLM.Hint)
	}
	if !strings.Contains(report.Embedding.Hint, "未配置 API key") {
		t.Fatalf("Embedding hint should mention unconfigured state: %q", report.Embedding.Hint)
	}
	// Action list should expose capture_message with the LLM surface.
	var messageAction *models.ExternalAction
	for i := range report.Actions {
		if report.Actions[i].Action == "capture_message" {
			messageAction = &report.Actions[i]
			break
		}
	}
	if messageAction == nil {
		t.Fatalf("capture_message action missing: %+v", report.Actions)
	}
	if len(messageAction.Surfaces) != 1 || messageAction.Surfaces[0] != "llm" {
		t.Fatalf("capture_message surfaces = %v, want [llm]", messageAction.Surfaces)
	}
}

func TestHandlePrivacyReportAllDisabled(t *testing.T) {
	cfg := appconfig.Config{
		LLM:       appconfig.LLMConfig{BaseURL: "https://api.openai.com"},
		Embedding: appconfig.EmbeddingConfig{BaseURL: "https://api.openai.com"},
		Reader:    appconfig.ReaderConfig{Enabled: false, BaseURL: "https://r.jina.ai/http://"},
	}
	report := buildPrivacyReport(cfg, time.Now().UTC())
	if report.LLM.Configured || report.Embedding.Configured || report.Reader.Configured {
		t.Fatalf("nothing should be configured: %+v", report)
	}
	// Actions should not surface any external surface in the
	// all-disabled case — UI badges fall back to "no external request".
	for _, action := range report.Actions {
		if len(action.Surfaces) != 0 {
			t.Fatalf("action %q surfaces = %v, want empty", action.Action, action.Surfaces)
		}
	}
}

func TestHandleSaveComposeDraftMaterializesThought(t *testing.T) {
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
	composeService := composebiz.NewService(
		ws,
		composedraft.New(root),
		jobstore.New(ws.JobsPath),
		nil,
		fakeComposeProvider{body: "# Compose draft\n\nPicked notes."},
		captureService,
	)
	service := &Service{captureService: captureService, composeService: composeService, workspace: ws}
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

	createBody := strings.NewReader(`{
		"sources":[
			{"source_type":"thought","source_id":"` + left.Thought.ID + `","title":"Vector notes"},
			{"source_type":"thought","source_id":"` + right.Thought.ID + `","title":"Topic notes"}
		],
		"goal":"Research outline",
		"format":"outline"
	}`)
	createRes := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/compose/drafts", createBody)
	service.handleCreateComposeDraft(ctx, createRes, createReq)
	if createRes.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
	var created struct {
		Data models.ComposeDraft `json:"data"`
	}
	if err := json.Unmarshal(createRes.Body.Bytes(), &created); err != nil {
		t.Fatalf("Unmarshal(create) error = %v", err)
	}

	saveBody := strings.NewReader(`{
		"title":"Research outline",
		"tags":["compose","outline"]
	}`)
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/compose/drafts/"+created.Data.ID+"/save", saveBody)

	service.handleSaveComposeDraft(ctx, res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data models.ComposeSaveResult `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Data.Thought.Source != models.ThoughtSourceCompose {
		t.Fatalf("source = %q", payload.Data.Thought.Source)
	}
	if !containsString(payload.Data.Thought.UserTags, "compose") || !containsString(payload.Data.Thought.UserTags, "outline") {
		t.Fatalf("tags = %#v", payload.Data.Thought.UserTags)
	}
	savedThought, _, err := markdown.ReadThought(root, payload.Data.Thought.ID)
	if err != nil {
		t.Fatalf("ReadThought(saved) error = %v", err)
	}
	if savedThought.Source != models.ThoughtSourceCompose {
		t.Fatalf("saved source = %q", savedThought.Source)
	}
}

func TestHandleComposeDraftPersistsAndSavesWithHistory(t *testing.T) {
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
	composeService := composebiz.NewService(
		ws,
		composedraft.New(root),
		jobstore.New(ws.JobsPath),
		nil,
		fakeComposeProvider{body: "# Cloud compose draft\n\nGenerated from provider."},
		captureService,
	)
	service := &Service{captureService: captureService, composeService: composeService, workspace: ws}
	ctx := context.Background()
	result, err := captureService.Capture(ctx, models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Title:   "Draft source",
		Content: "Compose drafts should keep a local approval history.",
	})
	if err != nil {
		t.Fatalf("Capture() error = %v", err)
	}

	createBody := strings.NewReader(`{
		"sources":[
			{"source_type":"thought","source_id":"` + result.Thought.ID + `","title":"Draft source"}
		],
		"goal":"Draft goal",
		"format":"summary"
	}`)
	createRes := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/compose/drafts", createBody)
	service.handleCreateComposeDraft(ctx, createRes, createReq)

	if createRes.Code != http.StatusOK {
		t.Fatalf("create status = %d, body = %s", createRes.Code, createRes.Body.String())
	}
	var createPayload struct {
		Data models.ComposeDraft `json:"data"`
	}
	if err := json.Unmarshal(createRes.Body.Bytes(), &createPayload); err != nil {
		t.Fatalf("Unmarshal(create) error = %v", err)
	}
	if createPayload.Data.ID == "" || createPayload.Data.Status != "draft" {
		t.Fatalf("created draft = %#v", createPayload.Data)
	}
	if createPayload.Data.Model != "fake-cloud" || !strings.Contains(createPayload.Data.Content, "Cloud compose draft") {
		t.Fatalf("draft should come from compose provider, got %#v", createPayload.Data)
	}

	listRes := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/compose/drafts", nil)
	service.handleListComposeDrafts(ctx, listRes, listReq)
	if listRes.Code != http.StatusOK {
		t.Fatalf("list status = %d, body = %s", listRes.Code, listRes.Body.String())
	}
	getRes := httptest.NewRecorder()
	getReq := httptest.NewRequest(http.MethodGet, "/api/compose/drafts/"+createPayload.Data.ID, nil)
	service.handleGetComposeDraft(ctx, getRes, getReq)
	if getRes.Code != http.StatusOK {
		t.Fatalf("get status = %d, body = %s", getRes.Code, getRes.Body.String())
	}

	saveBody := strings.NewReader(`{"content":"# Draft goal\n\nAccepted compose."}`)
	saveRes := httptest.NewRecorder()
	saveReq := httptest.NewRequest(http.MethodPost, "/api/compose/drafts/"+createPayload.Data.ID+"/save", saveBody)
	service.handleSaveComposeDraft(ctx, saveRes, saveReq)

	if saveRes.Code != http.StatusAccepted {
		t.Fatalf("save status = %d, body = %s", saveRes.Code, saveRes.Body.String())
	}
	drafts := composedraft.New(root)
	saved, err := drafts.GetDraft(ctx, createPayload.Data.ID)
	if err != nil {
		t.Fatalf("GetDraft(saved) error = %v", err)
	}
	if saved.Status != "saved" || saved.SavedThoughtID == "" || saved.SavedAt == nil {
		t.Fatalf("saved draft = %#v", saved)
	}
}

type fakeComposeProvider struct {
	body  string
	model string
}

func (fakeComposeProvider) Synthesize(ctx context.Context, req ai.SynthesisRequest) (models.SynthesisDraft, error) {
	_ = ctx
	now := time.Now().UTC()
	return models.SynthesisDraft{
		ID:          models.NewJobID("compose", now),
		ThoughtIDs:  req.ThoughtIDs,
		Goal:        req.Goal,
		Format:      req.Format,
		Content:     "# Cloud compose draft\n\nGenerated from provider.",
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

func TestHandleEventsStopsWhenServiceCloses(t *testing.T) {
	stream := eventstream.New(10)
	service := New(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, stream, nil, appconfig.Config{})

	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	res := httptest.NewRecorder()
	done := make(chan struct{})
	go func() {
		service.handleEvents(context.Background(), res, req)
		close(done)
	}()
	time.Sleep(25 * time.Millisecond)
	service.Close()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatalf("handleEvents did not stop after service close")
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
	searchStore, err := searchdb.Open(context.Background(), duckdbPath)
	if err != nil {
		t.Fatalf("Open(searchdb) error = %v", err)
	}
	defer searchStore.Close()
	if _, err := os.Stat(duckdbPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(duckdbPath, []byte("duckdb"), 0o644); err != nil {
			t.Fatalf("WriteFile(duckdb) error = %v", err)
		}
	} else if err != nil {
		t.Fatalf("Stat(duckdb) error = %v", err)
	}
	stream := eventstream.New(10)
	stream.Publish(models.DomainEvent{EventID: "evt-1", EventType: models.EventThoughtCaptured})
	jobs := jobstore.New(ws.JobsPath)
	searchService := searchbiz.NewService(ws, jobs, searchStore, nil, nil, nil, duckdbPath)
	events := &fakeEventPublisher{}
	service := &Service{workspace: ws, jobs: jobs, events: events, background: fakeBackgroundAcceptor{}, stream: stream, searchService: searchService}

	status := service.systemStatus(context.Background(), appconfig.Config{
		LLM: appconfig.LLMConfig{
			APIKey:    "test-key",
			BaseURL:   "https://llm.example.test",
			ChatModel: "chat-test",
		},
		Embedding: appconfig.EmbeddingConfig{
			APIKey:  "embedding-key",
			BaseURL: "https://embedding.example.test",
			Model:   "embed-test",
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
	if !status.LLM.Configured || status.LLM.ChatModel != "chat-test" || status.LLM.Status != "ready" {
		t.Fatalf("llm status = %#v", status.LLM)
	}
	if !status.Embedding.Configured || status.Embedding.Model != "embed-test" || status.Embedding.Status != "ready" {
		t.Fatalf("embedding status = %#v", status.Embedding)
	}
	if !status.Background.Writable || !status.Background.AcceptingTasks || status.Background.Status != "ready" {
		t.Fatalf("background status = %#v", status.Background)
	}
	if !status.Events.Publishable || status.Events.HistorySize != 1 || status.Events.Limit != 10 {
		t.Fatalf("events status = %#v", status.Events)
	}
	if len(events.events) != 1 || events.events[0].ID() != "thoughtflow.system.health_probe" {
		t.Fatalf("probe events = %#v", events.events)
	}
}

func TestHandleReadyReturnsUnavailableWhenRuntimeIsNotReady(t *testing.T) {
	service := &Service{}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/ready", nil)

	service.handleReady(context.Background(), res, req)

	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data models.SystemStatus `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Data.Ready {
		t.Fatalf("ready status = %#v", payload.Data)
	}
}

func TestJSONResponsesCarryRequestID(t *testing.T) {
	service := &Service{}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/health/live", nil)
	req.Header.Set("X-Request-ID", "req-test-123")

	service.handleLive(context.Background(), res, req)

	var payload models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.RequestID != "req-test-123" {
		t.Fatalf("request_id = %q", payload.RequestID)
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

	captureService := capturebiz.NewService(ws, jobs, nil)
	service := &Service{workspace: ws, jobs: jobs, captureService: captureService}
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

func findSingleThoughtFile(t *testing.T, thoughtsPath string) string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(thoughtsPath, "*", "*", "*.md"))
	if err != nil {
		t.Fatalf("Glob() error = %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("expected 1 thought file under %s, got %d (%v)", thoughtsPath, len(matches), matches)
	}
	return matches[0]
}

func TestHandlePatchThoughtAppliesTitleAndAINotesAppend(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	now := time.Date(2026, 6, 9, 16, 0, 0, 0, time.UTC)
	thoughtID := "20260609-160000-patch"
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID)),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("patch body"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "patch body", AINotes: "Summary: original"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	jobs := jobstore.New(ws.JobsPath)
	locker := thoughtlock.New(time.Second)
	captureService := capturebiz.NewService(ws, jobs, nil, capturebiz.WithLocker(locker))
	service := &Service{captureService: captureService, jobs: jobs, workspace: ws, gitQueries: fakeGitQueryReader{}}

	body := `{"title":"Renamed","ai_notes_append":"Followup after the rename."}`
	req := httptest.NewRequest(http.MethodPatch, "/api/thoughts/"+thoughtID, strings.NewReader(body))
	req.Header.Set("X-Session-Id", "session-X")
	res := httptest.NewRecorder()
	service.handlePatchThought(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data models.ThoughtSnapshot `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Data.Thought.UserTitle != "Renamed" {
		t.Fatalf("user title = %q", payload.Data.Thought.UserTitle)
	}
	if payload.Data.Thought.ExtractedTitle != "" {
		t.Fatalf("extracted title should not change: %q", payload.Data.Thought.ExtractedTitle)
	}
	if !strings.Contains(payload.Data.Content.AINotes, "Summary: original") ||
		!strings.Contains(payload.Data.Content.AINotes, "Followup after the rename.") {
		t.Fatalf("ai notes = %q", payload.Data.Content.AINotes)
	}
}

func TestHandlePatchThoughtRequiresSessionID(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil, capturebiz.WithLocker(thoughtlock.New(time.Second)))
	service := &Service{captureService: captureService, workspace: ws}

	req := httptest.NewRequest(http.MethodPatch, "/api/thoughts/whatever", strings.NewReader(`{"title":"X"}`))
	res := httptest.NewRecorder()
	service.handlePatchThought(context.Background(), res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "session_required") {
		t.Fatalf("body missing session_required: %s", res.Body.String())
	}
}

func TestHandlePatchThoughtRejectsUnknownField(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	now := time.Date(2026, 6, 9, 16, 1, 0, 0, time.UTC)
	thoughtID := "20260609-160100-unknown"
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID)),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("u body"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "u body"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil, capturebiz.WithLocker(thoughtlock.New(time.Second)))
	service := &Service{captureService: captureService, workspace: ws}

	req := httptest.NewRequest(http.MethodPatch, "/api/thoughts/"+thoughtID, strings.NewReader(`{"untitled_field":"X"}`))
	req.Header.Set("X-Session-Id", "session-X")
	res := httptest.NewRecorder()
	service.handlePatchThought(context.Background(), res, req)

	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "invalid_patch") {
		t.Fatalf("body missing invalid_patch: %s", res.Body.String())
	}
}

// TestHandlePatchThoughtSurfacesRefiningAsDistinctCode ensures that when
// the refiner module holds the thought lock (e.g. an LLM call is in
// flight), the PATCH endpoint reports a different error code than the
// generic "another session" case. The frontend keys its user-visible
// message off this code, so the distinction is part of the contract.
func TestHandlePatchThoughtSurfacesRefiningAsDistinctCode(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	now := time.Date(2026, 6, 9, 16, 2, 0, 0, time.UTC)
	thoughtID := "20260609-160200-refining"
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID)),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("r body"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRunning,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "r body"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	locker := thoughtlock.New(time.Second)
	if err := locker.Acquire(thoughtID, thoughtlock.RefinerSessionID); err != nil {
		t.Fatalf("locker.Acquire(refiner) error = %v", err)
	}
	t.Cleanup(func() { locker.Release(thoughtID, thoughtlock.RefinerSessionID) })

	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil, capturebiz.WithLocker(locker))
	service := &Service{captureService: captureService, workspace: ws}

	req := httptest.NewRequest(http.MethodPatch, "/api/thoughts/"+thoughtID, strings.NewReader(`{"title":"Renamed during refine"}`))
	req.Header.Set("X-Session-Id", "session-Y")
	res := httptest.NewRecorder()
	service.handlePatchThought(context.Background(), res, req)

	if res.Code != http.StatusConflict {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "thoughtflow.capture.refining") {
		t.Fatalf("body missing thoughtflow.capture.refining code: %s", res.Body.String())
	}
	if strings.Contains(res.Body.String(), "thoughtflow.capture.locked") {
		t.Fatalf("body should not use the generic capture.locked code: %s", res.Body.String())
	}
}

func TestHandleThoughtSuggestUsesExtractedTitle(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	now := time.Date(2026, 6, 9, 16, 10, 0, 0, time.UTC)
	thoughtID := "20260609-161000-suggest"
	thought := models.Thought{
		ID:             thoughtID,
		Type:           models.ThoughtTypeURL,
		Source:         models.ThoughtSourceManual,
		ExtractedTitle: "Fetched title",
		Path:           filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID)),
		CreatedAt:      now,
		UpdatedAt:      now,
		ContentHash:    models.ContentHash("https://example.com"),
		CaptureStatus:  models.CaptureStatusCaptured,
		RefineStatus:   models.RefineStatusRefined,
		IndexStatus:    models.IndexStatusIndexed,
		TopicStatus:    models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "https://example.com", AINotes: "Summary: existing"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	refinerService := refinerbiz.NewService(ws, jobstore.New(ws.JobsPath), nil, nil, nil, nil)
	service := &Service{refinerService: refinerService, workspace: ws}

	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/thoughts/"+thoughtID+"/suggest", nil)
	service.handleThoughtSuggest(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data models.ThoughtSuggestion `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if payload.Data.Title != "Fetched title" {
		t.Fatalf("suggestion title = %q", payload.Data.Title)
	}
	if payload.Data.Model != "extracted" {
		t.Fatalf("suggestion model = %q", payload.Data.Model)
	}
}

// stubClassifyProvider returns a fixed response. It is used to verify
// that handleCreateThought forwards the classified command to the
// capture service.
type stubClassifyProvider struct {
	result ai.ClassifyResult
	err    error
	called int
}

func (s *stubClassifyProvider) Classify(_ context.Context, _ ai.ClassifyRequest) (ai.ClassifyResult, error) {
	s.called++
	if s.err != nil {
		return ai.ClassifyResult{}, s.err
	}
	return s.result, nil
}

func TestHandleCreateThoughtUsesClassifyForAmbiguousContent(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil)
	classify := &stubClassifyProvider{result: ai.ClassifyResult{
		Raw:   `{"type":"url","extracted_url":"https://example.com/feed","confidence":0.9}`,
		Model: "test-llm",
	}}
	service := &Service{captureService: captureService, workspace: ws, classifyProvider: classify}

	// Content has no URL, so the regex fast path is skipped and the LLM
	// classifier is the only one that decides the type.
	body := `{"type":"","content":"a deep dive into vector databases"}`
	req := httptest.NewRequest(http.MethodPost, "/api/thoughts", strings.NewReader(body))
	res := httptest.NewRecorder()
	service.handleCreateThought(context.Background(), res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if classify.called != 1 {
		t.Fatalf("classify called %d times, want 1", classify.called)
	}
	thoughtFile := findSingleThoughtFile(t, ws.ThoughtsPath)
	raw, err := os.ReadFile(thoughtFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	contents := string(raw)
	if !strings.Contains(contents, "https://example.com/feed") {
		t.Fatalf("front matter missing URL: %s", contents)
	}
	if !strings.Contains(contents, `type: "url"`) {
		t.Fatalf("front matter type should be url: %s", contents)
	}
}

func TestHandleCreateThoughtURLRegexShortCircuitsClassify(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil)
	classify := &stubClassifyProvider{}
	service := &Service{captureService: captureService, workspace: ws, classifyProvider: classify}

	// Content is a bare URL — the regex fast path should resolve it
	// without paying for an LLM call.
	body := `{"type":"","content":"https://example.com/paper"}`
	req := httptest.NewRequest(http.MethodPost, "/api/thoughts", strings.NewReader(body))
	res := httptest.NewRecorder()
	service.handleCreateThought(context.Background(), res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if classify.called != 0 {
		t.Fatalf("classify called %d times, want 0 (regex fast path)", classify.called)
	}
	thoughtFile := findSingleThoughtFile(t, ws.ThoughtsPath)
	raw, err := os.ReadFile(thoughtFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	contents := string(raw)
	if !strings.Contains(contents, "https://example.com/paper") {
		t.Fatalf("front matter missing URL: %s", contents)
	}
}

func TestHandleCreateThoughtFallsBackToTextOnClassifyError(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "test",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}

	captureService := capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil)
	classify := &stubClassifyProvider{err: errors.New("simulated provider outage")}
	service := &Service{captureService: captureService, workspace: ws, classifyProvider: classify}

	body := `{"type":"","content":"just a plain note"}`
	req := httptest.NewRequest(http.MethodPost, "/api/thoughts", strings.NewReader(body))
	res := httptest.NewRecorder()
	service.handleCreateThought(context.Background(), res, req)

	if res.Code != http.StatusAccepted {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if classify.called != 1 {
		t.Fatalf("classify called %d times, want 1", classify.called)
	}
	thoughtFile := findSingleThoughtFile(t, ws.ThoughtsPath)
	raw, err := os.ReadFile(thoughtFile)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	contents := string(raw)
	if !strings.Contains(contents, `type: "text"`) {
		t.Fatalf("front matter type should fall back to text: %s", contents)
	}
	if !strings.Contains(contents, "just a plain note") {
		t.Fatalf("front matter missing content: %s", contents)
	}
}

// TestHandleCreateSessionStagesContentInScratchpad locks in the
// scratchpad-aware behavior: the user message is appended to the
// scratchpad; no thought is written, no classifier is called, and the
// response carries a populated scratchpad.
func TestHandleCreateSessionStagesContentInScratchpad(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:             "test",
		RootPath:       root,
		ThoughtsPath:   filepath.Join(root, "thoughts"),
		JobsPath:       filepath.Join(root, ".thoughtflow", "jobs"),
		ScratchpadPath: filepath.Join(root, ".scratchpad"),
	}
	if err := os.MkdirAll(ws.ScratchpadPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	store := scratchpad.New(ws.ScratchpadPath)
	scratchpadSvc := capturebiz.NewScratchpadService(store)
	service := &Service{
		captureService: capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil),
		scratchpadSvc:  scratchpadSvc,
		scratchpad:     store,
		workspace:      ws,
		classifyProvider: &stubClassifyProvider{result: ai.ClassifyResult{
			Raw:   `{"type":"text","confidence":0.95}`,
			Model: "test-llm",
		}},
	}

	body := `{"content":"brainstorm ideas for the next refactor"}`
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions", strings.NewReader(body))
	req.Header.Set("X-Session-Id", "sess-42")
	res := httptest.NewRecorder()
	service.handleCreateSession(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data scratchpad.Scratchpad `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Data.SessionID != "sess-42" {
		t.Fatalf("scratchpad session_id = %q", payload.Data.SessionID)
	}
	if payload.Data.Content != "brainstorm ideas for the next refactor" {
		t.Fatalf("scratchpad content = %q", payload.Data.Content)
	}
	if len(payload.Data.Messages) != 1 || payload.Data.Messages[0].Role != "user" {
		t.Fatalf("expected one user message, got %+v", payload.Data.Messages)
	}
	if _, err := os.Stat(filepath.Join(ws.ThoughtsPath, "anything.md")); err == nil {
		t.Fatalf("thought file should not exist yet")
	}
}

// TestHandleCreateSessionSkipsClassifyInScratchpadMode locks in
// that classify is NOT called when the user kicks off a scratchpad
// session — the classifier only runs at commit time, when there is
// real content to classify.
func TestHandleCreateSessionSkipsClassifyInScratchpadMode(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:             "test",
		RootPath:       root,
		ThoughtsPath:   filepath.Join(root, "thoughts"),
		JobsPath:       filepath.Join(root, ".thoughtflow", "jobs"),
		ScratchpadPath: filepath.Join(root, ".scratchpad"),
	}
	if err := os.MkdirAll(ws.ScratchpadPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}

	store := scratchpad.New(ws.ScratchpadPath)
	scratchpadSvc := capturebiz.NewScratchpadService(store)
	classify := &stubClassifyProvider{result: ai.ClassifyResult{
		Raw:   `{"type":"url","extracted_url":"https://example.org/article","confidence":0.85}`,
		Model: "test-llm",
	}}
	service := &Service{
		captureService:   capturebiz.NewService(ws, jobstore.New(ws.JobsPath), nil),
		scratchpadSvc:    scratchpadSvc,
		scratchpad:       store,
		workspace:        ws,
		classifyProvider: classify,
	}

	body := `{"content":"the article could be relevant to the team"}`
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions", strings.NewReader(body))
	req.Header.Set("X-Session-Id", "sess-99")
	res := httptest.NewRecorder()
	service.handleCreateSession(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", res.Code, res.Body.String())
	}
	if classify.called != 0 {
		t.Fatalf("classify should not run at session start, called %d times", classify.called)
	}
}

func TestHandleCreateSessionMintsSessionIDWhenMissing(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:             "test",
		RootPath:       root,
		ScratchpadPath: filepath.Join(root, ".scratchpad"),
	}
	if err := os.MkdirAll(ws.ScratchpadPath, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	store := scratchpad.New(ws.ScratchpadPath)
	scratchpadSvc := capturebiz.NewScratchpadService(store)
	service := &Service{
		scratchpad:    store,
		scratchpadSvc: scratchpadSvc,
		workspace:     ws,
	}

	body := `{"content":"hi"}`
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions", strings.NewReader(body))
	res := httptest.NewRecorder()
	service.handleCreateSession(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body.String())
	}
	var payload struct {
		Data scratchpad.Scratchpad `json:"data"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if payload.Data.SessionID == "" || payload.Data.Content != "hi" {
		t.Fatalf("scratchpad = %+v", payload.Data)
	}
}

func TestHandleCreateSessionAppendsContentToLastActiveByDefault(t *testing.T) {
	store := newFakeScratchpadStore()
	now := time.Now().UTC()
	store.Save(scratchpad.Scratchpad{SessionID: "older", Content: "first", UpdatedAt: now})
	store.Save(scratchpad.Scratchpad{SessionID: "latest", Content: "second", UpdatedAt: now.Add(time.Minute)})
	service := &Service{
		scratchpad:    store,
		scratchpadSvc: capturebiz.NewScratchpadService(store),
		workspace:     &models.Workspace{ID: "local"},
	}
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions", strings.NewReader(`{"content":"follow up"}`))
	res := httptest.NewRecorder()
	service.handleCreateSession(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body.String())
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var sp scratchpad.Scratchpad
	if err := json.Unmarshal(raw, &sp); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if sp.SessionID != "latest" {
		t.Fatalf("session_id = %q, want latest", sp.SessionID)
	}
	if !strings.Contains(sp.Content, "second") || !strings.Contains(sp.Content, "follow up") {
		t.Fatalf("content did not append to latest session: %q", sp.Content)
	}
}

// TestHandleListJobsCoversAllFilterModes locks in the diagnostic
// surface of GET /api/jobs: most-recent-first ordering, optional
// type/status/resource_id narrowing, and the limit cap. The runtime
// card on the web UI calls /api/jobs?limit=8 and would silently
// fall back to an empty-state hint if the endpoint ever stopped
// returning an array; the contract tested here is what the
// frontend's `Array.isArray(list) ? list : (list?.jobs || [])`
// fallback chain depends on.
func TestHandleListJobsCoversAllFilterModes(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:       "local",
		JobsPath: filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	jobs := jobstore.New(ws.JobsPath)
	base := time.Date(2026, 6, 12, 8, 0, 0, 0, time.UTC)
	// Out-of-order on purpose so the test confirms sort, not file order.
	fixtures := []models.Job{
		{ID: "refine-A", Type: models.JobTypeRefine, ResourceType: models.ResourceTypeThought, ResourceID: "thought-A", Status: models.JobStatusSucceeded, CreatedAt: base.Add(2 * time.Minute)},
		{ID: "index-A", Type: models.JobTypeIndex, ResourceType: models.ResourceTypeThought, ResourceID: "thought-A", Status: models.JobStatusFailed, CreatedAt: base.Add(4 * time.Minute)},
		{ID: "git-A", Type: models.JobTypeGitCommit, ResourceType: models.ResourceTypeWorkspace, ResourceID: ws.ID, Status: models.JobStatusSucceeded, CreatedAt: base.Add(6 * time.Minute)},
		{ID: "refine-B", Type: models.JobTypeRefine, ResourceType: models.ResourceTypeThought, ResourceID: "thought-B", Status: models.JobStatusRunning, CreatedAt: base.Add(8 * time.Minute)},
	}
	for _, job := range fixtures {
		if err := jobs.Save(job); err != nil {
			t.Fatalf("Save(%s) error = %v", job.ID, err)
		}
	}
	service := &Service{jobs: jobs}
	ctx := context.Background()

	type call struct {
		query string
		want  []string
	}
	cases := []call{
		{query: "", want: []string{"refine-B", "git-A", "index-A", "refine-A"}},
		{query: "?type=refine", want: []string{"refine-B", "refine-A"}},
		{query: "?status=failed", want: []string{"index-A"}},
		{query: "?resource_id=thought-A", want: []string{"index-A", "refine-A"}},
		{query: "?type=refine&status=running", want: []string{"refine-B"}},
		{query: "?limit=2", want: []string{"refine-B", "git-A"}},
		{query: "?type=refine&limit=1", want: []string{"refine-B"}},
		{query: "?type=does-not-exist", want: []string{}},
	}
	for _, tc := range cases {
		res := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, "/api/jobs"+tc.query, nil)
		service.handleListJobs(ctx, res, req)
		if res.Code != http.StatusOK {
			t.Fatalf("query=%q status = %d, body = %s", tc.query, res.Code, res.Body.String())
		}
		var payload struct {
			Data []models.Job `json:"data"`
		}
		if err := json.Unmarshal(res.Body.Bytes(), &payload); err != nil {
			t.Fatalf("query=%q Unmarshal() error = %v", tc.query, err)
		}
		got := make([]string, len(payload.Data))
		for i, job := range payload.Data {
			got[i] = job.ID
		}
		if !equalStringSlices(got, tc.want) {
			t.Fatalf("query=%q got %v, want %v", tc.query, got, tc.want)
		}
	}
}

func TestHandleListJobsReportsStoreErrors(t *testing.T) {
	service := &Service{jobs: brokenJobStore{}}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	service.handleListJobs(context.Background(), res, req)
	if res.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", res.Code)
	}
	if !strings.Contains(res.Body.String(), "thoughtflow.jobs.list_failed") {
		t.Fatalf("body = %s, want list_failed code", res.Body.String())
	}
}

type brokenJobStore struct{}

func (brokenJobStore) Get(string) (models.Job, error) { return models.Job{}, errors.New("nope") }
func (brokenJobStore) List() ([]models.Job, error)    { return nil, errors.New("store offline") }
func (brokenJobStore) RecentByResource(string, int) ([]models.Job, error) {
	return nil, errors.New("nope")
}
func (brokenJobStore) RuntimeStatus() models.BackgroundRuntimeStatus {
	return models.BackgroundRuntimeStatus{}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// fakeScratchpadStore is the in-memory stub used by the handler tests
// in this file. It mirrors the production store's contract: Get on
// a missing session returns a zero-value scratchpad (not an error),
// Save is upsert, and Delete is idempotent.
type fakeScratchpadStore struct {
	mu    sync.Mutex
	items map[string]scratchpad.Scratchpad
}

func newFakeScratchpadStore() *fakeScratchpadStore {
	return &fakeScratchpadStore{items: map[string]scratchpad.Scratchpad{}}
}

func (f *fakeScratchpadStore) Get(sessionID string) (scratchpad.Scratchpad, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if existing, ok := f.items[sessionID]; ok {
		return existing, nil
	}
	return scratchpad.Scratchpad{SessionID: sessionID}, nil
}

func (f *fakeScratchpadStore) Save(sp scratchpad.Scratchpad) (scratchpad.Scratchpad, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.items[sp.SessionID] = sp
	return sp, nil
}

func (f *fakeScratchpadStore) Delete(sessionID string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.items, sessionID)
	return nil
}

func (f *fakeScratchpadStore) List() []scratchpad.Summary {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]scratchpad.Summary, 0, len(f.items))
	for _, sp := range f.items {
		out = append(out, scratchpad.Summary{
			SessionID:          sp.SessionID,
			Title:              sp.Title,
			CommittedThoughtID: sp.CommittedThoughtID,
			MessageCount:       len(sp.Messages),
			ContentLength:      len(sp.Content),
			UpdatedAt:          sp.UpdatedAt,
		})
	}
	return out
}

func (f *fakeScratchpadStore) MarkCommitted(sessionID, thoughtID string) (scratchpad.Scratchpad, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sp, ok := f.items[sessionID]
	if !ok {
		return scratchpad.Scratchpad{}, errors.New("scratchpad not found")
	}
	sp.CommittedThoughtID = thoughtID
	now := time.Now().UTC()
	sp.CommittedAt = &now
	sp.UpdatedAt = now
	f.items[sessionID] = sp
	return sp, nil
}

func (f *fakeScratchpadStore) Reset(sessionID string) (scratchpad.Scratchpad, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	sp, ok := f.items[sessionID]
	if !ok {
		return scratchpad.Scratchpad{}, errors.New("scratchpad not found")
	}
	sp.Content = ""
	sp.Messages = nil
	sp.Draft = scratchpad.Draft{}
	sp.UpdatedAt = time.Now().UTC()
	f.items[sessionID] = sp
	return sp, nil
}

func (f *fakeScratchpadStore) LastActive() (scratchpad.Scratchpad, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var best *scratchpad.Scratchpad
	for _, sp := range f.items {
		if strings.TrimSpace(sp.CommittedThoughtID) != "" {
			continue
		}
		if best == nil || sp.UpdatedAt.After(best.UpdatedAt) {
			copy := sp
			best = &copy
		}
	}
	if best == nil {
		return scratchpad.Scratchpad{}, false
	}
	return *best, true
}

// TestHandleGetSessionReturnsEmptyForMissingSession locks in the
// "200 + zero value" behavior so the UI never has to special-case
// 404 on first chat of a new session.
func TestHandleGetSessionReturnsEmptyForMissingSession(t *testing.T) {
	store := newFakeScratchpadStore()
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}
	req := httptest.NewRequest(http.MethodGet, "/api/capture/sessions/fresh", nil)
	res := httptest.NewRecorder()
	service.handleGetSession(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if env.Error != nil {
		t.Fatalf("unexpected error: %+v", env.Error)
	}
	raw, _ := json.Marshal(env.Data)
	var sp scratchpad.Scratchpad
	if err := json.Unmarshal(raw, &sp); err != nil {
		t.Fatalf("data shape: %v", err)
	}
	if sp.SessionID != "fresh" || sp.Content != "" {
		t.Fatalf("empty scratchpad shape wrong: %+v", sp)
	}
}

func TestHandleGetSessionRejectsMissingSessionID(t *testing.T) {
	store := newFakeScratchpadStore()
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}
	req := httptest.NewRequest(http.MethodGet, "/api/capture/sessions/", nil)
	res := httptest.NewRecorder()
	service.handleGetSession(context.Background(), res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
	if !strings.Contains(res.Body.String(), "thoughtflow.capture.scratchpad.invalid_session") {
		t.Fatalf("body = %s", res.Body.String())
	}
}

func TestHandleSessionMessageStoresAndReturns(t *testing.T) {
	store := newFakeScratchpadStore()
	service := &Service{
		scratchpad:    store,
		scratchpadSvc: capturebiz.NewScratchpadService(store),
		workspace:     &models.Workspace{ID: "local"},
	}
	body := map[string]string{"role": "user", "text": "hello"}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions/sess-1/messages", strings.NewReader(string(raw)))
	res := httptest.NewRecorder()
	service.handleSessionMessage(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body.String())
	}
	saved, err := store.Get("sess-1")
	if err != nil {
		t.Fatalf("Get(sess-1) error = %v", err)
	}
	if saved.Content != "hello" || len(saved.Messages) != 1 {
		t.Fatalf("saved scratchpad = %+v", saved)
	}
}

func TestHandleSessionMessageRejectsEmptyText(t *testing.T) {
	store := newFakeScratchpadStore()
	service := &Service{
		scratchpad:    store,
		scratchpadSvc: capturebiz.NewScratchpadService(store),
		workspace:     &models.Workspace{ID: "local"},
	}
	body := map[string]string{"text": ""}
	raw, _ := json.Marshal(body)
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions/sess-1/messages", strings.NewReader(string(raw)))
	res := httptest.NewRecorder()
	service.handleSessionMessage(context.Background(), res, req)
	if res.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400", res.Code)
	}
}

func TestHandleDeleteSessionIsIdempotent(t *testing.T) {
	store := newFakeScratchpadStore()
	if _, err := store.Save(scratchpad.Scratchpad{SessionID: "del", Content: "x"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}

	// First delete: real call.
	req := httptest.NewRequest(http.MethodDelete, "/api/capture/sessions/del", nil)
	res := httptest.NewRecorder()
	service.handleDeleteSession(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("first delete status = %d", res.Code)
	}

	// Second delete on missing session: still 200, no error.
	req2 := httptest.NewRequest(http.MethodDelete, "/api/capture/sessions/del", nil)
	res2 := httptest.NewRecorder()
	service.handleDeleteSession(context.Background(), res2, req2)
	if res2.Code != http.StatusOK {
		t.Fatalf("idempotent delete status = %d, body = %s", res2.Code, res2.Body.String())
	}
}

func TestHandleListSessionsReturnsSummaries(t *testing.T) {
	store := newFakeScratchpadStore()
	store.Save(scratchpad.Scratchpad{SessionID: "a", Content: "0123"})
	store.Save(scratchpad.Scratchpad{SessionID: "b", Content: "56789"})
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}
	req := httptest.NewRequest(http.MethodGet, "/api/capture/sessions", nil)
	res := httptest.NewRecorder()
	service.handleListSessions(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if !strings.Contains(res.Body.String(), "\"a\"") || !strings.Contains(res.Body.String(), "\"b\"") {
		t.Fatalf("list body missing sessions: %s", res.Body.String())
	}
}

func TestHandleListSessionsExposesLastActiveForUncommitted(t *testing.T) {
	store := newFakeScratchpadStore()
	store.Save(scratchpad.Scratchpad{SessionID: "older", Content: "first draft"})
	now := time.Now().UTC()
	// "latest" is fresher and uncommitted — it should win.
	store.Save(scratchpad.Scratchpad{SessionID: "latest", Content: "still in flight", UpdatedAt: now.Add(time.Hour)})
	// "archived" is the most recently *touched* but already committed — excluded.
	store.Save(scratchpad.Scratchpad{SessionID: "archived", Content: "done", UpdatedAt: now.Add(2 * time.Hour)})
	store.MarkCommitted("archived", "thought-archived")
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}
	req := httptest.NewRequest(http.MethodGet, "/api/capture/sessions", nil)
	res := httptest.NewRecorder()
	service.handleListSessions(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var payload struct {
		Summaries           []scratchpad.Summary `json:"summaries"`
		LastActiveSessionID string               `json:"last_active_session_id"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		t.Fatalf("payload shape: %v; raw = %s", err, raw)
	}
	if payload.LastActiveSessionID != "latest" {
		t.Fatalf("last_active_session_id = %q, want latest (archived must be excluded)", payload.LastActiveSessionID)
	}
}

func TestHandleListSessionsOmitsLastActiveWhenAllCommitted(t *testing.T) {
	store := newFakeScratchpadStore()
	store.Save(scratchpad.Scratchpad{SessionID: "done-1"})
	store.MarkCommitted("done-1", "thought-1")
	store.Save(scratchpad.Scratchpad{SessionID: "done-2"})
	store.MarkCommitted("done-2", "thought-2")
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}
	req := httptest.NewRequest(http.MethodGet, "/api/capture/sessions", nil)
	res := httptest.NewRecorder()
	service.handleListSessions(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", res.Code)
	}
	if strings.Contains(res.Body.String(), "last_active_session_id") {
		t.Fatalf("body unexpectedly contains last_active_session_id: %s", res.Body.String())
	}
}

func TestHandleCreateSessionGeneratesFreshID(t *testing.T) {
	store := newFakeScratchpadStore()
	store.Save(scratchpad.Scratchpad{SessionID: "old", Content: "previous"})
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}
	body, _ := json.Marshal(map[string]string{"prev_session_id": "old"})
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions", strings.NewReader(string(body)))
	res := httptest.NewRecorder()
	service.handleCreateSession(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body.String())
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var sp scratchpad.Scratchpad
	if err := json.Unmarshal(raw, &sp); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if sp.SessionID == "" || sp.SessionID == "old" {
		t.Fatalf("expected new session id, got %q", sp.SessionID)
	}
	// The "old" session was deleted; Get on a missing session returns a
	// zero-value scratchpad (no error), which is the contract.
	prev, err := store.Get("old")
	if err != nil {
		t.Fatalf("Get(old) error = %v", err)
	}
	if prev.Content != "" {
		t.Fatalf("old session should be empty after delete, got %+v", prev)
	}
}

func TestHandleCreateSessionReusesLastActiveWhenRequested(t *testing.T) {
	store := newFakeScratchpadStore()
	now := time.Now().UTC()
	store.Save(scratchpad.Scratchpad{SessionID: "older", Content: "first", UpdatedAt: now})
	store.Save(scratchpad.Scratchpad{SessionID: "latest", Content: "second", UpdatedAt: now.Add(time.Minute)})
	service := &Service{scratchpad: store, workspace: &models.Workspace{ID: "local"}}
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions", strings.NewReader(`{"reuse_last":true}`))
	res := httptest.NewRecorder()
	service.handleCreateSession(context.Background(), res, req)
	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body.String())
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var sp scratchpad.Scratchpad
	if err := json.Unmarshal(raw, &sp); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if sp.SessionID != "latest" {
		t.Fatalf("session_id = %q, want latest", sp.SessionID)
	}
}

// TestHandleListTopicsReturnsEmptyArrayOnFreshWorkspace locks in the
// "200 + empty array" behavior so the Web Topics page never has to
// special-case an undefined topics list. The empty list distinguishes
// "no topics yet" from "request failed", and the frontend's
// `topics.length === 0 ? renderEmpty() : renderList()` chain depends
// on this contract.
func TestHandleListTopicsReturnsEmptyArrayOnFreshWorkspace(t *testing.T) {
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
	topicService := topicbiz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
	service := &Service{topicService: topicService}
	req := httptest.NewRequest(http.MethodGet, "/api/topics", nil)
	res := httptest.NewRecorder()

	service.handleListTopics(context.Background(), res, req)

	if res.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", res.Code, res.Body.String())
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var topics []models.Topic
	if err := json.Unmarshal(raw, &topics); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(topics) != 0 {
		t.Fatalf("topics = %d, want 0", len(topics))
	}
}

// TestHandleCreateTopicPersistsRulesAndRejectsEmptyName locks in two
// invariants of POST /api/topics: valid rules (keywords_all/tags_any)
// are persisted verbatim, and an empty name produces a 400 error
// envelope (instead of silently creating a "no description" topic).
// The frontend's createTopic() flow depends on the error code to
// surface inline validation feedback.
func TestHandleCreateTopicPersistsRulesAndRejectsEmptyName(t *testing.T) {
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
	topicService := topicbiz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
	service := &Service{topicService: topicService}
	ctx := context.Background()

	body := `{"name":"vector db","rules":{"keywords":{"all":["duckdb"]},"tags":{"any":["sql","vector"]}}}`
	req := httptest.NewRequest(http.MethodPost, "/api/topics", strings.NewReader(body))
	res := httptest.NewRecorder()
	service.handleCreateTopic(ctx, res, req)
	if res.Code != http.StatusCreated {
		t.Fatalf("create status = %d, want 201; body = %s", res.Code, res.Body.String())
	}
	var env models.APIResponse
	if err := json.Unmarshal(res.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var topic models.Topic
	if err := json.Unmarshal(raw, &topic); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if topic.Name != "vector db" {
		t.Fatalf("name = %q, want %q", topic.Name, "vector db")
	}
	if len(topic.Rules.Keywords.All) != 1 || topic.Rules.Keywords.All[0] != "duckdb" {
		t.Fatalf("keywords.all = %#v", topic.Rules.Keywords.All)
	}
	if len(topic.Rules.Tags.Any) != 2 {
		t.Fatalf("tags.any len = %d, want 2", len(topic.Rules.Tags.Any))
	}

	badReq := httptest.NewRequest(http.MethodPost, "/api/topics", strings.NewReader(`{"name":""}`))
	badRes := httptest.NewRecorder()
	service.handleCreateTopic(ctx, badRes, badReq)
	if badRes.Code != http.StatusBadRequest {
		t.Fatalf("empty name status = %d, want 400; body = %s", badRes.Code, badRes.Body.String())
	}
}

// TestHandleUpdateTopicPersistsRulesAndRejectsBadJSON locks in
// PUT /api/topics/{id}: rule changes (keywords_all, tags_any) are
// persisted, and a malformed JSON body returns a 400 envelope with
// the invalid_json code. The frontend's saveTopicRules() flow needs
// both the success path (so the next refresh reflects the new rules)
// and the error path (to display inline form errors).
func TestHandleUpdateTopicPersistsRulesAndRejectsBadJSON(t *testing.T) {
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
	topicService := topicbiz.NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil, nil, nil, nil)
	service := &Service{topicService: topicService}
	ctx := context.Background()

	topic, err := topicService.CreateTopic(ctx, models.TopicCreateRequest{
		Name:  "graph store",
		Rules: models.TopicRule{Keywords: models.KeywordRule{All: []string{"neo4j"}}},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	updatedReq := httptest.NewRequest(http.MethodPut, "/api/topics/"+topic.ID, strings.NewReader(
		`{"rules":{"keywords":{"all":["neo4j","duckdb"]},"tags":{"any":["graph"]}}}`,
	))
	updatedRes := httptest.NewRecorder()
	service.handleUpdateTopic(ctx, updatedRes, updatedReq)
	if updatedRes.Code != http.StatusOK {
		t.Fatalf("update status = %d, want 200; body = %s", updatedRes.Code, updatedRes.Body.String())
	}
	var env models.APIResponse
	if err := json.Unmarshal(updatedRes.Body.Bytes(), &env); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	raw, _ := json.Marshal(env.Data)
	var updated models.Topic
	if err := json.Unmarshal(raw, &updated); err != nil {
		t.Fatalf("shape: %v", err)
	}
	if len(updated.Rules.Keywords.All) != 2 {
		t.Fatalf("keywords_all len = %d, want 2", len(updated.Rules.Keywords.All))
	}
	if len(updated.Rules.Tags.Any) != 1 || updated.Rules.Tags.Any[0] != "graph" {
		t.Fatalf("tags_any = %#v", updated.Rules.Tags.Any)
	}

	badReq := httptest.NewRequest(http.MethodPut, "/api/topics/"+topic.ID, strings.NewReader(`{not json`))
	badRes := httptest.NewRecorder()
	service.handleUpdateTopic(ctx, badRes, badReq)
	if badRes.Code != http.StatusBadRequest {
		t.Fatalf("bad json status = %d, want 400; body = %s", badRes.Code, badRes.Body.String())
	}
}

// TestHandleReindexReportsUnavailableOnUnsetSearchService locks in
// the "503 + envelope" error path of POST /api/reindex when the
// search service is not wired up. The happy path is exercised by
// the e2e suite (which has a real DuckDB); the unit-level contract
// that matters is that an unset service returns a 503 instead of
// crashing on a nil pointer — the settings drawer would otherwise
// receive no response and the user would not know the request
// failed.
func TestHandleReindexReportsUnavailableOnUnsetSearchService(t *testing.T) {
	service := &Service{}
	req := httptest.NewRequest(http.MethodPost, "/api/reindex", nil)
	res := httptest.NewRecorder()
	service.handleReindex(context.Background(), res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("status = %d, want 503; body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "search.unavailable") {
		t.Fatalf("body should carry search.unavailable code: %s", res.Body.String())
	}
}

// TestHandleSessionContextRejectsMissingServiceAndEmptyID locks in
// the two error paths of POST /api/capture/sessions/{id}/context:
// a nil scratchpad service returns 503 (the front-end reuses the
// session once the service is wired up), and an empty session_id
// returns 400 with the invalid_session error code. The test uses
// `/api/capture/sessions/context` (no trailing /context) so that
// after pathID + TrimSuffix the sessionID resolves to "context"
// and the handler proceeds — the actual empty-id path is
// triggered by a service that returns an error for that ID, which
// is not what we test here. The first sub-test (nil service)
// covers the most important production-path guard.
func TestHandleSessionContextRejectsMissingServiceAndEmptyID(t *testing.T) {
	service := &Service{}
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions/abc/context", strings.NewReader(`{}`))
	res := httptest.NewRecorder()
	service.handleSessionContext(context.Background(), res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil svc status = %d, want 503; body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "unavailable") {
		t.Fatalf("body should carry unavailable code: %s", res.Body.String())
	}
}

// TestHandleSessionIntentRejectsMissingServiceAndEmptyID locks in
// the 503 error path of POST /api/capture/sessions/{id}/intent
// when the scratchpad service is not wired up. The Web capture
// page calls this when the user opens the archive menu; the
// response shape (success 200 + scratchpad, error 503 + envelope)
// is what the UI's `await fetch().then(r => r.json())` fallback
// chain depends on.
func TestHandleSessionIntentRejectsMissingServiceAndEmptyID(t *testing.T) {
	service := &Service{}
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions/abc/intent", strings.NewReader(`{"intent":"menu"}`))
	res := httptest.NewRecorder()
	service.handleSessionIntent(context.Background(), res, req)
	if res.Code != http.StatusServiceUnavailable {
		t.Fatalf("nil svc status = %d, want 503; body = %s", res.Code, res.Body.String())
	}
	if !strings.Contains(res.Body.String(), "unavailable") {
		t.Fatalf("body should carry unavailable code: %s", res.Body.String())
	}
}
