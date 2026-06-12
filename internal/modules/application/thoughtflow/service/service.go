package service

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	mcevent "github.com/muidea/magicCommon/event"
	engine "github.com/muidea/magicEngine/http"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	refinerbiz "thoughtflow/internal/modules/refiner/biz"
	searchbiz "thoughtflow/internal/modules/search/biz"
	topicbiz "thoughtflow/internal/modules/topic/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/eventstream"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/observability"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/pkg/workspace"
)

//go:embed web/*
var webAssets embed.FS

var errSynthesisThoughtIDsRequired = errors.New("thought_ids is required")

type Service struct {
	registry         engine.RouteRegistry
	captureService   *capturebiz.Service
	scratchpadSvc    *capturebiz.ScratchpadService
	refinerService   *refinerbiz.Service
	searchService    *searchbiz.Service
	topicService     *topicbiz.Service
	classifyProvider ai.ClassifyProvider
	scratchpad       scratchpadStore
	gitQueries       gitQueryReader
	jobs             jobQueryReader
	events           eventPublisher
	background       backgroundTaskAcceptor
	stream           *eventstream.Stream
	workspace        *models.Workspace
	config           appconfig.Config
	shutdownCtx      context.Context
	shutdownCancel   context.CancelFunc
}

type gitQueryReader interface {
	RecentCommits(ctx context.Context, relativePath string, resourceID string, limit int) []models.GitCommitRecord
	RuntimeStatus(ctx context.Context) models.GitRuntimeStatus
}

type jobQueryReader interface {
	Get(jobID string) (models.Job, error)
	List() ([]models.Job, error)
	RecentByResource(resourceID string, limit int) ([]models.Job, error)
	RuntimeStatus() models.BackgroundRuntimeStatus
}

// scratchpadStore is the slice of the scratchpad.Store API the HTTP
// handlers actually call. Tests can substitute a fake that records
// what was called without spinning up a real on-disk store.
type scratchpadStore interface {
	Get(sessionID string) (scratchpad.Scratchpad, error)
	Save(sp scratchpad.Scratchpad) (scratchpad.Scratchpad, error)
	Delete(sessionID string) error
	List() []scratchpad.Summary
	MarkCommitted(sessionID, thoughtID string) (scratchpad.Scratchpad, error)
	Reset(sessionID string) (scratchpad.Scratchpad, error)
	LastActive() (scratchpad.Scratchpad, bool)
}

type eventPublisher interface {
	Post(event mcevent.Event)
}

type backgroundTaskAcceptor interface {
	AsyncFunction(function func()) error
}

func New(registry engine.RouteRegistry, captureService *capturebiz.Service, scratchpadService *capturebiz.ScratchpadService, refinerService *refinerbiz.Service, searchService *searchbiz.Service, topicService *topicbiz.Service, scratchpadStore scratchpadStore, gitQueries gitQueryReader, jobs jobQueryReader, events eventPublisher, background backgroundTaskAcceptor, stream *eventstream.Stream, workspace *models.Workspace, cfg appconfig.Config) *Service {
	shutdownCtx, shutdownCancel := context.WithCancel(context.Background())
	return &Service{
		registry:         registry,
		captureService:   captureService,
		scratchpadSvc:    scratchpadService,
		refinerService:   refinerService,
		searchService:    searchService,
		topicService:     topicService,
		classifyProvider: ai.NewClassifyProvider(cfg.LLM),
		scratchpad:       scratchpadStore,
		gitQueries:       gitQueries,
		jobs:             jobs,
		events:           events,
		background:       background,
		stream:           stream,
		workspace:        workspace,
		config:           cfg,
		shutdownCtx:      shutdownCtx,
		shutdownCancel:   shutdownCancel,
	}
}

func (s *Service) Close() {
	if s.shutdownCancel != nil {
		s.shutdownCancel()
	}
}

func (s *Service) RegisterRoutes() {
	s.registry.AddHandler("/", engine.GET, s.handleWeb)
	s.registry.AddHandler("/index.html", engine.GET, s.handleWeb)
	s.registry.AddHandler("/styles.css", engine.GET, s.handleWeb)
	s.registry.AddHandler("/app.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/session-lock.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/i18n/index.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/i18n/zh-CN.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/i18n/en-US.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/vendor/markdown-it.min.js", engine.GET, s.handleWeb)
	s.registry.AddHandler("/vendor/markdown-it.LICENSE", engine.GET, s.handleWeb)
	s.registry.AddHandler("/api/thoughts", engine.POST, s.handleCreateThought)
	s.registry.AddHandler("/api/thoughts/:id/retry-refine", engine.POST, s.handleRetryRefine)
	s.registry.AddHandler("/api/thoughts/:id/suggest", engine.GET, s.handleThoughtSuggest)
	s.registry.AddHandler("/api/thoughts/:id", engine.GET, s.handleGetThought)
	s.registry.AddHandler("/api/thoughts/:id", "PATCH", s.handlePatchThought)
	s.registry.AddHandler("/api/thoughts/:id/reopen-session", engine.POST, s.handleReopenSession)
	// Capture-session resource (PRD §2.2). Old paths below are kept
	// as deprecation shims — they log a warning and forward to the
	// new handlers so existing front-ends and curl recipes keep
	// working through the transition window.
	s.registry.AddHandler("/api/capture/sessions", engine.POST, s.handleCreateSession)
	s.registry.AddHandler("/api/capture/sessions", engine.GET, s.handleListSessions)
	s.registry.AddHandler("/api/capture/sessions/active", engine.GET, s.handleGetActiveSession)
	s.registry.AddHandler("/api/capture/sessions/:id", engine.GET, s.handleGetSession)
	s.registry.AddHandler("/api/capture/sessions/:id", "DELETE", s.handleDeleteSession)
	s.registry.AddHandler("/api/capture/sessions/:id/messages", engine.POST, s.handleSessionMessage)
	s.registry.AddHandler("/api/capture/sessions/:id/context", engine.POST, s.handleSessionContext)
	s.registry.AddHandler("/api/capture/sessions/:id/intent", engine.POST, s.handleSessionIntent)
	s.registry.AddHandler("/api/capture/sessions/:id/strategy", engine.POST, s.handleSessionStrategy)
	s.registry.AddHandler("/api/capture/sessions/:id/archive/preview", engine.GET, s.handleArchivePreview)
	s.registry.AddHandler("/api/capture/sessions/:id/archive", engine.POST, s.handleSessionArchive)
	// Deprecation shims — old paths kept on purpose; see handleLegacyDeprecationLog.
	s.registry.AddHandler("/api/capture/sessions/start", engine.POST, s.handleStartCaptureSession)
	s.registry.AddHandler("/api/capture/scratchpad", engine.GET, s.handleGetScratchpad)
	s.registry.AddHandler("/api/capture/scratchpad", "POST", s.handlePostScratchpad)
	s.registry.AddHandler("/api/capture/scratchpad", "DELETE", s.handleDeleteScratchpad)
	s.registry.AddHandler("/api/capture/scratchpad/commit", engine.POST, s.handleCommitScratchpad)
	s.registry.AddHandler("/api/capture/scratchpad/list", engine.GET, s.handleListScratchpads)
	s.registry.AddHandler("/api/capture/new-session", engine.POST, s.handleNewCaptureSession)
	s.registry.AddHandler("/api/search", engine.GET, s.handleSearch)
	s.registry.AddHandler("/api/synthesis/save", engine.POST, s.handleSaveSynthesis)
	s.registry.AddHandler("/api/synthesis/:id", engine.GET, s.handleGetSynthesisDraft)
	s.registry.AddHandler("/api/synthesis", engine.GET, s.handleListSynthesisDrafts)
	s.registry.AddHandler("/api/synthesis", engine.POST, s.handleSynthesis)
	s.registry.AddHandler("/api/topics", engine.GET, s.handleListTopics)
	s.registry.AddHandler("/api/topics", engine.POST, s.handleCreateTopic)
	s.registry.AddHandler("/api/topics/:id/rebuild", engine.POST, s.handleRebuildTopic)
	s.registry.AddHandler("/api/topics/:id/weave-preview", engine.POST, s.handlePreviewWeave)
	s.registry.AddHandler("/api/topics/:id/weave-accept", engine.POST, s.handleAcceptWeave)
	s.registry.AddHandler("/api/topics/:id/weave-proposals/:proposal_id", engine.GET, s.handleGetWeaveProposal)
	s.registry.AddHandler("/api/topics/:id/weave-proposals", engine.GET, s.handleListWeaveProposals)
	s.registry.AddHandler("/api/topics/:id", engine.GET, s.handleGetTopic)
	s.registry.AddHandler("/api/topics/:id", engine.PUT, s.handleUpdateTopic)
	s.registry.AddHandler("/api/jobs", engine.GET, s.handleListJobs)
	s.registry.AddHandler("/api/jobs/:id", engine.GET, s.handleGetJob)
	s.registry.AddHandler("/api/events", engine.GET, s.handleEvents)
	s.registry.AddHandler("/api/system/status", engine.GET, s.handleSystemStatus)
	s.registry.AddHandler("/api/system/metrics", engine.GET, s.handleSystemMetrics)
	s.registry.AddHandler("/api/system/reindex", engine.POST, s.handleReindex)
	s.registry.AddHandler("/metrics", engine.GET, s.handlePrometheusMetrics)
	s.registry.AddHandler("/health/live", engine.GET, s.handleLive)
	s.registry.AddHandler("/health/ready", engine.GET, s.handleReady)
}

func (s *Service) handleWeb(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	filePath := strings.TrimPrefix(path.Clean(req.URL.Path), "/")
	if filePath == "." || filePath == "" {
		filePath = "index.html"
	}
	if strings.Contains(filePath, "..") {
		http.NotFound(res, req)
		return
	}
	raw, err := webAssets.ReadFile(path.Join("web", filePath))
	if errors.Is(err, fs.ErrNotExist) {
		http.NotFound(res, req)
		return
	}
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.ui.asset_failed", err.Error())
		return
	}
	switch path.Ext(filePath) {
	case ".css":
		res.Header().Set("Content-Type", "text/css; charset=utf-8")
	case ".js":
		res.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	case ".LICENSE":
		res.Header().Set("Content-Type", "text/plain; charset=utf-8")
	default:
		res.Header().Set("Content-Type", "text/html; charset=utf-8")
	}
	res.WriteHeader(http.StatusOK)
	_, _ = res.Write(raw)
}

func (s *Service) handleCreateThought(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var cmd models.CaptureCommand
	if err := json.NewDecoder(req.Body).Decode(&cmd); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
		return
	}
	resolved, classified := s.classifyCaptureCommand(ctx, cmd)
	if classified {
		cmd = resolved
	}
	result, err := s.captureService.Capture(ctx, cmd)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, result)
}

var classifyURLPattern = regexp.MustCompile(`(?i)\b((https?://|www\.)[^\s]+)`)

// classifyCaptureCommand fills in the Type field on a CaptureCommand when
// the caller left it empty. It runs the URL regex first (zero cost) and
// falls back to the configured ClassifyProvider. The boolean return value
// is true when the classifier actually ran — callers can use it to decide
// whether the resolved command is safe to forward.
//
// On any provider error or low-confidence response the helper degrades
// gracefully to text. We never let classification failures block a
// capture — the cost of a misclassified text is much lower than the cost
// of dropping the user's note.
func (s *Service) classifyCaptureCommand(ctx context.Context, cmd models.CaptureCommand) (models.CaptureCommand, bool) {
	if strings.TrimSpace(cmd.Type) != "" {
		return cmd, false
	}
	if strings.TrimSpace(cmd.Content) == "" {
		return cmd, false
	}
	if match := classifyURLPattern.FindStringIndex(cmd.Content); match != nil {
		url := strings.TrimRight(cmd.Content[match[0]:match[1]], ".,;:!?\"'")
		prefix := strings.TrimSpace(cmd.Content[:match[0]])
		suffix := strings.TrimSpace(cmd.Content[match[1]:])
		cmd.URL = url
		if prefix == "" && suffix == "" {
			cmd.Type = models.ThoughtTypeURL
			cmd.Content = ""
		} else {
			cmd.Type = models.ThoughtTypeURL
			cmd.Content = strings.TrimSpace(prefix + " " + suffix)
		}
		return cmd, true
	}
	if s.classifyProvider == nil {
		cmd.Type = models.ThoughtTypeText
		return cmd, true
	}
	result, err := s.classifyProvider.Classify(ctx, ai.ClassifyRequest{
		System:    ai.DefaultClassifySystem,
		User:      cmd.Content,
		MaxTokens: 64,
	})
	if err != nil {
		cmd.Type = models.ThoughtTypeText
		return cmd, true
	}
	var parsed struct {
		Type         string  `json:"type"`
		ExtractedURL string  `json:"extracted_url"`
		Confidence   float64 `json:"confidence"`
	}
	if err := json.Unmarshal([]byte(result.Raw), &parsed); err != nil {
		cmd.Type = models.ThoughtTypeText
		return cmd, true
	}
	if parsed.Confidence < 0.5 {
		cmd.Type = models.ThoughtTypeText
		return cmd, true
	}
	switch parsed.Type {
	case "url", "mixed":
		if strings.TrimSpace(parsed.ExtractedURL) == "" {
			cmd.Type = models.ThoughtTypeText
			return cmd, true
		}
		cmd.Type = models.ThoughtTypeURL
		cmd.URL = parsed.ExtractedURL
		if parsed.Type == "url" && strings.Contains(cmd.Content, parsed.ExtractedURL) {
			cmd.Content = strings.TrimSpace(strings.ReplaceAll(cmd.Content, parsed.ExtractedURL, ""))
		}
	default:
		cmd.Type = models.ThoughtTypeText
	}
	return cmd, true
}

// handleStartCaptureSession initializes a scratchpad-based chat
// session. The first user turn is appended to the scratchpad's
// message log; no thought file is written, no LLM call is fired, no
// git commit is produced. The frontend receives the freshly-staged
// scratchpad and can keep chatting from there. Committing
// (POST /api/capture/scratchpad/commit) is the only path that turns
// a scratchpad into a real thought.
//
// Behavior:
//   - body.session_id is required (the UI should call
//     /api/capture/new-session first if it has no id).
//   - body.content is the first user message; it gets appended to
//     the scratchpad's Messages and copied into Content so a
//     subsequent commit has something to capture.
//   - The response shape is still CaptureSessionStart for backward
//     compatibility, but Thought / Jobs / Suggestion are zero-valued
//     and Scratchpad carries the new state.
func (s *Service) handleStartCaptureSession(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	logDeprecation(req, "POST /api/capture/sessions/start → POST /api/capture/sessions")
	_ = ctx
	if s.scratchpad == nil || s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	var body struct {
		Content   string `json:"content"`
		SessionID string `json:"session_id"`
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(body.Content) == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", "content is required")
		return
	}
	sessionID := strings.TrimSpace(body.SessionID)
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	sp, err := s.scratchpadSvc.AppendMessage(sessionID, "user", body.Content)
	if err != nil {
		if errors.Is(err, capturebiz.ErrScratchpadUnavailable) {
			writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", err.Error())
			return
		}
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, models.CaptureSessionStart{
		SessionID:  sessionID,
		Thought:    models.Thought{},
		Jobs:       []models.Job{},
		Suggestion: models.ThoughtSuggestion{},
		Scratchpad: sp,
	})
}

// buildCaptureSuggestion asks the refiner for a quick title/tag
// suggestion. It is best-effort: any error degrades to a nil suggestion
// rather than failing the session start, because the user can still see
// the thought content even without an AI suggestion.
func (s *Service) buildCaptureSuggestion(ctx context.Context, thoughtID string) (models.ThoughtSuggestion, error) {
	if s.refinerService == nil {
		return models.ThoughtSuggestion{}, nil
	}
	return s.refinerService.Suggest(ctx, thoughtID)
}

func (s *Service) handleGetThought(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	thoughtID := pathID(req.URL.Path, "/api/thoughts/")
	if thoughtID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", "thought id is required")
		return
	}
	snapshot, err := s.captureService.GetThought(ctx, thoughtID)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file") {
			status = http.StatusNotFound
		}
		writeError(res, req, status, "thoughtflow.capture.not_found", err.Error())
		return
	}
	snapshot.Jobs = recentJobsByResource(s.jobs, thoughtID, 20)
	if s.gitQueries != nil {
		snapshot.GitCommits = s.gitQueries.RecentCommits(ctx, snapshot.Thought.Path, thoughtID, 5)
	}
	writeJSON(res, req, http.StatusOK, snapshot)
}

func (s *Service) handlePatchThought(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	thoughtID := pathID(req.URL.Path, "/api/thoughts/")
	if thoughtID == "" || strings.Contains(thoughtID, "/") {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", "thought id is required")
		return
	}
	sessionID := strings.TrimSpace(req.Header.Get("X-Session-Id"))
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.session_required", "X-Session-Id header is required")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var patch models.ThoughtPatchRequest
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &patch); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_patch", err.Error())
			return
		}
	}
	snapshot, err := s.captureService.PatchThought(ctx, thoughtID, sessionID, patch, rawBody)
	if err != nil {
		switch {
		case errors.Is(err, capturebiz.ErrInvalidPatchField):
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_patch", err.Error())
		case errors.Is(err, capturebiz.ErrLocked):
			writeError(res, req, http.StatusConflict, "thoughtflow.capture.locked", err.Error())
		case errors.Is(err, capturebiz.ErrRefining):
			writeError(res, req, http.StatusConflict, "thoughtflow.capture.refining", err.Error())
		case errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "no such file"):
			writeError(res, req, http.StatusNotFound, "thoughtflow.capture.not_found", err.Error())
		default:
			if strings.Contains(err.Error(), "must not be empty") {
				writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_patch", err.Error())
				return
			}
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.patch_failed", err.Error())
		}
		return
	}
	snapshot.Jobs = recentJobsByResource(s.jobs, thoughtID, 20)
	if s.gitQueries != nil {
		snapshot.GitCommits = s.gitQueries.RecentCommits(ctx, snapshot.Thought.Path, thoughtID, 5)
	}
	writeJSON(res, req, http.StatusOK, snapshot)
}

func (s *Service) handleRetryRefine(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	thoughtID := strings.TrimSuffix(pathID(req.URL.Path, "/api/thoughts/"), "/retry-refine")
	if thoughtID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.refiner.invalid_request", "thought id is required")
		return
	}
	job, err := s.refinerService.RetryRefineAsync(thoughtID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.refiner.invalid_request", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, job)
}

// handleGetScratchpad returns the scratchpad for a given session. A
// non-existent session is returned as an empty scratchpad (200 + zero
// value) so the UI can render an empty composer without a 404 dance.
//
//	GET /api/capture/scratchpad?session_id=X
func (s *Service) handleGetScratchpad(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	logDeprecation(req, "GET /api/capture/scratchpad → GET /api/capture/sessions/{id}")
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	sessionID := strings.TrimSpace(req.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	sp, err := s.scratchpad.Get(sessionID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, sp)
}

// handlePostScratchpad upserts a scratchpad. The frontend uses this
// to push the latest chat state (content + messages + draft) after
// every user turn; the body is a full Scratchpad minus session_id
// which is taken from the body itself.
//
//	POST /api/capture/scratchpad
//	body: Scratchpad (or partial — zero values mean "no change")
func (s *Service) handlePostScratchpad(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	logDeprecation(req, "POST /api/capture/scratchpad → POST /api/capture/sessions")
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 4<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var in scratchpad.Scratchpad
	if err := json.Unmarshal(rawBody, &in); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", err.Error())
		return
	}
	if strings.TrimSpace(in.SessionID) == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	if in.WorkspaceID == "" {
		in.WorkspaceID = s.workspace.ID
	}
	saved, err := s.scratchpad.Save(in)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, saved)
}

// handleDeleteScratchpad wipes a scratchpad. Missing sessions are
// not an error: the UI can issue DELETE on first send of a fresh
// session without special-casing.
//
//	DELETE /api/capture/scratchpad?session_id=X
func (s *Service) handleDeleteScratchpad(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	logDeprecation(req, "DELETE /api/capture/scratchpad → DELETE /api/capture/sessions/{id}")
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	sessionID := strings.TrimSpace(req.URL.Query().Get("session_id"))
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	if err := s.scratchpad.Delete(sessionID); err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, map[string]any{"deleted": true})
}

// handleCommitScratchpad is the "归档" path. The frontend calls this
// when the user says "归档" / "commit" / "save" in chat. The actual
// thought creation + event publishing is wired up in the next stage
// (see scratchpad.go); for now this handler is a stub that returns
// 501 Not Implemented so the route exists and can be smoke-tested
// without breaking the existing capture flow.
//
//	POST /api/capture/scratchpad/commit
//	body: {session_id: string}
func (s *Service) handleCommitScratchpad(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	logDeprecation(req, "POST /api/capture/scratchpad/commit → POST /api/capture/sessions/{id}/archive")
	_ = ctx
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
	}
	if err := json.Unmarshal(rawBody, &body); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
		return
	}
	if strings.TrimSpace(body.SessionID) == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	sp, err := s.scratchpad.Get(body.SessionID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", err.Error())
		return
	}
	if strings.TrimSpace(sp.Content) == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.empty_commit", "scratchpad content is empty")
		return
	}
	result, err := s.commitScratchpad(ctx, sp)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.commit_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, result)
}

// handleListScratchpads returns the diagnostic summary list. Used by
// the capture-sessions-drawer UI to show every existing scratchpad
// (active + committed) so the user can switch between them.
//
//	GET /api/capture/scratchpad/list
func (s *Service) handleListScratchpads(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	logDeprecation(req, "GET /api/capture/scratchpad/list → GET /api/capture/sessions")
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	payload := map[string]any{
		"summaries": s.scratchpad.List(),
	}
	// last_active_session_id is the scratchpad the front-end boot
	// path should land the user on when the capture page opens. It
	// is the most recently updated *uncommitted* scratchpad — once
	// a scratchpad has been committed, it has no content left to
	// chat against, so rehydrating it would surface a confusing
	// empty state. Omitted when no uncommitted scratchpad exists.
	if last, ok := s.scratchpad.LastActive(); ok {
		payload["last_active_session_id"] = last.SessionID
	}
	writeJSON(res, req, http.StatusOK, payload)
}

// handleNewCaptureSession explicitly opens a fresh scratchpad by
// generating a new session_id. The old scratchpad is deleted so the
// previous chat history does not leak into the new session. The
// request body may carry {prev_session_id?: string} to delete the
// old one; when omitted, the new session just starts empty.
//
//	POST /api/capture/new-session
//	body: {prev_session_id?: string}
func (s *Service) handleNewCaptureSession(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	logDeprecation(req, "POST /api/capture/new-session → POST /api/capture/sessions (reuse_last=false)")
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body struct {
		PrevSessionID string `json:"prev_session_id"`
	}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	prev := strings.TrimSpace(body.PrevSessionID)
	if prev != "" {
		if err := s.scratchpad.Delete(prev); err != nil {
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
			return
		}
	}
	newID := models.NewEventID(time.Now().UTC())
	fresh := scratchpad.Scratchpad{
		SessionID:   newID,
		WorkspaceID: s.workspace.ID,
	}
	saved, err := s.scratchpad.Save(fresh)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, saved)
}

// commitScratchpad delegates to the ScratchpadService's Commit
// method, which knows how to handle both the first-time capture
// (BuildCaptureCommand + captureService.Capture + MarkCommitted)
// and the repeat-commit "继续追加" path (PATCH the existing
// thought). See capture/biz/scratchpad.go for the full state
// machine.
func (s *Service) commitScratchpad(ctx context.Context, sp scratchpad.Scratchpad) (models.CaptureResult, error) {
	if s.scratchpadSvc == nil {
		return models.CaptureResult{}, errors.New("scratchpad service is not wired up")
	}
	return s.scratchpadSvc.Commit(ctx, sp.SessionID)
}

func (s *Service) handleThoughtSuggest(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	if s.refinerService == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.refiner.unavailable", "refiner service is not ready")
		return
	}
	thoughtID := strings.TrimSuffix(pathID(req.URL.Path, "/api/thoughts/"), "/suggest")
	if thoughtID == "" || strings.Contains(thoughtID, "/") {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.refiner.invalid_request", "thought id is required")
		return
	}
	suggestion, err := s.refinerService.Suggest(ctx, thoughtID)
	if err != nil {
		if strings.Contains(err.Error(), "no such file") {
			writeError(res, req, http.StatusNotFound, "thoughtflow.capture.not_found", err.Error())
			return
		}
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.refiner.suggest_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, suggestion)
}

func recentJobsByResource(store jobQueryReader, resourceID string, limit int) []models.Job {
	if store == nil || strings.TrimSpace(resourceID) == "" {
		return nil
	}
	jobs, err := store.RecentByResource(resourceID, limit)
	if err != nil {
		return nil
	}
	return jobs
}

func (s *Service) handleSearch(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	observability.IncrementSearchQuery()
	query := req.URL.Query()
	from, err := timeQuery(query.Get("from"), false)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.search.invalid_request", "from must be RFC3339 or YYYY-MM-DD")
		return
	}
	to, err := timeQuery(query.Get("to"), true)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.search.invalid_request", "to must be RFC3339 or YYYY-MM-DD")
		return
	}
	if !from.IsZero() && !to.IsZero() && from.After(to) {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.search.invalid_request", "from must be before to")
		return
	}
	searchQuery := models.SearchQuery{
		Query:    query.Get("q"),
		Mode:     firstNonEmpty(query.Get("mode"), "hybrid"),
		Sort:     query.Get("sort"),
		TopicID:  query.Get("topic_id"),
		Tags:     splitCSV(query.Get("tags")),
		From:     from,
		To:       to,
		Page:     intQuery(query.Get("page"), 1),
		PageSize: intQuery(query.Get("page_size"), 20),
		Explain:  boolQuery(query.Get("explain")),
		Weights: models.SearchWeights{
			Keyword:  floatQuery(firstNonEmpty(query.Get("keyword_weight"), query.Get("weight_keyword")), 0),
			Semantic: floatQuery(firstNonEmpty(query.Get("semantic_weight"), query.Get("weight_semantic")), 0),
			Recency:  floatQuery(firstNonEmpty(query.Get("recency_weight"), query.Get("weight_recency")), 0),
		},
	}
	result, err := s.searchService.Search(ctx, searchQuery)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.search.query_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, result)
}

func (s *Service) handleSynthesis(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var request models.SynthesisRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_json", err.Error())
		return
	}
	if len(request.ThoughtIDs) == 0 {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "thought_ids is required")
		return
	}
	if s.refinerService == nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.refiner_unavailable", "refiner service is not ready")
		return
	}
	snapshots, sourceLinks, err := s.synthesisSources(ctx, request.ThoughtIDs)
	if err != nil {
		writeSynthesisSourceError(res, req, err)
		return
	}
	draft, err := s.refinerService.CreateSynthesisDraft(ctx, request, snapshots, sourceLinks)
	if err != nil {
		var storeErr refinerbiz.SynthesisDraftStoreError
		if errors.As(err, &storeErr) {
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.draft_failed", err.Error())
			return
		}
		writeError(res, req, http.StatusBadGateway, "thoughtflow.synthesis.generate_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, draft)
}

func (s *Service) handleListSynthesisDrafts(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	if s.refinerService == nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.refiner_unavailable", "refiner service is not ready")
		return
	}
	drafts, err := s.refinerService.ListSynthesisDrafts(ctx)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.list_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, drafts)
}

func (s *Service) handleGetSynthesisDraft(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	draftID := pathID(req.URL.Path, "/api/synthesis/")
	if draftID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "draft id is required")
		return
	}
	if s.refinerService == nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.refiner_unavailable", "refiner service is not ready")
		return
	}
	draft, err := s.refinerService.GetSynthesisDraft(ctx, draftID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.synthesis.draft_not_found", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, draft)
}

func (s *Service) handleSaveSynthesis(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var request models.SynthesisSaveRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_json", err.Error())
		return
	}
	if s.refinerService == nil && strings.TrimSpace(request.DraftID) != "" {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.synthesis.refiner_unavailable", "refiner service is not ready")
		return
	}
	if strings.TrimSpace(request.DraftID) != "" {
		draft, err := s.refinerService.GetSynthesisDraft(ctx, request.DraftID)
		if err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.draft_not_found", err.Error())
			return
		}
		if len(request.ThoughtIDs) == 0 {
			request.ThoughtIDs = draft.ThoughtIDs
		}
		if strings.TrimSpace(request.Goal) == "" {
			request.Goal = draft.Goal
		}
		if strings.TrimSpace(request.Format) == "" {
			request.Format = draft.Format
		}
		if strings.TrimSpace(request.Content) == "" {
			request.Content = draft.Content
		}
	}
	if len(request.ThoughtIDs) == 0 {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "thought_ids is required")
		return
	}
	if strings.TrimSpace(request.Content) == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", "content is required")
		return
	}
	_, sourceLinks, err := s.synthesisSources(ctx, request.ThoughtIDs)
	if err != nil {
		writeSynthesisSourceError(res, req, err)
		return
	}
	format := firstNonEmpty(request.Format, "summary")
	title := firstNonEmpty(request.Title, request.Goal, "Synthesis draft")
	content := appendSynthesisSourceLinks(request.Content, sourceLinks)
	tags := []string{"synthesis"}
	if format != "" {
		tags = append(tags, format)
	}
	result, err := s.captureService.Capture(ctx, models.CaptureCommand{
		Type:    models.ThoughtTypeText,
		Source:  models.ThoughtSourceSynthesis,
		Title:   title,
		Content: content,
		Tags:    tags,
	})
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.save_failed", err.Error())
		return
	}
	if strings.TrimSpace(request.DraftID) != "" {
		if _, err := s.refinerService.MarkSynthesisSaved(ctx, request.DraftID, content, result.Thought); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.draft_save_failed", err.Error())
			return
		}
	}
	writeJSON(res, req, http.StatusAccepted, models.SynthesisSaveResult{
		Thought:     result.Thought,
		Jobs:        result.Jobs,
		SourceLinks: sourceLinks,
	})
}

func (s *Service) synthesisSources(ctx context.Context, thoughtIDs []string) ([]models.ThoughtSnapshot, []string, error) {
	snapshots := []models.ThoughtSnapshot{}
	sourceLinks := []string{}
	seen := map[string]struct{}{}
	for _, thoughtID := range thoughtIDs {
		thoughtID = strings.TrimSpace(thoughtID)
		if thoughtID == "" {
			continue
		}
		if _, ok := seen[thoughtID]; ok {
			continue
		}
		seen[thoughtID] = struct{}{}
		snapshot, err := s.captureService.GetThought(ctx, thoughtID)
		if err != nil {
			return nil, nil, err
		}
		snapshots = append(snapshots, snapshot)
		sourceLinks = append(sourceLinks, snapshot.Thought.Path)
	}
	if len(snapshots) == 0 {
		return nil, nil, errSynthesisThoughtIDsRequired
	}
	return snapshots, sourceLinks, nil
}

func writeSynthesisSourceError(res http.ResponseWriter, req *http.Request, err error) {
	if errors.Is(err, errSynthesisThoughtIDsRequired) {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.synthesis.invalid_request", err.Error())
		return
	}
	writeError(res, req, http.StatusNotFound, "thoughtflow.synthesis.thought_not_found", err.Error())
}

func appendSynthesisSourceLinks(content string, sourceLinks []string) string {
	content = strings.TrimSpace(content)
	missing := []string{}
	for _, link := range sourceLinks {
		link = strings.TrimSpace(link)
		if link == "" || strings.Contains(content, link) {
			continue
		}
		missing = append(missing, link)
	}
	if len(missing) == 0 {
		return content
	}
	var builder strings.Builder
	builder.WriteString(content)
	if strings.Contains(strings.ToLower(content), "### sources") {
		builder.WriteString("\n")
	} else {
		builder.WriteString("\n\n### Sources\n\n")
	}
	for _, link := range missing {
		builder.WriteString("- [[")
		builder.WriteString(link)
		builder.WriteString("]]\n")
	}
	return strings.TrimSpace(builder.String())
}

func (s *Service) handleListTopics(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topics, err := s.topicService.ListTopics(ctx)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.topic.list_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, topics)
}

func (s *Service) handleCreateTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	var request models.TopicCreateRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	topic, err := s.topicService.CreateTopic(ctx, request)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", err.Error())
		return
	}
	writeJSON(res, req, http.StatusCreated, topic)
}

func (s *Service) handleGetTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := pathID(req.URL.Path, "/api/topics/")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	detail, err := s.topicService.GetTopic(ctx, topicID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.topic.not_found", err.Error())
		return
	}
	detail.Activities = topicActivities(s.stream, topicID, 20)
	writeJSON(res, req, http.StatusOK, detail)
}

func topicActivities(stream *eventstream.Stream, topicID string, limit int) []models.DomainEvent {
	if stream == nil || strings.TrimSpace(topicID) == "" {
		return nil
	}
	history := stream.History()
	activities := []models.DomainEvent{}
	for idx := len(history) - 1; idx >= 0; idx-- {
		item := history[idx]
		if !isTopicActivity(item, topicID) {
			continue
		}
		activities = append(activities, item)
		if limit > 0 && len(activities) >= limit {
			break
		}
	}
	for left, right := 0, len(activities)-1; left < right; left, right = left+1, right-1 {
		activities[left], activities[right] = activities[right], activities[left]
	}
	return activities
}

func isTopicActivity(event models.DomainEvent, topicID string) bool {
	if event.ResourceType == models.ResourceTypeTopic && event.ResourceID == topicID {
		return true
	}
	if strings.HasPrefix(event.EventType, "topic.") && payloadReferencesTopic(event.Payload, topicID) {
		return true
	}
	return false
}

func payloadReferencesTopic(payload any, topicID string) bool {
	switch value := payload.(type) {
	case models.Topic:
		return value.ID == topicID
	case *models.Topic:
		return value != nil && value.ID == topicID
	case models.TopicMembership:
		return value.TopicID == topicID
	case *models.TopicMembership:
		return value != nil && value.TopicID == topicID
	case []models.TopicMembership:
		for _, membership := range value {
			if membership.TopicID == topicID {
				return true
			}
		}
	case []*models.TopicMembership:
		for _, membership := range value {
			if membership != nil && membership.TopicID == topicID {
				return true
			}
		}
	case map[string]any:
		if topicValue, ok := value["topic"]; ok && payloadReferencesTopic(topicValue, topicID) {
			return true
		}
		if topicIDValue, ok := value["topic_id"].(string); ok && topicIDValue == topicID {
			return true
		}
	}
	return false
}

func (s *Service) handleUpdateTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := pathID(req.URL.Path, "/api/topics/")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	var request models.TopicUpdateRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	topic, err := s.topicService.UpdateTopic(ctx, topicID, request)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.update_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, topic)
}

func (s *Service) handleRebuildTopic(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/rebuild")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	job, err := s.topicService.RebuildTopic(req.Context(), topicID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.rebuild_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, job)
}

func (s *Service) handlePreviewWeave(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/weave-preview")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	var request models.TopicWeavePreviewRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	proposal, err := s.topicService.PreviewWeave(ctx, topicID, request.ThoughtID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.weave_preview_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, proposal)
}

func (s *Service) handleAcceptWeave(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/weave-accept")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	var request models.TopicWeaveAcceptRequest
	if err := json.NewDecoder(req.Body).Decode(&request); err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_json", err.Error())
		return
	}
	detail, err := s.topicService.AcceptWeave(ctx, topicID, request)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.weave_accept_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, detail)
}

func (s *Service) handleListWeaveProposals(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID := strings.TrimSuffix(pathID(req.URL.Path, "/api/topics/"), "/weave-proposals")
	if topicID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id is required")
		return
	}
	proposals, err := s.topicService.ListWeaveProposals(ctx, topicID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.weave_proposals_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, proposals)
}

func (s *Service) handleGetWeaveProposal(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	topicID, proposalID := topicWeaveProposalPath(req.URL.Path)
	if topicID == "" || proposalID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.topic.invalid_request", "topic id and proposal id are required")
		return
	}
	proposal, err := s.topicService.GetWeaveProposal(ctx, topicID, proposalID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.topic.weave_proposal_not_found", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, proposal)
}

func (s *Service) handleGetJob(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	jobID := pathID(req.URL.Path, "/api/jobs/")
	if jobID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.system.invalid_request", "job id is required")
		return
	}
	job, err := s.jobs.Get(jobID)
	if err != nil {
		writeError(res, req, http.StatusNotFound, "thoughtflow.system.job_not_found", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, job)
}

// handleListJobs powers the diagnostic "runtime" cards in the web UI
// (Notes page status panel, system metrics rollup). The frontend
// calls /api/jobs?limit=8 and expects the data field to be a JSON
// array of Job records, most-recent-first. Optional query filters:
//   - type=refine|expand|index|git_commit|... narrows by job type
//   - status=queued|running|succeeded|failed|... narrows by status
//   - resource_id=<thought-id> restricts to one thought's jobs
//   - limit=N keeps only the most recent N (after filtering)
//
// The store's List() orders ASC by created_at; the runtime card
// wants the latest job first, so we sort DESC after filtering. We
// copy the slice before mutating it so the in-memory job order in
// the store stays untouched.
func (s *Service) handleListJobs(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.jobs == nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.jobs.unavailable", "job store is not ready")
		return
	}
	jobs, err := s.jobs.List()
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.jobs.list_failed", err.Error())
		return
	}
	jobs = filterAndSortJobs(jobs, req.URL.Query())
	writeJSON(res, req, http.StatusOK, jobs)
}

func filterAndSortJobs(jobs []models.Job, query url.Values) []models.Job {
	if len(jobs) == 0 {
		return jobs
	}
	sorted := make([]models.Job, len(jobs))
	copy(sorted, jobs)
	sort.Slice(sorted, func(i, j int) bool {
		if sorted[i].CreatedAt.Equal(sorted[j].CreatedAt) {
			return sorted[i].ID < sorted[j].ID
		}
		return sorted[i].CreatedAt.After(sorted[j].CreatedAt)
	})
	if typeFilter := strings.TrimSpace(query.Get("type")); typeFilter != "" {
		sorted = filterJobsByField(sorted, "type", typeFilter)
	}
	if statusFilter := strings.TrimSpace(query.Get("status")); statusFilter != "" {
		sorted = filterJobsByField(sorted, "status", statusFilter)
	}
	if resourceFilter := strings.TrimSpace(query.Get("resource_id")); resourceFilter != "" {
		sorted = filterJobsByField(sorted, "resource_id", resourceFilter)
	}
	if limit := parseJobsLimit(query.Get("limit")); limit > 0 && limit < len(sorted) {
		sorted = sorted[:limit]
	}
	return sorted
}

func filterJobsByField(jobs []models.Job, field string, want string) []models.Job {
	filtered := jobs[:0]
	for _, job := range jobs {
		var got string
		switch field {
		case "type":
			got = job.Type
		case "status":
			got = job.Status
		case "resource_id":
			got = job.ResourceID
		}
		if got == want {
			filtered = append(filtered, job)
		}
	}
	return filtered
}

func parseJobsLimit(raw string) int {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		return 0
	}
	return value
}

func (s *Service) handleEvents(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	res.Header().Set("Content-Type", "text/event-stream")
	res.Header().Set("Cache-Control", "no-cache")
	res.Header().Set("Connection", "keep-alive")

	flusher, ok := res.(http.Flusher)
	if !ok {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.system.sse_unavailable", "streaming is not supported")
		return
	}
	streamCtx, cancel := context.WithCancel(req.Context())
	defer cancel()
	if s.shutdownCtx != nil {
		go func() {
			select {
			case <-s.shutdownCtx.Done():
				cancel()
			case <-streamCtx.Done():
			}
		}()
	}
	events := s.stream.SubscribeWithOptions(streamCtx, eventstream.SubscribeOptions{
		LastEventID: req.Header.Get("Last-Event-ID"),
		Types:       splitCSV(req.URL.Query().Get("types")),
	})
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-streamCtx.Done():
			return
		case ev, ok := <-events:
			if !ok {
				return
			}
			raw, _ := json.Marshal(ev)
			_, _ = fmt.Fprintf(res, "event: %s\nid: %s\ndata: %s\n\n", ev.EventType, ev.EventID, raw)
			flusher.Flush()
		case <-ticker.C:
			_, _ = fmt.Fprint(res, ": ping\n\n")
			flusher.Flush()
		}
	}
}

func (s *Service) handleSystemStatus(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	status := s.systemStatus(ctx, s.config)
	writeJSON(res, req, http.StatusOK, status)
}

func (s *Service) handleSystemMetrics(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	metrics, err := s.systemMetrics(ctx, time.Now().UTC())
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.system.metrics_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, metrics)
}

func (s *Service) handlePrometheusMetrics(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	metrics, err := s.systemMetrics(ctx, time.Now().UTC())
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.system.metrics_failed", err.Error())
		return
	}
	_ = req
	res.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	res.WriteHeader(http.StatusOK)
	_, _ = res.Write([]byte(renderPrometheusMetrics(metrics)))
}

func (s *Service) systemMetrics(ctx context.Context, now time.Time) (models.SystemMetrics, error) {
	_ = ctx
	counters := observability.Snapshot()
	jobs := []models.Job{}
	if s.jobs != nil {
		listed, err := s.jobs.List()
		if err != nil {
			return models.SystemMetrics{}, err
		}
		jobs = listed
	}
	thoughts := []models.Thought{}
	if s.captureService != nil {
		listed, err := s.captureService.ListThoughts(ctx)
		if err != nil {
			return models.SystemMetrics{}, err
		}
		thoughts = listed
	}

	captureTotal := len(thoughts)
	gitCommitTotal := 0
	for _, job := range jobs {
		if job.Type == models.JobTypeGitCommit && job.Status == models.JobStatusSucceeded {
			gitCommitTotal++
		}
	}
	refineDuration := refineDurationMetric(jobs)
	backgroundJobs := backgroundJobsMetric(jobs)
	indexLag := thoughtIndexLagMetric(thoughts, now)
	values := map[string]float64{
		"thoughtflow_capture_total":           float64(captureTotal),
		"thoughtflow_refine_duration_seconds": refineDuration.Average,
		"thoughtflow_ai_request_total":        float64(counters.AIRequestTotal),
		"thoughtflow_search_query_total":      float64(counters.SearchQueryTotal),
		"thoughtflow_index_lag_seconds":       indexLag.Seconds,
		"thoughtflow_topic_weave_total":       float64(counters.TopicWeaveTotal),
		"thoughtflow_git_commit_total":        float64(gitCommitTotal),
		"thoughtflow_background_jobs":         float64(backgroundJobs.Total),
	}
	return models.SystemMetrics{
		GeneratedAt:           now,
		Values:                values,
		RefineDurationSeconds: refineDuration,
		BackgroundJobs:        backgroundJobs,
		ThoughtIndexLag:       indexLag,
	}, nil
}

func refineDurationMetric(jobs []models.Job) models.DurationMetric {
	metric := models.DurationMetric{}
	for _, job := range jobs {
		if job.Type != models.JobTypeRefine || job.StartedAt == nil || job.FinishedAt == nil {
			continue
		}
		duration := job.FinishedAt.Sub(*job.StartedAt).Seconds()
		if duration < 0 {
			continue
		}
		metric.Count++
		metric.Sum += duration
		metric.Latest = duration
	}
	if metric.Count > 0 {
		metric.Average = metric.Sum / float64(metric.Count)
	}
	return metric
}

func backgroundJobsMetric(jobs []models.Job) models.BackgroundJobsMetric {
	metric := models.BackgroundJobsMetric{
		Total:    len(jobs),
		ByStatus: map[string]int{},
		ByType:   map[string]int{},
	}
	for _, job := range jobs {
		if job.Status != "" {
			metric.ByStatus[job.Status]++
		}
		if job.Type != "" {
			metric.ByType[job.Type]++
		}
	}
	return metric
}

func thoughtIndexLagMetric(thoughts []models.Thought, now time.Time) models.ThoughtIndexLagMetric {
	metric := models.ThoughtIndexLagMetric{}
	for _, thought := range thoughts {
		switch thought.IndexStatus {
		case models.IndexStatusIndexed:
			continue
		case models.IndexStatusFailed:
			metric.FailedThoughts++
		default:
			metric.PendingThoughts++
		}
		updatedAt := thought.UpdatedAt
		if updatedAt.IsZero() {
			updatedAt = thought.CreatedAt
		}
		if updatedAt.IsZero() {
			continue
		}
		lag := now.Sub(updatedAt).Seconds()
		if lag > metric.Seconds {
			metric.Seconds = lag
		}
	}
	return metric
}

func renderPrometheusMetrics(metrics models.SystemMetrics) string {
	var builder strings.Builder
	writePrometheusSample(&builder, "thoughtflow_capture_total", "counter", "Total captured thoughts.", metrics.Values["thoughtflow_capture_total"])
	writePrometheusSample(&builder, "thoughtflow_refine_duration_seconds", "gauge", "Average thought refinement duration in seconds.", metrics.Values["thoughtflow_refine_duration_seconds"])
	writePrometheusSample(&builder, "thoughtflow_ai_request_total", "counter", "Total LLM and embedding provider requests.", metrics.Values["thoughtflow_ai_request_total"])
	writePrometheusSample(&builder, "thoughtflow_search_query_total", "counter", "Total search queries.", metrics.Values["thoughtflow_search_query_total"])
	writePrometheusSample(&builder, "thoughtflow_index_lag_seconds", "gauge", "Maximum lag for non-indexed thoughts in seconds.", metrics.Values["thoughtflow_index_lag_seconds"])
	writePrometheusSample(&builder, "thoughtflow_topic_weave_total", "counter", "Total topic weave operations.", metrics.Values["thoughtflow_topic_weave_total"])
	writePrometheusSample(&builder, "thoughtflow_git_commit_total", "counter", "Total successful git commit jobs.", metrics.Values["thoughtflow_git_commit_total"])
	writePrometheusSample(&builder, "thoughtflow_background_jobs", "gauge", "Total persisted background jobs.", metrics.Values["thoughtflow_background_jobs"])
	writePrometheusSample(&builder, "thoughtflow_refine_duration_seconds_sum", "counter", "Total refinement duration in seconds.", metrics.RefineDurationSeconds.Sum)
	writePrometheusSample(&builder, "thoughtflow_refine_duration_seconds_count", "counter", "Total completed refinement jobs.", float64(metrics.RefineDurationSeconds.Count))
	for status, count := range metrics.BackgroundJobs.ByStatus {
		writePrometheusLabeledSample(&builder, "thoughtflow_background_jobs", map[string]string{"status": status}, float64(count))
	}
	for jobType, count := range metrics.BackgroundJobs.ByType {
		writePrometheusLabeledSample(&builder, "thoughtflow_background_jobs", map[string]string{"type": jobType}, float64(count))
	}
	return builder.String()
}

func writePrometheusSample(builder *strings.Builder, name string, metricType string, help string, value float64) {
	_, _ = fmt.Fprintf(builder, "# HELP %s %s\n", name, help)
	_, _ = fmt.Fprintf(builder, "# TYPE %s %s\n", name, metricType)
	_, _ = fmt.Fprintf(builder, "%s %.6f\n", name, value)
}

func writePrometheusLabeledSample(builder *strings.Builder, name string, labels map[string]string, value float64) {
	parts := []string{}
	for key, value := range labels {
		parts = append(parts, fmt.Sprintf("%s=%q", key, sanitizePrometheusLabel(value)))
	}
	_, _ = fmt.Fprintf(builder, "%s{%s} %.6f\n", name, strings.Join(parts, ","), value)
}

func sanitizePrometheusLabel(value string) string {
	value = strings.ReplaceAll(value, "\\", "\\\\")
	value = strings.ReplaceAll(value, "\n", "\\n")
	return strings.ReplaceAll(value, "\"", "\\\"")
}

func (s *Service) systemStatus(ctx context.Context, cfg appconfig.Config) models.SystemStatus {
	workspaceStatus := workspace.RuntimeStatus(s.workspace)
	duckdbStatus := models.DuckDBRuntimeStatus{Status: "degraded", Error: "search service is not ready"}
	if s.searchService != nil {
		duckdbStatus = s.searchService.RuntimeStatus(ctx)
	}
	llmStatus := llmRuntimeStatus(cfg)
	embeddingStatus := embeddingRuntimeStatus(cfg)
	gitStatus := models.GitRuntimeStatus{Status: "disabled"}
	if s.gitQueries != nil {
		gitStatus = s.gitQueries.RuntimeStatus(ctx)
	}
	backgroundStatus := models.BackgroundRuntimeStatus{Status: "degraded", Error: "job store is not ready"}
	if s.jobs != nil {
		backgroundStatus = s.jobs.RuntimeStatus()
	}
	backgroundStatus = backgroundRuntimeStatus(backgroundStatus, s.background)
	eventsStatus := eventsRuntimeStatus(s.stream, s.events)

	ready := workspaceStatus.Status == "ready" &&
		duckdbStatus.Status == "ready" &&
		backgroundStatus.Status == "ready" &&
		eventsStatus.Status == "ready"
	status := "ready"
	if !ready || llmStatus.Status != "ready" || embeddingStatus.Status != "ready" || gitStatus.Status == "degraded" {
		status = "degraded"
	}
	return models.SystemStatus{
		Status:     status,
		Ready:      ready,
		Workspace:  workspaceStatus,
		DuckDB:     duckdbStatus,
		LLM:        llmStatus,
		Embedding:  embeddingStatus,
		Git:        gitStatus,
		Background: backgroundStatus,
		Events:     eventsStatus,
	}
}

func llmRuntimeStatus(cfg appconfig.Config) models.LLMRuntimeStatus {
	status := "not_configured"
	configured := strings.TrimSpace(cfg.LLM.APIKey) != ""
	if configured {
		status = "ready"
	}
	return models.LLMRuntimeStatus{
		Status:     status,
		Configured: configured,
		BaseURL:    cfg.LLM.BaseURL,
		ChatModel:  cfg.LLM.ChatModel,
	}
}

func embeddingRuntimeStatus(cfg appconfig.Config) models.EmbeddingRuntimeStatus {
	status := "not_configured"
	configured := strings.TrimSpace(cfg.Embedding.APIKey) != ""
	if configured {
		status = "ready"
	}
	return models.EmbeddingRuntimeStatus{
		Status:     status,
		Configured: configured,
		BaseURL:    cfg.Embedding.BaseURL,
		Model:      cfg.Embedding.Model,
	}
}

func backgroundRuntimeStatus(status models.BackgroundRuntimeStatus, background backgroundTaskAcceptor) models.BackgroundRuntimeStatus {
	if status.Status != "ready" {
		return status
	}
	if background == nil {
		status.Status = "degraded"
		status.Error = "background routine is not ready"
		return status
	}
	if err := background.AsyncFunction(func() {}); err != nil {
		status.Status = "degraded"
		status.Error = err.Error()
		return status
	}
	status.AcceptingTasks = true
	return status
}

func eventsRuntimeStatus(stream *eventstream.Stream, publisher eventPublisher) models.EventsRuntimeStatus {
	if stream == nil {
		return models.EventsRuntimeStatus{Status: "degraded"}
	}
	stats := stream.Stats()
	status := models.EventsRuntimeStatus{
		Status:      "ready",
		HistorySize: stats.HistorySize,
		Limit:       stats.Limit,
		Subscribers: stats.Subscribers,
	}
	if publisher == nil {
		status.Status = "degraded"
		return status
	}
	publisher.Post(mcevent.NewEvent("thoughtflow.system.health_probe", "application.thoughtflow", "", mcevent.NewHeader(), nil))
	status.Publishable = true
	return status
}

func (s *Service) handleReindex(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	job, err := s.searchService.ReindexWorkspace(req.Context())
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.search.reindex_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusAccepted, job)
}

func (s *Service) handleLive(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	writeJSON(res, req, http.StatusOK, map[string]string{"status": "live"})
}

func (s *Service) handleReady(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	status := s.systemStatus(ctx, s.config)
	if !status.Ready {
		writeJSON(res, req, http.StatusServiceUnavailable, status)
		return
	}
	writeJSON(res, req, http.StatusOK, status)
}

func writeJSON(res http.ResponseWriter, req *http.Request, status int, data any) {
	res.Header().Set("Content-Type", "application/json; charset=utf-8")
	res.WriteHeader(status)
	_ = json.NewEncoder(res).Encode(models.APIResponse{
		RequestID: requestID(req),
		Data:      data,
		Error:     nil,
	})
}

func writeError(res http.ResponseWriter, req *http.Request, status int, code string, message string) {
	res.Header().Set("Content-Type", "application/json; charset=utf-8")
	res.WriteHeader(status)
	_ = json.NewEncoder(res).Encode(models.APIResponse{
		RequestID: requestID(req),
		Data:      nil,
		Error: map[string]any{
			"code":    code,
			"message": message,
		},
	})
}

func requestID(req *http.Request) string {
	if value := req.Header.Get("X-Request-ID"); value != "" {
		return value
	}
	return models.NewEventID(time.Now().UTC())
}

func pathID(path string, prefix string) string {
	if !strings.HasPrefix(path, prefix) {
		return ""
	}
	return strings.Trim(strings.TrimPrefix(path, prefix), "/")
}

func topicWeaveProposalPath(value string) (string, string) {
	tail := pathID(value, "/api/topics/")
	parts := strings.Split(tail, "/")
	if len(parts) != 3 || parts[1] != "weave-proposals" {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[2])
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}
	return ""
}

func intQuery(value string, fallback int) int {
	if value == "" {
		return fallback
	}
	var ret int
	_, err := fmt.Sscanf(value, "%d", &ret)
	if err != nil || ret <= 0 {
		return fallback
	}
	return ret
}

func boolQuery(value string) bool {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

func floatQuery(value string, fallback float64) float64 {
	if value == "" {
		return fallback
	}
	ret, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fallback
	}
	return ret
}

func timeQuery(value string, endOfDay bool) (time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, nil
	}
	if parsed, err := time.Parse(time.RFC3339, value); err == nil {
		return parsed.UTC(), nil
	}
	parsed, err := time.Parse("2006-01-02", value)
	if err != nil {
		return time.Time{}, err
	}
	parsed = parsed.UTC()
	if endOfDay {
		parsed = parsed.Add(24*time.Hour - time.Nanosecond)
	}
	return parsed, nil
}

func splitCSV(value string) []string {
	if value == "" {
		return nil
	}
	items := []string{}
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			items = append(items, item)
		}
	}
	return items
}

// logDeprecation records a structured warning for old-path usage.
// Front ends and curl recipes that still hit the previous generation
// of capture routes get one line of context so the migration can be
// measured from server logs alone — the response body is still the
// same payload the legacy handler would have produced. The function
// never returns an error and is safe to call from any handler.
func logDeprecation(req *http.Request, hint string) {
	ua := req.Header.Get("User-Agent")
	rem := req.RemoteAddr
	fmt.Fprintf(os.Stderr, "thoughtflow.deprecation %s ua=%q remote=%q\n", hint, ua, rem)
}

// handleCreateSession is the new "open or reuse a capture session"
// entry point. Three shapes:
//
//	body.content is empty, body.reuse_last=true   →  return LastActive (or a fresh one)
//	body.content is empty, body.reuse_last=false  →  mint a new session_id
//	body.content is set, any reuse_last            →  AppendMessage (creates the
//	                                                scratchpad if absent)
//
// The shape mirrors handleStartCaptureSession + handleNewCaptureSession
// from the previous generation, collapsed into one POST so the front
// end has a single obvious "open the capture page" verb.
//
//	POST /api/capture/sessions
//	body: {content?, role?, reuse_last?, source_thought_id?, prev_session_id?}
func (s *Service) handleCreateSession(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body struct {
		Content       string `json:"content"`
		Role          string `json:"role"`
		ReuseLast     bool   `json:"reuse_last"`
		SourceThought string `json:"source_thought_id"`
		PrevSessionID string `json:"prev_session_id"`
	}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	if prev := strings.TrimSpace(body.PrevSessionID); prev != "" {
		if err := s.scratchpad.Delete(prev); err != nil {
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
			return
		}
	}
	role := strings.TrimSpace(body.Role)
	if role == "" {
		role = "user"
	}
	if content := strings.TrimSpace(body.Content); content != "" {
		// Content path: a brand-new session id is fine — AppendMessage
		// is upsert-style. The session id is required only as a
		// client-supplied handle so the front end can keep the URL
		// stable. If the caller did not supply one we mint it here.
		sessionID := strings.TrimSpace(req.Header.Get("X-Session-Id"))
		if sessionID == "" {
			sessionID = models.NewEventID(time.Now().UTC())
		}
		sp, err := s.scratchpadSvc.AppendMessage(sessionID, role, content)
		if err != nil {
			if errors.Is(err, capturebiz.ErrScratchpadUnavailable) {
				writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", err.Error())
				return
			}
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
			return
		}
		if st := strings.TrimSpace(body.SourceThought); st != "" && sp.SourceThoughtID == "" {
			sp.SourceThoughtID = st
			sp, err = s.scratchpad.Save(sp)
			if err != nil {
				writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
				return
			}
		}
		writeJSON(res, req, http.StatusOK, sp)
		return
	}
	if body.ReuseLast {
		if last, ok := s.scratchpad.LastActive(); ok {
			writeJSON(res, req, http.StatusOK, last)
			return
		}
	}
	newID := models.NewEventID(time.Now().UTC())
	fresh := scratchpad.Scratchpad{SessionID: newID, WorkspaceID: s.workspace.ID}
	if st := strings.TrimSpace(body.SourceThought); st != "" {
		fresh.SourceThoughtID = st
	}
	saved, err := s.scratchpad.Save(fresh)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, saved)
}

// handleListSessions returns every scratchpad summary, with the
// most-recently-active uncommitted session id surfaced separately
// so the front-end boot path can rehydrate it.
//
//	GET /api/capture/sessions
func (s *Service) handleListSessions(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	payload := map[string]any{
		"summaries": s.scratchpad.List(),
	}
	if last, ok := s.scratchpad.LastActive(); ok {
		payload["last_active_session_id"] = last.SessionID
	}
	writeJSON(res, req, http.StatusOK, payload)
}

// handleGetActiveSession returns the most recently active
// uncommitted scratchpad, or an empty 200 body when none exists
// (the front end uses that as "open an empty composer").
//
//	GET /api/capture/sessions/active
func (s *Service) handleGetActiveSession(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	sp, ok := s.scratchpad.LastActive()
	if !ok {
		writeJSON(res, req, http.StatusOK, map[string]any{})
		return
	}
	writeJSON(res, req, http.StatusOK, sp)
}

// handleGetSession is the canonical scratchpad-by-id read.
//
//	GET /api/capture/sessions/{id}
func (s *Service) handleGetSession(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	sp, err := s.scratchpad.Get(sessionID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, sp)
}

// handleDeleteSession removes a scratchpad. Missing sessions are
// not an error — the UI can issue DELETE on first send of a fresh
// session without special-casing.
//
//	DELETE /api/capture/sessions/{id}
func (s *Service) handleDeleteSession(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpad == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad store is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	if sessionID == "" || strings.Contains(sessionID, "/") {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	if err := s.scratchpad.Delete(sessionID); err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, map[string]any{"deleted": true})
}

// handleSessionMessage appends a chat message to a scratchpad.
// The LLM tool surface hits this on every turn.
//
//	POST /api/capture/sessions/{id}/messages
//	body: {role, text}
func (s *Service) handleSessionMessage(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad service is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	// pathID strips the prefix; we need to also strip the trailing /messages
	sessionID = strings.TrimSuffix(sessionID, "/messages")
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body struct {
		Role string `json:"role"`
		Text string `json:"text"`
	}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	if strings.TrimSpace(body.Text) == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", "text is required")
		return
	}
	sp, err := s.scratchpadSvc.AppendMessage(sessionID, body.Role, body.Text)
	if err != nil {
		if errors.Is(err, capturebiz.ErrScratchpadUnavailable) {
			writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", err.Error())
			return
		}
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, sp)
}

// handleSessionContext replaces the structured session_context
// block. The whole block is replaced (not merged) per the
// LLM-tool contract — see ScratchpadService.UpdateSessionContext.
//
//	POST /api/capture/sessions/{id}/context
//	body: {session_context: {topic, goal, ...}}
func (s *Service) handleSessionContext(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad service is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	sessionID = strings.TrimSuffix(sessionID, "/context")
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body scratchpad.SessionContext
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	sp, err := s.scratchpadSvc.UpdateSessionContext(sessionID, body)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, sp)
}

// handleSessionIntent records WHO is driving the archive.
//
//	POST /api/capture/sessions/{id}/intent
//	body: {intent: "none" | "menu" | "llm"}
func (s *Service) handleSessionIntent(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad service is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	sessionID = strings.TrimSuffix(sessionID, "/intent")
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body struct {
		Intent scratchpad.ArchiveIntent `json:"intent"`
	}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	sp, err := s.scratchpadSvc.SetArchiveIntent(sessionID, body.Intent)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, sp)
}

// handleSessionStrategy records the commit routing decision.
// thought_id is only required for "update_thought" / "supplement";
// the service persists it on the scratchpad's SourceThoughtID so
// the eventual commit can find it without the front end re-sending
// it on every call.
//
//	POST /api/capture/sessions/{id}/strategy
//	body: {strategy: "new" | "update_thought" | "supplement", thought_id?: string}
func (s *Service) handleSessionStrategy(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad service is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	sessionID = strings.TrimSuffix(sessionID, "/strategy")
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body struct {
		Strategy scratchpad.ArchiveStrategy `json:"strategy"`
		ThoughtID string                   `json:"thought_id"`
	}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	sp, err := s.scratchpadSvc.SetArchiveStrategy(sessionID, body.Strategy, body.ThoughtID)
	if err != nil {
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, sp)
}

// handleArchivePreview renders the read-only preview the UI shows
// before commit lands (PRD §3.1). The preview is persisted back
// onto the scratchpad so a re-entry into the capture page surfaces
// the same view rather than re-deriving it.
//
// The handler resolves the strategy in this order:
//
//	1. ?strategy=update_thought|supplement|new  (override)
//	2. sp.ArchiveStrategy (what SetArchiveStrategy last set)
//	3. "new" (default for fresh sessions)
//
// "update_thought" requires the scratchpad to know which thought
// to update (SourceThoughtID); an empty / unset id returns 400
// with the same code as the service-layer ErrDiffRequired so the
// front end has a stable key to key off.
//
//	GET /api/capture/sessions/{id}/archive/preview[?strategy=...]
func (s *Service) handleArchivePreview(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	_ = ctx
	if s.scratchpad == nil || s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad service is not ready")
		return
	}
	if s.captureService == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.unavailable", "capture service is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	sessionID = strings.TrimSuffix(sessionID, "/archive/preview")
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	sp, err := s.scratchpad.Get(sessionID)
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", err.Error())
		return
	}
	// Optional ?strategy= override lets the UI try a different
	// routing before the user has clicked the menu item.
	if q := strings.TrimSpace(req.URL.Query().Get("strategy")); q != "" {
		sp.ArchiveStrategy = scratchpad.ArchiveStrategy(q)
	}
	if sp.ArchiveStrategy == "" {
		sp.ArchiveStrategy = scratchpad.ArchiveStrategyNew
	}
	var current *models.ThoughtSnapshot
	if sp.ArchiveStrategy == scratchpad.ArchiveStrategyUpdate || sp.ArchiveStrategy == scratchpad.ArchiveStrategySupplement {
		thoughtID := strings.TrimSpace(sp.SourceThoughtID)
		if thoughtID == "" {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.diff_required", "source_thought_id is required for update_thought / supplement strategy")
			return
		}
		snapshot, gerr := s.captureService.GetThought(ctx, thoughtID)
		if gerr != nil {
			writeError(res, req, http.StatusNotFound, "thoughtflow.capture.thought_not_found", gerr.Error())
			return
		}
		current = &snapshot
	}
	preview, err := s.scratchpadSvc.BuildArchivePreview(sp, current)
	if err != nil {
		if errors.Is(err, capturebiz.ErrDiffRequired) {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.diff_required", err.Error())
			return
		}
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.preview_failed", err.Error())
		return
	}
	// Persist the preview back so re-entry shows the same view.
	sp.ArchivePreview = &preview
	if _, serr := s.scratchpad.Save(sp); serr != nil {
		// Preview render is the source of truth; persistence is
		// best-effort — log via the response error code but still
		// return the preview so the UI can show it.
		writeJSON(res, req, http.StatusOK, map[string]any{
			"preview":   preview,
			"persisted": false,
			"warning":   serr.Error(),
		})
		return
	}
	writeJSON(res, req, http.StatusOK, map[string]any{
		"preview":   preview,
		"persisted": true,
	})
}

// handleSessionArchive lands a scratchpad as a real thought. The
// routing decision (new / update_thought / supplement) is read
// from the scratchpad; the request body may override with
// {strategy, thought_id} for the menu-driven path. The handler
// always runs the preview-equivalent validations first: an
// update_thought / supplement request with no SourceThoughtID
// returns 400, and the "confirmed" flag is recorded but does not
// gate the actual commit (the UI is expected to show the preview
// before this endpoint is hit).
//
//	POST /api/capture/sessions/{id}/archive
//	body: {strategy?, thought_id?, confirmed?}
func (s *Service) handleSessionArchive(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	if s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad service is not ready")
		return
	}
	sessionID := strings.TrimSpace(pathID(req.URL.Path, "/api/capture/sessions/"))
	sessionID = strings.TrimSuffix(sessionID, "/archive")
	if sessionID == "" {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.scratchpad.invalid_session", "session_id is required")
		return
	}
	rawBody, err := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	if err != nil {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
		return
	}
	var body struct {
		Strategy scratchpad.ArchiveStrategy `json:"strategy"`
		ThoughtID string                   `json:"thought_id"`
		Confirmed bool                     `json:"confirmed"`
	}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	// Apply the request body's overrides onto the scratchpad so
	// the underlying Commit can stay strategy-agnostic. The two
	// side effects (stamp strategy, stamp source_thought_id) are
	// both safe to do on a "menu" path; for a re-commit the
	// scratchpad already carries the right values and the body
	// is allowed to leave them alone.
	if body.Strategy != "" {
		thoughtID := body.ThoughtID
		if _, err := s.scratchpadSvc.SetArchiveStrategy(sessionID, body.Strategy, thoughtID); err != nil {
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
			return
		}
	}
	// If the user explicitly confirmed, stamp the intent so the
	// scratchpad carries the audit trail.
	if body.Confirmed {
		if _, err := s.scratchpadSvc.SetArchiveIntent(sessionID, scratchpad.ArchiveIntentMenu); err != nil {
			writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.scratchpad.write_failed", err.Error())
			return
		}
	}
	result, err := s.scratchpadSvc.Commit(ctx, sessionID)
	if err != nil {
		if errors.Is(err, capturebiz.ErrDiffRequired) {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.diff_required", err.Error())
			return
		}
		if errors.Is(err, capturebiz.ErrAlreadyCommitted) {
			writeError(res, req, http.StatusConflict, "thoughtflow.capture.already_committed", err.Error())
			return
		}
		// Capture-side validation (empty content / invalid command)
		// surfaces as 400; everything else is 500.
		if strings.Contains(err.Error(), "is required") || strings.Contains(err.Error(), "is empty") {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", err.Error())
			return
		}
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.commit_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, result)
}

// handleReopenSession is the "从已归档 Thought 重新整理" entry
// point (PRD §3.1.1). It seeds a brand-new scratchpad from the
// existing thought's metadata and returns the new session id;
// the front end then reuses the normal capture flow to chat
// against the seeded context.
//
// The default archive strategy is "supplement" so a subsequent
// commit creates a sibling thought with a backlink. The user
// can switch to "update_thought" / "new" via
// /api/capture/sessions/{new_id}/strategy before committing.
//
//	POST /api/thoughts/{id}/reopen-session
//	body: {session_id?: string}
func (s *Service) handleReopenSession(ctx context.Context, res http.ResponseWriter, req *http.Request) {
	if s.scratchpadSvc == nil {
		writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", "scratchpad service is not ready")
		return
	}
	thoughtID := strings.TrimSpace(pathID(req.URL.Path, "/api/thoughts/"))
	thoughtID = strings.TrimSuffix(thoughtID, "/reopen-session")
	if thoughtID == "" || strings.Contains(thoughtID, "/") {
		writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_request", "thought id is required")
		return
	}
	rawBody, _ := io.ReadAll(io.LimitReader(req.Body, 1<<20))
	var body struct {
		SessionID string `json:"session_id"`
	}
	if len(rawBody) > 0 {
		if err := json.Unmarshal(rawBody, &body); err != nil {
			writeError(res, req, http.StatusBadRequest, "thoughtflow.capture.invalid_json", err.Error())
			return
		}
	}
	sp, err := s.scratchpadSvc.ReopenFromThought(ctx, thoughtID, body.SessionID)
	if err != nil {
		if strings.Contains(err.Error(), "source thought not found") {
			writeError(res, req, http.StatusNotFound, "thoughtflow.capture.thought_not_found", err.Error())
			return
		}
		if errors.Is(err, capturebiz.ErrScratchpadUnavailable) {
			writeError(res, req, http.StatusServiceUnavailable, "thoughtflow.capture.scratchpad.unavailable", err.Error())
			return
		}
		writeError(res, req, http.StatusInternalServerError, "thoughtflow.capture.reopen_failed", err.Error())
		return
	}
	writeJSON(res, req, http.StatusOK, map[string]any{
		"session_id": sp.SessionID,
		"scratchpad": sp,
	})
}
