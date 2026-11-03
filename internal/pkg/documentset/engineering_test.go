package documentset_test

import (
	"encoding/json"
	"os"
	"strings"
	"testing"

	. "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/testutil/designfixture"
)

func TestEngineeringContractsAndPurposeIsolation(t *testing.T) {
	c, s := designfixture.Capture()
	p := Builtins()[2]
	for _, tc := range []string{"missing-artifact", "unknown-applicability", "unapproved-normative", "bad-acceptance", "invalid-relation", "unapproved-exclusion", "scope-dependency", "nonmeasurable-quality", "optional-verification", "optional-realization", "wrong-recovery-payload", "wrong-quality-payload"} {
		t.Run(tc, func(t *testing.T) {
			raw, _ := json.Marshal(s)
			var next SharedDesign
			json.Unmarshal(raw, &next)
			switch tc {
			case "optional-verification", "optional-realization":
				i := 7
				if tc == "optional-realization" {
					i = 2
				}
				next.Facts[i].NonNormative = true
				next.Facts[i].Modality = "MAY"
				next.Facts[i].Status = "proposed"
			case "wrong-recovery-payload":
				next.Facts[5].StateRule = nil
				next.Facts[5].Quality = next.Facts[6].Quality
			case "wrong-quality-payload":
				next.Facts[6].Quality = nil
				next.Facts[6].StateRule = next.Facts[5].StateRule
			case "nonmeasurable-quality":
				next.Facts[6].Quality = &QualityContract{Kind: "measurement", Metric: "latency", Environment: "Linux", Workload: "100 requests", Statistic: "p99", Window: "1 minute", Operator: "<=", Threshold: "fast", Unit: "ms", FailurePolicy: "reject"}
			case "missing-artifact":
				next.Facts[len(next.Facts)-1].Artifact = nil
			case "unknown-applicability":
				delete(next.Conditions, "has_persistence")
			case "unapproved-normative":
				next.Facts[0].Provenance[0].Kind = "model_proposal"
			case "bad-acceptance":
				next.Facts[7].Acceptance.Observations = nil
			case "invalid-relation":
				next.Relations[0].From = "TEST-ORDER"
			case "unapproved-exclusion":
				next.Scope.Excluded = []ScopeExclusion{{FactID: "REQ-ORDER", Reason: "skip", Source: Source{Message: 99, Quote: "fake"}}}
			case "scope-dependency":
				next.Scope.Excluded = []ScopeExclusion{{FactID: "REQ-ORDER", Reason: "explicitly excluded", Source: next.Scope.Source}}
				next.Relations = append(next.Relations, Relation{ID: "CROSS", Kind: "depends_on", From: "ART-ORDER", To: "REQ-ORDER"})
			}
			plan, e := BuildPlan("engineering", p, c, next, 0, 1)
			if e == nil && len(plan.Shared.Questions) == 0 && len(EngineeringIssues(plan)) == 0 {
				t.Fatal("invalid engineering contract accepted")
			}
		})
	}
	if len(EngineeringIssues(Plan{Profile: Builtins()[0]})) != 0 {
		t.Fatal("ordinary profile affected")
	}
}
func TestPublicIdentityAndSemanticMutation(t *testing.T) {
	r := designfixture.NewRevision(t)
	d, files, e := PublicPayload(r)
	if e != nil {
		t.Fatal(e)
	}
	raw, _ := PublicJSON(d)
	if strings.Contains(string(raw), "PRIVATE-CAPTURE-CANARY") || strings.Contains(string(raw), `"source"`) {
		t.Fatal("private envelope leaked")
	}
	if len(files) == 0 || r.Report.PublicPayloadHash != documentstore.Hash(raw) {
		t.Fatal("public identity not frozen before review")
	}
	f := r.Plan.Shared.Facts[7]
	before := FactMeaning(f)
	changes := []func(*Fact){func(f *Fact) { f.Modality = "MAY" }, func(f *Fact) { f.ScopeRefs = []string{"another"} }, func(f *Fact) { f.Acceptance.PassRule = "always" }, func(f *Fact) { f.Provenance[0].Kind = "reference" }, func(f *Fact) { f.NonNormative = true }, func(f *Fact) { f.Quality = &QualityContract{Kind: "security"} }, func(f *Fact) { f.StateRule = &StateContract{Target: "other"} }, func(f *Fact) { f.Artifact = &ArtifactContract{Run: "other"} }}
	for _, change := range changes {
		raw, _ := json.Marshal(f)
		var next Fact
		json.Unmarshal(raw, &next)
		change(&next)
		if FactMeaning(next) == before {
			t.Fatal("new normative field omitted from meaning")
		}
	}
	d.Members[0].Body += " changed"
	other, _ := PublicJSON(d)
	if documentstore.Hash(other) == r.Report.PublicPayloadHash {
		t.Fatal("body mutation kept identity")
	}
}
func TestCanonicalPublicJSONVectors(t *testing.T) {
	for _, tc := range []struct{ in, out string }{{`{"z":1,"a":"<>&"}`, `{"a":"<>&","z":1}`}, {"{\"\ue000\":2,\"😀\":1}", "{\"😀\":1,\"\ue000\":2}"}, {`{"n":1e2,"zero":-0}`, `{"n":100,"zero":0}`}, {`{"a":[],"b":null,"c":{}}`, `{"a":[],"b":null,"c":{}}`}} {
		got, e := CanonicalPublicJSON([]byte(tc.in))
		if e != nil || string(got) != tc.out {
			t.Fatalf("%s: %s %v", tc.in, got, e)
		}
	}
	for _, raw := range []string{`{"x":1,"x":2}`, `{"x":9007199254740992}`, `{"x":0.5}`, `{} {}`, `{"s":"\ud800"}`, `{"n":1e9999999}`} {
		if _, e := CanonicalPublicJSON([]byte(raw)); e == nil {
			t.Fatal("invalid public number/duplicate accepted", raw)
		}
	}
}

func TestCanonicalCrossImplementationVectors(t *testing.T) {
	raw, e := os.ReadFile("../../../assets/publicdesign/canonical-vectors.json")
	if e != nil {
		t.Fatal(e)
	}
	var vectors []struct {
		Name      string `json:"name"`
		Input     string `json:"input"`
		Canonical string `json:"canonical"`
	}
	if e = json.Unmarshal(raw, &vectors); e != nil {
		t.Fatal(e)
	}
	for _, v := range vectors {
		t.Run(v.Name, func(t *testing.T) {
			canonical, e := CanonicalPublicJSON([]byte(v.Input))
			if e != nil || string(canonical) != v.Canonical {
				t.Fatalf("%s %v", canonical, e)
			}
		})
	}
}

func TestEngineeringEveryModuleNeedsObligationAndFreshImpact(t *testing.T) {
	c, s := designfixture.Capture()
	second := s.Facts[2]
	second.ID = "MOD-BILLING"
	second.Name = "Billing"
	second.Owner = "module:MOD-BILLING"
	s.Facts = append(s.Facts, second)
	s.Relations = append(s.Relations, Relation{ID: "VERIFY-BILLING", Kind: "verifies", From: "TEST-ORDER", To: second.ID})
	missing, e := BuildPlan("engineering", Builtins()[2], c, s, 0, 1)
	if e != nil {
		t.Fatal(e)
	}
	if len(missing.Shared.Questions) == 0 {
		t.Fatal("second module silently omitted its content obligations")
	}
	s.Obligations = append(s.Obligations, ObligationResolution{ID: "OBL-BEHAVIOR", State: "DEFINED", Facts: []string{second.ID}, Member: second.Owner, Section: "contract-mod-billing", Source: s.Facts[0].Source})
	complete, e := BuildPlan("engineering", Builtins()[2], c, s, 0, 1)
	if e != nil || len(complete.Shared.Questions) > 0 {
		t.Fatal(complete.Shared.Questions, e)
	}
	old := designfixture.NewRevision(t).Plan
	impact := AnalyzeImpact(old, complete)
	found := false
	for _, key := range impact.Regenerate {
		if key == second.Owner {
			found = true
		}
	}
	if !found || !impact.ReviewRequired {
		t.Fatal("scope expansion did not invalidate downstream", impact)
	}
}
