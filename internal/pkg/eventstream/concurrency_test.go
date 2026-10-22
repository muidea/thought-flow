package eventstream

import (
	"context"
	"sync"
	"testing"
	"thoughtflow/internal/pkg/models"
)

func TestPublishDuringSubscriberCancellation(t *testing.T) {
	stream := New(8)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	survivor := stream.Subscribe(ctx)
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for range 100 {
				ctx, cancel := context.WithCancel(context.Background())
				ch := stream.Subscribe(ctx)
				cancel()
				stream.Publish(models.DomainEvent{EventType: "test"})
				for range ch {
				}
			}
		}()
	}
	wg.Wait()
	for len(survivor) > 0 {
		<-survivor
	}
	stream.Publish(models.DomainEvent{EventType: "sentinel"})
	if ev := <-survivor; ev.EventType != "sentinel" {
		t.Fatal(ev)
	}
}
