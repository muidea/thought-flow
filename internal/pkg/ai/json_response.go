package ai

import (
	"encoding/json"
	"errors"
	"strings"
)

// Inspect the recognized object even when it is invalid. The acceptance path
// deliberately keeps invalid wrapped text intact; diagnostics must still locate
// the actual syntax failure rather than the wrapper's opening '<'. Offsets are
// one-based UTF-8 byte positions in the original completion.
func jsonObjectSyntaxFailure(content string) (*json.SyntaxError, int64) {
	value := jsonResponseCandidate(content)
	if !strings.HasPrefix(value, "{") {
		return nil, 0
	}
	var raw json.RawMessage
	err := json.Unmarshal([]byte(value), &raw)
	var syntax *json.SyntaxError
	if !errors.As(err, &syntax) {
		return nil, 0
	}
	// The candidate is an unchanged substring. The final occurrence is the
	// answer, even if an identical example appeared inside discarded reasoning.
	start := strings.LastIndex(content, value)
	if start < 0 {
		return nil, 0
	}
	return syntax, int64(start) + syntax.Offset
}

const jsonObjectInstructions = "Output exactly one complete JSON object matching the requested schema. Do not wrap it in reasoning tags, HTML or Markdown fences, or add commentary or example objects before or after it. Preserve all required document content inside JSON string fields."

// unwrapJSONResponse removes only complete, recognized presentation wrappers
// around one JSON object. It does not search prose, HTML, reasoning or nested
// examples for a usable answer, repair JSON, or relax the caller's schema checks.
// Unrecognized/incomplete output is left intact for the existing error path.
func unwrapJSONResponse(content string) string {
	value := jsonResponseCandidate(content)
	if !strings.HasPrefix(value, "{") || !json.Valid([]byte(value)) {
		return content
	}
	return value
}

// Recognize presentation only; this candidate is never accepted without the
// complete-object check above. Diagnostics also inspect it to distinguish an
// unfinished JSON value from an unknown markup response.
func jsonResponseCandidate(content string) string {
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
	// Text providers may use this exact marker either as a prefix or as a
	// paired wrapper. Only one outer pair is removed, never arbitrary markup.
	if strings.HasPrefix(value, "<escape>") {
		value = strings.TrimSpace(strings.TrimPrefix(value, "<escape>"))
		if strings.HasSuffix(value, "</escape>") {
			value = strings.TrimSpace(strings.TrimSuffix(value, "</escape>"))
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
	return value
}

func incompleteJSONObject(content string) bool {
	value := jsonResponseCandidate(content)
	if !strings.HasPrefix(value, "{") {
		return false
	}
	var out json.RawMessage
	err := json.Unmarshal([]byte(value), &out)
	// encoding/json uses this error only when parsing reaches EOF before a
	// value is complete. Malformed syntax in the middle remains invalid_json.
	return err != nil && err.Error() == "unexpected end of JSON input"
}
