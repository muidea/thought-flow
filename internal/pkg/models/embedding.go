package models

import "strings"

// Metadata limits count Unicode code points, not bytes or model tokens.
const (
	MetadataTitleMaxRunes   = 120
	MetadataSummaryMaxRunes = 600
	MetadataTagMaxRunes     = 24
	MetadataMaxTags         = 8
)

// BoundedMetadataText normalizes whitespace and bounds external/user metadata
// without changing the stored note. It never splits a UTF-8 character.
func BoundedMetadataText(value string, maxRunes int) string {
	runes := []rune(strings.Join(strings.Fields(value), " "))
	if len(runes) > maxRunes {
		runes = runes[:maxRunes]
	}
	return string(runes)
}

// EmbeddingText is the shared input for note indexing and semantic topic
// matching. Body content deliberately cannot be supplied. At most 912 Unicode
// code points plus labels/separators are sent, even for oversized legacy metadata.
func EmbeddingText(t Thought) string {
	title := ""
	for _, candidate := range []string{t.UserTitle, t.ExtractedTitle} {
		if title = BoundedMetadataText(candidate, MetadataTitleMaxRunes); title != "" {
			break
		}
	}
	parts := []string{}
	if title != "" {
		parts = append(parts, "Title: "+title)
	}
	if summary := BoundedMetadataText(t.Summary, MetadataSummaryMaxRunes); summary != "" {
		parts = append(parts, "Summary: "+summary)
	}
	tags := []string{}
	seen := map[string]bool{}
	for _, tag := range append(append([]string{}, t.UserTags...), t.AITags...) {
		tag = BoundedMetadataText(tag, MetadataTagMaxRunes)
		key := strings.ToLower(tag)
		if tag == "" || seen[key] {
			continue
		}
		seen[key] = true
		tags = append(tags, tag)
		if len(tags) == MetadataMaxTags {
			break
		}
	}
	if len(tags) > 0 {
		parts = append(parts, "Tags: "+strings.Join(tags, ", "))
	}
	return strings.Join(parts, "\n")
}
