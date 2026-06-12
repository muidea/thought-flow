package markdown

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestWriteAndReadThought(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 9, 14, 30, 10, 0, time.UTC)
	thought := models.Thought{
		ID:            "20260609-143010-8f3a",
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		UserTitle:     "Test title",
		Path:          filepath.ToSlash(ThoughtRelativePath("20260609-143010-8f3a")),
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("hello"),
		UserTags:      []string{"idea"},
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
		Errors: []models.ErrorRef{
			{
				Code:       "thoughtflow.capture.duplicate_warned",
				Message:    "possible duplicate content",
				OccurredAt: now,
				Retryable:  false,
			},
		},
	}
	content := models.ThoughtContent{Original: "hello"}

	if err := WriteThought(root, thought, content); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	gotThought, gotContent, err := ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if gotThought.ID != thought.ID {
		t.Fatalf("thought id = %q, want %q", gotThought.ID, thought.ID)
	}
	if gotThought.DisplayTitle != "Test title" {
		t.Fatalf("display title = %q", gotThought.DisplayTitle)
	}
	if gotContent.Original != "hello" {
		t.Fatalf("original = %q", gotContent.Original)
	}
	if len(gotThought.Errors) != 1 ||
		gotThought.Errors[0].Code != "thoughtflow.capture.duplicate_warned" ||
		gotThought.Errors[0].Message != "possible duplicate content" ||
		gotThought.Errors[0].OccurredAt.IsZero() ||
		gotThought.Errors[0].Retryable {
		t.Fatalf("errors = %#v", gotThought.Errors)
	}
}

func TestReadThoughtPreservesHeadingsInsideSections(t *testing.T) {
	root := t.TempDir()
	thoughtID := "20260610-091500-headings"
	relPath := filepath.ToSlash(ThoughtRelativePath(thoughtID))
	targetPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	raw := `---
id: "20260610-091500-headings"
type: "url"
source: "manual"
path: "thoughts/2026/06/20260610-091500-headings.md"
errors: []
---

## Original

https://github.com/example/project

## Extracted Content

# Project

Intro paragraph.

## Features

- One
- Two

` + "```bash\nmake check\n```\n" + `
## AI Notes

Summary: generated
`
	if err := os.WriteFile(targetPath, []byte(raw), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	_, content, err := ReadThought(root, thoughtID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if !strings.Contains(content.ExtractedContent, "## Features") ||
		!strings.Contains(content.ExtractedContent, "make check") {
		t.Fatalf("extracted content was truncated:\n%s", content.ExtractedContent)
	}
	if strings.Contains(content.ExtractedContent, "## AI Notes") {
		t.Fatalf("extracted content crossed section boundary:\n%s", content.ExtractedContent)
	}
	if content.AINotes != "Summary: generated" {
		t.Fatalf("ai notes = %q", content.AINotes)
	}
}

func TestWriteThoughtPreservesUnknownFrontMatter(t *testing.T) {
	root := t.TempDir()
	thoughtID := "20260609-143010-8f3a"
	relPath := filepath.ToSlash(ThoughtRelativePath(thoughtID))
	targetPath := filepath.Join(root, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(targetPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	oldRaw := `---
id: "20260609-143010-8f3a"
type: "text"
source: "manual"
path: "thoughts/2026/06/20260609-143010-8f3a.md"
summary: "old summary"
embedding_ref: "duckdb:thought_embeddings/20260609-143010-8f3a"
external_meta:
  owner: "research"
  confidence: "draft"
review_flags:
  - "human-review"
errors: []
---

## Original

hello
`
	if err := os.WriteFile(targetPath, []byte(oldRaw), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	now := time.Date(2026, 6, 9, 15, 0, 0, 0, time.UTC)
	thought := models.Thought{
		ID:            thoughtID,
		Type:          models.ThoughtTypeText,
		Source:        models.ThoughtSourceManual,
		Path:          relPath,
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   models.ContentHash("hello updated"),
		Summary:       "new summary",
		CaptureStatus: models.CaptureStatusCaptured,
		RefineStatus:  models.RefineStatusRefined,
		IndexStatus:   models.IndexStatusIndexed,
		TopicStatus:   models.TopicStatusMatched,
	}
	if err := WriteThought(root, thought, models.ThoughtContent{Original: "hello updated"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	raw, err := os.ReadFile(targetPath)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	text := string(raw)
	for _, expected := range []string{
		`summary: "new summary"`,
		`embedding_ref: "duckdb:thought_embeddings/20260609-143010-8f3a"`,
		`external_meta:`,
		`  owner: "research"`,
		`review_flags:`,
		`  - "human-review"`,
	} {
		if !strings.Contains(text, expected) {
			t.Fatalf("updated thought missing %q:\n%s", expected, text)
		}
	}
	if strings.Contains(text, `summary: "old summary"`) {
		t.Fatalf("known summary field was not replaced:\n%s", text)
	}
	if strings.Count(text, "errors: []") != 1 {
		t.Fatalf("known errors field should not be duplicated:\n%s", text)
	}
}

func TestAppendAINotes_CreatesSectionIfMissing(t *testing.T) {
	now := time.Date(2026, 6, 10, 9, 0, 0, 0, time.UTC)
	got := AppendAINotes("", "First note", now)
	if !strings.Contains(got, "## AI Notes") {
		t.Fatalf("expected AI Notes header, got %q", got)
	}
	if !strings.Contains(got, "First note") {
		t.Fatalf("expected note text in output, got %q", got)
	}
	if !strings.Contains(got, "2026-06-10 09:00:00 UTC") {
		t.Fatalf("expected timestamp heading, got %q", got)
	}
}

func TestAppendAINotes_AppendsBelowExisting(t *testing.T) {
	now := time.Date(2026, 6, 10, 9, 30, 0, 0, time.UTC)
	existing := "## AI Notes\n\n### 2026-06-10 09:00:00 UTC\nEarlier note\n\n---"
	got := AppendAINotes(existing, "Newer note", now)
	if !strings.Contains(got, "Earlier note") {
		t.Fatalf("existing note lost: %q", got)
	}
	if !strings.Contains(got, "Newer note") {
		t.Fatalf("new note missing: %q", got)
	}
	if !strings.Contains(got, "2026-06-10 09:30:00 UTC") {
		t.Fatalf("new timestamp missing: %q", got)
	}
	// The trailing separator should still be present and stay at the end
	// of the file, after the new note.
	noteIdx := strings.Index(got, "Newer note")
	sepIdx := strings.LastIndex(got, "---")
	if noteIdx < 0 || sepIdx < 0 || sepIdx <= noteIdx {
		t.Fatalf("separator should come after new note: note=%d sep=%d body=%q", noteIdx, sepIdx, got)
	}
}

func TestWriteAndReadExpansionFields(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 6, 12, 10, 15, 0, 0, time.UTC)
	thought := models.Thought{
		ID:                "20260612-101500-expand",
		Type:              models.ThoughtTypeURL,
		Source:            models.ThoughtSourceManual,
		UserTitle:         "Web 页面采集",
		Path:              filepath.ToSlash(ThoughtRelativePath("20260612-101500-expand")),
		CreatedAt:         now,
		UpdatedAt:         now,
		ContentHash:       models.ContentHash("seed"),
		RelatedThoughtIDs: []string{"20260501-090000-rag", "20260420-080000-crawl"},
		SuggestedTopicIDs: []string{"topic-web-research", "topic-pipelines"},
		URLFollowups: []models.URLFollowup{
			{URL: "https://example.com/a", Title: "A primer", Snippet: "intro to A"},
			{URL: "https://example.com/b", Title: "B deep dive"},
		},
		ExpansionPlan: "## 背景\n用户在搭建一个 web 采集工具。\n\n## 步骤\n1. 列目标站点\n2. 调度抓取",
	}
	if err := WriteThought(root, thought, models.ThoughtContent{Original: "https://example.com/seed"}); err != nil {
		t.Fatalf("WriteThought() error = %v", err)
	}
	gotThought, _, err := ReadThought(root, thought.ID)
	if err != nil {
		t.Fatalf("ReadThought() error = %v", err)
	}
	if got, want := gotThought.RelatedThoughtIDs, []string{"20260420-080000-crawl", "20260501-090000-rag"}; !equalStrings(got, want) {
		t.Fatalf("RelatedThoughtIDs = %v, want %v", got, want)
	}
	if got, want := gotThought.SuggestedTopicIDs, []string{"topic-pipelines", "topic-web-research"}; !equalStrings(got, want) {
		t.Fatalf("SuggestedTopicIDs = %v, want %v", got, want)
	}
	if len(gotThought.URLFollowups) != 2 {
		t.Fatalf("URLFollowups length = %d, want 2", len(gotThought.URLFollowups))
	}
	if gotThought.URLFollowups[0].URL != "https://example.com/a" ||
		gotThought.URLFollowups[0].Title != "A primer" ||
		gotThought.URLFollowups[0].Snippet != "intro to A" {
		t.Fatalf("URLFollowups[0] = %#v", gotThought.URLFollowups[0])
	}
	if gotThought.URLFollowups[1].URL != "https://example.com/b" ||
		gotThought.URLFollowups[1].Title != "B deep dive" ||
		gotThought.URLFollowups[1].Snippet != "" {
		t.Fatalf("URLFollowups[1] = %#v", gotThought.URLFollowups[1])
	}
	if !strings.Contains(gotThought.ExpansionPlan, "## 背景") ||
		!strings.Contains(gotThought.ExpansionPlan, "2. 调度抓取") {
		t.Fatalf("ExpansionPlan = %q", gotThought.ExpansionPlan)
	}
}

func equalStrings(a, b []string) bool {
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

func TestCleanupOrphanThoughtTempFilesRemovesDotTmp(t *testing.T) {
	thoughts := t.TempDir()
	target := filepath.Join(thoughts, "2026", "06")
	if err := os.MkdirAll(target, 0o755); err != nil {
		t.Fatalf("MkdirAll: %v", err)
	}
	real := filepath.Join(target, "abc12345.md")
	if err := os.WriteFile(real, []byte("---\nid: abc12345\n---\n"), 0o644); err != nil {
		t.Fatalf("WriteFile real: %v", err)
	}
	leftover := filepath.Join(target, "abc12345.md.1700000000000000000.tmp")
	if err := os.WriteFile(leftover, []byte("stale"), 0o644); err != nil {
		t.Fatalf("WriteFile leftover: %v", err)
	}
	if err := CleanupOrphanThoughtTempFiles(thoughts); err != nil {
		t.Fatalf("CleanupOrphanThoughtTempFiles: %v", err)
	}
	if _, err := os.Stat(leftover); !os.IsNotExist(err) {
		t.Fatalf("expected leftover removed, stat err = %v", err)
	}
	if _, err := os.Stat(real); err != nil {
		t.Fatalf("real thought file should survive, stat err = %v", err)
	}
}

func TestCleanupOrphanThoughtTempFilesEmptyPathIsNoop(t *testing.T) {
	if err := CleanupOrphanThoughtTempFiles(""); err != nil {
		t.Fatalf("empty path should be a no-op, got %v", err)
	}
}

func TestCleanupOrphanThoughtTempFilesMissingDirIsNoop(t *testing.T) {
	if err := CleanupOrphanThoughtTempFiles(filepath.Join(t.TempDir(), "no", "such", "dir")); err != nil {
		t.Fatalf("missing dir should be a no-op, got %v", err)
	}
}
