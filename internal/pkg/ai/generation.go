package ai

import (
	"context"
	"errors"
	"log/slog"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go/option"
	"github.com/openai/openai-go/responses"
	"thoughtflow/internal/pkg/models"
)

// Retryable describes a possible later attempt, not permission to replay a
// generation whose completion is unknown. Only a policy with execution evidence
// may set AutoRetry. HTTP status, Retry-After and token estimates are insufficient.
func CanAutomaticallyRetry(err error) bool {
	var failure ProviderError
	return errors.As(err, &failure) && failure.Retryable && failure.AutoRetry
}

func generationTimeout(timeout time.Duration) time.Duration {
	if timeout <= 0 {
		return 2 * time.Minute
	}
	return timeout
}

func generationContext(ctx context.Context, timeout time.Duration) (context.Context, context.CancelFunc) {
	deadline := time.Now().Add(generationTimeout(timeout))
	if existing, ok := ctx.Deadline(); ok && !existing.After(deadline) {
		return context.WithCancel(ctx)
	}
	return context.WithDeadline(ctx, deadline)
}

// WithGenerationBudget establishes the operation deadline before capability
// waiting, generation and explicit validation repairs. Nested calls only shorten it.
func WithGenerationBudget(ctx context.Context, provider any) (context.Context, context.CancelFunc) {
	timeout := 2 * time.Minute
	if bounded, ok := provider.(interface{ GenerationTimeout() time.Duration }); ok {
		timeout = bounded.GenerationTimeout()
	}
	return generationContext(ctx, timeout)
}

func (p *OpenAICompatibleProvider) GenerationTimeout() time.Duration { return p.requestTimeout }

type generationScopeKey struct{}

type requiredJSONKey struct{}

// WithRequiredJSON requires native json_object support for this operation and
// its child calls. It bypasses the ordinary conversation capability cache and
// never retries without text.format. It does not change that cache.
func WithRequiredJSON(ctx context.Context) context.Context {
	return context.WithValue(ctx, requiredJSONKey{}, true)
}

// WithGenerationScope associates safe job/operation/session identity with gateway
// request IDs. It never changes the deadline or places source content in headers.
func WithGenerationScope(ctx context.Context, id string) context.Context {
	if current, _ := ctx.Value(generationScopeKey{}).(string); current != "" {
		return ctx
	}
	return context.WithValue(ctx, generationScopeKey{}, safeDiagnostic(id))
}

func safeDiagnostic(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 160 {
		return ""
	}
	for _, r := range value {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("-_.:/", r)) {
			return ""
		}
	}
	return value
}

func (p *OpenAICompatibleProvider) executeGeneration(ctx context.Context, params responses.ResponseNewParams, opts ...option.RequestOption) (string, error) {
	ctx, cancel := generationContext(ctx, p.requestTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	requestID := models.NewEventID(time.Now())
	var response *http.Response
	var raw *http.Response
	// Force zero SDK retries even if a caller supplies different request options.
	opts = append(opts, option.WithMaxRetries(0), option.WithHeader("X-Request-ID", requestID),
		option.WithHeader("Accept", "application/json"), option.WithJSONSet("stream", false),
		option.WithResponseInto(&response), option.WithResponseBodyInto(&raw))
	_, err := p.client.Responses.New(ctx, params, opts...)
	var result *responses.Response
	var wire string
	if err == nil {
		result, wire, err = readResponsesHTTP(ctx, raw)
	}
	if response != nil {
		if upstreamID := safeDiagnostic(response.Header.Get("X-Request-ID")); upstreamID != "" {
			requestID = upstreamID
		}
	}
	scope, _ := ctx.Value(generationScopeKey{}).(string)
	requiredJSON, _ := ctx.Value(requiredJSONKey{}).(bool)
	attrs := []any{"model", p.chatModel, "scope_id", scope, "request_id", requestID, "endpoint", "/v1/responses", "json_format", params.Text.Format.OfJSONObject != nil, "json_required", requiredJSON}
	if response != nil {
		attrs = append(attrs, "status", response.StatusCode)
		if mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type")); err == nil {
			attrs = append(attrs, "response_content_type", safeDiagnostic(mediaType))
		}
	}
	if wire != "" {
		attrs = append(attrs, "response_wire", wire)
	}
	if err != nil {
		failure := err
		var providerErr ProviderError
		if !errors.As(failure, &providerErr) {
			failure = wrapSDKError(err)
		}
		if errors.As(failure, &providerErr) {
			if response != nil && response.StatusCode >= 400 && providerErr.StatusCode == 0 {
				providerErr.StatusCode = response.StatusCode
				providerErr.Code = "thoughtflow.ai.http_status"
				providerErr.Retryable = response.StatusCode == 429 || response.StatusCode >= 500
				if providerErr.Retryable {
					providerErr.Code = "thoughtflow.ai.transient_status"
				}
			}
			if response != nil && providerErr.StatusCode == 0 {
				providerErr.StatusCode = response.StatusCode
			}
			providerErr.RequestID = requestID
			providerErr.AutoRetry = false
			attrs = append(attrs, "code", providerErr.Code, "gateway_code", providerErr.GatewayCode, "failure_class", providerErr.FailureClass, "feature", providerErr.Feature, "unsupported_features", providerErr.UnsupportedFeatures)
			slog.Warn("LLM generation request failed", attrs...)
			return "", providerErr
		}
		slog.Warn("LLM generation request canceled", attrs...)
		return "", failure
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	content, failure := responseText(result)
	if failure.Code != "" {
		failure.RequestID = requestID
		if response != nil {
			failure.StatusCode = response.StatusCode
		}
		if result != nil {
			failure.Reason = safeDiagnostic(string(result.IncompleteDetails.Reason))
			failure.GatewayCode = safeDiagnostic(string(result.Error.Code))
		}
		slog.Warn("LLM generation returned incomplete output", append(attrs, "code", failure.Code)...)
		return "", failure
	}
	if trace, ok := ctx.Value(jsonResponseTraceKey{}).(*jsonResponseTrace); ok {
		status := 0
		if response != nil {
			status = response.StatusCode
		}
		trace.capture(requestID, status, content)
		trace.diagnostic.ResponseStatus = string(result.Status)
		trace.diagnostic.PromptTokens = result.Usage.InputTokens
		trace.diagnostic.CompletionTokens = result.Usage.OutputTokens
	}
	slog.Info("LLM generation request completed", attrs...)
	return content, nil
}

// Only completed assistant text is usable. Never accept a partial answer, a
// refusal, an unexecuted tool call or a legacy Chat Completions envelope as text.
func responseText(result *responses.Response) (string, ProviderError) {
	fail := func(code, message string) (string, ProviderError) {
		return "", ProviderError{Code: "thoughtflow.ai." + code, Message: message}
	}
	if result == nil || result.Object != "response" {
		return fail("invalid_response", "expected a Responses API response object")
	}
	if result.Error.Code != "" || result.Error.Message != "" || result.Status == "failed" {
		return fail("response_failed", "Responses generation failed; output was rejected")
	}
	if result.Status == "incomplete" && result.IncompleteDetails.Reason == "max_output_tokens" {
		return fail("output_truncated", "Responses generation exceeded max_output_tokens; partial output was rejected")
	}
	if result.Status != "completed" || result.IncompleteDetails.Reason != "" {
		return fail("output_incomplete", "Responses generation is not complete; output was rejected")
	}
	var text strings.Builder
	for _, item := range result.Output {
		if item.Type == "reasoning" {
			continue
		}
		if item.Type != "message" || item.Role != "assistant" || item.Status != "completed" {
			return fail("invalid_response", "Responses output contains an unsupported or incomplete item")
		}
		for _, part := range item.Content {
			if part.Type == "refusal" {
				return fail("refusal", "the model refused the request; output was rejected")
			}
			if part.Type != "output_text" {
				return fail("invalid_response", "Responses message contains unsupported content")
			}
			text.WriteString(part.Text)
		}
	}
	if strings.TrimSpace(text.String()) == "" {
		return fail("empty_completion", "Responses generation returned no assistant text")
	}
	return text.String(), ProviderError{}
}
