// session-lock.js
//
// Live-tab coordination for Capture sessions editing the same Thought.
// This module deliberately does not persist holders in browser storage.
// It keeps only in-memory state for the current document and, when
// available, uses BroadcastChannel to notify other open tabs. The backend
// thoughtlock remains the authority for write conflicts.

(function () {
  "use strict";

  const DEFAULT_TTL_MS = 90 * 1000;
  const HEARTBEAT_MS = 20 * 1000;
  const CHANNEL_NAME = "thoughtflow-session-lock";
  const BUS_EVENT_ACQUIRE = "lock:acquired";
  const BUS_EVENT_RELEASE = "lock:released";
  const BUS_EVENT_HEARTBEAT = "lock:heartbeat";

  const listeners = new Set();
  const holders = new Map(); // thoughtId -> { sessionId, ts }
  const owners = new Map(); // thoughtId -> heartbeat timer
  const now = () => (typeof performance !== "undefined" && performance.now)
    ? performance.now()
    : Date.now();

  let channel = null;
  if (typeof BroadcastChannel === "function") {
    try {
      channel = new BroadcastChannel(CHANNEL_NAME);
      channel.addEventListener("message", (event) => {
        const message = event?.data || {};
        if (!message || !message.event || !message.payload) return;
        applyRemote(message.event, message.payload);
      });
    } catch (_) {
      channel = null;
    }
  }

  function broadcast(event, payload, options = {}) {
    if (!options.remoteOnly) {
      for (const listener of listeners) {
        try {
          listener({ event, payload });
        } catch (_) {
          /* ignore listener errors */
        }
      }
    }
    if (!options.localOnly && channel) {
      try { channel.postMessage({ event, payload }); } catch (_) { /* ignore */ }
    }
  }

  function isExpired(holder, ms = now()) {
    if (!holder) return true;
    return ms - holder.ts > DEFAULT_TTL_MS;
  }

  function getHolder(thoughtId) {
    const holder = holders.get(thoughtId);
    if (!holder) return null;
    if (isExpired(holder)) {
      holders.delete(thoughtId);
      return null;
    }
    return { ...holder };
  }

  function setHolder(thoughtId, sessionId, ts = now()) {
    holders.set(thoughtId, { sessionId, ts });
  }

  function applyRemote(event, payload) {
    const thoughtId = payload?.thoughtId || "";
    const sessionId = payload?.sessionId || "";
    if (!thoughtId || !sessionId) return;
    if (event === BUS_EVENT_RELEASE) {
      const current = holders.get(thoughtId);
      if (current && current.sessionId === sessionId) holders.delete(thoughtId);
      broadcast(event, payload, { localOnly: true });
      return;
    }
    if (event === BUS_EVENT_ACQUIRE || event === BUS_EVENT_HEARTBEAT) {
      setHolder(thoughtId, sessionId, typeof payload.ts === "number" ? payload.ts : now());
      broadcast(event, payload, { localOnly: true });
    }
  }

  function startHeartbeat(thoughtId, sessionId) {
    stopHeartbeat(thoughtId);
    const timer = (typeof window !== "undefined" && window.setInterval) ? window.setInterval : null;
    if (!timer) return;
    const id = timer(() => heartbeat(thoughtId, sessionId), HEARTBEAT_MS);
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
    const ts = now();
    setHolder(thoughtId, sessionId, ts);
    startHeartbeat(thoughtId, sessionId);
    broadcast(BUS_EVENT_ACQUIRE, { thoughtId, sessionId, ts });
    return true;
  }

  function heartbeat(thoughtId, sessionId) {
    if (!thoughtId || !sessionId) return;
    const current = holders.get(thoughtId);
    if (!current || current.sessionId !== sessionId) return;
    const ts = now();
    setHolder(thoughtId, sessionId, ts);
    broadcast(BUS_EVENT_HEARTBEAT, { thoughtId, sessionId, ts });
  }

  function release(thoughtId, sessionId) {
    if (!thoughtId) return;
    stopHeartbeat(thoughtId);
    const current = holders.get(thoughtId);
    if (current && current.sessionId === sessionId) {
      holders.delete(thoughtId);
      broadcast(BUS_EVENT_RELEASE, { thoughtId, sessionId });
    }
  }

  function releaseAll(sessionId) {
    for (const [thoughtId, holder] of Array.from(holders.entries())) {
      if (holder.sessionId === sessionId) release(thoughtId, sessionId);
    }
  }

  function sweepStaleLocks() {
    const ms = now();
    let removed = 0;
    for (const [thoughtId, holder] of Array.from(holders.entries())) {
      if (isExpired(holder, ms)) {
        holders.delete(thoughtId);
        stopHeartbeat(thoughtId);
        removed++;
      }
    }
    return removed;
  }

  function on(handler) {
    listeners.add(handler);
    return () => listeners.delete(handler);
  }

  if (typeof window !== "undefined") {
    window.addEventListener("pagehide", () => {
      for (const thoughtId of Array.from(owners.keys())) {
        const holder = holders.get(thoughtId);
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
