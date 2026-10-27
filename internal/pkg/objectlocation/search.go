package objectlocation

import (
	"context"
	"encoding/json"
	"os"

	"gopkg.in/yaml.v3"
	"thoughtflow/internal/pkg/models"
)

// GlobalEligible is evaluated from authoritative placement and owner policy,
// never from asynchronously updated search projections. The caller holds a
// namespace read lease for a consistent candidate selection.
func (r *Registry) GlobalEligible(l models.ObjectLocation) bool {
	if l.Lifecycle != "active" || l.DeletedAt != nil {
		return false
	}
	switch l.GlobalSearch {
	case "include":
		return true
	case "exclude":
		return false
	case "inherit":
	default:
		return false
	}
	if l.OwnerTopicID == "" {
		return true
	}
	t, e := r.Topic(l.OwnerTopicID)
	return e == nil && t.SearchPolicy != nil && t.SearchPolicy.OwnedThoughtsGlobalDefault
}
func (r *Registry) Topic(id string) (models.Topic, error) {
	var t models.Topic
	if !ValidID(id) {
		return t, ErrInvalid
	}
	if e := r.CheckTopicActive(id); e != nil {
		return t, e
	}
	path, e := SafePath(r.Root, "topics/"+id+"/topic.yaml")
	if e != nil {
		return t, e
	}
	raw, e := os.ReadFile(path)
	if e != nil {
		return t, e
	}
	if e = json.Unmarshal(raw, &t); e != nil {
		if e = yaml.Unmarshal(raw, &t); e != nil {
			return t, e
		}
	}
	if t.ID != id || t.Slug != id {
		return t, ErrAmbiguous
	}
	return t, nil
}
func (r *Registry) Eligible(ctx context.Context, scope, topicID string) (map[string]models.ObjectLocation, error) {
	if scope == "" {
		scope = "global"
	}
	if (scope != "global" && scope != "topic") || (scope == "topic" && !ValidID(topicID)) {
		return nil, ErrInvalid
	}
	var members map[string]bool
	if topicID != "" {
		t, e := r.Topic(topicID)
		if e != nil {
			if os.IsNotExist(e) && scope == "global" {
				return map[string]models.ObjectLocation{}, nil
			}
			return nil, e
		}
		members = map[string]bool{}
		if t.Mode == models.TopicModeCollection {
			var c models.TopicCatalog
			if e = ReadJSON(r.Root, "topics/"+topicID+"/catalog.json", &c); e != nil {
				return nil, e
			}
			if c.TopicID != topicID {
				return nil, ErrAmbiguous
			}
			for _, entry := range c.Entries {
				if entry.Binding.Kind == "thought" {
					members[entry.Binding.ThoughtID] = true
				}
			}
		} else {
			for _, id := range t.Members {
				members[id] = true
			}
		}
	}
	all, e := r.ListActive(ctx)
	if e != nil {
		return nil, e
	}
	out := map[string]models.ObjectLocation{}
	for _, l := range all {
		if !r.ContentAvailable(l) {
			continue
		}
		if members != nil && !members[l.ObjectID] {
			continue
		}
		if scope == "topic" || r.GlobalEligible(l) {
			out[l.ObjectID] = l
		} else {
			r.AddMetric("policy_filtered_total", 1)
		}
	}
	return out, nil
}

type namespaceContextKey struct{}

func (r *Registry) NamespaceRead(ctx context.Context) (context.Context, func()) {
	if ctx.Value(namespaceContextKey{}) == r {
		return ctx, func() {}
	}
	r.Namespace.RLock()
	return context.WithValue(ctx, namespaceContextKey{}, r), r.Namespace.RUnlock
}

func (r *Registry) ContentAvailable(l models.ObjectLocation) bool {
	if r.CheckManaged(l) != nil {
		return false
	}
	var h models.DocumentHead
	e := ReadJSON(r.Root, l.Placement.DocumentRoot+"/head.json", &h)
	if e == nil && h.DeletedAt != nil {
		return false
	}
	if e != nil && !os.IsNotExist(e) {
		return false
	}
	path, e := SafePath(r.Root, l.Placement.ThoughtPath)
	if e != nil {
		return false
	}
	info, e := os.Stat(path)
	if e == nil {
		return info.Mode().IsRegular()
	}
	return os.IsNotExist(e) && h.PendingProjection && h.WorkingRevisionID != ""
}

func (r *Registry) CheckTopicActive(id string) error {
	if !ValidID(id) {
		return ErrInvalid
	}
	activation, e := SafePath(r.Root, ".content-registry/private/document-set-activations/"+id+".json")
	if e != nil {
		return e
	}
	if _, e = os.Stat(activation); e == nil {
		head, err := SafePath(r.Root, "topics/"+id+"/document-set/head.json")
		if err != nil {
			return err
		}
		if _, err = os.Stat(head); err != nil {
			return err
		}
	} else if !os.IsNotExist(e) {
		return e
	}
	path, e := SafePath(r.Root, ".content-registry/topic-deletions/"+id+".json")
	if e != nil {
		return e
	}
	if _, e = os.Stat(path); e == nil {
		return os.ErrNotExist
	} else if !os.IsNotExist(e) {
		return e
	}
	return nil
}
