package documentstore

import "path/filepath"

// ContentPaths is the complete explicit Git allowlist. Runtime evidence, model
// requests, stage directories and unreferenced revisions never enter it.
func (s *Store) ContentPaths(id string) ([]string, error) {
	h, e := s.Head(id)
	if e != nil {
		return nil, e
	}
	paths := []string{filepath.ToSlash(filepath.Join("documents", id, "head.json"))}
	seen := map[string]bool{}
	for _, start := range []string{h.WorkingRevisionID, h.FinalizedRevisionID} {
		for rev := start; rev != "" && !seen[rev]; {
			seen[rev] = true
			d, e := s.Revision(id, rev)
			if e != nil {
				return nil, e
			}
			for _, name := range []string{"body.md", "contract.json", "source-manifest.json", "profile.snapshot.md", "manifest.json"} {
				paths = append(paths, filepath.ToSlash(filepath.Join("documents", id, "revisions", rev, name)))
			}
			if d.Revision.ProjectionHash != "" {
				paths = append(paths, filepath.ToSlash(filepath.Join("documents", id, "revisions", rev, "projection.json")))
			}
			rev = d.Revision.ParentRevisionID
		}
	}
	for _, kind := range []string{"reports", "finalizations"} {
		keys, e := s.Records(id, kind)
		if e != nil {
			return nil, e
		}
		for _, key := range keys {
			paths = append(paths, filepath.ToSlash(filepath.Join("documents", id, kind, key+".json")))
		}
	}
	return paths, nil
}
