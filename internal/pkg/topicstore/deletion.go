package topicstore

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

type DeleteCollectionRequest struct {
	OperationID               string `json:"operation_id"`
	ExpectedGeneration        uint64 `json:"expected_generation"`
	ExpectedCatalogGeneration uint64 `json:"expected_catalog_generation"`
}
type collectionDeletion struct {
	TopicID     string              `json:"topic_id"`
	OperationID string              `json:"operation_id"`
	DeletedAt   time.Time           `json:"deleted_at"`
	Files       []models.FileDigest `json:"files"`
}

func deletionPath(id string) string { return ".content-registry/topic-deletions/" + id + ".json" }

// DeleteCollection is an internal convenience; HTTP always supplies both CAS values.
func (s *Store) DeleteCollection(ctx context.Context, id string) error {
	t, e := s.Get(ctx, id)
	if e != nil {
		return e
	}
	c, e := s.Catalog(ctx, id)
	if e != nil {
		return e
	}
	return s.DeleteCollectionCAS(ctx, id, DeleteCollectionRequest{OperationID: objectlocation.ID("delete-"), ExpectedGeneration: t.Generation, ExpectedCatalogGeneration: c.Generation})
}
func (s *Store) DeleteCollectionCAS(ctx context.Context, id string, req DeleteCollectionRequest) error {
	if !objectlocation.ValidID(id) || !objectlocation.ValidID(req.OperationID) {
		return objectlocation.ErrInvalid
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	unlock := s.lock(id)
	defer unlock()
	op, e := r.Begin(req.OperationID, "topic_delete", "", id, req)
	if e != nil {
		return e
	}
	var tombstone collectionDeletion
	if e = objectlocation.ReadJSON(s.rootPath, deletionPath(id), &tombstone); e == nil {
		if tombstone.OperationID != req.OperationID {
			return objectlocation.ErrConflict
		}
		if e = s.finishCollectionDeletion(tombstone); e != nil {
			return e
		}
		return r.Complete(op)
	} else if !os.IsNotExist(e) {
		return e
	}
	t, e := s.Get(ctx, id)
	if e != nil {
		return e
	}
	if t.Mode != models.TopicModeCollection {
		return ErrCollectionOnly
	}
	c, e := s.Catalog(ctx, id)
	if e != nil {
		return e
	}
	if t.Generation != req.ExpectedGeneration || c.Generation != req.ExpectedCatalogGeneration {
		return objectlocation.ErrConflict
	}
	registered, e := r.ListRegistered()
	if e != nil {
		return e
	}
	for _, l := range registered {
		if l.OwnerTopicID == id && l.Lifecycle != "deleted" {
			return objectlocation.ErrConflict
		}
	}
	if len(c.Entries) > 0 {
		return objectlocation.ErrConflict
	}
	dir, e := objectlocation.SafePath(s.rootPath, "topics/"+id)
	if e != nil {
		return e
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return e
	}
	allowed := map[string]bool{"topic.yaml": true, "catalog.json": true, "catalog.md": true, "index.md": true, "storage.snapshot.json": true}
	tombstone = collectionDeletion{TopicID: id, OperationID: req.OperationID, DeletedAt: time.Now().UTC()}
	emptyDirs := []string{}
	for _, entry := range entries {
		if entry.IsDir() {
			if entry.Name() != "thoughts" && entry.Name() != "documents" && entry.Name() != ".private" && entry.Name() != "attachments" && entry.Name() != "references" {
				return objectlocation.ErrConflict
			}
			base, e := objectlocation.SafePath(s.rootPath, "topics/"+id+"/"+entry.Name())
			if e != nil {
				return e
			}
			if e = filepath.WalkDir(base, func(path string, d fs.DirEntry, err error) error {
				if err != nil {
					return err
				}
				if !d.IsDir() {
					return objectlocation.ErrConflict
				}
				if _, err = objectlocation.SafePath(s.rootPath, strings.TrimPrefix(filepath.ToSlash(path), filepath.ToSlash(s.rootPath)+"/")); err != nil {
					return err
				}
				emptyDirs = append(emptyDirs, path)
				return nil
			}); e != nil {
				return e
			}
			continue
		}
		if !allowed[entry.Name()] {
			return objectlocation.ErrConflict
		}
		rel := "topics/" + id + "/" + entry.Name()
		p, e := objectlocation.SafePath(s.rootPath, rel)
		if e != nil {
			return e
		}
		hash, size, e := objectlocation.FileHash(p)
		if e != nil {
			return e
		}
		tombstone.Files = append(tombstone.Files, models.FileDigest{Path: rel, Hash: hash, Size: size})
	}
	sort.Slice(emptyDirs, func(i, j int) bool { return len(emptyDirs[i]) > len(emptyDirs[j]) })
	for _, path := range emptyDirs {
		if e = os.Remove(path); e != nil {
			return e
		}
	}
	op.TargetPath = deletionPath(id)
	op.CandidateHash = objectlocation.HashJSON(tombstone)
	op.EventType = "topic.deleted"
	op.BeforeGeneration = t.Generation
	op.AfterGeneration = t.Generation + 1
	if e = r.Prepare(op); e != nil {
		return e
	}
	if e = objectlocation.WriteJSON(s.rootPath, op.TargetPath, tombstone, false); e != nil {
		return e
	}
	r.BumpEpoch()
	if e = r.Checkpoint("collection_deleted"); e != nil {
		return e
	}
	if e = s.finishCollectionDeletion(tombstone); e != nil {
		return e
	}
	return r.Complete(op)
}
func (s *Store) finishCollectionDeletion(t collectionDeletion) error {
	if !objectlocation.ValidID(t.TopicID) || !objectlocation.ValidID(t.OperationID) {
		return objectlocation.ErrUnsafe
	}
	for _, file := range t.Files {
		if !strings.HasPrefix(file.Path, "topics/"+t.TopicID+"/") || strings.Count(file.Path, "/") != 2 {
			return objectlocation.ErrUnsafe
		}
		path, e := objectlocation.SafePath(s.rootPath, file.Path)
		if e != nil {
			return e
		}
		raw, e := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if objectlocation.Hash(raw) != file.Hash {
			return objectlocation.ErrConflict
		}
		if e = objectlocation.WriteFile(s.rootPath, ".content-registry/private/deleted-topics/"+t.OperationID+"/"+file.Path, raw, 0600); e != nil {
			return e
		}
		if e = os.Remove(path); e != nil {
			return e
		}
	}
	path, e := objectlocation.SafePath(s.rootPath, "topics/"+t.TopicID)
	if e != nil {
		return e
	}
	e = os.Remove(path)
	if os.IsNotExist(e) {
		return nil
	}
	return e
}
func (s *Store) recoverCollectionDeletions() error {
	dir, e := objectlocation.SafePath(s.rootPath, ".content-registry/topic-deletions")
	if e != nil {
		return e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return nil
	}
	if e != nil {
		return e
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return objectlocation.ErrUnsafe
		}
		var t collectionDeletion
		if e = objectlocation.ReadJSON(s.rootPath, ".content-registry/topic-deletions/"+entry.Name(), &t); e != nil {
			return e
		}
		if e = s.finishCollectionDeletion(t); e != nil {
			return e
		}
	}
	return nil
}
