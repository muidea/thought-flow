package thoughtlock

import (
	"errors"
	"sync"
	"testing"
	"time"
)

func TestAcquireRelease(t *testing.T) {
	locker := New(time.Second)
	if err := locker.Acquire("thought-1", "session-A"); err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	holder, ok := locker.Holder("thought-1")
	if !ok || holder != "session-A" {
		t.Fatalf("expected session-A to hold the lock, got %q ok=%v", holder, ok)
	}
	locker.Release("thought-1", "session-A")
	if _, ok := locker.Holder("thought-1"); ok {
		t.Fatalf("expected lock to be free after Release")
	}
}

func TestAcquireContended(t *testing.T) {
	locker := New(time.Second)
	if err := locker.Acquire("thought-1", "session-A"); err != nil {
		t.Fatalf("first Acquire returned error: %v", err)
	}
	err := locker.Acquire("thought-1", "session-B")
	if !errors.Is(err, ErrLocked) {
		t.Fatalf("expected ErrLocked, got %v", err)
	}
	// Re-acquiring from the same session is idempotent.
	if err := locker.Acquire("thought-1", "session-A"); err != nil {
		t.Fatalf("re-entrant Acquire returned error: %v", err)
	}
	// After Release, the next session can take over.
	locker.Release("thought-1", "session-A")
	if err := locker.Acquire("thought-1", "session-B"); err != nil {
		t.Fatalf("Acquire after Release returned error: %v", err)
	}
}

func TestHeartbeatExtends(t *testing.T) {
	locker := New(50 * time.Millisecond)
	if err := locker.Acquire("thought-1", "session-A"); err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	// Heartbeat keeps the lock alive past the original TTL.
	for i := 0; i < 4; i++ {
		time.Sleep(20 * time.Millisecond)
		if err := locker.Heartbeat("thought-1", "session-A"); err != nil {
			t.Fatalf("Heartbeat returned error: %v", err)
		}
	}
	if _, ok := locker.Holder("thought-1"); !ok {
		t.Fatalf("expected lock to still be held after heartbeats")
	}
}

func TestStaleAfterTTL(t *testing.T) {
	// Inject a deterministic clock so the test does not race the wall clock.
	locker := New(50 * time.Millisecond)
	current := time.Unix(0, 0)
	var clockMu sync.Mutex
	locker.now = func() time.Time {
		clockMu.Lock()
		defer clockMu.Unlock()
		return current
	}
	if err := locker.Acquire("thought-1", "session-A"); err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	// Advance past the TTL.
	clockMu.Lock()
	current = current.Add(100 * time.Millisecond)
	clockMu.Unlock()
	if err := locker.Acquire("thought-1", "session-B"); err != nil {
		t.Fatalf("expected stale lock to be reclaimable, got %v", err)
	}
	if holder, _ := locker.Holder("thought-1"); holder != "session-B" {
		t.Fatalf("expected session-B to hold the reclaimed lock, got %q", holder)
	}
}

func TestReleaseIsIdempotent(t *testing.T) {
	locker := New(time.Second)
	locker.Release("missing", "session-A")
	locker.Acquire("thought-1", "session-A")
	locker.Release("thought-1", "session-A")
	locker.Release("thought-1", "session-A")
	if _, ok := locker.Holder("thought-1"); ok {
		t.Fatalf("expected lock to be free after Release")
	}
}

func TestHeartbeatDoesNotStealLock(t *testing.T) {
	locker := New(time.Second)
	if err := locker.Acquire("thought-1", "session-A"); err != nil {
		t.Fatalf("Acquire returned error: %v", err)
	}
	// A non-holder heartbeat must not extend the lease or evict the holder.
	locker.Heartbeat("thought-1", "session-B")
	if holder, _ := locker.Holder("thought-1"); holder != "session-A" {
		t.Fatalf("expected session-A to still hold the lock, got %q", holder)
	}
}
