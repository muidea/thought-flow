package documentflow_test

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

func TestQuotedTitlesRemainStableAcrossReadsAndFinalization(t *testing.T) {
	for _, useExtracted := range []bool{false, true} {
		t.Run(map[bool]string{false: "user_title", true: "extracted_title"}[useExtracted], func(t *testing.T) {
			root, s, thought, content := setup(t, "design")
			title := `Quoted "title" at C:\notes\draft`
			thought.UserTitle = title
			thought.ExtractedTitle = `AI "title" at C:\notes`
			if useExtracted {
				thought.UserTitle = ""
				thought.ExtractedTitle = title
			}
			content.AINotes = strings.Replace(content.AINotes, "# Document", "# "+title, 1)
			content.Links = "- https://example.org/evidence"
			if e := markdown.WriteThought(root, thought, content); e != nil {
				t.Fatal(e)
			}
			before, e := s.Store.Head(thought.ID)
			if e != nil {
				t.Fatal(e)
			}
			for range 5 {
				read, body, e := markdown.ReadThought(root, thought.ID)
				if e != nil || read.UserTitle != thought.UserTitle || read.ExtractedTitle != thought.ExtractedTitle || body.AINotes != documentstore.Normalize(content.AINotes) {
					t.Fatalf("title/body changed: %+v %v", read, e)
				}
				after, e := s.Store.Head(thought.ID)
				if e != nil || !reflect.DeepEqual(before, after) {
					t.Fatalf("read published a revision: %+v -> %+v, %v", before, after, e)
				}
			}
			snap, report := check(t, s, thought.ID)
			if _, e := s.Finalize(thought.ID, snap.Head.Generation, request(snap, report, "quoted-title-final")); e != nil {
				t.Fatal(e)
			}
		})
	}
}

func TestFirstProjectionRecoversAfterHEADPublicationFailure(t *testing.T) {
	root, s, base, content := setup(t, "design")
	thought := base
	thought.ID = "20261021-first-projection"
	thought.Type = models.ThoughtTypeText
	thought.Source = models.ThoughtSourceAPI
	thought.Path = ""
	thought.Document = nil
	thought.CreatedAt = time.Date(2026, 10, 21, 1, 2, 3, 0, time.UTC)
	thought.UserTags = []string{"recovery"}
	content.Original = "Original input"
	content.ExtractedContent = "Extracted input"
	content.Links = "- https://example.org/evidence"
	s.Store.Fault = func(point string) error {
		if point == "head_published" {
			return errors.New("crash before first projection")
		}
		return nil
	}
	if e := markdown.WriteThought(root, thought, content); e == nil {
		t.Fatal("fault not reached")
	}
	path := filepath.Join(root, markdown.ThoughtRelativePath(thought.ID))
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatalf("projection exists: %v", e)
	}
	head, e := s.Store.Head(thought.ID)
	if e != nil || !head.PendingProjection {
		t.Fatal(head, e)
	}
	s.Close()
	restarted := documentflow.Configure(root, s.Profiles, s.Options)
	t.Cleanup(restarted.Close)
	read, actual, e := markdown.ReadThought(root, thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	if read.ID != thought.ID || read.Type != thought.Type || read.Source != thought.Source || !read.CreatedAt.Equal(thought.CreatedAt) || !reflect.DeepEqual(read.UserTags, thought.UserTags) {
		t.Fatalf("lost projection metadata: %+v", read)
	}
	want := models.ThoughtContent{AINotes: documentstore.Normalize(content.AINotes), Original: documentstore.Normalize(content.Original), ExtractedContent: documentstore.Normalize(content.ExtractedContent), Links: documentstore.Normalize(content.Links)}
	if actual != want {
		t.Fatalf("recovery body mismatch: %+v", actual)
	}
	for range 3 {
		if _, _, e = markdown.ReadThought(root, thought.ID); e != nil {
			t.Fatal(e)
		}
		after, e := restarted.Store.Head(thought.ID)
		if e != nil || after.Generation != head.Generation || after.WorkingRevisionID != head.WorkingRevisionID || after.PendingProjection {
			t.Fatalf("recovery created another revision: %+v %v", after, e)
		}
	}
}

func TestDeletionRevokesAccessBeforeProjectionCleanupAndSurvivesRestart(t *testing.T) {
	root, s, thought, content := setup(t, "design")
	snap, report := check(t, s, thought.ID)
	finalRequest := request(snap, report, "deleted-final")
	if _, e := s.Finalize(thought.ID, snap.Head.Generation, finalRequest); e != nil {
		t.Fatal(e)
	}
	s.Store.Fault = func(point string) error {
		if point == "deletion_published" {
			return errors.New("crash before unlink")
		}
		return nil
	}
	if e := markdown.DeleteThought(root, thought.ID); e == nil {
		t.Fatal("fault not reached")
	}
	path := filepath.Join(root, markdown.ThoughtRelativePath(thought.ID))
	if _, e := os.Stat(path); e != nil {
		t.Fatal("fixture did not leave the stale projection", e)
	}
	s.Close()
	s = documentflow.Configure(root, s.Profiles, s.Options)
	t.Cleanup(s.Close)
	if _, _, e := markdown.ReadThought(root, thought.ID); !os.IsNotExist(e) {
		t.Fatal("deleted projection restored", e)
	}
	if e := markdown.WriteThought(root, thought, content); !os.IsNotExist(e) {
		t.Fatal("stale writer resurrected deletion", e)
	}
	if _, e := s.Finalize(thought.ID, snap.Head.Generation, finalRequest); !os.IsNotExist(e) {
		t.Fatal("finalization retry bypassed revocation", e)
	}
	for _, mode := range []string{"draft", "final"} {
		if _, e := s.Export(thought.ID, report.RevisionID, mode); !os.IsNotExist(e) {
			t.Fatal("deleted export readable", mode, e)
		}
	}
	if e := markdown.DeleteThought(root, thought.ID); e != nil {
		t.Fatal("cleanup retry failed", e)
	}
	if _, e := os.Stat(path); !os.IsNotExist(e) {
		t.Fatal("projection not removed", e)
	}
	if _, e := s.Store.Revision(thought.ID, report.RevisionID); e != nil {
		t.Fatal("internal immutable history removed", e)
	}
	head, e := s.Store.Head(thought.ID)
	if e != nil || head.DeletedAt == nil || head.PendingProjection {
		t.Fatal("durable revocation missing", head, e)
	}
}

type semanticErrorReviewer struct{}

func (semanticErrorReviewer) ReviewDocument(context.Context, documentstore.RevisionData) ([]models.DocumentIssue, string, error) {
	return []models.DocumentIssue{{Code: "semantic_conflict", Severity: "error", Section: "proposal", Evidence: "G1", Basis: "Conflicting assumptions need human judgment", Suggestion: "Inspect constraints"}}, "fixture", nil
}
func TestConfirmableSemanticErrorsDoNotFailStructuralRun(t *testing.T) {
	_, s, thought, _ := setup(t, "design")
	s.Options.Reviewer = semanticErrorReviewer{}
	snap, e := s.Get(thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	report, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation)
	if e != nil || report.Status != "needs_review" {
		t.Fatalf("confirmable semantic issue became hard failure: %+v %v", report, e)
	}
	if report.ReviewRuns[0].Status != "passed" {
		t.Fatal("semantic issue changed structural status", report.ReviewRuns)
	}
	if report.ReviewRuns[1].Status != "needs_review" {
		t.Fatal("unconfirmed semantic errors reported as passed", report.ReviewRuns)
	}
	req := request(snap, report, "semantic-manual-final")
	unconfirmed := req
	unconfirmed.Confirmations = nil
	if _, e = s.Finalize(thought.ID, snap.Head.Generation, unconfirmed); !errors.Is(e, documentflow.ErrRules) {
		t.Fatal("unconfirmed issue accepted", e)
	}
	f, e := s.Finalize(thought.ID, snap.Head.Generation, req)
	if e != nil || f.ReviewMethod != "structure_and_manual" {
		t.Fatalf("explicit confirmation rejected: %+v %v", f, e)
	}
}

func TestDeletionPreventsLateValidationPublication(t *testing.T) {
	root, s, thought, _ := setup(t, "design")
	snap, e := s.Get(thought.ID)
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.Store.Records(thought.ID, "reports")
	if e != nil {
		t.Fatal(e)
	}
	review := &reviewer{entered: make(chan struct{}), gate: make(chan struct{})}
	s.Options.Reviewer = review
	done := make(chan error, 1)
	go func() {
		_, e := s.Validate(context.Background(), thought.ID, snap.Head.WorkingRevisionID, snap.Head.Generation)
		done <- e
	}()
	<-review.entered
	if e = markdown.DeleteThought(root, thought.ID); e != nil {
		close(review.gate)
		t.Fatal(e)
	}
	close(review.gate)
	if e = <-done; !os.IsNotExist(e) {
		t.Fatal("late validation published after deletion", e)
	}
	after, e := s.Store.Records(thought.ID, "reports")
	if e != nil || !reflect.DeepEqual(before, after) {
		t.Fatal("deleted document gained a report", before, after, e)
	}
}
