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
			add(string(link.Destination))
		case *ast.AutoLink:
			add(string(link.URL(body)))
		case *ast.CodeSpan:
			return ast.WalkSkipChildren, nil
		}
		return ast.WalkContinue, nil
	})
	for _, raw := range locatorText.FindAllString(value, -1) {
		add(strings.TrimRight(raw, ").,;:!?，。；："))
	}
	return out
}
