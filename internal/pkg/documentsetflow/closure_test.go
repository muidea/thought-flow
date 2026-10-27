package documentsetflow

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/objectlocation"
)

func TestProjectionFailureGatesEntireGenericSetAndRecovers(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	projections := 0
	s.Store.Fault = func(step string) error {
		if step == "before_projection" {
			projections++
			if projections == 2 {
				return errors.New("projection disk failure")
			}
		}
		return nil
	}
	start(t, s, p, "partial-projection")
	r := wait(t, s, "partial-projection")
	if r.State != "committed" || r.Error == "" {
		t.Fatal(r.State, r.Error)
	}
	for _, a := range r.Candidate.Members {
		if _, e := markdown.ReadThoughtRaw(s.Store.Root, a.DocumentID); !os.IsNotExist(e) {
			t.Fatal("partial generic publication", a.Key, e)
		}
		if _, e := s.Store.Member(p.SetID, r.Candidate.ID, a.Key); e != nil {
			t.Fatal("complete fixed set unavailable", e)
		}
	}
	s.Store.Fault = nil
	recovered, e := s.RecoverRun(r.ID)
	if e != nil || recovered.Error != "" || len(recovered.Calls) != len(r.Calls) {
		t.Fatal("recovery must repair without new calls", recovered.Error, e)
	}
	if _, e = s.RecoverRun(r.ID); e != nil {
		t.Fatal("idempotent recovery", e)
	}
	for _, a := range r.Candidate.Members {
		if _, _, e := markdown.ReadThought(s.Store.Root, a.DocumentID); e != nil {
			t.Fatal(e)
		}
	}
}

func TestFinalizationIntentRecoveryAndHistoricalIdempotency(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "approval-run")
	run := wait(t, s, "approval-run")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	h, _ := s.Store.Head(p.SetID)
	req := FinalizeRequest{OperationID: "approve-first", ExpectedGeneration: &h.Generation, RevisionID: h.Working, RevisionHash: run.Candidate.Hash, ApprovedBy: "tester", DeliveryLevel: "ready_for_use"}
	s.Store.Fault = func(step string) error {
		if step == "finalization_intent_written" {
			return errors.New("pre-head stop")
		}
		return nil
	}
	if _, e := s.Finalize(p.SetID, req); e == nil {
		t.Fatal("fault missed")
	}
	s.Store.Fault = nil
	if issues := s.Recover(); len(issues) > 0 {
		t.Fatal(issues)
	}
	untouched, _ := s.Store.Head(p.SetID)
	if untouched.Finalized != "" {
		t.Fatal("uncommitted approval replayed")
	}
	s.Store.Fault = func(step string) error {
		if step == "finalization_head_published" {
			return errors.New("post-head stop")
		}
		return nil
	}
	if _, e := s.Finalize(p.SetID, req); e == nil {
		t.Fatal("fault missed")
	}
	s.Store.Fault = nil
	if issues := s.Recover(); len(issues) > 0 {
		t.Fatal(issues)
	}
	final, _ := s.Store.Head(p.SetID)
	if final.Finalized != h.Working || len(final.Finalizations) != 1 {
		t.Fatal(final)
	}
	req2 := req
	req2.OperationID = "approve-again"
	req2.ExpectedGeneration = &final.Generation
	if _, e := s.Finalize(p.SetID, req2); e != nil {
		t.Fatal(e)
	}
	if replay, e := s.Finalize(p.SetID, req); e != nil || replay.Finalized != h.Working {
		t.Fatal("historical same-op replay", e)
	}
	latest, e := s.Store.Head(p.SetID)
	if e != nil || len(latest.Finalizations) != 2 {
		t.Fatal(latest, e)
	}
	// A mutable current pointer cannot upgrade or alter an immutable approval.
	for _, mutate := range []func(*ds.Finalization){
		func(f *ds.Finalization) { f.DeliveryLevel = "proposal" },
		func(f *ds.Finalization) { f.ReportHash = "wrong" },
		func(f *ds.Finalization) { f.RevisionHash = "wrong" },
		func(f *ds.Finalization) { f.ApprovedBy = "different reviewer" },
	} {
		changed := latest
		approval := *latest.Finalization
		mutate(&approval)
		changed.Finalization = &approval
		if e := objectlocation.WriteJSON(s.Store.Root, documentsetstore.Base(p.SetID)+"/head.json", changed, false); e != nil {
			t.Fatal(e)
		}
		if _, e := s.Store.Head(p.SetID); !errors.Is(e, ds.ErrInvalid) {
			t.Fatal("altered approval accepted", e)
		}
	}
	if e := objectlocation.WriteJSON(s.Store.Root, documentsetstore.Base(p.SetID)+"/head.json", latest, false); e != nil {
		t.Fatal(e)
	}
}

func TestFrozenSetResourcesAndDisabledGenerationRemainReadable(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	start(t, s, p, "frozen-resources")
	run := wait(t, s, "frozen-resources")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	a := run.Candidate.Members[0]
	r := objectlocation.For(s.Store.Root)
	l, e := r.Lookup(a.DocumentID)
	if e != nil {
		t.Fatal(e)
	}
	canary := []byte("LATE-UPLOAD-MUST-NOT-ENTER-OLD-DELIVERY")
	if _, e = r.UploadResource(context.Background(), p.SetID, a.DocumentID, "late-upload", "late.txt", l.Generation, canary); e != nil {
		t.Fatal(e)
	}
	old, e := s.Store.Read(p.SetID, run.Candidate.ID)
	if e != nil {
		t.Fatal(e)
	}
	for _, m := range old.Members {
		if len(m.Resources.Bindings) != 0 {
			t.Fatal("late resource entered immutable set")
		}
	}
	s.opts.Enabled = false
	if _, e = s.Start(RunRequest{}); !errors.Is(e, ds.ErrUnavailable) {
		t.Fatal(e)
	}
	h, _ := s.Store.Head(p.SetID)
	final, e := s.Finalize(p.SetID, FinalizeRequest{OperationID: "disabled-finalize", ExpectedGeneration: &h.Generation, RevisionID: h.Working, RevisionHash: old.Hash, ApprovedBy: "tester", DeliveryLevel: "ready_for_use"})
	if e != nil {
		t.Fatal(e)
	}
	raw, e := s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "old-delivery", ExpectedGeneration: &final.Generation, RevisionID: h.Working, Kind: "delivery"})
	if e != nil {
		t.Fatal(e)
	}
	z, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil {
		t.Fatal(e)
	}
	for _, f := range z.File {
		reader, e := f.Open()
		if e != nil {
			t.Fatal(e)
		}
		data, e := io.ReadAll(reader)
		reader.Close()
		if e != nil {
			t.Fatal(e)
		}
		if strings.Contains(string(data), string(canary)) {
			t.Fatal("unselected resource leaked", f.Name)
		}
	}
	// A later location change must not invalidate the old immutable resource list.
	if _, _, e = markdown.ReadThought(s.Store.Root, a.DocumentID); e != nil {
		t.Fatal(e)
	}
}

func TestCorruptRecoveryDoesNotBlockUnrelatedSet(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	if e := objectlocation.WriteFile(s.Store.Root, documentsetstore.Private("set-corrupt")+"/intents/broken.json", []byte("{"), 0600); e != nil {
		t.Fatal(e)
	}
	start(t, s, p, "independent")
	run := wait(t, s, "independent")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	issues := s.Recover()
	if len(issues) == 0 {
		t.Fatal("corruption not diagnosed")
	}
	if _, e := s.Store.Read(p.SetID, run.Candidate.ID); e != nil {
		t.Fatal(e)
	}
}

func TestBoundedCallsRetryOnlyProvenUnexecutedFailures(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	for _, known := range []bool{false, true} {
		id := "uncertain-call"
		if known {
			id = "safe-call"
		}
		run := ds.Run{ID: id, OperationID: id, Plan: p, State: "generating"}
		if e := s.Store.SaveRun(run); e != nil {
			t.Fatal(e)
		}
		calls := 0
		_, e := boundedCall(context.Background(), s, &run, "member", "architecture", map[string]string{"input": "bounded"}, func(context.Context) (string, error) {
			calls++
			return "", ai.ProviderError{StatusCode: 503, Retryable: true, AutoRetry: known, Message: "controlled failure"}
		})
		if e == nil {
			t.Fatal("failure swallowed")
		}
		want := 1
		if known {
			want = 3
		}
		if !known && run.Calls[0].State != "uncertain" {
			t.Fatal("unknown execution treated as known failure")
		}
		if calls != want || len(run.Calls) != want {
			t.Fatal("unsafe retries or lost ledger", known, calls, len(run.Calls))
		}
	}
}

func TestPlannerCannotRewriteConfirmedFactsUsingOldEvidence(t *testing.T) {
	capture, shared := fixture()
	p, e := ds.BuildPlan("guard-fixture", ds.Builtins()[0], capture, shared, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	next := shared
	next.Facts = append([]ds.Fact{}, shared.Facts...)
	next.Facts[0].Content = "Silently discard recovery"
	next.Facts = next.Facts[:len(next.Facts)-1]
	guardConfirmedFacts(&next, p)
	if next.Facts[0].Status != "proposed" || len(next.Questions) != 2 || next.Facts[len(next.Facts)-1].ID != "TEST-ORDER" {
		t.Fatal("confirmed scope silently replaced", next)
	}
}

func TestStatelessScopeAndModuleRenameHaveStableMembership(t *testing.T) {
	capture, shared := fixture()
	no := false
	shared.Conditions["has_persistence"] = &no
	kept := []ds.Fact{}
	for _, f := range shared.Facts {
		if f.Kind == "entity" {
			continue
		}
		if f.Kind == "interface" {
			f.Fields = []ds.Field{{ID: "id", Type: "string", Classification: "computed"}}
		}
		kept = append(kept, f)
	}
	shared.Facts = kept
	shared.Relations = shared.Relations[:3]
	inputHash := documentstore.HashJSON(shared)
	first, e := ds.BuildPlan("stateless", ds.Builtins()[0], capture, shared, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	if documentstore.HashJSON(shared) != inputHash {
		t.Fatal("planning mutated private source evidence")
	}
	if first.Omissions["storage"] == "" || len(first.Members) != 5 {
		t.Fatal(first.Omissions, first.Members)
	}
	for i := range shared.Facts {
		if shared.Facts[i].ID == "MOD-ORDER" {
			shared.Facts[i].Name = "Orders renamed"
		}
	}
	second, e := ds.BuildPlan("stateless", ds.Builtins()[0], capture, shared, 1, 2)
	if e != nil {
		t.Fatal(e)
	}
	for i, m := range first.Members {
		if second.Members[i].DocumentID != m.DocumentID || second.Members[i].Key != m.Key {
			t.Fatal("rename changed identity")
		}
	}
	first.ID = "fixed-plan"
	first.Hash = ds.PlanHash(first)
	if e = ds.ValidatePlan(first); e != nil {
		t.Fatal(e)
	}
	first.Members = first.Members[1:]
	first.Hash = ds.PlanHash(first)
	if e = ds.ValidatePlan(first); e == nil {
		t.Fatal("required role deletion accepted")
	}
}

func TestDefaultPublicationClosureExcludesPrivateConversationAndEvidence(t *testing.T) {
	s, _, p := setup(t, &generator{}, reviewer{})
	private := "PRIVATE-CONVERSATION-CANARY"
	if e := objectlocation.WriteFile(s.Store.Root, "topics/"+p.SetID+"/.private/evidence/canary.txt", []byte(private), 0600); e != nil {
		t.Fatal(e)
	}
	checked := false
	s.opts.Commit = func(paths []string) error {
		for _, path := range paths {
			if strings.Contains(path, ".private/") || strings.Contains(path, "/private/") {
				return errors.New("private path in Git closure")
			}
			raw, e := os.ReadFile(filepath.Join(s.Store.Root, filepath.FromSlash(path)))
			if e != nil {
				return e
			}
			if strings.Contains(string(raw), private) {
				return errors.New("private conversation in public closure")
			}
		}
		checked = true
		return nil
	}
	start(t, s, p, "private-canary")
	run := wait(t, s, "private-canary")
	if run.State != "committed" || !checked || run.Error != "" {
		t.Fatal(run.State, run.Error, checked)
	}
	// Existing orphan-projection cleanup must never remove retained set members.
	if e := markdown.CleanupOrphanThoughtTempFiles(filepath.Join(s.Store.Root, "topics", p.SetID)); e != nil {
		t.Fatal(e)
	}
	if _, e := s.Store.Read(p.SetID, run.Candidate.ID); e != nil {
		t.Fatal("retained closure lost by cleanup", e)
	}
	var schema struct {
		Minimum int `json:"minimum_writer_version"`
	}
	if e := objectlocation.ReadJSON(s.Store.Root, ".content-registry/schema.json", &schema); e != nil || schema.Minimum < 2 {
		t.Fatal("old writer not excluded", schema, e)
	}
}
