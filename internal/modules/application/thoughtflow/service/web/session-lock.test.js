// Test session-lock.js against a faked localStorage + timer.
// session-lock.js attaches to `globalThis.tflowSessionLock` and uses
// localStorage as the shared state. We replace both in a vm context so
// the production code can run unmodified.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function makeStorage() {
  const data = new Map();
  return {
    data,
    get length() { return data.size; },
    getItem(key) { return data.has(key) ? data.get(key) : null; },
    setItem(key, value) { data.set(key, String(value)); },
    removeItem(key) { data.delete(key); },
    key(index) { return Array.from(data.keys())[index] ?? null; },
    clear() { data.clear(); },
  };
}

function loadLockModule({ initialStorage = null, now = () => 1000 } = {}) {
  const storage = initialStorage || makeStorage();
  const timers = new Map();
  let nextId = 1;
  const code = fs.readFileSync(
    path.join(__dirname, "session-lock.js"),
    "utf8",
  );
  const context = {
    localStorage: storage,
    performance: { now },
    setInterval: (fn, ms) => {
      const id = nextId++;
      timers.set(id, { fn, ms });
      return id;
    },
    clearInterval: (id) => { timers.delete(id); },
    addEventListener: () => {},
    removeEventListener: () => {},
    Math,
    JSON,
    console,
  };
  vm.createContext(context);
  vm.runInContext(code, context, { filename: "session-lock.js" });
  return {
    lock: context.tflowSessionLock,
    storage,
    timers,
    fireHeartbeats: () => {
      for (const [, timer] of timers) {
        // Advance performance.now to mimic time passage so the writer
        // stamps a fresh ts.
        const fn = timer.fn;
        fn();
      }
    },
  };
}

test("acquire grants the lock to a fresh session and persists to localStorage", () => {
  const env = loadLockModule();
  assert.equal(env.lock.acquire("thought-1", "session-A"), true);
  const stored = env.storage.getItem("tflow.lock.thought-1");
  assert.ok(stored, "lock entry should be written to localStorage");
  const parsed = JSON.parse(stored);
  assert.equal(parsed.sessionId, "session-A");
  assert.equal(typeof parsed.ts, "number");
});

test("acquire refuses a session that does not own the lock", () => {
  const env = loadLockModule();
  assert.equal(env.lock.acquire("thought-1", "session-A"), true);
  assert.equal(env.lock.acquire("thought-1", "session-B"), false);
});

test("acquire is idempotent for the existing holder", () => {
  const env = loadLockModule();
  assert.equal(env.lock.acquire("thought-1", "session-A"), true);
  assert.equal(env.lock.acquire("thought-1", "session-A"), true);
  const holder = env.lock.getHolder("thought-1");
  assert.equal(holder.sessionId, "session-A");
});

test("release drops the lock for the owner and is a no-op for others", () => {
  const env = loadLockModule();
  env.lock.acquire("thought-1", "session-A");
  env.lock.release("thought-1", "session-B");
  assert.ok(env.lock.getHolder("thought-1"));
  env.lock.release("thought-1", "session-A");
  assert.equal(env.lock.getHolder("thought-1"), null);
});

test("heartbeat extends the lease timestamp for the owner only", () => {
  const tsValues = [1000, 5000];
  let i = 0;
  const env = loadLockModule({ now: () => tsValues[i++] ?? 6000 });
  env.lock.acquire("thought-1", "session-A");
  const before = env.lock.getHolder("thought-1").ts;
  env.lock.heartbeat("thought-1", "session-B"); // wrong session
  assert.equal(env.lock.getHolder("thought-1").ts, before);
  env.lock.heartbeat("thought-1", "session-A");
  assert.ok(env.lock.getHolder("thought-1").ts >= before);
});

test("stale lock above TTL is dropped and can be re-acquired", () => {
  let now = 1000;
  const env = loadLockModule({ now: () => now });
  env.lock.acquire("thought-1", "session-A");
  now = 1000 + 200_000; // well past the 90s TTL
  assert.equal(env.lock.getHolder("thought-1"), null);
  assert.equal(env.lock.acquire("thought-1", "session-B"), true);
});
