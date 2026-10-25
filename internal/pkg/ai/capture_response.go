package ai

import (
	"encoding/json"
	"strings"
)

// Select a complete response that satisfies the required answer contract. A
// leading auxiliary JSON object is not necessarily the requested response.
// Decode each candidate independently; never merge objects or promote a nested
// example/metadata object into a conversation answer. No model retry is needed.
func parseCaptureContextResponse(content string) (captureContextJSON, error) {
	remaining := strings.TrimSpace(content)
	var firstErr error
	for start := strings.Index(remaining, "{"); start >= 0; start = strings.Index(remaining, "{") {
		candidate := extractJSONObject(remaining[start:])
		var parsed captureContextJSON
		err := json.Unmarshal([]byte(candidate), &parsed)
		if err == nil {
			if strings.TrimSpace(parsed.AssistantReply) != "" {
				return parsed, nil
			}
			err = requiredJSONFieldError{field: "assistant_reply"}
		}
		if firstErr == nil {
			firstErr = err
		}
		remaining = remaining[start+len(candidate):]
	}
	if firstErr == nil {
		var parsed captureContextJSON
		firstErr = json.Unmarshal([]byte(content), &parsed)
		if firstErr == nil {
			firstErr = requiredJSONFieldError{field: "assistant_reply"}
		}
	}
	return captureContextJSON{}, firstErr
}
