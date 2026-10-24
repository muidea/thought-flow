package service

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

type auditBlockingGenerator struct{ started, release chan struct{} }

func (g *auditBlockingGenerator) GenerateDocument(ctx context.Context, req ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	select {
	case g.started <- struct{}{}:
	default:
	}
	select {
	case <-g.release:
	case <-ctx.Done():
		return models.DocumentDraft{}, ctx.Err()
	}
	return ai.NewLocalRefineProvider().GenerateDocument(ctx, req)
}

func TestArchiveRegressionPreviewOverwritingNewMessage(t *testing.T) {
	root := t.TempDir()
	store := scratchpad.New(filepath.Join(root, "scratchpad"))
	registry, err := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if err != nil {
		t.Fatal(err)
	}
	gen := &auditBlockingGenerator{make(chan struct{}, 1), make(chan struct{})}
	svc := capturebiz.NewScratchpadService(store, capturebiz.WithDocumentProfiles(registry, gen, 10, 0))
	initial := scratchpad.Scratchpad{SessionID: "audit", Content: "original", Messages: []scratchpad.Message{{Role: "user", Text: "original"}}, SessionContext: scratchpad.SessionContext{CandidateBody: "original content", CandidateProfileID: "builtin.design-doc", CandidateProfileVersion: 1, DocumentParameters: map[string]string{"problem": "failure", "goals": "recovery", "non_goals": "scope"}}}
	if _, err := store.Save(initial); err != nil {
		t.Fatal(err)
	}
	handler := &Service{scratchpad: store, scratchpadSvc: svc, captureService: capturebiz.NewService(&models.Workspace{RootPath: root}, nil, nil)}
	res := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/capture/sessions/audit/archive/preview", nil)
	done := make(chan struct{})
	go func() { defer close(done); handler.handleArchivePreview(req.Context(), res, req) }()
	select {
	case <-gen.started:
	case <-time.After(10 * time.Second):
		t.Fatal("generation not entered")
	}
	if _, err := svc.AppendMessage("audit", "user", "NEW MESSAGE DURING PREVIEW"); err != nil {
		close(gen.release)
		t.Fatal(err)
	}
	before, _ := store.Get("audit")
	if len(before.Messages) != 2 {
		close(gen.release)
		t.Fatalf("concurrent message not saved: %d", len(before.Messages))
	}
	close(gen.release)
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("handler timeout")
	}
	after, _ := store.Get("audit")
	if res.Code != 200 {
		t.Fatalf("preview status=%d body=%s", res.Code, res.Body)
	}
	if len(after.Messages) != 2 || !strings.Contains(after.Content, "NEW MESSAGE DURING PREVIEW") {
		t.Fatalf("new message was lost, messages=%d content=%q", len(after.Messages), after.Content)
	}
	reloaded, _ := scratchpad.New(filepath.Join(root, "scratchpad")).Get("audit")
	if len(reloaded.Messages) != 2 {
		t.Fatal("new message missing on disk")
	}
	if after.ArchivePreview != nil {
		t.Fatal("stale preview was published")
	}
}

type queuedArchiveBackground struct{ work []func() }

func (b *queuedArchiveBackground) AsyncFunction(f func()) error {
	b.work = append(b.work, f)
	return nil
}

func TestArchiveAcceptedRequestSurvivesDisconnectAndStoreRestart(t *testing.T) {
	root := t.TempDir()
	stateDir := filepath.Join(root, "sessions")
	store := scratchpad.New(stateDir)
	registry, e := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	capture := capturebiz.NewService(&models.Workspace{ID: "test", RootPath: root}, nil, nil)
	svc := capturebiz.NewScratchpadService(store, capturebiz.WithCapture(capture), capturebiz.WithDocumentProfiles(registry, nil, 10, 1))
	_, e = store.Save(scratchpad.Scratchpad{SessionID: "accepted", Content: "Persist accepted intent", SessionContext: scratchpad.SessionContext{CandidateTitle: "Accepted", CandidateBody: "Persist accepted intent", CandidateProfileID: "builtin.note", CandidateProfileVersion: 1}})
	if e != nil {
		t.Fatal(e)
	}
	background := &queuedArchiveBackground{}
	handler := &Service{scratchpad: store, scratchpadSvc: svc, captureService: capture, background: background}
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/capture/sessions/accepted/archive/preview", strings.NewReader(`{"commit_after_preview":true}`)).WithContext(ctx)
	response := httptest.NewRecorder()
	handler.handleArchivePreview(ctx, response, req)
	if response.Code != 202 || len(background.work) != 1 {
		t.Fatalf("not accepted: %d %s", response.Code, response.Body)
	}
	cancel()
	before, _ := store.Get("accepted")
	read := httptest.NewRecorder()
	get := httptest.NewRequest("GET", req.URL.String(), nil)
	handler.handleArchivePreview(get.Context(), read, get)
	after, _ := store.Get("accepted")
	if after.SessionGeneration != before.SessionGeneration {
		t.Fatal("GET mutated state")
	}
	// Discard the queued process and reload only the durable files.
	restored := scratchpad.New(stateDir)
	restoredSvc := capturebiz.NewScratchpadService(restored, capturebiz.WithCapture(capture), capturebiz.WithDocumentProfiles(registry, nil, 10, 1))
	nextQueue := &queuedArchiveBackground{}
	restarted := &Service{scratchpad: restored, scratchpadSvc: restoredSvc, captureService: capture, background: nextQueue}
	restarted.resumeCaptureArchives()
	if len(nextQueue.work) != 1 {
		t.Fatal("accepted operation was not resumed")
	}
	nextQueue.work[0]()
	saved, _ := restored.Get("accepted")
	if saved.Operation.State != "saved" || saved.CommittedThoughtID == "" {
		t.Fatalf("not archived without client: %+v", saved)
	}
	if _, e := capture.GetThought(context.Background(), saved.CommittedThoughtID); e != nil {
		t.Fatal(e)
	}
}
func TestArchiveShutdownCancelsAndLeavesResumableOperation(t *testing.T) {
	root := t.TempDir()
	store := scratchpad.New(filepath.Join(root, "sessions"))
	registry, e := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	capture := capturebiz.NewService(&models.Workspace{ID: "test", RootPath: root}, nil, nil)
	gen := &auditBlockingGenerator{make(chan struct{}, 1), make(chan struct{})}
	svc := capturebiz.NewScratchpadService(store, capturebiz.WithCapture(capture), capturebiz.WithDocumentProfiles(registry, gen, 10, 1))
	_, e = store.Save(scratchpad.Scratchpad{SessionID: "stop", Content: "Original", SessionContext: scratchpad.SessionContext{CandidateBody: "Original", CandidateTitle: "Title", CandidateProfileID: "builtin.design-doc", CandidateProfileVersion: 1, DocumentParameters: map[string]string{"problem": "failure", "goals": "recovery", "non_goals": "scope"}}})
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	queue := &queuedArchiveBackground{}
	handler := &Service{scratchpad: store, scratchpadSvc: svc, captureService: capture, background: queue, shutdownCtx: ctx, shutdownCancel: cancel}
	sp, e := svc.BeginPreview("stop", false)
	if e != nil {
		t.Fatal(e)
	}
	if e = handler.startCaptureArchive(sp); e != nil {
		t.Fatal(e)
	}
	done := make(chan struct{})
	go func() { defer close(done); queue.work[0]() }()
	select {
	case <-gen.started:
	case <-time.After(5 * time.Second):
		t.Fatal("worker not started")
	}
	closed := make(chan struct{})
	go func() { handler.Close(); close(closed) }()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("shutdown did not drain")
	}
	<-done
	final, _ := scratchpad.New(filepath.Join(root, "sessions")).Get("stop")
	if final.Operation.State != "interrupted" || final.Content != "Original" {
		t.Fatalf("interrupted operation lost: %+v", final)
	}
}

func TestCaptureDraftRemainsPrivateWhenPublicSharingIsEnabled(t *testing.T) {
	workspace := &models.Workspace{ID: "test", RootPath: t.TempDir()}
	capture := capturebiz.NewService(workspace, nil, nil)
	service := &Service{captureService: capture, workspace: workspace, config: appconfig.Config{Server: appconfig.ServerConfig{PublicThoughtsEnabled: true}}}
	for _, draft := range []bool{true, false} {
		result, e := capture.Capture(context.Background(), models.CaptureCommand{Type: models.ThoughtTypeText, Title: "Public gate fixture", Content: "Content for explicit sharing gate", CaptureDraft: draft})
		if e != nil {
			t.Fatal(e)
		}
		request := httptest.NewRequest("GET", "/p/thoughts/"+result.Thought.ID, nil)
		response := httptest.NewRecorder()
		service.handlePublicThought(request.Context(), response, request)
		if draft {
			if response.Code != 404 || !strings.Contains(response.Body.String(), "thoughtflow.document.not_final") {
				t.Fatalf("private draft leaked: %d %s", response.Code, response.Body)
			}
		} else if response.Code != 200 {
			t.Fatalf("ordinary public control failed: %d %s", response.Code, response.Body)
		}
	}
}
