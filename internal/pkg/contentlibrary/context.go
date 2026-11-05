package contentlibrary

import (
	"context"
	"os"
	"thoughtflow/internal/pkg/markdown"
	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

// Contexts resolves only the requested note identities, with one fixed read per
// referenced set/version. Search hits remain hits on mutable notes; the separate
// context link opens saved originals, never an unpublished candidate.
func (s *Store) Contexts(ctx context.Context, ids []string) (map[string]*models.ContentSetContext, error) {
	out := map[string]*models.ContentSetContext{}
	cache := map[string]Detail{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		thought, _, err := markdown.ReadThought(s.Root, id)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return nil, err
		}
		set := ""
		selection := Selection{}
		if thought.DesignDraft != nil {
			set = thought.DesignDraft.SetID
			selection = Selection{Kind: "archive", ID: thought.DesignDraft.RunID}
		}
		if set == "" {
			if location, err := objectlocation.For(s.Root).Lookup(id); err == nil {
				set = location.ManagedSetID
			}
		}
		if set == "" {
			continue
		}
		key := set + "/" + selection.Kind + "/" + selection.ID
		detail, ok := cache[key]
		if !ok {
			detail, err = s.Detail(ctx, set, selection)
			if os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return nil, err
			}
			cache[key] = detail
		}
		for _, member := range append(append([]Member{}, detail.Members...), detail.Sources...) {
			if member.ThoughtID == id {
				out[id] = &models.ContentSetContext{SetID: set, Title: detail.Title, Kind: detail.Selection.Kind, Version: detail.Selection.ID, Member: member.Key, Role: member.Role, State: detail.State}
				break
			}
		}
	}
	return out, nil
}
