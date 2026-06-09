package eventstream

import (
	"context"
	"sync"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/models"
)

type Stream struct {
	mu          sync.RWMutex
	subscribers map[chan models.DomainEvent]struct{}
	history     []models.DomainEvent
	limit       int
}

func New(limit int) *Stream {
	if limit <= 0 {
		limit = 100
	}
	return &Stream{
		subscribers: map[chan models.DomainEvent]struct{}{},
		limit:       limit,
	}
}

func (s *Stream) ID() string {
	return "application.eventstream"
}

func (s *Stream) Notify(ev event.Event, result event.Result) {
	domainEvent, ok := ev.Data().(models.DomainEvent)
	if !ok {
		return
	}
	s.Publish(domainEvent)
	if result != nil {
		result.Set(nil, nil)
	}
}

func (s *Stream) Publish(domainEvent models.DomainEvent) {
	s.mu.Lock()
	s.history = append(s.history, domainEvent)
	if len(s.history) > s.limit {
		s.history = s.history[len(s.history)-s.limit:]
	}
	subscribers := make([]chan models.DomainEvent, 0, len(s.subscribers))
	for ch := range s.subscribers {
		subscribers = append(subscribers, ch)
	}
	s.mu.Unlock()

	for _, ch := range subscribers {
		select {
		case ch <- domainEvent:
		default:
		}
	}
}

func (s *Stream) Subscribe(ctx context.Context) <-chan models.DomainEvent {
	ch := make(chan models.DomainEvent, 32)
	s.mu.Lock()
	for _, item := range s.history {
		ch <- item
	}
	s.subscribers[ch] = struct{}{}
	s.mu.Unlock()

	go func() {
		<-ctx.Done()
		s.mu.Lock()
		delete(s.subscribers, ch)
		close(ch)
		s.mu.Unlock()
	}()
	return ch
}
