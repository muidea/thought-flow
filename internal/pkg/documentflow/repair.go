package documentflow

import (
	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/text"
	"strings"
	"thoughtflow/internal/pkg/documentstore"
)

// CheckRepairScope compares complete byte slices outside selected AST sections.
// Even a cooperative model cannot change an unselected title/reference section.
func CheckRepairScope(before, after string, sections []string) error {
	allowed := map[string]bool{}
	for _, s := range sections {
		allowed[s] = true
	}
	skeleton := func(body string) (string, map[string]bool) {
		source := []byte(documentstore.Normalize(body))
		tree := goldmark.DefaultParser().Parse(text.NewReader(source))
		type boundary struct {
			start int
			name  string
		}
		bounds := []boundary{{0, "__preamble"}}
		ast.Walk(tree, func(n ast.Node, enter bool) (ast.WalkStatus, error) {
			if enter {
				if h, ok := n.(*ast.Heading); ok && h.Level == 2 && h.Lines().Len() > 0 {
					offset := h.Lines().At(0).Start
					start := strings.LastIndex(string(source[:offset]), "\n") + 1
					bounds = append(bounds, boundary{start, string(h.Text(source))})
				}
			}
			return ast.WalkContinue, nil
		})
		out := strings.Builder{}
		seen := map[string]bool{}
		for i, b := range bounds {
			end := len(source)
			if i+1 < len(bounds) {
				end = bounds[i+1].start
			}
			if allowed[b.name] {
				out.WriteString("## " + b.name + "\n<selected>\n")
				seen[b.name] = true
			} else {
				out.Write(source[b.start:end])
			}
		}
		return out.String(), seen
	}
	old, seen := skeleton(before)
	next, newSeen := skeleton(after)
	if old != next {
		return ErrRules
	}
	for name := range allowed {
		if !seen[name] || !newSeen[name] {
			return ErrRules
		}
	}
	return nil
}
