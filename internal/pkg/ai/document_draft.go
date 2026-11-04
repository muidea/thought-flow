package ai

import (
	"reflect"
	"strings"

	"thoughtflow/internal/pkg/documentprofile"
	ds "thoughtflow/internal/pkg/documentset"
	"thoughtflow/internal/pkg/models"
)

// The dynamic profile keys and the DTO value shape are one contract. This is
// prompt guidance for native json_object, not a json_schema capability request.
func documentDraftResponseSchema(profile documentprofile.DocumentProfile) map[string]any {
	schema := responseSchema(reflect.TypeOf(models.DocumentDraft{}))
	defs := schema["$defs"].(map[string]any)
	draft := defs["DocumentDraft"].(map[string]any)
	properties := draft["properties"].(map[string]any)
	sections := map[string]any{"type": "object", "properties": map[string]any{}, "additionalProperties": false, "required": profile.RequiredSectionKeys()}
	if profile.Validation.AllowUnknownSections {
		sections["additionalProperties"] = map[string]any{"$ref": "#/$defs/DocumentSection"}
	}
	for _, section := range profile.Sections {
		sections["properties"].(map[string]any)[section.Key] = map[string]any{"$ref": "#/$defs/DocumentSection"}
	}
	properties["sections"] = sections
	draft["description"] = "One document, not a set, role wrapper or validation report. references and assumptions are top-level arrays, not section keys unless the profile explicitly declares those sections."
	defs["DocumentSection"].(map[string]any)["properties"].(map[string]any)["content"].(map[string]any)["description"] = "Actual Markdown body string for this exact section key, not a nested object, array or renamed field."
	return schema
}

type documentDraftSectionsError struct{}

func (documentDraftSectionsError) Error() string {
	return "document draft sections contain keys not declared by the profile; references and assumptions belong at the top level"
}

// Parse exactly the normalized outer response. Never search a failed DTO for
// a nested object that happens to unmarshal with all of its fields discarded.
func decodeDocumentDraft(content string, profile documentprofile.DocumentProfile) (models.DocumentDraft, error) {
	var out models.DocumentDraft
	if err := ds.StrictJSON([]byte(content), &out); err != nil {
		return out, err
	}
	if out.Sections == nil {
		return out, requiredJSONFieldError{field: "sections"}
	}
	declared := map[string]bool{}
	for _, section := range profile.Sections {
		declared[section.Key] = true
	}
	if !profile.Validation.AllowUnknownSections {
		for key := range out.Sections {
			if !declared[key] {
				return out, documentDraftSectionsError{}
			}
		}
	}
	return out, nil
}

func validateDocumentDraftSections(draft models.DocumentDraft, profile documentprofile.DocumentProfile) error {
	for _, section := range profile.Sections {
		if section.Required && strings.TrimSpace(draft.Sections[section.Key].Content) == "" {
			return requiredJSONFieldError{field: "sections." + section.Key + ".content"}
		}
	}
	return nil
}
