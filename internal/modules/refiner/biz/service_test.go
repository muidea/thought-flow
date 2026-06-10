package biz

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/webfetch"
)

func TestRefineNowWritesSummaryTagsAndStatus(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	now := time.Date(2026, 6, 9, 15, 0, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            "20260609-150000-refine",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath("20260609-150000-refine")),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("DuckDB and markdown search should work."),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	content := models.ThoughtContent{Original: "DuckDB and markdown search should work."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	service := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, ai.NewLocalRefineProvider(), webfetch.New(time.Second))
	refinement, err := service.RefineNow(context.Background(), thought.ID)
	if err != nil {
		t.Fatalf("RefineNow() error = %v", err)
	}
	if refinement.Status != models.RefineStatusRefined {
		t.Fatalf("refinement status = %q", refinement.Status)
	}
	if refinement.Embedding == nil || len(refinement.Embedding.Vector) == 0 {
		t.Fatalf("expected refinement embedding")
	}

	gotThought, gotContent, err := markdown.ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if gotThought.RefineStatus != models.RefineStatusRefined {
		t.Fatalf("thought refine status = %q", gotThought.RefineStatus)
	}
	if gotThought.Summary == "" {
		t.Fatalf("expected summary")
	}
	if len(gotThought.Errors) != 0 {
		t.Fatalf("expected successful refine to clear previous errors, got %#v", gotThought.Errors)
	}
	if !contains(gotThought.AITags, "engineering") {
		t.Fatalf("expected engineering tag, got %#v", gotThought.AITags)
	}
	if !strings.Contains(gotContent.AINotes, "Summary:") {
		t.Fatalf("expected AI notes summary, got %q", gotContent.AINotes)
	}
}

func TestRefineNowSkipsUnchangedRefinedThought(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	original := "Already refined input should not call provider again."
	now := time.Date(2026, 6, 9, 15, 10, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            "20260609-151000-skip",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath("20260609-151000-skip")),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash(original),
		Summary:       "existing summary",
		KeyPoints:     []string{"existing point"},
		AITags:        []string{"existing"},
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	content := models.ThoughtContent{Original: original, AINotes: "Summary: existing summary"}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	provider := &countingRefineProvider{}
	service := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, provider, webfetch.New(time.Second))
	refinement, err := service.RefineNow(context.Background(), thought.ID)
	if err != nil {
		t.Fatalf("RefineNow() error = %v", err)
	}
	if provider.calls != 0 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if refinement.Model != skippedUnchangedModel || refinement.InputHash != thought.ContentHash {
		t.Fatalf("refinement = %#v", refinement)
	}
	gotThought, gotContent, err := markdown.ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if !gotThought.UpdatedAt.Equal(now) || gotContent.AINotes != content.AINotes {
		t.Fatalf("thought should not be rewritten: %#v %#v", gotThought, gotContent)
	}
}

func TestForceRefineIgnoresUnchangedSkip(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	original := "Retry should force refinement even when input is unchanged."
	now := time.Date(2026, 6, 9, 15, 20, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            "20260609-152000-force",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath("20260609-152000-force")),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash(original),
		Summary:       "existing summary",
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	content := models.ThoughtContent{Original: original, AINotes: "Summary: existing summary"}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	provider := &countingRefineProvider{summary: "forced summary"}
	service := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, provider, webfetch.New(time.Second))
	refinement, err := service.refineNow(context.Background(), thought.ID, true)
	if err != nil {
		t.Fatalf("refineNow(force) error = %v", err)
	}
	if provider.calls != 1 {
		t.Fatalf("provider calls = %d", provider.calls)
	}
	if refinement.Model == skippedUnchangedModel || refinement.Summary != "forced summary" {
		t.Fatalf("refinement = %#v", refinement)
	}
}

func TestRefineURLFetchFailurePreservesOriginalThought(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		http.Error(res, "origin unavailable", http.StatusBadGateway)
	}))
	defer origin.Close()
	reader := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		http.Error(res, "reader unavailable", http.StatusBadGateway)
	}))
	defer reader.Close()

	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	thoughtID := "20260609-152500-url-fail"
	original := origin.URL
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeURL,
		Source:        models.ThoughtSourceManual,
		URL:           origin.URL,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID)),
		CreatedAt:     time.Date(2026, 6, 9, 15, 25, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 9, 15, 25, 0, 0, time.UTC),
		ContentHash:   models.ContentHash(original),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: original}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	service := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, ai.NewLocalRefineProvider(), webfetch.New(time.Second, webfetch.WithReaderBaseURL(reader.URL)))
	_, err := service.RefineNow(context.Background(), thoughtID)
	if err == nil {
		t.Fatalf("expected fetch failure")
	}
	gotThought, gotContent, readErr := markdown.ReadThought(root, thoughtID)
	if readErr != nil {
		t.Fatalf("ReadThought() error = %v", readErr)
	}
	if gotContent.Original != original {
		t.Fatalf("original content changed: %q", gotContent.Original)
	}
	if gotThought.RefineStatus != models.RefineStatusFailed || len(gotThought.Errors) == 0 {
		t.Fatalf("thought after failure = %#v", gotThought)
	}
	if gotThought.Errors[0].Code != "thoughtflow.refiner.fetch_failed" || !gotThought.Errors[0].Retryable {
		t.Fatalf("error ref = %#v", gotThought.Errors[0])
	}
}

func TestRefineURLProviderFailurePreservesFetchedContent(t *testing.T) {
	origin := httptest.NewServer(http.HandlerFunc(func(res http.ResponseWriter, req *http.Request) {
		res.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = res.Write([]byte(`<html><head><title>Fetched title</title></head><body><main><h1>Fetched heading</h1><p>Useful URL body survives provider failure.</p></main></body></html>`))
	}))
	defer origin.Close()

	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	thoughtID := "20260609-152700-url-provider-fail"
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeURL,
		Source:        models.ThoughtSourceManual,
		URL:           origin.URL,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID)),
		CreatedAt:     time.Date(2026, 6, 9, 15, 27, 0, 0, time.UTC),
		UpdatedAt:     time.Date(2026, 6, 9, 15, 27, 0, 0, time.UTC),
		ContentHash:   models.ContentHash(origin.URL),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
		Errors:        []models.ErrorRef{models.NewErrorRef("thoughtflow.refiner.provider_failed", "old provider failure", true)},
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: origin.URL}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	service := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, failingRefineProvider{}, webfetch.New(time.Second))
	_, err := service.RefineNow(context.Background(), thoughtID)
	if err == nil {
		t.Fatalf("expected provider failure")
	}
	gotThought, gotContent, readErr := markdown.ReadThought(root, thoughtID)
	if readErr != nil {
		t.Fatalf("ReadThought() error = %v", readErr)
	}
	if gotThought.ExtractedTitle != "Fetched title" {
		t.Fatalf("extracted title = %q", gotThought.ExtractedTitle)
	}
	if !strings.Contains(gotContent.ExtractedContent, "Useful URL body survives provider failure") {
		t.Fatalf("extracted content = %q", gotContent.ExtractedContent)
	}
	if gotThought.RefineStatus != models.RefineStatusFailed || len(gotThought.Errors) == 0 {
		t.Fatalf("thought after provider failure = %#v", gotThought)
	}
	if gotThought.Errors[0].Code != "thoughtflow.refiner.provider_failed" || !gotThought.Errors[0].Retryable {
		t.Fatalf("error ref = %#v", gotThought.Errors[0])
	}
	if len(gotThought.Errors) != 1 || strings.Contains(gotThought.Errors[0].Message, "old provider failure") {
		t.Fatalf("provider errors should be replaced, got %#v", gotThought.Errors)
	}
}

func TestRefineJobRetriesRetryableProviderFailure(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:           "local",
		RootPath:     root,
		ThoughtsPath: filepath.Join(root, "thoughts"),
		RuntimePath:  filepath.Join(root, ".thoughtflow"),
		JobsPath:     filepath.Join(root, ".thoughtflow", "jobs"),
	}
	if err := os.MkdirAll(ws.JobsPath, 0o755); err != nil {
		t.Fatalf("mkdir jobs: %v", err)
	}
	thoughtID := "20260609-153000-retry"
	now := time.Date(2026, 6, 9, 15, 30, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID)),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("retry transient ai failure"),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	if err := markdown.WriteThought(root, thought, models.ThoughtContent{Original: "retry transient ai failure"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	jobs := jobstore.New(ws.JobsPath)
	service := NewService(ws, jobs, nil, nil, &flakyRefineProvider{}, webfetch.New(time.Second))
	job, err := jobs.CreateWithMaxAttempts(models.JobTypeRefine, models.ResourceTypeThought, thoughtID, "refine queued", refineMaxAttempts)
	if err != nil {
		t.Fatalf("CreateWithMaxAttempts() error = %v", err)
	}
	service.refineJob(job, false)

	loaded, err := jobs.Get(job.ID)
	if err != nil {
		t.Fatalf("Get(job) error = %v", err)
	}
	if loaded.Status != models.JobStatusSucceeded || loaded.Attempt != 2 || loaded.Error != nil {
		t.Fatalf("loaded job = %#v", loaded)
	}
	gotThought, _, err := markdown.ReadThought(root, thoughtID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if gotThought.RefineStatus != models.RefineStatusRefined || gotThought.Summary != "retry succeeded" {
		t.Fatalf("thought after retry = %#v", gotThought)
	}
}

type flakyRefineProvider struct {
	attempts int
}

func (p *flakyRefineProvider) Refine(ctx context.Context, req ai.RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	p.attempts++
	if p.attempts == 1 {
		return models.ThoughtRefinement{}, ai.ProviderError{
			Code:       "thoughtflow.ai.transient_status",
			StatusCode: 502,
			Message:    "temporary provider failure",
			Retryable:  true,
		}
	}
	return models.ThoughtRefinement{
		ThoughtID:   req.Thought.ID,
		Status:      models.RefineStatusRefined,
		Summary:     "retry succeeded",
		KeyPoints:   []string{"retried"},
		AITags:      []string{"retry"},
		GeneratedAt: time.Now().UTC(),
	}, nil
}

type failingRefineProvider struct{}

func (failingRefineProvider) Refine(ctx context.Context, req ai.RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	_ = req
	return models.ThoughtRefinement{}, ai.ProviderError{
		Code:      "thoughtflow.ai.request_failed",
		Message:   "provider timeout",
		Retryable: true,
	}
}

type countingRefineProvider struct {
	calls   int
	summary string
}

func (p *countingRefineProvider) Refine(ctx context.Context, req ai.RefineRequest) (models.ThoughtRefinement, error) {
	_ = ctx
	p.calls++
	summary := p.summary
	if summary == "" {
		summary = "counted summary"
	}
	return models.ThoughtRefinement{
		ThoughtID:   req.Thought.ID,
		Status:      models.RefineStatusRefined,
		Summary:     summary,
		KeyPoints:   []string{"counted"},
		AITags:      []string{"counted"},
		Model:       "counting",
		InputHash:   models.ContentHash(req.Content.Original),
		GeneratedAt: time.Now().UTC(),
	}, nil
}

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
