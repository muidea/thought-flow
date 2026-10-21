package documentvalidation

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"thoughtflow/assets/documentformats"
	"thoughtflow/internal/pkg/models"
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
	for _, prose := range []string{
		"[S1] 描述了当前实现。",
		"- [S1] 描述了当前实现。",
		"[S1] describes: the current implementation.",
		"- [S1] describes: the current implementation.",
		"- [S1] describes https://example.org/evidence: the current implementation.",
	} {
		d := fixture(t, documentformats.DesignDocV2)
		d.Body = strings.Replace(d.Body, "## 方案设计\n", "## 方案设计\n\n"+prose+"\n", 1)
		r := Validate(d)
		if r.Status != "needs_review" || strings.Contains(codes(r), "reference_duplicate") {
			t.Fatalf("citation registered as definition: %+v", r)
		}
	}
}

func TestRenderedReferencesPreserveBalancedURLParentheses(t *testing.T) {
	for _, locator := range []string{
		"https://en.wikipedia.org/wiki/Go_(programming_language)",
		"https://example.org/a_(b_(c))",
		"https://example.org/a_(b)/c_(d)",
	} {
		for _, title := range []string{"", "Source title"} {
			d := fixtureWithReference(t, documentformats.DesignDocV2, models.DocumentReference{ID: "S1", Title: title, SourceLink: locator})
			body := []byte(d.Body)
			tree := bodyParser().Parse(text.NewReader(body))
			found := false
			ast.Walk(tree, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
				if !enter || (n.Kind() != ast.KindParagraph && n.Kind() != ast.KindTextBlock) {
					return ast.WalkContinue, nil
				}
				value := plain(n, body)
				m := referenceDefinition.FindStringSubmatch(value)
				if len(m) == 0 {
					return ast.WalkContinue, nil
				}
				found = true
				locators := referenceLocators(n, m[2], body)
				if len(locators) != 1 || locators[0] != locator {
					t.Errorf("rendered reference %q extracted %v, want %s", value, locators, locator)
				}
				return ast.WalkSkipChildren, nil
			})
			if !found {
				t.Fatal("renderer did not emit a reference")
			}
			if r := Validate(d); r.Status != "needs_review" {
				t.Fatalf("rendered reference mismatches frozen source: %+v", r)
			}
		}
	}
}

func TestReferenceURLPunctuationAndLinkSyntax(t *testing.T) {
	const locator = "https://en.wikipedia.org/wiki/Go_(programming_language)"
	for _, tc := range []struct{ name, target string }{
		{"naked", locator},
		{"sentence", locator + "."},
		{"wrapped", "(" + locator + ")."},
		{"brackets", "[" + locator + "]."},
		{"nested_wrappers", "（(" + locator + ").）。"},
		{"markdown_link", "[source](" + locator + ")"},
		{"autolink", "<" + locator + ">"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			d := fixtureWithReference(t, documentformats.DesignDocV2, models.DocumentReference{ID: "S1", Title: "Source", SourceLink: locator})
			d.Body = strings.Replace(d.Body, "- [S1] Source: "+locator, "- [S1] Source: "+tc.target, 1)
			if r := Validate(d); r.Status != "needs_review" {
				t.Fatalf("valid delivered reference rejected: %+v", r)
			}
		})
	}
}
