package ai

import (
	"encoding/json"
	"errors"
	"strings"
)

// These fields are extensions to the OpenAI error envelope used by compatible
// gateways. Keep the unsupported capability available to jobs and the chat UI.
type conversionErrorDetails struct {
	Code                string   `json:"code"`
	Type                string   `json:"type"`
	Feature             string   `json:"feature"`
	UnsupportedFeatures []string `json:"unsupported_features"`
}

func (e conversionErrorDetails) unsupported() bool {
	return strings.EqualFold(strings.TrimSpace(e.Code), "conversion_unsupported") ||
		strings.EqualFold(strings.TrimSpace(e.Type), "conversion_unsupported")
}

func (e ProviderError) onlyResponseFormatUnsupported() bool {
	if e.Code != "thoughtflow.ai.conversion_unsupported" {
		return false
	}
	features := append([]string{e.Feature}, e.UnsupportedFeatures...)
	found := false
	for _, feature := range features {
		feature = strings.TrimSpace(feature)
		if feature == "" {
			continue
		}
		if feature != "response_format" && !strings.HasPrefix(feature, "response_format.") {
			return false
		}
		found = true
	}
	return found
}

// Fallback responses still pass the same parsers and business validators. A
// failed fallback is terminal so higher-level retries cannot repeat negotiation.
func jsonResponseError(err error, usedFallback bool) error {
	if err == nil {
		return err
	}
	var existing ProviderError
	if !usedFallback && errors.As(err, &existing) {
		existing.AutoRetry = false
		return existing
	}
	code := "thoughtflow.ai.invalid_json_response"
	if usedFallback {
		code = "thoughtflow.ai.invalid_json_fallback"
	}
	failure := ProviderError{Code: code, Message: "JSON response validation failed: " + err.Error(), Reason: "validation_failed", Retryable: false}
	var syntax *json.SyntaxError
	var fieldType *json.UnmarshalTypeError
	var missing requiredJSONFieldError
	switch {
	case errors.As(err, &syntax):
		failure.Reason = "invalid_json"
	case errors.As(err, &fieldType):
		failure.Reason, failure.Field = "invalid_field_type", fieldType.Field
	case errors.As(err, &missing):
		failure.Reason, failure.Field = "missing_required_field", missing.field
	}
	return failure
}

type requiredJSONFieldError struct{ field string }

func (e requiredJSONFieldError) Error() string { return e.field + " is required" }
