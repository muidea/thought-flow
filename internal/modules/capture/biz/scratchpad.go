package biz

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentprofile"
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
	archiveScheduler   func(scratchpad.Scratchpad) error
	store              ScratchpadStore
	capture            CaptureCommitter
	eventHub           eventHub
	contextAI          ai.CaptureContextProvider
	contextTimeout     time.Duration
	contextRetryDelays []time.Duration
	profiles           ProfileRegistry
	documentGenerator  ai.DocumentGenerationProvider
	maxMatchCandidates int
	maxRepairAttempts  int
	sessionID          string
	now                func() time.Time
}

const defaultCaptureContextEnrichTimeout = 2 * time.Minute

var defaultCaptureContextRetryDelays = []time.Duration{2 * time.Second, 5 * time.Second, 10 * time.Second}

const captureContextEnricherObserverID = "capture.context-enricher"

type captureContextEnrichRequest struct {
	InputGeneration   uint64
	SessionID         string
	ContentAtSchedule string
	ExistingContext   scratchpad.SessionContext
}

// ScratchpadStore is the subset of scratchpad.Store this package
// depends on. The HTTP layer uses the same interface, which keeps
// the test seam uniform.
type ScratchpadStore interface {
	Get(sessionID string) (scratchpad.Scratchpad, error)
	Save(sp scratchpad.Scratchpad) (scratchpad.Scratchpad, error)
	Update(string, func(*scratchpad.Scratchpad) error) (scratchpad.Scratchpad, error)
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

type ProfileRegistry interface {
	ListEnabled() []documentprofile.DocumentProfileDescriptor
	Resolve(ref models.DocumentProfileRef) (documentprofile.DocumentProfile, error)
	ResolveLatest(profileID string) (documentprofile.DocumentProfile, error)
	Default() documentprofile.DocumentProfile
}

// eventHub is the publish/subscribe surface the scratchpad commit
// and context-enrichment flows use. Context enrichment is queued on
// a per-session EventHub lane so multi-turn capture updates are
// processed in a deterministic order.
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

// WithEventHub wires in the event hub so Commit can fire domain events
// and capture context enrichment can run through per-session lanes.
func WithEventHub(h eventHub) ScratchpadServiceOption {
	return func(s *ScratchpadService) { s.eventHub = h }
}

func WithCaptureContextProvider(provider ai.CaptureContextProvider) ScratchpadServiceOption {
	return func(s *ScratchpadService) { s.contextAI = provider }
}

func WithDocumentProfiles(registry ProfileRegistry, generator ai.DocumentGenerationProvider, maxMatchCandidates, maxRepairAttempts int) ScratchpadServiceOption {
	return func(s *ScratchpadService) {
		s.profiles = registry
		s.documentGenerator = generator
		if maxMatchCandidates > 0 {
			s.maxMatchCandidates = maxMatchCandidates
		}
		if maxRepairAttempts >= 0 {
			s.maxRepairAttempts = maxRepairAttempts
		}
	}
}

func WithCaptureContextTimeout(timeout time.Duration) ScratchpadServiceOption {
	return func(s *ScratchpadService) {
		if timeout > 0 {
			s.contextTimeout = timeout
		}
	}
}

func WithCaptureContextRetryDelays(delays ...time.Duration) ScratchpadServiceOption {
	return func(s *ScratchpadService) {
		s.contextRetryDelays = append([]time.Duration(nil), delays...)
	}
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
		store:              store,
		contextTimeout:     defaultCaptureContextEnrichTimeout,
		contextRetryDelays: append([]time.Duration(nil), defaultCaptureContextRetryDelays...),
		sessionID:          "scratchpad",
		maxRepairAttempts:  2,
		now:                func() time.Time { return time.Now().UTC() },
	}
	for _, opt := range options {
		opt(s)
	}
	s.subscribeContextEnricher()
	return s
}

// ErrScratchpadUnavailable is returned when the scratchpad store has
// not been wired up. The HTTP layer surfaces it as 503.
var ErrScratchpadUnavailable = errors.New("capture: scratchpad store is not ready")
var ErrArchivePreviewRequired = errors.New("capture: validated archive preview is required")
var ErrArchivePreviewStale = errors.New("capture: archive preview is stale")
var ErrArchiveFormatInvalid = errors.New("capture: archive format validation failed")

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
	var existingContext scratchpad.SessionContext
	saved, err := s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
		existingContext = enrichmentSeedContext(*sp)
		sp.Messages = append(sp.Messages, scratchpad.Message{Role: role, Text: text, At: s.now()})
		if role == "user" {
			if sp.Content != "" {
				sp.Content += "\n\n"
			}
			sp.Content += text
			// Keep authoritative decisions while the new answer is pending.
			old := sp.SessionContext
			sp.SessionContext = scratchpad.SessionContext{DocumentParameters: old.DocumentParameters, ParameterValues: old.ParameterValues, SourceLinks: old.SourceLinks, SourceLinksConfirmed: old.SourceLinksConfirmed, Issues: old.Issues, FrozenProfile: old.FrozenProfile, ProfileExplicit: old.ProfileExplicit, CandidateProfileID: old.CandidateProfileID, CandidateProfileVersion: old.CandidateProfileVersion, CandidateDocumentFamily: old.CandidateDocumentFamily}
			sp.ArchiveIntent = scratchpad.ArchiveIntentNone
			sp.ArchivePreview = nil
		}
		return nil
	})
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	if role == "user" {
		s.requestSessionContextEnrichment(saved, existingContext)
	}
	return saved, nil
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
	return s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
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
		return nil
	})
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
	return s.updateSessionContext(sessionID, ctx, false)
}

func (s *ScratchpadService) updateSessionContext(sessionID string, ctx scratchpad.SessionContext, appendReply bool) (scratchpad.Scratchpad, error) {
	return s.updateSessionContextAt(sessionID, ctx, appendReply, nil)
}
func (s *ScratchpadService) updateSessionContextAt(sessionID string, ctx scratchpad.SessionContext, appendReply bool, expected *uint64) (scratchpad.Scratchpad, error) {
	if s == nil || s.store == nil {
		return scratchpad.Scratchpad{}, ErrScratchpadUnavailable
	}
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return scratchpad.Scratchpad{}, errors.New("capture: scratchpad session id is required")
	}
	saved, err := s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
		if expected != nil && sp.InputGeneration != *expected {
			return ErrArchivePreviewStale
		}
		sp.SessionContext = scratchpad.SessionContext{
			FrozenProfile:           ctx.FrozenProfile,
			ParameterValues:         ctx.ParameterValues,
			Issues:                  ctx.Issues,
			SourceLinksConfirmed:    ctx.SourceLinksConfirmed,
			AssistantReply:          strings.TrimSpace(ctx.AssistantReply),
			Topic:                   strings.TrimSpace(ctx.Topic),
			Goal:                    strings.TrimSpace(ctx.Goal),
			ConfirmedFacts:          trimNonEmpty(ctx.ConfirmedFacts),
			OpenQuestions:           trimNonEmpty(ctx.OpenQuestions),
			Conflicts:               trimNonEmpty(ctx.Conflicts),
			CandidateTitle:          strings.TrimSpace(ctx.CandidateTitle),
			CandidateTags:           trimNonEmpty(ctx.CandidateTags),
			CandidateSummary:        strings.TrimSpace(ctx.CandidateSummary),
			CandidateBody:           ctx.CandidateBody,
			SourceLinks:             trimNonEmpty(ctx.SourceLinks),
			RelatedThoughtIDs:       trimNonEmpty(ctx.RelatedThoughtIDs),
			SuggestedTopicIDs:       trimNonEmpty(ctx.SuggestedTopicIDs),
			ArchiveIntent:           normalizeArchiveIntent(ctx.ArchiveIntent),
			ArchiveStrategy:         normalizeArchiveStrategy(ctx.ArchiveStrategy),
			CandidateDocumentFamily: strings.TrimSpace(ctx.CandidateDocumentFamily),
			CandidateProfileID:      strings.TrimSpace(ctx.CandidateProfileID),
			CandidateProfileVersion: ctx.CandidateProfileVersion,
			ProfileConfidence:       ctx.ProfileConfidence,
			ProfileMatchReason:      strings.TrimSpace(ctx.ProfileMatchReason),
			ProfileExplicit:         ctx.ProfileExplicit,
			DocumentParameters:      cloneStringMap(ctx.DocumentParameters),
			MissingProfileInputs:    trimNonEmpty(ctx.MissingProfileInputs),
			ArchiveReadiness:        normalizeOptionalArchiveReadiness(ctx.ArchiveReadiness),
		}
		if sp.SessionContext.ArchiveIntent == scratchpad.ArchiveIntentNone {
			sp.SessionContext.ArchiveIntent = normalizeArchiveIntent(sp.ArchiveIntent)
		}
		if sp.SessionContext.ArchiveStrategy == scratchpad.ArchiveStrategyNew {
			sp.SessionContext.ArchiveStrategy = normalizeArchiveStrategy(sp.ArchiveStrategy)
		}
		sp.ArchiveIntent = sp.SessionContext.ArchiveIntent
		sp.ArchiveStrategy = sp.SessionContext.ArchiveStrategy
		if appendReply {
			if reply := sessionContextReplyText(sp.SessionContext); reply != "" {
				sp.Messages = appendContextReplyMessage(sp.Messages, reply, s.now())
			}
		}
		s.normalizeProfileContext(&sp.SessionContext)
		sp.ArchivePreview = nil
		return nil
	})
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	s.publishContextUpdatedEvent(sessionID)
	if appendReply && saved.ArchiveIntent == scratchpad.ArchiveIntentLLM && s.archiveScheduler != nil {
		pending, e := s.BeginPreview(sessionID, true)
		if e == nil {
			e = s.archiveScheduler(pending)
		}
		if e != nil && pending.Operation != nil {
			s.FailOperation(sessionID, pending.Operation.ID, e)
		}
	}
	return saved, nil
}

func (s *ScratchpadService) ID() string {
	return captureContextEnricherObserverID
}

func (s *ScratchpadService) Notify(ev event.Event, result event.Result) {
	if ev == nil {
		if result != nil {
			result.Set(nil, nil)
		}
		return
	}
	if ev.ID() != models.EventScratchpadContextEnrichRequested {
		if result != nil {
			result.Set(nil, nil)
		}
		return
	}
	req, ok := ev.Data().(captureContextEnrichRequest)
	if !ok {
		slog.Warn("capture context enrichment event has invalid payload", "event_id", ev.ID())
		if result != nil {
			result.Set(nil, nil)
		}
		return
	}
	s.handleCaptureContextEnrichRequest(req)
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *ScratchpadService) subscribeContextEnricher() {
	if s == nil || s.eventHub == nil || s.contextAI == nil {
		return
	}
	s.eventHub.Subscribe(models.EventScratchpadContextEnrichRequested, s)
}

func (s *ScratchpadService) requestSessionContextEnrichment(sp scratchpad.Scratchpad, existingContext scratchpad.SessionContext) {
	if s == nil || s.contextAI == nil || s.store == nil {
		return
	}
	sessionID := strings.TrimSpace(sp.SessionID)
	contentAtSchedule := sp.Content
	if sessionID == "" || strings.TrimSpace(contentAtSchedule) == "" {
		return
	}
	if s.eventHub == nil {
		s.markContextEnrichmentFailed(sessionID, contentAtSchedule, errors.New("capture: event hub is not configured"))
		return
	}
	ev := event.NewEvent(
		models.EventScratchpadContextEnrichRequested,
		"capture",
		captureContextEnricherObserverID,
		event.NewHeader(),
		captureContextEnrichRequest{
			InputGeneration:   sp.InputGeneration,
			SessionID:         sessionID,
			ContentAtSchedule: contentAtSchedule,
			ExistingContext:   existingContext,
		},
	)
	ev.BindLaneKey(captureContextLaneKey(sessionID))
	s.eventHub.Post(ev)
}

func enrichmentSeedContext(sp scratchpad.Scratchpad) scratchpad.SessionContext {
	ctx := sp.SessionContext
	if sp.ArchivePreview == nil {
		return ctx
	}
	preview := *sp.ArchivePreview
	previewBody := strings.TrimSpace(preview.Body)
	relatedTopics := trimNonEmpty(preview.RelatedTopics)
	ctx.CandidateTitle = firstNonEmptyString(preview.Title, ctx.CandidateTitle)
	ctx.CandidateTags = mergeContextStrings(preview.Tags, ctx.CandidateTags)
	ctx.CandidateSummary = seedArchivedBody(ctx.CandidateSummary, previewBody)
	ctx.CandidateBody = seedArchivedBody(ctx.CandidateBody, previewBody)
	ctx.SourceLinks = mergeContextStrings(preview.SourceLinks, ctx.SourceLinks)
	ctx.SuggestedTopicIDs = mergeContextStrings(relatedTopics, ctx.SuggestedTopicIDs)
	ctx.ArchiveIntent = scratchpad.ArchiveIntentNone
	strategy := firstNonEmptyString(string(ctx.ArchiveStrategy), string(preview.Strategy), string(sp.ArchiveStrategy))
	ctx.ArchiveStrategy = normalizeArchiveStrategy(scratchpad.ArchiveStrategy(strategy))
	return ctx
}

func seedArchivedBody(existing, archived string) string {
	existing = strings.TrimSpace(existing)
	archived = strings.TrimSpace(archived)
	if archived == "" {
		return existing
	}
	if existing == "" {
		return archived
	}
	if strings.Contains(compactText(existing), compactText(archived)) {
		return existing
	}
	return archived + "\n\n" + existing
}

func captureContextLaneKey(sessionID string) string {
	return "capture.context." + strings.TrimSpace(sessionID)
}

func (s *ScratchpadService) handleCaptureContextEnrichRequest(req captureContextEnrichRequest) {
	sessionID := strings.TrimSpace(req.SessionID)
	contentAtSchedule := req.ContentAtSchedule
	if sessionID == "" || strings.TrimSpace(contentAtSchedule) == "" {
		return
	}
	current, err := s.store.Get(sessionID)
	if err != nil || current.Content != contentAtSchedule || (req.InputGeneration != 0 && current.InputGeneration != req.InputGeneration) {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.contextTimeout)
	defer cancel()
	request := ai.CaptureContextRequest{
		SessionID:         sessionID,
		Content:           current.Content,
		Messages:          captureContextMessages(current.Messages),
		Existing:          captureContextFromScratchpad(req.ExistingContext),
		AvailableProfiles: s.availableProfileDescriptors(current),
		ExistingProfile:   s.existingProfile(current),
	}
	result, err := s.buildCaptureContextWithRetry(ctx, sessionID, contentAtSchedule, request)
	if err != nil {
		s.markContextEnrichmentFailed(sessionID, contentAtSchedule, err, current.InputGeneration)
		return
	}
	if result.ProfileContractHash != "" && s.profiles != nil {
		p, e := s.profiles.Resolve(models.DocumentProfileRef{ProfileID: result.CandidateProfileID, Version: result.CandidateProfileVersion})
		if e != nil || p.Ref.ContentHash != result.ProfileContractHash {
			s.markContextEnrichmentFailed(sessionID, contentAtSchedule, ErrArchivePreviewStale, current.InputGeneration)
			return
		}
	}
	latest, err := s.store.Get(sessionID)
	if err != nil || latest.Content != contentAtSchedule || latest.InputGeneration != current.InputGeneration {
		return
	}
	contextBase := latest
	contextBase.SessionContext = req.ExistingContext
	result = preserveLatestUserTurns(result, latest.Messages)
	_, _ = s.updateSessionContextAt(sessionID, captureContextToScratchpad(result, contextBase), true, &latest.InputGeneration)
}

func (s *ScratchpadService) buildCaptureContextWithRetry(ctx context.Context, sessionID, contentAtSchedule string, req ai.CaptureContextRequest) (ai.CaptureContextResult, error) {
	var lastErr error
	maxAttempts := len(s.contextRetryDelays) + 1
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			delay := s.contextRetryDelays[attempt-1]
			slog.Info("capture context enrichment retrying", "session_id", sessionID, "attempt", attempt+1, "max_attempts", maxAttempts, "delay", delay, "error", lastErr)
			if err := waitCaptureContextRetryDelay(ctx, delay); err != nil {
				return ai.CaptureContextResult{}, err
			}
			latest, err := s.store.Get(sessionID)
			if err != nil || latest.Content != contentAtSchedule {
				return ai.CaptureContextResult{}, context.Canceled
			}
		}
		result, err := s.contextAI.BuildCaptureContext(ctx, req)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if !isRetryableCaptureContextError(err) || attempt == maxAttempts-1 {
			return ai.CaptureContextResult{}, err
		}
	}
	return ai.CaptureContextResult{}, lastErr
}

func waitCaptureContextRetryDelay(ctx context.Context, delay time.Duration) error {
	if delay <= 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			return nil
		}
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableCaptureContextError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	var providerErr ai.ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Retryable
	}
	return strings.Contains(err.Error(), "parse capture context json")
}

func (s *ScratchpadService) markContextEnrichmentFailed(sessionID, contentAtSchedule string, cause error, expected ...uint64) {
	slog.Warn("capture context enrichment failed", "session_id", sessionID, "error", cause)
	latest, err := s.store.Get(sessionID)
	if err != nil || latest.Content != contentAtSchedule || (len(expected) > 0 && latest.InputGeneration != expected[0]) {
		return
	}
	text := captureContextFailureText(cause)
	if text == "" {
		return
	}
	_, err = s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
		if sp.InputGeneration != latest.InputGeneration {
			return ErrArchivePreviewStale
		}
		sp.Messages = appendContextReplyMessage(sp.Messages, text, s.now())
		return nil
	})
	if err != nil {
		slog.Warn("persist capture context enrichment failure failed", "session_id", sessionID, "error", err)
		return
	}

	s.publishContextUpdatedEvent(sessionID)
}

func captureContextFailureText(cause error) string {
	if cause == nil {
		return ""
	}
	if errors.Is(cause, context.DeadlineExceeded) {
		return "LLM 整理超时，请检查模型服务响应时间后重试。"
	}
	if errors.Is(cause, context.Canceled) {
		return "LLM 整理已取消，请重新发送本轮内容。"
	}
	var providerErr ai.ProviderError
	if errors.As(cause, &providerErr) {
		if providerErr.Code == "thoughtflow.ai.invalid_json_fallback" || providerErr.Code == "thoughtflow.ai.json_fallback_failed" {
			return "LLM 兼容请求失败：未能获得通过校验的 JSON 结果。请检查模型及 Provider 的 JSON 输出能力；本次兼容请求不会自动重试。"
		}
		if providerErr.Code == "thoughtflow.ai.conversion_unsupported" {
			features := mergeContextStrings([]string{providerErr.Feature}, providerErr.UnsupportedFeatures)
			detail := ""
			if len(features) > 0 {
				detail = "（" + strings.Join(features, "、") + "）"
			}
			return "LLM 请求参数不受当前模型或 Provider 支持" + detail + "。当前流程依赖 JSON 输出，请使用支持所需能力的模型及 Provider；本次参数错误不会自动重试。"
		}
		switch providerErr.StatusCode {
		case 401, 403:
			return "LLM 整理失败：模型服务鉴权失败，请检查 API Key 和模型服务配置。"
		case 404:
			return "LLM 整理失败：模型服务接口不存在，请检查 base_url 是否指向 OpenAI-compatible 接口。"
		case 429:
			return "LLM 整理失败：模型服务限流，请稍后重试。"
		default:
			if providerErr.StatusCode >= 500 {
				return "LLM 整理失败：模型服务暂时不可用，请稍后重试。"
			}
			return "LLM 整理失败：模型服务返回异常，请检查模型配置后重试。"
		}
	}
	message := strings.TrimSpace(cause.Error())
	if strings.Contains(message, "parse capture context json") {
		return "LLM 整理失败：模型返回格式无法解析，请重试或调整提示词模板。"
	}
	return "LLM 整理失败，请检查模型服务配置或稍后重试。"
}

func captureContextMessages(messages []scratchpad.Message) []ai.CaptureContextMessage {
	out := make([]ai.CaptureContextMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, ai.CaptureContextMessage{Role: msg.Role, Text: msg.Text})
	}
	return out
}

func captureContextFromScratchpad(ctx scratchpad.SessionContext) ai.CaptureContextResult {
	return ai.CaptureContextResult{
		AssistantReply:          ctx.AssistantReply,
		Topic:                   ctx.Topic,
		Goal:                    ctx.Goal,
		ConfirmedFacts:          append([]string(nil), ctx.ConfirmedFacts...),
		OpenQuestions:           append([]string(nil), ctx.OpenQuestions...),
		Conflicts:               append([]string(nil), ctx.Conflicts...),
		CandidateTitle:          ctx.CandidateTitle,
		CandidateTags:           append([]string(nil), ctx.CandidateTags...),
		CandidateSummary:        ctx.CandidateSummary,
		CandidateBody:           ctx.CandidateBody,
		SourceLinks:             append([]string(nil), ctx.SourceLinks...),
		RelatedThoughtIDs:       append([]string(nil), ctx.RelatedThoughtIDs...),
		SuggestedTopicIDs:       append([]string(nil), ctx.SuggestedTopicIDs...),
		ArchiveIntent:           string(ctx.ArchiveIntent),
		ArchiveStrategy:         string(ctx.ArchiveStrategy),
		CandidateDocumentFamily: ctx.CandidateDocumentFamily,
		CandidateProfileID:      ctx.CandidateProfileID,
		CandidateProfileVersion: ctx.CandidateProfileVersion,
		ProfileConfidence:       ctx.ProfileConfidence,
		ProfileMatchReason:      ctx.ProfileMatchReason,
		ProfileExplicit:         ctx.ProfileExplicit,
		DocumentParameters:      cloneStringMap(ctx.DocumentParameters),
		MissingProfileInputs:    append([]string(nil), ctx.MissingProfileInputs...),
		ArchiveReadiness:        ctx.ArchiveReadiness,
	}
}

func hasSessionContext(ctx scratchpad.SessionContext) bool {
	return strings.TrimSpace(ctx.AssistantReply) != "" || strings.TrimSpace(ctx.Topic) != "" ||
		strings.TrimSpace(ctx.Goal) != "" ||
		strings.TrimSpace(ctx.CandidateTitle) != "" ||
		strings.TrimSpace(ctx.CandidateSummary) != "" ||
		strings.TrimSpace(ctx.CandidateBody) != "" ||
		len(ctx.ConfirmedFacts) > 0 ||
		len(ctx.OpenQuestions) > 0 ||
		len(ctx.Conflicts) > 0 ||
		len(ctx.CandidateTags) > 0 ||
		len(ctx.SourceLinks) > 0 ||
		len(ctx.RelatedThoughtIDs) > 0 ||
		len(ctx.SuggestedTopicIDs) > 0 ||
		strings.TrimSpace(ctx.CandidateProfileID) != ""
}

func sessionContextReplyText(ctx scratchpad.SessionContext) string {
	if reply := strings.TrimSpace(ctx.AssistantReply); reply != "" {
		return reply
	}
	summary := strings.TrimSpace(ctx.CandidateSummary)
	body := strings.TrimSpace(ctx.CandidateBody)
	if summary == "" {
		return body
	}
	if body == "" {
		return summary
	}
	summaryKey := compactText(summary)
	bodyKey := compactText(body)
	if summaryKey == bodyKey || strings.Contains(summaryKey, bodyKey) {
		return summary
	}
	if strings.Contains(bodyKey, summaryKey) {
		return summary
	}
	if captureReplyNeedsBody(summary, body) {
		return summary + "\n\n" + body
	}
	return summary
}

func captureReplyNeedsBody(summary, body string) bool {
	_ = body
	for _, marker := range []string{"下面是", "具体如下", "整理如下", "结构如下", "内容如下"} {
		if strings.Contains(summary, marker) {
			return true
		}
	}
	return false
}

func appendContextReplyMessage(messages []scratchpad.Message, reply string, at time.Time) []scratchpad.Message {
	reply = strings.TrimSpace(reply)
	if reply == "" {
		return messages
	}
	replyKey := compactText(reply)
	for idx := len(messages) - 1; idx >= 0; idx-- {
		msg := messages[idx]
		if strings.TrimSpace(msg.Role) != "ai" {
			continue
		}
		if compactText(msg.Text) == replyKey {
			return messages
		}
	}
	return append(messages, scratchpad.Message{Role: "ai", Text: reply, At: at})
}

func captureContextToScratchpad(result ai.CaptureContextResult, current scratchpad.Scratchpad) scratchpad.SessionContext {
	if !current.SessionContext.ProfileExplicit && result.CandidateProfileID != "" && (result.CandidateProfileID != current.SessionContext.CandidateProfileID || result.CandidateProfileVersion != current.SessionContext.CandidateProfileVersion) {
		current.SessionContext.FrozenProfile = ""
		current.SessionContext.DocumentParameters = nil
		current.SessionContext.ParameterValues = nil
	}
	if current.SessionContext.ProfileExplicit {
		result.CandidateProfileID = current.SessionContext.CandidateProfileID
		result.CandidateProfileVersion = current.SessionContext.CandidateProfileVersion
		result.CandidateDocumentFamily = current.SessionContext.CandidateDocumentFamily
	}
	return scratchpad.SessionContext{
		// A reply belongs only to this turn; never reuse an older reply.
		AssistantReply:          strings.TrimSpace(result.AssistantReply),
		Topic:                   firstNonEmptyString(result.Topic, current.SessionContext.Topic),
		Goal:                    firstNonEmptyString(result.Goal, current.SessionContext.Goal),
		ConfirmedFacts:          mergeContextStrings(current.SessionContext.ConfirmedFacts, result.ConfirmedFacts),
		OpenQuestions:           trimNonEmpty(result.OpenQuestions),
		Issues:                  mergeIssues(current.SessionContext.Issues, result.Issues, result.Conflicts),
		ParameterValues:         current.SessionContext.ParameterValues,
		Conflicts:               activeConflicts(mergeIssues(mergeIssues(current.SessionContext.Issues, nil, current.SessionContext.Conflicts), result.Issues, result.Conflicts)),
		CandidateTitle:          firstNonEmptyString(result.CandidateTitle, current.SessionContext.CandidateTitle),
		CandidateTags:           mergeContextStrings(current.SessionContext.CandidateTags, result.CandidateTags),
		CandidateSummary:        firstNonEmptyString(result.CandidateSummary, current.SessionContext.CandidateSummary),
		CandidateBody:           firstNonEmptyString(result.CandidateBody, current.SessionContext.CandidateBody),
		SourceLinks:             contextSourceLinks(current.SessionContext, result.SourceLinks),
		SourceLinksConfirmed:    current.SessionContext.SourceLinksConfirmed,
		RelatedThoughtIDs:       mergeContextStrings(current.SessionContext.RelatedThoughtIDs, result.RelatedThoughtIDs),
		SuggestedTopicIDs:       mergeContextStrings(current.SessionContext.SuggestedTopicIDs, result.SuggestedTopicIDs),
		ArchiveIntent:           normalizeArchiveIntent(scratchpad.ArchiveIntent(firstNonEmptyString(result.ArchiveIntent, string(current.ArchiveIntent)))),
		ArchiveStrategy:         normalizeArchiveStrategy(scratchpad.ArchiveStrategy(firstNonEmptyString(result.ArchiveStrategy, string(current.ArchiveStrategy)))),
		CandidateDocumentFamily: firstNonEmptyString(result.CandidateDocumentFamily, current.SessionContext.CandidateDocumentFamily),
		CandidateProfileID:      firstNonEmptyString(result.CandidateProfileID, current.SessionContext.CandidateProfileID),
		CandidateProfileVersion: firstNonZeroInt(result.CandidateProfileVersion, current.SessionContext.CandidateProfileVersion),
		ProfileConfidence:       result.ProfileConfidence,
		ProfileMatchReason:      firstNonEmptyString(result.ProfileMatchReason, current.SessionContext.ProfileMatchReason),
		ProfileExplicit:         result.ProfileExplicit || current.SessionContext.ProfileExplicit,
		DocumentParameters:      reconcileParameters(current.SessionContext, result.DocumentParameters),
		FrozenProfile:           current.SessionContext.FrozenProfile,
		MissingProfileInputs:    trimNonEmpty(result.MissingProfileInputs),
		ArchiveReadiness:        normalizeArchiveReadiness(firstNonEmptyString(result.ArchiveReadiness, current.SessionContext.ArchiveReadiness)),
	}
}

func preserveLatestUserTurns(result ai.CaptureContextResult, messages []scratchpad.Message) ai.CaptureContextResult {
	for _, turn := range latestUserTurns(messages) {
		if lowSignalCaptureTurn(turn) || captureTurnCovered(result, turn) {
			continue
		}
		if likelyConflictingCaptureTurn(turn) {
			conflict := "最新提交可能与既有整理存在冲突或变更，需要确认：" + turn
			result.Conflicts = mergeContextStrings(result.Conflicts, []string{conflict})
			result.CandidateSummary = appendCaptureContextSection(result.CandidateSummary, "需确认的冲突/变更", []string{conflict})
			result.CandidateBody = appendCaptureContextSection(result.CandidateBody, "需确认的冲突/变更", []string{conflict})
			continue
		}
		fact := "用户补充：" + turn
		result.ConfirmedFacts = mergeContextStrings(result.ConfirmedFacts, []string{fact})
		result.CandidateSummary = appendCaptureContextSection(result.CandidateSummary, "待整合信息", []string{fact})
		result.CandidateBody = appendCaptureContextSection(result.CandidateBody, "待整合信息", []string{fact})
	}
	return result
}

func latestUserTurns(messages []scratchpad.Message) []string {
	start := 0
	for idx := len(messages) - 1; idx >= 0; idx-- {
		if strings.TrimSpace(messages[idx].Role) == "ai" {
			start = idx + 1
			break
		}
	}
	out := []string{}
	for _, msg := range messages[start:] {
		if strings.TrimSpace(msg.Role) != "user" {
			continue
		}
		text := strings.TrimSpace(msg.Text)
		if text != "" {
			out = append(out, text)
		}
	}
	return out
}

func captureTurnCovered(result ai.CaptureContextResult, turn string) bool {
	available := strings.Join([]string{
		result.Topic,
		result.Goal,
		strings.Join(result.ConfirmedFacts, "\n"),
		strings.Join(result.OpenQuestions, "\n"),
		strings.Join(result.Conflicts, "\n"),
		result.CandidateTitle,
		strings.Join(result.CandidateTags, "\n"),
		result.CandidateSummary,
		result.CandidateBody,
	}, "\n")
	available = normalizeCaptureCoverageText(available)
	turnText := normalizeCaptureCoverageText(turn)
	if turnText == "" || strings.Contains(available, turnText) {
		return true
	}
	anchors := captureCoverageAnchors(turnText)
	if len(anchors) == 0 {
		return true
	}
	hits := 0
	for _, anchor := range anchors {
		if strings.Contains(available, anchor) {
			hits++
		}
	}
	required := (len(anchors) + 3) / 4
	if required < 1 {
		required = 1
	}
	if required > 3 {
		required = 3
	}
	return hits >= required
}

var captureLatinTokenRE = regexp.MustCompile(`[a-z0-9][a-z0-9+._-]*`)
var captureCJKRE = regexp.MustCompile(`[\p{Han}]{2,}`)

func normalizeCaptureCoverageText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	replacements := map[string]string{
		"golang":  "go",
		"node.js": "nodejs",
		"node js": "nodejs",
	}
	for old, next := range replacements {
		value = strings.ReplaceAll(value, old, next)
	}
	return value
}

func captureCoverageAnchors(value string) []string {
	seen := map[string]struct{}{}
	out := []string{}
	add := func(anchor string) {
		anchor = strings.TrimSpace(anchor)
		if anchor == "" || captureCoverageStopAnchor(anchor) {
			return
		}
		if _, exists := seen[anchor]; exists {
			return
		}
		seen[anchor] = struct{}{}
		out = append(out, anchor)
	}
	for _, token := range captureLatinTokenRE.FindAllString(value, -1) {
		if len(token) >= 2 {
			add(token)
		}
	}
	for _, run := range captureCJKRE.FindAllString(value, -1) {
		runes := []rune(run)
		if len(runes) == 2 {
			add(run)
			continue
		}
		for idx := 0; idx+2 <= len(runes); idx++ {
			add(string(runes[idx : idx+2]))
		}
	}
	return out
}

func captureCoverageStopAnchor(anchor string) bool {
	switch anchor {
	case "使用", "需要", "要求", "进行", "继续", "当前", "内容", "信息", "整理", "补充", "这个", "那个", "方式", "开发":
		return true
	default:
		return false
	}
}

func lowSignalCaptureTurn(turn string) bool {
	compact := compactText(turn)
	switch compact {
	case "整理", "继续", "继续整理", "收口", "保存", "归档", "提交", "确认", "好的", "是的":
		return true
	default:
		return len([]rune(compact)) <= 2
	}
}

func likelyConflictingCaptureTurn(turn string) bool {
	lower := strings.ToLower(turn)
	for _, marker := range []string{
		"不是", "不再", "不要", "不能", "取消", "改成", "改为", "调整为", "替换", "覆盖", "冲突", "相反", "而是", "instead",
	} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
}

func appendCaptureContextSection(body, title string, items []string) string {
	items = trimNonEmpty(items)
	if len(items) == 0 {
		return strings.TrimSpace(body)
	}
	body = strings.TrimSpace(body)
	var builder strings.Builder
	if body != "" {
		builder.WriteString(body)
		builder.WriteString("\n\n")
	}
	builder.WriteString("**")
	builder.WriteString(title)
	builder.WriteString("**\n")
	for _, item := range items {
		builder.WriteString("- ")
		builder.WriteString(item)
		builder.WriteString("\n")
	}
	return strings.TrimSpace(builder.String())
}

func firstNonEmptyString(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

// mergeContextStrings keeps previously discovered session-context facts
// unless the new enrich pass contributes additional values. The async
// capture-context provider is best-effort and may return partial lists,
// so the enrich path should accumulate, not erase, prior context.
func mergeContextStrings(existing, next []string) []string {
	merged := trimNonEmpty(existing)
	merged = append(merged, trimNonEmpty(next)...)
	return uniqueStrings(merged)
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
	return s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
		switch scratchpad.ArchiveIntent(strings.TrimSpace(string(intent))) {
		case scratchpad.ArchiveIntentMenu, scratchpad.ArchiveIntentLLM:
			sp.ArchiveIntent = intent
		default:
			sp.ArchiveIntent = scratchpad.ArchiveIntentNone
		}
		sp.SessionContext.ArchiveIntent = sp.ArchiveIntent
		return nil
	})
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
	return s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
		switch scratchpad.ArchiveStrategy(strings.TrimSpace(string(strategy))) {
		case scratchpad.ArchiveStrategyUpdate, scratchpad.ArchiveStrategySupplement:
			sp.ArchiveStrategy = strategy
		default:
			sp.ArchiveStrategy = scratchpad.ArchiveStrategyNew
		}
		sp.SessionContext.ArchiveStrategy = sp.ArchiveStrategy
		if thoughtID = strings.TrimSpace(thoughtID); thoughtID != "" {
			// The strategy decision may name a target thought (update
			// or supplement). Persist it on SourceThoughtID so a
			// subsequent BuildArchivePreview / Commit can find it
			// without the HTTP layer re-sending it on every call.
			sp.SourceThoughtID = thoughtID
		}
		return nil
	})
}

func (s *ScratchpadService) SetDocumentProfile(sessionID, profileID string, version int) (scratchpad.Scratchpad, error) {
	if s == nil || s.store == nil {
		return scratchpad.Scratchpad{}, ErrScratchpadUnavailable
	}
	return s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
		profileID = strings.TrimSpace(profileID)
		if profileID == "" {
			sp.SessionContext.CandidateDocumentFamily = ""
			sp.SessionContext.CandidateProfileID = ""
			sp.SessionContext.CandidateProfileVersion = 0
			sp.SessionContext.ProfileConfidence = 0
			sp.SessionContext.ProfileMatchReason = ""
			sp.SessionContext.ProfileExplicit = false
			sp.SessionContext.FrozenProfile = ""
			sp.ArchivePreview = nil
			return nil
		}
		if s.profiles == nil {
			return errors.New("capture: document profile registry is not ready")
		}
		var err error
		var profile documentprofile.DocumentProfile
		if version > 0 {
			profile, err = s.profiles.Resolve(models.DocumentProfileRef{ProfileID: profileID, Version: version})
		} else {
			profile, err = s.profiles.ResolveLatest(profileID)
		}
		if err != nil {
			return err
		}
		if sp.SessionContext.CandidateProfileID != profile.Ref.ProfileID || sp.SessionContext.CandidateProfileVersion != profile.Ref.Version {
			sp.SessionContext.DocumentParameters = nil
			sp.SessionContext.ParameterValues = nil
		}
		sp.SessionContext.CandidateDocumentFamily = profile.Ref.Family
		sp.SessionContext.CandidateProfileID = profile.Ref.ProfileID
		sp.SessionContext.CandidateProfileVersion = profile.Ref.Version
		sp.SessionContext.ProfileConfidence = 100
		sp.SessionContext.ProfileMatchReason = "explicit user profile selection"
		sp.SessionContext.ProfileExplicit = true
		sp.SessionContext.FrozenProfile = profile.Snapshot
		s.normalizeProfileContext(&sp.SessionContext)
		sp.ArchivePreview = nil
		return nil
	})
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
//     contains the complete final document and a field-level diff
//     against the existing thought.
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

	body := archiveBody(sp)
	if strategy == scratchpad.ArchiveStrategyUpdate && currentThought != nil {
		body = fullUpdateArchiveBody(currentThought.Content, body)
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
		SessionID:         sp.SessionID,
		Title:             title,
		Body:              body,
		Tags:              tags,
		SourceLinks:       sourceLinks,
		RelatedThoughtIDs: uniqueStrings(sp.SessionContext.RelatedThoughtIDs),
		RelatedTopics:     relatedTopics,
		Strategy:          strategy,
		GeneratedAt:       now,
	}
	if strategy == scratchpad.ArchiveStrategySupplement && currentThought != nil {
		preview.ThoughtID = currentThought.Thought.ID
	}
	if strategy == scratchpad.ArchiveStrategyUpdate && currentThought != nil {
		preview.ThoughtID = currentThought.Thought.ID
		preview.Diff = buildArchiveDiff(*currentThought, preview)
	}
	return preview, nil
}

func buildArchiveDiff(current models.ThoughtSnapshot, preview scratchpad.ArchivePreview) *scratchpad.ThoughtDiff {
	beforeBody := firstNonEmptyString(current.Content.AINotes, current.Content.ExtractedContent, current.Content.Original)
	changed := []string{}
	if strings.TrimSpace(current.Thought.UserTitle) != strings.TrimSpace(preview.Title) {
		changed = append(changed, "title")
	}
	if !equalStringSets(current.Thought.UserTags, preview.Tags) {
		changed = append(changed, "tags")
	}
	if strings.TrimSpace(beforeBody) != strings.TrimSpace(preview.Body) {
		changed = append(changed, "body")
	}
	return &scratchpad.ThoughtDiff{
		Before:        strings.TrimSpace(beforeBody),
		After:         strings.TrimSpace(preview.Body),
		ChangedFields: changed,
	}
}

func equalStringSets(left, right []string) bool {
	a := uniqueStrings(left)
	b := uniqueStrings(right)
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)
	for idx := range a {
		if a[idx] != b[idx] {
			return false
		}
	}
	return true
}

func (s *ScratchpadService) PrepareArchive(ctx context.Context, sp scratchpad.Scratchpad, currentThought *models.ThoughtSnapshot) (scratchpad.ArchivePreview, error) {
	// A persisted preview is output, not source material. Feeding it back into a
	// fresh generation recursively nests the previous rendered document and can
	// multiply every section on each preview/re-archive attempt.
	source := sp
	source.ArchivePreview = nil
	preview, err := s.BuildArchivePreview(source, currentThought)
	if err != nil {
		return scratchpad.ArchivePreview{}, err
	}
	profile, err := s.resolveArchiveProfile(sp, currentThought)
	if err != nil {
		return scratchpad.ArchivePreview{}, err
	}
	parameters := profileParameters(profile, sp.SessionContext.DocumentParameters)
	preview.DocumentProfile = profile.Ref
	preview.ProfileSnapshot = profile.Snapshot
	preview.Parameters = parameters
	preview.ContextHash = archiveContextHash(sp, currentThought, profile.Ref)
	if currentThought != nil && currentThought.Thought.Document != nil {
		preview.SourceGeneration = currentThought.Thought.Document.Generation
	}
	preview.InputGeneration = sp.InputGeneration
	preview.Sources = archiveSources(preview.SourceLinks)
	if issues := archivePreflight(profile, sp, parameters); len(issues) > 0 {
		preview.Validation = models.ArchiveValidation{Status: models.ArchiveValidationInvalid, Issues: issues, ValidatedAt: s.now()}
		return preview, nil
	}
	if profile.Ref.Family == models.DocumentFamilyNote {
		if profile.Template == "" {
			preview.Validation = models.ArchiveValidation{Status: models.ArchiveValidationValid, ValidatedAt: s.now()}
			applyArchiveContextValidation(&preview, sp)
			return preview, nil
		}
		draft := models.DocumentDraft{
			Title:    preview.Title,
			Summary:  sp.SessionContext.CandidateSummary,
			Sections: map[string]models.DocumentSection{"body": {Content: stripDocumentTitle(preview.Body)}},
		}
		for idx, link := range preview.SourceLinks {
			draft.References = append(draft.References, models.DocumentReference{ID: fmt.Sprintf("S%d", idx+1), SourceLink: link})
		}
		rendered := documentprofile.Render(profile, draft, parameters)
		preview.Body = rendered.Content
		preview.Validation = rendered.Validation
		applyArchiveContextValidation(&preview, sp)
		preview.GeneratedAt = s.now()
		if preview.Strategy == scratchpad.ArchiveStrategyUpdate && currentThought != nil {
			preview.Diff = buildArchiveDiff(*currentThought, preview)
		}
		return preview, nil
	}
	request := ai.DocumentGenerationRequest{
		Profile:    profile,
		Parameters: parameters,
		Context: ai.DocumentSourceContext{
			Title:       preview.Title,
			Summary:     sp.SessionContext.CandidateSummary,
			Body:        preview.Body,
			Facts:       append([]string(nil), sp.SessionContext.ConfirmedFacts...),
			Questions:   append([]string(nil), sp.SessionContext.OpenQuestions...),
			Conflicts:   append([]string(nil), sp.SessionContext.Conflicts...),
			SourceLinks: append([]string(nil), preview.SourceLinks...),
		},
	}
	var rendered documentprofile.RenderResult
	var draft models.DocumentDraft
	generator := s.documentGenerator
	if generator == nil {
		generator = ai.NewLocalRefineProvider()
	}
	for attempt := 0; attempt <= s.maxRepairAttempts; attempt++ {
		draft, err = generator.GenerateDocument(ctx, request)
		if err != nil {
			if !recoverableArchiveDocumentGenerationError(err) {
				return scratchpad.ArchivePreview{}, fmt.Errorf("capture: generate archive document: %w", err)
			}
			if attempt < s.maxRepairAttempts {
				slog.Warn("archive document generation returned malformed output; retrying", "attempt", attempt+1, "max_attempts", s.maxRepairAttempts+1, "error", err)
				continue
			}
			if profile.SchemaVersion == 2 {
				return scratchpad.ArchivePreview{}, fmt.Errorf("capture: document generation failed after bounded repair: %w", err)
			}
			slog.Warn("archive document generation remained malformed; using local fallback", "attempts", attempt+1, "error", err)
			draft, err = ai.NewLocalRefineProvider().GenerateDocument(ctx, request)
			if err != nil {
				return scratchpad.ArchivePreview{}, fmt.Errorf("capture: generate archive document fallback: %w", err)
			}
		}
		draft = compactArchiveDraft(request, draft)
		rendered = documentprofile.Render(profile, draft, parameters)
		if issue := archiveSourceCoverageIssue(request.Context.Body, rendered.Content); issue != nil {
			rendered.Validation.Issues = append(rendered.Validation.Issues, *issue)
			rendered.Validation.Status = models.ArchiveValidationInvalid
		}
		if profile.SchemaVersion == 2 {
			candidate, e := documentflow.CheckCandidate(rendered.Content, draft.Title, profile, parameters, *preview.Sources, preview.ContextHash)
			if e != nil {
				return scratchpad.ArchivePreview{}, e
			}
			preview.Document = &candidate
			for _, issue := range candidate.Report.Issues {
				if issue.Severity == "error" {
					rendered.Validation.Status = models.ArchiveValidationInvalid
					rendered.Validation.Issues = append(rendered.Validation.Issues, models.ValidationIssue{Code: issue.Code, Severity: issue.Severity, Section: issue.Section, Message: issue.Basis})
				}
			}
		}
		rendered.Validation.RepairCount = attempt
		if rendered.Validation.Status == models.ArchiveValidationValid {
			break
		}
		request.PreviousDraft = &draft
		request.RepairIssues = append([]models.ValidationIssue(nil), rendered.Validation.Issues...)
	}
	if rendered.Validation.Status != models.ArchiveValidationValid && profile.SchemaVersion != 2 {
		fallbackDraft, fallbackErr := ai.NewLocalRefineProvider().GenerateDocument(ctx, request)
		if fallbackErr == nil {
			fallbackDraft = compactArchiveDraft(request, fallbackDraft)
			fallbackRendered := documentprofile.Render(profile, fallbackDraft, parameters)
			if issue := archiveSourceCoverageIssue(request.Context.Body, fallbackRendered.Content); issue != nil {
				fallbackRendered.Validation.Issues = append(fallbackRendered.Validation.Issues, *issue)
				fallbackRendered.Validation.Status = models.ArchiveValidationInvalid
			}
			fallbackRendered.Validation.RepairCount = s.maxRepairAttempts + 1
			if fallbackRendered.Validation.Status == models.ArchiveValidationValid {
				draft = fallbackDraft
				rendered = fallbackRendered
			}
		}
	}
	preview.Title = firstNonEmptyString(strings.TrimSpace(draft.Title), preview.Title)
	preview.Body = strings.TrimSpace(rendered.Content)
	preview.Validation = rendered.Validation

	applyArchiveContextValidation(&preview, sp)
	preview.GeneratedAt = s.now()
	if preview.Strategy == scratchpad.ArchiveStrategyUpdate && currentThought != nil {
		preview.Diff = buildArchiveDiff(*currentThought, preview)
	}
	return preview, nil
}

func applyArchiveContextValidation(preview *scratchpad.ArchivePreview, sp scratchpad.Scratchpad) {
	if preview == nil {
		return
	}
	if len(trimNonEmpty(sp.SessionContext.Conflicts)) > 0 {
		preview.Validation.Issues = append(preview.Validation.Issues, models.ValidationIssue{
			Code:     "thoughtflow.archive.conflicts_unresolved",
			Severity: models.ValidationSeverityError,
			Message:  "session context contains unresolved conflicts",
		})
		preview.Validation.Status = models.ArchiveValidationInvalid
	}
}

func archiveSourceCoverageIssue(source, rendered string) *models.ValidationIssue {
	source = strings.TrimSpace(source)
	if len([]rune(source)) < 1000 {
		return nil
	}
	if strings.Contains(compactText(rendered), compactText(source)) || archiveTextLineCoverage(rendered, source) >= 0.1 {
		return nil
	}
	return &models.ValidationIssue{
		Code:     "thoughtflow.profile.source_content_missing",
		Severity: models.ValidationSeverityError,
		Message:  "rendered document does not preserve enough of the structured source content",
	}
}

func compactArchiveDraft(request ai.DocumentGenerationRequest, draft models.DocumentDraft) models.DocumentDraft {
	base := strings.TrimSpace(request.Context.Body)
	if len(draft.Sections) == 0 {
		return draft
	}
	orderedKeys := preferredArchiveSectionKeys(request, draft)
	fullKey := orderedKeys[0]
	baseKey := compactText(base)
	for _, key := range orderedKeys[1:] {
		section := draft.Sections[key]
		if baseKey == "" || !containsSubstantialArchiveText(compactText(section.Content), baseKey) {
			continue
		}
		section.Content = archiveDuplicateReplacement(request, key, fullKey)
		draft.Sections[key] = section
	}

	// A valid remote response can still place the same complete source document
	// in several profile sections. Keep the richest copy in the semantically
	// preferred section and collapse later near-identical copies before render.
	for index, keepKey := range orderedKeys {
		keep := compactText(draft.Sections[keepKey].Content)
		if len([]rune(keep)) < 500 {
			continue
		}
		for _, duplicateKey := range orderedKeys[index+1:] {
			duplicate := compactText(draft.Sections[duplicateKey].Content)
			if !archiveTextNearlyDuplicate(keep, duplicate) {
				continue
			}
			section := draft.Sections[duplicateKey]
			section.Content = archiveDuplicateReplacement(request, duplicateKey, keepKey)
			draft.Sections[duplicateKey] = section
		}
	}
	for key, section := range draft.Sections {
		section.Content = normalizeEmbeddedDocumentSection(section.Content)
		draft.Sections[key] = section
	}
	return draft
}

func stripDocumentTitle(value string) string {
	lines := strings.Split(strings.TrimSpace(value), "\n")
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		if strings.HasPrefix(trimmed, "# ") {
			return strings.TrimSpace(strings.Join(append(lines[:idx], lines[idx+1:]...), "\n"))
		}
		break
	}
	return strings.TrimSpace(value)
}

func normalizeEmbeddedDocumentSection(value string) string {
	value = stripDocumentTitle(value)
	lines := strings.Split(value, "\n")
	inFence := false
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
			inFence = !inFence
			continue
		}
		if inFence {
			continue
		}
		indent := line[:len(line)-len(strings.TrimLeft(line, " \t"))]
		switch {
		case strings.HasPrefix(trimmed, "# "):
			lines[idx] = indent + "### " + strings.TrimSpace(strings.TrimPrefix(trimmed, "# "))
		case strings.HasPrefix(trimmed, "## "):
			lines[idx] = indent + "### " + strings.TrimSpace(strings.TrimPrefix(trimmed, "## "))
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func preferredArchiveSectionKeys(request ai.DocumentGenerationRequest, draft models.DocumentDraft) []string {
	keys := make([]string, 0, len(draft.Sections))
	seen := map[string]struct{}{}
	appendKey := func(key string) {
		if _, ok := draft.Sections[key]; !ok {
			return
		}
		if _, ok := seen[key]; ok {
			return
		}
		seen[key] = struct{}{}
		keys = append(keys, key)
	}
	for _, key := range []string{"proposal", "findings", "analysis", "body"} {
		appendKey(key)
	}
	for _, spec := range request.Profile.Sections {
		appendKey(spec.Key)
	}
	remaining := make([]string, 0, len(draft.Sections))
	for key := range draft.Sections {
		remaining = append(remaining, key)
	}
	sort.Strings(remaining)
	for _, key := range remaining {
		appendKey(key)
	}
	return keys
}

func containsSubstantialArchiveText(content, source string) bool {
	if len([]rune(source)) < 500 || len(content) < len(source) {
		return false
	}
	return strings.Contains(content, source)
}

func archiveTextNearlyDuplicate(left, right string) bool {
	leftRunes := len([]rune(left))
	rightRunes := len([]rune(right))
	if leftRunes < 500 || rightRunes < 500 {
		return false
	}
	shorter, longer := left, right
	shorterRunes, longerRunes := leftRunes, rightRunes
	if shorterRunes > longerRunes {
		shorter, longer = longer, shorter
		shorterRunes, longerRunes = longerRunes, shorterRunes
	}
	return float64(shorterRunes)/float64(longerRunes) >= 0.8 && strings.Contains(longer, shorter)
}

func archiveDuplicateReplacement(request ai.DocumentGenerationRequest, key, keepKey string) string {
	if key == "background" {
		if summary := strings.TrimSpace(request.Context.Summary); summary != "" {
			return summary
		}
	}
	label := map[string]string{
		"proposal": "方案设计",
		"findings": "研究发现",
		"analysis": "分析",
		"body":     "正文",
	}[keepKey]
	if label == "" {
		label = keepKey
	}
	if summary := strings.TrimSpace(request.Context.Summary); summary != "" {
		return summary
	}
	return fmt.Sprintf("本节以“%s”中的已确认内容作为实施依据。", label)
}

func recoverableArchiveDocumentGenerationError(err error) bool {
	if err == nil {
		return false
	}
	var providerErr ai.ProviderError
	if errors.As(err, &providerErr) {
		return providerErr.Code == "thoughtflow.ai.output_truncated"
	}
	return strings.Contains(err.Error(), "parse document draft json")
}

func (s *ScratchpadService) resolveArchiveProfile(sp scratchpad.Scratchpad, currentThought *models.ThoughtSnapshot) (documentprofile.DocumentProfile, error) {
	if sp.SessionContext.FrozenProfile != "" {
		return documentprofile.Parse([]byte(sp.SessionContext.FrozenProfile), documentprofile.Limits{})
	}
	if s.profiles == nil {
		return documentprofile.DocumentProfile{Ref: models.DocumentProfileRef{Family: models.DocumentFamilyNote, ProfileID: models.DocumentProfileBuiltinNote, Version: 1}}, nil
	}
	if currentThought != nil && currentThought.Thought.DocumentProfile != nil && !sp.SessionContext.ProfileExplicit && sp.ArchiveStrategy == scratchpad.ArchiveStrategyUpdate {
		return s.profiles.Resolve(*currentThought.Thought.DocumentProfile)
	}
	if id := strings.TrimSpace(sp.SessionContext.CandidateProfileID); id != "" {
		version := sp.SessionContext.CandidateProfileVersion
		if version > 0 {
			return s.profiles.Resolve(models.DocumentProfileRef{ProfileID: id, Version: version})
		}
		return s.profiles.ResolveLatest(id)
	}
	return s.profiles.Default(), nil
}

func profileParameters(profile documentprofile.DocumentProfile, values map[string]string) map[string]string {
	out := map[string]string{}
	for _, input := range profile.Inputs {
		if input.Default != "" {
			out[input.Key] = input.Default
		}
	}
	allowed := map[string]bool{}
	for _, input := range profile.Inputs {
		allowed[input.Key] = true
	}
	for key, value := range values {
		if value = strings.TrimSpace(value); allowed[key] {
			out[key] = value
		}
	}
	return out
}

func archiveContextHash(sp scratchpad.Scratchpad, currentThought *models.ThoughtSnapshot, ref models.DocumentProfileRef) string {
	stableContext := sp.SessionContext
	stableContext.ArchiveIntent = ""
	stableContext.ArchiveStrategy = ""
	payload := struct {
		Content     string
		Messages    []scratchpad.Message
		Context     scratchpad.SessionContext
		Strategy    scratchpad.ArchiveStrategy
		SourceID    string
		CurrentHash string
		Profile     models.DocumentProfileRef
	}{sp.Content, sp.Messages, stableContext, sp.ArchiveStrategy, sp.SourceThoughtID, "", ref}
	if currentThought != nil {
		payload.CurrentHash = currentThought.Thought.ContentHash
		if currentThought.Thought.Document != nil {
			payload.CurrentHash += ":" + currentThought.Thought.Document.RevisionID
		}
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func (s *ScratchpadService) validateArchivePreview(ctx context.Context, sp scratchpad.Scratchpad) error {
	if sp.ArchivePreview == nil {
		if s.profiles != nil {
			return ErrArchivePreviewRequired
		}
		return nil
	}
	preview := sp.ArchivePreview
	if preview.Validation.Status != models.ArchiveValidationValid {
		return ErrArchiveFormatInvalid
	}
	if preview.DocumentProfile.ProfileID == "" || s.profiles == nil {
		return nil
	}
	if preview.DocumentProfile.ProfileID != "" {
		if _, err := s.profiles.Resolve(preview.DocumentProfile); err != nil {
			return fmt.Errorf("%w: %v", ErrArchivePreviewStale, err)
		}
	}
	var current *models.ThoughtSnapshot
	if target := archiveTarget(sp); target != "" {
		snapshot, err := s.capture.GetThought(ctx, target)
		if err != nil {
			return err
		}
		current = &snapshot
	}

	if preview.ContextHash == "" || preview.ContextHash != archiveContextHash(sp, current, preview.DocumentProfile) {
		return ErrArchivePreviewStale
	}
	return nil
}

// thoughtBodyForDiff picks the string used as the "before" side
// of a diff. UserTitle / ExtractedTitle / DisplayTitle are tried
// fullUpdateArchiveBody builds the source material for re-archiving an
// existing thought. Reopen sessions may contain only the latest supplement in
// CandidateBody after context enrichment, so persisting that projection alone
// would replace the archived document with a delta. Keep the existing archived
// body as the baseline and append genuinely new material for the document
// generator to synthesize into one complete final document.
func fullUpdateArchiveBody(current models.ThoughtContent, addition string) string {
	base := firstNonEmptyString(current.AINotes, current.ExtractedContent, current.Original)
	base = strings.TrimSpace(base)
	addition = strings.TrimSpace(addition)
	if base == "" {
		return addition
	}
	if addition == "" {
		return base
	}
	baseKey := compactText(base)
	additionKey := compactText(addition)
	if baseKey == additionKey || strings.Contains(additionKey, baseKey) {
		return addition
	}
	if strings.Contains(baseKey, additionKey) {
		if completeArchiveDocument(addition) && hasRepeatedArchiveHeadings(base) {
			return addition
		}
		return base
	}
	if completeArchiveDocument(addition) && archiveTextLineCoverage(base, addition) >= 0.65 {
		return addition
	}
	return base + "\n\n## 本轮补充信息\n\n" + addition
}

func completeArchiveDocument(value string) bool {
	if len([]rune(strings.TrimSpace(value))) < 1000 {
		return false
	}
	headings := 0
	for _, line := range strings.Split(value, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "#") {
			headings++
		}
	}
	return headings >= 3
}

func hasRepeatedArchiveHeadings(value string) bool {
	seen := map[string]struct{}{}
	for _, line := range strings.Split(value, "\n") {
		line = compactText(line)
		if !strings.HasPrefix(line, "##") || len([]rune(line)) < 4 {
			continue
		}
		if _, ok := seen[line]; ok {
			return true
		}
		seen[line] = struct{}{}
	}
	return false
}

func archiveTextLineCoverage(container, candidate string) float64 {
	available := map[string]struct{}{}
	for _, line := range strings.Split(container, "\n") {
		line = compactText(line)
		if len([]rune(line)) >= 8 {
			available[line] = struct{}{}
		}
	}
	matchedWeight := 0
	totalWeight := 0
	seen := map[string]struct{}{}
	for _, line := range strings.Split(candidate, "\n") {
		line = compactText(line)
		weight := len([]rune(line))
		if weight < 8 {
			continue
		}
		if _, ok := seen[line]; ok {
			continue
		}
		seen[line] = struct{}{}
		totalWeight += weight
		if _, ok := available[line]; ok {
			matchedWeight += weight
		}
	}
	if totalWeight == 0 {
		return 0
	}
	return float64(matchedWeight) / float64(totalWeight)
}

// archiveBody returns the body that should be shown in archive
// preview and persisted on commit. The LLM-facing contract treats
// AssistantReply as the per-turn chat answer, CandidateSummary as archive
// metadata and CandidateBody as the archive body, but providers can return an
// incomplete final body. The cumulative SessionContext remains the
// single source of truth; historical AI bubbles are never concatenated
// into the archive because they may be obsolete full-document revisions.
// Never fall back to scratchpad.Content here:
// Content is the user's raw capture-session input and must not be
// persisted into a Thought after archive.
func archiveBody(sp scratchpad.Scratchpad) string {
	if sp.ArchivePreview != nil &&
		strings.TrimSpace(sp.ArchivePreview.Body) != "" &&
		sp.ArchivePreview.Validation.Status == models.ArchiveValidationValid &&
		(sp.ArchivePreview.Strategy == "" || sp.ArchivePreview.Strategy == sp.ArchiveStrategy) {
		return sp.ArchivePreview.Body
	}
	summary := strings.TrimSpace(sp.SessionContext.CandidateSummary)
	body := strings.TrimSpace(sp.SessionContext.CandidateBody)
	base := ""
	if body == "" {
		base = summary
	} else if summary == "" {
		base = body
	} else {
		base = richerArchiveText(body, summary)
	}
	return strings.TrimSpace(base)
}

func richerArchiveText(body, summary string) string {
	body = strings.TrimSpace(body)
	summary = strings.TrimSpace(summary)
	if body == "" {
		return summary
	}
	if summary == "" {
		return body
	}
	bodyKey := compactText(body)
	summaryKey := compactText(summary)
	if bodyKey == summaryKey || strings.Contains(bodyKey, summaryKey) {
		return body
	}
	if strings.Contains(summaryKey, bodyKey) {
		return summary
	}
	bodyScore := archiveTextScore(body)
	summaryScore := archiveTextScore(summary)
	if summaryScore > bodyScore {
		return summary
	}
	return body
}

func compactText(value string) string {
	return strings.Join(strings.Fields(value), "")
}

func archiveTextScore(value string) int {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return 0
	}
	score := len([]rune(trimmed))
	for _, line := range strings.Split(trimmed, "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "#"):
			score += 120
		case strings.HasPrefix(line, "- "), strings.HasPrefix(line, "* "):
			score += 25
		case strings.HasPrefix(line, "1. "), strings.HasPrefix(line, "2. "), strings.HasPrefix(line, "3. "):
			score += 25
		}
	}
	return score
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
	if strings.TrimSpace(sp.CommittedThoughtID) != "" && sp.ArchivePreview == nil {
		return models.CaptureCommand{}, ErrAlreadyCommitted
	}
	content := archiveBody(sp)
	if content == "" {
		return models.CaptureCommand{}, errors.New("capture: scratchpad content is empty")
	}
	title := strings.TrimSpace(sp.SessionContext.CandidateTitle)
	if title == "" {
		title = strings.TrimSpace(sp.Draft.TitleSet)
	}
	if title == "" {
		title = strings.TrimSpace(sp.Title)
	}
	tags := uniqueStrings(sp.SessionContext.CandidateTags)
	if len(tags) == 0 {
		tags = uniqueStrings(sp.Tags)
	}
	topicHints := uniqueStrings(append(append([]string(nil), sp.SessionContext.SuggestedTopicIDs...), sp.TopicHints...))
	links := trimNonEmpty(sp.SessionContext.SourceLinks)
	if sp.ArchivePreview != nil {
		title = strings.TrimSpace(sp.ArchivePreview.Title)
		tags = uniqueStrings(sp.ArchivePreview.Tags)
		topicHints = uniqueStrings(sp.ArchivePreview.RelatedTopics)
		links = trimNonEmpty(sp.ArchivePreview.SourceLinks)
	}
	cmdType := models.ThoughtTypeText
	capturedURL := ""
	typedPreview := sp.ArchivePreview != nil &&
		strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" &&
		sp.ArchivePreview.DocumentProfile.Family != models.DocumentFamilyNote
	if !typedPreview {
		if url := extractURL(firstNonEmptyString(sp.Content, content)); url != "" {
			cmdType = models.ThoughtTypeURL
			capturedURL = url
		}
	}
	var profileRef *models.DocumentProfileRef
	if sp.ArchivePreview != nil && strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" {
		ref := sp.ArchivePreview.DocumentProfile
		profileRef = &ref
	}
	return models.CaptureCommand{
		Type:                    cmdType,
		Content:                 content,
		URL:                     capturedURL,
		Title:                   title,
		Tags:                    tags,
		TopicHints:              topicHints,
		Links:                   links,
		RelatedThoughtIDs:       previewRelatedThoughtIDs(sp),
		SuggestedTopicIDs:       uniqueStrings(sp.SessionContext.SuggestedTopicIDs),
		Source:                  models.ThoughtSourceScratchpadCommit,
		DocumentProfile:         profileRef,
		DocumentParameters:      previewParameters(sp),
		DocumentSources:         previewSources(sp),
		DocumentProfileSnapshot: previewSnapshot(sp),
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
	return s.store.Update(sessionID, func(sp *scratchpad.Scratchpad) error {
		sp.Content = ""
		sp.Messages = nil
		sp.Draft = scratchpad.Draft{}
		sp.ArchivePreview = nil
		sp.Operation = nil
		return nil
	})
}

// ReopenFromThought seeds a brand-new scratchpad from an existing
// archived thought (PRD §3.1.1). The new session is wired up
// such that:
//
//   - SourceThoughtID points at the parent thought;
//   - SessionContext is pre-populated from the thought's
//     metadata so the LLM can resume the conversation without
//     re-reading the file;
//   - ArchiveStrategy defaults to "update_thought" so the next
//     archive saves back to the source thought file. The user can
//     still explicitly choose "supplement" or "new" via
//     /api/capture/sessions/{id}/strategy before committing.
//
// The function generates a new sessionID if the caller did not
// supply one (the common case — the front end wants a clean
// slate with no risk of merging into an old conversation).
//
// Returns the new scratchpad, already persisted.
func (s *ScratchpadService) ReopenFromThought(ctx context.Context, thoughtID, sessionID string) (scratchpad.Scratchpad, error) {
	if s == nil || s.store == nil {
		return scratchpad.Scratchpad{}, ErrScratchpadUnavailable
	}
	if s.capture == nil {
		return scratchpad.Scratchpad{}, errors.New("capture: scratchpad reopen pipeline is not wired up")
	}
	thoughtID = strings.TrimSpace(thoughtID)
	if thoughtID == "" {
		return scratchpad.Scratchpad{}, errors.New("capture: thought id is required")
	}
	snapshot, err := s.capture.GetThought(ctx, thoughtID)
	if err != nil {
		return scratchpad.Scratchpad{}, fmt.Errorf("capture: source thought not found: %w", err)
	}
	thought := snapshot.Thought
	content := snapshot.Content

	newID := strings.TrimSpace(sessionID)
	if newID == "" {
		newID = models.NewEventID(s.now())
	}
	tags := append([]string(nil), thought.UserTags...)
	tags = append(tags, thought.AITags...)
	tags = uniqueStrings(tags)
	sourceLinks := []string{}
	if url := strings.TrimSpace(thought.URL); url != "" {
		sourceLinks = append(sourceLinks, url)
	}
	for _, f := range thought.URLFollowups {
		if u := strings.TrimSpace(f.URL); u != "" {
			sourceLinks = append(sourceLinks, u)
		}
	}
	sourceLinks = uniqueStrings(sourceLinks)

	title := strings.TrimSpace(thought.UserTitle)
	if title == "" {
		title = strings.TrimSpace(thought.ExtractedTitle)
	}

	related := append([]string(nil), thought.RelatedThoughtIDs...)
	related = uniqueStrings(related)

	body := reopenThoughtBody(content)
	profileRef := models.DocumentProfileRef{Family: models.DocumentFamilyNote, ProfileID: models.DocumentProfileBuiltinNote, Version: 1}
	if thought.DocumentProfile != nil {
		profileRef = *thought.DocumentProfile
	}
	messages := []scratchpad.Message{}
	if body != "" {
		messages = append(messages, scratchpad.Message{Role: "ai", Text: body, At: s.now()})
	}

	sp := scratchpad.Scratchpad{
		SessionID:       newID,
		SourceThoughtID: thoughtID,
		Title:           title,
		Content:         "",
		Messages:        messages,
		Tags:            tags,
		TopicHints:      append([]string(nil), thought.TopicIDs...),
		SessionContext: scratchpad.SessionContext{
			Topic:                   strings.TrimSpace(thought.UserTitle),
			CandidateTitle:          title,
			CandidateTags:           tags,
			CandidateSummary:        body,
			CandidateBody:           body,
			SourceLinks:             sourceLinks,
			RelatedThoughtIDs:       related,
			ArchiveIntent:           scratchpad.ArchiveIntentMenu,
			ArchiveStrategy:         scratchpad.ArchiveStrategyUpdate,
			CandidateDocumentFamily: profileRef.Family,
			CandidateProfileID:      profileRef.ProfileID,
			CandidateProfileVersion: profileRef.Version,
			ProfileConfidence:       95,
			ProfileMatchReason:      "inherited existing thought profile",
			ArchiveReadiness:        "ready",
		},
		ArchiveStrategy: scratchpad.ArchiveStrategyUpdate,
		ArchiveIntent:   scratchpad.ArchiveIntentMenu,
	}
	return s.store.Update(newID, func(current *scratchpad.Scratchpad) error {
		if current.SessionGeneration != 0 {
			return scratchpad.ErrConflict
		}
		*current = sp
		return nil
	})
}

func reopenThoughtBody(content models.ThoughtContent) string {
	return stripLeadingMarkdownHeading(content.AINotes, "AI Notes")
}

func stripLeadingMarkdownHeading(value, heading string) string {
	value = strings.TrimSpace(value)
	marker := "## " + heading
	for {
		if strings.TrimSpace(value) == marker {
			return ""
		}
		if !strings.HasPrefix(value, marker+"\n") && !strings.HasPrefix(value, marker+"\r\n") {
			return value
		}
		value = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(value, marker+"\r\n"), marker+"\n"))
	}
}

// applyDraftToThought runs after a fresh commit to apply the
// user's chat-time commands (rename / tags / notes / topics) onto
// the just-committed thought. Each non-empty draft field becomes
// one PATCH field.
func (s *ScratchpadService) applyDraftToThought(ctx context.Context, sp scratchpad.Scratchpad, thoughtID string) error {
	patch, rawBody, err := buildPatchFromScratchpad(sp, false)
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
// state into a ThoughtPatchRequest. Capture sessions treat the LLM
// synthesis as the archive body: when the user continues an already
// committed session, the latest synthesized body replaces AI Notes.
// User raw input remains
// scratchpad-only. includeBody is false for the fresh-commit
// post-capture draft application because Capture already wrote the
// body to AI Notes.
//
// Returns (nil, nil, nil) when the scratchpad carries nothing new
// beyond the original commit — the caller should treat that as a
// no-op.
func buildPatchFromScratchpad(sp scratchpad.Scratchpad, includeBody bool) (*models.ThoughtPatchRequest, []byte, error) {
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
	if includeBody {
		body := archiveBody(sp)
		if body != "" {
			req.Body = &body
			hasAny = true
		}
		if sp.ArchivePreview != nil {
			if title := strings.TrimSpace(sp.ArchivePreview.Title); title != "" {
				req.Title = &title
				hasAny = true
			}
			tags := uniqueStrings(sp.ArchivePreview.Tags)
			req.Tags = &tags
			topics := uniqueStrings(sp.ArchivePreview.RelatedTopics)
			req.TopicIDs = &topics
			links := trimNonEmpty(sp.ArchivePreview.SourceLinks)
			req.Links = &links
			related := previewRelatedThoughtIDs(sp)
			req.RelatedThoughtIDs = &related
			suggested := uniqueStrings(sp.SessionContext.SuggestedTopicIDs)
			req.SuggestedTopicIDs = &suggested
			if strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" {
				ref := sp.ArchivePreview.DocumentProfile
				req.DocumentProfile = &ref
				parameters := cloneStringMap(sp.ArchivePreview.Parameters)
				req.DocumentParameters = &parameters
			}
			hasAny = true
		}
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
// "update_thought" path. It projects the scratchpad's
// CandidateTitle / CandidateBody / CandidateTags — the
// session_context fields the user (or LLM) staged for the update —
// and emits them as the patch payload. Returns (nil, nil, nil)
// when the scratchpad carries no projected changes, so the caller
// can degrade to a no-op.
//
// The merge rule:
//   - title  ← sp.SessionContext.CandidateTitle | sp.Draft.TitleSet
//   - tags   ← sp.SessionContext.CandidateTags | sp.Tags
//   - ai_notes ← archiveBody(sp) as the archived synthesis body
func buildPatchForUpdate(sp scratchpad.Scratchpad) (*models.ThoughtPatchRequest, []byte, error) {
	hasAny := false
	req := models.ThoughtPatchRequest{}
	if sp.ArchivePreview != nil && strings.TrimSpace(sp.ArchivePreview.Title) != "" {
		title := strings.TrimSpace(sp.ArchivePreview.Title)
		req.Title = &title
		hasAny = true
	} else if title := strings.TrimSpace(sp.SessionContext.CandidateTitle); title != "" {
		req.Title = &title
		hasAny = true
	} else if title := strings.TrimSpace(sp.Draft.TitleSet); title != "" {
		req.Title = &title
		hasAny = true
	}
	tags := sp.SessionContext.CandidateTags
	if sp.ArchivePreview != nil && sp.ArchivePreview.Tags != nil {
		tags = sp.ArchivePreview.Tags
	}
	if len(tags) == 0 {
		tags = sp.Tags
	}
	if len(tags) > 0 {
		merged := uniqueStrings(tags)
		req.Tags = &merged
		hasAny = true
	}
	if body := archiveBody(sp); body != "" {
		req.AINotes = &body
		hasAny = true
	}
	if sp.ArchivePreview != nil && strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" {
		ref := sp.ArchivePreview.DocumentProfile
		req.DocumentProfile = &ref
		parameters := cloneStringMap(sp.ArchivePreview.Parameters)
		req.DocumentParameters = &parameters
		hasAny = true
	}
	if topics := uniqueStrings(append(append([]string(nil), sp.SessionContext.SuggestedTopicIDs...), sp.Draft.TopicIDs...)); len(topics) > 0 {
		if sp.ArchivePreview != nil {
			topics = uniqueStrings(sp.ArchivePreview.RelatedTopics)
		}
		req.TopicIDs = &topics
		hasAny = true
	}
	if sp.ArchivePreview != nil {
		links := trimNonEmpty(sp.ArchivePreview.SourceLinks)
		req.Links = &links
		related := previewRelatedThoughtIDs(sp)
		req.RelatedThoughtIDs = &related
		suggested := uniqueStrings(sp.SessionContext.SuggestedTopicIDs)
		req.SuggestedTopicIDs = &suggested
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

func previewRelatedThoughtIDs(sp scratchpad.Scratchpad) []string {
	if sp.ArchivePreview != nil {
		return uniqueStrings(sp.ArchivePreview.RelatedThoughtIDs)
	}
	return uniqueStrings(sp.SessionContext.RelatedThoughtIDs)
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
// the commit fired, and so the topic module can purge the session
// from every topic's candidate list.
func (s *ScratchpadService) publishCommittedEvent(thoughtID, sessionID, mode string) {
	if s.eventHub == nil {
		return
	}
	now := s.now()
	ev := models.DomainEvent{
		EventType:      models.EventScratchpadCommitted,
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

// publishContextUpdatedEvent emits a scratchpad-context-updated
// domain event so the topic module can re-match the session against
// every topic's rules. The event is best-effort: a missing event
// hub leaves the candidate list slightly stale until the next
// event, but the data is still on disk.
func (s *ScratchpadService) publishContextUpdatedEvent(sessionID string) {
	if s.eventHub == nil {
		return
	}
	now := s.now()
	ev := models.DomainEvent{
		EventType:      models.EventScratchpadContextUpdated,
		SourceUnit:     "capture",
		OccurredAt:     now,
		WorkspaceID:    "",
		ResourceType:   models.ResourceTypeSession,
		ResourceID:     sessionID,
		PayloadVersion: 1,
		Payload: map[string]any{
			"session_id": sessionID,
		},
	}
	eventutil.Post(s.eventHub, ev)
}

func firstNonEmptyLine(text string) string {
	for _, line := range strings.Split(text, "\n") {
		if trimmed := strings.TrimSpace(line); trimmed != "" {
			return trimmed
		}
	}
	return ""
}

func normalizeArchiveIntent(intent scratchpad.ArchiveIntent) scratchpad.ArchiveIntent {
	switch scratchpad.ArchiveIntent(strings.TrimSpace(string(intent))) {
	case scratchpad.ArchiveIntentMenu:
		return scratchpad.ArchiveIntentMenu
	case scratchpad.ArchiveIntentLLM:
		return scratchpad.ArchiveIntentLLM
	default:
		return scratchpad.ArchiveIntentNone
	}
}

func normalizeArchiveStrategy(strategy scratchpad.ArchiveStrategy) scratchpad.ArchiveStrategy {
	switch scratchpad.ArchiveStrategy(strings.TrimSpace(string(strategy))) {
	case scratchpad.ArchiveStrategyUpdate:
		return scratchpad.ArchiveStrategyUpdate
	case scratchpad.ArchiveStrategySupplement:
		return scratchpad.ArchiveStrategySupplement
	default:
		return scratchpad.ArchiveStrategyNew
	}
}

func (s *ScratchpadService) availableProfileDescriptors(preferred ...scratchpad.Scratchpad) []documentprofile.DocumentProfileDescriptor {
	if s == nil || s.profiles == nil {
		return nil
	}
	profiles := s.profiles.ListEnabled()
	if len(preferred) > 0 && preferred[0].SessionContext.CandidateProfileID != "" {
		if p, e := s.resolveArchiveProfile(preferred[0], nil); e == nil {
			ordered := []documentprofile.DocumentProfileDescriptor{p.Descriptor()}
			for _, item := range profiles {
				if item.ID != p.Ref.ProfileID || item.Version != p.Ref.Version {
					ordered = append(ordered, item)
				}
			}
			profiles = ordered
		}
	}
	if s.maxMatchCandidates > 0 && len(profiles) > s.maxMatchCandidates {
		profiles = profiles[:s.maxMatchCandidates]
	}
	var bounded []documentprofile.DocumentProfileDescriptor
	size := 0
	for _, profile := range profiles {
		raw, _ := json.Marshal(profile)
		if size+len(raw) > 65536 {
			break
		}
		bounded = append(bounded, profile)
		size += len(raw)
	}
	return bounded
}

func (s *ScratchpadService) existingProfile(sp scratchpad.Scratchpad) *models.DocumentProfileRef {
	if sp.ArchivePreview != nil && strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" {
		ref := sp.ArchivePreview.DocumentProfile
		return &ref
	}
	if s == nil || s.capture == nil || strings.TrimSpace(sp.SourceThoughtID) == "" {
		return nil
	}
	snapshot, err := s.capture.GetThought(context.Background(), sp.SourceThoughtID)
	if err != nil || snapshot.Thought.DocumentProfile == nil {
		return nil
	}
	ref := *snapshot.Thought.DocumentProfile
	return &ref
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return map[string]string{}
	}
	out := make(map[string]string, len(values))
	for key, value := range values {
		out[key] = value
	}
	return out
}

func mergeStringMaps(base, update map[string]string) map[string]string {
	out := cloneStringMap(base)
	for key, value := range update {
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	return out
}

func firstNonZeroInt(values ...int) int {
	for _, value := range values {
		if value != 0 {
			return value
		}
	}
	return 0
}

func normalizeArchiveReadiness(value string) string {
	switch strings.TrimSpace(value) {
	case "diverging", "converging", "ready":
		return strings.TrimSpace(value)
	default:
		return "converging"
	}
}

func normalizeOptionalArchiveReadiness(value string) string {
	if strings.TrimSpace(value) == "" {
		return ""
	}
	return normalizeArchiveReadiness(value)
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

func previewSnapshot(sp scratchpad.Scratchpad) string {
	if sp.ArchivePreview != nil {
		return sp.ArchivePreview.ProfileSnapshot
	}
	return ""
}

func (s *ScratchpadService) archiveDocuments() *documentflow.Service {
	if provider, ok := s.capture.(interface{ Documents() *documentflow.Service }); ok {
		return provider.Documents()
	}
	return nil
}

// SetArchiveScheduler is installed during application setup, before requests.
// The scheduler owns shutdown and accepted tasks; context delivery only queues.
func (s *ScratchpadService) SetArchiveScheduler(schedule func(scratchpad.Scratchpad) error) {
	s.archiveScheduler = schedule
}
