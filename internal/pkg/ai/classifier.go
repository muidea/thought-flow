package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"

	"thoughtflow/internal/pkg/appconfig"
)

// ClassifyRequest is the input to a ClassifyProvider. The LLM receives the
// concatenated System + User prompts and returns a small JSON object that
// the service then translates into a Capture command.
type ClassifyRequest struct {
	System      string
	User        string
	Temperature float64
	MaxTokens   int
}

// ClassifyResult carries the raw LLM response and the model that produced
// it. The caller parses Raw to get a structured classification.
type ClassifyResult struct {
	Raw   string
	Model string
}

// ClassifyProvider is the abstraction Capture uses to decide whether
// incoming text is a URL, a free-form note, or a mix of both. The
// implementation is either an LLM-backed OpenAI-compatible provider or a
// zero-cost URL-regex local provider (used as a fast-path and as a
// fallback when no LLM is configured).
type ClassifyProvider interface {
	Classify(ctx context.Context, req ClassifyRequest) (ClassifyResult, error)
}

// urlPattern matches the common URL shapes users paste into Capture:
// http(s)://... and scheme-less www.example.com paths. Anchored at the
// first non-whitespace character so a pasted link inside a paragraph
// still counts.
var urlPattern = regexp.MustCompile(`(?i)\b((https?://|www\.)[^\s]+)`)

// LocalRefineClassify returns the URL/text classification using a regex
// and a length heuristic. It is intentionally simple — the goal is to
// short-circuit before paying for an LLM call when the input is
// obviously a URL, and to provide a sensible default when no LLM is
// configured. Confidence is always 1.0 because the regex is binary.
type LocalRefineClassify struct{}

// NewLocalRefineClassify builds a regex-based ClassifyProvider.
func NewLocalRefineClassify() *LocalRefineClassify {
	return &LocalRefineClassify{}
}

// Classify returns a JSON envelope with type=url when urlPattern matches
// (and the matched URL is the only meaningful content) or type=text
// otherwise. The model field is empty because no LLM ran.
func (LocalRefineClassify) Classify(_ context.Context, req ClassifyRequest) (ClassifyResult, error) {
	text := strings.TrimSpace(req.User)
	if text == "" {
		return ClassifyResult{Raw: `{"type":"text","confidence":1.0}`}, nil
	}
	if match := urlPattern.FindStringIndex(text); match != nil {
		url := strings.TrimRight(text[match[0]:match[1]], ".,;:!?\"'")
		// If the URL is the entire payload, classify as url. If there is
		// surrounding prose, classify as mixed so the capture service can
		// still attach a note alongside the URL fetch.
		prefix := strings.TrimSpace(text[:match[0]])
		suffix := strings.TrimSpace(text[match[1]:])
		if prefix == "" && suffix == "" {
			return ClassifyResult{Raw: fmt.Sprintf(`{"type":"url","extracted_url":%q,"confidence":1.0}`, url)}, nil
		}
		return ClassifyResult{Raw: fmt.Sprintf(`{"type":"mixed","extracted_url":%q,"confidence":1.0}`, url)}, nil
	}
	return ClassifyResult{Raw: `{"type":"text","confidence":1.0}`}, nil
}

// Classify implements ClassifyProvider on the existing OpenAI provider
// without disturbing its Refine/Embed/Synthesize methods. The chat
// completion prompt asks for a strict JSON object with a type, an
// optional extracted_url, and a confidence score.
func (p *OpenAICompatibleProvider) Classify(ctx context.Context, req ClassifyRequest) (ClassifyResult, error) {
	if strings.TrimSpace(p.apiKey) == "" {
		return ClassifyResult{}, errors.New("ai api key is required")
	}
	temperature := req.Temperature
	if temperature == 0 {
		temperature = 0
	}
	payload := map[string]any{
		"model":       p.chatModel,
		"temperature": temperature,
		"messages": []map[string]string{
			{"role": "system", "content": req.System},
			{"role": "user", "content": req.User},
		},
	}
	if req.MaxTokens > 0 {
		payload["max_tokens"] = req.MaxTokens
	}
	var response chatCompletionResponse
	if err := p.postJSON(ctx, "/chat/completions", payload, &response); err != nil {
		return ClassifyResult{}, err
	}
	if len(response.Choices) == 0 {
		return ClassifyResult{}, errors.New("classify returned no choices")
	}
	raw := strings.TrimSpace(response.Choices[0].Message.Content)
	if raw == "" {
		return ClassifyResult{}, errors.New("classify returned empty content")
	}
	// Validate that the LLM returned a JSON object we can parse. We do
	// not enforce a schema here — the caller decides which fields matter.
	if err := json.Unmarshal([]byte(extractJSONObject(raw)), &struct{}{}); err != nil {
		return ClassifyResult{}, fmt.Errorf("classify: parse json: %w", err)
	}
	return ClassifyResult{Raw: extractJSONObject(raw), Model: p.chatModel}, nil
}

// DefaultClassifySystem is the system prompt used by the capture service
// when constructing a ClassifyRequest for the LLM.
const DefaultClassifySystem = "You classify ThoughtFlow capture input. Return strict JSON only with fields: type (one of url, text, mixed), extracted_url (string, only when type is url or mixed), confidence (number between 0 and 1)."

// NewClassifyProvider returns a ClassifyProvider. The local regex is used
// when no LLM is configured; otherwise the OpenAI-compatible provider
// handles the call.
func NewClassifyProvider(cfg appconfig.LLMConfig) ClassifyProvider {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return NewLocalRefineClassify()
	}
	return NewOpenAICompatibleProvider(cfg, appconfig.EmbeddingConfig{})
}
