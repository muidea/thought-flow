package biz

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/models"
)

func TestEnqueueChangeSetMergesAndFiltersPaths(t *testing.T) {
	service := NewService(&models.Workspace{ID: "local"}, nil, nil, nil, true, time.Hour)
	if service.timer != nil {
		defer service.timer.Stop()
	}

	changeSet := service.EnqueueChangeSet(
		[]string{
			"thoughts/2026/06/a.md",
			"thoughts/2026/06/a.md",
			"topics/demo/index.md",
			".thoughtflow/jobs/job.yaml",
			".thoughtflow/thoughtflow.duckdb",
			"runtime.duckdb",
			"runtime.duckdb.wal",
			"../outside.md",
		},
		"capture",
		[]string{"thought-a", "thought-a"},
	)

	if changeSet.ID == "" {
		t.Fatalf("expected changeset id")
	}
	if changeSet.Reason != "capture" {
		t.Fatalf("reason = %q", changeSet.Reason)
	}
	if len(changeSet.Paths) != 2 {
		t.Fatalf("paths = %#v", changeSet.Paths)
	}
	if changeSet.Paths[0] != "thoughts/2026/06/a.md" || changeSet.Paths[1] != "topics/demo/index.md" {
		t.Fatalf("paths = %#v", changeSet.Paths)
	}
	if len(changeSet.ResourceIDs) != 1 || changeSet.ResourceIDs[0] != "thought-a" {
		t.Fatalf("resource ids = %#v", changeSet.ResourceIDs)
	}
	if changeSet.CreatedAt.IsZero() || changeSet.DebounceUntil.IsZero() || !changeSet.DebounceUntil.After(changeSet.CreatedAt) {
		t.Fatalf("change set times = %#v", changeSet)
	}
}

func TestEnqueueChangeSetReturnsCurrentPendingSnapshot(t *testing.T) {
	service := NewService(&models.Workspace{ID: "local"}, nil, nil, nil, true, time.Hour)
	if service.timer != nil {
		defer service.timer.Stop()
	}

	first := service.EnqueueChangeSet([]string{"thoughts/a.md"}, "capture", []string{"a"})
	second := service.EnqueueChangeSet([]string{"topics/demo/index.md"}, "topic_update", []string{"topic-demo"})

	if second.ID != first.ID {
		t.Fatalf("expected same pending changeset id, first=%q second=%q", first.ID, second.ID)
	}
	if len(second.Paths) != 2 {
		t.Fatalf("paths = %#v", second.Paths)
	}
	if second.Reason != "capture,topic_update" {
		t.Fatalf("reason = %q", second.Reason)
	}
	if len(second.ResourceIDs) != 2 || second.ResourceIDs[0] != "a" || second.ResourceIDs[1] != "topic-demo" {
		t.Fatalf("resource ids = %#v", second.ResourceIDs)
	}
}

func TestNormalizedCommitPathRejectsRuntimeAndUnsafePaths(t *testing.T) {
	rejected := []string{
		"",
		".",
		"..",
		"../outside.md",
		"/tmp/outside.md",
		".thoughtflow",
		".thoughtflow/jobs/job.yaml",
		"thoughtflow.duckdb",
		"thoughtflow.duckdb.wal",
	}
	for _, path := range rejected {
		if got, ok := normalizedCommitPath(path); ok {
			t.Fatalf("normalizedCommitPath(%q) = %q, true", path, got)
		}
	}

	if got, ok := normalizedCommitPath("topics/demo/../demo/index.md"); !ok || got != "topics/demo/index.md" {
		t.Fatalf("normalizedCommitPath() = %q, %v", got, ok)
	}
}

func TestRecentCommitsReturnsPathHistory(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	root := t.TempDir()
	thoughtPath := filepath.ToSlash(filepath.Join("thoughts", "2026", "06", "20260609-143010-detail.md"))
	fullPath := filepath.Join(root, filepath.FromSlash(thoughtPath))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(fullPath, []byte("# Detail thought\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "config", "user.name", "ThoughtFlow Test")
	runGitForTest(t, root, "config", "user.email", "thoughtflow-test@example.test")
	runGitForTest(t, root, "add", "--", thoughtPath)
	runGitForTest(t, root, "commit", "-m", "thoughtflow: add detail thought")

	service := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
	records := service.RecentCommits(context.Background(), thoughtPath, "20260609-143010-detail", 5)

	if len(records) != 1 {
		t.Fatalf("records = %#v", records)
	}
	if records[0].CommitHash == "" ||
		records[0].Message != "thoughtflow: add detail thought" ||
		len(records[0].Paths) != 1 ||
		records[0].Paths[0] != thoughtPath ||
		len(records[0].ResourceIDs) != 1 ||
		records[0].ResourceIDs[0] != "20260609-143010-detail" ||
		records[0].CommittedAt.IsZero() {
		t.Fatalf("record = %#v", records[0])
	}
}

func TestRuntimeStatusReportsRepositoryIdentityAndDirtyState(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skipf("git is unavailable: %v", err)
	}
	root := t.TempDir()
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "config", "user.name", "ThoughtFlow Test")
	runGitForTest(t, root, "config", "user.email", "thoughtflow-test@example.test")
	if err := os.WriteFile(filepath.Join(root, "README.md"), []byte("dirty\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	service := NewService(&models.Workspace{ID: "local", RootPath: root}, nil, nil, nil, true, time.Hour)
	status := service.RuntimeStatus(context.Background())

	if status.Status != "ready" ||
		!status.Enabled ||
		!status.Repository ||
		!status.IdentityConfigured ||
		!status.Dirty {
		t.Fatalf("status = %#v", status)
	}
}

func TestRuntimeStatusReportsDisabledGit(t *testing.T) {
	service := NewService(&models.Workspace{ID: "local", RootPath: t.TempDir()}, nil, nil, nil, false, time.Hour)
	status := service.RuntimeStatus(context.Background())

	if status.Status != "disabled" || status.Enabled {
		t.Fatalf("status = %#v", status)
	}
}

func runGitForTest(t *testing.T, root string, args ...string) {
	t.Helper()
	cmdArgs := append([]string{"-C", root}, args...)
	cmd := exec.Command("git", cmdArgs...)
	raw, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %s: %v", strings.Join(cmdArgs, " "), strings.TrimSpace(string(raw)), err)
	}
}
