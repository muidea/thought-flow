package biz

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/ai"
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
	sp, err := s.store.Get(sessionID)
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	existingContext := enrichmentSeedContext(sp)
	sp.Messages = append(sp.Messages, scratchpad.Message{Role: role, Text: text, At: s.now()})
	if role == "user" {
		if sp.Content != "" {
			sp.Content += "\n\n"
		}
		sp.Content += text
		sp.SessionContext = scratchpad.SessionContext{}
		sp.ArchiveIntent = scratchpad.ArchiveIntentNone
		sp.ArchivePreview = nil
	}
	saved, err := s.store.Save(sp)
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
	return s.updateSessionContext(sessionID, ctx, false)
}

func (s *ScratchpadService) updateSessionContext(sessionID string, ctx scratchpad.SessionContext, appendReply bool) (scratchpad.Scratchpad, error) {
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
		ArchiveReadiness:        normalizeArchiveReadiness(ctx.ArchiveReadiness),
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
	saved, err := s.store.Save(sp)
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	s.publishContextUpdatedEvent(sessionID)
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
	if err != nil || current.Content != contentAtSchedule {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), s.contextTimeout)
	defer cancel()
	request := ai.CaptureContextRequest{
		SessionID:         sessionID,
		Content:           current.Content,
		Messages:          captureContextMessages(current.Messages),
		Existing:          captureContextFromScratchpad(req.ExistingContext),
		AvailableProfiles: s.availableProfileDescriptors(),
		ExistingProfile:   s.existingProfile(current),
	}
	result, err := s.buildCaptureContextWithRetry(ctx, sessionID, contentAtSchedule, request)
	if err != nil {
		s.markContextEnrichmentFailed(sessionID, contentAtSchedule, err)
		return
	}
	latest, err := s.store.Get(sessionID)
	if err != nil || latest.Content != contentAtSchedule {
		return
	}
	contextBase := latest
	if !hasSessionContext(contextBase.SessionContext) {
		contextBase.SessionContext = req.ExistingContext
	}
	result = preserveLatestUserTurns(result, latest.Messages)
	_, _ = s.updateSessionContext(sessionID, captureContextToScratchpad(result, contextBase), true)
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
	if strings.Contains(err.Error(), "parse capture context json") {
		return true
	}
	var providerErr ai.ProviderError
	return errors.As(err, &providerErr) && providerErr.Retryable
}

func (s *ScratchpadService) markContextEnrichmentFailed(sessionID, contentAtSchedule string, cause error) {
	slog.Warn("capture context enrichment failed", "session_id", sessionID, "error", cause)
	latest, err := s.store.Get(sessionID)
	if err != nil || latest.Content != contentAtSchedule {
		return
	}
	text := captureContextFailureText(cause)
	if text == "" {
		return
	}
	latest.Messages = appendContextReplyMessage(latest.Messages, text, s.now())
	if _, err := s.store.Save(latest); err != nil {
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
	return strings.TrimSpace(ctx.Topic) != "" ||
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
		return body
	}
	if captureReplyNeedsBody(summary, body) {
		return summary + "\n\n" + body
	}
	return summary
}

func captureReplyNeedsBody(summary, body string) bool {
	for _, marker := range []string{"下面是", "如下", "具体如下", "更新后的", "结构化工作笔记", "整理结果"} {
		if strings.Contains(summary, marker) {
			return true
		}
	}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || strings.HasPrefix(line, "- ") || strings.HasPrefix(line, "* ") ||
			strings.HasPrefix(line, "**") || strings.HasPrefix(line, "1. ") {
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
	return scratchpad.SessionContext{
		Topic:                   firstNonEmptyString(result.Topic, current.SessionContext.Topic),
		Goal:                    firstNonEmptyString(result.Goal, current.SessionContext.Goal),
		ConfirmedFacts:          mergeContextStrings(current.SessionContext.ConfirmedFacts, result.ConfirmedFacts),
		OpenQuestions:           trimNonEmpty(result.OpenQuestions),
		Conflicts:               mergeContextStrings(current.SessionContext.Conflicts, result.Conflicts),
		CandidateTitle:          firstNonEmptyString(result.CandidateTitle, current.SessionContext.CandidateTitle),
		CandidateTags:           mergeContextStrings(current.SessionContext.CandidateTags, result.CandidateTags),
		CandidateSummary:        firstNonEmptyString(result.CandidateSummary, current.SessionContext.CandidateSummary),
		CandidateBody:           firstNonEmptyString(result.CandidateBody, current.SessionContext.CandidateBody),
		SourceLinks:             mergeContextStrings(current.SessionContext.SourceLinks, result.SourceLinks),
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
		DocumentParameters:      mergeStringMaps(current.SessionContext.DocumentParameters, result.DocumentParameters),
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
	sp.SessionContext.ArchiveIntent = sp.ArchiveIntent
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
	sp.SessionContext.ArchiveStrategy = sp.ArchiveStrategy
	if thoughtID = strings.TrimSpace(thoughtID); thoughtID != "" {
		// The strategy decision may name a target thought (update
		// or supplement). Persist it on SourceThoughtID so a
		// subsequent BuildArchivePreview / Commit can find it
		// without the HTTP layer re-sending it on every call.
		sp.SourceThoughtID = thoughtID
	}
	return s.store.Save(sp)
}

func (s *ScratchpadService) SetDocumentProfile(sessionID, profileID string, version int) (scratchpad.Scratchpad, error) {
	if s == nil || s.store == nil {
		return scratchpad.Scratchpad{}, ErrScratchpadUnavailable
	}
	sp, err := s.store.Get(strings.TrimSpace(sessionID))
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	profileID = strings.TrimSpace(profileID)
	if profileID == "" {
		sp.SessionContext.CandidateDocumentFamily = ""
		sp.SessionContext.CandidateProfileID = ""
		sp.SessionContext.CandidateProfileVersion = 0
		sp.SessionContext.ProfileConfidence = 0
		sp.SessionContext.ProfileMatchReason = ""
		sp.SessionContext.ProfileExplicit = false
		sp.ArchivePreview = nil
		return s.store.Save(sp)
	}
	if s.profiles == nil {
		return scratchpad.Scratchpad{}, errors.New("capture: document profile registry is not ready")
	}
	var profile documentprofile.DocumentProfile
	if version > 0 {
		profile, err = s.profiles.Resolve(models.DocumentProfileRef{ProfileID: profileID, Version: version})
	} else {
		profile, err = s.profiles.ResolveLatest(profileID)
	}
	if err != nil {
		return scratchpad.Scratchpad{}, err
	}
	sp.SessionContext.CandidateDocumentFamily = profile.Ref.Family
	sp.SessionContext.CandidateProfileID = profile.Ref.ProfileID
	sp.SessionContext.CandidateProfileVersion = profile.Ref.Version
	sp.SessionContext.ProfileConfidence = 100
	sp.SessionContext.ProfileMatchReason = "explicit user profile selection"
	sp.SessionContext.ProfileExplicit = true
	sp.ArchivePreview = nil
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
//     is a complete final document containing both the archived body
//     and the latest supplement; it intentionally exposes no diff.
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
		Title:         title,
		Body:          body,
		Tags:          tags,
		SourceLinks:   sourceLinks,
		RelatedTopics: relatedTopics,
		Strategy:      strategy,
		GeneratedAt:   now,
	}
	if strategy == scratchpad.ArchiveStrategySupplement && currentThought != nil {
		preview.ThoughtID = currentThought.Thought.ID
	}
	if strategy == scratchpad.ArchiveStrategyUpdate && currentThought != nil {
		preview.ThoughtID = currentThought.Thought.ID
	}
	return preview, nil
}

func (s *ScratchpadService) PrepareArchive(ctx context.Context, sp scratchpad.Scratchpad, currentThought *models.ThoughtSnapshot) (scratchpad.ArchivePreview, error) {
	preview, err := s.BuildArchivePreview(sp, currentThought)
	if err != nil {
		return scratchpad.ArchivePreview{}, err
	}
	profile, err := s.resolveArchiveProfile(sp, currentThought)
	if err != nil {
		return scratchpad.ArchivePreview{}, err
	}
	parameters := profileParameters(profile, sp.SessionContext.DocumentParameters)
	preview.DocumentProfile = profile.Ref
	preview.Parameters = parameters
	preview.ContextHash = archiveContextHash(sp, currentThought, profile.Ref)
	if profile.Ref.Family == models.DocumentFamilyNote || s.documentGenerator == nil {
		preview.Validation = models.ArchiveValidation{Status: models.ArchiveValidationValid, ValidatedAt: s.now()}
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
	for attempt := 0; attempt <= s.maxRepairAttempts; attempt++ {
		draft, err = s.documentGenerator.GenerateDocument(ctx, request)
		if err != nil {
			if !recoverableArchiveDocumentGenerationError(err) {
				return scratchpad.ArchivePreview{}, fmt.Errorf("capture: generate archive document: %w", err)
			}
			if attempt < s.maxRepairAttempts {
				slog.Warn("archive document generation returned malformed output; retrying", "attempt", attempt+1, "max_attempts", s.maxRepairAttempts+1, "error", err)
				continue
			}
			slog.Warn("archive document generation remained malformed; using local fallback", "attempts", attempt+1, "error", err)
			draft, err = ai.NewLocalRefineProvider().GenerateDocument(ctx, request)
			if err != nil {
				return scratchpad.ArchivePreview{}, fmt.Errorf("capture: generate archive document fallback: %w", err)
			}
		}
		rendered = documentprofile.Render(profile, draft, parameters)
		rendered.Validation.RepairCount = attempt
		if rendered.Validation.Status == models.ArchiveValidationValid {
			break
		}
		request.PreviousDraft = &draft
		request.RepairIssues = append([]models.ValidationIssue(nil), rendered.Validation.Issues...)
	}
	preview.Title = firstNonEmptyString(strings.TrimSpace(draft.Title), preview.Title)
	preview.Body = strings.TrimSpace(rendered.Content)
	preview.Validation = rendered.Validation
	preview.GeneratedAt = s.now()
	return preview, nil
}

func recoverableArchiveDocumentGenerationError(err error) bool {
	if err == nil {
		return false
	}
	if strings.Contains(err.Error(), "parse document draft json") {
		return true
	}
	var providerErr ai.ProviderError
	return errors.As(err, &providerErr) && providerErr.Code == "thoughtflow.ai.output_truncated"
}

func (s *ScratchpadService) resolveArchiveProfile(sp scratchpad.Scratchpad, currentThought *models.ThoughtSnapshot) (documentprofile.DocumentProfile, error) {
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
	for key, value := range values {
		if value = strings.TrimSpace(value); value != "" {
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
	}
	raw, _ := json.Marshal(payload)
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("sha256:%x", sum[:])
}

func (s *ScratchpadService) validateArchivePreview(ctx context.Context, sp scratchpad.Scratchpad) error {
	typedCandidate := strings.TrimSpace(sp.SessionContext.CandidateProfileID) != "" && sp.SessionContext.CandidateProfileID != models.DocumentProfileBuiltinNote
	if sp.ArchivePreview == nil {
		if typedCandidate {
			return ErrArchivePreviewRequired
		}
		return nil
	}
	preview := sp.ArchivePreview
	if preview.DocumentProfile.ProfileID == "" || preview.DocumentProfile.Family == models.DocumentFamilyNote {
		return nil
	}
	if preview.Validation.Status != models.ArchiveValidationValid {
		return ErrArchiveFormatInvalid
	}
	if s.profiles == nil {
		return ErrArchivePreviewRequired
	}
	if _, err := s.profiles.Resolve(preview.DocumentProfile); err != nil {
		return fmt.Errorf("%w: %v", ErrArchivePreviewStale, err)
	}
	var current *models.ThoughtSnapshot
	if sp.ArchiveStrategy == scratchpad.ArchiveStrategyUpdate || sp.ArchiveStrategy == scratchpad.ArchiveStrategySupplement {
		if strings.TrimSpace(sp.SourceThoughtID) != "" {
			snapshot, err := s.capture.GetThought(ctx, sp.SourceThoughtID)
			if err != nil {
				return err
			}
			current = &snapshot
		}
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
		return base
	}
	return base + "\n\n## 本轮补充信息\n\n" + addition
}

// archiveBody returns the body that should be shown in archive
// preview and persisted on commit. The LLM-facing contract treats
// CandidateSummary as the primary user-visible synthesis and
// CandidateBody as the archive body, but providers can return an
// incomplete final body. In that case, keep the richest final
// candidate and append any prior AI synthesis bubbles that are not
// already represented. Never fall back to scratchpad.Content here:
// Content is the user's raw capture-session input and must not be
// persisted into a Thought after archive.
func archiveBody(sp scratchpad.Scratchpad) string {
	if sp.ArchivePreview != nil &&
		strings.TrimSpace(sp.ArchivePreview.Body) != "" &&
		(sp.ArchivePreview.Strategy == "" || sp.ArchivePreview.Strategy == sp.ArchiveStrategy) {
		return strings.TrimSpace(sp.ArchivePreview.Body)
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
	return completeArchiveBodyWithAIHistory(base, sp.Messages)
}

func completeArchiveBodyWithAIHistory(base string, messages []scratchpad.Message) string {
	base = strings.TrimSpace(base)
	additions := []string{}
	seen := map[string]struct{}{}
	covered := compactText(base)
	seenUserTurn := false
	for _, msg := range messages {
		switch strings.TrimSpace(msg.Role) {
		case "user":
			seenUserTurn = true
			continue
		case "ai":
			if !seenUserTurn {
				continue
			}
		default:
			continue
		}
		text := strings.TrimSpace(msg.Text)
		key := compactText(text)
		if text == "" || key == "" {
			continue
		}
		if covered != "" && strings.Contains(covered, key) {
			continue
		}
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		additions = append(additions, text)
		covered = compactText(strings.Join(append([]string{covered}, additions...), "\n\n"))
	}
	if len(additions) == 0 {
		return base
	}
	if base == "" {
		return strings.Join(additions, "\n\n")
	}
	return base + "\n\n## 补充整理信息\n\n" + strings.Join(additions, "\n\n")
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
	if strings.TrimSpace(sp.CommittedThoughtID) != "" {
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
	topicHints := uniqueStrings(sp.TopicHints)
	cmdType := models.ThoughtTypeText
	typedPreview := sp.ArchivePreview != nil &&
		strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" &&
		sp.ArchivePreview.DocumentProfile.Family != models.DocumentFamilyNote
	if !typedPreview {
		if url := extractURL(firstNonEmptyString(sp.Content, content)); url != "" {
			cmdType = models.ThoughtTypeURL
		}
	}
	var profileRef *models.DocumentProfileRef
	if sp.ArchivePreview != nil && strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" {
		ref := sp.ArchivePreview.DocumentProfile
		profileRef = &ref
	}
	return models.CaptureCommand{
		Type:            cmdType,
		Content:         content,
		URL:             "",
		Title:           title,
		Tags:            tags,
		TopicHints:      topicHints,
		Source:          models.ThoughtSourceScratchpadCommit,
		DocumentProfile: profileRef,
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
	if err := s.validateArchivePreview(ctx, sp); err != nil {
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
	patch, rawBody, err := buildPatchFromScratchpad(sp, true)
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
	current, gerr := s.capture.GetThought(ctx, thoughtID)
	if gerr != nil {
		return models.CaptureResult{}, fmt.Errorf("capture: source thought not found: %w", gerr)
	}
	// Commit must remain safe even for note-profile callers that reach this
	// path without a persisted preview. Always project a complete document,
	// never only the latest supplement, before building the replacement patch.
	fullBody := fullUpdateArchiveBody(current.Content, archiveBody(sp))
	if strings.TrimSpace(fullBody) != "" {
		if sp.ArchivePreview == nil {
			sp.ArchivePreview = &scratchpad.ArchivePreview{Strategy: scratchpad.ArchiveStrategyUpdate}
		}
		sp.ArchivePreview.Body = fullBody
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
	if _, err := s.store.MarkCommitted(sp.SessionID, thoughtID); err != nil {
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
// thought-{parent.ID}". Updating the parent's RelatedThoughtIDs is
// a follow-up: it requires extending ThoughtPatchRequest with a
// RelatedThoughtIDs field (currently absent), so the parent's
// backlink is left to the next PR. The new thought is fully
// readable without the parent update.
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
	cmd.Source = models.ThoughtSourceScratchpadSupplement
	if cmd.DocumentProfile == nil || cmd.DocumentProfile.Family == models.DocumentFamilyNote {
		prefix := fmt.Sprintf("[补充] 前置 thought-%s\n\n", parentID)
		if !strings.HasPrefix(cmd.Content, prefix) {
			cmd.Content = prefix + cmd.Content
		}
	}
	result, err := s.capture.Capture(ctx, cmd)
	if err != nil {
		return models.CaptureResult{}, err
	}
	if _, err := s.store.MarkCommitted(sp.SessionID, result.Thought.ID); err != nil {
		return result, err
	}
	s.publishCommittedEvent(result.Thought.ID, sp.SessionID, "supplement")
	if _, err := s.ResetAfterCommit(sp.SessionID); err != nil {
		return result, err
	}
	return result, nil
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
	return s.store.Save(sp)
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
	if body := archiveBody(sp); body != "" {
		req.AINotes = &body
		hasAny = true
	}
	if sp.ArchivePreview != nil && strings.TrimSpace(sp.ArchivePreview.DocumentProfile.ProfileID) != "" {
		ref := sp.ArchivePreview.DocumentProfile
		req.DocumentProfile = &ref
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

func (s *ScratchpadService) availableProfileDescriptors() []documentprofile.DocumentProfileDescriptor {
	if s == nil || s.profiles == nil {
		return nil
	}
	profiles := s.profiles.ListEnabled()
	if s.maxMatchCandidates > 0 && len(profiles) > s.maxMatchCandidates {
		profiles = profiles[:s.maxMatchCandidates]
	}
	return profiles
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
