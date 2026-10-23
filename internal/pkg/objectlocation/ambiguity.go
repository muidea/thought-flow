package objectlocation

import (
	"encoding/json"
	"gopkg.in/yaml.v3"
	"os"
	"strings"

	"thoughtflow/internal/pkg/models"
)

type ambiguityDiagnosis struct {
	HadAuthority bool                    `json:"had_authority"`
	Authority    *models.ObjectLocation  `json:"authority,omitempty"`
	Candidates   []models.ObjectLocation `json:"candidates"`
}

func ambiguityPath(id string) string { return ".content-registry/private/ambiguities/" + id + ".json" }

// Candidates are diagnostic facts, never replacements for existing authority.
func (r *Registry) markAmbiguous(id string, candidates []models.ObjectLocation) error {
	var current models.ObjectLocation
	err := ReadJSON(r.Root, RecordPath(id), &current)
	hadAuthority := err == nil
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	if hadAuthority && (current.ObjectID != id || !ValidLocation(current)) {
		return ErrRecovery
	}
	var authority *models.ObjectLocation
	if hadAuthority {
		copy := current
		authority = &copy
	}
	var previous ambiguityDiagnosis
	if err := ReadJSON(r.Root, ambiguityPath(id), &previous); err == nil {
		hadAuthority = hadAuthority && previous.HadAuthority
		if previous.Authority != nil {
			authority = previous.Authority
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := WriteJSON(r.Root, ambiguityPath(id), ambiguityDiagnosis{HadAuthority: hadAuthority, Authority: authority, Candidates: candidates}, true); err != nil {
		return err
	}
	if current.ObjectID == "" {
		current = candidates[0]
	}
	if current.Lifecycle != "ambiguous" {
		current.Generation++
	}
	current.Lifecycle = "ambiguous"
	return WriteJSON(r.Root, RecordPath(id), current, false)
}

// Validate against preserved metadata without publishing an active record or
// invoking store lookups that intentionally reject ambiguous objects.
func (r *Registry) validateRecoveredLocation(l models.ObjectLocation) error {
	if !ValidLocation(l) {
		return ErrRecovery
	}
	path, err := SafePath(r.Root, l.Placement.ThoughtPath)
	if err != nil {
		return err
	}
	raw, err := ReadFileBounded(path, MaxResourceBytes)
	if err != nil {
		return err
	}
	parts := strings.SplitN(string(raw), "---", 3)
	if len(parts) != 3 {
		return ErrRecovery
	}
	var meta struct {
		ID         string                     `yaml:"id"`
		Profile    *models.DocumentProfileRef `yaml:"document_profile"`
		DocumentID string                     `yaml:"document_id"`
	}
	if err = yaml.Unmarshal([]byte(parts[1]), &meta); err != nil {
		return err
	}
	if meta.ID != l.ObjectID || (meta.DocumentID != "" && meta.DocumentID != l.ObjectID) {
		return ErrRecovery
	}
	bindings, err := r.bindingsForLocation(l)
	if err != nil {
		return err
	}
	for _, b := range bindings.Bindings {
		path, err := SafePath(r.Root, b.Path)
		if err != nil {
			return err
		}
		hash, _, err := FileHash(path)
		if err != nil {
			return err
		}
		if hash != b.Hash {
			return ErrRecovery
		}
	}
	var head models.DocumentHead
	err = ReadJSON(r.Root, l.Placement.DocumentRoot+"/head.json", &head)
	if os.IsNotExist(err) {
		path, e := SafePath(r.Root, l.Placement.DocumentRoot)
		if e != nil {
			return e
		}
		entries, e := os.ReadDir(path)
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if len(entries) > 0 || meta.Profile != nil || meta.DocumentID != "" {
			return ErrRecovery
		}
		return nil
	}
	if err != nil {
		return err
	}
	if head.Generation == 0 || !ValidID(head.WorkingRevisionID) || head.DeletedAt != nil {
		return ErrRecovery
	}
	seen := map[string]bool{}
	for _, start := range []string{head.WorkingRevisionID, head.FinalizedRevisionID} {
		chain := map[string]bool{}
		for rev := start; rev != ""; {
			if chain[rev] || !ValidID(rev) {
				return ErrRecovery
			}
			chain[rev] = true
			if seen[rev] {
				break
			}
			seen[rev] = true
			base := l.Placement.DocumentRoot + "/revisions/" + rev + "/"
			var m models.DocumentRevision
			if err = ReadJSON(r.Root, base+"manifest.json", &m); err != nil {
				return err
			}
			if m.DocumentID != l.ObjectID || m.RevisionID != rev {
				return ErrRecovery
			}
			for name, want := range map[string]string{"body.md": m.BodyHash, "profile.snapshot.md": m.ProfileHash} {
				path, e := SafePath(r.Root, base+name)
				if e != nil {
					return e
				}
				raw, e := ReadFileBounded(path, MaxResourceBytes)
				if e != nil {
					return e
				}
				if name == "profile.snapshot.md" {
					raw = []byte(strings.TrimRight(strings.ReplaceAll(strings.TrimSpace(string(raw)), "\r\n", "\n"), "\n") + "\n")
				}
				if Hash(raw) != want {
					return ErrRecovery
				}
			}
			// Revision hashes use typed JSON encoding (field order is significant).
			var contract models.DocumentContract
			var sources models.SourceManifest
			var resources models.ResourceBindings
			var projection models.ThoughtSnapshot
			for _, part := range []struct {
				name, hash string
				value      any
			}{
				{"contract.json", m.ContractHash, &contract}, {"source-manifest.json", m.SourceManifestHash, &sources},
				{"resources.json", m.ResourceBindingsHash, &resources}, {"projection.json", m.ProjectionHash, &projection},
			} {
				if part.hash == "" {
					continue
				}
				if err = ReadJSON(r.Root, base+part.name, part.value); err != nil {
					return err
				}
				raw, e := json.Marshal(part.value)
				if e != nil {
					return e
				}
				if Hash(raw) != part.hash {
					return ErrRecovery
				}
			}
			rev = m.ParentRevisionID
		}
	}
	if head.FinalizationID != "" {
		if !ValidID(head.FinalizationID) {
			return ErrRecovery
		}
		var final models.FinalizationRecord
		if err = ReadJSON(r.Root, l.Placement.DocumentRoot+"/finalizations/"+head.FinalizationID+".json", &final); err != nil {
			return err
		}
		if final.FinalizationID != head.FinalizationID || final.RevisionID != head.FinalizedRevisionID {
			return ErrRecovery
		}
	}
	return nil
}
