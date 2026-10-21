package documentvalidation

import (
	"encoding/json"
	"strings"
	"testing"

	"thoughtflow/assets/documentformats"
)

func TestDeliveredReferenceLocatorsAreCheckedAgainstFrozenManifest(t *testing.T) {
	for _, tc := range []struct{ name, definition, code string }{
		{"different_naked_url", "https://other.example/evidence", "reference_mismatch"},
		{"credential_naked_url", "https://user:SECRET@example.org/evidence", "reference_locator"},
		{"private_query", "https://example.org/evidence?token=SECRET", "reference_locator"},
		{"different_link_target", "[source](https://other.example/evidence)", "reference_mismatch"},
		{"different_autolink", "<https://other.example/evidence>", "reference_mismatch"},
		{"empty_definition", "no locator available", "reference_locator"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixture(t, documentformats.DesignDocV2)
			const old = "- [S1] https://example.org/evidence: https://example.org/evidence"
			if !strings.Contains(d.Body, old) {
				t.Fatal("reference fixture changed")
			}
			d.Body = strings.Replace(d.Body, old, "- [S1] Source: "+tc.definition, 1)
			r := Validate(d)
			if r.Status != "failed" || !strings.Contains(codes(r), tc.code) {
				t.Fatalf("delivered URL bypassed frozen source: %+v", r)
			}
			raw, e := json.Marshal(r)
			if e != nil || strings.Contains(string(raw), "SECRET") {
				t.Fatalf("credential leaked to diagnostics: %s %v", raw, e)
			}
		})
	}
}

func TestCitationAtStartOfProseIsNotAReferenceDefinition(t *testing.T) {
	for _, prose := range []string{"[S1] 描述了当前实现。", "- [S1] 描述了当前实现。", "[S1] describes: the current implementation."} {
		d := fixture(t, documentformats.DesignDocV2)
		d.Body = strings.Replace(d.Body, "## 方案设计\n", "## 方案设计\n\n"+prose+"\n", 1)
		r := Validate(d)
		if r.Status != "needs_review" || strings.Contains(codes(r), "reference_duplicate") {
			t.Fatalf("citation registered as definition: %+v", r)
		}
	}
}
