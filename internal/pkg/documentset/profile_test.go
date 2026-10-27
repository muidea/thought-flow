package documentset

import (
	"encoding/json"
	"errors"
	"testing"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
)

func TestProfileSafetyAndGenerationDAG(t *testing.T) {
	for _, p := range Builtins() {
		if e := ValidateProfile(p); e != nil {
			t.Fatal(e)
		}
	}
	for _, which := range []string{"condition-expression", "missing-required", "cycle", "unfrozen-profile", "member-budget", "unknown-rule"} {
		t.Run(which, func(t *testing.T) {
			p := Builtins()[0]
			switch which {
			case "condition-expression":
				p.Roles[0].When = "exec('shell')"
			case "missing-required":
				p.Roles[0].Required = nil
			case "cycle":
				p.Roles[0].DependsOn = []string{"verification"}
			case "unfrozen-profile":
				p.Roles[0].ProfileRef.ContentHash = ""
			case "member-budget":
				p.Limits.MaxMembers = 1000
			case "unknown-rule":
				p.Checks = append(p.Checks, "run-script")
			}
			p.Hash = ProfileHash(p)
			if e := ValidateProfile(p); e == nil {
				t.Fatal("unsafe profile accepted")
			}
		})
	}
}
func TestUnknownConditionsAndFalseOmissions(t *testing.T) {
	p := Builtins()[0]
	c := CaptureSnapshot{Messages: []Message{{Role: "user", Text: "Architecture only"}}}
	no := false
	shared := SharedDesign{Facts: []Fact{{ID: "REQ-1", Kind: "requirement", Name: "Scope", Content: "Architecture only", Status: "confirmed", Source: Source{Message: 0, Quote: "Architecture only"}, Owner: "architecture"}}, Conditions: map[string]Condition{"has_interfaces": {Value: &no, Status: "confirmed", Source: Source{Message: 0, Quote: "Architecture only"}, Reason: "Explicit exclusion"}}, Inputs: map[string]string{"goal": "Architecture", "scope": "Architecture", "constraints": "None"}}
	plan, e := BuildPlan("example", p, c, shared, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	if plan.Omissions["interfaces"] == "" {
		t.Fatal("false condition not recorded")
	}
	found := false
	for _, q := range plan.Shared.Questions {
		if q.ID == "condition-has_persistence" && q.Blocking {
			found = true
		}
	}
	if !found {
		t.Fatal("unknown treated as false")
	}
	shared.Facts[0].Source.Quote = "Invented confirmation"
	if _, e = BuildPlan("example", p, c, shared, 0, 1); !errors.Is(e, ErrInvalid) {
		t.Fatal("fabricated confirmation", e)
	}
}
func TestStableMemberIdentityAndFrozenProfileHash(t *testing.T) {
	p := Builtins()[1]
	c := CaptureSnapshot{}
	shared := SharedDesign{Inputs: map[string]string{"goal": "Research", "scope": "Evidence"}, Conditions: map[string]Condition{}}
	a, e := BuildPlan("research", p, c, shared, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(a.Members) != 3 {
		t.Fatal("non-software roles missing")
	}
	b, e := BuildPlan("research", p, c, shared, 1, 2)
	if e != nil {
		t.Fatal(e)
	}
	for i := range a.Members {
		if a.Members[i].DocumentID != b.Members[i].DocumentID {
			t.Fatal("identity drift")
		}
	}
	before := p.Hash
	p.Roles[0].ProfileSnapshot += "\nChanged role template.\n"
	p.Hash = ProfileHash(p)
	if p.Hash == before || ValidateProfile(p) == nil {
		t.Fatal("unresolved child change allowed")
	}
	_ = documentstore.HashJSON(a)
}

func TestPublishedSetProfileResolvesExactRoleSnapshot(t *testing.T) {
	registry, e := documentprofile.NewRegistry("", "", documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	single, e := registry.ResolveLatest("builtin.design-doc")
	if e != nil {
		t.Fatal(e)
	}
	p := Builtins()[1]
	p.ID = "custom.research-set"
	p.Roles[0].ProfileRef = single.Ref
	p.Roles[0].ProfileSnapshot = ""
	raw, e := json.Marshal(p)
	if e != nil {
		t.Fatal(e)
	}
	resolved, e := ParseProfile(raw, registry)
	if e != nil {
		t.Fatal(e)
	}
	if resolved.Roles[0].ProfileSnapshot != single.Snapshot || resolved.Roles[0].ProfileRef != single.Ref {
		t.Fatal("role reference not frozen")
	}
	p.Roles[0].ProfileRef.ContentHash = "sha256:wrong"
	raw, _ = json.Marshal(p)
	if _, e = ParseProfile(raw, registry); e == nil {
		t.Fatal("mismatched role snapshot accepted")
	}
}

func TestConditionalOmissionRequiresConfirmedUserEvidence(t *testing.T) {
	no := false
	for _, tc := range []struct {
		name, quote, status string
		omit                bool
	}{
		{"model-false", "", "confirmed", false},
		{"invented-exclusion", "No storage required", "confirmed", false},
		{"suggested-exclusion", "No persistence in this scope", "proposed", false},
		{"confirmed-exclusion", "No persistence in this scope", "confirmed", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := CaptureSnapshot{Messages: []Message{{Role: "user", Text: "Build order recovery. No persistence in this scope."}}}
			shared := SharedDesign{Conditions: map[string]Condition{"has_persistence": {Value: &no, Status: tc.status, Source: Source{Message: 0, Quote: tc.quote}, Reason: "Scope decision"}}, Inputs: map[string]string{"goal": "Order recovery", "scope": "Orders", "constraints": "Local"}}
			plan, e := BuildPlan("set-conditions", Builtins()[0], c, shared, 0, 1)
			if e != nil {
				t.Fatal(e)
			}
			if (plan.Omissions["storage"] != "") != tc.omit {
				t.Fatal("unsupported scope reduction", plan.Omissions)
			}
			blocked := false
			for _, q := range plan.Shared.Questions {
				if q.ID == "condition-has_persistence" && q.Blocking {
					blocked = true
				}
			}
			if blocked == tc.omit {
				t.Fatal("clarification gate incorrect", plan.Shared.Questions)
			}
		})
	}
	// Explicit user requirement survives an extractor that returns an unevidenced false.
	c := CaptureSnapshot{Messages: []Message{{Role: "user", Text: "We require persistent storage."}}}
	plan, e := BuildPlan("set-required", Builtins()[0], c, SharedDesign{Conditions: map[string]Condition{"has_persistence": {Value: &no, Status: "confirmed"}}, Inputs: map[string]string{"goal": "Recovery", "scope": "Orders", "constraints": "Local"}}, 0, 1)
	if e != nil || plan.Omissions["storage"] != "" || len(plan.Shared.Questions) == 0 {
		t.Fatal("required storage lost", plan, e)
	}
}
