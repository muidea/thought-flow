package biz

import (
	"context"
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
	if !contains(gotThought.AITags, "engineering") {
		t.Fatalf("expected engineering tag, got %#v", gotThought.AITags)
	}
	if !strings.Contains(gotContent.AINotes, "Summary:") {
		t.Fatalf("expected AI notes summary, got %q", gotContent.AINotes)
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
	service.refineJob(job)

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

func contains(values []string, expected string) bool {
	for _, value := range values {
		if value == expected {
			return true
		}
	}
	return false
}
