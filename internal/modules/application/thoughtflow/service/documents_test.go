package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	capturebiz "thoughtflow/internal/modules/capture/biz"
	"thoughtflow/internal/pkg/appconfig"
	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func documentHTTPFixture(t *testing.T, family string) (*Service, string) {
	t.Helper()
	root := t.TempDir()
	ws := &models.Workspace{ID: "local", RootPath: root, RuntimePath: filepath.Join(root, "runtime"), JobsPath: filepath.Join(root, "runtime", "jobs")}
	cfg := appconfig.Config{Documents: appconfig.DocumentsConfig{Enabled: true, AllowManual: true, MaxRepairAttempts: 2}}
	cfg.Server.PublicThoughtsEnabled = false
	profiles, e := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	s := &Service{workspace: ws, config: cfg, captureService: capturebiz.NewService(ws, nil, nil)}
	s.SetDocumentProfiles(profiles)
	t.Cleanup(s.Close)
	profileID := map[string]string{"design": "builtin.design-doc", "research": "builtin.research-report", "article": "builtin.blog-post"}[family]
	p, e := profiles.ResolveLatest(profileID)
	if e != nil {
		t.Fatal(e)
	}
	params := map[string]string{}
	sections := map[string]models.DocumentSection{}
	for _, input := range p.Inputs {
		params[input.Key] = "confirmed"
	}
	params["subtype"] = "opinion"
	for _, section := range p.Sections {
		sections[section.Key] = models.DocumentSection{Content: "G1 D1 T1 Q1 F1 C1: failure recovery and a counterexample / limitation. Evidence [S1]."}
	}
	body := documentprofile.Render(p, models.DocumentDraft{Title: "HTTP document", Sections: sections, References: []models.DocumentReference{{ID: "S1", SourceLink: "https://example.org/evidence"}}}, params).Content
	captured, e := s.captureService.Capture(context.Background(), models.CaptureCommand{Type: "text", Content: body, Title: "HTTP document", DocumentProfile: &p.Ref, DocumentParameters: params})
	if e != nil {
		t.Fatal(e)
	}
	return s, captured.Thought.ID
}
func documentCall(t *testing.T, s *Service, method, path string, body any, generation *uint64) (int, []byte) {
	t.Helper()
	var raw []byte
	if body != nil {
		var e error
		raw, e = json.Marshal(body)
		if e != nil {
			t.Fatal(e)
		}
	}
	req := httptest.NewRequest(method, path, bytes.NewReader(raw))
	if generation != nil {
		req.Header.Set("If-Match", fmt.Sprint(*generation))
	}
	res := httptest.NewRecorder()
	s.handleDocument(req.Context(), res, req)
	return res.Code, res.Body.Bytes()
}
func decodeDocumentData[T any](t *testing.T, raw []byte) T {
	t.Helper()
	var envelope struct {
		Data T `json:"data"`
	}
	if e := json.Unmarshal(raw, &envelope); e != nil {
		t.Fatal(e)
	}
	return envelope.Data
}
func TestDocumentHTTPThreeTypesAndStatusCodes(t *testing.T) {
	for _, family := range []string{"design", "research", "article"} {
		t.Run(family, func(t *testing.T) {
			s, id := documentHTTPFixture(t, family)
			base := "/api/documents/" + id
			status, raw := documentCall(t, s, "GET", base, nil, nil)
			if status != 200 {
				t.Fatalf("GET %d %s", status, raw)
			}
			snap := decodeDocumentData[models.DocumentSnapshot](t, raw)
			status, _ = documentCall(t, s, "POST", base+"/validate", map[string]any{"revision_id": snap.Head.WorkingRevisionID}, nil)
			if status != 428 {
				t.Fatalf("no precondition: %d", status)
			}
			status, raw = documentCall(t, s, "POST", base+"/validate", map[string]any{"revision_id": snap.Head.WorkingRevisionID}, &snap.Head.Generation)
			if status != 200 {
				t.Fatalf("validate %d %s", status, raw)
			}
			report := decodeDocumentData[models.ValidationReport](t, raw)
			if report.Status != "needs_review" {
				t.Fatalf("validation %+v", report)
			}
			confirmations := []models.DocumentConfirmation{}
			for _, issue := range report.Issues {
				if issue.HumanConfirmation {
					confirmations = append(confirmations, models.DocumentConfirmation{IssueID: issue.ID, Reason: "Manually inspected the exact revision and evidence."})
				}
			}
			request := documentflow.FinalizeRequest{RevisionID: report.RevisionID, ReportID: report.ReportID, OperationID: "http-final", DeliveryLevel: "proposal", Confirmations: confirmations}
			status, raw = documentCall(t, s, "POST", base+"/finalize", request, &snap.Head.Generation)
			if status != 200 {
				t.Fatalf("finalize %d %s", status, raw)
			}
			status, body := documentCall(t, s, "GET", base+"/revisions/"+report.RevisionID+"/export?mode=final", nil, nil)
			if status != 200 || string(body) != snap.Body {
				t.Fatal("formal export contains wrapper or different bytes")
			}
			_, raw = documentCall(t, s, "GET", base, nil, nil)
			current := decodeDocumentData[models.DocumentSnapshot](t, raw)
			status, _ = documentCall(t, s, "POST", base+"/revisions", map[string]string{"body": "# Incomplete\n"}, &snap.Head.Generation)
			if status != 409 {
				t.Fatalf("stale generation %d", status)
			}
			status, raw = documentCall(t, s, "POST", base+"/revisions", map[string]string{"body": "# Incomplete\n"}, &current.Head.Generation)
			if status != 201 {
				t.Fatalf("incomplete draft must save: %d %s", status, raw)
			}
			next := decodeDocumentData[models.DocumentSnapshot](t, raw)
			request.RevisionID = next.Head.WorkingRevisionID
			request.OperationID = "stale-report"
			status, _ = documentCall(t, s, "POST", base+"/finalize", request, &next.Head.Generation)
			if status != 409 {
				t.Fatalf("stale report accepted: %d", status)
			}
			status, _ = documentCall(t, s, "GET", base+"/revisions/"+next.Head.WorkingRevisionID+"/export?mode=final", nil, nil)
			if status != 422 {
				t.Fatalf("draft masquerades as final: %d", status)
			}
			// Explicit public opt-in is still required, and a URL is pinned to a revision.
			req := httptest.NewRequest(http.MethodGet, "/p/documents/"+id+"/revisions/"+report.RevisionID, nil)
			res := httptest.NewRecorder()
			s.handlePublicDocument(req.Context(), res, req)
			if res.Code != 404 {
				t.Fatal("public sharing enabled implicitly")
			}
			s.config.Server.PublicThoughtsEnabled = true
			res = httptest.NewRecorder()
			s.handlePublicDocument(req.Context(), res, req)
			if res.Code != 200 || res.Body.String() != snap.Body {
				t.Fatal("public final changed with current draft")
			}
		})
	}
}
func TestHardErrorsCannotBeConfirmedThroughHTTP(t *testing.T) {
	s, id := documentHTTPFixture(t, "design")
	snap, e := s.documents.Get(id)
	if e != nil {
		t.Fatal(e)
	}
	status, raw := documentCall(t, s, "POST", "/api/documents/"+id+"/revisions", map[string]string{"body": "# draft\n\nTODO\n"}, &snap.Head.Generation)
	if status != 201 {
		t.Fatal(status)
	}
	next := decodeDocumentData[models.DocumentSnapshot](t, raw)
	r, e := s.documents.Validate(context.Background(), id, next.Head.WorkingRevisionID, next.Head.Generation)
	if e != nil {
		t.Fatal(e)
	}
	req := documentflow.FinalizeRequest{OperationID: "ignore-all", RevisionID: r.RevisionID, ReportID: r.ReportID, DeliveryLevel: "ready_for_use"}
	for _, i := range r.Issues {
		req.Confirmations = append(req.Confirmations, models.DocumentConfirmation{IssueID: i.ID, Reason: "ignore"})
	}
	status, _ = documentCall(t, s, "POST", "/api/documents/"+id+"/finalize", req, &next.Head.Generation)
	if status != 422 {
		t.Fatalf("hard rules bypassed: %d", status)
	}
}
func TestEvidenceStaysPrivateAndMissingEvidenceIsUnavailable(t *testing.T) {
	s, id := documentHTTPFixture(t, "research")
	snap, _ := s.documents.Get(id)
	sources := models.SourceManifest{SchemaVersion: 1, Sources: []models.DocumentSource{{ID: "S1", Type: "external", Locator: "https://example.org/evidence", Version: "frozen", Hash: "sha256:original", VerificationStatus: "unverified"}}}
	_, raw := documentCall(t, s, "POST", "/api/documents/"+id+"/revisions", map[string]any{"source_manifest": sources, "evidence": map[string]string{"S1": "PRIVATE ORIGINAL EXCERPT"}}, &snap.Head.Generation)
	next := decodeDocumentData[models.DocumentSnapshot](t, raw)
	if len(next.Sources.Sources) != 1 || next.Sources.Sources[0].EvidenceHash == "" {
		t.Fatalf("evidence not frozen: %s", raw)
	}
	paths, e := s.documents.Store.ContentPaths(id)
	if e != nil {
		t.Fatal(e)
	}
	for _, path := range paths {
		if strings.Contains(path, "evidence") {
			t.Fatal("runtime evidence in Git allowlist")
		}
	}
	body, e := s.documents.Export(id, next.Head.WorkingRevisionID, "draft")
	if e != nil || strings.Contains(body, "PRIVATE ORIGINAL") {
		t.Fatal("evidence leaked to export")
	}
	if next.Freshness == "current" {
		t.Fatal("source replacement reused report")
	}
	hash := strings.TrimPrefix(next.Sources.Sources[0].EvidenceHash, "sha256:")
	l, e := objectlocation.For(s.workspace.RootPath).Lookup(id)
	if e != nil {
		t.Fatal(e)
	}
	if e = os.Remove(filepath.Join(s.workspace.RootPath, l.Placement.EvidenceRoot, hash+".txt")); e != nil {
		t.Fatal(e)
	}
	missing, e := s.documents.Get(id)
	if e != nil || missing.SourceFreshness != "unavailable" || missing.Freshness != "unavailable" {
		t.Fatalf("missing evidence claimed available: %+v %v", missing, e)
	}

}

func TestDocumentDeletionRevokesPublicLinksExportsAndWrites(t *testing.T) {
	s, id := documentHTTPFixture(t, "design")
	snap, e := s.documents.Get(id)
	if e != nil {
		t.Fatal(e)
	}
	report, e := s.documents.Validate(context.Background(), id, snap.Head.WorkingRevisionID, snap.Head.Generation)
	if e != nil {
		t.Fatal(e)
	}
	req := documentflow.FinalizeRequest{RevisionID: report.RevisionID, ReportID: report.ReportID, OperationID: "deleted-public-final", DeliveryLevel: "proposal"}
	for _, issue := range report.Issues {
		if issue.HumanConfirmation {
			req.Confirmations = append(req.Confirmations, models.DocumentConfirmation{IssueID: issue.ID, Reason: "Inspected exact revision"})
		}
	}
	if _, e = s.documents.Finalize(id, snap.Head.Generation, req); e != nil {
		t.Fatal(e)
	}
	s.config.Server.PublicThoughtsEnabled = true
	public := httptest.NewRequest("GET", "/p/documents/"+id+"/revisions/"+report.RevisionID, nil)
	before := httptest.NewRecorder()
	s.handlePublicDocument(public.Context(), before, public)
	if before.Code != 200 {
		t.Fatal("public fixture did not return final", before.Code)
	}
	if e = s.captureService.DeleteThought(context.Background(), id, "delete-document"); e != nil {
		t.Fatal(e)
	}
	after := httptest.NewRecorder()
	s.handlePublicDocument(public.Context(), after, public)
	if after.Code != 404 || strings.Contains(after.Body.String(), snap.Body) {
		t.Fatal("deleted final remained public", after.Code)
	}
	base := "/api/documents/" + id
	for _, path := range []string{base, base + "/validation-reports/" + report.ReportID, base + "/revisions/" + report.RevisionID + "/export?mode=final", base + "/revisions/" + report.RevisionID + "/export?mode=draft"} {
		status, raw := documentCall(t, s, "GET", path, nil, nil)
		if status != 404 {
			t.Fatalf("deleted read %s: %d %s", path, status, raw)
		}
	}
	for _, operation := range []string{"revisions", "validate", "repair", "finalize"} {
		status, raw := documentCall(t, s, "POST", base+"/"+operation, req, &snap.Head.Generation)
		if status != 404 {
			t.Fatalf("deleted write %s: %d %s", operation, status, raw)
		}
	}
	if e = s.captureService.DeleteThought(context.Background(), id, "delete-retry"); e != nil {
		t.Fatal("deletion retry failed", e)
	}
	if _, e = s.documents.Store.Revision(id, report.RevisionID); e != nil {
		t.Fatal("internal history was destroyed", e)
	}
}
