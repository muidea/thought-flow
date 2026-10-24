package ai

import "strings"

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
	if err == nil || !usedFallback {
		return err
	}
	return ProviderError{Code: "thoughtflow.ai.invalid_json_fallback", Message: "JSON fallback validation failed: " + err.Error(), Retryable: false}
}
