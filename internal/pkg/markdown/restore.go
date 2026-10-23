package markdown

import (
	"context"
	"os"
	"strings"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

type deletionSnapshot struct {
	ObjectID string `json:"object_id"`
	Hash     string `json:"hash"`
}
type RestoreRequest struct {
	OperationID        string `json:"operation_id"`
	ExpectedGeneration uint64 `json:"expected_generation"`
}
type restoreState struct {
	Operation  models.WorkspaceOperation `json:"operation"`
	Source     models.ObjectLocation     `json:"source"`
	Target     models.ObjectLocation     `json:"target"`
	SourceHead models.DocumentHead       `json:"source_head"`
	TargetHead models.DocumentHead       `json:"target_head"`
	Hash       string                    `json:"hash"`
}

func deletedBase(id string) string { return ".content-registry/private/deleted-thoughts/" + id }
func saveDeletionProjection(root, id, path string) error {
	raw, e := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
	if e != nil {
		return e
	}
	thought, _ := ParseThought(raw)
	if thought.ID != id {
		return objectlocation.ErrAmbiguous
	}
	if e = objectlocation.WriteFile(root, deletedBase(id)+"/projection.md", raw, 0600); e != nil {
		return e
	}
	return objectlocation.WriteJSON(root, deletedBase(id)+"/snapshot.json", deletionSnapshot{ObjectID: id, Hash: objectlocation.Hash(raw)}, true)
}
func RestoreThought(ctx context.Context, root, id string, req RestoreRequest) (models.ObjectLocation, error) {
	r := objectlocation.For(root)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	unlock := r.LockObject(id)
	defer unlock()
	l, e := r.Lookup(id)
	if e != nil {
		return l, e
	}
	op, e := r.Begin(req.OperationID, "thought_restore", id, l.OwnerTopicID, req)
	if e != nil {
		return l, e
	}
	if op.State == "completed" {
		return l, nil
	}
	var state restoreState
	statePath := ".content-registry/private/restores/" + req.OperationID + ".json"
	if e = objectlocation.ReadJSON(root, statePath, &state); os.IsNotExist(e) {
		if l.Generation != req.ExpectedGeneration || l.DeletedAt == nil || l.Lifecycle != "deleted" {
			return l, objectlocation.ErrConflict
		}
		if l.OwnerTopicID != "" {
			if _, e = r.Topic(l.OwnerTopicID); e != nil {
				return l, e
			}
		}
		var snapshot deletionSnapshot
		if e = objectlocation.ReadJSON(root, deletedBase(id)+"/snapshot.json", &snapshot); e != nil {
			return l, e
		}
		if snapshot.ObjectID != id {
			return l, objectlocation.ErrRecovery
		}
		var head models.DocumentHead
		if e = objectlocation.ReadJSON(root, l.Placement.DocumentRoot+"/head.json", &head); e != nil {
			return l, e
		}
		if head.DeletedAt == nil {
			return l, objectlocation.ErrRecovery
		}
		state = restoreState{Operation: op, Source: l, Target: l, SourceHead: head, TargetHead: head, Hash: snapshot.Hash}
		state.Target.Lifecycle = "active"
		state.Target.DeletedAt = nil
		state.Target.Generation++
		state.Target.LastOperationID = op.ID
		state.TargetHead.DeletedAt = nil
		state.TargetHead.Generation++
		state.TargetHead.PendingProjection = false
		state.Operation.TargetPath = objectlocation.RecordPath(id)
		state.Operation.CandidateHash = objectlocation.HashJSON(state.Target)
		state.Operation.EventType = "thought.restored"
		state.Operation.BeforeGeneration = l.Generation
		state.Operation.AfterGeneration = state.Target.Generation
		if e = r.Prepare(state.Operation); e != nil {
			return l, e
		}
		if e = objectlocation.WriteJSON(root, statePath, state, true); e != nil {
			return l, e
		}
	} else if e != nil {
		return l, e
	}
	if ctx.Err() != nil {
		return l, ctx.Err()
	}
	if e = resumeRestore(root, &state); e != nil {
		return l, e
	}
	return r.Lookup(id)
}
func resumeRestore(root string, state *restoreState) error {
	r := objectlocation.For(root)
	id := state.Source.ObjectID
	if !objectlocation.ValidLocation(state.Source) || !objectlocation.ValidLocation(state.Target) || state.Target.ObjectID != id {
		return objectlocation.ErrRecovery
	}
	l, e := r.Lookup(id)
	if e != nil {
		return e
	}
	if objectlocation.HashJSON(l) != objectlocation.HashJSON(state.Source) && objectlocation.HashJSON(l) != objectlocation.HashJSON(state.Target) {
		return objectlocation.ErrConflict
	}
	path, e := objectlocation.SafePath(root, deletedBase(id)+"/projection.md")
	if e != nil {
		return e
	}
	raw, e := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
	if e != nil {
		return e
	}
	if objectlocation.Hash(raw) != state.Hash {
		return objectlocation.ErrRecovery
	}
	thought, _ := ParseThought(raw)
	if thought.ID != id {
		return objectlocation.ErrAmbiguous
	}
	livePath, e := objectlocation.SafePath(root, l.Placement.ThoughtPath)
	if e != nil {
		return e
	}
	if old, e := objectlocation.ReadFileBounded(livePath, objectlocation.MaxResourceBytes); e == nil {
		if objectlocation.Hash(old) != state.Hash {
			return objectlocation.ErrConflict
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	var head models.DocumentHead
	headPath := l.Placement.DocumentRoot + "/head.json"
	e = objectlocation.ReadJSON(root, headPath, &head)
	if os.IsNotExist(e) && state.SourceHead.WorkingRevisionID == "" {
		head = state.TargetHead
	} else if e != nil {
		return e
	}
	if objectlocation.HashJSON(head) != objectlocation.HashJSON(state.SourceHead) && objectlocation.HashJSON(head) != objectlocation.HashJSON(state.TargetHead) {
		return objectlocation.ErrConflict
	}
	if e = objectlocation.WriteFile(root, l.Placement.ThoughtPath, raw, 0644); e != nil {
		return e
	}
	if e = r.Checkpoint("restore_projection"); e != nil {
		return e
	}
	if state.SourceHead.WorkingRevisionID == "" {
		path, err := objectlocation.SafePath(root, headPath)
		if err != nil {
			return err
		}
		if err = os.Remove(path); err != nil && !os.IsNotExist(err) {
			return err
		}
	} else if e = objectlocation.WriteJSON(root, headPath, state.TargetHead, false); e != nil {
		return e
	}
	if e = r.Checkpoint("restore_head"); e != nil {
		return e
	}
	if e = objectlocation.WriteJSON(root, objectlocation.RecordPath(id), state.Target, false); e != nil {
		return e
	}
	r.BumpEpoch()
	if e = r.Checkpoint("restore_location"); e != nil {
		return e
	}
	return r.Complete(state.Operation)
}
func RecoverRestores(root string) error {
	r := objectlocation.For(root)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	dir, e := objectlocation.SafePath(root, ".content-registry/private/restores")
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
		var state restoreState
		if e = objectlocation.ReadJSON(root, ".content-registry/private/restores/"+entry.Name(), &state); e != nil {
			return e
		}
		op, e := r.Operation(state.Operation.ID)
		if e != nil {
			return e
		}
		if op.State == "completed" {
			continue
		}
		unlock := r.LockObject(state.Source.ObjectID)
		e = resumeRestore(root, &state)
		unlock()
		if e != nil {
			return e
		}
	}
	return nil
}
