package topicstore

import (
	"context"
	"os"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func (s *Store) RecoverCollections(ctx context.Context) error {
	r := objectlocation.For(s.rootPath)
	if e := s.recoverCollectionDeletions(); e != nil {
		return e
	}
	// Complete multi-file topic initialization before completing its fact intent.
	topics, e := os.ReadDir(s.rootPath + "/topics")
	if e != nil && !os.IsNotExist(e) {
		return e
	}
	for _, entry := range topics {
		if !entry.IsDir() {
			continue
		}
		t, e := r.Topic(entry.Name())
		if e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return e
		}
		if t.Mode != "collection" {
			continue
		}
		if _, e = s.Catalog(ctx, t.ID); os.IsNotExist(e) {
			op, e := r.Operation(t.LastOperationID)
			if e != nil || op.Kind != "topic_create" || t.Generation != 1 {
				return objectlocation.ErrRecovery
			}
			if e = objectlocation.WriteJSON(s.rootPath, catalogPath(t.ID), initialCatalog(t.ID, op.ID), false); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
		path, e := objectlocation.SafePath(s.rootPath, "topics/"+t.ID+"/index.md")
		if e != nil {
			return e
		}
		if _, e = os.Stat(path); os.IsNotExist(e) {
			if e = objectlocation.WriteFile(s.rootPath, "topics/"+t.ID+"/index.md", []byte(initialDocument(t)), 0644); e != nil {
				return e
			}
		} else if e != nil {
			return e
		}
	}
	if e = r.ReconcileIntents(); e != nil {
		return e
	}
	list, e := s.List(ctx)
	if e != nil {
		return e
	}
	for _, t := range list {
		if t.Mode != "collection" {
			continue
		}
		for attempts := 0; attempts < 3; attempts++ {
			_, e = s.RebuildCatalog(ctx, t.ID)
			if e != objectlocation.ErrConflict {
				break
			}
		}
		if e != nil {
			return e
		}
		if e = s.StorageSnapshot(ctx, t.ID); e != nil {
			return e
		}
	}
	return nil
}
func (s *Store) StorageSnapshot(ctx context.Context, id string) error {
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	owned, e := r.ListOwned(ctx, id)
	if e != nil {
		return e
	}
	return objectlocation.WriteJSON(s.rootPath, "topics/"+id+"/storage.snapshot.json", owned, false)
}
func (s *Store) RefreshCollectionsForObject(ctx context.Context, id string) error {
	topics, e := s.List(ctx)
	if e != nil {
		return e
	}
	for _, topic := range topics {
		if topic.Mode != models.TopicModeCollection {
			continue
		}
		found := false
		for _, member := range topic.Members {
			if member == id {
				found = true
			}
		}
		if !found {
			continue
		}
		if _, e = s.RebuildCatalog(ctx, topic.ID); e != nil {
			return e
		}
	}
	return nil
}
