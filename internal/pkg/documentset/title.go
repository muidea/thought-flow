package documentset

import (
	"encoding/hex"
	"strings"
	"unicode"
)

// OpaqueSetTitle identifies an internal set identity used as a display title.
func OpaqueSetTitle(value string) bool {
	value = strings.TrimSpace(value)
	if !strings.HasPrefix(value, "set-") || len(value) < 20 {
		return false
	}
	_, err := hex.DecodeString(strings.TrimPrefix(value, "set-"))
	return err == nil
}

// DisplayTitle uses extracted subject context rather than an internal identity
// or a generic profile label. It does not require another model request.
func DisplayTitle(shared SharedDesign, preferred, profile string) string {
	normalize := func(value string) string {
		value = strings.Map(func(r rune) rune {
			if unicode.IsControl(r) {
				return ' '
			}
			return r
		}, value)
		value = strings.Join(strings.Fields(value), " ")
		if OpaqueSetTitle(value) {
			return ""
		}
		runes := []rune(value)
		if len(runes) > 60 {
			value = string(runes[:60]) + "…"
		}
		return value
	}
	if strings.TrimSpace(preferred) != strings.TrimSpace(profile) {
		if value := normalize(preferred); value != "" {
			return value
		}
	}
	for _, key := range []string{"project_name", "title", "subject", "goal"} {
		if value := normalize(shared.Inputs[key]); value != "" {
			return value
		}
	}
	for _, fact := range shared.Facts {
		if fact.Kind == "requirement" {
			if value := normalize(fact.Name); value != "" {
				return value
			}
		}
	}
	if value := normalize(profile); value != "" {
		return value
	}
	return "文档成果集"
}
