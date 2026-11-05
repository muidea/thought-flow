package contentlibrary

import (
	"context"
	"strings"

	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/models"
)

// TopicDisplay projects legacy automatic topic names at the API boundary only.
// Reads never rename directories or rewrite authoritative metadata/generations.
// A user-supplied readable name always takes precedence.
func (s *Store) TopicDisplay(ctx context.Context, topic models.Topic) models.Topic {
	if topic.Mode != models.TopicModeCollection || strings.TrimSpace(topic.Name) != topic.ID || !ds.OpaqueSetTitle(topic.ID) {
		return topic
	}
	if detail, err := s.Detail(ctx, topic.ID, Selection{}); err == nil {
		topic.Name = detail.Title
	} else {
		topic.Name = ds.DisplayTitle(ds.SharedDesign{}, topic.Description, "")
	}
	return topic
}
