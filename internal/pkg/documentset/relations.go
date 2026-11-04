package documentset

import "fmt"

// These rules are shared by planning guidance and the publication gate. A '*'
// endpoint means any existing fact; historical targets use fixed references.
type RelationRule struct {
	Kind             string   `json:"kind"`
	FromKinds        []string `json:"from_kinds"`
	ToKinds          []string `json:"to_kinds"`
	HistoricalTarget bool     `json:"historical_target,omitempty"`
	DisallowSelf     bool     `json:"disallow_self,omitempty"`
}

func EngineeringRelationRules() []RelationRule {
	return []RelationRule{
		{Kind: "realizes", FromKinds: []string{"module", "decision"}, ToKinds: []string{"requirement"}},
		{Kind: "constrains", FromKinds: []string{"constraint"}, ToKinds: []string{"interface", "module", "artifact_spec"}},
		{Kind: "uses_contract", FromKinds: []string{"module", "decision"}, ToKinds: []string{"interface", "entity"}},
		{Kind: "verifies", FromKinds: []string{"verification"}, ToKinds: []string{"requirement", "constraint", "interface", "artifact_spec", "module"}},
		{Kind: "depends_on", FromKinds: []string{"*"}, ToKinds: []string{"*"}, DisallowSelf: true},
		{Kind: "supersedes", FromKinds: []string{"*"}, ToKinds: []string{"*"}, HistoricalTarget: true},
	}
}

type RelationValidationError struct {
	RelationID, Kind, From, To, FromKind, ToKind, Reason string
}

func relationLabel(s string) string {
	if s == "" {
		return "<missing>"
	}
	if !ValidKey(s) {
		return "<invalid>"
	}
	return s
}

func (e RelationValidationError) Error() string {
	if e.Reason == "cyclic_prerequisite" {
		return fmt.Sprintf("%s: cyclic prerequisite relation (reason=%s)", ErrInvalid, e.Reason)
	}
	return fmt.Sprintf("%s: invalid typed relation %s (kind=%s; from=%s(%s); to=%s(%s); reason=%s)", ErrInvalid,
		relationLabel(e.RelationID), relationLabel(e.Kind), relationLabel(e.From), relationLabel(e.FromKind), relationLabel(e.To), relationLabel(e.ToKind), e.Reason)
}

func (e RelationValidationError) Unwrap() error { return ErrInvalid }

// ValidateEngineeringRelations never changes relation meaning, direction, IDs or
// coverage. Keep nonengineering/research interpretation outside this gate.
func ValidateEngineeringRelations(s SharedDesign) error {
	facts := map[string]Fact{}
	for _, fact := range s.Facts {
		facts[fact.ID] = fact
	}
	rules := map[string]RelationRule{}
	for _, rule := range EngineeringRelationRules() {
		rules[rule.Kind] = rule
	}
	matches := func(kinds []string, kind string) bool {
		for _, allowed := range kinds {
			if allowed == "*" || allowed == kind {
				return true
			}
		}
		return false
	}
	seen := map[string]bool{}
	for _, r := range s.Relations {
		a, b := facts[r.From], facts[r.To]
		rule, registered := rules[r.Kind]
		reason := ""
		switch {
		case !ValidKey(r.ID):
			reason = "invalid_relation_id"
		case seen[r.ID]:
			reason = "duplicate_relation_id"
		case !registered:
			reason = "unknown_kind"
		case a.ID == "":
			reason = "missing_from"
		case rule.HistoricalTarget:
			if r.Historical == nil || r.Historical.FactID != r.To {
				reason = "invalid_historical_target"
			}
		case b.ID == "":
			reason = "missing_to"
		case rule.DisallowSelf && a.ID == b.ID:
			reason = "self_dependency"
		case !matches(rule.FromKinds, a.Kind) || !matches(rule.ToKinds, b.Kind):
			reason = "endpoint_kind_mismatch"
		}
		if reason != "" {
			return RelationValidationError{RelationID: r.ID, Kind: r.Kind, From: r.From, To: r.To, FromKind: a.Kind, ToKind: b.Kind, Reason: reason}
		}
		seen[r.ID] = true
	}
	graph := map[string][]string{}
	for id := range facts {
		graph[id] = []string{}
	}
	for _, r := range s.Relations {
		if r.Kind == "depends_on" {
			graph[r.From] = append(graph[r.From], r.To)
		}
	}
	if !Acyclic(graph) {
		return RelationValidationError{Reason: "cyclic_prerequisite"}
	}
	return nil
}
