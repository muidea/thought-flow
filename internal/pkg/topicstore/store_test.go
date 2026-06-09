package topicstore

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func TestStoreCreateMatchAndAddMembership(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	ctx := context.Background()

	topic, err := store.Create(ctx, models.TopicCreateRequest{
		Name:        "DuckDB Notes",
		Description: "Local analytical database notes.",
		Rules: models.TopicRule{
			Keywords: models.KeywordRule{Any: []string{"duckdb"}},
			Tags:     models.TagRule{Any: []string{"engineering"}},
		},
	})
	if err != nil {
		t.Fatalf("Create() error = %v", err)
	}
	if topic.ID != "duckdb-notes" {
		t.Fatalf("topic id = %q", topic.ID)
	}

	thought := testThought("20260609-143010-8f3a", "Query planner note", []string{"engineering"})
	content := models.ThoughtContent{Original: "DuckDB can keep local thought search fast."}
	if err := markdown.WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}

	membership, ok := store.MatchThought(topic, thought, content)
	if !ok {
		t.Fatalf("expected topic rule match")
	}
	if membership.Score <= 0 {
		t.Fatalf("expected positive match score, got %v", membership.Score)
	}
	if !containsString(membership.Reasons, "keyword:duckdb") {
		t.Fatalf("expected keyword reason, got %#v", membership.Reasons)
	}

	updated, changed, err := store.AddMembership(ctx, topic, thought, content, membership)
	if err != nil {
		t.Fatalf("AddMembership() error = %v", err)
	}
	if !changed {
		t.Fatalf("expected membership to be added")
	}
	if updated.MemberCount != 1 {
		t.Fatalf("member count = %d", updated.MemberCount)
	}

	document, err := store.ReadDocument(ctx, updated.ID)
	if err != nil {
		t.Fatalf("ReadDocument() error = %v", err)
	}
	if !strings.Contains(document, "## Query planner note") {
		t.Fatalf("expected thought section in document:\n%s", document)
	}
	if !strings.Contains(document, "Sources: [[../../thoughts/2026/06/20260609-143010-8f3a.md]]") {
		t.Fatalf("expected relative source link in document:\n%s", document)
	}

	_, changed, err = store.AddMembership(ctx, updated, thought, content, membership)
	if err != nil {
		t.Fatalf("second AddMembership() error = %v", err)
	}
	if changed {
		t.Fatalf("duplicate membership should not change topic")
	}
}

func TestStoreMatchHonorsManualExclude(t *testing.T) {
	store := New(t.TempDir())
	thought := testThought("20260609-143010-8f3a", "Excluded", nil)
	content := models.ThoughtContent{Original: "DuckDB appears in this text."}
	topic := models.Topic{
		ID:   "duckdb-notes",
		Name: "DuckDB Notes",
		Rules: models.TopicRule{
			Keywords:      models.KeywordRule{Any: []string{"duckdb"}},
			ManualExclude: []string{thought.ID},
		},
	}

	if _, ok := store.MatchThought(topic, thought, content); ok {
		t.Fatalf("expected manual exclude to suppress rule match")
	}
}

func testThought(id string, title string, tags []string) models.Thought {
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
		UserTags:      tags,
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusUnmatched,
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
