package topicstore

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"thoughtflow/internal/pkg/models"
	"thoughtflow/internal/pkg/objectlocation"
)

func TestCreateWithIDKeepsNameIdentityAndOperationSeparate(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	s := New(root)
	req := models.TopicCreateRequest{Name: "订单平台设计", Mode: models.TopicModeCollection, OperationID: "create-one"}
	first, err := s.CreateWithID(ctx, "set-first", req)
	if err != nil || first.ID != "set-first" || first.Slug != first.ID || first.Name != req.Name {
		t.Fatal(first, err)
	}
	replayed, err := s.CreateWithID(ctx, first.ID, req)
	if err != nil || objectlocation.HashJSON(first) != objectlocation.HashJSON(replayed) {
		t.Fatal("operation replay changed topic", replayed, err)
	}
	// Re-enter the durable creation recovery path with the same explicit ID.
	registry := objectlocation.For(root)
	op, err := registry.Operation(req.OperationID)
	if err != nil {
		t.Fatal(err)
	}
	op.State = "prepared"
	if err = registry.Prepare(op); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"index.md", "catalog.json"} {
		if err = os.Remove(filepath.Join(root, "topics", first.ID, path)); err != nil {
			t.Fatal(err)
		}
	}
	recovered, err := s.CreateWithID(ctx, first.ID, req)
	if err != nil || recovered.ID != first.ID || recovered.Name != first.Name {
		t.Fatal("creation recovery changed identity or title", recovered, err)
	}
	if _, err = s.Catalog(ctx, first.ID); err != nil {
		t.Fatal("catalog not recovered", err)
	}
	req.OperationID = "create-two"
	second, err := s.CreateWithID(ctx, "set-second", req)
	if err != nil || second.ID == first.ID || second.Name != first.Name {
		t.Fatal(second, err)
	}
	before, err := os.ReadFile(filepath.Join(root, "topics", first.ID, "topic.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	req.OperationID = "collision"
	if _, err = s.CreateWithID(ctx, first.ID, req); !errors.Is(err, objectlocation.ErrConflict) {
		t.Fatal("adopted existing identity", err)
	}
	after, _ := os.ReadFile(filepath.Join(root, "topics", first.ID, "topic.yaml"))
	if string(before) != string(after) {
		t.Fatal("collision changed existing topic")
	}
	req.OperationID = "create-one"
	if _, err = s.CreateWithID(ctx, second.ID, req); !errors.Is(err, objectlocation.ErrOperationConflict) {
		t.Fatal("operation reused for another identity", err)
	}
	for _, invalid := range []string{"", "../escape", "SET-UPPER", "set_alias", "set.file"} {
		if _, err = s.CreateWithID(ctx, invalid, req); !errors.Is(err, objectlocation.ErrInvalid) {
			t.Fatal(invalid, err)
		}
	}
}
