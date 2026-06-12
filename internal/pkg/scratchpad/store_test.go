package scratchpad

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestStoreBasicGetSaveDelete(t *testing.T) {
	root := t.TempDir()
	store := New(root)

	// 1. Get on a missing session returns a zero-value Scratchpad,
	//    not an error — the HTTP layer can return 200 + empty object
	//    instead of 404.
	sp, err := store.Get("missing")
	if err != nil {
		t.Fatalf("Get(missing) error = %v", err)
	}
	if sp.SessionID != "missing" || sp.Content != "" {
		t.Fatalf("Get(missing) = %+v", sp)
	}

	// 2. Save persists to disk; a fresh store re-hydrates it.
	now := time.Date(2026, 6, 12, 9, 0, 0, 0, time.UTC)
	store.now = func() time.Time { return now }
	in := Scratchpad{
		SessionID: "session-A",
		WorkspaceID: "local",
		Title:   "draft title",
		Content: "hello world",
		Tags:    []string{"ai", "draft"},
		Messages: []Message{
			{Role: "user", Text: "hi", At: now},
		},
	}
	saved, err := store.Save(in)
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if saved.UpdatedAt.IsZero() || saved.CreatedAt.IsZero() {
		t.Fatalf("timestamps not stamped: %+v", saved)
	}

	// 3. Disk file exists with the expected shape.
	raw, err := os.ReadFile(filepath.Join(root, "session-A.json"))
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	var pf persistedFile
	if err := json.Unmarshal(raw, &pf); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	if pf.Version != formatVersion {
		t.Fatalf("file version = %d, want %d", pf.Version, formatVersion)
	}
	if pf.Scratchpad.SessionID != "session-A" {
		t.Fatalf("session_id in file = %q", pf.Scratchpad.SessionID)
	}

	// 4. A fresh store re-hydrates from disk.
	fresh := New(root)
	got, err := fresh.Get("session-A")
	if err != nil {
		t.Fatalf("Get on fresh store error = %v", err)
	}
	if got.Title != "draft title" || got.Content != "hello world" || len(got.Tags) != 2 {
		t.Fatalf("fresh store hydrated wrongly: %+v", got)
	}

	// 5. Delete removes the file and the map entry.
	if err := fresh.Delete("session-A"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "session-A.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("file still present after Delete: %v", err)
	}
	sp, err = fresh.Get("session-A")
	if err != nil {
		t.Fatalf("Get after Delete error = %v", err)
	}
	if sp.Content != "" {
		t.Fatalf("Get after Delete returned content: %+v", sp)
	}

	// 6. Idempotent Delete on missing session.
	if err := fresh.Delete("does-not-exist"); err != nil {
		t.Fatalf("Delete(missing) error = %v", err)
	}
}

func TestStoreLoadFromDiskIgnoresCorruptFiles(t *testing.T) {
	root := t.TempDir()
	// A valid file.
	good := persistedFile{Version: formatVersion, Scratchpad: Scratchpad{SessionID: "ok", Content: "fine"}}
	raw, _ := json.MarshalIndent(good, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "ok.json"), raw, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// Garbage in another file.
	if err := os.WriteFile(filepath.Join(root, "bad.json"), []byte("not json"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// A file with the wrong extension that should be skipped silently.
	if err := os.WriteFile(filepath.Join(root, "ignore.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// A future-version file that should be skipped (not crashed on).
	future := persistedFile{Version: 999, Scratchpad: Scratchpad{SessionID: "future"}}
	fraw, _ := json.MarshalIndent(future, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "future.json"), fraw, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	// A file with no session id (corrupt schema).
	noID := persistedFile{Version: formatVersion, Scratchpad: Scratchpad{Content: "no session"}}
	nraw, _ := json.MarshalIndent(noID, "", "  ")
	if err := os.WriteFile(filepath.Join(root, "noid.json"), nraw, 0o644); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	store := New(root)
	got, err := store.Get("ok")
	if err != nil || got.Content != "fine" {
		t.Fatalf("Get(ok) on re-hydrated store = %+v, err = %v", got, err)
	}
	if _, err := store.Get("future"); err != nil {
		t.Fatalf("Get(future) should not error, got %v", err)
	}
	if _, err := store.Get("bad"); err != nil {
		t.Fatalf("Get(bad) should not error, got %v", err)
	}
	if _, err := store.Get("noid"); err != nil {
		t.Fatalf("Get(noid) should not error, got %v", err)
	}
}

func TestStoreConcurrentAccess(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sessionID := "concurrent-" + string(rune('A'+i%5))
			sp := Scratchpad{
				SessionID: sessionID,
				Content:   "msg",
			}
			if _, err := store.Save(sp); err != nil {
				t.Errorf("Save(%d) error = %v", i, err)
			}
		}(i)
	}
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			sp, err := store.Get("concurrent-A")
			if err != nil {
				t.Errorf("Get(%d) error = %v", i, err)
			}
			if sp.SessionID != "concurrent-A" {
				t.Errorf("Get(%d) returned wrong session: %q", i, sp.SessionID)
			}
		}(i)
	}
	wg.Wait()
}

func TestStoreMarkCommittedStampsAndPersists(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	store.now = func() time.Time { return time.Date(2026, 6, 12, 10, 0, 0, 0, time.UTC) }
	if _, err := store.Save(Scratchpad{SessionID: "session-X", Content: "draft"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	updated, err := store.MarkCommitted("session-X", "thought-abc")
	if err != nil {
		t.Fatalf("MarkCommitted() error = %v", err)
	}
	if updated.CommittedThoughtID != "thought-abc" || updated.CommittedAt == nil {
		t.Fatalf("commit fields not stamped: %+v", updated)
	}
	// Re-hydration sees the commit fields.
	fresh := New(root)
	got, err := fresh.Get("session-X")
	if err != nil {
		t.Fatalf("Get on fresh store error = %v", err)
	}
	if got.CommittedThoughtID != "thought-abc" {
		t.Fatalf("committed_thought_id after reload = %q", got.CommittedThoughtID)
	}
}

func TestStoreMarkCommittedRequiresExistingSession(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.MarkCommitted("missing", "thought-1"); err == nil {
		t.Fatalf("MarkCommitted on missing session should error")
	}
}

func TestStoreResetClearsVolatileFieldsButKeepsCommittedLink(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	sp := Scratchpad{
		SessionID: "session-Y",
		Content:   "draft",
		Messages:  []Message{{Role: "user", Text: "hi"}},
		Draft:     Draft{NotesAppended: []string{"x"}},
	}
	if _, err := store.Save(sp); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	updated, err := store.MarkCommitted("session-Y", "thought-1")
	if err != nil {
		t.Fatalf("MarkCommitted() error = %v", err)
	}
	if updated.CommittedThoughtID != "thought-1" {
		t.Fatalf("commit not stamped")
	}
	reset, err := store.Reset("session-Y")
	if err != nil {
		t.Fatalf("Reset() error = %v", err)
	}
	if reset.Content != "" || len(reset.Messages) != 0 || len(reset.Draft.NotesAppended) != 0 {
		t.Fatalf("Reset did not clear volatile fields: %+v", reset)
	}
	if reset.CommittedThoughtID != "thought-1" || reset.CommittedAt == nil {
		t.Fatalf("Reset wiped the committed link: %+v", reset)
	}
}

func TestStoreListOrdersByUpdatedAtDesc(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	base := time.Date(2026, 6, 12, 12, 0, 0, 0, time.UTC)
	for i, sid := range []string{"first", "second", "third"} {
		store.now = func() time.Time { return base.Add(time.Duration(i) * time.Minute) }
		if _, err := store.Save(Scratchpad{SessionID: sid}); err != nil {
			t.Fatalf("Save(%s) error = %v", sid, err)
		}
	}
	summaries := store.List()
	if len(summaries) != 3 {
		t.Fatalf("List() = %d, want 3", len(summaries))
	}
	want := []string{"third", "second", "first"}
	for i, s := range summaries {
		if s.SessionID != want[i] {
			t.Fatalf("summaries[%d] = %q, want %q", i, s.SessionID, want[i])
		}
	}
}

func TestStoreRuntimeStatusReportsReady(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	status := store.RuntimeStatus()
	if status.Status != "ready" || !status.Writable {
		t.Fatalf("status = %+v", status)
	}
}

func TestStoreRuntimeStatusReportsDisabledForEmptyRoot(t *testing.T) {
	store := New("")
	status := store.RuntimeStatus()
	if status.Status != "disabled" {
		t.Fatalf("status = %+v, want disabled", status)
	}
}

func TestStoreCloneIsDeepCopy(t *testing.T) {
	original := Scratchpad{
		SessionID:  "deep",
		Tags:       []string{"a", "b"},
		Messages:   []Message{{Role: "user", Text: "x"}},
		Draft:      Draft{NotesAppended: []string{"n"}},
		CommittedAt: func() *time.Time { t := time.Now().UTC(); return &t }(),
	}
	clone := cloneScratchpad(original)
	clone.Tags[0] = "MUTATED"
	clone.Messages[0].Text = "MUTATED"
	clone.Draft.NotesAppended[0] = "MUTATED"
	if original.Tags[0] == "MUTATED" {
		t.Fatalf("clone tags share underlying slice")
	}
	if original.Messages[0].Text == "MUTATED" {
		t.Fatalf("clone messages share underlying slice")
	}
	if original.Draft.NotesAppended[0] == "MUTATED" {
		t.Fatalf("clone draft notes share underlying slice")
	}
}

func TestStoreGetReturnsValidSessionIDForMissing(t *testing.T) {
	store := New(t.TempDir())
	sp, err := store.Get("anything")
	if err != nil {
		t.Fatalf("Get(anything) error = %v", err)
	}
	if sp.SessionID != "anything" {
		t.Fatalf("SessionID = %q, want anything", sp.SessionID)
	}
}

func TestStoreGetRejectsEmptySessionID(t *testing.T) {
	store := New(t.TempDir())
	if _, err := store.Get("   "); err == nil {
		t.Fatalf("Get(whitespace) should error")
	}
	if _, err := store.Get(""); err == nil {
		t.Fatalf("Get(empty) should error")
	}
}

func TestStoreListSummariesReflectContent(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	_, err := store.Save(Scratchpad{
		SessionID: "summary-test",
		Title:     "drafted title",
		Content:   "0123456789",
		Messages:  []Message{{Role: "user", Text: "x"}, {Role: "ai", Text: "y"}},
	})
	if err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	summaries := store.List()
	if len(summaries) != 1 {
		t.Fatalf("List() = %d", len(summaries))
	}
	s := summaries[0]
	if s.SessionID != "summary-test" || s.Title != "drafted title" {
		t.Fatalf("summary identity = %+v", s)
	}
	if s.MessageCount != 2 || s.ContentLength != 10 {
		t.Fatalf("summary sizes = %+v", s)
	}
}

func TestStorePersistsDraftOnSave(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	sp := Scratchpad{
		SessionID: "draft-test",
		Title:     "before",
		Draft: Draft{
			TitleSet:      "renamed title",
			TagsAdded:     []string{"x", "y"},
			NotesAppended: []string{"note 1"},
			TopicIDs:      []string{"topic-1"},
		},
	}
	if _, err := store.Save(sp); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	fresh := New(root)
	got, err := fresh.Get("draft-test")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Draft.TitleSet != "renamed title" {
		t.Fatalf("TitleSet not persisted: %+v", got.Draft)
	}
	if len(got.Draft.TagsAdded) != 2 || got.Draft.TagsAdded[0] != "x" {
		t.Fatalf("TagsAdded not persisted: %+v", got.Draft)
	}
}

func TestStoreSaveOverwritesSameSession(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	if _, err := store.Save(Scratchpad{SessionID: "over", Content: "first"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	if _, err := store.Save(Scratchpad{SessionID: "over", Content: "second"}); err != nil {
		t.Fatalf("Save() error = %v", err)
	}
	got, err := store.Get("over")
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if got.Content != "second" {
		t.Fatalf("Content = %q, want second", got.Content)
	}
}

func TestStoreListSortedByUpdatedAtDescWithEqualTimestampsBreaksOnID(t *testing.T) {
	root := t.TempDir()
	store := New(root)
	store.now = func() time.Time { return time.Date(2026, 6, 12, 13, 0, 0, 0, time.UTC) }
	for _, sid := range []string{"z", "a", "m"} {
		if _, err := store.Save(Scratchpad{SessionID: sid}); err != nil {
			t.Fatalf("Save() error = %v", err)
		}
	}
	summaries := store.List()
	gotIDs := []string{}
	for _, s := range summaries {
		gotIDs = append(gotIDs, s.SessionID)
	}
	sort.Strings(gotIDs)
	if strings.Join(gotIDs, ",") != "a,m,z" {
		t.Fatalf("List = %v", summaries)
	}
}
