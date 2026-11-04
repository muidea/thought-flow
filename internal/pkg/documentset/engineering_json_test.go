package documentset_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	. "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/testutil/designfixture"
)

func TestArtifactEmptyApplicabilityPreservesContractAndValidation(t *testing.T) {
	capture, shared := designfixture.Capture()
	var original *ArtifactContract
	for _, fact := range shared.Facts {
		if fact.Artifact != nil {
			original = fact.Artifact
			break
		}
	}
	if original == nil {
		t.Fatal("fixture has no artifact contract")
	}
	raw, err := json.Marshal(original)
	if err != nil {
		t.Fatal(err)
	}
	for _, suffix := range []string{`}`, `,"not_applicable":{}}`, ",\"not_applicable\": { \n\t }}"} {
		var decoded ArtifactContract
		if err := StrictJSON(append(append([]byte{}, raw[:len(raw)-1]...), suffix...), &decoded); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(*original, decoded) {
			t.Fatal("compatibility changed substantive artifact content")
		}
		encoded, _ := json.Marshal(decoded)
		if string(encoded) != string(raw) {
			t.Fatal("placeholder entered canonical contract identity")
		}
		for i := range shared.Facts {
			if shared.Facts[i].Artifact != nil {
				shared.Facts[i].Artifact = &decoded
			}
		}
		plan, err := BuildPlan("engineering", Builtins()[2], capture, shared, 0, 1)
		if err != nil || len(EngineeringIssues(plan)) != 0 {
			t.Fatalf("valid artifact blocked: %v", err)
		}
		for i := range plan.Shared.Facts {
			if plan.Shared.Facts[i].Artifact != nil {
				plan.Shared.Facts[i].Artifact.Build = ""
			}
		}
		blocked := false
		for _, issue := range EngineeringIssues(plan) {
			blocked = blocked || issue.Code == "design_artifact"
		}
		if !blocked {
			t.Fatal("empty placeholder exempted missing build contract")
		}
	}
}

func TestArtifactApplicabilityCannotDiscardReasonsOrUnknownFields(t *testing.T) {
	for _, raw := range []string{
		`{"not_applicable":{"build":"PRIVATE-REASON"}}`,
		`{"not_applicable":{"build":"PRIVATE-REASON"},"not_applicable":{}}`,
		`{"not_applicable":{},"not_applicable":{"build":"PRIVATE-REASON"}}`,
		`{"not_applicable":null}`, `{"not_applicable":[]}`, `{"not_applicable":""}`,
		`{"not_applicable":false}`, `{"not_applicable":0}`,
		`{"not_applicable":{},"unexpected":"PRIVATE-VALUE"}`,
	} {
		var artifact ArtifactContract
		err := StrictJSON([]byte(raw), &artifact)
		if err == nil || strings.Contains(err.Error(), "PRIVATE-") {
			t.Fatalf("substantive or invalid field silently accepted or exposed: %v", err)
		}
	}
}
