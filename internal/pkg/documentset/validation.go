package documentset

import (
	"fmt"
	"net/url"
	"path"
	"sort"
	"strings"
	"thoughtflow/internal/pkg/documentprofile"
	"unicode"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func escapeCell(s string) string {
	return strings.NewReplacer("|", "\\|", "\n", " ", "\r", " ", "<", "&lt;", ">", "&gt;").Replace(s)
}

// FactBlock is the sole renderer of authoritative definitions. Explanatory prose
// is reviewed separately; table bytes in the saved body are checked again.
func FactBlock(f Fact) string {
	b := fmt.Sprintf("\n### contract-%s\n\n%s: %s\n\n%s\n", f.ID, escapeCell(f.ID), escapeCell(f.Name), f.Content)
	if len(f.Fields) > 0 {
		b += "\n| Field | Type | Nullable | Classification | Entity | Permission | Errors |\n| --- | --- | --- | --- | --- | --- | --- |\n"
		for _, v := range f.Fields {
			b += fmt.Sprintf("| %s | %s | %t | %s | %s | %s | %s |\n", escapeCell(v.ID), escapeCell(v.Type), v.Nullable, escapeCell(v.Classification), escapeCell(v.EntityID+"."+v.EntityField), escapeCell(v.Permission), escapeCell(strings.Join(v.Errors, ", ")))
		}
	}
	return b
}
func Contract(p Plan, m PlanMember) models.DocumentContract {
	parameters := map[string]string{}
	for k, v := range p.Shared.Inputs {
		parameters[k] = v
	}
	parameters["_links_hash"] = documentstore.Hash(nil)
	c := models.DocumentContract{SchemaVersion: 1, ProfileRef: m.ProfileRef, Title: m.Title, Purpose: p.Shared.Inputs["goal"], Audience: "Design participants", Language: "zh-CN", Parameters: parameters, Terms: map[string]string{}, Constraints: map[string]string{}, ContextHash: p.Capture.ContextHash, ReviewPolicy: models.DocumentReviewPolicy{Version: documentvalidation.ReviewPolicyVersion, SemanticRequired: true, AllowManual: true}}
	for _, f := range p.Shared.Facts {
		switch f.Kind {
		case "term":
			c.Terms[f.Name] = f.Content
		case "constraint":
			c.Constraints[f.Name] = f.Content
		case "requirement":
			for _, id := range m.Covers {
				if id == f.ID {
					c.Requirements = append(c.Requirements, models.DocumentRequirement{ID: f.ID, Blocking: true})
				}
			}
		}
	}
	return c
}
func CommonBlock(p Plan, m PlanMember) string {
	out := ""
	for _, f := range p.Shared.Facts {
		if f.Owner == m.Key || f.Kind == "term" || f.Kind == "constraint" {
			out += FactBlock(f)
		}
	}
	for _, id := range m.Covers {
		out += "\nCoverage: " + id + "\n"
	}
	for _, other := range p.Members {
		if other.Key != m.Key {
			anchor := "scope"
			if profile, e := documentprofile.Parse([]byte(other.ProfileSnapshot), documentprofile.Limits{}); e == nil && len(profile.Sections) > 0 {
				anchor = StableAnchor(profile.Validation.SectionHeadings[profile.Sections[0].Key])
			}
			out += fmt.Sprintf("\n[%s](docset://%s/%s#%s)\n", escapeCell(other.Title), p.SetID, url.PathEscape(other.Key), anchor)
		}
	}
	return out
}
func CandidateHash(r Revision) string {
	return documentstore.HashJSON(struct {
		Plan    Plan       `json:"plan"`
		Members []Artifact `json:"members"`
		Version string     `json:"validator_version"`
	}{r.Plan, r.Members, ValidatorVersion})
}
func ReportHash(r Report) string     { r.Hash = ""; return documentstore.HashJSON(r) }
func RevisionHash(r Revision) string { r.Hash = ""; return documentstore.HashJSON(r) }
func Rehash(r *Revision) {
	r.Report.CandidateHash = CandidateHash(*r)
	r.Report.Hash = ReportHash(r.Report)
	r.Hash = RevisionHash(*r)
}
func Checklist(p Plan) []ReviewItem {
	keys := []string{}
	for _, m := range p.Members {
		keys = append(keys, m.Key)
	}
	out := []ReviewItem{{ID: "capture-scope", Members: keys}}
	for _, m := range p.Members {
		out = append(out, ReviewItem{ID: "member:" + m.Key, Members: []string{m.Key}, Facts: m.Covers})
	}
	for _, f := range p.Shared.Facts {
		members := []string{}
		for _, m := range p.Members {
			for _, id := range m.Covers {
				if id == f.ID {
					members = append(members, m.Key)
					break
				}
			}
		}
		if len(members) == 0 && len(p.Members) > 0 {
			members = []string{p.Members[0].Key}
		}
		out = append(out, ReviewItem{ID: "fact:" + f.ID, Members: members, Facts: []string{f.ID}})
	}
	for _, rel := range p.Shared.Relations {
		members := []string{}
		for _, m := range p.Members {
			for _, id := range m.Covers {
				if id == rel.From || id == rel.To {
					members = append(members, m.Key)
					break
				}
			}
		}
		out = append(out, ReviewItem{ID: "relation:" + rel.ID, Members: members, Facts: []string{rel.From, rel.To}, Relations: []string{rel.ID}})
	}
	return out
}
func Validate(r Revision) Report {
	report := Report{CandidateHash: CandidateHash(r), ValidatorVersion: ValidatorVersion, StructuralStatus: "passed", SemanticStatus: "not_run", Status: "awaiting_review", Checklist: Checklist(r.Plan), Issues: []Issue{}, Evidence: []ReviewEvidence{}}
	add := func(code, msg string, loc ...Location) {
		report.Issues = append(report.Issues, Issue{ID: fmt.Sprintf("hard-%d", len(report.Issues)+1), Code: code, Severity: "error", Hard: true, Message: msg, Locations: loc})
		report.StructuralStatus = "failed"
		report.Status = "blocked"
	}
	if e := ValidatePlan(r.Plan); e != nil {
		add("plan_invalid", e.Error())
	}
	for _, q := range r.Plan.Shared.Questions {
		if q.Blocking {
			add("clarification_required", q.Message)
		}
	}
	planned := map[string]PlanMember{}
	artifacts := map[string]Artifact{}
	covered := map[string][]string{}
	factMap := map[string]Fact{}
	for _, m := range r.Plan.Members {
		planned[m.Key] = m
		for _, id := range m.Covers {
			covered[id] = append(covered[id], m.Key)
		}
	}
	for _, a := range r.Members {
		m, ok := planned[a.Key]
		if !ok || artifacts[a.Key].Key != "" {
			add("member_identity", "Unplanned or duplicate member", Location{Member: a.Key})
			continue
		}
		artifacts[a.Key] = a
		if (a.ResourceHash != "" && a.ResourceHash != documentstore.HashJSON(a.Resources)) || (len(a.Resources.Bindings) > 0 && (a.ResourceHash == "" || a.Resources.ObjectID != a.DocumentID)) {
			add("resource_hash", "Frozen resource bindings differ", Location{Member: a.Key})
		}
		if a.OriginalHash != "" && a.OriginalHash != documentstore.Hash([]byte(a.Original)) {
			add("original_hash", "Imported original bytes differ", Location{Member: a.Key})
		}
		if a.DocumentID != m.DocumentID || !documentstore.ValidID(a.RevisionID) || a.BodyHash != documentstore.Hash([]byte(a.Body)) || !utf8.ValidString(a.Body) || len(a.Body) > 1<<20 || !contractMatches(r.Plan, m, a) || a.ProfileSnapshot != m.ProfileSnapshot {
			add("member_hash", "Frozen identity, contract or bytes differ", Location{Member: a.Key})
		}
		for _, binding := range a.Resources.Bindings {
			if !objectlocation.ValidBinding(binding) {
				add("resource_unsafe", "Resource is not a public controlled asset", Location{Member: a.Key})
			}
		}
		d := documentstore.RevisionData{Resources: a.Resources, Body: a.Body, Profile: a.ProfileSnapshot, Contract: a.Contract, Sources: a.Sources, Revision: models.DocumentRevision{DocumentID: a.DocumentID, RevisionID: a.RevisionID, BodyHash: a.BodyHash}}
		check := documentvalidation.Validate(d)
		for _, i := range check.Issues {
			if i.Severity == "error" {
				add("single:"+i.Code, i.Basis, Location{Member: a.Key, Section: i.Section, Evidence: i.Evidence})
			}
			if i.Severity == "warning" && i.Code != "manual_review" {
				report.Issues = append(report.Issues, Issue{ID: singleIssueID(a.Key, i), Code: "single:" + i.Code, Severity: "warning", Confirmable: true, Message: i.Basis, Locations: []Location{singleWarningLocation(a, i)}})
			}
		}
		for _, dep := range m.DependsOn {
			if a.Dependencies[dep] == "" {
				add("dependency_version", "Missing frozen dependency", Location{Member: a.Key})
			}
		}
	}
	for key, m := range planned {
		if artifacts[key].Key == "" && m.Required {
			add("member_missing", "Required member missing", Location{Member: key})
		}
	}
	for _, f := range r.Plan.Shared.Facts {
		factMap[f.ID] = f
		if f.Status != "confirmed" {
			add("fact_unconfirmed", "Required fact remains proposed or unresolved: "+f.ID)
		}
		if len(covered[f.ID]) == 0 {
			add("scope_uncovered", "No delivery location for "+f.ID)
		}
		if f.Kind == "interface" || f.Kind == "entity" {
			if planned[f.Owner].Key == "" {
				add("contract_owner", "Exactly one planned definition owner is required: "+f.ID)
			}
		}
		for _, a := range r.Members {
			expected := FactBlock(f)
			count := strings.Count(a.Body, expected)
			if a.Key == f.Owner || f.Kind == "term" || f.Kind == "constraint" {
				if count != 1 || !HasAnchor(a.Body, "contract-"+strings.ToLower(f.ID)) {
					add("final_contract_mismatch", "Final body is missing or changes authoritative definition "+f.ID, Location{Member: a.Key, Section: "contract-" + f.ID})
				}
			} else if count > 0 {
				add("duplicate_contract_owner", "Definition appears outside authoritative owner", Location{Member: a.Key, Section: "contract-" + f.ID})
			}
		}
	}
	for _, f := range r.Plan.Shared.Facts {
		for _, field := range f.Fields {
			if f.Kind != "interface" || field.Classification != "persisted" {
				continue
			}
			entity, ok := factMap[field.EntityID]
			found := false
			for _, target := range entity.Fields {
				if target.ID == field.EntityField {
					found = true
					if target.Type != field.Type || target.Nullable != field.Nullable {
						add("interface_entity_conflict", "Type/nullability mismatch for "+f.ID+"."+field.ID, Location{Member: f.Owner, Section: "contract-" + f.ID}, Location{Member: entity.Owner, Section: "contract-" + entity.ID})
					}
				}
			}
			if !ok || entity.Kind != "entity" || !found {
				add("entity_mapping_missing", "Persisted field lacks an authoritative entity mapping: "+f.ID+"."+field.ID)
			}
		}
	}
	for _, f := range r.Plan.Shared.Facts {
		if f.Kind == "requirement" || f.Kind == "module" {
			found := false
			for _, rel := range r.Plan.Shared.Relations {
				if (rel.From == f.ID && factMap[rel.To].Kind == "verification") || (rel.To == f.ID && factMap[rel.From].Kind == "verification") {
					found = true
				}
			}
			if !found {
				add("verification_coverage_missing", "No verification relation for "+f.ID)
			}
		}
	}
	for _, a := range r.Members {
		for key, rev := range a.Dependencies {
			if artifacts[key].RevisionID != rev {
				add("dependency_version", "Dependency points at another revision", Location{Member: a.Key})
			}
		}
		for _, link := range Links(a.Body) {
			u, e := url.Parse(link)
			if e != nil {
				add("link_invalid", link, Location{Member: a.Key})
				continue
			}
			if u.Scheme == "" && strings.EqualFold(path.Ext(u.Path), ".md") {
				add("member_link_unresolved", link, Location{Member: a.Key})
			}
			if u.Scheme == "docset" {
				key, e := url.PathUnescape(strings.TrimPrefix(u.Path, "/"))
				target, ok := artifacts[key]
				if e != nil || u.Host != r.Plan.SetID || u.RawQuery != "" || u.User != nil || !ok || !HasAnchor(target.Body, u.Fragment) {
					add("member_link_unresolved", link, Location{Member: a.Key})
				}
			} else if u.Scheme == "thought" || u.Scheme == "topic" {
				if u.Host == "" || u.User != nil {
					add("link_invalid", link, Location{Member: a.Key})
				}
			}
		}
	}
	sort.Slice(report.Issues, func(i, j int) bool { return report.Issues[i].ID < report.Issues[j].ID })
	report.Hash = ReportHash(report)
	return report
}
func Links(body string) []string {
	raw := []byte(body)
	tree := goldmark.DefaultParser().Parse(text.NewReader(raw))
	out := []string{}
	ast.Walk(tree, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		switch v := n.(type) {
		case *ast.Link:
			out = append(out, documentvalidation.MarkdownLocator(string(v.Destination)))
		case *ast.Image:
			out = append(out, documentvalidation.MarkdownLocator(string(v.Destination)))
		case *ast.AutoLink:
			out = append(out, string(v.URL(raw)))
		}
		return ast.WalkContinue, nil
	})
	return out
}

// StableAnchor is shared by final-body checks and the reader's heading convention.
func StableAnchor(s string) string {
	var out strings.Builder
	for _, r := range strings.ToLower(s) {
		if unicode.IsLetter(r) || unicode.IsNumber(r) || r == '-' {
			out.WriteRune(r)
		} else if unicode.IsSpace(r) || r == '_' {
			out.WriteRune(' ')
		}
	}
	value := strings.Join(strings.Fields(out.String()), "-")
	if value == "" {
		return "section"
	}
	return value
}

type bodyHeading struct {
	anchor       string
	start, level int
}

func bodyHeadings(body string) []bodyHeading {
	raw := []byte(body)
	tree := goldmark.DefaultParser().Parse(text.NewReader(raw))
	out := []bodyHeading{}
	counts := map[string]int{}
	ast.Walk(tree, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
		if h, ok := n.(*ast.Heading); ok && enter {
			base := StableAnchor(string(h.Text(raw)))
			id := base
			if counts[base] > 0 {
				id = fmt.Sprintf("%s-%d", base, counts[base])
			}
			counts[base]++
			out = append(out, bodyHeading{id, h.Lines().At(0).Start, h.Level})
		}
		return ast.WalkContinue, nil
	})
	return out
}
func HasAnchor(body, anchor string) bool {
	if anchor == "" {
		return true
	}
	for _, h := range bodyHeadings(body) {
		if h.anchor == anchor {
			return true
		}
	}
	return false
}
func EvidenceInSection(body, anchor, quote string) bool {
	headings := bodyHeadings(body)
	for i, h := range headings {
		if h.anchor != anchor {
			continue
		}
		end := len(body)
		for _, next := range headings[i+1:] {
			if next.level <= h.level {
				end = next.start
				break
			}
		}
		return strings.TrimSpace(quote) != "" && strings.Contains(body[h.start:end], quote)
	}
	return false
}
func VerifyEvidence(e ReviewEvidence, items map[string]ReviewItem, members map[string]Artifact) error {
	item, ok := items[e.CheckID]
	if !ok || e.Reason == "" || len(e.Locations) == 0 || (e.Verdict != "passed" && e.Verdict != "risk" && e.Verdict != "blocked") {
		return ErrInvalid
	}
	seen := map[string]bool{}
	for _, l := range e.Locations {
		a, ok := members[l.Member]
		if !ok || l.Section == "" || !HasAnchor(a.Body, l.Section) || strings.TrimSpace(l.Evidence) == "" || !EvidenceInSection(a.Body, l.Section, l.Evidence) {
			return ErrInvalid
		}
		seen[l.Member] = true
	}
	for _, key := range item.Members {
		if !seen[key] {
			return fmt.Errorf("%w: review omits %s", ErrInvalid, key)
		}
	}
	return nil
}
func ApplyReview(r *Revision, response ReviewResponse, mode, reviewer string) error {
	if r.Report.CandidateHash != CandidateHash(*r) || r.Report.StructuralStatus != "passed" {
		return ErrRules
	}
	items := map[string]ReviewItem{}
	members := map[string]Artifact{}
	for _, i := range r.Report.Checklist {
		items[i.ID] = i
	}
	for _, a := range r.Members {
		members[a.Key] = a
	}
	seen := map[string]bool{}
	for _, e := range response.Evidence {
		if seen[e.CheckID] {
			return ErrInvalid
		}
		if err := VerifyEvidence(e, items, members); err != nil {
			return err
		}
		seen[e.CheckID] = true
	}
	if len(seen) != len(items) {
		r.Report.SemanticStatus = "incomplete"
		r.Report.Status = "awaiting_review"
		return ErrRules
	}
	for i := range response.Issues {
		issue := &response.Issues[i]
		if issue.Hard || issue.Code == "" || issue.Message == "" || len(issue.Locations) == 0 || (issue.Severity != "warning" && issue.Severity != "error") {
			return ErrInvalid
		}
		issue.ID = fmt.Sprintf("semantic-%d", i+1)
		for _, l := range issue.Locations {
			if members[l.Member].Key == "" || !HasAnchor(members[l.Member].Body, l.Section) || l.Evidence == "" || !EvidenceInSection(members[l.Member].Body, l.Section, l.Evidence) {
				return ErrInvalid
			}
		}
		issue.Confirmable = issue.Severity == "warning"
	}
	r.Report.Evidence = response.Evidence
	r.Report.Issues = append(r.Report.Issues, response.Issues...)
	r.Report.ReviewedBy = reviewer
	r.Report.SemanticStatus = mode
	r.Report.Status = "ready"
	for _, e := range response.Evidence {
		if e.Verdict != "passed" {
			r.Report.Status = "awaiting_review"
		}
	}
	for _, i := range r.Report.Issues {
		if i.Severity == "error" {
			r.Report.Status = "blocked"
		} else {
			r.Report.Status = "awaiting_review"
		}
	}
	Rehash(r)
	return nil
}

// Impact computes a semantic dependency closure independently of scheduling.
func Impact(old, next SharedDesign) []string {
	previous := map[string]string{}
	changed := map[string]bool{}
	for _, f := range old.Facts {
		previous[f.ID] = f.Hash
	}
	for _, f := range next.Facts {
		if previous[f.ID] != f.Hash {
			changed[f.ID] = true
		}
		delete(previous, f.ID)
	}
	for id := range previous {
		changed[id] = true
	}
	progress := true
	for progress {
		progress = false
		for _, rel := range append(append([]Relation{}, old.Relations...), next.Relations...) {
			if changed[rel.From] && !changed[rel.To] {
				changed[rel.To] = true
				progress = true
			}
			if changed[rel.To] && !changed[rel.From] {
				changed[rel.From] = true
				progress = true
			}
		}
	}
	out := []string{}
	for id := range changed {
		out = append(out, id)
	}
	sort.Strings(out)
	return out
}

func contractMatches(p Plan, m PlanMember, a Artifact) bool {
	expected := Contract(p, m)
	if a.ReusedFrom != "" {
		expected.ContextHash = a.Contract.ContextHash
	}
	return documentstore.HashJSON(expected) == documentstore.HashJSON(a.Contract)
}

// Ready rechecks the complete persisted gate, including evidence coverage. A
// serialized status string alone can never authorize publication or delivery.
func Ready(r Revision) error {
	if r.Report.Status != "ready" || r.Report.ValidatorVersion != ValidatorVersion || r.Report.CandidateHash != CandidateHash(r) || r.Report.Hash != ReportHash(r.Report) || r.Hash != RevisionHash(r) || r.Report.ReviewedBy == "" {
		return ErrRules
	}
	if r.Report.SemanticStatus != "model_reviewed" && r.Report.SemanticStatus != "manual_reviewed" {
		return ErrRules
	}
	check := Validate(r)
	if check.StructuralStatus != "passed" || documentstore.HashJSON(check.Checklist) != documentstore.HashJSON(r.Report.Checklist) {
		return ErrRules
	}
	for _, required := range check.Issues {
		found := false
		for _, actual := range r.Report.Issues {
			if documentstore.HashJSON(required) == documentstore.HashJSON(actual) {
				found = true
				break
			}
		}
		if !found {
			return ErrRules
		}
	}
	items := map[string]ReviewItem{}
	members := map[string]Artifact{}
	seen := map[string]bool{}
	for _, v := range check.Checklist {
		items[v.ID] = v
	}
	for _, a := range r.Members {
		members[a.Key] = a
	}
	for _, e := range r.Report.Evidence {
		if seen[e.CheckID] || e.Verdict != "passed" || VerifyEvidence(e, items, members) != nil {
			return ErrRules
		}
		seen[e.CheckID] = true
	}
	if len(seen) != len(items) {
		return ErrRules
	}
	decisions := map[string]ReviewDecision{}
	for _, d := range r.Report.ReviewDecisions {
		if d.IssueID == "" || strings.TrimSpace(d.Reason) == "" || len(d.Locations) == 0 || decisions[d.IssueID].IssueID != "" {
			return ErrRules
		}
		for _, l := range d.Locations {
			if !EvidenceInSection(members[l.Member].Body, l.Section, l.Evidence) {
				return ErrRules
			}
		}
		decisions[d.IssueID] = d
	}
	for _, issue := range r.Report.Issues {
		if issue.Hard || issue.Severity != "warning" || !issue.Confirmable || decisions[issue.ID].IssueID == "" {
			return ErrRules
		}
	}
	return nil
}

// ApplicableFacts includes the semantic closure and shared contracts needed to
// explain the member. Contracts without a narrower applicability declaration
// apply to module and verification roles; a revision ID is not context text.
func ApplicableFacts(p Plan, m PlanMember) ([]Fact, []Relation) {
	included := map[string]bool{}
	for _, id := range m.Covers {
		included[id] = true
	}
	for _, f := range p.Shared.Facts {
		if f.Owner == m.Key || f.Kind == "term" || f.Kind == "constraint" || ((m.Role == "module" || m.Role == "verification") && (f.Kind == "interface" || f.Kind == "entity")) {
			included[f.ID] = true
		}
	}
	changed := true
	for changed {
		changed = false
		for _, r := range p.Shared.Relations {
			if included[r.From] || included[r.To] {
				if !included[r.From] || !included[r.To] {
					changed = true
				}
				included[r.From], included[r.To] = true, true
			}
		}
	}
	facts := []Fact{}
	relations := []Relation{}
	for _, f := range p.Shared.Facts {
		if included[f.ID] {
			facts = append(facts, f)
		}
	}
	for _, r := range p.Shared.Relations {
		if included[r.From] && included[r.To] {
			relations = append(relations, r)
		}
	}
	return facts, relations
}

func singleIssueID(member string, issue models.DocumentIssue) string {
	// A single-document report's display ordinal is not a persisted risk identity.
	// The remaining fields identify the rule, source/section and actual evidence;
	// candidate/report hashes separately bind every approval to the frozen bytes.
	issue.ID = ""
	return "single:" + member + ":" + issue.Code + ":" + strings.TrimPrefix(documentstore.HashJSON(issue), "sha256:")
}

func singleWarningLocation(a Artifact, i models.DocumentIssue) Location {
	// Prefer the actual reference or issue text, preserving a real section quote.
	headings := bodyHeadings(a.Body)
	for _, h := range headings {
		for _, line := range strings.Split(a.Body[h.start:], "\n") {
			quote := strings.TrimSpace(line)
			if quote != "" && ((i.Evidence != "" && strings.Contains(quote, i.Evidence)) || (i.Section != "" && strings.Contains(quote, "["+i.Section+"]"))) && EvidenceInSection(a.Body, h.anchor, quote) {
				return Location{Member: a.Key, Section: h.anchor, Evidence: quote}
			}
		}
	}
	if len(headings) > 0 {
		h := headings[0]
		line := strings.Split(a.Body[h.start:], "\n")[0]
		return Location{Member: a.Key, Section: h.anchor, Evidence: line}
	}
	return Location{Member: a.Key}
}
