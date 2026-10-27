package documentsetstore

import (
	"encoding/json"
	"os"
	"path"
	"strings"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

// Package members never grant write access to arbitrary workspace files. Each
// admitted target belongs to a validated set object or an exact public record.
func allowedPath(name, set string, objects map[string]models.ObjectLocation) bool {
	if path.Clean(name) != name || strings.ContainsAny(name, "\\\x00\r\n") {
		return false
	}
	base := "topics/" + set + "/"
	if name == ".content-registry/schema.json" || name == base+"topic.yaml" || name == base+"catalog.json" || name == base+"catalog.md" || name == base+"index.md" {
		return true
	}
	if strings.HasPrefix(name, base+"document-set/") {
		parts := strings.Split(strings.TrimPrefix(name, base+"document-set/"), "/")
		if len(parts) == 1 {
			return parts[0] == "head.json"
		}
		if len(parts) == 2 && parts[0] == "finalizations" && documentstore.ValidID(strings.TrimSuffix(parts[1], ".json")) && strings.HasSuffix(parts[1], ".json") {
			return true
		}
		if len(parts) == 3 && parts[0] == "revisions" && documentstore.ValidID(parts[1]) {
			switch parts[2] {
			case "manifest.json", "plan.json", "shared-design.json", "validation.json":
				return true
			}
		}
		return false
	}
	for id, l := range objects {
		if name == objectlocation.RecordPath(id) || name == l.Placement.ThoughtPath {
			return true
		}
		if l.ResourceBindingsRef != nil && name == l.ResourceBindingsRef.Path {
			return true
		}
		if strings.HasPrefix(name, l.Placement.DocumentRoot+"/") {
			p := strings.Split(strings.TrimPrefix(name, l.Placement.DocumentRoot+"/"), "/")
			if len(p) == 1 {
				return p[0] == "head.json"
			}
			if len(p) == 2 && (p[0] == "reports" || p[0] == "finalizations") && documentstore.ValidID(strings.TrimSuffix(p[1], ".json")) && strings.HasSuffix(p[1], ".json") {
				return true
			}
			if len(p) == 3 && p[0] == "revisions" && documentstore.ValidID(p[1]) {
				switch p[2] {
				case "manifest.json", "contract.json", "source-manifest.json", "body.md", "profile.snapshot.md", "resources.json", "projection.json":
					return true
				}
			}
		}
	}
	if strings.HasPrefix(name, base+"references/") {
		p := strings.Split(strings.TrimPrefix(name, base+"references/"), "/")
		return len(p) == 2 && documentstore.ValidID(p[0]) && (p[1] == "snapshot.json" || p[1] == "body.md") || len(p) == 3 && documentstore.ValidID(p[0]) && p[1] == "assets" && documentstore.ValidID(p[2])
	}
	if strings.HasPrefix(name, base+"attachments/") {
		p := strings.Split(strings.TrimPrefix(name, base+"attachments/"), "/")
		return len(p) == 2 && objectlocation.ValidID(p[0]) && objectlocation.ValidResourceName(p[1])
	}
	if strings.HasPrefix(name, ".content-registry/resources/") {
		p := strings.Split(name, "/")
		return len(p) == 3 && documentstore.ValidID(strings.TrimSuffix(p[2], ".json")) && strings.HasSuffix(p[2], ".json")
	}
	return false
}

func validateStagedObjects(root, set string, files map[string][]byte, objects map[string]models.ObjectLocation) error {
	r := objectlocation.For(root)
	for id, l := range objects {
		t, _ := markdown.ParseThought(files[l.Placement.ThoughtPath])
		if t.ID != id {
			return ds.ErrInvalid
		}
		bindings, e := r.Bindings(id)
		if e != nil {
			return e
		}
		for _, b := range bindings.Bindings {
			if b.Private || !strings.HasPrefix(b.Path, "topics/"+set+"/attachments/"+b.ResourceID+"/") {
				return ds.ErrRules
			}
			raw, ok := files[b.Path]
			if !ok || objectlocation.Hash(raw) != b.Hash {
				return ds.ErrInvalid
			}
			var resource models.TopicResource
			if e = json.Unmarshal(files[".content-registry/resources/"+b.ResourceID+".json"], &resource); e != nil {
				return ds.ErrInvalid
			}
			mime, e := objectlocation.ValidateResource(path.Base(b.Path), raw)
			if e != nil || !resource.Shareable || resource.Hash != b.Hash || resource.ID != b.ResourceID || resource.Size != int64(len(raw)) || resource.MIME != mime {
				return ds.ErrInvalid
			}
		}
		store := documentstore.New(root)
		if _, e := store.Head(id); e == nil {
			if _, e = store.ContentPaths(id); e != nil {
				return e
			}
		} else if !os.IsNotExist(e) || t.DocumentProfile != nil {
			return ds.ErrInvalid
		}
	}
	var catalog models.TopicCatalog
	if e := objectlocation.ReadJSON(root, "topics/"+set+"/catalog.json", &catalog); e != nil {
		return e
	}
	if catalog.TopicID != set || catalog.Generation == 0 {
		return ds.ErrInvalid
	}
	for _, entry := range catalog.Entries {
		if entry.Binding.Kind != "snapshot" {
			continue
		}
		base := "topics/" + set + "/references/" + entry.Binding.SnapshotID
		if !documentstore.ValidID(entry.Binding.SnapshotID) {
			return ds.ErrInvalid
		}
		var snapshot models.TopicSnapshot
		if e := objectlocation.ReadJSON(root, base+"/snapshot.json", &snapshot); e != nil {
			return e
		}
		body, ok := files[base+"/body.md"]
		if !ok || snapshot.ID != entry.Binding.SnapshotID || snapshot.BodyHash != documentstore.Hash(body) || snapshot.Private {
			return ds.ErrInvalid
		}
		for _, b := range snapshot.Resources {
			raw, ok := files[base+"/assets/"+b.ResourceID]
			if !ok || b.Private || objectlocation.Hash(raw) != b.Hash {
				return ds.ErrInvalid
			}
		}
	}
	return nil
}
func validateImportObjects(files map[string][]byte, set string) (map[string]models.ObjectLocation, error) {
	objects := map[string]models.ObjectLocation{}
	for name, raw := range files {
		if !strings.HasPrefix(name, ".content-registry/objects/") {
			continue
		}
		var l models.ObjectLocation
		if e := ds.StrictJSON(raw, &l); e != nil || !objectlocation.ValidLocation(l) || l.OwnerTopicID != set || (l.ManagedSetID != "" && l.ManagedSetID != set) || name != objectlocation.RecordPath(l.ObjectID) || l.Lifecycle != "active" || l.DeletedAt != nil {
			return nil, ds.ErrInvalid
		}
		objects[l.ObjectID] = l
	}
	if len(objects) == 0 {
		return nil, ds.ErrInvalid
	}
	return objects, nil
}
