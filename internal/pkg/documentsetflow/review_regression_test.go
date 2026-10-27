package documentsetflow

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"thoughtflow/internal/pkg/ai"
	"thoughtflow/internal/pkg/documentflow"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func TestPendingProjectionChecksBothCrashWindows(t *testing.T) {
	for _, step := range []string{"projection_head_written", "projection_body_written"} {
		for _, edit := range []bool{false, true} {
			t.Run(step+map[bool]string{false: "/unchanged", true: "/edited"}[edit], func(t *testing.T) {
				s, _, p := setup(t, &generator{}, reviewer{})
				s.Store.Fault = func(at string) error {
					if at == step {
						return errors.New("power loss")
					}
					return nil
				}
				start(t, s, p, "pending-run")
				run := wait(t, s, "pending-run")
				if run.State != "committed" || run.Error == "" {
					t.Fatal(run.State, run.Error)
				}
				a := run.Candidate.Members[0]
				l, e := objectlocation.For(s.Store.Root).Lookup(a.DocumentID)
				if e != nil {
					t.Fatal(e)
				}
				path := filepath.Join(s.Store.Root, l.Placement.ThoughtPath)
				h, e := documentstore.New(s.Store.Root).Head(a.DocumentID)
				if e != nil || !h.PendingProjection || h.SetProjection == nil {
					t.Fatal(h, e)
				}
				if edit {
					if e = os.MkdirAll(filepath.Dir(path), 0755); e != nil {
						t.Fatal(e)
					}
					if e = os.WriteFile(path, []byte("MANUAL INPUT MUST SURVIVE\n"), 0644); e != nil {
						t.Fatal(e)
					}
				}
				s.Store.Fault = nil
				e = s.Store.Project(p.SetID)
				if edit {
					raw, _ := os.ReadFile(path)
					if !errors.Is(e, ds.ErrConflict) || string(raw) != "MANUAL INPUT MUST SURVIVE\n" {
						t.Fatal("lost edit", e, string(raw))
					}
					return
				}
				if e != nil {
					t.Fatal(e)
				}
				raw, e := os.ReadFile(path)
				if e != nil || !strings.Contains(string(raw), a.Body) {
					t.Fatal("projection not recovered", e)
				}
				h, _ = documentstore.New(s.Store.Root).Head(a.DocumentID)
				if h.PendingProjection || h.SetProjection != nil {
					t.Fatal("intent not cleared")
				}
			})
		}
	}
}

func approve(t *testing.T, s *Service, r ds.Run, operation string) (ds.Head, error) {
	t.Helper()
	h, e := s.Store.Head(r.Plan.SetID)
	if e != nil {
		t.Fatal(e)
	}
	return s.Finalize(r.Plan.SetID, FinalizeRequest{OperationID: operation, ExpectedGeneration: &h.Generation, RevisionID: h.Working, RevisionHash: r.Candidate.Hash, ApprovedBy: "tester", DeliveryLevel: "ready_for_use"})
}
func TestFinalizationDistinguishesMissingCaptureFromMissingSource(t *testing.T) {
	for _, kind := range []string{"deleted-source", "missing-capture", "missing-capture-with-sources", "permission"} {
		t.Run(kind, func(t *testing.T) {
			s, c, p := setup(t, &generator{}, reviewer{})
			sourceID := "20261027-100000-source"
			if kind == "deleted-source" || kind == "missing-capture-with-sources" {
				e := markdown.WriteThought(s.Store.Root, models.Thought{ID: sourceID, Type: "text"}, models.ThoughtContent{Original: "source content"})
				if e != nil {
					t.Fatal(e)
				}
				c.Sources.Sources = []models.DocumentSource{{ID: "S1", Type: "thought", Locator: "thought://" + sourceID, Hash: "sourcehash", Version: "v1", VerificationStatus: "verified"}}
				p.Capture.Sources = c.Sources
				p.Hash = ds.PlanHash(p)
				p.ID = "sources-plan"
				p.Hash = ds.PlanHash(p)
				if e = s.Store.SavePlan(p); e != nil {
					t.Fatal(e)
				}
				if e = objectlocation.WriteJSON(s.Store.Root, documentsetstore.Private(p.SetID)+"/captures/"+p.ID+".json", *c, true); e != nil {
					t.Fatal(e)
				}
			}
			start(t, s, p, "source-run")
			r := wait(t, s, "source-run")
			if r.State != "committed" {
				t.Fatal(r.State, r.Error)
			}
			s.opts.SnapshotUnderLease = func(string) (ds.CaptureSnapshot, error) {
				switch kind {
				case "deleted-source":
					if _, _, e := markdown.ReadThoughtUnderNamespace(s.Store.Root, sourceID); e != nil {
						return ds.CaptureSnapshot{}, ds.ErrSourceMissing
					}
				case "permission":
					return ds.CaptureSnapshot{}, os.ErrPermission
				default:
					return ds.CaptureSnapshot{}, ds.ErrCaptureMissing
				}
				return *c, nil
			}
			if kind == "deleted-source" {
				if e := markdown.DeleteThought(s.Store.Root, sourceID); e != nil {
					t.Fatal(e)
				}
			}
			h, e := approve(t, s, r, "approve-source")
			if kind == "missing-capture" {
				if e != nil || h.Finalized == "" {
					t.Fatal(h, e)
				}
			} else {
				if e == nil {
					t.Fatal("missing evidence accepted")
				}
				stored, _ := s.Store.Head(p.SetID)
				if stored.Finalized != "" {
					t.Fatal("approval mutated")
				}
			}
		})
	}
}

type fixturePlanner struct{ shared ds.SharedDesign }

func (p fixturePlanner) PlanDocumentSet(context.Context, ds.CaptureSnapshot, ds.Profile, *ds.SharedDesign) (ds.SharedDesign, error) {
	return p.shared, nil
}
func TestCaptureCanExplicitlyRemoveConfirmedModule(t *testing.T) {
	s, c, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "scope-original")
	old := wait(t, s, "scope-original")
	if old.State != "committed" {
		t.Fatal(old.Error)
	}
	_, next := fixture()
	facts := []ds.Fact{}
	for _, f := range next.Facts {
		if f.ID != "MOD-BILLING" {
			facts = append(facts, f)
		}
	}
	next.Facts = facts
	relations := []ds.Relation{}
	for _, r := range next.Relations {
		if r.From != "MOD-BILLING" && r.To != "MOD-BILLING" {
			relations = append(relations, r)
		}
	}
	next.Relations = relations
	c.Messages = append(c.Messages, ds.Message{Role: "user", Text: "Remove Billing and its verification relation; Order recovery remains covered by TEST-ORDER."})
	c.Generation++
	c.ContextHash = "context-delete"
	h, _ := s.Store.Head(p.SetID)
	request := PlanRequest{OperationID: "scope-without-permission", SetID: p.SetID, ExpectedGeneration: &h.Generation, CaptureGeneration: &c.Generation, ProfileID: p.Profile.ID, ProfileVersion: p.Profile.Version, ProfileHash: p.Profile.Hash}
	s.opts.Planner = fixturePlanner{next}
	blocked, e := s.Plan(context.Background(), c.SessionID, request)
	if e != nil {
		t.Fatal(e)
	}
	if len(blocked.Shared.Questions) == 0 {
		t.Fatal("unauthorized omission allowed")
	}
	next.ScopeChanges = []ds.ScopeChange{{FactID: "MOD-BILLING", Action: "remove", Status: "confirmed", Source: ds.Source{Message: 1, Quote: "Remove Billing and its verification relation"}, Reason: "Explicitly reduced scope", Affected: []string{"module:MOD-BILLING", "verification"}, Coverage: "Order recovery remains covered by TEST-ORDER"}}
	c.Messages = append(c.Messages, ds.Message{Role: "user", Text: "Confirmed: remove Billing from this delivery."})
	c.Generation++
	c.ContextHash = "context-confirm-delete"
	s.opts.Planner = fixturePlanner{next}
	request.OperationID = "scope-confirmed"
	request.CaptureGeneration = &c.Generation
	revised, e := s.Plan(context.Background(), c.SessionID, request)
	if e != nil {
		t.Fatal(e)
	}
	if len(revised.Members) != 5 || len(revised.Shared.Questions) > 0 {
		t.Fatal(revised.Shared.Questions, len(revised.Members))
	}
	start(t, s, revised, "scope-reduced")
	run := wait(t, s, "scope-reduced")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	if _, e = s.Store.Member(p.SetID, old.Candidate.ID, "module:MOD-BILLING"); e != nil {
		t.Fatal("old version lost", e)
	}
}

type recordingGenerator struct {
	mu       sync.Mutex
	requests []ai.DocumentGenerationRequest
}

func (g *recordingGenerator) GenerateDocument(ctx context.Context, r ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	g.mu.Lock()
	g.requests = append(g.requests, r)
	g.mu.Unlock()
	return (&generator{}).GenerateDocument(ctx, r)
}

type scopeReviewer struct {
	t    *testing.T
	seen bool
}

func (g *scopeReviewer) ReviewDocumentSet(ctx context.Context, items []ds.ReviewItem, members []ds.Artifact, shared ds.SharedDesign, capture ds.CaptureSnapshot) (ds.ReviewResponse, string, error) {
	for _, i := range items {
		if i.ID == "capture-scope" {
			if len(capture.Messages) == 0 || !strings.Contains(capture.Messages[0].Text, "persistent storage") {
				g.t.Error("original requirements omitted")
			}
			g.seen = true
		}
	}
	return (reviewer{}).ReviewDocumentSet(ctx, items, members, shared, capture)
}
func TestGenerationReceivesContractsAndImmutableUpstream(t *testing.T) {
	g := &recordingGenerator{}
	review := &scopeReviewer{t: t}
	s, _, p := setup(t, g, review)
	start(t, s, p, "context-run")
	r := wait(t, s, "context-run")
	if r.State != "committed" {
		t.Fatal(r.State, r.Error)
	}
	if !review.seen {
		t.Fatal("no independent scope review")
	}
	checked := 0
	for _, request := range g.requests {
		if request.Context.Title != "Order" && request.Context.Title != "Verification" {
			continue
		}
		checked++
		var payload struct {
			Upstream map[string]struct {
				ID       string `json:"document_id"`
				Revision string `json:"revision_id"`
				Hash     string `json:"body_hash"`
				Body     string `json:"body"`
			} `json:"upstream"`
		}
		if e := json.Unmarshal([]byte(request.Context.Body), &payload); e != nil {
			t.Fatal(e)
		}
		if len(payload.Upstream) == 0 {
			t.Fatal("only dependency IDs supplied")
		}
		for _, input := range payload.Upstream {
			frozen, e := documentstore.New(s.Store.Root).FrozenRevision(input.ID, input.Revision)
			if e != nil || input.Body != frozen.Body || input.Hash != frozen.Revision.BodyHash {
				t.Fatal("dependency content not frozen", e)
			}
		}
		for _, text := range []string{"API-ORDER", "authentication and an idempotency key", "ENTITY-ORDER", "Atomic persistence and rollback", "REL-DATA", "body_hash", "This scope section explains"} {
			if !strings.Contains(request.Context.Body, text) {
				t.Errorf("%s missing %s", request.Context.Title, text)
			}
		}
	}
	if checked != 2 {
		t.Fatal("did not inspect actual module and verification calls", checked)
	}
}

func TestSourceWarningSurvivesJointReviewAndNeedsRiskDecision(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "warning-base")
	run := wait(t, s, "warning-base")
	if run.State != "committed" {
		t.Fatal(run.Error)
	}
	r, _ := withPreparedMember(t, s, *run.Candidate, run.Candidate.Members[0].Body+"\n\nEvidence [S1].\n\n## References\n\n- [S1] Evidence: https://example.org/evidence\n")
	r.Report = ds.Validate(r)
	response, _, _ := (reviewer{}).ReviewDocumentSet(context.Background(), r.Report.Checklist, r.Members, r.Plan.Shared, ds.CaptureSnapshot{})
	if e := ds.ApplyReview(&r, response, "model_reviewed", "test"); e != nil {
		t.Fatal(e)
	}
	ds.Rehash(&r)
	if r.Report.Status == "ready" || ds.Ready(r) == nil {
		t.Fatal("warning silently approved")
	}
	count := 0
	for _, issue := range r.Report.Issues {
		if issue.Code == "single:source_unverified" {
			count++
			r.Report.ReviewDecisions = append(r.Report.ReviewDecisions, ds.ReviewDecision{IssueID: issue.ID, Reason: "Reviewed URL availability; retain unverified support risk", Locations: issue.Locations})
		}
	}
	if count != 1 {
		t.Fatal("known source warning missing", r.Report.Issues)
	}
	r.Report.Status = "ready"
	ds.Rehash(&r)
	if e := ds.Ready(r); e != nil {
		t.Fatal("explicit risk disposition rejected", e, r.Report.Issues)
	}
	// Exercise actual manual approval/publication and final delivery evidence.
	riskRun := ds.Run{ID: "risk-review-run", OperationID: "risk-review-run", Plan: r.Plan, Candidate: &r, State: "awaiting_review"}
	riskRun.Candidate.Report.Status = "awaiting_review"
	ds.Rehash(riskRun.Candidate)
	if e := s.Store.SaveRun(riskRun); e != nil {
		t.Fatal(e)
	}
	reviewed, e := s.Review(ReviewRequest{OperationID: "risk-review", ExpectedGeneration: &r.Plan.BaseGeneration, CandidateHash: r.Report.CandidateHash, RunID: riskRun.ID, Reviewer: "human reviewer", Evidence: r.Report.Evidence, Decisions: r.Report.ReviewDecisions})
	if e != nil || reviewed.State != "committed" {
		t.Fatal("risk approval failed", e, reviewed.State)
	}
	final, e := approve(t, s, reviewed, "risk-final")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "risk-delivery", ExpectedGeneration: &final.Generation, RevisionID: reviewed.Candidate.ID, Kind: "delivery"})
	if e != nil {
		t.Fatal(e)
	}
	zr, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	recorded := false
	for _, f := range zr.File {
		if f.Name == "review.json" {
			reader, _ := f.Open()
			body, _ := io.ReadAll(reader)
			reader.Close()
			recorded = strings.Contains(string(body), "single:source_unverified") && strings.Contains(string(body), "retain unverified support risk")
		}
	}
	if !recorded {
		t.Fatal("risk absent from delivered approval evidence")
	}
	r.Report.Issues = nil
	ds.Rehash(&r)
	if ds.Ready(r) == nil {
		t.Fatal("warning erased from approval evidence")
	}
}

// Produces a new valid set using an actual prepared immutable revision, leaving
// private candidate accessibility observable before Commit/Project.
func withPreparedMember(t *testing.T, s *Service, base ds.Revision, body string) (ds.Revision, ds.Artifact) {
	t.Helper()
	r := cloneRevision(t, base)
	r.ID = documentstore.ID("set-r-")
	r.OperationID = documentstore.ID("op-")
	r.Parent = base.ID
	h, _ := s.Store.Head(r.Plan.SetID)
	r.Plan.BaseGeneration = h.Generation
	var catalog models.TopicCatalog
	if e := objectlocation.ReadJSON(s.Store.Root, "topics/"+r.Plan.SetID+"/catalog.json", &catalog); e != nil {
		t.Fatal(e)
	}
	r.Plan.CatalogGeneration = catalog.Generation
	for _, m := range r.Plan.Members {
		r.Plan.Baselines[m.Key] = documentsetstore.WorkingHash(s.Store.Root, m.DocumentID)
	}
	a := &r.Members[0]
	head, e := documentstore.New(s.Store.Root).Head(a.DocumentID)
	if e != nil {
		t.Fatal(e)
	}
	d, e := documentstore.New(s.Store.Root).Prepare(a.DocumentID, head.Generation, documentstore.RevisionData{Body: body, Profile: a.ProfileSnapshot, Contract: a.Contract, Sources: a.Sources})
	if e != nil {
		t.Fatal(e)
	}
	a.Body = d.Body
	a.BodyHash = d.Revision.BodyHash
	a.RevisionID = d.Revision.RevisionID
	a.Resources = d.Resources
	a.ResourceHash = d.Revision.ResourceBindingsHash
	for i := range r.Members {
		if _, ok := r.Members[i].Dependencies[a.Key]; ok {
			r.Members[i].Dependencies[a.Key] = a.RevisionID
		}
	}
	r.Plan.Hash = ds.PlanHash(r.Plan)
	r.Report = ds.Validate(r)
	response, _, _ := (reviewer{}).ReviewDocumentSet(context.Background(), r.Report.Checklist, r.Members, r.Plan.Shared, ds.CaptureSnapshot{})
	if e = ds.ApplyReview(&r, response, "model_reviewed", "fixture"); e != nil {
		t.Fatal(e)
	}
	ds.Rehash(&r)

	return r, *a
}
func TestOrdinaryExportRejectsPreparedRevisionOfExistingMember(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "visibility-base")
	run := wait(t, s, "visibility-base")
	if run.State != "committed" {
		t.Fatal(run.Error)
	}
	old := run.Candidate.Members[0]
	r, a := withPreparedMember(t, s, *run.Candidate, old.Body+"\nCandidate private text.\n")
	flow := &documentflow.Service{Store: documentstore.New(s.Store.Root)}
	if _, e := flow.Export(a.DocumentID, a.RevisionID, "draft"); !os.IsNotExist(e) {
		t.Fatal("private revision exposed", e)
	}
	if _, e := flow.Export(old.DocumentID, old.RevisionID, "draft"); e != nil {
		t.Fatal("published history hidden", e)
	}
	if e := s.Store.Commit(r, r.Plan.BaseGeneration, "published", nil); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.Project(p.SetID); e != nil {
		t.Fatal(e)
	}
	for _, rev := range []string{a.RevisionID, old.RevisionID} {
		if _, e := flow.Export(a.DocumentID, rev, "draft"); e != nil {
			t.Fatal("committed revision hidden", e)
		}
	}
}

func TestPublicFrozenAttachmentDeliveryAndGitClosure(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "resource-base")
	run := wait(t, s, "resource-base")
	if run.State != "committed" {
		t.Fatal(run.Error)
	}
	a := run.Candidate.Members[0]
	registry := objectlocation.For(s.Store.Root)
	l, _ := registry.Lookup(a.DocumentID)
	resource, e := registry.UploadResource(context.Background(), p.SetID, a.DocumentID, "resource-upload", "proof.txt", l.Generation, []byte("public attachment proof"))
	if e != nil {
		t.Fatal(e)
	}
	png, e := base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+j3ioAAAAASUVORK5CYII=")
	if e != nil {
		t.Fatal(e)
	}
	l, _ = registry.Lookup(a.DocumentID)
	image, e := registry.UploadResource(context.Background(), p.SetID, a.DocumentID, "image-upload", "pixel.png", l.Generation, png)
	if e != nil {
		t.Fatal(e)
	}
	r, changed := withPreparedMember(t, s, *run.Candidate, a.Body+"\n[Proof](resource://"+resource.ID+")\n\n![Pixel](resource://"+image.ID+")\n")
	if e = s.Store.Commit(r, r.Plan.BaseGeneration, "resource-version", nil); e != nil {
		t.Fatal(e)
	}
	if e = s.Store.Project(p.SetID); e != nil {
		t.Fatal(e)
	}
	finalRun := ds.Run{Plan: r.Plan, Candidate: &r}
	h, e := approve(t, s, finalRun, "approve-resource")
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "public-delivery", ExpectedGeneration: &h.Generation, RevisionID: r.ID, Kind: "delivery"})
	if e != nil {
		t.Fatal("valid attachment refused", e)
	}
	zr, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	found := false
	imageFound := false
	for _, f := range zr.File {
		reader, _ := f.Open()
		b, _ := io.ReadAll(reader)
		reader.Close()
		if strings.HasPrefix(f.Name, "assets/") && string(b) == "public attachment proof" {
			found = true
		}
		if strings.HasPrefix(f.Name, "assets/") && bytes.Equal(b, png) {
			imageFound = true
		}
		if strings.HasSuffix(f.Name, ".md") && strings.Contains(string(b), "resource://") {
			t.Fatal("unrewritten asset", f.Name)
		}
	}
	if !found || !imageFound {
		t.Fatal("file or image not packaged")
	}
	// Only the explicit set closure is committed. No search or embedding succeeds.
	paths, e := s.Store.ContentPaths(p.SetID)
	if e != nil {
		t.Fatal(e)
	}
	git := func(dir string, args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, e := cmd.CombinedOutput(); e != nil {
			t.Fatalf("git %v: %s %v", args, out, e)
		}
	}
	git(s.Store.Root, "init", "-q")
	git(s.Store.Root, "config", "user.name", "fixture")
	git(s.Store.Root, "config", "user.email", "fixture@example.org")
	args := append([]string{"add", "--"}, paths...)
	git(s.Store.Root, args...)
	git(s.Store.Root, "commit", "-qm", "set closure")
	clone := t.TempDir()
	git(clone, "clone", "-q", s.Store.Root, "copy")
	root := filepath.Join(clone, "copy")
	for _, member := range r.Members {
		if _, _, e = markdown.ReadThought(root, member.DocumentID); e != nil {
			t.Fatal("clone ordinary read failed", e)
		}
	}
	_, b, e := objectlocation.For(root).ReadResource(context.Background(), resource.ID, changed.DocumentID)
	if e != nil || string(b) != "public attachment proof" {
		t.Fatal("clone resource failed", e)
	}
	if _, e = os.Stat(filepath.Join(root, documentsetstore.Private(p.SetID))); !os.IsNotExist(e) {
		t.Fatal("private recovery data committed")
	}
	if e = os.WriteFile(filepath.Join(s.Store.Root, resource.Path), []byte("corrupt"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "corrupt-delivery", ExpectedGeneration: &h.Generation, RevisionID: r.ID, Kind: "delivery"}); e == nil {
		t.Fatal("corrupt attachment delivered")
	}
	if e = os.Remove(filepath.Join(s.Store.Root, resource.Path)); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "missing-delivery", ExpectedGeneration: &h.Generation, RevisionID: r.ID, Kind: "delivery"}); e == nil {
		t.Fatal("missing resource delivered")
	}
	if e = os.WriteFile(filepath.Join(s.Store.Root, resource.Path), []byte("public attachment proof"), 0644); e != nil {
		t.Fatal(e)
	}
	resource.Shareable = false
	if e = objectlocation.WriteJSON(s.Store.Root, ".content-registry/resources/"+resource.ID+".json", resource, false); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "private-delivery", ExpectedGeneration: &h.Generation, RevisionID: r.ID, Kind: "delivery"}); e == nil {
		t.Fatal("non-shareable resource delivered")
	}
}

func TestConditionChangesCannotReuseOldScopeEvidence(t *testing.T) {
	capture, shared := fixture()
	plan, e := ds.BuildPlan("conditions-guard", ds.Builtins()[0], capture, shared, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	no := false
	condition := shared.Conditions["has_persistence"]
	condition.Value = &no
	shared.Conditions["has_persistence"] = condition
	guardConfirmedFacts(&shared, plan, capture)
	if shared.Conditions["has_persistence"].Status == "confirmed" {
		t.Fatal("old evidence changed scope without authorization")
	}
}

type scopeOmissionReviewer struct{}

func (scopeOmissionReviewer) ReviewDocumentSet(ctx context.Context, items []ds.ReviewItem, members []ds.Artifact, shared ds.SharedDesign, capture ds.CaptureSnapshot) (ds.ReviewResponse, string, error) {
	response, model, e := (reviewer{}).ReviewDocumentSet(ctx, items, members, shared, capture)
	for _, item := range items {
		if item.ID == "capture-scope" && shared.Conditions["has_persistence"].Value != nil && !*shared.Conditions["has_persistence"].Value && strings.Contains(capture.Messages[0].Text, "persistent storage") {
			response.Issues = append(response.Issues, ds.Issue{Code: "scope_omission", Severity: "error", Message: "Original user requested persistence; a planner omission cannot override it", Locations: []ds.Location{{Member: members[0].Key, Section: "scope", Evidence: "This scope section explains"}}})
		}
	}
	return response, model, e
}
func TestIndependentScopeReviewBlocksPlannerOmittingUserRequirement(t *testing.T) {
	s, c, p := setup(t, &generator{}, scopeOmissionReviewer{})
	_, shared := fixture()
	no := false
	// Even a planner misusing a real but unrelated quote is checked against the
	// independently frozen source, rather than its own reduced fact collection.
	shared.Conditions["has_persistence"] = ds.Condition{Value: &no, Status: "confirmed", Source: ds.Source{Message: 0, Quote: "Build ordering"}, Reason: "Planner incorrectly inferred no persistence"}
	facts := []ds.Fact{}
	for _, fact := range shared.Facts {
		if fact.Kind == "entity" {
			continue
		}
		if fact.Kind == "interface" {
			fact.Fields[0].Classification = "computed"
			fact.Fields[0].EntityID = ""
			fact.Fields[0].EntityField = ""
		}
		facts = append(facts, fact)
	}
	shared.Facts = facts
	relations := []ds.Relation{}
	for _, rel := range shared.Relations {
		if rel.To != "ENTITY-ORDER" && rel.From != "ENTITY-ORDER" {
			relations = append(relations, rel)
		}
	}
	shared.Relations = relations
	zero := uint64(0)
	s.opts.Planner = fixturePlanner{shared}
	revised, e := s.Plan(context.Background(), c.SessionID, PlanRequest{OperationID: "scope-omission-plan", SetID: p.SetID, ProfileID: p.Profile.ID, ProfileVersion: p.Profile.Version, ProfileHash: p.Profile.Hash, ExpectedGeneration: &zero, CaptureGeneration: &c.Generation})
	if e != nil {
		t.Fatal(e)
	}
	start(t, s, revised, "scope-omission-run")
	run := wait(t, s, "scope-omission-run")
	if run.State != "awaiting_review" {
		t.Fatal("unsupported exclusion published", run.State, run.Error)
	}
	if _, e = s.Store.Head(p.SetID); !os.IsNotExist(e) {
		t.Fatal("reduced scope became authoritative", e)
	}
}
