// Backend HTTP API end-to-end tests.
//
// Spawns a real thoughtflow binary against a private content_dir and
// state_dir under os.tmpdir() so the suite never touches the user's
// running instance or its workspace. Every API endpoint registered
// in internal/modules/application/thoughtflow/service/service.go is
// exercised at least once, with assertions on the response envelope
// (request_id, data, error) and the routing behaviour the smoke
// tests in app.browser.test.js do not cover.
//
// SSE-only endpoints live in events.e2e.test.js so this file can run
// fast and stay focused on request/response semantics.

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
  const configDir = await fsp.mkdtemp(path.join(os.tmpdir(), "tf-e2e-cfg-"));
  const contentDir = await fsp.mkdtemp(path.join(os.tmpdir(), "tf-e2e-content-"));
  const stateDir = await fsp.mkdtemp(path.join(os.tmpdir(), "tf-e2e-state-"));
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
      const res = await request(baseURL, "/health/ready", "GET");
      if (res.status === 200) return;
      lastError = new Error(`/health/ready returned ${res.status}`);
    } catch (err) {
      lastError = err;
    }
    await sleep(150);
  }
  throw new Error(`thoughtflow did not become ready in time: ${lastError && lastError.message}`);
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

function request(baseURL, path, method, { body, headers } = {}) {
  return new Promise((resolve, reject) => {
    const url = new URL(path, baseURL);
    const data = body == null ? null : (typeof body === "string" ? Buffer.from(body, "utf8") : Buffer.from(JSON.stringify(body), "utf8"));
    const req = http.request(
      {
        hostname: url.hostname,
        port: url.port,
        path: url.pathname + url.search,
        method,
        headers: {
          ...(data ? { "Content-Type": "application/json", "Content-Length": data.length } : {}),
          ...(headers || {}),
        },
      },
      (res) => {
        const chunks = [];
        res.on("data", (chunk) => chunks.push(chunk));
        res.on("end", () => {
          const text = Buffer.concat(chunks).toString("utf8");
          let json = null;
          if (res.headers["content-type"] && res.headers["content-type"].includes("application/json") && text) {
            try {
              json = JSON.parse(text);
            } catch (_) {
              json = null;
            }
          }
          resolve({ status: res.statusCode, headers: res.headers, text, json });
        });
        res.on("error", reject);
      }
    );
    req.on("error", reject);
    if (data) req.write(data);
    req.end();
  });
}

function envelope(res) {
  assert.ok(res.json, `expected JSON body, got status ${res.status} text=${res.text}`);
  assert.equal(typeof res.json.request_id, "string", "envelope.request_id must be a string");
  assert.ok("data" in res.json, "envelope must have data field");
  assert.ok("error" in res.json, "envelope must have error field");
  return res.json;
}

test("API e2e", async (t) => {
  const server = await startServer();
  t.after(() => server.stop());

  await t.test("health endpoints respond 200", async () => {
    const live = await request(server.baseURL, "/health/live", "GET");
    assert.equal(live.status, 200);
    const ready = await request(server.baseURL, "/health/ready", "GET");
    assert.equal(ready.status, 200);
  });

  await t.test("system status reports duckdb ready", async () => {
    const res = await request(server.baseURL, "/api/system/status", "GET");
    assert.equal(res.status, 200);
    const body = envelope(res);
    assert.equal(body.data.duckdb.status, "ready");
    assert.ok(body.data.duckdb.path.endsWith("thoughtflow.duckdb"));
  });

  await t.test("thoughts lifecycle: create, get, patch, suggest", async () => {
    const create = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "e2e note", content: "DuckDB hybrid search under test." },
    });
    assert.equal(create.status, 202, `create status=${create.status} body=${create.text}`);
    const created = envelope(create).data;
    assert.ok(created.thought.id, "create response must include thought.id");
    assert.equal(created.thought.capture_status, "captured");
    const id = created.thought.id;

    const getRes = await request(server.baseURL, `/api/thoughts/${id}`, "GET");
    assert.equal(getRes.status, 200);
    assert.equal(envelope(getRes).data.thought.id, id);

    const patch = await request(server.baseURL, `/api/thoughts/${id}`, "PATCH", {
      body: { title: "e2e note renamed", ai_notes_append: "Renamed during e2e." },
      headers: { "X-Session-Id": "e2e-session-1" },
    });
    assert.equal(patch.status, 200, `patch status=${patch.status} body=${patch.text}`);
    const patched = envelope(patch).data;
    assert.equal(patched.thought.user_title, "e2e note renamed");

    const suggest = await request(server.baseURL, `/api/thoughts/${id}/suggest`, "GET");
    assert.ok([200, 204, 404].includes(suggest.status), `unexpected suggest status=${suggest.status}`);
  });

  await t.test("patch thought rejects unknown fields and missing session id", async () => {
    const create = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "patch validation", content: "Body" },
    });
    const id = envelope(create).data.thought.id;

    const noSession = await request(server.baseURL, `/api/thoughts/${id}`, "PATCH", {
      body: { title: "x" },
    });
    assert.ok(noSession.status >= 400, `expected 4xx without X-Session-Id, got ${noSession.status}`);

    const unknown = await request(server.baseURL, `/api/thoughts/${id}`, "PATCH", {
      body: { nonsense_field: true },
      headers: { "X-Session-Id": "e2e-session-1" },
    });
    assert.ok(unknown.status >= 400, `expected 4xx on unknown field, got ${unknown.status}`);
  });

  await t.test("capture session start returns a thought", async () => {
    const res = await request(server.baseURL, "/api/capture/sessions/start", "POST", {
      body: { content: "Captured via e2e", session_id: "e2e-capture-1" },
      headers: { "X-Session-Id": "e2e-capture-1" },
    });
    assert.ok([200, 202].includes(res.status), `start session status=${res.status} body=${res.text}`);
    const data = envelope(res).data;
    assert.ok(data.session_id, "session_id must be echoed back");
    assert.ok(data.thought && data.thought.id, "thought.id must be present");
  });

  await t.test("search responds in keyword, semantic and hybrid modes", async () => {
    await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "search-target", content: "alpha beta gamma DuckDB keyword search test" },
    });
    await sleep(50);
    for (const mode of ["keyword", "semantic", "hybrid"]) {
      const res = await request(
        server.baseURL,
        `/api/search?q=DuckDB&mode=${mode}&limit=5&explain=true`,
        "GET"
      );
      assert.equal(res.status, 200, `search mode=${mode} status=${res.status} body=${res.text}`);
      const data = envelope(res).data;
      assert.ok(Array.isArray(data.items), "items must be an array");
      // keyword mode should surface at least one hit on a freshly indexed note
      if (mode === "keyword") {
        assert.ok(data.items.length > 0, "keyword mode should hit the seeded thought");
        assert.equal(data.items[0].explain.keyword_source, "duckdb_fts");
      }
    }
  });

  await t.test("topics CRUD: create, get, update, rebuild, weave-proposals", async () => {
    const create = await request(server.baseURL, "/api/topics", "POST", {
      body: {
        name: "e2e topic",
        description: "topic for e2e",
        rules: { keywords: { any: ["duckdb"] }, auto_weave: false },
      },
    });
    assert.equal(create.status, 201, `topic create status=${create.status} body=${create.text}`);
    const topicId = envelope(create).data.id;
    assert.ok(topicId, "topic id required");

    const list = await request(server.baseURL, "/api/topics", "GET");
    assert.equal(list.status, 200);
    const listed = envelope(list).data;
    assert.ok(Array.isArray(listed));
    assert.ok(listed.find((topic) => topic.id === topicId), "newly created topic should appear in list");

    const getOne = await request(server.baseURL, `/api/topics/${topicId}`, "GET");
    assert.equal(getOne.status, 200);

    const update = await request(server.baseURL, `/api/topics/${topicId}`, "PUT", {
      body: {
        name: "e2e topic v2",
        description: "updated",
        rules: { keywords: { any: ["hybrid"] }, auto_weave: false },
      },
    });
    assert.equal(update.status, 200, `topic update status=${update.status} body=${update.text}`);

    const rebuild = await request(server.baseURL, `/api/topics/${topicId}/rebuild`, "POST", {});
    assert.ok([200, 202].includes(rebuild.status), `rebuild status=${rebuild.status}`);

    const proposals = await request(server.baseURL, `/api/topics/${topicId}/weave-proposals`, "GET");
    assert.equal(proposals.status, 200);
    assert.ok(Array.isArray(envelope(proposals).data));
  });

  await t.test("topics weave preview + accept round-trip", async () => {
    const create = await request(server.baseURL, "/api/topics", "POST", {
      body: { name: "weave topic", rules: { keywords: { any: ["weave"] }, auto_weave: true } },
    });
    const topicId = envelope(create).data.id;

    await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "weave candidate", content: "weave related content" },
    });
    await sleep(50);

    const preview = await request(server.baseURL, `/api/topics/${topicId}/weave-preview`, "POST", {
      body: { source_thought_id: "weave candidate" },
    });
    assert.ok([200, 201, 400, 404].includes(preview.status), `weave-preview status=${preview.status}`);

    const accept = await request(server.baseURL, `/api/topics/${topicId}/weave-accept`, "POST", {
      body: { document: "# weave topic\n\nWeave body." },
    });
    assert.ok([200, 400, 404, 409].includes(accept.status), `weave-accept status=${accept.status}`);
  });

  await t.test("synthesis draft list/create/save", async () => {
    const list = await request(server.baseURL, "/api/synthesis", "GET");
    assert.equal(list.status, 200);
    assert.ok(Array.isArray(envelope(list).data));

    // Create a real thought to feed the synthesizer.
    const thoughtRes = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "synthesis source", content: "Sourced for synthesis." },
    });
    const thoughtId = envelope(thoughtRes).data.thought.id;

    const create = await request(server.baseURL, "/api/synthesis", "POST", {
      body: { thought_ids: [thoughtId], goal: "compose from e2e", format: "summary" },
    });
    assert.equal(create.status, 200, `synthesis create status=${create.status} body=${create.text}`);
    const draftId = envelope(create).data.id;
    assert.ok(draftId);

    const get = await request(server.baseURL, `/api/synthesis/${draftId}`, "GET");
    assert.equal(get.status, 200);

    const save = await request(server.baseURL, "/api/synthesis/save", "POST", {
      body: { draft_id: draftId, content: "E2E saved synthesis", format: "summary" },
    });
    assert.ok([200, 202, 400].includes(save.status), `synthesis save status=${save.status}`);
  });

  await t.test("jobs and metrics endpoints respond", async () => {
    const metrics = await request(server.baseURL, "/api/system/metrics", "GET");
    assert.equal(metrics.status, 200);
    const data = envelope(metrics).data;
    assert.ok(data && typeof data === "object");

    const prom = await request(server.baseURL, "/metrics", "GET");
    assert.equal(prom.status, 200);
    assert.ok(typeof prom.text === "string" && prom.text.length > 0);

    const bogus = await request(server.baseURL, "/api/jobs/does-not-exist", "GET");
    assert.ok([404, 400].includes(bogus.status), `bogus job status=${bogus.status}`);
  });

  await t.test("reindex accepts POST and survives follow-up search", async () => {
    const reindex = await request(server.baseURL, "/api/system/reindex", "POST", {});
    assert.ok([200, 202].includes(reindex.status), `reindex status=${reindex.status}`);
    const search = await request(server.baseURL, "/api/search?q=alpha&mode=keyword&limit=1", "GET");
    assert.equal(search.status, 200);
  });

  await t.test("invalid thought id surfaces 4xx", async () => {
    const res = await request(server.baseURL, "/api/thoughts/does-not-exist", "GET");
    assert.ok(res.status >= 400, `expected 4xx for unknown thought, got ${res.status}`);
  });

  await t.test("unknown route returns 404 envelope", async () => {
    const res = await request(server.baseURL, "/api/this-route-does-not-exist", "GET");
    assert.equal(res.status, 404);
  });
});
