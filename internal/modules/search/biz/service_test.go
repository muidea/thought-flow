package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/searchdb"
)

func TestRuntimeStatusReportsSearchIndexPath(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:       "local",
		RootPath: root,
		JobsPath: filepath.Join(root, ".thoughtflow", "jobs"),
	}
	dbPath := filepath.Join(root, ".thoughtflow", "thoughtflow.duckdb")
	store, err := searchdb.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	if _, err := os.Stat(dbPath); errors.Is(err, os.ErrNotExist) {
		if err := os.WriteFile(dbPath, []byte("duckdb"), 0o644); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	} else if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}

	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, dbPath)
	status := service.RuntimeStatus(context.Background())

	if status.Status != "ready" || status.Path != dbPath || !status.Exists {
		t.Fatalf("status = %#v", status)
	}
}

func TestRuntimeStatusReportsSearchIndexPathAsReadyRebuildable(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:       "local",
		RootPath: root,
		JobsPath: filepath.Join(root, ".thoughtflow", "jobs"),
	}
	dbPath := filepath.Join(root, ".thoughtflow", "thoughtflow.duckdb")
	store, err := searchdb.Open(context.Background(), dbPath)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()

	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, dbPath)
	status := service.RuntimeStatus(context.Background())

	if status.Status != "ready" || status.Path != dbPath {
		t.Fatalf("status = %#v", status)
	}
}

func TestRuntimeStatusReportsUnreadySearchStore(t *testing.T) {
	service := NewService(&models.Workspace{ID: "local", RootPath: t.TempDir()}, nil, nil, nil, nil, nil, "")
	status := service.RuntimeStatus(context.Background())

	if status.Status != "degraded" || status.Error == "" {
		t.Fatalf("status = %#v", status)
	}
}

func TestGetSearchPreviewReturnsIndexedSnippet(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{
		ID:       "local",
		RootPath: root,
		JobsPath: filepath.Join(root, ".thoughtflow", "jobs"),
	}
	store, err := searchdb.Open(context.Background(), filepath.Join(root, ".thoughtflow", "thoughtflow.duckdb"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}
	defer store.Close()
	thought := models.Thought{
		ID:           "20260610-100000-preview",
		DisplayTitle: "Preview note",
		Path:         "thoughts/2026/06/20260610-100000-preview.md",
		UpdatedAt:    time.Date(2026, 6, 10, 10, 0, 0, 0, time.UTC),
		UserTags:     []string{"preview"},
		TopicIDs:     []string{"search"},
	}
	content := models.ThoughtContent{Original: "Search preview should return an indexed snippet and backlink path."}
	if err := store.IndexThought(context.Background(), thought, content); err != nil {
		t.Fatalf("IndexThought() error = %v", err)
	}
	service := NewService(ws, jobstore.New(ws.JobsPath), store, nil, nil, nil, "")

	preview, err := service.GetSearchPreview(context.Background(), thought.ID)
	if err != nil {
		t.Fatalf("GetSearchPreview() error = %v", err)
	}
	if preview.ThoughtID != thought.ID || preview.Snippet == "" || preview.Path != thought.Path {
		t.Fatalf("preview = %#v", preview)
	}
}

func TestNormalizeIndexPathUsesWorkspaceDefault(t *testing.T) {
	root := t.TempDir()
	got := normalizeIndexPath(&models.Workspace{RootPath: root}, "")
	want := filepath.Join(root, ".thoughtflow", "thoughtflow.duckdb")
	if got != want {
		t.Fatalf("path = %q, want %q", got, want)
	}
	if got := normalizeIndexPath(nil, "/tmp/thoughtflow.duckdb"); got != "/tmp/thoughtflow.duckdb" {
		t.Fatalf("absolute path = %q", got)
	}
}
