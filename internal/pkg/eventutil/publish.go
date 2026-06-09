package eventutil

import (
	"time"

	"github.com/muidea/magicCommon/event"

	"thoughtflow/internal/pkg/models"
)

func Post(hub event.Hub, domainEvent models.DomainEvent) {
	if hub == nil {
		return
	}
	if domainEvent.EventID == "" {
		domainEvent.EventID = models.NewEventID(time.Now().UTC())
	}
	if domainEvent.PayloadVersion == 0 {
		domainEvent.PayloadVersion = 1
	}
	hub.Post(event.NewEvent(domainEvent.EventType, domainEvent.SourceUnit, "#", event.NewHeader(), domainEvent))
}
