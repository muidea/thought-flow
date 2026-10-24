package scratchpad

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

var ErrConflict = errors.New("scratchpad generation conflict")

// Save operations serialize separately from edits: network and document writes
// never hold the scratchpad mutex, and incoming messages remain writable.
var saveLocks sync.Map

func LockSave(store any, id string) func() {
	key := struct {
		Store any
		ID    string
	}{store, id}
	v, _ := saveLocks.LoadOrStore(key, &sync.Mutex{})
	m := v.(*sync.Mutex)
	m.Lock()
	return m.Unlock
}

// Update executes a short read/modify/persist transaction. On any failure the
// in-memory value stays unchanged. Callbacks must not call this Store again.
func (s *Store) Update(id string, change func(*Scratchpad) error) (Scratchpad, error) {
	id = strings.TrimSpace(id)
	if id == "" {
		return Scratchpad{}, errors.New("scratchpad: session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sp := Scratchpad{SessionID: id}
	if old := s.items[id]; old != nil {
		sp = cloneScratchpad(*old)
	}
	if err := change(&sp); err != nil {
		return Scratchpad{}, err
	}
	return s.saveLocked(sp)
}

func inputJSON(sp Scratchpad) string {
	ctx := sp.SessionContext
	ctx.ArchiveIntent = ""
	raw, _ := json.Marshal(struct {
		Content      string
		Messages     []Message
		Draft        Draft
		Context      SessionContext
		Title        string
		Tags, Topics []string
		Strategy     ArchiveStrategy
		Source       string
	}{sp.Content, sp.Messages, sp.Draft, ctx, sp.Title, sp.Tags, sp.TopicHints, sp.ArchiveStrategy, sp.SourceThoughtID})
	return string(raw)
}

func (s *Store) saveLocked(sp Scratchpad) (Scratchpad, error) {
	sp.SessionID = strings.TrimSpace(sp.SessionID)
	if sp.SessionID == "" {
		return Scratchpad{}, errors.New("scratchpad: session id is required")
	}
	old := s.items[sp.SessionID]
	if old != nil {
		if sp.SessionGeneration != 0 && sp.SessionGeneration != old.SessionGeneration {
			return Scratchpad{}, ErrConflict
		}
		sp.SessionGeneration = old.SessionGeneration + 1
		sp.InputGeneration = old.InputGeneration
		if inputJSON(sp) != inputJSON(*old) {
			sp.InputGeneration++
		}
	} else {
		sp.SessionGeneration = 1
		sp.InputGeneration = 1
	}
	sp.UpdatedAt = s.now()
	if sp.CreatedAt.IsZero() {
		sp.CreatedAt = sp.UpdatedAt
	}
	if err := s.writeFile(sp); err != nil {
		return Scratchpad{}, err
	}
	cp := cloneScratchpad(sp)
	s.items[sp.SessionID] = &cp
	return cloneScratchpad(sp), nil
}
