package contentlibrary

import (
	"context"
	"os"
	"thoughtflow/internal/pkg/contentreference"
	"thoughtflow/internal/pkg/markdown"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/objectlocation"
	"thoughtflow/internal/pkg/topicstore"
)

type Link = contentreference.Link
type Links = contentreference.Links
type LinkRequest struct {
	OperationID string  `json:"operation_id"`
	Expected    *uint64 `json:"expected_generation"`
	Entries     []Link  `json:"entries"`
}
type TopicSet struct {
	Link
	Item  Item   `json:"item"`
	Error string `json:"error,omitempty"`
}
type TopicContent struct {
	Sets    []TopicSet `json:"sets"`
	Notes   []Item     `json:"notes"`
	Sources []Item     `json:"sources"`
	Links   Links      `json:"links"`
}

func LinksPath(id string) string { return "topics/" + id + "/content-links.json" }
func (s *Store) Links(ctx context.Context, id string) (Links, error) {
	out := Links{Schema: 1, Entries: []Link{}}
	if _, e := topicstore.New(s.Root).Get(ctx, id); e != nil {
		return out, e
	}
	e := objectlocation.ReadJSON(s.Root, LinksPath(id), &out)
	if os.IsNotExist(e) {
		return out, nil
	}
	if e != nil {
		return out, e
	}
	if !contentreference.Validate(out) {
		return out, ds.ErrInvalid
	}
	seen := map[string]bool{}
	for _, link := range out.Entries {
		if seen[link.SetID] || !validLink(link) {
			return out, ds.ErrInvalid
		}
		seen[link.SetID] = true
	}
	return out, nil
}
func validLink(l Link) bool { return contentreference.Valid(l) }
func (s *Store) resolve(ctx context.Context, l Link) (Detail, error) {
	if !validLink(l) {
		return Detail{}, ds.ErrInvalid
	}
	if l.Policy == "pinned" {
		return s.Detail(ctx, l.SetID, l.Selection)
	}
	versions, h, e := s.versions(l.SetID)
	if e != nil {
		return Detail{}, e
	}
	for _, v := range versions {
		if l.Policy == "latest_archive" && v.Kind == "archive" {
			return s.Detail(ctx, l.SetID, v.Selection)
		}
	}
	if h != nil {
		if l.Policy == "latest_working" && h.Working != "" {
			return s.Detail(ctx, l.SetID, Selection{"revision", h.Working})
		}
		if l.Policy == "latest_finalized" && h.Finalized != "" {
			return s.Detail(ctx, l.SetID, Selection{"revision", h.Finalized})
		}
	}
	return Detail{}, os.ErrNotExist
}
func (s *Store) WriteLinks(ctx context.Context, id string, req LinkRequest) (Links, error) {
	if req.Expected == nil {
		return Links{}, ds.ErrPrecondition
	}
	if !objectlocation.ValidID(id) || !objectlocation.ValidID(req.OperationID) || len(req.Entries) > 500 {
		return Links{}, ds.ErrInvalid
	}
	registry := objectlocation.For(s.Root)
	unlock := registry.LockObject("content-links-" + id)
	defer unlock()
	epoch := registry.Epoch()
	old, e := s.Links(ctx, id)
	if e != nil {
		return old, e
	}
	op, e := registry.Begin(req.OperationID, "topic.content_links", "", id, req)
	if e != nil {
		return old, e
	}
	if op.State == "completed" {
		return old, nil
	}
	if old.OperationID == req.OperationID && op.CandidateHash == objectlocation.HashJSON(old) {
		return old, registry.Complete(op)
	}
	if *req.Expected != old.Generation {
		return old, ds.ErrConflict
	}
	seen := map[string]bool{}
	for _, l := range req.Entries {
		if seen[l.SetID] || !validLink(l) {
			return old, ds.ErrInvalid
		}
		seen[l.SetID] = true
		if _, e = s.resolve(ctx, l); e != nil {
			return old, e
		}
	}
	registry.Namespace.Lock()
	defer registry.Namespace.Unlock()
	if registry.Epoch() != epoch {
		return old, ds.ErrConflict
	}
	next := Links{Schema: 1, Generation: old.Generation + 1, OperationID: req.OperationID, Entries: append([]Link{}, req.Entries...)}
	op.BeforeGeneration = old.Generation
	op.AfterGeneration = next.Generation
	op.TargetPath = LinksPath(id)
	op.CandidateHash = objectlocation.HashJSON(next)
	op.EventType = "topic.content_links_updated"
	if e = registry.Prepare(op); e != nil {
		return old, e
	}
	if e = objectlocation.WriteJSON(s.Root, LinksPath(id), next, false); e != nil {
		return old, e
	}
	registry.BumpEpoch()
	return next, registry.Complete(op)
}
func (s *Store) Topic(ctx context.Context, id string) (TopicContent, error) {
	out := TopicContent{Sets: []TopicSet{}, Notes: []Item{}, Sources: []Item{}}
	links, e := s.Links(ctx, id)
	if e != nil {
		return out, e
	}
	out.Links = links
	t, e := topicstore.New(s.Root).Get(ctx, id)
	if e != nil {
		return out, e
	}
	membership := map[string]bool{}
	for _, m := range t.Members {
		membership[m] = true
	}
	// Topic memberships are stored separately from the optional Topic.Members.
	detail, e := topicstore.New(s.Root).Detail(ctx, id)
	if e != nil {
		return out, e
	}
	{
		for _, m := range detail.Members {
			membership[m.ThoughtID] = true
		}
	}
	memberSets := map[string]bool{}
	for thoughtID := range membership {
		if thought, _, err := markdown.ReadThought(s.Root, thoughtID); err == nil && thought.DesignDraft != nil {
			memberSets[thought.DesignDraft.SetID] = true
		}
		if location, err := objectlocation.For(s.Root).Lookup(thoughtID); err == nil && location.ManagedSetID != "" {
			memberSets[location.ManagedSetID] = true
		}
	}
	items, e := s.all(ctx)
	if e != nil {
		return out, e
	}
	selected := map[string]Link{}
	for _, l := range links.Entries {
		selected[l.SetID] = l
	}
	for _, i := range items {
		if i.Kind == "document_set" {
			include := i.ID == id || memberSets[i.ID]
			for _, m := range append(append([]Member{}, i.Members...), i.Sources...) {
				if membership[m.ThoughtID] {
					include = true
				}
			}
			if include {
				if _, ok := selected[i.ID]; !ok {
					policy := "latest_working"
					if i.Selection.Kind == "archive" {
						policy = "latest_archive"
					} else if i.State == "finalized" {
						policy = "latest_finalized"
					}
					selected[i.ID] = Link{SetID: i.ID, Policy: policy}
				}
			}
		} else if membership[i.ID] {
			if i.Thought != nil && i.Thought.DesignDraft != nil && i.Thought.DesignDraft.MemberKey == "archive-source" {
				out.Sources = append(out.Sources, i)
			} else {
				out.Notes = append(out.Notes, i)
			}
		}
	}
	for _, l := range selected {
		d, err := s.resolve(ctx, l)
		row := TopicSet{Link: l}
		if err != nil {
			row.Item = Item{ID: l.SetID, Kind: "document_set", Title: l.SetID, Unavailable: true}
			row.Error = "content_unavailable"
		} else {
			row.Item = d.Item
			for n := range row.Item.Members {
				row.Item.Members[n].Body = ""
			}
			for _, m := range row.Item.Sources {
				out.Sources = append(out.Sources, Item{ID: l.SetID + ":" + m.Key, Kind: "source", Title: m.Title, SetID: l.SetID, MemberKey: m.Key, Selection: d.Selection})
			}
			for _, source := range row.Item.SourceRecords {
				out.Sources = append(out.Sources, Item{ID: l.SetID + ":" + source.ID, Kind: "reference", Title: source.Locator, Description: source.VerificationStatus, SetID: l.SetID, Selection: d.Selection})
			}
			for n := range row.Item.Sources {
				row.Item.Sources[n].Body = ""
			}
		}
		out.Sets = append(out.Sets, row)
	}
	sortTopicSets(out.Sets)
	return out, nil
}
func sortTopicSets(v []TopicSet) {
	for i := 1; i < len(v); i++ {
		for j := i; j > 0 && v[j].SetID < v[j-1].SetID; j-- {
			v[j], v[j-1] = v[j-1], v[j]
		}
	}
}
