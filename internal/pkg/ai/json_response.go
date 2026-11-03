package ai

import (
	"encoding/json"
	"strings"
)

const jsonObjectInstructions = "Output exactly one complete JSON object matching the requested schema. Do not wrap it in reasoning tags, HTML or Markdown fences, or add commentary or example objects before or after it. Preserve all required document content inside JSON string fields."

// unwrapJSONResponse removes only complete, recognized presentation wrappers
// around one JSON object. It does not search prose, HTML, reasoning or nested
// examples for a usable answer, repair JSON, or relax the caller's schema checks.
// Unrecognized/incomplete output is left intact for the existing error path.
func unwrapJSONResponse(content string) string {
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "\ufeff"))
	for _, tag := range []string{"think", "analysis"} {
		open, close := "<"+tag+">", "</"+tag+">"
		if strings.HasPrefix(value, open) {
			end := strings.Index(value[len(open):], close)
			if end < 0 {
				return content
			}
			value = strings.TrimSpace(value[len(open)+end+len(close):])
			break
		}
	}
	if strings.HasPrefix(value, "```") {
		line := strings.IndexByte(value, '\n')
		if line < 0 {
			return content
		}
		language := strings.ToLower(strings.TrimSpace(value[3:line]))
		if language != "" && language != "json" {
			return content
		}
		body := strings.TrimSpace(value[line+1:])
		end := strings.LastIndexByte(body, '\n')
		if end < 0 || strings.TrimSpace(body[end+1:]) != "```" {
			return content
		}
		value = strings.TrimSpace(body[:end])
	}
	if !strings.HasPrefix(value, "{") || !json.Valid([]byte(value)) {
		return content
	}
	return value
}
