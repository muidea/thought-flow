package objectlocation

import (
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
	"thoughtflow/internal/pkg/models"
)

// Rebuild is an explicit repair operation. It uses self-described identities,
// never titles or recency. Multiple candidates remain ambiguous.
func (r *Registry) Rebuild() error {
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	defer r.BumpEpoch()
	return r.discoverLocked()
}
func (r *Registry) scanThoughts() (map[string][]models.ObjectLocation, error) {
	found := map[string][]models.ObjectLocation{}
	bases := map[string]string{"thoughts": ""}
	topics, e := os.ReadDir(filepath.Join(r.Root, "topics"))
	if e != nil && !os.IsNotExist(e) {
		return nil, e
	}
	for _, topic := range topics {
		if !topic.IsDir() {
			continue
		}
		if !ValidID(topic.Name()) {
			return nil, ErrUnsafe
		}
		bases["topics/"+topic.Name()+"/thoughts"] = topic.Name()
	}
	for relative, owner := range bases {
		base, e := SafePath(r.Root, relative)
		if e != nil {
			return nil, e
		}
		e = filepath.WalkDir(base, func(path string, d fs.DirEntry, e error) error {
			if os.IsNotExist(e) {
				return nil
			}
			if e != nil {
				return e
			}
			if d.Type()&os.ModeSymlink != 0 {
				return ErrUnsafe
			}
			if d.IsDir() || filepath.Ext(path) != ".md" {
				return nil
			}
			id := strings.TrimSuffix(d.Name(), ".md")
			if !ValidID(id) {
				return ErrInvalid
			}
			raw, e := os.ReadFile(path)
			if e != nil {
				return e
			}
			parts := strings.SplitN(string(raw), "---", 3)
			if len(parts) != 3 {
				return ErrInvalid
			}
			var meta struct {
				ID string `yaml:"id"`
			}
			if e = yaml.Unmarshal([]byte(parts[1]), &meta); e != nil {
				return e
			}
			if meta.ID != "" && meta.ID != id {
				return ErrAmbiguous
			}
			rel, _ := filepath.Rel(r.Root, path)
			l := proposed(id, owner)
			l.Lifecycle = "active"
			// A missing authority record cannot prove its former visibility.
			// Repair restores identity while failing closed on global search.
			l.GlobalSearch = "exclude"
			l.Generation = 1
			l.Placement.ThoughtPath = filepath.ToSlash(rel)
			l.LastOperationID = "registry-rebuild"
			// A complete topic backup carries a location snapshot, including nondefault
			// policy, deletion and binding metadata. It can rebuild a missing registry.
			if owner != "" {
				var snapshot []models.ObjectLocation
				if e = ReadJSON(r.Root, "topics/"+owner+"/storage.snapshot.json", &snapshot); e == nil {
					for _, candidate := range snapshot {
						if candidate.ObjectID == id && candidate.Placement.ThoughtPath == l.Placement.ThoughtPath {
							l = candidate
						}
					}
				} else if !os.IsNotExist(e) {
					return e
				}
			}
			found[id] = append(found[id], l)
			return nil
		})
		if e != nil {
			return nil, e
		}
	}
	return found, nil
}
func (r *Registry) rebuildLocations() error {
	found, e := r.scanThoughts()
	if e != nil {
		return e
	}
	ambiguous := false
	for id, locations := range found {
		if len(locations) > 1 && r.knownMigrationCopies(id, locations) {
			continue
		}
		if len(locations) != 1 {
			if e = r.markAmbiguous(id, locations); e != nil {
				return e
			}
			ambiguous = true
			continue
		}
		l := locations[0]
		// Snapshots recover placement/bindings, but may predate a last policy
		// change. Reconfirm visibility explicitly after authority repair.
		l.GlobalSearch = "exclude"
		var existing models.ObjectLocation
		if e = ReadJSON(r.Root, RecordPath(id), &existing); e == nil {
			if existing.Placement.ThoughtPath != l.Placement.ThoughtPath {
				if e = r.markAmbiguous(id, locations); e != nil {
					return e
				}
				ambiguous = true
			} else if existing.Lifecycle == "ambiguous" && existing.DeletedAt == nil {
				var diagnosis ambiguityDiagnosis
				if e = ReadJSON(r.Root, ambiguityPath(id), &diagnosis); e == nil && (!diagnosis.HadAuthority || diagnosis.Authority != nil && diagnosis.Authority.Lifecycle != "active") {
					ambiguous = true
					continue
				} else if e != nil && !os.IsNotExist(e) {
					return e
				}
				if e = r.validateRecoveredLocation(existing); e != nil {
					return e
				}
				existing.Lifecycle = "active"
				existing.Generation++
				existing.LastOperationID = "registry-rebuild"
				if e = WriteJSON(r.Root, RecordPath(id), existing, false); e != nil {
					return e
				}
			}
			continue
		} else if !os.IsNotExist(e) {
			return e
		}
		if e = WriteJSON(r.Root, RecordPath(id), l, false); e != nil {
			return e
		}
	}
	if ambiguous {
		return ErrAmbiguous
	}
	return nil
}

// Promoted copies are not independent originals when the authoritative record
// and a durable migration agree on both exact locations.
func (r *Registry) knownMigrationCopies(id string, locations []models.ObjectLocation) bool {
	var current models.ObjectLocation
	if ReadJSON(r.Root, RecordPath(id), &current) != nil || !ValidLocation(current) {
		return false
	}
	dir, e := SafePath(r.Root, ".content-registry/private/migrations")
	if e != nil {
		return false
	}
	entries, e := os.ReadDir(dir)
	if e != nil {
		return false
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		var state migrationState
		if ReadJSON(r.Root, migrationPath(entry.Name()), &state) != nil || state.Plan.ObjectID != id {
			continue
		}
		source, target := state.Plan.Source.Placement.ThoughtPath, state.Plan.Target.Placement.ThoughtPath
		if current.Placement.ThoughtPath != source && current.Placement.ThoughtPath != target {
			continue
		}
		valid := true
		for _, l := range locations {
			if l.Placement.ThoughtPath != source && l.Placement.ThoughtPath != target {
				valid = false
			}
		}
		if valid {
			return true
		}
	}
	return false
}
