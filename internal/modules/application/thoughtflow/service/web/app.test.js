const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

// Minimal stub for window.tflow_i18n so app.js can run inside `vm`. Tests
// pass `t(key) → key` (identity) so assertions on the rendered HTML can use
// either the literal English string or the dotted key. `tn` defers to `t`.
const stubTflow = {
  t: (key) => key,
  tn: (key) => key,
  setLocale: () => {},
  getLocale: () => "en-US",
  init: () => "en-US",
  applyTranslations: () => {},
  onLocaleChange: () => () => {},
  listLocales: () => ["en-US", "zh-CN"],
  resetMissingReport: () => {},
};

// Build a minimal DOM stub with input values that the page serializers read
// and that restoreRoutePage writes. Backed by a plain object so tests can
// inspect and mutate inputs between operations. Other selectors return null.
function makeDomStub(initial = {}) {
  const store = { ...initial };
  const controls = ["search-query", "search-mode", "search-topic-id", "search-tags",
    "search-from", "search-to", "search-sort", "search-explain", "topic-filter",
    "topic-auto-filter", "event-type-filter"];
  // Each control is a live proxy over the store: reads go to store, writes
  // (and `checked` toggles) flow back into the store so assertions can see them.
  const nodes = Object.fromEntries(controls.map((id) => {
    const node = {
      get value() { return store[id] ?? ""; },
      set value(v) { store[id] = v; },
      get checked() { return Boolean(store[id + "_checked"]); },
      set checked(v) { store[id + "_checked"] = Boolean(v); },
    };
    return [id, node];
  }));
  function find(selector) {
    const m = selector.match(/^#([\w-]+)$/);
    if (!m) return null;
    return nodes[m[1]] || null;
  }
  return {
    store,
    find,
    all: (_selector) => [],
  };
}

// Build a localStorage stub. Records every key set so tests can inspect.
function makeStorageStub(initial = {}) {
  const data = { ...initial };
  return {
    data,
    getItem: (k) => (k in data ? data[k] : null),
    setItem: (k, v) => { data[k] = String(v); },
    removeItem: (k) => { delete data[k]; },
  };
}

function loadAppFunctionsWith(opts = {}) {
  const appPath = path.join(__dirname, "app.js");
  const parserPath = path.join(__dirname, "vendor", "markdown-it.min.js");
  const parserCode = fs.readFileSync(parserPath, "utf8");
  const code = fs.readFileSync(appPath, "utf8")
    .replace(/\nboot\(\)\.catch\(\(error\) => toast\(error\.message\)\);\s*$/, "");
  const dom = opts.dom || makeDomStub();
  const storage = opts.storage || makeStorageStub();
  // Note: do NOT override `globalThis` in the context object — the markdown-it
  // UMD wrapper uses it to attach `markdownit`, and shadowing it with `{}`
  // would hide the parser.
  const context = {
    document: {
      querySelector: (selector) => dom.find(selector),
      querySelectorAll: (selector) => dom.all(selector),
      addEventListener: () => {},
    },
    window: {
      clearTimeout: () => {},
      setTimeout: () => 0,
      tflow_i18n: stubTflow,
      location: { hash: opts.hash || "" },
      history: { replaceState: () => {} },
      localStorage: storage,
    },
    URLSearchParams,
    fetch: async () => ({ ok: true, json: async () => ({ data: null }) }),
    EventSource: function EventSource() {},
    console,
  };
  const result = vm.runInNewContext(
    `${parserCode}
    ${code}
    ({
      escapeHTML,
      renderMarkdown,
      renderDiff,
      renderSynthesisDraft,
      outlineFromText,
      outlineText,
      parseRoute,
      navItemClass,
      navItemAriaCurrent,
      statusBadge,
      renderSearchResultItem,
      createSynthesisBasket,
      displayWorkspace,
      displayRuntimePath,
      buildRouteHash,
      restoreRoutePage,
      PAGE_SERIALIZERS,
      persistBasket,
      restoreBasket,
      saveToStorage,
      loadFromStorage,
      trapFocus,
      classifyCaptureInput,
      parseCaptureCommand,
      appendCaptureMessage,
      formatBadgeCount,
      computeSidebarBadgeCounts,
      appendExpansionSections,
      appState: state,
    });`,
    context,
    { filename: appPath },
  );
  if (opts.exposeState) result._state = result.appState;
  return result;
}

function loadAppFunctions() {
  return loadAppFunctionsWith();
}

test("renderMarkdown escapes HTML and renders supported Markdown", () => {
  const app = loadAppFunctions();

  const html = app.renderMarkdown(`# Title

Text with **strong** and \`code\`.
- [[thoughts/2026/06/source.md|Source]]
<script>alert("x")</script>`);

  assert.match(html, /<h1>Title<\/h1>/);
  assert.match(html, /<strong>strong<\/strong>/);
  assert.match(html, /<code>code<\/code>/);
  assert.match(html, /title="thoughts\/2026\/06\/source\.md">Source<\/code>/);
  assert.doesNotMatch(html, /<script>/);
  assert.match(html, /&lt;script&gt;alert\(&quot;x&quot;\)&lt;\/script&gt;/);
});

test("parseRoute maps hash routes to pages and navigation groups", () => {
  const app = loadAppFunctions();
  const route = (hash) => JSON.parse(JSON.stringify(app.parseRoute(hash)));

  assert.deepEqual(route(""), { page: "dashboard", nav: "overview", params: {}, query: {} });
  assert.deepEqual(route("#/overview"), { page: "dashboard", nav: "overview", params: {}, query: {} });
  // PR2: topic detail and review are tabs under #/topics. The URL
  // /topics/{id} opens the detail tab by default; the /review segment
  // is rewritten to ?tab=proposals so the same page section hosts both
  // views.
  assert.deepEqual(route("#/topics/demo"), { page: "topics", nav: "topics", params: { topicId: "demo" }, query: { tab: "detail" } });
  assert.deepEqual(route("#/topics/demo/review"), { page: "topics", nav: "topics", params: { topicId: "demo" }, query: { tab: "proposals" } });
  assert.deepEqual(route("#/notes?id=abc"), { page: "thoughts", nav: "notes", params: { thoughtId: "abc" }, query: { id: "abc" } });
  assert.deepEqual(route("#/compose"), { page: "compose", nav: "compose", params: {}, query: {} });
  // PR3: /jobs is no longer a live page — the redirect table at the top
  // of parseRoute rewrites it to #/notes before we get here, so the
  // assertion below just confirms the post-redirect resolution lands on
  // the notes page.
  assert.deepEqual(route("#/notes"), { page: "thoughts", nav: "notes", params: { thoughtId: "" }, query: {} });
});

test("parseRoute redirects deprecated hash paths to their new names", () => {
  const app = loadAppFunctions();
  // The redirect side-effect on window.location.hash is exercised by the
  // browser smoke tests; here we just verify the route resolution matches
  // the new segment so callers see the canonical page/nav pair. We
  // JSON-clone the result to bridge the vm context's Object prototype
  // into the test realm (see route() helper above).
  const route = (hash) => JSON.parse(JSON.stringify(app.parseRoute(hash)));
  assert.deepEqual(
    route("#/dashboard"),
    { page: "dashboard", nav: "overview", params: {}, query: {} },
  );
  assert.deepEqual(
    route("#/thoughts?id=abc"),
    { page: "thoughts", nav: "notes", params: { thoughtId: "abc" }, query: { id: "abc" } },
  );
  assert.deepEqual(
    route("#/synthesis"),
    // /synthesis redirects to /compose with the query string preserved.
    { page: "compose", nav: "compose", params: {}, query: {} },
  );
  // PR3: /settings is gone (gear opens the settings drawer) and /jobs is
  // gone (jobs are surfaced via the notes runtime card and the settings
  // drawer event tab). Both segments now resolve to live pages after the
  // redirect side-effect runs in parseRoute.
  assert.deepEqual(
    route("#/settings"),
    { page: "dashboard", nav: "overview", params: {}, query: {} },
  );
  assert.deepEqual(
    route("#/notes"),
    { page: "thoughts", nav: "notes", params: { thoughtId: "" }, query: {} },
  );
});

test("navigation and status helpers map to AntD-style classes", () => {
  const app = loadAppFunctions();
  const topicRoute = app.parseRoute("#/topics/demo/review");

  assert.equal(app.navItemClass(topicRoute, "topics"), "tf-menu-item active");
  assert.equal(app.navItemClass(topicRoute, "search"), "tf-menu-item");
  assert.equal(app.statusBadge("ready"), "tf-badge tf-badge-success");
  assert.equal(app.statusBadge("degraded"), "tf-badge tf-badge-warning");
  assert.equal(app.statusBadge("failed"), "tf-badge tf-badge-error");
  assert.equal(app.statusBadge("disabled"), "tf-badge tf-badge-default");
});

test("runtime path display avoids leaking absolute workspace paths", () => {
  const app = loadAppFunctions();
  const root = "/home/fedquery/codespace/skillSuite/thought-flow/thoughtflow-workspace";

  assert.equal(app.displayWorkspace({ id: "local", root_path: root }), "local");
  assert.equal(app.displayRuntimePath(`${root}/.thoughtflow/thoughtflow.duckdb`, root), ".thoughtflow/thoughtflow.duckdb");
  assert.equal(app.displayRuntimePath("/var/lib/thoughtflow/external.duckdb", root), "external.duckdb");
  assert.equal(app.displayRuntimePath(".thoughtflow/thoughtflow.duckdb", root), ".thoughtflow/thoughtflow.duckdb");
});

test("renderSearchResultItem exposes scores and action targets", () => {
  const app = loadAppFunctions();

  const html = app.renderSearchResultItem({
    thought_id: "thought-1",
    title: "Search Result",
    snippet: "Snippet",
    score: 0.91,
    keyword_score: 0.8,
    semantic_score: 0.7,
    recency_score: 0.6,
    tags: ["ui"],
    path: "thoughts/demo.md",
    explain: {
      mode: "hybrid",
      sort: "score",
      score_formula: "kw + sem + rec",
      weights: { keyword: 1, semantic: 1, recency: 0.2 },
      keyword_source: "fts",
      semantic_source: "embedding",
    },
  }, { selected: true, activeTopicId: "topic-1" });

  assert.match(html, /data-select-id="thought-1" checked/);
  assert.match(html, /search\.score_label/);
  assert.match(html, /0\.91/);
  assert.match(html, /0\.80/);
  assert.match(html, /0\.70/);
  assert.match(html, /0\.60/);
  assert.match(html, /data-basket-id="thought-1"/);
  assert.match(html, /data-weave-id="thought-1"/);
  assert.match(html, /thoughts\/demo\.md/);
  assert.match(html, /search\.explain\.summary/);
  assert.match(html, /kw \+ sem \+ rec/);
  assert.match(html, /embedding/);
});

test("synthesis basket helper deduplicates and clears sources", () => {
  const app = loadAppFunctions();
  const basket = app.createSynthesisBasket(["one", "one"]);
  const values = (result) => JSON.parse(JSON.stringify(result));

  assert.deepEqual(values(basket.values()), ["one"]);
  assert.deepEqual(values(basket.add("two")), ["one", "two"]);
  assert.deepEqual(values(basket.addMany(["two", "three"])), ["one", "two", "three"]);
  assert.deepEqual(values(basket.clear()), []);
  assert.deepEqual(values(basket.values()), []);
});

test("renderMarkdown supports extended document structures safely", () => {
  const app = loadAppFunctions();

  const html = app.renderMarkdown(`---
id: demo
type: topic
---

| Name | Link |
| --- | --- |
| Alpha | [Open](https://example.test/a) |
| Unsafe | [Nope](javascript:alert(1)) |

1. First
2. Second
- [x] Done
- [ ] Todo
---
*emphasis*
~~removed~~
![Diagram](./attachments/diagram.png)
![Unsafe](javascript:alert(1))`);

  assert.match(html, /<dl class="front-matter">/);
  assert.match(html, /<dt>id<\/dt><dd>demo<\/dd>/);
  assert.match(html, /<table>/);
  assert.match(html, /<th>Name<\/th>/);
  assert.match(html, /<a href="https:\/\/example\.test\/a" target="_blank" rel="noreferrer">Open<\/a>/);
  assert.doesNotMatch(html, /javascript:alert/);
  assert.match(html, /<ol>\s*<li>First<\/li>\s*<li>Second<\/li>\s*<\/ol>/);
  assert.match(html, /<li class="task-item"><input type="checkbox" disabled checked>Done<\/li>/);
  assert.match(html, /<li class="task-item"><input type="checkbox" disabled>Todo<\/li>/);
  assert.match(html, /<hr>/);
  assert.match(html, /<em>emphasis<\/em>/);
  assert.match(html, /<s>removed<\/s>/);
  assert.match(html, /<img src=".\/attachments\/diagram\.png" alt="Diagram" loading="lazy">/);
  assert.doesNotMatch(html, /<img src="javascript/);
});

test("renderMarkdown uses CommonMark block parsing with GFM extensions", () => {
  const app = loadAppFunctions();

  const html = app.renderMarkdown(`Paragraph
continues on the next line.

> Quote
>
> - nested

    indented code

~~strike~~

| A | B |
| --- | --- |
| 1 | 2 |`);

  assert.match(html, /<p>Paragraph\ncontinues on the next line\.<\/p>/);
  assert.match(html, /<blockquote>\n<p>Quote<\/p>\n<ul>\n<li>nested<\/li>\n<\/ul>\n<\/blockquote>/);
  assert.match(html, /<pre><code>indented code\n<\/code><\/pre>/);
  assert.match(html, /<s>strike<\/s>/);
  assert.match(html, /<table>/);
});

test("renderDiff marks added and removed lines", () => {
  const app = loadAppFunctions();

  const html = app.renderDiff([
    { op: "context", text: "same" },
    { op: "remove", text: "old" },
    { op: "add", text: "new" },
  ]);

  assert.match(html, /diff-line context/);
  assert.match(html, /diff-line remove/);
  assert.match(html, /diff-line add/);
  assert.match(html, />-<\/span><code>old<\/code>/);
  assert.match(html, />\+<\/span><code>new<\/code>/);
});

test("renderSynthesisDraft appends only missing source links", () => {
  const app = loadAppFunctions();

  const content = app.renderSynthesisDraft({
    content: "# Draft\n\nAlready cites [[thoughts/one.md]].",
    source_links: ["thoughts/one.md", "thoughts/two.md"],
  });

  assert.equal((content.match(/\[\[thoughts\/one\.md\]\]/g) || []).length, 1);
  assert.match(content, /\[\[thoughts\/two\.md\]\]/);
  assert.match(content, /### Sources/);
});

test("outline helpers preserve one title per line", () => {
  const app = loadAppFunctions();

  const outline = app.outlineFromText("Background\n\nOpen Questions\n");

  assert.equal(JSON.stringify(outline), JSON.stringify([{ title: "Background" }, { title: "Open Questions" }]));
  assert.equal(app.outlineText(outline), "Background\nOpen Questions");
});

test("app.js reads i18n keys from window.tflow_i18n (lazy stub is identity)", () => {
  // The stub above returns the key itself, so the rendered HTML exposes
  // dotted keys instead of literal English — assert that the keys are
  // referenced for both the new (i18n) and the structural pieces.
  const app = loadAppFunctions();
  const html = app.renderSearchResultItem({ thought_id: "x", title: "t", score: 0.1 }, { selected: false, activeTopicId: "" });
  assert.match(html, /search\.score_label/);
  assert.match(html, /search\.keyword_label/);
  assert.match(html, /search\.semantic_label/);
  assert.match(html, /search\.recency_label/);
  assert.match(html, /search\.result\.add_basket/);
});

test("buildRouteHash omits empty query fields and keeps the path clean", () => {
  const app = loadAppFunctions();

  assert.equal(app.buildRouteHash("search", {}, {}), "#/search");
  assert.equal(app.buildRouteHash("search", {}, { q: "rag" }), "#/search?q=rag");
  assert.equal(app.buildRouteHash("search", {}, { q: "rag", mode: "keyword" }), "#/search?q=rag&mode=keyword");
  // Empty values are dropped, null/undefined are dropped, so common default state
  // does not pollute the URL.
  assert.equal(app.buildRouteHash("search", {}, { q: "", mode: null, sort: undefined }), "#/search");
  // PR2: topic detail / proposals / rules share the topics page. The
  // topic id is read from params and the active tab is encoded as
  // ?tab=... so deep-links land on the right pane.
  assert.equal(app.buildRouteHash("topics", { topicId: "ai-notes" }, { tab: "rules" }), "#/topics/ai-notes?tab=rules");
  assert.equal(app.buildRouteHash("topics", { topicId: "ai-notes" }, {}), "#/topics/ai-notes");
  // Special characters are URL-encoded.
  assert.equal(app.buildRouteHash("search", {}, { q: "a b&c" }), "#/search?q=a%20b%26c");
});

test("PAGE_SERIALIZERS.search captures only the non-default state of inputs", () => {
  const dom = makeDomStub({ "search-query": "rag", "search-mode": "semantic", "search-topic-id": "topic-1" });
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  // Seed the global Set used by the serializer.
  app._state.selectedThoughts = new Set(["t-1", "t-2"]);
  const result = app.PAGE_SERIALIZERS.search();

  assert.equal(result.q, "rag");
  assert.equal(result.mode, "semantic");
  assert.equal(result.topic_id, "topic-1");
  assert.equal(result.selected, "t-1,t-2");
  assert.equal(result.explain, undefined);
});

test("PAGE_SERIALIZERS omits fields that are at their default value", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });
  app._state.selectedThoughts = new Set();

  // All inputs at their default state — nothing in the URL.
  assert.equal(JSON.stringify(app.PAGE_SERIALIZERS.search()), "{}");
  assert.equal(JSON.stringify(app.PAGE_SERIALIZERS.topics()), "{}");
});

test("restoreRoutePage populates search inputs from the query object", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });
  app._state.selectedThoughts = new Set();

  app.restoreRoutePage("search", {
    q: "vector store",
    mode: "keyword",
    topic_id: "t-1",
    tags: "rag,llm",
    from: "2026-01-01",
    to: "2026-12-31",
    sort: "recency",
    explain: "true",
    selected: "thought-7,thought-8",
    unknown_field: "ignored",
  });

  assert.equal(dom.store["search-query"], "vector store");
  assert.equal(dom.store["search-mode"], "keyword");
  assert.equal(dom.store["search-topic-id"], "t-1");
  assert.equal(dom.store["search-tags"], "rag,llm");
  assert.equal(dom.store["search-from"], "2026-01-01");
  assert.equal(dom.store["search-to"], "2026-12-31");
  assert.equal(dom.store["search-sort"], "recency");
  assert.equal(dom.store["search-explain_checked"], true);
  assert.deepEqual(Array.from(app._state.selectedThoughts), ["thought-7", "thought-8"]);
});

test("restoreRoutePage ignores unknown / malformed keys without throwing", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  // Non-string where a string is expected, plus unknown keys — must not throw
  // and must not corrupt existing state.
  app._state.selectedThoughts = new Set(["keep"]);
  app.restoreRoutePage("search", { q: 7, mode: null, random: "thing" });
  assert.equal(dom.store["search-query"] ?? "", "");
  assert.equal(dom.store["search-mode"] ?? "", "");
  assert.deepEqual(Array.from(app._state.selectedThoughts), ["keep"]);

  // Unknown page identifier is a no-op.
  app.restoreRoutePage("nope", { q: "rag" });
  assert.equal(dom.store["search-query"] ?? "", "");
});

test("restoreRoutePage hydrates topic state from query", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  app.restoreRoutePage("topics", { keyword: "ai", auto_weave: "true" });
  assert.equal(dom.store["topic-filter"], "ai");
  assert.equal(dom.store["topic-auto-filter_checked"], true);
});

test("persistBasket writes a JSON envelope; restoreBasket reads it back", () => {
  const storage = makeStorageStub();
  const app = loadAppFunctionsWith({ storage, exposeState: true });

  app._state.synthesisBasket = new Set(["t-1", "t-2", "t-3"]);
  app.persistBasket();
  const raw = storage.data["tflow.basket"];
  assert.ok(raw, "basket should be persisted to localStorage");
  const envelope = JSON.parse(raw);
  assert.deepEqual(envelope.ids, ["t-1", "t-2", "t-3"]);
  assert.ok(envelope.updated_at, "envelope should carry a timestamp");

  // Simulate a reload: the new module instance has an empty Set, then we
  // hydrate from storage.
  app._state.synthesisBasket = new Set();
  app.restoreBasket();
  assert.deepEqual(Array.from(app._state.synthesisBasket), ["t-1", "t-2", "t-3"]);
});

test("restoreBasket is tolerant of missing or corrupt payloads", () => {
  const storage = makeStorageStub();
  const app = loadAppFunctionsWith({ storage, exposeState: true });

  app._state.synthesisBasket = new Set(["keep"]);
  // No payload at all.
  app.restoreBasket();
  assert.deepEqual(Array.from(app._state.synthesisBasket), ["keep"]);

  // Garbage payload — set stays at its current value.
  storage.data["tflow.basket"] = "not-json";
  app.restoreBasket();
  assert.deepEqual(Array.from(app._state.synthesisBasket), ["keep"]);

  // Payload missing the ids array.
  storage.data["tflow.basket"] = JSON.stringify({ updated_at: "now" });
  app.restoreBasket();
  assert.deepEqual(Array.from(app._state.synthesisBasket), ["keep"]);
});

test("navItemAriaCurrent marks the active page and clears others", () => {
  // Re-import without the dom stub so navItemAriaCurrent is exposed.
  const app = loadAppFunctions();
  const route = { page: "search", nav: "search", params: {}, query: {} };
  assert.equal(app.navItemAriaCurrent(route, "search"), "page");
  assert.equal(app.navItemAriaCurrent(route, "dashboard"), null);
  assert.equal(app.navItemAriaCurrent(route, "thoughts"), null);
});

test("renderDiff emits translated empty-state key", () => {
  const app = loadAppFunctions();
  const html = app.renderDiff([]);
  assert.match(html, /diff\.no_changes/);
});

test("classifyCaptureInput recognizes URLs and plain text", () => {
  const app = loadAppFunctions();
  const classify = (text) => JSON.parse(JSON.stringify(app.classifyCaptureInput(text)));
  assert.deepEqual(classify("https://example.com/article"), {
    type: "url",
    url: "https://example.com/article",
    content: "",
  });
  assert.deepEqual(classify("see https://example.com for context"), {
    type: "url",
    url: "https://example.com",
    content: "see  for context",
  });
  assert.equal(app.classifyCaptureInput("just a thought").type, "text");
});

test("parseCaptureCommand matches known intents and ignores noise", () => {
  const app = loadAppFunctions();
  const parse = (text) => JSON.parse(JSON.stringify(app.parseCaptureCommand(text)));
  assert.deepEqual(parse("rename to RAG notes"), { kind: "rename", title: "RAG notes" });
  assert.deepEqual(parse("set title RAG notes"), { kind: "rename", title: "RAG notes" });
  assert.deepEqual(parse("把标题改为 RAG 笔记"), { kind: "rename", title: "RAG 笔记" });
  assert.deepEqual(parse("add tag engineering, search"), {
    kind: "add_tag",
    tags: ["engineering", "search"],
  });
  assert.deepEqual(parse("add tags engineering, search"), {
    kind: "add_tag",
    tags: ["engineering", "search"],
  });
  assert.deepEqual(parse("AI 笔记加 Important followup"), {
    kind: "append_note",
    paragraph: "Important followup",
  });
  assert.deepEqual(parse("move to topic research"), {
    kind: "move_topic",
    topicRef: "research",
  });
  assert.deepEqual(parse("归到 research 专题"), {
    kind: "move_topic",
    topicRef: "research",
  });
  assert.deepEqual(parse("refine again"), { kind: "refine_again" });
  assert.equal(app.parseCaptureCommand("just chatting"), null);
});

test("appendCaptureMessage records the message into state.capture", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  const before = (app._state?.capture?.messages?.length) || 0;
  const entry = app.appendCaptureMessage({ role: "user", text: "hi" });
  assert.equal(entry.role, "user");
  assert.equal(entry.text, "hi");
  assert.equal(app._state.capture.messages.length, before + 1);
  assert.equal(app._state.capture.messages[before].text, "hi");
});

test("formatBadgeCount returns empty for zero/negative/non-finite, caps at 99+", () => {
  const app = loadAppFunctions();
  assert.equal(app.formatBadgeCount(0), "");
  assert.equal(app.formatBadgeCount(-1), "");
  assert.equal(app.formatBadgeCount(null), "");
  assert.equal(app.formatBadgeCount(undefined), "");
  assert.equal(app.formatBadgeCount(NaN), "");
  assert.equal(app.formatBadgeCount("abc"), "");
  assert.equal(app.formatBadgeCount(1), "1");
  assert.equal(app.formatBadgeCount(42), "42");
  assert.equal(app.formatBadgeCount(99), "99");
  assert.equal(app.formatBadgeCount(100), "99+");
  assert.equal(app.formatBadgeCount(1234), "99+");
  // Strings that look numeric still pass through Number() coercion.
  assert.equal(app.formatBadgeCount("7"), "7");
  assert.equal(app.formatBadgeCount("0"), "");
});

test("computeSidebarBadgeCounts reads notes/topics/compose from state", () => {
  const app = loadAppFunctions();
  const counts = app.computeSidebarBadgeCounts({
    metrics: { values: { thoughtflow_capture_total: 42 } },
    topics: [{ id: "a" }, { id: "b" }, { id: "c" }],
    synthesisDrafts: [{ id: "d1" }],
  });
  // The returned object comes from a different vm context, so we compare
  // via JSON to avoid prototype/reference-equality false negatives.
  assert.equal(JSON.stringify(counts), JSON.stringify({ notes: "42", topics: "3", compose: "1" }));

  // Missing slices should render as empty so the badges stay hidden.
  const empty = app.computeSidebarBadgeCounts({});
  assert.equal(JSON.stringify(empty), JSON.stringify({ notes: "", topics: "", compose: "" }));

  // Zero and non-finite inputs are treated as no data.
  const zeros = app.computeSidebarBadgeCounts({
    metrics: { values: { thoughtflow_capture_total: 0 } },
    topics: [],
    synthesisDrafts: [],
  });
  assert.equal(JSON.stringify(zeros), JSON.stringify({ notes: "", topics: "", compose: "" }));
});

test("appendExpansionSections renders the 4 expansion fields when present", () => {
  const app = loadAppFunctions();
  const out = app.appendExpansionSections({
    related_thought_ids: ["20260610-100000-rag", "20260610-110000-crawl"],
    suggested_topic_ids: ["topic-pipelines"],
    url_followups: [
      { url: "https://example.com/a", title: "A primer", snippet: "intro" },
      { url: "https://example.com/b", title: "" },
    ],
    expansion_plan: "## 背景\n...\n## 步骤\n1. ...",
  });
  // The function returns raw markdown, not HTML — the caller feeds it
  // into renderMarkdown. The i18n stub is identity, so dotted keys
  // appear as section headers.
  assert.match(out, /## thoughts\.section_related/);
  assert.match(out, /## thoughts\.section_near_topics/);
  assert.match(out, /## thoughts\.section_url_followups/);
  assert.match(out, /## thoughts\.section_expansion_plan/);
  assert.match(out, /- `20260610-100000-rag`/);
  assert.match(out, /- \[A primer\]\(https:\/\/example\.com\/a\)/);
  // Empty title falls back to the URL so the link is still useful.
  assert.match(out, /\[https:\/\/example\.com\/b\]\(https:\/\/example\.com\/b\)/);
  // Plan is rendered as a multi-line block, not a single line.
  assert.match(out, /## 步骤/);
});

test("appendExpansionSections emits pending hint when nothing has landed", () => {
  const app = loadAppFunctions();
  const out = app.appendExpansionSections({});
  assert.match(out, /thoughts\.expansion_pending/);
});

test("appendExpansionSections stays silent once any field lands", () => {
  const app = loadAppFunctions();
  // A single related thought is enough to stop the pending hint; the
  // user has at least one concrete piece of expansion to look at.
  const out = app.appendExpansionSections({ related_thought_ids: ["x"] });
  assert.doesNotMatch(out, /thoughts\.expansion_pending/);
});

test("appendExpansionSections surfaces partial-failure errors", () => {
  const app = loadAppFunctions();
  const out = app.appendExpansionSections({
    related_thought_ids: ["x"],
    errors: [{ code: "thoughtflow.expand.partial_failed", message: "search index offline" }],
  });
  assert.match(out, /thoughts\.expansion_failed/);
});
