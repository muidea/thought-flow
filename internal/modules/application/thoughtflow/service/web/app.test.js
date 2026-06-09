const assert = require("node:assert/strict");
const fs = require("node:fs");
const path = require("node:path");
const test = require("node:test");
const vm = require("node:vm");

function loadAppFunctions() {
  const appPath = path.join(__dirname, "app.js");
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
    fetch: async () => ({ ok: true, json: async () => ({ data: null }) }),
    EventSource: function EventSource() {},
    console,
  };
  return vm.runInNewContext(
    `${code}
    ({
      escapeHTML,
      renderMarkdown,
      renderDiff,
      renderSynthesisDraft,
      outlineFromText,
      outlineText
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
  assert.match(html, /<ol><li>First<\/li><li>Second<\/li><\/ol>/);
  assert.match(html, /<li class="task-item"><input type="checkbox" disabled checked>Done<\/li>/);
  assert.match(html, /<li class="task-item"><input type="checkbox" disabled>Todo<\/li>/);
  assert.match(html, /<hr>/);
  assert.match(html, /<em>emphasis<\/em>/);
  assert.match(html, /<del>removed<\/del>/);
  assert.match(html, /<img src=".\/attachments\/diagram\.png" alt="Diagram">/);
  assert.doesNotMatch(html, /<img src="javascript/);
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
