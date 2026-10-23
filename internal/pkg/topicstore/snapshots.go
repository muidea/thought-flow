package topicstore

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"time"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

type SnapshotRequest struct {
	OperationID    string `json:"operation_id"`
	ThoughtID      string `json:"thought_id"`
	RevisionID     string `json:"revision_id"`
	FinalizationID string `json:"finalization_id"`
}

func (s *Store) CreateSnapshot(ctx context.Context, topic string, req SnapshotRequest) (models.TopicSnapshot, error) {
	var snapshot models.TopicSnapshot
	r := objectlocation.For(s.rootPath)
	unlock := r.ReadLease(req.ThoughtID)
	if _, e := r.Resolve(ctx, req.ThoughtID); e != nil {
		unlock()
		return snapshot, e
	}
	f, e := s.finalization(req.ThoughtID, req.RevisionID, req.FinalizationID)
	if e != nil {
		unlock()
		return snapshot, e
	}
	d, e := documentstore.New(s.rootPath).Revision(req.ThoughtID, f.RevisionID)
	if e != nil {
		unlock()
		return snapshot, e
	}
	if d.ResourceError != "" {
		unlock()
		return snapshot, objectlocation.ErrRecovery
	}
	resources := map[string][]byte{}
	for _, b := range d.Resources.Bindings {
		if b.Private {
			unlock()
			return snapshot, objectlocation.ErrUnsafe
		}
		path, e := r.BoundResourcePath(req.ThoughtID, b)
		if e != nil {
			unlock()
			return snapshot, e
		}
		raw, e := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
		if e != nil || objectlocation.Hash(raw) != b.Hash {
			unlock()
			return snapshot, objectlocation.ErrRecovery
		}
		resources[b.ResourceID] = raw
	}
	unlock()
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	t, e := s.Get(ctx, topic)
	if e != nil {
		return snapshot, e
	}
	if t.Mode != models.TopicModeCollection {
		return snapshot, ErrCollectionOnly
	}
	if _, e = r.Resolve(ctx, req.ThoughtID); e != nil {
		return snapshot, e
	}
	op, e := r.Begin(req.OperationID, "snapshot", req.ThoughtID, topic, req)
	if e != nil {
		return snapshot, e
	}
	id := "snapshot-" + strings.TrimPrefix(objectlocation.Hash([]byte(req.OperationID)), "sha256:")[:32]
	base := "topics/" + topic + "/references/" + id
	if op.State == "completed" {
		e = objectlocation.ReadJSON(s.rootPath, base+"/snapshot.json", &snapshot)
		return snapshot, e
	}
	snapshot = models.TopicSnapshot{DeliveryLevel: f.DeliveryLevel, ID: id, SourceThoughtID: req.ThoughtID, RevisionID: f.RevisionID, FinalizationID: f.FinalizationID, BodyHash: objectlocation.Hash([]byte(d.Body)), Complete: "complete", CreatedAt: time.Now().UTC(), Resources: []models.ResourceBinding{}}
	snapshot.ResourceIssues = objectlocation.OfflineResourceIssues(d.Body, d.Resources.Bindings)
	if len(snapshot.ResourceIssues) > 0 {
		snapshot.Complete = "partial"
	}
	for _, b := range d.Resources.Bindings {
		b.Path = base + "/assets/" + b.ResourceID + filepath.Ext(b.Path)
		if e = objectlocation.WriteFile(s.rootPath, b.Path, resources[b.ResourceID], 0644); e != nil {
			return snapshot, e
		}
		snapshot.Resources = append(snapshot.Resources, b)
	}
	if e = objectlocation.WriteFile(s.rootPath, base+"/body.md", []byte(d.Body), 0644); e != nil {
		return snapshot, e
	}
	op.TargetPath = base + "/snapshot.json"
	op.CandidateHash = objectlocation.HashJSON(snapshot)
	op.EventType = "topic.snapshot_created"
	if e = r.Prepare(op); e != nil {
		return snapshot, e
	}
	if e = objectlocation.WriteJSON(s.rootPath, op.TargetPath, snapshot, false); e != nil {
		return snapshot, e
	}
	return snapshot, r.Complete(op)
}
func (s *Store) ReadSnapshotResource(ctx context.Context, topic, id, resource string) ([]byte, string, error) {
	if !objectlocation.ValidID(topic) || !objectlocation.ValidID(id) || !objectlocation.ValidID(resource) {
		return nil, "", objectlocation.ErrInvalid
	}
	var snapshot models.TopicSnapshot
	base := "topics/" + topic + "/references/" + id
	if _, e := s.Get(ctx, topic); e != nil {
		return nil, "", e
	}
	if e := objectlocation.ReadJSON(s.rootPath, base+"/snapshot.json", &snapshot); e != nil {
		return nil, "", e
	}
	if snapshot.ID != id || snapshot.Private {
		return nil, "", objectlocation.ErrUnsafe
	}
	for _, b := range snapshot.Resources {
		if b.ResourceID != resource || b.Private {
			continue
		}
		if !strings.HasPrefix(b.Path, base+"/assets/") {
			return nil, "", objectlocation.ErrUnsafe
		}
		path, e := objectlocation.SafePath(s.rootPath, b.Path)
		if e != nil {
			return nil, "", e
		}
		raw, e := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
		if e != nil {
			return nil, "", e
		}
		if _, e = objectlocation.ValidateResource(filepath.Base(b.Path), raw); e != nil {
			return nil, "", e
		}
		if objectlocation.Hash(raw) != b.Hash {
			return nil, "", objectlocation.ErrRecovery
		}
		return raw, b.Path, nil
	}
	return nil, "", os.ErrNotExist
}
