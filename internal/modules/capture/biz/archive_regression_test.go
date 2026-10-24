package biz

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

type auditConcurrentCapture struct {
	*stubCapture
	entered chan struct{}
	release chan struct{}
	calls   atomic.Int32
}

func (c *auditConcurrentCapture) Capture(_ context.Context, _ models.CaptureCommand) (models.CaptureResult, error) {
	n := c.calls.Add(1)
	c.entered <- struct{}{}
	<-c.release
	return models.CaptureResult{Thought: models.Thought{ID: fmt.Sprintf("audit-thought-%d", n)}}, nil
}

func TestArchiveRegressionConcurrentFirstArchive(t *testing.T) {
	store := scratchpad.New(filepath.Join(t.TempDir(), "scratchpad"))
	profiles, err := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	capture := &auditConcurrentCapture{stubCapture: &stubCapture{}, entered: make(chan struct{}, 2), release: make(chan struct{})}
	svc := NewScratchpadService(store, WithCapture(capture), WithDocumentProfiles(profiles, nil, 10, 0))
	sp := scratchpad.Scratchpad{SessionID: "audit", Content: "Archive my material", ArchiveStrategy: scratchpad.ArchiveStrategyNew, SessionContext: scratchpad.SessionContext{CandidateTitle: "Audit", CandidateBody: "Archive my material", CandidateProfileID: "builtin.note", CandidateProfileVersion: 1}}
	p, err := svc.PrepareArchive(context.Background(), sp, nil)
	if err != nil {
		t.Fatal(err)
	}
	if p.Validation.Status != models.ArchiveValidationValid {
		t.Fatalf("invalid test preview: %+v", p.Validation)
	}
	sp.ArchivePreview = &p
	if _, err := store.Save(sp); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() { _, err := svc.Commit(context.Background(), "audit"); done <- err }()
	}
	for i := 0; i < 1; i++ {
		select {
		case <-capture.entered:
		case <-time.After(10 * time.Second):
			close(capture.release)
			t.Fatal("first save did not enter Capture")
		}
	}
	close(capture.release)
	for i := 0; i < 2; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	if capture.calls.Load() != 1 {
		t.Fatal("duplicate capture invocations")
	}
	t.Log("concurrent requests reused the same save operation")
}

func TestArchiveRegressionConflictsWithoutResolution(t *testing.T) {
	current := scratchpad.Scratchpad{SessionContext: scratchpad.SessionContext{Conflicts: []string{"old unresolved item"}}}
	result := captureContextToScratchpad(ai.CaptureContextResult{}, current)
	if len(result.Conflicts) != 1 {
		t.Fatal("unresolved contradiction was discarded")
	}
	t.Log("a missing model report does not silently resolve a contradiction")
}

type auditFailCommittedStore struct {
	*scratchpad.Store
	failed bool
}

func (s *auditFailCommittedStore) MarkCommitted(session, thought string) (scratchpad.Scratchpad, error) {
	if !s.failed {
		s.failed = true
		return scratchpad.Scratchpad{}, errors.New("injected commit marker failure")
	}
	return s.Store.MarkCommitted(session, thought)
}

func TestArchiveRegressionDuplicateMarkdownOnCommitMarkerRetry(t *testing.T) {
	root := t.TempDir()
	store := &auditFailCommittedStore{Store: scratchpad.New(filepath.Join(root, "scratchpad"))}
	profiles, err := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	realCapture := NewService(&models.Workspace{ID: "audit", RootPath: root}, nil, nil)
	svc := NewScratchpadService(store, WithCapture(realCapture), WithDocumentProfiles(profiles, nil, 10, 0))
	sp := scratchpad.Scratchpad{SessionID: "audit", Content: "Retry-safe archive content", ArchiveStrategy: scratchpad.ArchiveStrategyNew, SessionContext: scratchpad.SessionContext{CandidateTitle: "Audit", CandidateBody: "Retry-safe archive content", CandidateProfileID: "builtin.note", CandidateProfileVersion: 1}}
	p, err := svc.PrepareArchive(context.Background(), sp, nil)
	if err != nil || p.Validation.Status != models.ArchiveValidationValid {
		t.Fatalf("prepare preview: %+v %v", p.Validation, err)
	}
	sp.ArchivePreview = &p
	if _, err := store.Save(sp); err != nil {
		t.Fatal(err)
	}
	first, err := svc.Commit(context.Background(), "audit")
	if err == nil || first.Thought.ID == "" {
		t.Fatalf("expected post-write marker failure, result=%+v err=%v", first, err)
	}
	svc = NewScratchpadService(scratchpad.New(filepath.Join(root, "scratchpad")), WithCapture(realCapture), WithDocumentProfiles(profiles, nil, 10, 0))
	second, err := svc.Commit(context.Background(), "audit")
	if err != nil {
		t.Fatal(err)
	}
	ids, err := markdown.ListThoughtIDs(root)
	if err != nil || len(ids) != 1 || first.Thought.ID != second.Thought.ID {
		t.Fatalf("duplicate Markdown files, IDs=%v err=%v", ids, err)
	}
	t.Logf("real Capture retry reused one Thought Markdown file: %v", ids)
}
