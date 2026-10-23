package topicstore

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

type ImportResult struct {
	Operation models.WorkspaceOperation `json:"operation"`
	TopicID   string                    `json:"topic_id"`
	Objects   map[string]string         `json:"objects"`
	Phase     string                    `json:"phase"`
	Manifest  PackageManifest           `json:"manifest"`
}

func importBase(id string) string { return ".content-registry/private/imports/" + id }
func allowedImportPath(path, topic string, objects map[string]models.ObjectLocation) bool {
	if !validPackagePath(path) {
		return false
	}
	if path == "topics/"+topic+"/topic.yaml" || path == catalogPath(topic) || path == "topics/"+topic+"/catalog.md" || path == "topics/"+topic+"/index.md" || path == "topics/"+topic+"/storage.snapshot.json" {
		return true
	}
	for id, l := range objects {
		if path == objectlocation.RecordPath(id) || path == l.Placement.ThoughtPath {
			return true
		}
		if strings.HasPrefix(path, l.Placement.DocumentRoot+"/") {
			rel := strings.TrimPrefix(path, l.Placement.DocumentRoot+"/")
			parts := strings.Split(rel, "/")
			if rel == "head.json" {
				return true
			}
			if len(parts) == 2 && (parts[0] == "reports" || parts[0] == "finalizations") && objectlocation.ValidID(strings.TrimSuffix(parts[1], ".json")) && strings.HasSuffix(parts[1], ".json") {
				return true
			}
			if len(parts) == 3 && parts[0] == "revisions" && objectlocation.ValidID(parts[1]) {
				switch parts[2] {
				case "manifest.json", "body.md", "contract.json", "profile.snapshot.md", "source-manifest.json", "projection.json", "resources.json":
					return true
				}
			}
		}
		if l.ResourceBindingsRef != nil && path == l.ResourceBindingsRef.Path && strings.HasPrefix(path, ".content-registry/bindings/"+id+"/") {
			return true
		}
		if l.Placement.EvidenceRoot != "" && strings.HasPrefix(path, l.Placement.EvidenceRoot+"/") {
			name := strings.TrimPrefix(path, l.Placement.EvidenceRoot+"/")
			if len(name) == 68 && strings.HasSuffix(name, ".txt") && objectlocation.ValidID(name) {
				return true
			}
		}
	}
	if strings.HasPrefix(path, ".content-registry/resources/") && strings.Count(path, "/") == 2 && strings.HasSuffix(path, ".json") && objectlocation.ValidID(strings.TrimSuffix(filepath.Base(path), ".json")) {
		return true
	}
	if strings.HasPrefix(path, "topics/"+topic+"/attachments/") {
		parts := strings.Split(strings.TrimPrefix(path, "topics/"+topic+"/attachments/"), "/")
		return len(parts) == 2 && objectlocation.ValidID(parts[0]) && objectlocation.ValidResourceName(parts[1])
	}
	if strings.HasPrefix(path, "topics/"+topic+"/assets/") {
		return objectlocation.ValidID(filepath.Base(path)) && strings.Count(path, "/") == 3
	}
	if strings.HasPrefix(path, "topics/"+topic+"/references/") {
		parts := strings.Split(strings.TrimPrefix(path, "topics/"+topic+"/references/"), "/")
		return (len(parts) == 2 && objectlocation.ValidID(parts[0]) && (parts[1] == "snapshot.json" || parts[1] == "body.md")) || (len(parts) == 3 && objectlocation.ValidID(parts[0]) && parts[1] == "assets" && objectlocation.ValidID(parts[2]))
	}
	return false
}
func (s *Store) ImportPackage(ctx context.Context, operation string, raw []byte) (ImportResult, error) {
	var result ImportResult
	if !objectlocation.ValidID(operation) {
		return result, objectlocation.ErrInvalid
	}
	manifest, files, e := ReadPackage(raw)
	if e != nil {
		return result, e
	}
	r := objectlocation.For(s.rootPath)
	if e = r.Initialize(); e != nil {
		return result, e
	}
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	op, e := r.Begin(operation, "import", "", manifest.TopicID, objectlocation.Hash(raw))
	if e != nil {
		return result, e
	}
	if e = objectlocation.ReadJSON(s.rootPath, importBase(operation)+"/state.json", &result); e == nil {
		return s.resumeImport(ctx, &result)
	} else if !os.IsNotExist(e) {
		return result, e
	}
	op.EventType = "topic.import_completed"
	if e = r.Prepare(op); e != nil {
		return result, e
	}
	result.Operation = op
	result.TopicID = manifest.TopicID
	result.Manifest = manifest
	result.Objects = map[string]string{}
	result.Phase = "staging"
	if _, e = os.Stat(filepath.Join(s.rootPath, "topics", manifest.TopicID, "topic.yaml")); e == nil {
		return result, objectlocation.ErrConflict
	} else if !os.IsNotExist(e) {
		return result, e
	}
	objects := map[string]models.ObjectLocation{}
	for _, l := range manifest.Objects {
		if !objectlocation.ValidLocation(l) || l.OwnerTopicID != manifest.TopicID || l.Placement.ThoughtPath != "topics/"+manifest.TopicID+"/thoughts/"+l.ObjectID+".md" || l.Placement.DocumentRoot != "topics/"+manifest.TopicID+"/documents/"+l.ObjectID || (l.Placement.EvidenceRoot != "" && l.Placement.EvidenceRoot != "topics/"+manifest.TopicID+"/.private/evidence/"+l.ObjectID) || l.ObjectKind != "thought" || l.Lifecycle != "active" || l.DeletedAt != nil || (l.GlobalSearch != "inherit" && l.GlobalSearch != "include" && l.GlobalSearch != "exclude") {
			return result, objectlocation.ErrInvalid
		}
		if _, exists := objects[l.ObjectID]; exists {
			return result, objectlocation.ErrInvalid
		}
		if _, e = r.Lookup(l.ObjectID); e == nil {
			return result, objectlocation.ErrConflict
		} else if !os.IsNotExist(e) {
			return result, e
		}
		objects[l.ObjectID] = l
		result.Objects[l.ObjectID] = "staged"
	}
	stage := importBase(operation) + "/stage"
	for _, file := range manifest.Files {
		if !allowedImportPath(file.Path, manifest.TopicID, objects) {
			return result, objectlocation.ErrUnsafe
		}
		livePath, err := objectlocation.SafePath(s.rootPath, file.Path)
		if err != nil {
			return result, err
		}
		if _, err = os.Stat(livePath); err == nil {
			return result, objectlocation.ErrConflict
		} else if !os.IsNotExist(err) {
			return result, err
		}
		mode := os.FileMode(0644)
		if strings.Contains(file.Path, "/.private/") && !file.Private {
			return result, objectlocation.ErrUnsafe
		}
		if file.Private {
			if !manifest.Private || !strings.Contains(file.Path, "/.private/evidence/") {
				return result, objectlocation.ErrUnsafe
			}
			mode = 0600
		}
		if e = objectlocation.WriteFile(s.rootPath, stage+"/"+file.Path, files[file.Path], mode); e != nil {
			return result, e
		}
	}
	// Validate identities and immutable hash chains entirely inside staging.
	stageRoot, e := objectlocation.SafePath(s.rootPath, stage)
	if e != nil {
		return result, e
	}
	var importedTopic models.Topic
	var importedCatalog models.TopicCatalog
	if e = objectlocation.ReadJSON(stageRoot, "topics/"+manifest.TopicID+"/topic.yaml", &importedTopic); e != nil || importedTopic.ID != manifest.TopicID || importedTopic.Slug != manifest.TopicID || importedTopic.Mode != "collection" {
		return result, objectlocation.ErrInvalid
	}
	if e = objectlocation.ReadJSON(stageRoot, catalogPath(manifest.TopicID), &importedCatalog); e != nil || importedCatalog.TopicID != manifest.TopicID {
		return result, objectlocation.ErrInvalid
	}
	// Body identity is independent of the JSON record identity.
	for id, l := range objects {
		note, _ := markdown.ParseThought(files[l.Placement.ThoughtPath])
		if note.ID != id {
			return result, objectlocation.ErrInvalid
		}
		var stored models.ObjectLocation
		if e = json.Unmarshal(files[objectlocation.RecordPath(id)], &stored); e != nil || objectlocation.HashJSON(stored) != objectlocation.HashJSON(l) {
			return result, objectlocation.ErrInvalid
		}
		bindings, e := objectlocation.For(stageRoot).Bindings(id)
		if e != nil {
			return result, e
		}
		for _, binding := range bindings.Bindings {
			if binding.Private || !objectlocation.ValidID(binding.ResourceID) || !strings.HasPrefix(binding.Path, "topics/"+manifest.TopicID+"/attachments/"+binding.ResourceID+"/") {
				return result, objectlocation.ErrUnsafe
			}
			raw, ok := files[binding.Path]
			var resource models.TopicResource
			if e = json.Unmarshal(files[".content-registry/resources/"+binding.ResourceID+".json"], &resource); e != nil {
				return result, objectlocation.ErrInvalid
			}
			mime, err := objectlocation.ValidateResource(filepath.Base(binding.Path), raw)
			if err != nil || mime != resource.MIME || resource.ID != binding.ResourceID || resource.Hash != binding.Hash || resource.Size != int64(len(raw)) || !resource.Shareable {
				return result, objectlocation.ErrUnsafe
			}
			if !ok || objectlocation.Hash(raw) != binding.Hash {
				return result, objectlocation.ErrInvalid
			}
		}
		ds := documentstore.New(stageRoot)
		if h, e := ds.Head(id); e == nil {
			if h.FinalizationID != "" {
				if _, e = New(stageRoot).finalization(id, h.FinalizedRevisionID, h.FinalizationID); e != nil {
					return result, e
				}
			}
			seen := map[string]bool{}
			for _, start := range []string{h.WorkingRevisionID, h.FinalizedRevisionID} {
				for rev := start; rev != ""; {
					if seen[rev] {
						break
					}
					seen[rev] = true
					d, e := ds.Revision(id, rev)
					if e != nil {
						return result, e
					}
					rev = d.Revision.ParentRevisionID
				}
			}
		} else if !os.IsNotExist(e) {
			return result, e
		}
	}
	// External references and missing sources are relations, not objects owned by
	// this package. Keep them unresolved until the destination can resolve them.
	if !validCatalogShape(importedCatalog) {
		return result, objectlocation.ErrInvalid
	}
	if len(manifest.Relations) != len(importedCatalog.Entries) {
		return result, objectlocation.ErrInvalid
	}
	relations := map[string]PackageRelation{}
	for _, relation := range manifest.Relations {
		if _, duplicate := relations[relation.EntryID]; duplicate {
			return result, objectlocation.ErrInvalid
		}
		relations[relation.EntryID] = relation
	}
	checked := importedCatalog
	checked.Entries = append([]models.CatalogEntry{}, importedCatalog.Entries...)
	for i, entry := range checked.Entries {
		relation, ok := relations[entry.ID]
		if !ok || relation.ThoughtID != entry.Binding.ThoughtID {
			return result, objectlocation.ErrInvalid
		}
		if entry.Binding.Kind == "thought" {
			_, owned := objects[entry.Binding.ThoughtID]
			if owned && relation.Kind != "owned" {
				return result, objectlocation.ErrInvalid
			}
			if !owned {
				if relation.Kind != "external_reference" && relation.Kind != "missing" {
					return result, objectlocation.ErrInvalid
				}
				checked.Entries[i].Binding = models.CatalogBinding{Kind: "planned"}
			}
		} else if relation.Kind != entry.Binding.Kind {
			return result, objectlocation.ErrInvalid
		}
	}
	// Validate owned immutable versions and snapshots against staging only.
	if e = New(stageRoot).validateCatalog(ctx, checked); e != nil {
		return result, e
	}
	result.Phase = "validated"
	if e = objectlocation.WriteJSON(s.rootPath, importBase(operation)+"/state.json", result, true); e != nil {
		return result, e
	}
	if e = r.Checkpoint("import_validated"); e != nil {
		return result, e
	}
	return s.resumeImport(ctx, &result)
}
func (s *Store) resumeImport(ctx context.Context, result *ImportResult) (ImportResult, error) {
	r := objectlocation.For(s.rootPath)
	if op, e := r.Operation(result.Operation.ID); e == nil && op.State == "completed" {
		return s.finishImport(result)
	}
	if result.Phase == "conflicted" {
		return *result, objectlocation.ErrConflict
	}
	if err := s.checkImportTargets(result); err != nil {
		if errors.Is(err, objectlocation.ErrConflict) {
			result.Phase, result.Operation.State, result.Operation.Error = "conflicted", "conflicted", err.Error()
			if e := r.Prepare(result.Operation); e != nil {
				return *result, e
			}
			if e := objectlocation.WriteJSON(s.rootPath, importBase(result.Operation.ID)+"/state.json", result, true); e != nil {
				return *result, e
			}
		}
		return *result, err
	}
	stage := importBase(result.Operation.ID) + "/stage"
	save := func() error {
		return objectlocation.WriteJSON(s.rootPath, importBase(result.Operation.ID)+"/state.json", result, true)
	}
	for _, l := range result.Manifest.Objects {
		if result.Objects[l.ObjectID] == "active" {
			continue
		}
		live, e := r.Lookup(l.ObjectID)
		if e == nil && live.ImportOperationID != result.Operation.ID {
			return *result, objectlocation.ErrConflict
		}
		if e != nil && !os.IsNotExist(e) {
			return *result, e
		}
		l.Lifecycle = "import_pending"
		l.ImportOperationID = result.Operation.ID
		l.LastOperationID = result.Operation.ID
		if e = objectlocation.WriteJSON(s.rootPath, objectlocation.RecordPath(l.ObjectID), l, false); e != nil {
			return *result, e
		}
		result.Objects[l.ObjectID] = "import_pending"
		if e = save(); e != nil {
			return *result, e
		}
	}
	if e := r.Checkpoint("import_reserved"); e != nil {
		return *result, e
	}
	for _, file := range result.Manifest.Files {
		if strings.HasPrefix(file.Path, ".content-registry/objects/") || file.Path == "topics/"+result.TopicID+"/topic.yaml" || file.Path == catalogPath(result.TopicID) {
			continue
		}
		p, e := objectlocation.SafePath(s.rootPath, stage+"/"+file.Path)
		if e != nil {
			return *result, e
		}
		raw, e := os.ReadFile(p)
		if e != nil || objectlocation.Hash(raw) != file.Hash {
			return *result, objectlocation.ErrRecovery
		}
		mode := os.FileMode(0644)
		if file.Private {
			mode = 0600
		}
		if e = objectlocation.WriteFile(s.rootPath, file.Path, raw, mode); e != nil {
			return *result, e
		}
	}
	if e := r.Checkpoint("import_copied"); e != nil {
		return *result, e
	}
	for _, l := range result.Manifest.Objects {
		l.Lifecycle = "active"
		l.ImportOperationID = result.Operation.ID
		l.LastOperationID = result.Operation.ID
		if e := objectlocation.WriteJSON(s.rootPath, objectlocation.RecordPath(l.ObjectID), l, false); e != nil {
			return *result, e
		}
		result.Objects[l.ObjectID] = "active"
		if e := save(); e != nil {
			return *result, e
		}
	}
	var topic models.Topic
	if e := objectlocation.ReadJSON(s.rootPath, stage+"/topics/"+result.TopicID+"/topic.yaml", &topic); e != nil {
		return *result, e
	}
	if topic.ID != result.TopicID || topic.Slug != result.TopicID || topic.Mode != "collection" {
		return *result, objectlocation.ErrInvalid
	}
	topic.ImportOperationID = result.Operation.ID
	if e := s.writeTopic(topic); e != nil {
		return *result, e
	}
	var catalog models.TopicCatalog
	if e := objectlocation.ReadJSON(s.rootPath, stage+"/"+catalogPath(result.TopicID), &catalog); e != nil {
		return *result, e
	}
	if catalog.TopicID != result.TopicID {
		return *result, objectlocation.ErrInvalid
	}
	if e := objectlocation.WriteJSON(s.rootPath, catalogPath(result.TopicID), catalog, false); e != nil {
		return *result, e
	}
	result.Phase = "activating"
	if e := save(); e != nil {
		return *result, e
	}
	if e := r.Checkpoint("import_activated"); e != nil {
		return *result, e
	}
	if e := r.Complete(result.Operation); e != nil {
		return *result, e
	}
	return s.finishImport(result)
}
func (s *Store) RecoverImports(ctx context.Context) error {
	r := objectlocation.For(s.rootPath)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	dir, e := objectlocation.SafePath(s.rootPath, ".content-registry/private/imports")
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
		if !entry.IsDir() {
			continue
		}
		var result ImportResult
		e = objectlocation.ReadJSON(s.rootPath, importBase(entry.Name())+"/state.json", &result)
		if os.IsNotExist(e) {
			continue
		}
		if e != nil {
			return e
		}
		if result.Phase == "completed" || result.Phase == "conflicted" {
			continue
		}
		if _, e = s.resumeImport(ctx, &result); e != nil {
			return e
		}
	}
	return nil
}

// Completion cleanup merges into current metadata. It never replays staged
// document bytes after the parent operation has become visible.
func (s *Store) finishImport(result *ImportResult) (ImportResult, error) {
	r := objectlocation.For(s.rootPath)
	for _, original := range result.Manifest.Objects {
		l, e := r.Lookup(original.ObjectID)
		if e != nil {
			return *result, e
		}
		if l.ImportOperationID == result.Operation.ID {
			l.ImportOperationID = ""
			if e = objectlocation.WriteJSON(s.rootPath, objectlocation.RecordPath(l.ObjectID), l, false); e != nil {
				return *result, e
			}
		}
	}
	t, e := r.Topic(result.TopicID)
	if e != nil {
		return *result, e
	}
	if t.ImportOperationID == result.Operation.ID {
		t.ImportOperationID = ""
		if e = s.writeTopic(t); e != nil {
			return *result, e
		}
	}
	result.Operation.State = "completed"
	result.Phase = "completed"
	r.BumpEpoch()
	return *result, objectlocation.WriteJSON(s.rootPath, importBase(result.Operation.ID)+"/state.json", result, true)
}

func (s *Store) RetryImport(ctx context.Context, id string) (ImportResult, error) {
	var result ImportResult
	if !objectlocation.ValidID(id) {
		return result, objectlocation.ErrInvalid
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	if e := objectlocation.ReadJSON(s.rootPath, importBase(id)+"/state.json", &result); e != nil {
		return result, e
	}
	return s.resumeImport(ctx, &result)
}

func (s *Store) ImportStatus(id string) (ImportResult, error) {
	var result ImportResult
	if !objectlocation.ValidID(id) {
		return result, objectlocation.ErrInvalid
	}
	err := objectlocation.ReadJSON(s.rootPath, importBase(id)+"/state.json", &result)
	return result, err
}

// Every recovery attempt preflights every live destination before any write.
// Exact immutable bytes can be reused; mutable topic identity must be reserved
// by this operation. A foreign creation or edit is never replayed over.
func (s *Store) checkImportTargets(result *ImportResult) error {
	for _, file := range result.Manifest.Files {
		path, err := objectlocation.SafePath(s.rootPath, file.Path)
		if err != nil {
			return err
		}
		raw, err := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return err
		}
		if file.Path == "topics/"+result.TopicID+"/topic.yaml" {
			var live, staged models.Topic
			if json.Unmarshal(raw, &live) != nil || live.ImportOperationID != result.Operation.ID {
				return objectlocation.ErrConflict
			}
			if err = objectlocation.ReadJSON(s.rootPath, importBase(result.Operation.ID)+"/stage/"+file.Path, &staged); err != nil {
				return err
			}
			staged.ImportOperationID = result.Operation.ID
			if objectlocation.HashJSON(live) != objectlocation.HashJSON(staged) {
				return objectlocation.ErrConflict
			}
		} else if strings.HasPrefix(file.Path, ".content-registry/objects/") {
			var live models.ObjectLocation
			if json.Unmarshal(raw, &live) != nil || live.ImportOperationID != result.Operation.ID {
				return objectlocation.ErrConflict
			}
			id := strings.TrimSuffix(filepath.Base(file.Path), ".json")
			found := false
			for _, expected := range result.Manifest.Objects {
				if expected.ObjectID != id {
					continue
				}
				expected.Lifecycle, expected.ImportOperationID, expected.LastOperationID = live.Lifecycle, result.Operation.ID, result.Operation.ID
				if live.Lifecycle != "active" && live.Lifecycle != "import_pending" {
					return objectlocation.ErrConflict
				}
				if objectlocation.HashJSON(live) != objectlocation.HashJSON(expected) {
					return objectlocation.ErrConflict
				}
				found = true
			}
			if !found {
				return objectlocation.ErrConflict
			}
		} else if objectlocation.Hash(raw) != file.Hash || int64(len(raw)) != file.Size {
			return objectlocation.ErrConflict
		}
	}
	return nil
}
