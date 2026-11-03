package documentset

import (
	"bytes"
	"encoding/json"
	"fmt"
	"html"
	"net/url"
	"path"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	"github.com/yuin/goldmark/parser"
	"thoughtflow/internal/pkg/documentprofile"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/objectlocation"
)

const PublicProjectionVersion = "engineering-public-v1"
const PublicConversionVersion = "engineering-markdown-html-v1"

type PublicFact struct {
	ID         string              `json:"id"`
	Kind       string              `json:"kind"`
	Name       string              `json:"name"`
	Content    string              `json:"content"`
	Status     string              `json:"knowledge_status"`
	Epistemic  string              `json:"epistemic_status"`
	Modality   string              `json:"modality"`
	Provenance []Provenance        `json:"provenance"`
	Scope      []string            `json:"scope_refs"`
	Optional   bool                `json:"non_normative"`
	Owner      string              `json:"owner"`
	Fields     []Field             `json:"fields"`
	Acceptance *AcceptanceContract `json:"acceptance"`
	Quality    *QualityContract    `json:"quality"`
	State      *StateContract      `json:"state_rule"`
	Artifact   *ArtifactContract   `json:"artifact"`
}

func projectFact(s SharedDesign, f Fact) PublicFact {
	return PublicFact{f.ID, f.Kind, f.Name, f.Content, f.Status, EpistemicStatus(s, f), f.Modality, f.Provenance, f.ScopeRefs, f.NonNormative, f.Owner, f.Fields, f.Acceptance, f.Quality, f.StateRule, f.Artifact}
}
func (f PublicFact) fact() Fact {
	return Fact{ID: f.ID, Kind: f.Kind, Name: f.Name, Content: f.Content, Status: f.Epistemic, Modality: f.Modality, Provenance: f.Provenance, ScopeRefs: f.Scope, NonNormative: f.Optional, Owner: f.Owner, Fields: f.Fields, Acceptance: f.Acceptance, Quality: f.Quality, StateRule: f.State, Artifact: f.Artifact}
}

type PublicScope struct {
	ID           string            `json:"id"`
	Requirements []string          `json:"requirement_refs"`
	Constraints  []string          `json:"global_constraint_refs"`
	Exclusions   []PublicExclusion `json:"exclusions"`
}
type PublicExclusion struct {
	Fact   string `json:"fact_id"`
	Reason string `json:"reason"`
}
type PublicObligation struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Facts   []string `json:"facts"`
	Member  string   `json:"member_key"`
	Section string   `json:"section"`
	Reason  string   `json:"reason"`
}
type PublicChange struct {
	Action   string          `json:"action"`
	From     []FactReference `json:"from"`
	To       []string        `json:"to"`
	Reason   string          `json:"reason"`
	Coverage string          `json:"replacement_coverage"`
}
type PublicHistory struct {
	Ref  FactReference `json:"source_reference"`
	Fact PublicFact    `json:"fact"`
	Hash string        `json:"public_fact_hash"`
}
type PublicFile struct {
	Path string `json:"path"`
	Size int    `json:"size"`
	Hash string `json:"hash"`
}
type PublicMember struct {
	Key      string            `json:"member_key"`
	Role     string            `json:"role"`
	Title    string            `json:"title"`
	Filename string            `json:"filename"`
	Revision string            `json:"revision_id"`
	Body     string            `json:"canonical_body"`
	Headings map[string]string `json:"section_headings"`
	Covers   []string          `json:"covers"`
}
type PublicResource struct {
	Name string `json:"name"`
	ID   string `json:"resource_id"`
	Path string `json:"path"`
	Hash string `json:"hash"`
	Size int    `json:"size"`
}
type PublicDescriptor struct {
	Schema           int                 `json:"schema_version"`
	CanonicalVersion string              `json:"canonical_json"`
	Projection       string              `json:"projection_version"`
	Conversion       string              `json:"conversion_version"`
	Validator        string              `json:"validator_version"`
	Set              string              `json:"set_id"`
	Revision         string              `json:"set_revision_id"`
	ProfileID        string              `json:"profile_id"`
	ProfileVersion   int                 `json:"profile_version"`
	ProfileHash      string              `json:"profile_hash"`
	Obligations      []ContentObligation `json:"profile_obligations"`
	Scope            PublicScope         `json:"scope"`
	Facts            []PublicFact        `json:"facts"`
	Relations        []Relation          `json:"relations"`
	Resolutions      []PublicObligation  `json:"content_obligations"`
	Changes          []PublicChange      `json:"fact_changes"`
	History          []PublicHistory     `json:"historical_facts"`
	Members          []PublicMember      `json:"members"`
	Resources        []PublicResource    `json:"resources"`
	Files            []PublicFile        `json:"files"`
}

var secretPattern = regexp.MustCompile(`(?i)(-----BEGIN .*PRIVATE KEY-----|\b(?:api[_-]?key|password|access[_-]?token|secret)\s*[:=]\s*["']?[^\s"']{6,}|\bBearer\s+[A-Za-z0-9._-]{8,}|/(?:home|Users|root)/[^\s]+)`)

func PublicTextSafe(s string) bool {
	if !utf8.ValidString(s) || strings.ContainsRune(s, 0) || secretPattern.MatchString(s) {
		return false
	}
	for _, l := range Links(s) {
		if documentvalidation.SensitiveLocator(l) {
			return false
		}
		u, e := url.Parse(l)
		if e != nil || u.Scheme == "file" {
			return false
		}
	}
	return true
}
func PublicPayload(r Revision) (PublicDescriptor, map[string][]byte, error) {
	d := PublicDescriptor{Schema: 1, CanonicalVersion: PublicJSONVersion, Projection: PublicProjectionVersion, Conversion: PublicConversionVersion, Validator: ValidatorVersion, Set: r.Plan.SetID, Revision: r.ID, ProfileID: r.Plan.Profile.ID, ProfileVersion: r.Plan.Profile.Version, ProfileHash: r.Plan.Profile.Hash, Obligations: r.Plan.Profile.Obligations, Facts: []PublicFact{}, Relations: r.Plan.Shared.Relations, Resolutions: []PublicObligation{}, Changes: []PublicChange{}, History: []PublicHistory{}, Members: []PublicMember{}, Resources: []PublicResource{}, Files: []PublicFile{}}
	if !Engineering(r.Plan.Profile) || r.Plan.Shared.Scope == nil {
		return d, nil, ErrRules
	}
	sc := r.Plan.Shared.Scope
	d.Scope = PublicScope{ID: sc.ID, Requirements: sc.Requirements, Constraints: sc.GlobalConstraints, Exclusions: []PublicExclusion{}}
	for _, x := range sc.Excluded {
		d.Scope.Exclusions = append(d.Scope.Exclusions, PublicExclusion{x.FactID, x.Reason})
	}
	for _, f := range r.Plan.Shared.Facts {
		d.Facts = append(d.Facts, projectFact(r.Plan.Shared, f))
	}
	for _, o := range r.Plan.Shared.Obligations {
		d.Resolutions = append(d.Resolutions, PublicObligation{o.ID, o.State, o.Facts, o.Member, o.Section, o.Reason})
	}
	for _, c := range r.Plan.Shared.Changes {
		d.Changes = append(d.Changes, PublicChange{c.Action, c.From, c.To, c.Reason, c.Coverage})
	}
	for _, h := range r.Plan.Shared.History {
		f := projectFact(SharedDesign{}, h.Fact)
		raw, e := PublicJSON(f)
		if e != nil {
			return d, nil, e
		}
		d.History = append(d.History, PublicHistory{h.Ref, f, documentstore.Hash(raw)})
	}
	arts := map[string]Artifact{}
	for _, a := range r.Members {
		arts[a.Key] = a
	}
	resources := map[string]PublicResource{}
	for _, m := range r.Plan.Members {
		a, ok := arts[m.Key]
		if !ok {
			return d, nil, ErrRules
		}
		p, e := documentprofile.Parse([]byte(m.ProfileSnapshot), documentprofile.Limits{})
		if e != nil {
			return d, nil, e
		}
		if !PublicTextSafe(a.Body) {
			return d, nil, fmt.Errorf("%w: unsafe public member %s", ErrRules, m.Key)
		}
		for _, issue := range objectlocation.OfflineResourceIssues(a.Body, a.Resources.Bindings) {
			u, _ := url.Parse(issue.Locator)
			if u == nil || u.Scheme != "docset" {
				return d, nil, fmt.Errorf("%w: offline resource %s", ErrRules, issue.Locator)
			}
		}
		d.Members = append(d.Members, PublicMember{m.Key, m.Role, m.Title, m.Filename, a.RevisionID, a.Body, p.Validation.SectionHeadings, m.Covers})
		for _, b := range a.Resources.Bindings {
			if b.Private || !objectlocation.ValidBinding(b) {
				return d, nil, ErrRules
			}
			target := "assets/" + b.ResourceID + "/asset" + strings.ToLower(path.Ext(b.Path))
			size, ok := a.ResourceSizes[b.ResourceID]
			if !ok {
				return d, nil, ErrRules
			}
			v := PublicResource{Name: path.Base(b.Path), ID: b.ResourceID, Path: target, Hash: b.Hash, Size: size}
			if old, ok := resources[b.ResourceID]; ok && old != v {
				return d, nil, ErrInvalid
			}
			resources[b.ResourceID] = v
		}
	}
	keys := []string{}
	for k := range resources {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		d.Resources = append(d.Resources, resources[k])
	}
	files, e := PublicDerivedFiles(d)
	if e != nil {
		return d, nil, e
	}
	for _, res := range d.Resources {
		d.Files = append(d.Files, PublicFile{res.Path, res.Size, res.Hash})
	}
	for name, raw := range files {
		d.Files = append(d.Files, PublicFile{name, len(raw), documentstore.Hash(raw)})
	}
	sort.Slice(d.Files, func(i, j int) bool { return d.Files[i].Path < d.Files[j].Path })
	raw, e := PublicJSON(d)
	if e != nil || !PublicTextSafe(string(raw)) {
		return d, nil, ErrRules
	}
	return d, files, nil
}
func PublicPayloadHash(r Revision) (string, error) {
	d, _, e := PublicPayload(r)
	if e != nil {
		return "", e
	}
	raw, e := PublicJSON(d)
	return documentstore.Hash(raw), e
}

// The descriptor includes canonical bodies and conversion identity. Consumers can
// reproduce all derived bytes, rather than trusting a rewritten body's old hash.
func PublicDerivedFiles(d PublicDescriptor) (map[string][]byte, error) {
	if d.Conversion != PublicConversionVersion || d.Projection != PublicProjectionVersion || d.CanonicalVersion != PublicJSONVersion || d.Validator != ValidatorVersion {
		return nil, ErrInvalid
	}
	files := map[string][]byte{}
	names := map[string]string{}
	for _, m := range d.Members {
		names[m.Key] = m.Filename
	}
	index := "# Engineering design\n\nVersion: " + d.Revision + "\n\n"
	md := goldmark.New(goldmark.WithExtensions(extension.GFM), goldmark.WithParserOptions(parser.WithAutoHeadingID()))
	for _, m := range d.Members {
		body := m.Body
		for _, l := range Links(body) {
			u, e := url.Parse(l)
			if e != nil {
				return nil, e
			}
			if u.Scheme == "docset" {
				key, e := url.PathUnescape(strings.TrimPrefix(u.Path, "/"))
				if e != nil || u.Host != d.Set || names[key] == "" {
					return nil, ErrInvalid
				}
				body = strings.ReplaceAll(body, l, names[key]+"#"+u.Fragment)
			}
		}
		for _, r := range d.Resources {
			body = strings.ReplaceAll(body, "resource://"+r.ID, r.Path)
		}
		var rendered bytes.Buffer
		if e := md.Convert([]byte(body), &rendered, parser.WithContext(parser.NewContext(parser.WithIDs(publicHeadingIDs{})))); e != nil {
			return nil, e
		}
		h := rendered.String()
		for _, name := range names {
			h = strings.ReplaceAll(h, `href="`+name, `href="`+strings.TrimSuffix(name, ".md")+".html")
		}
		page := `<!doctype html><meta charset="utf-8"><meta http-equiv="Content-Security-Policy" content="default-src 'none'; img-src 'self'; style-src 'unsafe-inline'"><title>` + html.EscapeString(m.Title) + `</title><style>body{max-width:960px;margin:2em auto;padding:1em;line-height:1.7}pre{overflow:auto}</style>` + h
		files[m.Filename] = []byte(body)
		files[strings.TrimSuffix(m.Filename, ".md")+".html"] = []byte(page)
		index += "- [" + m.Title + "](" + m.Filename + ")\n"
	}
	files["index.md"] = []byte(index)
	files["public-schema.md"] = []byte(PublicSchemaText)
	return files, nil
}

type publicHeadingIDs map[string]int

func (ids publicHeadingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := StableAnchor(string(value))
	id := base
	if ids[base] > 0 {
		id = fmt.Sprintf("%s-%d", base, ids[base])
	}
	ids[base]++
	return []byte(id)
}
func (ids publicHeadingIDs) Put(value []byte) { ids[string(value)]++ }

// PublicSemanticPlan reconstructs only the public, normative contract for offline
// validation. It never reconstructs a capture or claims to verify private approval.
func PublicSemanticPlan(d PublicDescriptor) Plan {
	s := SharedDesign{Scope: &DesignScope{ID: d.Scope.ID, Requirements: d.Scope.Requirements, GlobalConstraints: d.Scope.Constraints}, Conditions: map[string]Condition{}}
	for _, x := range d.Scope.Exclusions {
		s.Scope.Excluded = append(s.Scope.Excluded, ScopeExclusion{FactID: x.Fact, Reason: x.Reason})
	}
	for _, f := range d.Facts {
		s.Facts = append(s.Facts, f.fact())
	}
	for _, h := range d.History {
		f := h.Fact.fact()
		f.Status = h.Fact.Status
		f.Hash = h.Ref.Hash
		s.History = append(s.History, HistoricalFact{Ref: h.Ref, Fact: f})
	}
	for _, c := range d.Changes {
		s.Changes = append(s.Changes, FactChange{Action: c.Action, From: c.From, To: c.To, Reason: c.Reason, Coverage: c.Coverage})
	}
	s.Relations = d.Relations
	for _, o := range d.Resolutions {
		s.Obligations = append(s.Obligations, ObligationResolution{ID: o.ID, State: o.State, Facts: o.Facts, Member: o.Member, Section: o.Section, Reason: o.Reason})
		for _, obl := range d.Obligations {
			if obl.ID == o.ID && obl.When != "" {
				v := o.State != "NOT_APPLICABLE"
				s.Conditions[obl.When] = Condition{Value: &v, Status: "confirmed"}
			}
		}
	}
	p := Plan{SetID: d.Set, Shared: s, Profile: Profile{Purpose: EngineeringPurpose, Obligations: d.Obligations}}
	for _, m := range d.Members {
		p.Members = append(p.Members, PlanMember{Key: m.Key, Role: m.Role, Title: m.Title, Filename: m.Filename, Covers: m.Covers})
	}
	return p
}

// PublicSchemaText is itself part of the frozen payload, so a policy change
// cannot silently reuse an old review or distribution identity.
const PublicSchemaText = `# Engineering public contract v1

Encoding: public-json-jcs-v1 (RFC 8785, safe integers only). UTF-8; strings are not normalized. Object keys use UTF-16 ordering. Arrays retain order; null, empty and omitted fields are distinct. Duplicate and unknown keys are rejected.

Projection: engineering-public-v1. Canonical member bodies are frozen UTF-8/LF. Logical docset links resolve to the manifest member filename and anchor; resource links resolve to the exact asset path and hash. Conversion: engineering-markdown-html-v1, Goldmark GFM, Unicode-safe stable heading anchors, script-disabled CSP. Derived files are reproducible by the supported designcheck tool.

Facts are the sole authority: ID, kind, owner, epistemic status, modality, classified provenance, scope, fields and typed payloads. Owner is a stable member key. Normative facts must be confirmed; unresolved assumptions and conflicts block delivery. Scope retains all requirements and explicit exclusions; dependencies cannot silently cross excluded requirements.

Typed relations: module/decision realizes requirement; constraint constrains interface/module/artifact_spec; module/decision uses_contract interface/entity; verification verifies requirement/constraint/interface/module/artifact_spec; depends_on uses prerequisite direction and must be acyclic. Supersedes points only to a frozen historical fact; it never counts as current verification coverage.

Acceptance needs preconditions, stimulus, expected observations, pass rule, verification method and evidence requirements, with negative/boundary/recovery cases or explicit applicability reasons. State constraints specify initial state, trigger, guard, target, failure and persistence. Quality contracts specify measurement conditions or testable principal/resource/permission/prohibited operation/outcome. Artifact contracts define files, format, build/run entry and commands, environment, configuration, data policy, usage, security, license and verification. These are expected results, not claims of actual implementation or test execution.

DEFINED content obligations bind facts to a member and stable section. NOT_APPLICABLE is permitted only for a confirmed false applicability condition with rationale. UNRESOLVED blocks readiness. Public review binds the exact payload hash, candidate source reference and validator policy. Warnings require located risk decisions. Candidate/internal hashes are provenance references, not public recomputable hashes. Distribution hashes include review, while payload hashes exclude review and descriptor self-hashes.

Offline PASS proves public consistency only. Private approval provenance and publisher authenticity require independent trusted evidence; a supplied trusted payload hash detects replacement. No approval, DoD, execution permission, consumer acceptance or final artifact success is created by this package.
`

func ValidateEngineeringPublicRelations(s SharedDesign) error { return validateEngineeringRelations(s) }

func ScopeBlock(p Plan) string {
	if !Engineering(p.Profile) || p.Shared.Scope == nil {
		return ""
	}
	s := p.Shared.Scope
	public := PublicScope{ID: s.ID, Requirements: s.Requirements, Constraints: s.GlobalConstraints, Exclusions: []PublicExclusion{}}
	for _, x := range s.Excluded {
		public.Exclusions = append(public.Exclusions, PublicExclusion{x.FactID, x.Reason})
	}
	raw, _ := json.MarshalIndent(public, "", "  ")
	return "\n### design-scope\n\nScope exclusions are binding. Excluded requirements remain visible for traceability and are not implementation authorization.\n\n```json\n" + string(raw) + "\n```\n"
}
