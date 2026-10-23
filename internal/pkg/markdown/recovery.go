package markdown

import (
	"context"
	"os"

	"thoughtflow/internal/pkg/documentstore"
	"thoughtflow/internal/pkg/objectlocation"
)

// RecoverWorkspace runs before HTTP writes. Durable deletion and first-create
// projection intent are resolved from HEAD, never guessed from a missing file.
func RecoverWorkspace(ctx context.Context, root string) error {
	if e := RecoverRestores(root); e != nil {
		return e
	}
	r := objectlocation.For(root)
	locations, e := r.ListRegistered()
	if e != nil {
		return e
	}
	for _, l := range locations {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if l.DeletedAt != nil || l.Lifecycle == "import_pending" {
			continue
		}
		h, e := documentstore.New(root).Head(l.ObjectID)
		if e == nil && h.DeletedAt != nil {
			if e = r.MarkDeleted(l.ObjectID); e != nil {
				return e
			}
			continue
		}
		if e != nil && !os.IsNotExist(e) {
			return e
		}
		if l.Lifecycle == "reserved" || h.PendingProjection {
			if _, _, e = ReadThought(root, l.ObjectID); e != nil && !os.IsNotExist(e) {
				return e
			}
		}
	}
	return nil
}
