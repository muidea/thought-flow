package objectlocation

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"thoughtflow/internal/pkg/models"
)

func (r *Registry) Operation(id string) (models.WorkspaceOperation, error) {
	var op models.WorkspaceOperation
	if !ValidID(id) {
		return op, ErrInvalid
	}
	e := ReadJSON(r.Root, ".content-registry/private/operations/"+id+".json", &op)
	return op, e
}
func (r *Registry) Prepare(op models.WorkspaceOperation) error {
	if !ValidID(op.ID) {
		return ErrInvalid
	}
	op.UpdatedAt = time.Now().UTC()
	return WriteJSON(r.Root, ".content-registry/private/operations/"+op.ID+".json", op, true)
}
func (r *Registry) Begin(id, kind, object, topic string, request any) (models.WorkspaceOperation, error) {
	if !ValidID(id) {
		return models.WorkspaceOperation{}, ErrInvalid
	}
	hash := HashJSON(request)
	op, e := r.Operation(id)
	if e == nil {
		if op.RequestHash != hash || op.Kind != kind || op.ObjectID != object || op.TopicID != topic {
			return op, ErrOperationConflict
		}
		return op, nil
	}
	if !os.IsNotExist(e) {
		return op, e
	}
	op = models.WorkspaceOperation{ID: id, Kind: kind, ObjectID: object, TopicID: topic, RequestHash: hash, State: "prepared", Actor: "local", CreatedAt: time.Now().UTC()}
	return op, r.Prepare(op)
}
func (r *Registry) Complete(op models.WorkspaceOperation) error {
	op.State = "completed"
	op.UpdatedAt = time.Now().UTC()
	if e := WriteJSON(r.Root, ".content-registry/private/outbox/"+op.ID+".json", op, true); e != nil {
		return e
	}
	return r.Prepare(op)
}

// ReconcileIntents repairs the fact/outbox crash window by comparing the exact
// committed candidate bytes. Pending migrations have their own phase recovery.
func (r *Registry) ReconcileIntents() error {
	dir, e := SafePath(r.Root, ".content-registry/private/operations")
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
			continue
		}
		op, e := r.Operation(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return e
		}
		if op.State == "completed" || op.Kind == "migration" {
			continue
		}
		if op.TargetPath == "" {
			continue
		}
		var candidate any
		if e = ReadJSON(r.Root, op.TargetPath, &candidate); e != nil {
			if os.IsNotExist(e) {
				continue
			}
			return e
		}
		if HashJSON(candidate) != op.CandidateHash {
			continue
		}
		if e = r.Complete(op); e != nil {
			return e
		}
	}
	return nil
}
func (r *Registry) Activities(topic string) ([]models.WorkspaceOperation, error) {
	dir, e := SafePath(r.Root, ".content-registry/private/outbox")
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return []models.WorkspaceOperation{}, nil
	}
	if e != nil {
		return nil, e
	}
	out := []models.WorkspaceOperation{}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		var op models.WorkspaceOperation
		if e = ReadJSON(r.Root, ".content-registry/private/outbox/"+entry.Name(), &op); e != nil {
			return nil, e
		}
		if topic == "*" || op.TopicID == topic {
			out = append(out, op)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAt.Equal(out[j].UpdatedAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].UpdatedAt.Before(out[j].UpdatedAt)
	})
	return out, nil
}

func (r *Registry) Operations() ([]models.WorkspaceOperation, error) {
	dir, e := SafePath(r.Root, ".content-registry/private/operations")
	if e != nil {
		return nil, e
	}
	entries, e := os.ReadDir(dir)
	if os.IsNotExist(e) {
		return []models.WorkspaceOperation{}, nil
	}
	if e != nil {
		return nil, e
	}
	result := []models.WorkspaceOperation{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		op, e := r.Operation(strings.TrimSuffix(entry.Name(), ".json"))
		if e != nil {
			return nil, e
		}
		result = append(result, op)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].UpdatedAt.After(result[j].UpdatedAt) })
	return result, nil
}
