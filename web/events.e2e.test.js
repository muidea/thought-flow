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

  await t.test("patched and compose lifecycle events reach the real stream", async () => {
    const create = await postJSON(server.baseURL, "/api/thoughts", {
      type: "text", title: "event contract source", content: "Unique factual source for lifecycle events.",
    });
    assert.equal(create.status, 202, create.text);
    const thoughtID = create.json.data.thought.id;
    const types = ["thought.patched", "compose.draft_created", "compose.draft_saved", "compose.draft_deleted"];
    const received = new Set();
    const subscription = subscribeEvents(server.baseURL, {
      types,
      deadlineMs: 15000,
      predicate: (frame) => {
        received.add(frame.event);
        return types.every((type) => received.has(type));
      },
    });
    const deadline = Date.now() + 10000;
    let patch;
    do {
      patch = await fetch(`${server.baseURL}/api/thoughts/${thoughtID}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-Session-Id": "sse-contract" },
        body: JSON.stringify({ title: "Patched event contract source" }),
      });
      if (patch.status !== 409) break;
      await new Promise((resolve) => setTimeout(resolve, 50));
    } while (Date.now() < deadline);
    assert.equal(patch.status, 200, await patch.text());

    const draftResponse = await postJSON(server.baseURL, "/api/compose/drafts", {
      sources: [{ source_type: "thought", source_id: thoughtID }],
      goal: "SSE lifecycle contract", format: "summary",
    });
    assert.equal(draftResponse.status, 202, draftResponse.text);
    const draftID = draftResponse.json.data.resource_id;
    let draft;
    for (let i = 0; i < 100; i++) {
      const response = await fetch(`${server.baseURL}/api/compose/drafts/${draftID}`);
      assert.equal(response.status, 200);
      draft = (await response.json()).data;
      if (draft.status === "draft" || draft.status === "failed") break;
      await new Promise((resolve) => setTimeout(resolve, 50));
    }
    assert.equal(draft.status, "draft", JSON.stringify(draft));
    const save = await postJSON(server.baseURL, `/api/compose/drafts/${draftID}/save`, {
      title: "SSE saved draft", content: draft.content,
    });
    assert.equal(save.status, 202, save.text);
    const deleted = await fetch(`${server.baseURL}/api/compose/drafts/${draftID}`, { method: "DELETE" });
    assert.equal(deleted.status, 200, await deleted.text());
    const frames = await subscription;
    for (const type of types) {
      const frame = frames.find((candidate) => candidate.event === type &&
        (type === "compose.draft_saved"
          ? candidate.payload?.payload?.id === draftID && candidate.payload?.resource_id === save.json.data.thought.id
          : candidate.payload?.resource_id === (type === "thought.patched" ? thoughtID : draftID)));
      assert.ok(frame, `missing ${type} for target resource: ${JSON.stringify(frames)}`);
      assert.ok(frame.id, "every event carries a replay id");
    }
  });

  await t.test("collection catalog and policy changes arrive without document bodies", async () => {
    const created=await postJSON(server.baseURL,"/api/topics",{operation_id:"sse-create",name:"SSE Collection",mode:"collection"});
    assert.equal(created.status,201,created.text);
    const topic=created.json.data.id;
    const catalogResponse=await fetch(`${server.baseURL}/api/topics/${topic}/catalog`);
    assert.equal(catalogResponse.status,200);
    const view=(await catalogResponse.json()).data;
    const received=new Set();
    const subscription=subscribeEvents(server.baseURL,{types:["topic.catalog_updated","topic.search_policy_updated"],deadlineMs:12000,predicate:frame=>{received.add(frame.event);return received.size===2;}});
    const saved=await fetch(`${server.baseURL}/api/topics/${topic}/catalog`,{method:"PUT",headers:{"Content-Type":"application/json"},body:JSON.stringify({operation_id:"sse-catalog",expected_generation:view.catalog.generation,groups:view.catalog.groups,entries:[{id:"planned",group_id:"default",title:"Planned delivery",required:true,binding:{kind:"planned"}}]})});
    assert.equal(saved.status,200,await saved.text());
    const settings=await fetch(`${server.baseURL}/api/topics/${topic}/settings`,{method:"PATCH",headers:{"Content-Type":"application/json"},body:JSON.stringify({operation_id:"sse-policy",expected_generation:view.topic.generation,name:"SSE Collection",description:"Metadata only",search_policy:{topic_global_visible:true,owned_thoughts_global_default:false}})});
    assert.equal(settings.status,200,await settings.text());
    const frames=await subscription;
    for(const type of ["topic.catalog_updated","topic.search_policy_updated"]){
      const frame=frames.find(item=>item.event===type&&item.payload?.resource_id===topic);
      assert.ok(frame,`missing ${type}: ${JSON.stringify(frames)}`);
      assert.ok(frame.payload.payload.operation_id);
      assert.ok(frame.payload.payload.generation>=1);
      assert.equal(frame.payload.payload.body,undefined);
    }
    const current=(await(await fetch(`${server.baseURL}/api/topics/${topic}/catalog`)).json()).data;
    assert.equal(current.catalog.entries[0].id,"planned");
    assert.equal(current.topic.search_policy.owned_thoughts_global_default,false);
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
