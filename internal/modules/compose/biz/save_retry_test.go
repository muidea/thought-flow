package biz

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"thoughtflow/assets/documentformats"
	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/composedraft"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

type captureFunc func(context.Context, models.CaptureCommand) (models.CaptureResult, error)

func (f captureFunc) Capture(ctx context.Context, cmd models.CaptureCommand) (models.CaptureResult, error) {
	return f(ctx, cmd)
}

func saveTestDraft(t *testing.T, svc *Service) models.ComposeDraft {
	t.Helper()
	draft, err := svc.draftStore.SaveDraft(context.Background(), models.ComposeDraft{
		ID: "save-test", Sources: []models.ComposeSource{{SourceType: "thought", SourceID: "seed"}}, Content: "# Saved draft\n\nBody", Status: models.ComposeStatusDraft,
	})
	if err != nil {
		t.Fatal(err)
	}
	return draft
}

func TestConcurrentSaveDraftCapturesOnce(t *testing.T) {
	svc, sink, _ := newTestService(t)
	draft := saveTestDraft(t, svc)
	start := make(chan struct{})
	var wg sync.WaitGroup
	results := make(chan error, 20)
	// Two service instances sharing the same workspace must use the same lock.
	other := NewService(svc.workspace, composedraft.New(svc.workspace.RootPath), svc.jobs, nil, nil, svc.synthesis, sink)
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			service := svc
			if i%2 != 0 {
				service = other
			}
			_, err := service.SaveDraft(context.Background(), draft.ID, models.ComposeSaveRequest{})
			results <- err
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	succeeded := 0
	for err := range results {
		if err == nil {
			succeeded++
		} else if !strings.Contains(err.Error(), "already saved") {
			t.Fatal(err)
		}
	}
	if succeeded != 1 || len(sink.calls) != 1 {
		t.Fatalf("successes=%d capture calls=%d", succeeded, len(sink.calls))
	}
	saved, err := svc.GetDraft(context.Background(), draft.ID)
	if err != nil || saved.SavedThoughtID != sink.results[0].Thought.ID {
		t.Fatalf("draft=%+v err=%v", saved, err)
	}
}

func TestSaveDraftRetriesPersistedReservationAfterMarkSavedFailure(t *testing.T) {
	svc, _, _ := newTestService(t)
	draft := saveTestDraft(t, svc)
	ctx := context.Background()
	realCapture := capturebiz.NewService(svc.workspace, svc.jobs, nil)
	draftsDir := filepath.Join(svc.workspace.RootPath, "compose", "drafts")
	backupDir := draftsDir + "-unavailable"
	var captured models.Thought
	svc.capture = captureFunc(func(ctx context.Context, cmd models.CaptureCommand) (models.CaptureResult, error) {
		persisted, err := composedraft.New(svc.workspace.RootPath).GetDraft(ctx, draft.ID)
		if err != nil || persisted.SaveCommand == nil || persisted.SaveCommand.ThoughtID != cmd.ThoughtID {
			t.Fatalf("capture ran before reservation persisted: %+v %v", persisted, err)
		}
		result, err := realCapture.Capture(ctx, cmd)
		if err != nil {
			return result, err
		}
		captured = result.Thought
		// Make the draft directory temporarily unavailable after Capture writes
		// Markdown, leaving the durable reservation intact for the restart.
		if err := os.Rename(draftsDir, backupDir); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(draftsDir, []byte("unavailable"), 0644); err != nil {
			t.Fatal(err)
		}
		return result, nil
	})
	if _, err := svc.SaveDraft(ctx, draft.ID, models.ComposeSaveRequest{Title: "First title"}); err == nil {
		t.Fatal("expected MarkSaved failure")
	}
	if err := os.Remove(draftsDir); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(backupDir, draftsDir); err != nil {
		t.Fatal(err)
	}
	// An edit made after capture must survive retry, as must the original ID.
	thought, content, err := markdown.ReadThought(svc.workspace.RootPath, captured.ID)
	if err != nil {
		t.Fatal(err)
	}
	content.AINotes = "User edited the captured Thought"
	if err := markdown.WriteThought(svc.workspace.RootPath, thought, content); err != nil {
		t.Fatal(err)
	}
	restarted := NewService(svc.workspace, composedraft.New(svc.workspace.RootPath), jobstore.New(svc.workspace.JobsPath), nil, nil, svc.synthesis, realCapture)
	result, err := restarted.SaveDraft(ctx, draft.ID, models.ComposeSaveRequest{Title: "Retry title", Content: "Retry replacement"})
	if err != nil {
		t.Fatal(err)
	}
	if result.Thought.ID != captured.ID {
		t.Fatalf("retry ID=%q original=%q", result.Thought.ID, captured.ID)
	}
	thoughts, err := filepath.Glob(filepath.Join(svc.workspace.RootPath, "thoughts", "*", "*", "*.md"))
	if err != nil || len(thoughts) != 1 {
		t.Fatalf("thoughts=%+v err=%v", thoughts, err)
	}
	_, after, err := markdown.ReadThought(svc.workspace.RootPath, captured.ID)
	if err != nil || after.AINotes != content.AINotes {
		t.Fatalf("capture overwrote edit: %+v err=%v", after, err)
	}
	saved, err := restarted.GetDraft(ctx, draft.ID)
	if err != nil || saved.SavedThoughtID != captured.ID || saved.Content != draft.Content {
		t.Fatalf("saved=%+v err=%v", saved, err)
	}
}

func TestTypedComposeSavePreservesCanonicalMarkdown(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	profile, err := documentprofile.Parse([]byte(documentformats.DesignDocV2), documentprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	body := "\n\n    indented code\r\n\r\n# Saved draft\r\n\r\nHard break  \r\n\r\n## Sources\r\n\r\n- https://example.org/source\r\n"
	draft, err := svc.draftStore.SaveDraft(ctx, models.ComposeDraft{
		ID: "typed-roundtrip", Status: models.ComposeStatusDraft, Content: "# Generated",
		Sources:         []models.ComposeSource{{SourceType: "thought", SourceID: "seed"}},
		SourceLinks:     []string{"https://example.org/source"},
		DocumentProfile: &profile.Ref, ProfileSnapshot: profile.Snapshot,
	})
	if err != nil {
		t.Fatal(err)
	}
	svc.capture = capturebiz.NewService(svc.workspace, svc.jobs, nil)
	result, err := svc.SaveDraft(ctx, draft.ID, models.ComposeSaveRequest{Content: body})
	if err != nil {
		t.Fatal(err)
	}
	_, content, err := markdown.ReadThought(svc.workspace.RootPath, result.Thought.ID)
	if err != nil {
		t.Fatal(err)
	}
	want := documentstore.Normalize(body)
	saved, err := svc.draftStore.GetDraft(ctx, draft.ID)
	if err != nil {
		t.Fatal(err)
	}
	if content.AINotes != want || saved.Content != want || saved.SaveCommand.Content != want {
		t.Fatalf("canonical body changed: thought=%q draft=%q", content.AINotes, saved.Content)
	}
}
