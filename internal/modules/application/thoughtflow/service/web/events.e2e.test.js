// SSE end-to-end test.
//
// Opens a long-lived /api/events subscription, triggers a real
// capture (which fans out an event through the in-process event hub),
// and asserts the event arrives with the expected framing. This
// covers the streaming path that api.e2e.test.js deliberately skips,
// since SSE blocks the response and is easier to reason about in
// isolation.

const assert = require("node:assert/strict");
const fs = require("node:fs");
const fsp = require("node:fs/promises");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { spawn } = require("node:child_process");
const test = require("node:test");

const REPO_ROOT = path.resolve(__dirname, "..", "..", "..", "..", "..");
const BINARY_CANDIDATES = [
  path.join(REPO_ROOT, "thoughtflow"),
  path.join(os.tmpdir(), "thoughtflow"),
];
const BINARY = BINARY_CANDIDATES.find((candidate) => {
  try {
    return fs.statSync(candidate).isFile() && fs.accessSync(candidate, fs.constants.X_OK) === undefined;
  } catch (_) {
    return false;
  }
});

if (!BINARY) {
  test("thoughtflow binary present", () => {
    assert.fail(
      `expected one of: ${BINARY_CANDIDATES.join(", ")} to be an executable. ` +
        "Run `make build` first or set the binary on PATH."
    );
  });
}

function pickFreePort() {
  return new Promise((resolve, reject) => {
    const probe = http.createServer();
    probe.listen(0, "127.0.0.1", () => {
      const { port } = probe.address();
      probe.close(() => resolve(port));
    });
    probe.on("error", reject);
  });
}

async function startServer() {
  const configDir = await fsp.mkdtemp(path.join(os.tmpdir(), "tf-sse-cfg-"));
  const contentDir = await fsp.mkdtemp(path.join(os.tmpdir(), "tf-sse-content-"));
  const stateDir = await fsp.mkdtemp(path.join(os.tmpdir(), "tf-sse-state-"));
  const port = await pickFreePort();

  const config = [
    "[server]",
    `host = "127.0.0.1"`,
    `port = ${port}`,
    "[workspace]",
    `content_dir = ${JSON.stringify(contentDir)}`,
    "auto_init_git = false",
    "[runtime]",
    `state_dir = ${JSON.stringify(stateDir)}`,
    "[git_sync]",
    "enabled = false",
    "[embedding]",
    "enabled = false",
    "[llm]",
    "configured = false",
    "[search]",
    'duckdb_path = "thoughtflow.duckdb"',
    'default_mode = "hybrid"',
    "",
  ].join("\n");
  await fsp.writeFile(path.join(configDir, "application.toml"), config, "utf8");

  const proc = spawn(BINARY, ["-config-dir", configDir], {
    stdio: ["ignore", "pipe", "pipe"],
    env: { ...process.env },
  });
  const stderrChunks = [];
  proc.stderr.on("data", (chunk) => stderrChunks.push(chunk));

  const baseURL = `http://127.0.0.1:${port}`;
  await waitForReady(baseURL);

  return {
    baseURL,
    configDir,
    contentDir,
    stateDir,
    async stop() {
      proc.kill("SIGTERM");
      await new Promise((resolve) => {
        const timeout = setTimeout(() => {
          proc.kill("SIGKILL");
          resolve();
        }, 5000);
        proc.once("exit", () => {
          clearTimeout(timeout);
          resolve();
        });
      });
      await fsp.rm(configDir, { recursive: true, force: true });
      await fsp.rm(contentDir, { recursive: true, force: true });
      await fsp.rm(stateDir, { recursive: true, force: true });
      if (proc.exitCode !== 0 && proc.exitCode !== null) {
        const stderr = Buffer.concat(stderrChunks).toString("utf8");
        if (stderr) {
          process.stderr.write(`--- thoughtflow stderr ---\n${stderr}\n--- end ---\n`);
        }
      }
    },
  };
}

async function waitForReady(baseURL) {
  const deadline = Date.now() + 15_000;
  let lastError = null;
  while (Date.now() < deadline) {
    try {
      const res = await fetch(`${baseURL}/health/ready`);
      if (res.status === 200) return;
      lastError = new Error(`/health/ready returned ${res.status}`);
    } catch (err) {
      lastError = err;
    }
    await new Promise((resolve) => setTimeout(resolve, 150));
  }
  throw new Error(`thoughtflow did not become ready in time: ${lastError && lastError.message}`);
}

function postJSON(baseURL, pathname, body) {
  return new Promise((resolve, reject) => {
    const url = new URL(pathname, baseURL);
    const data = Buffer.from(JSON.stringify(body), "utf8");
    const req = http.request(
      {
        hostname: url.hostname,
        port: url.port,
        path: url.pathname,
        method: "POST",
        headers: { "Content-Type": "application/json", "Content-Length": data.length },
      },
      (res) => {
        const chunks = [];
        res.on("data", (c) => chunks.push(c));
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString("utf8");
          let json = null;
          try { json = JSON.parse(text); } catch (_) {}
          resolve({ status: res.statusCode, text, json });
        });
        res.on("error", reject);
      }
    );
    req.on("error", reject);
    req.write(data);
    req.end();
  });
}

// Open /api/events and accumulate frames into a buffer. Resolves when
// the supplied predicate(frame) returns true or the deadline elapses.
function subscribeEvents(baseURL, { types, deadlineMs = 8000, predicate = () => false } = {}) {
  return new Promise((resolve) => {
    const url = new URL("/api/events", baseURL);
    if (types && types.length) {
      url.searchParams.set("types", types.join(","));
    }
    const frames = [];
    const controller = new AbortController();
    const timer = setTimeout(() => {
      controller.abort();
      resolve(frames);
    }, deadlineMs);
    fetch(url, { headers: { Accept: "text/event-stream" }, signal: controller.signal })
      .then(async (res) => {
        if (!res.ok || !res.body) {
          clearTimeout(timer);
          resolve(frames);
          return;
        }
        const reader = res.body.getReader();
        const decoder = new TextDecoder();
        let buffer = "";
        // eslint-disable-next-line no-constant-condition
        while (true) {
          const { value, done } = await reader.read();
          if (done) break;
          buffer += decoder.decode(value, { stream: true });
          let sep = buffer.indexOf("\n\n");
          while (sep !== -1) {
            const raw = buffer.slice(0, sep);
            buffer = buffer.slice(sep + 2);
            const frame = parseFrame(raw);
            if (frame) {
              frames.push(frame);
              if (predicate(frame)) {
                clearTimeout(timer);
                controller.abort();
                resolve(frames);
                return;
              }
            }
            sep = buffer.indexOf("\n\n");
          }
        }
        clearTimeout(timer);
        resolve(frames);
      })
      .catch(() => {
        clearTimeout(timer);
        resolve(frames);
      });
  });

  function parseFrame(raw) {
    if (!raw || raw.startsWith(":")) return null;
    const out = { event: null, id: null, data: null };
    for (const line of raw.split("\n")) {
      if (line.startsWith("event: ")) out.event = line.slice(7).trim();
      else if (line.startsWith("id: ")) out.id = line.slice(4).trim();
      else if (line.startsWith("data: ")) out.data = line.slice(6);
    }
    if (out.data) {
      try { out.payload = JSON.parse(out.data); } catch (_) { out.payload = null; }
    }
    return out;
  }
}

test("SSE e2e", async (t) => {
  const server = await startServer();
  t.after(() => server.stop());

  await t.test("subscribe and receive thought.captured after a POST", async () => {
    // Open subscription first so the event is guaranteed to be in the
    // stream's history. Stop as soon as a thought.captured frame
    // arrives to keep the test fast.
    const subscription = subscribeEvents(server.baseURL, {
      types: ["thought.captured"],
      deadlineMs: 8000,
      predicate: (frame) => frame.event === "thought.captured",
    });
    // Give the server a tick to register the subscriber before we publish.
    await new Promise((resolve) => setTimeout(resolve, 250));
    const create = await postJSON(server.baseURL, "/api/thoughts", {
      type: "text",
      title: "sse smoke",
      content: "SSE pipe test",
    });
    assert.equal(create.status, 202, `create status=${create.status} body=${create.text}`);

    const frames = await subscription;
    const captured = frames.find((frame) => frame.event === "thought.captured");
    assert.ok(captured, `expected a thought.captured frame, got: ${JSON.stringify(frames)}`);
    assert.ok(captured.id, "frame must carry an id");
    assert.ok(
      captured.payload && captured.payload.resource_id,
      `payload must include resource_id, got: ${JSON.stringify(captured.payload)}`
    );
  });

  await t.test("type filter suppresses non-matching events", async () => {
    // Filter for a type the system never emits; subscription should
    // hit the deadline (well under the 20s server keepalive) without
    // surfacing any frames.
    const frames = await subscribeEvents(server.baseURL, {
      types: ["thoughtflow.nonexistent.event"],
      deadlineMs: 1500,
    });
    assert.equal(frames.length, 0, `expected no frames for filtered type, got ${frames.length}`);
  });
});
