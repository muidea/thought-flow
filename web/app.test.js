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

const classSet = (initialClasses = []) => {
  const set = new Set(initialClasses);
  return {
    add: (...names) => names.forEach((name) => set.add(name)),
    remove: (...names) => names.forEach((name) => set.delete(name)),
    toggle: (name, enabled) => {
      if (enabled) set.add(name);
      else set.delete(name);
    },
    contains: (name) => set.has(name),
    has: (name) => set.has(name),
    toString: () => Array.from(set).join(" "),
  };
};

function makeElementStub(tagName = "div") {
  const node = {
    tagName: String(tagName).toUpperCase(),
    dataset: {},
    attributes: {},
    children: [],
    hidden: false,
    style: {},
    _className: "",
    _innerHTML: "",
    textContent: "",
    get className() { return this._className; },
    set className(value) {
      this._className = String(value || "");
      this.classList = classSet(this._className.split(/\s+/).filter(Boolean));
    },
    get innerHTML() { return this._innerHTML; },
    set innerHTML(value) {
      this._innerHTML = String(value || "");
      this.children = [];
    },
    get lastChild() {
      return this.children[this.children.length - 1] || null;
    },
    classList: classSet(),
    setAttribute(name, value) {
      this.attributes[name] = String(value);
      if (name === "class") this.className = value;
    },
    getAttribute(name) { return this.attributes[name]; },
    removeAttribute(name) { delete this.attributes[name]; },
    append(...items) {
      this.children.push(...items.filter(Boolean));
    },
    prepend(...items) {
      this.children.unshift(...items.filter(Boolean));
    },
    removeChild(child) {
      const index = this.children.indexOf(child);
      if (index >= 0) this.children.splice(index, 1);
      return child;
    },
    addEventListener: () => {},
    removeEventListener: () => {},
    focus: () => {},
    querySelectorAll(selector) {
      if (selector === ".event-item") {
        return this.children.filter((child) => child?.classList?.contains?.("event-item"));
      }
      return [];
    },
    cloneNode(deep = false) {
      const clone = makeElementStub(this.tagName);
      clone.className = this.className;
      clone.dataset = { ...this.dataset };
      clone.attributes = { ...this.attributes };
      clone.hidden = this.hidden;
      clone.style = { ...this.style };
      clone.innerHTML = this.innerHTML;
      clone.textContent = this.textContent;
      if (deep) clone.children = this.children.map((child) => child.cloneNode ? child.cloneNode(true) : child);
      return clone;
    },
  };
  return node;
}

// Build a minimal DOM stub with input values that the page serializers read
// and that restoreRoutePage writes. Backed by a plain object so tests can
// inspect and mutate inputs between operations. Other selectors return null.
function makeDomStub(initial = {}) {
  const store = { ...initial };
  const controls = ["search-query", "search-tags",
    "topic-filter", "event-type-filter",
    "thought-filter", "settings-drawer-event-type", "settings-drawer-event-resource",
    "settings-help-visible"];
  // Side-effect nodes (toast, source list, etc.) only need the methods the
  // app touches — they all swallow writes silently so callers don't crash
  // when the test doesn't drive them.
  const sideEffectNodes = new Set(["toast", "compose-source-count", "compose-source-list",
    "clear-compose-sources", "compose-sources-list", "compose-source-count-sources",
    "clear-compose-sources-tab", "open-compose-create", "open-compose-create-sources",
    "selected-count", "add-selected-compose", "clear-selected",
    "topic-rules-summary"]);
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
  const body = makeElementStub("body");
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
  nodes["dashboard-events"] = makeElementStub("div");
  nodes["settings-drawer-event-list"] = makeElementStub("div");
  const sessionsPanel = {
    dataset: {},
    addEventListener: () => {},
    removeEventListener: () => {},
    querySelectorAll: () => [],
    focus: () => {},
  };
  const sessionsDrawerClasses = new Set();
  nodes["capture-sessions-drawer"] = {
    hidden: true,
    attributes: {},
    classList: {
      add: (name) => sessionsDrawerClasses.add(name),
      remove: (name) => sessionsDrawerClasses.delete(name),
      contains: (name) => sessionsDrawerClasses.has(name),
    },
    setAttribute(name, value) { this.attributes[name] = String(value); },
    getAttribute(name) { return this.attributes[name]; },
    querySelector: (selector) => selector === ".tf-drawer-panel" ? sessionsPanel : null,
    querySelectorAll: () => [],
  };
  nodes["capture-sessions-toggle"] = {
    attributes: {},
    setAttribute(name, value) { this.attributes[name] = String(value); },
    getAttribute(name) { return this.attributes[name]; },
    focus: () => {},
  };
  nodes["capture-sessions-list"] = {
    innerHTML: "",
    querySelectorAll: () => [],
  };
  nodes["capture-conversation"] = {
    innerHTML: "",
    scrollTop: 0,
    scrollHeight: 0,
  };
  nodes["capture-refresh-preview"] = { disabled: false };
  nodes["capture-archive-commit"] = { disabled: false };
  nodes["capture-archive-preview"] = { innerHTML: "" };
  nodes["capture-lock-indicator"] = {
    hidden: true,
    setAttribute: () => {},
    classList: classSet(),
  };
  nodes["capture-composer-input"] = {
    id: "capture-composer-input",
    value: store["capture-composer-input"] || "",
    form: null,
  };
  nodes["capture-composer-send"] = {
    disabled: false,
    textContent: "",
    dataset: {},
  };
  nodes["capture-composer"] = {
    requestSubmit: () => {},
    addEventListener: () => {},
  };
  nodes["capture-composer-input"].form = nodes["capture-composer"];
  nodes["search-results"] = {
    get innerHTML() { return store["search-results_innerHTML"] || ""; },
    set innerHTML(v) { store["search-results_innerHTML"] = String(v); },
    querySelectorAll: () => [],
  };
  let thoughtListItems = [];
  nodes["thought-list"] = {
    get innerHTML() { return store["thought-list_innerHTML"] || ""; },
    set innerHTML(v) {
      store["thought-list_innerHTML"] = String(v);
      thoughtListItems = Array.from(String(v).matchAll(/data-thought-id="([^"]+)"/g)).map((match) => {
        const item = {
          dataset: { thoughtId: match[1] },
          handlers: {},
          addEventListener(name, handler) { this.handlers[name] = handler; },
        };
        return item;
      });
    },
    querySelectorAll: (selector) => {
      if (selector === ".result-item[data-thought-id]") return thoughtListItems;
      return [];
    },
  };
  nodes["thought-list-count"] = {
    textContent: "",
    setAttribute: (name, value) => { store[`thought-list-count_${name}`] = String(value); },
  };
  nodes["thought-detail"] = {
    get innerHTML() { return store["thought-detail_innerHTML"] || ""; },
    set innerHTML(v) { store["thought-detail_innerHTML"] = String(v); },
    classList: classSet(),
  };
  nodes["thought-status-detail"] = {
    get innerHTML() { return store["thought-status-detail_innerHTML"] || ""; },
    set innerHTML(v) { store["thought-status-detail_innerHTML"] = String(v); },
    classList: classSet(),
  };
  const noteTabs = ["notes-all", "notes-detail", "notes-status", "notes-runtime"].map((tab) => ({
    dataset: { tab },
    classList: classSet(tab === "notes-all" ? ["active"] : []),
  }));
  const notePanels = ["notes-all", "notes-detail", "notes-status", "notes-runtime"].map((tab) => ({
    id: `tab-${tab}`,
    classList: classSet(tab === "notes-all" ? ["active"] : []),
  }));
  nodes["page-thoughts"] = {
    dataset: { page: "thoughts" },
    querySelectorAll: (selector) => {
      if (selector === ".tab") return noteTabs;
      if (selector === ".tab-panel") return notePanels;
      return [];
    },
  };
  let topicListItems = [];
  nodes["topic-list"] = {
    get innerHTML() { return store["topic-list_innerHTML"] || ""; },
    set innerHTML(v) {
      store["topic-list_innerHTML"] = String(v);
      topicListItems = Array.from(String(v).matchAll(/data-topic-id="([^"]+)"/g)).map((match) => ({
        dataset: { topicId: match[1] },
        handlers: {},
        addEventListener(name, handler) { this.handlers[name] = handler; },
      }));
    },
    querySelectorAll: (selector) => {
      if (selector === ".topic-item") return topicListItems;
      return [];
    },
  };
  const topicTabs = ["topics-list", "topics-detail", "topics-rules"].map((tab) => ({
    dataset: { tab },
    classList: classSet(tab === "topics-list" ? ["active"] : []),
    disabled: tab !== "topics-list",
  }));
  const topicPanels = ["topics-list", "topics-detail", "topics-rules"].map((tab) => ({
    id: `tab-${tab}`,
    classList: classSet(tab === "topics-list" ? ["active"] : []),
  }));
  nodes["page-topics"] = {
    dataset: { page: "topics" },
    querySelectorAll: (selector) => {
      if (selector === ".tab") return topicTabs;
      if (selector === ".tab-panel") return topicPanels;
      return [];
    },
  };
  function find(selector) {
    if (selector === "#page-thoughts .tab.active") {
      return noteTabs.find((tab) => tab.classList.contains("active")) || null;
    }
    if (selector === "#page-topics .tab.active") {
      return topicTabs.find((tab) => tab.classList.contains("active")) || null;
    }
    const m = selector.match(/^#([\w-]+)$/);
    if (!m) return null;
    return nodes[m[1]] || null;
  }
  return {
    store,
    nodes,
    body,
    find,
    all: (selector) => {
      if (selector === "#settings-drawer-event-list .event-item") {
        return nodes["settings-drawer-event-list"].children.filter((child) => child?.classList?.contains?.("event-item"));
      }
      return [];
    },
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

function activeNotesTab(dom) {
  const tabs = dom.find("#page-thoughts").querySelectorAll(".tab");
  const active = tabs.find((tab) => tab.classList.contains("active"));
  return active?.dataset?.tab || "";
}

function activeTopicsTab(dom) {
  const tabs = dom.find("#page-topics").querySelectorAll(".tab");
  const active = tabs.find((tab) => tab.classList.contains("active"));
  return active?.dataset?.tab || "";
}

function loadAppFunctionsWith(opts = {}) {
  const appPath = path.join(__dirname, "app.js");
  const parserPath = path.join(__dirname, "vendor", "markdown-it.min.js");
  const parserCode = fs.readFileSync(parserPath, "utf8");
  const code = fs.readFileSync(appPath, "utf8")
    .replace(/\nboot\(\)\.catch\([\s\S]*$/, "");
  const dom = opts.dom || makeDomStub();
  const storage = opts.storage || makeStorageStub();
  // Note: do NOT override `globalThis` in the context object — the markdown-it
  // UMD wrapper uses it to attach `markdownit`, and shadowing it with `{}`
  // would hide the parser.
  const context = {
    document: {
      querySelector: (selector) => dom.find(selector),
      querySelectorAll: (selector) => dom.all(selector),
      createElement: (tagName) => makeElementStub(tagName),
      addEventListener: () => {},
      body: dom.body,
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
    fetch: opts.fetch || (async () => ({ ok: true, json: async () => ({ data: null }) })),
    EventSource: opts.EventSource || function EventSource() {},
    BroadcastChannel: opts.BroadcastChannel,
    console,
  };
  const result = vm.runInNewContext(
    `${parserCode}
    ${code}
    ({
      escapeHTML,
      renderMarkdown,
      renderTopicDocumentMarkdown,
      thoughtLinksForDisplay,
      renderComposeDraft,
      composeTitleFromContent,
      outlineFromText,
      outlineText,
      parseRoute,
      normalizeTopicsTabName,
      topicTabRouteValue,
      navItemClass,
      navItemAriaCurrent,
      statusBadge,
      renderSearchResultItem,
      renderComposeSourcesItem,
      renderThoughtsList,
      renderTopics,
      runSearch,
      renderSearchIdle,
      renderTopicCandidateImpact,
      renderTopicCandidates,
      renderTopicRules,
      createComposeSources,
      addToComposeSources,
      clearComposeSources,
      displayWorkspace,
      displayRuntimePath,
      buildRouteHash,
      restoreRoutePage,
      refreshRouteData,
      applyRoute,
      PAGE_SERIALIZERS,
      persistSources,
      restoreSources,
      isHelpVisible,
      setHelpVisible,
      applyHelpVisibility,
      trapFocus,
      classifyCaptureInput,
      parseCaptureCommand,
      appendCaptureMessage,
      renderCaptureThoughtCard,
      renderCaptureThoughtCardFromSnapshot,
      buildCaptureExpansionSections,
      formatPatchFeedback,
      upsertCaptureContextMessage,
      upsertArchivePreviewMessage,
      loadCaptureSessions,
      saveCaptureSessions,
      rememberCaptureSession,
      renderArchivePreviewCard,
      renderArchivePreviewBody,
      renderCaptureContextRows,
      renderDocumentProfileOptions,
      loadDocumentProfiles,
      aboutDocumentProfilesHTML,
      renderCaptureBubbleBody,
      handleCaptureComposerKeydown,
      handleTabClick,
      openCaptureSessionsDrawer,
      closeCaptureSessionsDrawer,
      renderCaptureSessionItem,
      switchCaptureSession,
      submitCaptureComposer,
      appendSessionMessage,
      deleteCaptureSession,
      connectEvents,
      appendEvent,
      DOMAIN_EVENT_TYPES,
      handleCaptureEvent,
      formatBadgeCount,
      computeSidebarBadgeCounts,
      appendExpansionSections,
      appState: state,
    });`,
    context,
    { filename: appPath },
  );
  if (opts.exposeState) result._state = result.appState;
  if (opts.exposeWindow) result._window = context.window;
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
  assert.deepEqual(route("#/about"), { page: "about", nav: "about", params: {}, query: {} });
  // Legacy /topics/{id}/review links land on the topic detail workspace.
  assert.deepEqual(route("#/topics/demo"), { page: "topics", nav: "topics", params: { topicId: "demo" }, query: {} });
  assert.deepEqual(route("#/topics/demo/review"), { page: "topics", nav: "topics", params: { topicId: "demo" }, query: { tab: "detail" } });
  // /topics?topic=...&tab=... is an alternate path syntax (no path id);
  // the topic id lives in the query and the page has no in-param topicId.
  assert.deepEqual(route("#/topics?topic=demo&tab=rules"), { page: "topics", nav: "topics", params: {}, query: { topic: "demo", tab: "rules" } });
  assert.deepEqual(route("#/notes?id=abc"), { page: "thoughts", nav: "notes", params: { thoughtId: "abc" }, query: { id: "abc" } });
  assert.deepEqual(route("#/write"), { page: "compose", nav: "compose", params: {}, query: {} });
  assert.deepEqual(route("#/write?tab=sources"), { page: "compose", nav: "compose", params: {}, query: { tab: "sources" } });
  assert.deepEqual(route("#/write?draft=d-1"), { page: "compose", nav: "compose", params: {}, query: { draft: "d-1" } });
  assert.deepEqual(route("#/compose?tab=basket"), { page: "dashboard", nav: "overview", params: {}, query: { tab: "basket" } });
  // The bare /notes segment opens the notes list with no thought selected;
  // ?id= selects a specific thought.
  assert.deepEqual(route("#/notes"), { page: "thoughts", nav: "notes", params: { thoughtId: "" }, query: {} });
});

test("parseRoute falls back to overview for unknown segments", () => {
  const app = loadAppFunctions();
  const route = (hash) => JSON.parse(JSON.stringify(app.parseRoute(hash)));
  // Any top-level segment that isn't in the live set (overview / capture /
  // search / topics / notes / write / about) falls through to overview. The
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

test("renderSearchResultItem exposes source actions without score details", () => {
  const app = loadAppFunctions();

  // SearchResultView 投影不再下放 score / explain 字段，Web 仅暴露
  // 内容相关字段和可执行动作。
  const html = app.renderSearchResultItem({
    thought_id: "thought-1",
    title: "Search Result",
    snippet: "Snippet",
    tags: ["ui"],
    path_hint: "thoughts/demo.md",
  }, { selected: true, activeTopicId: "topic-1" });

  assert.match(html, /data-select-id="thought-1" checked/);
  assert.doesNotMatch(html, /search\.score_label/);
  assert.doesNotMatch(html, /0\.91/);
  // score 字段不在主流程展示。
  assert.doesNotMatch(html, /0\.80/);
  assert.doesNotMatch(html, /0\.70/);
  assert.doesNotMatch(html, /0\.60/);
  assert.match(html, /data-compose-source-id="thought-1"/);
  assert.match(html, /data-compose-source-title="Search Result"/);
  assert.doesNotMatch(html, /data-weave-id="thought-1"/);
  assert.match(html, /thoughts\/demo\.md/);
  assert.doesNotMatch(html, /tf-explain/);
});

test("compose sources helper deduplicates and clears sources", () => {
  const app = loadAppFunctions();
  // Initial entries are full source objects keyed by (source_type, source_id).
  const sourceQueue = app.createComposeSources([
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "thought", source_id: "one", title: "duplicate" },
  ]);
  const values = (result) => JSON.parse(JSON.stringify(result));

  assert.deepEqual(values(sourceQueue.values()), [
    { source_type: "thought", source_id: "one", title: "One" },
  ]);
  // add() of a new (type, id) extends the source queue.
  assert.deepEqual(values(sourceQueue.add({ source_type: "search_result", source_id: "two" })), [
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "search_result", source_id: "two", title: "" },
  ]);
  // add() of a duplicate is a no-op (no error, no double entry).
  assert.deepEqual(values(sourceQueue.add({ source_type: "thought", source_id: "one", title: "ignored" })), [
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "search_result", source_id: "two", title: "" },
  ]);
  // addMany() iterates and deduplicates.
  assert.deepEqual(values(sourceQueue.addMany([
    { source_type: "search_result", source_id: "two" },
    { source_type: "topic_section", source_id: "three", title: "Three" },
  ])), [
    { source_type: "thought", source_id: "one", title: "One" },
    { source_type: "search_result", source_id: "two", title: "" },
    { source_type: "topic_section", source_id: "three", title: "Three" },
  ]);
  // clear() empties the source queue.
  assert.deepEqual(values(sourceQueue.clear()), []);
  assert.deepEqual(values(sourceQueue.values()), []);
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

test("renderTopicDocumentMarkdown hides front matter from topic detail", () => {
  const app = loadAppFunctions();

  const html = app.renderTopicDocumentMarkdown(`---
id: antd
type: topic
members:
  - 20260622-025934-aa1470

---

# AntD

## Notes

Topic body`);

  assert.doesNotMatch(html, /front-matter/);
  assert.doesNotMatch(html, /<dt>id<\/dt>/);
  assert.match(html, /<h1>AntD<\/h1>/);
  assert.match(html, /Topic body/);
});

test("thoughtLinksForDisplay hides internal topic backlink metadata", () => {
  const app = loadAppFunctions();

  const out = app.thoughtLinksForDisplay(`- [Design](https://example.test/design)

Topics:
- [[../../../topics/antd/index.md|AntD]] <!-- topic:antd -->

- [Source](https://example.test/source)`);

  assert.match(out, /Design/);
  assert.match(out, /Source/);
  assert.doesNotMatch(out, /Topics:/);
  assert.doesNotMatch(out, /topic:antd/);
  assert.doesNotMatch(out, /AntD/);
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

test("renderComposeDraft keeps source links out of the editable body", () => {
  const app = loadAppFunctions();

  const content = app.renderComposeDraft({
    content: "# Draft\n\nAlready cites [[thoughts/one.md]].",
    source_links: ["thoughts/one.md", "thoughts/two.md"],
  });

  assert.equal((content.match(/\[\[thoughts\/one\.md\]\]/g) || []).length, 1);
  assert.doesNotMatch(content, /\[\[thoughts\/two\.md\]\]/);
  assert.doesNotMatch(content, /### Sources/);
});

test("composeTitleFromContent prefers the first markdown heading", () => {
  const app = loadAppFunctions();

  assert.equal(app.composeTitleFromContent("## Final Thought\n\nBody", { goal: "Goal title" }), "Final Thought");
  assert.equal(app.composeTitleFromContent("Body only", { goal: "Goal title\nextra" }), "Goal title");
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
  assert.match(html, /data-candidate-thought=""/);
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
  assert.match(html, /data-candidate-thought="t1"/);
  assert.match(html, /data-candidate-ref="s1"/);
});

test("renderComposeSourcesItem exposes source metadata and actions", () => {
  const app = loadAppFunctions();

  const html = app.renderComposeSourcesItem({
    source_type: "thought",
    source_id: "thought-1",
    title: "Readable title",
  });

  assert.match(html, /tf-source-queue-item/);
  assert.match(html, /Readable title/);
  assert.match(html, /compose\.source_type\.thought/);
  assert.match(html, /data-compose-source-preview="thought-1"/);
  assert.match(html, /data-compose-source-remove="thought-1"/);
  assert.match(html, /compose\.source_id/);
});

test("outline helpers preserve one title per line", () => {
  const app = loadAppFunctions();

  const outline = app.outlineFromText("Background\n\nOpen Questions\n");

  assert.equal(JSON.stringify(outline), JSON.stringify([{ title: "Background" }, { title: "Open Questions" }]));
  assert.equal(app.outlineText(outline), "Background\nOpen Questions");
});

test("app.js reads i18n keys from window.tflow_i18n (lazy stub is identity)", () => {
  // The stub above returns the key itself, so the rendered HTML exposes
  // dotted keys instead of literal English — assert that the action labels
  // resolve through the i18n helper. The previous score labels are gone with
  // the explain block.
  const app = loadAppFunctions();
  const html = app.renderSearchResultItem({ thought_id: "x", title: "t", score: 0.1 }, { selected: false, activeTopicId: "" });
  assert.doesNotMatch(html, /search\.score_label/);
  assert.match(html, /search\.result\.add_source/);
  assert.doesNotMatch(html, /search\.keyword_label/);
  assert.doesNotMatch(html, /search\.semantic_label/);
  assert.doesNotMatch(html, /search\.recency_label/);
});

test("appendEvent feeds overview and drawer activity lists with dashboard limit", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom });

  app.appendEvent("compose.draft_created", JSON.stringify({
    resource_type: "compose",
    resource_id: "draft-1",
  }));

  const dashboard = dom.nodes["dashboard-events"];
  const drawer = dom.nodes["settings-drawer-event-list"];
  assert.equal(dashboard.children.length, 1);
  assert.equal(drawer.children.length, 1);
  assert.match(dashboard.children[0].innerHTML, /compose\.draft_created/);
  assert.match(dashboard.children[0].innerHTML, /compose:draft-1/);
  assert.equal(dashboard.children[0].dataset.eventType, "compose.draft_created");
  assert.equal(drawer.children[0].dataset.resourceId, "draft-1");

  for (let i = 0; i < 9; i += 1) {
    app.appendEvent("search.index_updated", JSON.stringify({
      resource_type: "thought",
      resource_id: `thought-${i}`,
    }));
  }

  assert.equal(dashboard.children.length, 8);
  assert.equal(drawer.children.length, 10);
  assert.match(dashboard.children[0].innerHTML, /thought:thought-8/);
  assert.doesNotMatch(dashboard.children.map((item) => item.innerHTML).join("\n"), /draft-1/);
});

test("connectEvents subscribes overview activity to every known domain event", () => {
  const dom = makeDomStub();
  const listeners = {};
  let sourceURL = "";
  function FakeEventSource(url) {
    sourceURL = url;
    this.addEventListener = (type, handler) => { listeners[type] = handler; };
  }
  const app = loadAppFunctionsWith({ dom, EventSource: FakeEventSource });

  app.connectEvents();

  assert.equal(sourceURL, "/api/events");
  assert.equal(dom.nodes["dashboard-events"].dataset.empty, "true");
  assert.equal(dom.nodes["settings-drawer-event-list"].dataset.empty, "true");
  for (const type of app.DOMAIN_EVENT_TYPES) {
    assert.equal(typeof listeners[type], "function", `${type} should be subscribed`);
  }
  assert.equal(typeof listeners["search.index_failed"], "function");
  assert.equal(typeof listeners["topic.refresh_started"], "function");
  assert.equal(typeof listeners["scratchpad.context_enrich_requested"], "function");
  assert.equal(typeof listeners["compose.draft_created"], "function");

  listeners["compose.draft_created"]({
    data: JSON.stringify({ resource_type: "compose", resource_id: "draft-2" }),
  });

  assert.equal(dom.nodes["dashboard-events"].dataset.empty, undefined);
  assert.equal(dom.nodes["settings-drawer-event-list"].dataset.empty, undefined);
  assert.equal(dom.nodes["dashboard-events"].children.length, 1);
  assert.match(dom.nodes["dashboard-events"].children[0].innerHTML, /compose:draft-2/);
});

test("buildRouteHash omits empty query fields and keeps the path clean", () => {
  const app = loadAppFunctions();

  assert.equal(app.buildRouteHash("search", {}, {}), "#/search");
  assert.equal(app.buildRouteHash("search", {}, { q: "rag" }), "#/search?q=rag");
  assert.equal(app.buildRouteHash("search", {}, { q: "rag", mode: "keyword" }), "#/search?q=rag&mode=keyword");
  // Empty values are dropped, null/undefined are dropped, so common default state
  // does not pollute the URL.
  assert.equal(app.buildRouteHash("search", {}, { q: "", mode: null, sort: undefined }), "#/search");
  // Topic detail / rules share the topics page. The
  // topic id is read from params and the active tab is encoded as
  // ?tab=... so deep-links land on the right pane.
  assert.equal(app.buildRouteHash("topics", { topicId: "ai-notes" }, { tab: "rules" }), "#/topics/ai-notes?tab=rules");
  assert.equal(app.buildRouteHash("topics", { topicId: "ai-notes" }, {}), "#/topics/ai-notes");
  assert.equal(app.buildRouteHash("compose", {}, { tab: "sources" }), "#/write?tab=sources");
  // Special characters are URL-encoded.
  assert.equal(app.buildRouteHash("search", {}, { q: "a b&c" }), "#/search?q=a%20b%26c");
});

test("PAGE_SERIALIZERS.search captures only the non-default state of inputs", () => {
  const dom = makeDomStub({ "search-query": "rag", "search-tags": "ai,notes" });
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  // Seed the global Set used by the serializer.
  app._state.selectedThoughts = new Set(["t-1", "t-2"]);
  const result = app.PAGE_SERIALIZERS.search();

  assert.equal(result.q, "rag");
  assert.equal(result.tags, "ai,notes");
  assert.equal(result.topic_id, undefined);
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

test("PAGE_SERIALIZERS.topics omits the selected topic detail default tab", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });
  app._state.route = { page: "topics", params: { topicId: "antd" }, query: {} };
  app.restoreRoutePage("topics", {});

  assert.equal(activeTopicsTab(dom), "topics-detail");
  assert.equal(JSON.stringify(app.PAGE_SERIALIZERS.topics()), "{}");

  app.restoreRoutePage("topics", { tab: "rules" });
  assert.equal(activeTopicsTab(dom), "topics-rules");
  assert.equal(JSON.stringify(app.PAGE_SERIALIZERS.topics()), JSON.stringify({ tab: "rules" }));
});

test("runSearch with empty criteria renders idle state without fetching", async () => {
  const dom = makeDomStub();
  let fetched = false;
  const app = loadAppFunctionsWith({
    dom,
    exposeState: true,
    fetch: async () => {
      fetched = true;
      return { ok: true, json: async () => ({ data: { results: [] } }) };
    },
  });
  app._state.activeTopicId = "topic-hidden";

  await app.runSearch({ preventDefault: () => {} });

  assert.equal(fetched, false);
  assert.match(dom.store["search-results_innerHTML"], /search\.idle/);
  assert.equal(Array.isArray(app._state.lastResults), true);
  assert.equal(app._state.lastResults.length, 0);
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
  assert.equal(dom.store["search-tags"], "rag,llm");
  assert.equal(dom.store["search-topic-id"] ?? "", "");
  assert.equal(dom.store["search-mode"] ?? "", "");
  assert.equal(dom.store["search-explain_checked"] ?? false, false);
  assert.deepEqual(Array.from(app._state.selectedThoughts), ["thought-7", "thought-8"]);
});

test("restoreRoutePage ignores unknown / malformed keys without throwing", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  // Non-string where a string is expected, plus unknown keys — must not throw.
  // Route restoration treats the URL as authoritative, so absent recognized
  // keys reset their controls to defaults instead of preserving stale UI.
  app._state.selectedThoughts = new Set(["keep"]);
  app.restoreRoutePage("search", { q: 7, mode: null, random: "thing" });
  assert.equal(dom.store["search-query"] ?? "", "");
  assert.equal(dom.store["search-mode"] ?? "", "");
  assert.deepEqual(Array.from(app._state.selectedThoughts), []);

  // Unknown page identifier is a no-op.
  app.restoreRoutePage("nope", { q: "rag" });
  assert.equal(dom.store["search-query"] ?? "", "");
});

test("restoreRoutePage hydrates topic state from query", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  app.restoreRoutePage("topics", { keyword: "ai" });
  assert.equal(dom.store["topic-filter"], "ai");

  app.restoreRoutePage("topics", {});
  assert.equal(dom.store["topic-filter"], "");
});

test("topics list item click navigates directly to topic detail", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true, exposeWindow: true });
  app._state.topics = [{ id: "antd", name: "AntD", description: "Design system", member_count: 1, word_count: 33 }];

  app.renderTopics();
  const [item] = dom.find("#topic-list").querySelectorAll(".topic-item");
  item.handlers.click({ target: { closest: () => null } });

  assert.equal(app._window.location.hash, "#/topics/antd");
});

test("normalizeTopicsTabName maps route query aliases to DOM tab ids", () => {
  const app = loadAppFunctions();

  assert.equal(app.normalizeTopicsTabName("detail"), "topics-detail");
  assert.equal(app.normalizeTopicsTabName("rules"), "topics-rules");
  assert.equal(app.normalizeTopicsTabName("topics-detail"), "topics-detail");
  assert.equal(app.normalizeTopicsTabName("topics-proposals"), "topics-detail");
  assert.equal(app.normalizeTopicsTabName("unknown"), "topics-detail");
  assert.equal(app.normalizeTopicsTabName(""), "topics-detail");
});

test("topicTabRouteValue writes stable URL aliases instead of DOM tab ids", () => {
  const app = loadAppFunctions();

  assert.equal(app.topicTabRouteValue("topics-detail"), "detail");
  assert.equal(app.topicTabRouteValue("topics-rules"), "rules");
  assert.equal(app.topicTabRouteValue("detail"), "detail");
});

test("renderTopicRules shows the simplified rule summary and hides inactive internals", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom });

  app.renderTopicRules({
    rules: {
      keywords: { any: ["web"], all: [], exclude: [] },
      tags: { any: ["crawler"] },
      semantic: { enabled: false, threshold: 0.75 },
      manual_include: [],
      manual_exclude: [],
    },
    outline: [{ title: "Notes" }, { title: "Open Questions" }],
  });

  const html = dom.find("#topic-rules-summary").innerHTML;
  assert.match(html, /topics\.rule\.keywords_any/);
  assert.match(html, /web/);
  assert.match(html, /topics\.rule\.tags_any/);
  assert.match(html, /crawler/);
  assert.doesNotMatch(html, /topics\.rule\.auto_weave/);
  assert.doesNotMatch(html, /topics\.rule\.keywords_all/);
  assert.doesNotMatch(html, /topics\.rule\.keywords_exclude/);
  assert.doesNotMatch(html, /topics\.rule\.manual_include/);
  assert.doesNotMatch(html, /topics\.rule\.manual_exclude/);
  assert.doesNotMatch(html, /topics\.rule\.semantic/);
  assert.doesNotMatch(html, /topics\.rule\.outline/);
});

test("handleTabClick scopes ordinary page tabs to their own page", () => {
  const app = loadAppFunctions();
  const classes = (initial = []) => {
    const set = new Set(initial);
    return {
      has: (name) => set.has(name),
      toggle: (name, enabled) => {
        if (enabled) set.add(name);
        else set.delete(name);
      },
    };
  };
  const tabA = { dataset: { tab: "compose-writing" }, classList: classes(["active"]) };
  const tabB = { dataset: { tab: "compose-sources" }, classList: classes() };
  const panelA = { id: "tab-compose-writing", classList: classes(["active"]) };
  const panelB = { id: "tab-compose-sources", classList: classes() };
  const outsideTab = { dataset: { tab: "notes-all" }, classList: classes(["active"]) };
  const page = {
    dataset: { page: "compose" },
    querySelectorAll: (selector) => {
      if (selector === ".tab") return [tabA, tabB];
      if (selector === ".tab-panel") return [panelA, panelB];
      return [];
    },
  };
  tabB.closest = (selector) => (selector === ".tf-page" ? page : null);
  tabA.closest = tabB.closest;
  outsideTab.closest = () => null;

  app.handleTabClick({ currentTarget: tabB });

  assert.equal(tabA.classList.has("active"), false);
  assert.equal(tabB.classList.has("active"), true);
  assert.equal(panelA.classList.has("active"), false);
  assert.equal(panelB.classList.has("active"), true);
  assert.equal(outsideTab.classList.has("active"), true);
});

test("applyRoute refreshes the target page data when navigation enters search", async () => {
  const dom = makeDomStub();
  const calls = [];
  const app = loadAppFunctionsWith({
    dom,
    fetch: async (url) => {
      calls.push(String(url));
      return {
        ok: true,
        json: async () => ({ data: { results: [] } }),
      };
    },
  });

  await app.applyRoute("#/search?q=rg");

  assert.equal(dom.store["search-query"], "rg");
  assert.ok(calls.some((url) => url.startsWith("/api/search?")));
  assert.match(dom.store["search-results_innerHTML"], /empty\.no_matching/);
});

test("persistSources writes through the backend and clears browser storage", async () => {
  const storage = makeStorageStub({
    "tflow.compose.sources": JSON.stringify({ sources: [{ source_type: "thought", source_id: "stale" }] }),
  });
  const calls = [];
  const app = loadAppFunctionsWith({
    storage,
    exposeState: true,
    fetch: async (url, options = {}) => {
      calls.push({ url: String(url), method: options.method || "GET", body: options.body || "" });
      return { ok: true, json: async () => ({ data: { sources: [] } }) };
    },
  });

  app._state.composeSources = new Map([
    ["thought::t-1", { source_type: "thought", source_id: "t-1", title: "T1" }],
    ["search_result::t-2", { source_type: "search_result", source_id: "t-2", title: "T2" }],
  ]);
  await app.persistSources();

  assert.equal(storage.data["tflow.compose.sources"], undefined);
  assert.equal(calls.length, 1);
  assert.equal(calls[0].method, "PUT");
  assert.equal(calls[0].url, "/api/compose/sources");
  assert.deepEqual(JSON.parse(calls[0].body).sources, [
    { source_type: "thought", source_id: "t-1", title: "T1" },
    { source_type: "search_result", source_id: "t-2", title: "T2" },
  ]);
});

test("persistSources clears the backend sources when no sources remain", async () => {
  const calls = [];
  const app = loadAppFunctionsWith({
    exposeState: true,
    fetch: async (url, options = {}) => {
      calls.push({ url: String(url), method: options.method || "GET" });
      return { ok: true, json: async () => ({ data: { sources: [] } }) };
    },
  });

  app._state.composeSources = new Map();
  await app.persistSources();

  assert.deepEqual(calls, [{ url: "/api/compose/sources", method: "DELETE" }]);
});

test("restoreSources reads from the backend only", async () => {
  const storage = makeStorageStub({
    "tflow.compose.sources": JSON.stringify({ sources: [{ source_type: "thought", source_id: "stale" }] }),
  });
  const app = loadAppFunctionsWith({
    storage,
    exposeState: true,
    fetch: async (url, options = {}) => {
      assert.equal(String(url), "/api/compose/sources");
      assert.equal(options.method || "GET", "GET");
      return {
        ok: true,
        json: async () => ({
          data: {
            sources: [
              { source_type: "thought", source_id: "t-1", title: "T1" },
              { source_type: "search_result", source_id: "t-2", title: "T2" },
            ],
          },
        }),
      };
    },
  });

  await app.restoreSources();
  const restored = JSON.parse(JSON.stringify(Array.from(app._state.composeSources.values())));
  assert.equal(storage.data["tflow.compose.sources"], undefined);
  assert.deepEqual(restored, [
    { source_type: "thought", source_id: "t-1", title: "T1" },
    { source_type: "search_result", source_id: "t-2", title: "T2" },
  ]);
});

test("usage guide visibility is controlled by settings storage", () => {
  const dom = makeDomStub();
  const storage = makeStorageStub({ "tflow.help.visible": "false" });
  const app = loadAppFunctionsWith({ dom, storage });

  assert.equal(app.isHelpVisible(), false);
  app.applyHelpVisibility();
  assert.equal(dom.body.classList.contains("tf-help-hidden"), true);
  assert.equal(dom.find("#settings-help-visible").checked, false);

  app.setHelpVisible(true);
  assert.equal(storage.data["tflow.help.visible"], "true");
  assert.equal(dom.body.classList.contains("tf-help-hidden"), false);
  assert.equal(dom.find("#settings-help-visible").checked, true);

  app.setHelpVisible(false);
  assert.equal(storage.data["tflow.help.visible"], "false");
  assert.equal(dom.body.classList.contains("tf-help-hidden"), true);
  assert.equal(dom.find("#settings-help-visible").checked, false);
});

test("createComposeSources deduplicates by source_type+source_id and supports clear", () => {
  const app = loadAppFunctions();
  // sourceQueue.values() returns objects created in the vm context, so flatten via
  // JSON before comparing against literals defined in this test realm.
  const flat = (arr) => JSON.parse(JSON.stringify(arr));

  const sourceQueue = app.createComposeSources();
  assert.equal(sourceQueue.size(), 0);
  assert.deepEqual(flat(sourceQueue.values()), []);

  // New entries appear in insertion order. add() returns the full values list.
  assert.deepEqual(flat(sourceQueue.add({ source_type: "thought", source_id: "a", title: "A" })), [
    { source_type: "thought", source_id: "a", title: "A" },
  ]);
  assert.deepEqual(flat(sourceQueue.add({ source_type: "search_result", source_id: "b" })), [
    { source_type: "thought", source_id: "a", title: "A" },
    { source_type: "search_result", source_id: "b", title: "" },
  ]);
  assert.equal(sourceQueue.size(), 2);

  // Same (source_type, source_id) twice — kept only once. The returned values
  // list reflects the unchanged state.
  assert.deepEqual(flat(sourceQueue.add({ source_type: "thought", source_id: "a", title: "ignored" })), [
    { source_type: "thought", source_id: "a", title: "A" },
    { source_type: "search_result", source_id: "b", title: "" },
  ]);
  assert.equal(sourceQueue.size(), 2);

  // Same source_id under a different source_type is a distinct source.
  assert.deepEqual(flat(sourceQueue.add({ source_type: "search_result", source_id: "a", title: "" })), [
    { source_type: "thought", source_id: "a", title: "A" },
    { source_type: "search_result", source_id: "b", title: "" },
    { source_type: "search_result", source_id: "a", title: "" },
  ]);
  assert.equal(sourceQueue.size(), 3);

  // addMany iterates all sources; malformed entries are silently dropped.
  sourceQueue.addMany([
    null,
    { source_type: "thought" },
    { source_id: "no-type" },
    { source_type: "topic_section", source_id: "t-1", title: "T" },
  ]);
  assert.equal(sourceQueue.size(), 4);
  assert.equal(sourceQueue.has({ source_type: "topic_section", source_id: "t-1" }), true);

  // clear empties the source queue.
  sourceQueue.clear();
  assert.equal(sourceQueue.size(), 0);
  assert.deepEqual(flat(sourceQueue.values()), []);
});

test("addToComposeSources accepts strings and source objects, defaults to thought", () => {
  // addToComposeSources is a side-effecting helper (it persists, broadcasts,
  // and renders) so it needs the dom + storage stubs.
  const dom = makeDomStub();
  const storage = makeStorageStub();
  const app = loadAppFunctionsWith({ dom, storage, exposeState: true });

  const flat = () => JSON.parse(JSON.stringify(Array.from(app._state.composeSources.values())));

  // A bare string defaults to source_type "thought".
  app.addToComposeSources(["t-1"]);
  assert.deepEqual(flat(), [{ source_type: "thought", source_id: "t-1", title: "" }]);

  // A second thought under the default sourceType — string path again.
  app.addToComposeSources(["t-2"]);
  assert.deepEqual(flat(), [
    { source_type: "thought", source_id: "t-1", title: "" },
    { source_type: "thought", source_id: "t-2", title: "" },
  ]);

  // Duplicate thought id is a no-op (no second entry, no error).
  app.addToComposeSources(["t-1"]);
  assert.equal(app._state.composeSources.size, 2);

  // Source objects override source_type and carry title metadata.
  app.addToComposeSources([
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
  app.addToComposeSources(
    [{ source_type: "capture_session", source_id: "c-1", title: "C1" }, "t-3"],
    "thought",
  );
  assert.equal(app._state.composeSources.size, 6);
  assert.deepEqual(flat(), [
    { source_type: "thought", source_id: "t-1", title: "" },
    { source_type: "thought", source_id: "t-2", title: "" },
    { source_type: "search_result", source_id: "s-1", title: "S1" },
    { source_type: "topic_section", source_id: "u-1", title: "U1" },
    { source_type: "capture_session", source_id: "c-1", title: "C1" },
    { source_type: "thought", source_id: "t-3", title: "" },
  ]);

  // clearComposeSources empties state without touching storage directly.
  app.clearComposeSources();
  assert.equal(app._state.composeSources.size, 0);
  assert.equal(flat().length, 0);
});

test("compose create actions are disabled until the source queue has sources", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });
  const create = dom.find("#open-compose-create");
  const createFromSources = dom.find("#open-compose-create-sources");

  app.clearComposeSources();
  assert.equal(create.disabled, true);
  assert.equal(createFromSources.disabled, true);

  app.addToComposeSources(["t-1"]);
  assert.equal(create.disabled, false);
  assert.equal(createFromSources.disabled, false);

  app.clearComposeSources();
  assert.equal(create.disabled, true);
  assert.equal(createFromSources.disabled, true);
});

test("navItemAriaCurrent marks the active page and clears others", () => {
  // Re-import without the dom stub so navItemAriaCurrent is exposed.
  const app = loadAppFunctions();
  const route = { page: "search", nav: "search", params: {}, query: {} };
  assert.equal(app.navItemAriaCurrent(route, "search"), "page");
  assert.equal(app.navItemAriaCurrent(route, "dashboard"), null);
  assert.equal(app.navItemAriaCurrent(route, "thoughts"), null);
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
  assert.deepEqual(parse("/save"), { kind: "commit" });
  assert.deepEqual(parse("/save new"), { kind: "commit", strategy: "new" });
  assert.deepEqual(parse("/save new thought"), { kind: "commit", strategy: "new" });
  assert.deepEqual(parse("/save update"), { kind: "commit", strategy: "update_thought" });
  assert.deepEqual(parse("/save update_thought"), { kind: "commit", strategy: "update_thought" });
  assert.deepEqual(parse("/save supplement"), { kind: "commit", strategy: "supplement" });
  assert.equal(app.parseCaptureCommand("归档"), null);
  assert.equal(app.parseCaptureCommand("保存"), null);
  assert.equal(app.parseCaptureCommand("save"), null);
  assert.equal(app.parseCaptureCommand("commit"), null);
  assert.equal(app.parseCaptureCommand("将上述内容进行归档"), null);
  assert.equal(app.parseCaptureCommand("请把当前会话保存为 Thought"), null);
  assert.equal(app.parseCaptureCommand("归档当前整理结果"), null);
  assert.equal(app.parseCaptureCommand("archive current session as thought"), null);
  assert.equal(app.parseCaptureCommand("将上述内容归档至新文件"), null);
  assert.equal(app.parseCaptureCommand("请把当前会话保存为一个新文件"), null);
  assert.equal(app.parseCaptureCommand("另存为新 Thought"), null);
  assert.equal(app.parseCaptureCommand("archive current session as new note"), null);
  assert.equal(app.parseCaptureCommand("我想讨论归档策略"), null);
  assert.equal(app.parseCaptureCommand("我想讨论归档到新文件的策略"), null);
  assert.equal(app.parseCaptureCommand("我想 commit 一段代码"), null);
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

test("capture composer Enter submits while modified Enter keeps editing", () => {
  const app = loadAppFunctions();
  let submitCount = 0;
  let prevented = 0;
  const target = {
    id: "capture-composer-input",
    form: {
      requestSubmit: () => { submitCount++; },
    },
  };

  app.handleCaptureComposerKeydown({
    key: "Enter",
    target,
    preventDefault: () => { prevented++; },
  });
  app.handleCaptureComposerKeydown({
    key: "Enter",
    shiftKey: true,
    target,
    preventDefault: () => { prevented++; },
  });
  app.handleCaptureComposerKeydown({
    key: "Enter",
    metaKey: true,
    target,
    preventDefault: () => { prevented++; },
  });

  assert.equal(submitCount, 1);
  assert.equal(prevented, 1);
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

test("computeSidebarBadgeCounts only shows badges for surfaces with a real enumerable collection", () => {
  const app = loadAppFunctions();
  const counts = app.computeSidebarBadgeCounts({
    notes: [{ id: "n1" }, { id: "n2" }],
    topics: [{ id: "a" }, { id: "b" }, { id: "c" }],
    composeDrafts: [{ id: "d1" }],
  });
  // The returned object comes from a different vm context, so we compare
  // via JSON to avoid prototype/reference-equality false negatives.
  assert.equal(JSON.stringify(counts), JSON.stringify({ notes: "2", topics: "3", compose: "1" }));

  // Missing slices should render as empty so the badges stay hidden.
  const empty = app.computeSidebarBadgeCounts({});
  assert.equal(JSON.stringify(empty), JSON.stringify({ notes: "", topics: "", compose: "" }));

  // Zero and non-finite inputs are treated as no data.
  const zeros = app.computeSidebarBadgeCounts({
    notes: [],
    topics: [],
    composeDrafts: [],
  });
  assert.equal(JSON.stringify(zeros), JSON.stringify({ notes: "", topics: "", compose: "" }));
});

test("restoreRoutePage keeps notes deep-link state without a manual ID input", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });

  app.restoreRoutePage("thoughts", { id: "thought-123" });

  assert.equal(Object.prototype.hasOwnProperty.call(dom.store, "thought-id"), false);
  assert.equal(activeNotesTab(dom), "notes-detail");
  app._state.activeThoughtId = "thought-123";
  assert.equal(JSON.stringify(app.PAGE_SERIALIZERS.thoughts()), JSON.stringify({ id: "thought-123" }));
});

test("restoreRoutePage preserves explicit notes tab over detail default", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom });

  app.restoreRoutePage("thoughts", { id: "thought-123", tab: "status" });

  assert.equal(activeNotesTab(dom), "notes-status");
});

test("notes list item click switches directly to detail tab", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({ dom, exposeState: true });
  app._state.notes = [{ id: "thought-123", display_title: "Thought 123", summary: "Summary" }];

  app.renderThoughtsList();
  const [item] = dom.find("#thought-list").querySelectorAll(".result-item[data-thought-id]");
  item.handlers.click({ target: { closest: () => null } });

  assert.equal(activeNotesTab(dom), "notes-detail");
});

test("restoreRoutePage hydrates compose sources tab from query", () => {
  const classes = (initial = []) => {
    const set = new Set(initial);
    return {
      has: (name) => set.has(name),
      toggle: (name, enabled) => {
        if (enabled) set.add(name);
        else set.delete(name);
      },
    };
  };
  const tabDrafts = { dataset: { tab: "compose-writing" }, classList: classes(["active"]) };
  const tabSources = { dataset: { tab: "compose-sources" }, classList: classes() };
  const panelDrafts = { id: "tab-compose-writing", classList: classes(["active"]) };
  const panelSources = { id: "tab-compose-sources", classList: classes() };
  const composePage = {
    querySelectorAll: (selector) => {
      if (selector === ".tab") return [tabDrafts, tabSources];
      if (selector === ".tab-panel") return [panelDrafts, panelSources];
      return [];
    },
  };
  const dom = makeDomStub();
  const baseFind = dom.find;
  dom.find = (selector) => (selector === "#page-compose" ? composePage : baseFind(selector));
  const app = loadAppFunctionsWith({ dom });

  app.restoreRoutePage("compose", { tab: "sources" });

  assert.equal(tabDrafts.classList.has("active"), false);
  assert.equal(tabSources.classList.has("active"), true);
  assert.equal(panelDrafts.classList.has("active"), false);
  assert.equal(panelSources.classList.has("active"), true);
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

test("renderCaptureThoughtCardFromSnapshot shows the complete archived typed document", () => {
  const app = loadAppFunctions();
  const archivedBody = `# Agent design

## 背景与问题

完整背景说明。

## 目标

完整目标说明。

## 方案设计

这里是不能被摘要替代的完整方案正文。`;
  const html = app.renderCaptureThoughtCardFromSnapshot({
    thought: {
      id: "design-1",
      display_title: "Agent design",
      capture_status: "captured",
      refine_status: "refined",
      summary: "简短加工摘要。",
      document_profile: {
        family: "design",
        profile_id: "builtin.design-doc",
        version: 1,
      },
    },
    content: { ai_notes: archivedBody },
  });

  assert.match(html, /capture\.card\.section_document/);
  assert.match(html, /builtin\.design-doc · v1/);
  assert.match(html, /这里是不能被摘要替代的完整方案正文/);
  assert.match(html, /<h2[^>]*>方案设计<\/h2>/);
  assert.ok(html.indexOf("这里是不能被摘要替代的完整方案正文") < html.indexOf("简短加工摘要"));
});

test("renderCaptureThoughtCardFromSnapshot keeps ordinary note cards compact", () => {
  const app = loadAppFunctions();
  const html = app.renderCaptureThoughtCardFromSnapshot({
    thought: {
      id: "note-1",
      display_title: "Note",
      summary: "普通笔记摘要。",
      document_profile: {
        family: "note",
        profile_id: "builtin.note",
        version: 1,
      },
    },
    content: { ai_notes: "不应在普通笔记卡片中完整展开" },
  });

  assert.doesNotMatch(html, /capture\.card\.section_document/);
  assert.doesNotMatch(html, /不应在普通笔记卡片中完整展开/);
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

test("renderCaptureBubbleBody re-renders thoughtId-bound bubbles as markdown from the active snapshot", () => {
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
      key_points: ["Question one?", "Question two?"],
      ai_tags: ["RAG", "LLM"],
    },
    content: {
      original: "Raw user capture",
    },
  };
  const out = app.renderCaptureBubbleBody(message);
  assert.doesNotMatch(out, /Raw user capture/);
  assert.match(out, /<strong>capture\.conversation\.summary<\/strong>/);
  assert.match(out, /<p>Refine succeeded\.<\/p>/);
  assert.match(out, /<strong>capture\.conversation\.key_points<\/strong>/);
  assert.match(out, /<li>Question one\?<\/li>/);
  assert.match(out, /<li>Question two\?<\/li>/);
  assert.doesNotMatch(out, /RAG, LLM/);
  assert.doesNotMatch(out, /tf-suggestion-card/);
  assert.doesNotMatch(out, /tf-capture-status-row/);
  assert.doesNotMatch(out, /<stale\/>/);
});

test("renderCaptureBubbleBody falls back to stored html/text for non-bound messages", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  const out = app.renderCaptureBubbleBody({ role: "system", text: "**hello**" });
  assert.match(out, /<strong>hello<\/strong>/);
  assert.match(out, /tf-msg-body-markdown/);
  const htmlOut = app.renderCaptureBubbleBody({ role: "ai", html: "<b>static</b>" });
  assert.match(htmlOut, /<b>static<\/b>/);
});

test("capture context is rendered as markdown conversation text", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_title: "Draft title",
      candidate_tags: ["draft"],
      candidate_summary: "**当前收敛结论**\n\n主题已经形成可继续推进的整理方向。",
    },
  };
  const first = app.upsertCaptureContextMessage();
  assert.equal(first.kind, "context");
  assert.equal(app._state.capture.messages.length, 1);
  const html = app.renderCaptureBubbleBody(first);
  assert.doesNotMatch(html, /capture\.context\.title/);
  assert.doesNotMatch(html, /tf-capture-message-card/);
  assert.match(html, /<strong>当前收敛结论<\/strong>/);
  assert.match(html, /主题已经形成可继续推进的整理方向/);
  assert.doesNotMatch(html, /Draft title/);

  app._state.capture.activeScratchpad.session_context.candidate_title = "Updated title";
  const updated = app.upsertCaptureContextMessage();
  assert.equal(updated.id, first.id);
  assert.equal(app._state.capture.messages.length, 1);
  assert.match(app.renderCaptureBubbleBody(updated), /主题已经形成可继续推进的整理方向/);
  assert.doesNotMatch(app.renderCaptureBubbleBody(updated), /Updated title/);
});

test("capture context text can render a pending placeholder and is moved to the latest turn", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.messages = [
    { id: "u1", role: "user", text: "first user turn" },
  ];
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_title: "First title",
      candidate_summary: "First summary",
      candidate_body: "First body draft",
    },
  };
  const pending = app.upsertCaptureContextMessage({ pending: true });
  assert.equal(app._state.capture.messages[app._state.capture.messages.length - 1].kind, "context");
  assert.match(app.renderCaptureBubbleBody(pending), /capture\.context\.pending/);

  app._state.capture.activeScratchpad.session_context = {
    candidate_title: "Updated title",
    candidate_summary: "Updated summary",
    candidate_body: "Updated body draft should stay out of the conversation card",
  };
  const resolved = app.upsertCaptureContextMessage();
  const resolvedHTML = app.renderCaptureBubbleBody(resolved);
  assert.equal(resolved.id, pending.id);
  assert.equal(app._state.capture.messages.length, 2);
  assert.doesNotMatch(resolvedHTML, /Updated body draft should stay out of the conversation card/);
  assert.match(resolvedHTML, /<p>Updated summary<\/p>/);
  assert.doesNotMatch(resolvedHTML, /Updated title/);
  assert.doesNotMatch(resolvedHTML, /tf-capture-context-card/);

  app._state.capture.messages.push({ id: "u2", role: "user", text: "second user turn" });
  app._state.capture.activeScratchpad.session_context = {
    candidate_title: "Second title",
    candidate_summary: "Second summary",
  };
  const moved = app.upsertCaptureContextMessage();
  assert.equal(app._state.capture.messages[app._state.capture.messages.length - 1].id, moved.id);
  assert.equal(app._state.capture.messages[app._state.capture.messages.length - 2].id, "u2");
});

test("capture context messages stay interleaved with their user turn snapshots", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.messages = [
    { id: "u1", role: "user", text: "first user turn" },
  ];
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_body: "First body",
      candidate_summary: "First summary",
    },
  };
  const first = app.upsertCaptureContextMessage();

  app._state.capture.messages.push({ id: "u2", role: "user", text: "second user turn" });
  app._state.capture.activeScratchpad.session_context = {
    candidate_body: "Second body",
    candidate_summary: "Second summary",
  };
  const secondPending = app.upsertCaptureContextMessage({ pending: true });
  const second = app.upsertCaptureContextMessage();

  assert.equal(first.id, app._state.capture.messages[1].id);
  assert.equal(app._state.capture.messages[0].id, "u1");
  assert.equal(app._state.capture.messages[1].kind, "context");
  assert.equal(app._state.capture.messages[2].id, "u2");
  assert.equal(app._state.capture.messages[3].id, second.id);
  assert.equal(secondPending.id, second.id);

  const firstHTML = app.renderCaptureBubbleBody(app._state.capture.messages[1]);
  const secondHTML = app.renderCaptureBubbleBody(app._state.capture.messages[3]);
  assert.match(firstHTML, /First summary/);
  assert.doesNotMatch(firstHTML, /Second body/);
  assert.doesNotMatch(secondHTML, /Second body/);
  assert.match(secondHTML, /Second summary/);
});

test("capture context restore keeps persisted message timeline without duplicating last context", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.messages = [
    { role: "user", text: "first user turn", at: "2026-06-18T00:00:00Z" },
    { role: "user", text: "second user turn", at: "2026-06-18T00:00:02Z" },
    { role: "ai", text: "second synthesized reply", at: "2026-06-18T00:00:03Z" },
  ];
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_summary: "second synthesized reply",
    },
  };

  const message = app.upsertCaptureContextMessage();

  assert.equal(message, null);
  assert.equal(app._state.capture.messages.length, 3);
  assert.equal(app._state.capture.messages[0].role, "user");
  assert.equal(app._state.capture.messages[1].role, "user");
  assert.equal(app._state.capture.messages[2].text, "second synthesized reply");
});

test("scratchpad context event replaces pending context with persisted ai reply", async () => {
  const app = loadAppFunctionsWith({
    exposeState: true,
    fetch: async (url) => {
      assert.match(url, /\/api\/capture\/sessions\/s1$/);
      return {
        ok: true,
        json: async () => ({
          data: {
            session_id: "s1",
            messages: [
              { role: "user", text: "current user turn", at: "2026-06-18T00:00:00Z" },
              { role: "ai", text: "synthesized reply", at: "2026-06-18T00:00:01Z" },
            ],
            session_context: {
              candidate_summary: "synthesized reply",
            },
          },
        }),
      };
    },
  });
  app._state.capture.sessionId = "s1";
  app._state.capture.messages = [
    { id: "u1", role: "user", text: "current user turn" },
    { id: "pending", role: "ai", kind: "context", pending: true, messageKey: "u1" },
  ];

  await app.handleCaptureEvent("scratchpad.context_updated", JSON.stringify({
    resource_id: "s1",
    payload: {},
  }));

  assert.deepEqual(app._state.capture.messages.map((msg) => [msg.role, msg.kind || "", msg.text || ""]), [
    ["user", "", "current user turn"],
    ["ai", "", "synthesized reply"],
  ]);
});

test("scratchpad context event auto-previews llm archive intent before commit", async () => {
  const calls = [];
  const app = loadAppFunctionsWith({
    exposeState: true,
    fetch: async (url, options = {}) => {
      calls.push({ url, method: options.method || "GET", body: options.body || "" });
      if (url === "/api/capture/sessions/s1") {
        return {
          ok: true,
          json: async () => ({
            data: {
              session_id: "s1",
              archive_intent: "llm",
              archive_strategy: "new",
              messages: [
                { role: "user", text: "将上述内容进行归档", at: "2026-06-18T00:00:00Z" },
                { role: "ai", text: "ready to archive", at: "2026-06-18T00:00:01Z" },
              ],
              session_context: {
                candidate_summary: "ready to archive",
                archive_intent: "llm",
                archive_strategy: "new",
              },
            },
          }),
        };
      }
      if (url === "/api/capture/sessions/s1/intent") {
        return { ok: true, json: async () => ({ data: { session_id: "s1" } }) };
      }
      if (url === "/api/capture/sessions/s1/archive/preview?strategy=new") {
        return {
          ok: true,
          json: async () => ({
            data: {
              preview: {
                title: "Archive title",
                body: "Archive body",
                tags: [],
                source_links: [],
                strategy: "new",
              },
            },
          }),
        };
      }
      if (url === "/api/capture/sessions/s1/archive") {
        return { ok: true, json: async () => ({ data: { thought_id: "thought-1" } }) };
      }
      if (url === "/api/thoughts/thought-1") {
        return { ok: true, json: async () => ({ data: { thought: { id: "thought-1" } } }) };
      }
      throw new Error(`unexpected fetch ${options.method || "GET"} ${url}`);
    },
  });
  app._state.capture.sessionId = "s1";

  await app.handleCaptureEvent("scratchpad.context_updated", JSON.stringify({
    resource_id: "s1",
    payload: {},
  }));

  assert.deepEqual(calls.map((call) => `${call.method} ${call.url}`), [
    "GET /api/capture/sessions/s1",
    "POST /api/capture/sessions/s1/intent",
    "GET /api/capture/sessions/s1/archive/preview?strategy=new",
    "POST /api/capture/sessions/s1/archive",
    "GET /api/thoughts/thought-1",
  ]);
  assert.equal(app._state.capture.activeThoughtId, "thought-1");
});

test("scratchpad context archive intent ignores duplicate event while commit is in flight", async () => {
  const calls = [];
  let releasePreview;
  const previewGate = new Promise((resolve) => { releasePreview = resolve; });
  let markPreviewStarted;
  const previewStarted = new Promise((resolve) => { markPreviewStarted = resolve; });
  const app = loadAppFunctionsWith({
    exposeState: true,
    fetch: async (url, options = {}) => {
      calls.push({ url, method: options.method || "GET" });
      if (url === "/api/capture/sessions/s1") {
        return {
          ok: true,
          json: async () => ({
            data: {
              session_id: "s1",
              archive_intent: "llm",
              archive_strategy: "new",
              messages: [
                { role: "user", text: "将上述内容进行归档", at: "2026-06-18T00:00:00Z" },
                { role: "ai", text: "ready to archive", at: "2026-06-18T00:00:01Z" },
              ],
              session_context: {
                candidate_summary: "ready to archive",
                archive_intent: "llm",
                archive_strategy: "new",
              },
            },
          }),
        };
      }
      if (url === "/api/capture/sessions/s1/intent") {
        return { ok: true, json: async () => ({ data: { session_id: "s1" } }) };
      }
      if (url === "/api/capture/sessions/s1/archive/preview?strategy=new") {
        markPreviewStarted();
        await previewGate;
        return {
          ok: true,
          json: async () => ({
            data: { preview: { title: "Archive title", body: "Archive body", strategy: "new" } },
          }),
        };
      }
      if (url === "/api/capture/sessions/s1/archive") {
        return { ok: true, json: async () => ({ data: { thought_id: "thought-1" } }) };
      }
      if (url === "/api/thoughts/thought-1") {
        return { ok: true, json: async () => ({ data: { thought: { id: "thought-1" } } }) };
      }
      throw new Error(`unexpected fetch ${options.method || "GET"} ${url}`);
    },
  });
  app._state.capture.sessionId = "s1";
  const event = JSON.stringify({ resource_id: "s1", payload: {} });

  const first = app.handleCaptureEvent("scratchpad.context_updated", event);
  await previewStarted;
  await app.handleCaptureEvent("scratchpad.context_updated", event);
  releasePreview();
  await first;

  const previewCalls = calls.filter((call) => call.url.includes("/archive/preview"));
  const commitCalls = calls.filter((call) => call.url.endsWith("/archive") && call.method === "POST");
  assert.equal(previewCalls.length, 1);
  assert.equal(commitCalls.length, 1);
});

test("opened committed capture history auto-archives llm intent after follow-up turns", async () => {
  const calls = [];
  const app = loadAppFunctionsWith({
    exposeState: true,
    fetch: async (url, options = {}) => {
      calls.push({ url, method: options.method || "GET", body: options.body || "" });
      if (url === "/api/capture/sessions/history-1") {
        return {
          ok: true,
          json: async () => ({
            data: {
              session_id: "history-1",
              committed_thought_id: "thought-1",
              archive_intent: "llm",
              archive_strategy: "new",
              content: "继续补充历史会话\n\n请将上述内容归档",
              messages: [
                { role: "user", text: "继续补充历史会话", at: "2026-06-18T00:00:00Z" },
                { role: "ai", text: "continued summary", at: "2026-06-18T00:00:01Z" },
                { role: "user", text: "请将上述内容归档", at: "2026-06-18T00:01:00Z" },
                { role: "ai", text: "ready to archive", at: "2026-06-18T00:01:01Z" },
              ],
              session_context: {
                candidate_summary: "ready to archive",
                candidate_body: "final body",
                archive_intent: "llm",
                archive_strategy: "new",
              },
            },
          }),
        };
      }
      if (url === "/api/capture/sessions/history-1/intent") {
        return { ok: true, json: async () => ({ data: { session_id: "history-1" } }) };
      }
      if (url === "/api/capture/sessions/history-1/archive/preview?strategy=new") {
        return {
          ok: true,
          json: async () => ({
            data: { preview: { title: "Archive title", body: "final body", strategy: "new" } },
          }),
        };
      }
      if (url === "/api/capture/sessions/history-1/archive") {
        return { ok: true, json: async () => ({ data: { thought_id: "thought-1" } }) };
      }
      if (url === "/api/thoughts/thought-1") {
        return { ok: true, json: async () => ({ data: { thought: { id: "thought-1" } } }) };
      }
      throw new Error(`unexpected fetch ${options.method || "GET"} ${url}`);
    },
  });
  app._state.capture.sessionId = "history-1";
  app._state.capture.activeThoughtId = "thought-1";

  await app.handleCaptureEvent("scratchpad.context_updated", JSON.stringify({
    resource_id: "history-1",
    payload: {},
  }));

  assert.deepEqual(calls.map((call) => `${call.method} ${call.url}`), [
    "GET /api/capture/sessions/history-1",
    "POST /api/capture/sessions/history-1/intent",
    "GET /api/capture/sessions/history-1/archive/preview?strategy=new",
    "POST /api/capture/sessions/history-1/archive",
    "GET /api/thoughts/thought-1",
  ]);
});

test("capture context text drops duplicate and low-signal LLM fields", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_body: "我想整理一个主题方向，用于后续继续讨论。\n\n我想整理一个主题方向，用于后续继续讨论。",
      candidate_summary: "我想整理一个主题方向",
      candidate_title: "我想整理一个主题方向",
      topic: "我想整理一个主题方向",
      goal: "持续收集并澄清当前主题",
      open_questions: [
        "最终产出更适合作为说明、提纲、草稿、方案还是行动清单？",
        "当前主题最重要的成功标准是什么？",
      ],
      conflicts: ["持续收集并澄清当前主题"],
      candidate_tags: ["整理", "主题"],
    },
  };
  const message = app.upsertCaptureContextMessage();
  const html = app.renderCaptureBubbleBody(message);

  assert.doesNotMatch(html, /我想整理一个主题方向，用于后续继续讨论。/);
  assert.match(html, /<strong>capture\.conversation\.questions<\/strong>/);
  assert.match(html, /最终产出更适合/);
  assert.match(html, /当前主题最重要的成功标准/);
  assert.doesNotMatch(html, /持续收集并澄清当前主题/);
  assert.doesNotMatch(html, /整理, 主题/);
});

test("capture context text prefers synthesized summary over mechanical context fields", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_body: "我想整理一个主题方向，用于后续继续讨论。",
      candidate_summary: [
        "## 当前收敛结论",
        "",
        "正在形成一个可继续讨论、加工或归档的主题材料。",
        "",
        "## 下一轮建议补充",
        "",
        "- 补充背景、边界和预期产出。",
      ].join("\n"),
      confirmed_facts: ["原始输入已经提供主题方向", "产出需要能用于后续归档或继续讨论"],
      open_questions: ["最终产出更适合作为说明、提纲、草稿、方案还是行动清单？"],
      candidate_tags: ["整理", "主题"],
    },
  };
  const message = app.upsertCaptureContextMessage();
  const html = app.renderCaptureBubbleBody(message);

  assert.doesNotMatch(html, /我想整理一个主题方向，用于后续继续讨论。/);
  assert.match(html, /当前收敛结论/);
  assert.match(html, /正在形成一个可继续讨论、加工或归档的主题材料/);
  assert.match(html, /下一轮建议补充/);
  assert.doesNotMatch(html, /capture\.conversation\.facts/);
  assert.doesNotMatch(html, /capture\.conversation\.questions/);
  assert.doesNotMatch(html, /原始输入已经提供主题方向/);
  assert.doesNotMatch(html, /最终产出更适合/);
  assert.doesNotMatch(html, /整理, 主题/);
});

test("capture context text keeps cumulative candidate body out of an ordinary reply", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_summary: "可以按一个可配置的采集工具继续收敛。",
      candidate_body: [
        "## 当前收敛结论",
        "",
        "这个需求更像是一个面向命令行和配置文件的 Web 采集工具，需要先明确目标输入、字段抽取、存储格式和运行边界。",
        "",
        "## 已确认约束",
        "",
        "- 使用 Golang 实现。",
        "- 目标站点可以通过配置文件或命令行输入。",
        "- 数据采用文件方式落盘。",
        "",
        "## 下一轮建议补充",
        "",
        "- 明确目标网站是否需要登录、验证码或 JS 渲染。",
        "- 确认输出文件格式采用 JSONL、CSV 还是分目录文件。",
      ].join("\n"),
    },
  };

  const message = app.upsertCaptureContextMessage();
  const html = app.renderCaptureBubbleBody(message);

  assert.match(html, /可以按一个可配置的采集工具继续收敛/);
	assert.doesNotMatch(html, /当前收敛结论/);
	assert.doesNotMatch(html, /Golang 实现/);
	assert.doesNotMatch(html, /JSONL、CSV/);
});

test("persisted complete context reply suppresses duplicate synthetic bubble", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  const ctx = {
    candidate_summary: "下面是更新后的结构化工作笔记。",
    candidate_body: "## 候选实体\n\n- Credential\n\n## 一致性约束\n\n- 同一类型只能有一条有效凭证。",
  };
  app._state.capture.sessionId = "s1";
  app._state.capture.activeScratchpad = { session_id: "s1", session_context: ctx };
  app._state.capture.messages = [
    { id: "u1", role: "user", text: "增加一致性约束" },
    { id: "a1", role: "ai", text: `${ctx.candidate_summary}\n\n${ctx.candidate_body}` },
  ];

  const message = app.upsertCaptureContextMessage();

  assert.equal(message, null);
  assert.equal(app._state.capture.messages.length, 2);
  assert.match(app.renderCaptureBubbleBody(app._state.capture.messages[1]), /一致性约束/);
});

test("capture context text strips raw input embedded in synthesized sections", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.activeScratchpad = {
    session_id: "s1",
    session_context: {
      candidate_body: "我想整理一个主题方向\n\n补充背景、目标和预期产出",
      candidate_summary: [
        "我想整理一个主题方向 补充背景、目标和预期产出",
        "",
        "**可展开方向**",
        "- 明确主题边界、受众、产出形式和判断标准。",
      ].join("\n"),
      open_questions: ["最终产出更适合作为说明、提纲、草稿、方案还是行动清单？"],
    },
  };
  const message = app.upsertCaptureContextMessage();
  const html = app.renderCaptureBubbleBody(message);

  assert.doesNotMatch(html, /我想整理一个主题方向/);
  assert.doesNotMatch(html, /补充背景、目标和预期产出/);
  assert.match(html, /明确主题边界、受众、产出形式和判断标准/);
  assert.doesNotMatch(html, /最终产出更适合/);
});

test("archive preview is rendered as markdown conversation text with a stored snapshot", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.capture.sessionId = "s1";
  app._state.capture.archivePreview = {
    title: "Preview title",
    strategy: "update_thought",
    thought_id: "thought-target",
    tags: ["capture"],
    body: "Preview body",
    diff: {
      before: "Old body",
      after: "Preview body",
      changed_fields: ["body"],
    },
  };
  const message = app.upsertArchivePreviewMessage();
  assert.equal(message.kind, "archive_preview");
  const html = app.renderCaptureBubbleBody(message);
  assert.doesNotMatch(html, /capture\.archive\.preview_title/);
  assert.doesNotMatch(html, /tf-capture-preview-card/);
  assert.match(html, /Preview title/);
  assert.match(html, /Preview body/);
  assert.match(html, /thought-target/);
  assert.match(html, /Old body/);

  app._state.capture.archivePreview = null;
  assert.match(app.renderCaptureBubbleBody(message), /Preview title/);
});

test("capture session history drawer opens with visible drawer state", () => {
  const dom = makeDomStub();
  const app = loadAppFunctionsWith({
    dom,
    exposeState: true,
    fetch: async () => ({ ok: true, json: async () => ({ data: { summaries: [] } }) }),
  });
  const drawer = dom.find("#capture-sessions-drawer");
  const toggle = dom.find("#capture-sessions-toggle");

  app.openCaptureSessionsDrawer();

  assert.equal(drawer.hidden, false);
  assert.equal(drawer.classList.contains("open"), true);
  assert.equal(drawer.getAttribute("aria-hidden"), "false");
  assert.equal(toggle.getAttribute("aria-expanded"), "true");

  app.closeCaptureSessionsDrawer();

  assert.equal(drawer.hidden, true);
  assert.equal(drawer.classList.contains("open"), false);
  assert.equal(drawer.getAttribute("aria-hidden"), "true");
  assert.equal(toggle.getAttribute("aria-expanded"), "false");
});

test("capture session history item uses session title and includes delete action", () => {
  const app = loadAppFunctions();
  const html = app.renderCaptureSessionItem({
    sessionId: "20260620-abcdef",
    title: "Web 采集程序需求收敛",
    updatedAt: "2026-06-20T10:20:30Z",
  });

  assert.match(html, /Web 采集程序需求收敛/);
  assert.doesNotMatch(html, /<span class="tf-sessions-label">20260620-abcdef<\/span>/);
  assert.match(html, /tf-sessions-delete/);
  assert.match(html, /data-session-id="20260620-abcdef"/);
});

test("capture session history is not persisted in browser storage", () => {
  const storage = makeStorageStub({
    "tflow.capture.sessions": JSON.stringify([{ sessionId: "legacy", title: "legacy content" }]),
  });
  const app = loadAppFunctionsWith({ storage, exposeState: true });

  const loaded = app.loadCaptureSessions();
  app._state.capture.sessions = [{ sessionId: "s1", title: "server only" }];
  app.saveCaptureSessions();
  app.rememberCaptureSession({
    sessionId: "s2",
    title: "in-memory only",
    messages: [{ role: "user", text: "must not be stored" }],
  });

  assert.equal(Array.isArray(loaded), true);
  assert.equal(loaded.length, 0);
  assert.equal(storage.data["tflow.capture.sessions"], undefined);
  assert.equal(app._state.capture.sessions[0].sessionId, "s2");
  assert.equal(app._state.capture.sessions[0].messages, undefined);
});

test("opened capture history session can continue through composer", async () => {
  const calls = [];
  const dom = makeDomStub({ "capture-composer-input": "继续完善历史会话" });
  const app = loadAppFunctionsWith({
    dom,
    exposeState: true,
    fetch: async (url, options = {}) => {
      calls.push({ url, method: options.method || "GET", body: options.body || "" });
      if (url === "/api/capture/sessions/history-1") {
        return {
          ok: true,
          json: async () => ({
            data: {
              session_id: "history-1",
              committed_thought_id: "thought-1",
              archive_preview: { title: "Archived", body: "Archived body", strategy: "new" },
              messages: [{ role: "ai", text: "Archived body" }],
            },
          }),
        };
      }
      return {
        ok: true,
        json: async () => ({
          data: {
            session_id: "history-1",
            committed_thought_id: "thought-1",
            messages: [
              { role: "ai", text: "Archived body" },
              { role: "user", text: "继续完善历史会话" },
            ],
          },
        }),
      };
    },
  });
  app._state.capture.sessions = [{
    sessionId: "history-1",
    thoughtId: "thought-1",
    title: "Archived session",
  }];

  app.switchCaptureSession("history-1");
  await Promise.resolve();
  await app.submitCaptureComposer({ preventDefault: () => {} });

  assert.equal(app._state.capture.sessionId, "history-1");
  assert.ok(
    calls.some((call) => call.method === "POST" && call.url === "/api/capture/sessions/history-1/messages"),
    JSON.stringify(calls),
  );
  const posted = calls.find((call) => call.method === "POST" && call.url === "/api/capture/sessions/history-1/messages");
  assert.match(posted.body, /继续完善历史会话/);
});

test("deleteCaptureSession removes one session and calls backend delete", async () => {
  const calls = [];
  const app = loadAppFunctionsWith({
    exposeState: true,
    fetch: async (url, options = {}) => {
      calls.push(`${options.method || "GET"} ${url}`);
      return { ok: true, json: async () => ({ data: { deleted: true } }) };
    },
  });
  app._state.capture.sessionId = "s2";
  app._state.capture.messages = [{ role: "user", text: "active" }];
  app._state.capture.sessions = [
    { sessionId: "s1", title: "保留的会话" },
    { sessionId: "s2", title: "删除的会话" },
  ];

  await app.deleteCaptureSession("s2");

  assert.deepEqual(calls, ["DELETE /api/capture/sessions/s2"]);
  assert.deepEqual(app._state.capture.sessions.map((item) => item.sessionId), ["s1"]);
  assert.equal(app._state.capture.sessionId, "");
  assert.equal(app._state.capture.messages.length, 0);
});

test("handleCaptureEvent ignores unrelated thought events for the current capture conversation", async () => {
  let fetchCalls = 0;
  const app = loadAppFunctionsWith({
    exposeState: true,
    fetch: async () => {
      fetchCalls++;
      return { ok: true, json: async () => ({ data: { thought: { id: "thought-other" } } }) };
    },
  });
  app._state.capture.sessionId = "s1";
  app._state.capture.activeThoughtId = "thought-capture";
  app._state.capture.messages = [
    { id: "m1", role: "ai", thoughtId: "thought-capture", text: "anchored thought" },
  ];

  await app.handleCaptureEvent("thought.refined", JSON.stringify({
    resource_id: "thought-other",
    payload: {},
  }));
  await app.handleCaptureEvent("thought.refine_failed", JSON.stringify({
    resource_id: "thought-other",
    payload: {},
  }));
  await app.handleCaptureEvent("thought.expanded", JSON.stringify({
    resource_id: "thought-other",
    payload: {},
  }));

  assert.equal(fetchCalls, 0);
  assert.equal(app._state.capture.messages.length, 1);
  assert.equal(app._state.capture.messages[0].thoughtId, "thought-capture");
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

test("document profile UI renders dynamic choices and strict validation issues", () => {
  const app = loadAppFunctionsWith({ exposeState: true });
  app._state.documentProfiles = [
    { name: "Note", description: "Flexible note", ref: { profile_id: "builtin.note", version: 1, family: "note" } },
    { name: "Design Doc", description: "Technical design", ref: { profile_id: "builtin.design-doc", version: 1, family: "design" } },
  ];
  const options = app.renderDocumentProfileOptions("builtin.design-doc", 1);
  assert.match(options, /builtin\.design-doc/);
  assert.match(options, /selected/);

  const context = app.renderCaptureContextRows({
    candidate_profile_id: "builtin.design-doc",
    candidate_profile_version: 1,
    candidate_document_family: "design",
    profile_confidence: 96,
    profile_match_reason: "Explicit design request",
  }, { includeBody: true }).join("");
  assert.match(context, /data-capture-profile-select/);
  assert.match(context, /96%/);
  assert.match(context, /Explicit design request/);

  const preview = app.renderArchivePreviewBody({
    title: "Design",
    body: "# Design",
    document_profile: { profile_id: "builtin.design-doc", version: 1 },
    validation: { status: "invalid", issues: [{ code: "missing", message: "Missing rollback section" }] },
  });
  assert.match(preview, /builtin\.design-doc/);
  assert.match(preview, /document_profile\.validation_invalid/);
  assert.match(preview, /Missing rollback section/);
});

test("about page document profile guide summarizes loaded profiles and registry issues", () => {
  const app = loadAppFunctionsWith();
  const html = app.aboutDocumentProfilesHTML([
    {
      name: "Team RFC",
      description: "Team design contract",
      enabled: true,
      ref: { profile_id: "custom.team-rfc", version: 2, family: "design" },
    },
    {
      name: "Disabled",
      enabled: false,
      ref: { profile_id: "custom.disabled", version: 1, family: "other" },
    },
  ], [{ code: "thoughtflow.profile.invalid" }]);
  assert.match(html, /Team RFC · v2/);
  assert.match(html, /design/);
  assert.doesNotMatch(html, /custom\.disabled/);
  assert.match(html, /about\.profiles\.issue_count/);
});

test("about page explains document profile purpose and lifecycle", () => {
  const html = fs.readFileSync(path.join(__dirname, "index.html"), "utf8");
  assert.match(html, /about\.profiles\.background_title/);
  assert.match(html, /about\.profiles\.concept_profile_body/);
  assert.match(html, /about\.profiles\.concept_lifecycle_body/);
  assert.match(html, /about\.profiles\.usage_case_3/);
  assert.match(html, /about\.profiles\.usage_avoid/);
  assert.match(html, /about\.profiles\.quickstart_step_5/);
  assert.match(html, /about\.profiles\.activation_title/);
  assert.doesNotMatch(html, /POST \/api\/document-profiles\/(validate|publish|reload)/);
  const profileGuide = html.match(/<section class="tf-about-advanced"[\s\S]*?<\/section>\s*<\/section>/)?.[0] || "";
  assert.doesNotMatch(profileGuide, /multi-turn conversations|selected source material|Capture should|Compose should/i);
});

test("document profile loader preserves the active compose selection across automatic refresh", async () => {
  const dom = makeDomStub();
  const select = {
    value: "custom.team-rfc",
    selectedOptions: [{ dataset: { version: "2" } }],
    innerHTML: "",
  };
  dom.nodes["compose-format"] = select;
  const app = loadAppFunctionsWith({
    dom,
    fetch: async () => ({
      ok: true,
      json: async () => ({
        data: {
          profiles: [
            { name: "Note", ref: { profile_id: "builtin.note", version: 1 } },
            { name: "Team RFC", ref: { profile_id: "custom.team-rfc", version: 2 } },
          ],
          issues: [],
        },
      }),
    }),
  });

  await app.loadDocumentProfiles();

  assert.match(select.innerHTML, /value="custom\.team-rfc"[^>]*selected/);
});
