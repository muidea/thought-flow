package documentsetflow

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"testing"

	"thoughtflow/internal/pkg/ai"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/testutil/designfixture"
)

type engineeringGenerator struct {
	mu       sync.Mutex
	requests []ai.DocumentGenerationRequest
	empty    bool
}

func (g *engineeringGenerator) GenerateDocument(_ context.Context, r ai.DocumentGenerationRequest) (models.DocumentDraft, error) {
	g.mu.Lock()
	g.requests = append(g.requests, r)
	g.mu.Unlock()
	out := models.DocumentDraft{Title: r.Context.Title, Sections: map[string]models.DocumentSection{}}
	if g.empty {
		return out, nil
	}
	for _, sec := range r.Profile.Sections {
		out.Sections[sec.Key] = models.DocumentSection{Content: "Order requests require authentication, validation and atomic persistence. Unauthorized requests return 403 and create no row. Restart preserves acknowledged orders. Verify with HTTP assertions and persisted row counts."}
	}
	return out, nil
}

type engineeringReviewer struct{}

func (engineeringReviewer) ReviewDocumentSet(_ context.Context, items []ds.ReviewItem, _ []ds.Artifact, _ ds.SharedDesign, _ ds.CaptureSnapshot) (ds.ReviewResponse, string, error) {
	out := ds.ReviewResponse{Evidence: []ds.ReviewEvidence{}}
	for _, item := range items {
		ev := ds.ReviewEvidence{CheckID: item.ID, Verdict: "passed", Reason: "Compared final explanations to frozen normative contracts"}
		for _, key := range item.Members {
			ev.Locations = append(ev.Locations, ds.Location{Member: key, Section: "scope", Evidence: "Order requests require authentication, validation and atomic persistence."})
		}
		out.Evidence = append(out.Evidence, ev)
	}
	return out, "independent-fixture-reviewer", nil
}
func engineeringSetup(t *testing.T, g *engineeringGenerator) (*Service, ds.Plan, *ds.CaptureSnapshot) {
	t.Helper()
	capture, shared := designfixture.Capture()
	s := New(t.TempDir(), Options{Enabled: true, Generator: g, Reviewer: engineeringReviewer{}, Snapshot: func(string) (ds.CaptureSnapshot, error) { return capture, nil }, ModelHash: "engineering-model"})
	t.Cleanup(s.Close)
	p := ds.Builtins()[2]
	zero, one := uint64(0), uint64(1)
	plan, e := s.Plan(context.Background(), capture.SessionID, PlanRequest{OperationID: "engineering-plan", ExpectedGeneration: &zero, CaptureGeneration: &one, ProfileID: p.ID, ProfileVersion: p.Version, ProfileHash: p.Hash, Shared: &shared})
	if e != nil {
		t.Fatal(e)
	}
	return s, plan, &capture
}
func TestEngineeringGenerationPublicationHandoffAndRestore(t *testing.T) {
	g := &engineeringGenerator{}
	s, plan, _ := engineeringSetup(t, g)
	if len(plan.Shared.Questions) > 0 {
		t.Fatal(plan.Shared.Questions)
	}
	start(t, s, plan, "engineering-run")
	run := wait(t, s, "engineering-run")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error, run.Candidate)
	}
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.requests) != len(plan.Members) {
		t.Fatal("missing generator calls")
	}
	for _, req := range g.requests {
		if req.Purpose != ds.EngineeringPurpose {
			t.Fatal("purpose not propagated")
		}
		if !strings.Contains(req.Context.Body, `"scope"`) || !strings.Contains(req.Context.Body, `"obligations"`) {
			t.Fatal("scope missing")
		}
		if req.Profile.Ref.ProfileID == "builtin.engineering-module" {
			for _, text := range []string{"API-ORDER", "ENTITY-ORDER", "RULE-RECOVERY", "body_hash", "Order requests require authentication"} {
				if !strings.Contains(req.Context.Body, text) {
					t.Fatal("frozen upstream missing", text)
				}
			}
		}
	}
	head, e := approve(t, s, run, "engineering-approve")
	if e != nil {
		t.Fatal(e)
	}
	delivery, e := s.Store.Export(plan.SetID, documentsetstore.ExportRequest{OperationID: "engineering-export", ExpectedGeneration: &head.Generation, RevisionID: head.Finalized, Kind: "delivery", PublicAuthorized: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = documentsetstore.VerifyPublicPackage(delivery, run.Candidate.Report.PublicPayloadHash); e != nil {
		t.Fatal(e)
	}
	preview, e := s.Store.Handoff(plan.SetID, documentsetstore.HandoffRequest{ExpectedGeneration: &head.Generation, Revision: head.Finalized})
	if e != nil || len(preview.Input.Acceptance) != 0 || preview.Budget.Consumer != "NOT_RUN" {
		t.Fatal(preview, e)
	}
	backup, e := s.Store.Export(plan.SetID, documentsetstore.ExportRequest{OperationID: "engineering-backup", ExpectedGeneration: &head.Generation, RevisionID: head.Finalized, Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	restored := documentsetstore.New(t.TempDir())
	state, e := restored.Import(backup, "restore-engineering")
	if e != nil {
		t.Fatal(e)
	}
	r, e := restored.Read(state.SetID, head.Finalized)
	if e != nil || ds.Ready(r) != nil {
		t.Fatal("new format backup not ready", e)
	}
	raw, e := restored.Export(state.SetID, documentsetstore.ExportRequest{OperationID: "restored-delivery", ExpectedGeneration: &head.Generation, RevisionID: head.Finalized, Kind: "delivery", PublicAuthorized: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = documentsetstore.VerifyPublicPackage(raw, r.Report.PublicPayloadHash); e != nil {
		t.Fatal(e)
	}
}
func TestEngineeringEmptySectionsCannotBecomeCompletedContracts(t *testing.T) {
	s, plan, _ := engineeringSetup(t, &engineeringGenerator{empty: true})
	start(t, s, plan, "empty-engineering")
	run := wait(t, s, "empty-engineering")
	if run.State == "committed" || !strings.Contains(run.Error, "missing section") {
		t.Fatal(run.State, run.Error)
	}
}
func TestEngineeringNewPayloadsNeedNewApproval(t *testing.T) {
	capture, shared := designfixture.Capture()
	old, e := ds.BuildPlan("engineering", ds.Builtins()[2], capture, shared, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	for _, which := range []string{"modality", "acceptance", "quality", "state", "artifact", "scope", "provenance"} {
		t.Run(which, func(t *testing.T) {
			raw, _ := json.Marshal(shared)
			var next ds.SharedDesign
			json.Unmarshal(raw, &next)
			index := 0
			switch which {
			case "modality":
				next.Facts[0].Modality = "MAY"
			case "acceptance":
				index = 7
				next.Facts[index].Acceptance.PassRule = "weaker"
			case "quality":
				index = 6
				next.Facts[index].Quality.Outcome = "200"
			case "state":
				index = 5
				next.Facts[index].StateRule.Persistence = "volatile"
			case "artifact":
				index = 8
				next.Facts[index].Artifact.Run = "changed"
			case "scope":
				next.Facts[0].ScopeRefs = []string{"other"}
			case "provenance":
				next.Facts[0].Provenance[0].Kind = "reference"
			}
			guardConfirmedFacts(&next, old, capture)
			if next.Facts[index].Status == "confirmed" {
				t.Fatal("old quote authorized new normative payload")
			}
		})
	}
}
func TestEngineeringHistoricalEndpointsRequireRealAncestry(t *testing.T) {
	s, p, c := engineeringSetup(t, &engineeringGenerator{})
	start(t, s, p, "history-base")
	run := wait(t, s, "history-base")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	old := run.Candidate.Plan.Shared.Facts[0]
	ref := ds.FactReference{SetID: p.SetID, RevisionID: run.Candidate.ID, FactID: old.ID, Hash: old.Hash}
	for _, which := range []string{"valid", "wrong-hash", "non-ancestor", "wrong-set"} {
		t.Run(which, func(t *testing.T) {
			next := ds.SharedDesign{Facts: []ds.Fact{{ID: "NEW-A"}, {ID: "NEW-B"}}, Changes: []ds.FactChange{{Action: "split", From: []ds.FactReference{ref}, To: []string{"NEW-A", "NEW-B"}, Source: ds.Source{Message: 1, Quote: "Split"}, Reason: "authorized split", Coverage: "all former responsibilities"}}}
			switch which {
			case "wrong-hash":
				next.Changes[0].From[0].Hash = "sha256:wrong"
			case "non-ancestor":
				next.Changes[0].From[0].RevisionID = "not-retained"
			case "wrong-set":
				next.Changes[0].From[0].SetID = "other"
			}
			e := s.resolveHistory(p.SetID, run.Candidate.ID, &next)
			if (e == nil) != (which == "valid") {
				t.Fatal(which, e)
			}
			if which == "valid" {
				if len(next.History) != 1 || next.History[0].Fact.Hash != old.Hash {
					t.Fatal("verified snapshot not retained")
				}
			}
		})
	}
	_ = c
}

func TestEngineeringFrozenAttachmentDeliveryAndTamper(t *testing.T) {
	s, p, c := engineeringSetup(t, &engineeringGenerator{})
	start(t, s, p, "asset-base")
	base := wait(t, s, "asset-base")
	if base.State != "committed" {
		t.Fatal(base.State, base.Error)
	}
	registry := objectlocation.For(s.Store.Root)
	architecture := p.Members[0]
	loc, e := registry.Lookup(architecture.DocumentID)
	if e != nil {
		t.Fatal(e)
	}
	asset, e := registry.UploadResource(context.Background(), p.SetID, architecture.DocumentID, "engineering-asset", "设计说明.txt", loc.Generation, []byte("Approved supporting design evidence.\n"))
	if e != nil {
		t.Fatal(e)
	}
	_, shared := designfixture.Capture()
	c.Generation++
	c.ContextHash = "with-asset"
	c.Messages = append(c.Messages, ds.Message{Role: "user", Text: "Include the approved design attachment."})
	shared.Facts[0].Content += "\n[Supporting design](resource://" + asset.ID + ")"
	shared.Facts[0].Source = ds.Source{Message: 1, Quote: c.Messages[1].Text}
	head, e := s.Store.Head(p.SetID)
	if e != nil {
		t.Fatal(e)
	}
	next, e := s.Plan(context.Background(), c.SessionID, PlanRequest{OperationID: "asset-plan", SetID: p.SetID, ExpectedGeneration: &head.Generation, CaptureGeneration: &c.Generation, ProfileID: p.Profile.ID, ProfileVersion: p.Profile.Version, ProfileHash: p.Profile.Hash, Shared: &shared})
	if e != nil {
		t.Fatal(e)
	}
	start(t, s, next, "asset-run")
	run := wait(t, s, "asset-run")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	head, e = approve(t, s, run, "asset-final")
	if e != nil {
		t.Fatal(e)
	}
	req := documentsetstore.ExportRequest{OperationID: "asset-export", ExpectedGeneration: &head.Generation, RevisionID: head.Finalized, Kind: "delivery", PublicAuthorized: true}
	raw, e := s.Store.Export(p.SetID, req)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = documentsetstore.VerifyPublicPackage(raw, run.Candidate.Report.PublicPayloadHash); e != nil {
		t.Fatal(e)
	}
	if e = objectlocation.WriteFile(s.Store.Root, asset.Path, []byte("CHANGED SUPPORTING FILE"), 0644); e != nil {
		t.Fatal(e)
	}
	if _, e = s.Store.Export(p.SetID, req); e == nil {
		t.Fatal("changed resource kept old public approval")
	}
}

func TestEngineeringManagedNarrativeAndContractEditsStaySeparate(t *testing.T) {
	s, p, _ := engineeringSetup(t, &engineeringGenerator{})
	start(t, s, p, "managed-engineering")
	run := wait(t, s, "managed-engineering")
	if run.State != "committed" {
		t.Fatal(run.State, run.Error)
	}
	member := run.Candidate.Members[1]
	if e := markdown.UpdateThought(s.Store.Root, member.DocumentID, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes = strings.Replace(c.AINotes, "Unauthorized requests return 403 and create no row.", "Unauthorized requests return 403 and create no row. This narrative explains the authenticated caller boundary.", 1)
		return nil
	}); e != nil {
		t.Fatal("narrative edit incorrectly treated as a missing foreign contract", e)
	}
	if e := markdown.UpdateThought(s.Store.Root, member.DocumentID, func(_ *models.Thought, c *models.ThoughtContent) error {
		c.AINotes = strings.Replace(c.AINotes, `"scope`, `"changed_scope`, 1)
		return nil
	}); e == nil {
		t.Fatal("scope change silently forked the shared contract")
	}
}

func TestEngineeringSplitMergeRetireRestoreAndBackupClosure(t *testing.T) {
	s, p, c := engineeringSetup(t, &engineeringGenerator{})
	start(t, s, p, "lifecycle-base")
	base := wait(t, s, "lifecycle-base")
	if base.State != "committed" {
		t.Fatal(base.State, base.Error)
	}
	initial := *base.Candidate
	original := initial.Plan.Shared.Facts[0]
	previous := initial
	ref := func(r ds.Revision, id string) ds.FactReference {
		for _, f := range r.Plan.Shared.Facts {
			if f.ID == id {
				return ds.FactReference{SetID: p.SetID, RevisionID: r.ID, FactID: id, Hash: f.Hash}
			}
		}
		t.Fatal("missing lifecycle fact", id)
		return ds.FactReference{}
	}
	stages := []struct {
		action string
		ids    []string
		from   func() []ds.FactReference
	}{
		{"split", []string{"REQ-FAST", "REQ-DURABLE", "REQ-KEEP"}, func() []ds.FactReference { return []ds.FactReference{ref(previous, "REQ-ORDER")} }},
		{"merge", []string{"REQ-MERGED", "REQ-KEEP"}, func() []ds.FactReference {
			return []ds.FactReference{ref(previous, "REQ-FAST"), ref(previous, "REQ-DURABLE")}
		}},
		{"retire", []string{"REQ-KEEP"}, func() []ds.FactReference { return []ds.FactReference{ref(previous, "REQ-MERGED")} }},
		{"restore", []string{"REQ-KEEP", "REQ-ORDER"}, func() []ds.FactReference { return []ds.FactReference{ref(initial, "REQ-ORDER")} }},
	}
	revisions := []ds.Revision{initial}
	for _, stage := range stages {
		_, shared := designfixture.Capture()
		c.Generation++
		c.ContextHash = "context-" + stage.action
		c.ScopeHash = "scope-" + stage.action
		quote := "Approve " + stage.action + " of the ordering requirements; preserve authenticated behavior, recovery and the remaining current scope."
		c.Messages = append(c.Messages, ds.Message{Role: "user", Text: quote})
		evidence := ds.Source{Message: len(c.Messages) - 1, Quote: quote}
		shared.Facts = shared.Facts[1:]
		for _, id := range stage.ids {
			f := original
			f.Hash = ""
			f.ID = id
			f.Name = id
			f.Source = evidence
			shared.Facts = append(shared.Facts, f)
		}
		relations := shared.Relations[:0]
		for _, rel := range shared.Relations {
			if rel.From != "REQ-ORDER" && rel.To != "REQ-ORDER" {
				relations = append(relations, rel)
			}
		}
		shared.Relations = relations
		for _, id := range stage.ids {
			shared.Relations = append(shared.Relations, ds.Relation{ID: "REALIZES-" + id, Kind: "realizes", From: "MOD-ORDER", To: id}, ds.Relation{ID: "VERIFIES-" + id, Kind: "verifies", From: "TEST-ORDER", To: id})
		}
		shared.Scope.Requirements = stage.ids
		shared.Scope.Source = evidence
		for i := range shared.Obligations {
			if shared.Obligations[i].ID == "OBL-SCOPE" {
				shared.Obligations[i].Facts = []string{stage.ids[0]}
				shared.Obligations[i].Section = "contract-" + strings.ToLower(stage.ids[0])
			}
		}
		targets := []string{}
		switch stage.action {
		case "split":
			targets = stage.ids[:2]
		case "merge":
			targets = stage.ids[:1]
		case "restore":
			targets = []string{"REQ-ORDER"}
		}
		shared.Changes = []ds.FactChange{{Action: stage.action, From: stage.from(), To: targets, Source: evidence, Reason: quote, Coverage: "The complete former ordering behavior is preserved in current normative requirements or explicitly retired by this user decision."}}
		head, e := s.Store.Head(p.SetID)
		if e != nil {
			t.Fatal(e)
		}
		next, e := s.Plan(context.Background(), c.SessionID, PlanRequest{OperationID: "lifecycle-plan-" + stage.action, SetID: p.SetID, ExpectedGeneration: &head.Generation, CaptureGeneration: &c.Generation, ProfileID: p.Profile.ID, ProfileVersion: p.Profile.Version, ProfileHash: p.Profile.Hash, Shared: &shared})
		if e != nil {
			t.Fatal(stage.action, e)
		}
		if len(next.Shared.Questions) > 0 {
			t.Fatal(stage.action, next.Shared.Questions)
		}
		start(t, s, next, "lifecycle-run-"+stage.action)
		run := wait(t, s, "lifecycle-run-"+stage.action)
		if run.State != "committed" {
			t.Fatal(stage.action, run.State, run.Error)
		}
		previous = *run.Candidate
		revisions = append(revisions, previous)
		if len(previous.Plan.Shared.Scope.Requirements) != len(stage.ids) {
			t.Fatal("scope denominator incorrect")
		}
	}
	for _, rev := range revisions {
		saved, e := s.Store.Read(p.SetID, rev.ID)
		if e != nil || saved.Hash != rev.Hash {
			t.Fatal("historical bytes changed", e)
		}
	}
	last, e := s.Store.Run("lifecycle-run-restore")
	if e != nil {
		t.Fatal(e)
	}
	head, e := approve(t, s, last, "lifecycle-final")
	if e != nil {
		t.Fatal(e)
	}
	backup, e := s.Store.Export(p.SetID, documentsetstore.ExportRequest{OperationID: "lifecycle-backup", ExpectedGeneration: &head.Generation, RevisionID: head.Finalized, Kind: "backup"})
	if e != nil {
		t.Fatal(e)
	}
	restored := documentsetstore.New(t.TempDir())
	state, e := restored.Import(backup, "lifecycle-import")
	if e != nil {
		t.Fatal(e)
	}
	for _, rev := range revisions {
		saved, e := restored.Read(state.SetID, rev.ID)
		if e != nil || saved.Hash != rev.Hash || ds.Ready(saved) != nil {
			t.Fatal("history did not survive backup", rev.ID, e)
		}
	}
	raw, e := restored.Export(state.SetID, documentsetstore.ExportRequest{OperationID: "lifecycle-delivery", ExpectedGeneration: &head.Generation, RevisionID: head.Finalized, Kind: "delivery", PublicAuthorized: true})
	if e != nil {
		t.Fatal(e)
	}
	if _, e = documentsetstore.VerifyPublicPackage(raw, previous.Report.PublicPayloadHash); e != nil {
		t.Fatal(e)
	}
}

func TestEngineeringOpenIssuesCannotBeDeletedOrClosedWithOldQuote(t *testing.T) {
	c, shared := designfixture.Capture()
	shared.Questions = []ds.Question{{ID: "Q-RECOVERY", Kind: "conflict", Message: "Select the recovery behavior", Blocking: true, Facts: []string{"REQ-ORDER"}}}
	p, e := ds.BuildPlan("engineering", ds.Builtins()[2], c, shared, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	for _, which := range []string{"delete", "old-quote", "downgrade", "new-answer"} {
		t.Run(which, func(t *testing.T) {
			next := shared
			next.Questions = append([]ds.Question{}, shared.Questions...)
			switch which {
			case "delete":
				next.Questions = nil
			case "old-quote":
				next.Questions[0].Resolution = "resolved"
				source := ds.Source{Message: 0, Quote: "Build recoverable orders"}
				next.Questions[0].Source = &source
			case "downgrade":
				next.Questions[0].Blocking = false
			case "new-answer":
				c.Messages = append(c.Messages, ds.Message{Role: "user", Text: "Use durable restart recovery."})
				next.Questions[0].Resolution = "Use durable restart recovery"
				source := ds.Source{Message: 1, Quote: c.Messages[1].Text}
				next.Questions[0].Source = &source
			}
			guardEngineeringScope(&next, p, c)
			if which == "new-answer" {
				if next.Questions[0].Resolution == "" {
					t.Fatal("new user answer rejected")
				}
			} else if len(next.Questions) == 0 || next.Questions[0].Resolution != "" || !next.Questions[0].Blocking {
				t.Fatal("unresolved issue lost", which)
			}
		})
	}
}
