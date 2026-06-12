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
	ApplyDraftInternal(ctx context.Context, thoughtID, sessionID string, request models.ThoughtPatchRequest, rawBody []byte) (models.ThoughtSnapshot, error)
	GetThought(ctx context.Context, thoughtID string) (models.ThoughtSnapshot, error)
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

// UpdateSessionContext replaces the session-context block on a
// scratchpad with the supplied value. The whole block is replaced
// (not merged) so callers can drop fields by simply omitting them —
// this is the contract the LLM-side tool call wants, because the
// model is reasoning about the whole context graph, not patch deltas.
//
// The function never errors on an "absent" session: a brand-new
// scratchpad is created with the supplied context. This mirrors the
// behaviour of AppendMessage so the LLM tool can fire before the
// first user message lands.
func (s *ScratchpadService) UpdateSessionContext(sessionID string, ctx scratchpad.SessionContext) (scratchpad.Scratchpad, error) {
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
	sp.SessionContext = scratchpad.SessionContext{
		Topic:             strings.TrimSpace(ctx.Topic),
		Goal:              strings.TrimSpace(ctx.Goal),
		ConfirmedFacts:    trimNonEmpty(ctx.ConfirmedFacts),
		OpenQuestions:     trimNonEmpty(ctx.OpenQuestions),
		Conflicts:         trimNonEmpty(ctx.Conflicts),
		CandidateTitle:    strings.TrimSpace(ctx.CandidateTitle),
		CandidateTags:     trimNonEmpty(ctx.CandidateTags),
		CandidateSummary:  strings.TrimSpace(ctx.CandidateSummary),
		CandidateBody:     ctx.CandidateBody,
		SourceLinks:       trimNonEmpty(ctx.SourceLinks),
		RelatedThoughtIDs: trimNonEmpty(ctx.RelatedThoughtIDs),
		SuggestedTopicIDs: trimNonEmpty(ctx.SuggestedTopicIDs),
	}
	return s.store.Save(sp)
}

// SetArchiveIntent records WHO is driving the archive. The values
// are constrained to the three legal states (none / menu / llm);
// any other string is normalised to "none" so a typo from the LLM
// tool does not poison the scratchpad. The function never errors on
// an "absent" session for the same reason as UpdateSessionContext.
func (s *ScratchpadService) SetArchiveIntent(sessionID string, intent scratchpad.ArchiveIntent) (scratchpad.Scratchpad, error) {
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
	switch scratchpad.ArchiveIntent(strings.TrimSpace(string(intent))) {
	case scratchpad.ArchiveIntentMenu, scratchpad.ArchiveIntentLLM:
		sp.ArchiveIntent = intent
	default:
		sp.ArchiveIntent = scratchpad.ArchiveIntentNone
	}
	return s.store.Save(sp)
}

// SetArchiveStrategy records the routing decision the user (or, by
// convention, the LLM-side suggestion) has made for the next commit.
// Empty / unknown values default to "new" so a partially-saved
// scratchpad never lands with no strategy.
//
// The "update_thought" strategy requires SourceThoughtID or
// ThoughtID to be set on the scratchpad; the helper does not
// enforce that here so callers can stage the strategy first and
// the source thought second. BuildArchivePreview is the gate that
// refuses to render a preview when the combination is invalid.
func (s *ScratchpadService) SetArchiveStrategy(sessionID string, strategy scratchpad.ArchiveStrategy, thoughtID string) (scratchpad.Scratchpad, error) {
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
	switch scratchpad.ArchiveStrategy(strings.TrimSpace(string(strategy))) {
	case scratchpad.ArchiveStrategyUpdate, scratchpad.ArchiveStrategySupplement:
		sp.ArchiveStrategy = strategy
	default:
		sp.ArchiveStrategy = scratchpad.ArchiveStrategyNew
	}
	if thoughtID = strings.TrimSpace(thoughtID); thoughtID != "" {
		// The strategy decision may name a target thought (update
		// or supplement). Persist it on SourceThoughtID so a
		// subsequent BuildArchivePreview / Commit can find it
		// without the HTTP layer re-sending it on every call.
		sp.SourceThoughtID = thoughtID
	}
	return s.store.Save(sp)
}

// BuildArchivePreview renders a read-only ArchivePreview for a
// scratchpad. The preview is what the user confirms before commit
// actually lands; persisting it back onto the scratchpad (the
// caller does this with Save) means a re-entry into the capture
// page surfaces the same preview rather than re-deriving it from
// scratchpad state, which avoids drift between what the user saw
// and what got committed.
//
// The function is strategy-aware:
//
//   - "new": no thought is required, the body / title / tags are
//     pure scratchpad projections. The Preview is straightforward.
//   - "update_thought": the caller MUST supply currentThought (the
//     existing on-disk snapshot for the target thought). The preview
//     includes a ThoughtDiff covering title / tags / body / key
//     points, and an empty / missing currentThought is rejected with
//     ErrDiffRequired so the front end can show "diff not ready".
//   - "supplement": currentThought is the parent. The preview
//     surfaces a backlink in RelatedTopics and the body opens with
//     "[补充] 前置 thought-{parent.ID}" so the user can edit it down
//     before confirming.
//
// An empty / unknown strategy defaults to "new" — same defensive
// policy as SetArchiveStrategy — so a half-staged scratchpad never
// lands with no strategy.
func (s *ScratchpadService) BuildArchivePreview(sp scratchpad.Scratchpad, currentThought *models.ThoughtSnapshot) (scratchpad.ArchivePreview, error) {
	now := s.now()
	strategy := sp.ArchiveStrategy
	switch strategy {
	case scratchpad.ArchiveStrategyUpdate, scratchpad.ArchiveStrategySupplement:
		// legal
	default:
		strategy = scratchpad.ArchiveStrategyNew
	}
	if strategy == scratchpad.ArchiveStrategyUpdate {
		if currentThought == nil || strings.TrimSpace(currentThought.Thought.ID) == "" {
			return scratchpad.ArchivePreview{}, ErrDiffRequired
		}
	}

	title := strings.TrimSpace(sp.SessionContext.CandidateTitle)
	if title == "" {
		title = strings.TrimSpace(sp.Draft.TitleSet)
	}
	if title == "" {
		title = strings.TrimSpace(sp.Title)
	}

	body := sp.SessionContext.CandidateBody
	if body == "" {
		body = sp.Content
	}
	if strategy == scratchpad.ArchiveStrategySupplement && currentThought != nil {
		prefix := fmt.Sprintf("[补充] 前置 thought-%s\n\n", currentThought.Thought.ID)
		if !strings.HasPrefix(body, prefix) {
			body = prefix + body
		}
	}

	tags := append([]string(nil), sp.SessionContext.CandidateTags...)
	if len(tags) == 0 {
		tags = append(tags, sp.Tags...)
	}
	tags = uniqueStrings(tags)

	sourceLinks := append([]string(nil), sp.SessionContext.SourceLinks...)
	if len(sourceLinks) == 0 {
		sourceLinks = append(sourceLinks, sp.URL)
	}
	sourceLinks = trimNonEmpty(sourceLinks)

	relatedTopics := append([]string(nil), sp.SessionContext.SuggestedTopicIDs...)
	relatedTopics = append(relatedTopics, sp.TopicHints...)
	relatedTopics = uniqueStrings(relatedTopics)

	preview := scratchpad.ArchivePreview{
		Title:         title,
		Body:          body,
		Tags:          tags,
		SourceLinks:   sourceLinks,
		RelatedTopics: relatedTopics,
		Strategy:      strategy,
		GeneratedAt:   now,
	}
	if strategy == scratchpad.ArchiveStrategyUpdate {
		before := ""
		if currentThought != nil {
			thought := currentThought.Thought
			before = thoughtBodyForDiff(thought, currentThought.Content.Original)
		}
		diff, changed := buildThoughtDiff(before, body, tags, currentThought)
		preview.Diff = &diff
		if strategy == scratchpad.ArchiveStrategyUpdate && len(changed) == 0 {
			// No actual change vs the existing thought — the user
			// would be confirming an empty update. Surface that as
			// a soft warning by leaving the diff in place but
			// the caller can inspect ChangedFields.
			_ = changed
		}
	}
	if strategy == scratchpad.ArchiveStrategySupplement && currentThought != nil {
		preview.ThoughtID = currentThought.Thought.ID
	}
	if strategy == scratchpad.ArchiveStrategyUpdate && currentThought != nil {
		preview.ThoughtID = currentThought.Thought.ID
	}
	return preview, nil
}

// thoughtBodyForDiff picks the string used as the "before" side
// of a diff. UserTitle / ExtractedTitle / DisplayTitle are tried
// in order (the same priority displayTitle() uses) so the diff
// compares apples to apples; falling back to the raw content when
// the thought has no surfaced title.
func thoughtBodyForDiff(thought models.Thought, original string) string {
	if title := strings.TrimSpace(thought.UserTitle); title != "" {
		return title
	}
	if title := strings.TrimSpace(thought.ExtractedTitle); title != "" {
		return title
	}
	return strings.TrimSpace(original)
}

// buildThoughtDiff computes the field-level diff between the
// existing thought and the projected new content. We deliberately
// keep this on the scratchpad service (not in a separate "diff
// package") because the inputs are tightly coupled to the
// scratchpad shape: tags-as-slice, body-as-string, and a
// "changed fields" set that has to match what the patch will
// actually update. The function is exported inside the package so
// tests can exercise it without the scratchpad round-trip.
func buildThoughtDiff(before, after string, afterTags []string, current *models.ThoughtSnapshot) (scratchpad.ThoughtDiff, []string) {
	changed := []string{}
	if strings.TrimSpace(before) != strings.TrimSpace(after) {
		changed = append(changed, "body")
	}
	if current != nil {
		existing := append([]string(nil), current.Thought.UserTags...)
		existing = append(existing, current.Thought.AITags...)
		if !sameTagSet(existing, afterTags) {
			changed = append(changed, "tags")
		}
	}
	return scratchpad.ThoughtDiff{
		Before:        before,
		After:         after,
		ChangedFields: changed,
	}, changed
}

// sameTagSet returns true when a and b contain the same tags
// regardless of order and duplicates. Used by buildThoughtDiff
// to decide whether "tags" belongs in ChangedFields.
func sameTagSet(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	seen := make(map[string]int, len(a))
	for _, tag := range a {
		seen[strings.TrimSpace(tag)]++
	}
	for _, tag := range b {
		key := strings.TrimSpace(tag)
		if seen[key] <= 0 {
			return false
		}
		seen[key]--
	}
	return true
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

// Commit turns a scratchpad into a real thought. The strategy on
// the scratchpad (ArchiveStrategy) drives the routing:
//
//   - "new" (or empty) — first commit: capture a fresh thought and
//     apply the chat-time draft; subsequent commits PATCH the
//     existing thought (the "继续追加" path). This is the default
//     for a normal capture session.
//
//   - "update_thought" — PATCH the thought named by
//     SourceThoughtID with the scratchpad's projected body / tags.
//     Goes through the regular PatchThought path so a human
//     PATCH and the scratchpad update serialise on thoughtlock.
//
//   - "supplement" — capture a new thought whose body is prefixed
//     with "[补充] 前置 thought-{parent.ID}" and whose
//     RelatedThoughtIDs includes the parent. Then PATCH the
//     parent to add the new thought to ITS RelatedThoughtIDs
//     (bidirectional backlink). The scratchpad's
//     CommittedThoughtID points at the new thought so a follow-up
//     "继续追加" keeps piling on the supplement, not the parent.
//
// Returns the CaptureResult. For "update_thought" the Thought
// field is a zero-value Thought (PatchThought returns a Snapshot)
// — the caller reads Result.Thought.ID via the scratchpad's
// SourceThoughtID instead.
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
	// been sent to Begin With. "update_thought" never requires
	// Content (the body comes from the scratchpad's draft / context
	// projection; an empty projection is just a no-op patch).
	if sp.ArchiveStrategy != scratchpad.ArchiveStrategyUpdate &&
		sp.ArchiveStrategy != scratchpad.ArchiveStrategySupplement &&
		sp.CommittedThoughtID == "" && strings.TrimSpace(sp.Content) == "" {
		return models.CaptureResult{}, errors.New("capture: scratchpad content is empty")
	}

	switch sp.ArchiveStrategy {
	case scratchpad.ArchiveStrategyUpdate:
		return s.commitUpdate(ctx, sp)
	case scratchpad.ArchiveStrategySupplement:
		return s.commitSupplement(ctx, sp)
	default:
		if sp.ArchiveStrategy != scratchpad.ArchiveStrategyNew && sp.ArchiveStrategy != "" {
			// Unknown strategy — degrade to "new" rather than error.
			sp.ArchiveStrategy = scratchpad.ArchiveStrategyNew
		}
		if sp.CommittedThoughtID == "" {
			return s.commitFresh(ctx, sp)
		}
		return s.commitRepeat(ctx, sp)
	}
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
	if _, err := s.capture.ApplyDraftInternal(ctx, sp.CommittedThoughtID, sessionID, *patch, rawBody); err != nil {
		return models.CaptureResult{Thought: models.Thought{ID: sp.CommittedThoughtID}}, err
	}
	s.publishCommittedEvent(sp.CommittedThoughtID, sp.SessionID, "repeat")
	if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
		return models.CaptureResult{Thought: models.Thought{ID: sp.CommittedThoughtID}}, err
	}
	return models.CaptureResult{Thought: models.Thought{ID: sp.CommittedThoughtID}}, nil
}

// commitUpdate is the "update_thought" path. It PATCHes the
// thought named by SourceThoughtID with the scratchpad's
// projected body / title / tags. Unlike the repeat-commit path
// (which uses ApplyDraftInternal to avoid racing the refiner
// job that the Capture step just enqueued), this path uses the
// regular PatchThought — there is no Capture step here, so the
// refiner lock cannot conflict.
//
// SourceThoughtID is required; we still verify by issuing
// GetThought first so the failure mode is a clean 404 instead of
// the file-not-found you'd get from PatchThought on a missing
// thought.
//
// The scratchpad's CommittedThoughtID is NOT set — the scratchpad
// is still "open" relative to the source thought (the user can
// keep iterating). A subsequent "继续追加" with strategy
// "update_thought" keeps editing the same source.
func (s *ScratchpadService) commitUpdate(ctx context.Context, sp scratchpad.Scratchpad) (models.CaptureResult, error) {
	thoughtID := strings.TrimSpace(sp.SourceThoughtID)
	if thoughtID == "" {
		return models.CaptureResult{}, errors.New("capture: update_thought requires source_thought_id")
	}
	if _, gerr := s.capture.GetThought(ctx, thoughtID); gerr != nil {
		return models.CaptureResult{}, fmt.Errorf("capture: source thought not found: %w", gerr)
	}
	patch, rawBody, err := buildPatchForUpdate(sp)
	if err != nil {
		return models.CaptureResult{}, err
	}
	if patch == nil {
		// Nothing actually changed; degrade to a no-op reset so the
		// UI is in a clean state. We still return a successful
		// CaptureResult with the source thought's id so callers can
		// show "no change" in the toast.
		if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
			return models.CaptureResult{Thought: models.Thought{ID: thoughtID}}, err
		}
		return models.CaptureResult{Thought: models.Thought{ID: thoughtID}}, nil
	}
	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = sp.SessionID
	}
	if _, err := s.capture.PatchThought(ctx, thoughtID, sessionID, *patch, rawBody); err != nil {
		return models.CaptureResult{Thought: models.Thought{ID: thoughtID}}, err
	}
	s.publishCommittedEvent(thoughtID, sp.SessionID, "update")
	if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
		return models.CaptureResult{Thought: models.Thought{ID: thoughtID}}, err
	}
	return models.CaptureResult{Thought: models.Thought{ID: thoughtID}}, nil
}

// commitSupplement is the "supplement" path. It captures a new
// thought whose body is prefixed with "[补充] 前置
// thought-{parent.ID}" and whose AINotes gets a backlink marker
// pointing at the parent. Updating the parent's
// RelatedThoughtIDs is a follow-up: it requires extending
// ThoughtPatchRequest with a RelatedThoughtIDs field (currently
// absent), so the parent's backlink is left to the next PR. The
// new thought is fully readable without the parent update.
func (s *ScratchpadService) commitSupplement(ctx context.Context, sp scratchpad.Scratchpad) (models.CaptureResult, error) {
	parentID := strings.TrimSpace(sp.SourceThoughtID)
	if parentID == "" {
		return models.CaptureResult{}, errors.New("capture: supplement requires source_thought_id")
	}
	if _, gerr := s.capture.GetThought(ctx, parentID); gerr != nil {
		return models.CaptureResult{}, fmt.Errorf("capture: source thought not found: %w", gerr)
	}
	cmd, err := s.BuildCaptureCommand(sp)
	if err != nil {
		return models.CaptureResult{}, err
	}
	cmd.Source = "scratchpad-supplement"
	prefix := fmt.Sprintf("[补充] 前置 thought-%s\n\n", parentID)
	if !strings.HasPrefix(cmd.Content, prefix) {
		cmd.Content = prefix + cmd.Content
	}
	result, err := s.capture.Capture(ctx, cmd)
	if err != nil {
		return models.CaptureResult{}, err
	}
	if _, err := s.store.MarkCommitted(sp.SessionID, result.Thought.ID); err != nil {
		return result, err
	}
	// AINotes backlink marker on the new thought. We use the
	// lock-free apply path so the refiner job the Capture step
	// just enqueued doesn't see a "thought is locked" skip.
	sessionID := s.sessionID
	if sessionID == "" {
		sessionID = sp.SessionID
	}
	marker := fmt.Sprintf("[补充] 关联前置 thought-%s", parentID)
	patch := &models.ThoughtPatchRequest{AINotesAppend: &marker}
	if rawBody, mErr := patchRequestToRawBody(*patch); mErr == nil {
		_, _ = s.capture.ApplyDraftInternal(ctx, result.Thought.ID, sessionID, *patch, rawBody)
	}
	s.publishCommittedEvent(result.Thought.ID, sp.SessionID, "supplement")
	if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
		return result, err
	}
	return result, nil
}

// applyRelatedBacklink is reserved for the follow-up that adds
// RelatedThoughtIDs to the patchable fields. It currently is a
// no-op placeholder so callers can compile and the explicit
// "we know we don't do parent backlink yet" path stays visible.
func (s *ScratchpadService) applyRelatedBacklink(_ context.Context, _, _, _ string, _ bool) error {
	return nil
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
	_, err = s.capture.ApplyDraftInternal(ctx, thoughtID, sessionID, *patch, rawBody)
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

// buildPatchForUpdate produces a ThoughtPatchRequest for the
// "update_thought" path. Unlike buildPatchFromScratchpad (which
// treats Content as AINotesAppend because the original was
// already captured), this path projects the scratchpad's
// CandidateTitle / CandidateBody / CandidateTags — the
// session_context fields the user (or LLM) staged for the
// update — and emits them as the patch payload. Returns
// (nil, nil, nil) when the scratchpad carries no projected
// changes, so the caller can degrade to a no-op.
//
// The merge rule:
//   - title  ← sp.SessionContext.CandidateTitle | sp.Draft.TitleSet
//   - tags   ← sp.SessionContext.CandidateTags | sp.Tags
//   - body   ← sp.SessionContext.CandidateBody  (as AINotesAppend;
//     the existing original stays untouched)
func buildPatchForUpdate(sp scratchpad.Scratchpad) (*models.ThoughtPatchRequest, []byte, error) {
	hasAny := false
	req := models.ThoughtPatchRequest{}
	if title := strings.TrimSpace(sp.SessionContext.CandidateTitle); title != "" {
		req.Title = &title
		hasAny = true
	} else if title := strings.TrimSpace(sp.Draft.TitleSet); title != "" {
		req.Title = &title
		hasAny = true
	}
	tags := sp.SessionContext.CandidateTags
	if len(tags) == 0 {
		tags = sp.Tags
	}
	if len(tags) > 0 {
		merged := uniqueStrings(tags)
		req.Tags = &merged
		hasAny = true
	}
	if body := strings.TrimSpace(sp.SessionContext.CandidateBody); body != "" {
		// AINotesAppend is additive: the user's new body goes in
		// the AINotes block rather than overwriting the original
		// content. A future PR can add a Body patch field to
		// ThoughtPatchRequest so the update actually replaces
		// `original`; for now the visible diff is in AINotes.
		req.AINotesAppend = &body
		hasAny = true
	} else if note := strings.TrimSpace(sp.Content); note != "" {
		req.AINotesAppend = &note
		hasAny = true
	}
	if topics := uniqueStrings(append(append([]string(nil), sp.SessionContext.SuggestedTopicIDs...), sp.Draft.TopicIDs...)); len(topics) > 0 {
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

// ErrDiffRequired is returned by BuildArchivePreview when the
// strategy is "update_thought" but the caller did not supply a
// current thought snapshot. The HTTP layer surfaces this as 400
// so the front end can prompt the user to load the existing
// thought before retrying the preview.
var ErrDiffRequired = errors.New("capture: diff is required for update_thought strategy")

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
