// session-lock.js
//
// Process-wide (per-origin) mutex for in-flight Capture sessions editing
// the same Thought. The backend has a thoughtlock.Locker that serializes
// PATCH /api/thoughts/:id against the refiner's refineJob, but that lock
// lives on the server. We also want a client-side check so a second tab
// opening the same Thought can warn the user before they type.
//
// The bus and a small holder map live in localStorage so multiple tabs see
// each other. The default TTL is 90s; we heartbeat every 20s while a
// session is active. On pagehide we drop the lock so the other tab is
// released without waiting for TTL to lapse.

(function () {
  "use strict";

  const STORAGE_PREFIX = "tflow.lock.";
  const DEFAULT_TTL_MS = 90 * 1000;
  const HEARTBEAT_MS = 20 * 1000;
  const STORAGE_EVENT = typeof window !== "undefined" ? "storage" : null;
  const BUS_EVENT_ACQUIRE = "lock:acquired";
  const BUS_EVENT_RELEASE = "lock:released";
  const BUS_EVENT_HEARTBEAT = "lock:heartbeat";

  const listeners = new Set();
  const owners = new Map(); // thoughtId -> heartbeat timer
  const now = () => (typeof performance !== "undefined" && performance.now)
    ? performance.now()
    : Date.now();

  function storageKey(thoughtId) {
    return STORAGE_PREFIX + thoughtId;
  }

  function read(thoughtId) {
    if (typeof localStorage === "undefined") return null;
    try {
      const raw = localStorage.getItem(storageKey(thoughtId));
      if (!raw) return null;
      const value = JSON.parse(raw);
      if (!value || typeof value !== "object") return null;
      if (typeof value.sessionId !== "string" || typeof value.ts !== "number") return null;
      return value;
    } catch (_) {
      return null;
    }
  }

  function write(thoughtId, value) {
    if (typeof localStorage === "undefined") return;
    try {
      if (value === null) localStorage.removeItem(storageKey(thoughtId));
      else localStorage.setItem(storageKey(thoughtId), JSON.stringify(value));
    } catch (_) {
      /* quota or unavailable — drop the write */
    }
  }

  function broadcast(event, payload) {
    if (listeners.size === 0) return;
    for (const listener of listeners) {
      try {
        listener({ event, payload });
      } catch (_) {
        /* ignore listener errors */
      }
    }
  }

  function isExpired(holder, ms = now()) {
    if (!holder) return true;
    return ms - holder.ts > DEFAULT_TTL_MS;
  }

  function getHolder(thoughtId) {
    const holder = read(thoughtId);
    if (!holder) return null;
    if (isExpired(holder)) {
      write(thoughtId, null);
      return null;
    }
    return holder;
  }

  function startHeartbeat(thoughtId, sessionId) {
    stopHeartbeat(thoughtId);
    const timer = (typeof window !== "undefined" && window.setInterval) ? window.setInterval : null;
    if (!timer) return;
    const id = timer(() => {
      const current = read(thoughtId);
      if (!current || current.sessionId !== sessionId) {
        stopHeartbeat(thoughtId);
        return;
      }
      write(thoughtId, { sessionId, ts: now() });
      broadcast(BUS_EVENT_HEARTBEAT, { thoughtId, sessionId });
    }, HEARTBEAT_MS);
    owners.set(thoughtId, id);
  }

  function stopHeartbeat(thoughtId) {
    const id = owners.get(thoughtId);
    if (id === undefined) return;
    if (typeof window !== "undefined" && window.clearInterval) window.clearInterval(id);
    owners.delete(thoughtId);
  }

  function acquire(thoughtId, sessionId) {
    if (!thoughtId || !sessionId) return false;
    const existing = getHolder(thoughtId);
    if (existing && existing.sessionId !== sessionId) return false;
    write(thoughtId, { sessionId, ts: now() });
    startHeartbeat(thoughtId, sessionId);
    broadcast(BUS_EVENT_ACQUIRE, { thoughtId, sessionId });
    return true;
  }

  function heartbeat(thoughtId, sessionId) {
    if (!thoughtId || !sessionId) return;
    const current = read(thoughtId);
    if (!current || current.sessionId !== sessionId) return;
    write(thoughtId, { sessionId, ts: now() });
    broadcast(BUS_EVENT_HEARTBEAT, { thoughtId, sessionId });
  }

  function release(thoughtId, sessionId) {
    if (!thoughtId) return;
    stopHeartbeat(thoughtId);
    const current = read(thoughtId);
    if (current && current.sessionId === sessionId) {
      write(thoughtId, null);
      broadcast(BUS_EVENT_RELEASE, { thoughtId, sessionId });
    }
  }

  function releaseAll(sessionId) {
    if (typeof localStorage === "undefined") return;
    try {
      for (let i = localStorage.length - 1; i >= 0; i--) {
        const key = localStorage.key(i);
        if (!key || !key.startsWith(STORAGE_PREFIX)) continue;
        const value = read(key.slice(STORAGE_PREFIX.length));
        if (value && value.sessionId === sessionId) {
          stopHeartbeat(key.slice(STORAGE_PREFIX.length));
          localStorage.removeItem(key);
          broadcast(BUS_EVENT_RELEASE, { thoughtId: key.slice(STORAGE_PREFIX.length), sessionId });
        }
      }
    } catch (_) {
      /* ignore */
    }
  }

  // sweepStaleLocks drops every localStorage entry whose holder has
  // outlived the TTL. The per-key read() / getHolder() check only fires
  // for thoughts the user actually opens; locks for thoughts the user
  // is not currently editing would otherwise sit in localStorage for
  // up to 90s after the previous tab crashed. That can briefly show the
  // "another session is editing" indicator on the next visit if the
  // user happens to open the same thought — even though the previous
  // holder is long gone. Sweeping at boot keeps the indicator honest.
  function sweepStaleLocks() {
    if (typeof localStorage === "undefined") return 0;
    const ms = now();
    let removed = 0;
    try {
      for (let i = localStorage.length - 1; i >= 0; i--) {
        const key = localStorage.key(i);
        if (!key || !key.startsWith(STORAGE_PREFIX)) continue;
        const value = read(key.slice(STORAGE_PREFIX.length));
        if (!value) {
          // Corrupt entry — best-effort remove.
          localStorage.removeItem(key);
          continue;
        }
        if (isExpired(value, ms)) {
          localStorage.removeItem(key);
          removed++;
        }
      }
    } catch (_) {
      /* ignore */
    }
    return removed;
  }

  function on(handler) {
    listeners.add(handler);
    return () => listeners.delete(handler);
  }

  // Cross-tab awareness: another tab acquiring or releasing the same lock
  // should be visible to listeners without polling. storage event only
  // fires for *other* tabs, so this catches the case where the bus is
  // unavailable.
  function onStorageEvent(event) {
    if (!event || !event.key) return;
    if (!event.key.startsWith(STORAGE_PREFIX)) return;
    const thoughtId = event.key.slice(STORAGE_PREFIX.length);
    if (event.newValue === null) {
      broadcast(BUS_EVENT_RELEASE, { thoughtId, sessionId: "" });
      return;
    }
    let parsed = null;
    try { parsed = JSON.parse(event.newValue); } catch (_) { return; }
    if (!parsed || !parsed.sessionId) return;
    broadcast(BUS_EVENT_ACQUIRE, { thoughtId, sessionId: parsed.sessionId });
  }

  if (typeof window !== "undefined") {
    window.addEventListener(STORAGE_EVENT, onStorageEvent);
    window.addEventListener("pagehide", () => {
      for (const thoughtId of Array.from(owners.keys())) {
        const holder = read(thoughtId);
        if (holder) release(thoughtId, holder.sessionId);
      }
    });
  }

  const api = {
    acquire,
    heartbeat,
    release,
    releaseAll,
    sweepStaleLocks,
    getHolder,
    on,
    DEFAULT_TTL_MS,
    HEARTBEAT_MS,
  };

  if (typeof window !== "undefined") {
    window.tflowSessionLock = api;
  }
  if (typeof globalThis !== "undefined") {
    globalThis.tflowSessionLock = api;
  }
  if (typeof module !== "undefined" && module.exports) {
    module.exports = api;
  }
})();
