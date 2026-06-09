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

func TestStreamSubscribeWithLastEventIDAndTypes(t *testing.T) {
	stream := New(10)
	stream.Publish(models.DomainEvent{EventID: "evt-1", EventType: models.EventThoughtCaptured, ResourceID: "one"})
	stream.Publish(models.DomainEvent{EventID: "evt-2", EventType: models.EventJobUpdated, ResourceID: "two"})
	stream.Publish(models.DomainEvent{EventID: "evt-3", EventType: models.EventTopicUpdated, ResourceID: "three"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	events := stream.SubscribeWithOptions(ctx, SubscribeOptions{
		LastEventID: "evt-1",
		Types:       []string{models.EventTopicUpdated},
	})

	assertNextEvent(t, events, "evt-3")
	stream.Publish(models.DomainEvent{EventID: "evt-4", EventType: models.EventJobUpdated, ResourceID: "four"})
	stream.Publish(models.DomainEvent{EventID: "evt-5", EventType: models.EventTopicUpdated, ResourceID: "five"})
	assertNextEvent(t, events, "evt-5")
}

func assertNextEvent(t *testing.T, events <-chan models.DomainEvent, eventID string) {
	t.Helper()
	select {
	case got := <-events:
		if got.EventID != eventID {
			t.Fatalf("event id = %q, want %q", got.EventID, eventID)
		}
	case <-time.After(time.Second):
		t.Fatalf("expected event %s", eventID)
	}
}
