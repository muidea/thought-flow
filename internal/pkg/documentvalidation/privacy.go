package documentvalidation

import (
	"net/url"
	"regexp"
	"strings"
)

var urlText = regexp.MustCompile(`https?://[^\s<>"\x60]+`)
var credentialText = regexp.MustCompile(`(?i)(bearer\s+|api[_ -]?key\s*[:=]\s*)[^\s,;]+`)

func SensitiveLocator(locator string) bool {
	u, e := url.Parse(locator)
	if e != nil {
		return true
	}
	if u.User != nil {
		return true
	}
	for key := range u.Query() {
		switch strings.ToLower(key) {
		case "token", "access_token", "api_key", "apikey", "key", "password", "secret", "auth", "authorization", "signature", "sig":
			return true
		}
	}
	return false
}

// Redact is used only for diagnostics. Delivery bytes are never silently edited.
func Redact(value string) string {
	value = urlText.ReplaceAllStringFunc(value, func(raw string) string {
		u, e := url.Parse(raw)
		if e != nil {
			return "[redacted URL]"
		}
		u.User = nil
		query := u.Query()
		for key := range query {
			switch strings.ToLower(key) {
			case "token", "access_token", "api_key", "apikey", "key", "password", "secret", "auth", "authorization", "signature", "sig":
				query.Set(key, "REDACTED")
			}
		}
		u.RawQuery = query.Encode()
		return u.String()
	})
	return credentialText.ReplaceAllString(value, "${1}REDACTED")
}
