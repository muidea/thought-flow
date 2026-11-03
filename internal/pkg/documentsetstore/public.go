package documentsetstore

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io"
	"net/http"
	"path"
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/objectlocation"
)

type PublicReview struct {
	Set         string              `json:"set_id"`
	Revision    string              `json:"set_revision_id"`
	PayloadHash string              `json:"public_payload_hash"`
	Projection  string              `json:"projection_version"`
	Validator   string              `json:"validator_version"`
	Candidate   string              `json:"source_candidate_hash"`
	ReportHash  string              `json:"source_report_hash"`
	Status      string              `json:"status"`
	Semantic    string              `json:"semantic_status"`
	Evidence    []ds.ReviewEvidence `json:"evidence"`
	Issues      []ds.Issue          `json:"issues"`
	Decisions   []ds.ReviewDecision `json:"risk_decisions"`
}

type PublicCheck struct {
	Status      string `json:"status"`
	PayloadHash string `json:"public_payload_hash"`
	ReviewHash  string `json:"public_review_hash"`
	Publisher   string `json:"publisher_authentication"`
}

func (s *Store) publicFiles(r ds.Revision) (ds.PublicDescriptor, map[string][]byte, error) {
	d, files, e := ds.PublicPayload(r)
	if e != nil {
		return d, nil, e
	}
	hash, e := ds.PublicPayloadHash(r)
	if e != nil || hash != r.Report.PublicPayloadHash || r.Report.ProjectionVersion != ds.PublicProjectionVersion {
		return d, nil, ds.ErrRules
	}
	registry := objectlocation.For(s.Root)
	for _, res := range d.Resources {
		found := false
		for _, a := range r.Members {
			for _, b := range a.Resources.Bindings {
				if b.ResourceID != res.ID {
					continue
				}
				p, e := registry.FrozenResourcePath(a.DocumentID, b)
				if e != nil {
					return d, nil, e
				}
				raw, e := objectlocation.ReadFileBounded(p, objectlocation.MaxResourceBytes)
				if e != nil {
					return d, nil, e
				}
				if len(raw) != res.Size || documentstore.Hash(raw) != res.Hash || !ds.PublicBytesSafe(raw) {
					return d, nil, ds.ErrRules
				}
				if strings.HasPrefix(http.DetectContentType(raw), "text/") && !ds.PublicTextSafe(string(raw)) {
					return d, nil, ds.ErrRules
				}
				files[res.Path] = raw
				found = true
				break
			}
		}
		if !found {
			return d, nil, ds.ErrRules
		}
	}
	descriptor, e := ds.PublicJSON(d)
	if e != nil {
		return d, nil, e
	}
	files["design.json"] = descriptor
	review := PublicReview{Set: d.Set, Revision: d.Revision, PayloadHash: hash, Projection: d.Projection, Validator: d.Validator, Candidate: r.Report.CandidateHash, ReportHash: r.Report.Hash, Status: r.Report.Status, Semantic: r.Report.SemanticStatus, Evidence: r.Report.Evidence, Issues: r.Report.Issues, Decisions: r.Report.ReviewDecisions}
	raw, e := ds.PublicJSON(review)
	if e != nil || !ds.PublicTextSafe(string(raw)) {
		return d, nil, ds.ErrRules
	}
	files["review.json"] = raw
	return d, files, nil
}

func (s *Store) exportEngineering(r ds.Revision, authorized bool) ([]byte, error) {
	if !authorized {
		return nil, fmt.Errorf("%w: explicit authorization to publish required", ds.ErrPrecondition)
	}
	d, files, e := s.publicFiles(r)
	if e != nil {
		return nil, e
	}
	manifest := PackageManifest{Schema: 1, Kind: "delivery", SetID: d.Set, Revision: d.Revision, RevisionHash: r.Hash, Status: "complete", PublicPayloadHash: r.Report.PublicPayloadHash, PublicReviewHash: documentstore.Hash(files["review.json"]), ProjectionVersion: d.Projection}
	for _, m := range d.Members {
		manifest.Members = append(manifest.Members, PackageMember{Key: m.Key, Revision: m.Revision, Filename: m.Filename, MarkdownHash: documentstore.Hash(files[m.Filename]), HTMLHash: documentstore.Hash(files[strings.TrimSuffix(m.Filename, ".md")+".html"])})
	}
	raw, e := zipFiles(files, manifest)
	if e != nil {
		return nil, e
	}
	if _, e = VerifyPublicPackage(raw, manifest.PublicPayloadHash); e != nil {
		return nil, e
	}
	return raw, nil
}

// VerifyPublicPackage uses only public ZIP bytes and an optional trusted payload
// anchor. Successful consistency checking is never publisher authentication.
func VerifyPublicPackage(raw []byte, trustedPayload string) (PublicCheck, error) {
	fail := func() (PublicCheck, error) { return PublicCheck{Status: "FAIL"}, ds.ErrInvalid }
	if len(raw) > MaxPackageBytes {
		return fail()
	}
	zr, e := zip.NewReader(bytes.NewReader(raw), int64(len(raw)))
	if e != nil || len(zr.File) > MaxPackageFiles {
		return fail()
	}
	files := map[string][]byte{}
	fold := map[string]bool{}
	total := 0
	for _, f := range zr.File {
		name := f.Name
		if !publicPath(name) || !f.Mode().IsRegular() || fold[strings.ToLower(name)] {
			return fail()
		}
		fold[strings.ToLower(name)] = true
		if f.UncompressedSize64 > MaxPackageBytes {
			return fail()
		}
		reader, e := f.Open()
		if e != nil {
			return fail()
		}
		b, e := io.ReadAll(io.LimitReader(reader, MaxPackageBytes+1))
		reader.Close()
		if e != nil {
			return fail()
		}
		total += len(b)
		if total > MaxPackageBytes {
			return fail()
		}
		files[name] = b
	}
	decode := func(name string, v any) error {
		b, ok := files[name]
		if !ok {
			return ds.ErrInvalid
		}
		if _, e := ds.CanonicalPublicJSON(b); e != nil {
			return e
		}
		return ds.StrictJSON(b, v)
	}
	var manifest PackageManifest
	var d ds.PublicDescriptor
	var review PublicReview
	if decode("package.json", &manifest) != nil || decode("design.json", &d) != nil || decode("review.json", &review) != nil {
		return fail()
	}
	if manifest.Kind != "delivery" || manifest.Status != "complete" || manifest.Schema != 1 || d.Schema != 1 || !documentstore.ValidID(d.Set) || !documentstore.ValidID(d.Revision) || len(d.Members) == 0 {
		return fail()
	}
	listed := map[string]bool{}
	for _, f := range manifest.Files {
		b, ok := files[f.Path]
		if !ok || !publicPath(f.Path) || listed[f.Path] || f.Path == "package.json" || f.Size != len(b) || f.Hash != documentstore.Hash(b) {
			return fail()
		}
		listed[f.Path] = true
	}
	if len(listed)+1 != len(files) {
		return fail()
	}
	canonical, e := ds.PublicJSON(d)
	if e != nil {
		return fail()
	}
	hash := documentstore.Hash(canonical)
	if manifest.SetID != d.Set || manifest.Revision != d.Revision || manifest.PublicPayloadHash != hash || review.PayloadHash != hash || review.Set != d.Set || review.Revision != d.Revision || review.Validator != d.Validator || review.Projection != d.Projection || manifest.ProjectionVersion != d.Projection || review.Status != "ready" || (review.Semantic != "model_reviewed" && review.Semantic != "manual_reviewed") || manifest.PublicReviewHash != documentstore.Hash(files["review.json"]) {
		return fail()
	}
	if trustedPayload != "" && trustedPayload != hash {
		return fail()
	}
	p := ds.PublicSemanticPlan(d)
	if ds.ValidateEngineeringPublicRelations(p.Shared) != nil {
		return fail()
	}
	if len(ds.EngineeringIssues(p)) > 0 {
		return fail()
	}
	expected, e := ds.PublicDerivedFiles(d)
	if e != nil {
		return fail()
	}
	for name, b := range expected {
		if !bytes.Equal(b, files[name]) {
			return fail()
		}
	}
	for _, m := range d.Members {
		if strings.Count(m.Body, ds.ScopeBlock(p)) != 1 {
			return fail()
		}
	}
	for _, o := range d.Resolutions {
		if o.State != "DEFINED" {
			continue
		}
		found := false
		for _, m := range d.Members {
			if m.Key == o.Member {
				found = true
				for _, id := range o.Facts {
					var content string
					for _, f := range d.Facts {
						if f.ID == id {
							content = f.Content
						}
					}
					if !ds.EvidenceInSection(m.Body, o.Section, content) {
						return fail()
					}
				}
			}
		}
		if !found {
			return fail()
		}
	}
	memberKeys := map[string]bool{}
	facts := map[string]ds.PublicFact{}
	for _, f := range d.Facts {
		if facts[f.ID].ID != "" {
			return fail()
		}
		facts[f.ID] = f
	}
	for _, m := range d.Members {
		if memberKeys[m.Key] || !ds.ValidKey(m.Key) || !publicPath(m.Filename) {
			return fail()
		}
		memberKeys[m.Key] = true
		for _, f := range d.Facts {
			count := strings.Count(m.Body, ds.FactBlock(publicInternalFact(f)))
			if f.Owner == m.Key {
				if count != 1 {
					return fail()
				}
			} else if count > 0 {
				return fail()
			}
		}
	}
	if len(manifest.Members) != len(d.Members) {
		return fail()
	}
	for _, m := range manifest.Members {
		found := false
		for _, v := range d.Members {
			if m.Key == v.Key && m.Filename == v.Filename && m.Revision == v.Revision && m.MarkdownHash == documentstore.Hash(files[v.Filename]) && m.HTMLHash == documentstore.Hash(files[strings.TrimSuffix(v.Filename, ".md")+".html"]) {
				found = true
			}
		}
		if !found {
			return fail()
		}
	}
	payloadFiles := map[string]bool{}
	for _, f := range d.Files {
		if payloadFiles[f.Path] || !publicPath(f.Path) || f.Path == "design.json" || f.Path == "review.json" || f.Path == "package.json" {
			return fail()
		}
		payloadFiles[f.Path] = true
		b, ok := files[f.Path]
		if !ok || len(b) != f.Size || documentstore.Hash(b) != f.Hash {
			return fail()
		}
	}
	for _, res := range d.Resources {
		if !payloadFiles[res.Path] || documentstore.Hash(files[res.Path]) != res.Hash || len(files[res.Path]) != res.Size {
			return fail()
		}
	}
	if len(payloadFiles) != len(expected)+len(d.Resources) {
		return fail()
	}
	if len(payloadFiles)+3 != len(files) {
		return fail()
	}
	for _, h := range d.History {
		b, e := ds.PublicJSON(h.Fact)
		if e != nil || documentstore.Hash(b) != h.Hash || h.Ref.SetID != d.Set {
			return fail()
		}
	}
	for _, b := range files {
		if !ds.PublicBytesSafe(b) {
			return fail()
		}
		if !ds.PublicTextSafe(string(b)) { // binary resources are checked by their exact frozen hash
			binary := false
			for _, res := range d.Resources {
				if bytes.Equal(b, files[res.Path]) && !strings.HasPrefix(http.DetectContentType(b), "text/") {
					binary = true
				}
			}
			if !binary {
				return fail()
			}
		}
	}
	issueIDs := map[string]bool{}
	decisions := map[string]ds.ReviewDecision{}
	for _, decision := range review.Decisions {
		if decisions[decision.IssueID].IssueID != "" || strings.TrimSpace(decision.Reason) == "" || len(decision.Locations) == 0 {
			return fail()
		}
		decisions[decision.IssueID] = decision
	}
	for _, issue := range review.Issues {
		if issueIDs[issue.ID] || issue.Hard || issue.Severity != "warning" || !issue.Confirmable {
			return fail()
		}
		issueIDs[issue.ID] = true
		decision, ok := decisions[issue.ID]
		if !ok {
			return fail()
		}
		for _, loc := range decision.Locations {
			found := false
			for _, m := range d.Members {
				if m.Key == loc.Member && ds.EvidenceInSection(m.Body, loc.Section, loc.Evidence) {
					found = true
				}
			}
			if !found {
				return fail()
			}
		}
	}
	if len(decisions) != len(issueIDs) {
		return fail()
	}
	// Validate review locations against the canonical public body, not transformed HTML.
	arts := map[string]ds.Artifact{}
	items := map[string]ds.ReviewItem{}
	seen := map[string]bool{}
	for _, m := range d.Members {
		arts[m.Key] = ds.Artifact{Key: m.Key, Body: m.Body}
	}
	for _, item := range ds.Checklist(p) {
		items[item.ID] = item
	}
	for _, ev := range review.Evidence {
		if seen[ev.CheckID] || ev.Verdict != "passed" || ds.VerifyEvidence(ev, items, arts) != nil {
			return fail()
		}
		seen[ev.CheckID] = true
	}
	if len(seen) != len(items) {
		return fail()
	}
	return PublicCheck{Status: "PASS", PayloadHash: hash, ReviewHash: manifest.PublicReviewHash, Publisher: "NOT_AUTHENTICATED"}, nil
}
func publicInternalFact(f ds.PublicFact) ds.Fact {
	return ds.Fact{ID: f.ID, Kind: f.Kind, Name: f.Name, Content: f.Content, Status: f.Status, Modality: f.Modality, Provenance: f.Provenance, ScopeRefs: f.Scope, NonNormative: f.Optional, Owner: f.Owner, Fields: f.Fields, Acceptance: f.Acceptance, Quality: f.Quality, StateRule: f.State, Artifact: f.Artifact}
}
func publicPath(s string) bool {
	for _, r := range s {
		if r < 32 || r > 126 {
			return false
		}
	}
	return s != "" && path.Clean(s) == s && s != "." && !strings.HasPrefix(s, "/") && !strings.HasPrefix(s, "../") && !strings.ContainsAny(s, "\\:\x00") && len(s) < 1024
}

func (s *Store) ValidatePublicMaterial(r ds.Revision) error {
	if !ds.Engineering(r.Plan.Profile) {
		return nil
	}
	_, _, e := s.publicFiles(r)
	return e
}

// History snapshots are conveniences for public projection, never alternative
// authorities. Authenticate their exact content against retained ancestor bytes.
func (s *Store) validateHistoricalReferences(r ds.Revision) error {
	if !ds.Engineering(r.Plan.Profile) {
		return nil
	}
	if e := ds.ValidateHistory(r.Plan.Shared); e != nil {
		return e
	}
	pending := map[ds.FactReference]ds.Fact{}
	for _, h := range r.Plan.Shared.History {
		pending[h.Ref] = h.Fact
	}
	current := r.Parent
	seen := map[string]bool{r.ID: true}
	for len(pending) > 0 && current != "" {
		if seen[current] {
			return ds.ErrInvalid
		}
		seen[current] = true
		var parent ds.Revision
		if e := objectlocation.ReadJSON(s.Root, Base(r.Plan.SetID)+"/revisions/"+current+"/manifest.json", &parent); e != nil {
			return e
		}
		if parent.ID != current || parent.Plan.SetID != r.Plan.SetID || parent.Hash != ds.RevisionHash(parent) || parent.Report.CandidateHash != ds.CandidateHash(parent) || parent.Report.Hash != ds.ReportHash(parent.Report) {
			return ds.ErrInvalid
		}
		for ref, snapshot := range pending {
			if ref.SetID != r.Plan.SetID {
				return ds.ErrInvalid
			}
			if ref.RevisionID != current {
				continue
			}
			found := false
			for _, f := range parent.Plan.Shared.Facts {
				if f.ID == ref.FactID && f.Hash == ref.Hash && ds.FactMeaning(f) == ds.FactMeaning(snapshot) {
					found = true
					break
				}
			}
			if !found {
				return ds.ErrInvalid
			}
			delete(pending, ref)
		}
		current = parent.Parent
	}
	if len(pending) > 0 {
		return ds.ErrInvalid
	}
	return nil
}
