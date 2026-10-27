package objectlocation

import (
	"os"
	"thoughtflow/internal/pkg/models"
)

// CheckManaged authenticates activation against retained set manifests, never
// against a speculative personal HEAD or the mere presence of Markdown bytes.
func (r *Registry) CheckManaged(l models.ObjectLocation) error {
	if l.OwnerTopicID != "" {
		if e := r.CheckTopicActive(l.OwnerTopicID); e != nil {
			return e
		}
	}
	if l.ManagedSetID == "" {
		return nil
	}
	if !ValidID(l.ManagedSetID) || l.OwnerTopicID != l.ManagedSetID {
		return ErrAmbiguous
	}
	var h struct {
		Working   string `json:"working_set_revision"`
		Projected string `json:"projected_working_revision"`
		Finalized string `json:"finalized_set_revision"`
	}
	if e := ReadJSON(r.Root, "topics/"+l.ManagedSetID+"/document-set/head.json", &h); e != nil {
		return os.ErrNotExist
	}
	// Generic personal reads are unavailable until ALL projections complete.
	// Fixed set resolvers can already read the complete immutable publication.
	if h.Working == "" || h.Projected != h.Working {
		return os.ErrNotExist
	}
	seen := map[string]bool{}
	for _, start := range []string{h.Working, h.Finalized} {
		for rev := start; rev != "" && !seen[rev]; {
			if len(seen) > 10000 || !ValidID(rev) {
				return ErrAmbiguous
			}
			seen[rev] = true
			var m struct {
				Parent  string `json:"parent_revision_id"`
				Members []struct {
					Key string `json:"member_key"`
					ID  string `json:"document_id"`
				} `json:"members"`
			}
			if e := ReadJSON(r.Root, "topics/"+l.ManagedSetID+"/document-set/revisions/"+rev+"/manifest.json", &m); e != nil {
				return e
			}
			for _, a := range m.Members {
				if a.ID == l.ObjectID && a.Key == l.ManagedMemberKey {
					return nil
				}
			}
			rev = m.Parent
		}
	}
	return os.ErrNotExist
}
func (r *Registry) RequireSetWriter() error {
	if e := r.Initialize(); e != nil {
		return e
	}
	return WriteJSON(r.Root, ".content-registry/schema.json", struct {
		Layout int `json:"layout_version"`
		Writer int `json:"minimum_writer_version"`
	}{LayoutVersion, WriterVersion}, false)
}

// CheckManagedRevision permits only exact members selected by retained set
// manifests. Prepared candidates are readable through private attempt APIs.
func (r *Registry) CheckManagedRevision(l models.ObjectLocation, target string) error {
	if l.ManagedSetID == "" {
		return os.ErrNotExist
	}
	var h struct {
		Working   string `json:"working_set_revision"`
		Finalized string `json:"finalized_set_revision"`
		History   []struct {
			Revision string `json:"set_revision_id"`
		} `json:"finalization_history"`
	}
	if e := ReadJSON(r.Root, "topics/"+l.ManagedSetID+"/document-set/head.json", &h); e != nil {
		return e
	}
	roots := []string{h.Working, h.Finalized}
	for _, f := range h.History {
		roots = append(roots, f.Revision)
	}
	seen := map[string]bool{}
	for _, start := range roots {
		for rev := start; rev != "" && !seen[rev]; {
			if !ValidID(rev) || len(seen) > 10000 {
				return ErrAmbiguous
			}
			seen[rev] = true
			var m struct {
				Parent  string `json:"parent_revision_id"`
				Members []struct {
					ID       string `json:"document_id"`
					Key      string `json:"member_key"`
					Revision string `json:"revision_id"`
				} `json:"members"`
			}
			if e := ReadJSON(r.Root, "topics/"+l.ManagedSetID+"/document-set/revisions/"+rev+"/manifest.json", &m); e != nil {
				return e
			}
			for _, a := range m.Members {
				if a.ID == l.ObjectID && a.Key == l.ManagedMemberKey && a.Revision == target {
					return nil
				}
			}
			rev = m.Parent
		}
	}
	return os.ErrNotExist
}
