package biz

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/models"
	"time"
)

func TestFailedChangesetSurvivesRestartAndRetriesWithoutEditing(t *testing.T) {
	root := t.TempDir()
	runtime := filepath.Join(root, "runtime")
	ws := &models.Workspace{ID: "local", RootPath: root, RuntimePath: runtime, JobsPath: filepath.Join(runtime, "jobs")}
	runGitForTest(t, root, "init")
	runGitForTest(t, root, "config", "user.name", "Audit")
	runGitForTest(t, root, "config", "user.email", "audit@example.test")
	if err := os.WriteFile(filepath.Join(root, "note.md"), []byte("immutable retry content"), 0600); err != nil {
		t.Fatal(err)
	}
	runGitForTest(t, root, "config", "core.hooksPath", filepath.Join(root, ".git/hooks"))
	hook := filepath.Join(root, ".git/hooks/pre-commit")
	if err := os.WriteFile(hook, []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	first := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, true, time.Hour)
	first.EnqueueChangeSet([]string{"note.md"}, "capture", []string{"note"})
	first.flush()
	raw, err := os.ReadFile(first.pendingPath())
	if err != nil || !strings.Contains(string(raw), "note.md") {
		t.Fatal("failed change was lost", err)
	}
	first.Close()
	if err = os.Remove(hook); err != nil {
		t.Fatal(err)
	}
	next := NewService(ws, jobstore.New(ws.JobsPath), nil, nil, true, 10*time.Millisecond)
	defer next.Close()
	if err = next.Recover(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for {
		if _, err = outputGit(context.Background(), "-C", root, "cat-file", "-e", "HEAD:note.md"); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("recovered pending change did not retry", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	next.Flush(context.Background(), time.Second)
	if _, err = os.Stat(next.pendingPath()); !os.IsNotExist(err) {
		t.Fatal("successful change was not acknowledged", err)
	}
}

func TestConcurrentEnqueueDuringCommitRemainsPending(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{ID: "local", RootPath: root, RuntimePath: filepath.Join(root, "runtime")}
	service := NewService(ws, nil, nil, nil, true, time.Hour)
	defer service.Close()
	service.EnqueueChangeSet([]string{"same.md"}, "first", nil)
	paths, _ := service.takePending()
	if len(paths) != 1 {
		t.Fatal(paths)
	}
	service.EnqueueChangeSet([]string{"same.md", "new.md"}, "second", nil)
	service.finishPending(true)
	service.mu.Lock()
	snapshot := service.pendingChangeSetLocked()
	service.mu.Unlock()
	if len(snapshot.Paths) != 2 {
		t.Fatalf("commit ack lost later event: %+v", snapshot)
	}
	raw, err := os.ReadFile(service.pendingPath())
	if err != nil || !strings.Contains(string(raw), "same.md") {
		t.Fatal("later event not durable", err)
	}
}

func TestClosedServicePersistsLateEventsWithoutRestartingTimer(t *testing.T) {
	root := t.TempDir()
	ws := &models.Workspace{RootPath: root, RuntimePath: filepath.Join(root, "runtime")}
	service := NewService(ws, nil, nil, nil, true, 0)
	service.Close()
	service.EnqueueChangeSet([]string{"late.md"}, "worker drained after Git teardown", []string{"late"})
	service.mu.Lock()
	timer := service.timer
	service.mu.Unlock()
	if timer != nil {
		t.Fatal("late shutdown event started a new timer")
	}
	raw, err := os.ReadFile(service.pendingPath())
	if err != nil || !strings.Contains(string(raw), "late.md") {
		t.Fatalf("late shutdown event lost: %s %v", raw, err)
	}
	service.Close()
}
