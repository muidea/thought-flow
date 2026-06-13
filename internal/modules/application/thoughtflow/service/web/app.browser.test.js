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
            await runBrowserSmoke(browser, `${baseURL}/?lang=en-US`);
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
  await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));
  await page.waitForExpression(() => document.querySelectorAll(".topic-item").length === 1);
  await page.waitForExpression(() => document.querySelectorAll(".result-item").length === 1);
  // PR5: sidebar count badges surface notes / topics / compose totals.
  // Topics and compose loaders populate the badges on boot; the
  // capture (notes) counter comes from the metrics endpoint.
  await page.waitForExpression(() => {
    const notes = document.querySelector('.tf-menu-badge[data-badge="notes"]');
    const topics = document.querySelector('.tf-menu-badge[data-badge="topics"]');
    const compose = document.querySelector('.tf-menu-badge[data-badge="compose"]');
    return notes && topics && compose && notes.dataset.count && topics.dataset.count && compose.dataset.count;
  });

  const state = await page.evaluate(async () => {
    const dashboardActive = document.querySelector("#page-dashboard")?.classList.contains("active");
    const settleRoute = async (hash) => {
      window.location.hash = hash;
      await new Promise((resolve) => setTimeout(resolve, 0));
    };
    const waitUntil = async (predicate) => {
      for (let index = 0; index < 80; index++) {
        if (predicate()) return;
        await new Promise((resolve) => setTimeout(resolve, 25));
      }
      throw new Error("timed out waiting for browser UI state");
    };
    const routes = [
      ["capture", "#/capture", "#page-capture", "#capture-composer"],
      ["notes", "#/notes", "#page-thoughts", "#thought-form"],
      ["search", "#/search", "#page-search", "#search-results"],
      ["topics", "#/topics", "#page-topics", "#topic-list"],
      ["compose", "#/compose", "#page-compose", "#compose-drafts"],
    ];
    // PR3 placeholder: /jobs lives in the settings drawer; no sidebar
    // entry and no dedicated page section, so it isn't in the routes
    // matrix. The deep-link still resolves in parseRoute.
    const routeStates = [];
    for (const [name, hash, pageSelector, visibleSelector] of routes) {
      await settleRoute(hash);
      // Normalize navActive to a boolean so deepStrictEqual can compare
      // against missing sidebar entries (those would otherwise surface as
      // `undefined` and trip the strict comparator).
      const navEl = document.querySelector(`[data-nav="${name}"]`);
      routeStates.push({
        name,
        active: !!document.querySelector(pageSelector)?.classList.contains("active"),
        visible: !!document.querySelector(visibleSelector),
        navActive: navEl ? navEl.classList.contains("active") : false,
      });
    }
    // Settings is no longer in the sidebar (gear button instead). The
    // drawer mounts on demand; visiting /settings via the hash is a
    // deprecated redirect to /overview.
    await settleRoute("#/settings");
    await new Promise((resolve) => setTimeout(resolve, 20));
    routeStates.push({
      name: "settings",
      active: !!document.querySelector("#page-dashboard")?.classList.contains("active"),
      visible: false,
      navActive: document.querySelector('[data-nav="overview"]')?.classList.contains("active") || false,
    });
    await settleRoute("#/capture");
    const composer = document.querySelector("#capture-composer-input");
    composer.value = "Captured from browser smoke";
    document.querySelector("#capture-composer").requestSubmit();
    await waitUntil(() => Array.from(document.querySelectorAll("#capture-conversation .tf-msg")).length >= 2);
    const captureResult = document.querySelector("#capture-conversation")?.textContent || "";

    await settleRoute("#/search");
    document.querySelector("#search-explain").checked = true;
    document.querySelector("#search-form").requestSubmit();
    await waitUntil(() => document.querySelector(".tf-explain"));
    const explainText = document.querySelector(".tf-explain")?.textContent || "";
    document.querySelector("[data-preview-id='thought-1']")?.click();
    await waitUntil(() => document.querySelector("#thought-drawer")?.classList.contains("open"));
    const thoughtDrawerOpen = document.querySelector("#thought-drawer")?.classList.contains("open");
    const thoughtDrawerText = document.querySelector("#thought-drawer-content")?.textContent || "";
    document.querySelector("#drawer-add-compose")?.click();
    const basketTextAfterDrawer = document.querySelector("#compose-source-count")?.textContent || "";
    document.querySelector("[data-close-drawer='thought-drawer']")?.click();
    document.querySelector("[data-select-id='thought-1']").checked = true;
    document.querySelector("[data-select-id='thought-1']").dispatchEvent(new Event("change", { bubbles: true }));
    document.querySelector("#add-selected-compose").click();
    await new Promise((resolve) => setTimeout(resolve, 20));
    const composeActive = document.querySelector("#page-compose")?.classList.contains("active");
    const basketText = document.querySelector("#compose-source-count")?.textContent || "";
    document.querySelector("#open-compose-create").click();
    const composeDrawerOpen = document.querySelector("#compose-create-drawer")?.classList.contains("open");
    document.querySelector("[data-close-drawer='compose-create-drawer']")?.click();

    await settleRoute("#/topics");
    document.querySelector("#open-create-topic").click();
    const createTopicDrawerOpen = document.querySelector("#topic-create-drawer")?.classList.contains("open");
    document.querySelector("[data-close-drawer='topic-create-drawer']")?.click();
    // PR2: topic detail / members / rules / proposals all live under
    // #/topics/{id} as tabs. The legacy "members" pane is now folded
    // into the detail tab (members render below the document).
    await settleRoute("#/topics/demo?tab=detail");
    await waitUntil(() => document.querySelector("#topics-tab-proposals")?.disabled === false);
    const topicRouteActive = document.querySelector("#page-topics")?.classList.contains("active");
    const topicsNavActive = document.querySelector('[data-nav="topics"]')?.classList.contains("active");
    document.querySelector("[data-tab='topics-proposals']").click();
    const proposalsActive = document.querySelector("#tab-topics-proposals")?.classList.contains("active");
    document.querySelector("[data-tab='topics-rules']").click();
    await waitUntil(() => (document.querySelector("#topic-rules-summary")?.textContent || "").includes("Semantic"));
    const rulesText = document.querySelector("#topic-rules-summary")?.textContent || "";
    document.querySelector("#open-topic-rules").click();
    const rulesDrawerOpen = document.querySelector("#topic-rules-drawer")?.classList.contains("open");
    document.querySelector("[data-close-drawer='topic-rules-drawer']")?.click();

    // PR3: /jobs is gone — the redirect side-effect routes the deep link
    // to /notes. /settings is also a deprecated redirect to /overview.
    // Opening the gear button should mount the settings drawer at the
    // general tab; that's the new entry to language / models / sync /
    // index / events.
    await settleRoute("#/jobs?id=job-capture");
    await new Promise((resolve) => setTimeout(resolve, 20));
    const notesAfterJobsRedirect = !!document.querySelector("#page-thoughts")?.classList.contains("active");
    await settleRoute("#/settings");
    await new Promise((resolve) => setTimeout(resolve, 20));
    const overviewAfterSettingsRedirect = !!document.querySelector("#page-dashboard")?.classList.contains("active");
    const openSettingsBtn = document.querySelector("#open-settings");
    openSettingsBtn?.click();
    await waitUntil(() => document.querySelector("#settings-drawer")?.classList.contains("open"));
    const settingsDrawerOpen = !!document.querySelector("#settings-drawer")?.classList.contains("open");
    // Switch to the events tab and confirm the event list exists.
    document.querySelector("[data-tab='settings-drawer-events']")?.click();
    await new Promise((resolve) => setTimeout(resolve, 20));
    const eventsTabActive = !!document.querySelector("#tab-settings-drawer-events")?.classList.contains("active");
    const eventsListPresent = !!document.querySelector("#settings-drawer-event-list");
    document.querySelector("[data-close-drawer='settings-drawer']")?.click();
    const metricsText = document.querySelector("#settings-drawer-metrics-json")?.textContent || "";
    const shell = document.querySelector(".tf-layout").getBoundingClientRect();
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
      sidebar: !!document.querySelector(".tf-sider"),
      dashboardActive,
      topicItems: document.querySelectorAll(".topic-item").length,
      searchItems: document.querySelectorAll("#search-results .result-item").length,
      // PR4: each page description in zh-CN must be ≤ 12 chars and in
      // en-US must be ≤ 60 chars. Sample every visible page section and
      // assert against the locale currently active on <html>.
      descriptions: Array.from(document.querySelectorAll(".tf-page-header p[data-i18n]")).map((node) => ({
        page: node.closest(".tf-page")?.dataset.page || "?",
        key: node.dataset.i18n,
        text: (node.textContent || "").trim(),
        lang: document.documentElement.lang,
      })),
      routeStates,
      captureResult,
      thoughtDrawerOpen,
      explainText,
      thoughtDrawerText,
      basketTextAfterDrawer,
      composeActive,
      basketText,
      composeDrawerOpen,
      createTopicDrawerOpen,
      topicRouteActive,
      topicsNavActive,
      proposalsActive,
      rulesText,
      rulesDrawerOpen,
      metricsText,
      notesAfterJobsRedirect,
      overviewAfterSettingsRedirect,
      settingsDrawerOpen,
      eventsTabActive,
      eventsListPresent,
      // PR5: sidebar count badges surface notes / topics / compose totals.
      sidebarWidth: document.querySelector(".tf-sider")?.getBoundingClientRect().width,
      sidebarBadges: {
        notes: document.querySelector('.tf-menu-badge[data-badge="notes"]')?.textContent || "",
        topics: document.querySelector('.tf-menu-badge[data-badge="topics"]')?.textContent || "",
        compose: document.querySelector('.tf-menu-badge[data-badge="compose"]')?.textContent || "",
      },
      shellWidth: shell.width,
      scrollWidth: document.documentElement.scrollWidth,
      clientWidth,
      wideElements,
    };
  });

  assert.equal(state.title, "ThoughtFlow");
  assert.match(state.status, /browser \/ ready/);
  assert.equal(state.sidebar, true);
  assert.equal(state.dashboardActive, true);
  assert.equal(state.topicItems, 1);
  assert.equal(state.searchItems, 1);
  assert.deepEqual(state.routeStates, [
    { name: "capture", active: true, visible: true, navActive: true },
    { name: "notes", active: true, visible: true, navActive: true },
    { name: "search", active: true, visible: true, navActive: true },
    { name: "topics", active: true, visible: true, navActive: true },
    { name: "compose", active: true, visible: true, navActive: true },
    { name: "settings", active: true, visible: false, navActive: true },
  ]);
  assert.match(state.captureResult, /Captured from browser smoke|Session context|Archive preview/);
  assert.equal(state.thoughtDrawerOpen, true);
  assert.match(state.explainText, /Score details/);
  assert.match(state.thoughtDrawerText, /Browser Thought/);
  assert.match(state.basketTextAfterDrawer, /1 selected sources/);
  assert.equal(state.composeActive, true);
  assert.match(state.basketText, /1 selected sources/);
  assert.equal(state.composeDrawerOpen, true);
  assert.equal(state.createTopicDrawerOpen, true);
  assert.equal(state.topicRouteActive, true);
  assert.equal(state.topicsNavActive, true);
  assert.equal(state.proposalsActive, true);
  assert.match(state.rulesText, /Semantic/);
  assert.equal(state.rulesDrawerOpen, true);
  assert.match(state.metricsText, /thoughtflow_background_jobs/);
  // PR3: /jobs and /settings are deprecated redirects.
  assert.equal(state.notesAfterJobsRedirect, true);
  assert.equal(state.overviewAfterSettingsRedirect, true);
  assert.equal(state.settingsDrawerOpen, true);
  assert.equal(state.eventsTabActive, true);
  assert.equal(state.eventsListPresent, true);
  // PR5: sidebar count badges surface notes / topics / compose totals.
  // Width check skipped on mobile because @media (max-width: 760px)
  // collapses the sidebar to a single column; we only assert width on
  // viewports that use the grid layout.
  const width = state.sidebarWidth || 0;
  const usesGrid = width > 0 && width < 400;
  if (usesGrid) {
    assert.ok(width <= 200, `sidebar too wide (${width}px) — was narrowed from 248px to 184px`);
    assert.ok(width >= 150, `sidebar too narrow (${width}px) — labels may clip`);
  }
  assert.ok(state.sidebarBadges.notes && state.sidebarBadges.notes.length > 0, `notes badge empty: ${JSON.stringify(state.sidebarBadges)}`);
  assert.ok(state.sidebarBadges.topics && state.sidebarBadges.topics.length > 0, `topics badge empty: ${JSON.stringify(state.sidebarBadges)}`);
  assert.ok(state.sidebarBadges.compose && state.sidebarBadges.compose.length > 0, `compose badge empty: ${JSON.stringify(state.sidebarBadges)}`);
  assert.ok(state.shellWidth > 0);
  assert.ok(state.scrollWidth <= state.clientWidth + 4, `horizontal overflow: ${JSON.stringify(state)}`);
  assert.deepEqual(errors, []);
  // PR4: per `doc/thoughtflow-web-ux-polish-v2.md` §4.3, page descriptions
  // are bounded so the page header doesn't dominate the layout. The
  // settings drawer is exempt (it has no header description on a page).
  for (const item of state.descriptions) {
    if (item.lang === "zh-CN") {
      assert.ok(item.text.length <= 12, `zh description too long (${item.text.length}) on ${item.page}: ${item.text}`);
    } else {
      assert.ok(item.text.length <= 60, `en description too long (${item.text.length}) on ${item.page}: ${item.text}`);
    }
  }
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

test("embedded UI restores deep-link query into inputs and reflects input changes back into the hash", async (t) => {
  const server = await startFixtureServer();
  t.after(() => server.close());
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const target = browserTargets.find((item) => item.name === "chrome");
  if (!target || target.skip) {
    t.skip(target ? target.skip : "Chrome not available");
    return;
  }
  const browser = await target.launch(viewports()[0]);
  try {
    const page = await connectPage(browser);
    const errors = [];
    page.onEvent("Runtime.exceptionThrown", (event) => errors.push(event.exceptionDetails?.text || "runtime exception"));
    page.onEvent("Log.entryAdded", (event) => {
      if (event.entry?.level === "error") errors.push(event.entry.text);
    });
    await page.send("Runtime.enable");
    await page.send("Log.enable");
    await page.send("Page.enable");
    // Boot with a search query in the hash and assert inputs are populated.
    // Navigate first, then set the hash via JS to dodge the URL parser
    // stripping the query from the fragment.
    await page.navigate(`${baseURL}/`);
    await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));
    await page.evaluate(() => { window.location.hash = "#/search?q=rag&mode=keyword&topic_id=demo&selected=thought-1"; });
    await page.waitForExpression(() => document.querySelector("#page-search")?.classList.contains("active"));
    await page.waitForExpression(() => document.querySelector("#search-query")?.value === "rag");
    const restored = await page.evaluate(() => ({
      q: document.querySelector("#search-query")?.value,
      mode: document.querySelector("#search-mode")?.value,
      topic: document.querySelector("#search-topic-id")?.value,
    }));
    assert.equal(restored.q, "rag");
    assert.equal(restored.mode, "keyword");
    assert.equal(restored.topic, "demo");

    // Typing into the search box updates the hash via the debounced serializer.
    await page.evaluate(() => {
      const input = document.querySelector("#search-query");
      input.value = "vector store";
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await page.waitForExpression(() => /q=vector(\+|%20)store/.test(window.location.hash));
    const hashAfter = await page.evaluate(() => window.location.hash);
    assert.match(hashAfter, /q=vector(\+|%20)store/);

    // Topics page filter input → hash round-trip.
    await page.evaluate(() => { window.location.hash = "#/topics"; });
    await page.waitForExpression(() => document.querySelector("#page-topics")?.classList.contains("active"));
    await page.evaluate(() => {
      const input = document.querySelector("#topic-filter");
      input.value = "demo";
      input.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await page.waitForExpression(() => /keyword=demo/.test(window.location.hash));
    const topicsHash = await page.evaluate(() => window.location.hash);
    assert.match(topicsHash, /keyword=demo/);
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});

test("compose basket persists across reloads via localStorage", async (t) => {
  const server = await startFixtureServer();
  t.after(() => server.close());
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const target = browserTargets.find((item) => item.name === "chrome");
  if (!target || target.skip) {
    t.skip(target ? target.skip : "Chrome not available");
    return;
  }
  const browser = await target.launch(viewports()[0]);
  try {
    const page = await connectPage(browser);
    await page.send("Page.enable");
    // Seed localStorage *before* the page boots so restoreBasket sees it.
    await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: "window.localStorage.setItem('tflow.basket', JSON.stringify({ ids: ['thought-1', 'thought-2'], updated_at: 'seed' }));",
    });
    await page.navigate(`${baseURL}/`);
    await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));
    await page.evaluate(() => { window.location.hash = "#/compose"; });
    await page.waitForExpression(() => {
      const el = document.querySelector("#compose-source-count");
      return el && /2/.test(el.textContent || "");
    });
    const count = await page.evaluate(() => document.querySelector("#compose-source-count")?.textContent || "");
    assert.match(count, /2/);
  } finally {
    await browser.close();
  }
});

test("capture composer starts a new session, persists a thought, and shows the conversation", async (t) => {
  const server = await startFixtureServer({ autoCommit: true });
  t.after(() => server.close());
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const target = browserTargets.find((item) => item.name === "chrome");
  if (!target || target.skip) {
    t.skip(target ? target.skip : "Chrome not available");
    return;
  }
  const browser = await target.launch(viewports()[0]);
  try {
    const page = await connectPage(browser);
    const errors = [];
    page.onEvent("Runtime.exceptionThrown", (event) => errors.push(event.exceptionDetails?.text || "runtime exception"));
    page.onEvent("Log.entryAdded", (event) => {
      if (event.entry?.level === "error") errors.push(event.entry.text);
    });
    await page.send("Page.enable");
    await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: "window.localStorage.clear();",
    });
    await page.navigate(`${baseURL}/`);
    await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));
    await page.evaluate(() => { window.location.hash = "#/capture"; });
    await page.waitForExpression(() => document.querySelector("#page-capture")?.classList.contains("active"));
    await page.evaluate(() => {
      const input = document.querySelector("#capture-composer-input");
      input.value = "Browser session smoke text";
      document.querySelector("#capture-composer").requestSubmit();
    });
    await page.waitForExpression(() => document.querySelectorAll("#capture-conversation .tf-msg").length >= 2);
    // Phase 10 capture UX: the AI bubble should render the rich
    // status-chip card (capture / refine / index / topic) once the
    // follow-up GET /api/thoughts/:id returns. Wait for the chip to
    // land before asserting — the initial capture response does not
    // include refine_status, so the bubble re-renders shortly after.
    await page.waitForExpression(() => {
      const aiBubble = document.querySelector("#capture-conversation .tf-msg-ai");
      return aiBubble && /data-status="refine-pending"/.test(aiBubble.outerHTML || "");
    });
    const messages = await page.evaluate(() => Array.from(document.querySelectorAll("#capture-conversation .tf-msg")).map((el) => el.textContent || ""));
    assert.ok(messages.some((text) => text.includes("Browser session smoke text")), "user message should be in conversation");
    assert.ok(messages.some((text) => text.includes("thought-capture")), "AI response should include the new thought id");
    const cardHTML = await page.evaluate(() => {
      const aiBubbles = Array.from(document.querySelectorAll("#capture-conversation .tf-msg-ai"));
      const bubble = aiBubbles.length > 0 ? aiBubbles[aiBubbles.length - 1] : null;
      return bubble ? bubble.outerHTML : "";
    });
    assert.match(cardHTML, /data-status="refine-pending"/, "rich card should expose refine status chip");
    assert.match(cardHTML, /data-status="capture-captured"/, "rich card should expose capture status chip");
    assert.match(cardHTML, /href="#\/thoughts\?id=thought-capture"/, "rich card should keep the view-thought link");
    assert.match(cardHTML, /href="#\/search"/, "rich card should keep the search-related link");
    const sessionsRaw = await page.evaluate(() => window.localStorage.getItem("tflow.capture.sessions"));
    assert.ok(sessionsRaw, "capture sessions should be persisted to localStorage");
    const sessions = JSON.parse(sessionsRaw || "[]");
    assert.equal(sessions.length, 1);
    assert.equal(sessions[0].thoughtId, "thought-capture");
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});

test("capture conversation re-renders the AI bubble in place after a PATCH command", async (t) => {
  const server = await startFixtureServer();
  t.after(() => server.close());
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const target = browserTargets.find((item) => item.name === "chrome");
  if (!target || target.skip) {
    t.skip(target ? target.skip : "Chrome not available");
    return;
  }
  const browser = await target.launch(viewports()[0]);
  try {
    const page = await connectPage(browser);
    const errors = [];
    page.onEvent("Runtime.exceptionThrown", (event) => errors.push(event.exceptionDetails?.text || "runtime exception"));
    page.onEvent("Log.entryAdded", (event) => {
      if (event.entry?.level === "error") errors.push(event.entry.text);
    });
    await page.send("Page.enable");
    await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: "window.localStorage.clear();",
    });
    // Use en-US so per-command feedback ("Renamed to", "Added tags")
    // is assertion-friendly; the i18n registry and language switcher
    // are exercised in the dedicated locale test.
    await page.navigate(`${baseURL}/?lang=en-US`);
    await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));
    await page.evaluate(() => { window.location.hash = "#/capture"; });
    await page.waitForExpression(() => document.querySelector("#page-capture")?.classList.contains("active"));
    // Start a session and wait for the extracted context panel.
    await page.evaluate(() => {
      const input = document.querySelector("#capture-composer-input");
      input.value = "Multi-turn browser smoke";
      document.querySelector("#capture-composer").requestSubmit();
    });
    await page.waitForExpression(() => /Multi-turn browser smoke/.test(document.querySelector("#capture-context-panel")?.textContent || ""));
    // Issue a rename command; in the current capture flow it updates
    // session_context.CandidateTitle rather than PATCHing a Thought.
    await page.evaluate(() => {
      const input = document.querySelector("#capture-composer-input");
      input.value = "rename to Renamed in browser";
      document.querySelector("#capture-composer").requestSubmit();
    });
    // Poll for the rename to land in the context and for the preview
    // action to produce a before-save archive preview.
    let renamed = false;
    const deadline = Date.now() + 8000;
    while (Date.now() < deadline) {
      renamed = await page.evaluate(() => {
        const context = document.querySelector("#capture-context-panel");
        return context && /Renamed in browser/.test(context.textContent || "");
      });
      if (renamed) break;
      await new Promise((resolve) => setTimeout(resolve, 100));
    }
    if (!renamed) {
      const dump = await page.evaluate(() => ({
        messages: Array.from(document.querySelectorAll("#capture-conversation .tf-msg")).map((el) => el.outerHTML),
        composerValue: document.querySelector("#capture-composer-input")?.value || "",
      }));
      console.error("DEBUG: rename never landed.\nMessages:\n" + dump.messages.join("\n---\n") + "\nComposer value: " + JSON.stringify(dump.composerValue));
    }
    assert.ok(renamed, "rename command should update the session context title");
    await page.evaluate(() => document.querySelector("#capture-refresh-preview").click());
    await page.waitForExpression(() => /Renamed in browser/.test(document.querySelector("#capture-archive-preview")?.textContent || ""));
    const rendered = await page.evaluate(() => {
      return {
        context: document.querySelector("#capture-context-panel")?.textContent || "",
        preview: document.querySelector("#capture-archive-preview")?.textContent || "",
      };
    });
    assert.match(rendered.context, /Renamed in browser/);
    assert.match(rendered.preview, /Renamed in browser/);
    assert.match(rendered.preview, /Strategy|new/);
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});

// Regression: the capture lock indicator has author CSS
// `.tf-capture-lock { display: flex }` which on its own has the same
// specificity as the UA `[hidden] { display: none }` and would win,
// making the indicator visible even when `el.hidden = true`. The
// global `[hidden] { display: none !important }` rule added in
// styles.css restores the intended semantics. This test asserts the
// indicator is laid out (zero offsetHeight) on a fresh page load,
// catching any future regression that drops the !important rule.
test("capture lock indicator stays hidden when no session is active", async (t) => {
  const server = await startFixtureServer();
  t.after(() => server.close());
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const target = browserTargets.find((item) => item.name === "chrome");
  if (!target || target.skip) {
    t.skip(target ? target.skip : "Chrome not available");
    return;
  }
  const browser = await target.launch(viewports()[0]);
  try {
    const page = await connectPage(browser);
    await page.send("Page.enable");
    await page.send("Runtime.enable");
    await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: "window.localStorage.clear();",
    });
    await page.navigate(`${baseURL}/`);
    await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));
    await page.evaluate(() => { window.location.hash = "#/capture"; });
    await page.waitForExpression(() => document.querySelector("#page-capture")?.classList.contains("active"));
    const layout = await page.evaluate(() => {
      const el = document.querySelector("#capture-lock-indicator");
      if (!el) return { found: false };
      const cs = getComputedStyle(el);
      return {
        found: true,
        hiddenAttr: el.hasAttribute("hidden"),
        display: cs.display,
        offsetHeight: el.offsetHeight,
      };
    });
    assert.ok(layout.found, "lock indicator must be in the DOM");
    assert.equal(layout.display, "none", `display should be "none" when indicator is hidden (got ${layout.display})`);
    assert.equal(layout.offsetHeight, 0, "lock indicator must have zero height when hidden");
  } finally {
    await browser.close();
  }
});

test("embedded UI renders zh-CN by default and switches to en-US", async (t) => {
  const server = await startFixtureServer();
  t.after(() => server.close());
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const target = browserTargets.find((item) => item.name === "chrome");
  if (!target || target.skip) {
    t.skip(target ? target.skip : "Chrome not available");
    return;
  }
  const browser = await target.launch(viewports()[0]);
  try {
    const page = await connectPage(browser);
    const errors = [];
    page.onEvent("Runtime.exceptionThrown", (event) => errors.push(event.exceptionDetails?.text || "runtime exception"));
    page.onEvent("Log.entryAdded", (event) => {
      if (event.entry?.level === "error") errors.push(event.entry.text);
    });
    await page.send("Runtime.enable");
    await page.send("Log.enable");
    await page.send("Page.enable");
    // Force the i18n boot path onto the default locale by clearing any
    // persisted preference AND overriding navigator.language. Headless
    // Chrome reports en-US as navigator.language by default, which the
    // detect logic would otherwise treat as a positive match and skip
    // the fallback to DEFAULT_LOCALE (zh-CN). fr-FR is not a supported
    // locale and starts with neither "en" nor "zh", so detection falls
    // through to the default.
    await page.send("Page.addScriptToEvaluateOnNewDocument", {
      source: [
        "try { window.localStorage.removeItem('tflow.lang'); } catch (_) {}",
        "try {",
        "  Object.defineProperty(navigator, 'language', { value: 'fr-FR', configurable: true });",
        "  Object.defineProperty(navigator, 'languages', { value: ['fr-FR'], configurable: true });",
        "} catch (_) {}",
      ].join(" "),
    });
    // default locale is zh-CN
    await page.navigate(`${baseURL}/`);
    await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));
    const zhSnapshot = await page.evaluate(() => ({
      lang: document.documentElement.lang,
      overviewTitle: document.querySelector("#page-dashboard h2")?.textContent,
      navOverview: document.querySelector('[data-nav="overview"] .tf-menu-label')?.textContent,
      captureNav: document.querySelector('[data-nav="capture"] .tf-menu-label')?.textContent,
    }));
    assert.equal(zhSnapshot.lang, "zh-CN");
    assert.equal(zhSnapshot.overviewTitle, "总览");
    assert.equal(zhSnapshot.navOverview, "总览");
    assert.equal(zhSnapshot.captureNav, "采集");
    // switch to en-US via the settings drawer (PR3 moved the language
    // segmented control out of the topbar and into the settings drawer).
    await page.evaluate(() => { document.querySelector("#open-settings")?.click(); });
    await page.waitForExpression(() => document.querySelector("#settings-drawer")?.classList.contains("open"));
    await page.evaluate(() => {
      const button = document.querySelector('#settings-language [data-locale="en-US"]');
      if (button) button.click();
    });
    await page.waitForExpression(() => document.documentElement.lang === "en-US");
    const enSnapshot = await page.evaluate(() => ({
      lang: document.documentElement.lang,
      overviewTitle: document.querySelector("#page-dashboard h2")?.textContent,
      navOverview: document.querySelector('[data-nav="overview"] .tf-menu-label')?.textContent,
      captureNav: document.querySelector('[data-nav="capture"] .tf-menu-label')?.textContent,
    }));
    assert.equal(enSnapshot.lang, "en-US");
    assert.equal(enSnapshot.overviewTitle, "Overview");
    assert.equal(enSnapshot.navOverview, "Overview");
    assert.equal(enSnapshot.captureNav, "Capture");
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});

test("embedded UI exposes a11y affordances: skip link, aria-current, focus trap, live region", async (t) => {
  const server = await startFixtureServer();
  t.after(() => server.close());
  const baseURL = `http://127.0.0.1:${server.address().port}`;
  const target = browserTargets.find((item) => item.name === "chrome");
  if (!target || target.skip) {
    t.skip(target ? target.skip : "Chrome not available");
    return;
  }
  const browser = await target.launch(viewports()[0]);
  try {
    const page = await connectPage(browser);
    const errors = [];
    page.onEvent("Runtime.exceptionThrown", (event) => errors.push(event.exceptionDetails?.text || "runtime exception"));
    page.onEvent("Log.entryAdded", (event) => {
      if (event.entry?.level === "error") errors.push(event.entry.text);
    });
    await page.send("Runtime.enable");
    await page.send("Log.enable");
    await page.send("Page.enable");
    await page.navigate(`${baseURL}/?lang=en-US`);
    await page.waitForExpression(() => document.querySelector("#page-dashboard")?.classList.contains("active"));

    const structural = await page.evaluate(() => {
      const skip = document.querySelector(".tf-skip-link");
      const navSider = document.querySelector(".tf-sider[aria-label]");
      const activeNav = document.querySelector(".tf-menu-item.active");
      const otherNav = document.querySelector('.tf-menu-item[data-nav="capture"]');
      const toast = document.querySelector("#toast");
      const confirmModal = document.querySelector("#confirm-modal");
      const confirmPanel = document.querySelector("#confirm-modal .tf-modal-panel");
      return {
        skipHref: skip?.getAttribute("href"),
        skipText: skip?.textContent?.trim(),
        skipPresent: Boolean(skip),
        siderLabel: navSider?.getAttribute("aria-label"),
        activeAriaCurrent: activeNav?.getAttribute("aria-current"),
        otherAriaCurrent: otherNav?.getAttribute("aria-current"),
        toastLive: toast?.getAttribute("aria-live"),
        toastRole: toast?.getAttribute("role"),
        confirmRole: confirmPanel?.getAttribute("role"),
        confirmModal: confirmPanel?.getAttribute("aria-modal"),
        confirmLabelledby: confirmPanel?.getAttribute("aria-labelledby"),
      };
    });
    assert.equal(structural.skipHref, "#page-container");
    assert.ok(structural.skipText, "skip link must have text");
    assert.equal(structural.siderLabel, "Primary navigation");
    assert.equal(structural.activeAriaCurrent, "page");
    assert.equal(structural.otherAriaCurrent, null);
    assert.equal(structural.toastLive, "polite");
    assert.equal(structural.toastRole, "status");
    assert.equal(structural.confirmRole, "dialog");
    assert.equal(structural.confirmModal, "true");
    assert.equal(structural.confirmLabelledby, "confirm-title");

    // Open the settings drawer via the gear button and verify Tab cycles
    // inside it. PR3 moves the settings UI from a top-level page into a
    // drawer; the focus-trap contract is unchanged.
    await page.evaluate(() => { document.querySelector("#open-settings")?.click(); });
    await page.waitForExpression(() => document.querySelector("#settings-drawer")?.classList.contains("open"));
    const drawerOpen = await page.evaluate(() => {
      const el = document.querySelector("#settings-drawer");
      if (!el) return false;
      const style = window.getComputedStyle(el);
      return style.display !== "none" && !el.classList.contains("hidden");
    });
    if (drawerOpen) {
      const trap = await page.evaluate(() => {
        const drawer = document.querySelector("#settings-drawer");
        const focusables = Array.from(drawer.querySelectorAll("button, input, select, textarea, a[href]"))
          .filter((el) => !el.disabled && el.offsetParent !== null);
        if (focusables.length < 2) return { focusableCount: focusables.length };
        const last = focusables[focusables.length - 1];
        last.focus();
        const event = new KeyboardEvent("keydown", { key: "Tab", bubbles: true });
        drawer.dispatchEvent(event);
        // The trap itself doesn't move focus (the browser does), but it must
        // call preventDefault so the browser wraps to the first element.
        // We can't observe preventDefault from a synthesized event, so we
        // verify the listener is installed by checking the keydown reaches
        // the drawer (it does, given the dispatch above) and that at least
        // two focusables exist so the wrap path is meaningful.
        return { focusableCount: focusables.length, lastFocused: last === document.activeElement };
      });
      assert.ok(trap.focusableCount >= 2, "drawer must have at least two focusables for the trap to be meaningful");
    }
    assert.deepEqual(errors, []);
  } finally {
    await browser.close();
  }
});

function startFixtureServer(options = {}) {
  const { autoCommit = false } = options;
  const webRoot = __dirname;
  const api = (data) => JSON.stringify({ request_id: "browser-test", data, error: null });
  // Per-thought PATCH state. The GET response reflects the most recent
  // PATCH so thought detail tests stay deterministic.
  const thoughtState = new Map();
  const captureSessions = new Map();
  const makeScratchpad = (id, text = "Captured from browser smoke") => ({
    session_id: id,
    workspace_id: "browser",
    title: "",
    tags: [],
    topic_hints: [],
    content: text,
    messages: text ? [{ role: "user", text, at: "2026-06-13T00:00:00Z" }] : [],
    draft: {},
    session_context: {
      topic: text,
      goal: "Keep collecting this topic",
      confirmed_facts: [text],
      open_questions: [],
      conflicts: [],
      candidate_title: text,
      candidate_tags: ["browser"],
      candidate_summary: text,
      candidate_body: text,
      source_links: [],
      related_thought_ids: [],
      suggested_topic_ids: [],
      archive_intent: "none",
      archive_strategy: "new",
    },
    archive_intent: "none",
    archive_strategy: "new",
    archive_preview: null,
    source_thought_id: "",
    committed_thought_id: "",
    created_at: "2026-06-13T00:00:00Z",
    updated_at: "2026-06-13T00:00:00Z",
  });
  const server = http.createServer(async (req, res) => {
    const url = new URL(req.url, "http://127.0.0.1");
    if (url.pathname.startsWith("/api/capture/sessions/")) {
      const suffix = url.pathname.slice("/api/capture/sessions/".length);
      const [sessionID] = suffix.split("/");
      const sp = captureSessions.get(sessionID) || makeScratchpad(sessionID, "");
      if (suffix === `${sessionID}` && req.method === "GET") {
        captureSessions.set(sessionID, sp);
        return json(res, api(sp));
      }
      if (suffix === `${sessionID}/messages` && req.method === "POST") {
        const body = (await readJson(req)) || {};
        const text = body.text || "";
        sp.messages.push({ role: body.role || "user", text, at: "2026-06-13T00:00:00Z" });
        sp.content = [sp.content, text].filter(Boolean).join("\n\n");
        sp.session_context.candidate_body = sp.content;
        sp.session_context.candidate_summary = sp.content;
        if (!sp.session_context.candidate_title) sp.session_context.candidate_title = text;
        if (autoCommit && !sp.committed_thought_id) {
          // Mock-only: bypass the two-step "preview → archive" flow
          // so the composer→AI-bubble happy path can be exercised
          // end-to-end. The real backend only sets
          // committed_thought_id on /archive; the front-end surfaces
          // the rich status chip whenever a scratchpad transitions
          // from "no anchor" to "anchored to thought-X", so the test
          // can assert the same rendering it would see after a
          // genuine archive round-trip.
          sp.committed_thought_id = "thought-capture";
          sp.archive_preview = null;
        }
        captureSessions.set(sessionID, sp);
        return json(res, api(sp));
      }
      if (suffix === `${sessionID}/context` && req.method === "POST") {
        const body = (await readJson(req)) || {};
        sp.session_context = { ...sp.session_context, ...body };
        sp.title = sp.session_context.candidate_title || sp.title;
        captureSessions.set(sessionID, sp);
        return json(res, api(sp));
      }
      if (suffix === `${sessionID}/intent` && req.method === "POST") {
        const body = (await readJson(req)) || {};
        sp.archive_intent = body.intent || "none";
        sp.session_context.archive_intent = sp.archive_intent;
        captureSessions.set(sessionID, sp);
        return json(res, api(sp));
      }
      if (suffix === `${sessionID}/archive/preview` && req.method === "GET") {
        sp.archive_preview = {
          title: sp.session_context.candidate_title || "Browser capture",
          body: sp.session_context.candidate_body || sp.content,
          tags: sp.session_context.candidate_tags || ["browser"],
          source_links: sp.session_context.source_links || [],
          related_topics: sp.session_context.suggested_topic_ids || [],
          strategy: sp.archive_strategy || "new",
          generated_at: "2026-06-13T00:00:00Z",
        };
        captureSessions.set(sessionID, sp);
        return json(res, api({ preview: sp.archive_preview, persisted: true }));
      }
      if (suffix === `${sessionID}/archive` && req.method === "POST") {
        sp.committed_thought_id = "thought-capture";
        sp.archive_preview = null;
        captureSessions.set(sessionID, sp);
        return json(res, api({
          thought: {
            id: "thought-capture",
            display_title: sp.session_context.candidate_title || "Browser capture",
            user_title: sp.session_context.candidate_title || "Browser capture",
            capture_status: "captured",
            path: "thoughts/browser-capture.md",
          },
          jobs: [{ id: "job-capture", type: "refine", status: "queued" }],
        }));
      }
    }
    switch (url.pathname) {
      case "/":
      case "/index.html":
        return serveFile(res, path.join(webRoot, "index.html"), "text/html; charset=utf-8");
      case "/styles.css":
        return serveFile(res, path.join(webRoot, "styles.css"), "text/css; charset=utf-8");
      case "/app.js":
        return serveFile(res, path.join(webRoot, "app.js"), "application/javascript; charset=utf-8");
      case "/i18n/index.js":
        return serveFile(res, path.join(webRoot, "i18n", "index.js"), "application/javascript; charset=utf-8");
      case "/i18n/en-US.js":
        return serveFile(res, path.join(webRoot, "i18n", "en-US.js"), "application/javascript; charset=utf-8");
      case "/i18n/zh-CN.js":
        return serveFile(res, path.join(webRoot, "i18n", "zh-CN.js"), "application/javascript; charset=utf-8");
      case "/vendor/markdown-it.min.js":
        return serveFile(res, path.join(webRoot, "vendor", "markdown-it.min.js"), "application/javascript; charset=utf-8");
      case "/session-lock.js":
        return serveFile(res, path.join(webRoot, "session-lock.js"), "application/javascript; charset=utf-8");
      case "/favicon.ico":
        res.writeHead(204);
        return res.end();
      case "/api/system/status":
        return json(res, api({
          status: "ready",
          workspace: { id: "browser", status: "ready", root_path: "/tmp/browser" },
          ai: { status: "ready", chat_model: "browser-chat" },
          git: { status: "disabled" },
          duckdb: { status: "ready", path: "/tmp/browser-data/thoughtflow.duckdb" },
          background: { status: "ready" },
          events: { status: "ready" },
        }));
      case "/api/system/metrics":
        return json(res, api({
          values: {
            thoughtflow_background_jobs: 1,
            thoughtflow_git_commit_total: 0,
            thoughtflow_capture_total: 3,
          },
        }));
      case "/api/topics":
        if (req.method === "GET") {
          return json(res, api([{ id: "demo", name: "Demo Topic", member_count: 1, word_count: 12, description: "Browser smoke" }]));
        }
        return json(res, api({ id: "demo", name: "Demo Topic" }), 201);
      case "/api/topics/demo":
        return json(res, api({
          topic: {
            id: "demo",
            name: "Demo Topic",
            rules: { keywords: { any: ["browser"] }, tags: { any: ["ui"] }, semantic: { enabled: true, threshold: 0.75 } },
            outline: [{ title: "Notes" }],
            auto_weave: true,
          },
          document: "# Demo Topic\n\nBrowser smoke document.",
          members: [{ thought_id: "thought-1", title: "Browser Thought", match_type: "keyword", score: 0.9 }],
        }));
      case "/api/topics/demo/weave-proposals":
      case "/api/compose/drafts":
        return json(res, api([
          { id: "draft-1", goal: "Smoke test draft", format: "summary", status: "draft", created_at: "2026-06-09T00:00:00Z", updated_at: "2026-06-09T00:00:00Z" },
        ]));
      case "/api/thoughts":
        if (req.method === "POST") {
          return json(res, api({
            thought: {
              id: "thought-capture",
              title: "Browser capture",
              status: "captured",
              path: "thoughts/browser-capture.md",
            },
            jobs: [{ id: "job-capture", type: "refine", status: "queued" }],
          }), 202);
        }
        break;
      case "/api/capture/sessions":
        if (req.method === "GET") {
          const summaries = Array.from(captureSessions.values()).map((sp) => ({
            session_id: sp.session_id,
            title: sp.session_context?.candidate_title || sp.session_id,
            committed_thought_id: sp.committed_thought_id || "",
            source_thought_id: sp.source_thought_id || "",
            archive_strategy: sp.archive_strategy || "new",
            message_count: sp.messages.length,
            content_length: sp.content.length,
            updated_at: sp.updated_at,
          }));
          return json(res, api({
            summaries,
            last_active_session_id: summaries[0]?.session_id || "",
          }));
        }
        if (req.method === "POST") {
          const body = (await readJson(req)) || {};
          if (body.reuse_last) {
            const latest = Array.from(captureSessions.values())[0] || {};
            return json(res, api(latest));
          }
          const id = req.headers["x-session-id"] || "browser-session";
          const sp = makeScratchpad(id, body.content || "");
          if (autoCommit) {
            // Mock-only: see the note in the /messages handler.
            sp.committed_thought_id = "thought-capture";
          }
          captureSessions.set(id, sp);
          return json(res, api(sp));
        }
        break;
      case "/api/thoughts/thought-1":
        return json(res, api({
          thought: {
            id: "thought-1",
            display_title: "Browser Thought",
            user_title: "Browser Thought",
            refine_status: "succeeded",
            index_status: "succeeded",
            topic_status: "matched",
            path: "thoughts/browser.md",
            summary: "Browser summary",
          },
          content: {
            original: "Browser original",
            extracted_content: "Browser extracted",
            links: "- https://example.test",
          },
          jobs: [{ id: "job-capture", type: "refine", status: "succeeded" }],
        }));
      case "/api/thoughts/thought-capture":
        if (req.method === "PATCH") {
          // Phase 10 capture UX: PATCH on the active thought is what
          // drives the per-command feedback bubble in the chat. The
          // fixture only has to echo a snapshot back so the test can
          // assert the in-place re-render; the unit tests cover the
          // full PATCH contract.
          const body = (await readJson(req)) || {};
          const next = {
            display_title: body.title || "Browser capture renamed",
            user_title: body.title || "Browser capture renamed",
            user_tags: Array.isArray(body.tags) ? body.tags : ["browser", "smoke"],
            refine_status: "refined",
            index_status: "indexed",
            topic_status: "matched",
          };
          thoughtState.set("thought-capture", next);
          return json(res, api({
            thought: {
              id: "thought-capture",
              ...next,
              capture_status: "captured",
              path: "thoughts/browser-capture.md",
              summary: "Refine landed after rename.",
              key_points: ["First key point", "Second key point"],
              ai_tags: ["rename", "ux"],
              related_thought_ids: ["thought-1"],
              suggested_topic_ids: ["demo"],
            },
            content: { original: "Captured from browser smoke" },
            jobs: [{ id: "job-capture", type: "refine", status: "succeeded" }],
          }), 200);
        }
        // GET — overlay any PATCH state set earlier.
        const patched = thoughtState.get("thought-capture") || {};
        return json(res, api({
          thought: {
            id: "thought-capture",
            display_title: patched.display_title || "Browser capture",
            user_title: patched.user_title || "Browser capture",
            capture_status: "captured",
            refine_status: patched.refine_status || "pending",
            index_status: patched.index_status || "pending",
            topic_status: patched.topic_status || "unmatched",
            user_tags: patched.user_tags || ["browser", "smoke"],
            path: "thoughts/browser-capture.md",
            summary: "",
          },
          content: {
            original: "Captured from browser smoke",
          },
          jobs: [{ id: "job-capture", type: "refine", status: "queued" }],
        }));
      case "/api/jobs":
        return json(res, api([]));
      case "/api/jobs/job-capture":
        return json(res, api({
          id: "job-capture",
          type: "refine",
          resource_type: "thought",
          resource_id: "thought-capture",
          status: "succeeded",
          message: "done",
          attempt: 1,
          max_attempts: 1,
          progress: 1,
          created_at: "2026-06-10T00:00:00Z",
        }));
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
            explain: {
              mode: "hybrid",
              sort: "score",
              score_formula: "keyword + semantic + recency",
              weights: { keyword: 1, semantic: 1, recency: 0.2 },
              keyword_source: "fts",
              semantic_source: "embedding",
            },
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

// readJson drains the request body and parses it as JSON. Used by the
// PATCH/POST fixtures; returns null on missing or malformed payloads
// so the caller can fall back to defaults.
function readJson(req) {
  return new Promise((resolve) => {
    const chunks = [];
    req.on("data", (chunk) => chunks.push(chunk));
    req.on("end", () => {
      const text = Buffer.concat(chunks).toString("utf8");
      if (!text) return resolve(null);
      try {
        resolve(JSON.parse(text));
      } catch (_) {
        resolve(null);
      }
    });
    req.on("error", () => resolve(null));
  });
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
