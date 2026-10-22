package documentvalidation

import (
	"regexp"
	"strings"

	"github.com/yuin/goldmark/ast"
)

var locatorText = regexp.MustCompile("(?:https?|thought|topic)://[^\\s<>\"`]+")

// Check destinations as well as visible naked URLs. Link labels alone cannot
// attest to where a delivered reference actually points.
func referenceLocators(n ast.Node, value string, body []byte) []string {
	seen := map[string]bool{}
	out := []string{}
	add := func(value string) {
		if !seen[value] {
			seen[value] = true
			out = append(out, value)
		}
	}
	ast.Walk(n, func(child ast.Node, enter bool) (ast.WalkStatus, error) {
		if !enter {
			return ast.WalkContinue, nil
		}
		switch link := child.(type) {
		case *ast.Link:
			add(MarkdownLocator(string(link.Destination)))
		case *ast.AutoLink:
			add(string(link.URL(body)))
		case *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	for _, raw := range locatorText.FindAllString(value, -1) {
		add(MarkdownLocator(trimLocatorPunctuation(raw)))
	}
	return out
}

func trimLocatorPunctuation(raw string) string {
	pairs := [][2]string{{"(", ")"}, {"[", "]"}, {"{", "}"}, {"（", "）"}}
	var excess [4]int
	for i, pair := range pairs {
		excess[i] = strings.Count(raw, pair[1]) - strings.Count(raw, pair[0])
	}
	for {
		raw = strings.TrimRight(raw, ".,;:!?，。；：")
		trimmed := false
		for i, pair := range pairs {
			// Naked URLs may be wrapped in prose parentheses. Keep paired
			// delimiters that belong to the URL, including nested pairs.
			if strings.HasSuffix(raw, pair[1]) && excess[i] > 0 {
				raw = strings.TrimSuffix(raw, pair[1])
				excess[i]--
				trimmed = true
				break
			}
		}
		if !trimmed {
			return raw
		}
	}
}
