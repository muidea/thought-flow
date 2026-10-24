package ai

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
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

func (p *OpenAICompatibleProvider) executeGeneration(ctx context.Context, params openai.ChatCompletionNewParams, opts ...option.RequestOption) (string, error) {
	ctx, cancel := generationContext(ctx, p.requestTimeout)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	requestID := models.NewEventID(time.Now())
	var response *http.Response
	// Force zero SDK retries even if a caller supplies different request options.
	opts = append(opts, option.WithMaxRetries(0), option.WithHeader("X-Request-ID", requestID), option.WithResponseInto(&response))
	result, err := p.client.Chat.Completions.New(ctx, params, opts...)
	if response != nil {
		if upstreamID := safeDiagnostic(response.Header.Get("X-Request-ID")); upstreamID != "" {
			requestID = upstreamID
		}
	}
	scope, _ := ctx.Value(generationScopeKey{}).(string)
	attrs := []any{"model", p.chatModel, "scope_id", scope, "request_id", requestID, "json_format", params.ResponseFormat.OfJSONObject != nil}
	if response != nil {
		attrs = append(attrs, "status", response.StatusCode)
	}
	if err != nil {
		failure := wrapSDKError(err)
		var providerErr ProviderError
		if errors.As(failure, &providerErr) {
			if response != nil && response.StatusCode >= 400 && providerErr.StatusCode == 0 {
				providerErr.StatusCode = response.StatusCode
				providerErr.Code = "thoughtflow.ai.http_status"
				providerErr.Retryable = response.StatusCode == 429 || response.StatusCode >= 500
				if providerErr.Retryable {
					providerErr.Code = "thoughtflow.ai.transient_status"
				}
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
	failure := ProviderError{RequestID: requestID}
	if len(result.Choices) == 0 {
		failure.Code, failure.Message = "thoughtflow.ai.empty_completion", "chat completion returned no choices"
	} else if strings.EqualFold(strings.TrimSpace(result.Choices[0].FinishReason), "length") {
		failure.Code, failure.Message = "thoughtflow.ai.output_truncated", "chat completion stopped because the output token limit was reached"
	}
	if failure.Code != "" {
		slog.Warn("LLM generation returned incomplete output", append(attrs, "code", failure.Code)...)
		return "", failure
	}
	slog.Info("LLM generation request completed", attrs...)
	return result.Choices[0].Message.Content, nil
}
