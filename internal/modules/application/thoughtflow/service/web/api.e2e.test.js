// Backend HTTP API end-to-end tests.
//
// Spawns a real thoughtflow binary against a private content_dir and
// state_dir under os.tmpdir() so the suite never touches the user's
// running instance or its workspace. Every API endpoint registered
// in internal/modules/application/thoughtflow/service/service.go is
// exercised at least once, with assertions on the response envelope
// (request_id, data, error).
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

const REPO_ROOT = path.resolve(__dirname, "..", "..", "..", "..", "..", "..");
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

async function createServerFixture() {
  return {
    configDir: await fsp.mkdtemp(path.join(os.tmpdir(), "tf-e2e-cfg-")),
    contentDir: await fsp.mkdtemp(path.join(os.tmpdir(), "tf-e2e-content-")),
    stateDir: await fsp.mkdtemp(path.join(os.tmpdir(), "tf-e2e-state-")),
    async cleanup() {
      await fsp.rm(this.configDir, { recursive: true, force: true });
      await fsp.rm(this.contentDir, { recursive: true, force: true });
      await fsp.rm(this.stateDir, { recursive: true, force: true });
    },
  };
}

async function startServer(fixture = null) {
  const ownedFixture = fixture || await createServerFixture();
  const { configDir, contentDir, stateDir } = ownedFixture;
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
    fixture: ownedFixture,
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
      if (!fixture) {
        await ownedFixture.cleanup();
      }
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

    // The expander pipeline (post-refine 4-way expansion) holds the
    // thoughtlock with session id "expander" for the duration of the
    // background job. The first PATCH attempt may race with that
    // lock and surface 409. Retry briefly until the lock is released
    // so the lifecycle assertion is deterministic; if the lock never
    // clears we surface the original 409 for diagnosis.
    const patchDeadline = Date.now() + 5000;
    let patch;
    for (;;) {
      patch = await request(server.baseURL, `/api/thoughts/${id}`, "PATCH", {
        body: { title: "e2e note renamed", ai_notes_append: "Renamed during e2e." },
        headers: { "X-Session-Id": "e2e-session-1" },
      });
      if (patch.status !== 409) break;
      if (Date.now() > patchDeadline) break;
      await sleep(100);
    }
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

  await t.test("post-refine expansion writes 4 fields to the thought", async () => {
    // PR5b/Phase 10: after the refiner emits thought.refined, the
    // expander module runs a 4-way pipeline (related thoughts, LLM
    // plan, near-miss topics, URL followups) and persists the result
    // as front-matter fields. The test env has [llm] configured =
    // false, so the LLM stage uses the local provider which always
    // returns a non-empty plan — that's the most reliable signal that
    // the expander ran end-to-end.
    const create = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "expansion e2e", content: "post-refine expansion coverage." },
    });
    assert.equal(create.status, 202, `create status=${create.status} body=${create.text}`);
    const id = envelope(create).data.thought.id;

    // The expansion runs in the background; poll GET until the local
    // LLM plan lands or the deadline passes. The 4 fields may be empty
    // arrays / empty strings for short thoughts, so we only assert that
    // the keys are present in the snapshot — the field-level
    // guarantees live in the Go unit tests for the expander.
    const deadline = Date.now() + 8000;
    let snapshot;
    let planLanded = false;
    for (;;) {
      const res = await request(server.baseURL, `/api/thoughts/${id}`, "GET");
      assert.equal(res.status, 200);
      snapshot = envelope(res).data;
      if (typeof snapshot.thought.expansion_plan === "string" && snapshot.thought.expansion_plan.length > 0) {
        planLanded = true;
        break;
      }
      if (Date.now() > deadline) break;
      await sleep(150);
    }
    assert.ok(planLanded, `expansion_plan did not land within deadline; snapshot=${JSON.stringify(snapshot.thought).slice(0, 300)}`);
    // The 4 expansion fields must all be present in some form on the
    // snapshot so the frontend can render them. The Go model tags the
    // slice / string fields with `omitempty`, so empty values surface
    // as `undefined` in JSON (a missing key) — that is still a
    // well-formed expansion result, the frontend treats it as "no
    // items". The LLM plan however is non-empty once the local
    // provider finishes, so `expansion_plan` is always a string here.
    const isArrayOrMissing = (value) => Array.isArray(value) || value === undefined;
    assert.ok(isArrayOrMissing(snapshot.thought.related_thought_ids), "related_thought_ids must be an array or omitted");
    assert.ok(isArrayOrMissing(snapshot.thought.suggested_topic_ids), "suggested_topic_ids must be an array or omitted");
    assert.ok(isArrayOrMissing(snapshot.thought.url_followups), "url_followups must be an array or omitted (text type has no followups)");
    assert.equal(typeof snapshot.thought.expansion_plan, "string", "expansion_plan must be a string");
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
      assert.ok(Array.isArray(data.results), "results must be an array");
      // The freshly seeded note must appear somewhere in the top 5 — the
      // rank is not asserted because the e2e suite shares an index and
      // other notes (e.g. "e2e note") may outrank on keyword frequency.
      const hit = data.results.find((r) => r.title === "search-target");
      assert.ok(hit, `search mode=${mode} should surface the seeded note; got titles=${data.results.map((r) => r.title).join(",")}`);
      assert.ok(typeof hit.score === "number", "score must be a number");
    }
  });

  await t.test("search filters by tag and topic_id, returns SearchResultView shape", async () => {
    // The Web-facing search surface (per convergence todo 6.1) only emits
    // q / tags / topic_id; assertions here pin the API contract to that
    // surface and verify SearchResultView projects results without an
    // explain block.
    await request(server.baseURL, "/api/thoughts", "POST", {
      body: {
        type: "text",
        title: "search-tagged",
        content: "vector store retrieval",
        tags: ["rag"],
      },
    });
    await sleep(50);

    // tags=rag should surface the freshly seeded note.
    const byTag = await request(
      server.baseURL,
      `/api/search?q=vector&tags=rag&limit=5`,
      "GET"
    );
    assert.equal(byTag.status, 200, `tags filter status=${byTag.status}`);
    const tagData = envelope(byTag).data;
    assert.ok(Array.isArray(tagData.results), "results must be an array");
    assert.ok(
      tagData.results.find((r) => r.title === "search-tagged"),
      "tags=rag must surface the seeded note",
    );
    // SearchResultView does not expose an `explain` field; the legacy
    // /api/search?explain=true still works server-side but the projection is
    // a flat results array.
    assert.equal(typeof tagData.explain, "undefined", "explain field is not part of the projection");
  });

  await t.test("topics CRUD: create, get, update, refresh, weave-proposals", async () => {
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

    const refresh = await request(server.baseURL, `/api/topics/${topicId}/refresh`, "POST", {});
    assert.ok([200, 202].includes(refresh.status), `refresh status=${refresh.status}`);

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

  await t.test("compose draft list/create/save", async () => {
    const list = await request(server.baseURL, "/api/compose/drafts", "GET");
    assert.equal(list.status, 200);
    assert.ok(Array.isArray(envelope(list).data));

    // Create a real thought to feed the synthesizer.
    const thoughtRes = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "compose source", content: "Sourced for compose." },
    });
    const thoughtId = envelope(thoughtRes).data.thought.id;

    const create = await request(server.baseURL, "/api/compose/drafts", "POST", {
      body: {
        sources: [{ source_type: "thought", source_id: thoughtId }],
        selected_thought_ids: [thoughtId],
        goal: "compose from e2e",
        format: "summary",
      },
    });
    assert.equal(create.status, 200, `compose create status=${create.status} body=${create.text}`);
    const draftId = envelope(create).data.id;
    assert.ok(draftId);

    const get = await request(server.baseURL, `/api/compose/drafts/${draftId}`, "GET");
    assert.equal(get.status, 200);

    const save = await request(server.baseURL, `/api/compose/drafts/${draftId}/save`, "POST", {
      body: { content: "E2E saved compose", title: "compose from e2e" },
    });
    assert.ok([200, 202, 400].includes(save.status), `compose save status=${save.status}`);
  });

  await t.test("capture session recovery round-trips through active and reuse_last", async () => {
    // Open a brand-new session through the canonical capture session
    // endpoint. X-Session-Id is optional, but using it here makes the
    // session id deterministic for the round-trip assertions below.
    const sessionID = `e2e-recover-${Date.now()}`;
    const start = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { content: "Recovery seed message" },
      headers: { "X-Session-Id": sessionID },
    });
    assert.equal(start.status, 200, `start status=${start.status} body=${start.text}`);
    assert.equal(envelope(start).data.session_id, sessionID);

    // GET /api/capture/sessions/active must surface the most-recent
    // uncommitted scratchpad so the front end can rehydrate on
    // reload. With one session present it must be the one we just
    // created.
    const active = await request(server.baseURL, "/api/capture/sessions/active", "GET");
    assert.equal(active.status, 200);
    const activeData = envelope(active).data;
    assert.equal(activeData.session_id, sessionID, "active session must be the freshly started one");

    // POST /api/capture/sessions with reuse_last must echo the
    // same scratchpad back without minting a new id. This is the
    // "boot the page without a fresh id" path the UI uses.
    const reuse = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { reuse_last: true },
    });
    assert.equal(reuse.status, 200, `reuse status=${reuse.status} body=${reuse.text}`);
    const reuseData = envelope(reuse).data;
    assert.equal(reuseData.session_id, sessionID, "reuse_last must round-trip the active session id");
    assert.ok(Array.isArray(reuseData.messages), "scratchpad must include the message log");

  });

  await t.test("capture session survives service restart with session_context", async () => {
    const fixture = await createServerFixture();
    let first = null;
    let second = null;
    try {
      first = await startServer(fixture);
      const sessionID = `e2e-restart-${Date.now()}`;
      const start = await request(first.baseURL, "/api/capture/sessions", "POST", {
        body: { content: "Restart recovery #prd https://example.com/restart" },
        headers: { "X-Session-Id": sessionID },
      });
      assert.equal(start.status, 200, `restart start status=${start.status} body=${start.text}`);
      const started = envelope(start).data;
      assert.equal(started.session_id, sessionID);
      assert.equal(started.session_context.candidate_tags[0], "prd");
      assert.equal(started.session_context.source_links[0], "https://example.com/restart");

      await first.stop();
      first = null;
      second = await startServer(fixture);
      const active = await request(second.baseURL, "/api/capture/sessions/active", "GET");
      assert.equal(active.status, 200, `active after restart status=${active.status} body=${active.text}`);
      const data = envelope(active).data;
      assert.equal(data.session_id, sessionID, "restart must restore the last unarchived session");
      assert.equal(data.session_context.candidate_tags[0], "prd");
      assert.equal(data.session_context.source_links[0], "https://example.com/restart");
      assert.match(data.session_context.candidate_body, /Restart recovery/);

      const appendDefault = await request(second.baseURL, "/api/capture/sessions", "POST", {
        body: { content: "Default append after restart without explicit session id" },
      });
      assert.equal(appendDefault.status, 200, `appendDefault status=${appendDefault.status} body=${appendDefault.text}`);
      const appended = envelope(appendDefault).data;
      assert.equal(appended.session_id, sessionID, "content without X-Session-Id must append to the restored last active session");
      assert.match(appended.content, /Default append after restart without explicit session id/);
    } finally {
      if (first) await first.stop();
      if (second) await second.stop();
      await fixture.cleanup();
    }
  });

  await t.test("session_context update persists structured fields and survives a follow-up read", async () => {
    const sessionID = `e2e-context-${Date.now()}`;
    const start = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { content: "Context seed" },
      headers: { "X-Session-Id": sessionID },
    });
    assert.equal(start.status, 200);

    // POST /api/capture/sessions/{id}/context takes a flat
    // SessionContext payload (not a wrapped {session_context: ...}
    // envelope). The LLM tool surface calls this on every
    // refinement turn with a fully-replaced context block.
    const contextBody = {
      topic: "e2e context topic",
      goal: "verify session_context round-trip",
      confirmed_facts: ["fact one", "fact two"],
      open_questions: ["q one"],
      conflicts: [],
      suggested_topic_ids: ["e2e topic context"],
    };
    const ctx = await request(server.baseURL, `/api/capture/sessions/${sessionID}/context`, "POST", {
      body: contextBody,
    });
    assert.equal(ctx.status, 200, `context status=${ctx.status} body=${ctx.text}`);
    const ctxData = envelope(ctx).data;
    assert.equal(ctxData.session_context.topic, "e2e context topic");
    assert.equal(ctxData.session_context.goal, "verify session_context round-trip");
    assert.deepEqual(ctxData.session_context.confirmed_facts, ["fact one", "fact two"]);

    // Re-read the session and confirm the context survived.
    const get = await request(server.baseURL, `/api/capture/sessions/${sessionID}`, "GET");
    assert.equal(get.status, 200);
    const reloaded = envelope(get).data;
    assert.equal(reloaded.session_context.topic, "e2e context topic");
  });

  await t.test("session messages auto-refresh context without explicit context call", async () => {
    const sessionID = `e2e-auto-context-${Date.now()}`;
    const start = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { content: "Auto context seed #autocontext https://example.com/auto" },
      headers: { "X-Session-Id": sessionID },
    });
    assert.equal(start.status, 200, `auto context start status=${start.status} body=${start.text}`);

    const followup = await request(server.baseURL, `/api/capture/sessions/${sessionID}/messages`, "POST", {
      body: { role: "user", text: "但是这个自动上下文还有冲突？" },
    });
    assert.equal(followup.status, 200, `auto context followup status=${followup.status} body=${followup.text}`);
    const data = envelope(followup).data;
    assert.equal(data.session_context.candidate_tags[0], "autocontext");
    assert.equal(data.session_context.source_links[0], "https://example.com/auto");
    assert.match(data.session_context.candidate_body, /自动上下文/);
    assert.ok(data.session_context.open_questions.length >= 1, "question turn must be tracked");
    assert.ok(data.session_context.conflicts.length >= 1, "conflict turn must be tracked");
    assert.equal(data.session_context.archive_strategy, "new");
  });

  await t.test("archive preview then commit (new strategy) lands a thought", async () => {
    const sessionID = `e2e-archive-${Date.now()}`;
    const start = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { content: "Archive flow e2e" },
      headers: { "X-Session-Id": sessionID },
    });
    assert.equal(start.status, 200);

    const preview = await request(
      server.baseURL,
      `/api/capture/sessions/${sessionID}/archive/preview`,
      "GET"
    );
    assert.equal(preview.status, 200, `preview status=${preview.status} body=${preview.text}`);
    const previewData = envelope(preview).data;
    assert.ok(previewData.preview, "preview response must include preview block");
    assert.equal(previewData.preview.strategy, "new", "fresh session should default to strategy=new");
    assert.ok(typeof previewData.preview.title === "string", "preview.title must be a string");

    // Commit with explicit strategy=new. The LLM is disabled in
    // the e2e harness so the refiner falls back to the local
    // provider which still produces a non-empty thought.
    const commit = await request(server.baseURL, `/api/capture/sessions/${sessionID}/archive`, "POST", {
      body: { strategy: "new", confirmed: true },
    });
    assert.equal(commit.status, 200, `commit status=${commit.status} body=${commit.text}`);
    const commitData = envelope(commit).data;
    assert.ok(commitData.thought && commitData.thought.id, "commit must return thought.id");
    assert.ok(Array.isArray(commitData.jobs), "commit must include jobs array");
  });

  await t.test("update_thought preview exposes diff before confirmed update", async () => {
    const create = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "update source", content: "Original body for update protection." },
    });
    assert.equal(create.status, 202, `create update source status=${create.status} body=${create.text}`);
    const sourceID = envelope(create).data.thought.id;

    const reopen = await request(server.baseURL, `/api/thoughts/${sourceID}/reopen-session`, "POST", {
      body: {},
    });
    assert.equal(reopen.status, 200, `reopen update status=${reopen.status} body=${reopen.text}`);
    const sessionID = envelope(reopen).data.session_id;

    const context = await request(server.baseURL, `/api/capture/sessions/${sessionID}/context`, "POST", {
      body: {
        candidate_title: "update source revised",
        candidate_tags: ["updated", "protected"],
        candidate_body: "Updated body from protected update flow.",
      },
    });
    assert.equal(context.status, 200, `context update status=${context.status} body=${context.text}`);

    const strategy = await request(server.baseURL, `/api/capture/sessions/${sessionID}/strategy`, "POST", {
      body: { strategy: "update_thought", thought_id: sourceID },
    });
    assert.equal(strategy.status, 200, `strategy update status=${strategy.status} body=${strategy.text}`);

    const preview = await request(server.baseURL, `/api/capture/sessions/${sessionID}/archive/preview`, "GET");
    assert.equal(preview.status, 200, `update preview status=${preview.status} body=${preview.text}`);
    const previewData = envelope(preview).data.preview;
    assert.equal(previewData.strategy, "update_thought");
    assert.equal(previewData.thought_id, sourceID);
    assert.ok(previewData.diff, "update preview must include a diff");
    assert.ok(previewData.diff.changed_fields.includes("body"), "diff must include changed body");
    assert.ok(previewData.diff.changed_fields.includes("tags"), "diff must include changed tags");

    const commitDeadline = Date.now() + 5000;
    let commit;
    for (;;) {
      commit = await request(server.baseURL, `/api/capture/sessions/${sessionID}/archive`, "POST", {
        body: { strategy: "update_thought", thought_id: sourceID, confirmed: true },
      });
      if (commit.status !== 409) break;
      if (Date.now() > commitDeadline) break;
      await sleep(100);
    }
    assert.equal(commit.status, 200, `update commit status=${commit.status} body=${commit.text}`);
    assert.equal(envelope(commit).data.thought.id, sourceID, "update strategy must return the original thought id");
  });

  await t.test("reopen-session seeds supplement strategy and commit lands a sibling thought", async () => {
    // Land a real thought first so reopen has a source.
    const create = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "reopen source", content: "Source content for reopen flow." },
    });
    const sourceID = envelope(create).data.thought.id;

    // POST /api/thoughts/{id}/reopen-session → new session id,
    // scratchpad seeded from the thought, default strategy=supplement.
    const reopen = await request(server.baseURL, `/api/thoughts/${sourceID}/reopen-session`, "POST", {
      body: {},
    });
    assert.equal(reopen.status, 200, `reopen status=${reopen.status} body=${reopen.text}`);
    const reopenData = envelope(reopen).data;
    const newSession = reopenData.session_id;
    assert.ok(newSession, "reopen must return a new session_id");
    assert.equal(reopenData.scratchpad.archive_strategy, "supplement", "default strategy must be supplement");
    assert.equal(reopenData.scratchpad.source_thought_id, sourceID, "scratchpad must remember the source thought");
    assert.equal(reopenData.scratchpad.title, "reopen source", "scratchpad.title seeded from source thought");

    // Append a follow-up message and commit; the commit path
    // should land a sibling thought and emit the supplement
    // event so the topic can wire a backlink.
    const followup = await request(server.baseURL, `/api/capture/sessions/${newSession}/messages`, "POST", {
      body: { role: "user", text: "Adding a follow-up angle." },
    });
    assert.equal(followup.status, 200, `followup status=${followup.status} body=${followup.text}`);

    const commit = await request(server.baseURL, `/api/capture/sessions/${newSession}/archive`, "POST", {
      body: { strategy: "supplement", thought_id: sourceID, confirmed: true },
    });
    assert.equal(commit.status, 200, `commit status=${commit.status} body=${commit.text}`);
    const commitData = envelope(commit).data;
    assert.ok(commitData.thought && commitData.thought.id, "supplement commit must return thought.id");
    assert.notEqual(commitData.thought.id, sourceID, "supplement must create a sibling thought, not overwrite");
  });

  await t.test("topic candidates list returns matching unarchived sessions", async () => {
    // Build a topic whose keyword only one of the live sessions
    // will match. Other sessions (created earlier in this e2e run)
    // must not show up as candidates for this topic.
    const create = await request(server.baseURL, "/api/topics", "POST", {
      body: {
        name: "e2e candidate topic",
        description: "topic for session candidate e2e",
        rules: { keywords: { any: ["zooglefloof"] }, auto_weave: false },
      },
    });
    assert.equal(create.status, 201, `topic create status=${create.status} body=${create.text}`);
    const topicID = envelope(create).data.id;

    // Empty candidates for a brand-new topic — no session has
    // hit it yet.
    const empty = await request(server.baseURL, `/api/topics/${topicID}/candidates`, "GET");
    assert.equal(empty.status, 200);
    assert.deepEqual(envelope(empty).data, [], "fresh topic must have empty candidates");

    // Create a session whose only message contains the keyword.
    const sessionID = `e2e-cand-${Date.now()}`;
    const start = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { content: "First turn about zooglefloof indexing" },
      headers: { "X-Session-Id": sessionID },
    });
    assert.equal(start.status, 200);

    // AppendMessage now refreshes SessionContext and publishes the
    // scratchpad.context_updated event automatically. No explicit
    // /context call is needed for topic candidate refresh.
    const followup = await request(server.baseURL, `/api/capture/sessions/${sessionID}/messages`, "POST", {
      body: { role: "user", text: "zooglefloof candidate details #topic" },
    });
    assert.equal(followup.status, 200);

    // The matching job is dispatched on the background routine; a
    // short poll covers the dispatcher latency on a fresh boot.
    const deadline = Date.now() + 4000;
    let candidates = [];
    while (Date.now() < deadline) {
      const list = await request(server.baseURL, `/api/topics/${topicID}/candidates`, "GET");
      assert.equal(list.status, 200);
      candidates = envelope(list).data;
      if (candidates.length > 0) break;
      await sleep(150);
    }
    assert.ok(candidates.length >= 1, "matching session must surface as a candidate");
    const matched = candidates.find((c) => c.session_id === sessionID);
    assert.ok(matched, "candidates list must include our session by id");
    assert.ok(["tag_hint", "keyword", "semantic"].includes(matched.match_type), "match_type must be one of the known kinds");
  });

  await t.test("system privacy report lists surfaces and actions", async () => {
    const res = await request(server.baseURL, "/api/system/privacy", "GET");
    assert.equal(res.status, 200, `privacy status=${res.status} body=${res.text}`);
    const data = envelope(res).data;
    // The wire shape is flat: top-level llm / embedding / reader
    // ExternalSurface fields plus an actions list. We assert each
    // surface is well-formed and the actions list covers the four
    // user-visible triggers documented in the privacy UI.
    assert.ok(data.llm, "report must include llm surface");
    assert.ok(data.embedding, "report must include embedding surface");
    assert.ok(data.reader, "report must include reader surface");
    for (const surface of [data.llm, data.embedding, data.reader]) {
      assert.equal(typeof surface.kind, "string");
      assert.equal(typeof surface.configured, "boolean");
      assert.equal(typeof surface.enabled, "boolean");
      assert.equal(typeof surface.provider, "string");
      assert.ok(typeof surface.base_url === "string" && surface.base_url.length > 0, `${surface.kind} surface must include base_url`);
      assert.ok(typeof surface.hint === "string" && surface.hint.length > 0, `${surface.kind} surface must include a non-empty hint`);
    }
    // The e2e harness has [llm] configured=false (no api_key) and
    // [embedding] enabled=false (also no api_key, since the harness
    // does not set one). The privacy surface builder always reports
    // surfaces as `enabled: true` (the URL is what gets hit, not a
    // feature flag); "configured" is what flips to false when the
    // API key is absent. We assert that semantic here so the test
    // stays stable if the builder ever reads an explicit enabled
    // flag from config.
    assert.equal(data.llm.configured, false, "llm surface must be marked unconfigured when api_key is empty");
    assert.equal(data.embedding.configured, false, "embedding surface must be marked unconfigured when api_key is empty");

    assert.ok(Array.isArray(data.actions), "report must include actions array");
    const actionNames = data.actions.map((a) => a.action);
    for (const expected of ["capture_message", "capture_commit", "url_capture", "topic_refresh"]) {
      assert.ok(actionNames.includes(expected), `actions must include ${expected}`);
    }
    for (const action of data.actions) {
      assert.equal(typeof action.method, "string");
      assert.equal(typeof action.path, "string");
      // action.surfaces is a nil slice when the surfaces it would
      // reference are not configured (e.g. the e2e harness leaves
      // llm / embedding unconfigured); JSON serialises a nil slice
      // to `null` rather than `[]` because the field has no
      // omitempty tag. We accept either so the test still works
      // with partial configurations.
      assert.ok(action.surfaces === null || Array.isArray(action.surfaces), `action ${action.action} surfaces must be an array or null`);
      assert.ok(typeof action.hint === "string" && action.hint.length > 0, `action ${action.action} must include a non-empty hint`);
    }
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
