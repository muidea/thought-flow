// Package documentvalidation validates the actual frozen delivery body, never
// sections left over from an earlier generation. It performs no network I/O.
package documentvalidation

import (
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

const Version = "final-body-ast-v2.5"
const ReviewPolicyVersion = "review-v1"

var citations = regexp.MustCompile(`\[(S[0-9]+)\]`)
var identifiers = regexp.MustCompile(`\b([GDTQFC][0-9]+)\b`)
var placeholders = regexp.MustCompile(`(?i)\{\{[^}]*\}\}|\b(TODO|TBD|FIXME)\b|待补充|待填写|待完善`)
var referenceDefinition = regexp.MustCompile(`^\s*\[(S[0-9]+)\]\s*(.*)$`)

type block struct {
	title, body string
	line        int
}

type reference struct {
	text     string
	locators []string
	line     int
}

func Key(d documentstore.RevisionData) string {
	r := d.Revision
	return documentstore.HashJSON([]string{r.BodyHash, r.ContractHash, r.ProfileHash, r.SourceManifestHash, r.ResourceBindingsHash, d.ResourceMappingHash, Version, d.Contract.ReviewPolicy.Version})
}
func Validate(d documentstore.RevisionData) models.ValidationReport {
	r := models.ValidationReport{ReportID: documentstore.ID("v-"), DocumentID: d.Revision.DocumentID, RevisionID: d.Revision.RevisionID, ValidationKey: Key(d), ValidatorVersion: Version, Status: "passed", CreatedAt: time.Now().UTC(), Checks: []string{"frozen_identity", "contract_inputs", "markdown_ast", "section_mapping", "references", "type_rules"}, Issues: []models.DocumentIssue{}, ReviewRuns: []models.DocumentReviewRun{{Kind: "structure", Status: "passed"}}}
	add := func(code, group, section, evidence, basis string, line int, hard bool) {
		severity := "warning"
		if hard {
			severity = "error"
		}
		r.Issues = append(r.Issues, models.DocumentIssue{ID: fmt.Sprintf("i%d", len(r.Issues)+1), Code: code, Severity: severity, Group: group, Section: section, Line: line, Evidence: evidence, Basis: basis, Suggestion: "Update the frozen inputs or the indicated body section, then validate the new revision.", HumanConfirmation: !hard})
	}
	allowedLocator := func(locator string) bool {
		if validLocator(locator) {
			return true
		}
		id, resource := objectlocation.ResourceID(locator)
		for _, b := range d.Resources.Bindings {
			if !b.Private && b.Hash != "" && ((resource && b.ResourceID == id) || (b.OriginalLocator != "" && b.OriginalLocator == locator)) {
				return true
			}
		}
		return false
	}
	profile, e := documentprofile.Parse([]byte(d.Profile), documentprofile.Limits{})
	if e != nil || profile.Ref != d.Contract.ProfileRef {
		add("profile_unavailable", "structure", "", "", "Exact profile snapshot is unavailable or does not match its identity", 0, true)
		return finish(r)
	}
	if d.Contract.SchemaVersion != 1 || d.Contract.ReviewPolicy.Version != ReviewPolicyVersion {
		add("contract_schema", "structure", "", "", "Unsupported contract or review policy version", 0, true)
	}
	if profile.SchemaVersion != 2 {
		add("mapping_unavailable", "structure", "", "", "This historical/custom profile needs an explicit v2 AST mapping before formal delivery", 0, true)
	}
	if d.Sources.SchemaVersion != 1 {
		add("source_schema", "evidence", "", "", "Unsupported source manifest schema", 0, true)
	}
	for _, input := range profile.Inputs {
		if input.Required && placeholders.MatchString(d.Contract.Parameters[input.Key]) {
			add("input_unconfirmed", "structure", input.Key, "", "Required input is an unconfirmed placeholder", 0, true)
		}
		if input.Required && strings.TrimSpace(d.Contract.Parameters[input.Key]) == "" {
			add("input_required", "structure", input.Key, "", "Confirmed input is required: "+input.Label, 0, true)
		}
	}
	if strings.TrimSpace(d.Contract.Purpose) == "" || strings.TrimSpace(d.Contract.Audience) == "" || strings.TrimSpace(d.Contract.Language) == "" {
		add("contract_required", "structure", "", "", "Purpose, audience and language must be explicit", 0, true)
	}
	body := []byte(d.Body)
	tree := bodyParser().Parse(text.NewReader(body))
	blocks := []block{}
	current := -1
	h1 := 0
	path := map[int]string{}
	headingSeen := map[string]bool{}
	anchors := map[string]bool{}
	links := []string{}
	// Inspect destinations independently of section traversal, including links
	// nested in headings. Code samples are not rendered as active links.
	ast.Walk(tree, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		if n.Kind() == ast.KindCodeSpan || n.Kind() == ast.KindCodeBlock || n.Kind() == ast.KindFencedCodeBlock {
			return ast.WalkSkipChildren, nil
		}
		switch link := n.(type) {
		case *ast.Link:
			links = append(links, MarkdownLocator(string(link.Destination)))
		case *ast.Image:
			links = append(links, MarkdownLocator(string(link.Destination)))
		case *ast.AutoLink:
			links = append(links, string(link.URL(body)))
		}
		return ast.WalkContinue, nil
	})
	visible := strings.Builder{}
	refs := map[string]reference{}
	used := map[string]bool{}
	ast.Walk(tree, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		if n.Kind() == ast.KindFencedCodeBlock {
			f := n.(*ast.FencedCodeBlock)
			if current >= 0 {
				blocks[current].body += "<code>" + string(f.Lines().Value(body)) + "\n"
			}
			if !closedFence(f, body) {
				add("fence_unclosed", "structure", "", "", "Code fence is not closed", nodeLine(n, body), true)
			}
			return ast.WalkSkipChildren, nil
		}
		if n.Kind() == ast.KindCodeBlock && current >= 0 {
			blocks[current].body += "<code>"
		}
		if n.Kind() == ast.KindCodeBlock || n.Kind() == ast.KindCodeSpan {
			return ast.WalkSkipChildren, nil
		}
		if h, ok := n.(*ast.Heading); ok {
			title := plain(n, body)
			if h.Level == 1 {
				h1++
				if strings.TrimSpace(title) != strings.TrimSpace(d.Contract.Title) {
					add("title_mismatch", "consistency", "", title, "The delivery title differs from the frozen contract title", nodeLine(n, body), true)
				}
			}
			for level := h.Level; level <= 6; level++ {
				delete(path, level)
			}
			parents := []string{}
			for l := 1; l < h.Level; l++ {
				parents = append(parents, path[l])
			}
			scope := strings.Join(parents, "/") + "/" + strconv.Itoa(h.Level) + "/" + title
			if headingSeen[scope] {
				add("heading_duplicate", "structure", title, title, "Duplicate heading within the same scope", nodeLine(n, body), true)
			}
			headingSeen[scope] = true
			path[h.Level] = title
			anchors[slug(title)] = true
			if h.Level == profile.Validation.HeadingLevel {
				blocks = append(blocks, block{title: title, line: nodeLine(n, body)})
				current = len(blocks) - 1
			}
			return ast.WalkSkipChildren, nil
		}
		if n.Kind() == ast.KindParagraph || n.Kind() == ast.KindTextBlock {
			value := plain(n, body)
			visible.WriteString(value + "\n")
			if current >= 0 {
				blocks[current].body += value + "\n"
			}
			m := referenceDefinition.FindStringSubmatch(value)
			// A citation at the start of prose is still a use, not a definition.
			// List markers, colons and URLs also occur in ordinary prose, so
			// definitions require an explicit references section.
			inReferences := path[profile.Validation.HeadingLevel] == "References" || path[profile.Validation.HeadingLevel] == "参考资料" || path[profile.Validation.HeadingLevel] == "参考来源"
			if len(m) > 0 && inReferences {
				if _, ok := refs[m[1]]; ok {
					add("reference_duplicate", "evidence", m[1], value, "Duplicate source ID", nodeLine(n, body), true)
				}
				refs[m[1]] = reference{text: m[2], locators: referenceLocators(n, m[2], body), line: nodeLine(n, body)}
			} else {
				for _, m := range citations.FindAllStringSubmatch(value, -1) {
					used[m[1]] = true
				}
			}
			if placeholders.MatchString(value) {
				add("placeholder", "structure", "", value, "Unresolved template/placeholder in delivery body", nodeLine(n, body), true)
			}
		}
		return ast.WalkContinue, nil
	})
	if h1 != 1 {
		add("title_count", "structure", "", "", "Delivery body must contain exactly one level-one title", 1, true)
	}
	if profile.Validation.MinimumBodyChars > utf8.RuneCount(body) || profile.Validation.MaximumBodyChars < utf8.RuneCount(body) {
		add("body_length", "structure", "", "", "Body length is outside the frozen profile bounds", 0, true)
	}
	mapped := map[string]bool{}
	for _, spec := range profile.Sections {
		heading := profile.Validation.SectionHeadings[spec.Key]
		found := false
		for _, b := range blocks {
			if b.title == heading {
				mapped[b.title] = true
				found = true
				if (spec.Required || profile.Validation.RequireNonEmptySections) && strings.TrimSpace(b.body) == "" {
					add("section_empty", "structure", spec.Key, "", "Required semantic block is empty", b.line, true)
				}
			}
		}
		if spec.Required && !found {
			add("section_missing", "structure", spec.Key, heading, "Required AST section is missing at the declared heading level", 0, true)
		}
	}
	if !profile.Validation.AllowUnknownSections {
		for _, b := range blocks {
			if !mapped[b.title] && b.title != "假设 / Assumptions" && b.title != "参考资料" && b.title != "参考来源" && b.title != "References" {
				add("section_unknown", "structure", b.title, b.title, "Section is not declared by the frozen profile", b.line, true)
			}
		}
	}
	for id := range used {
		if _, ok := refs[id]; !ok {
			add("reference_unknown", "evidence", id, "["+id+"]", "Citation has no source definition in the final body", 0, true)
		}
	}
	manifest := map[string]models.DocumentSource{}
	for _, source := range d.Sources.Sources {
		if _, ok := manifest[source.ID]; ok {
			add("source_duplicate", "evidence", source.ID, "", "Duplicate manifest source identity", 0, true)
		}
		manifest[source.ID] = source
	}
	for id, definition := range refs {
		source, exists := manifest[id]
		if len(definition.locators) == 0 || (exists && !allowedLocator(source.Locator)) {
			add("reference_locator", "evidence", id, definition.text, "Both the delivered reference and frozen source require safe locators", definition.line, true)
		}
		for _, locator := range definition.locators {
			if !allowedLocator(locator) {
				add("reference_locator", "evidence", id, locator, "The delivered reference contains an unsafe locator", definition.line, true)
			}
			if exists && source.Locator != locator {
				add("reference_mismatch", "evidence", id, locator, "The delivered reference differs from the frozen source locator", definition.line, true)
			}
		}
		if !exists || source.Hash == "" || source.Version == "" || source.VerificationStatus != "verified" {
			add("source_unverified", "evidence", id, "", "Citation presence does not verify source content/support. Inspect the original and its version.", 0, false)
		}
	}
	if d.ResourceError != "" {
		add("resource_incomplete", "structure", "", "", "Resource bytes or bindings are unavailable", 0, true)
	}
	for _, destination := range links {
		if strings.HasPrefix(destination, "resource://") {
			id, valid := objectlocation.ResourceID(destination)
			found := false
			for _, binding := range d.Resources.Bindings {
				if valid && binding.ResourceID == id && !binding.Private && binding.Hash != "" {
					found = true
				}
			}
			if !found {
				add("resource_incomplete", "structure", "", destination, "A shareable resource binding in this document revision is required", 0, true)
			}
		} else if strings.HasPrefix(destination, "#") {
			target, e := url.PathUnescape(strings.TrimPrefix(destination, "#"))
			if e != nil || !anchors[target] {
				add("link_target", "structure", "", destination, "Internal link has no heading target", 0, true)
			}
		} else if !allowedLocator(destination) {
			add("unsafe_link", "structure", "", destination, "Only safe source locators are supported", 0, true)
		}
	}
	textBody := visible.String()
	for _, requirement := range d.Contract.Requirements {
		for _, id := range append(append([]string{requirement.ID}, requirement.DecisionIDs...), requirement.TestIDs...) {
			if !regexp.MustCompile(`\b` + regexp.QuoteMeta(id) + `\b`).MatchString(textBody) {
				add("traceability_missing_id", "usability", "", id, "Contract traceability ID is absent from the actual delivery body", 0, true)
			}
		}
	}
	for key, value := range d.Contract.Constraints {
		pattern := regexp.MustCompile(regexp.QuoteMeta(key) + `\s*[:=：]\s*([^\s,;，；]+)`)
		for _, match := range pattern.FindAllStringSubmatch(textBody, -1) {
			if match[1] != value {
				add("constraint_conflict", "consistency", "", match[0], "Value contradicts frozen constraint: "+key+"="+value, 0, true)
			}
		}
	}
	for term, definition := range d.Contract.Terms {
		if !strings.Contains(textBody, term) || !strings.Contains(textBody, definition) {
			add("term_definition_missing", "consistency", "", term, "Frozen term or its definition is missing from the final body", 0, true)
		}
	}
	if strings.Contains(textBody, "BLOCKER:") {
		add("blocking_open_question", "usability", "open_questions", "BLOCKER:", "Unresolved implementation blockers limit delivery to proposal", 0, false)
	}

	for _, rule := range profile.Validation.Rules {
		switch rule {
		case "design_traceability":
			goals := map[string]bool{}
			edges := map[string]bool{}
			for _, line := range strings.Split(textBody, "\n") {
				ids := identifiers.FindAllString(line, -1)
				hasD, hasT := false, false
				for _, id := range ids {
					hasD = hasD || strings.HasPrefix(id, "D")
					hasT = hasT || strings.HasPrefix(id, "T")
				}
				for _, id := range ids {
					if strings.HasPrefix(id, "G") {
						goals[id] = true
						if hasD && hasT {
							edges[id] = true
						}
					}
				}
			}
			if len(goals) == 0 {
				add("goal_ids", "usability", "goals", "", "Design needs explicit G1 → D1 → T1 traceability in the final body", 0, true)
			}
			for goal := range goals {
				if !edges[goal] {
					add("goal_without_test", "usability", "testing", goal, "Each goal needs a decision and acceptance/test reference on its traceability row", 0, true)
				}
			}
			for _, req := range d.Contract.Requirements {
				if req.Blocking && (len(req.DecisionIDs) == 0 || len(req.TestIDs) == 0) {
					add("requirement_uncovered", "usability", "testing", req.ID, "Blocking requirement has no decision or test", 0, true)
				}
			}
			requireConcept(textBody, []string{"失败|failure", "恢复|recovery"}, "failure_recovery", "usability", add)
			if d.Contract.Parameters["deployment"] == "required" {
				requireConcept(textBody, []string{"前置|prerequisite", "成功|success", "回滚|rollback"}, "deployment_plan", "usability", add)
			}
		case "research_evidence":
			requireConcept(textBody, []string{"Q[0-9]+", "F[0-9]+", "C[0-9]+"}, "research_chain", "evidence", add)
			if len(used) == 0 {
				add("research_evidence_missing", "evidence", "findings", "", "Research findings need citations and evidence support", 0, true)
			}
			for _, line := range strings.Split(textBody, "\n") {
				if regexp.MustCompile(`\bF[0-9]+\b`).MatchString(line) && (!regexp.MustCompile(`\bQ[0-9]+\b`).MatchString(line) || !regexp.MustCompile(`\bC[0-9]+\b`).MatchString(line) || !citations.MatchString(line)) {
					add("research_traceability", "evidence", "findings", line, "Each finding trace row needs a question, evidence citation and conclusion ID", 0, true)
				}
			}
			add("research_support_review", "evidence", "analysis", "", "Confirm each important claim against specific evidence, comparable versions/units, conflicting evidence, uncertainty and recommendation conditions", 0, false)
		case "article_subtype":
			switch d.Contract.Parameters["subtype"] {
			case "tutorial":
				requireConcept(textBody, []string{"环境|environment", "输入|input", "步骤|step", "预期|expected", "验证|validation", "失败|failure"}, "tutorial_completeness", "usability", add)
				if d.Contract.Parameters["examples_execution"] != "user_verified" {
					requireConcept(textBody, []string{"未运行验证|not executed|not run"}, "tutorial_execution_disclosure", "evidence", add)
				}
			case "case":
				requireConcept(textBody, []string{"真实案例|real case|匿名化案例|anonymized case|假设案例|hypothetical case"}, "case_provenance", "evidence", add)
			case "opinion":
				requireConcept(textBody, []string{"反例|counterexample|边界|limitation"}, "opinion_boundary", "usability", add)
			default:
				add("article_subtype", "structure", "subtype", "", "Choose opinion, tutorial or case explicitly", 0, true)
			}
		}
	}
	// Semantic review is a separate call. This report never pretends that AST checks prove facts.
	add("manual_review", "consistency", "", "", "Review terminology, constraints, factual support and usability for this exact revision. A reason is required if using manual review.", 0, false)
	r.ReviewRuns = append(r.ReviewRuns, models.DocumentReviewRun{Kind: "semantic", Status: "not_run", PromptVersion: "document-review-v1", Message: "No semantic call has run; manual review is required unless a separate reviewer succeeds."})
	for i := range r.Issues {
		if r.Issues[i].Line == 0 && r.Issues[i].Evidence != "" {
			if start := strings.Index(d.Body, r.Issues[i].Evidence); start >= 0 {
				r.Issues[i].Line = strings.Count(d.Body[:start], "\n") + 1
			}
		}
	}
	return finish(r)
}
func finish(r models.ValidationReport) models.ValidationReport {
	// Rule iteration over maps must not give different issue identities.
	sort.SliceStable(r.Issues, func(i, j int) bool {
		a, b := r.Issues[i], r.Issues[j]
		return a.Code+a.Section+a.Evidence < b.Code+b.Section+b.Evidence
	})
	r.Status = "passed"
	for i := range r.Issues {
		r.Issues[i].ID = fmt.Sprintf("i%d", i+1)
		r.Issues[i].Evidence = Redact(r.Issues[i].Evidence)
		r.Issues[i].Basis = Redact(r.Issues[i].Basis)
		r.Issues[i].Suggestion = Redact(r.Issues[i].Suggestion)
		if r.Issues[i].Severity == "error" && !r.Issues[i].HumanConfirmation {
			r.Status = "failed"
		} else if r.Status == "passed" {
			r.Status = "needs_review"
		}
	}
	for i := range r.ReviewRuns {
		if r.ReviewRuns[i].Kind == "structure" {
			r.ReviewRuns[i].Status = "passed"
			if r.Status == "failed" {
				r.ReviewRuns[i].Status = "failed"
			}
		}
	}
	return r
}
func Recompute(r models.ValidationReport) models.ValidationReport { return finish(r) }
func plain(n ast.Node, source []byte) string {
	var out strings.Builder
	ast.Walk(n, func(c ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		if c.Kind() == ast.KindCodeSpan || c.Kind() == ast.KindFencedCodeBlock || c.Kind() == ast.KindCodeBlock {
			return ast.WalkSkipChildren, nil
		}
		switch v := c.(type) {
		case *ast.Text:
			out.Write(v.Segment.Value(source))
			if v.SoftLineBreak() || v.HardLineBreak() {
				out.WriteByte('\n')
			}
		case *ast.String:
			out.Write(v.Value)
		}
		return ast.WalkContinue, nil
	})
	return out.String()
}
func nodeLine(n ast.Node, body []byte) int {
	if n.Type() == ast.TypeBlock && n.Lines().Len() > 0 {
		return strings.Count(string(body[:n.Lines().At(0).Start]), "\n") + 1
	}
	return 0
}
func closedFence(n *ast.FencedCodeBlock, body []byte) bool {
	value, ok := n.AttributeString("document_fence_closed")
	return ok && value == true
}

func slug(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '_' || r == '-' {
			b.WriteRune(r)
		} else if unicode.IsSpace(r) {
			b.WriteByte('-')
		}
	}
	return b.String()
}
func validLocator(s string) bool {
	if strings.HasPrefix(s, "thought://") || strings.HasPrefix(s, "topic://") {
		return len(strings.SplitN(s, "://", 2)[1]) > 0
	}
	u, e := url.Parse(s)
	return e == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Host != "" && u.User == nil && !SensitiveLocator(s)
}
func requireConcept(body string, patterns []string, code, group string, add func(string, string, string, string, string, int, bool)) {
	for _, p := range patterns {
		if !regexp.MustCompile("(?i)" + p).MatchString(body) {
			add(code, group, "", p, "Required type-specific information is absent from the final body", 0, true)
		}
	}
}
