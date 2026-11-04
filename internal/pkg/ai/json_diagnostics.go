package ai

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"

	"thoughtflow/internal/pkg/models"
)

// JSONResponseDiagnostic is private failure evidence, not a public API payload.
// Content contains only the model completion, never prompts or HTTP credentials.
// Callers must persist it under their existing private storage boundary.
type JSONResponseDiagnostic struct {
	Version          int    `json:"version"`
	RequestID        string `json:"request_id"`
	StatusCode       int    `json:"status_code"`
	ResponseShape    string `json:"response_shape"`
	ResponseBytes    int    `json:"response_bytes"`
	ResponseHash     string `json:"response_hash"`
	FinishReason     string `json:"finish_reason,omitempty"`
	ResponseStatus   string `json:"response_status,omitempty"`
	PromptTokens     int64  `json:"prompt_tokens,omitempty"`
	CompletionTokens int64  `json:"completion_tokens,omitempty"`
	Content          string `json:"content"`
}

// Keep raw evidence out of Error(), ordinary logs and default JSON marshaling.
type jsonValidationError struct {
	failure    ProviderError
	diagnostic JSONResponseDiagnostic
	cause      error
}

func (e *jsonValidationError) Error() string { return e.failure.Error() }
func (e *jsonValidationError) Unwrap() []error {
	if e.cause != nil {
		return []error{e.failure, e.cause}
	}
	return []error{e.failure}
}

func JSONResponseErrorDiagnostic(err error) (JSONResponseDiagnostic, bool) {
	var failure *jsonValidationError
	if errors.As(err, &failure) {
		return failure.diagnostic, true
	}
	return JSONResponseDiagnostic{}, false
}

// One trace belongs to one synchronous structured-output call, including its
// capability fallback. It is discarded on success and never shared across calls.
type jsonResponseTrace struct{ diagnostic JSONResponseDiagnostic }
type jsonResponseTraceKey struct{}

func withJSONResponseTrace(ctx context.Context) (context.Context, *jsonResponseTrace) {
	trace := &jsonResponseTrace{}
	return context.WithValue(ctx, jsonResponseTraceKey{}, trace), trace
}

func (t *jsonResponseTrace) capture(requestID string, status int, content string) {
	t.diagnostic = JSONResponseDiagnostic{Version: 1, RequestID: requestID, StatusCode: status,
		ResponseShape: jsonResponseShape(content), ResponseBytes: len(content), ResponseHash: models.ContentHash(content), Content: content}
}

func (t *jsonResponseTrace) validationError(err error, fallback bool) error {
	failure := jsonResponseError(err, fallback)
	var providerErr ProviderError
	if !errors.As(failure, &providerErr) || t.diagnostic.Version == 0 {
		return failure
	}
	d := t.diagnostic
	providerErr.RequestID, providerErr.StatusCode = d.RequestID, d.StatusCode
	providerErr.ResponseShape, providerErr.ResponseBytes, providerErr.ResponseHash = d.ResponseShape, d.ResponseBytes, d.ResponseHash
	var syntax *json.SyntaxError
	if errors.As(err, &syntax) {
		providerErr.JSONOffset = syntax.Offset
	}
	if syntax, offset := jsonObjectSyntaxFailure(d.Content); syntax != nil {
		providerErr.Reason = "invalid_json"
		providerErr.Message = "JSON object has invalid syntax; output was rejected without repair or automatic retry"
		providerErr.JSONOffset = offset
	}
	if strings.HasPrefix(err.Error(), "json: unknown field ") {
		// Unknown key names are model-controlled and may contain source text.
		// The full response is available only through the private diagnostic.
		providerErr.Reason = "unknown_field"
		providerErr.Message = "JSON response contains fields outside the requested schema"
	}
	if incompleteJSONObject(d.Content) {
		providerErr.Reason = "incomplete_json"
		providerErr.Message = "JSON response is incomplete; output was rejected without repair or automatic retry"
		// Byte position in the original response, including recognized wrappers.
		providerErr.JSONOffset = int64(len(d.Content) + 1)
	}
	slog.Warn("LLM structured output validation failed", "request_id", d.RequestID, "code", providerErr.Code, "reason", providerErr.Reason, "response_shape", d.ResponseShape, "response_bytes", d.ResponseBytes, "response_hash", d.ResponseHash, "json_offset", providerErr.JSONOffset)
	return &jsonValidationError{failure: providerErr, diagnostic: d}
}

// Only fixed labels leave the parser. Never copy provider tag names, body
// snippets or error-page text into ordinary logs or the persisted chat error.
func jsonResponseShape(content string) string {
	value := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(content), "\ufeff"))
	if value == "" {
		return "empty"
	}
	if incompleteJSONObject(content) {
		return "incomplete_json_object"
	}
	if syntax, _ := jsonObjectSyntaxFailure(content); syntax != nil {
		return "invalid_json_object"
	}
	if strings.HasPrefix(value, "<escape>") {
		return "escape_prefix"
	}
	lower := strings.ToLower(value)
	for _, prefix := range []string{"<!doctype html", "<html", "<head", "<body"} {
		if strings.HasPrefix(lower, prefix) && (len(lower) == len(prefix) || strings.ContainsRune(" \t\r\n\f/>", rune(lower[len(prefix)]))) {
			return "html_document"
		}
	}
	for _, tag := range []string{"think", "analysis"} {
		if strings.HasPrefix(value, "<"+tag+">") {
			end := strings.Index(value, "</"+tag+">")
			if end < 0 {
				return "unclosed_reasoning_wrapper"
			}
			if strings.TrimSpace(value[end+len(tag)+3:]) == "" {
				return "reasoning_only"
			}
			return "reasoning_wrapper"
		}
	}
	if strings.HasPrefix(value, "```") {
		return "markdown_fence"
	}
	if strings.HasPrefix(value, "<") {
		return "unrecognized_markup"
	}
	if strings.HasPrefix(value, "{") {
		if json.Valid([]byte(value)) {
			return "json_object"
		}
		return "invalid_json_object"
	}
	if json.Valid([]byte(value)) {
		return "non_object_json"
	}
	return "non_json_text"
}
