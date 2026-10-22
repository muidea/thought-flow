package documentvalidation

import (
	"html"
	"net/url"
	"regexp"
	"strings"

	"github.com/yuin/goldmark/util"
)

var urlText = regexp.MustCompile(`https?://[^\s<>"\x60]+`)
var credentialText = regexp.MustCompile(`(?i)(bearer\s+|api[_ -]?key\s*[:=]\s*)[^\s,;]+`)
var markdownEscape = regexp.MustCompile(`\\[[:punct:]]|&[a-zA-Z#][a-zA-Z0-9]{1,31};`)

// MarkdownLocator resolves Markdown escapes/entities before URL validation,
// matching rendered link/image destinations. Percent escapes remain encoded
// until the URL parser handles them. Autolinks already carry literal URLs.
func MarkdownLocator(locator string) string {
	// markdown-it unescapes once, in a single pass. Separate punctuation and
	// entity passes would incorrectly decode an escaped ampersand twice.
	decoded := markdownEscape.ReplaceAllStringFunc(locator, func(value string) string {
		if value[0] == '\\' {
			return value[1:]
		}
		return html.UnescapeString(value)
	})
	return string(util.URLEscape([]byte(decoded), false))
}

func SensitiveLocator(locator string) bool {
	u, e := url.Parse(locator)
	if e != nil {
		return true
	}
	if u.User != nil {
		return true
	}
	query, err := url.ParseQuery(u.RawQuery)
	if err != nil {
		return true
	}
	for key := range query {
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
		u, e := url.Parse(MarkdownLocator(raw))
		if e != nil {
			return "[redacted URL]"
		}
		u.User = nil
		query, err := url.ParseQuery(u.RawQuery)
		if err != nil {
			return "[redacted URL]"
		}
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
