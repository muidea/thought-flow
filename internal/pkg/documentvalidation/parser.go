package documentvalidation

import (
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/parser"
	"github.com/yuin/goldmark/text"
	"github.com/yuin/goldmark/util"
)

// ParseBody and VisibleText share the delivery validator's Markdown semantics
// with repair scope checks.
func ParseBody(source []byte) ast.Node             { return bodyParser().Parse(text.NewReader(source)) }
func VisibleText(n ast.Node, source []byte) string { return plain(n, source) }

// Track closure in the actual parser rather than counting fence-like lines.
// This also handles empty fences, long delimiters and nested blockquotes/lists.
type fenceParser struct{ parser.BlockParser }

func (p fenceParser) Continue(node ast.Node, reader text.Reader, pc parser.Context) parser.State {
	state := p.BlockParser.Continue(node, reader, pc)
	if state == parser.Close {
		node.SetAttributeString("document_fence_closed", true)
	}
	return state
}
func bodyParser() parser.Parser {
	// The embedded markdown-it renderer has html:false. Parse those same bytes
	// as ordinary text so HTML-looking placeholders cannot escape validation.
	blocks := []util.PrioritizedValue{}
	for _, entry := range parser.DefaultBlockParsers() {
		if entry.Priority != 900 {
			blocks = append(blocks, entry)
		}
	}
	blocks = append(blocks, util.Prioritized(fenceParser{parser.NewFencedCodeBlockParser()}, 699))
	inline := []util.PrioritizedValue{}
	for _, entry := range parser.DefaultInlineParsers() {
		if entry.Priority != 400 {
			inline = append(inline, entry)
		}
	}
	return parser.NewParser(parser.WithBlockParsers(blocks...), parser.WithInlineParsers(inline...), parser.WithParagraphTransformers(parser.DefaultParagraphTransformers()...))
}
