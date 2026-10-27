package documentsetstore

import (
	"fmt"

	"github.com/yuin/goldmark/ast"
	ds "thoughtflow/internal/pkg/documentset"
)

// Use the same Unicode-safe anchors in validation, the reader and offline HTML.
// Goldmark's default auto IDs discard non-ASCII characters.
type headingIDs map[string]int

func (ids headingIDs) Generate(value []byte, _ ast.NodeKind) []byte {
	base := ds.StableAnchor(string(value))
	id := base
	if ids[base] > 0 {
		id = fmt.Sprintf("%s-%d", base, ids[base])
	}
	ids[base]++
	return []byte(id)
}
func (ids headingIDs) Put(value []byte) { ids[string(value)]++ }
