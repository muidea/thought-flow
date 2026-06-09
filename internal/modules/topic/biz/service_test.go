package biz

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/topicstore"
)

func TestServiceCreateTopicAndMatchThought(t *testing.T) {
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

	service := NewService(ws, jobstore.New(ws.JobsPath), topicstore.New(root), nil, nil)
	ctx := context.Background()
	topic, err := service.CreateTopic(ctx, models.TopicCreateRequest{
		Name: "Engineering Search",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
		},
	})
	if err != nil {
		t.Fatalf("CreateTopic() error = %v", err)
	}

	thought := serviceTestThought("20260609-143010-8f3a", "Search architecture")
	content := models.ThoughtContent{Original: "DuckDB indexes should support the first local search baseline."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	memberships, err := service.MatchThought(ctx, thought.ID)
	if err != nil {
		t.Fatalf("MatchThought() error = %v", err)
	}
	if len(memberships) != 1 {
		t.Fatalf("membership count = %d, want 1", len(memberships))
	}
	if memberships[0].TopicID != topic.ID {
		t.Fatalf("membership topic = %q, want %q", memberships[0].TopicID, topic.ID)
	}

	detail, err := service.GetTopic(ctx, topic.ID)
	if err != nil {
		t.Fatalf("GetTopic() error = %v", err)
	}
	if detail.Topic.MemberCount != 1 {
		t.Fatalf("topic member count = %d", detail.Topic.MemberCount)
	}
	if !strings.Contains(detail.Document, "DuckDB indexes should support") {
		t.Fatalf("expected matched content in topic document:\n%s", detail.Document)
	}
}

func serviceTestThought(id string, title string) models.Thought {
	now := time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC)
	return models.Thought{
		ID:            id,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     title,
		DisplayTitle:  title,
		Path:          filepath.ToSlash(markdown.ThoughtRelativePath(id)),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash(title),
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
	}
}
