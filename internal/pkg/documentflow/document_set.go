package documentflow

import (
	"os"
	"path/filepath"
	"strings"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/objectlocation"
)

// Editing narrative creates a personal working revision. Editing an automatic
// definition creates a private shared-design proposal instead of a silent fork.
func (s *Service) checkManagedContract(id string, d documentstore.RevisionData) error {
	root := filepath.Dir(s.Store.Root)
	r := objectlocation.For(root)
	l, e := r.Lookup(id)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	if l.ManagedSetID == "" {
		return nil
	}
	var h ds.Head
	if e = objectlocation.ReadJSON(root, "topics/"+l.ManagedSetID+"/document-set/head.json", &h); os.IsNotExist(e) {
		return nil
	} else if e != nil {
		return e
	}
	var revision ds.Revision
	if e = objectlocation.ReadJSON(root, "topics/"+l.ManagedSetID+"/document-set/revisions/"+h.Working+"/manifest.json", &revision); e != nil {
		return e
	}
	changed := false
	for _, f := range revision.Plan.Shared.Facts {
		if f.Owner == l.ManagedMemberKey || f.Kind == "term" || f.Kind == "constraint" {
			if strings.Count(d.Body, ds.FactBlock(f)) != 1 {
				changed = true
			}
		}
	}
	if !changed {
		return nil
	}
	proposal := map[string]any{"document_id": id, "set_id": l.ManagedSetID, "base_set_revision": h.Working, "body": d.Body, "contract": d.Contract, "state": "requires_shared_design_review"}
	key := strings.TrimPrefix(documentstore.HashJSON(proposal), "sha256:")
	if e = objectlocation.WriteJSON(root, ".content-registry/private/document-set-proposals/"+id+"/"+key+".json", proposal, true); e != nil {
		return e
	}
	return ErrRules
}
