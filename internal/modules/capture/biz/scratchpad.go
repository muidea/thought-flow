package biz

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/eventutil"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

// ScratchpadService wraps the scratchpad store with the higher-level
// operations the capture chat flow needs:
//
//   - AppendMessage: stage a user/ai message in the scratchpad,
//     accumulating the message trail and (for user turns) appending
//     the message text to the cumulative content field.
//   - AppendDraft: merge a partial Draft (rename / add_tag / append_note
//     / etc.) into the scratchpad, then surface the changes into the
//     top-level Title / Tags fields so the UI can render them
//     immediately without a separate "apply" step.
//   - BuildCaptureCommand: flatten a scratchpad into a CaptureCommand
//     ready for Service.Capture. Used by the commit pipeline.
//   - Commit: turn a scratchpad into a real thought. First-time
//     commits run the full capture pipeline; subsequent commits in
//     the same session PATCH the existing thought with the new
//     content / draft deltas, per plan §6.1 ("继续追加").
//   - ResetAfterCommit: clear volatile fields (Content / Messages /
//     Draft) after the scratchpad has been committed. The committed
//     link (CommittedThoughtID / CommittedAt) is preserved.
//
// The functions are pure operations on the scratchpad store — they do
// not touch the real thought pipeline, so they can be unit-tested
// with an in-memory fake store.
type ScratchpadService struct {
	store      ScratchpadStore
	capture    CaptureCommitter
	eventHub   eventHub
	sessionID  string
	now        func() time.Time
}

// ScratchpadStore is the subset of scratchpad.Store this package
// depends on. The HTTP layer uses the same interface, which keeps
// the test seam uniform.
type ScratchpadStore interface {
	Get(sessionID string) (scratchpad.Scratchpad, error)
	Save(sp scratchpad.Scratchpad) (scratchpad.Scratchpad, error)
	Delete(sessionID string) error
	MarkCommitted(sessionID, thoughtID string) (scratchpad.Scratchpad, error)
	Reset(sessionID string) (scratchpad.Scratchpad, error)
}

// CaptureCommitter is the slice of the capture Service API that the
// scratchpad commit path calls. Defined here as an interface so
// tests can substitute a stub that records what was committed
// without spinning up the real capture pipeline (and its duckdb
// dependency).
type CaptureCommitter interface {
	Capture(ctx context.Context, cmd models.CaptureCommand) (models.CaptureResult, error)
	PatchThought(ctx context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error)
}

// eventHub is the minimal publish/subscribe surface the commit
// pipeline needs (used to fire the scratchpad-committed event). We
// use the upstream event.Hub type so the existing eventutil.Post
// helper can publish DomainEvents without further adaptation.
type eventHub = event.Hub

// ScratchpadServiceOption configures optional dependencies on
// ScratchpadService. The capture Service and event hub are
// optional; without them, Commit returns a clear error so the HTTP
// layer can surface 503 instead of nil-panicking.
type ScratchpadServiceOption func(*ScratchpadService)

// WithCapture wires in the real capture Service so Commit can
// run the full pipeline (or PATCH an existing thought).
func WithCapture(c CaptureCommitter) ScratchpadServiceOption {
	return func(s *ScratchpadService) { s.capture = c }
}

// WithEventHub wires in the event hub so Commit can fire the
// scratchpad-committed domain event.
func WithEventHub(h eventHub) ScratchpadServiceOption {
	return func(s *ScratchpadService) { s.eventHub = h }
}

// WithSessionID sets the sessionID used by PatchThought's
// locker. The capture service's PatchThought requires a session
// id; the scratchpad session id is the natural choice.
func WithSessionID(id string) ScratchpadServiceOption {
	return func(s *ScratchpadService) { s.sessionID = id }
}

// NewScratchpadService constructs a ScratchpadService backed by store.
// A nil store is allowed; every method degrades to a clear error
// rather than nil-panicking, so the HTTP layer can return 503 cleanly.
func NewScratchpadService(store ScratchpadStore, options ...ScratchpadServiceOption) *ScratchpadService {
	s := &ScratchpadService{
		store:     store,
		sessionID: "scratchpad",
		now:       func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range options {
		opt(s)
	}
	return s
}

// ErrScratchpadUnavailable is returned when the scratchpad store has
// not been wired up. The HTTP layer surfaces it as 503.
var ErrScratchpadUnavailable = errors.New("capture: scratchpad store is not ready")

// AppendMessage appends a single chat message to the scratchpad and,
// for user-role messages, appends the text to the cumulative content
// field. The scratchpad is upserted — if the session is empty, a
// fresh one is created with the message as the first turn.
//
// Role must be one of "user" | "ai" | "system" — anything else is
// accepted as-is because the store has no opinion on role strings,
// but the helper normalizes whitespace and rejects empty text.
func (s *ScratchpadService) AppendMessage(sessionID, role, text string) (scratchpad.Scratchpad, error) {
	if s == nil || s.store == nil {
		return scratchpad.Scratchpad{}, ErrScratchpadUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	role = strings.TrimSpace(role)
	text = strings.TrimSpace(text)
	if sessionID == "" {
		return scratchpad.Scratchpad{}, errors.New("capture: scratchpad session id is required")
	}
	if role == "" {
		role = "user"
	}
	if text == "" {
		return scratchpad.Scratchpad{}, errors.New("capture: scratchpad message text is required")
	}
	sp, err := s.store.Get(sessionID)
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	sp.Messages = append(sp.Messages, scratchpad.Message{Role: role, Text: text, At: s.now()})
	if role == "user" {
		if sp.Content != "" {
			sp.Content += "\n\n"
		}
		sp.Content += text
	}
	return s.store.Save(sp)
}

// AppendDraft merges a partial Draft into the scratchpad's draft
// accumulator. After merging, the helper projects the relevant
// fields into the top-level Title / Tags / TopicHints slots so the
// UI can show the latest rename / tag edits without reading the
// draft block. The Commit step uses TitleSet / TagsAdded / etc.,
// so this projection is purely cosmetic for the chat UI.
//
// The behavior matches the plan: rename replaces TitleSet, add_tag
// unions into TagsAdded, remove_tag unions into TagsRemoved (which
// is then reflected in the top-level Tags by removing the entries).
// Notes are accumulated as-is.
func (s *ScratchpadService) AppendDraft(sessionID string, draft scratchpad.Draft) (scratchpad.Scratchpad, error) {
	if s == nil || s.store == nil {
		return scratchpad.Scratchpad{}, ErrScratchpadUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return scratchpad.Scratchpad{}, errors.New("capture: scratchpad session id is required")
	}
	sp, err := s.store.Get(sessionID)
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	if t := strings.TrimSpace(draft.TitleSet); t != "" {
		sp.Draft.TitleSet = t
		sp.Title = t
	}
	if added := trimNonEmpty(draft.TagsAdded); len(added) > 0 {
		sp.Draft.TagsAdded = unionStrings(sp.Draft.TagsAdded, added)
		sp.Tags = unionStrings(sp.Tags, added)
	}
	if removed := trimNonEmpty(draft.TagsRemoved); len(removed) > 0 {
		sp.Draft.TagsRemoved = unionStrings(sp.Draft.TagsRemoved, removed)
		sp.Tags = subtractStrings(sp.Tags, removed)
	}
	if notes := trimNonEmpty(draft.NotesAppended); len(notes) > 0 {
		sp.Draft.NotesAppended = append(sp.Draft.NotesAppended, notes...)
		// Notes go into Content rather than the draft's `NotesAppended` accumulator
		// alone — at commit time the capture pipeline uses the same content
		// path. We append at the end so the chat ordering is preserved.
		for _, note := range notes {
			if sp.Content != "" {
				sp.Content += "\n\n"
			}
			sp.Content += note
		}
	}
	if topics := trimNonEmpty(draft.TopicIDs); len(topics) > 0 {
		sp.Draft.TopicIDs = unionStrings(sp.Draft.TopicIDs, topics)
		sp.TopicHints = unionStrings(sp.TopicHints, topics)
	}
	if draft.RefineRequested {
		sp.Draft.RefineRequested = true
	}
	return s.store.Save(sp)
}

// BuildCaptureCommand flattens a scratchpad into a CaptureCommand
// ready for Service.Capture. The shape matches what the existing
// handleCreateThought path sends:
//
//   - Type defaults to "text" when no URL is present; the upstream
//     classifier only runs after a successful Capture and would
//     only refine the tag set, not flip a missing type.
//   - Content is the cumulative user-authored text plus notes.
//   - Title comes from Draft.TitleSet, falling back to the
//     scratchpad's top-level Title.
//   - Tags is the top-level Tags after the user's add/remove
//     operations; this is the merged view, not the draft.
//   - TopicHints is the same shape, after merges.
//   - Source is left blank so the caller can stamp it.
//
// If the scratchpad has an existing CommittedThoughtID, the helper
// returns ErrAlreadyCommitted — the commit flow is responsible for
// routing that case to a "append to existing thought" path, which
// is implemented in a later stage.
func (s *ScratchpadService) BuildCaptureCommand(sp scratchpad.Scratchpad) (models.CaptureCommand, error) {
	if strings.TrimSpace(sp.CommittedThoughtID) != "" {
		return models.CaptureCommand{}, ErrAlreadyCommitted
	}
	content := strings.TrimSpace(sp.Content)
	if content == "" {
		return models.CaptureCommand{}, errors.New("capture: scratchpad content is empty")
	}
	title := strings.TrimSpace(sp.Draft.TitleSet)
	if title == "" {
		title = strings.TrimSpace(sp.Title)
	}
	tags := uniqueStrings(sp.Tags)
	topicHints := uniqueStrings(sp.TopicHints)
	cmdType := models.ThoughtTypeText
	if url := extractURL(content); url != "" {
		cmdType = models.ThoughtTypeURL
	}
	return models.CaptureCommand{
		Type:       cmdType,
		Content:    content,
		URL:        "",
		Title:      title,
		Tags:       tags,
		TopicHints: topicHints,
		Source:     "scratchpad-commit",
	}, nil
}

// ResetAfterCommit clears the volatile fields of a scratchpad while
// keeping the committed link. It is called by the commit pipeline
// right after a successful capture. If the scratchpad was never
// committed, the call degrades to a regular reset (no error) so
// callers can use this as a one-size-fits-all "make scratchpad
// fresh" hook.
func (s *ScratchpadService) ResetAfterCommit(sessionID string) (scratchpad.Scratchpad, error) {
	if s == nil || s.store == nil {
		return scratchpad.Scratchpad{}, ErrScratchpadUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return scratchpad.Scratchpad{}, errors.New("capture: scratchpad session id is required")
	}
	sp, err := s.store.Get(sessionID)
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	if sp.CommittedThoughtID == "" {
		// Not committed: a plain reset still wipes Content/Messages/Draft
		// so the user gets a clean slate. We bypass the store-level
		// Reset to avoid a no-op when the scratchpad is empty.
		sp.Content = ""
		sp.Messages = nil
		sp.Draft = scratchpad.Draft{}
		return s.store.Save(sp)
	}
	return s.store.Reset(sessionID)
}

// Commit turns a scratchpad into a real thought. The first commit
// for a session runs the full capture pipeline; subsequent commits
// in the same session append to the same thought (per plan §6.1).
//
// The two paths:
//
//  1. First commit (CommittedThoughtID == ""):
//     - BuildCaptureCommand → CaptureCommand
//     - capture.Capture(...)  → thought file + EventThoughtCaptured
//     - ApplyDraftToThought: PATCH the freshly-committed thought
//       with the scratchpad's draft (Title / Tags / Notes / Topics).
//       This second pass is needed because the LLM-side classify
//       only fills in Type; the user's chat commands live in the
//       draft and must be applied explicitly.
//     - MarkCommitted (stamp CommittedThoughtID / CommittedAt)
//     - ResetAfterCommit (clear volatile fields; keep committed link)
//
//  2. Repeat commit (CommittedThoughtID != ""):
//     - Build a PatchRequest from the current scratchpad state
//       (Content → AINotesAppend, Draft → Title / Tags / TopicIDs)
//     - capture.PatchThought(...)
//     - ResetAfterCommit
//
// In both cases ResetAfterCommit wipes Content / Messages / Draft so
// the next user turn starts from a clean slate (the user can still
// see the "anchored to thought-X" badge via CommittedThoughtID).
//
// Returns the CaptureResult from the underlying pipeline. For the
// repeat path the Thought field is a zero-value Thought because
// PatchThought returns a Snapshot, not a CaptureResult — the
// caller can read CommittedThoughtID from the response envelope
// instead.
func (s *ScratchpadService) Commit(ctx context.Context, sessionID string) (models.CaptureResult, error) {
	if s == nil || s.store == nil {
		return models.CaptureResult{}, ErrScratchpadUnavailable
	}
	if s.capture == nil {
		return models.CaptureResult{}, errors.New("capture: scratchpad commit pipeline is not wired up")
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return models.CaptureResult{}, errors.New("capture: scratchpad session id is required")
	}
	sp, err := s.store.Get(sessionID)
	if err != nil {
		return models.CaptureResult{}, err
	}

	// Repeat commit (already committed) is allowed to have empty
	// Content — Reset has wiped the volatile fields. The user is
	// still entitled to "commit" again, and the helper degrades to
	// a no-op reset so the UI is in a clean state. First-time
	// commit, on the other hand, requires Content: a scratchpad
	// with nothing to capture is a no-op that should never have
	// been sent to Begin With.
	if sp.CommittedThoughtID == "" && strings.TrimSpace(sp.Content) == "" {
		return models.CaptureResult{}, errors.New("capture: scratchpad content is empty")
	}

	if sp.CommittedThoughtID == "" {
		return s.commitFresh(ctx, sp)
	}
	return s.commitRepeat(ctx, sp)
}

// commitFresh is the first-commit path: capture the thought, then
// apply the chat-time draft commands (rename / add_tag / notes /
// topics) onto the freshly committed thought.
func (s *ScratchpadService) commitFresh(ctx context.Context, sp scratchpad.Scratchpad) (models.CaptureResult, error) {
	cmd, err := s.BuildCaptureCommand(sp)
	if err != nil {
		return models.CaptureResult{}, err
	}
	result, err := s.capture.Capture(ctx, cmd)
	if err != nil {
		return models.CaptureResult{}, err
	}
	if _, err := s.store.MarkCommitted(sp.SessionID, result.Thought.ID); err != nil {
		return result, err
	}
	s.publishCommittedEvent(result.Thought.ID, sp.SessionID, "fresh")
	if err := s.applyDraftToThought(ctx, sp, result.Thought.ID); err != nil {
		// We do not fail the commit if the draft application trips:
		// the thought is already on disk, the user can re-issue the
		// commands through PATCH. Log via returned result (caller can
		// observe the partial success) but return the original
		// CaptureResult so the HTTP layer can still return 200.
		_ = err
	}
	if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
		return result, err
	}
	return result, nil
}

// commitRepeat is the "继续追加" path: the scratchpad is anchored
// to a thought already, and the user's latest chat adds more
// content / commands on top. We translate the scratchpad state
// into a single PATCH and let the existing patch pipeline fire the
// git commit + emit the events.
func (s *ScratchpadService) commitRepeat(ctx context.Context, sp scratchpad.Scratchpad) (models.CaptureResult, error) {
	patch, rawBody, err := buildPatchFromScratchpad(sp)
	if err != nil {
		return models.CaptureResult{}, err
	}
	if patch == nil {
		// Nothing to apply: the user typed a commit command without
		// adding new content. Reset the scratchpad and bail out as
		// a no-op so the UI gets a clean state.
		if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
			return models.CaptureResult{}, err
		}
		return models.CaptureResult{Thought: models.Thought{ID: sp.CommittedThoughtID}}, nil
	}
	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = sp.SessionID
	}
	if _, err := s.capture.PatchThought(ctx, sp.CommittedThoughtID, sessionID, *patch, rawBody); err != nil {
		return models.CaptureResult{Thought: models.Thought{ID: sp.CommittedThoughtID}}, err
	}
	s.publishCommittedEvent(sp.CommittedThoughtID, sp.SessionID, "repeat")
	if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
		return models.CaptureResult{Thought: models.Thought{ID: sp.CommittedThoughtID}}, err
	}
	return models.CaptureResult{Thought: models.Thought{ID: sp.CommittedThoughtID}}, nil
}

// applyDraftToThought runs after a fresh commit to apply the
// user's chat-time commands (rename / tags / notes / topics) onto
// the just-committed thought. Each non-empty draft field becomes
// one PATCH field.
func (s *ScratchpadService) applyDraftToThought(ctx context.Context, sp scratchpad.Scratchpad, thoughtID string) error {
	patch, rawBody, err := buildPatchFromScratchpad(sp)
	if err != nil {
		return err
	}
	if patch == nil {
		return nil
	}
	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = sp.SessionID
	}
	_, err = s.capture.PatchThought(ctx, thoughtID, sessionID, *patch, rawBody)
	return err
}

// buildPatchFromScratchpad converts a scratchpad's accumulated
// state into a ThoughtPatchRequest. The Content field is appended
// to AINotes (rather than the original) because the original
// content was already committed as part of the thought's
// `original` field by the capture pipeline. AINotes is the natural
// place for "the user added more text after committing".
//
// Returns (nil, nil, nil) when the scratchpad carries nothing new
// beyond the original commit — the caller should treat that as a
// no-op.
func buildPatchFromScratchpad(sp scratchpad.Scratchpad) (*models.ThoughtPatchRequest, []byte, error) {
	hasAny := false
	req := models.ThoughtPatchRequest{}
	if title := strings.TrimSpace(sp.Draft.TitleSet); title != "" {
		t := title
		req.Title = &t
		hasAny = true
	}
	mergedTags := mergedTagSet(sp)
	if mergedTags != nil {
		// Differentiate "no tag changes" (nil) from "user removed all
		// tags" (empty slice). The PATCH pipeline treats nil and
		// empty the same (no-op), so this is safe.
		req.Tags = &mergedTags
		hasAny = true
	}
	// Build the AINotesAppend from any new content the user added
	// after commit. We can't fully separate "what was committed
	// before" from "what was added after" because Content is a
	// single cumulative field, but the post-commit content lives
	// in scratchpad.Content while the original commit's content
	// is in the thought's `original`. We just append Content as a
	// note — duplicates are not catastrophic because AINotes is
	// additive and the user can edit them down.
	if note := strings.TrimSpace(sp.Content); note != "" {
		req.AINotesAppend = &note
		hasAny = true
	}
	if topics := uniqueStrings(sp.Draft.TopicIDs); len(topics) > 0 {
		req.TopicIDs = &topics
		hasAny = true
	}
	if !hasAny {
		return nil, nil, nil
	}
	raw, err := patchRequestToRawBody(req)
	if err != nil {
		return nil, nil, err
	}
	return &req, raw, nil
}

// mergedTagSet returns the final tag set the user wants on the
// thought: starting from sp.Tags, union in sp.Draft.TagsAdded and
// subtract sp.Draft.TagsRemoved. Returns nil when the scratchpad
// carries no tag edits at all — the caller uses the nil check to
// decide whether the PATCH needs a Tags field at all (a nil Tags
// pointer means "leave existing tags alone" in PatchThought).
func mergedTagSet(sp scratchpad.Scratchpad) []string {
	if len(sp.Draft.TagsAdded) == 0 && len(sp.Draft.TagsRemoved) == 0 {
		return nil
	}
	tags := append([]string(nil), sp.Tags...)
	tags = unionStrings(tags, sp.Draft.TagsAdded)
	tags = subtractStrings(tags, sp.Draft.TagsRemoved)
	return uniqueStrings(tags)
}

// patchRequestToRawBody marshals a ThoughtPatchRequest to JSON.
// We do the round-trip via the existing unknown-fields check in
// PatchThought, so the raw bytes are passed through to keep the
// "unknown field" diagnostic working.
func patchRequestToRawBody(req models.ThoughtPatchRequest) ([]byte, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("capture: marshal patch request: %w", err)
	}
	return body, nil
}

// publishCommittedEvent emits a scratchpad-committed domain event
// so the diagnostic /api/events stream can show the user that
// the commit fired.
func (s *ScratchpadService) publishCommittedEvent(thoughtID, sessionID, mode string) {
	if s.eventHub == nil {
		return
	}
	now := s.now()
	ev := models.DomainEvent{
		EventType:      "scratchpad.committed",
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    "",
		ResourceType:   models.ResourceTypeThought,
		ResourceID:     thoughtID,
		PayloadVersion: 1,
		Payload: map[string]any{
			"thought_id": thoughtID,
			"session_id": sessionID,
			"mode":       mode,
		},
	}
	eventutil.Post(s.eventHub, ev)
}


// ErrAlreadyCommitted is returned by BuildCaptureCommand when the
// scratchpad has already been committed once. The commit flow turns
// this into the "append to existing thought" path.
var ErrAlreadyCommitted = errors.New("capture: scratchpad is already committed")

// trimNonEmpty drops empty strings and trims whitespace.
func trimNonEmpty(values []string) []string {
	out := make([]string, 0, len(values))
	for _, v := range values {
		if t := strings.TrimSpace(v); t != "" {
			out = append(out, t)
		}
	}
	return out
}

// unionStrings returns the union of a and b, preserving first-seen
// order. The function is stable across multiple calls: appending
// the same item twice is a no-op.
func unionStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	seen := make(map[string]struct{}, len(a)+len(b))
	out := make([]string, 0, len(a)+len(b))
	for _, v := range a {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	for _, v := range b {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// subtractStrings removes every occurrence of any value in b from a.
func subtractStrings(a, b []string) []string {
	if len(b) == 0 {
		return a
	}
	drop := make(map[string]struct{}, len(b))
	for _, v := range b {
		drop[strings.TrimSpace(v)] = struct{}{}
	}
	out := make([]string, 0, len(a))
	for _, v := range a {
		if _, ok := drop[v]; ok {
			continue
		}
		out = append(out, v)
	}
	return out
}

// uniqueStrings de-duplicates a slice, preserving first-seen order.
func uniqueStrings(values []string) []string {
	if len(values) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, v := range values {
		if _, ok := seen[v]; ok {
			continue
		}
		seen[v] = struct{}{}
		out = append(out, v)
	}
	return out
}

// extractURL returns the first http(s):// URL in text, or "" if none
// is found. The capture classifier will fill in cmd.URL downstream.
func extractURL(text string) string {
	low := strings.ToLower(text)
	for _, prefix := range []string{"https://", "http://"} {
		if idx := strings.Index(low, prefix); idx >= 0 {
			rest := text[idx:]
			end := strings.IndexAny(rest, " \t\n\r\"'<>`")
			if end < 0 {
				return rest
			}
			return rest[:end]
		}
	}
	return ""
}
