package eventstream

import (
	"context"
	"strings"
	"sync"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/models"
)

type Stream struct {
	mu          sync.RWMutex
	subscribers map[chan models.DomainEvent]SubscribeOptions
	history     []models.DomainEvent
	limit       int
}

type SubscribeOptions struct {
	LastEventID string
	Types       []string
}

type Stats struct {
	HistorySize int
	Limit       int
	Subscribers int
}

func New(limit int) *Stream {
	if limit <= 0 {
		limit = 100
	}
	return &Stream{
		subscribers: map[chan models.DomainEvent]SubscribeOptions{},
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
	s.Publish(sanitize(domainEvent))
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
	subscribers := make(map[chan models.DomainEvent]SubscribeOptions, len(s.subscribers))
	for ch, options := range s.subscribers {
		subscribers[ch] = options
	}
	s.mu.Unlock()

	for ch, options := range subscribers {
		if !matchesTypes(domainEvent, options.Types) {
			continue
		}
		select {
		case ch <- domainEvent:
		default:
		}
	}
}

func sanitize(domainEvent models.DomainEvent) models.DomainEvent {
	if domainEvent.EventType != models.EventThoughtRefined {
		return domainEvent
	}
	switch payload := domainEvent.Payload.(type) {
	case models.ThoughtRefinement:
		domainEvent.Payload = sanitizeRefinement(payload)
	case *models.ThoughtRefinement:
		if payload != nil {
			refinement := sanitizeRefinement(*payload)
			domainEvent.Payload = refinement
		}
	}
	return domainEvent
}

func sanitizeRefinement(refinement models.ThoughtRefinement) models.ThoughtRefinement {
	if refinement.Embedding == nil {
		return refinement
	}
	embedding := *refinement.Embedding
	embedding.Vector = nil
	refinement.Embedding = &embedding
	return refinement
}

func (s *Stream) Subscribe(ctx context.Context) <-chan models.DomainEvent {
	return s.SubscribeWithOptions(ctx, SubscribeOptions{})
}

func (s *Stream) Stats() Stats {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return Stats{
		HistorySize: len(s.history),
		Limit:       s.limit,
		Subscribers: len(s.subscribers),
	}
}

func (s *Stream) SubscribeWithOptions(ctx context.Context, options SubscribeOptions) <-chan models.DomainEvent {
	options.Types = normalizeTypes(options.Types)
	s.mu.Lock()
	replay := []models.DomainEvent{}
	for _, item := range replayHistory(s.history, options) {
		if matchesTypes(item, options.Types) {
			replay = append(replay, item)
		}
	}
	ch := make(chan models.DomainEvent, len(replay)+32)
	for _, item := range replay {
		ch <- item
	}
	s.subscribers[ch] = options
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

func normalizeTypes(types []string) []string {
	seen := map[string]struct{}{}
	ret := []string{}
	for _, eventType := range types {
		eventType = strings.TrimSpace(eventType)
		if eventType == "" {
			continue
		}
		if _, exists := seen[eventType]; exists {
			continue
		}
		seen[eventType] = struct{}{}
		ret = append(ret, eventType)
	}
	return ret
}

func replayHistory(history []models.DomainEvent, options SubscribeOptions) []models.DomainEvent {
	if options.LastEventID == "" {
		return history
	}
	for idx, item := range history {
		if item.EventID == options.LastEventID {
			return history[idx+1:]
		}
	}
	return history
}

func matchesTypes(domainEvent models.DomainEvent, types []string) bool {
	if len(types) == 0 {
		return true
	}
	for _, eventType := range types {
		if eventType == domainEvent.EventType {
			return true
		}
	}
	return false
}
