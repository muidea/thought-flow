package documentsetflow

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func interruptedArchiveNotes(t *testing.T, stage string) (*Service, ds.Run) {
	t.Helper()
	s, p := proposedArchivePlan(t, &engineeringGenerator{})
	s.Store.Fault = func(point string) error {
		if point == stage {
			return errors.New("interrupted local note projection")
		}
		return nil
	}
	_, err := s.Start(RunRequest{ArchiveDraft: true, OperationID: "notes-interrupted", SetID: p.SetID, PlanID: p.ID, PlanHash: p.Hash, ExpectedGeneration: &p.BaseGeneration, CaptureGeneration: &p.Capture.Generation})
	if err != nil {
		t.Fatal(err)
	}
	run := wait(t, s, "notes-interrupted")
	if run.State != "failed_terminal" || run.Draft == nil || !run.Draft.NotesPending || run.Draft.NotesReady {
		t.Fatal(run.State, run.Error)
	}
	s.Close()
	s.Store.Fault = nil
	return s, run
}

func TestArchiveThoughtRecoveryPreservesEditsAndCommitsReadableClosure(t *testing.T) {
	for _, stage := range []string{"archive_thoughts_reserved", "archive_thought_written", "archive_thoughts_projected"} {
		t.Run(stage, func(t *testing.T) {
			old, run := interruptedArchiveNotes(t, stage)
			first := run.Draft.Members[0]
			edited := stage != "archive_thoughts_reserved"
			if edited {
				thought, content, err := markdown.ReadThought(old.Store.Root, first.ThoughtID)
				if err != nil {
					t.Fatal(err)
				}
				content.AINotes = "MANUAL INPUT\n\n## Original\n\nKeep this embedded heading.\n"
				thought.UserTitle = "Manual title"
				if err := markdown.WriteThought(old.Store.Root, thought, content); err != nil {
					t.Fatal(err)
				}
			}
			var commits [][]string
			var events int
			restarted := New(old.Store.Root, Options{Enabled: true, Emit: func(kind string, payload map[string]any) error {
				if kind == models.EventThoughtPatched {
					events++
					if payload["capture_draft"] != true || payload["thought_id"] == "" {
						t.Error("note notification permits refinement or lacks identity")
					}
				}
				return nil
			}, Commit: func(paths []string) error { commits = append(commits, paths); return nil }})
			defer restarted.Close()
			if issues := restarted.Recover(); len(issues) > 0 {
				t.Fatal(issues)
			}
			saved, err := restarted.Store.Run(run.ID)
			if err != nil || saved.State != "draft_saved" || !saved.Draft.NotesReady || saved.Draft.NotesPending || events != 7 || len(commits) != 1 {
				t.Fatal(saved.State, saved.Error, events, err)
			}
			ids, err := markdown.ListThoughtIDs(restarted.Store.Root)
			if err != nil || len(ids) != 7 {
				t.Fatal(ids, err)
			}
			// Copy the exact admitted Git closure into an independent workspace.
			clone := t.TempDir()
			for _, path := range commits[0] {
				if strings.Contains(path, "private/") {
					t.Fatal("private run or diagnostics admitted to Git", path)
				}
				raw, err := os.ReadFile(filepath.Join(restarted.Store.Root, filepath.FromSlash(path)))
				if err != nil {
					t.Fatal("Git closure contains missing files", path, err)
				}
				target := filepath.Join(clone, filepath.FromSlash(path))
				if err := os.MkdirAll(filepath.Dir(target), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(target, raw, 0600); err != nil {
					t.Fatal(err)
				}
			}
			for _, member := range saved.Draft.Members {
				if member.ThoughtID != archiveThoughtID(run, member.Key) {
					t.Fatal("recovery changed reserved identity")
				}
				thought, content, err := markdown.ReadThought(clone, member.ThoughtID)
				if err != nil || thought.DesignDraft == nil || !thought.CaptureDraft {
					t.Fatal("cloned note not readable", err)
				}
				if edited && member.Key == first.Key {
					if thought.UserTitle != "Manual title" || !strings.Contains(content.AINotes, "MANUAL INPUT") || !strings.Contains(content.AINotes, "Keep this embedded heading.") {
						t.Fatal("recovery overwrote manual input")
					}
				} else if content.AINotes != member.Body {
					t.Fatal("note bytes differ from immutable draft", member.Key)
				}
			}
			if issues := restarted.Recover(); len(issues) > 0 {
				t.Fatal(issues)
			}
			if events != 7 || len(commits) != 1 {
				t.Fatal("completed archive was replayed")
			}
			// A subsequently removed projection is not silently resurrected.
			location, err := objectlocation.For(restarted.Store.Root).Lookup(first.ThoughtID)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(restarted.Store.Root, location.Placement.ThoughtPath)); err != nil {
				t.Fatal(err)
			}
			if issues := restarted.Recover(); len(issues) > 0 {
				t.Fatal(issues)
			}
			if _, err := os.Stat(filepath.Join(restarted.Store.Root, location.Placement.ThoughtPath)); !os.IsNotExist(err) {
				t.Fatal("completed archive recreated removed note", err)
			}
		})
	}
}

func TestLegacyArchiveDraftBackfillsNotesWithoutLiveConversationOrModel(t *testing.T) {
	s, run := interruptedArchiveNotes(t, "archive_thoughts_reserved")
	run.State = "draft_saved"
	run.Error = ""
	run.Draft.OriginRunID = ""
	run.Draft.NotesPending = false
	for i := range run.Draft.Members {
		run.Draft.Members[i].ThoughtID = ""
	}
	if err := s.Store.SaveRun(run); err != nil {
		t.Fatal(err)
	}
	restarted := New(s.Store.Root, Options{Enabled: true})
	defer restarted.Close()
	if issues := restarted.Recover(); len(issues) > 0 {
		t.Fatal(issues)
	}
	saved, err := restarted.Store.Run(run.ID)
	if err != nil || saved.State != "draft_saved" || !saved.Draft.NotesReady || saved.Draft.OriginRunID != run.ID {
		t.Fatal(saved.State, saved.Error, err)
	}
	if ids, err := markdown.ListThoughtIDs(s.Store.Root); err != nil || len(ids) != 7 {
		t.Fatal(ids, err)
	}
}

func TestCompletedArchiveThoughtsRejectNewRetry(t *testing.T) {
	old, run := interruptedArchiveNotes(t, "archive_thoughts_projected")
	s := New(old.Store.Root, Options{Enabled: true, ModelHash: run.ModelHash})
	defer s.Close()
	if issues := s.Recover(); len(issues) > 0 {
		t.Fatal(issues)
	}
	bound := false
	_, err := s.RetryWithBinding(run.ID, RetryRequest{OperationID: "notes-completed-retry", ExpectedGeneration: &run.Plan.BaseGeneration}, func(ds.Run) error {
		bound = true
		return nil
	})
	if !errors.Is(err, ds.ErrConflict) || bound {
		t.Fatal("completed archive launched or rebound a new attempt", err, bound)
	}
	if _, err := s.Store.Run("notes-completed-retry"); !os.IsNotExist(err) {
		t.Fatal("completed archive created a retry run", err)
	}
	saved, err := s.Store.Run(run.ID)
	if err != nil || saved.State != "draft_saved" || !saved.Draft.NotesReady {
		t.Fatal("completed archive state changed", saved.State, err)
	}
}

func TestArchiveThoughtRecoveryRejectsCollisionsAndCorruptEvidence(t *testing.T) {
	for _, which := range []string{"foreign-note", "changed-private-bytes", "changed-private-capture"} {
		t.Run(which, func(t *testing.T) {
			s, run := interruptedArchiveNotes(t, "archive_thoughts_reserved")
			first := run.Draft.Members[0]
			var collisionPath string
			if which == "foreign-note" {
				if err := markdown.WriteThought(s.Store.Root, models.Thought{ID: first.ThoughtID, UserTitle: "Foreign note"}, models.ThoughtContent{AINotes: "FOREIGN INPUT"}); err != nil {
					t.Fatal(err)
				}
				location, _ := objectlocation.For(s.Store.Root).Lookup(first.ThoughtID)
				collisionPath = filepath.Join(s.Store.Root, location.Placement.ThoughtPath)
			} else if which == "changed-private-bytes" {
				path := filepath.Join(s.Store.Root, documentsetstore.Private(run.Plan.SetID), "drafts", run.ID, first.Filename)
				if err := os.WriteFile(path, []byte("EDITED PRIVATE ARCHIVE"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				path := documentsetstore.Private(run.Plan.SetID) + "/captures/" + run.Plan.ID + ".json"
				var capture ds.CaptureSnapshot
				if err := objectlocation.ReadJSON(s.Store.Root, path, &capture); err != nil {
					t.Fatal(err)
				}
				capture.Messages[0].Text = "TAMPERED EVIDENCE"
				if err := objectlocation.WriteJSON(s.Store.Root, path, capture, true); err != nil {
					t.Fatal(err)
				}
			}
			restarted := New(s.Store.Root, Options{Enabled: true})
			defer restarted.Close()
			if _, err := restarted.RecoverRun(run.ID); err == nil {
				t.Fatal("conflicting archive projected")
			}
			saved, _ := restarted.Store.Run(run.ID)
			if saved.Draft.NotesReady || saved.Error == "" {
				t.Fatal("recovery disguised as success")
			}
			ids, err := markdown.ListThoughtIDs(s.Store.Root)
			if err != nil {
				t.Fatal(err)
			}
			if which == "foreign-note" {
				raw, err := os.ReadFile(collisionPath)
				if err != nil || !strings.Contains(string(raw), "FOREIGN INPUT") || len(ids) != 1 {
					t.Fatal("foreign note overwritten", err)
				}
			} else if len(ids) != 0 {
				t.Fatal("corrupt evidence produced notes", ids)
			}
		})
	}
}
