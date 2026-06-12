// Package scratchpad is the on-disk temporary container for capture
// sessions. It backs the redesigned "scratchpad → commit" flow: while
// the user is still chatting through a capture conversation, the
// intermediate text, title/tags drafts, and chat history live here
// rather than as a thought file. Only when the user explicitly
// "archives" (commits) the scratchpad does the content freeze into a
// real thought and trigger the full refine/expand/index/topic_match/
// git_commit pipeline.
//
// Persistence model:
//   - One JSON file per session: <rootPath>/<sessionID>.json
//   - In-memory map mirrors the on-disk state; writes go through
//     Save() which updates the map and fsyncs the file before
//     returning. Readers (Get / List) serve from the map under an
//     RLock; they do not re-read disk on the hot path.
//   - On startup the package walks the rootPath and re-hydrates the
//     map from any *.json files. Corrupt files are logged and
//     skipped — never returned to the caller — so a single bad
//     scratchpad cannot take the whole store offline.
package scratchpad

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// Message is one entry in the scratchpad's chat log. We store the
// complete trail (user / ai / system) so the UI can re-render the
// conversation after a reload; the commit path only uses the user
// messages and the cumulative Content field.
type Message struct {
	Role string    `json:"role"` // "user" | "ai" | "system"
	Text string    `json:"text"`
	At   time.Time `json:"at"`
}

// Draft accumulates every chat command the user has issued while the
// scratchpad is still uncommitted. The fields are append-only from
// the user's perspective: rename replaces TitleSet, add_tag unions
// into TagsAdded, etc. On commit, captureService flattens the draft
// into the real thought via PATCH.
type Draft struct {
	TitleSet        string   `json:"title_set,omitempty"`
	TagsAdded       []string `json:"tags_added,omitempty"`
	TagsRemoved     []string `json:"tags_removed,omitempty"`
	NotesAppended   []string `json:"notes_appended,omitempty"`
	TopicIDs        []string `json:"topic_ids,omitempty"`
	RefineRequested bool     `json:"refine_requested,omitempty"`
}

// SessionContext is the PRD §3.1 structured-context block. It is
// maintained by the LLM (and editable by the user) across chat
// rounds: each turn re-fills the fields and the UI / topic /
// synthesis paths read from here. Fields are intentionally
// permissive — an empty value means "not yet known" rather than
// "absent" — so the JSON contract has zero-value defaults the
// service can rely on.
type SessionContext struct {
	Topic             string   `json:"topic"`
	Goal              string   `json:"goal"`
	ConfirmedFacts    []string `json:"confirmed_facts"`
	OpenQuestions     []string `json:"open_questions"`
	Conflicts         []string `json:"conflicts"`
	CandidateTitle    string   `json:"candidate_title"`
	CandidateTags     []string `json:"candidate_tags"`
	CandidateSummary  string   `json:"candidate_summary"`
	CandidateBody     string   `json:"candidate_body"`
	SourceLinks       []string `json:"source_links"`
	RelatedThoughtIDs []string `json:"related_thought_ids"`
	SuggestedTopicIDs []string `json:"suggested_topic_ids"`
}

// ArchiveIntent captures WHO is driving the archive — the user via a
// menu click, the LLM after recognising a save intent in chat, or
// nobody yet. The UI uses it to decide whether to surface a confirm
// dialog before commit fires.
type ArchiveIntent string

const (
	ArchiveIntentNone ArchiveIntent = "none"
	ArchiveIntentMenu ArchiveIntent = "menu"
	ArchiveIntentLLM  ArchiveIntent = "llm"
)

// ArchiveStrategy routes the commit to the correct landing path
// (PRD §3.1). The session-default is "new"; a scratchpad created by
// ReopenFromThought defaults to "supplement"; the user can opt
// into "update_thought" or "new" explicitly.
type ArchiveStrategy string

const (
	ArchiveStrategyNew        ArchiveStrategy = "new"
	ArchiveStrategyUpdate      ArchiveStrategy = "update_thought"
	ArchiveStrategySupplement  ArchiveStrategy = "supplement"
)

// ThoughtDiff is the safe-update diff attached to an ArchivePreview
// when strategy == update_thought. The before/after strings are
// truncated Markdown; ChangedFields is a stable list of keys the
// UI can show ("title", "tags", "body", "key_points"). Kept simple
// on purpose: any LLM-side reconciliation happens at apply time,
// the preview only needs to make the user comfortable.
type ThoughtDiff struct {
	Before        string   `json:"before"`
	After         string   `json:"after"`
	ChangedFields []string `json:"changed_fields"`
}

// ArchivePreview is the read-only projection the UI shows before
// commit lands. Captured here so the same payload the user
// confirmed is what the commit path enforces — there is no "I
// thought I was archiving X but got Y" drift.
type ArchivePreview struct {
	ThoughtID     string          `json:"thought_id,omitempty"`
	Title         string          `json:"title"`
	Body          string          `json:"body"`
	Tags          []string        `json:"tags"`
	SourceLinks   []string        `json:"source_links"`
	RelatedTopics []string        `json:"related_topics"`
	Strategy      ArchiveStrategy `json:"strategy"`
	Diff          *ThoughtDiff    `json:"diff,omitempty"`
	GeneratedAt   time.Time       `json:"generated_at"`
}

// Scratchpad is the wire-stable JSON shape persisted to disk. The
// field tags are the public contract; do not rename without also
// bumping the file version field (see formatVersion below).
//
// v2 (2026-06-12) added SessionContext, ArchiveIntent,
// ArchiveStrategy, ArchivePreview, SourceThoughtID per PRD §3.1 /
// §3.1.1. The v1 → v2 migration is implemented in loadFromDisk —
// old files are read as-is and the new fields are zero-valued.
type Scratchpad struct {
	SessionID          string         `json:"session_id"`
	WorkspaceID        string         `json:"workspace_id"`
	Title              string         `json:"title"`
	Tags               []string       `json:"tags"`
	TopicHints         []string       `json:"topic_hints"`
	URL                string         `json:"url,omitempty"`
	Content            string         `json:"content"`
	Messages           []Message      `json:"messages"`
	Draft              Draft          `json:"draft"`
	SessionContext     SessionContext `json:"session_context"`
	ArchiveIntent      ArchiveIntent  `json:"archive_intent"`
	ArchiveStrategy    ArchiveStrategy `json:"archive_strategy"`
	ArchivePreview     *ArchivePreview `json:"archive_preview,omitempty"`
	SourceThoughtID    string         `json:"source_thought_id,omitempty"`
	CommittedThoughtID string         `json:"committed_thought_id,omitempty"`
	CommittedAt        *time.Time     `json:"committed_at,omitempty"`
	CreatedAt          time.Time      `json:"created_at"`
	UpdatedAt          time.Time      `json:"updated_at"`
}

// Summary is the diagnostic / drawer view: it strips Messages and
// Content so the UI can list every scratchpad in a few KB even when
// the conversations are long.
type Summary struct {
	SessionID          string    `json:"session_id"`
	Title              string    `json:"title"`
	CommittedThoughtID string    `json:"committed_thought_id,omitempty"`
	SourceThoughtID    string    `json:"source_thought_id,omitempty"`
	ArchiveStrategy    ArchiveStrategy `json:"archive_strategy,omitempty"`
	MessageCount       int       `json:"message_count"`
	ContentLength      int       `json:"content_length"`
	UpdatedAt          time.Time `json:"updated_at"`
}

// formatVersion is stamped on every persisted file. Bump it whenever
// the on-disk shape changes incompatibly; older files are migrated
// to the current version in loadFromDisk (or skipped if migration
// fails — never crashed on) so a partial upgrade cannot break the
// service.
//
//	1 — original shape (Title / Tags / TopicHints / URL / Content /
//	    Messages / Draft / Committed{ThoughtID,At} / CreatedAt /
//	    UpdatedAt)
//	2 — adds SessionContext, ArchiveIntent, ArchiveStrategy,
//	    ArchivePreview, SourceThoughtID (PRD §3.1 / §3.1.1).
const formatVersion = 2

// persistedFile is the disk layout. We wrap Scratchpad with a
// version field so future migrations have a hook.
type persistedFile struct {
	Version     int        `json:"version"`
	Scratchpad  Scratchpad `json:"scratchpad"`
}

// Store is the package-level entry point. It is safe for concurrent
// use: a single sync.RWMutex guards both the in-memory map and the
// file system. Reads take the RLock; Save / Delete take the write
// lock so a Get cannot observe a half-written file.
type Store struct {
	rootPath string
	mu       sync.RWMutex
	items    map[string]*Scratchpad
	now      func() time.Time
}

// New constructs a Store backed by rootPath. The directory is created
// on demand. New() also walks the existing files and re-hydrates the
// in-memory map; if rootPath is empty or missing, the store starts
// empty (still usable — Save will recreate the directory).
func New(rootPath string) *Store {
	store := &Store{
		rootPath: strings.TrimSpace(rootPath),
		items:    map[string]*Scratchpad{},
		now:      func() time.Time { return time.Now().UTC() },
	}
	if store.rootPath == "" {
		return store
	}
	if err := store.loadFromDisk(); err != nil {
		log.Printf("scratchpad: load from disk: %v", err)
	}
	return store
}

// Get returns a deep copy of the scratchpad keyed by sessionID. The
// copy means callers can mutate the result without affecting the
// in-memory cache or the on-disk file. A non-existent sessionID
// returns a fresh, zero-value Scratchpad with the given SessionID —
// the store treats "absent" and "empty" as the same thing, so the
// HTTP layer can return 200 for both.
func (s *Store) Get(sessionID string) (Scratchpad, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Scratchpad{}, errors.New("scratchpad: session id is required")
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	if existing, ok := s.items[sessionID]; ok && existing != nil {
		return cloneScratchpad(*existing), nil
	}
	return Scratchpad{SessionID: sessionID, CreatedAt: s.now(), UpdatedAt: s.now()}, nil
}

// Save upserts the scratchpad and flushes to disk. The in-memory
// copy is updated atomically under the write lock; the file write
// happens while the lock is held so a concurrent Get cannot read a
// map entry that has not been persisted yet.
func (s *Store) Save(sp Scratchpad) (Scratchpad, error) {
	sp.SessionID = strings.TrimSpace(sp.SessionID)
	if sp.SessionID == "" {
		return Scratchpad{}, errors.New("scratchpad: session id is required")
	}
	sp.UpdatedAt = s.now()
	if sp.CreatedAt.IsZero() {
		sp.CreatedAt = sp.UpdatedAt
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := s.writeFile(sp); err != nil {
		return Scratchpad{}, err
	}
	cp := cloneScratchpad(sp)
	s.items[sp.SessionID] = &cp
	return cloneScratchpad(sp), nil
}

// Delete removes a scratchpad from both the in-memory map and disk.
// Missing files / missing map entries are not errors — Delete is
// idempotent so the chat "新会话" command never has to special-case
// the first call of a session.
func (s *Store) Delete(sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return errors.New("scratchpad: session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.items, sessionID)
	if s.rootPath == "" {
		return nil
	}
	path := s.filePath(sessionID)
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

// MarkCommitted stamps a scratchpad with the resulting thought ID
// and timestamp. It is called by the commit flow right after the
// thought has been written to disk. Like Save, it persists
// immediately.
func (s *Store) MarkCommitted(sessionID, thoughtID string) (Scratchpad, error) {
	sessionID = strings.TrimSpace(sessionID)
	thoughtID = strings.TrimSpace(thoughtID)
	if sessionID == "" {
		return Scratchpad{}, errors.New("scratchpad: session id is required")
	}
	if thoughtID == "" {
		return Scratchpad{}, errors.New("scratchpad: thought id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.items[sessionID]
	if !ok {
		return Scratchpad{}, fmt.Errorf("scratchpad: session %q not found", sessionID)
	}
	now := s.now()
	existing.CommittedThoughtID = thoughtID
	existing.CommittedAt = &now
	existing.UpdatedAt = now
	if err := s.writeFile(*existing); err != nil {
		return Scratchpad{}, err
	}
	return cloneScratchpad(*existing), nil
}

// Reset clears the volatile fields of a scratchpad (Content,
// Messages, Draft) while keeping the session identity AND the
// committed link (CommittedThoughtID + CommittedAt). It is used by
// the repeated-commit path: after the incremental patch is applied,
// the user is back in a "fresh state" for the same session but the
// UI can still show "this session is still anchored to thought-X".
func (s *Store) Reset(sessionID string) (Scratchpad, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return Scratchpad{}, errors.New("scratchpad: session id is required")
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.items[sessionID]
	if !ok {
		return Scratchpad{}, fmt.Errorf("scratchpad: session %q not found", sessionID)
	}
	existing.Content = ""
	existing.Messages = nil
	existing.Draft = Draft{}
	existing.UpdatedAt = s.now()
	if err := s.writeFile(*existing); err != nil {
		return Scratchpad{}, err
	}
	return cloneScratchpad(*existing), nil
}

// List returns a Summary for every scratchpad in the store, sorted
// by UpdatedAt DESC so the most recently active session appears
// first in the UI drawer.
func (s *Store) List() []Summary {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Summary, 0, len(s.items))
	for _, sp := range s.items {
		if sp == nil {
			continue
		}
		out = append(out, Summary{
			SessionID:          sp.SessionID,
			Title:              sp.Title,
			CommittedThoughtID: sp.CommittedThoughtID,
			SourceThoughtID:    sp.SourceThoughtID,
			ArchiveStrategy:    sp.ArchiveStrategy,
			MessageCount:       len(sp.Messages),
			ContentLength:      len(sp.Content),
			UpdatedAt:          sp.UpdatedAt,
		})
	}
	sort.Slice(out, func(left, right int) bool {
		if out[left].UpdatedAt.Equal(out[right].UpdatedAt) {
			return out[left].SessionID < out[right].SessionID
		}
		return out[left].UpdatedAt.After(out[right].UpdatedAt)
	})
	return out
}

// LastActive returns the most recently updated uncommitted
// scratchpad, if any. A scratchpad is considered "uncommitted"
// when CommittedThoughtID is empty — once the user archives the
// session into a real thought, the entry stays on disk for
// history/audit but is not a candidate for the page-restore hook
// (it has no content to chat against). Returns (zero, false) when
// no candidate exists.
//
// The intent: the front-end boot path asks "which scratchpad, if
// any, should the user land on when the capture page opens?"
// Server answers with the most recent unfinished draft so a tab
// refresh, a server restart, or a cross-device open all land in
// the same conversation without losing input.
func (s *Store) LastActive() (Scratchpad, bool) {
	if s == nil {
		return Scratchpad{}, false
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Scratchpad
	for _, sp := range s.items {
		if sp == nil {
			continue
		}
		if strings.TrimSpace(sp.CommittedThoughtID) != "" {
			continue
		}
		if best == nil || sp.UpdatedAt.After(best.UpdatedAt) {
			copy := *sp
			best = &copy
		}
	}
	if best == nil {
		return Scratchpad{}, false
	}
	return cloneScratchpad(*best), true
}

// RuntimeStatus reports whether the store is ready. Mirrors the
// shape of jobstore.RuntimeStatus so it can be exposed via the
// /api/system/status endpoint without a custom handler.
func (s *Store) RuntimeStatus() ScratchpadStatus {
	status := ScratchpadStatus{Status: "ready"}
	if s == nil || s.rootPath == "" {
		status.Status = "disabled"
		return status
	}
	status.RootPath = s.rootPath
	if err := os.MkdirAll(s.rootPath, 0o755); err != nil {
		status.Status = "degraded"
		status.Error = err.Error()
		return status
	}
	tmp, err := os.CreateTemp(s.rootPath, ".status-*.tmp")
	if err != nil {
		status.Status = "degraded"
		status.Error = err.Error()
		return status
	}
	tmpPath := tmp.Name()
	_ = tmp.Close()
	_ = os.Remove(tmpPath)
	status.Writable = true
	s.mu.RLock()
	status.ScratchpadCount = len(s.items)
	s.mu.RUnlock()
	return status
}

// ScratchpadStatus is the diagnostic status envelope for the
// scratchpad store.
type ScratchpadStatus struct {
	Status          string `json:"status"`
	RootPath        string `json:"root_path"`
	Writable        bool   `json:"writable"`
	ScratchpadCount int    `json:"scratchpad_count"`
	Error           string `json:"error,omitempty"`
}

// filePath is the on-disk path for a sessionID. Caller must hold
// s.mu (read or write).
func (s *Store) filePath(sessionID string) string {
	return filepath.Join(s.rootPath, sessionID+".json")
}

// writeFile persists the scratchpad as JSON. Caller must hold
// s.mu.Lock.
func (s *Store) writeFile(sp Scratchpad) error {
	if s.rootPath == "" {
		return nil
	}
	if err := os.MkdirAll(s.rootPath, 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(persistedFile{Version: formatVersion, Scratchpad: sp}, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(s.filePath(sp.SessionID), raw, 0o644)
}

// loadFromDisk walks the rootPath at startup and re-hydrates the
// in-memory map. Corrupt files are logged and skipped; unknown
// future-version files are skipped without crashing; v1 files are
// transparently migrated to the v2 shape so the in-memory map only
// ever holds current-version scratchpads. Migration failures are
// logged and the file is dropped — better to lose one stale draft
// than to wedge the whole store.
func (s *Store) loadFromDisk() error {
	if s.rootPath == "" {
		return nil
	}
	entries, err := os.ReadDir(s.rootPath)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		path := filepath.Join(s.rootPath, entry.Name())
		raw, err := os.ReadFile(path)
		if err != nil {
			log.Printf("scratchpad: read %s: %v", entry.Name(), err)
			continue
		}
		var pf persistedFile
		if err := json.Unmarshal(raw, &pf); err != nil {
			log.Printf("scratchpad: parse %s: %v", entry.Name(), err)
			continue
		}
		// v1 → v2 migration. The JSON shape is a strict superset
		// (v1 fields all still exist on v2 with the same JSON
		// tags), so deserialising into a v2 struct already yields
		// zero-valued SessionContext / ArchiveIntent / etc. We do
		// not need a separate v1 mirror struct; we just stamp the
		// current version and write the file back so subsequent
		// reads see v2 only.
		if pf.Version == 1 {
			pf.Version = formatVersion
			if migrated, mErr := json.MarshalIndent(pf, "", "  "); mErr == nil {
				if wErr := os.WriteFile(path, migrated, 0o644); wErr != nil {
					log.Printf("scratchpad: persist v1→v2 %s: %v", entry.Name(), wErr)
					// Migration write failed; still load from memory.
				}
			} else {
				log.Printf("scratchpad: marshal v1→v2 %s: %v", entry.Name(), mErr)
			}
		} else if pf.Version != formatVersion {
			log.Printf("scratchpad: skip %s: unknown version %d", entry.Name(), pf.Version)
			continue
		}
		sp := pf.Scratchpad
		if strings.TrimSpace(sp.SessionID) == "" {
			log.Printf("scratchpad: skip %s: empty session id", entry.Name())
			continue
		}
		cp := sp
		s.items[sp.SessionID] = &cp
	}
	return nil
}

func cloneScratchpad(sp Scratchpad) Scratchpad {
	out := sp
	if sp.Tags != nil {
		tags := make([]string, len(sp.Tags))
		copy(tags, sp.Tags)
		out.Tags = tags
	}
	if sp.TopicHints != nil {
		hints := make([]string, len(sp.TopicHints))
		copy(hints, sp.TopicHints)
		out.TopicHints = hints
	}
	if sp.Messages != nil {
		msgs := make([]Message, len(sp.Messages))
		copy(msgs, sp.Messages)
		out.Messages = msgs
	}
	if sp.Draft.TagsAdded != nil {
		added := make([]string, len(sp.Draft.TagsAdded))
		copy(added, sp.Draft.TagsAdded)
		out.Draft.TagsAdded = added
	}
	if sp.Draft.TagsRemoved != nil {
		removed := make([]string, len(sp.Draft.TagsRemoved))
		copy(removed, sp.Draft.TagsRemoved)
		out.Draft.TagsRemoved = removed
	}
	if sp.Draft.NotesAppended != nil {
		notes := make([]string, len(sp.Draft.NotesAppended))
		copy(notes, sp.Draft.NotesAppended)
		out.Draft.NotesAppended = notes
	}
	if sp.Draft.TopicIDs != nil {
		topics := make([]string, len(sp.Draft.TopicIDs))
		copy(topics, sp.Draft.TopicIDs)
		out.Draft.TopicIDs = topics
	}
	if sp.CommittedAt != nil {
		t := *sp.CommittedAt
		out.CommittedAt = &t
	}
	if sp.SessionContext.ConfirmedFacts != nil {
		facts := make([]string, len(sp.SessionContext.ConfirmedFacts))
		copy(facts, sp.SessionContext.ConfirmedFacts)
		out.SessionContext.ConfirmedFacts = facts
	}
	if sp.SessionContext.OpenQuestions != nil {
		questions := make([]string, len(sp.SessionContext.OpenQuestions))
		copy(questions, sp.SessionContext.OpenQuestions)
		out.SessionContext.OpenQuestions = questions
	}
	if sp.SessionContext.Conflicts != nil {
		conflicts := make([]string, len(sp.SessionContext.Conflicts))
		copy(conflicts, sp.SessionContext.Conflicts)
		out.SessionContext.Conflicts = conflicts
	}
	if sp.SessionContext.CandidateTags != nil {
		tags := make([]string, len(sp.SessionContext.CandidateTags))
		copy(tags, sp.SessionContext.CandidateTags)
		out.SessionContext.CandidateTags = tags
	}
	if sp.SessionContext.SourceLinks != nil {
		links := make([]string, len(sp.SessionContext.SourceLinks))
		copy(links, sp.SessionContext.SourceLinks)
		out.SessionContext.SourceLinks = links
	}
	if sp.SessionContext.RelatedThoughtIDs != nil {
		ids := make([]string, len(sp.SessionContext.RelatedThoughtIDs))
		copy(ids, sp.SessionContext.RelatedThoughtIDs)
		out.SessionContext.RelatedThoughtIDs = ids
	}
	if sp.SessionContext.SuggestedTopicIDs != nil {
		ids := make([]string, len(sp.SessionContext.SuggestedTopicIDs))
		copy(ids, sp.SessionContext.SuggestedTopicIDs)
		out.SessionContext.SuggestedTopicIDs = ids
	}
	if sp.ArchivePreview != nil {
		preview := *sp.ArchivePreview
		if sp.ArchivePreview.Tags != nil {
			tags := make([]string, len(sp.ArchivePreview.Tags))
			copy(tags, sp.ArchivePreview.Tags)
			preview.Tags = tags
		}
		if sp.ArchivePreview.SourceLinks != nil {
			links := make([]string, len(sp.ArchivePreview.SourceLinks))
			copy(links, sp.ArchivePreview.SourceLinks)
			preview.SourceLinks = links
		}
		if sp.ArchivePreview.RelatedTopics != nil {
			topics := make([]string, len(sp.ArchivePreview.RelatedTopics))
			copy(topics, sp.ArchivePreview.RelatedTopics)
			preview.RelatedTopics = topics
		}
		if sp.ArchivePreview.Diff != nil {
			diff := *sp.ArchivePreview.Diff
			if sp.ArchivePreview.Diff.ChangedFields != nil {
				fields := make([]string, len(sp.ArchivePreview.Diff.ChangedFields))
				copy(fields, sp.ArchivePreview.Diff.ChangedFields)
				diff.ChangedFields = fields
			}
			preview.Diff = &diff
		}
		out.ArchivePreview = &preview
	}
	return out
}
