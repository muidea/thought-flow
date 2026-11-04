package documentset

import (
	"encoding/json"
	"math"
	"sort"
	"strconv"
	"strings"
	"thoughtflow/internal/pkg/documentstore"
)

const EngineeringPurpose = "engineering-design-v1"
const EngineeringPromptVersion = "engineering-prompts-v1"

type ContentObligation struct {
	ID   string `json:"id" yaml:"id"`
	Role string `json:"role" yaml:"role"`
	Kind string `json:"kind" yaml:"kind"`
	When string `json:"when,omitempty" yaml:"when,omitempty"`
}
type ObligationResolution struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Facts   []string `json:"facts"`
	Member  string   `json:"member_key"`
	Section string   `json:"section"`
	Reason  string   `json:"reason"`
	Source  Source   `json:"source"`
}
type Provenance struct {
	ID        string `json:"id"`
	Kind      string `json:"kind"`
	PublicRef string `json:"public_ref"`
}
type AcceptanceContract struct {
	Preconditions []string          `json:"preconditions"`
	Stimulus      []string          `json:"stimulus"`
	Observations  []string          `json:"expected_observations"`
	PassRule      string            `json:"pass_rule"`
	Method        string            `json:"verification_method"`
	Evidence      []string          `json:"evidence_requirements"`
	Negative      []string          `json:"negative_cases"`
	Boundary      []string          `json:"boundary_cases"`
	Recovery      []string          `json:"recovery_cases"`
	NotApplicable map[string]string `json:"not_applicable,omitempty"`
}
type QualityContract struct {
	Kind          string `json:"kind"`
	Metric        string `json:"metric,omitempty"`
	Environment   string `json:"environment,omitempty"`
	Workload      string `json:"workload,omitempty"`
	Statistic     string `json:"statistic,omitempty"`
	Window        string `json:"window,omitempty"`
	Operator      string `json:"operator,omitempty"`
	Threshold     string `json:"threshold,omitempty"`
	Unit          string `json:"unit,omitempty"`
	FailurePolicy string `json:"failure_policy,omitempty"`
	Principal     string `json:"principal,omitempty"`
	Resource      string `json:"resource,omitempty"`
	Permission    string `json:"permission,omitempty"`
	Forbidden     string `json:"forbidden,omitempty"`
	Outcome       string `json:"outcome,omitempty"`
}
type StateContract struct {
	Initial     string `json:"initial_state"`
	Trigger     string `json:"trigger"`
	Guard       string `json:"guard"`
	Target      string `json:"target_state"`
	Failure     string `json:"failure_result"`
	Persistence string `json:"persistence"`
}
type ArtifactContract struct {
	Format        string   `json:"format"`
	Files         []string `json:"files"`
	EntryPoint    string   `json:"entry_point"`
	Build         string   `json:"build"`
	Run           string   `json:"run"`
	Environment   string   `json:"environment"`
	Configuration string   `json:"configuration"`
	DataPolicy    string   `json:"data_policy"`
	Usage         string   `json:"usage"`
	Security      string   `json:"security"`
	License       string   `json:"license"`
	Verification  []string `json:"verification"`
}
type ScopeExclusion struct {
	FactID string `json:"fact_id"`
	Reason string `json:"reason"`
	Source Source `json:"source"`
}
type DesignScope struct {
	ID                string           `json:"id"`
	Requirements      []string         `json:"requirement_refs"`
	GlobalConstraints []string         `json:"global_constraint_refs"`
	Excluded          []ScopeExclusion `json:"excluded_requirements"`
	Source            Source           `json:"source"`
}
type FactReference struct {
	SetID      string `json:"set_id"`
	RevisionID string `json:"revision_id"`
	FactID     string `json:"fact_id"`
	Hash       string `json:"fact_hash"`
}
type HistoricalFact struct {
	Ref  FactReference `json:"ref"`
	Fact Fact          `json:"fact"`
}
type FactChange struct {
	Action   string          `json:"action"`
	From     []FactReference `json:"from"`
	To       []string        `json:"to"`
	Source   Source          `json:"source"`
	Reason   string          `json:"reason"`
	Coverage string          `json:"replacement_coverage"`
}

func Engineering(p Profile) bool { return p.Purpose == EngineeringPurpose }

// FactMeaning is the common identity used by authorization and change analysis.
// Input evidence and cached hashes are envelopes, not a second semantic model.
func FactMeaning(f Fact) string {
	f.Hash = ""
	f.Source = Source{}
	return documentstore.HashJSON(f)
}
func publicFact(f Fact) Fact {
	f.Source = Source{}
	f.Hash = ""
	return f
}
func MeaningBlock(f Fact) string {
	raw, _ := json.MarshalIndent(struct {
		Modality   string              `json:"modality"`
		Status     string              `json:"status"`
		Provenance []Provenance        `json:"provenance"`
		Optional   bool                `json:"non_normative"`
		Scope      []string            `json:"scope_refs"`
		Acceptance *AcceptanceContract `json:"acceptance,omitempty"`
		Quality    *QualityContract    `json:"quality,omitempty"`
		State      *StateContract      `json:"state_rule,omitempty"`
		Artifact   *ArtifactContract   `json:"artifact,omitempty"`
	}{f.Modality, f.Status, f.Provenance, f.NonNormative, f.ScopeRefs, f.Acceptance, f.Quality, f.StateRule, f.Artifact}, "", "  ")
	return "\n```json\n" + string(raw) + "\n```\n"
}
func nonempty(ss ...string) bool {
	for _, s := range ss {
		if strings.TrimSpace(s) == "" {
			return false
		}
	}
	return true
}
func texts(ss []string) bool {
	if len(ss) == 0 {
		return false
	}
	for _, s := range ss {
		if !nonempty(s) {
			return false
		}
	}
	return true
}

// EngineeringIssues diagnoses missing substantive contracts without inventing
// defaults. Unknown content can be persisted as a plan, never marked ready.
func EngineeringIssues(p Plan) []Issue {
	if !Engineering(p.Profile) {
		return nil
	}
	out := []Issue{}
	add := func(code, id, field, message string) {
		owner := ""
		for _, f := range p.Shared.Facts {
			if f.ID == id {
				owner = f.Owner
			}
		}
		out = append(out, Issue{ID: "design:" + code + ":" + id + ":" + field, Code: code, Hard: true, Severity: "error", Status: "FAIL", FactID: id, Path: field, Message: message, Suggestion: "Revise the referenced contract or provide new user evidence; create a new plan and rerun validation. Hard integrity and provenance checks cannot be waived.", Locations: []Location{{Member: owner, Section: "contract-" + strings.ToLower(id)}}})
	}
	facts := map[string]Fact{}
	confirmedNormative := map[string]bool{}
	for _, f := range p.Shared.Facts {
		facts[f.ID] = f
		confirmedNormative[f.ID] = !f.NonNormative && EpistemicStatus(p.Shared, f) == "confirmed"
	}
	scope := p.Shared.Scope
	if scope == nil || !ValidKey(scope.ID) || len(scope.Requirements) == 0 {
		add("design_scope_missing", "scope", "scope", "Confirmed complete scope is required")
	}
	required := map[string]bool{}
	excluded := map[string]bool{}
	if scope != nil {
		for _, id := range scope.Requirements {
			if required[id] || facts[id].Kind != "requirement" {
				add("design_scope_invalid", id, "scope.requirement_refs", "Scope must reference unique requirements")
			}
			required[id] = true
		}
		for _, x := range scope.Excluded {
			if excluded[x.FactID] || !required[x.FactID] || !nonempty(x.Reason) {
				add("design_scope_exclusion", x.FactID, "scope.excluded_requirements", "Exclusion requires a known requirement and approval")
			}
			excluded[x.FactID] = true
		}
		for _, id := range scope.GlobalConstraints {
			if facts[id].Kind != "constraint" {
				add("design_scope_invalid", id, "scope.global_constraint_refs", "Unknown global constraint")
			}
		}
	}
	for _, r := range p.Shared.Relations {
		if r.Kind == "depends_on" && !excluded[r.From] && excluded[r.To] {
			add("design_scope_dependency", r.From, "relations", "Included work depends on an excluded requirement")
		}
	}
	for _, q := range p.Shared.Questions {
		for _, id := range q.Facts {
			if facts[id].ID == "" {
				add("design_question_reference", id, "open_questions", "Issue references an unknown fact")
			}
		}
		if q.OwnerRole != "" {
			known := false
			for _, role := range p.Profile.Roles {
				if role.Key == q.OwnerRole {
					known = true
				}
			}
			if !known {
				add("design_question_reference", q.ID, "owner_role", "Issue owner must be a declared responsibility role, not an invented identity")
			}
		}
		if q.Resolution == "" && q.Blocking {
			add("design_issue_open", q.ID, "open_questions", "Unresolved blocking design issue")
		}
	}
	provenanceIDs := map[string]Provenance{}
	artifact := false
	for _, f := range p.Shared.Facts {
		if !f.NonNormative && EpistemicStatus(p.Shared, f) != "confirmed" {
			add("design_confirmation", f.ID, "status", "Required normative fact has unresolved knowledge status")
		}
		for _, field := range f.Fields {
			if !engineeringFieldType(field.Type) {
				add("design_field_type", f.ID, "fields."+field.ID+".type", "Unknown field type")
			}
		}
		if f.NonNormative {
			if f.Modality != "MAY" || required[f.ID] {
				add("design_optional_invalid", f.ID, "non_normative", "Required scope cannot be relabeled optional")
			}
			continue
		}
		if !nonempty(f.Modality) || (f.Modality != "MUST" && f.Modality != "MUST_NOT" && f.Modality != "MAY" && f.Modality != "NEEDS_CONFIRMATION") {
			add("design_modality", f.ID, "modality", "Explicit normative modality is required")
		}
		if f.Modality == "NEEDS_CONFIRMATION" {
			add("design_confirmation", f.ID, "modality", "Normative choice still needs confirmation")
		}
		if len(f.Provenance) == 0 {
			add("design_provenance", f.ID, "provenance", "Normative facts require classified provenance")
		}
		if scope != nil {
			found := false
			for _, id := range f.ScopeRefs {
				if id == scope.ID {
					found = true
				} else {
					add("design_scope_invalid", f.ID, "scope_refs", "Unknown scope reference")
				}
			}
			if !found {
				add("design_scope_invalid", f.ID, "scope_refs", "Fact must belong to the confirmed scope")
			}
		}
		for _, v := range f.Provenance {
			if old, ok := provenanceIDs[v.ID]; ok && old != v {
				add("design_provenance", f.ID, "provenance", "One provenance ID cannot have conflicting definitions")
			}
			provenanceIDs[v.ID] = v
			if !ValidKey(v.ID) || !nonempty(v.PublicRef) {
				add("design_provenance", f.ID, "provenance", "Provenance requires stable public identity")
			}
			switch v.Kind {
			case "user_requirement", "user_decision", "model_proposal", "implementation_derived", "reference":
			default:
				add("design_provenance", f.ID, "provenance", "Unknown provenance kind")
			}
			if f.Status == "confirmed" && (v.Kind == "model_proposal" || v.Kind == "implementation_derived") {
				add("design_unapproved", f.ID, "provenance", "Derived suggestions cannot claim user confirmation")
			}
		}
		if (f.Acceptance != nil && f.Kind != "verification") || (f.Quality != nil && f.Kind != "constraint") || (f.StateRule != nil && f.Kind != "constraint") || (f.Artifact != nil && f.Kind != "artifact_spec") {
			add("design_payload_kind", f.ID, "kind", "Typed payload does not match fact kind")
		}
		if f.Kind == "verification" {
			a := f.Acceptance
			if a == nil {
				add("design_acceptance", f.ID, "acceptance", "Acceptance requires observable outcomes, method and evidence")
			} else {
				if !texts(a.Stimulus) || !texts(a.Observations) || !texts(a.Evidence) || !nonempty(a.PassRule, a.Method) {
					add("design_acceptance", f.ID, "acceptance", "Acceptance must be independently decidable")
				}
				for _, pair := range []struct {
					key string
					v   []string
				}{{"preconditions", a.Preconditions}, {"negative", a.Negative}, {"boundary", a.Boundary}, {"recovery", a.Recovery}} {
					if !texts(pair.v) && !nonempty(a.NotApplicable[pair.key]) {
						add("design_acceptance", f.ID, "acceptance."+pair.key, "Missing cases require an explicit applicability reason")
					}
				}
			}
		}
		if f.Kind == "constraint" && f.Quality == nil && f.StateRule == nil {
			add("design_quality", f.ID, "constraint", "Constraint needs a measurable quality or explicit state contract")
		}
		if q := f.Quality; q != nil {
			valid := false
			switch q.Kind {
			case "measurement":
				valid = nonempty(q.Metric, q.Environment, q.Workload, q.Statistic, q.Window, q.Operator, q.Threshold, q.Unit, q.FailurePolicy)
				threshold, e := strconv.ParseFloat(q.Threshold, 64)
				if e != nil || math.IsNaN(threshold) || math.IsInf(threshold, 0) {
					valid = false
				}
				switch q.Operator {
				case "<", "<=", ">", ">=", "==", "!=", "lt", "lte", "gt", "gte", "eq", "ne":
				default:
					valid = false
				}
			case "security":
				valid = nonempty(q.Principal, q.Resource, q.Permission, q.Forbidden, q.Outcome)
			}
			if !valid {
				add("design_quality", f.ID, "quality", "Quality requires measurement conditions or a testable security boundary")
			}
		}
		if v := f.StateRule; v != nil && !nonempty(v.Initial, v.Trigger, v.Guard, v.Target, v.Failure, v.Persistence) {
			add("design_state", f.ID, "state_rule", "State transition, failure and persistence must be defined")
		}
		if f.Kind == "artifact_spec" {
			artifact = true
			a := f.Artifact
			if a == nil || !nonempty(a.Format, a.EntryPoint, a.Build, a.Run, a.Environment, a.Configuration, a.DataPolicy, a.Usage, a.Security, a.License) || !texts(a.Files) || !texts(a.Verification) {
				add("design_artifact", f.ID, "artifact", "Deliverable requires contents, build/run/environment and verification")
			}
		}
		if f.Kind == "requirement" && !required[f.ID] {
			add("design_scope_unlisted", f.ID, "scope", "A requirement cannot disappear from the full scope denominator")
		}
		if excluded[f.ID] {
			continue
		}
		if f.Kind == "requirement" || f.Kind == "module" || f.Kind == "interface" || f.Kind == "artifact_spec" || f.Kind == "constraint" {
			verified := false
			realized := f.Kind != "requirement"
			for _, r := range p.Shared.Relations {
				source := facts[r.From]
				confirmed := confirmedNormative[r.From]
				if r.Kind == "verifies" && r.To == f.ID && source.Kind == "verification" && source.Acceptance != nil && confirmed {
					verified = true
				}
				if r.Kind == "realizes" && r.To == f.ID && (source.Kind == "module" || source.Kind == "decision") && confirmed {
					realized = true
				}
			}
			if !verified {
				add("design_coverage", f.ID, "relations", "Missing typed, decidable verification coverage")
			}
			if !realized {
				add("design_realization", f.ID, "relations", "Requirement needs an actual design responsibility")
			}
		}
	}
	if !artifact {
		add("design_artifact_missing", "artifact", "facts", "Engineering delivery must define its final artifact")
	}
	knownObligations := map[string]bool{}
	for _, o := range p.Profile.Obligations {
		knownObligations[o.ID] = true
		expected := map[string]bool{}
		for _, m := range p.Members {
			if m.Role == o.Role {
				expected[m.Key] = false
			}
		}
		seen := map[string]bool{}
		count := 0
		for _, r := range p.Shared.Obligations {
			if r.ID != o.ID {
				continue
			}
			count++
			key := r.Member
			if seen[key] {
				add("design_obligation", o.ID, "content_obligations", "Duplicate obligation for the same role instance")
			}
			seen[key] = true
			if r.State == "NOT_APPLICABLE" {
				cond := p.Shared.Conditions[o.When]
				if o.When == "" || cond.Value == nil || cond.Status != "confirmed" || *cond.Value || !nonempty(r.Reason) {
					add("design_obligation", o.ID, "state", "Inapplicability needs a confirmed exclusion and rationale")
				}
				continue
			}
			if r.State != "DEFINED" || len(r.Facts) == 0 || !nonempty(r.Member, r.Section) {
				add("design_obligation", o.ID, "content_obligations", "Required content remains unresolved")
				continue
			}
			if _, ok := expected[r.Member]; !ok {
				add("design_obligation", o.ID, "member_key", "Obligation points to the wrong role instance")
			} else {
				expected[r.Member] = true
			}
			for _, id := range r.Facts {
				f, ok := facts[id]
				if !ok || f.Kind != o.Kind || f.Owner != r.Member || !confirmedNormative[id] {
					add("design_obligation", o.ID, "facts", "Required content needs a confirmed normative fact defined by its authority owner")
				}
				if o.ID == "OBL-RUNTIME-RECOVERY" && f.StateRule == nil || o.ID == "OBL-QUALITY" && f.Quality == nil {
					add("design_obligation", o.ID, "facts", "Runtime recovery and quality obligations require their corresponding typed contracts")
				}
			}
		}
		if count == 0 {
			add("design_obligation", o.ID, "content_obligations", "Required content remains unresolved")
		}
		for member, found := range expected {
			if !found {
				add("design_obligation", o.ID, "member_key."+member, "Required role instance has no defined content")
			}
		}
	}
	for _, r := range p.Shared.Obligations {
		if !knownObligations[r.ID] {
			add("design_obligation", r.ID, "content_obligations", "Unknown content obligation")
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// EngineeringSchema is shared by the planner and deterministic fixture tooling.
func EngineeringSchema(p Profile) any {
	if !Engineering(p) {
		return nil
	}
	return map[string]any{"fact": Fact{Acceptance: &AcceptanceContract{}, Quality: &QualityContract{}, StateRule: &StateContract{}, Artifact: &ArtifactContract{}}, "scope": DesignScope{}, "obligation": ObligationResolution{}, "change": FactChange{}, "historical_reference": FactReference{}}
}

func EpistemicStatus(s SharedDesign, f Fact) string {
	status := f.Status
	for _, q := range s.Questions {
		if q.Resolution != "" {
			continue
		}
		for _, id := range q.Facts {
			if id != f.ID {
				continue
			}
			if q.Kind == "conflict" {
				return "conflicted"
			}
			if q.Kind == "assumption" {
				status = "assumed"
			}
		}
	}
	return status
}

func ValidateHistory(s SharedDesign) error {
	history := map[FactReference]Fact{}
	for _, h := range s.History {
		if h.Ref.FactID != h.Fact.ID || h.Fact.Hash != h.Ref.Hash || !ValidKey(h.Ref.SetID) || !ValidKey(h.Ref.RevisionID) {
			return ErrInvalid
		}
		if _, ok := history[h.Ref]; ok {
			return ErrInvalid
		}
		history[h.Ref] = h.Fact
	}
	current := map[string]bool{}
	for _, f := range s.Facts {
		current[f.ID] = true
	}
	used := map[FactReference]bool{}
	for _, c := range s.Changes {
		if !nonempty(c.Reason, c.Coverage) || len(c.From) == 0 {
			return ErrInvalid
		}
		switch c.Action {
		case "split", "merge", "replace", "restore":
			if len(c.To) == 0 {
				return ErrInvalid
			}
		case "retire":
			if len(c.To) != 0 {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
		for _, ref := range c.From {
			if _, ok := history[ref]; !ok || used[ref] {
				return ErrInvalid
			}
			used[ref] = true
		}
		for _, id := range c.To {
			if !current[id] {
				return ErrInvalid
			}
			for _, old := range c.From {
				if id == old.FactID && c.Action != "restore" {
					return ErrInvalid
				}
			}
		}
		if c.Action == "split" && (len(c.From) != 1 || len(c.To) < 2) || c.Action == "merge" && (len(c.From) < 2 || len(c.To) != 1) {
			return ErrInvalid
		}
	}
	for _, r := range s.Relations {
		if r.Kind == "supersedes" {
			if r.Historical == nil || !used[*r.Historical] || !current[r.From] || r.From == r.Historical.FactID {
				return ErrInvalid
			}
		}
	}
	for ref := range history {
		if !used[ref] {
			return ErrInvalid
		}
	}
	return nil
}

func engineeringFieldType(t string) bool {
	if strings.HasSuffix(t, "[]") {
		return engineeringFieldType(strings.TrimSuffix(t, "[]"))
	}
	switch t {
	case "string", "integer", "int", "number", "float", "bool", "boolean", "object", "array", "bytes", "timestamp", "datetime", "uuid":
		return true
	}
	return false
}

func PublicBytesSafe(raw []byte) bool { return !secretPattern.Match(raw) }

type ImpactAnalysis struct {
	Facts          []string `json:"changed_fact_ids"`
	Regenerate     []string `json:"regenerate_members"`
	ReuseEligible  []string `json:"reuse_candidates"`
	ReviewRequired bool     `json:"new_review_required"`
	Reason         string   `json:"reason"`
}

func AnalyzeImpact(old, next Plan) ImpactAnalysis {
	out := ImpactAnalysis{Facts: Impact(old.Shared, next.Shared), Regenerate: []string{}, ReuseEligible: []string{}, ReviewRequired: true, Reason: "Changed normative facts and their dependency closure require fresh generation and review. Reuse candidates still require working-byte, resource and model checks."}
	changed := map[string]bool{}
	for _, id := range out.Facts {
		changed[id] = true
	}
	members := map[string]bool{}
	for _, m := range next.Members {
		facts, _ := ApplicableFacts(next, m)
		for _, f := range facts {
			if changed[f.ID] {
				members[m.Key] = true
			}
		}
		found := false
		for _, prior := range old.Members {
			if prior.Key == m.Key {
				found = true
			}
		}
		if !found || old.Profile.Hash != next.Profile.Hash {
			members[m.Key] = true
		}
	}
	progress := true
	for progress {
		progress = false
		for _, m := range next.Members {
			if members[m.Key] {
				continue
			}
			for _, dep := range m.DependsOn {
				if members[dep] {
					members[m.Key] = true
					progress = true
					break
				}
			}
		}
	}
	for _, m := range next.Members {
		if members[m.Key] {
			out.Regenerate = append(out.Regenerate, m.Key)
		} else {
			out.ReuseEligible = append(out.ReuseEligible, m.Key)
		}
	}
	return out
}
