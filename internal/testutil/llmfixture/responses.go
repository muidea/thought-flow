// Package llmfixture builds native Responses envelopes for controlled LLM tests.
package llmfixture

import "encoding/json"

func Response(text string, extra ...map[string]any) map[string]any {
	result := map[string]any{
		"object": "response", "status": "completed",
		"output": []any{map[string]any{
			"type": "message", "role": "assistant", "status": "completed",
			"content": []any{map[string]any{"type": "output_text", "text": text}},
		}},
	}
	for _, fields := range extra {
		for key, value := range fields {
			result[key] = value
		}
	}
	return result
}

// SSE supplies the authoritative completed snapshot, without inventing deltas.
func SSE(text string, extra ...map[string]any) string {
	result := Response(text, extra...)
	if result["id"] == nil {
		result["id"] = "resp-fixture"
	}
	raw, _ := json.Marshal(map[string]any{"type": "response.completed", "response": result})
	return "event: response.completed\ndata: " + string(raw) + "\n\n"
}
