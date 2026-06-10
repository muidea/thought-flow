const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function loadAppFunctions() {
  const appPath = path.join(__dirname, "app.js");
  const parserPath = path.join(__dirname, "vendor", "markdown-it.min.js");
  const parserCode = fs.readFileSync(parserPath, "utf8");
  const code = fs.readFileSync(appPath, "utf8").replace(/\nboot\(\)\.catch\(\(error\) => toast\(error\.message\)\);\s*$/, "");
  const context = {
    document: {
      querySelector: () => null,
      querySelectorAll: () => [],
      addEventListener: () => {},
    },
    window: {
      clearTimeout: () => {},
      setTimeout: () => 0,
    },
    URLSearchParams,
    fetch: async () => ({ ok: true, json: async () => ({ data: null }) }),
    EventSource: function EventSource() {},
    console,
  };
  return vm.runInNewContext(
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
      statusBadge,
      renderSearchResultItem,
      createSynthesisBasket
    });`,
    context,
    { filename: appPath },
  );
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

  assert.deepEqual(route(""), { page: "dashboard", nav: "dashboard", params: {}, query: {} });
  assert.deepEqual(route("#/topics/demo"), { page: "topic-detail", nav: "topics", params: { topicId: "demo" }, query: {} });
  assert.deepEqual(route("#/topics/demo/review"), { page: "topic-review", nav: "topics", params: { topicId: "demo" }, query: {} });
  assert.deepEqual(route("#/thoughts?id=abc"), { page: "thoughts", nav: "thoughts", params: { thoughtId: "abc" }, query: { id: "abc" } });
  assert.deepEqual(route("#/jobs?id=job-1"), { page: "jobs", nav: "jobs", params: { jobId: "job-1" }, query: { id: "job-1" } });
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
  assert.match(html, /score 0\.91/);
  assert.match(html, /kw 0\.80/);
  assert.match(html, /sem 0\.70/);
  assert.match(html, /rec 0\.60/);
  assert.match(html, /data-basket-id="thought-1"/);
  assert.match(html, /data-weave-id="thought-1"/);
  assert.match(html, /thoughts\/demo\.md/);
  assert.match(html, /Score details/);
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
