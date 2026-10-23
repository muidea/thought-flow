package markdown

import (
	"context"
	"errors"
	"os"
	"testing"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func TestRestoreDeletionCrashPhasesPreserveIdentityAndContent(t *testing.T) {
	for _, phase := range []string{"restore_projection", "restore_head", "restore_location"} {
		t.Run(phase, func(t *testing.T) {
			root := t.TempDir()
			id := "20261023-restored"
			ctx := context.Background()
			if e := WriteThought(root, models.Thought{ID: id, UserTitle: "Keep title"}, models.ThoughtContent{AINotes: "Keep independent body"}); e != nil {
				t.Fatal(e)
			}
			if e := DeleteThought(root, id); e != nil {
				t.Fatal(e)
			}
			r := objectlocation.For(root)
			l, e := r.Lookup(id)
			if e != nil {
				t.Fatal(e)
			}
			req := RestoreRequest{OperationID: "restore", ExpectedGeneration: l.Generation}
			r.Fault = func(actual string) error {
				if actual == phase {
					return os.ErrClosed
				}
				return nil
			}
			if _, e = RestoreThought(ctx, root, id, req); !errors.Is(e, os.ErrClosed) {
				t.Fatal("fault not reached", e)
			}
			r.Fault = nil
			if e = RecoverWorkspace(ctx, root); e != nil {
				t.Fatal(e)
			}
			thought, body, e := ReadThought(root, id)
			if e != nil || thought.ID != id || thought.UserTitle != "Keep title" || body.AINotes != "Keep independent body" {
				t.Fatal(thought, body, e)
			}
			if e = UpdateThought(root, id, func(_ *models.Thought, c *models.ThoughtContent) error { c.AINotes = "New edit"; return nil }); e != nil {
				t.Fatal(e)
			}
			if _, e = RestoreThought(ctx, root, id, req); e != nil {
				t.Fatal(e)
			}
			_, body, e = ReadThought(root, id)
			if e != nil || body.AINotes != "New edit" {
				t.Fatal("restore replay overwrote new edit", body, e)
			}
		})
	}
}
