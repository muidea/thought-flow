package biz

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/scratchpad"
)

func previewParameters(sp scratchpad.Scratchpad) map[string]string {
	if sp.ArchivePreview != nil {
		return cloneStringMap(sp.ArchivePreview.Parameters)
	}
	return cloneStringMap(sp.SessionContext.DocumentParameters)
}
func previewSources(sp scratchpad.Scratchpad) *models.SourceManifest {
	if sp.ArchivePreview != nil {
		return sp.ArchivePreview.Sources
	}
	return nil
}

func captureSaveHash(cmd models.CaptureCommand) string {
	return objectlocation.HashJSON(struct {
		Command models.CaptureCommand
		Profile string
		Draft   bool
	}{cmd, cmd.DocumentProfileSnapshot, cmd.CaptureDraft})
}
func (s *Service) PrepareCaptureSave(operation string, cmd models.CaptureCommand) error {
	_, err := objectlocation.For(s.workspace.RootPath).Begin(operation, "capture_save", cmd.ThoughtID, "", captureSaveHash(cmd))
	return err
}
func (s *Service) validCaptureSave(cmd models.CaptureCommand) bool {
	op, err := objectlocation.For(s.workspace.RootPath).Operation(cmd.SaveOperationID)
	return err == nil && op.Kind == "capture_save" && op.ObjectID == cmd.ThoughtID && op.RequestHash == objectlocation.HashJSON(captureSaveHash(cmd))
}

func (s *ScratchpadService) BeginPreview(id string, auto bool) (scratchpad.Scratchpad, error) {
	return s.store.Update(id, func(sp *scratchpad.Scratchpad) error {
		if sp.Operation != nil && sp.Operation.InputGeneration == sp.InputGeneration && (sp.Operation.State == "preparing" || sp.Operation.State == "saving" || sp.Operation.State == "ready") {
			return nil
		}
		if sp.ObjectID == "" {
			sp.ObjectID = models.NewThoughtID(s.now(), "capture:"+id)
		}
		sp.Operation = &scratchpad.ArchiveOperation{ID: models.NewEventID(s.now()), State: "preparing", InputGeneration: sp.InputGeneration, AutoCommit: auto}
		return nil
	})
}

func (s *ScratchpadService) PublishPreview(initial scratchpad.Scratchpad, preview scratchpad.ArchivePreview) (scratchpad.Scratchpad, error) {
	return s.store.Update(initial.SessionID, func(sp *scratchpad.Scratchpad) error {
		if sp.InputGeneration != initial.InputGeneration || sp.Operation == nil || initial.Operation == nil || sp.Operation.ID != initial.Operation.ID {
			return ErrArchivePreviewStale
		}
		profile, err := s.resolveArchiveProfile(*sp, nil)
		if err != nil {
			return err
		}
		if profile.Ref.ContentHash != preview.DocumentProfile.ContentHash {
			return ErrArchivePreviewStale
		}
		preview.OperationID = sp.Operation.ID
		preview.InputGeneration = sp.InputGeneration
		sp.Operation.InputGeneration = sp.InputGeneration
		if profile.SchemaVersion == 2 && s.archiveDocuments() != nil && archiveTarget(*sp) == "" {
			candidate, err := s.archiveDocuments().Candidate(context.Background(), sp.ObjectID, preview.Body, preview.Title, profile, preview.Parameters, *preview.Sources, preview.ContextHash)
			if err != nil {
				return err
			}
			preview.Document = &candidate
		}
		sp.ArchivePreview = &preview
		sp.Operation.State = "ready"
		if preview.Validation.Status != models.ArchiveValidationValid {
			sp.Operation.State = "blocked"
			if len(sp.SessionContext.MissingProfileInputs) > 0 || archiveNeedsInput(preview.Validation) {
				sp.Operation.State = "needs_input"
			}
		}
		return nil
	})
}

func (s *ScratchpadService) FailOperation(id, operation string, cause error) {
	_, _ = s.store.Update(id, func(sp *scratchpad.Scratchpad) error {
		if sp.Operation == nil || sp.Operation.ID != operation {
			return ErrArchivePreviewStale
		}
		sp.Operation.State = "failed"
		if errors.Is(cause, context.Canceled) {
			sp.Operation.State = "interrupted"
		}
		if errors.Is(cause, scratchpad.ErrConflict) || errors.Is(cause, documentstore.ErrConflict) || errors.Is(cause, ErrArchivePreviewStale) {
			sp.Operation.State = "conflicted"
		}
		sp.Operation.Error = cause.Error()
		return nil
	})
	s.publishContextUpdatedEvent(id)
}

func (s *ScratchpadService) Commit(ctx context.Context, id string) (models.CaptureResult, error) {
	return s.saveArchive(ctx, id, "archive")
}
func (s *ScratchpadService) SaveDraft(ctx context.Context, id string) (models.CaptureResult, error) {
	return s.saveArchive(ctx, id, "draft")
}

func (s *ScratchpadService) saveArchive(ctx context.Context, id, mode string) (models.CaptureResult, error) {
	if s == nil || s.store == nil {
		return models.CaptureResult{}, ErrScratchpadUnavailable
	}
	if s.capture == nil {
		return models.CaptureResult{}, errors.New("capture: scratchpad commit pipeline is not wired up")
	}
	unlock := scratchpad.LockSave(s.store, id)
	defer unlock()
	sp, err := s.store.Get(id)
	if err != nil {
		return models.CaptureResult{}, err
	}
	if save := sp.SaveCommand; save != nil {
		if save.State != "completed" {
			if save.Mode != mode {
				return models.CaptureResult{}, scratchpad.ErrConflict
			}
			result, e := s.runSave(ctx, sp, *save)
			if e != nil {
				s.FailOperation(id, save.OperationID, e)
			}
			return result, e
		}
		if save.Mode == mode && (strings.TrimSpace(sp.Content) == "" || save.InputGeneration == sp.InputGeneration) {
			return models.CaptureResult{Thought: models.Thought{ID: save.ThoughtID}}, nil
		}
	}
	if mode == "archive" {
		if err = s.validateArchivePreview(ctx, sp); err != nil {
			return models.CaptureResult{}, err
		}
	}
	if s.profiles == nil && sp.ArchivePreview == nil && strings.TrimSpace(sp.Content) == "" && strings.TrimSpace(sp.SessionContext.CandidateBody) == "" && strings.TrimSpace(sp.SessionContext.CandidateSummary) == "" && sp.Draft.TitleSet == "" && len(sp.Draft.TagsAdded) == 0 {
		if target := firstNonEmptyString(sp.CommittedThoughtID, sp.SourceThoughtID); target != "" {
			return models.CaptureResult{Thought: models.Thought{ID: target}}, nil
		}
	}
	if sp.ArchivePreview == nil {
		if s.profiles != nil {
			return models.CaptureResult{}, ErrArchivePreviewRequired
		}
		var current *models.ThoughtSnapshot
		if sp.ArchiveStrategy == scratchpad.ArchiveStrategyUpdate || sp.ArchiveStrategy == scratchpad.ArchiveStrategySupplement {
			if sp.SourceThoughtID == "" {
				return models.CaptureResult{}, errors.New("source_thought_id is required")
			}
			value, e := s.capture.GetThought(ctx, sp.SourceThoughtID)
			if e != nil {
				return models.CaptureResult{}, fmt.Errorf("source thought not found: %w", e)
			}
			current = &value
		}
		preview, e := s.BuildArchivePreview(sp, current)
		if e != nil {
			return models.CaptureResult{}, e
		}
		sp.ArchivePreview = &preview
	}
	preview := *sp.ArchivePreview
	if tags := mergedTagSet(sp); tags != nil {
		preview.Tags = tags
		sp.ArchivePreview = &preview
	}
	if preview.InputGeneration != 0 && preview.InputGeneration != sp.InputGeneration {
		return models.CaptureResult{}, ErrArchivePreviewStale
	}
	if mode == "draft" {
		profile, e := s.resolveArchiveProfile(sp, nil)
		if e != nil {
			return models.CaptureResult{}, e
		}
		sources := preview.Sources
		if sources == nil {
			sources = archiveSources(preview.SourceLinks)
		}
		checked, e := documentflow.CheckCandidate(preview.Body, preview.Title, profile, preview.Parameters, *sources, preview.ContextHash)
		if e != nil {
			return models.CaptureResult{}, e
		}
		for _, issue := range checked.Report.Issues {
			if issue.Code == "unsafe_link" || issue.Code == "reference_locator" {
				return models.CaptureResult{}, fmt.Errorf("%w: unsafe draft link", documentstore.ErrInvalid)
			}
		}
	}
	cmd, err := s.BuildCaptureCommand(sp)
	if err != nil {
		return models.CaptureResult{}, err
	}
	objectID := firstNonEmptyString(sp.DraftThoughtID, sp.CommittedThoughtID, sp.ObjectID)
	if objectID == "" {
		objectID = models.NewThoughtID(s.now(), "capture:"+id)
	}
	if sp.ArchiveStrategy == scratchpad.ArchiveStrategyUpdate {
		objectID = sp.SourceThoughtID
	}
	if objectID == "" {
		return models.CaptureResult{}, ErrDiffRequired
	}
	operation := models.NewEventID(s.now())

	cmd.ThoughtID = objectID
	cmd.SaveOperationID = operation
	if sp.ArchiveStrategy == scratchpad.ArchiveStrategySupplement {
		if sp.SourceThoughtID == "" {
			return models.CaptureResult{}, errors.New("source_thought_id is required")
		}
		if cmd.DocumentProfile == nil || cmd.DocumentProfile.Family == models.DocumentFamilyNote {
			prefix := fmt.Sprintf("[补充] 前置 thought-%s\n\n", sp.SourceThoughtID)
			if !strings.HasPrefix(cmd.Content, prefix) {
				cmd.Content = prefix + cmd.Content
			}
			preview.Body = cmd.Content
		}
		cmd.Source = models.ThoughtSourceScratchpadSupplement
		cmd.RelatedThoughtIDs = uniqueStrings(append(cmd.RelatedThoughtIDs, sp.SourceThoughtID))
	}
	save := scratchpad.SaveCommand{Update: sp.ArchiveStrategy == scratchpad.ArchiveStrategyUpdate || sp.DraftThoughtID != "" || sp.CommittedThoughtID != "", OperationID: operation, ThoughtID: objectID, InputGeneration: sp.InputGeneration, Mode: mode, Strategy: sp.ArchiveStrategy, SourceThoughtID: sp.SourceThoughtID, Preview: preview, Command: cmd, ProfileSnapshot: preview.ProfileSnapshot, State: "prepared"}
	if save.Update {
		target, e := s.capture.GetThought(ctx, objectID)
		if e != nil {
			return models.CaptureResult{}, e
		}
		save.ExpectedTargetHash = captureTargetHash(target.Thought, target.Content)
	}
	sp, err = s.store.Update(id, func(current *scratchpad.Scratchpad) error {
		if current.InputGeneration != sp.InputGeneration {
			return ErrArchivePreviewStale
		}
		current.ObjectID = objectID
		current.SaveCommand = &save
		current.Operation = &scratchpad.ArchiveOperation{ID: operation, State: "saving", InputGeneration: sp.InputGeneration}
		return nil
	})
	if err != nil {
		return models.CaptureResult{}, err
	}
	result, e := s.runSave(ctx, sp, save)
	if e != nil {
		s.FailOperation(id, save.OperationID, e)
	}
	return result, e
}

func (s *ScratchpadService) runSave(ctx context.Context, sp scratchpad.Scratchpad, save scratchpad.SaveCommand) (models.CaptureResult, error) {
	cmd := save.Command
	cmd.ThoughtID = save.ThoughtID
	cmd.SaveOperationID = save.OperationID
	cmd.CaptureDraft = save.Mode == "draft"
	cmd.DocumentProfileSnapshot = save.ProfileSnapshot
	isUpdate := save.Update
	var result models.CaptureResult
	var err error
	if trusted, ok := s.capture.(interface {
		PrepareCaptureSave(string, models.CaptureCommand) error
	}); ok {
		if err = trusted.PrepareCaptureSave(save.OperationID, cmd); err != nil {
			return result, err
		}
	}
	if isUpdate {
		target := save.ThoughtID
		current, e := s.capture.GetThought(ctx, target)
		if e != nil {
			return result, e
		}
		if !captureSaveMatches(current.Thought, current.Content, cmd) {
			draftOnly := save.Mode == "draft"
			patch := models.ThoughtPatchRequest{DocumentProfile: cmd.DocumentProfile, DocumentProfileSnapshot: cmd.DocumentProfileSnapshot, SaveOperationID: save.OperationID, ExpectedCaptureHash: save.ExpectedTargetHash, CaptureDraft: &draftOnly, Body: &save.Preview.Body, Title: &save.Preview.Title, Tags: &save.Preview.Tags, Links: &save.Preview.SourceLinks, DocumentParameters: &save.Preview.Parameters, DocumentSources: save.Preview.Sources}
			if save.Strategy == scratchpad.ArchiveStrategyUpdate {
				patch.AINotes = patch.Body
				patch.Body = nil
			}

			if save.Preview.SourceGeneration > 0 {
				patch.ExpectedDocumentGeneration = &save.Preview.SourceGeneration
			}
			raw, e := patchRequestToRawBody(patch)
			if e != nil {
				return result, e
			}
			if save.Strategy == scratchpad.ArchiveStrategyUpdate {
				_, err = s.capture.PatchThought(ctx, target, sp.SessionID, patch, raw)
			} else {
				_, err = s.capture.ApplyDraftInternal(ctx, target, sp.SessionID, patch, raw)
			}
		}
		result = models.CaptureResult{Thought: models.Thought{ID: target}}
	} else {
		result, err = s.capture.Capture(ctx, cmd)
	}
	if err != nil {
		return result, err
	}
	if result.Thought.ID == "" {
		return result, errors.New("save returned no thought ID")
	}
	if save.Strategy == scratchpad.ArchiveStrategySupplement && save.SourceThoughtID != "" {
		parent, e := s.capture.GetThought(ctx, save.SourceThoughtID)
		if e != nil {
			return result, e
		}
		related := uniqueStrings(append(parent.Thought.RelatedThoughtIDs, result.Thought.ID))
		if len(related) != len(parent.Thought.RelatedThoughtIDs) {
			patch := models.ThoughtPatchRequest{ExpectedCaptureHash: captureTargetHash(parent.Thought, parent.Content), RelatedThoughtIDs: &related}
			raw, e := patchRequestToRawBody(patch)
			if e != nil {
				return result, e
			}
			if _, err = s.capture.PatchThought(ctx, save.SourceThoughtID, sp.SessionID, patch, raw); err != nil {
				return result, err
			}
		}
	}
	if trusted, ok := s.capture.(interface{ CompleteCaptureSave(string, string) error }); ok {
		if err = trusted.CompleteCaptureSave(save.OperationID, save.ThoughtID); err != nil {
			return result, err
		}
	}
	if save.Mode == "archive" {
		if _, err = s.store.MarkCommitted(sp.SessionID, result.Thought.ID); err != nil {
			return result, err
		}
	}
	var draftSnapshot *models.ThoughtSnapshot
	if save.Mode == "draft" {
		value, e := s.capture.GetThought(ctx, result.Thought.ID)
		if e != nil {
			return result, e
		}
		draftSnapshot = &value
	}
	_, err = s.store.Update(sp.SessionID, func(current *scratchpad.Scratchpad) error {
		if current.SaveCommand == nil || current.SaveCommand.OperationID != save.OperationID {
			return scratchpad.ErrConflict
		}
		current.SaveCommand.State = "completed"
		current.SaveCommand.ThoughtID = result.Thought.ID
		if save.Mode == "draft" {
			current.DraftThoughtID = result.Thought.ID
			if current.InputGeneration == save.InputGeneration && current.ArchivePreview != nil && draftSnapshot != nil {
				if draftSnapshot.Thought.Document != nil {
					current.ArchivePreview.SourceGeneration = draftSnapshot.Thought.Document.Generation
				}
				current.ArchivePreview.ContextHash = archiveContextHash(*current, draftSnapshot, current.ArchivePreview.DocumentProfile)
			}
		} else if current.InputGeneration == save.InputGeneration {
			current.Content = ""
			current.Messages = nil
			current.Draft = scratchpad.Draft{}
			current.ArchivePreview = nil
		}
		current.Operation = &scratchpad.ArchiveOperation{ID: save.OperationID, State: map[bool]string{true: "draft_saved", false: "saved"}[save.Mode == "draft"], InputGeneration: save.InputGeneration}
		return nil
	})
	if err == nil {
		if save.Mode == "archive" {
			s.publishCommittedEvent(result.Thought.ID, sp.SessionID, string(save.Strategy))
		}
		s.publishContextUpdatedEvent(sp.SessionID)
	}
	return result, err
}

// PreviewAndArchive is resumable from the durable operation. Generation occurs
// outside the Store transaction; publishing and accepting it use a CAS.
func (s *ScratchpadService) PreviewAndArchive(ctx context.Context, sp scratchpad.Scratchpad) error {
	if sp.Operation == nil {
		return fmt.Errorf("archive operation is required")
	}
	// Persist before any potentially generative context/document call. A crash
	// after this point has unknown completion and needs an explicit user retry.
	if _, err := s.store.Update(sp.SessionID, func(current *scratchpad.Scratchpad) error {
		if current.Operation == nil || current.Operation.ID != sp.Operation.ID || current.InputGeneration != sp.InputGeneration {
			return ErrArchivePreviewStale
		}
		current.Operation.GenerationStarted = true
		return nil
	}); err != nil {
		return err
	}
	var err error
	sp, err = s.prepareAutomaticArchiveContext(ctx, sp)
	if err != nil {
		return err
	}
	var current *models.ThoughtSnapshot
	if id := archiveTarget(sp); id != "" {
		value, e := s.capture.GetThought(ctx, id)
		if e != nil {
			return e
		}
		current = &value
	}

	preview, err := s.PrepareArchive(ctx, sp, current)
	if err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	saved, err := s.PublishPreview(sp, preview)
	if err != nil {
		return err
	}
	s.publishContextUpdatedEvent(sp.SessionID)
	if saved.Operation.AutoCommit && saved.Operation.State == "ready" {
		_, err = s.Commit(ctx, sp.SessionID)
	}
	return err
}

// A menu click may arrive before the asynchronous context response. Classify
// and extract from this generation instead of silently archiving as a default
// note. The same CAS rules prevent a late result from consuming newer input.
func (s *ScratchpadService) prepareAutomaticArchiveContext(ctx context.Context, sp scratchpad.Scratchpad) (scratchpad.Scratchpad, error) {
	hasProfile := sp.SessionContext.CandidateProfileID != "" || sp.SessionContext.FrozenProfile != ""
	if s.profiles == nil || s.contextAI == nil || strings.TrimSpace(sp.Content) == "" || (hasProfile && strings.TrimSpace(sp.SessionContext.CandidateBody) != "") {
		return sp, nil
	}
	result, err := s.buildCaptureContextWithRetry(ctx, sp.SessionID, sp.Content, ai.CaptureContextRequest{
		SessionID: sp.SessionID, Content: sp.Content, Messages: captureContextMessages(sp.Messages),
		Existing:          captureContextFromScratchpad(enrichmentSeedContext(sp)),
		AvailableProfiles: s.availableProfileDescriptors(sp), ExistingProfile: s.existingProfile(sp),
	})
	if err != nil {
		return sp, err
	}
	if result.ProfileContractHash != "" {
		profile, err := s.profiles.Resolve(models.DocumentProfileRef{ProfileID: result.CandidateProfileID, Version: result.CandidateProfileVersion})
		if err != nil || profile.Ref.ContentHash != result.ProfileContractHash {
			return sp, ErrArchivePreviewStale
		}
	}
	result = preserveLatestUserTurns(result, sp.Messages)
	next := captureContextToScratchpad(result, sp)
	// A menu's accepted target and save intent remain authoritative even when
	// the extraction model describes exploration rather than an archive intent.
	next.ArchiveStrategy = sp.ArchiveStrategy
	next.ArchiveIntent = sp.ArchiveIntent
	return s.updateSessionContextAt(sp.SessionID, next, false, &sp.InputGeneration)
}

func archiveTarget(sp scratchpad.Scratchpad) string {
	if sp.ArchiveStrategy == scratchpad.ArchiveStrategyUpdate || sp.ArchiveStrategy == scratchpad.ArchiveStrategySupplement {
		return sp.SourceThoughtID
	}
	return firstNonEmptyString(sp.DraftThoughtID, sp.CommittedThoughtID)
}

// A replay may acknowledge only the frozen bytes. A later edit belongs to the
// user, even if the session's completion marker was never written.
func captureSaveMatches(thought models.Thought, content models.ThoughtContent, cmd models.CaptureCommand) bool {
	body := originalContent(cmd)
	if cmd.DocumentProfile != nil {
		body = cmd.Content
	}
	return strings.TrimSpace(content.AINotes) == strings.TrimSpace(body) && thought.UserTitle == strings.TrimSpace(cmd.Title) && thought.CaptureDraft == cmd.CaptureDraft && equalStringSets(thought.UserTags, cmd.Tags) && strings.TrimSpace(content.Links) == strings.TrimSpace(renderLinks(cmd.Links)) &&
		(len(thought.DocumentParameters) == 0 && len(cmd.DocumentParameters) == 0 || documentstore.HashJSON(thought.DocumentParameters) == documentstore.HashJSON(cmd.DocumentParameters)) && (emptySources(thought.DocumentSources) && emptySources(cmd.DocumentSources) || documentstore.HashJSON(thought.DocumentSources) == documentstore.HashJSON(cmd.DocumentSources)) && documentstore.HashJSON(thought.DocumentProfile) == documentstore.HashJSON(cmd.DocumentProfile)
}

func emptySources(s *models.SourceManifest) bool { return s == nil || len(s.Sources) == 0 }

func captureTargetHash(t models.Thought, c models.ThoughtContent) string {
	return documentstore.HashJSON(struct {
		Title      string
		Content    models.ThoughtContent
		Parameters map[string]string
		Sources    *models.SourceManifest
		Draft      bool
		Tags       []string
		Profile    *models.DocumentProfileRef
	}{t.UserTitle, c, t.DocumentParameters, t.DocumentSources, t.CaptureDraft, t.UserTags, t.DocumentProfile})
}

func archiveNeedsInput(v models.ArchiveValidation) bool {
	for _, issue := range v.Issues {
		if issue.Code == "input_required" || issue.Code == "research_evidence_missing" {
			return true
		}
	}
	return false
}

// CompleteCaptureSave records a replayable outbox before notifications. Events
// use the operation identity; consumers can collapse delivery after a restart.
func (s *Service) CompleteCaptureSave(operation, id string) error {
	registry := objectlocation.For(s.workspace.RootPath)
	op, e := registry.Operation(operation)
	if e != nil {
		return e
	}
	if op.Kind != "capture_save" || op.ObjectID != id {
		return scratchpad.ErrConflict
	}
	op.EventType = models.EventThoughtCaptured
	if e = registry.Complete(op); e != nil {
		return e
	}
	return s.ReplayCaptureSave(operation)
}
func (s *Service) ReplayCaptureSave(operation string) error {
	op, e := objectlocation.For(s.workspace.RootPath).Operation(operation)
	if e != nil {
		return e
	}
	if op.Kind != "capture_save" || op.State != "completed" {
		return scratchpad.ErrConflict
	}
	snapshot, e := s.GetThought(context.Background(), op.ObjectID)
	if e != nil {
		return e
	}
	eventutil.Post(s.eventHub, models.DomainEvent{EventID: op.ID + "-captured", EventType: models.EventThoughtCaptured, SourceUnit: "capture", OccurredAt: op.CreatedAt, WorkspaceID: s.workspace.ID, ResourceType: models.ResourceTypeThought, ResourceID: op.ObjectID, PayloadVersion: 1, Payload: snapshot.Thought})
	eventutil.Post(s.eventHub, models.DomainEvent{EventID: op.ID + "-git", EventType: models.EventGitCommitRequested, SourceUnit: "capture", OccurredAt: op.CreatedAt, WorkspaceID: s.workspace.ID, ResourceType: models.ResourceTypeThought, ResourceID: op.ObjectID, PayloadVersion: 1, Payload: models.GitCommitRequestedPayload{Paths: markdown.GitPaths(s.workspace.RootPath, op.ObjectID), Reason: "capture.archive", ResourceIDs: []string{op.ObjectID}}})
	return nil
}
