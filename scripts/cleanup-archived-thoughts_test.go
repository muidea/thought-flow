package main

import (
	"strings"
	"testing"

	"thoughtflow/internal/pkg/models"
)

func TestUnwrapProfileDocumentKeepsFinalNestedDocument(t *testing.T) {
	input := `# Final title

## Background

# Intermediate duplicate

archive process text

## Proposal

# Complete document

## 1. Scope

kept

## Alternatives

discarded wrapper`
	got, err := unwrapProfileDocument(input, "## Alternatives")
	if err != nil {
		t.Fatalf("unwrapProfileDocument: %v", err)
	}
	if !strings.HasPrefix(got, "# Final title\n") || !strings.Contains(got, "## 1. Scope") || strings.Contains(got, "discarded wrapper") || strings.Contains(got, "Intermediate duplicate") {
		t.Fatalf("unexpected output:\n%s", got)
	}
}

func TestNormalizeSingleTitleIsIdempotent(t *testing.T) {
	thought := models.Thought{UserTitle: "Archived title"}
	first := normalizeSingleTitle(thought, "## Scope\n\nbody")
	second := normalizeSingleTitle(thought, first)
	if first != second {
		t.Fatalf("normalization is not idempotent:\nfirst=%s\nsecond=%s", first, second)
	}
	if strings.Count(first, "# Archived title") != 1 {
		t.Fatalf("missing single title: %s", first)
	}
}

func TestNormalizeSingleTitleDemotesNestedTitles(t *testing.T) {
	thought := models.Thought{UserTitle: "Ignored"}
	got := normalizeSingleTitle(thought, "# Main\n\n## Section\n\n# Nested")
	if strings.Count(got, "\n# ") != 0 || !strings.Contains(got, "### Nested") {
		t.Fatalf("nested title was not demoted:\n%s", got)
	}
}
