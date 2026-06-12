package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/thoughtlock"
)

// ErrInvalidPatchField is returned when a PATCH request contains an
// unknown key. The HTTP layer surfaces this as 400 with the offending
// field names in the error details.
var ErrInvalidPatchField = errors.New("capture: invalid patch field")

// ErrLocked is returned when the thought is being held by another
// session. The HTTP layer surfaces this as 409.
var ErrLocked = thoughtlock.ErrLocked

// ErrRefining is returned when the thought is held by the refiner
// (LLM call in flight). It is a sub-condition of ErrLocked, but signals
// a different remediation to the caller: a brief retry is appropriate
// because the lock is typically released within seconds. The HTTP
// layer surfaces this as 409 with a distinct error code.
var ErrRefining = errors.New("capture: thought is being refined")

type Service struct {
	workspace       *models.Workspace
	jobs            *jobstore.Store
	eventHub        event.Hub
	locker          *thoughtlock.Locker
	now             func() time.Time
	duplicatePolicy string
}

type Option func(*Service)

func WithDuplicatePolicy(policy string) Option {
	return func(s *Service) {
		if strings.TrimSpace(policy) != "" {
			s.duplicatePolicy = strings.ToLower(strings.TrimSpace(policy))
		}
	}
}

// WithLocker attaches a thoughtlock.Locker. The same instance should be
// shared with the refiner service so PATCH and refine serialize against
// each other.
func WithLocker(locker *thoughtlock.Locker) Option {
	return func(s *Service) {
		s.locker = locker
	}
}

func NewService(workspace *models.Workspace, jobs *jobstore.Store, eventHub event.Hub, options ...Option) *Service {
	service := &Service{
		workspace:       workspace,
		jobs:            jobs,
		eventHub:        eventHub,
		now:             func() time.Time { return time.Now().UTC() },
		duplicatePolicy: "warn",
	}
	for _, option := range options {
		option(service)
	}
	return service
}

func (s *Service) Capture(ctx context.Context, cmd models.CaptureCommand) (models.CaptureResult, error) {
	_ = ctx
	if s == nil || s.workspace == nil {
		return models.CaptureResult{}, errors.New("capture service is not ready")
	}
	if err := validateCaptureCommand(cmd); err != nil {
		return models.CaptureResult{}, err
	}

	now := s.now()
	source := cmd.Source
	if source == "" {
		source = models.ThoughtSourceManual
	}
	original := originalContent(cmd)
	contentHash := models.ContentHash(original)
	captureStatus := models.CaptureStatusCaptured
	errorRefs := []models.ErrorRef{}
	duplicates, err := findDuplicateThoughts(s.workspace.RootPath, contentHash, "")
	if err != nil {
		return models.CaptureResult{}, err
	}
	if len(duplicates) > 0 && s.duplicatePolicy != "allow" {
		captureStatus = models.CaptureStatusDuplicateWarned
		errorRefs = append(errorRefs, models.NewErrorRef(
			"thoughtflow.capture.duplicate_warned",
			fmt.Sprintf("possible duplicate content with thought(s): %s", strings.Join(duplicates, ", ")),
			false,
		))
	}
	thoughtID := models.NewThoughtID(now, original)
	relPath := filepath.ToSlash(markdown.ThoughtRelativePath(thoughtID))
	thought := models.Thought{
		ID:            thoughtID,
		Type:          cmd.Type,
		Source:        source,
		UserTitle:     strings.TrimSpace(cmd.Title),
		URL:           strings.TrimSpace(cmd.URL),
		Path:          relPath,
		CreatedAt:     now,
		UpdatedAt:     now,
		ContentHash:   contentHash,
		UserTags:      normalizeList(cmd.Tags),
		TopicIDs:      normalizeList(cmd.TopicHints),
		Errors:        errorRefs,
		CaptureStatus: captureStatus,
		RefineStatus:  models.RefineStatusPending,
		IndexStatus:   models.IndexStatusPending,
		TopicStatus:   models.TopicStatusUnmatched,
	}
	thought.DisplayTitle = displayTitle(thought, original)
	content := models.ThoughtContent{Original: original}

	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		return models.CaptureResult{}, err
	}

	captured := models.DomainEvent{
		EventType:      models.EventThoughtCaptured,
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    s.workspace.ID,
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thought.ID,
		PayloadVersion: 1,
		Payload:        thought,
	}
	eventutil.Post(s.eventHub, captured)
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:    models.EventGitCommitRequested,
		SourceUnit:   "capture",
		OccurredAt:   now,
		WorkspaceID:  s.workspace.ID,
		ResourceType: models.ResourceTypeThought,
		ResourceID:   thought.ID,
		Payload: models.GitCommitRequestedPayload{
			Paths:       []string{thought.Path},
			Reason:      "capture",
			ResourceIDs: []string{thought.ID},
		},
		PayloadVersion: 1,
	})

	return models.CaptureResult{Thought: thought, Jobs: []models.Job{}}, nil
}

func (s *Service) GetThought(ctx context.Context, thoughtID string) (models.ThoughtSnapshot, error) {
	_ = ctx
	if strings.TrimSpace(thoughtID) == "" {
		return models.ThoughtSnapshot{}, errors.New("thought id is required")
	}
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.ThoughtSnapshot{}, err
	}
	return models.ThoughtSnapshot{Thought: thought, Content: content}, nil
}

// PatchThought applies a partial update to a thought in place. Pointer
// fields in the request distinguish "field absent" from "field present
// with empty value" — the absent case leaves the existing value alone,
// while the present-empty case clears it (where the field semantics
// allow). Unknown keys are rejected with ErrInvalidPatchField so the
// front end catches typos at the API boundary.
//
// The thought is locked for the duration of the write so a concurrent
// refine or PATCH cannot race the disk write. Callers MUST supply a
// non-empty sessionID; the lock is released even on error.
func (s *Service) PatchThought(ctx context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	_ = ctx
	if s == nil || s.workspace == nil {
		return models.ThoughtSnapshot{}, errors.New("capture service is not ready")
	}
	if strings.TrimSpace(thoughtID) == "" {
		return models.ThoughtSnapshot{}, errors.New("thought id is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		return models.ThoughtSnapshot{}, errors.New("session id is required")
	}
	if unknown, err := unknownPatchFields(rawBody); err != nil {
		return models.ThoughtSnapshot{}, fmt.Errorf("%w: %s", ErrInvalidPatchField, strings.Join(unknown, ","))
	}
	if s.locker != nil {
		if err := s.locker.Acquire(thoughtID, sessionID); err != nil {
			if errors.Is(err, ErrLocked) {
				if holder, ok := s.locker.Holder(thoughtID); ok && holder == thoughtlock.RefinerSessionID {
					return models.ThoughtSnapshot{}, ErrRefining
				}
			}
			return models.ThoughtSnapshot{}, err
		}
		defer s.locker.Release(thoughtID, sessionID)
	}
	return s.applyPatchLocked(thoughtID, sessionID, request, rawBody)
}

// ApplyDraftInternal writes a patch to a thought without acquiring
// the thought lock. It exists for the scratchpad commit pipeline:
// commitFresh runs capture.Capture → applyDraftToThought back-to-back
// inside one HTTP request, and PatchThought's lock would race the
// async refiner/expander that the Capture step just enqueued — they
// try to acquire the same lock, see it held, and MarkSucceeded with
// "skipped: thought is locked" without retrying. Net effect was that
// scratchpad-committed thoughts permanently lost their summary /
// ai_tags / key_points / expansion_plan.
//
// Concurrency is still safe: commitFresh holds the scratchpad itself
// (not the thought) as its serialization point, and the refiner /
// expander have their own background-loop retry on the next sweep
// if the apply races. The user-driven PATCH path is unaffected —
// it still goes through PatchThought, which keeps the lock so a
// human edit cannot be clobbered by a background job.
func (s *Service) ApplyDraftInternal(ctx context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	_ = ctx
	if s == nil || s.workspace == nil {
		return models.ThoughtSnapshot{}, errors.New("capture service is not ready")
	}
	if strings.TrimSpace(thoughtID) == "" {
		return models.ThoughtSnapshot{}, errors.New("thought id is required")
	}
	if strings.TrimSpace(sessionID) == "" {
		return models.ThoughtSnapshot{}, errors.New("session id is required")
	}
	if unknown, err := unknownPatchFields(rawBody); err != nil {
		return models.ThoughtSnapshot{}, fmt.Errorf("%w: %s", ErrInvalidPatchField, strings.Join(unknown, ","))
	}
	return s.applyPatchLocked(thoughtID, sessionID, request, rawBody)
}

// applyPatchLocked holds the actual field merge + disk write +
// event publish. The lock is the caller's responsibility (PatchThought
// acquires one; ApplyDraftInternal does not).
func (s *Service) applyPatchLocked(thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.ThoughtSnapshot{}, err
	}
	now := s.now()
	if request.Title != nil {
		title := strings.TrimSpace(*request.Title)
		if title == "" {
			return models.ThoughtSnapshot{}, errors.New("title must not be empty")
		}
		thought.UserTitle = title
	}
	if request.Tags != nil {
		thought.UserTags = normalizeTags(*request.Tags)
	}
	if request.AINotesAppend != nil {
		paragraph := strings.TrimSpace(*request.AINotesAppend)
		if paragraph != "" {
			content.AINotes = markdown.AppendAINotes(content.AINotes, paragraph, now)
		}
	}
	if request.TopicIDs != nil {
		thought.TopicIDs = append([]string(nil), (*request.TopicIDs)...)
	}
	thought.UpdatedAt = now
	if err := markdown.WriteThought(s.workspace.RootPath, thought, content); err != nil {
		return models.ThoughtSnapshot{}, err
	}
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:      models.EventThoughtPatched,
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    s.workspace.ID,
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thought.ID,
		PayloadVersion: 1,
		Payload: map[string]any{
			"thought_id": thought.ID,
			"patched_by": sessionID,
			"patch_keys": patchKeys(request),
		},
	})
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:      models.EventGitCommitRequested,
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    s.workspace.ID,
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thought.ID,
		PayloadVersion: 1,
		Payload: map[string]any{
			"reason": "patch",
			"paths":  []string{thought.Path},
		},
	})
	return models.ThoughtSnapshot{Thought: thought, Content: content}, nil
}

// unknownPatchFields returns the JSON keys in rawBody that are not
// declared on models.ThoughtPatchRequest. An empty result means the body
// only contained known fields (including none).
func unknownPatchFields(rawBody []byte) ([]string, error) {
	if len(rawBody) == 0 {
		return nil, nil
	}
	var generic map[string]json.RawMessage
	if err := json.Unmarshal(rawBody, &generic); err != nil {
		return nil, err
	}
	allowed := map[string]struct{}{}
	rt := reflect.TypeOf(models.ThoughtPatchRequest{})
	for idx := 0; idx < rt.NumField(); idx++ {
		field := rt.Field(idx)
		tag := field.Tag.Get("json")
		name := strings.Split(tag, ",")[0]
		if name == "" || name == "-" {
			continue
		}
		allowed[name] = struct{}{}
	}
	unknown := []string{}
	for key := range generic {
		if _, ok := allowed[key]; !ok {
			unknown = append(unknown, key)
		}
	}
	if len(unknown) == 0 {
		return nil, nil
	}
	sort.Strings(unknown)
	return unknown, ErrInvalidPatchField
}

func patchKeys(request models.ThoughtPatchRequest) []string {
	keys := []string{}
	if request.Title != nil {
		keys = append(keys, "title")
	}
	if request.Tags != nil {
		keys = append(keys, "tags")
	}
	if request.AINotesAppend != nil {
		keys = append(keys, "ai_notes_append")
	}
	if request.TopicIDs != nil {
		keys = append(keys, "topic_ids")
	}
	return keys
}

func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		cleaned := strings.TrimSpace(tag)
		if cleaned == "" {
			continue
		}
		if _, ok := seen[cleaned]; ok {
			continue
		}
		seen[cleaned] = struct{}{}
		out = append(out, cleaned)
	}
	sort.Strings(out)
	return out
}

func (s *Service) ListThoughts(ctx context.Context) ([]models.Thought, error) {
	_ = ctx
	if s == nil || s.workspace == nil || strings.TrimSpace(s.workspace.RootPath) == "" {
		return []models.Thought{}, nil
	}
	return listThoughts(s.workspace.RootPath)
}

func (s *Service) FindDuplicatesByContentHash(ctx context.Context, contentHash string, currentID string) ([]models.Thought, error) {
	_ = ctx
	if s == nil || s.workspace == nil || strings.TrimSpace(s.workspace.RootPath) == "" {
		return []models.Thought{}, nil
	}
	return findDuplicateThoughtRecords(s.workspace.RootPath, contentHash, currentID)
}

func (s *Service) Workspace() *models.Workspace {
	return s.workspace
}

func listThoughts(rootPath string) ([]models.Thought, error) {
	if strings.TrimSpace(rootPath) == "" {
		return []models.Thought{}, nil
	}
	thoughtsPath := filepath.Join(rootPath, "thoughts")
	thoughts := []models.Thought{}
	err := filepath.WalkDir(thoughtsPath, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(filePath) != ".md" {
			return nil
		}
		thoughtID := strings.TrimSuffix(filepath.Base(filePath), ".md")
		thought, _, err := markdown.ReadThought(rootPath, thoughtID)
		if err != nil {
			return err
		}
		thoughts = append(thoughts, thought)
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return []models.Thought{}, nil
	}
	return thoughts, err
}

func findDuplicateThoughts(rootPath string, contentHash string, currentID string) ([]string, error) {
	records, err := findDuplicateThoughtRecords(rootPath, contentHash, currentID)
	if err != nil {
		return nil, err
	}
	duplicates := make([]string, 0, len(records))
	for _, thought := range records {
		duplicates = append(duplicates, thought.ID)
	}
	return duplicates, nil
}

func findDuplicateThoughtRecords(rootPath string, contentHash string, currentID string) ([]models.Thought, error) {
	if strings.TrimSpace(rootPath) == "" || strings.TrimSpace(contentHash) == "" {
		return []models.Thought{}, nil
	}
	thoughtsPath := filepath.Join(rootPath, "thoughts")
	duplicates := []models.Thought{}
	err := filepath.WalkDir(thoughtsPath, func(filePath string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() || filepath.Ext(filePath) != ".md" {
			return nil
		}
		thoughtID := strings.TrimSuffix(filepath.Base(filePath), ".md")
		if thoughtID == currentID {
			return nil
		}
		thought, _, err := markdown.ReadThought(rootPath, thoughtID)
		if err != nil {
			return err
		}
		if thought.ContentHash == contentHash {
			duplicates = append(duplicates, thought)
		}
		return nil
	})
	if errors.Is(err, os.ErrNotExist) {
		return []models.Thought{}, nil
	}
	return duplicates, err
}

func validateCaptureCommand(cmd models.CaptureCommand) error {
	switch cmd.Type {
	case models.ThoughtTypeText:
		if strings.TrimSpace(cmd.Content) == "" {
			return errors.New("content is required")
		}
	case models.ThoughtTypeURL:
		if strings.TrimSpace(cmd.URL) == "" {
			return errors.New("url is required")
		}
		parsed, err := url.ParseRequestURI(strings.TrimSpace(cmd.URL))
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return errors.New("url is invalid")
		}
	default:
		return errors.New("type must be text or url")
	}
	return nil
}

func originalContent(cmd models.CaptureCommand) string {
	if cmd.Type == models.ThoughtTypeURL {
		if strings.TrimSpace(cmd.Content) == "" {
			return strings.TrimSpace(cmd.URL)
		}
		return strings.TrimSpace(cmd.URL) + "\n\n" + strings.TrimSpace(cmd.Content)
	}
	return strings.TrimSpace(cmd.Content)
}

func normalizeList(values []string) []string {
	seen := map[string]struct{}{}
	ret := []string{}
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		ret = append(ret, value)
	}
	return ret
}

func displayTitle(thought models.Thought, original string) string {
	if thought.UserTitle != "" {
		return thought.UserTitle
	}
	if thought.Type == models.ThoughtTypeURL && thought.URL != "" {
		return thought.URL
	}
	firstLine := strings.TrimSpace(strings.Split(original, "\n")[0])
	if len(firstLine) > 80 {
		return firstLine[:80]
	}
	return firstLine
}
