package contentlibrary

import (
	"context"
	"fmt"
	"net/url"
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

// SourceRequest selects saved content. Empty Member selects all body members,
// never source material; explicit source-member selection remains possible.
type SourceRequest struct {
	ThoughtID string    `json:"thought_id"`
	SetID     string    `json:"set_id"`
	Selection Selection `json:"selection"`
	Member    string    `json:"member"`
}

type frozenThought struct {
	Schema   int                    `json:"schema_version"`
	Snapshot models.ThoughtSnapshot `json:"snapshot"`
}

func snapshotPath(id string) string {
	return ".content-registry/private/compose-sources/" + id + ".json"
}
func sourceID(v any) string {
	return "snapshot-" + strings.TrimPrefix(documentstore.HashJSON(v), "sha256:")
}

func (s *Store) Sources(ctx context.Context, request SourceRequest) ([]models.ComposeSource, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if request.ThoughtID != "" {
		if request.SetID != "" || request.Selection != (Selection{}) || request.Member != "" {
			return nil, ds.ErrInvalid
		}
		thought, content, err := markdown.ReadThought(s.Root, request.ThoughtID)
		if err != nil {
			return nil, err
		}
		record := frozenThought{Schema: 1, Snapshot: models.ThoughtSnapshot{Thought: thought, Content: content}}
		id := sourceID(record)
		// Content-addressed snapshots preserve the bytes selected by the reader even
		// after a subsequent note edit. They stay private to this workspace.
		if err = objectlocation.WriteJSON(s.Root, snapshotPath(id), record, false); err != nil {
			return nil, err
		}
		return []models.ComposeSource{{SourceType: models.ComposeSourceTypeFrozenThought, SourceID: id, Title: first(thought.DisplayTitle, thought.UserTitle, thought.ExtractedTitle, thought.ID), SourceLink: "/api/content-library/sources/" + id, Metadata: map[string]string{"thought_id": thought.ID}}}, nil
	}
	if request.SetID == "" {
		return nil, ds.ErrInvalid
	}
	detail, err := s.Detail(ctx, request.SetID, request.Selection)
	if err != nil {
		return nil, err
	}
	members := detail.Members
	if request.Member != "" {
		members = nil
		for _, m := range append(append([]Member{}, detail.Members...), detail.Sources...) {
			if m.Key == request.Member {
				members = append(members, m)
			}
		}
		if len(members) != 1 {
			return nil, ds.ErrInvalid
		}
	}
	out := []models.ComposeSource{}
	for _, m := range members {
		metadata := map[string]string{"set_id": detail.ID, "kind": detail.Selection.Kind, "version": detail.Selection.ID, "member": m.Key, "body_hash": documentstore.Hash([]byte(m.Body))}
		link := "/#/notes?" + url.Values{"set": {detail.ID}, "kind": {detail.Selection.Kind}, "version": {detail.Selection.ID}, "member": {m.Key}}.Encode()
		out = append(out, models.ComposeSource{SourceType: models.ComposeSourceTypeSetMember, SourceID: sourceID(metadata), Title: detail.Title + " › " + m.Title, SourceLink: link, Metadata: metadata})
	}
	return out, nil
}

// ResolveSource reopens the exact authoritative source. Client-provided snippets
// are never a replacement for a missing or changed frozen source.
func (s *Store) ResolveSource(ctx context.Context, source models.ComposeSource) (models.ThoughtSnapshot, error) {
	if err := ctx.Err(); err != nil {
		return models.ThoughtSnapshot{}, err
	}
	switch source.SourceType {
	case models.ComposeSourceTypeFrozenThought:
		if !documentstore.ValidID(source.SourceID) {
			return models.ThoughtSnapshot{}, ds.ErrInvalid
		}
		var record frozenThought
		if err := objectlocation.ReadJSON(s.Root, snapshotPath(source.SourceID), &record); err != nil {
			return models.ThoughtSnapshot{}, err
		}
		if record.Schema != 1 || sourceID(record) != source.SourceID {
			return models.ThoughtSnapshot{}, ds.ErrInvalid
		}
		record.Snapshot.Thought.ID = source.SourceID
		return record.Snapshot, nil
	case models.ComposeSourceTypeSetMember:
		md := source.Metadata
		if sourceID(md) != source.SourceID {
			return models.ThoughtSnapshot{}, ds.ErrInvalid
		}
		detail, err := s.Detail(ctx, md["set_id"], Selection{Kind: md["kind"], ID: md["version"]})
		if err != nil {
			return models.ThoughtSnapshot{}, err
		}
		for _, member := range append(append([]Member{}, detail.Members...), detail.Sources...) {
			if member.Key != md["member"] {
				continue
			}
			if documentstore.Hash([]byte(member.Body)) != md["body_hash"] {
				return models.ThoughtSnapshot{}, ds.ErrConflict
			}
			return models.ThoughtSnapshot{Thought: models.Thought{ID: source.SourceID, UserTitle: member.Title, DisplayTitle: member.Title}, Content: models.ThoughtContent{AINotes: member.Body}}, nil
		}
		return models.ThoughtSnapshot{}, fmt.Errorf("%w: frozen member missing", ds.ErrUnavailable)
	}
	return models.ThoughtSnapshot{}, ds.ErrInvalid
}
