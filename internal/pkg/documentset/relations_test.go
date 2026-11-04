package documentset

import (
	"errors"
	"strings"
	"testing"
)

func TestEngineeringRelationTypesAndDirectionsRemainStrict(t *testing.T) {
	facts := []Fact{{ID: "REQ", Kind: "requirement"}, {ID: "MOD", Kind: "module"}, {ID: "DEC", Kind: "decision"}, {ID: "API", Kind: "interface"}, {ID: "ENT", Kind: "entity"}, {ID: "CON", Kind: "constraint"}, {ID: "VER", Kind: "verification"}, {ID: "ART", Kind: "artifact_spec"}}
	for _, tc := range []struct{ kind, from, to, reason string }{
		{"realizes", "MOD", "REQ", ""}, {"realizes", "DEC", "REQ", ""},
		{"realizes", "REQ", "MOD", "endpoint_kind_mismatch"},
		{"constrains", "CON", "API", ""}, {"constrains", "CON", "ART", ""}, {"constrains", "CON", "ENT", "endpoint_kind_mismatch"},
		{"uses_contract", "MOD", "ENT", ""}, {"uses_contract", "DEC", "API", ""}, {"uses_contract", "API", "ENT", "endpoint_kind_mismatch"},
		{"verifies", "VER", "REQ", ""}, {"verifies", "VER", "MOD", ""}, {"verifies", "REQ", "VER", "endpoint_kind_mismatch"}, {"verifies", "VER", "ENT", "endpoint_kind_mismatch"},
		{"implements", "MOD", "REQ", "unknown_kind"}, {"PRIVATE INVALID KIND", "MOD", "REQ", "unknown_kind"},
		{"realizes", "absent", "REQ", "missing_from"}, {"realizes", "MOD", "absent", "missing_to"},
		{"depends_on", "MOD", "REQ", ""}, {"depends_on", "MOD", "MOD", "self_dependency"},
		{"supersedes", "MOD", "OLD", "invalid_historical_target"},
	} {
		t.Run(tc.kind+"/"+tc.from+"/"+tc.to, func(t *testing.T) {
			relation := Relation{ID: "REL-003", Kind: tc.kind, From: tc.from, To: tc.to}
			shared := SharedDesign{Facts: facts, Relations: []Relation{relation}}
			err := ValidateEngineeringRelations(shared)
			if tc.reason == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			var failure RelationValidationError
			if !errors.Is(err, ErrInvalid) || !errors.As(err, &failure) || failure.Reason != tc.reason || failure.RelationID != "REL-003" || shared.Relations[0] != relation {
				t.Fatal("changed meaning or lost diagnosis", err)
			}
			if strings.Contains(err.Error(), "PRIVATE INVALID KIND") {
				t.Fatal("raw model text leaked", err)
			}
		})
	}
	shared := SharedDesign{Facts: facts, Relations: []Relation{{ID: "REL-003", Kind: "supersedes", From: "MOD", To: "OLD", Historical: &FactReference{FactID: "OLD"}}}}
	if err := ValidateEngineeringRelations(shared); err != nil {
		t.Fatal("fixed historical target rejected", err)
	}
	shared.Relations = append(shared.Relations, shared.Relations[0])
	var duplicate RelationValidationError
	if err := ValidateEngineeringRelations(shared); !errors.As(err, &duplicate) || duplicate.Reason != "duplicate_relation_id" {
		t.Fatal("duplicate accepted", err)
	}
	shared.Relations = []Relation{{ID: "A", Kind: "depends_on", From: "MOD", To: "REQ"}, {ID: "B", Kind: "depends_on", From: "REQ", To: "MOD"}}
	var cycle RelationValidationError
	if err := ValidateEngineeringRelations(shared); !errors.As(err, &cycle) || cycle.Reason != "cyclic_prerequisite" {
		t.Fatal("cycle accepted", err)
	}
}

func TestRelationRegistryExposesEveryAllowedPairWithoutMutableAuthority(t *testing.T) {
	rules := EngineeringRelationRules()
	for _, rule := range rules {
		if rule.HistoricalTarget {
			continue
		}
		for _, from := range rule.FromKinds {
			for _, to := range rule.ToKinds {
				if from == "*" {
					from = "module"
				}
				if to == "*" {
					to = "requirement"
				}
				s := SharedDesign{Facts: []Fact{{ID: "FROM", Kind: from}, {ID: "TO", Kind: to}}, Relations: []Relation{{ID: "REL", Kind: rule.Kind, From: "FROM", To: "TO"}}}
				if err := ValidateEngineeringRelations(s); err != nil {
					t.Fatal("advertised pair rejected", rule, err)
				}
			}
		}
	}
	rules[0].Kind = "unregistered"
	rules[1].ToKinds[0] = "entity"
	s := SharedDesign{Facts: []Fact{{ID: "FROM", Kind: "constraint"}, {ID: "TO", Kind: "entity"}}, Relations: []Relation{{ID: "REL", Kind: "constrains", From: "FROM", To: "TO"}}}
	if err := ValidateEngineeringRelations(s); err == nil {
		t.Fatal("caller changed authoritative relation rules")
	}
}
