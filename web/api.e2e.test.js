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

const REPO_ROOT = path.resolve(__dirname, "..");
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

async function startServer(fixture = null, options = {}) {
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
    ...(options.llmBaseURL ? [`base_url = ${JSON.stringify(options.llmBaseURL)}`,'api_key = "test-only-local-gateway"','chat_model = "fixture-model"','timeout_seconds = 60'] : ["configured = false"]),
    "[search]",
    'duckdb_path = "thoughtflow.duckdb"',
    'default_mode = "hybrid"',
    "[document_profiles]",
    `custom_dir = ${JSON.stringify(path.join(configDir, "document-formats"))}`,
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


async function generateArchivePreview(baseURL, endpoint, body = {}) {
  const deadline = Date.now() + 10000;
  for (;;) {
    let response = await request(baseURL, endpoint, "POST", {body});
    assert.ok([200,202].includes(response.status), response.text);
    const operationID = envelope(response).data.operation?.id;
    while (["preparing","saving"].includes(envelope(response).data.operation?.state) && Date.now() < deadline) {
      await sleep(30);
      response = await request(baseURL, endpoint, "GET");
      assert.equal(envelope(response).data.operation?.id, operationID, "polling must not start another operation");
    }
    const data = envelope(response).data;
    assert.ok(!["preparing","saving"].includes(data.operation?.state), response.text);
    const superseded = data.operation?.input_generation !== data.scratchpad?.input_generation;
    if (Date.now() < deadline && (superseded || data.operation?.state === "conflicted")) {
      // Background enrichment/refinement can invalidate a just-requested preview.
      // Retry the explicit generation command, while GET remains read-only.
      await sleep(50);
      continue;
    }
    assert.ok(data.preview || data.operation?.state === "saved", response.text);
    return response;
  }
}

function envelope(res) {
  assert.ok(res.json, `expected JSON body, got status ${res.status} text=${res.text}`);
  assert.equal(typeof res.json.request_id, "string", "envelope.request_id must be a string");
  assert.ok("data" in res.json, "envelope must have data field");
  assert.ok("error" in res.json, "envelope must have error field");
  return res.json;
}

async function waitForSearchHit(baseURL, path, title, timeoutMs = 8000) {
  const deadline = Date.now() + timeoutMs;
  let lastTitles = [];
  while (Date.now() < deadline) {
    const res = await request(baseURL, path, "GET");
    assert.equal(res.status, 200, `search status=${res.status} body=${res.text}`);
    const data = envelope(res).data;
    assert.ok(Array.isArray(data.results), "results must be an array");
    lastTitles = data.results.map((r) => r.title);
    const hit = data.results.find((r) => r.title === title);
    if (hit) {
      return { res, data, hit };
    }
    await sleep(150);
  }
  assert.fail(`search should surface ${title}; got titles=${lastTitles.join(",")}`);
}

// Follow the archive contract: a missing or stale preview requires a new
// preview; lock conflicts only require waiting. Other conflicts must surface.
async function confirmArchive(baseURL, sessionID, body) {
  const deadline = Date.now() + 5000;
  const archivePath = `/api/capture/sessions/${encodeURIComponent(sessionID)}/archive`;
  let refreshedPreview;
  for (;;) {
    const response = await request(baseURL, archivePath, "POST", { body });
    if (response.status !== 409 || Date.now() >= deadline) return { ...response, refreshedPreview };
    const code = envelope(response).error?.code;
    if (code === "thoughtflow.archive.preview_required" || code === "thoughtflow.archive.preview_stale") {
      const preview = await generateArchivePreview(baseURL, `${archivePath}/preview`);
      assert.equal(preview.status, 200, `refresh preview status=${preview.status} body=${preview.text}`);
      refreshedPreview = envelope(preview).data.preview;
      assert.equal(refreshedPreview.validation?.status, "valid", preview.text);
    } else if (code !== "thoughtflow.capture.refining" && code !== "thoughtflow.capture.locked") {
      return { ...response, refreshedPreview };
    }
    await sleep(100);
  }
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

  await t.test("Markdown import preserves all bytes and never adopts source identity", async () => {
    const original = "\ufeff---\r\ntitle: Imported e2e\r\nid: malicious-source-id\r\npath: ../../escape.md\r\ncustom: [preserve, everything]\r\n---\r\n# Source\r\n\r\n## AI Notes\r\n\r\n```md\r\n## Original\r\n```\r\n![local](./image.png)\r\n\r\n";
    const boundary = "thoughtflow-e2e-import-boundary";
    const raw = `--${boundary}\r\nContent-Disposition: form-data; name="file"; filename="import.md"\r\nContent-Type: text/markdown\r\n\r\n${original}\r\n--${boundary}--\r\n`;
    const result = await request(server.baseURL, "/api/thoughts/import", "POST", { body: raw, headers: { "Content-Type": `multipart/form-data; boundary=${boundary}` } });
    assert.equal(result.status, 201, result.text);
    const thought = envelope(result).data.thought;
    assert.notEqual(thought.id, "malicious-source-id");
    assert.equal(thought.user_title, "Imported e2e");
    assert.ok(thought.path.startsWith("thoughts/"));
    const saved = await request(server.baseURL, `/api/thoughts/${thought.id}`, "GET");
    assert.equal(envelope(saved).data.content.original, original);
    const source = await request(server.baseURL, `/api/thoughts/${thought.id}/import-source`, "GET");
    assert.equal(source.status, 200, source.text);
    assert.equal(source.text, original);
    const document = await request(server.baseURL, `/api/thoughts/${thought.id}/markdown`, "GET");
    assert.equal(document.status, 200, document.text);
    assert.ok(document.text.includes(original));
    assert.ok(fs.readFileSync(path.join(server.contentDir, thought.path), "utf8").includes(original));
    // Keep the list-order fixture below independent of this import.
    const deleteDeadline = Date.now() + 5000;
    let removed;
    do {
      removed = await request(server.baseURL, `/api/thoughts/${thought.id}`, "DELETE", { headers: { "X-Session-Id": "import-cleanup" } });
      if (removed.status !== 409) break;
      await sleep(100);
    } while (Date.now() < deleteDeadline);
    assert.ok([200, 204].includes(removed.status), removed.text);
    const missing = await request(server.baseURL, `/api/thoughts/${thought.id}/import-source`, "GET");
    assert.equal(missing.status, 404);
  });

  await t.test("thoughts list returns workspace notes in recent-first order", async () => {
    const first = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "list-first", content: "first list note" },
    });
    assert.equal(first.status, 202, `create(first) status=${first.status} body=${first.text}`);
    const firstID = envelope(first).data.thought.id;
    await sleep(25);
    const second = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "list-second", content: "second list note" },
    });
    assert.equal(second.status, 202, `create(second) status=${second.status} body=${second.text}`);
    const secondID = envelope(second).data.thought.id;

    const listed = await request(server.baseURL, "/api/thoughts", "GET");
    assert.equal(listed.status, 200, `list status=${listed.status} body=${listed.text}`);
    const body = envelope(listed).data;
    assert.ok(Array.isArray(body), `expected array, got ${typeof body}`);
    const firstIndex = body.findIndex((thought) => thought.id === firstID);
    const secondIndex = body.findIndex((thought) => thought.id === secondID);
    assert.ok(firstIndex >= 0, `missing first thought in list: ${JSON.stringify(body).slice(0, 400)}`);
    assert.ok(secondIndex >= 0, `missing second thought in list: ${JSON.stringify(body).slice(0, 400)}`);
    // Refine/index jobs can update the first note after the second capture.
    // The list contract orders by updated_at, rather than capture order.
    for (let index = 1; index < body.length; index += 1) {
      assert.ok(
        Date.parse(body[index - 1].updated_at) >= Date.parse(body[index].updated_at),
        `expected recent updates first: ${JSON.stringify(body.map((thought) => ({ id: thought.id, updated_at: thought.updated_at })))}`
      );
    }
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

  await t.test("HTTP search remains available when unsupported mode parameters are supplied", async () => {
    await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "search-target", content: "alpha beta gamma DuckDB keyword search test" },
    });
    for (const mode of ["keyword", "semantic", "hybrid"]) {
      const { hit } = await waitForSearchHit(
        server.baseURL,
        `/api/search?q=DuckDB&mode=${mode}&limit=5&explain=true`,
        "search-target"
      );
      assert.equal(typeof hit.score, "undefined", "score is intentionally hidden from the Web search projection");
    }
  });

  await t.test("search filters by tag and returns SearchResultView shape", async () => {
    // The Web-facing search surface (per convergence todo 6.1) only emits
    // q / tags / topic_id; assertions here pin the API contract to that surface and
    // verify SearchResultView projects results without an explain block.
    await request(server.baseURL, "/api/thoughts", "POST", {
      body: {
        type: "text",
        title: "search-tagged",
        content: "vector store retrieval",
        tags: ["rag"],
      },
    });

    // tags=rag should surface the freshly seeded note.
    const { data: tagData } = await waitForSearchHit(
      server.baseURL,
      `/api/search?q=vector&tags=rag&limit=5`,
      "search-tagged"
    );
    // SearchResultView does not expose an `explain` field; the legacy
    // /api/search?explain=true still works server-side but the projection is
    // a flat results array.
    assert.equal(typeof tagData.explain, "undefined", "explain field is not part of the projection");
    const filtered = await request(server.baseURL, "/api/search?q=vector&topic_id=missing-topic&limit=5", "GET");
    assert.equal(filtered.status, 200, filtered.text);
    assert.equal(envelope(filtered).data.results.length, 0, "HTTP topic_id must reach exact membership filtering");
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

  await t.test("topic creation and refresh synchronize exact search membership", async () => {
    const seeds = [
      { title: "topic-created-member", content: "topicindexalpha unique existing member" },
      { title: "topic-refreshed-member", content: "topicindexbeta unique refreshed member" },
    ];
    const ids = [];
    const paths = [];
    for (const seed of seeds) {
      const created = await request(server.baseURL, "/api/thoughts", "POST", { body: { type: "text", ...seed } });
      assert.equal(created.status, 202, created.text);
      ids.push(envelope(created).data.thought.id);
      paths.push(path.join(server.contentDir, envelope(created).data.thought.path));
      await waitForSearchHit(server.baseURL, `/api/search?q=${seed.content.split(" ")[0]}`, seed.title);
    }
    const created = await request(server.baseURL, "/api/topics", "POST", {
      body: { name: "Exact topic index", rules: { keywords: { any: ["topicindexalpha"] } }, auto_weave: true },
    });
    assert.equal(created.status, 201, created.text);
    const topic = envelope(created).data;
    assert.ok(topic.members.includes(ids[0]), created.text);
    await waitForSearchHit(server.baseURL, `/api/search?q=topicindexalpha&topic_id=${topic.id}`, seeds[0].title);

    // External Markdown edits emit no thought events. Only the background
    // topic refresh below can repair membership metadata in the search index.
    await fsp.appendFile(paths[1], "\n\ntopicindexalpha\n");
    await fsp.rm(paths[0]);
    const refreshed = await request(server.baseURL, `/api/topics/${topic.id}/refresh`, "POST", {});
    assert.equal(refreshed.status, 202, refreshed.text);
    await waitForSearchHit(server.baseURL, `/api/search?q=topicindexbeta&topic_id=${topic.id}`, seeds[1].title);
    // The new member now contains alpha too, so a late content index can
    // legitimately return it. Pin the removed identity, not an empty result
    // set, and allow the asynchronous membership update to finish.
    const deadline = Date.now() + 8000;
    let results;
    do {
      const old = await request(server.baseURL, `/api/search?q=topicindexalpha&topic_id=${topic.id}`, "GET");
      assert.equal(old.status, 200, old.text);
      results = envelope(old).data.results;
      assert.ok(results.every((result) => ids.includes(result.thought_id)), old.text);
      if (!results.some((result) => result.thought_id === ids[0])) break;
      await sleep(150);
    } while (Date.now() < deadline);
    assert.ok(!results.some((result) => result.thought_id === ids[0]), `refresh must remove obsolete search membership: ${JSON.stringify(results)}`);
  });

  await t.test("document profile republishing returns a conflict without overwriting", async () => {
    const content = fs.readFileSync(path.join(REPO_ROOT, "assets/documentformats/note-v1.md"), "utf8").replace("builtin.note", "custom.e2e-profile");
    const first = await request(server.baseURL, "/api/document-profiles/publish", "POST", { body: { content } });
    assert.equal(first.status, 201, first.text);
    const duplicate = await request(server.baseURL, "/api/document-profiles/publish", "POST", {
      body: { content: content.replace("通用笔记", "Changed profile title") },
    });
    assert.equal(duplicate.status, 409, duplicate.text);
    assert.equal(envelope(duplicate).error.code, "thoughtflow.profile.conflict");
    const saved = await request(server.baseURL, "/api/document-profiles/custom.e2e-profile?version=1", "GET");
    assert.equal(saved.status, 200, saved.text);
    assert.deepEqual(envelope(saved).data.ref, envelope(first).data.ref);
    assert.equal(envelope(saved).data.name, envelope(first).data.name);
    const invalid = await request(server.baseURL, "/api/document-profiles/publish", "POST", { body: { content: "invalid" } });
    assert.equal(invalid.status, 422, invalid.text);
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

    const basket = await request(server.baseURL, "/api/compose/sources", "GET");
    const queue = await request(server.baseURL, "/api/compose/sources", "PUT", {
      headers: { "If-Match": String(envelope(basket).data.version) },
      body: {
        sources: [
          { source_type: "thought", source_id: thoughtId },
          { source_type: "search_result", source_id: "keep-for-next", title: "Keep for next draft" },
        ],
      },
    });
    assert.equal(queue.status, 200, `compose source queue status=${queue.status} body=${queue.text}`);

    const create = await request(server.baseURL, "/api/compose/drafts", "POST", {
      body: {
        sources: [{ source_type: "thought", source_id: thoughtId }],
        selected_thought_ids: [thoughtId],
        goal: "compose from e2e",
        format: "summary",
      },
    });
    // Async generate returns a queued job (202) with resource_id = draft id.
    assert.equal(create.status, 202, `compose create status=${create.status} body=${create.text}`);
    const job = envelope(create).data;
    assert.ok(job && job.id, `compose job missing id: ${create.text}`);
    const draftId = job.resource_id || job.id;
    assert.ok(draftId);

    // Wait for the background generate to materialise draft content.
    let ready = null;
    for (let i = 0; i < 50; i++) {
      const get = await request(server.baseURL, `/api/compose/drafts/${draftId}`, "GET");
      if (get.status === 200) {
        const draft = envelope(get).data;
        if (draft && draft.status === "draft" && draft.content) {
          ready = draft;
          break;
        }
        if (draft && draft.status === "failed") {
          assert.fail(`compose draft failed: ${get.text}`);
        }
      }
      await sleep(50);
    }
    assert.ok(ready, "compose draft did not become ready in time");

    let queueAfterGenerate = null;
    for (let i = 0; i < 50; i++) {
      const response = await request(server.baseURL, "/api/compose/sources", "GET");
      if (response.status === 200 && envelope(response).data.sources.map((source) => source.source_id).join(",") === "keep-for-next") {
        queueAfterGenerate = response;
        break;
      }
      await sleep(50);
    }
    assert.ok(queueAfterGenerate, "generated sources were not cleared from the queue");

    // A second create with the same sources/goal should reuse the in-flight
    // or just-finished fingerprint path without erroring.
    const createAgain = await request(server.baseURL, "/api/compose/drafts", "POST", {
      body: {
        sources: [{ source_type: "thought", source_id: thoughtId }],
        selected_thought_ids: [thoughtId],
        goal: "compose from e2e",
        format: "summary",
      },
    });
    assert.ok([200, 202].includes(createAgain.status), `second compose create status=${createAgain.status}`);

    const save = await request(server.baseURL, `/api/compose/drafts/${draftId}/save`, "POST", {
      body: { content: "E2E saved compose", title: "compose from e2e" },
    });
    assert.ok([200, 202, 400].includes(save.status), `compose save status=${save.status}`);

    // Create another draft and delete it without saving.
    const createDel = await request(server.baseURL, "/api/compose/drafts", "POST", {
      body: {
        sources: [{ source_type: "thought", source_id: thoughtId }],
        goal: "compose to delete",
        format: "summary",
      },
    });
    assert.equal(createDel.status, 202, `compose create-for-delete status=${createDel.status}`);
    const delJob = envelope(createDel).data;
    const delDraftId = delJob.resource_id || delJob.id;
    let delReady = false;
    for (let i = 0; i < 50; i++) {
      const get = await request(server.baseURL, `/api/compose/drafts/${delDraftId}`, "GET");
      if (get.status === 200) {
        const draft = envelope(get).data;
        if (draft && (draft.status === "draft" || draft.status === "failed" || draft.status === "generating")) {
          delReady = true;
          break;
        }
      }
      await sleep(50);
    }
    assert.ok(delReady, "draft for delete never appeared");
    const del = await request(server.baseURL, `/api/compose/drafts/${delDraftId}`, "DELETE");
    assert.equal(del.status, 200, `compose delete status=${del.status} body=${del.text}`);
    const gone = await request(server.baseURL, `/api/compose/drafts/${delDraftId}`, "GET");
    assert.ok([404, 500].includes(gone.status), `get after delete status=${gone.status}`);
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
      assert.match(started.content, /Restart recovery/);
      assert.ok(Array.isArray(started.messages), "scratchpad must include messages");
      assert.match(started.messages[0]?.text || "", /Restart recovery/);
      assert.equal(typeof started.session_context, "object", "session_context must be present even before LLM enrichment");
      assert.equal(started.session_context.candidate_body || "", "", "LLM-disabled fixture must not synthesize candidate_body");

      await first.stop();
      first = null;
      second = await startServer(fixture);
      const active = await request(second.baseURL, "/api/capture/sessions/active", "GET");
      assert.equal(active.status, 200, `active after restart status=${active.status} body=${active.text}`);
      const data = envelope(active).data;
      assert.equal(data.session_id, sessionID, "restart must restore the last unarchived session");
      assert.match(data.content, /Restart recovery/);
      assert.match(data.messages[0]?.text || "", /Restart recovery/);
      assert.match(data.session_context.candidate_body || data.content, /Restart recovery/);

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
    assert.match(data.content, /Auto context seed/);
    assert.match(data.content, /自动上下文/);
    const userMessages = data.messages.filter((msg) => msg.role === "user").map((msg) => msg.text || "");
    assert.ok(userMessages.some((text) => /Auto context seed/.test(text)), "seed turn must be persisted in message history");
    assert.ok(userMessages.some((text) => /自动上下文/.test(text)), "follow-up turn must be persisted in message history");
    assert.equal(typeof data.session_context, "object", "session_context must remain present");
    assert.match(data.session_context.candidate_body || data.content, /自动上下文/);
    assert.equal(data.archive_strategy, "new");
  });

  await t.test("archive preview then commit (new strategy) lands a thought", async () => {
    const sessionID = `e2e-archive-${Date.now()}`;
    const start = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { content: "Archive flow e2e" },
      headers: { "X-Session-Id": sessionID },
    });
    assert.equal(start.status, 200);

    const preview = await generateArchivePreview(server.baseURL, `/api/capture/sessions/${sessionID}/archive/preview`);
    assert.equal(preview.status, 200, `preview status=${preview.status} body=${preview.text}`);
    const previewData = envelope(preview).data;
    assert.ok(previewData.preview, "preview response must include preview block");
    assert.equal(previewData.preview.strategy, "new", "fresh session should default to strategy=new");
    assert.ok(typeof previewData.preview.title === "string", "preview.title must be a string");
    assert.equal(
      previewData.preview.validation?.status,
      "valid",
      `new preview validation=${JSON.stringify(previewData.preview.validation)}`
    );

    // Commit with explicit strategy=new. The LLM is disabled in
    // the e2e harness so the refiner falls back to the local
    // provider which still produces a non-empty thought.
    const commit = await confirmArchive(server.baseURL, sessionID, {
      strategy: "new", confirmed: true,
    });
    assert.equal(commit.status, 200, `commit status=${commit.status} body=${commit.text}`);
    const commitData = envelope(commit).data;
    assert.ok(commitData.thought && commitData.thought.id, "commit must return thought.id");
    assert.ok(Array.isArray(commitData.jobs), "commit must include jobs array");
  });

  await t.test("update_thought preview exposes the full document before confirmed update", async () => {
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

    const preview = await generateArchivePreview(server.baseURL, `/api/capture/sessions/${sessionID}/archive/preview`);
    assert.equal(preview.status, 200, `update preview status=${preview.status} body=${preview.text}`);
    const previewData = envelope(preview).data.preview;
    assert.equal(previewData.strategy, "update_thought");
    assert.equal(previewData.thought_id, sourceID);
    assert.equal(
      previewData.validation?.status,
      "valid",
      `update preview validation=${JSON.stringify(previewData.validation)}`
    );
    assert.ok(previewData.diff, "update preview must include a diff");
    assert.match(previewData.diff.before, /Original body for update protection\./);
    assert.match(previewData.diff.after, /Updated body from protected update flow\./);
    assert.deepEqual(previewData.diff.changed_fields, ["title", "tags", "body"]);
    assert.match(previewData.body, /Original body for update protection\./, "preview must retain the archived body");
    assert.match(previewData.body, /Updated body from protected update flow\./, "preview must include the supplement");

    const commit = await confirmArchive(server.baseURL, sessionID, {
      strategy: "update_thought", thought_id: sourceID, confirmed: true,
    });
    assert.equal(commit.status, 200, `update commit status=${commit.status} body=${commit.text}`);
    assert.equal(envelope(commit).data.thought.id, sourceID, "update strategy must return the original thought id");
  });

  await t.test("reopen-session defaults to updating the source thought", async () => {
    // Land a real thought first so reopen has a source.
    const create = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "reopen source", content: "Source content for reopen flow." },
    });
    const sourceID = envelope(create).data.thought.id;

    // POST /api/thoughts/{id}/reopen-session → new session id,
    // scratchpad seeded from the thought, default strategy=update_thought.
    const reopen = await request(server.baseURL, `/api/thoughts/${sourceID}/reopen-session`, "POST", {
      body: {},
    });
    assert.equal(reopen.status, 200, `reopen status=${reopen.status} body=${reopen.text}`);
    const reopenData = envelope(reopen).data;
    const newSession = reopenData.session_id;
    assert.ok(newSession, "reopen must return a new session_id");
    assert.equal(reopenData.scratchpad.archive_strategy, "update_thought", "default strategy must update the source thought");
    assert.equal(reopenData.scratchpad.source_thought_id, sourceID, "scratchpad must remember the source thought");
    assert.equal(reopenData.scratchpad.title, "reopen source", "scratchpad.title seeded from source thought");

    // Append a follow-up message, preview it, and commit; the default
    // commit path should patch the source thought instead of creating
    // a sibling.
    const followup = await request(server.baseURL, `/api/capture/sessions/${newSession}/messages`, "POST", {
      body: { role: "user", text: "Adding a follow-up angle." },
    });
    assert.equal(followup.status, 200, `followup status=${followup.status} body=${followup.text}`);

    const preview = await generateArchivePreview(server.baseURL, `/api/capture/sessions/${newSession}/archive/preview`);
    assert.equal(preview.status, 200, `reopen preview status=${preview.status} body=${preview.text}`);
    const previewData = envelope(preview).data.preview;
    assert.equal(previewData.strategy, "update_thought");
    assert.equal(previewData.thought_id, sourceID);
    assert.ok(previewData.diff, "reopen update preview must include a diff");

    const commit = await confirmArchive(server.baseURL, newSession, {
      strategy: "update_thought", thought_id: sourceID, confirmed: true,
    });
    assert.equal(commit.status, 200, `commit status=${commit.status} body=${commit.text}`);
    const commitData = envelope(commit).data;
    assert.ok(commitData.thought && commitData.thought.id, "update commit must return thought.id");
    assert.equal(commitData.thought.id, sourceID, "update strategy must save back to the source thought");
  });

  await t.test("archive re-previews after a follow-up invalidates the previous preview", async () => {
    const sessionID = `e2e-preview-required-${Date.now()}`;
    const start = await request(server.baseURL, "/api/capture/sessions", "POST", {
      body: { content: "Initial archive content." },
      headers: { "X-Session-Id": sessionID },
    });
    assert.equal(start.status, 200, start.text);
    const preview = await generateArchivePreview(server.baseURL, `/api/capture/sessions/${sessionID}/archive/preview`);
    assert.equal(preview.status, 200, preview.text);
    assert.equal(envelope(preview).data.preview.validation?.status, "valid", preview.text);

    // A user turn clears the persisted preview deterministically, covering the
    // same API response observed during the macOS background-enrichment race.
    const fact = "The confirmed budget is 314159 dollars.";
    const followup = await request(server.baseURL, `/api/capture/sessions/${sessionID}/messages`, "POST", {
      body: { role: "user", text: fact },
    });
    assert.equal(followup.status, 200, followup.text);
    const body = { strategy: "new", confirmed: true };
    const rejected = await request(server.baseURL, `/api/capture/sessions/${sessionID}/archive`, "POST", { body });
    assert.equal(rejected.status, 409, rejected.text);
    assert.equal(envelope(rejected).error?.code, "thoughtflow.archive.preview_required", rejected.text);

    const commit = await confirmArchive(server.baseURL, sessionID, body);
    assert.equal(commit.status, 200, commit.text);
    const thoughtID = envelope(commit).data.thought.id;
    const saved = await request(server.baseURL, `/api/thoughts/${thoughtID}`, "GET");
    assert.equal(saved.status, 200, saved.text);
    assert.ok(commit.refreshedPreview, "confirmation must regenerate the missing preview");
    assert.notEqual(commit.refreshedPreview.context_hash, envelope(preview).data.preview.context_hash);
    assert.equal(envelope(saved).data.content.ai_notes, commit.refreshedPreview.body.replace(/\n*$/, "\n"),
      "archive must persist the regenerated, confirmed body exactly");
  });

  await t.test("archive GET is read-only; private draft survives reload and archives with one ID", async () => {
    const sessionID = `e2e-private-draft-${Date.now()}`;
    await request(server.baseURL,"/api/capture/sessions","POST",{body:{content:"Private draft material"},headers:{"X-Session-Id":sessionID}});
    const endpoint = `/api/capture/sessions/${sessionID}/archive/preview`;
    const before = await request(server.baseURL,endpoint,"GET");
    assert.equal(envelope(before).data.preview,null);
    assert.equal(envelope(before).data.operation,null);
    const preview = await generateArchivePreview(server.baseURL,endpoint);
    const stored = await request(server.baseURL,endpoint,"GET");
    assert.deepEqual(envelope(stored).data.preview,envelope(preview).data.preview);
    const draft = await request(server.baseURL,`/api/capture/sessions/${sessionID}/archive/draft`,"POST");
    assert.equal(draft.status,200,draft.text);
    const id = envelope(draft).data.draft_thought_id;
    const detail = envelope(await request(server.baseURL,`/api/capture/sessions/${sessionID}`,"GET")).data;
    assert.equal(detail.draft_thought_id,id);
    assert.ok(!detail.committed_thought_id);
    assert.ok(detail.messages.length);
    const privateRead = await request(server.baseURL,`/p/thoughts/${id}`,"GET");
    assert.equal(privateRead.status,404,privateRead.text);
    const commit = await confirmArchive(server.baseURL,sessionID,{strategy:"new",confirmed:true});
    assert.equal(commit.status,200,commit.text);
    assert.equal(envelope(commit).data.thought.id,id);
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

  await t.test("delete thought removes the note", async () => {
    const create = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "delete me", content: "invalid temporary note" },
    });
    assert.equal(create.status, 202, `create delete fixture status=${create.status} body=${create.text}`);
    const thoughtID = envelope(create).data.thought.id;
    const deleteDeadline = Date.now() + 5000;
    let deleted;
    for (;;) {
      deleted = await request(server.baseURL, `/api/thoughts/${thoughtID}`, "DELETE", {
        headers: { "X-Session-Id": "e2e-delete" },
      });
      if (deleted.status !== 409 || Date.now() > deleteDeadline) break;
      await sleep(100);
    }
    assert.equal(deleted.status, 200, `delete status=${deleted.status} body=${deleted.text}`);
    assert.equal(envelope(deleted).data.thought_id, thoughtID);
    const get = await request(server.baseURL, `/api/thoughts/${thoughtID}`, "GET");
    assert.equal(get.status, 404, `deleted thought get status=${get.status} body=${get.text}`);
  });


  await t.test("encoded rendered links block validation and cannot be confirmed into final documents", async () => {
    const profiles = await request(server.baseURL, "/api/document-profiles", "GET");
    const profile = envelope(profiles).data.profiles.find((p) => p.ref.family === "design");
    const parameters = Object.fromEntries(profile.inputs.map((input) => [input.key, "confirmed input"]));
    const sections = profile.sections.map((section) => `## ${profile.validation.section_headings[section.key]}\n\nG1 D1 T1: failure recovery. Evidence [S1].`).join("\n\n");
    const body = `# Link validation\n\n${sections}\n\n## 参考资料\n\n- [S1] Source: https://example.org/evidence\n`;
    const created = await request(server.baseURL, "/api/thoughts", "POST", {
      body: { type: "text", title: "Link validation", content: body, document_profile: profile.ref, document_parameters: parameters },
    });
    assert.equal(created.status, 202, created.text);
    const base = `/api/documents/${envelope(created).data.thought.id}`;
    const fixtures = JSON.parse(fs.readFileSync(path.join(REPO_ROOT, "internal/pkg/documentvalidation/testdata/rendered-links.json"), "utf8"));
    for (const tc of fixtures) {
      const current = await request(server.baseURL, base, "GET");
      assert.equal(current.status, 200, current.text);
      const edited = await request(server.baseURL, base + "/revisions", "POST", {
        headers: { "If-Match": String(envelope(current).data.head.generation) },
        body: { body: body + `\n![diagram](${tc.locator})\n` },
      });
      assert.equal(edited.status, 201, edited.text);
      const snapshot = envelope(edited).data;
      const headers = { "If-Match": String(snapshot.head.generation) };
      const validated = await request(server.baseURL, base + "/validate", "POST", {
        headers, body: { revision_id: snapshot.head.working_revision_id, review_mode: "manual" },
      });
      assert.equal(validated.status, 200, validated.text);
      const report = envelope(validated).data;
      assert.equal(report.issues.some((issue) => issue.code === "unsafe_link"), tc.unsafe, `${tc.name}: ${validated.text}`);
      assert.ok(!validated.text.includes("SECRET"), "validation diagnostics must redact credentials");
      if (!tc.unsafe) continue;
      const finalized = await request(server.baseURL, base + "/finalize", "POST", {
        headers,
        body: { revision_id: report.revision_id, report_id: report.report_id, operation_id: tc.name, delivery_level: "proposal",
          confirmations: report.issues.map((issue) => ({ issue_id: issue.id, reason: "Attempt to confirm all issues." })) },
      });
      assert.equal(finalized.status, 422, finalized.text);
      const exported = await request(server.baseURL, `${base}/revisions/${report.revision_id}/export?mode=final`, "GET");
      assert.equal(exported.status, 422, exported.text);
    }
  });

  await t.test("typed documents freeze edits, manual reviews, final versions and exact exports", async () => {
    const profilesRes=await request(server.baseURL,"/api/document-profiles","GET");
    const profiles=envelope(profilesRes).data.profiles;
    for(const family of ["design","research","article"]) {
      const profile=profiles.find(p=>p.ref.family===family);
      assert.equal(profile.ref.version,2);
      const parameters=Object.fromEntries(profile.inputs.map(input=>[input.key,"confirmed input"]));parameters.subtype="opinion";
      const sections=profile.sections.map(section=>`## ${profile.validation.section_headings[section.key]}\n\nG1 D1 T1 Q1 F1 C1: failure and recovery. A limitation and counterexample supported by [S1].`).join("\n\n");
      const body=`# Typed ${family}\n\n${sections}\n\n## 参考资料\n\n- [S1] Source: https://example.org/evidence\n`;
      const created=await request(server.baseURL,"/api/thoughts","POST",{body:{type:"text",title:`Typed ${family}`,content:body,document_profile:profile.ref,document_parameters:parameters}});
      assert.equal(created.status,202,created.text);const id=envelope(created).data.thought.id;const base=`/api/documents/${id}`;
      let response=await request(server.baseURL,base,"GET");assert.equal(response.status,200,response.text);const snapshot=envelope(response).data;
      response=await request(server.baseURL,base+"/validate","POST",{body:{revision_id:snapshot.head.working_revision_id}});assert.equal(response.status,428);
      response=await request(server.baseURL,base+"/validate","POST",{headers:{"If-Match":String(snapshot.head.generation)},body:{revision_id:snapshot.head.working_revision_id,review_mode:"manual"}});
      assert.equal(response.status,200,response.text);const report=envelope(response).data;assert.equal(report.status,"needs_review");
      const finalize={revision_id:report.revision_id,report_id:report.report_id,operation_id:`final-${family}`,delivery_level:"proposal",confirmations:report.issues.filter(issue=>issue.human_confirmation).map(issue=>({issue_id:issue.id,reason:"Inspected this exact revision and the original evidence."}))};
      response=await request(server.baseURL,base+"/finalize","POST",{headers:{"If-Match":String(snapshot.head.generation)},body:finalize});assert.equal(response.status,200,response.text);
      response=await request(server.baseURL,`${base}/revisions/${report.revision_id}/export?mode=final`,"GET");assert.equal(response.status,200,response.text);assert.equal(response.text,snapshot.body);assert.equal(response.headers["x-document-mode"],"final");
      response=await request(server.baseURL,base,"GET");const finalized=envelope(response).data;
      response=await request(server.baseURL,base+"/revisions","POST",{headers:{"If-Match":String(finalized.head.generation)},body:{body:"# Incomplete draft\n\nTODO\n"}});assert.equal(response.status,201,response.text);const next=envelope(response).data;
      response=await request(server.baseURL,base+"/finalize","POST",{headers:{"If-Match":String(next.head.generation)},body:{...finalize,operation_id:`stale-${family}`,revision_id:next.head.working_revision_id}});assert.equal(response.status,409,response.text);
      response=await request(server.baseURL,`${base}/revisions/${next.head.working_revision_id}/export?mode=final`,"GET");assert.equal(response.status,422);
      response=await request(server.baseURL,`${base}/revisions/${report.revision_id}/export?mode=final`,"GET");assert.equal(response.text,snapshot.body);
      let deleted;
      const deadline=Date.now()+5000;
      do {
        deleted=await request(server.baseURL,`/api/thoughts/${id}`,"DELETE",{headers:{"X-Session-Id":"typed-delete"}});
        if(deleted.status!==409)break;
        await sleep(100);
      } while(Date.now()<deadline);
      assert.equal(deleted.status,200,deleted.text);
      for(const path of [base,`${base}/revisions/${report.revision_id}/export?mode=final`,`${base}/revisions/${next.head.working_revision_id}/export?mode=draft`]) {
        response=await request(server.baseURL,path,"GET");assert.equal(response.status,404,response.text);
      }
      response=await request(server.baseURL,base+"/finalize","POST",{headers:{"If-Match":String(snapshot.head.generation)},body:finalize});assert.equal(response.status,404,response.text);
    }
  });

  await t.test("collection catalog owns independent documents with CAS, migration, resource and scope boundaries",async()=>{
    let response=await request(server.baseURL,"/api/topics","POST",{body:{operation_id:"e2e-collection-create",mode:"collection",name:"Collection E2E",search_policy:{topic_global_visible:true,owned_thoughts_global_default:false}}});assert.equal(response.status,201,response.text);const topic=envelope(response).data;const base=`/api/topics/${topic.id}`;
    response=await request(server.baseURL,base+"/catalog","GET");assert.equal(response.status,200,response.text);let catalog=envelope(response).data.catalog;
    const edit={operation_id:"e2e-planned",expected_generation:catalog.generation,groups:catalog.groups,entries:[{id:"planned",group_id:catalog.groups[0].id,title:"Owned design",required:true,role:"design",binding:{kind:"planned"}}]};response=await request(server.baseURL,base+"/catalog","PUT",{body:edit});assert.equal(response.status,200,response.text);catalog=envelope(response).data.catalog;
    response=await request(server.baseURL,base+"/catalog","PUT",{body:{...edit,operation_id:"e2e-stale"}});assert.equal(response.status,409,response.text);
    const profiles=envelope(await request(server.baseURL,"/api/document-profiles","GET")).data.profiles;const profile=profiles.find(p=>p.ref.profile_id==="builtin.design-doc");assert.ok(profile);
    const body={operation_id:"e2e-owned-draft",expected_generation:catalog.generation,profile:profile.ref,parameters:{},content:"# Owned design\n\nIndependent content for a collection."};for(let i=0;i<2;i++){response=await request(server.baseURL,base+"/entries/planned/drafts","POST",{body});assert.equal(response.status,200,response.text);}
    const data=envelope(response).data;const id=data.catalog.entries[0].binding.thought_id;assert.ok(id);response=await request(server.baseURL,`/api/thoughts/${id}/placement`,"GET");assert.equal(response.status,200,response.text);let placement=envelope(response).data;assert.equal(placement.owner_topic_id,topic.id);assert.match(placement.placement.thought_path,new RegExp(`^topics/${topic.id}/thoughts/`));
    response=await request(server.baseURL,base+"/entries/planned/content?view=final","GET");assert.equal(response.status,422,response.text);response=await request(server.baseURL,base+"/entries/planned/content?view=draft","GET");assert.equal(response.status,200,response.text);assert.match(envelope(response).data.body,/Independent content/);
    let workingHit;
    for(let attempt=0;attempt<80;attempt++){
      const scoped=await request(server.baseURL,`/api/search?scope=topic&topic_id=${topic.id}&q=Independent`,"GET");assert.equal(scoped.status,200,scoped.text);
      workingHit=envelope(scoped).data.results.find(hit=>hit.thought_id===id);
      if(workingHit?.indexed_revision_id)break;
      await new Promise(resolve=>setTimeout(resolve,50));
    }
    assert.equal(workingHit?.version_kind,"working");assert.ok(workingHit?.indexed_revision_id);
    const indexedContent=await request(server.baseURL,base+`/entries/planned/content?view=draft&revision=${workingHit.indexed_revision_id}`,"GET");assert.equal(indexedContent.status,200,indexedContent.text);assert.equal(envelope(indexedContent).data.revision.revision_id,workingHit.indexed_revision_id);
    response=await request(server.baseURL,base+"/resources","POST",{body:{operation_id:"e2e-resource",object_id:id,expected_generation:placement.generation,name:"readme.txt",content:Buffer.from("shareable attachment").toString("base64")}});assert.equal(response.status,200,response.text);const resource=envelope(response).data;
    response=await request(server.baseURL,`/api/resources/${resource.id}/content?object_id=${id}`,"GET");assert.equal(response.status,200,response.text);assert.equal(response.text,"shareable attachment");response=await request(server.baseURL,`/api/resources/${resource.id}/content?object_id=other-object`,"GET");assert.equal(response.status,404,response.text);
    response=await request(server.baseURL,`/api/search?scope=global&q=Independent`,"GET");assert.equal(response.status,200,response.text);assert.ok(!envelope(response).data.results.some(hit=>hit.thought_id===id));response=await request(server.baseURL,"/api/search?scope=topic","GET");assert.equal(response.status,400,response.text);
    // Capture refinement may finish after the indexed revision is first visible.
    // A stale preview must conflict; obtain a fresh plan rather than weakening CAS.
    for(let attempt=0;attempt<30;attempt++){
      response=await request(server.baseURL,base+"/migrations/preview","POST",{body:{object_id:id,target_topic_id:""}});assert.equal(response.status,200,response.text);const preview=envelope(response).data;assert.equal(preview.before_global,false);assert.equal(preview.after_global,true);
      response=await request(server.baseURL,base+"/migrations","POST",{body:{operation_id:`e2e-migrate-out-${attempt}`,preview_id:preview.id,plan_hash:preview.plan_hash,expected_object_generation:preview.source.generation}});
      if(response.status!==409)break;
      assert.equal(envelope(response).error.code,"generation_conflict",response.text);
      const unchanged=envelope(await request(server.baseURL,`/api/thoughts/${id}/placement`,"GET")).data;assert.equal(unchanged.owner_topic_id,topic.id);
      await sleep(50);
    }
    assert.equal(response.status,200,response.text);response=await request(server.baseURL,`/api/thoughts/${id}/placement`,"GET");assert.equal(response.status,200,response.text);placement=envelope(response).data;assert.ok(!placement.owner_topic_id);assert.match(placement.placement.thought_path,/^thoughts\//);response=await request(server.baseURL,base+"/entries/planned/content?view=draft","GET");assert.equal(response.status,200,response.text);assert.match(envelope(response).data.body,/Independent content/);
  });

  await t.test("unknown route returns 404 envelope", async () => {
    const res = await request(server.baseURL, "/api/this-route-does-not-exist", "GET");
    assert.equal(res.status, 404);
  });
});

test("first startup records import conflicts and continues healthy recovery", async (t) => {
  if (!BINARY) return;
  const crypto = require("node:crypto");
  const sourceFixture = await createServerFixture();
  const destFixture = await createServerFixture();
  t.after(async () => { await sourceFixture.cleanup(); await destFixture.cleanup(); });
  let source = await startServer(sourceFixture);
  let dest;
  try {
    const topics = [];
    for (const name of ["Conflicting startup", "Healthy startup"]) {
      const response = await request(source.baseURL, "/api/topics", "POST", {body:{operation_id:`create-${topics.length}`,mode:"collection",name}});
      assert.equal(response.status,201,response.text);
      topics.push(envelope(response).data);
    }
    await source.stop(); source = null;
    dest = await startServer(destFixture);
    const foreign = await request(dest.baseURL,"/api/topics","POST",{body:{operation_id:"foreign",mode:"collection",name:"Conflicting startup",description:"Created after interrupted import"}});
    assert.equal(foreign.status,201,foreign.text);
    await dest.stop(); dest = null;
    const foreignPath = path.join(destFixture.contentDir,"topics",topics[0].id,"index.md");
    await fsp.writeFile(foreignPath,"Foreign content survives the first startup.\n");
    // Materialize the durable validated checkpoint of two empty-topic backups.
    // All source bytes come from real HTTP-created topics; no live writer runs.
    for (const [index,topic] of topics.entries()) {
      const id = `startup-import-${index}`;
      const base = path.join(destFixture.contentDir,".content-registry/private/imports",id);
      const files = [];
      for (const name of ["topic.yaml","catalog.json","index.md"]) {
        const relative = `topics/${topic.id}/${name}`;
        const bytes = await fsp.readFile(path.join(sourceFixture.contentDir,relative));
        const staged = path.join(base,"stage",relative);
        await fsp.mkdir(path.dirname(staged),{recursive:true}); await fsp.writeFile(staged,bytes);
        files.push({path:relative,hash:`sha256:${crypto.createHash("sha256").update(bytes).digest("hex")}`,size:bytes.length});
      }
      const operation = {id,kind:"import",topic_id:topic.id,state:"prepared",created_at:new Date().toISOString(),actor:"local"};
      await fsp.writeFile(path.join(base,"state.json"),JSON.stringify({operation,topic_id:topic.id,objects:{},phase:"validated",manifest:{schema_version:1,export_kind:"backup",topic_id:topic.id,files,objects:[],relations:[]}}));
      const operationPath = path.join(destFixture.contentDir,".content-registry/private/operations",`${id}.json`);
      await fsp.mkdir(path.dirname(operationPath),{recursive:true});await fsp.writeFile(operationPath,JSON.stringify(operation));
    }
    dest = await startServer(destFixture); // must become ready on the FIRST restart
    const status = await request(dest.baseURL,"/api/operations/startup-import-0","GET");
    assert.equal(status.status,200,status.text);
    assert.equal(envelope(status).data.operation.state,"conflicted");
    assert.ok(envelope(status).data.operation.error);
    const healthy = await request(dest.baseURL,`/api/topics/${topics[1].id}`,"GET");
    assert.equal(healthy.status,200,healthy.text);
    const completed = await request(dest.baseURL,"/api/operations/startup-import-1","GET");
    assert.equal(completed.status,200,completed.text);assert.equal(envelope(completed).data.operation.state,"completed");
    assert.equal(await fsp.readFile(foreignPath,"utf8"),"Foreign content survives the first startup.\n");
    const created = await request(dest.baseURL,"/api/thoughts","POST",{body:{type:"text",content:"Unrelated note after conflict recovery"}});
    assert.equal(created.status,202,created.text);
  } finally {
    if (source) await source.stop();
    if (dest) await dest.stop();
  }
});

// Real service and filesystem, deterministic local generator/reviewer. No
// request is sent to a real provider or to the user's running workspace.
test("Capture document set: six files, joint gate, fixed reading, delivery, backup and restart",{timeout:240000},async(t)=>{
 const crypto=require("node:crypto");
 const userText="Build ordering with API and persistent storage. Modules: Order and Billing. Verify recovery.";
 const shared={facts:[
  {id:"REQ-ORDER",kind:"requirement",name:"Order flow",content:"Orders must be recoverable.",owner:"architecture"},
  {id:"MOD-ORDER",kind:"module",name:"Order",content:"Own order lifecycle.",owner:"module:MOD-ORDER"},
  {id:"MOD-BILLING",kind:"module",name:"Billing",content:"Own billing lifecycle.",owner:"module:MOD-BILLING"},
  {id:"API-ORDER",kind:"interface",name:"Create order",content:"POST /orders requires authentication and idempotence.",owner:"interfaces",fields:[{id:"id",type:"string",nullable:false,classification:"persisted",entity_id:"ENTITY-ORDER",entity_field:"id",permission:"user",errors:["409"]}]},
  {id:"ENTITY-ORDER",kind:"entity",name:"Order record",content:"Atomic persistence and rollback preserve consistency.",owner:"storage",fields:[{id:"id",type:"string",nullable:false,classification:"persisted"}]},
  {id:"TEST-ORDER",kind:"verification",name:"Recovery check",content:"Exercise failure and recovery of both modules.",owner:"verification"}
 ].map(f=>({...f,status:"confirmed",source:{message:0,quote:"Build ordering"},reason:"Explicit user scope"})),relations:[{id:"REL-REQ",kind:"verification",from:"REQ-ORDER",to:"TEST-ORDER"},{id:"REL-ORDER",kind:"verification",from:"MOD-ORDER",to:"TEST-ORDER"},{id:"REL-BILLING",kind:"verification",from:"MOD-BILLING",to:"TEST-ORDER"},{id:"REL-DATA",kind:"mapping",from:"API-ORDER",to:"ENTITY-ORDER"}],conditions:{has_interfaces:true,has_persistence:true},inputs:{goal:"Recoverable orders",scope:"Ordering and billing",constraints:"Local persistence"},open_questions:[]};
 let calls=0;const gateway=http.createServer(async(req,res)=>{try{const chunks=[];for await(const c of req)chunks.push(c);const request=JSON.parse(Buffer.concat(chunks));const system=request.messages[0].content;const prompt=request.messages.at(-1).content;let content;
  if(system.startsWith("Plan a coordinated"))content=shared;
  else if(system.startsWith("Review the final document")){const input=JSON.parse(prompt);content={evidence:input.checklist.map(item=>({check_id:item.id,verdict:"passed",locations:item.members.map(key=>({member_key:key,section:"scope",evidence:input.members.find(member=>member.member_key===key).body.split("## Scope")[1].split("## Design")[0].trim().split("\n")[0]})),reason:"Final body contracts and recovery evidence were inspected."})),issues:[]};}
  else if(system.startsWith("Generate structured")){const match=prompt.match(/Profile JSON:\n([^\n]+)/);const profile=JSON.parse(match[1]);content={title:"Generated",summary:"Role-specific design",sections:Object.fromEntries(profile.allowed_section_keys.map(key=>[key,{content:`The ${key} preserves agreed responsibilities, precise contracts and recoverable failure handling.`}])),references:[],assumptions:[]};}
  else content={assistant_reply:"The requested scope is retained. Ready to generate a document set.",topic:"Ordering",goal:"Recoverable orders",confirmed_facts:[userText],open_questions:[],conflicts:[],candidate_title:"Ordering",candidate_tags:[],candidate_summary:"Ordering and billing",candidate_body:userText,source_links:[],related_thought_ids:[],suggested_topic_ids:[],archive_intent:"none",candidate_document_family:"note",candidate_profile_id:"builtin.note",candidate_profile_version:1,profile_confidence:100,document_parameters:{},archive_readiness:"ready"};
  calls++;res.writeHead(200,{"Content-Type":"application/json"});res.end(JSON.stringify({id:"fixture",object:"chat.completion",created:1,model:"fixture-model",choices:[{index:0,message:{role:"assistant",content:JSON.stringify(content)},finish_reason:"stop"}]}));
 }catch(error){res.writeHead(500);res.end(String(error));}});
 await new Promise(resolve=>gateway.listen(0,"127.0.0.1",resolve));t.after(()=>gateway.close());const options={llmBaseURL:`http://127.0.0.1:${gateway.address().port}/v1`};
 const original=await createServerFixture();const restored=await createServerFixture();t.after(()=>original.cleanup());t.after(()=>restored.cleanup());let server=await startServer(original,options);let target;
 try{
  const created=await request(server.baseURL,"/api/capture/sessions","POST",{body:{content:userText}});assert.equal(created.status,200,created.text);let sp=envelope(created).data;sp=sp.scratchpad||sp;const session=sp.session_id;assert.ok(session,created.text);
  const deadline=Date.now()+10000;while(Date.now()<deadline){const fetched=envelope(await request(server.baseURL,`/api/capture/sessions/${session}`,"GET")).data;sp=fetched;if(sp.session_context?.assistant_reply)break;await new Promise(r=>setTimeout(r,30));}
  const profiles=envelope(await request(server.baseURL,"/api/document-set-profiles","GET")).data;const profile=profiles.find(p=>p.profile_id==="builtin.feature-design-set");assert.ok(profile);
  const planned=await request(server.baseURL,`/api/capture/sessions/${session}/document-set-plans`,"POST",{body:{operation_id:"e2e-set-plan",expected_generation:0,capture_generation:sp.input_generation,profile_id:profile.profile_id,profile_version:profile.version,profile_hash:profile.content_hash}});assert.equal(planned.status,200,planned.text);const plan=envelope(planned).data;assert.equal(plan.members.length,6);assert.equal(plan.capture.messages,null);assert.ok(plan.shared_design.facts.every(f=>!f.source.quote));
  const originals=[{path:"architecture.md",member_key:"architecture",content:"\ufeff# Reference architecture\r\n\r\nPreserve this exact original source and every line. [Order](modules/order.md#scope)\r\n"},{path:"modules/order.md",member_key:"module:MOD-ORDER",content:"# Order source\n\nRetain boundaries and [Architecture](../architecture.md#scope).\n"}];
  const previewResponse=await request(server.baseURL,`/api/capture/sessions/${session}/document-set-markdown-previews`,"POST",{body:{operation_id:"e2e-md-preview",set_id:plan.set_id,plan_id:plan.plan_id,plan_hash:plan.plan_hash,files:originals}});assert.equal(previewResponse.status,200,previewResponse.text);const preview=envelope(previewResponse).data;assert.equal(preview.mappings[0].original,originals[0].content);assert.ok(preview.cycles.length>0);
  const body={operation_id:"e2e-set-run",set_id:plan.set_id,plan_id:plan.plan_id,plan_hash:plan.plan_hash,expected_generation:0,capture_generation:plan.capture.generation,markdown_preview_id:preview.preview_id};const started=await request(server.baseURL,`/api/capture/sessions/${session}/document-set-runs`,"POST",{body});assert.equal(started.status,202,started.text);
  let run;const finish=Date.now()+60000;while(Date.now()<finish){const polled=await request(server.baseURL,"/api/document-set-runs/e2e-set-run","GET");assert.equal(polled.status,200,polled.text);run=envelope(polled).data;if(["committed","failed_terminal","awaiting_review","stale"].includes(run.state))break;await new Promise(r=>setTimeout(r,30));}
  assert.equal(run.state,"committed",JSON.stringify(run));assert.equal(run.candidate.members.length,6);assert.ok(calls>=8);
  const setPath=`/api/document-sets/${plan.set_id}`;let data=envelope(await request(server.baseURL,setPath,"GET")).data;assert.equal(data.head.finalized_set_revision,"");const rev=data.head.working_set_revision;
  for(const member of run.candidate.members){const response=await request(server.baseURL,`${setPath}/members/${encodeURIComponent(member.member_key)}/content?set_revision=${rev}`,"GET");assert.equal(response.status,200,response.text);assert.equal(envelope(response).data.body_hash,`sha256:${crypto.createHash("sha256").update(member.body).digest("hex")}`);assert.ok(fs.existsSync(path.join(server.contentDir,"topics",plan.set_id,"thoughts",`${member.document_id}.md`)));}
  const rejected=await request(server.baseURL,`${setPath}/exports`,"POST",{body:{operation_id:"e2e-premature",expected_generation:data.head.generation,set_revision_id:rev,kind:"delivery"}});assert.equal(rejected.status,422,rejected.text);
  const finalized=await request(server.baseURL,`${setPath}/finalizations`,"POST",{body:{operation_id:"e2e-set-final",expected_generation:data.head.generation,set_revision_id:rev,set_revision_hash:run.candidate.set_revision_hash,approved_by:"fixture user",delivery_level:"ready_for_use"}});assert.equal(finalized.status,200,finalized.text);data.head=envelope(finalized).data;
  async function exported(kind){const response=await fetch(`${server.baseURL}${setPath}/exports`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({operation_id:`e2e-export-${kind}`,expected_generation:data.head.generation,set_revision_id:rev,kind})});assert.equal(response.status,200,await response.clone().text());return Buffer.from(await response.arrayBuffer());}
  const delivery=await exported("delivery");assert.equal(delivery.readUInt32LE(),0x04034b50);const backup=await exported("backup");
  if(process.env.THOUGHTFLOW_DOCSET_ARTIFACT_DIR){const output=path.resolve(process.env.THOUGHTFLOW_DOCSET_ARTIFACT_DIR);fs.mkdirSync(output,{recursive:true});fs.writeFileSync(path.join(output,"delivery.zip"),delivery);fs.writeFileSync(path.join(output,"backup.zip"),backup);fs.writeFileSync(path.join(output,"set.json"),JSON.stringify(run.candidate,null,2));}

  await server.stop();server=null;server=await startServer(original,options);const reopened=await request(server.baseURL,`${setPath}/revisions/${rev}`,"GET");assert.equal(reopened.status,200,reopened.text);assert.equal(envelope(reopened).data.set_revision_hash,run.candidate.set_revision_hash);
  target=await startServer(restored,options);const imported=await fetch(`${target.baseURL}/api/document-set-imports?operation_id=e2e-set-import`,{method:"POST",headers:{"Content-Type":"application/zip"},body:backup});assert.equal(imported.status,201,await imported.clone().text());await target.stop();target=null;target=await startServer(restored,options);
  const restoredSet=await request(target.baseURL,`${setPath}/revisions/${rev}`,"GET");assert.equal(restoredSet.status,200,restoredSet.text);assert.equal(envelope(restoredSet).data.set_revision_hash,run.candidate.set_revision_hash);
  for(const original of originals){const member=envelope(restoredSet).data.members.find(member=>member.member_key===original.member_key);assert.equal(member.original,original.content);const thought=envelope(await request(target.baseURL,`/api/thoughts/${member.document_id}`,"GET")).data;assert.equal(thought.content.original,original.content);}

  const duplicate=await fetch(`${target.baseURL}/api/document-set-imports?operation_id=e2e-another-import`,{method:"POST",body:backup});assert.equal(duplicate.status,409);
 }finally{if(server)await server.stop();if(target)await target.stop();}
});
