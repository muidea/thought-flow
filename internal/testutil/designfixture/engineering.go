// Package designfixture provides deterministic engineering contracts for regression tests.
package designfixture

import (
	"strings"
	"testing"
	"thoughtflow/internal/pkg/documentprofile"
	. "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
)

// EngineeringFixture is intentionally deterministic and source bounded. It
// exercises contracts and gates; it makes no claim about real model quality.
func Capture() (CaptureSnapshot, SharedDesign) {
	yes := true
	source := Source{Message: 0, Quote: "Build recoverable orders with an API, persistence, verification and a runnable deliverable."}
	c := CaptureSnapshot{SessionID: "engineering-capture", Generation: 1, Messages: []Message{{Role: "user", Text: source.Quote + " PRIVATE-CAPTURE-CANARY"}}, ContextHash: "context", ScopeHash: "scope", Sources: models.SourceManifest{SchemaVersion: 1}}
	acceptance := &AcceptanceContract{Preconditions: []string{"An empty database"}, Stimulus: []string{"Submit one order, interrupt after commit, restart"}, Observations: []string{"Exactly one order remains; retry returns the same identity"}, PassRule: "All observations hold", Method: "Temporary service restart and HTTP assertions", Evidence: []string{"Request log and persisted row count"}, Negative: []string{"Unauthorized request rejected"}, Boundary: []string{"Empty order rejected"}, Recovery: []string{"Restart preserves committed order"}}
	shared := SharedDesign{Scope: &DesignScope{ID: "SCOPE-ORDER", Requirements: []string{"REQ-ORDER"}, GlobalConstraints: []string{"RULE-RECOVERY", "RULE-QUALITY"}, Source: source}, Inputs: map[string]string{"goal": "Recoverable orders", "scope": "Order creation and recovery", "constraints": "Local durable storage"}, Conditions: map[string]Condition{"has_interfaces": {Value: &yes, Status: "confirmed", Source: source, Reason: "API explicitly requested"}, "has_persistence": {Value: &yes, Status: "confirmed", Source: source, Reason: "Persistence explicitly requested"}}, Facts: []Fact{
		{ID: "REQ-ORDER", Kind: "requirement", Owner: "architecture", Name: "Recoverable orders", Content: "Create one order exactly once and preserve it on restart."},
		{ID: "DEC-STORE", Kind: "decision", Owner: "architecture", Name: "Durable commit", Content: "Commit atomically before acknowledging an order."},
		{ID: "MOD-ORDER", Kind: "module", Owner: "module:MOD-ORDER", Name: "Orders", Content: "Authenticate, validate, atomically commit and acknowledge orders."},
		{ID: "API-ORDER", Kind: "interface", Owner: "interfaces", Name: "Create order", Content: "POST /orders is authenticated and idempotent; unauthorized requests fail with 403.", Fields: []Field{{ID: "id", Type: "string", Classification: "persisted", EntityID: "ENTITY-ORDER", EntityField: "id", Permission: "authenticated", Errors: []string{"403", "409"}}}},
		{ID: "ENTITY-ORDER", Kind: "entity", Owner: "storage", Name: "Order storage", Content: "Orders use immutable IDs with atomic durable commits.", Fields: []Field{{ID: "id", Type: "string", Classification: "persisted"}}},
		{ID: "RULE-RECOVERY", Kind: "constraint", Owner: "architecture", Name: "Restart recovery", Content: "Acknowledged orders survive interruption.", StateRule: &StateContract{Initial: "pending", Trigger: "commit", Guard: "authenticated valid input", Target: "committed", Failure: "rollback and reject", Persistence: "persist before acknowledgment"}},
		{ID: "RULE-QUALITY", Kind: "constraint", Owner: "verification", Name: "Security boundary", Content: "Unauthorized users cannot create orders.", Quality: &QualityContract{Kind: "security", Principal: "anonymous", Resource: "orders", Permission: "create", Forbidden: "unauthorized creation", Outcome: "403, no new row"}},
		{ID: "TEST-ORDER", Kind: "verification", Owner: "verification", Name: "Order evidence", Content: "Exercise normal, unauthorized, empty and restart paths.", Acceptance: acceptance},
		{ID: "ART-ORDER", Kind: "artifact_spec", Owner: "delivery", Name: "Runnable service", Content: "Deliver a runnable binary with build instructions and verification.", Artifact: &ArtifactContract{Format: "binary and source", Files: []string{"service", "README.md"}, EntryPoint: "service", Build: "go build ./cmd/service", Run: "./service", Environment: "Linux amd64", Configuration: "local configuration", DataPolicy: "retain committed orders", Usage: "POST /orders", Security: "require authenticated requests", License: "project license", Verification: []string{"run order recovery acceptance"}}},
	}, Relations: []Relation{{ID: "R-REALIZE", Kind: "realizes", From: "MOD-ORDER", To: "REQ-ORDER"}, {ID: "R-API", Kind: "uses_contract", From: "MOD-ORDER", To: "API-ORDER"}, {ID: "R-DATA", Kind: "uses_contract", From: "MOD-ORDER", To: "ENTITY-ORDER"}, {ID: "R-RECOVERY", Kind: "constrains", From: "RULE-RECOVERY", To: "MOD-ORDER"}, {ID: "R-SECURITY", Kind: "constrains", From: "RULE-QUALITY", To: "API-ORDER"}}}
	for _, id := range []string{"REQ-ORDER", "MOD-ORDER", "RULE-RECOVERY", "RULE-QUALITY", "ART-ORDER", "API-ORDER"} {
		shared.Relations = append(shared.Relations, Relation{ID: "VERIFY-" + id, Kind: "verifies", From: "TEST-ORDER", To: id})
	}
	for i := range shared.Facts {
		f := &shared.Facts[i]
		f.Status = "confirmed"
		f.Modality = "MUST"
		f.Source = source
		f.Provenance = []Provenance{{ID: "USER-SCOPE", Kind: "user_requirement", PublicRef: "approved-scope"}}
		f.ScopeRefs = []string{"SCOPE-ORDER"}
	}
	for _, o := range Builtins()[2].Obligations {
		for _, f := range shared.Facts {
			if f.Kind == o.Kind && (f.Owner == o.Role || strings.HasPrefix(f.Owner, o.Role+":")) {
				shared.Obligations = append(shared.Obligations, ObligationResolution{ID: o.ID, State: "DEFINED", Facts: []string{f.ID}, Member: f.Owner, Section: "contract-" + strings.ToLower(f.ID), Source: source})
				break
			}
		}
	}
	return c, shared
}
func NewRevision(t testing.TB) Revision {
	t.Helper()
	c, s := Capture()
	p, e := BuildPlan("engineering", Builtins()[2], c, s, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	p.ID = "plan"
	p.Hash = PlanHash(p)
	if len(p.Shared.Questions) > 0 {
		t.Fatalf("fixture has gaps: %+v", p.Shared.Questions)
	}
	r := Revision{SchemaVersion: 1, ID: "set-r-one", Plan: p}
	for _, m := range p.Members {
		profile, e := documentprofile.Parse([]byte(m.ProfileSnapshot), documentprofile.Limits{})
		if e != nil {
			t.Fatal(e)
		}
		draft := models.DocumentDraft{Title: m.Title, Sections: map[string]models.DocumentSection{}}
		for _, sec := range profile.Sections {
			draft.Sections[sec.Key] = models.DocumentSection{Content: "Order requests require authentication, validation and atomic persistence. Rejected requests create no row. Restart recovery preserves every acknowledged order."}
		}
		part := draft.Sections["design"]
		part.Content += CommonBlock(p, m)
		draft.Sections["design"] = part
		render := documentprofile.Render(profile, draft, s.Inputs)
		body := documentstore.Normalize(render.Content)
		a := Artifact{Key: m.Key, DocumentID: m.DocumentID, RevisionID: "r-" + strings.ReplaceAll(m.Key, ":", "-"), Body: body, BodyHash: documentstore.Hash([]byte(body)), ProfileSnapshot: m.ProfileSnapshot, Contract: Contract(p, m), Sources: models.SourceManifest{SchemaVersion: 1}, Dependencies: map[string]string{}}
		for _, key := range m.DependsOn {
			a.Dependencies[key] = "r-" + strings.ReplaceAll(key, ":", "-")
		}
		r.Members = append(r.Members, a)
	}
	r.Report = Validate(r)
	if r.Report.StructuralStatus != "passed" {
		t.Fatalf("invalid fixture: %+v", r.Report.Issues)
	}
	Rehash(&r)
	return r
}

func ReviewedRevision(t testing.TB) Revision {
	t.Helper()
	r := NewRevision(t)
	evidence := []ReviewEvidence{}
	for _, item := range r.Report.Checklist {
		e := ReviewEvidence{CheckID: item.ID, Verdict: "passed", Reason: "Checked against the frozen scope, behavior, failure and artifact contracts"}
		for _, key := range item.Members {
			e.Locations = append(e.Locations, Location{Member: key, Section: "scope", Evidence: "Order requests require authentication, validation and atomic persistence."})
		}
		evidence = append(evidence, e)
	}
	if e := ApplyReview(&r, ReviewResponse{Evidence: evidence}, "model_reviewed", "fixture-reviewer"); e != nil {
		t.Fatal(e)
	}
	if e := Ready(r); e != nil {
		t.Fatal("fixture not ready", e)
	}
	return r
}
