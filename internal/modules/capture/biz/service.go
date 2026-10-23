package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/jobstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
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

	// A compose save persists this reservation before calling capture. On
	// replay, read the existing Markdown instead of overwriting user/AI edits.
	if cmd.ThoughtID != "" {
		if cmd.Source != models.ThoughtSourceCompose || strings.ContainsAny(cmd.ThoughtID, `/\`) {
			return models.CaptureResult{}, errors.New("invalid reserved capture ID")
		}
		existing, _, err := markdown.ReadThought(s.workspace.RootPath, cmd.ThoughtID)
		if err == nil {
			return models.CaptureResult{Thought: existing, Jobs: []models.Job{}}, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return models.CaptureResult{}, err
		}
	}
	now := s.now()
	source := cmd.Source
	if source == "" {
		source = models.ThoughtSourceManual
	}
	original := originalContent(cmd)
	if cmd.DocumentProfile != nil {
		original = cmd.Content
	}
	contentHash := models.ContentHash(original)
	captureStatus := models.CaptureStatusCaptured
	errorRefs := []models.ErrorRef{}
	duplicates, err := findDuplicateThoughtsByContent(s.workspace.RootPath, original, contentHash, "")
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
	thoughtID := cmd.ThoughtID
	if thoughtID == "" {
		thoughtID = models.NewThoughtID(now, original)
	}
	location, err := objectlocation.For(s.workspace.RootPath).Reserve(thoughtID, cmd.OwnerTopicID, "capture-"+thoughtID)
	if err != nil {
		return models.CaptureResult{}, err
	}
	relPath := location.Placement.ThoughtPath
	thought := models.Thought{
		OwnerTopicID:            cmd.OwnerTopicID,
		ID:                      thoughtID,
		Type:                    cmd.Type,
		Source:                  source,
		UserTitle:               strings.TrimSpace(cmd.Title),
		URL:                     strings.TrimSpace(cmd.URL),
		Path:                    relPath,
		CreatedAt:               now,
		UpdatedAt:               now,
		ContentHash:             contentHash,
		UserTags:                normalizeList(cmd.Tags),
		TopicIDs:                normalizeList(cmd.TopicHints),
		Errors:                  errorRefs,
		CaptureStatus:           captureStatus,
		RefineStatus:            models.RefineStatusPending,
		IndexStatus:             models.IndexStatusPending,
		TopicStatus:             models.TopicStatusUnmatched,
		RelatedThoughtIDs:       normalizeList(cmd.RelatedThoughtIDs),
		SuggestedTopicIDs:       normalizeList(cmd.SuggestedTopicIDs),
		DocumentProfile:         cloneDocumentProfileRef(cmd.DocumentProfile),
		DocumentParameters:      cmd.DocumentParameters,
		DocumentSources:         cmd.DocumentSources,
		DocumentProfileSnapshot: cmd.DocumentProfileSnapshot,
	}
	thought.DisplayTitle = displayTitle(thought, original)
	content := models.ThoughtContent{
		AINotes: original,
		Links:   renderLinks(cmd.Links),
	}

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
	bindings, e := objectlocation.For(s.workspace.RootPath).Bindings(thoughtID)
	if e != nil {
		return models.ThoughtSnapshot{}, e
	}
	return models.ThoughtSnapshot{Thought: thought, Content: content, ResourceBindings: bindings.Bindings}, nil
}

func (s *Service) DeleteThought(ctx context.Context, thoughtID, sessionID string) error {
	_ = ctx
	if s == nil || s.workspace == nil {
		return errors.New("capture service is not ready")
	}
	thoughtID = strings.TrimSpace(thoughtID)
	if thoughtID == "" {
		return errors.New("thought id is required")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("session id is required")
	}
	location, err := objectlocation.For(s.workspace.RootPath).Lookup(thoughtID)
	if err != nil {
		return err
	}
	thoughtPath := location.Placement.ThoughtPath
	if s.locker != nil {
		if err := s.locker.Acquire(thoughtID, sessionID); err != nil {
			if errors.Is(err, ErrLocked) {
				if holder, ok := s.locker.Holder(thoughtID); ok && thoughtlock.IsWorkflowSession(holder) {
					return ErrRefining
				}
			}
			return err
		}
		defer s.locker.Release(thoughtID, sessionID)
	}
	if err := markdown.DeleteThought(s.workspace.RootPath, thoughtID); err != nil {
		return err
	}
	now := s.now()
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:      models.EventThoughtDeleted,
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    s.workspace.ID,
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thoughtID,
		PayloadVersion: 1,
		Payload:        map[string]any{"thought_id": thoughtID, "path": thoughtPath, "deleted_by": sessionID},
	})
	eventutil.Post(s.eventHub, models.DomainEvent{
		EventType:      models.EventGitCommitRequested,
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    s.workspace.ID,
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thoughtID,
		PayloadVersion: 1,
		Payload:        models.GitCommitRequestedPayload{Paths: append(markdown.GitPaths(s.workspace.RootPath, thoughtID), location.Placement.DocumentRoot+"/head.json"), Reason: "delete", ResourceIDs: []string{thoughtID}},
	})
	return nil
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
				if holder, ok := s.locker.Holder(thoughtID); ok && thoughtlock.IsWorkflowSession(holder) {
					return models.ThoughtSnapshot{}, ErrRefining
				}
			}
			return models.ThoughtSnapshot{}, err
		}
		defer s.locker.Release(thoughtID, sessionID)
	}
	return s.applyPatchLocked(thoughtID, sessionID, request, rawBody)
}

// ApplyDraftInternal applies repeated archive edits under the same Thought lock
// as interactive edits and background processing.
func (s *Service) ApplyDraftInternal(ctx context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	return s.PatchThought(ctx, thoughtID, sessionID, request, rawBody)
}

func (s *Service) applyPatchLocked(thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error) {
	thought, content, err := markdown.ReadThought(s.workspace.RootPath, thoughtID)
	if err != nil {
		return models.ThoughtSnapshot{}, err
	}
	if thought.Document != nil && request.ExpectedDocumentGeneration != nil {
		head, e := s.Documents().Store.Head(thought.ID)
		if e != nil {
			return models.ThoughtSnapshot{}, e
		}
		if head.Generation != *request.ExpectedDocumentGeneration {
			return models.ThoughtSnapshot{}, documentstore.ErrConflict
		}
	}
	thought.DocumentExpectedGeneration = request.ExpectedDocumentGeneration
	if request.DocumentParameters != nil {
		thought.DocumentParameters = *request.DocumentParameters
	}
	now := s.now()
	if request.Title != nil {
		title := strings.TrimSpace(*request.Title)
		if title == "" {
			return models.ThoughtSnapshot{}, errors.New("title must not be empty")
		}
		thought.UserTitle = title
	}
	if request.Body != nil {
		body := *request.Body
		if thought.DocumentProfile == nil {
			body = strings.TrimSpace(body)
		}
		if body == "" {
			return models.ThoughtSnapshot{}, errors.New("body must not be empty")
		}
		content.AINotes = body
		content.Original = ""
		thought.ContentHash = models.ContentHash(body)
	}
	if request.AINotes != nil {
		notes := *request.AINotes
		if thought.DocumentProfile == nil {
			notes = strings.TrimSpace(notes)
		}
		if notes == "" {
			return models.ThoughtSnapshot{}, errors.New("ai_notes must not be empty")
		}
		content.AINotes = notes
		content.Original = ""
		thought.ContentHash = models.ContentHash(notes)
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
	if request.Links != nil {
		content.Links = renderLinks(*request.Links)
	}
	if request.RelatedThoughtIDs != nil {
		thought.RelatedThoughtIDs = normalizeList(*request.RelatedThoughtIDs)
	}
	if request.SuggestedTopicIDs != nil {
		thought.SuggestedTopicIDs = normalizeList(*request.SuggestedTopicIDs)
	}
	if request.DocumentProfile != nil {
		thought.DocumentProfile = cloneDocumentProfileRef(request.DocumentProfile)
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
	bindings, e := objectlocation.For(s.workspace.RootPath).Bindings(thoughtID)
	if e != nil {
		return models.ThoughtSnapshot{}, e
	}
	return models.ThoughtSnapshot{Thought: thought, Content: content, ResourceBindings: bindings.Bindings}, nil
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
	if request.Body != nil {
		keys = append(keys, "body")
	}
	if request.AINotes != nil {
		keys = append(keys, "ai_notes")
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
	if request.DocumentProfile != nil {
		keys = append(keys, "document_profile")
	}
	return keys
}

func cloneDocumentProfileRef(ref *models.DocumentProfileRef) *models.DocumentProfileRef {
	if ref == nil {
		return nil
	}
	copy := *ref
	return &copy
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
	ids, err := markdown.ListThoughtIDs(rootPath)
	if err != nil {
		return nil, err
	}
	thoughts := []models.Thought{}
	for _, id := range ids {
		thought, _, e := markdown.ReadThought(rootPath, id)
		if errors.Is(e, os.ErrNotExist) {
			continue
		}
		if e != nil {
			return nil, e
		}
		thoughts = append(thoughts, thought)
	}
	sort.Slice(thoughts, func(i, j int) bool {
		if !thoughts[i].UpdatedAt.Equal(thoughts[j].UpdatedAt) {
			return thoughts[i].UpdatedAt.After(thoughts[j].UpdatedAt)
		}
		if !thoughts[i].CreatedAt.Equal(thoughts[j].CreatedAt) {
			return thoughts[i].CreatedAt.After(thoughts[j].CreatedAt)
		}
		return thoughts[i].ID > thoughts[j].ID
	})
	return thoughts, nil
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

func findDuplicateThoughtsByContent(rootPath, content, contentHash, currentID string) ([]string, error) {
	ids, e := markdown.ListThoughtIDs(rootPath)
	if e != nil {
		return nil, e
	}
	out := []string{}
	for _, id := range ids {
		if id == currentID {
			continue
		}
		thought, c, e := markdown.ReadThought(rootPath, id)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return nil, e
		}
		if thought.ContentHash == contentHash || nearDuplicateContent(content, firstNonEmptyContent(c.AINotes, c.ExtractedContent, c.Original)) {
			out = append(out, id)
		}
	}
	return out, nil
}

func firstNonEmptyContent(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func nearDuplicateContent(left, right string) bool {
	leftRunes := normalizedContentRunes(left)
	rightRunes := normalizedContentRunes(right)
	if len(leftRunes) < 400 || len(rightRunes) < 400 {
		return false
	}
	shorter, longer := leftRunes, rightRunes
	if len(shorter) > len(longer) {
		shorter, longer = longer, shorter
	}
	if float64(len(shorter))/float64(len(longer)) < 0.8 {
		return false
	}
	const width = 8
	leftSet := runeShingles(shorter, width)
	rightSet := runeShingles(longer, width)
	shared := 0
	for value := range leftSet {
		if _, ok := rightSet[value]; ok {
			shared++
		}
	}
	return 2*float64(shared)/float64(len(leftSet)+len(rightSet)) >= 0.92
}

func normalizedContentRunes(value string) []rune {
	out := []rune{}
	for _, r := range strings.ToLower(value) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) {
			out = append(out, r)
		}
	}
	return out
}

func runeShingles(value []rune, width int) map[string]struct{} {
	out := map[string]struct{}{}
	if len(value) < width {
		return out
	}
	for idx := 0; idx+width <= len(value); idx++ {
		out[string(value[idx:idx+width])] = struct{}{}
	}
	return out
}

func findDuplicateThoughtRecords(rootPath string, contentHash string, currentID string) ([]models.Thought, error) {
	thoughts, e := listThoughts(rootPath)
	if e != nil {
		return nil, e
	}
	out := []models.Thought{}
	for _, thought := range thoughts {
		if thought.ID != currentID && thought.ContentHash == contentHash {
			out = append(out, thought)
		}
	}
	return out, nil
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

func renderLinks(values []string) string {
	links := normalizeList(values)
	if len(links) == 0 {
		return ""
	}
	lines := make([]string, 0, len(links))
	for _, link := range links {
		lines = append(lines, "- [["+link+"]]")
	}
	return strings.Join(lines, "\n")
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

func (s *Service) Documents() *documentflow.Service { return documentflow.For(s.workspace.RootPath) }
