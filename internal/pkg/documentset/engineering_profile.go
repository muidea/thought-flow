package documentset

import (
	"encoding/json"
	"fmt"
	"strings"
	"thoughtflow/internal/pkg/documentprofile"
)

func engineeringProfile(base Profile) Profile {
	raw, _ := json.Marshal(base)
	var p Profile
	json.Unmarshal(raw, &p)
	p.ID = "builtin.engineering-design-set"
	p.Name = "工程设计与交付成果集"
	p.Purpose = EngineeringPurpose
	p.GenerationVersion = EngineeringPromptVersion
	// These sections are questions the generator must answer, not filler text.
	for i := range p.Roles {
		r := &p.Roles[i]
		instructions := engineeringRoleInstructions(r.Key)
		raw := fmt.Sprintf("---\nschema_version: 2\nid: builtin.engineering-%s\nversion: 1\nname: Engineering %s\nfamily: other\ndescription: Define observable behavior, contracts, failures and evidence for this engineering role; never substitute generic filler.\nenabled: true\ngeneration:\n  additional_instructions: %s\nvalidation:\n  heading_level: 2\n  section_headings: {scope: Scope, behavior: Behavior, design: Design, failures: Failure and recovery, verification: Verification}\n  minimum_body_chars: 100\n  maximum_body_chars: 100000\n  require_non_empty_sections: true\n---\n# {{title}}\n\n## Scope\n{{section:scope|required}}\n\n## Behavior\n{{section:behavior|required}}\n\n## Design\n{{section:design|required}}\n\n## Failure and recovery\n{{section:failures|required}}\n\n## Verification\n{{section:verification|required}}\n", r.Key, r.Key, instructions)
		single, e := documentprofile.Parse([]byte(raw), documentprofile.Limits{})
		if e != nil {
			panic(e)
		}
		r.ProfileRef = single.Ref
		if r.Key == "module" {
			r.DependsOn = []string{"architecture", "interfaces", "storage"}
		}
		if r.Key == "verification" {
			r.DependsOn = []string{"architecture", "module", "interfaces", "storage"}
		}
		r.ProfileSnapshot = raw
	}
	delivery := p.Roles[len(p.Roles)-1]
	delivery.Key = "delivery"
	delivery.DependsOn = []string{"verification"}
	delivery.When = ""
	delivery.Cardinality = "one"
	delivery.ProfileSnapshot = strings.ReplaceAll(delivery.ProfileSnapshot, "engineering-verification", "engineering-delivery")
	delivery.ProfileSnapshot = strings.ReplaceAll(delivery.ProfileSnapshot, engineeringRoleInstructions("verification"), engineeringRoleInstructions("delivery"))
	single, e := documentprofile.Parse([]byte(delivery.ProfileSnapshot), documentprofile.Limits{})
	if e != nil {
		panic(e)
	}
	delivery.ProfileRef = single.Ref
	p.Roles = append(p.Roles, delivery)
	p.Obligations = []ContentObligation{
		{ID: "OBL-SCOPE", Role: "architecture", Kind: "requirement"},
		{ID: "OBL-DESIGN", Role: "architecture", Kind: "decision"},
		{ID: "OBL-INTERFACES", Role: "interfaces", Kind: "interface", When: "has_interfaces"},
		{ID: "OBL-DATA", Role: "storage", Kind: "entity", When: "has_persistence"},
		{ID: "OBL-VERIFICATION", Role: "verification", Kind: "verification"},
		{ID: "OBL-BEHAVIOR", Role: "module", Kind: "module"},
		{ID: "OBL-RUNTIME-RECOVERY", Role: "architecture", Kind: "constraint"},
		{ID: "OBL-QUALITY", Role: "verification", Kind: "constraint"},
		{ID: "OBL-ARTIFACT", Role: "delivery", Kind: "artifact_spec"},
	}
	p.Hash = ProfileHash(p)
	return p
}

func validateEngineeringApprovals(s SharedDesign, c CaptureSnapshot) error {
	if s.Scope != nil {
		if !UserEvidence(c, s.Scope.Source) {
			return fmt.Errorf("%w: scope requires user evidence", ErrInvalid)
		}
		for _, x := range s.Scope.Excluded {
			if !UserEvidence(c, x.Source) {
				return ErrInvalid
			}
		}
	}
	for _, o := range s.Obligations {
		if o.State != "DEFINED" && o.State != "NOT_APPLICABLE" && o.State != "UNRESOLVED" {
			return ErrInvalid
		}
		if o.State == "NOT_APPLICABLE" && (!UserEvidence(c, o.Source) || !nonempty(o.Reason)) {
			return fmt.Errorf("%w: applicability requires user evidence", ErrInvalid)
		}
	}
	for _, q := range s.Questions {
		if !ValidKey(q.ID) || (q.Kind != "assumption" && q.Kind != "conflict" && q.Kind != "question") {
			return ErrInvalid
		}
		if q.Resolution != "" && (q.Source == nil || !UserEvidence(c, *q.Source)) {
			return fmt.Errorf("%w: issue closure needs user evidence", ErrInvalid)
		}
	}
	for _, x := range s.Changes {
		if (x.Action != "split" && x.Action != "merge" && x.Action != "replace" && x.Action != "retire" && x.Action != "restore") || len(x.From) == 0 || !UserEvidence(c, x.Source) || !nonempty(x.Reason, x.Coverage) {
			return ErrInvalid
		}
		if x.Action == "retire" && len(x.To) != 0 || x.Action != "retire" && len(x.To) == 0 {
			return ErrInvalid
		}
	}
	return nil
}

func engineeringRoleInstructions(role string) string {
	switch role {
	case "architecture":
		return "Define users, success conditions, complete scope and exclusions, module responsibilities, state ownership, forbidden dependencies, quality and recovery decisions. Explain confirmed requirements without inventing new requirements."
	case "module":
		return "Define preconditions, inputs, main flow, outputs, exceptions, boundaries, state transitions, concurrency, idempotence and recovery. Apply upstream API and storage contracts exactly and map each responsibility to verification."
	case "interfaces":
		return "Define operations, input and output fields, types and nullability, validation, authentication, permissions, error codes, idempotence and compatibility. Keep persisted fields consistent with authoritative entities."
	case "storage":
		return "Define entity fields, constraints, atomic transaction boundaries, indexes, ownership, retention, deletion, concurrency and interruption recovery. Include concrete failure outcomes."
	case "verification":
		return "Define test preconditions, stimulus, observable outcomes, pass rules, methods and required evidence for every requirement and applicable constraint. Include negative, boundary and recovery cases; explain approved inapplicability. Never claim tests ran."
	case "delivery":
		return "Define artifact format and complete file list, entry point, build and run commands, environment and dependencies, configuration, initialization and data policy, usage, security, license and verification. Describe expected artifacts without claiming they exist."
	}
	return "Explain the frozen engineering contract and identify all remaining gaps."
}
