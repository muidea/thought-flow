package models

import "strings"

// EmbeddingText is the shared, normalized input for index and topic matching.
func EmbeddingText(t Thought, c ThoughtContent) string {
	return strings.TrimSpace(strings.Join([]string{t.UserTitle, t.ExtractedTitle, t.Summary, strings.Join(t.UserTags, " "), strings.Join(t.AITags, " "), c.Original, c.ExtractedContent, c.AINotes}, "\n"))
}
