package topicstore

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"thoughtflow/internal/pkg/documentflow"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/documentvalidation"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

var ErrCollectionOnly = errors.New("operation requires a collection topic")
var ErrAggregateOnly = errors.New("weaving is unavailable for collection topics")
var ErrRevisionUnavailable = errors.New("required_revision_unavailable")

func DefaultSearchPolicy() *models.TopicSearchPolicy {
	return &models.TopicSearchPolicy{TopicGlobalVisible: true, OwnedThoughtsGlobalDefault: true}
}
func catalogPath(id string) string { return "topics/" + id + "/catalog.json" }
func initialCatalog(id, op string) models.TopicCatalog {
	return models.TopicCatalog{SchemaVersion: 1, TopicID: id, Generation: 1, RequiredDeliveryLevel: "ready_for_use", Groups: []models.CatalogGroup{{ID: "default", Title: "Documents"}}, Entries: []models.CatalogEntry{}, LastOperationID: op}
}
func (s *Store) Catalog(ctx context.Context, id string) (models.TopicCatalog, error) {
	var c models.TopicCatalog
	if !objectlocation.ValidID(id) {
		return c, objectlocation.ErrInvalid
	}
	e := objectlocation.ReadJSON(s.rootPath, catalogPath(id), &c)
	if e != nil {
		return c, e
	}
	if c.TopicID != id || c.SchemaVersion != 1 || !validCatalogShape(c) {
		return c, objectlocation.ErrInvalid
	}
	return c, nil
}
func (s *Store) validateCatalog(ctx context.Context, c models.TopicCatalog) error {
	if len(c.Groups) == 0 || len(c.Groups) > 100 || len(c.Entries) > 5000 {
		return objectlocation.ErrInvalid
	}
	if c.RequiredDeliveryLevel != "proposal" && c.RequiredDeliveryLevel != "ready_for_use" {
		return objectlocation.ErrInvalid
	}
	groups, entries, thoughts := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, g := range c.Groups {
		if !objectlocation.ValidID(g.ID) || strings.TrimSpace(g.Title) == "" || groups[g.ID] {
			return objectlocation.ErrInvalid
		}
		groups[g.ID] = true
	}
	for _, entry := range c.Entries {
		if !objectlocation.ValidID(entry.ID) || entries[entry.ID] || !groups[entry.GroupID] || strings.TrimSpace(entry.Title) == "" {
			return objectlocation.ErrInvalid
		}
		entries[entry.ID] = true
		b := entry.Binding
		switch b.Kind {
		case "planned":
			if b.ThoughtID != "" || b.SnapshotID != "" || b.RevisionID != "" || b.FinalizationID != "" {
				return objectlocation.ErrInvalid
			}
		case "thought":
			if !objectlocation.ValidID(b.ThoughtID) || thoughts[b.ThoughtID] || b.SnapshotID != "" {
				return objectlocation.ErrInvalid
			}
			thoughts[b.ThoughtID] = true
			if _, e := objectlocation.For(s.rootPath).Resolve(ctx, b.ThoughtID); e != nil {
				return e
			}
			if b.VersionPolicy != "latest_finalized" && b.VersionPolicy != "pinned_finalized" {
				return objectlocation.ErrInvalid
			}
			if b.VersionPolicy == "latest_finalized" && (b.RevisionID != "" || b.FinalizationID != "") {
				return objectlocation.ErrInvalid
			}
			if b.VersionPolicy == "pinned_finalized" {
				if !objectlocation.ValidID(b.RevisionID) || !objectlocation.ValidID(b.FinalizationID) {
					return objectlocation.ErrInvalid
				}
				ds := documentstore.New(s.rootPath)
				f, e := ds.Finalization(b.ThoughtID, b.FinalizationID)
				if e != nil || f.RevisionID != b.RevisionID {
					return ErrRevisionUnavailable
				}
				if _, e = documentflow.For(s.rootPath).Export(b.ThoughtID, b.RevisionID, "final"); e != nil {
					return ErrRevisionUnavailable
				}
			}
		case "snapshot":
			var snap models.TopicSnapshot
			if !objectlocation.ValidID(b.SnapshotID) {
				return objectlocation.ErrInvalid
			}
			if e := objectlocation.ReadJSON(s.rootPath, "topics/"+c.TopicID+"/references/"+b.SnapshotID+"/snapshot.json", &snap); e != nil {
				return e
			}
			if snap.ID != b.SnapshotID || snap.Private || snap.Complete != "complete" {
				return objectlocation.ErrUnsafe
			}
			path, e := objectlocation.SafePath(s.rootPath, "topics/"+c.TopicID+"/references/"+b.SnapshotID+"/body.md")
			if e != nil {
				return e
			}
			raw, e := objectlocation.ReadFileBounded(path, objectlocation.MaxResourceBytes)
			if e != nil {
				return e
			}
			if objectlocation.Hash(raw) != snap.BodyHash {
				return objectlocation.ErrRecovery
			}
			for _, resource := range snap.Resources {
				if resource.Private {
					return objectlocation.ErrUnsafe
				}
				if _, _, e = s.ReadSnapshotResource(ctx, c.TopicID, b.SnapshotID, resource.ResourceID); e != nil {
					return e
				}
			}
		default:
			return objectlocation.ErrInvalid
		}
	}
	return nil
}
func (s *Store) WriteCatalog(ctx context.Context, id string, req models.CatalogWriteRequest) (models.CatalogView, error) {
	r := objectlocation.For(s.rootPath)
	r.Namespace.Lock()
	unlock := s.lock(id)
	err := func() error {
		t, e := s.Get(ctx, id)
		if e != nil {
			return e
		}
		if t.Mode != models.TopicModeCollection {
			return ErrCollectionOnly
		}
		op, e := r.Begin(req.OperationID, "catalog", "", id, req)
		if e != nil {
			return e
		}
		if op.State == "completed" {
			return nil
		}
		old, e := s.Catalog(ctx, id)
		if e != nil {
			return e
		}
		if old.LastOperationID == req.OperationID && old.Generation == req.ExpectedGeneration+1 {
			return r.Complete(op)
		}
		if old.Generation != req.ExpectedGeneration {
			return objectlocation.ErrConflict
		}
		level := req.RequiredDeliveryLevel
		if level == "" {
			level = old.RequiredDeliveryLevel
		}
		c := models.TopicCatalog{SchemaVersion: 1, TopicID: id, Generation: old.Generation + 1, Groups: req.Groups, Entries: req.Entries, RequiredDeliveryLevel: level, LastOperationID: req.OperationID}
		if e = s.validateCatalogUpdateLocked(ctx, c, old); e != nil {
			return e
		}
		op.BeforeGeneration = old.Generation
		op.AfterGeneration = c.Generation
		op.TargetPath = catalogPath(id)
		op.CandidateHash = objectlocation.HashJSON(c)
		op.EventType = "topic.catalog_updated"
		if e = r.Prepare(op); e != nil {
			return e
		}
		if e = objectlocation.WriteJSON(s.rootPath, catalogPath(id), c, false); e != nil {
			return e
		}
		r.BumpEpoch()
		return r.Complete(op)
	}()
	unlock()
	r.Namespace.Unlock()
	if err != nil {
		return models.CatalogView{}, err
	}
	_, err = s.RebuildCatalog(ctx, id)
	if err != nil {
		view, e := s.CatalogView(ctx, id)
		if e != nil {
			return view, e
		}
		view.ProjectionStatus = "failed"
		return view, nil
	}
	return s.CatalogView(ctx, id)
}

// Validation under the namespace write lock uses immutable records directly;
// Export would acquire a nested read lease and is therefore deliberately avoided.
func (s *Store) validateCatalogLocked(ctx context.Context, c models.TopicCatalog) error {
	for _, entry := range c.Entries {
		if entry.Binding.Kind == "thought" && entry.Binding.VersionPolicy == "pinned_finalized" {
			if _, e := s.finalization(entry.Binding.ThoughtID, entry.Binding.RevisionID, entry.Binding.FinalizationID); e != nil {
				return e
			}
		}
	}
	copy := c
	copy.Entries = append([]models.CatalogEntry{}, c.Entries...)
	for i := range copy.Entries {
		if copy.Entries[i].Binding.VersionPolicy == "pinned_finalized" {
			copy.Entries[i].Binding.VersionPolicy = "latest_finalized"
			copy.Entries[i].Binding.RevisionID = ""
			copy.Entries[i].Binding.FinalizationID = ""
		}
	}
	return s.validateCatalog(ctx, copy)
}
func (s *Store) finalization(id, rev, key string) (models.FinalizationRecord, error) {
	ds := documentstore.New(s.rootPath)
	h, e := ds.ActiveHead(id)
	if e != nil {
		return models.FinalizationRecord{}, e
	}
	seen := map[string]bool{}
	for current := h.FinalizationID; current != ""; {
		if seen[current] {
			return models.FinalizationRecord{}, objectlocation.ErrInvalid
		}
		seen[current] = true
		f, e := ds.Finalization(id, current)
		if e != nil {
			return f, e
		}
		if (rev == "" || f.RevisionID == rev) && (key == "" || f.FinalizationID == key) {
			d, e := ds.Revision(id, f.RevisionID)
			if e != nil {
				return f, e
			}
			report, e := ds.Report(id, f.ReportID)
			if e != nil || report.DocumentID != id || report.RevisionID != d.Revision.RevisionID || report.ValidationKey != f.ValidationKey {
				return f, ErrRevisionUnavailable
			}
			return f, nil
		}
		current = f.PreviousFinalizationID
	}
	return models.FinalizationRecord{}, ErrRevisionUnavailable
}
func (s *Store) CatalogView(ctx context.Context, id string) (models.CatalogView, error) {
	t, e := s.Get(ctx, id)
	if e != nil {
		return models.CatalogView{}, e
	}
	if t.Mode != models.TopicModeCollection {
		return models.CatalogView{}, ErrCollectionOnly
	}
	c, e := s.Catalog(ctx, id)
	if e != nil {
		return models.CatalogView{}, e
	}
	view := models.CatalogView{WorkspaceKey: strings.TrimPrefix(objectlocation.Hash([]byte(s.rootPath)), "sha256:")[:24], Topic: t, Catalog: c, Entries: []models.CatalogEntryView{}, Uncatalogued: []models.ObjectLocation{}, ProjectionStatus: "current"}
	bound := map[string]bool{}
	for _, entry := range c.Entries {
		v := s.entryView(ctx, id, entry)
		view.Entries = append(view.Entries, v)
		if entry.Binding.ThoughtID != "" {
			bound[entry.Binding.ThoughtID] = true
		}
		if entry.Required {
			view.RequiredCount++
			if (v.BindingStatus == "bound" || v.BindingStatus == "snapshot") && v.Availability == "available" && (v.DeliveryStatus == "ready_for_use" || (c.RequiredDeliveryLevel == "proposal" && v.DeliveryStatus == "proposal")) {
				view.DeliveredCount++
			}
		}
	}
	owned, e := objectlocation.For(s.rootPath).ListRegistered()
	if e != nil {
		return view, e
	}
	for _, l := range owned {
		if l.OwnerTopicID == id && l.Lifecycle == "deleted" {
			view.Deleted = append(view.Deleted, l)
		}
		if l.OwnerTopicID == id && l.Lifecycle != "deleted" && !bound[l.ObjectID] {
			view.Uncatalogued = append(view.Uncatalogued, l)
		}
	}
	view.ProjectionHash = objectlocation.Hash([]byte(renderCatalog(view)))
	path, e := objectlocation.SafePath(s.rootPath, "topics/"+id+"/catalog.md")
	if e != nil {
		return view, e
	}
	data, e := os.ReadFile(path)
	if e != nil || objectlocation.Hash(data) != view.ProjectionHash {
		view.ProjectionStatus = "reconciling"
	}
	return view, nil
}
func (s *Store) entryView(ctx context.Context, topic string, entry models.CatalogEntry) models.CatalogEntryView {
	v := models.CatalogEntryView{Entry: entry, BindingStatus: entry.Binding.Kind, DeliveryStatus: "not_finalized", WorkStatus: "no_new_draft", VersionStatus: entry.Binding.VersionPolicy, ValidationStatus: "unavailable", Availability: "unavailable"}
	if entry.Binding.Kind == "planned" {
		return v
	}
	if entry.Binding.Kind == "snapshot" {
		v.BindingStatus = "snapshot"
		var snap models.TopicSnapshot
		base := "topics/" + topic + "/references/" + entry.Binding.SnapshotID
		if e := objectlocation.ReadJSON(s.rootPath, base+"/snapshot.json", &snap); e != nil {
			v.Error = e.Error()
			return v
		}
		path, e := objectlocation.SafePath(s.rootPath, base+"/body.md")
		if e != nil {
			v.Error = e.Error()
			return v
		}
		raw, e := os.ReadFile(path)
		if e != nil || snap.Private || objectlocation.Hash(raw) != snap.BodyHash {
			return v
		}
		for _, b := range snap.Resources {
			if _, _, e = s.ReadSnapshotResource(ctx, topic, snap.ID, b.ResourceID); e != nil {
				return v
			}
		}
		v.SelectedRevisionID = snap.RevisionID
		v.SelectedFinalizationID = snap.FinalizationID
		v.Availability = "available"
		v.DeliveryStatus = snap.DeliveryLevel
		return v
	}
	id := entry.Binding.ThoughtID
	l, e := objectlocation.For(s.rootPath).Resolve(ctx, id)
	if e != nil {
		v.BindingStatus = "missing"
		v.Error = e.Error()
		return v
	}
	v.Location = &l
	t, c, e := markdown.ReadThought(s.rootPath, id)
	if e != nil {
		v.BindingStatus = "missing"
		v.Error = e.Error()
		return v
	}
	_ = c
	v.BindingStatus = "bound"
	v.OriginalTitle = t.DisplayTitle
	v.Profile = t.DocumentProfile
	v.UpdatedAt = t.UpdatedAt
	v.Availability = "available"
	flow := documentflow.For(s.rootPath)
	unlock := flow.Store.Lock(id)
	defer unlock()
	h, e := flow.Store.ActiveHead(id)
	if os.IsNotExist(e) {
		v.DeliveryStatus = "ordinary_note"
		return v
	}
	if e != nil {
		v.Error = e.Error()
		v.Availability = "unavailable"
		return v
	}
	v.WorkingRevisionID = h.WorkingRevisionID
	v.HasNewDraft = h.FinalizedRevisionID != "" && h.WorkingRevisionID != h.FinalizedRevisionID
	working, e := flow.Get(id)
	if e != nil {
		v.Availability = "unavailable"
		v.Error = e.Error()
		return v
	}
	if flow.ValidationRunning(id, h.WorkingRevisionID) {
		v.WorkStatus = "validation_running"
	}
	if v.WorkStatus != "validation_running" && working.Report != nil && working.Report.RevisionID == h.WorkingRevisionID {
		if working.Freshness == "current" && working.Report.Status == "failed" {
			v.WorkStatus = "validation_failed"
		} else if working.Freshness == "current" && working.Report.Status == "needs_review" {
			v.WorkStatus = "needs_review"
		}
	}
	f, e := s.finalization(id, entry.Binding.RevisionID, entry.Binding.FinalizationID)
	if entry.Binding.VersionPolicy == "latest_finalized" {
		f, e = s.finalization(id, "", "")
	}
	if e != nil {
		if entry.Binding.VersionPolicy == "pinned_finalized" {
			v.VersionStatus = "pinned_unavailable"
			v.Availability = "unavailable"
		}
		return v
	}
	v.SelectedRevisionID = f.RevisionID
	v.SelectedFinalizationID = f.FinalizationID
	v.DeliveryStatus = f.DeliveryLevel
	if v.DeliveryStatus != "proposal" && v.DeliveryStatus != "ready_for_use" {
		v.DeliveryStatus = "not_finalized"
	}
	if h.WorkingRevisionID != h.FinalizedRevisionID && v.WorkStatus == "no_new_draft" {
		v.WorkStatus = "new_draft"
	}
	d, e := flow.Store.Revision(id, f.RevisionID)
	if e != nil {
		v.Availability = "unavailable"
		v.Error = e.Error()
		return v
	}
	report, e := flow.Store.Report(id, f.ReportID)
	if e == nil && report.RevisionID == f.RevisionID && report.ValidationKey == documentvalidation.Key(d) {
		v.ValidationStatus = report.Status
	} else {
		v.ValidationStatus = "stale"
	}
	if d.ResourceError != "" || !flow.EvidenceAvailable(id, d.Sources) {
		v.Availability = "unavailable"
	}
	return v
}
func renderCatalog(v models.CatalogView) string {
	var out strings.Builder
	fmt.Fprintf(&out, "<!-- Generated from catalog generation %d, topic generation %d. Edit through ThoughtFlow. -->\n# %s\n\n%s\n\nDelivery threshold: %s; required: %d; delivered: %d.\n", v.Catalog.Generation, v.Topic.Generation, v.Topic.Name, v.Topic.Description, v.Catalog.RequiredDeliveryLevel, v.RequiredCount, v.DeliveredCount)
	for _, g := range v.Catalog.Groups {
		fmt.Fprintf(&out, "\n## %s\n", g.Title)
		for _, e := range v.Entries {
			if e.Entry.GroupID != g.ID {
				continue
			}
			target := ""
			if e.Entry.Binding.ThoughtID != "" {
				target = " (thought://" + e.Entry.Binding.ThoughtID + ")"
			}
			fmt.Fprintf(&out, "- %s%s — %s / %s / %s; revision: %s\n", strings.ReplaceAll(e.Entry.Title, "\n", " "), target, e.BindingStatus, e.DeliveryStatus, e.WorkStatus, e.SelectedRevisionID)
		}
	}
	return out.String()
}
func (s *Store) RebuildCatalog(ctx context.Context, id string) (result models.CatalogView, resultErr error) {
	defer func() {
		if resultErr != nil {
			objectlocation.For(s.rootPath).AddMetric("index_rebuild_failures_total", 1)
		}
	}()
	r := objectlocation.For(s.rootPath)
	epoch := r.Epoch()
	view, e := s.CatalogView(ctx, id)
	if e != nil {
		return view, e
	}
	r.Namespace.RLock()
	defer r.Namespace.RUnlock()
	if epoch != r.Epoch() {
		r.AddMetric("epoch_retries_total", 1)
		return view, objectlocation.ErrConflict
	}
	unlock := s.lock(id)
	defer unlock()
	latest, e := s.Catalog(ctx, id)
	if e != nil {
		return view, e
	}
	if latest.Generation != view.Catalog.Generation {
		return view, objectlocation.ErrConflict
	}
	if e = objectlocation.WriteFile(s.rootPath, "topics/"+id+"/catalog.md", []byte(renderCatalog(view)), 0644); e != nil {
		return view, e
	}
	r.SetMetric("catalog_generation", float64(view.Catalog.Generation))
	if op, e := r.Operation(view.Catalog.LastOperationID); e == nil {
		r.SetMetric("projection_lag_seconds", time.Since(op.UpdatedAt).Seconds())
	}
	view.ProjectionStatus = "current"
	return view, nil
}
func (s *Store) Settings(ctx context.Context, id string, req models.TopicSettingsRequest) (models.Topic, error) {
	r := objectlocation.For(s.rootPath)
	r.Namespace.Lock()
	defer r.Namespace.Unlock()
	unlock := s.lock(id)
	defer unlock()
	t, e := s.Get(ctx, id)
	if e != nil {
		return t, e
	}
	op, e := r.Begin(req.OperationID, "topic_settings", "", id, req)
	if e != nil {
		return t, e
	}
	if op.State == "completed" {
		return t, nil
	}
	if t.LastOperationID == req.OperationID && t.Generation == req.ExpectedGeneration+1 {
		return t, r.Complete(op)
	}
	if t.Generation != req.ExpectedGeneration {
		return t, objectlocation.ErrConflict
	}
	if strings.TrimSpace(req.Name) == "" {
		return t, objectlocation.ErrInvalid
	}
	t.Name = strings.TrimSpace(req.Name)
	t.Description = strings.TrimSpace(req.Description)
	t.SearchPolicy = &req.SearchPolicy
	t.Generation++
	t.LastOperationID = req.OperationID
	t.UpdatedAt = time.Now().UTC()
	op.BeforeGeneration = req.ExpectedGeneration
	op.AfterGeneration = t.Generation
	op.TargetPath = "topics/" + t.Slug + "/topic.yaml"
	op.CandidateHash = objectlocation.HashJSON(t)
	op.EventType = "topic.search_policy_updated"
	if e = r.Prepare(op); e != nil {
		return t, e
	}
	if e = s.writeTopic(t); e != nil {
		return t, e
	}
	r.BumpEpoch()
	return t, r.Complete(op)
}
func (s *Store) EntryContent(ctx context.Context, id, entryID, view, revision string) (models.DocumentSnapshot, error) {
	c, e := s.Catalog(ctx, id)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	var entry *models.CatalogEntry
	for i := range c.Entries {
		if c.Entries[i].ID == entryID {
			entry = &c.Entries[i]
			break
		}
	}
	if entry == nil {
		return models.DocumentSnapshot{}, os.ErrNotExist
	}
	if entry.Binding.Kind == "snapshot" {
		var snap models.TopicSnapshot
		base := "topics/" + id + "/references/" + entry.Binding.SnapshotID
		if e = objectlocation.ReadJSON(s.rootPath, base+"/snapshot.json", &snap); e != nil {
			return models.DocumentSnapshot{}, e
		}
		path, e := objectlocation.SafePath(s.rootPath, base+"/body.md")
		if e != nil {
			return models.DocumentSnapshot{}, e
		}
		raw, e := os.ReadFile(path)
		if e != nil {
			return models.DocumentSnapshot{}, e
		}
		if snap.ID != entry.Binding.SnapshotID || snap.Private || objectlocation.Hash(raw) != snap.BodyHash {
			return models.DocumentSnapshot{}, ErrRevisionUnavailable
		}
		return models.DocumentSnapshot{ResourceBindings: snap.Resources, DocumentID: snap.SourceThoughtID, Revision: models.DocumentRevision{RevisionID: snap.RevisionID, BodyHash: snap.BodyHash}, Body: string(raw), Freshness: "snapshot"}, nil
	}
	if entry.Binding.Kind != "thought" {
		return models.DocumentSnapshot{}, ErrRevisionUnavailable
	}
	thoughtID := entry.Binding.ThoughtID
	if _, e = objectlocation.For(s.rootPath).Resolve(ctx, thoughtID); e != nil {
		return models.DocumentSnapshot{}, e
	}

	flow := documentflow.For(s.rootPath)
	// Materialize any pending projection before acquiring the consistent read
	// lease. No nested object lock is taken below.
	if _, _, e = markdown.ReadThought(s.rootPath, thoughtID); e != nil {
		return models.DocumentSnapshot{}, e
	}
	unlock := flow.Store.Lock(thoughtID)
	defer unlock()
	if _, e = objectlocation.For(s.rootPath).Resolve(ctx, thoughtID); e != nil {
		return models.DocumentSnapshot{}, e
	}
	if view == "draft" {
		h, err := flow.Store.ActiveHead(thoughtID)
		if os.IsNotExist(err) {
			raw, e := markdown.ReadThoughtRaw(s.rootPath, thoughtID)
			if e != nil {
				return models.DocumentSnapshot{}, e
			}
			_, content := markdown.ParseThought(raw)
			b, e := objectlocation.For(s.rootPath).Bindings(thoughtID)
			return models.DocumentSnapshot{DocumentID: thoughtID, Body: documentflow.ExtractBody(content), Freshness: "ordinary_note", ResourceBindings: b.Bindings}, e
		}
		if err != nil {
			return models.DocumentSnapshot{}, err
		}
		if revision != "" && revision != h.WorkingRevisionID {
			// Search hits bind the indexed working revision. Permit only its
			// immutable ancestry and label it explicitly as a working version.
			seen := map[string]bool{}
			for current := h.WorkingRevisionID; current != "" && !seen[current]; {
				seen[current] = true
				d, e := flow.Store.Revision(thoughtID, current)
				if e != nil {
					return models.DocumentSnapshot{}, e
				}
				if current == revision {
					if d.ResourceError != "" {
						return models.DocumentSnapshot{}, ErrRevisionUnavailable
					}
					return models.DocumentSnapshot{DocumentID: thoughtID, Head: h, Revision: d.Revision, Body: d.Body, Contract: d.Contract, Sources: d.Sources, ResourceBindings: d.Resources.Bindings, Freshness: "historical_working"}, nil
				}
				current = d.Revision.ParentRevisionID
			}
			return models.DocumentSnapshot{}, ErrRevisionUnavailable
		}
		return flow.Get(thoughtID)
	}
	if view != "final" {
		return models.DocumentSnapshot{}, objectlocation.ErrInvalid
	}
	selected, key := revision, ""
	if selected == "" && entry.Binding.VersionPolicy == "pinned_finalized" {
		selected = entry.Binding.RevisionID
		key = entry.Binding.FinalizationID
	}
	f, e := s.finalization(thoughtID, selected, key)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	d, e := flow.Store.Revision(thoughtID, f.RevisionID)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	if d.ResourceError != "" {
		return models.DocumentSnapshot{}, ErrRevisionUnavailable
	}
	report, e := flow.Store.Report(thoughtID, f.ReportID)
	if e != nil {
		return models.DocumentSnapshot{}, e
	}
	h, e := flow.Store.ActiveHead(thoughtID)
	return models.DocumentSnapshot{ResourceBindings: d.Resources.Bindings, DocumentID: thoughtID, Head: h, Revision: d.Revision, Contract: d.Contract, Sources: d.Sources, Body: d.Body, Finalization: &f, Report: &report, Freshness: "finalized"}, e

}
func (s *Store) TopicPaths(ctx context.Context, id string) ([]string, error) {
	t, e := s.Get(ctx, id)
	if e != nil {
		return nil, e
	}
	paths := []string{filepath.ToSlash(filepath.Join("topics", t.Slug, "topic.yaml")), "topics/" + t.Slug + "/index.md"}
	if t.Mode == models.TopicModeCollection {
		paths = append(paths, catalogPath(id), "topics/"+id+"/catalog.md")
	}
	return paths, nil
}

func validCatalogShape(c models.TopicCatalog) bool {
	if c.Generation == 0 || len(c.Groups) == 0 || len(c.Groups) > 100 || len(c.Entries) > 5000 || (c.RequiredDeliveryLevel != "proposal" && c.RequiredDeliveryLevel != "ready_for_use") {
		return false
	}
	groups, entries, thoughts := map[string]bool{}, map[string]bool{}, map[string]bool{}
	for _, g := range c.Groups {
		if !objectlocation.ValidID(g.ID) || groups[g.ID] || strings.TrimSpace(g.Title) == "" {
			return false
		}
		groups[g.ID] = true
	}
	for _, e := range c.Entries {
		if !objectlocation.ValidID(e.ID) || entries[e.ID] || !groups[e.GroupID] || strings.TrimSpace(e.Title) == "" {
			return false
		}
		entries[e.ID] = true
		b := e.Binding
		switch b.Kind {
		case "planned":
			if b.ThoughtID != "" || b.SnapshotID != "" || b.RevisionID != "" || b.FinalizationID != "" {
				return false
			}
		case "thought":
			if !objectlocation.ValidID(b.ThoughtID) || thoughts[b.ThoughtID] || b.SnapshotID != "" {
				return false
			}
			thoughts[b.ThoughtID] = true
			if b.VersionPolicy == "pinned_finalized" {
				if !objectlocation.ValidID(b.RevisionID) || !objectlocation.ValidID(b.FinalizationID) {
					return false
				}
			} else if b.VersionPolicy != "latest_finalized" || b.RevisionID != "" || b.FinalizationID != "" {
				return false
			}
		case "snapshot":
			if !objectlocation.ValidID(b.SnapshotID) || b.ThoughtID != "" {
				return false
			}
		default:
			return false
		}
	}
	return true
}

// Preserve unavailable existing bindings during unrelated catalog edits. Their
// missing state remains visible and required entries stay in the denominator.
func (s *Store) validateCatalogUpdateLocked(ctx context.Context, c, old models.TopicCatalog) error {
	if !validCatalogShape(c) {
		return objectlocation.ErrInvalid
	}
	prior := map[string]models.CatalogBinding{}
	for _, entry := range old.Entries {
		prior[entry.ID] = entry.Binding
	}
	copy := c
	copy.Entries = append([]models.CatalogEntry{}, c.Entries...)
	for i, entry := range copy.Entries {
		if previous, ok := prior[entry.ID]; ok && previous == entry.Binding {
			copy.Entries[i].Binding = models.CatalogBinding{Kind: "planned"}
		}
	}
	return s.validateCatalogLocked(ctx, copy)
}
