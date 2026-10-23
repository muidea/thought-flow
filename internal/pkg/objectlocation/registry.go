// Package objectlocation owns object identity, physical placement, mutation
// coordination and durable operation intents. Projections never choose routes.
package objectlocation

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"gopkg.in/yaml.v3"
	"thoughtflow/internal/pkg/models"
)

var (
	ErrConflict          = errors.New("generation_conflict")
	ErrOperationConflict = errors.New("operation_conflict")
	ErrAmbiguous         = errors.New("location_ambiguous")
	ErrUnsafe            = fmt.Errorf("unsafe_path: %w", os.ErrPermission)
	ErrRecovery          = errors.New("recovery_required")
	ErrInvalid           = errors.New("invalid_workspace_request")
)

const LayoutVersion = 1
const WriterVersion = 1

type Registry struct {
	Root         string
	Namespace    sync.RWMutex
	mu           sync.Mutex
	initialized  bool
	initErr      error
	objects      sync.Map
	migrations   sync.Map
	metricsMu    sync.Mutex
	metrics      map[string]float64
	privateRoots []string
	epoch        uint64
	Fault        func(string) error
}

var registries sync.Map

func For(root string) *Registry {
	if strings.TrimSpace(root) == "" {
		return &Registry{initialized: true, initErr: ErrInvalid}
	}
	absolute, _ := filepath.Abs(root)
	v, _ := registries.LoadOrStore(absolute, &Registry{Root: absolute})
	return v.(*Registry)
}
func (r *Registry) LockObject(id string) func() {
	v, _ := r.objects.LoadOrStore(id, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// Migration coordination is outside the namespace/object lock order. It only
// serializes migration workers; normal document edits remain possible while a
// worker copies its private staging files.
func (r *Registry) lockMigration(id string) func() {
	v, _ := r.migrations.LoadOrStore(id, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}
func (r *Registry) ReadLease(id string) func() {
	r.Namespace.RLock()
	unlock := r.LockObject(id)
	return func() { unlock(); r.Namespace.RUnlock() }
}
func (r *Registry) Epoch() uint64 { r.mu.Lock(); defer r.mu.Unlock(); return r.epoch }
func (r *Registry) BumpEpoch()    { r.mu.Lock(); r.epoch++; r.mu.Unlock() }
func RecordPath(id string) string { return ".content-registry/objects/" + id + ".json" }
func (r *Registry) Initialize() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.initialized {
		return r.initErr
	}
	r.initialized = true
	var schema struct {
		LayoutVersion        int `json:"layout_version"`
		MinimumWriterVersion int `json:"minimum_writer_version"`
	}
	e := ReadJSON(r.Root, ".content-registry/schema.json", &schema)
	if e == nil && (schema.LayoutVersion > LayoutVersion || schema.MinimumWriterVersion > WriterVersion) {
		r.initErr = fmt.Errorf("incompatible workspace layout: %w", ErrRecovery)
		return r.initErr
	}
	if e != nil && !os.IsNotExist(e) {
		r.initErr = e
		return e
	}
	if os.IsNotExist(e) {
		schema.LayoutVersion = LayoutVersion
		schema.MinimumWriterVersion = WriterVersion
		if e = r.discoverLocked(); e != nil {
			r.initErr = e
			return e
		}
		if e = WriteJSON(r.Root, ".content-registry/schema.json", schema, false); e != nil {
			r.initErr = e
			return e
		}

	}
	return r.initErr
}
func (r *Registry) discoverLocked() error { return r.rebuildLocations() }
func proposed(id, owner string) models.ObjectLocation {
	base := ""
	thought := "thoughts/unknown/unknown/" + id + ".md"
	if len(id) >= 6 {
		thought = "thoughts/" + id[:4] + "/" + id[4:6] + "/" + id + ".md"
	}
	if owner != "" {
		base = "topics/" + owner + "/"
		thought = base + "thoughts/" + id + ".md"
	}
	evidence := ".content-registry/private/evidence/" + id
	if owner != "" {
		evidence = base + ".private/evidence/" + id
	}
	return models.ObjectLocation{SchemaVersion: 1, ObjectID: id, ObjectKind: "thought", GlobalSearch: "inherit", Lifecycle: "reserved", OwnerTopicID: owner, Placement: models.ObjectPlacement{ThoughtPath: thought, DocumentRoot: base + "documents/" + id, EvidenceRoot: evidence}}
}
func (r *Registry) Lookup(id string) (models.ObjectLocation, error) {
	var l models.ObjectLocation
	if !ValidID(id) {
		return l, ErrInvalid
	}
	if e := r.Initialize(); e != nil {
		return l, e
	}
	e := ReadJSON(r.Root, RecordPath(id), &l)
	if e == nil {
		if l.ObjectID != id || l.SchemaVersion != 1 || !ValidLocation(l) {
			return l, ErrAmbiguous
		}
		for _, p := range []string{l.Placement.ThoughtPath, l.Placement.DocumentRoot, l.Placement.EvidenceRoot} {
			if p != "" {
				if _, e = SafePath(r.Root, p); e != nil {
					return l, e
				}
			}
		}
		if l.Lifecycle == "ambiguous" {
			r.AddMetric("location_conflicts_total", 1)
			return l, ErrAmbiguous
		}
		if l.Lifecycle == "recovery_required" {
			return l, ErrRecovery
		}
	}
	return l, e
}
func (r *Registry) Resolve(_ context.Context, id string) (models.ObjectLocation, error) {
	l, e := r.Lookup(id)
	if e == nil && l.ImportOperationID != "" {
		op, err := r.Operation(l.ImportOperationID)
		if err != nil || op.State != "completed" {
			return l, os.ErrNotExist
		}
	}
	if e == nil && (l.Lifecycle != "active" || l.DeletedAt != nil) {
		e = os.ErrNotExist
	}
	return l, e
}
func (r *Registry) Reserve(id, owner, operation string) (models.ObjectLocation, error) {
	if !ValidID(id) || (owner != "" && !ValidID(owner)) {
		return models.ObjectLocation{}, ErrInvalid
	}
	if e := r.Initialize(); e != nil {
		return models.ObjectLocation{}, e
	}
	if owner != "" {
		path, e := SafePath(r.Root, "topics/"+owner+"/topic.yaml")
		if e != nil {
			return models.ObjectLocation{}, e
		}
		data, e := os.ReadFile(path)
		if e != nil {
			return models.ObjectLocation{}, e
		}
		var t models.Topic
		if e = json.Unmarshal(data, &t); e != nil {
			if e = yaml.Unmarshal(data, &t); e != nil {
				return models.ObjectLocation{}, e
			}
		}
		if t.Mode != models.TopicModeCollection {
			return models.ObjectLocation{}, ErrInvalid
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	var l models.ObjectLocation
	e := ReadJSON(r.Root, RecordPath(id), &l)
	if e == nil {
		if l.DeletedAt != nil {
			return l, os.ErrNotExist
		}
		if owner != "" && l.OwnerTopicID != owner {
			return l, ErrConflict
		}
		return l, nil
	}
	if !os.IsNotExist(e) {
		return l, e
	}
	l = proposed(id, owner)
	l.LastOperationID = operation
	l.Generation = 1
	if e = r.CommitLocation(l, "object.reserved"); e != nil {
		return l, e
	}
	return l, nil
}

// Activate is called after the writer's read lease has been released.
func (r *Registry) Activate(id string) error {
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	l, e := r.Lookup(id)
	if e != nil {
		return e
	}
	if l.Lifecycle == "active" {
		return nil
	}
	if l.Lifecycle != "reserved" {
		return ErrRecovery
	}
	path, e := SafePath(r.Root, l.Placement.ThoughtPath)
	if e != nil {
		return e
	}
	if _, e = os.Stat(path); e != nil {
		return e
	}
	l.Lifecycle = "active"
	l.Generation++
	if e = r.CommitLocation(l, "object.created"); e != nil {
		return e
	}
	r.BumpEpoch()
	return nil
}
func (r *Registry) ListActive(ctx context.Context) ([]models.ObjectLocation, error) {
	return r.list(ctx, "", false)
}
func (r *Registry) ListOwned(ctx context.Context, topic string) ([]models.ObjectLocation, error) {
	return r.list(ctx, topic, true)
}
func (r *Registry) list(_ context.Context, topic string, owned bool) ([]models.ObjectLocation, error) {
	if e := r.Initialize(); e != nil {
		return nil, e
	}
	dir, e := SafePath(r.Root, ".content-registry/objects")
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return []models.ObjectLocation{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := []models.ObjectLocation{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		l, e := r.Lookup(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return nil, e
		}
		if l.Lifecycle == "active" && l.DeletedAt == nil && (!owned || l.OwnerTopicID == topic) {
			if l.ImportOperationID != "" {
				op, e := r.Operation(l.ImportOperationID)
				if e != nil || op.State != "completed" {
					continue
				}
			}
			out = append(out, l)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ObjectID < out[j].ObjectID })
	return out, nil
}
func (r *Registry) CommitLocation(l models.ObjectLocation, event string) error {
	if !ValidID(l.ObjectID) {
		return ErrInvalid
	}
	l.LastOperationID = ID("op-")
	op := models.WorkspaceOperation{ID: l.LastOperationID, Kind: "placement", ObjectID: l.ObjectID, AfterGeneration: l.Generation, TargetPath: RecordPath(l.ObjectID), CandidateHash: HashJSON(l), EventType: event, Actor: "local", State: "prepared", CreatedAt: time.Now().UTC()}
	if e := r.Prepare(op); e != nil {
		return e
	}
	if e := WriteJSON(r.Root, RecordPath(l.ObjectID), l, false); e != nil {
		return e
	}
	return r.Complete(op)
}
func (r *Registry) SetSearchPolicy(id string, req models.SearchPolicyRequest) (models.ObjectLocation, error) {
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	unlock := r.LockObject(id)
	defer unlock()
	l, e := r.Resolve(context.Background(), id)
	if e != nil {
		return l, e
	}
	if req.GlobalSearch != "inherit" && req.GlobalSearch != "include" && req.GlobalSearch != "exclude" {
		return l, ErrInvalid
	}
	op, e := r.Begin(req.OperationID, "search_policy", id, "", req)
	if e != nil {
		return l, e
	}
	if op.State == "completed" {
		return l, nil
	}
	if l.LastOperationID == req.OperationID && l.Generation == req.ExpectedGeneration+1 {
		return l, r.Complete(op)
	}
	if l.Generation != req.ExpectedGeneration {
		return l, ErrConflict
	}
	l.GlobalSearch = req.GlobalSearch
	l.Generation++
	l.LastOperationID = req.OperationID
	op.TargetPath = RecordPath(id)
	op.CandidateHash = HashJSON(l)
	op.AfterGeneration = l.Generation
	op.EventType = "thought.search_policy_updated"
	if e = r.Prepare(op); e != nil {
		return l, e
	}
	if e = WriteJSON(r.Root, RecordPath(id), l, false); e != nil {
		return l, e
	}
	r.BumpEpoch()
	return l, r.Complete(op)
}
func (r *Registry) MarkDeleted(id string) error {
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	l, e := r.Lookup(id)
	if e != nil {
		return e
	}
	if l.DeletedAt != nil {
		return nil
	}
	now := time.Now().UTC()
	l.DeletedAt = &now
	l.Lifecycle = "deleted"
	l.Generation++
	if e = r.CommitLocation(l, "object.deleted"); e == nil {
		r.BumpEpoch()
	}
	return e
}

func (r *Registry) ListRegistered() ([]models.ObjectLocation, error) {
	if e := r.Initialize(); e != nil {
		return nil, e
	}
	dir, e := SafePath(r.Root, ".content-registry/objects")
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return []models.ObjectLocation{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := []models.ObjectLocation{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		l, e := r.Lookup(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return nil, e
		}
		out = append(out, l)
	}
	return out, nil
}

// ValidLocation checks authority shape as well as path containment. A location
// record must never alias another document or turn private evidence into a body.
func ValidLocation(l models.ObjectLocation) bool {
	if !ValidID(l.ObjectID) || l.ObjectKind != "thought" || l.Generation == 0 || (l.OwnerTopicID != "" && !ValidID(l.OwnerTopicID)) {
		return false
	}
	if l.GlobalSearch != "inherit" && l.GlobalSearch != "include" && l.GlobalSearch != "exclude" {
		return false
	}
	expected := proposed(l.ObjectID, l.OwnerTopicID)
	if l.Placement.DocumentRoot != expected.Placement.DocumentRoot || l.Placement.EvidenceRoot != expected.Placement.EvidenceRoot {
		return false
	}
	if l.OwnerTopicID != "" {
		if l.Placement.ThoughtPath != expected.Placement.ThoughtPath {
			return false
		}
	} else {
		p := l.Placement.ThoughtPath
		if !strings.HasPrefix(p, "thoughts/") || filepath.Base(p) != l.ObjectID+".md" || filepath.ToSlash(filepath.Clean(p)) != p {
			return false
		}
	}
	switch l.Lifecycle {
	case "reserved", "active", "deleted", "import_pending", "ambiguous", "recovery_required":
	default:
		return false
	}
	if ref := l.ResourceBindingsRef; ref != nil {
		if !strings.HasPrefix(ref.Path, ".content-registry/bindings/"+l.ObjectID+"/") || filepath.Base(ref.Path) != strings.TrimPrefix(ref.Hash, "sha256:")+".json" {
			return false
		}
	}
	return true
}

// Checkpoint provides deterministic crash injection without changing persisted facts.
func (r *Registry) Checkpoint(phase string) error { return r.fault(phase) }

// ProtectPrivateRoot remembers configured runtime locations, including those
// nested below an otherwise approved attachment namespace.
func (r *Registry) ProtectPrivateRoot(path string) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, existing := range r.privateRoots {
		if existing == absolute {
			return
		}
	}
	r.privateRoots = append(r.privateRoots, absolute)
}
func (r *Registry) PublicResourcePath(relative string) bool {
	path, err := SafePath(r.Root, relative)
	if err != nil {
		return false
	}
	for _, part := range strings.Split(relative, "/") {
		if strings.HasPrefix(part, ".") {
			return false
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, private := range r.privateRoots {
		rel, err := filepath.Rel(private, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return false
		}
	}
	return true
}
