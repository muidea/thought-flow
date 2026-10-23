package ai

import (
	"fmt"
	"strings"
	"unicode/utf8"

	"thoughtflow/assets/prompts"
	"thoughtflow/internal/pkg/models"
)

func refinementSystemPrompt() string {
	return fmt.Sprintf(prompts.RefineSystem, models.MetadataTitleMaxRunes, models.MetadataSummaryMaxRunes, models.MetadataMaxTags, models.MetadataTagMaxRunes)
}

// ValidateRefinementMetadata rejects unusable model output instead of silently
// clipping a generated summary and potentially discarding its qualifications.
// Factual grounding is required by the prompt, but cannot be proven by lengths.
func ValidateRefinementMetadata(r models.ThoughtRefinement) error {
	invalid := func(message string) error {
		return ProviderError{Code: "thoughtflow.ai.invalid_refinement_metadata", Message: message, Retryable: true}
	}
	if strings.TrimSpace(r.Summary) == "" || utf8.RuneCountInString(r.Summary) > models.MetadataSummaryMaxRunes {
		return invalid(fmt.Sprintf("summary must contain 1-%d Unicode characters", models.MetadataSummaryMaxRunes))
	}
	if utf8.RuneCountInString(r.ExtractedTitle) > models.MetadataTitleMaxRunes {
		return invalid(fmt.Sprintf("title must contain at most %d Unicode characters", models.MetadataTitleMaxRunes))
	}
	if len(r.AITags) > models.MetadataMaxTags {
		return invalid(fmt.Sprintf("tags must contain at most %d entries", models.MetadataMaxTags))
	}
	seen := map[string]bool{}
	for _, tag := range r.AITags {
		key := strings.ToLower(strings.TrimSpace(tag))
		if key == "" || utf8.RuneCountInString(tag) > models.MetadataTagMaxRunes || strings.ContainsAny(tag, "\r\n") || strings.Contains(key, "://") || seen[key] {
			return invalid(fmt.Sprintf("tags must be distinct plain labels of 1-%d Unicode characters", models.MetadataTagMaxRunes))
		}
		seen[key] = true
	}
	return nil
}
