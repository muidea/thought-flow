package topicstore

import (
	"context"
	"strings"
	"time"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

type ModeConversionPlan struct {
	ID                string    `json:"preview_id"`
	TopicID           string    `json:"topic_id"`
	From              string    `json:"from"`
	To                string    `json:"to"`
	Generation        uint64    `json:"generation"`
	CatalogGeneration uint64    `json:"catalog_generation"`
	IndexHash         string    `json:"index_hash"`
	PlanHash          string    `json:"plan_hash"`
	CreatedAt         time.Time `json:"created_at"`
	Members           []string  `json:"members"`
}
type ModeConversionRequest struct {
	OperationID string `json:"operation_id"`
	PreviewID   string `json:"preview_id"`
	PlanHash    string `json:"plan_hash"`
}

func (s *Store) PreviewConversion(ctx context.Context, id, mode string) (ModeConversionPlan, error) {
	var plan ModeConversionPlan
	r := objectlocation.For(s.rootPath)
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	t, e := s.Get(ctx, id)
	if e != nil {
		return plan, e
	}
	if mode != "aggregate" && mode != "collection" || mode == t.Mode {
		return plan, objectlocation.ErrInvalid
	}
	document, e := s.ReadDocument(ctx, id)
	if e != nil {
		return plan, e
	}
	plan = ModeConversionPlan{ID: objectlocation.ID("conversion-"), TopicID: id, From: t.Mode, To: mode, Generation: t.Generation, IndexHash: objectlocation.Hash([]byte(document)), CreatedAt: time.Now().UTC(), Members: t.Members}
	if t.Mode == "collection" {
		c, e := s.Catalog(ctx, id)
		if e != nil {
			return plan, e
		}
		plan.CatalogGeneration = c.Generation
	}
	plan.PlanHash = objectlocation.HashJSON(plan)
	return plan, objectlocation.WriteJSON(s.rootPath, ".content-registry/private/conversions/"+plan.ID+".json", plan, true)
}
func (s *Store) ConvertMode(ctx context.Context, id string, req ModeConversionRequest) (models.Topic, error) {
	var t models.Topic
	if !objectlocation.ValidID(req.PreviewID) {
		return t, objectlocation.ErrInvalid
	}
	var plan ModeConversionPlan
	if e := objectlocation.ReadJSON(s.rootPath, ".content-registry/private/conversions/"+req.PreviewID+".json", &plan); e != nil {
		return t, e
	}
	hash := plan.PlanHash
	plan.PlanHash = ""
	if hash != objectlocation.HashJSON(plan) || hash != req.PlanHash || plan.TopicID != id {
		return t, objectlocation.ErrConflict
	}
	r := objectlocation.For(s.rootPath)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	unlock := s.lock(id)
	defer unlock()
	t, e := s.Get(ctx, id)
	if e != nil {
		return t, e
	}
	op, e := r.Begin(req.OperationID, "mode_conversion", "", id, req)
	if e != nil {
		return t, e
	}
	if op.State == "completed" {
		return t, nil
	}
	if t.LastOperationID == req.OperationID && t.Generation == plan.Generation+1 && t.Mode == plan.To {
		return t, r.Complete(op)
	}
	if t.Generation != plan.Generation || t.Mode != plan.From {
		return t, objectlocation.ErrConflict
	}
	document, e := s.ReadDocument(ctx, id)
	if e != nil {
		return t, e
	}
	if objectlocation.Hash([]byte(document)) != plan.IndexHash {
		return t, objectlocation.ErrConflict
	}
	if plan.To == "collection" {
		c := initialCatalog(id, req.OperationID)
		for _, member := range t.Members {
			c.Entries = append(c.Entries, models.CatalogEntry{ID: "entry-" + strings.TrimPrefix(objectlocation.Hash([]byte(member)), "sha256:")[:32], GroupID: "default", Title: member, Required: false, Binding: models.CatalogBinding{Kind: "thought", ThoughtID: member, VersionPolicy: "latest_finalized"}})
		}
		if e = objectlocation.WriteJSON(s.rootPath, catalogPath(id), c, false); e != nil {
			return t, e
		}
	} else {
		c, e := s.Catalog(ctx, id)
		if e != nil {
			return t, e
		}
		if c.Generation != plan.CatalogGeneration {
			return t, objectlocation.ErrConflict
		}
		owned, e := r.ListOwned(ctx, id)
		if e != nil {
			return t, e
		}
		if len(owned) > 0 {
			return t, objectlocation.ErrConflict
		}
		if e = objectlocation.WriteJSON(s.rootPath, "topics/"+id+"/catalog.archived.json", c, false); e != nil {
			return t, e
		}
	}
	t.Mode = plan.To
	t.AutoWeave = false
	t.Generation++
	t.LastOperationID = req.OperationID
	t.UpdatedAt = time.Now().UTC()
	op.TargetPath = "topics/" + id + "/topic.yaml"
	op.CandidateHash = objectlocation.HashJSON(t)
	op.EventType = "topic.mode_changed"
	if e = r.Prepare(op); e != nil {
		return t, e
	}
	if e = s.writeTopic(t); e != nil {
		return t, e
	}
	r.BumpEpoch()
	return t, r.Complete(op)
}
