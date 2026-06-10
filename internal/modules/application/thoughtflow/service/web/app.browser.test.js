const assert = require("node:assert/strict");
const fs = require("node:fs");
const http = require("node:http");
const os = require("node:os");
const path = require("node:path");
const { spawn } = require("node:child_process");
const test = require("node:test");

const chromePath = process.env.CHROME_PATH || findChrome();
const browserTargets = discoverBrowserTargets();

test("embedded UI browser smoke matrix", async (t) => {
  const server = await startFixtureServer();
  t.after(() => server.close());

  const baseURL = `http://127.0.0.1:${server.address().port}`;
  for (const target of browserTargets) {
    await t.test(target.name, { skip: target.skip }, async (browserTest) => {
      for (const viewport of viewports()) {
        await browserTest.test(viewport.name, async () => {
          const browser = await target.launch(viewport);
          try {
            await runBrowserSmoke(browser, `${baseURL}/`);
          } finally {
            await browser.close();
          }
        });
      }
    });
  }
});

function discoverBrowserTargets() {
  return [
    {
      name: "chrome",
      skip: chromePath ? false : "Chrome executable not found",
      launch: launchChrome,
    },
    {
      name: "firefox",
      skip: firefoxSkipReason(),
      launch: async () => {
        throw new Error("Firefox automation is unavailable");
      },
    },
    {
      name: "safari",
      skip: safariSkipReason(),
      launch: async () => {
        throw new Error("Safari automation is unavailable");
      },
    },
  ];
}

function viewports() {
  return [
    { name: "desktop", width: 1280, height: 800 },
    { name: "mobile", width: 390, height: 844 },
  ];
}

async function runBrowserSmoke(browser, url) {
  const page = await connectPage(browser);
  const errors = [];
  page.onEvent("Runtime.exceptionThrown", (event) => errors.push(event.exceptionDetails?.text || "runtime exception"));
  page.onEvent("Log.entryAdded", (event) => {
    if (event.entry?.level === "error") errors.push(event.entry.text);
  });
  await page.send("Runtime.enable");
  await page.send("Log.enable");
  await page.send("Page.enable");
  await page.navigate(url);
  await page.waitForExpression(() => document.querySelector("#system-status")?.textContent.includes("browser"));
  await page.waitForExpression(() => document.querySelectorAll(".topic-item").length === 1);
  await page.waitForExpression(() => document.querySelectorAll(".result-item").length === 1);

  const state = await page.evaluate(() => {
    document.querySelector('[data-tab="review"]').click();
    const reviewActive = document.querySelector("#tab-review").classList.contains("active");
    document.querySelector('[data-tab="synthesis"]').click();
    const synthesisActive = document.querySelector("#tab-synthesis").classList.contains("active");
    const shell = document.querySelector(".app-shell").getBoundingClientRect();
    const capture = document.querySelector("#capture-form").getBoundingClientRect();
    const clientWidth = document.documentElement.clientWidth;
    const wideElements = Array.from(document.querySelectorAll("body *"))
      .map((node) => {
        const rect = node.getBoundingClientRect();
        return {
          tag: node.tagName.toLowerCase(),
          id: node.id,
          className: String(node.className || ""),
          width: Math.round(rect.width),
          right: Math.round(rect.right),
        };
      })
      .filter((item) => item.right > clientWidth + 4)
      .slice(0, 8);
    return {
      title: document.querySelector("h1")?.textContent,
      status: document.querySelector("#system-status")?.textContent,
      topicItems: document.querySelectorAll(".topic-item").length,
      searchItems: document.querySelectorAll(".result-item").length,
      reviewActive,
      synthesisActive,
      shellWidth: shell.width,
      captureWidth: capture.width,
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth,
      wideElements,
    };
  });

  assert.equal(state.title, "ThoughtFlow");
  assert.match(state.status, /browser \/ ready/);
  assert.equal(state.topicItems, 1);
  assert.equal(state.searchItems, 1);
  assert.equal(state.reviewActive, true);
  assert.equal(state.synthesisActive, true);
  assert.ok(state.shellWidth > 0);
  assert.ok(state.captureWidth > 0);
  assert.ok(state.scrollWidth <= state.clientWidth + 4, `horizontal overflow: ${JSON.stringify(state)}`);
  assert.deepEqual(errors, []);
}

function findChrome() {
  for (const candidate of [
    findExecutable("google-chrome"),
    findExecutable("google-chrome-stable"),
    findExecutable("chromium"),
    findExecutable("chromium-browser"),
    "/usr/bin/google-chrome",
    "/usr/bin/chromium",
    "/usr/bin/chromium-browser",
  ]) {
    if (fs.existsSync(candidate)) return candidate;
  }
  return "";
}

function firefoxSkipReason() {
  const firefoxPath = process.env.FIREFOX_PATH || findFirefox();
  const geckodriverPath = process.env.GECKODRIVER_PATH || findExecutable("geckodriver");
  if (!firefoxPath) return "Firefox executable not found";
  if (isUnavailableSnapWrapper(firefoxPath)) return "Firefox snap wrapper is present but Firefox is not installed";
  if (!geckodriverPath) return "geckodriver executable not found";
  return "Firefox WebDriver smoke is not wired yet";
}

function safariSkipReason() {
  if (process.platform !== "darwin") return "Safari/WebKit automation is unavailable on this Linux test host";
  return "Safari WebDriver smoke is not wired yet";
}

function findFirefox() {
  return findExecutable("firefox") || findExecutable("firefox-esr");
}

function findExecutable(name) {
  const paths = String(process.env.PATH || "").split(path.delimiter);
  for (const dir of paths) {
    const candidate = path.join(dir, name);
    if (fs.existsSync(candidate)) return candidate;
  }
  return "";
}

function isUnavailableSnapWrapper(filePath) {
  try {
    const body = fs.readFileSync(filePath, "utf8");
    return body.includes("requires the firefox snap to be installed");
  } catch {
    return false;
  }
}

test("browser smoke matrix declares cross-browser targets", () => {
  assert.deepEqual(browserTargets.map((target) => target.name), ["chrome", "firefox", "safari"]);
});

function startFixtureServer() {
  const webRoot = __dirname;
  const api = (data) => JSON.stringify({ request_id: "browser-test", data, error: null });
  const server = http.createServer((req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    switch (url.pathname) {
      case "/":
      case "/index.html":
        return serveFile(res, path.join(webRoot, "index.html"), "text/html; charset=utf-8");
      case "/styles.css":
        return serveFile(res, path.join(webRoot, "styles.css"), "text/css; charset=utf-8");
      case "/app.js":
        return serveFile(res, path.join(webRoot, "app.js"), "application/javascript; charset=utf-8");
      case "/vendor/markdown-it.min.js":
        return serveFile(res, path.join(webRoot, "vendor", "markdown-it.min.js"), "application/javascript; charset=utf-8");
      case "/favicon.ico":
        res.writeHead(204);
        return res.end();
      case "/api/system/status":
        return json(res, api({ status: "ready", workspace: { id: "browser" } }));
      case "/api/topics":
        if (req.method === "GET") {
          return json(res, api([{ id: "demo", name: "Demo Topic", member_count: 1, word_count: 12, description: "Browser smoke" }]));
        }
        return json(res, api({ id: "demo", name: "Demo Topic" }), 201);
      case "/api/topics/demo":
        return json(res, api({
          topic: { id: "demo", name: "Demo Topic", rules: {}, auto_weave: true },
          document: "# Demo Topic\n\nBrowser smoke document.",
          members: [],
        }));
      case "/api/topics/demo/weave-proposals":
      case "/api/synthesis":
        return json(res, api([]));
      case "/api/search":
        return json(res, api({
          items: [{
            thought_id: "thought-1",
            title: "Browser Thought",
            snippet: "Smoke result",
            score: 1,
            keyword_score: 1,
            semantic_score: 0,
            recency_score: 0,
            tags: ["ui"],
          }],
          page: 1,
          page_size: 20,
          total: 1,
        }));
      case "/api/events":
        res.writeHead(200, {
          "Content-Type": "text/event-stream",
          "Cache-Control": "no-cache",
          "Connection": "keep-alive",
        });
        res.write(": browser smoke\n\n");
        return;
      default:
        res.writeHead(404);
        res.end("not found");
    }
  });
  return new Promise((resolve) => server.listen(0, "127.0.0.1", () => resolve(server)));
}

function serveFile(res, filePath, contentType) {
  res.writeHead(200, { "Content-Type": contentType });
  fs.createReadStream(filePath).pipe(res);
}

function json(res, body, status = 200) {
  res.writeHead(status, { "Content-Type": "application/json; charset=utf-8" });
  res.end(body);
}

async function launchChrome(viewport) {
  const userDataDir = fs.mkdtempSync(path.join(os.tmpdir(), "thoughtflow-chrome-"));
  const chrome = spawn(chromePath, [
    "--headless=new",
    "--disable-gpu",
    "--no-sandbox",
    "--disable-dev-shm-usage",
    "--remote-debugging-port=0",
    `--user-data-dir=${userDataDir}`,
    `--window-size=${viewport.width},${viewport.height}`,
    "about:blank",
  ], { stdio: ["ignore", "pipe", "pipe"] });
  const wsURL = await new Promise((resolve, reject) => {
    let settled = false;
    let output = "";
    const fail = (error) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      chrome.kill("SIGTERM");
      cleanupChromeUserDataDir(userDataDir);
      reject(error);
    };
    const pass = (url) => {
      if (settled) return;
      settled = true;
      clearTimeout(timer);
      resolve(url);
    };
    const timer = setTimeout(() => {
      fail(new Error(`timed out waiting for Chrome DevTools endpoint from ${chromePath}: ${output.trim()}`));
    }, 20000);
    const onData = (chunk) => {
      output += chunk;
      if (output.length > 4000) output = output.slice(-4000);
      const match = chunk.match(/DevTools listening on (ws:\/\/[^\s]+)/);
      if (match) {
        pass(match[1]);
      }
    };
    chrome.stdout.setEncoding("utf8");
    chrome.stderr.setEncoding("utf8");
    chrome.stdout.on("data", onData);
    chrome.stderr.on("data", onData);
    chrome.once("exit", (code) => {
      fail(new Error(`Chrome exited before DevTools endpoint was ready: ${code}; output: ${output.trim()}`));
    });
    chrome.once("error", fail);
  });
  return {
    wsURL,
    async close() {
      chrome.kill("SIGTERM");
      if (!await waitForProcessExit(chrome, 5000)) {
        chrome.kill("SIGKILL");
        await waitForProcessExit(chrome, 5000);
      }
      cleanupChromeUserDataDir(userDataDir);
    },
  };
}

function waitForProcessExit(child, timeout) {
  if (child.exitCode !== null || child.signalCode !== null) return Promise.resolve(true);
  return new Promise((resolve) => {
    const timer = setTimeout(() => {
      child.off("exit", onExit);
      resolve(false);
    }, timeout);
    const onExit = () => {
      clearTimeout(timer);
      resolve(true);
    };
    child.once("exit", onExit);
  });
}

function cleanupChromeUserDataDir(userDataDir) {
  try {
    fs.rmSync(userDataDir, { recursive: true, force: true, maxRetries: 10, retryDelay: 200 });
  } catch (error) {
    process.emitWarning(`failed to remove Chrome user data dir ${userDataDir}: ${error.message}`);
  }
}

async function connectPage(browser) {
  const { port } = new URL(browser.wsURL);
  const targets = await getJSON(`http://127.0.0.1:${port}/json/list`);
  const target = targets.find((item) => item.type === "page" && item.webSocketDebuggerUrl);
  assert.ok(target, "Chrome page target not found");
  return new CDPPage(target.webSocketDebuggerUrl);
}

function getJSON(url) {
  return new Promise((resolve, reject) => {
    http.get(url, (res) => {
      let data = "";
      res.on("data", (chunk) => { data += chunk; });
      res.on("end", () => {
        try {
          resolve(JSON.parse(data));
        } catch (error) {
          reject(error);
        }
      });
    }).on("error", reject);
  });
}

class CDPPage {
  constructor(wsURL) {
    this.nextID = 1;
    this.pending = new Map();
    this.listeners = new Map();
    this.ready = new Promise((resolve, reject) => {
      this.ws = new WebSocket(wsURL);
      this.ws.addEventListener("open", resolve, { once: true });
      this.ws.addEventListener("error", reject, { once: true });
      this.ws.addEventListener("message", (event) => this.handleMessage(event));
    });
  }

  onEvent(method, handler) {
    this.listeners.set(method, handler);
  }

  async send(method, params = {}) {
    await this.ready;
    const id = this.nextID++;
    const promise = new Promise((resolve, reject) => this.pending.set(id, { resolve, reject }));
    this.ws.send(JSON.stringify({ id, method, params }));
    return promise;
  }

  async navigate(url) {
    const loaded = this.waitForEvent("Page.loadEventFired", 10000);
    await this.send("Page.navigate", { url });
    await loaded;
  }

  async evaluate(fn) {
    const result = await this.send("Runtime.evaluate", {
      expression: `(${fn.toString()})()`,
      returnByValue: true,
      awaitPromise: true,
    });
    if (result.exceptionDetails) {
      throw new Error(result.exceptionDetails.text || "evaluation failed");
    }
    return result.result.value;
  }

  async waitForExpression(fn, timeout = 10000) {
    const started = Date.now();
    while (Date.now() - started < timeout) {
      const ok = await this.evaluate(fn);
      if (ok) return;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    throw new Error("timed out waiting for browser expression");
  }

  waitForEvent(method, timeout) {
    return new Promise((resolve, reject) => {
      const previous = this.listeners.get(method);
      const timer = setTimeout(() => {
        this.listeners.set(method, previous);
        reject(new Error(`timed out waiting for ${method}`));
      }, timeout);
      this.listeners.set(method, (event) => {
        clearTimeout(timer);
        this.listeners.set(method, previous);
        if (previous) previous(event);
        resolve(event);
      });
    });
  }

  handleMessage(event) {
    const message = JSON.parse(event.data);
    if (message.id && this.pending.has(message.id)) {
      const { resolve, reject } = this.pending.get(message.id);
      this.pending.delete(message.id);
      if (message.error) reject(new Error(message.error.message));
      else resolve(message.result || {});
      return;
    }
    if (message.method && this.listeners.has(message.method)) {
      this.listeners.get(message.method)(message.params || {});
    }
  }
}
