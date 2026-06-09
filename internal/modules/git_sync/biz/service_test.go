package biz

import (
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
