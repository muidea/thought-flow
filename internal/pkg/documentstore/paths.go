package documentstore

import (
	"os"
	"path/filepath"
	"thoughtflow/internal/pkg/objectlocation"
)

// ContentPaths is the complete explicit Git allowlist. Runtime evidence, model
// requests, stage directories and unreferenced revisions never enter it.
func (s *Store) ContentPaths(id string) ([]string, error) {
	h, e := s.Head(id)
	if e != nil {
		return nil, e
	}
	l, e := objectlocation.For(filepath.Dir(s.Root)).Lookup(id)
	if e != nil {
		return nil, e
	}
	base := l.Placement.DocumentRoot
	paths := []string{filepath.ToSlash(filepath.Join(base, "head.json")), objectlocation.RecordPath(id), ".content-registry/schema.json"}
	seen := map[string]bool{}
	for _, start := range []string{h.WorkingRevisionID, h.FinalizedRevisionID} {
		for rev := start; rev != "" && !seen[rev]; {
			seen[rev] = true
			d, e := s.Revision(id, rev)
			if e != nil {
				return nil, e
			}
			for _, name := range []string{"body.md", "contract.json", "source-manifest.json", "profile.snapshot.md", "manifest.json"} {
				paths = append(paths, filepath.ToSlash(filepath.Join(base, "revisions", rev, name)))
			}
			if d.Revision.ResourceBindingsHash != "" {
				paths = append(paths, filepath.ToSlash(filepath.Join(base, "revisions", rev, "resources.json")))
			}
			if d.Revision.ProjectionHash != "" {
				paths = append(paths, filepath.ToSlash(filepath.Join(base, "revisions", rev, "projection.json")))
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
			paths = append(paths, filepath.ToSlash(filepath.Join(base, kind, key+".json")))
		}
	}
	return paths, nil
}

// CheckPublishedRevision distinguishes a durable personal history/set selection
// from an immutable file merely prepared by an unfinished attempt.
func (s *Store) CheckPublishedRevision(id, target string) error {
	if !ValidID(target) {
		return ErrInvalid
	}
	h, e := s.ActiveHead(id)
	if e != nil {
		return e
	}
	seen := map[string]bool{}
	for _, start := range []string{h.WorkingRevisionID, h.FinalizedRevisionID} {
		for rev := start; rev != "" && !seen[rev]; {
			if len(seen) > 10000 {
				return ErrInvalid
			}
			seen[rev] = true
			d, e := s.FrozenRevision(id, rev)
			if e != nil {
				return e
			}
			if rev == target {
				return nil
			}
			rev = d.Revision.ParentRevisionID
		}
	}
	registry := objectlocation.For(filepath.Dir(s.Root))
	l, e := registry.Lookup(id)
	if e != nil {
		return e
	}
	if l.ManagedSetID != "" {
		return registry.CheckManagedRevision(l, target)
	}
	return os.ErrNotExist
}
