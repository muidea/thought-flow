package documentsetstore

import (
	"fmt"
	"strings"
	"unicode/utf8"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/objectlocation"
)

const HandoffVersion = "workorch-authoring-input-v1"
const ConsumerBaseline = "05a823064e1b99aa60e8ba38c1e9d090cf2c5fe0"

type DirectAcceptance struct {
	Text       string `json:"text"`
	DesignFact string `json:"design_fact_id,omitempty"`
}
type ConsumerTemplate struct {
	ID    string   `json:"template_id"`
	Items []string `json:"dod_items"`
	Hash  string   `json:"snapshot_hash"`
}
type HandoffRequest struct {
	ExpectedGeneration *uint64            `json:"expected_generation"`
	Revision           string             `json:"set_revision_id"`
	Goal               string             `json:"goal_text"`
	Direct             []DirectAcceptance `json:"direct_acceptance"`
	TemplateID         string             `json:"template_id"`
	WorkspaceID        string             `json:"workspace_id"`
	Version            *int               `json:"input_version"`
	InputRevision      *uint64            `json:"input_revision"`
	Template           *ConsumerTemplate  `json:"template_snapshot"`
	ConfirmedInputHash string             `json:"confirmed_input_hash"`
	PublicAuthorized   bool               `json:"public_authorized"`
}
type ConsumerInput struct {
	Version      *int     `json:"version"`
	Revision     *uint64  `json:"revision"`
	Goal         string   `json:"goal_text"`
	Acceptance   []string `json:"acceptance_requirements"`
	TemplateID   string   `json:"template_id"`
	WorkspaceID  string   `json:"workspace_id"`
	ResourceRefs []string `json:"resource_refs"`
}
type HandoffResource struct {
	Path    string `json:"path"`
	Content string `json:"content"`
	Hash    string `json:"hash"`
	Bytes   int    `json:"bytes"`
}
type HandoffMapping struct {
	Fact                 string          `json:"source_design_fact_ref"`
	Status               string          `json:"design_status"`
	Modality             string          `json:"modality"`
	Provenance           []ds.Provenance `json:"provenance"`
	TextHash             string          `json:"public_fact_hash"`
	Channel              string          `json:"channel"`
	Resource             string          `json:"resource_ref,omitempty"`
	Anchor               string          `json:"anchor,omitempty"`
	Index                *int            `json:"direct_index,omitempty"`
	TransportHash        string          `json:"transport_hash"`
	InputAdoption        string          `json:"producer_input_adoption,omitempty"`
	ConsumerConfirmation string          `json:"consumer_confirmation"`
}
type HandoffBudget struct {
	InputBytes       int    `json:"normalized_input_bytes"`
	ResourceBytes    int    `json:"resource_snapshot_bytes"`
	MergedLowerBound int    `json:"merged_acceptance_lower_bound_bytes"`
	Input            string `json:"input_status"`
	Resources        string `json:"resource_status"`
	Merged           string `json:"merged_acceptance_status"`
	Assembly         string `json:"request_assembly_status"`
	Consumer         string `json:"consumer_validation_status"`
}
type HandoffPreview struct {
	Adapter      string            `json:"adapter_version"`
	Baseline     string            `json:"consumer_baseline"`
	Set          string            `json:"set_id"`
	Revision     string            `json:"set_revision_id"`
	PayloadHash  string            `json:"public_payload_hash"`
	Input        ConsumerInput     `json:"input"`
	InputHash    string            `json:"input_confirmation_hash"`
	Confirmation string            `json:"input_confirmation_status"`
	Resources    []HandoffResource `json:"resources"`
	Mappings     []HandoffMapping  `json:"mappings"`
	Budget       HandoffBudget     `json:"budget"`
	Blockers     []string          `json:"blockers"`
	Pending      []string          `json:"pending"`
	Status       string            `json:"status"`
}

func TemplateHash(t ConsumerTemplate) string { t.Hash = ""; return documentstore.HashJSON(t) }
func validText(s string, max int) bool {
	return s != "" && len(s) <= max && utf8.ValidString(s) && !strings.ContainsRune(s, 0)
}

func BuildHandoff(d ds.PublicDescriptor, req HandoffRequest) (HandoffPreview, error) {
	p := HandoffPreview{Adapter: HandoffVersion, Baseline: ConsumerBaseline, Set: d.Set, Revision: d.Revision, Resources: []HandoffResource{}, Mappings: []HandoffMapping{}, Blockers: []string{}, Pending: []string{}}
	raw, e := ds.PublicJSON(d)
	if e != nil {
		return p, e
	}
	p.PayloadHash = documentstore.Hash(raw)
	p.Input = ConsumerInput{Version: req.Version, Revision: req.InputRevision, Goal: strings.TrimSpace(req.Goal), Acceptance: []string{}, TemplateID: strings.TrimSpace(req.TemplateID), WorkspaceID: req.WorkspaceID, ResourceRefs: []string{}}
	inputValid := true
	if p.Input.Goal == "" {
		p.Input.Goal = "Implement the confirmed design scope " + d.Scope.ID + " for set " + d.Set + " at " + d.Revision + ". Preserve the normative contracts, exclusions, dependency closure, acceptance and artifact requirements in the ordered resources."
	}
	if !validText(p.Input.Goal, 32768) {
		p.Blockers = append(p.Blockers, "goal_text_invalid")
		inputValid = false
	}
	if req.Version == nil || req.InputRevision == nil || p.Input.TemplateID == "" || p.Input.WorkspaceID == "" {
		p.Pending = append(p.Pending, "consumer_identity_not_bound")
	}
	if req.Version != nil && *req.Version != 1 ||
		req.InputRevision != nil && (*req.InputRevision < 1 || *req.InputRevision > 9007199254740991) ||
		p.Input.TemplateID != "" && !validText(p.Input.TemplateID, 256) ||
		p.Input.WorkspaceID != "" && !validText(p.Input.WorkspaceID, 256) {
		p.Blockers = append(p.Blockers, "consumer_identity_invalid")
		inputValid = false
	}
	if len(req.Direct) > 128 {
		p.Blockers = append(p.Blockers, "direct_acceptance_count")
		inputValid = false
	}
	facts := map[string]ds.PublicFact{}
	for _, f := range d.Facts {
		facts[f.ID] = f
	}
	for i, x := range req.Direct {
		text := strings.TrimSpace(x.Text)
		p.Input.Acceptance = append(p.Input.Acceptance, text)
		if !validText(text, 4096) {
			p.Blockers = append(p.Blockers, "direct_acceptance_invalid")
			inputValid = false
		}
		if x.DesignFact != "" {
			f, ok := facts[x.DesignFact]
			if !ok {
				p.Blockers = append(p.Blockers, "unknown_direct_design_fact")
				continue
			}
			b, _ := ds.PublicJSON(f)
			index := i
			p.Mappings = append(p.Mappings, HandoffMapping{Fact: d.Set + "/" + d.Revision + "/" + f.ID, Status: f.Status, Modality: f.Modality, Provenance: f.Provenance, TextHash: documentstore.Hash(b), Channel: "direct_acceptance", Index: &index, TransportHash: documentstore.Hash([]byte(text)), InputAdoption: "UNCONFIRMED", ConsumerConfirmation: "PENDING_CONSUMER_CONFIRMATION"})
		}
	}
	// Full scope, including exclusions and fixed history, is carried once. Each
	// fact is indivisible; no truncation, summarization or automatic direct inputs.
	header, _ := ds.PublicJSON(struct {
		Set         string                `json:"set_id"`
		Revision    string                `json:"revision_id"`
		Payload     string                `json:"public_payload_hash"`
		Scope       ds.PublicScope        `json:"scope"`
		Relations   []ds.Relation         `json:"relations"`
		Changes     []ds.PublicChange     `json:"fact_changes"`
		History     []ds.PublicHistory    `json:"historical_facts"`
		Obligations []ds.PublicObligation `json:"content_obligations"`
	}{d.Set, d.Revision, p.PayloadHash, d.Scope, d.Relations, d.Changes, d.History, d.Resolutions})
	chunks := []string{"# Frozen engineering design\n\nDesign facts in this resource are DERIVED consumer inputs, not direct USER_EXPLICIT requests or execution authorization.\n\n" + string(header) + "\n"}
	if len(chunks[0]) > 32768 {
		p.Blockers = append(p.Blockers, "scope_contract_exceeds_resource_limit")
	}
	factChunk := map[string]int{}
	factHashes := map[string]string{}
	for _, f := range d.Facts {
		b, e := ds.PublicJSON(f)
		if e != nil {
			return p, e
		}
		part := "\n## " + f.ID + "\n\n" + string(b) + "\n"
		factHashes[f.ID] = documentstore.Hash(b)
		if len(part) > 32768 {
			p.Blockers = append(p.Blockers, "indivisible_fact_exceeds_resource_limit:"+f.ID)
		}
		if len(chunks[len(chunks)-1])+len(part) > 32768 {
			chunks = append(chunks, "")
		}
		factChunk[f.ID] = len(chunks) - 1
		chunks[len(chunks)-1] += part
	}
	for i, text := range chunks {
		ref := fmt.Sprintf("thoughtflow/%s/%s/contracts-%02d.md", d.Set, d.Revision, i+1)
		p.Input.ResourceRefs = append(p.Input.ResourceRefs, ref)
		p.Resources = append(p.Resources, HandoffResource{ref, text, documentstore.Hash([]byte(text)), len(text)})
	}
	for _, f := range d.Facts {
		res := p.Resources[factChunk[f.ID]]
		p.Mappings = append(p.Mappings, HandoffMapping{Fact: d.Set + "/" + d.Revision + "/" + f.ID, Status: f.Status, Modality: f.Modality, Provenance: f.Provenance, TextHash: factHashes[f.ID], Channel: "design_resource", Resource: res.Path, Anchor: ds.StableAnchor(f.ID), TransportHash: res.Hash, ConsumerConfirmation: "PENDING_CONSUMER_DERIVATION"})
	}
	// Match the fixed consumer's byte formulas, keeping the layers separate.
	p.Budget = HandoffBudget{Input: "PASS", Resources: "PASS", Merged: "PENDING_TEMPLATE", Assembly: "NOT_RUN", Consumer: "NOT_RUN"}
	p.Budget.InputBytes = len(p.Input.Goal) + len(p.Input.TemplateID) + len(p.Input.WorkspaceID)
	directBytes := 0
	for _, x := range p.Input.Acceptance {
		p.Budget.InputBytes += len(x)
		directBytes += len(x)
	}
	seen := map[string]bool{}
	for _, r := range p.Resources {
		p.Budget.InputBytes += len(r.Path)
		p.Budget.ResourceBytes += r.Bytes
		if !publicPath(r.Path) || !validText(r.Path, 1024) || seen[r.Path] || r.Bytes > 32768 {
			p.Blockers = append(p.Blockers, "resource_invalid")
			p.Budget.Resources = "FAIL"
		}
		seen[r.Path] = true
	}
	if len(p.Resources) > 8 || p.Budget.ResourceBytes > 131072 {
		p.Budget.Resources = "FAIL"
		p.Blockers = append(p.Blockers, "resource_budget_exceeded")
	}
	if p.Budget.InputBytes > 131072 {
		p.Budget.Input = "FAIL"
		p.Blockers = append(p.Blockers, "input_budget_exceeded")
	}
	if !inputValid {
		p.Budget.Input = "FAIL"
	}
	p.Budget.MergedLowerBound = directBytes
	if req.Template != nil {
		if req.Template.ID != p.Input.TemplateID || req.Template.Hash != TemplateHash(*req.Template) {
			p.Blockers = append(p.Blockers, "template_identity_invalid")
		}
		for _, item := range req.Template.Items {
			if !validText(item, 4096) {
				p.Blockers = append(p.Blockers, "template_item_invalid")
			}
			p.Budget.MergedLowerBound += len(item)
		}
		p.Budget.Merged = "PENDING_DERIVED_ITEMS"
		if p.Budget.MergedLowerBound > 131072 {
			p.Budget.Merged = "FAIL"
			p.Blockers = append(p.Blockers, "merged_acceptance_lower_bound_exceeded")
		}
	} else {
		p.Pending = append(p.Pending, "template_snapshot_not_bound")
	}
	p.Pending = append(p.Pending, "consumer_file_owner_materialization", "derived_acceptance_and_source_mapping", "consumer_assembled_requests", "consumer_creation_and_review")
	binding, bindingErr := ds.PublicJSON(struct {
		Input     ConsumerInput     `json:"input"`
		Payload   string            `json:"payload"`
		Resources []HandoffResource `json:"resources"`
		Template  *ConsumerTemplate `json:"template"`
	}{p.Input, p.PayloadHash, p.Resources, req.Template})
	if bindingErr != nil {
		return p, ds.ErrInvalid
	}
	p.InputHash = documentstore.Hash(binding)
	p.Confirmation = "UNCONFIRMED"
	if req.ConfirmedInputHash != "" {
		if req.ConfirmedInputHash != p.InputHash {
			p.Blockers = append(p.Blockers, "input_confirmation_stale")
		} else {
			// This records local adoption of exact handoff material. Only the
			// consumer can confirm its authoritative input revision and user.
			p.Confirmation = "CONFIRMED_PRODUCER_INPUT"
			for i := range p.Mappings {
				if p.Mappings[i].Channel == "direct_acceptance" {
					p.Mappings[i].InputAdoption = p.Confirmation
				}
			}
		}
	}
	p.Status = "MATERIALS_EXPORTABLE_CONSUMER_PENDING"
	if len(p.Blockers) > 0 {
		p.Status = "BLOCKED"
	}
	return p, nil
}
func (s *Store) Handoff(id string, req HandoffRequest) (HandoffPreview, error) {
	if req.ExpectedGeneration == nil || req.Revision == "" {
		return HandoffPreview{}, ds.ErrPrecondition
	}
	registry := objectlocation.For(s.Root)
	registry.Namespace.RLock()
	defer registry.Namespace.RUnlock()
	h, e := s.Head(id)
	if e != nil {
		return HandoffPreview{}, e
	}
	if h.Generation != *req.ExpectedGeneration {
		return HandoffPreview{}, ds.ErrConflict
	}
	r, e := s.Read(id, req.Revision)
	if e != nil {
		return HandoffPreview{}, e
	}
	if h.Finalized != r.ID || h.Finalization == nil || h.Finalization.DeliveryLevel != "ready_for_use" || h.Finalization.RevisionHash != r.Hash || h.Finalization.ReportHash != r.Report.Hash || ds.Ready(r) != nil {
		return HandoffPreview{}, ds.ErrRules
	}
	d, _, e := s.publicFiles(r)
	if e != nil {
		return HandoffPreview{}, e
	}
	return BuildHandoff(d, req)
}
func (s *Store) ExportHandoff(id string, req HandoffRequest) ([]byte, error) {
	if !req.PublicAuthorized {
		return nil, ds.ErrPrecondition
	}
	preview, e := s.Handoff(id, req)
	if e != nil {
		return nil, e
	}
	if len(preview.Blockers) > 0 {
		return nil, ds.ErrBudget
	}
	files := map[string][]byte{}
	for _, r := range preview.Resources {
		files[r.Path] = []byte(r.Content)
	}
	raw, e := ds.PublicJSON(preview)
	if e != nil {
		return nil, e
	}
	files["handoff-preview.json"] = raw
	return zipFiles(files, PackageManifest{Schema: 1, Kind: "handoff", SetID: id, Revision: req.Revision, Status: "consumer_pending", PublicPayloadHash: preview.PayloadHash})
}
