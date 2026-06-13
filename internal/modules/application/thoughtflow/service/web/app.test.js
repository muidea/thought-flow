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
  const controls = ["search-query", "search-topic-id", "search-tags",
    "topic-filter", "topic-auto-filter", "event-type-filter"];
  // Side-effect nodes (toast, basket list, etc.) only need the methods the
  // app touches — they all swallow writes silently so callers don't crash
  // when the test doesn't drive them.
  const sideEffectNodes = new Set(["toast", "compose-source-count", "compose-source-list",
    "clear-compose-basket", "compose-basket-list", "compose-source-count-basket",
    "clear-compose-basket-tab"]);
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
  for (const id of sideEffectNodes) {
    nodes[id] = {
      textContent: "",
      innerHTML: "",
      classList: { add: () => {}, remove: () => {}, contains: () => false },
      setAttribute: () => {},
      removeAttribute: () => {},
      disabled: false,
      dataset: {},
      style: {},
    };
  }
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
      renderComposeDraft,
      outlineFromText,
      outlineText,
      parseRoute,
      navItemClass,
      navItemAriaCurrent,
      statusBadge,
      renderSearchResultItem,
      renderTopicCandidateImpact,
      renderTopicCandidates,
      createComposeBasket,
      addToComposeBasket,
      clearComposeBasket,
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
      renderCaptureThoughtCard,
      renderCaptureThoughtCardFromSnapshot,
      buildCaptureExpansionSections,
      formatPatchFeedback,
      renderCaptureBubbleBody,
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
  assert.deepEqual(route("#/capture"), { page: "capture", nav: "capture", params: {}, query: {} });
  assert.deepEqual(route("#/search"), { page: "search", nav: "search", params: {}, query: {} });
  // PR2: topic detail and review are tabs under #/topics. The URL
  // /topics/{id} opens the detail tab by default; the /review segment
  // is rewritten to ?tab=proposals so the same page section hosts both
  // views.
  assert.deepEqual(route("#/topics/demo"), { page: "topics", nav: "topics", params: { topicId: "demo" }, query: { tab: "detail" } });
  assert.deepEqual(route("#/topics/demo/review"), { page: "topics", nav: "topics", params: { topicId: "demo" }, query: { tab: "proposals" } });
  // /topics?topic=...&tab=... is an alternate path syntax (no path id);
  // the topic id lives in the query and the page has no in-param topicId.
  assert.deepEqual(route("#/topics?topic=demo&tab=rules"), { page: "topics", nav: "topics", params: {}, query: { topic: "demo", tab: "rules" } });
  assert.deepEqual(route("#/notes?id=abc"), { page: "thoughts", nav: "notes", params: { thoughtId: "abc" }, query: { id: "abc" } });
  assert.deepEqual(route("#/compose"), { page: "compose", nav: "compose", params: {}, query: {} });
  assert.deepEqual(route("#/compose?draft=d-1"), { page: "compose", nav: "compose", params: {}, query: { draft: "d-1" } });
  // The bare /notes segment opens the notes list with no thought selected;
  // ?id= selects a specific thought.
  assert.deepEqual(route("#/notes"), { page: "thoughts", nav: "notes", params: { thoughtId: "" }, query: {} });
});

test("parseRoute falls back to overview for unknown segments", () => {
  const app = loadAppFunctions();
  const route = (hash) => JSON.parse(JSON.stringify(app.parseRoute(hash)));
  // Any top-level segment that isn't in the live set (overview / capture /
  // search / topics / notes / compose) falls through to overview. The
  // query is preserved so legacy query params don't silently vanish.
  assert.deepEqual(
    route("#/legacy-dashboard"),
    { page: "dashboard", nav: "overview", params: {}, query: {} },
  );
  assert.deepEqual(
    route("#/legacy-thoughts?id=abc"),
    { page: "dashboard", nav: "overview", params: {}, query: { id: "abc" } },
  );
  assert.deepEqual(
    route("#/legacy-synthesis"),
    { page: "dashboard", nav: "overview", params: {}, query: {} },
  );
  assert.deepEqual(
    route("#/legacy-settings"),
    { page: "dashboard", nav: "overview", params: {}, query: {} },
  );
  assert.deepEqual(
    route("#/legacy-jobs?id=foo"),
    { page: "dashboard", nav: "overview", params: {}, query: { id: "foo" } },
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

  // SearchResultView 投影不再下放 keyword/semantic/recency 拆分与 explain,
  // Web 仅暴露 thought_id / title / snippet / score / tags / path 即可。
  const html = app.renderSearchResultItem({
    thought_id: "thought-1",
    title: "Search Result",
    snippet: "Snippet",
    score: 0.91,
    tags: ["ui"],
    path: "thoughts/demo.md",
  }, { selected: true, activeTopicId: "topic-1" });

  assert.match(html, /data-select-id="thought-1" checked/);
  assert.match(html, /search\.score_label/);
  assert.match(html, /0\.91/);
  // 拆分 score 字段不在主流程展示。
  assert.doesNotMatch(html, /0\.80/);
  assert.doesNotMatch(html, /0\.70/);
  assert.doesNotMatch(html, /0\.60/);
  assert.match(html, /data-basket-id="thought-1"/);
  assert.match(html, /data-weave-id="thought-1"/);
  assert.match(html, /thoughts\/demo\.md/);
  assert.doesNotMatch(html, /tf-explain/);
});

test("compose basket helper deduplicates and clears sources", () => {
  const app = loadAppFunctions();
  // Initial entries are full source objects keyed by (source_type, source_id).
  const basket = app.createComposeBasket([
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "thought", source_id: "one", title: "duplicate" },
  ]);
  const values = (result) => JSON.parse(JSON.stringify(result));

  assert.deepEqual(values(basket.values()), [
    { source_type: "thought", source_id: "one", title: "One" },
  ]);
  // add() of a new (type, id) extends the basket.
  assert.deepEqual(values(basket.add({ source_type: "search_result", source_id: "two" })), [
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "search_result", source_id: "two", title: "" },
  ]);
  // add() of a duplicate is a no-op (no error, no double entry).
  assert.deepEqual(values(basket.add({ source_type: "thought", source_id: "one", title: "ignored" })), [
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "search_result", source_id: "two", title: "" },
  ]);
  // addMany() iterates and deduplicates.
  assert.deepEqual(values(basket.addMany([
    { source_type: "search_result", source_id: "two" },
    { source_type: "topic_section", source_id: "three", title: "Three" },
  ])), [
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "search_result", source_id: "two", title: "" },
    { source_type: "topic_section", source_id: "three", title: "Three" },
  ]);
  // clear() empties the basket.
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

test("renderComposeDraft appends only missing source links", () => {
  const app = loadAppFunctions();

  const content = app.renderComposeDraft({
    content: "# Draft\n\nAlready cites [[thoughts/one.md]].",
    source_links: ["thoughts/one.md", "thoughts/two.md"],
  });

  assert.equal((content.match(/\[\[thoughts\/one\.md\]\]/g) || []).length, 1);
  assert.match(content, /\[\[thoughts\/two\.md\]\]/);
  assert.match(content, /### Sources/);
});

test("renderTopicCandidateImpact surfaces source discriminator and metadata", () => {
  const app = loadAppFunctions();
  const html = app.renderTopicCandidateImpact({
    source: "compose_draft",
    candidate_id: "cand-1",
    draft_id: "draft-1",
    title: "Compose draft 1",
    match_type: "keyword",
    score: 0.82,
    status: "pending",
    reasons: ["shares keyword: DuckDB", "shares thought: thought-9"],
  });
  assert.match(html, /data-candidate-source="compose_draft"/);
  assert.match(html, /data-candidate-id="cand-1"/);
  assert.match(html, /data-candidate-ref="draft-1"/);
  assert.match(html, /Compose draft 1/);
  assert.match(html, /topics\.candidate_source\.compose_draft/);
  assert.match(html, /topics\.score_label|search\.score_label/);
  assert.match(html, /keyword/);
  // reasons are joined with " · " so users can scan why this candidate landed
  assert.match(html, /shares keyword: DuckDB/);
  assert.match(html, /shares thought: thought-9/);
});

test("renderTopicCandidates lists every item and falls back to empty state", () => {
  const app = loadAppFunctions();
  const htmlEmpty = app.renderTopicCandidates([]);
  assert.match(htmlEmpty, /topics\.candidates_empty/);

  const html = app.renderTopicCandidates([
    { source: "thought", candidate_id: "c1", thought_id: "t1", title: "T1", score: 0.5 },
    { source: "capture_session", candidate_id: "c2", session_id: "s1", title: "S1", score: 0.4 },
  ]);
  assert.match(html, /topics\.candidates_title/);
  assert.match(html, /data-candidate-source="thought"/);
  assert.match(html, /data-candidate-source="capture_session"/);
  assert.match(html, /data-candidate-ref="t1"/);
  assert.match(html, /data-candidate-ref="s1"/);
});

test("outline helpers preserve one title per line", () => {
  const app = loadAppFunctions();

  const outline = app.outlineFromText("Background\n\nOpen Questions\n");

  assert.equal(JSON.stringify(outline), JSON.stringify([{ title: "Background" }, { title: "Open Questions" }]));
  assert.equal(app.outlineText(outline), "Background\nOpen Questions");
});

test("app.js reads i18n keys from window.tflow_i18n (lazy stub is identity)", () => {
  // The stub above returns the key itself, so the rendered HTML exposes
  // dotted keys instead of literal English — assert that the score and
  // the action labels resolve through the i18n helper. The previous split
  // keyword/semantic/recency labels are gone with the explain block.
  const app = loadAppFunctions();
  const html = app.renderSearchResultItem({ thought_id: "x", title: "t", score: 0.1 }, { selected: false, activeTopicId: "" });
  assert.match(html, /search\.score_label/);
  assert.match(html, /search\.result\.add_basket/);
  assert.doesNotMatch(html, /search\.keyword_label/);
  assert.doesNotMatch(html, /search\.semantic_label/);
  assert.doesNotMatch(html, /search\.recency_label/);
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
  const dom = makeDomStub({ "search-query": "rag", "search-topic-id": "topic-1" });
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  // Seed the global Set used by the serializer.
  app._state.selectedThoughts = new Set(["t-1", "t-2"]);
  const result = app.PAGE_SERIALIZERS.search();

  assert.equal(result.q, "rag");
  assert.equal(result.topic_id, "topic-1");
  assert.equal(result.selected, "t-1,t-2");
  // Search 主流程不再携带 mode/explain/from/to/sort 等可调参数。
  assert.equal(result.mode, undefined);
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
    topic_id: "t-1",
    tags: "rag,llm",
    // Legacy keys are silently ignored — they no longer correspond to
    // any input on the search page.
    mode: "keyword",
    from: "2026-01-01",
    to: "2026-12-31",
    sort: "recency",
    explain: "true",
    selected: "thought-7,thought-8",
    unknown_field: "ignored",
  });

  assert.equal(dom.store["search-query"], "vector store");
  assert.equal(dom.store["search-topic-id"], "t-1");
  assert.equal(dom.store["search-tags"], "rag,llm");
  assert.equal(dom.store["search-mode"] ?? "", "");
  assert.equal(dom.store["search-explain_checked"] ?? false, false);
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

  // Basket is keyed by `${source_type}::${source_id}`; the persisted envelope
  // carries the source list so the next session can rebuild it.
  app._state.composeBasket = new Map([
    ["thought::t-1", { source_type: "thought", source_id: "t-1", title: "T1" }],
    ["search_result::t-2", { source_type: "search_result", source_id: "t-2", title: "T2" }],
  ]);
  app.persistBasket();
  const raw = storage.data["tflow.basket"];
  assert.ok(raw, "basket should be persisted to localStorage");
  const envelope = JSON.parse(raw);
  assert.ok(envelope.sources, "envelope should carry sources array");
  assert.deepEqual(envelope.sources, [
    { source_type: "thought", source_id: "t-1", title: "T1" },
    { source_type: "search_result", source_id: "t-2", title: "T2" },
  ]);
  assert.ok(envelope.updated_at, "envelope should carry a timestamp");
  assert.equal(Array.isArray(envelope.ids), false, "legacy ids field is gone");

  // Simulate a reload: the new module instance has an empty Map, then we
  // hydrate from storage. JSON round-trip flattens cross-realm Object
  // prototypes so deepEqual can compare against literals defined here.
  app._state.composeBasket = new Map();
  app.restoreBasket();
  const restored = JSON.parse(JSON.stringify(Array.from(app._state.composeBasket.values())));
  assert.deepEqual(restored, [
    { source_type: "thought", source_id: "t-1", title: "T1" },
    { source_type: "search_result", source_id: "t-2", title: "T2" },
  ]);
});

test("restoreBasket is tolerant of missing or corrupt payloads", () => {
  const storage = makeStorageStub();
  const app = loadAppFunctionsWith({ storage, exposeState: true });

  const expected = [
    { source_type: "thought", source_id: "keep", title: "" },
  ];
  const flatten = () => JSON.parse(JSON.stringify(Array.from(app._state.composeBasket.values())));

  app._state.composeBasket = new Map([
    ["thought::keep", expected[0]],
  ]);
  // No payload at all — basket keeps its current value.
  app.restoreBasket();
  assert.deepEqual(flatten(), expected);

  // Garbage payload — basket stays at its current value.
  storage.data["tflow.basket"] = "not-json";
  app.restoreBasket();
  assert.deepEqual(flatten(), expected);

  // Payload missing the sources array — basket stays at its current value.
  storage.data["tflow.basket"] = JSON.stringify({ updated_at: "now" });
  app.restoreBasket();
  assert.deepEqual(flatten(), expected);

  // Legacy ids-only payload is ignored (no backward compat).
  storage.data["tflow.basket"] = JSON.stringify({
    ids: ["legacy-1", "legacy-2"],
    updated_at: "now",
  });
  app.restoreBasket();
  assert.deepEqual(flatten(), expected);
});

test("createComposeBasket deduplicates by source_type+source_id and supports clear", () => {
  const app = loadAppFunctions();
  // basket.values() returns objects created in the vm context, so flatten via
  // JSON before comparing against literals defined in this test realm.
  const flat = (arr) => JSON.parse(JSON.stringify(arr));

  const basket = app.createComposeBasket();
  assert.equal(basket.size(), 0);
  assert.deepEqual(flat(basket.values()), []);

  // New entries appear in insertion order. add() returns the full values list.
  assert.deepEqual(flat(basket.add({ source_type: "thought", source_id: "a", title: "A" })), [
    { source_type: "thought", source_id: "a", title: "A" },
  ]);
  assert.deepEqual(flat(basket.add({ source_type: "search_result", source_id: "b" })), [
    { source_type: "thought", source_id: "a", title: "A" },
    { source_type: "search_result", source_id: "b", title: "" },
  ]);
  assert.equal(basket.size(), 2);

  // Same (source_type, source_id) twice — kept only once. The returned values
  // list reflects the unchanged state.
  assert.deepEqual(flat(basket.add({ source_type: "thought", source_id: "a", title: "ignored" })), [
    { source_type: "thought", source_id: "a", title: "A" },
    { source_type: "search_result", source_id: "b", title: "" },
  ]);
  assert.equal(basket.size(), 2);

  // Same source_id under a different source_type is a distinct source.
  assert.deepEqual(flat(basket.add({ source_type: "search_result", source_id: "a", title: "" })), [
    { source_type: "thought", source_id: "a", title: "A" },
    { source_type: "search_result", source_id: "b", title: "" },
    { source_type: "search_result", source_id: "a", title: "" },
  ]);
  assert.equal(basket.size(), 3);

  // addMany iterates all sources; malformed entries are silently dropped.
  basket.addMany([
    null,
    { source_type: "thought" },
    { source_id: "no-type" },
    { source_type: "topic_section", source_id: "t-1", title: "T" },
  ]);
  assert.equal(basket.size(), 4);
  assert.equal(basket.has({ source_type: "topic_section", source_id: "t-1" }), true);

  // clear empties the basket.
  basket.clear();
  assert.equal(basket.size(), 0);
  assert.deepEqual(flat(basket.values()), []);
});

test("addToComposeBasket accepts strings and source objects, defaults to thought", () => {
  // addToComposeBasket is a side-effecting helper (it persists, broadcasts,
  // and renders) so it needs the dom + storage stubs.
  const dom = makeDomStub();
  const storage = makeStorageStub();
  const app = loadAppFunctionsWith({ dom, storage, exposeState: true });

  const flat = () => JSON.parse(JSON.stringify(Array.from(app._state.composeBasket.values())));

  // A bare string defaults to source_type "thought".
  app.addToComposeBasket(["t-1"]);
  assert.deepEqual(flat(), [{ source_type: "thought", source_id: "t-1", title: "" }]);

  // A second thought under the default sourceType — string path again.
  app.addToComposeBasket(["t-2"]);
  assert.deepEqual(flat(), [
    { source_type: "thought", source_id: "t-1", title: "" },
    { source_type: "thought", source_id: "t-2", title: "" },
  ]);

  // Duplicate thought id is a no-op (no second entry, no error).
  app.addToComposeBasket(["t-1"]);
  assert.equal(app._state.composeBasket.size, 2);

  // Source objects override source_type and carry title metadata.
  app.addToComposeBasket([
    { source_type: "search_result", source_id: "s-1", title: "S1" },
    { source_type: "topic_section", source_id: "u-1", title: "U1" },
  ]);
  assert.deepEqual(flat(), [
    { source_type: "thought", source_id: "t-1", title: "" },
    { source_type: "thought", source_id: "t-2", title: "" },
    { source_type: "search_result", source_id: "s-1", title: "S1" },
    { source_type: "topic_section", source_id: "u-1", title: "U1" },
  ]);

  // Mixing strings and objects in one call is supported; strings get the
  // explicit sourceType, objects use their own source_type.
  app.addToComposeBasket(
    [{ source_type: "capture_session", source_id: "c-1", title: "C1" }, "t-3"],
    "thought",
  );
  assert.equal(app._state.composeBasket.size, 6);
  assert.deepEqual(flat(), [
    { source_type: "thought", source_id: "t-1", title: "" },
    { source_type: "thought", source_id: "t-2", title: "" },
    { source_type: "search_result", source_id: "s-1", title: "S1" },
    { source_type: "topic_section", source_id: "u-1", title: "U1" },
    { source_type: "capture_session", source_id: "c-1", title: "C1" },
    { source_type: "thought", source_id: "t-3", title: "" },
  ]);

  // clearComposeBasket empties state without touching storage directly.
  app.clearComposeBasket();
  assert.equal(app._state.composeBasket.size, 0);
  assert.equal(flat().length, 0);
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
    composeDrafts: [{ id: "d1" }],
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
    composeDrafts: [],
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

test("renderCaptureThoughtCardFromSnapshot renders status chips and refine sections", () => {
  const app = loadAppFunctions();
  const snapshot = {
    thought: {
      id: "20260610-100000-rag",
      display_title: "RAG 检索范式",
      capture_status: "captured",
      refine_status: "refined",
      index_status: "indexed",
      topic_status: "matched",
      summary: "RAG 范式把检索与生成结合。",
      key_points: ["检索外部知识", "拼到 prompt", "用 LLM 生成回答"],
      ai_tags: ["RAG", "检索", "生成"],
      user_tags: ["重点"],
    },
    jobs: [{ id: "j1", type: "refine", status: "succeeded" }],
  };
  const html = app.renderCaptureThoughtCardFromSnapshot(snapshot);
  assert.match(html, /RAG 检索范式/);
  assert.match(html, /data-status="refine-refined"/);
  assert.match(html, /data-status="index-indexed"/);
  assert.match(html, /data-status="topic-matched"/);
  assert.match(html, /RAG 范式把检索与生成结合/);
  assert.match(html, /<li>检索外部知识<\/li>/);
  assert.match(html, /data-tag="user"[^>]*>重点/);
  assert.match(html, /data-tag="ai"[^>]*>RAG/);
});

test("renderCaptureThoughtCardFromSnapshot renders the 4 expansion sections", () => {
  const app = loadAppFunctions();
  const snapshot = {
    thought: {
      id: "t1",
      display_title: "demo",
      refine_status: "expanding",
      related_thought_ids: ["t2", "t3"],
      suggested_topic_ids: ["topic-A"],
      url_followups: [{ url: "https://example.com/a", title: "A primer" }],
      expansion_plan: "## 步骤\n1. 先检索",
    },
  };
  const html = app.renderCaptureThoughtCardFromSnapshot(snapshot);
  assert.match(html, /thoughts\.section_related/);
  assert.match(html, /thoughts\.section_near_topics/);
  assert.match(html, /thoughts\.section_url_followups/);
  assert.match(html, /thoughts\.section_expansion_plan/);
  // Related section lists every id and links to the notes page.
  assert.match(html, /href="#\/notes\?id=t2"/);
  assert.match(html, /href="#\/notes\?id=t3"/);
  // Plan is rendered through renderMarkdown so ## 步骤 becomes a heading.
  assert.match(html, /<h2[^>]*>步骤<\/h2>/);
});

test("renderCaptureThoughtCardFromSnapshot surfaces a pending hint when nothing has landed yet", () => {
  const app = loadAppFunctions();
  const snapshot = {
    thought: {
      id: "t1",
      display_title: "demo",
      refine_status: "refined",
    },
  };
  const html = app.renderCaptureThoughtCardFromSnapshot(snapshot);
  assert.match(html, /thoughts\.expansion_pending/);
  // The pending hint wraps in tf-capture-expansion-stack but no actual
  // <details> expansion block has rendered yet.
  assert.doesNotMatch(html, /<details class="tf-capture-expansion"/);
});

test("renderCaptureThoughtCardFromSnapshot surfaces partial-failure errors", () => {
  const app = loadAppFunctions();
  const snapshot = {
    thought: {
      id: "t1",
      display_title: "demo",
      related_thought_ids: ["x"],
      errors: [{ code: "thoughtflow.expand.partial_failed", message: "LLM timeout" }],
    },
  };
  const html = app.renderCaptureThoughtCardFromSnapshot(snapshot);
  assert.match(html, /thoughts\.expansion_failed/);
});

test("renderCaptureBubbleBody re-renders thoughtId-bound bubbles from the active snapshot", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  // Set up an active thought + a freshly refined snapshot.
  app._state.capture.activeThoughtId = "t1";
  app._state.capture.activeSnapshot = {
    thought: {
      id: "t1",
      display_title: "Before refine",
      refine_status: "pending",
    },
  };
  // Message bound to the active thought — should be regenerated.
  const message = { id: "m1", role: "ai", thoughtId: "t1", html: "<stale/>" };
  // Now simulate the refine landing.
  app._state.capture.activeSnapshot = {
    thought: {
      id: "t1",
      display_title: "After refine",
      refine_status: "refined",
      summary: "Refine succeeded.",
    },
  };
  const out = app.renderCaptureBubbleBody(message);
  assert.match(out, /After refine/);
  assert.match(out, /Refine succeeded/);
  assert.doesNotMatch(out, /<stale\/>/);
});

test("renderCaptureBubbleBody falls back to stored html/text for non-bound messages", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  const out = app.renderCaptureBubbleBody({ role: "system", text: "hello" });
  assert.match(out, /<div class="tf-msg-body">hello<\/div>/);
  const htmlOut = app.renderCaptureBubbleBody({ role: "ai", html: "<b>static</b>" });
  assert.match(htmlOut, /<b>static<\/b>/);
});

test("formatPatchFeedback picks the right message per PATCH shape", () => {
  const app = loadAppFunctions();
  const snap = { thought: { id: "t1" } };
  assert.match(app.formatPatchFeedback({ title: "新标题" }, snap), /capture\.feedback\.renamed/);
  assert.match(app.formatPatchFeedback({ tags: ["a", "b"] }, snap), /capture\.feedback\.tags_added/);
  assert.match(app.formatPatchFeedback({ ai_notes_append: "x" }, snap), /capture\.feedback\.note_appended/);
  app.appState.topics = [{ id: "tt1", name: "研究专题" }];
  assert.match(app.formatPatchFeedback({ topic_ids: ["tt1"] }, snap), /capture\.feedback\.moved_to_topic/);
  // Unknown patch shapes still produce a sensible saved-path message.
  assert.match(app.formatPatchFeedback({}, snap), /capture\.session\.saved_path/);
});
