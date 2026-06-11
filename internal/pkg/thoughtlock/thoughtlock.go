// Package thoughtlock provides a process-wide mutex for thought files.
// The Capture session-mode PATCH path and the async refiner both write to
// the same Markdown file on disk. Without coordination, a PATCH landing
// while a refiner is mid-flight can race the writer and corrupt the
// front-matter or duplicate the AI Notes section.
//
// # Why the lock is in-memory only
//
// The lock is intentionally in-memory only. Persisting it to disk would
// create a worse problem than the one it solves: the on-disk state of
// the lock could drift from the actual process state. A crashed holder
// would leave a "you are still editing" marker in a file that no live
// process is using, and the next start of the service would either
// trust the stale marker (blocking legitimate writes) or ignore it
// (defeating the purpose of persisting). Neither is acceptable.
//
// Instead we lean on three invariants that together keep file state
// and process state consistent across all failure modes:
//
//  1. The lock is a single in-memory map. A process restart resets it
//     cleanly — there is no stale state to recover from.
//  2. markdown.WriteThought uses the tmp+rename pattern: data is written
//     to a sibling *.tmp file and renamed onto the target in a single
//     inode swap. A crash mid-write leaves the target at its prior
//     valid state; the only leftover is the *.tmp file, which
//     workspace.Open() sweeps at startup.
//  3. Every Acquire site pairs with a defer Release, so panics, errors,
//     and even SIGTERM-triggered shutdowns drop the lock. A holder that
//     somehow still leaks (e.g. SIGKILL during the LLM call) is reaped
//     by the TTL on the next Acquire for the same thoughtID.
//
// Holders must call Release (or fail to heartbeat past the TTL) before
// the lock is reusable.
package thoughtlock

import (
	"errors"
	"sync"
	"time"
)

// ErrLocked is returned when the lock is held by another session.
var ErrLocked = errors.New("thoughtlock: thought is locked by another session")

// RefinerSessionID is the sessionID the refiner module uses when it
// acquires a thought lock. Callers that fail to acquire and see this
// sessionID in the holder slot can map the failure to "AI is currently
// processing this thought" rather than "another browser tab is editing".
// Centralizing the constant here keeps the capture module's lock-reason
// detection and the refiner's Acquire calls in sync.
const RefinerSessionID = "refiner"

// DefaultTTL is the staleness window. A holder that fails to call
// Heartbeat within this window is considered abandoned and its lock may
// be taken over.
const DefaultTTL = 90 * time.Second

// Locker tracks in-flight thought mutations. Safe for concurrent use.
type Locker struct {
	mu   sync.Mutex
	held map[string]holder
	ttl  time.Duration
	now  func() time.Time
}

type holder struct {
	sessionID string
	expiresAt time.Time
}

// New returns a Locker with the given TTL. A zero or negative TTL falls
// back to DefaultTTL.
func New(ttl time.Duration) *Locker {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Locker{
		held: make(map[string]holder),
		ttl:  ttl,
		now:  time.Now,
	}
}

// holder returns the active holder for thoughtID, or the zero value if
// the entry is missing or expired. The boolean indicates whether an
// active holder exists.
func (l *Locker) holder(thoughtID string) (holder, bool) {
	h, ok := l.held[thoughtID]
	if !ok {
		return holder{}, false
	}
	if !h.expiresAt.After(l.now()) {
		// Stale — drop on read so the next Acquire is a clean slate.
		delete(l.held, thoughtID)
		return holder{}, false
	}
	return h, true
}

// Acquire takes the lock for thoughtID on behalf of sessionID. The same
// session may re-acquire an already-held lock (idempotent for the holder).
// Returns ErrLocked when another session holds an unexpired lock.
func (l *Locker) Acquire(thoughtID, sessionID string) error {
	if thoughtID == "" || sessionID == "" {
		return errors.New("thoughtlock: thoughtID and sessionID are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if existing, ok := l.holder(thoughtID); ok {
		if existing.sessionID == sessionID {
			// Re-entrant: extend the lease.
			l.held[thoughtID] = holder{sessionID: sessionID, expiresAt: l.now().Add(l.ttl)}
			return nil
		}
		return ErrLocked
	}
	l.held[thoughtID] = holder{sessionID: sessionID, expiresAt: l.now().Add(l.ttl)}
	return nil
}

// Heartbeat extends the lease for the existing holder. It is a no-op
// (returns nil) when the lock is not held by sessionID, so callers may
// heartbeat freely without first checking.
func (l *Locker) Heartbeat(thoughtID, sessionID string) error {
	if thoughtID == "" || sessionID == "" {
		return errors.New("thoughtlock: thoughtID and sessionID are required")
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.held[thoughtID]
	if !ok || h.sessionID != sessionID {
		return nil
	}
	l.held[thoughtID] = holder{sessionID: sessionID, expiresAt: l.now().Add(l.ttl)}
	return nil
}

// Release drops the lock for the given holder. It is a no-op when the
// lock is not held by sessionID, so callers may Release freely (e.g. on
// page unload) without first checking.
func (l *Locker) Release(thoughtID, sessionID string) {
	if thoughtID == "" || sessionID == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.held[thoughtID]
	if !ok || h.sessionID != sessionID {
		return
	}
	delete(l.held, thoughtID)
}

// Holder reports the session currently holding the lock (or empty if
// the lock is free or stale). Returned alongside a boolean for parity
// with the internal helper.
func (l *Locker) Holder(thoughtID string) (string, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	h, ok := l.holder(thoughtID)
	if !ok {
		return "", false
	}
	return h.sessionID, true
}

// defaultSingleton is a process-wide Locker used by the capture and
// refiner modules so a PATCH and a refine serialize against the same
// in-memory mutex. It is created lazily on first access.
var (
	defaultSingleton     *Locker
	defaultSingletonOnce sync.Once
)

// Default returns the process-wide Locker. The first call constructs
// it; subsequent calls return the same instance. Modules that need to
// guard thought writes should call this rather than constructing their
// own Locker so that cross-module coordination is automatic.
func Default() *Locker {
	defaultSingletonOnce.Do(func() {
		defaultSingleton = New(DefaultTTL)
	})
	return defaultSingleton
}
