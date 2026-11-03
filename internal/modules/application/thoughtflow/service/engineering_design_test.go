package service

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetflow"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/scratchpad"
	"thoughtflow/internal/testutil/designfixture"
)

type engineeringHTTPGenerator struct{}

func (engineeringHTTPGenerator) GenerateDocument(_ context.Context, req ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	out := models.DocumentDraft{Title: req.Context.Title, Sections: map[string]models.DocumentSection{}}
	for _, s := range req.Profile.Sections {
		out.Sections[s.Key] = models.DocumentSection{Content: "Order requests require authentication, validation and atomic persistence. Unauthorized input returns 403 without creating a row. Restart preserves committed data. Verify HTTP outcomes and persisted row counts."}
	}
	return out, nil
}

type engineeringHTTPReviewer struct{}

func (engineeringHTTPReviewer) ReviewDocumentSet(_ context.Context, items []ds.ReviewItem, _ []ds.Artifact, _ ds.SharedDesign, _ ds.CaptureSnapshot) (ds.ReviewResponse, string, error) {
	out := ds.ReviewResponse{Evidence: []ds.ReviewEvidence{}}
	for _, item := range items {
		ev := ds.ReviewEvidence{CheckID: item.ID, Verdict: "passed", Reason: "Checked frozen normative behavior, failure cases and public projection"}
		for _, key := range item.Members {
			ev.Locations = append(ev.Locations, ds.Location{Member: key, Section: "scope", Evidence: "Order requests require authentication, validation and atomic persistence."})
		}
		out.Evidence = append(out.Evidence, ev)
	}
	return out, "http-fixture-reviewer", nil
}
func TestEngineeringHTTPPlanPublishPublicAuthorizationAndHandoff(t *testing.T) {
	s, _ := documentHTTPFixture(t, "design")
	c, shared := designfixture.Capture()
	store := scratchpad.New(filepath.Join(s.workspace.RootPath, "sessions"))
	s.scratchpad = store
	sp, e := store.Save(scratchpad.Scratchpad{SessionID: c.SessionID, InputGeneration: 1, Messages: []scratchpad.Message{{Role: "user", Text: c.Messages[0].Text}}})
	if e != nil {
		t.Fatal(e)
	}
	s.documentSets = documentsetflow.New(s.workspace.RootPath, documentsetflow.Options{Enabled: true, ModelHash: "http-model", Snapshot: s.captureSetSnapshot, SnapshotUnderLease: func(id string) (ds.CaptureSnapshot, error) { return s.captureSetSnapshotUnderLease(id, true) }, Generator: engineeringHTTPGenerator{}, Reviewer: engineeringHTTPReviewer{}})
	t.Cleanup(s.documentSets.Close)
	call := func(path string, body any) *httptest.ResponseRecorder {
		raw, e := json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
		req := httptest.NewRequest("POST", path, bytes.NewReader(raw))
		res := httptest.NewRecorder()
		s.handleDocumentSet(context.Background(), res, req)
		return res
	}
	profile := ds.Builtins()[2]
	zero := uint64(0)
	response := call("/api/capture/sessions/"+c.SessionID+"/document-set-plans", documentsetflow.PlanRequest{OperationID: "http-engineering-plan", ExpectedGeneration: &zero, CaptureGeneration: &sp.InputGeneration, ProfileID: profile.ID, ProfileVersion: profile.Version, ProfileHash: profile.Hash, Shared: &shared})
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var envelope struct {
		Data ds.Plan `json:"data"`
	}
	if e = json.Unmarshal(response.Body.Bytes(), &envelope); e != nil {
		t.Fatal(e)
	}
	plan := envelope.Data
	response = call("/api/capture/sessions/"+c.SessionID+"/document-set-runs", documentsetflow.RunRequest{OperationID: "http-engineering-run", SetID: plan.SetID, PlanID: plan.ID, PlanHash: plan.Hash, ExpectedGeneration: &zero, CaptureGeneration: &sp.InputGeneration})
	if response.Code != http.StatusAccepted {
		t.Fatal(response.Code, response.Body.String())
	}
	var run ds.Run
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		run, e = s.documentSets.Store.Run("http-engineering-run")
		if e != nil {
			t.Fatal(e)
		}
		if run.State == "committed" || run.State == "failed_terminal" {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	head, e := s.documentSets.Store.Head(plan.SetID)
	if e != nil {
		t.Fatal(e)
	}
	response = call("/api/document-sets/"+plan.SetID+"/finalizations", documentsetflow.FinalizeRequest{OperationID: "http-final", ExpectedGeneration: &head.Generation, RevisionID: head.Working, RevisionHash: run.Candidate.Hash, ApprovedBy: "test user", DeliveryLevel: "ready_for_use"})
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	head, e = s.documentSets.Store.Head(plan.SetID)
	if e != nil {
		t.Fatal(e)
	}
	export := documentsetstore.ExportRequest{OperationID: "http-export", ExpectedGeneration: &head.Generation, RevisionID: head.Finalized, Kind: "delivery"}
	response = call("/api/document-sets/"+plan.SetID+"/exports", export)
	if response.Code != http.StatusPreconditionRequired {
		t.Fatal("missing public authorization accepted", response.Code, response.Body.String())
	}
	export.PublicAuthorized = true
	response = call("/api/document-sets/"+plan.SetID+"/exports", export)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	if _, e = documentsetstore.VerifyPublicPackage(response.Body.Bytes(), run.Candidate.Report.PublicPayloadHash); e != nil {
		t.Fatal(e)
	}
	req := documentsetstore.HandoffRequest{ExpectedGeneration: &head.Generation, Revision: head.Finalized}
	response = call("/api/document-sets/"+plan.SetID+"/handoff-previews", req)
	if response.Code != http.StatusOK {
		t.Fatal(response.Code, response.Body.String())
	}
	var preview struct {
		Data documentsetstore.HandoffPreview `json:"data"`
	}
	json.Unmarshal(response.Body.Bytes(), &preview)
	if len(preview.Data.Input.Acceptance) != 0 || preview.Data.Budget.Consumer != "NOT_RUN" {
		t.Fatal("consumer source or readiness fabricated")
	}
	req.PublicAuthorized = true
	response = call("/api/document-sets/"+plan.SetID+"/handoff-exports", req)
	if response.Code != http.StatusOK || response.Header().Get("Content-Type") != "application/zip" {
		t.Fatal(response.Code, response.Body.String())
	}
	stale := head.Generation - 1
	req.ExpectedGeneration = &stale
	response = call("/api/document-sets/"+plan.SetID+"/handoff-previews", req)
	if response.Code != http.StatusConflict {
		t.Fatal("stale preview was accepted", response.Code)
	}
}
