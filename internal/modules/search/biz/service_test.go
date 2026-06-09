package biz

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

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
