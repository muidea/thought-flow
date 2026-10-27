package documentsetflow

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

type generator struct {
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (g *generator) GenerateDocument(ctx context.Context, r ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	if g.entered != nil {
		g.once.Do(func() { close(g.entered) })
		select {
		case <-g.release:
		case <-ctx.Done():
			return models.DocumentDraft{}, ctx.Err()
		}
	}
	draft := models.DocumentDraft{Title: r.Context.Title, Sections: map[string]models.DocumentSection{}}
	for _, s := range r.Profile.Sections {
		draft.Sections[s.Key] = models.DocumentSection{Content: "This " + s.Key + " section explains the agreed boundaries and failure handling for " + r.Context.Title + "."}
	}
	return draft, nil
}

type reviewer struct{ bad bool }

func (r reviewer) ReviewDocumentSet(ctx context.Context, items []ds.ReviewItem, members []ds.Artifact, shared ds.SharedDesign, capture ds.CaptureSnapshot) (ds.ReviewResponse, string, error) {
	response := ds.ReviewResponse{Evidence: []ds.ReviewEvidence{}, Issues: []ds.Issue{}}
	for _, item := range items {
		entry := ds.ReviewEvidence{CheckID: item.ID, Verdict: "passed", Reason: "Reviewed final definitions, responsibility and failure handling"}
		for _, key := range item.Members {
			for _, m := range members {
				if m.Key == key {
					entry.Locations = append(entry.Locations, ds.Location{Member: key, Section: "scope", Evidence: "This scope section explains"})
				}
			}
		}
		response.Evidence = append(response.Evidence, entry)
	}
	if r.bad {
		response.Evidence = nil
	}
	return response, "deterministic-reviewer", nil
}
func fixture() (ds.CaptureSnapshot, ds.SharedDesign) {
	yes := true
	c := ds.CaptureSnapshot{SessionID: "capture-fixture", Generation: 1, Messages: []ds.Message{{Role: "user", Text: "Build ordering with API and persistent storage. Modules: Order and Billing. Verify recovery. PRIVATE-CONVERSATION-CANARY."}}, ScopeHash: "scope-1", ContextHash: "context-1", Sources: models.SourceManifest{SchemaVersion: 1}}
	facts := []ds.Fact{
		{ID: "REQ-ORDER", Kind: "requirement", Name: "Order flow", Content: "Orders must be recoverable.", Owner: "architecture"},
		{ID: "MOD-ORDER", Kind: "module", Name: "Order", Content: "Own order lifecycle.", Owner: "module:MOD-ORDER"},
		{ID: "MOD-BILLING", Kind: "module", Name: "Billing", Content: "Own billing lifecycle.", Owner: "module:MOD-BILLING"},
		{ID: "API-ORDER", Kind: "interface", Name: "Create order", Content: "POST /orders requires authentication and an idempotency key.", Owner: "interfaces", Fields: []ds.Field{{ID: "id", Type: "string", Classification: "persisted", EntityID: "ENTITY-ORDER", EntityField: "id", Permission: "user", Errors: []string{"409"}}}},
		{ID: "ENTITY-ORDER", Kind: "entity", Name: "Order record", Content: "Atomic persistence and rollback preserve consistency.", Owner: "storage", Fields: []ds.Field{{ID: "id", Type: "string", Classification: "persisted"}}},
		{ID: "TEST-ORDER", Kind: "verification", Name: "Recovery check", Content: "Exercise failure and recovery of both modules.", Owner: "verification"},
	}
	for i := range facts {
		facts[i].Status = "confirmed"
		facts[i].Source = ds.Source{Message: 0, Quote: "Build ordering"}
		facts[i].Reason = "Captured explicit scope"
	}
	return c, ds.SharedDesign{Facts: facts, Relations: []ds.Relation{{ID: "REL-REQ", Kind: "verification", From: "REQ-ORDER", To: "TEST-ORDER"}, {ID: "REL-ORDER", Kind: "verification", From: "MOD-ORDER", To: "TEST-ORDER"}, {ID: "REL-BILLING", Kind: "verification", From: "MOD-BILLING", To: "TEST-ORDER"}, {ID: "REL-DATA", Kind: "mapping", From: "API-ORDER", To: "ENTITY-ORDER"}}, Conditions: map[string]ds.Condition{"has_interfaces": {Value: &yes, Status: "confirmed", Source: ds.Source{Message: 0, Quote: "with API"}, Reason: "User scope"}, "has_persistence": {Value: &yes, Status: "confirmed", Source: ds.Source{Message: 0, Quote: "persistent storage"}, Reason: "User scope"}}, Inputs: map[string]string{"goal": "Recoverable orders", "scope": "Ordering and billing", "constraints": "Local persistence"}}
}
func setup(t *testing.T, g ai.DocumentGenerationProvider, review Reviewer) (*Service, *ds.CaptureSnapshot, ds.Plan) {
	t.Helper()
	root := t.TempDir()
	snapshot, shared := fixture()
	s := New(root, Options{Enabled: true, Generator: g, Reviewer: review, Snapshot: func(string) (ds.CaptureSnapshot, error) { return snapshot, nil }, ModelHash: "fixture"})
	t.Cleanup(s.Close)
	profiles := s.Profiles()
	p := ds.Builtins()[0]
	for _, p := range profiles {
		if e := ds.ValidateProfile(p); e != nil {
			t.Fatal(e)
		}
	}
	zero, one := uint64(0), uint64(1)
	plan, e := s.Plan(context.Background(), snapshot.SessionID, PlanRequest{OperationID: "plan-1", Name: "Feature set", ExpectedGeneration: &zero, CaptureGeneration: &one, ProfileID: p.ID, ProfileVersion: p.Version, ProfileHash: p.Hash, Shared: &shared})
	if e != nil {
		t.Fatal(e)
	}
	if len(plan.Members) != 6 {
		t.Fatalf("members %d", len(plan.Members))
	}
	return s, &snapshot, plan
}
func start(t *testing.T, s *Service, p ds.Plan, id string) ds.Run {
	t.Helper()
	r, e := s.Start(RunRequest{OperationID: id, SetID: p.SetID, PlanID: p.ID, PlanHash: p.Hash, ExpectedGeneration: &p.BaseGeneration, CaptureGeneration: &p.Capture.Generation})
	if e != nil {
		t.Fatal(e)
	}
	return r
}
func wait(t *testing.T, s *Service, id string) ds.Run {
	t.Helper()
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		r, e := s.Store.Run(id)
		if e != nil {
			t.Fatal(e)
		}
		switch r.State {
		case "committed", "awaiting_review", "failed_terminal", "failed_retryable", "cancelled", "stale":
			// Durable terminal state is written just before worker teardown.
			// Join that boundary before tests replace callbacks or inspect data
			// produced by them; filesystem polling is not a memory barrier.
			s.mu.Lock()
			finished := s.running[id] == nil
			s.mu.Unlock()
			if finished {
				return r
			}
		}
		time.Sleep(time.Millisecond * 5)
	}
	r, _ := s.Store.Run(id)
	t.Fatalf("run stuck: %s: %s", r.State, r.Error)
	return ds.Run{}
}
func TestSixMemberSetAndPinnedRevisions(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "run-1")
	r := wait(t, s, "run-1")
	if r.State != "committed" {
		t.Fatalf("%s: %s %+v", r.State, r.Error, r.Candidate)
	}
	h, e := s.Store.Head(p.SetID)
	if e != nil || h.Working == "" || h.Finalized != "" {
		t.Fatalf("head %+v %v", h, e)
	}
	for _, a := range r.Candidate.Members {
		t1, c, e := markdown.ReadThought(s.Store.Root, a.DocumentID)
		if e != nil {
			t.Fatal(e)
		}
		if c.AINotes != a.Body || t1.Document.RevisionID != a.RevisionID {
			t.Fatalf("projection differs %s", a.Key)
		}
		_, _, e = markdown.ReadThought(s.Store.Root, a.DocumentID)
		if e != nil {
			t.Fatal(e)
		}
		personal, _ := documentstore.New(s.Store.Root).Head(a.DocumentID)
		if personal.WorkingRevisionID != a.RevisionID {
			t.Fatalf("read created revision %s", a.Key)
		}
	}
	f, e := s.Finalize(p.SetID, FinalizeRequest{OperationID: "final-1", ExpectedGeneration: &h.Generation, RevisionID: h.Working, RevisionHash: r.Candidate.Hash, ApprovedBy: "workspace user", DeliveryLevel: "ready_for_use"})
	if e != nil || f.Finalized != h.Working {
		t.Fatalf("finalize %v %+v", e, f)
	}
}
func TestJointConflictAndMissingReviewCannotPublish(t *testing.T) {
	for _, which := range []string{"type-conflict", "missing-review"} {
		t.Run(which, func(t *testing.T) {
			s, _, p := setup(t, &generator{}, reviewer{bad: which == "missing-review"})
			if which == "type-conflict" {
				for i := range p.Shared.Facts {
					if p.Shared.Facts[i].ID == "API-ORDER" {
						p.Shared.Facts[i].Fields[0].Type = "integer"
					}
				}
				p.ID = "plan-conflict"
				p.Hash = ds.PlanHash(p)
				if e := s.Store.SavePlan(p); e != nil {
					t.Fatal(e)
				}
			}
			start(t, s, p, "run-blocked")
			r := wait(t, s, "run-blocked")
			if r.State != "awaiting_review" {
				t.Fatalf("%s %s", r.State, r.Error)
			}
			if _, e := s.Store.Head(p.SetID); !os.IsNotExist(e) {
				t.Fatalf("blocked set published: %v", e)
			}
			for _, m := range p.Members {
				if _, e := objectlocation.For(s.Store.Root).Resolve(context.Background(), m.DocumentID); !os.IsNotExist(e) {
					t.Fatalf("pending member readable: %v", e)
				}
			}
		})
	}
}
func TestCancellationDiscardsLateResultAndIdempotentStart(t *testing.T) {
	g := &generator{entered: make(chan struct{}), release: make(chan struct{})}
	s, _, p := setup(t, g, reviewer{})
	r := start(t, s, p, "run-cancel")
	<-g.entered
	again := start(t, s, p, "run-cancel")
	if r.ID != again.ID {
		t.Fatal("not idempotent")
	}
	if _, e := s.Cancel(r.ID); e != nil {
		t.Fatal(e)
	}
	close(g.release)
	r = wait(t, s, r.ID)
	if r.State != "cancelled" {
		t.Fatal(r.State)
	}
	s.Close()
	if _, e := s.Store.Head(p.SetID); !os.IsNotExist(e) {
		t.Fatal("cancelled published")
	}
}
func TestRecoverAfterCommitBeforeProjections(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	s.Store.Fault = func(step string) error {
		if step == "set_head_published" {
			return errors.New("power loss")
		}
		return nil
	}
	start(t, s, p, "run-crash")
	r := wait(t, s, "run-crash")
	if r.State != "failed_terminal" {
		t.Fatal(r.State, r.Error)
	}
	s.Store.Fault = nil
	if errs := s.Recover(); len(errs) != 0 {
		t.Fatal(errs)
	}
	r, _ = s.Store.Run(r.ID)
	if r.State != "committed" {
		t.Fatal(r.State)
	}
	for _, a := range r.Candidate.Members {
		if _, _, e := markdown.ReadThought(s.Store.Root, a.DocumentID); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRecoveryNeverOverwritesLaterManualEdit(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "run-manual")
	r := wait(t, s, "run-manual")
	if r.State != "committed" {
		t.Fatal(r.State, r.Error)
	}
	a := r.Candidate.Members[0]
	if e := markdown.UpdateThought(s.Store.Root, a.DocumentID, func(t *models.Thought, c *models.ThoughtContent) error {
		c.AINotes += "\nManual addition retained.\n"
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if e := s.Store.Project(p.SetID); e != nil {
		t.Fatal(e)
	}
	_, content, e := markdown.ReadThought(s.Store.Root, a.DocumentID)
	if e != nil || !strings.Contains(content.AINotes, "Manual addition retained.") {
		t.Fatal("manual edit lost", e)
	}
	pinned, e := s.Store.Member(p.SetID, r.Candidate.ID, a.Key)
	if e != nil || strings.Contains(pinned.Body, "Manual addition retained.") {
		t.Fatal("fixed revision drifted", e)
	}
}
func TestOriginalPendingRawAndResourceGate(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{bad: true})
	start(t, s, p, "run-private")
	r := wait(t, s, "run-private")
	if r.State != "awaiting_review" {
		t.Fatal(r.State)
	}
	for _, m := range p.Members {
		if _, e := markdown.ReadThoughtRaw(s.Store.Root, m.DocumentID); !os.IsNotExist(e) {
			t.Fatal("raw leaked", e)
		}
	}
}
func TestSharedFactImpactAllowsModuleCycles(t *testing.T) {
	_, shared := fixture()
	shared.Relations = append(shared.Relations, ds.Relation{ID: "cycle-a", From: "MOD-ORDER", To: "MOD-BILLING", Kind: "depends"}, ds.Relation{ID: "cycle-b", From: "MOD-BILLING", To: "MOD-ORDER", Kind: "depends"})
	c, _ := fixture()
	p, e := ds.BuildPlan("set-test", ds.Builtins()[0], c, shared, 0, 1)
	if e != nil || len(p.Members) != 6 {
		t.Fatal(e)
	}
	other := p.Shared
	other.Facts = append([]ds.Fact{}, p.Shared.Facts...)
	other.Facts[3].Hash = "changed"
	if len(ds.Impact(p.Shared, other)) < 2 {
		t.Fatal("impact closure missing")
	}
}

var _ = fmt.Sprint
var _ = filepath.Separator
var _ = documentsetstore.Base

func TestBackupRoundTripAndImportConflictRecovery(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "run-package")
	r := wait(t, s, "run-package")
	if r.State != "committed" {
		t.Fatal(r.State, r.Error)
	}
	h, _ := s.Store.Head(p.SetID)
	raw, e := s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "backup-1", ExpectedGeneration: &h.Generation, RevisionID: h.Working, Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	restored := documentsetstore.New(t.TempDir())
	state, e := restored.Import(raw, "import-1")
	if e != nil || state.State != "completed" {
		t.Fatalf("restore %v %+v", e, state)
	}
	v, e := restored.Read(p.SetID, h.Working)
	if e != nil || len(v.Members) != 6 {
		t.Fatal("lost set", e)
	}
	for _, a := range v.Members {
		if _, _, e := markdown.ReadThought(restored.Root, a.DocumentID); e != nil {
			t.Fatal(e)
		}
	}
	if _, e = restored.Import(raw, "another-import"); !errors.Is(e, ds.ErrConflict) {
		t.Fatal("overwrote identity", e)
	}
	conflict := documentsetstore.New(t.TempDir())
	conflict.Fault = func(step string) error {
		if step == "import_validated" {
			return errors.New("power loss")
		}
		return nil
	}
	_, e = conflict.Import(raw, "crash-import")
	if e == nil {
		t.Fatal("fault not injected")
	}
	conflict.Fault = nil
	path := "topics/" + p.SetID + "/topic.yaml"
	canary := []byte("foreign topic preserved")
	if e = objectlocation.WriteFile(conflict.Root, path, canary, 0644); e != nil {
		t.Fatal(e)
	}
	if issues := conflict.RecoverImports(); len(issues) != 0 {
		t.Fatal("terminated conflict stopped recovery", issues)
	}
	content, e := os.ReadFile(filepath.Join(conflict.Root, path))
	if e != nil || string(content) != string(canary) {
		t.Fatal("foreign content lost", e)
	}
}
func TestManualReviewRequiresEntireChecklistAndHardErrorsNotWaivable(t *testing.T) {
	s, _, p := setup(t, &generator{}, nil)
	start(t, s, p, "manual-run")
	r := wait(t, s, "manual-run")
	if r.State != "awaiting_review" {
		t.Fatal(r.State, r.Error)
	}
	reviewer := reviewer{}
	response, _, e := reviewer.ReviewDocumentSet(context.Background(), r.Candidate.Report.Checklist, r.Candidate.Members, r.Plan.Shared, ds.CaptureSnapshot{})
	if e != nil {
		t.Fatal(e)
	}
	req := ReviewRequest{OperationID: "manual-review", ExpectedGeneration: &p.BaseGeneration, CandidateHash: r.Candidate.Report.CandidateHash, RunID: r.ID, Reviewer: "workspace user", Evidence: response.Evidence[:1]}
	if _, e = s.Review(req); e == nil {
		t.Fatal("partial manual review passed")
	}
	req.Evidence = response.Evidence
	reviewed, e := s.Review(req)
	if e != nil || reviewed.State != "committed" || reviewed.Candidate.Report.SemanticStatus != "manual_reviewed" {
		t.Fatal("manual review", e, reviewed.State)
	}
	again, e := s.Review(req)
	if e != nil || again.ID != reviewed.ID {
		t.Fatal("review replay", e)
	}
}
func TestCommitCASAndProfileSnapshotIsolation(t *testing.T) {
	g := &generator{entered: make(chan struct{}), release: make(chan struct{})}
	s, _, p := setup(t, g, reviewer{})
	start(t, s, p, "cas-run")
	<-g.entered
	var c models.TopicCatalog
	if e := objectlocation.ReadJSON(s.Store.Root, "topics/"+p.SetID+"/catalog.json", &c); e != nil {
		t.Fatal(e)
	}
	c.Generation++
	objectlocation.WriteJSON(s.Store.Root, "topics/"+p.SetID+"/catalog.json", c, false)
	close(g.release)
	r := wait(t, s, "cas-run")
	if r.State != "stale" {
		t.Fatal(r.State, r.Error)
	}
	if _, e := s.Store.Head(p.SetID); !os.IsNotExist(e) {
		t.Fatal("stale run published")
	}
}
