package markdown

import (
	"context"
	"strings"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func ThoughtPath(root, id string) (string, error) {
	l, e := objectlocation.For(root).Lookup(id)
	return l.Placement.ThoughtPath, e
}
func ListThoughtIDs(root string) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return []string{}, nil
	}
	locations, e := objectlocation.For(root).ListActive(context.Background())
	if e != nil {
		return nil, e
	}
	ids := make([]string, 0, len(locations))
	for _, l := range locations {
		ids = append(ids, l.ObjectID)
	}
	return ids, nil
}

// GitPaths enumerates explicit public business metadata only; private evidence
// and migration journals are never admitted by an enclosing directory.
func GitPaths(root, id string) []string {
	l, e := objectlocation.For(root).Lookup(id)
	if e != nil {
		return nil
	}
	paths := []string{l.Placement.ThoughtPath, objectlocation.RecordPath(id), ".content-registry/schema.json"}
	if l.ResourceBindingsRef != nil {
		paths = append(paths, l.ResourceBindingsRef.Path)
	}
	if b, e := objectlocation.For(root).Bindings(id); e == nil {
		for _, resource := range b.Bindings {
			if !resource.Private {
				paths = append(paths, resource.Path, ".content-registry/resources/"+resource.ResourceID+".json")
			}
		}
	}
	if journal, e := documentstore.New(root).ContentPaths(id); e == nil {
		paths = append(paths, journal...)
	}
	return paths
}

// These variants require the caller to hold the namespace read lease. They
// avoid recursive RWMutex acquisition when a migration writer is queued.
func ReadThoughtUnderNamespace(root, id string) (models.Thought, models.ThoughtContent, error) {
	unlock := objectlocation.For(root).LockObject(id)
	defer unlock()
	return readThoughtLocked(root, id)
}
func UpdateThoughtUnderNamespace(root, id string, update func(*models.Thought, *models.ThoughtContent) error) error {
	unlock := objectlocation.For(root).LockObject(id)
	defer unlock()
	return updateThoughtLocked(root, id, update)
}
