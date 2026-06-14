// Package compose implements the Compose running unit. It owns
// the compose draft lifecycle (create → list/get → save-as-thought)
// and is the successor to the legacy synthesis flow. The HTTP
// surface is /api/compose/drafts*; the on-disk store lives in
// internal/pkg/composedraft (workspace/compose/drafts/{id}.yaml).
//
// compose.Service depends on the LLM synthesis provider (the same
// Provider interface used by the refiner module) and on a small
// CaptureSink interface for save-as-thought. The sink indirection
// avoids a circular import with internal/modules/capture; the
// application module wires the concrete capture.Capture sink in at
// startup.
package biz

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/composedraft"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
)

const (
	composeSessionID = "compose-session"

	EventComposeDraftCreated = "compose.draft_created"
	EventComposeDraftSaved   = "compose.draft_saved"

	composeJobType = "compose_save"
)

// CaptureSink is the small subset of capture.Service that compose
// needs to materialise a draft as a Thought. The interface lives
// here so the compose module can stay decoupled from the capture
// module; the application module wires the concrete implementation
// at startup.
type CaptureSink interface {
	Capture(ctx context.Context, cmd models.CaptureCommand) (models.CaptureResult, error)
}

type Service struct {
	workspace  *models.Workspace
	draftStore *composedraft.Store
	jobs       *jobstore.Store
	eventHub   event.Hub
	synthesis  ai.SynthesisProvider
	capture    CaptureSink
	now        func() time.Time
	model      string
}

func NewService(
	workspace *models.Workspace,
	draftStore *composedraft.Store,
	jobs *jobstore.Store,
	eventHub event.Hub,
	synthesis ai.SynthesisProvider,
	capture CaptureSink,
) *Service {
	return &Service{
		workspace:  workspace,
		draftStore: draftStore,
		jobs:       jobs,
		eventHub:   eventHub,
		synthesis:  synthesis,
		capture:    capture,
		now:        func() time.Time { return time.Now().UTC() },
		model:      "local-rule",
	}
}

// SetModel lets the application module override the reported model
// string (typically the chat model name from LLM config) so the
// persisted ComposeDraft carries the model that actually generated
// the content.
func (s *Service) SetModel(model string) {
	if model = strings.TrimSpace(model); model != "" {
		s.model = model
	}
}

// CreateDraft hydrates the incoming sources into ThoughtSnapshots
// (for thought sources) and context blocks (for search/topic/
// capture sources), calls the LLM synthesis provider, and persists
// the resulting draft to compose/drafts/{id}.yaml.
//
// Sources are deduplicated by (source_type, source_id) and the
// source_links list is unioned across all sources so the saved
// Thought can carry a complete provenance trail.
func (s *Service) CreateDraft(ctx context.Context, req models.ComposeRequest) (models.ComposeDraft, error) {
	if s == nil || s.draftStore == nil {
		return models.ComposeDraft{}, errors.New("compose service is not ready")
	}
	if s.synthesis == nil {
		return models.ComposeDraft{}, errors.New("compose synthesis provider is not ready")
	}
	if len(req.Sources) == 0 {
		return models.ComposeDraft{}, errors.New("sources are required")
	}

	deduped, sourceLinks := dedupeSources(req.Sources)
	snapshots, hydrateErrs := s.hydrateSnapshots(ctx, deduped)
	if len(snapshots) == 0 {
		// We tolerate partial hydration: a missing thought file is
		// skipped when other source types can still provide context.
		// Only a purely-broken thought-only request should fail.
		if len(hydrateErrs) > 0 {
			return models.ComposeDraft{}, hydrateErrs[0]
		}
		return models.ComposeDraft{}, errors.New("no compose sources could be loaded")
	}

	thoughtIDs := make([]string, 0, len(snapshots))
	for _, snap := range snapshots {
		thoughtIDs = append(thoughtIDs, snap.Thought.ID)
	}

	now := s.now()
	synthReq := ai.SynthesisRequest{
		ThoughtIDs:  thoughtIDs,
		Goal:        firstNonEmpty(req.Goal, "Compose selected sources"),
		Format:      firstNonEmpty(req.Format, models.ComposeFormatSummary),
		Snapshots:   snapshots,
		SourceLinks: sourceLinks,
	}
	if prompt := strings.TrimSpace(req.Prompt); prompt != "" {
		// Selected_thought_ids and prompt are appended to the goal
		// line so the LLM gets a single instruction block; the
		// prompt is otherwise opaque to the wire shape.
		synthReq.Goal = synthReq.Goal + "\n\n" + prompt
	}

	synthDraft, err := s.synthesis.Synthesize(ctx, synthReq)
	if err != nil {
		return models.ComposeDraft{}, fmt.Errorf("compose synthesize: %w", err)
	}

	draft := models.ComposeDraft{
		ID:          firstNonEmpty(synthDraft.ID, models.NewJobID("compose", now)),
		Sources:     deduped,
		Goal:        synthReq.Goal,
		Format:      firstNonEmpty(synthReq.Format, models.ComposeFormatSummary),
		Content:     synthDraft.Content,
		SourceLinks: sourceLinks,
		Model:       firstNonEmpty(synthDraft.Model, s.model),
		Status:      models.ComposeStatusDraft,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if len(synthDraft.History) > 0 {
		draft.History = convertHistory(synthDraft.History)
	}
	// Selected_thought_ids travels on the prompt line above; the
	// source list itself is the authoritative record of which
	// thoughts the user picked, so we do not duplicate it here.

	saved, err := s.draftStore.SaveDraft(ctx, draft)
	if err != nil {
		return models.ComposeDraft{}, err
	}
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      EventComposeDraftCreated,
			SourceUnit:     "compose",
			OccurredAt:     s.now(),
			WorkspaceID:    s.workspaceID(),
			ResourceType:   models.ResourceTypeWorkspace,
			ResourceID:     s.workspaceID(),
			PayloadVersion: 1,
			Payload:        saved,
		})
	}
	return saved, nil
}

func (s *Service) ListDrafts(ctx context.Context) ([]models.ComposeDraft, error) {
	if s == nil || s.draftStore == nil {
		return nil, errors.New("compose draft store is not ready")
	}
	return s.draftStore.ListDrafts(ctx)
}

func (s *Service) GetDraft(ctx context.Context, draftID string) (models.ComposeDraft, error) {
	if s == nil || s.draftStore == nil {
		return models.ComposeDraft{}, errors.New("compose draft store is not ready")
	}
	return s.draftStore.GetDraft(ctx, draftID)
}

// SaveDraft materialises a stored draft as a Thought via the
// capture sink. The Thought's source is set to "compose" and the
// user-supplied title/tags override the defaults the LLM produced.
// The original draft file is updated to record the saved_thought_id
// and a history event.
func (s *Service) SaveDraft(ctx context.Context, draftID string, req models.ComposeSaveRequest) (models.ComposeSaveResult, error) {
	if s == nil || s.capture == nil {
		return models.ComposeSaveResult{}, errors.New("compose capture sink is not ready")
	}
	if s.draftStore == nil {
		return models.ComposeSaveResult{}, errors.New("compose draft store is not ready")
	}
	draft, err := s.draftStore.GetDraft(ctx, draftID)
	if err != nil {
		return models.ComposeSaveResult{}, err
	}
	if draft.Status == models.ComposeStatusSaved {
		return models.ComposeSaveResult{}, fmt.Errorf("compose draft %q already saved as thought %q", draftID, draft.SavedThoughtID)
	}

	content := strings.TrimSpace(req.Content)
	if content == "" {
		content = draft.Content
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = deriveComposeTitle(draft)
	}
	tags := req.Tags
	if len(tags) == 0 {
		tags = deriveComposeTags(draft)
	}

	cmd := models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Content: content,
		Title:   title,
		Tags:    tags,
		Source:  models.ThoughtSourceCompose,
	}
	job, jobErr := s.recordJob(draftID)
	if jobErr != nil && s.jobs == nil {
		return models.ComposeSaveResult{}, jobErr
	}

	result, err := s.capture.Capture(ctx, cmd)
	if err != nil {
		if jobErr == nil {
			_, _ = s.jobs.MarkFailed(job, models.NewErrorRef("thoughtflow.compose.save_failed", err.Error(), true))
		}
		return models.ComposeSaveResult{}, fmt.Errorf("compose capture: %w", err)
	}
	if jobErr == nil {
		job, _ = s.jobs.MarkSucceeded(job, "compose draft saved")
		eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
	}

	saved, err := s.draftStore.MarkSaved(ctx, draftID, content, result.Thought)
	if err != nil {
		return models.ComposeSaveResult{}, err
	}
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, models.DomainEvent{
			EventType:      EventComposeDraftSaved,
			SourceUnit:     "compose",
			OccurredAt:     s.now(),
			WorkspaceID:    s.workspaceID(),
			ResourceType:   models.ResourceTypeThought,
			ResourceID:     result.Thought.ID,
			PayloadVersion: 1,
			Payload:        saved,
		})
	}
	return models.ComposeSaveResult{
		Thought:     result.Thought,
		Jobs:        result.Jobs,
		SourceLinks: dedupeStrings(append([]string{}, draft.SourceLinks...)),
	}, nil
}

// hydrateSnapshots turns a ComposeSource list into the ThoughtSnapshot
// list the synthesis provider needs. Thought sources are loaded from
// disk. Search result, topic section, and capture session sources are
// represented as minimal context snapshots so a draft can be generated
// from any supported source type while preserving source_links.
//
// Partial hydration is allowed: missing thought files are recorded
// in the returned error list and skipped, so a single bad source ID
// in a multi-source request does not abort the whole compose. The
// caller only receives a non-nil error when no source of any type
// survived hydration, because then the LLM has nothing to draw on.
func (s *Service) hydrateSnapshots(_ context.Context, sources []models.ComposeSource) ([]models.ThoughtSnapshot, []error) {
	if s == nil || s.workspace == nil {
		return nil, []error{errors.New("compose workspace is not ready")}
	}
	snapshots := []models.ThoughtSnapshot{}
	errs := []error{}
	for _, src := range sources {
		switch src.SourceType {
		case models.ComposeSourceTypeThought, "":
			thought, content, err := markdown.ReadThought(s.workspace.RootPath, src.SourceID)
			if err != nil {
				errs = append(errs, fmt.Errorf("hydrate thought %q: %w", src.SourceID, err))
				continue
			}
			snapshots = append(snapshots, models.ThoughtSnapshot{Thought: thought, Content: content})
		case models.ComposeSourceTypeSearchResult, models.ComposeSourceTypeTopicSection, models.ComposeSourceTypeCaptureSession:
			snapshots = append(snapshots, composeSourceSnapshot(src))
		default:
			continue
		}
	}
	if len(snapshots) > 0 {
		return snapshots, nil
	}
	return snapshots, errs
}

func composeSourceSnapshot(src models.ComposeSource) models.ThoughtSnapshot {
	title := strings.TrimSpace(src.Title)
	if title == "" {
		title = strings.TrimSpace(src.SourceID)
	}
	original := strings.Join([]string{
		"Compose source type: " + strings.TrimSpace(src.SourceType),
		"Compose source id: " + strings.TrimSpace(src.SourceID),
	}, "\n")
	if link := strings.TrimSpace(src.SourceLink); link != "" {
		original += "\nSource link: " + link
	}
	return models.ThoughtSnapshot{
		Thought: models.Thought{
			ID:           strings.TrimSpace(src.SourceID),
			Type:         models.ThoughtTypeText,
			Source:       strings.TrimSpace(src.SourceType),
			UserTitle:    title,
			DisplayTitle: title,
			Path:         strings.TrimSpace(src.SourceLink),
		},
		Content: models.ThoughtContent{
			Original: original,
			Links:    strings.TrimSpace(src.SourceLink),
		},
	}
}

func (s *Service) recordJob(draftID string) (models.Job, error) {
	if s == nil || s.jobs == nil {
		return models.Job{}, errors.New("jobstore is not ready")
	}
	job, err := s.jobs.Create(composeJobType, models.ResourceTypeWorkspace, s.workspaceID(), "compose draft "+draftID+" save queued")
	if err != nil {
		return models.Job{}, err
	}
	job, _ = s.jobs.MarkRunning(job)
	if s.eventHub != nil {
		eventutil.Post(s.eventHub, jobEvent(s.workspaceID(), job))
	}
	return job, nil
}

func (s *Service) workspaceID() string {
	if s == nil || s.workspace == nil {
		return ""
	}
	return s.workspace.ID
}

func dedupeSources(sources []models.ComposeSource) ([]models.ComposeSource, []string) {
	seen := map[string]struct{}{}
	out := []models.ComposeSource{}
	links := []string{}
	for _, src := range sources {
		key := strings.TrimSpace(src.SourceType) + "\x00" + strings.TrimSpace(src.SourceID)
		if key == "\x00" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, src)
		if link := strings.TrimSpace(src.SourceLink); link != "" {
			links = append(links, link)
		}
	}
	// Stable, sorted source_links for deterministic YAML output and
	// for tests that assert the list of links.
	sort.Strings(links)
	links = dedupeStrings(links)
	return out, links
}

func dedupeStrings(values []string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	for _, v := range values {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func deriveComposeTitle(draft models.ComposeDraft) string {
	goal := strings.TrimSpace(draft.Goal)
	if goal != "" {
		first := strings.SplitN(goal, "\n", 2)[0]
		first = strings.TrimSpace(first)
		if first != "" {
			return first
		}
	}
	for _, src := range draft.Sources {
		if strings.TrimSpace(src.Title) != "" {
			return strings.TrimSpace(src.Title)
		}
	}
	if len(draft.Sources) > 0 {
		return strings.TrimSpace(draft.Sources[0].SourceID)
	}
	return "Untitled compose"
}

func deriveComposeTags(draft models.ComposeDraft) []string {
	tags := []string{"compose"}
	for _, src := range draft.Sources {
		switch src.SourceType {
		case models.ComposeSourceTypeTopicSection:
			tags = append(tags, "topic")
		case models.ComposeSourceTypeSearchResult:
			tags = append(tags, "search")
		case models.ComposeSourceTypeCaptureSession:
			tags = append(tags, "capture")
		}
	}
	return dedupeStrings(tags)
}

func jobEvent(workspaceID string, job models.Job) models.DomainEvent {
	return models.DomainEvent{
		EventType:      models.EventJobUpdated,
		SourceUnit:     "compose",
		OccurredAt:     time.Now().UTC(),
		WorkspaceID:    workspaceID,
		ResourceType:   job.ResourceType,
		ResourceID:     job.ResourceID,
		PayloadVersion: 1,
		Payload:        job,
	}
}

func convertHistory(in []models.SynthesisDraftHistory) []models.ComposeDraftHistory {
	if len(in) == 0 {
		return nil
	}
	out := make([]models.ComposeDraftHistory, 0, len(in))
	for _, h := range in {
		out = append(out, models.ComposeDraftHistory{
			Status:    h.Status,
			Message:   h.Message,
			ThoughtID: h.ThoughtID,
			At:        h.At,
		})
	}
	return out
}
