package documentvalidation

import (
	"strings"
	"testing"

	"thoughtflow/assets/documentformats"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
)

func fixture(t *testing.T, raw string) documentstore.RevisionData {
	t.Helper()
	p, e := documentprofile.Parse([]byte(raw), documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	params := map[string]string{}
	for _, input := range p.Inputs {
		params[input.Key] = "confirmed input"
	}
	params["subtype"] = "opinion"
	sections := map[string]models.DocumentSection{}
	for _, s := range p.Sections {
		sections[s.Key] = models.DocumentSection{Content: "G1 D1 T1 Q1 F1 C1: failure and recovery, a limitation and a counterexample. Supported by [S1]."}
	}
	rendered := documentprofile.Render(p, models.DocumentDraft{Title: "Delivery fixture", Sections: sections, References: []models.DocumentReference{{ID: "S1", SourceLink: "https://example.org/evidence"}}}, params)
	d := documentstore.RevisionData{Body: rendered.Content, Profile: p.Snapshot, Contract: models.DocumentContract{SchemaVersion: 1, Title: "Delivery fixture", Purpose: "explicit purpose", Audience: "engineers", Language: "en-US", ProfileRef: p.Ref, Parameters: params, ReviewPolicy: models.DocumentReviewPolicy{Version: ReviewPolicyVersion, AllowManual: true, SemanticRequired: true}}, Sources: models.SourceManifest{SchemaVersion: 1, Sources: []models.DocumentSource{{ID: "S1", Type: "external", Locator: "https://example.org/evidence", Hash: "sha256:source", Version: "2026-10-21", VerificationStatus: "verified"}}}}
	d.Revision = models.DocumentRevision{DocumentID: "doc", RevisionID: "r-1", BodyHash: documentstore.Hash([]byte(d.Body)), ContractHash: documentstore.HashJSON(d.Contract), ProfileHash: p.Ref.ContentHash, SourceManifestHash: documentstore.HashJSON(d.Sources)}
	return d
}
func codes(r models.ValidationReport) string {
	out := ""
	for _, i := range r.Issues {
		out += i.Code + " " + i.Section + "\n"
	}
	return out
}
func TestThreeFamiliesUseActualBody(t *testing.T) {
	for _, raw := range []string{documentformats.DesignDocV2, documentformats.ResearchReportV2, documentformats.BlogPostV2} {
		d := fixture(t, raw)
		r := Validate(d)
		if r.Status != "needs_review" {
			t.Fatalf("%s: %+v", d.Contract.ProfileRef.Family, r)
		}
		if r.ValidationKey != Key(d) || !strings.Contains(codes(r), "manual_review") {
			t.Fatal("missing frozen identity/manual scope")
		}
	}
}
func TestBodyDefectsCannotBeOverridden(t *testing.T) {
	cases := []struct {
		name   string
		change func(*documentstore.RevisionData)
		code   string
	}{
		{"missing", func(d *documentstore.RevisionData) { d.Body = strings.Replace(d.Body, "## 目标", "## moved", 1) }, "section_missing"},
		{"placeholder", func(d *documentstore.RevisionData) { d.Body = strings.Replace(d.Body, "G1 D1 T1", "待补充", 1) }, "placeholder"},
		{"unknown_reference", func(d *documentstore.RevisionData) { d.Body = strings.Replace(d.Body, "[S1]", "[S9]", 1) }, "reference_unknown"},
		{"duplicate_reference", func(d *documentstore.RevisionData) { d.Body += "\n- [S1] Duplicate: https://example.org/other\n" }, "reference_duplicate"},
		{"empty_locator", func(d *documentstore.RevisionData) {
			d.Body = strings.ReplaceAll(d.Body, "https://example.org/evidence", "")
			d.Sources.Sources = nil
		}, "reference_locator"},
		{"no_test", func(d *documentstore.RevisionData) { d.Body = strings.ReplaceAll(d.Body, "T1", "Z1") }, "goal_without_test"},
		{"missing_input", func(d *documentstore.RevisionData) { delete(d.Contract.Parameters, "problem") }, "input_required"},
		{"internal_link", func(d *documentstore.RevisionData) { d.Body += "\n[missing](#absent-heading)\n" }, "link_target"},
		{"unclosed_fence", func(d *documentstore.RevisionData) { d.Body += "\n```go\nprivate code\n" }, "fence_unclosed"},
		{"credential_link", func(d *documentstore.RevisionData) { d.Body += "\n[unsafe](https://user:secret@example.org)\n" }, "unsafe_link"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := fixture(t, documentformats.DesignDocV2)
			tc.change(&d)
			r := Validate(d)
			if r.Status != "failed" || !strings.Contains(codes(r), tc.code) {
				t.Fatalf("expected %s: %+v", tc.code, r)
			}
			for _, i := range r.Issues {
				if i.Code == tc.code && (i.HumanConfirmation || i.Severity != "error") {
					t.Fatal("hard error can be ignored")
				}
			}
		})
	}
}
func TestASTIgnoresCodeAndAllowsScopedHeadings(t *testing.T) {
	d := fixture(t, documentformats.DesignDocV2)
	d.Body = strings.Replace(d.Body, "## 方案设计\n", "## 方案设计\n\n```md\n# Fake H1\n## Original\n[S99]\nTODO\n```\n\n### Details\nA detail\n", 1)
	d.Body = strings.Replace(d.Body, "## 测试方案\n", "## 测试方案\n\n### Details\nAnother detail\n", 1)
	r := Validate(d)
	if r.Status != "needs_review" {
		t.Fatalf("code became structure or scoped heading rejected: %+v", r)
	}
}
func TestTutorialAndOpinionHaveDifferentRules(t *testing.T) {
	d := fixture(t, documentformats.BlogPostV2)
	if r := Validate(d); strings.Contains(codes(r), "tutorial_completeness") {
		t.Fatal(r)
	}
	d.Contract.Parameters["subtype"] = "tutorial"
	r := Validate(d)
	if r.Status != "failed" || !strings.Contains(codes(r), "tutorial_completeness") {
		t.Fatal(r)
	}
	d.Body += "\n环境 environment 输入 input 步骤 step 预期 expected 验证 validation 失败 failure 未运行验证\n"
	if r = Validate(d); r.Status != "needs_review" {
		t.Fatal(r)
	}
}
func TestIdentityIncludesEveryFrozenInput(t *testing.T) {
	d := fixture(t, documentformats.DesignDocV2)
	key := Key(d)
	d.Revision.SourceManifestHash = "new source"
	if Key(d) == key {
		t.Fatal("sources excluded")
	}
	d = fixture(t, documentformats.DesignDocV2)
	d.Contract.ReviewPolicy.Version = "new-policy"
	if Key(d) == key {
		t.Fatal("policy excluded")
	}
}
func TestUnknownProfileSchemaAndMandatoryRuleRejected(t *testing.T) {
	for _, raw := range []string{strings.Replace(documentformats.DesignDocV2, "schema_version: 2", "schema_version: 9", 1), strings.Replace(documentformats.DesignDocV2, "design_traceability", "unknown_mandatory", 1)} {
		if _, e := documentprofile.Parse([]byte(raw), documentprofile.Limits{}); e == nil {
			t.Fatal("unsupported rule silently ignored")
		}
	}
}
func TestAssumptionsAndHardBreaksSurviveRendering(t *testing.T) {
	p, e := documentprofile.Parse([]byte(documentformats.BlogPostV2), documentprofile.Limits{})
	if e != nil {
		t.Fatal(e)
	}
	content := "    indented  \nline  \n\n\nnext"
	sections := map[string]models.DocumentSection{}
	for _, s := range p.Sections {
		sections[s.Key] = models.DocumentSection{Content: content}
	}
	out := documentprofile.Render(p, models.DocumentDraft{Title: "title", Sections: sections, Assumptions: []string{"hypothesis, unverified"}}, nil)
	if !strings.Contains(out.Content, content) || !strings.Contains(out.Content, "hypothesis, unverified") {
		t.Fatalf("render lost body bytes/assumptions: %q", out.Content)
	}
}

func TestParserTracksEmptyAndNestedFenceClosure(t *testing.T) {
	for _, code := range []string{"```go\n", "```\n"} {
		d := fixture(t, documentformats.DesignDocV2)
		d.Body += "\n" + code
		if r := Validate(d); !strings.Contains(codes(r), "fence_unclosed") {
			t.Fatalf("empty unclosed fence accepted: %+v", r)
		}
	}
	for _, code := range []string{"```\n```\n", "> ````go\n> # In code [S999]\n> ````\n"} {
		d := fixture(t, documentformats.DesignDocV2)
		d.Body += "\n" + code
		if r := Validate(d); strings.Contains(codes(r), "fence_unclosed") {
			t.Fatalf("closed fence rejected: %+v", r)
		}
	}
}
func TestConstraintConflictsAndDiagnosticRedaction(t *testing.T) {
	d := fixture(t, documentformats.DesignDocV2)
	d.Contract.Constraints = map[string]string{"timeout": "5s"}
	d.Body += "\ntimeout: 30s\n"
	if r := Validate(d); !strings.Contains(codes(r), "constraint_conflict") {
		t.Fatal(r)
	}
	value := Redact("https://user:secret@example.org/path?token=SECRET&purpose=research API_KEY=SECRET")
	if strings.Contains(value, "secret") || strings.Contains(value, "SECRET") {
		t.Fatal(value)
	}
}
