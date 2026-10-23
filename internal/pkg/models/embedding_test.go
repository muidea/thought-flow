package models

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestEmbeddingUsesOnlyBoundedMetadata(t *testing.T) {
	thought := Thought{UserTitle: "  User\n title ", ExtractedTitle: "Other title", Summary: "Actual summary", UserTags: []string{"Go", "search"}, AITags: []string{"go", "DuckDB"}}
	want := "Title: User title\nSummary: Actual summary\nTags: Go, search, DuckDB"
	if got := EmbeddingText(thought); got != want {
		t.Fatalf("input = %q, want %q", got, want)
	}
	thought.UserTitle = ""
	if !strings.HasPrefix(EmbeddingText(thought), "Title: Other title\n") {
		t.Fatal("missing extracted title fallback")
	}
	if got := EmbeddingText(Thought{DisplayTitle: "Derived body text", KeyPoints: []string{"body detail"}, URL: "https://example.test"}); got != "" {
		t.Fatalf("non-metadata leaked: %q", got)
	}
	// Four-byte characters exercise the worst-case UTF-8 byte budget; this is
	// deliberately a byte bound, not an assumed character-to-token conversion.
	thought = Thought{UserTitle: strings.Repeat("😀", 20000), Summary: strings.Repeat("😀", 20000)}
	for i := range 100 {
		thought.UserTags = append(thought.UserTags, string(rune('甲'+i))+strings.Repeat("😀", 500))
	}
	got := EmbeddingText(thought)
	if !utf8.ValidString(got) || len(got) > 4096 {
		t.Fatalf("unbounded or invalid UTF-8 input: %d bytes", len(got))
	}
	if len(thought.Summary) != len(strings.Repeat("😀", 20000)) {
		t.Fatal("stored metadata mutated")
	}
}
