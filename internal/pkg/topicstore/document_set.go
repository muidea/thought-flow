package topicstore

import (
	"context"
	"os"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentsetstore"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func (s *Store) setEntry(ctx context.Context, topic string, entry models.CatalogEntry) (models.CatalogEntryView, bool) {
	l, e := objectlocation.For(s.rootPath).Lookup(entry.Binding.ThoughtID)
	if e != nil || l.ManagedSetID != topic {
		return models.CatalogEntryView{}, false
	}
	v := models.CatalogEntryView{Entry: entry, Location: &l, BindingStatus: "bound", WorkStatus: "new_draft", VersionStatus: "set_revision", Availability: "available", DeliveryStatus: "not_finalized"}
	store := documentsetstore.New(s.rootPath)
	h, e := store.Head(topic)
	if e != nil {
		v.Availability = "unavailable"
		v.Error = e.Error()
		return v, true
	}
	working, e := store.Read(topic, h.Working)
	if e != nil {
		v.Availability = "unavailable"
		v.Error = e.Error()
		return v, true
	}
	for _, a := range working.Members {
		if a.Key == l.ManagedMemberKey {
			v.WorkingRevisionID = a.RevisionID
			v.Profile = &a.Contract.ProfileRef
			v.OriginalTitle = a.Contract.Title
			v.ValidationStatus = working.Report.Status
		}
	}
	v.HasNewDraft = h.Working != h.Finalized
	if h.Finalized != "" {
		final, e := store.Read(topic, h.Finalized)
		if e != nil {
			v.Availability = "unavailable"
			v.Error = e.Error()
			return v, true
		}
		for _, a := range final.Members {
			if a.Key == l.ManagedMemberKey {
				v.SelectedRevisionID = a.RevisionID
				v.SelectedFinalizationID = h.Finalization.ID
				v.DeliveryStatus = h.Finalization.DeliveryLevel
			}
		}
	}
	return v, true
}
func (s *Store) setContent(topic, thought, view, revision string) (models.DocumentSnapshot, bool, error) {
	l, e := objectlocation.For(s.rootPath).Lookup(thought)
	if e != nil || l.ManagedSetID != topic {
		return models.DocumentSnapshot{}, false, nil
	}
	store := documentsetstore.New(s.rootPath)
	h, e := store.Head(topic)
	if e != nil {
		return models.DocumentSnapshot{}, true, e
	}
	selected := h.Finalized
	if view == "draft" {
		selected = h.Working
	}
	if selected == "" {
		return models.DocumentSnapshot{}, true, ErrRevisionUnavailable
	}
	// Existing collection APIs must not silently follow a personal working HEAD.
	r, e := store.Read(topic, selected)
	if e != nil {
		return models.DocumentSnapshot{}, true, e
	}
	var artifact ds.Artifact
	for _, a := range r.Members {
		if a.Key == l.ManagedMemberKey {
			artifact = a
		}
	}
	if artifact.Key == "" {
		return models.DocumentSnapshot{}, true, os.ErrNotExist
	}
	if revision != "" && artifact.RevisionID != revision {
		return models.DocumentSnapshot{}, true, ErrRevisionUnavailable
	}
	snapshot := models.DocumentSnapshot{DocumentID: thought, Body: artifact.Body, Contract: artifact.Contract, Sources: artifact.Sources, Revision: models.DocumentRevision{DocumentID: thought, RevisionID: artifact.RevisionID, BodyHash: artifact.BodyHash}, Freshness: "set_revision", Report: &artifact.SingleReport}
	return snapshot, true, nil
}
