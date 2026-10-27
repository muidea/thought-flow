package service

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetflow"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
)

func TestSetSnapshotMissingRelatedThoughtIsNotMissingCapture(t *testing.T) {
	s, id := documentHTTPFixture(t, "design")
	store := scratchpad.New(filepath.Join(s.workspace.RootPath, "sessions"))
	s.scratchpad = store
	_, e := store.Save(scratchpad.Scratchpad{SessionID: "review-source", InputGeneration: 1, SessionContext: scratchpad.SessionContext{RelatedThoughtIDs: []string{id}}})
	if e != nil {
		t.Fatal(e)
	}
	before, e := s.captureSetSnapshot("review-source")
	if e != nil || len(before.Sources.Sources) != 1 {
		t.Fatal(before, e)
	}
	if e = markdown.DeleteThought(s.workspace.RootPath, id); e != nil {
		t.Fatal(e)
	}
	if _, e = s.captureSetSnapshot("review-source"); !errors.Is(e, ds.ErrSourceMissing) || errors.Is(e, ds.ErrCaptureMissing) {
		t.Fatal("source error conflated", e)
	}
	if _, e = s.captureSetSnapshot("absent-session"); !errors.Is(e, ds.ErrCaptureMissing) {
		t.Fatal("capture absence not classified", e)
	}
}

type retryBlockedGenerator struct{ entered chan struct{} }

func (g retryBlockedGenerator) GenerateDocument(ctx context.Context, r ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	close(g.entered)
	<-ctx.Done()
	return models.DocumentDraft{}, ctx.Err()
}
func TestSetRetryPersistsNewRunBeforeReturningAndRejectsStaleBinding(t *testing.T) {
	s, _ := documentHTTPFixture(t, "design")
	store := scratchpad.New(filepath.Join(s.workspace.RootPath, "sessions"))
	s.scratchpad = store
	sp, e := store.Save(scratchpad.Scratchpad{SessionID: "retry-session", InputGeneration: 1, Messages: []scratchpad.Message{{Role: "user", Text: "Research recovery"}}})
	if e != nil {
		t.Fatal(e)
	}
	entered := make(chan struct{})
	s.documentSets = documentsetflow.New(s.workspace.RootPath, documentsetflow.Options{Enabled: true, ModelHash: "fixture", Snapshot: s.captureSetSnapshot, Generator: retryBlockedGenerator{entered: entered}})
	t.Cleanup(s.documentSets.Close)
	profile := ds.Builtins()[1]
	zero := uint64(0)
	plan, e := s.documentSets.Plan(context.Background(), sp.SessionID, documentsetflow.PlanRequest{OperationID: "retry-plan", ProfileID: profile.ID, ProfileVersion: profile.Version, ProfileHash: profile.Hash, ExpectedGeneration: &zero, CaptureGeneration: &sp.InputGeneration, Shared: &ds.SharedDesign{Inputs: map[string]string{"goal": "Research", "scope": "Recovery"}}})
	if e != nil {
		t.Fatal(e)
	}
	_, e = store.Update(sp.SessionID, func(sp *scratchpad.Scratchpad) error {
		sp.DocumentSetID = plan.SetID
		sp.DocumentSetPlanID = plan.ID
		sp.DocumentSetRunID = "old-attempt"
		return nil
	})
	if e != nil {
		t.Fatal(e)
	}
	old := ds.Run{ID: "old-attempt", OperationID: "old-attempt", ModelHash: "fixture", State: "failed_terminal", Plan: plan}
	for _, member := range plan.Members {
		old.Members = append(old.Members, ds.MemberProgress{Key: member.Key, State: "failed"})
	}
	if e = s.documentSets.Store.SaveRun(old); e != nil {
		t.Fatal(e)
	}
	retry := func(op string) *httptest.ResponseRecorder {
		t.Helper()
		raw, _ := json.Marshal(documentsetflow.RetryRequest{OperationID: op, ExpectedGeneration: &zero})
		req := httptest.NewRequest("POST", "/api/document-set-runs/old-attempt/retry", bytes.NewReader(raw))
		res := httptest.NewRecorder()
		s.handleDocumentSet(context.Background(), res, req)
		return res
	}
	res := retry("new-attempt")
	if res.Code != http.StatusAccepted {
		t.Fatal(res.Code, res.Body.String())
	}
	// Join actual generation rather than letting cleanup cancel the worker before
	// it ever accesses the persisted member progress.
	select {
	case <-entered:
	case <-time.After(5 * time.Second):
		t.Fatal("retry never reached member generation")
	}
	req := httptest.NewRequest("GET", "/api/capture/sessions/"+sp.SessionID, nil)
	get := httptest.NewRecorder()
	s.handleGetSession(context.Background(), get, req)
	if get.Code != 200 {
		t.Fatal(get.Body.String())
	}
	saved := decodeDocumentData[scratchpad.Scratchpad](t, get.Body.Bytes())
	if saved.DocumentSetRunID != "new-attempt" {
		t.Fatal("reload still restores failed attempt", saved.DocumentSetRunID)
	}
	_, e = store.Update(sp.SessionID, func(sp *scratchpad.Scratchpad) error { sp.DocumentSetRunID = "even-newer"; return nil })
	if e != nil {
		t.Fatal(e)
	}
	res = retry("new-attempt")
	if res.Code != 409 {
		t.Fatal("old replay overwrote newer authority", res.Code, res.Body.String())
	}
	saved, e = store.Get(sp.SessionID)
	if e != nil || saved.DocumentSetRunID != "even-newer" {
		t.Fatal(saved.DocumentSetRunID, e)
	}
}
