package ai

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/openai/openai-go/responses"
)

const maxResponsesBytes = 4 << 20

func responseWireError(message string) ProviderError {
	return ProviderError{Code: "thoughtflow.ai.invalid_response", Message: message}
}

// Preserve a real EOF at the budget boundary; detect one extra byte without
// handing it to any decoder. In particular, SSE completion cannot hide overflow.
type boundedResponseBody struct {
	io.ReadCloser
	remaining int64
}

func (b *boundedResponseBody) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	if b.remaining == 0 {
		var extra [1]byte
		n, err := b.ReadCloser.Read(extra[:])
		if n > 0 {
			return 0, responseWireError("LLM response exceeds the response size limit")
		}
		return 0, err
	}
	if int64(len(p)) > b.remaining {
		p = p[:b.remaining]
	}
	n, err := b.ReadCloser.Read(p)
	b.remaining -= int64(n)
	return n, err
}

// Decode the same response, never replay a completed/unknown model request.
// Some gateways copy an SSE Content-Type onto their assembled JSON response.
// Actual framing, rather than that header, determines the decoder. Buffering is
// bounded and no partial text is exposed before EOF and completion validation.
func readResponsesHTTP(ctx context.Context, raw *http.Response) (*responses.Response, string, error) {
	if raw == nil || raw.Body == nil {
		return nil, "", responseWireError("Responses API returned no body")
	}
	defer raw.Body.Close()
	body, err := io.ReadAll(io.LimitReader(raw.Body, maxResponsesBytes+1))
	if ctx.Err() != nil {
		return nil, "", ctx.Err()
	}
	if err != nil {
		return nil, "", err
	}
	if len(body) > maxResponsesBytes {
		return nil, "", responseWireError("LLM response exceeds the response size limit")
	}
	framed := bytes.TrimPrefix(body, []byte{0xef, 0xbb, 0xbf})
	body = bytes.TrimSpace(framed)
	if len(body) > 0 && body[0] == '{' {
		var result responses.Response
		if json.Unmarshal(body, &result) != nil {
			return nil, "json", responseWireError("Responses API returned an invalid JSON envelope")
		}
		return &result, "json", nil
	}
	// Keep the original final newline: an SSE record must end with a blank
	// line. A truncated terminal record is not a complete response.
	if bytes.HasPrefix(body, []byte("event:")) || bytes.HasPrefix(body, []byte("data:")) || bytes.HasPrefix(body, []byte(":")) {
		result, err := decodeResponsesSSE(framed)
		return result, "sse", err
	}
	return nil, "", responseWireError("Responses API returned neither JSON nor Responses SSE")
}

type observedResponseText struct {
	text   strings.Builder
	done   *string
	itemID string
	seen   bool
}

func decodeResponsesSSE(body []byte) (*responses.Response, error) {
	fail := func() (*responses.Response, error) {
		return nil, responseWireError("Responses SSE is incomplete or inconsistent; output was rejected")
	}
	var completed *responses.Response
	var responseID string
	texts := map[[2]int]*observedResponseText{}
	var eventName string
	var data []string
	done := false
	dispatch := func() bool {
		if len(data) == 0 {
			return true
		}
		payload := strings.Join(data, "\n")
		if payload == "[DONE]" {
			if done || completed == nil {
				return false
			}
			done = true
			return true
		}
		if done || completed != nil {
			return false
		}
		var event struct {
			Type         string          `json:"type"`
			Response     json.RawMessage `json:"response"`
			OutputIndex  *int            `json:"output_index"`
			ContentIndex *int            `json:"content_index"`
			ItemID       string          `json:"item_id"`
			Delta        *string         `json:"delta"`
			Text         *string         `json:"text"`
		}
		if json.Unmarshal([]byte(payload), &event) != nil || !strings.HasPrefix(event.Type, "response.") || (eventName != "" && event.Type != eventName) {
			return false
		}
		switch event.Type {
		case "response.failed", "response.incomplete", "response.error":
			return false
		case "response.created", "response.in_progress", "response.completed":
			var snapshot responses.Response
			if json.Unmarshal(event.Response, &snapshot) != nil || snapshot.ID == "" || snapshot.Object != "response" || (responseID != "" && snapshot.ID != responseID) {
				return false
			}
			responseID = snapshot.ID
			if event.Type == "response.completed" {
				completed = &snapshot
			}
		case "response.output_text.delta", "response.output_text.done":
			if event.OutputIndex == nil || event.ContentIndex == nil || *event.OutputIndex < 0 || *event.ContentIndex < 0 {
				return false
			}
			key := [2]int{*event.OutputIndex, *event.ContentIndex}
			observed := texts[key]
			if observed == nil {
				observed = &observedResponseText{}
				texts[key] = observed
			}
			if observed.done != nil || (observed.itemID != "" && event.ItemID != "" && observed.itemID != event.ItemID) {
				return false
			}
			if event.ItemID != "" {
				observed.itemID = event.ItemID
			}
			if event.Type == "response.output_text.delta" {
				if event.Delta == nil {
					return false
				}
				observed.seen = true
				observed.text.WriteString(*event.Delta)
			} else {
				if event.Text == nil || (observed.seen && observed.text.String() != *event.Text) {
					return false
				}
				observed.done = event.Text
			}
		}
		return true
	}
	scanner := bufio.NewScanner(bytes.NewReader(body))
	scanner.Buffer(make([]byte, 4096), maxResponsesBytes+1)
	for scanner.Scan() {
		line := scanner.Text()
		if line == "" {
			if !dispatch() {
				return fail()
			}
			eventName, data = "", nil
			continue
		}
		if strings.HasPrefix(line, ":") {
			continue
		}
		field, value, _ := strings.Cut(line, ":")
		value = strings.TrimPrefix(value, " ")
		switch field {
		case "event":
			eventName = value
		case "data":
			data = append(data, value)
		case "id", "retry": // SSE transport metadata, not model content.
		default:
			return fail()
		}
	}
	if scanner.Err() != nil || len(data) > 0 || eventName != "" || completed == nil {
		return fail()
	}
	for key, observed := range texts {
		if key[0] >= len(completed.Output) || key[1] >= len(completed.Output[key[0]].Content) {
			return fail()
		}
		item := completed.Output[key[0]]
		part := item.Content[key[1]]
		if part.Type != "output_text" || (observed.itemID != "" && observed.itemID != item.ID) ||
			(observed.seen && observed.text.String() != part.Text) || (observed.done != nil && *observed.done != part.Text) {
			return fail()
		}
	}
	return completed, nil
}
