package documentset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"path"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
)

var keyPattern = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_.:-]{0,95}$`)

func ValidKey(s string) bool       { return keyPattern.MatchString(s) }
func ProfileHash(p Profile) string { p.Hash = ""; return documentstore.HashJSON(p) }
func ParseProfile(raw []byte, registry *documentprofile.Registry) (Profile, error) {
	var p Profile
	if len(raw) > 256<<10 {
		return p, ErrInvalid
	}
	d := yaml.NewDecoder(bytes.NewReader(raw))
	d.KnownFields(true)
	if e := d.Decode(&p); e != nil {
		return p, fmt.Errorf("%w: %v", ErrInvalid, e)
	}
	var trailing any
	if e := d.Decode(&trailing); e != io.EOF {
		return p, ErrInvalid
	}
	for i := range p.Roles {
		r := &p.Roles[i]
		if registry != nil && r.ProfileSnapshot == "" {
			v, e := registry.Resolve(r.ProfileRef)
			if e != nil {
				return p, e
			}
			r.ProfileRef = v.Ref
			r.ProfileSnapshot = v.Snapshot
		}
	}
	p.Hash = ProfileHash(p)
	return p, ValidateProfile(p)
}
func ValidateProfile(p Profile) error {
	if p.SchemaVersion != 1 || p.Kind != "document_set" || !ValidKey(p.ID) || p.Version < 1 || strings.TrimSpace(p.Name) == "" || len(p.Roles) == 0 || len(p.Roles) > 20 || p.Hash != ProfileHash(p) {
		return ErrInvalid
	}
	l := p.Limits
	if l.MaxInputTokens < 1 || l.MaxInputTokens > 8<<20 || l.MaxOutputTokens < 1 || l.MaxOutputTokens > 16<<20 || l.MaxContextBytes < 1 || l.MaxContextBytes > 1<<20 {
		return ErrBudget
	}
	if l.MaxMembers < 1 || l.MaxMembers > 50 || l.MaxRepairRounds < 0 || l.MaxRepairRounds > 2 || l.MaxParallel < 1 || l.MaxParallel > 3 || l.MaxCalls < 1 || l.MaxCalls > 100 || l.MaxInputBytes < 1 || l.MaxInputBytes > 8<<20 || l.MaxOutputBytes < 1 || l.MaxOutputBytes > 16<<20 || l.WallSeconds < 1 || l.WallSeconds > 3600 {
		return ErrBudget
	}
	roles := map[string]bool{}
	graph := map[string][]string{}
	for _, r := range p.Roles {
		if !ValidKey(r.Key) || roles[r.Key] || r.Required == nil || (r.Cardinality != "one" && r.Cardinality != "per_confirmed_module") {
			return ErrInvalid
		}
		if r.When != "" && r.When != "has_interfaces" && r.When != "has_persistence" {
			return ErrInvalid
		}
		single, e := documentprofile.Parse([]byte(r.ProfileSnapshot), documentprofile.Limits{MaxFormatBytes: 65536, MaxSections: 30})
		if e != nil || single.Ref != r.ProfileRef || r.ProfileRef.ContentHash == "" {
			return fmt.Errorf("%w: unresolved role %s", ErrInvalid, r.Key)
		}
		roles[r.Key] = true
		graph[r.Key] = r.DependsOn
	}
	if !Acyclic(graph) {
		return fmt.Errorf("%w: generation DAG cycle or unknown dependency", ErrInvalid)
	}
	checks := map[string]bool{}
	for _, c := range p.Checks {
		switch c {
		case "required_scope_coverage", "unique_contract_owner", "interface_entity_compatibility", "resolved_member_links", "cross_document_semantic_review":
		default:
			return ErrInvalid
		}
		if checks[c] {
			return ErrInvalid
		}
		checks[c] = true
	}
	for _, c := range []string{"required_scope_coverage", "unique_contract_owner", "interface_entity_compatibility", "resolved_member_links", "cross_document_semantic_review"} {
		if !checks[c] {
			return ErrInvalid
		}
	}
	seen := map[string]bool{}
	for _, in := range p.Inputs {
		if !ValidKey(in.Key) || seen[in.Key] {
			return ErrInvalid
		}
		seen[in.Key] = true
	}
	return nil
}

// Acyclic applies to generation dependencies, never to the real module graph.
func Acyclic(graph map[string][]string) bool {
	state := map[string]int{}
	var visit func(string) bool
	visit = func(k string) bool {
		if state[k] == 1 {
			return false
		}
		if state[k] == 2 {
			return true
		}
		deps, ok := graph[k]
		if !ok {
			return false
		}
		state[k] = 1
		for _, v := range deps {
			if !visit(v) {
				return false
			}
		}
		state[k] = 2
		return true
	}
	for k := range graph {
		if !visit(k) {
			return false
		}
	}
	return true
}
func Builtins() []Profile {
	makeRole := func(key, name string, card string, when string, deps []string) Role {
		raw := fmt.Sprintf("---\nschema_version: 2\nid: builtin.set-%s\nversion: 1\nname: %s\nfamily: other\ndescription: %s role for a coordinated document set\nenabled: true\nvalidation:\n  heading_level: 2\n  section_headings: {scope: Scope, design: Design, verification: Verification}\n  minimum_body_chars: 20\n  maximum_body_chars: 50000\n  require_non_empty_sections: true\n---\n# {{title}}\n\n## Scope\n{{section:scope|required}}\n\n## Design\n{{section:design|required}}\n\n## Verification\n{{section:verification|required}}\n", key, name, name)
		p, e := documentprofile.Parse([]byte(raw), documentprofile.Limits{})
		if e != nil {
			panic(e)
		}
		required := true
		return Role{Key: key, Cardinality: card, Required: &required, When: when, ProfileRef: p.Ref, ProfileSnapshot: raw, DependsOn: deps}
	}
	limits := Limits{MaxMembers: 20, MaxRepairRounds: 2, MaxParallel: 3, MaxCalls: 60, MaxInputTokens: 2 << 20, MaxOutputTokens: 4 << 20, MaxContextBytes: 256 << 10, MaxInputBytes: 2 << 20, MaxOutputBytes: 4 << 20, WallSeconds: 900}
	checks := []string{"required_scope_coverage", "unique_contract_owner", "interface_entity_compatibility", "resolved_member_links", "cross_document_semantic_review"}
	software := Profile{SchemaVersion: 1, Kind: "document_set", ID: "builtin.feature-design-set", Version: 1, Name: "功能设计成果集", Enabled: true, Inputs: []documentprofile.ProfileInput{{Key: "goal", Required: true}, {Key: "scope", Required: true}, {Key: "constraints", Required: true}}, Limits: limits, Checks: checks, Roles: []Role{
		makeRole("architecture", "Architecture", "one", "", nil), makeRole("module", "Module", "per_confirmed_module", "", []string{"architecture"}), makeRole("interfaces", "Interfaces", "one", "has_interfaces", []string{"architecture"}), makeRole("storage", "Storage", "one", "has_persistence", []string{"architecture"}), makeRole("verification", "Verification", "one", "", []string{"module"})}}
	research := Profile{SchemaVersion: 1, Kind: "document_set", ID: "builtin.research-set", Version: 1, Name: "研究成果集", Enabled: true, Inputs: []documentprofile.ProfileInput{{Key: "goal", Required: true}, {Key: "scope", Required: true}}, Limits: limits, Checks: checks, Roles: []Role{makeRole("brief", "Research brief", "one", "", nil), makeRole("analysis", "Evidence analysis", "one", "", []string{"brief"}), makeRole("recommendations", "Recommendations", "one", "", []string{"analysis"})}}
	software.Hash = ProfileHash(software)
	research.Hash = ProfileHash(research)
	return []Profile{software, research}
}
func FreezeShared(s SharedDesign, capture CaptureSnapshot) (SharedDesign, error) {
	if len(s.Facts) > 1000 || len(s.Relations) > 2000 || len(s.Questions) > 100 {
		return s, ErrBudget
	}
	// Freeze must own its slices and maps. Removing quotations for the public
	// plan must never erase the private evidence used by a later plan.
	raw, err := json.Marshal(s)
	if err != nil {
		return s, ErrInvalid
	}
	var frozen SharedDesign
	if err := json.Unmarshal(raw, &frozen); err != nil {
		return s, ErrInvalid
	}
	s = frozen
	seen := map[string]Fact{}
	for i := range s.Facts {
		f := &s.Facts[i]
		if !ValidKey(f.ID) || seen[f.ID].ID != "" || strings.TrimSpace(f.Name) == "" || strings.TrimSpace(f.Content) == "" || len(f.Content) > 16000 {
			return s, ErrInvalid
		}
		switch f.Kind {
		case "requirement", "term", "module", "interface", "entity", "decision", "constraint", "verification":
		default:
			return s, ErrInvalid
		}
		if f.Status != "confirmed" && f.Status != "proposed" && f.Status != "unresolved" {
			return s, ErrInvalid
		}
		if f.Status == "confirmed" {
			n := f.Source.Message
			if n < 0 || n >= len(capture.Messages) || capture.Messages[n].Role != "user" || strings.TrimSpace(f.Source.Quote) == "" || !strings.Contains(capture.Messages[n].Text, f.Source.Quote) {
				return s, fmt.Errorf("%w: fact %s lacks user evidence", ErrInvalid, f.ID)
			}
		}
		fieldIDs := map[string]bool{}
		for _, v := range f.Fields {
			if !ValidKey(v.ID) || v.Type == "" || fieldIDs[v.ID] || (v.Classification != "persisted" && v.Classification != "computed" && v.Classification != "external") {
				return s, ErrInvalid
			}
			fieldIDs[v.ID] = true
		}
		f.Hash = ""
		f.Hash = documentstore.HashJSON(*f)
		seen[f.ID] = *f
	}
	for _, r := range s.Relations {
		if !ValidKey(r.ID) || seen[r.From].ID == "" || seen[r.To].ID == "" || r.Kind == "" {
			return s, ErrInvalid
		}
	}
	for k := range s.Conditions {
		if k != "has_interfaces" && k != "has_persistence" {
			return s, ErrInvalid
		}
	}
	s.Hash = ""
	s.RevisionID = ""
	s.RevisionID = "sd-" + strings.TrimPrefix(documentstore.HashJSON(s), "sha256:")[:24]
	s.Hash = documentstore.HashJSON(s)
	return s, nil
}
func PlanHash(p Plan) string { p.Hash = ""; return documentstore.HashJSON(p) }

// BuildPlan is deterministic: the model proposes facts, never paths or identities.
func BuildPlan(id string, p Profile, c CaptureSnapshot, shared SharedDesign, base, catalog uint64) (Plan, error) {
	out := Plan{SetID: id, Profile: p, Capture: c, BaseGeneration: base, CatalogGeneration: catalog, Baselines: map[string]string{}, LocationBaselines: map[string]string{}, Omissions: map[string]string{}}
	if !documentstore.ValidID(id) {
		return out, ErrInvalid
	}
	if e := ValidateProfile(p); e != nil {
		return out, e
	}
	var e error
	out.Shared, e = FreezeShared(shared, c)
	if e != nil {
		return out, e
	}
	for _, f := range shared.Facts {
		if f.Status != "confirmed" {
			out.Shared.Questions = append(out.Shared.Questions, Question{ID: "fact-" + f.ID, Message: "Confirm shared fact: " + f.Name, Blocking: true, Members: []string{f.Owner}})
		}
	}
	for _, in := range p.Inputs {
		if in.Required && strings.TrimSpace(shared.Inputs[in.Key]) == "" {
			out.Shared.Questions = append(out.Shared.Questions, Question{ID: "input-" + in.Key, Message: "Missing input: " + in.Key, Blocking: true})
		}
	}
	modules := []Fact{}
	for _, f := range shared.Facts {
		if f.Kind == "module" && f.Status == "confirmed" {
			modules = append(modules, f)
		}
	}
	sort.Slice(modules, func(i, j int) bool { return modules[i].ID < modules[j].ID })
	for _, role := range p.Roles {
		if role.When != "" {
			v, ok := shared.Conditions[role.When]
			if !ok || v == nil {
				out.Shared.Questions = append(out.Shared.Questions, Question{ID: "condition-" + role.When, Message: "Please confirm " + role.When, Blocking: true})
				continue
			}
			if !*v {
				out.Omissions[role.Key] = "Confirmed " + role.When + "=false"
				continue
			}
		}
		instances := []string{""}
		if role.Cardinality == "per_confirmed_module" {
			instances = nil
			for _, m := range modules {
				instances = append(instances, m.ID)
			}
			if len(instances) == 0 && *role.Required {
				out.Shared.Questions = append(out.Shared.Questions, Question{ID: "modules", Message: "Confirm module boundaries", Blocking: true})
			}
		}
		for _, instance := range instances {
			key := role.Key
			if instance != "" {
				key += ":" + instance
			}
			digest := strings.TrimPrefix(documentstore.Hash([]byte(id+"/"+key)), "sha256:")[:24]
			title := role.ProfileRef.ProfileID
			if single, err := documentprofile.Parse([]byte(role.ProfileSnapshot), documentprofile.Limits{}); err == nil {
				title = single.Name
			}
			if instance != "" {
				for _, module := range modules {
					if module.ID == instance {
						title = module.Name
					}
				}
			}
			m := PlanMember{Key: key, Role: role.Key, Title: title, Filename: strings.ReplaceAll(key, ":", "-") + ".md", DocumentID: "ds-" + digest, Required: *role.Required, ProfileRef: role.ProfileRef, ProfileSnapshot: role.ProfileSnapshot}
			for _, f := range shared.Facts {
				if f.Owner == key || f.Kind == "term" || f.Kind == "constraint" || (f.Kind == "module" && f.ID == instance) {
					m.Covers = append(m.Covers, f.ID)
				}
			}
			out.Members = append(out.Members, m)
		}
	}
	if len(out.Members) > p.Limits.MaxMembers {
		return out, ErrBudget
	}
	for i, m := range out.Members {
		var deps []string
		for _, role := range p.Roles {
			if role.Key == m.Role {
				for _, d := range role.DependsOn {
					for _, other := range out.Members {
						if other.Role == d {
							deps = append(deps, other.Key)
						}
					}
				}
			}
		}
		out.Members[i].DependsOn = deps
	}
	// Raw conversation and exact source quotations remain private; hashes and
	// message positions preserve provenance without including them in Git.
	out.Capture.MessageCount = len(out.Capture.Messages)
	out.Capture.Messages = nil
	out.Capture.ConfirmedFacts = nil
	for i := range out.Shared.Facts {
		out.Shared.Facts[i].Source.Quote = ""
	}
	out.Shared.Hash = ""
	out.Shared.Hash = documentstore.HashJSON(out.Shared)
	return out, nil
}
func ValidatePlan(p Plan) error {
	if !documentstore.ValidID(p.ID) || !documentstore.ValidID(p.SetID) || p.Hash != PlanHash(p) || len(p.Members) > p.Profile.Limits.MaxMembers {
		return ErrInvalid
	}
	if e := ValidateProfile(p.Profile); e != nil {
		return e
	}
	graph := map[string][]string{}
	ids := map[string]bool{}
	for _, m := range p.Members {
		if !ValidKey(m.Key) || graph[m.Key] != nil || !documentstore.ValidID(m.DocumentID) || ids[m.DocumentID] || path.Base(m.Filename) != m.Filename || !strings.HasSuffix(m.Filename, ".md") {
			return ErrInvalid
		}
		ids[m.DocumentID] = true
		graph[m.Key] = append([]string{}, m.DependsOn...)
	}
	if !Acyclic(graph) {
		return ErrInvalid
	}
	return validateMembership(p)
}
func StrictJSON(raw []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	var extra any
	if d.Decode(&extra) != io.EOF {
		return ErrInvalid
	}
	return nil
}

// validateMembership recomputes the declared output set. Deleting a required
// role from a serialized plan must not turn an incomplete package into ready.
func validateMembership(p Plan) error {
	expected := map[string]Role{}
	omitted := map[string]string{}
	questions := map[string]bool{}
	for _, q := range p.Shared.Questions {
		if q.Blocking {
			questions[q.ID] = true
		}
	}
	for _, role := range p.Profile.Roles {
		if role.When != "" {
			condition := p.Shared.Conditions[role.When]
			if condition == nil {
				if !questions["condition-"+role.When] {
					return ErrInvalid
				}
				continue
			}
			if !*condition {
				omitted[role.Key] = "Confirmed " + role.When + "=false"
				continue
			}
		}
		if role.Cardinality == "one" {
			expected[role.Key] = role
			continue
		}
		count := 0
		for _, f := range p.Shared.Facts {
			if f.Kind == "module" && f.Status == "confirmed" {
				expected[role.Key+":"+f.ID] = role
				count++
			}
		}
		if count == 0 && *role.Required && !questions["modules"] {
			return ErrInvalid
		}
	}
	if len(expected) != len(p.Members) || documentstore.HashJSON(omitted) != documentstore.HashJSON(p.Omissions) {
		return ErrInvalid
	}
	filenames := map[string]bool{}
	for _, m := range p.Members {
		role, ok := expected[m.Key]
		if !ok || role.Key != m.Role || m.Required != *role.Required || m.ProfileRef != role.ProfileRef || m.ProfileSnapshot != role.ProfileSnapshot || filenames[m.Filename] {
			return ErrInvalid
		}
		filenames[m.Filename] = true
		digest := strings.TrimPrefix(documentstore.Hash([]byte(p.SetID+"/"+m.Key)), "sha256:")[:24]
		if m.DocumentID != "ds-"+digest || m.Filename != strings.ReplaceAll(m.Key, ":", "-")+".md" {
			return ErrInvalid
		}
		covers := []string{}
		for _, f := range p.Shared.Facts {
			if f.Owner == m.Key || f.Kind == "term" || f.Kind == "constraint" || (f.Kind == "module" && m.Key == role.Key+":"+f.ID) {
				covers = append(covers, f.ID)
			}
		}
		if strings.Join(covers, "\x00") != strings.Join(m.Covers, "\x00") {
			return ErrInvalid
		}
		deps := []string{}
		for _, dependencyRole := range role.DependsOn {
			for _, other := range p.Members {
				if other.Role == dependencyRole {
					deps = append(deps, other.Key)
				}
			}
		}
		if strings.Join(deps, "\x00") != strings.Join(m.DependsOn, "\x00") {
			return ErrInvalid
		}
	}
	return nil
}
