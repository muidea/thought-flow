package documentsetflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func archiveThoughtID(run ds.Run, key string) string {
	hash := documentstore.HashJSON([]string{run.Plan.SetID, run.Draft.OriginRunID, key})
	return run.Plan.CreatedAt.UTC().Format("20060102") + "-design-" + strings.TrimPrefix(hash, "sha256:")[:32]
}

// Verify the complete immutable archive before creating any readable notes.
// Recovery uses frozen evidence, not today's conversation or a new LLM call.
func (s *Service) verifyArchiveThoughtSource(run ds.Run) error {
	if !run.ArchiveDraft || run.Draft == nil || validateRunMembers(run) != nil {
		return ds.ErrInvalid
	}
	var capture ds.CaptureSnapshot
	if err := objectlocation.ReadJSON(s.Store.Root, documentsetstore.Private(run.Plan.SetID)+"/captures/"+run.Plan.ID+".json", &capture); err != nil {
		return err
	}
	if capture.ContextHash != run.Plan.Capture.ContextHash || capture.Generation != run.Plan.Capture.Generation || capture.SessionID != run.Plan.Capture.SessionID || documentstore.HashJSON(capture) != run.Draft.SourceHash {
		return ds.ErrConflict
	}
	members, err := archiveDraftMembers(run.Plan)
	if err != nil {
		return err
	}
	if len(run.Draft.Members) != len(members)+1 {
		return ds.ErrInvalid
	}
	expected := map[string]ds.PlanMember{}
	for _, member := range members {
		expected[member.Key] = member
	}
	original := archiveOriginalMember(capture)
	seen := map[string]bool{}
	for _, member := range run.Draft.Members {
		if filepath.Base(member.Filename) != member.Filename || strings.ContainsAny(member.Filename, "/\\") || member.Filename == "." {
			return ds.ErrInvalid
		}
		if seen[member.Key] || member.Hash != documentstore.Hash([]byte(member.Body)) {
			return ds.ErrInvalid
		}
		seen[member.Key] = true
		if member.Key == original.Key {
			if member.Filename != original.Filename || member.Title != original.Title || member.Body != original.Body {
				return ds.ErrConflict
			}
		} else if spec, ok := expected[member.Key]; !ok || spec.Filename != member.Filename || spec.Title != member.Title {
			return ds.ErrInvalid
		}
		path, err := objectlocation.SafePath(s.Store.Root, documentsetstore.Private(run.Plan.SetID)+"/drafts/"+run.ID+"/"+member.Filename)
		if err != nil {
			return err
		}
		raw, err := objectlocation.ReadFileBounded(path, 32<<20)
		if err != nil {
			return err
		}
		if string(raw) != member.Body {
			return ds.ErrConflict
		}
	}
	return nil
}

// VerifySavedArchive verifies frozen evidence before exposing saved archive content.
func VerifySavedArchive(root string, run ds.Run) error {
	return (&Service{Store: documentsetstore.New(root)}).verifyArchiveThoughtSource(run)
}

// This is ordinary Thought materialization, not set publication. IDs are
// reserved durably before I/O; exclusive creation never replaces user bytes.
func (s *Service) projectArchiveThoughts(ctx context.Context, run *ds.Run) error {
	if run.Draft != nil && run.Draft.NotesReady {
		return nil
	}
	if err := s.verifyArchiveThoughtSource(*run); err != nil {
		return err
	}
	if run.Draft.OriginRunID == "" {
		run.Draft.OriginRunID = run.ID
	}
	if !documentstore.ValidID(run.Draft.OriginRunID) {
		return ds.ErrInvalid
	}
	if run.Draft.OriginRunID != run.ID {
		origin, err := s.Store.Run(run.Draft.OriginRunID)
		if err != nil {
			return err
		}
		if !origin.ArchiveDraft || origin.Draft == nil || origin.Plan.Hash != run.Plan.Hash || origin.Draft.SourceHash != run.Draft.SourceHash {
			return ds.ErrConflict
		}
	}
	release := objectlocation.For(s.Store.Root).LockObject("archive-thoughts-" + run.Draft.OriginRunID)
	defer release()
	for i := range run.Draft.Members {
		member := &run.Draft.Members[i]
		id := archiveThoughtID(*run, member.Key)
		if member.ThoughtID != "" && member.ThoughtID != id {
			return ds.ErrConflict
		}
		member.ThoughtID = id
	}
	run.Draft.NotesPending = true
	run.State = "committing"
	if err := s.save(run); err != nil {
		return err
	}
	if run.CancelRequested {
		return context.Canceled
	}
	if err := s.archiveNoteCheckpoint("archive_thoughts_reserved"); err != nil {
		return err
	}
	paths := map[string]bool{}
	for _, member := range run.Draft.Members {
		if err := ctx.Err(); err != nil {
			return err
		}
		link := &models.DesignDraftLink{SetID: run.Plan.SetID, RunID: run.Draft.OriginRunID, MemberKey: member.Key, SourceHash: run.Draft.SourceHash, BodyHash: member.Hash}
		thought := models.Thought{ID: member.ThoughtID, Type: models.ThoughtTypeText, Source: models.ThoughtSourceScratchpadCommit,
			UserTitle: member.Title, CreatedAt: run.Plan.CreatedAt, UpdatedAt: run.Plan.CreatedAt, ContentHash: models.ContentHash(member.Body),
			CaptureDraft: true, DesignDraft: link, CaptureStatus: models.CaptureStatusCaptured, RefineStatus: models.RefineStatusDisabled, IndexStatus: models.IndexStatusPending, TopicStatus: models.TopicStatusUnmatched}
		if err := s.createArchiveThought(thought, member.Body); err != nil {
			return fmt.Errorf("archive Thought %s: %w", member.Key, err)
		}
		if err := s.archiveNoteCheckpoint("archive_thought_written"); err != nil {
			return err
		}
		for _, path := range markdown.GitPaths(s.Store.Root, member.ThoughtID) {
			paths[path] = true
		}
	}
	// Notification and Git admission can be replayed without regenerating content.
	for _, member := range run.Draft.Members {
		if s.opts.Emit != nil {
			if err := s.opts.Emit(models.EventThoughtPatched, map[string]any{"set_id": run.Plan.SetID, "run_id": run.ID, "thought_id": member.ThoughtID, "capture_draft": true, "event_id": run.Draft.OriginRunID + ":draft:" + member.Key}); err != nil {
				return err
			}
		}
	}
	if s.opts.Commit != nil {
		list := make([]string, 0, len(paths))
		for path := range paths {
			list = append(list, path)
		}
		// Git paths never include the enclosing private archive or diagnostics.
		sort.Strings(list)
		if err := s.opts.Commit(list); err != nil {
			return err
		}
	}
	if err := s.archiveNoteCheckpoint("archive_thoughts_projected"); err != nil {
		return err
	}
	run.Draft.NotesReady, run.Draft.NotesPending = true, false
	run.State, run.Error = "draft_saved", ""
	if err := s.save(run); err != nil {
		return err
	}
	s.emit("document_set.progress", *run, "")
	return nil
}

func (s *Service) createArchiveThought(thought models.Thought, body string) error {
	verify := func(existing models.Thought) error {
		if existing.DesignDraft == nil || documentstore.HashJSON(existing.DesignDraft) != documentstore.HashJSON(thought.DesignDraft) {
			return ds.ErrConflict
		}
		// The user may have edited the readable copy; preserve it verbatim.
		return nil
	}
	if existing, _, err := markdown.ReadThought(s.Store.Root, thought.ID); err == nil {
		return verify(existing)
	} else if !os.IsNotExist(err) {
		return err
	}
	err := markdown.CreateThought(s.Store.Root, thought, models.ThoughtContent{AINotes: body})
	if errors.Is(err, os.ErrExist) {
		existing, _, readErr := markdown.ReadThought(s.Store.Root, thought.ID)
		if readErr != nil {
			return readErr
		}
		return verify(existing)
	}
	return err
}

func (s *Service) archiveNoteCheckpoint(stage string) error {
	if s.Store.Fault != nil {
		return s.Store.Fault(stage)
	}
	return nil
}
