package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/thoughtlock"
	"thoughtflow/internal/pkg/webfetch"
)

type fakeSearcher struct {
	mu      sync.Mutex
	results []models.SearchResult
	err     error
	calls   int
}

func (f *fakeSearcher) Search(ctx context.Context, query models.SearchQuery) (models.SearchResponse, error) {
	_ = ctx
	_ = query
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return models.SearchResponse{}, f.err
	}
	return models.SearchResponse{Items: append([]models.SearchResult(nil), f.results...)}, nil
}

type fakeTopicSuggester struct {
	mu      sync.Mutex
	results []models.TopicMatchSuggestion
	err     error
	calls   int
}

func (f *fakeTopicSuggester) NearMissTopics(ctx context.Context, thoughtID string, topK int, minScore float64) ([]models.TopicMatchSuggestion, error) {
	_ = ctx
	_ = thoughtID
	_ = topK
	_ = minScore
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return nil, f.err
	}
	return append([]models.TopicMatchSuggestion(nil), f.results...), nil
}

type fakeExpandProvider struct {
	mu    sync.Mutex
	plan  string
	err   error
	calls int
}

func (f *fakeExpandProvider) Expand(ctx context.Context, req ai.ExpandRequest) (ai.ExpandResult, error) {
	_ = ctx
	_ = req
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	if f.err != nil {
		return ai.ExpandResult{}, f.err
	}
	return ai.ExpandResult{Plan: f.plan, Model: "fake", GeneratedAt: time.Now().UTC()}, nil
}

func newTestService(t *testing.T) (*Service, *fakeSearcher, *fakeTopicSuggester, *fakeExpandProvider, *thoughtlock.Locker, string) {
	t.Helper()
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
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	searcher := &fakeSearcher{}
	suggester := &fakeTopicSuggester{}
	provider := &fakeExpandProvider{}
	fetcher := webfetch.New(time.Second)
	locker := thoughtlock.New(time.Minute)
	svc := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, provider, fetcher, locker)
	svc.SetSearcherLookup(func() Searcher { return searcher })
	svc.SetTopicSuggesterLookup(func() TopicSuggester { return suggester })
	return svc, searcher, suggester, provider, locker, root
}

func writeTestThought(t *testing.T, root string, id string, thoughtType string, url string) {
	t.Helper()
	now := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            id,
		Type:          thoughtType,
		Source:        models.ThoughtSourceManual,
		UserTitle:     "测试主题 " + id,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(id)),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("seed"),
		Summary:       "笔记摘要",
		KeyPoints:     []string{"关键点 1", "关键点 2"},
		AITags:        []string{"ai", "test"},
		RefineStatus:  models.RefineStatusRefined,
		CaptureStatus: models.CaptureStatusCaptured,
	}
	if thoughtType == models.ThoughtTypeURL {
		thought.URL = url
	}
	content := models.ThoughtContent{Original: "原始内容 " + id}
	if thoughtType == models.ThoughtTypeURL {
		content.ExtractedContent = "抓取内容 " + id
	}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
}

func TestExpandJobWritesRelatedAndPlan(t *testing.T) {
	svc, searcher, suggester, provider, _, root := newTestService(t)
	searcher.results = []models.SearchResult{
		{ThoughtID: "20260610-100000-rag", Title: "RAG note", Score: 0.9},
		{ThoughtID: "20260610-110000-crawl", Title: "Crawl note", Score: 0.8},
		{ThoughtID: "20260611-120000-pipe", Title: "Pipeline note", Score: 0.7},
		{ThoughtID: "20260612-120000-target", Title: "Self", Score: 0.99},
	}
	suggester.results = []models.TopicMatchSuggestion{
		{TopicID: "topic-pipelines", TopicName: "Pipelines", Score: 0.62},
		{TopicID: "topic-web", TopicName: "Web", Score: 0.55},
	}
	provider.plan = "## 背景\n用户提交了一条 web 采集相关笔记\n\n## 步骤\n1. ..."

	writeTestThought(t, root, "20260612-120000-target", models.ThoughtTypeText, "")

	job, err := svc.ExpandAsync("20260612-120000-target")
	if err != nil {
		t.Fatalf("ExpandAsync() error = %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		updated, _ := jobstore.New(filepath.Join(root, ".thoughtflow", "jobs")).Get(job.ID)
		if updated.Status == models.JobStatusSucceeded || updated.Status == models.JobStatusFailed {
			job = updated
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("expand job did not finish: %+v", job)
		}
		time.Sleep(50 * time.Millisecond)
	}
	if job.Status != models.JobStatusSucceeded {
		t.Fatalf("expand job status = %q, want succeeded; message = %q", job.Status, job.Message)
	}
	thought, _, err := markdown.ReadThought(root, "20260612-120000-target")
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if got, want := thought.RelatedThoughtIDs, []string{"20260610-100000-rag", "20260610-110000-crawl", "20260611-120000-pipe"}; !equalStringSlices(got, want) {
		t.Fatalf("RelatedThoughtIDs = %v, want %v", got, want)
	}
	if got, want := thought.SuggestedTopicIDs, []string{"topic-pipelines", "topic-web"}; !equalStringSlices(got, want) {
		t.Fatalf("SuggestedTopicIDs = %v, want %v", got, want)
	}
	if thought.ExpansionPlan != provider.plan {
		t.Fatalf("ExpansionPlan = %q, want %q", thought.ExpansionPlan, provider.plan)
	}
}

func TestExpandJobPartialFailureStillWrites(t *testing.T) {
	svc, searcher, suggester, provider, _, root := newTestService(t)
	searcher.err = errors.New("search index offline")
	suggester.results = []models.TopicMatchSuggestion{
		{TopicID: "topic-only", TopicName: "Only", Score: 0.5},
	}
	provider.plan = "## 背景\nplan still works"
	writeTestThought(t, root, "20260612-130000-partial", models.ThoughtTypeText, "")

	job, err := svc.ExpandAsync("20260612-130000-partial")
	if err != nil {
		t.Fatalf("ExpandAsync() error = %v", err)
	}
	waitForJob(t, filepath.Join(root, ".thoughtflow", "jobs"), job, 5*time.Second)
	thought, _, err := markdown.ReadThought(root, "20260612-130000-partial")
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if thought.ExpansionPlan != "## 背景\nplan still works" {
		t.Fatalf("plan not persisted: %q", thought.ExpansionPlan)
	}
	if len(thought.SuggestedTopicIDs) != 1 || thought.SuggestedTopicIDs[0] != "topic-only" {
		t.Fatalf("near-miss topic missing: %#v", thought.SuggestedTopicIDs)
	}
	if len(thought.Errors) == 0 || thought.Errors[0].Code != "thoughtflow.expand.partial_failed" {
		t.Fatalf("expected partial-failed error, got: %#v", thought.Errors)
	}
}

func TestExpandJobSkipsWhenLocked(t *testing.T) {
	svc, _, _, _, locker, root := newTestService(t)
	provider := svc.provider
	_ = provider
	writeTestThought(t, root, "20260612-140000-locked", models.ThoughtTypeText, "")

	if err := locker.Acquire("20260612-140000-locked", "other-session"); err != nil {
		t.Fatalf("prelock acquire error: %v", err)
	}

	job, err := svc.ExpandAsync("20260612-140000-locked")
	if err != nil {
		t.Fatalf("ExpandAsync() error = %v", err)
	}
	final := waitForJob(t, filepath.Join(root, ".thoughtflow", "jobs"), job, 5*time.Second)
	if final.Status != models.JobStatusSucceeded {
		t.Fatalf("expected succeeded (skipped), got %q", final.Status)
	}
	if !contains(final.Message, "skipped") {
		t.Fatalf("expected skipped message, got %q", final.Message)
	}
}

func TestExpandJobURLFollowupsSkippedForText(t *testing.T) {
	svc, _, _, _, _, root := newTestService(t)
	writeTestThought(t, root, "20260612-150000-text", models.ThoughtTypeText, "")
	job, err := svc.ExpandAsync("20260612-150000-text")
	if err != nil {
		t.Fatalf("ExpandAsync() error = %v", err)
	}
	waitForJob(t, filepath.Join(root, ".thoughtflow", "jobs"), job, 5*time.Second)
	thought, _, err := markdown.ReadThought(root, "20260612-150000-text")
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if len(thought.URLFollowups) != 0 {
		t.Fatalf("expected no URL followups for text type, got %#v", thought.URLFollowups)
	}
}

func waitForJob(t *testing.T, jobsPath string, initial models.Job, deadline time.Duration) models.Job {
	t.Helper()
	store := jobstore.New(jobsPath)
	end := time.Now().Add(deadline)
	for {
		updated, err := store.Get(initial.ID)
		if err == nil && (updated.Status == models.JobStatusSucceeded || updated.Status == models.JobStatusFailed) {
			return updated
		}
		if time.Now().After(end) {
			t.Fatalf("job %s did not finish in %s (status=%s)", initial.ID, deadline, updated.Status)
		}
		time.Sleep(20 * time.Millisecond)
	}
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

func contains(s string, substr string) bool {
	return len(s) >= len(substr) && (len(substr) == 0 || indexOf(s, substr) >= 0)
}

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}
