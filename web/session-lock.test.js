// Test session-lock.js against an in-memory browser-like context.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function loadLockModule({ now = () => 1000 } = {}) {
  const timers = new Map();
  let nextId = 1;
  const code = fs.readFileSync(
    path.join(__dirname, "session-lock.js"),
    "utf8",
  );
  const context = {
    performance: { now },
    setInterval: (fn, ms) => {
      const id = nextId++;
      timers.set(id, { fn, ms });
      return id;
    },
    clearInterval: (id) => { timers.delete(id); },
    addEventListener: () => {},
    removeEventListener: () => {},
    BroadcastChannel: undefined,
    Math,
    JSON,
    console,
  };
  vm.createContext(context);
  vm.runInContext(code, context, { filename: "session-lock.js" });
  return {
    lock: context.tflowSessionLock,
    timers,
    fireHeartbeats: () => {
      for (const [, timer] of timers) timer.fn();
    },
  };
}

test("acquire grants the lock to a fresh session without browser storage", () => {
  const env = loadLockModule();
  assert.equal(env.lock.acquire("thought-1", "session-A"), true);
  const holder = env.lock.getHolder("thought-1");
  assert.equal(holder.sessionId, "session-A");
  assert.equal(typeof holder.ts, "number");
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
  env.lock.heartbeat("thought-1", "session-B");
  assert.equal(env.lock.getHolder("thought-1").ts, before);
  env.lock.heartbeat("thought-1", "session-A");
  assert.ok(env.lock.getHolder("thought-1").ts >= before);
});

test("stale lock above TTL is dropped and can be re-acquired", () => {
  let now = 1000;
  const env = loadLockModule({ now: () => now });
  env.lock.acquire("thought-1", "session-A");
  now = 1000 + 200_000;
  assert.equal(env.lock.getHolder("thought-1"), null);
  assert.equal(env.lock.acquire("thought-1", "session-B"), true);
});

test("sweepStaleLocks drops expired entries and keeps live ones", () => {
  let now = 1000;
  const env = loadLockModule({ now: () => now });
  env.lock.acquire("thought-1", "session-A");
  env.lock.acquire("thought-2", "session-A");
  now = 1000 + 200_000;
  env.lock.acquire("thought-3", "session-B");
  now += 10_000;
  const removed = env.lock.sweepStaleLocks();
  assert.equal(removed, 2);
  assert.equal(env.lock.getHolder("thought-1"), null);
  assert.equal(env.lock.getHolder("thought-2"), null);
  assert.equal(env.lock.getHolder("thought-3").sessionId, "session-B");
});

test("sweepStaleLocks is a no-op on an empty store", () => {
  const env = loadLockModule();
  assert.equal(env.lock.sweepStaleLocks(), 0);
});
