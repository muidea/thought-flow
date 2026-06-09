package eventstream

import (
	"context"
	"testing"
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/models"
)

func TestStreamSanitizesRefinementEmbeddingVector(t *testing.T) {
	stream := New(10)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := stream.Subscribe(ctx)

	stream.Notify(event.NewEvent(models.EventThoughtRefined, "refiner", "#", event.NewHeader(), models.DomainEvent{
		EventType: models.EventThoughtRefined,
		Payload: models.ThoughtRefinement{
			ThoughtID: "thought-1",
			Embedding: &models.EmbeddingRecord{
				ThoughtID: "thought-1",
				Model:     "test",
				Dimension: 3,
				Vector:    []float64{1, 2, 3},
			},
		},
	}), nil)

	select {
	case got := <-events:
		refinement, ok := got.Payload.(models.ThoughtRefinement)
		if !ok {
			t.Fatalf("payload type = %T", got.Payload)
		}
		if refinement.Embedding == nil {
			t.Fatalf("expected embedding metadata")
		}
		if len(refinement.Embedding.Vector) != 0 {
			t.Fatalf("embedding vector should be sanitized, got %#v", refinement.Embedding.Vector)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected event")
	}
}
