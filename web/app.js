// i18n is loaded as a separate <script> before this file and exposes itself
// on `window.tflow_i18n`. We grab a defensive reference here so app.js can
// run unchanged under Node `vm` (tests stub `tflow`) and gracefully fall back
// to identity functions if the script load order is ever broken.
const tflow = (typeof window !== "undefined" && window.tflow_i18n) || (typeof globalThis !== "undefined" && globalThis.tflow_i18n) || null;
const t = (tflow && tflow.t) || ((key) => key);
const tn = (tflow && tflow.tn) || ((key) => key);
const tApply = (tflow && tflow.applyTranslations) || (() => {});
const tSetLocale = (tflow && tflow.setLocale) || (() => {});
const tOnLocaleChange = (tflow && tflow.onLocaleChange) || (() => () => {});
const tGetLocale = (tflow && tflow.getLocale) || (() => "zh-CN");
const tInit = (tflow && tflow.init) || (() => "zh-CN");
const tListLocales = (tflow && tflow.listLocales) || (() => ["zh-CN", "en-US"]);

const state = {
  route: { page: "dashboard", nav: "dashboard", params: {}, query: {} },
  notes: [],
  topics: [],
  activeTopicId: "",
  selectedThoughts: new Set(),
  composeSources: new Map(),
  composeSourcesVersion: 0,
  composeSourcesRevision: 0,
  composeSourcesSync: Promise.resolve(),
  lastResults: [],
  composeDraft: null,
  composeDrafts: [],
  composeGenerating: false,
  composeActiveJobId: "",
  composeGenerationPollTimer: null,
  // Compose reader UI: preview/edit mode with local unsaved content.
  composeReader: {
    activeDraftID: "",
    mode: "preview", // preview | edit
    localContent: "",
    loadedContent: "",
    status: "",
  },
  documentProfiles: [],
  documentProfileIssues: [],
  activeThoughtId: "",
  activeThoughtSnapshot: null,
  activeTopicDetail: null,
  status: null,
  metrics: null,
  pendingConfirm: null,
  capture: {
    sessionId: "",
    activeThoughtId: "",
    activeSnapshot: null,
    // thoughtSnapshots keeps a per-thoughtId cache of the latest
    // /api/thoughts/:id response. The conversation can therefore show
    // rich thoughtId-bound bubbles for every thought committed in the
    // scratchpad session (including supplement chains), each rendered
    // from its own snapshot. SSE handlers (scratchpad.committed,
    // thought.refined, thought.expanded, …) write to this map; the
    // 1.95 s refreshActiveScratchpadContext poll is just a fast-path
    // best-effort, not the source of truth.
    thoughtSnapshots: new Map(),
    activeScratchpad: null,
    archivePreview: null,
    messages: [],
    sessions: [],
    suggestion: null,
    lockedBy: "",
  },
};

// Cross-tab bus built on BroadcastChannel. It never persists business
// payloads in browser storage; tabs reload shared state from the backend.
let tflowBus = null;
function initTflowBus() {
  if (typeof window === "undefined") return null;
  if (tflowBus) return tflowBus;
  if (typeof BroadcastChannel === "function") {
    try {
      const channel = new BroadcastChannel("tflow");
      const handlers = new Set();
      channel.addEventListener("message", (event) => {
        for (const handler of handlers) {
          try { handler(event.data || {}); } catch (_) { /* ignore */ }
        }
      });
      tflowBus = {
        post(message) {
          try { channel.postMessage(message); } catch (_) { /* ignore */ }
        },
        on(handler) {
          handlers.add(handler);
          return () => handlers.delete(handler);
        },
        close() { try { channel.close(); } catch (_) {} },
      };
      return tflowBus;
    } catch (_) {
      return null;
    }
  }
  return null;
}

// Wire compose source updates onto the bus so other tabs see add/remove/clear.
function broadcastSourcesChange() {
  if (tflowBus) {
    tflowBus.post({ kind: "compose:sources_changed" });
  }
}

let markdownParser = null;

const $ = (selector) => document.querySelector(selector);

function decodeRouteSegment(value) {
  try {
    return decodeURIComponent(value);
  } catch (_) {
    // Keep malformed legacy hashes navigable; API calls will return their
    // normal validation error instead of the router throwing synchronously.
    return value;
  }
}

function parseRoute(hash) {
  const raw = String(hash || "").replace(/^#\/?/, "");
  const [pathPart, queryPart = ""] = raw.split("?");
  const parts = pathPart.split("/").filter(Boolean);
  const query = Object.fromEntries(new URLSearchParams(queryPart).entries());
  if (parts.length === 0) return { page: "dashboard", nav: "overview", params: {}, query };
  // Legacy /topics/{id}/review links now land on the topic detail workspace.
  if (parts[0] === "topics" && parts[1] && parts[2] === "review") {
    const detailQuery = { ...query, tab: "detail" };
    return { page: "topics", nav: "topics", params: { topicId: decodeRouteSegment(parts[1]) }, query: detailQuery };
  }
  if (parts[0] === "topics" && parts[1]) {
    return { page: "topics", nav: "topics", params: { topicId: decodeRouteSegment(parts[1]) }, query };
  }
  if (parts[0] === "notes") {
    return { page: "thoughts", nav: "notes", params: { thoughtId: query.id || "" }, query };
  }
  if (parts[0] === "write") {
    return { page: "compose", nav: "compose", params: {}, query };
  }
  if (parts[0] === "overview") {
    return { page: "dashboard", nav: "overview", params: {}, query };
  }
  const known = new Set(["capture", "search", "topics", "about"]);
  if (known.has(parts[0])) return { page: parts[0], nav: parts[0], params: {}, query };
  return { page: "dashboard", nav: "overview", params: {}, query };
}

// Each page declares how its inputs/UI map into the hash query. Only fields
// that differ from defaults are written. This keeps short URLs for the common
// case and avoids polluting the history with intermediate keystrokes.
const PAGE_SERIALIZERS = {
  search: () => {
    const q = {};
    const v = $("#search-query")?.value.trim();
    if (v) q.q = v;
    const tags = $("#search-tags")?.value.trim();
    if (tags) q.tags = tags;
    if (state.selectedThoughts.size > 0) q.selected = Array.from(state.selectedThoughts).join(",");
    return q;
  },
  topics: () => {
    const q = topicFilterRouteQuery();
    if (state.route?.params?.topicId) {
      const active = document.querySelector(`#page-topics .tab.active`);
      if (active && active.dataset.tab && active.dataset.tab !== defaultTopicsTabForRoute()) q.tab = topicTabRouteValue(active.dataset.tab);
    }
    return q;
  },
  thoughts: () => {
    const q = {};
    const id = state.activeThoughtId || state.route?.params?.thoughtId || "";
    if (id) q.id = id;
    const active = document.querySelector(`#page-thoughts .tab.active`);
    if (active && active.dataset.tab && active.dataset.tab !== defaultNotesTabForQuery(q)) q.tab = active.dataset.tab;
    return q;
  },
  compose: () => {
    const q = {};
    const active = document.querySelector(`#page-compose .tab.active`);
    if (active && active.dataset.tab && active.dataset.tab !== defaultComposeTabName()) q.tab = composeTabRouteValue(active.dataset.tab);
    return q;
  },
};

function topicFilterRouteQuery() {
  const q = {};
  const f = $("#topic-filter")?.value.trim();
  if (f) q.keyword = f;
  return q;
}

function topicTabRouteValue(tab) {
  const normalized = normalizeTopicsTabName(tab);
  return normalized.startsWith("topics-") ? normalized.slice("topics-".length) : normalized;
}

function defaultTopicsTabForRoute() {
  return state.route?.params?.topicId ? "topics-detail" : "topics-list";
}

// Reverse of PAGE_SERIALIZERS — read a query object and apply it to inputs /
// state. Unknown keys are silently ignored so older URLs don't crash on a
// forward port. Returns nothing; mutates DOM and state in place.
function restoreRoutePage(page, query) {
  if (!query || typeof query !== "object") return;
  if (page === "search") {
    $("#search-query").value = typeof query.q === "string" ? query.q : "";
    $("#search-tags").value = typeof query.tags === "string" ? query.tags : "";
    if (typeof query.selected === "string" && query.selected) {
      state.selectedThoughts = new Set(query.selected.split(",").filter(Boolean));
    } else {
      state.selectedThoughts = new Set();
    }
  } else if (page === "topics") {
    $("#topic-filter").value = typeof query.keyword === "string" ? query.keyword : "";
    if (state.route?.params?.topicId) {
      activateTab(normalizeTopicsTabName(query.tab), $("#page-topics"));
    } else {
      activateTab("topics-list", $("#page-topics"));
    }
  } else if (page === "thoughts") {
    activateTab(normalizeNotesTabName(query.tab, query), $("#page-thoughts"));
  } else if (page === "compose") {
    activateTab(normalizeComposeTabName(query.tab), $("#page-compose"));
  }
  // Compose draft IDs come back via API calls and are stored on state.
}

function defaultNotesTabForQuery(query = {}) {
  return typeof query.id === "string" && query.id.trim() ? "notes-detail" : "notes-all";
}

function defaultComposeTabName() {
  return "compose-writing";
}

function normalizeComposeTabName(value = "") {
  const raw = String(value || "").trim().replace(/^compose-/, "");
  const aliases = {
    writing: "compose-writing",
    sources: "compose-sources",
  };
  return aliases[raw] || defaultComposeTabName();
}

function composeTabRouteValue(tabName = "") {
  return normalizeComposeTabName(tabName) === "compose-sources" ? "sources" : "writing";
}

function normalizeNotesTabName(tab, query = {}) {
  const raw = typeof tab === "string" ? tab.trim() : "";
  if (!raw) return defaultNotesTabForQuery(query);
  if (raw.startsWith("notes-")) return raw;
  const aliases = {
    all: "notes-all",
    list: "notes-all",
    detail: "notes-detail",
    status: "notes-status",
    runtime: "notes-runtime",
  };
  return aliases[raw] || defaultNotesTabForQuery(query);
}

function normalizeTopicsTabName(tab) {
  const value = typeof tab === "string" && tab.trim() ? tab.trim() : "detail";
  if (value.startsWith("topics-")) {
    return ["topics-list", "topics-detail", "topics-rules"].includes(value) ? value : "topics-detail";
  }
  const aliases = {
    list: "topics-list",
    detail: "topics-detail",
    rules: "topics-rules",
 documents: "topics-detail",
  };
  return aliases[value] || "topics-detail";
}

function buildRouteHash(page, params = {}, query = {}) {
  // Map internal page names back to user-facing segment names. The page
  // identifiers (used in `data-page` and route.page) preserve the historical
  // names so existing renderers and CSS hooks keep working; only the
  // top-level hash segment is rewritten to the new wording.
  const pageToSegment = {
    dashboard: "overview",
    thoughts: "notes",
    compose: "write",
  };
  let path = `#/${pageToSegment[page] || page}`;
  if (page === "topics" && params.topicId) {
    path = `#/topics/${encodeURIComponent(params.topicId)}`;
  }
  const entries = Object.entries(query).filter(([, v]) => v !== undefined && v !== null && v !== "");
  if (entries.length === 0) return path;
  const search = entries
    .map(([k, v]) => `${encodeURIComponent(k)}=${encodeURIComponent(v)}`)
    .join("&");
  return `${path}?${search}`;
}

// Replace the current hash without pushing a new history entry. Used by
// debounced input handlers to keep the URL in sync without filling history.
function replaceHashSilently(hash) {
  if (typeof window === "undefined") return;
  if (window.location.hash === hash) return;
  try {
    const url = new URL(window.location.href);
    url.hash = hash;
    window.history.replaceState(null, "", url.toString());
  } catch (_error) {
    // fall back to assignment (still avoids the hashchange ping)
    window.location.hash = hash;
  }
}

let persistRouteTimer = null;
let routeApplySerial = 0;
function persistRouteDebounced() {
  if (typeof window === "undefined") return;
  window.clearTimeout(persistRouteTimer);
  persistRouteTimer = window.setTimeout(persistRouteNow, 150);
}

function persistRouteNow() {
  if (typeof window === "undefined") return;
  const page = state.route?.page;
  if (!page || !PAGE_SERIALIZERS[page]) return;
  const params = state.route?.params || {};
  const query = PAGE_SERIALIZERS[page]() || {};
  const hash = buildRouteHash(page, params, query);
  replaceHashSilently(hash);
}

// Build the current page's hash from the current DOM and replace history.
// Used after explicit user actions (form submit) to surface the new URL
// without waiting for the debounce.
function syncHash() {
  persistRouteNow();
}

if (typeof window !== "undefined") {
  window.syncHash = syncHash;
}

async function refreshRouteData(route, options = {}) {
  if (!route || !route.page) return;
  switch (route.page) {
    case "dashboard":
      await Promise.all([loadStatus(), loadMetrics()]);
      break;
    case "capture":
      renderCaptureConversation();
      if (options.force && typeof rehydrateActiveScratchpad === "function") {
        await rehydrateActiveScratchpad();
      }
      break;
    case "thoughts":
      await loadThoughts();
      break;
    case "search":
      await runSearch();
      break;
    case "topics":
      await loadTopics();
      break;
    case "compose":
      await loadComposeDrafts();
      renderComposeSources();
      break;
    default:
      break;
  }
}

function handleNavClick(event) {
  const link = event.currentTarget || event.target?.closest?.("[data-nav]");
  const rawHref = link?.getAttribute?.("href") || "";
  if (!rawHref.startsWith("#/")) return;
  event.preventDefault();
  navigateHash(rawHref, { force: true });
}

function navigateHash(hash, options = {}) {
  if (window.location.hash === hash) {
    applyRoute(hash, options).catch((error) => toast(error.message));
    return;
  }
  window.location.hash = hash;
}

function tabScope(tab) {
  return tab.closest(".tf-drawer") || tab.closest(".tf-page") || tab.closest(".tf-card") || document;
}

function handleTopicsTabClick(tabName) {
  const normalized = normalizeTopicsTabName(tabName);
  activateTab(normalized, $("#page-topics"));
  const query = topicFilterRouteQuery();
  if (normalized === "topics-list") {
    navigateHash(buildRouteHash("topics", {}, query), { force: true });
    return;
  }
  if (!state.activeTopicId) return;
  if (normalized !== "topics-detail") query.tab = topicTabRouteValue(normalized);
  navigateHash(buildRouteHash("topics", { topicId: state.activeTopicId }, query), { force: true });
}

function handleTabClick(event) {
  const tab = event.currentTarget;
  const tabName = tab?.dataset?.tab;
  if (!tabName) return;
  const page = tab.closest(".tf-page");
  if (page?.dataset?.page === "topics") {
    handleTopicsTabClick(tabName);
    return;
  }
  activateTab(tabName, tabScope(tab));
  persistRouteDebounced();
  if (tabName === "notes-runtime") {
    refreshNotesRuntime().catch(() => {});
  }
}

function removeFromStorage(key) {
  try {
    window.localStorage?.removeItem(key);
  } catch (_error) {
    /* ignore */
  }
}

const COMPOSE_SOURCES_STORAGE_KEY = "tflow.compose.sources";
const HELP_VISIBLE_STORAGE_KEY = "tflow.help.visible";

function persistSources() {
  removeFromStorage(COMPOSE_SOURCES_STORAGE_KEY);
  const revision = ++state.composeSourcesRevision;
  const sources = Array.from(state.composeSources.values());
  // Reads and writes share one queue. Snapshot each edit when it is queued,
  // so a refresh cannot change what an already requested save will submit.
  const saved = state.composeSourcesSync.then(async () => {
    const payload = await api("/api/compose/sources", {
      method: sources.length === 0 ? "DELETE" : "PUT",
      headers: { "If-Match": `"${state.composeSourcesVersion}"` },
      ...(sources.length ? { body: JSON.stringify({ sources }) } : {}),
    });
    state.composeSourcesVersion = payload?.version ?? state.composeSourcesVersion;
    broadcastSourcesChange();
  }).catch(async (error) => {
    toast(error.message);
    // Already inside the queue: calling restoreSources would wait on this
    // same save. Fetch directly, with the same protection for later edits.
    await refreshSourcesSnapshot(revision).catch(() => {});
  });
  state.composeSourcesSync = saved;
  return saved;
}

function restoreSources() {
  removeFromStorage(COMPOSE_SOURCES_STORAGE_KEY);
  const revision = state.composeSourcesRevision;
  const refreshed = state.composeSourcesSync.then(() => refreshSourcesSnapshot(revision));
  // A failed GET is returned to its caller without blocking later saves.
  state.composeSourcesSync = refreshed.catch(() => {});
  return refreshed;
}

async function refreshSourcesSnapshot(revision) {
  if (revision !== state.composeSourcesRevision) return;
  const payload = await api("/api/compose/sources");
  // An edit may be queued while this GET is in flight, even if the server
  // version has not advanced yet. Keep that local edit until its save runs.
  if (revision !== state.composeSourcesRevision || (payload?.version ?? 0) < state.composeSourcesVersion) return;
  state.composeSourcesVersion = payload?.version ?? 0;
  const next = new Map();
  for (const source of payload?.sources || []) {
    if (!source || !source.source_type || !source.source_id) continue;
    next.set(`${source.source_type}::${source.source_id}`, {
      source_type: String(source.source_type),
      source_id: String(source.source_id),
      title: source.title ? String(source.title) : "",
    });
  }
  state.composeSources = next;
  renderComposeSources();
}

function isHelpVisible() {
  try {
    return window.localStorage?.getItem(HELP_VISIBLE_STORAGE_KEY) !== "false";
  } catch (_error) {
    return true;
  }
}

function setHelpVisible(visible) {
  const enabled = visible !== false;
  try {
    window.localStorage?.setItem(HELP_VISIBLE_STORAGE_KEY, enabled ? "true" : "false");
  } catch (_error) {
    /* ignore */
  }
  applyHelpVisibility(enabled);
}

function applyHelpVisibility(visible = isHelpVisible()) {
  const enabled = visible !== false;
  document.body?.classList?.toggle("tf-help-hidden", !enabled);
  const control = $("#settings-help-visible");
  if (control) control.checked = enabled;
}

function navItemClass(route, nav) {
  return route.nav === nav ? "tf-menu-item active" : "tf-menu-item";
}

// Returns the aria-current value for a nav item given the active route.
// Screen readers announce "current page" so users know where they are.
function navItemAriaCurrent(route, nav) {
  return route.nav === nav ? "page" : null;
}

function statusBadge(status) {
  switch (String(status || "").toLowerCase()) {
    case "ready":
    case "configured":
    case "ok":
      return "tf-badge tf-badge-success";
    case "degraded":
    case "retrying":
    case "not_configured":
      return "tf-badge tf-badge-warning";
    case "failed":
    case "error":
    case "unready":
      return "tf-badge tf-badge-error";
    default:
      return "tf-badge tf-badge-default";
  }
}

// Traps focus within a container while it is open. Returns a release
// function that detaches the listeners and restores focus to `returnEl`
// (or document.body if not given). Walks the focusable descendants of
// `container` and wraps Tab/Shift+Tab around the ends.
function trapFocus(container, returnEl) {
  if (!container) return () => {};
  const selector = [
    "a[href]",
    "button:not([disabled])",
    "input:not([disabled])",
    "select:not([disabled])",
    "textarea:not([disabled])",
    "[tabindex]:not([tabindex='-1'])",
  ].join(",");
  function focusables() {
    return Array.from(container.querySelectorAll(selector)).filter((el) => {
      const rects = el.getClientRects();
      return rects.length > 0 && el.offsetParent !== null;
    });
  }
  function handleKeydown(event) {
    if (event.key !== "Tab") return;
    const list = focusables();
    if (list.length === 0) {
      event.preventDefault();
      return;
    }
    const first = list[0];
    const last = list[list.length - 1];
    const active = document.activeElement;
    if (event.shiftKey && active === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && active === last) {
      event.preventDefault();
      first.focus();
    }
  }
  container.addEventListener("keydown", handleKeydown);
  // Move initial focus into the container so Tab/Shift-Tab work from the start.
  const firstFocusable = focusables()[0];
  if (firstFocusable) {
    try { firstFocusable.focus({ preventScroll: true }); } catch (_) { firstFocusable.focus(); }
  }
  return () => {
    container.removeEventListener("keydown", handleKeydown);
    const ret = returnEl || document.activeElement;
    if (ret && typeof ret.focus === "function") {
      try { ret.focus({ preventScroll: true }); } catch (_) { ret.focus(); }
    }
  };
}

// Track the current focus-trap release function so Escape knows which
// drawer/modal to release. Replaced on every open.
let activeFocusRelease = null;

function openDrawer(id) {
  const drawer = $(`#${id}`);
  if (!drawer) return;
  // If another drawer is open, release its focus trap first so they don't stack.
  if (activeFocusRelease) {
    try { activeFocusRelease(); } catch (_) { /* ignore */ }
    activeFocusRelease = null;
  }
  drawer.classList.add("open");
  drawer.setAttribute("aria-hidden", "false");
  // Record the element that opened the drawer so we can return focus on close.
  activeFocusRelease = trapFocus(drawer, document.activeElement);
}

function closeDrawer(id) {
  const drawer = $(`#${id}`);
  if (!drawer) return;
  drawer.classList.remove("open");
  drawer.setAttribute("aria-hidden", "true");
  if (activeFocusRelease) {
    try { activeFocusRelease(); } catch (_) { /* ignore */ }
    activeFocusRelease = null;
  }
}

function setButtonLoading(button, loading, loadingLabel) {
  if (!button) return;
  if (loading) {
    button.dataset.label = button.textContent;
    button.textContent = loadingLabel || t("common.loading");
    button.disabled = true;
  } else {
    if (button.dataset.label) button.textContent = button.dataset.label;
    button.disabled = false;
  }
}

function confirmAction(title, message) {
  const modal = $("#confirm-modal");
  if (!modal) return Promise.resolve(true);
  const titleNode = $("#confirm-title");
  const messageNode = $("#confirm-message");
  if (titleNode) titleNode.textContent = title;
  if (messageNode) messageNode.textContent = message;
  if (activeFocusRelease) {
    try { activeFocusRelease(); } catch (_) { /* ignore */ }
    activeFocusRelease = null;
  }
  modal.classList.add("open");
  modal.setAttribute("aria-hidden", "false");
  try {
    activeFocusRelease = trapFocus(modal, document.activeElement);
  } catch (_) {
    activeFocusRelease = null;
  }
  return new Promise((resolve) => {
    state.pendingConfirm = resolve;
  });
}

function closeConfirm(result) {
  const modal = $("#confirm-modal");
  if (modal) {
    modal.classList.remove("open");
    modal.setAttribute("aria-hidden", "true");
  }
  if (activeFocusRelease) {
    try { activeFocusRelease(); } catch (_) { /* ignore */ }
    activeFocusRelease = null;
  }
  if (state.pendingConfirm) {
    state.pendingConfirm(result);
    state.pendingConfirm = null;
  }
}

function toast(message) {
  const node = $("#toast");
  node.textContent = message;
  node.classList.add("show");
  window.clearTimeout(toast.timer);
  toast.timer = window.setTimeout(() => node.classList.remove("show"), 2600);
}

// Selection epochs also distinguish A → B → A from an earlier request for A.
let captureSelectionEpoch = 0;
let searchRequestEpoch = 0;
let topicRequestEpoch = 0;
let collectionReadEpoch = 0;
let collectionSearchEpoch = 0;
let collectionWorkspace = null;
let captureDocumentSet = null;
let captureSetEpoch = 0;
let captureSetRequest = 0;
let captureSetEventRequest = 0;
let captureSetQualifiedEvent = 0;
let setSearchEpoch = 0;
let markdownResourceContext = null;
let thoughtRequestEpoch = 0;
let composeRequestEpoch = 0;
function captureRequestScope() {
  const epoch = captureSelectionEpoch;
  const sessionId = state.capture.sessionId || "";
  return { sessionId, isCurrent: () => epoch === captureSelectionEpoch && sessionId === (state.capture.sessionId || "") };
}
async function captureAPI(scope,path,options) {
  try { return await api(path,options); }
  catch (error) { if (scope.isCurrent()) throw error; return null; }
}
function beginCaptureSelection() {
  captureSelectionEpoch++;
 captureSetEpoch++;
  if (state.capture.activeThoughtId && window.tflowSessionLock) {
    window.tflowSessionLock.release(state.capture.activeThoughtId, state.capture.sessionId);
  }
}

async function api(path, options = {}) {
  const locale = window.tflow_i18n?.getLocale?.() || window.navigator?.language || "zh-CN";
  const { headers: requestHeaders, ...requestOptions } = options;
  const response = await fetch(path, {
    ...requestOptions,
    headers: { ...(typeof FormData !== "undefined" && requestOptions.body instanceof FormData ? {} : { "Content-Type": "application/json" }), "Accept-Language": locale, ...(requestHeaders || {}) },
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok || payload.error) {
    const message = payload.error?.message || response.statusText || t("toast.request_failed");
    const requestID = payload.request_id ? `${payload.request_id}: ` : "";
    const error = new Error(`${requestID}${message}`);
    error.code = payload.error?.code || "";
    error.status = response.status;
    error.requestID = payload.request_id || "";
    error.details = payload.error?.details || payload.error || {};
    throw error;
  }
  return payload.data;
}

function csv(value) {
  return value
    .split(",")
    .map((item) => item.trim())
    .filter(Boolean);
}

function escapeHTML(value) {
  return String(value ?? "")
    .replaceAll("&", "&amp;")
    .replaceAll("<", "&lt;")
    .replaceAll(">", "&gt;")
    .replaceAll('"', "&quot;");
}

function fmtDate(value) {
  if (!value) return t("date.never");
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return t("date.unknown");
  return parsed.toLocaleString();
}

function score(value) {
  if (!Number.isFinite(value)) return "0.00";
  return value.toFixed(2);
}

function joinCSV(values) {
  return (values || []).join(", ");
}

function profileRef(profile = {}) {
  return profile.ref || profile.document_profile || {};
}

function profileByID(profileID, version = 0) {
  return (state.documentProfiles || []).find((profile) => {
    const ref = profileRef(profile);
    return ref.profile_id === profileID && (!version || ref.version === Number(version));
  }) || null;
}

function profileLabel(profile = {}) {
  const ref = profileRef(profile);
  return `${profile.name || ref.profile_id || t("document_profile.unknown")} · v${ref.version || 1}`;
}

function renderDocumentProfileOptions(selectedID = "", selectedVersion = 0) {
  const profiles = state.documentProfiles || [];
  if (!profiles.length) {
    return `<option value="">${escapeHTML(t("document_profile.unavailable"))}</option>`;
  }
  return profiles.map((profile) => {
    const ref = profileRef(profile);
    const selected = ref.profile_id === selectedID && (!selectedVersion || ref.version === Number(selectedVersion));
    return `<option value="${escapeHTML(ref.profile_id || "")}" data-version="${escapeHTML(ref.version || 1)}"${selected ? " selected" : ""}>${escapeHTML(profileLabel(profile))}</option>`;
  }).join("");
}

function syncComposeProfileSelect(selectedID = "", selectedVersion = 0) {
  const select = $("#compose-format");
  if (!select) return;
  const fallback = profileByID("builtin.note") || state.documentProfiles[0] || null;
  const fallbackRef = profileRef(fallback || {});
  select.innerHTML = renderDocumentProfileOptions(selectedID || fallbackRef.profile_id, selectedVersion || fallbackRef.version);
 renderComposeDocumentInputs();
}

async function loadDocumentProfiles() {
  const payload = await api("/api/document-profiles");
  const profiles = Array.isArray(payload?.profiles) ? payload.profiles : [];
  const issues = Array.isArray(payload?.issues) ? payload.issues : [];
  const changed = JSON.stringify([state.documentProfiles, state.documentProfileIssues]) !== JSON.stringify([profiles, issues]);
  state.documentProfiles = profiles;
  state.documentProfileIssues = issues;
  if (!changed) return false;
  const composeSelect = $("#compose-format");
  const selectedID = composeSelect?.value || "";
  const selectedVersion = Number(composeSelect?.selectedOptions?.[0]?.dataset?.version || 0);
  syncComposeProfileSelect(selectedID, selectedVersion);
  renderAboutDocumentProfiles();
  renderCaptureConversation();
  return true;
}

function aboutDocumentProfilesHTML(profiles = [], issues = []) {
  const enabled = (profiles || []).filter((profile) => profile && profile.enabled !== false);
  const chips = enabled.map((profile) => {
    const ref = profileRef(profile);
    return `<span class="tf-chip" title="${escapeHTML(profile.description || "")}">${escapeHTML(profileLabel(profile))}<small>${escapeHTML(ref.family || "")}</small></span>`;
  });
  if (!chips.length) {
    chips.push(`<span class="tf-text-secondary">${escapeHTML(t("about.profiles.none"))}</span>`);
  }
  if (Array.isArray(issues) && issues.length) {
    chips.push(`<span class="tf-profile-issue">${escapeHTML(t("about.profiles.issue_count", { count: issues.length }))}</span>`);
  }
  return chips.join("");
}

function renderAboutDocumentProfiles() {
  const pathNode = $("#about-document-format-path");
  if (pathNode) {
    pathNode.textContent = state.status?.workspace?.document_formats_path || "document-formats/";
  }
  const listNode = $("#about-document-profile-list");
  if (listNode) {
    listNode.innerHTML = aboutDocumentProfilesHTML(state.documentProfiles, state.documentProfileIssues);
  }
}

function renderDescription(rows) {
  return `<dl>${rows
    .filter((row) => row[1] !== undefined && row[1] !== null && row[1] !== "")
    .map(([label, value]) => `<dt>${escapeHTML(label)}</dt><dd>${escapeHTML(value)}</dd>`)
    .join("")}</dl>`;
}

function normalizeDisplayPath(value) {
  return String(value || "").trim().replaceAll("\\", "/");
}

function baseName(value) {
  const normalized = normalizeDisplayPath(value).replace(/\/+$/, "");
  if (!normalized) return "";
  const parts = normalized.split("/");
  return parts[parts.length - 1] || normalized;
}

function isAbsoluteDisplayPath(value) {
  return /^\/|^[A-Za-z]:\//.test(normalizeDisplayPath(value));
}

function displayWorkspace(workspace = {}) {
  if (workspace.id) return workspace.id;
  if (workspace.root_path) return `workspace:${baseName(workspace.root_path)}`;
  return workspace.status || t("settings.card.workspace").toLowerCase();
}

function displayRuntimePath(value, workspaceRoot = "") {
  const path = normalizeDisplayPath(value);
  if (!path) return "";
  const root = normalizeDisplayPath(workspaceRoot).replace(/\/+$/, "");
  if (root && path === root) return ".";
  if (root && path.startsWith(`${root}/`)) return path.slice(root.length + 1);
  if (isAbsoluteDisplayPath(path)) return baseName(path);
  return path;
}

function createComposeSources(initial = []) {
  // 每个条目是 {source_type, source_id[, title?]} 复合源;
  // 去重以 (source_type, source_id) 联合键为基准,同一 thought 既可作为
  // thought 来源又可作为 search_result 来源重复加入,互不影响。
  const map = new Map();
  for (const source of initial || []) addSource(source);
  function keyOf(source) {
    return `${source.source_type}::${source.source_id}`;
  }
  function addSource(source) {
    if (!source || !source.source_type || !source.source_id) return false;
    const key = keyOf(source);
    if (map.has(key)) return false;
    map.set(key, {
      source_type: String(source.source_type),
      source_id: String(source.source_id),
      title: source.title ? String(source.title) : "",
    });
    return true;
  }
  return {
    add(source) {
      addSource(source);
      return Array.from(map.values());
    },
    addMany(sources) {
      for (const source of sources || []) addSource(source);
      return Array.from(map.values());
    },
    clear() {
      map.clear();
      return [];
    },
    values() {
      return Array.from(map.values());
    },
    size() {
      return map.size;
    },
    has(source) {
      if (!source || !source.source_type || !source.source_id) return false;
      return map.has(keyOf(source));
    },
  };
}

function outlineText(outline) {
  return (outline || []).map((node) => node.title).filter(Boolean).join("\n");
}

function outlineFromText(value) {
  return value
    .split("\n")
    .map((title) => title.trim())
    .filter(Boolean)
    .map((title) => ({ title }));
}

function renderInlineMarkdown(value) {
  let text = escapeHTML(value);
  text = text.replace(/\[\[([^\]|]+)(?:\|([^\]]+))?\]\]/g, (_match, target, label) => {
    const cleanTarget = String(target || "").trim();
    const cleanLabel = String(label || target || "").trim();
    return `<code title="${escapeHTML(cleanTarget)}">${escapeHTML(cleanLabel)}</code>`;
  });
  text = text.replace(/!\[([^\]]*)\]\(([^)]+)\)/g, (_match, alt, src) => {
    const cleanSrc = safeMarkdownHref(src);
    if (!cleanSrc) return escapeHTML(alt);
    return `<img src="${cleanSrc}" alt="${alt}">`;
  });
  text = text.replace(/\[([^\]]+)\]\(([^)]+)\)/g, (_match, label, href) => {
    const cleanHref = safeMarkdownHref(href);
    if (!cleanHref) return label;
    return `<a href="${cleanHref}" target="_blank" rel="noreferrer">${label}</a>`;
  });
  text = text.replace(/`([^`]+)`/g, "<code>$1</code>");
  text = text.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  text = text.replace(/~~([^~]+)~~/g, "<del>$1</del>");
  text = text.replace(/(^|[^*])\*([^*]+)\*/g, "$1<em>$2</em>");
  return text;
}

let documentSetRenderContext = null;
function cleanMarkdownHref(value) {
 const logical=String(value || "").trim();
 if (/^(docset|thought|topic):\/\//.test(logical)) {
  try { const link=new URL(logical); if(link.username||link.password||link.search) return "";
   if(link.protocol==="thought:" && /^[a-zA-Z0-9_.-]+$/.test(link.hostname))return buildRouteHash("thoughts",{thoughtId:link.hostname},{id:link.hostname});
   if(link.protocol==="topic:" && /^[a-zA-Z0-9_.-]+$/.test(link.hostname))return buildRouteHash("topics",{topicId:link.hostname},{tab:"documents"});
   if(link.protocol==="docset:" && documentSetRenderContext?.id===link.hostname){const key=decodeURIComponent(link.pathname.slice(1));if(documentSetRenderContext.keys.has(key))return setRoute(link.hostname,documentSetRenderContext.revision,key,decodeURIComponent(link.hash.slice(1)));}
  }catch(_){} return "";
 }

 if(String(value||"").startsWith("resource://"))return resourceContentHref(value);
 const mapped=markdownResourceContext?.links?.[String(value||"")];if(mapped)return resourceContentHref(`resource://${mapped}`);
  const href = String(value || "").trim();
  if (/^(https?:|mailto:|#|\/|\.\/|\.\.\/)/i.test(href)) {
    return href;
  }
  return "";
}

function safeMarkdownHref(value) {
  const href = cleanMarkdownHref(value);
  return href ? escapeHTML(href) : "";
}

function getMarkdownParser() {
  if (markdownParser) return markdownParser;
  const factory =
    typeof markdownit === "function" ? markdownit : typeof window !== "undefined" && typeof window.markdownit === "function" ? window.markdownit : null;
  if (!factory) return null;
  const parser = factory({
    html: false,
    linkify: false,
    typographer: false,
  });
  parser.validateLink = (href) => Boolean(cleanMarkdownHref(href));
  installObsidianLinkRule(parser);
  installTaskListRule(parser);
  installSafeLinkRenderers(parser);
  markdownParser = parser;
  return markdownParser;
}

function installSafeLinkRenderers(parser) {
  const defaultLinkOpen = parser.renderer.rules.link_open || ((tokens, index, options, _env, self) => self.renderToken(tokens, index, options));
  parser.renderer.rules.link_open = (tokens, index, options, env, self) => {
    const token = tokens[index];
    const href = cleanMarkdownHref(token.attrGet("href"));
    if (!href) {
      token.attrSet("href", "#");
      token.attrSet("aria-disabled", "true");
    } else {
      token.attrSet("href", href);
    }
    token.attrSet("target",href.startsWith("#")?"_self":"_blank");
    token.attrSet("rel", "noreferrer");
    return defaultLinkOpen(tokens, index, options, env, self);
  };

  const defaultImage = parser.renderer.rules.image || ((tokens, index, options, _env, self) => self.renderToken(tokens, index, options));
  parser.renderer.rules.image = (tokens, index, options, env, self) => {
    const token = tokens[index];
    const src = cleanMarkdownHref(token.attrGet("src"));
    if (!src) return escapeHTML(token.content || "");
    token.attrSet("src", src);
    token.attrSet("loading", "lazy");
    return defaultImage(tokens, index, options, env, self);
  };
}

function installObsidianLinkRule(parser) {
  parser.renderer.rules.thoughtflow_obsidian_link = (tokens, index) => {
    const token = tokens[index];
    return `<code title="${escapeHTML(token.attrGet("title") || "")}">${escapeHTML(token.content || "")}</code>`;
  };
  parser.inline.ruler.before("emphasis", "thoughtflow_obsidian_link", (stateInline, silent) => {
    const start = stateInline.pos;
    if (stateInline.src.charCodeAt(start) !== 0x5b || stateInline.src.charCodeAt(start + 1) !== 0x5b) return false;
    const end = stateInline.src.indexOf("]]", start + 2);
    if (end < 0) return false;
    if (!silent) {
      const raw = stateInline.src.slice(start + 2, end);
      const separator = raw.indexOf("|");
      const target = (separator >= 0 ? raw.slice(0, separator) : raw).trim();
      const label = (separator >= 0 ? raw.slice(separator + 1) : raw).trim();
      const token = stateInline.push("thoughtflow_obsidian_link", "code", 0);
      token.content = label || target;
      token.attrSet("title", target);
    }
    stateInline.pos = end + 2;
    return true;
  });
}

function installTaskListRule(parser) {
  parser.core.ruler.after("inline", "thoughtflow_task_lists", (parserState) => {
    const tokens = parserState.tokens;
    for (let index = 2; index < tokens.length; index++) {
      const inlineToken = tokens[index];
      if (inlineToken.type !== "inline") continue;
      if (tokens[index - 1]?.type !== "paragraph_open" || tokens[index - 2]?.type !== "list_item_open") continue;
      const marker = inlineToken.content.match(/^\[([ xX])\]\s+/);
      if (!marker) continue;
      const checked = marker[1].toLowerCase() === "x";
      tokens[index - 2].attrJoin("class", "task-item");
      inlineToken.content = inlineToken.content.slice(marker[0].length);
      stripTaskMarkerFromChildren(inlineToken, marker[0].length);
      const checkbox = new parserState.Token("html_inline", "", 0);
      checkbox.content = `<input type="checkbox" disabled${checked ? " checked" : ""}>`;
      inlineToken.children = [checkbox, ...(inlineToken.children || [])];
    }
  });
}

function stripTaskMarkerFromChildren(inlineToken, markerLength) {
  let remaining = markerLength;
  for (const child of inlineToken.children || []) {
    if (remaining <= 0) return;
    if (child.type !== "text") continue;
    if (child.content.length <= remaining) {
      remaining -= child.content.length;
      child.content = "";
    } else {
      child.content = child.content.slice(remaining);
      remaining = 0;
    }
  }
}

function renderMarkdown(value) {
  const parser = getMarkdownParser();
  if (parser) {
    const { frontMatter, body } = splitFrontMatter(value);
    return `${frontMatter}${parser.render(sanitizeMarkdownInput(body || ""))}`;
  }
  return renderMarkdownFallback(value);
}

function renderObjectMarkdown(value,id,bindings=[]) {
 const previous=markdownResourceContext;
 markdownResourceContext={object_id:id,links:Object.fromEntries(bindings.filter(b=>!b.private&&b.original_locator).map(b=>[b.original_locator,b.resource_id]))};
 try{return renderMarkdown(value);}finally{markdownResourceContext=previous;}
}

function renderTopicDocumentMarkdown(value) {
  return renderMarkdown(stripMarkdownFrontMatter(value));
}

function thoughtLinksForDisplay(value) {
  const lines = String(value || "").split(/\r?\n/);
  const out = [];
  let inTopicBlock = false;
  for (const line of lines) {
    const trimmed = line.trim();
    if (/^Topics:\s*$/i.test(trimmed)) {
      inTopicBlock = true;
      continue;
    }
    if (/<!--\s*topic:[^>]*-->/i.test(line)) {
      inTopicBlock = true;
      continue;
    }
    if (inTopicBlock) {
      if (!trimmed) {
        inTopicBlock = false;
        continue;
      }
      if (/^[-*]\s+/.test(trimmed) || /^\[\[/.test(trimmed)) continue;
      inTopicBlock = false;
    }
    out.push(line);
  }
  return out.join("\n").trim();
}

function stripMarkdownFrontMatter(value) {
  return splitFrontMatter(value).body;
}

function sanitizeMarkdownInput(value) {
  return String(value || "")
    .replace(/!\[([^\]]*)\]\(((?:[^()\s]+|\([^)]*\))+)\)/g, (match, alt, src) => (cleanMarkdownHref(src) ? match : alt))
    .replace(/\[([^\]]+)\]\(((?:[^()\s]+|\([^)]*\))+)\)/g, (match, label, href) => (cleanMarkdownHref(href) ? match : label));
}

function splitFrontMatter(value) {
  const lines = String(value || "").split(/\r?\n/);
  if (lines[0]?.trim() !== "---") {
    return { frontMatter: "", body: String(value || "") };
  }
  const end = lines.slice(1).findIndex((line) => line.trim() === "---");
  if (end < 0) {
    return { frontMatter: "", body: String(value || "") };
  }
  return {
    frontMatter: renderFrontMatter(lines.slice(1, end + 1)),
    body: lines.slice(end + 2).join("\n"),
  };
}

function renderMarkdownFallback(value) {
  const lines = String(value || "").split(/\r?\n/);
  const html = [];
  let listType = "";
  let inCode = false;
  let index = 0;
  const closeList = () => {
    if (listType) {
      html.push(`</${listType}>`);
      listType = "";
    }
  };
  if (lines[0]?.trim() === "---") {
    const end = lines.slice(1).findIndex((line) => line.trim() === "---");
    if (end >= 0) {
      html.push(renderFrontMatter(lines.slice(1, end + 1)));
      index = end + 2;
    }
  }
  for (; index < lines.length; index++) {
    const rawLine = lines[index];
    const line = rawLine.trimEnd();
    if (line.trim().startsWith("```")) {
      if (inCode) {
        html.push("</code></pre>");
        inCode = false;
      } else {
        closeList();
        html.push("<pre><code>");
        inCode = true;
      }
      continue;
    }
    if (inCode) {
      html.push(`${escapeHTML(line)}\n`);
      continue;
    }
    if (!line.trim()) {
      closeList();
      continue;
    }
    const heading = line.match(/^(#{1,4})\s+(.+)$/);
    if (heading) {
      closeList();
      const level = heading[1].length;
      html.push(`<h${level}>${renderInlineMarkdown(heading[2])}</h${level}>`);
      continue;
    }
    if (/^[-*_]{3,}$/.test(line.trim())) {
      closeList();
      html.push("<hr>");
      continue;
    }
    if (isTableStart(lines, index)) {
      closeList();
      const table = collectTable(lines, index);
      html.push(renderTable(table.rows));
      index = table.nextIndex - 1;
      continue;
    }
    const orderedItem = line.match(/^\d+[.)]\s+(.+)$/);
    if (orderedItem) {
      if (listType !== "ol") {
        closeList();
        html.push("<ol>");
        listType = "ol";
      }
      html.push(`<li>${renderInlineMarkdown(orderedItem[1])}</li>`);
      continue;
    }
    const listItem = line.match(/^[-*]\s+(\[[ xX]\]\s+)?(.+)$/);
    if (listItem) {
      if (listType !== "ul") {
        closeList();
        html.push("<ul>");
        listType = "ul";
      }
      const task = listItem[1];
      const body = renderInlineMarkdown(listItem[2]);
      if (task) {
        const checked = /\[[xX]\]/.test(task) ? " checked" : "";
        html.push(`<li class="task-item"><input type="checkbox" disabled${checked}>${body}</li>`);
      } else {
        html.push(`<li>${body}</li>`);
      }
      continue;
    }
    if (line.startsWith(">")) {
      closeList();
      html.push(`<blockquote>${renderInlineMarkdown(line.replace(/^>\s?/, ""))}</blockquote>`);
      continue;
    }
    closeList();
    html.push(`<p>${renderInlineMarkdown(line)}</p>`);
  }
  closeList();
  if (inCode) html.push("</code></pre>");
  return html.join("");
}

function renderFrontMatter(lines) {
  const rows = lines
    .map((line) => line.match(/^([^:#][^:]*):\s*(.*)$/))
    .filter(Boolean)
    .map((match) => `<dt>${renderInlineMarkdown(match[1].trim())}</dt><dd>${renderInlineMarkdown(match[2].trim())}</dd>`);
  if (rows.length === 0) {
    return "";
  }
  return `<dl class="front-matter">${rows.join("")}</dl>`;
}

function isTableStart(lines, index) {
  return hasTableCells(lines[index]) && isTableSeparator(lines[index + 1]);
}

function collectTable(lines, index) {
  const rows = [splitTableRow(lines[index])];
  index += 2;
  while (index < lines.length && hasTableCells(lines[index])) {
    rows.push(splitTableRow(lines[index]));
    index++;
  }
  return { rows, nextIndex: index };
}

function renderTable(rows) {
  const [header, ...body] = rows;
  const head = `<thead><tr>${header.map((cell) => `<th>${renderInlineMarkdown(cell)}</th>`).join("")}</tr></thead>`;
  const rowsHTML = body
    .map((row) => `<tr>${header.map((_cell, idx) => `<td>${renderInlineMarkdown(row[idx] || "")}</td>`).join("")}</tr>`)
    .join("");
  return `<table>${head}<tbody>${rowsHTML}</tbody></table>`;
}

function hasTableCells(line) {
  return typeof line === "string" && line.includes("|") && splitTableRow(line).length > 1;
}

function isTableSeparator(line) {
  if (!hasTableCells(line)) return false;
  return splitTableRow(line).every((cell) => /^:?-{3,}:?$/.test(cell.trim()));
}

function splitTableRow(line) {
  return String(line || "")
    .trim()
    .replace(/^\|/, "")
    .replace(/\|$/, "")
    .split("|")
    .map((cell) => cell.trim());
}

function renderComposeDrafts() {
  const list = $("#compose-writing");
  if (!list) return;
  if (!state.composeDrafts || state.composeDrafts.length === 0) {
    list.innerHTML = `<div class="topic-meta">${escapeHTML(t("compose.writing_empty"))}</div>`;
    return;
  }
  list.innerHTML = state.composeDrafts
    .map((draft) => {
      const active = state.composeDraft?.id === draft.id ? " active" : "";
      const status = draft.status || "draft";
      const statusLabel = t(`compose.status.${status}`) || status;
      const profile = profileByID(draft.document_profile?.profile_id || "", draft.document_profile?.version || 0);
      const formatLabel = profile ? profileLabel(profile) : (draft.format || t("compose.format.summary"));
      const title = draft.goal || draft.id || "";
      return `
        <article class="approval-item compose-draft-item${active}" data-compose-id="${escapeHTML(draft.id)}" data-compose-status="${escapeHTML(status)}">
          <div class="compose-draft-content">
            <strong class="compose-draft-title">${escapeHTML(title)}</strong>
            <div class="topic-meta">
              <span class="pill">${escapeHTML(statusLabel)}</span>
              <span>${escapeHTML(formatLabel)}</span>
              <span>${escapeHTML(fmtDate(draft.updated_at || draft.created_at))}</span>
            </div>
          </div>
          <button class="compose-draft-delete" type="button" data-delete-compose="${escapeHTML(draft.id)}" data-delete-compose-title="${escapeHTML(title)}" aria-label="${escapeHTML(t("compose.delete"))}" title="${escapeHTML(t("compose.delete"))}">
            <svg viewBox="0 0 24 24" aria-hidden="true" focusable="false"><path d="M4 7h16M9 7V5h6v2m-8 0 1 13h8l1-13m-7 4v5m4-5v5" /></svg>
          </button>
        </article>
      `;
    })
    .join("");
  list.querySelectorAll("[data-compose-id]").forEach((item) => {
    item.addEventListener("click", (event) => {
      if (event.target?.closest?.("[data-delete-compose]")) return;
      loadComposeDraft(item.dataset.composeId).catch((error) => toast(error.message));
    });
  });
  list.querySelectorAll("[data-delete-compose]").forEach((button) => {
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      deleteComposeDraft(button.dataset.deleteCompose, button.dataset.deleteComposeTitle).catch((error) => toast(error.message));
    });
  });
}

async function loadPrivacyReport() {
  const node = $("#privacy-report");
  if (!node) return;
  try {
    const report = await api("/api/system/privacy");
    node.innerHTML = `<p>${escapeHTML(t("privacy.origin"))}</p>` + [report.llm, report.embedding, report.reader].filter(Boolean).map((surface) => `<p><strong>${escapeHTML(surface.kind)}</strong>: ${escapeHTML(t(surface.enabled ? "privacy.enabled" : "privacy.disabled"))}${surface.enabled ? ` · ${escapeHTML(surface.base_url || "")}` : ""}</p>`).join("");
  } catch (_) { node.textContent = t("privacy.failed"); }
}

async function loadStatus() {
  try {
    const status = await api("/api/system/status");
    state.status = status;
    const statusText = `${status.workspace.id} / ${status.status}`;
    $("#system-status").textContent = statusText;
    const aboutStatus = $("#about-system-status");
    if (aboutStatus) aboutStatus.textContent = statusText;
    $("#dashboard-workspace").textContent = status.workspace?.status || status.status;
    $("#dashboard-llm").textContent = status.llm?.status || t("toast.unknown");
    $("#dashboard-embedding").textContent = status.embedding?.status || t("toast.unknown");
    $("#dashboard-git").textContent = status.git?.status || t("toast.unknown");
    $("#dashboard-search").textContent = status.duckdb?.status || t("toast.unknown");
    // PR3: status tiles now live in the settings drawer; renderSettingsStatus
    // applies them. The dashboard cards above keep the lightweight summary.
    renderTopbarStatus(status);
    renderSettingsStatus(status);
    renderAboutDocumentProfiles();
    const dashboardAlert = $("#dashboard-alert");
    if (dashboardAlert) {
      dashboardAlert.hidden = status.status === "ready";
      dashboardAlert.textContent = status.status === "ready" ? "" : t("dashboard.status_alert", { status: status.status });
    }
  } catch (error) {
    $("#system-status").textContent = "degraded";
    const aboutStatus = $("#about-system-status");
    if (aboutStatus) aboutStatus.textContent = "degraded";
    const dashboardAlert = $("#dashboard-alert");
    if (dashboardAlert) {
      dashboardAlert.hidden = false;
      dashboardAlert.textContent = error.message;
    }
    const alert = $("#settings-drawer-degraded");
    if (alert) {
      alert.hidden = false;
      alert.textContent = error.message;
    }
  }
}

async function loadMetrics() {
  try {
    const metrics = await api("/api/system/metrics");
    state.metrics = metrics;
    renderMetrics(metrics);
    renderSidebarBadges();
  } catch (error) {
    const node = $("#settings-drawer-metrics-json");
    if (node) node.innerHTML = `<div class="tf-alert tf-alert-warning">${escapeHTML(error.message)}</div>`;
  }
}

function formatBadgeCount(value) {
  const n = Number(value);
  if (!Number.isFinite(n) || n <= 0) return "";
  if (n > 99) return "99+";
  return String(n);
}

function computeSidebarBadgeCounts(snapshot) {
  const state = snapshot || {};
  return {
    notes: formatBadgeCount(state.notes?.length),
    topics: formatBadgeCount(state.topics?.length),
    compose: formatBadgeCount(state.composeDrafts?.length),
  };
}

function renderSidebarBadges() {
  const counts = computeSidebarBadgeCounts(state);
  for (const [key, value] of Object.entries(counts)) {
    const node = document.querySelector(`.tf-menu-badge[data-badge="${key}"]`);
    if (!node) continue;
    if (value) {
      node.textContent = value;
      node.dataset.count = String(value);
    } else {
      node.textContent = "";
      delete node.dataset.count;
    }
  }
}

function thoughtDisplayTitle(thought = {}) {
  return String(thought.display_title || thought.user_title || thought.extracted_title || thought.id || "").trim();
}

function thoughtTimeValue(value) {
  const parsed = new Date(value || "");
  return Number.isNaN(parsed.getTime()) ? 0 : parsed.getTime();
}

function sortThoughts(thoughts) {
  return [...(thoughts || [])].sort((left, right) => {
    const updatedDelta = thoughtTimeValue(right.updated_at) - thoughtTimeValue(left.updated_at);
    if (updatedDelta !== 0) return updatedDelta;
    const createdDelta = thoughtTimeValue(right.created_at) - thoughtTimeValue(left.created_at);
    if (createdDelta !== 0) return createdDelta;
    return String(right.id || "").localeCompare(String(left.id || ""));
  });
}

function upsertThoughtRecord(thought, { render = true } = {}) {
  if (!thought || !thought.id) return;
  const next = Array.isArray(state.notes) ? state.notes.slice() : [];
  const index = next.findIndex((item) => item.id === thought.id);
  if (index >= 0) next[index] = { ...next[index], ...thought };
  else next.push(thought);
  state.notes = sortThoughts(next);
  if (render) {
    renderThoughtsList();
    renderSidebarBadges();
  }
}

const DASHBOARD_CAPTURE_LIMIT = 8;

function renderDashboardCaptureSessionItem(session) {
  const label = captureSessionLabel(session);
  const status = session.thoughtId ? t("capture.drawer.archived") : t("capture.drawer.draft");
  return `
    <article class="result-item" data-dashboard-session-id="${escapeHTML(session.sessionId || "")}">
      <strong>${escapeHTML(label)}</strong>
      <div class="result-meta">${escapeHTML(status)} · ${escapeHTML(fmtDate(session.updatedAt))}</div>
    </article>
  `;
}

// The overview deliberately renders persisted capture sessions instead of the
// live domain-event stream. Sessions are user-visible conversation history and
// can be reopened; events contain background implementation details.
function renderDashboardCaptureSessions() {
  const list = $("#dashboard-capture-sessions");
  if (!list) return;
  const sessions = (state.capture.sessions || []).slice(0, DASHBOARD_CAPTURE_LIMIT);
  if (sessions.length === 0) {
    list.innerHTML = `<div class="tf-empty">${escapeHTML(t("empty.no_capture"))}</div>`;
    return;
  }
  list.innerHTML = sessions.map((session) => renderDashboardCaptureSessionItem(session)).join("");
  list.querySelectorAll("[data-dashboard-session-id]").forEach((item) => {
    item.addEventListener("click", () => {
      const sessionID = item.dataset.dashboardSessionId;
      if (!sessionID) return;
      navigateHash(buildRouteHash("capture"), { force: true });
      switchCaptureSession(sessionID);
    });
  });
}

function renderThoughtListItem(thought) {
  const title = thoughtDisplayTitle(thought) || thought.id || "";
  const updatedAt = fmtDate(thought.updated_at || thought.created_at);
  const summary = String(thought.summary || thought.url || "").trim();
  const tags = [...(thought.user_tags || []), ...(thought.ai_tags || [])]
    .filter((tag) => typeof tag === "string" && tag.trim())
    .slice(0, 5)
    .map((tag) => `<span class="pill">${escapeHTML(tag)}</span>`)
    .join("");
  const statuses = [
    [t("capture.card.status_refine"), thought.refine_status || ""],
    [t("capture.card.status_index"), thought.index_status || ""],
    [t("capture.card.status_topic"), thought.topic_status || ""],
  ]
    .filter(([, value]) => value)
    .map(([label, value]) => `<span class="pill" title="${escapeHTML(label)}">${escapeHTML(value)}</span>`)
    .join("");
  const active = state.activeThoughtId === thought.id ? " active" : "";
  return `
    <article class="result-item${active}" data-thought-id="${escapeHTML(thought.id || "")}">
      <div>
        <strong>${escapeHTML(title)}</strong>
        <div class="result-meta">${escapeHTML(updatedAt)}${thought.path ? ` · ${escapeHTML(thought.path)}` : ""}</div>
        ${summary ? `<div class="result-meta">${escapeHTML(summary)}</div>` : ""}
        ${(statuses || tags) ? `<div class="score-line">${statuses}${tags}</div>` : ""}
        <div class="tf-action-row">
          <button class="mini-button" data-read-thought="${escapeHTML(thought.id || "")}" type="button">${escapeHTML(t("notes.reader.open"))}</button>
          <button class="mini-button" data-reopen-capture="${escapeHTML(thought.id || "")}" type="button">${escapeHTML(t("thoughts.action.reopen_capture"))}</button>
          <button class="mini-button" data-note-compose="${escapeHTML(thought.id || "")}" type="button">${escapeHTML(t("search.result.add_source"))}</button>
          ${(publicThoughtSharingEnabled() && thought.id) ? `<button class="mini-button" data-copy-thought-id="${escapeHTML(thought.id)}" type="button">${escapeHTML(t("search.result.copy_share_link"))}</button>` : ""}
        </div>
      </div>
    </article>
  `;
}

function renderThoughtsList() {
  const list = $("#thought-list");
  if (!list) return;
  const count = $("#thought-list-count");
  const filterText = ($("#thought-filter")?.value || "").trim().toLowerCase();
  const thoughts = Array.isArray(state.notes) ? state.notes : [];
  const filtered = thoughts.filter((thought) => {
    const haystack = [
      thought.id,
      thought.display_title,
      thought.user_title,
      thought.extracted_title,
      thought.summary,
      thought.url,
      ...(thought.user_tags || []),
      ...(thought.ai_tags || []),
      ...(thought.topic_ids || []),
    ]
      .filter(Boolean)
      .join(" ")
      .toLowerCase();
    return !filterText || haystack.includes(filterText);
  });
  if (count) {
    count.setAttribute("data-n", String(filtered.length));
    count.textContent = t("notes.list_count", { n: filtered.length });
  }
  if (thoughts.length === 0) {
    list.innerHTML = `<div class="tf-empty" data-i18n="notes.list_empty">${escapeHTML(t("notes.list_empty"))}</div>`;
    return;
  }
  if (filtered.length === 0) {
    list.innerHTML = `<div class="tf-empty" data-i18n="notes.list_empty_filtered">${escapeHTML(t("notes.list_empty_filtered"))}</div>`;
    return;
  }
  list.innerHTML = filtered.map((thought) => renderThoughtListItem(thought)).join("");
  list.querySelectorAll(".result-item[data-thought-id]").forEach((item) => {
    item.addEventListener("click", (event) => {
      if (event.target.closest("button")) return;
      activateTab("notes-detail", $("#page-thoughts"));
      previewThought(item.dataset.thoughtId, { syncRoute: true }).catch((error) => toast(error.message));
    });
  });
  list.querySelectorAll("[data-note-compose]").forEach((button) => {
    button.addEventListener("click", () => addToComposeSources([button.dataset.noteCompose]));
  });
  bindThoughtReopenButtons(list);
  bindThoughtReadButtons(list);
  list.querySelectorAll("[data-copy-thought-id]").forEach((button) => {
    button.addEventListener("click", () => {
      copyThoughtLink(button.dataset.copyThoughtId || "");
    });
  });
}

function renderEmptyThoughtPanels() {
  thoughtRequestEpoch++;
  const preview = $("#thought-preview");
  if (preview) preview.innerHTML = escapeHTML(t("empty.no_thought"));
  const detail = $("#thought-detail");
  if (detail) detail.innerHTML = `<div class="tf-empty" data-i18n="empty.no_thought">${escapeHTML(t("empty.no_thought"))}</div>`;
  const status = $("#thought-status-detail");
  if (status) status.innerHTML = `<div class="tf-empty" data-i18n="empty.no_thought">${escapeHTML(t("empty.no_thought"))}</div>`;
  const drawer = $("#thought-drawer-content");
  if (drawer) drawer.innerHTML = escapeHTML(t("empty.no_thought"));
  const addCompose = $("#drawer-add-compose");
  if (addCompose) addCompose.disabled = true;
  const retry = $("#retry-refine");
  if (retry) retry.disabled = true;
  const reader = $("#drawer-read-full");
  if (reader) reader.disabled = true;
}

function renderThoughtDetailPanel(snapshot) {
  const thought = snapshot?.thought || {};
  const content = snapshot?.content || {};
  const title = thoughtDisplayTitle(thought) || thought.id || "";
  const keyPoints = Array.isArray(thought.key_points) ? thought.key_points.filter((item) => typeof item === "string" && item.trim()) : [];
  const displayLinks = thoughtLinksForDisplay(content.links || "");
  const sections = [
    `# ${title}`,
    thought.summary ? `## ${t("thoughts.preview_summary")}\n${thought.summary}` : "",
    keyPoints.length > 0 ? `## ${t("notes.detail.key_points")}\n${keyPoints.map((item) => `- ${item}`).join("\n")}` : "",
    content.ai_notes ? `## ${t("notes.detail.ai_notes")}\n${content.ai_notes}` : "",
    `## ${t("thoughts.preview_original")}\n${content.original || ""}`,
    content.extracted_content ? `## ${t("thoughts.preview_extracted")}\n${content.extracted_content}` : "",
    displayLinks ? `## ${t("thoughts.preview_links")}\n${displayLinks}` : "",
    appendExpansionSections(thought),
  ].filter(Boolean);
  const metaRows = [
    [t("notes.status.id"), thought.id],
    [t("notes.status.path"), thought.path],
    [t("notes.status.updated_at"), fmtDate(thought.updated_at || thought.created_at)],
    [t("capture.context.tags"), joinCSV([...(thought.user_tags || []), ...(thought.ai_tags || [])])],
    [t("notes.status.topics"), joinCSV(thought.topic_ids || [])],
  ];
  return `<div class="tf-result">
    <div class="tf-description-list">${renderDescription(metaRows)}</div>
    <div class="tf-action-row">
      <button class="tf-btn" data-read-thought="${escapeHTML(thought.id || "")}" type="button">${escapeHTML(t("notes.reader.open"))}</button>
      <a class="tf-btn" href="/api/thoughts/${encodeURIComponent(thought.id || "")}/markdown">${escapeHTML(t("notes.reader.download"))}</a>
      ${thought.markdown_import ? `<a class="tf-btn" href="/api/thoughts/${encodeURIComponent(thought.id)}/import-source">${escapeHTML(t("notes.reader.original"))}</a><p class="tf-text-secondary">${escapeHTML(t("notes.import.attachments"))}</p>` : ""}
      <button class="tf-btn" data-reopen-capture="${escapeHTML(thought.id || "")}" type="button">${escapeHTML(t("thoughts.action.reopen_capture"))}</button>
      ${thought.document_profile && thought.document_profile.family !== "note" ? `<button class="tf-btn" data-document-open="${escapeHTML(thought.id)}" type="button">${escapeHTML(t("document.open"))}</button>` : ""}
      <button class="tf-btn tf-thought-delete" data-delete-thought="${escapeHTML(thought.id || "")}" data-delete-title="${escapeHTML(title)}" type="button">${escapeHTML(t("thoughts.action.delete"))}</button>
    </div>
    <div class="preview-box tf-thought-content markdown-rendered">${renderObjectMarkdown(sections.join("\n\n"),thought.id,snapshot.resource_bindings)}</div>
  </div>`;
}

function renderThoughtStatusPanel(snapshot) {
  const thought = snapshot?.thought || {};
  const jobs = snapshot?.jobs || [];
  const commits = snapshot?.git_commits || [];
  const errors = thought.errors || [];
  const rows = [
    [t("notes.status.id"), thought.id],
    [t("capture.card.status_capture"), thought.capture_status || t("toast.unknown")],
    [t("capture.card.status_refine"), thought.refine_status || t("toast.unknown")],
    [t("capture.card.status_index"), thought.index_status || t("toast.unknown")],
    [t("capture.card.status_topic"), thought.topic_status || t("toast.unknown")],
    [t("notes.status.path"), thought.path],
    [t("notes.status.created_at"), fmtDate(thought.created_at)],
    [t("notes.status.updated_at"), fmtDate(thought.updated_at)],
    [t("notes.status.source_url"), thought.url],
    [t("notes.status.content_hash"), thought.content_hash],
    [t("capture.context.tags"), joinCSV([...(thought.user_tags || []), ...(thought.ai_tags || [])])],
    [t("notes.status.topics"), joinCSV(thought.topic_ids || [])],
  ];
  const jobsBlock = jobs.length > 0
    ? `<section><h4>${escapeHTML(t("notes.runtime.jobs_label"))}</h4><div class="tf-description-list">${renderDescription(jobs.map((job) => [
      `${job.id || ""} · ${job.type || "job"}`,
      `${job.status || t("toast.unknown")} · ${job.resource_type || ""}:${job.resource_id || ""}`,
    ]))}</div></section>`
    : "";
  const commitsBlock = commits.length > 0
    ? `<section><h4>${escapeHTML(t("notes.status.git"))}</h4><div class="results-list">${commits.map((commit) => `
      <article class="result-item">
        <strong>${escapeHTML(commit.message || commit.commit_hash || "")}</strong>
        <div class="result-meta">${escapeHTML(commit.commit_hash || "")} · ${escapeHTML(fmtDate(commit.committed_at))}</div>
      </article>
    `).join("")}</div></section>`
    : "";
  const errorsBlock = errors.length > 0
    ? `<section><h4>${escapeHTML(t("notes.status.errors"))}</h4><div class="results-list">${errors.map((error) => `
      <article class="result-item">
        <strong>${escapeHTML(error.code || t("common.error"))}</strong>
        <div class="result-meta">${escapeHTML(error.message || "")}</div>
      </article>
    `).join("")}</div></section>`
    : "";
  return `<div class="tf-result">
    <div class="tf-description-list">${renderDescription(rows)}</div>
    ${jobsBlock}
    ${commitsBlock}
    ${errorsBlock}
  </div>`;
}

function renderThoughtPanels(snapshot = state.activeThoughtSnapshot) {
  if (!snapshot || !snapshot.thought || !snapshot.thought.id) {
    renderEmptyThoughtPanels();
    return;
  }
  const thought = snapshot.thought;
  const content = snapshot.content || {};
  const displayLinks = thoughtLinksForDisplay(content.links || "");
  const sections = [
    `# ${thoughtDisplayTitle(thought) || thought.id}`,
    "",
    `status: ${thought.refine_status} / ${thought.index_status} / ${thought.topic_status}`,
    `path: ${thought.path}`,
    "",
    `## ${t("thoughts.preview_summary")}`,
    thought.summary || t("compose.empty"),
    content.ai_notes ? `## ${t("notes.detail.ai_notes")}\n${content.ai_notes}` : "",
    "",
    `## ${t("thoughts.preview_original")}`,
    content.original || "",
    "",
    content.extracted_content ? `## ${t("thoughts.preview_extracted")}\n${content.extracted_content}` : "",
    displayLinks ? `## ${t("thoughts.preview_links")}\n${displayLinks}` : "",
    (snapshot.jobs || []).length > 0 ? `## ${t("thoughts.preview_jobs")}\n${(snapshot.jobs || []).map((job) => `- ${job.id} (${job.status})`).join("\n")}` : "",
  ];
  sections.push(appendExpansionSections(thought));
  const html = renderObjectMarkdown(sections.filter(Boolean).join("\n"),thought.id,snapshot.resource_bindings);
  const preview = $("#thought-preview");
  if (preview) preview.innerHTML = html;
  const drawer = $("#thought-drawer-content");
  if (drawer) drawer.innerHTML = html;
  const detail = $("#thought-detail");
  if (detail) detail.innerHTML = renderThoughtDetailPanel(snapshot);
  const status = $("#thought-status-detail");
  if (status) status.innerHTML = renderThoughtStatusPanel(snapshot);
  bindThoughtReopenButtons(document);
  bindThoughtReadButtons(document);
  bindThoughtDeleteButtons(document);
 bindDocumentButtons(document);
  const addCompose = $("#drawer-add-compose");
  if (addCompose) addCompose.disabled = false;
  const retry = $("#retry-refine");
  if (retry) retry.disabled = false;
  const reader = $("#drawer-read-full");
  if (reader) reader.disabled = false;
  renderThoughtsList();
}

async function loadThoughts() {
  const thoughts = await api("/api/thoughts");
  state.notes = sortThoughts(Array.isArray(thoughts) ? thoughts : []);
  if (state.activeThoughtSnapshot?.thought?.id) {
    upsertThoughtRecord(state.activeThoughtSnapshot.thought, { render: false });
  }
  renderThoughtsList();
  renderSidebarBadges();
}

// PR3: the notes runtime card is a lightweight summary — most recent jobs
// and a clone of the live event stream. We hit the existing endpoints
// (jobs + status) instead of inventing a new "runtime summary" one. The
// card is refreshed on demand via the Refresh button; opening the
// collapsible details element also refreshes once.
async function refreshNotesRuntime() {
  const jobsNode = $("#notes-runtime-jobs");
  const eventsNode = $("#notes-runtime-events");
  if (jobsNode) jobsNode.innerHTML = `<div class="tf-text-secondary">${escapeHTML(t("common.loading"))}</div>`;
  if (eventsNode) eventsNode.innerHTML = "";
  let jobs = [];
  try {
    const list = await api("/api/jobs?limit=8");
    jobs = Array.isArray(list) ? list : (list?.jobs || []);
  } catch (_error) {
    // The /api/jobs endpoint may not exist on older builds. Show a hint
    // rather than failing the whole card.
    if (jobsNode) {
      jobsNode.innerHTML = `<div class="tf-empty" data-i18n="notes.runtime.empty">${escapeHTML(t("notes.runtime.empty"))}</div>`;
    }
  }
  if (jobsNode) {
    if (jobs.length === 0) {
      jobsNode.innerHTML = `<div class="tf-empty" data-i18n="notes.runtime.empty">${escapeHTML(t("notes.runtime.empty"))}</div>`;
    } else {
      jobsNode.innerHTML = renderDescription(jobs.map((job) => [
        `${escapeHTML(job.id || "")} · ${escapeHTML(job.type || "job")}`,
        `${escapeHTML(job.status || t("toast.unknown"))} · ${escapeHTML(job.resource_type || "")}:${escapeHTML(job.resource_id || "")}`,
      ]));
    }
  }
  // The overview is intentionally capture-only. The runtime card still
  // exposes the full event stream by cloning the settings event list.
  if (eventsNode) {
    const source = $("#settings-drawer-event-list");
    const items = source
      ? Array.from(source.children).filter((item) => item?.classList?.contains?.("event-item")).slice(0, 6)
      : [];
    if (items.length === 0) {
      eventsNode.innerHTML = `<div class="tf-empty" data-i18n="settings.events.empty">${escapeHTML(t("settings.events.empty"))}</div>`;
    } else {
      eventsNode.append(...items.map((item) => item.cloneNode(true)));
    }
  }
}

function renderTopbarStatus(status) {
  const topbar = $("#topbar-status");
  if (!topbar || !status) return;
  const items = [
    [t("topbar.badge.workspace"), status.workspace?.status],
    [t("topbar.badge.llm"), status.llm?.status],
    [t("topbar.badge.embedding"), status.embedding?.status],
    [t("topbar.badge.git"), status.git?.status],
    [t("topbar.badge.search"), status.duckdb?.status],
  ];
  topbar.innerHTML = items
    .map(([label, value]) => `<span class="${statusBadge(value)}">${escapeHTML(label)} · ${escapeHTML(value || t("toast.unknown"))}</span>`)
    .join("");
}

// PR3: status tiles live in the settings drawer. Tiles are grouped by tab —
// workspace + duckdb go to the index tab, llm + embedding to the models tab,
// git to the sync tab. The degraded alert lives at the bottom of the index
// tab so it shows in the most likely "something is wrong" landing spot.
function renderSettingsStatus(status) {
  if (!status) return;
  const map = {
    "settings-drawer-workspace": displayWorkspace(status.workspace),
    "settings-drawer-duckdb": displayRuntimePath(status.duckdb?.path, status.workspace?.root_path) || status.duckdb?.status || t("toast.unknown"),
    "settings-drawer-llm": `${status.llm?.status || t("toast.unknown")} · ${status.llm?.chat_model || "local"}`,
    "settings-drawer-embedding": `${status.embedding?.status || t("toast.unknown")} · ${status.embedding?.model || "local"}`,
  };
  for (const [id, value] of Object.entries(map)) {
    const node = $(`#${id}`);
    if (node) node.textContent = value;
  }
  const alert = $("#settings-drawer-degraded");
  if (alert) {
    const degraded = status.status && status.status !== "ready";
    alert.hidden = !degraded;
    alert.textContent = degraded ? t("settings.degraded_alert", { status: status.status }) : "";
  }
  const index = $("#settings-drawer-index-detail");
  if (index) {
    index.innerHTML = renderDescription([
      [t("settings.duckdb_status"), status.duckdb?.status || t("toast.unknown")],
      [t("settings.duckdb_path"), displayRuntimePath(status.duckdb?.path, status.workspace?.root_path) || t("toast.unknown")],
      [t("settings.background"), status.background?.status || t("toast.unknown")],
      [t("settings.events"), status.events?.status || t("toast.unknown")],
    ]);
  }
  const git = $("#settings-drawer-git-detail");
  if (git) {
    git.innerHTML = renderDescription([
      [t("settings.git_status"), status.git?.status || t("toast.unknown")],
      [t("settings.git_repository"), displayRuntimePath(status.git?.repository || status.workspace?.root_path, status.workspace?.root_path) || displayWorkspace(status.workspace)],
      [t("settings.git_dirty"), status.git?.dirty === undefined ? t("toast.unknown") : String(status.git.dirty)],
      [t("settings.git_error"), status.git?.error || t("topics.rule.none")],
    ]);
  }
}

function renderMetrics(metrics) {
  const node = $("#settings-drawer-metrics-json");
  if (!node) return;
  const values = metrics.values || {};
  const rows = Object.keys(values)
    .sort()
    .map((key) => [key, String(values[key])]);
  node.innerHTML = rows.length > 0 ? renderDescription(rows) : `<div class="tf-empty">${escapeHTML(t("settings.metrics_empty"))}</div>`;
}

async function loadTopics() {
  const topics = await api("/api/topics");
  state.topics = topics || [];
  renderTopics();
  renderSidebarBadges();
}

function renderTopics() {
  const list = $("#topic-list");
  const textFilter = ($("#topic-filter")?.value || "").trim().toLowerCase();
  const filteredTopics = state.topics.filter((topic) => {
    const text = `${topic.name || ""} ${topic.description || ""} ${topic.id || ""}`.toLowerCase();
    const matchesText = !textFilter || text.includes(textFilter);
    return matchesText;
  });
  if (state.topics.length === 0) {
    state.activeTopicId = "";
    state.activeTopicDetail = null;
    populateTopicEditor(null);
    $("#topic-document").innerHTML = renderMarkdown(t("topics.document_empty"));
    $("#refresh-topic").disabled = true;
    $("#open-topic-rules").disabled = true;
    $("#topic-members").innerHTML = `<div class="tf-empty">${escapeHTML(t("empty.no_topic"))}</div>`;
    $("#topic-rules-summary").innerHTML = escapeHTML(t("empty.no_topic"));
    list.innerHTML = `<div class="topic-meta">${escapeHTML(t("topics.empty"))}</div>`;
    return;
  }
  if (filteredTopics.length === 0) {
    list.innerHTML = `<div class="topic-meta">${escapeHTML(t("topics.empty_filtered"))}</div>`;
    return;
  }
  list.innerHTML = filteredTopics
    .map((topic) => {
      const active = topic.id === state.activeTopicId ? " active" : "";
      return `
        <article class="topic-item${active}" data-topic-id="${escapeHTML(topic.id)}">
          <strong>${escapeHTML(topic.name)}</strong>
          <div class="topic-meta">${topic.member_count || 0} thoughts · ${topic.word_count || 0} words</div>
          <div class="topic-meta">${escapeHTML(topic.description || t("topics.no_description"))}</div>
        </article>
      `;
    })
    .join("");
  list.querySelectorAll(".topic-item").forEach((item) => {
    item.addEventListener("click", (event) => {
      if (event.target.closest("button")) return;
      navigateTopic(item.dataset.topicId);
    });
  });
}

function resetTopicFilters() {
  $("#topic-filter").value = "";
  renderTopics();
}

function navigateTopic(topicId) {
  if (!topicId) return;
  navigateHash(`#/topics/${encodeURIComponent(topicId)}`, { force: true });
}

async function openTopic(topicId) {
  const epoch = ++topicRequestEpoch;
  if (!topicId) return;
  const detail = await api(`/api/topics/${encodeURIComponent(topicId)}`);
  if (epoch !== topicRequestEpoch) return;
  state.activeTopicId = topicId;
  state.activeTopicDetail = detail;
  // Topic detail / rules are tabs inside the topics
  // page; enable them once a topic is loaded and populate the document
  // panel. Tab activation follows the URL `?tab=` query so deep-links
  // land on the right pane.
  ["topics-tab-detail", "topics-tab-rules"].forEach((id) => {
    const el = document.getElementById(id);
    if (el) el.disabled = false;
  });
  const collection = detail.topic?.mode === "collection";
 $("#topic-document").closest(".tf-topic-detail-layout")?.classList.toggle("is-collection",collection);
 if (collection) { await openCollectionWorkspace(topicId,epoch); if(epoch!==topicRequestEpoch)return; }
 else { $("#topic-document").innerHTML = renderTopicDocumentMarkdown(detail.document || t("topics.document_empty")); renderAggregateConversion(topicId); }
  $("#refresh-topic").disabled = false;
  $("#open-topic-rules").disabled = false;
  populateTopicEditor(detail.topic);
  renderTopicMembers(detail.members || []);
  renderTopicRules(detail.topic);
  renderTopics();
  await loadTopicCandidates(topicId);
}

async function loadTopicCandidates(topicId = state.activeTopicId) {
  if (!topicId) return [];
  try {
    const candidates = await api(`/api/topics/${encodeURIComponent(topicId)}/candidates`);
    if (topicId !== state.activeTopicId) return [];
    renderTopicCandidatesInto(candidates || []);
    return candidates || [];
  } catch (error) {
    // The candidate list is best-effort observability — a transient
    // failure should not break the topic detail view. Surface the
    // error in the candidate panel so the user can retry.
    if (topicId !== state.activeTopicId) return [];
    const node = $("#topic-candidates");
    if (node) node.innerHTML = `<div class="tf-empty">${escapeHTML(t("toast.request_failed"))}: ${escapeHTML(error.message)}</div>`;
    return [];
  }
}

function renderTopicCandidates(candidates) {
  if (!Array.isArray(candidates) || candidates.length === 0) {
    return `<div class="tf-empty">${escapeHTML(t("topics.candidates_empty"))}</div>`;
  }
  const items = candidates.map(renderTopicCandidateImpact).join("");
  const header = `<div class="tf-card-header"><h3>${escapeHTML(t("topics.candidates_title"))}</h3><span class="tf-text-secondary">${escapeHTML(t("topics.candidates_hint"))}</span></div>`;
  return `${header}<div class="approval-list">${items}</div>`;
}

function renderTopicCandidatesInto(candidates) {
  const node = $("#topic-candidates");
  if (!node) return;
  node.innerHTML = renderTopicCandidates(candidates);
}

function renderTopicCandidateImpact(candidate) {
  const sourceLabel = t(`topics.candidate_source.${candidate.source}`) || candidate.source;
  const status = candidate.status ? `<span class="pill">${escapeHTML(candidate.status)}</span>` : "";
  const reasons = Array.isArray(candidate.reasons) && candidate.reasons.length > 0
    ? `<div class="topic-meta">${candidate.reasons.map((reason) => escapeHTML(reason)).join(" · ")}</div>`
    : "";
  const referenceID = candidate.thought_id || candidate.draft_id || candidate.session_id || candidate.candidate_id || "";
  const thoughtID = candidate.thought_id || "";
  return `
    <article class="result-item" data-candidate-source="${escapeHTML(candidate.source)}" data-candidate-id="${escapeHTML(candidate.candidate_id)}" data-candidate-ref="${escapeHTML(referenceID)}" data-candidate-thought="${escapeHTML(thoughtID)}">
      <div class="result-row">
        <div>
          <strong>${escapeHTML(candidate.title || candidate.candidate_id || t("topics.candidates_empty"))}</strong>
          <div class="topic-meta">
            <span class="pill">${escapeHTML(sourceLabel)}</span>
            ${candidate.match_type ? `<span class="pill">${escapeHTML(candidate.match_type)}</span>` : ""}
            ${status}
            ${typeof candidate.score === "number" ? `<span class="pill green">${t("search.score_label")} ${score(candidate.score)}</span>` : ""}
          </div>
          ${reasons}
        </div>
      </div>
    </article>
  `;
}

function renderTopicMembers(members) {
  const node = $("#topic-members");
  if (!node) return;
  if (!members || members.length === 0) {
    node.innerHTML = `<div class="tf-empty">${escapeHTML(t("empty.no_members"))}</div>`;
    return;
  }
  node.innerHTML = members
    .map((member) => `
      <article class="result-item">
        <div class="result-row">
          <div>
            <strong>${escapeHTML(member.title || member.thought_id || member.id)}</strong>
            <div class="result-meta">${escapeHTML(member.match_type || t("match.label"))} · ${t("match.score", { value: score(member.score) })}</div>
            <div class="score-line">
              <button class="mini-button" data-preview-id="${escapeHTML(member.thought_id || member.id)}" type="button">${escapeHTML(t("search.result.preview"))}</button>
              <button class="mini-button" data-compose-source-id="${escapeHTML(member.thought_id || member.id)}" type="button">${escapeHTML(t("search.result.add_source"))}</button>
            </div>
          </div>
        </div>
      </article>
    `)
    .join("");
  node.querySelectorAll("[data-preview-id]").forEach((button) => {
    button.addEventListener("click", () => previewThought(button.dataset.previewId, { drawer: true }).catch((error) => toast(error.message)));
  });
  node.querySelectorAll("[data-compose-source-id]").forEach((button) => {
    button.addEventListener("click", () => {
      // Stay on the topic page after enqueueing — only add to the source queue.
      addToComposeSources([button.dataset.composeSourceId]);
    });
  });
}

function renderTopicRules(topic) {
  const node = $("#topic-rules-summary");
  if (!node || !topic) return;
  const rules = topic.rules || {};
  const keywords = rules.keywords || {};
  const tags = rules.tags || {};
  const semantic = rules.semantic || {};
  const rows = [
    [t("topics.rule.keywords_any"), joinCSV(keywords.any) || t("topics.rule.none")],
    [t("topics.rule.tags_any"), joinCSV(tags.any) || t("topics.rule.none")],
  ];
  if ((keywords.all || []).length > 0) rows.push([t("topics.rule.keywords_all"), joinCSV(keywords.all)]);
  if ((keywords.exclude || []).length > 0) rows.push([t("topics.rule.keywords_exclude"), joinCSV(keywords.exclude)]);
  if ((rules.manual_include || []).length > 0) rows.push([t("topics.rule.manual_include"), joinCSV(rules.manual_include)]);
  if ((rules.manual_exclude || []).length > 0) rows.push([t("topics.rule.manual_exclude"), joinCSV(rules.manual_exclude)]);
  if (semantic.enabled) rows.push([t("topics.rule.semantic"), t("topics.rule.semantic_enabled", { threshold: semantic.threshold || 0.75 })]);
  const outline = outlineText(topic.outline);
  if (outline && outline !== outlineText(outlineFromText("Notes\nOpen Questions"))) rows.push([t("topics.rule.outline"), outline]);
  node.innerHTML = renderDescription(rows);
}

function populateTopicEditor(topic) {
  const fields = [
    "edit-topic-name",
    "edit-topic-description",
    "edit-keywords-any",
    "edit-tags-any",
    "save-topic-rules",
  ];
  const enabled = Boolean(topic);
  fields.forEach((id) => {
    const node = $(`#${id}`);
    if (node) node.disabled = !enabled;
  });
  if (!topic) return;
  const rules = topic.rules || {};
  const keywords = rules.keywords || {};
  const tags = rules.tags || {};
  $("#edit-topic-name").value = topic.name || "";
  $("#edit-topic-description").value = topic.description || "";
  $("#edit-keywords-any").value = joinCSV(keywords.any);
  $("#edit-tags-any").value = joinCSV(tags.any);
}

async function createTopic(event) {
  event.preventDefault();
  const name = $("#topic-name").value.trim();
  if (!name) {
    toast(t("toast.topic_name_required"));
    return;
  }
  const topic = await api("/api/topics", {
    method: "POST",
    body: JSON.stringify({
      name,
 mode: $("#topic-mode")?.value || "aggregate",
 operation_id: workspaceOperationID(),
 search_policy: {topic_global_visible: true, owned_thoughts_global_default: true},
      description: $("#topic-description").value.trim(),
      rules: {
        keywords: {
          any: csv($("#topic-keywords").value),
          all: [],
          exclude: [],
        },
        tags: { any: csv($("#topic-tags").value) },
        semantic: { enabled: false, threshold: 0.75 },
        manual_include: [],
        manual_exclude: [],
      },
      outline: outlineFromText("Notes\nOpen Questions"),
    }),
  });
  event.target.reset();
  toast(t("toast.topic_created"));
  closeDrawer("topic-create-drawer");
  await loadTopics();
  navigateTopic(topic.id);
}

async function saveTopicRules(event) {
  event.preventDefault();
  if (!state.activeTopicId) {
    toast(t("toast.select_topic_first"));
    return;
  }
  const currentTopic = state.activeTopicDetail?.topic || {};
  const currentRules = currentTopic.rules || {};
  const currentKeywords = currentRules.keywords || {};
  const currentSemantic = currentRules.semantic || {};
  const topic = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}`, {
    method: "PUT",
    body: JSON.stringify({
      name: $("#edit-topic-name").value.trim(),
      description: $("#edit-topic-description").value.trim(),
      rules: {
        keywords: {
          any: csv($("#edit-keywords-any").value),
          all: Array.isArray(currentKeywords.all) ? currentKeywords.all : [],
          exclude: Array.isArray(currentKeywords.exclude) ? currentKeywords.exclude : [],
        },
        tags: { any: csv($("#edit-tags-any").value) },
        semantic: {
          enabled: Boolean(currentSemantic.enabled),
          threshold: Number.isFinite(currentSemantic.threshold) && currentSemantic.threshold > 0 ? currentSemantic.threshold : 0.75,
        },
        manual_include: Array.isArray(currentRules.manual_include) ? currentRules.manual_include : [],
        manual_exclude: Array.isArray(currentRules.manual_exclude) ? currentRules.manual_exclude : [],
      },
      outline: Array.isArray(currentTopic.outline) ? currentTopic.outline : outlineFromText("Notes\nOpen Questions"),
    }),
  });
  toast(t("toast.topic_rules_saved"));
  closeDrawer("topic-rules-drawer");
  await loadTopics();
  navigateTopic(topic.id);
}

async function captureThought(event) {
  event.preventDefault();
  // Phase 9: the legacy single-form capture path is replaced by the
  // conversation composer. Kept as a no-op shim so the old form's
  // submit handler doesn't fire on legacy markup; new UI lives in
  // #capture-composer.
  $("#capture-composer-input")?.focus();
}

function renderCaptureResult(result) {
  // Phase 9: result rendering now lives in renderCaptureConversation().
  // This shim is kept for any remaining legacy callers.
  if (!result?.thought) return;
  appendCaptureMessage({
    role: "system",
    text: t("capture.session.saved_path", { id: result.thought.id }),
  });
}

function renderJobLinks(jobs) {
  if (!jobs || jobs.length === 0) return `<div class="tf-empty">${escapeHTML(t("capture.result.no_jobs"))}</div>`;
  return `<div class="tf-job-links">${jobs
    .map((job) => `<a class="${statusBadge(job.status)}" href="#/notes">${escapeHTML(job.type || "job")} · ${escapeHTML(job.status || "queued")}</a>`)
    .join("")}</div>`;
}

// ----- Phase 9: Capture conversation ---------------------------------

function newCaptureSessionId() {
  if (typeof crypto !== "undefined" && typeof crypto.randomUUID === "function") {
    return crypto.randomUUID();
  }
  return "cap-" + Math.random().toString(36).slice(2) + "-" + Date.now().toString(36);
}

function appendCaptureMessage(message) {
  if (!message || !message.role) return null;
  const entry = {
    id: `msg-${Date.now()}-${Math.random().toString(36).slice(2, 6)}`,
    at: new Date().toISOString(),
    // thoughtId binds the message to an active thought; when set, the
    // html is regenerated on every render from the freshest snapshot so
    // SSE updates (refined / expanded / patched) flow into the bubble
    // in place without rewriting the conversation history.
    thoughtId: message.thoughtId || null,
    ...message,
  };
  state.capture.messages = [...(state.capture.messages || []), entry];
  renderCaptureConversation();
  return entry;
}

function upsertCaptureMessage(kind, message) {
  if (!kind || !message || !message.role) return null;
  const sessionId = message.sessionId || state.capture.sessionId || "";
  const messages = state.capture.messages || [];
  const messageKey = message.messageKey || "";
  const idx = messages.findIndex((item) =>
    item.kind === kind &&
    (item.sessionId || "") === sessionId &&
    (item.messageKey || "") === messageKey
  );
  if (idx >= 0) {
    const existing = messages[idx];
    const next = {
      ...existing,
      ...message,
      kind,
      sessionId,
      messageKey,
      at: new Date().toISOString(),
    };
    if (message.moveToEnd) {
      state.capture.messages = messages.filter((_, itemIdx) => itemIdx !== idx).concat(next);
    } else {
      state.capture.messages = messages.map((item, itemIdx) => itemIdx === idx ? next : item);
    }
    renderCaptureConversation();
    return next;
  }
  return appendCaptureMessage({ ...message, kind, sessionId, messageKey });
}

function upsertCaptureContextMessage(options = {}) {
  const sp = options.scratchpad || state.capture.activeScratchpad || {};
  const ctx = sp.session_context || sp.SessionContext || {};
  const sessionId = options.sessionId || state.capture.sessionId || "";
  const messageKey = options.messageKey || latestCaptureUserTurnKey() || "session";
  const pending = Boolean(options.pending);
  if (!sessionId) return null;
  if (!pending && !stringsTrim(ctx.assistant_reply)) return null;
  if (!pending && hasPersistedCaptureContextReply(ctx)) return null;
  const message = {
    role: "ai",
    sessionId,
    messageKey,
    pending,
    moveToEnd: false,
  };
  if (!pending) message.sessionContext = { assistant_reply: stringsTrim(ctx.assistant_reply) };
  return upsertCaptureMessage("context", message);
}

function replaceCaptureMessagesFromScratchpad(detail) {
  if (!detail || !Array.isArray(detail.messages)) return false;
  state.capture.messages = detail.messages.slice();
  return true;
}

function hasPersistedCaptureContextReply(ctx) {
  const reply = stringsTrim(captureContextPlainText({ sessionContext: ctx || {} }));
  if (!reply) return false;
  const replyKey = normalizePlainTextForCompare(reply);
  if (!replyKey) return false;
  const messages = state.capture.messages || [];
  let lastUserIndex = -1;
  for (let index = messages.length - 1; index >= 0; index--) {
    if (messages[index] && messages[index].role === "user") {
      lastUserIndex = index;
      break;
    }
  }
  const start = lastUserIndex >= 0 ? lastUserIndex + 1 : 0;
  return messages.slice(start).some((msg) => (
    msg &&
    msg.role === "ai" &&
    !msg.kind &&
    normalizePlainTextForCompare(msg.text) === replyKey
  ));
}

function stringsTrim(value) {
  return typeof value === "string" ? value.trim() : "";
}

function latestCaptureUserTurnKey() {
  const messages = state.capture.messages || [];
  for (let index = messages.length - 1; index >= 0; index--) {
    const msg = messages[index];
    if (!msg || msg.role !== "user") continue;
    return msg.id || msg.at || `user-${index}`;
  }
  return "";
}

function captureRoleClass(role) {
  switch (role) {
    case "user": return "tf-msg-user";
    case "ai": return "tf-msg-ai";
    case "suggestion": return "tf-msg-suggestion";
    case "system":
    default: return "tf-msg-system";
  }
}

// renderCaptureBubbleBody chooses between a stored static body and a
// re-renderable snapshot. Messages that carry thoughtId always pick up
// the freshest snapshot (refine / expand / patch) so the user sees the
// AI bubble update in place as the pipeline progresses.
function renderCaptureBubbleBody(msg) {
  if (msg.kind === "archive_status") {
    const link = msg.thoughtId ? `<p><a href="${escapeHTML(buildRouteHash("thoughts", { thoughtId: msg.thoughtId }, { id: msg.thoughtId }))}">${escapeHTML(msg.thoughtId)}</a></p>` : "";
    return `<div class="tf-msg-body"><p>${escapeHTML(msg.text)}</p>${msg.error ? `<p>${escapeHTML(msg.error)}</p>` : ""}${link}</div>`;
  }

  if (msg.thoughtId) {
    const snap = state.capture.thoughtSnapshots.get(msg.thoughtId)
      || (state.capture.activeSnapshot
        && state.capture.activeSnapshot.thought
        && state.capture.activeSnapshot.thought.id === msg.thoughtId
        ? state.capture.activeSnapshot
        : null);
    if (snap) return renderMarkdownMessage(thoughtSnapshotPlainText(snap, msg));
  }
  if (msg.kind === "context") return renderMarkdownMessage(captureContextPlainText(msg));
  if (msg.kind === "archive_preview") return "";
  if (msg.html) return msg.html;
  if (msg.text) return renderMarkdownMessage(msg.text);
  return "";
}

function renderMarkdownMessage(text) {
  return `<div class="tf-msg-body tf-msg-body-markdown markdown-rendered">${renderMarkdown(text || "")}</div>`;
}

function thoughtSnapshotPlainText(snapshot, msg = {}) {
  const thought = (snapshot && snapshot.thought) || {};
  const content = (snapshot && snapshot.content) || {};
  const fallback = msg.text || thought.display_title || thought.user_title || thought.extracted_title || thought.id || "";
  const builder = createConversationTextBuilder([content.original, content.Original]);
  builder.addSection("capture.conversation.summary", [thought.summary]);
  builder.addSection("capture.conversation.key_points", thought.key_points, { limit: 5 });
  builder.addSection("capture.conversation.followups", Array.isArray(thought.url_followups)
    ? thought.url_followups.map((item) => [item.title, item.url].filter(Boolean).join(" "))
    : [], { limit: 3 });
  builder.addSection("capture.conversation.expansion_plan", [thought.expansion_plan]);
  return builder.text() || fallback;
}

function captureContextPlainText(message = {}) {
  if (message.pending) return t("capture.context.pending");
  const sp = state.capture.activeScratchpad || {};
  const ctx = message.sessionContext || sp.session_context || sp.SessionContext || {};
  if (stringsTrim(ctx.assistant_reply)) return stringsTrim(ctx.assistant_reply);
  return "";
}

function compactPlainTextParts(parts) {
  const cleaned = (parts || [])
    .flatMap((item) => splitPlainTextPart(item))
    .map((item) => item.trim())
    .filter(Boolean)
    .filter((item) => !isLowSignalCaptureText(item));
  const deduped = [];
  for (const item of cleaned) {
    const key = normalizePlainTextForCompare(item);
    if (!key) continue;
    const coveredByExisting = deduped.some((existing) => {
      const existingKey = normalizePlainTextForCompare(existing);
      return existingKey === key || (existingKey.length > key.length && existingKey.includes(key));
    });
    if (coveredByExisting) continue;
    for (let index = deduped.length - 1; index >= 0; index--) {
      const existingKey = normalizePlainTextForCompare(deduped[index]);
      if (key.length > existingKey.length && key.includes(existingKey)) {
        deduped.splice(index, 1);
      }
    }
    deduped.push(item);
  }
  return deduped;
}

function createConversationTextBuilder(referenceParts = []) {
  const output = [];
  const seen = [];
  const references = compactPlainTextParts(referenceParts);
  const referenceFragments = references.flatMap((reference) => extractReferenceFragments(reference));
  const includesSeen = (item) => {
    const key = normalizePlainTextForCompare(item);
    if (!key) return true;
    if (references.some((reference) => {
      const referenceKey = normalizePlainTextForCompare(reference);
      return referenceKey === key || referenceKey.includes(key);
    })) {
      return true;
    }
    return seen.some((existing) => {
      const existingKey = normalizePlainTextForCompare(existing);
      return existingKey === key || existingKey.includes(key);
    });
  };
  const remember = (items) => {
    for (const item of items) seen.push(item);
  };
  return {
    addText(value) {
      const items = compactPlainTextParts([value])
        .map((item) => stripConversationReferences(item, referenceFragments))
        .flatMap((item) => compactPlainTextParts([item]))
        .filter((item) => !includesSeen(item));
      if (!items.length) return;
      remember(items);
      output.push(...items);
    },
    addSection(labelKey, values, options = {}) {
      const limit = Number.isFinite(options.limit) ? options.limit : 4;
      const items = compactPlainTextParts(values)
        .map((item) => stripConversationReferences(item, referenceFragments))
        .flatMap((item) => compactPlainTextParts([item]))
        .filter((item) => !includesSeen(item))
        .slice(0, Math.max(1, limit));
      if (!items.length) return;
      remember(items);
      output.push(formatConversationSection(labelKey, items));
    },
    text() {
      return output.join("\n\n");
    },
  };
}

function extractReferenceFragments(text) {
  if (typeof text !== "string") return [];
  const fragments = text
    .split(/[\n。.!?！？]+/)
    .map((item) => item.trim())
    .filter(Boolean);
  if (text.trim()) fragments.push(text.trim());
  const seen = new Set();
  return fragments.filter((item) => {
    const key = normalizePlainTextForCompare(item);
    if (key.length < 8 || seen.has(key)) return false;
    seen.add(key);
    return true;
  }).sort((left, right) => right.length - left.length);
}

function stripConversationReferences(text, references) {
  if (typeof text !== "string" || !text.trim() || !Array.isArray(references) || references.length === 0) {
    return text;
  }
  let cleaned = text;
  for (const reference of references) {
    const referenceKey = normalizePlainTextForCompare(reference);
    if (!referenceKey) continue;
    const escaped = reference.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
    cleaned = cleaned.replace(new RegExp(escaped, "g"), "");
  }
  return cleaned
    .split("\n")
    .map((line) => line.replace(/[ \t]{2,}/g, " ").trim())
    .filter((line) => {
      const key = normalizePlainTextForCompare(line);
      if (!key) return false;
      return !references.some((reference) => {
        const referenceKey = normalizePlainTextForCompare(reference);
        return referenceKey === key || referenceKey.includes(key);
      });
    })
    .join("\n")
    .trim();
}

function formatConversationSection(labelKey, items) {
  const label = t(labelKey);
  if (items.length === 1) return `**${label}**\n\n${items[0]}`;
  return `**${label}**\n\n${items.map((item) => `- ${item}`).join("\n")}`;
}

function splitPlainTextPart(item) {
  if (Array.isArray(item)) return item.flatMap((entry) => splitPlainTextPart(entry));
  if (typeof item !== "string") return [];
  return item
    .split(/\n{2,}/)
    .map((entry) => entry.trim())
    .filter(Boolean);
}

function normalizePlainTextForCompare(text) {
  return String(text || "")
    .toLowerCase()
    .replace(/[\s，。、“”‘’：:；;,.!?！？()[\]{}<>《》【】"'`~\-_*#]+/g, "");
}

function isLowSignalCaptureText(text) {
  const normalized = normalizePlainTextForCompare(text);
  if (!normalized) return true;
  const generic = new Set([
    "持续收集并澄清当前主题",
    "持续收集澄清当前主题",
    "继续收集并澄清当前主题",
    "继续收集澄清当前主题",
  ]);
  return generic.has(normalized);
}

function renderCaptureConversation() {
  const list = $("#capture-conversation");
  if (!list) return;
  // Backend context and generated previews are internal state, never chat content.
  const status = captureArchiveStatusMessage();
 const setMessage = captureDocumentSetMessage();
  const messages = (state.capture.messages || []).filter((msg) =>
    msg.kind !== "archive_preview" &&
    (msg.kind !== "context" || msg.pending || stringsTrim(msg.sessionContext?.assistant_reply)) &&
    !(status?.thoughtId && msg.thoughtId === status.thoughtId)
  );
  if (status) messages.push(status);
 if (setMessage) messages.push(setMessage);
  if (messages.length === 0) {
    list.innerHTML = `<li class="tf-msg tf-msg-system tf-msg-empty">${escapeHTML(t("capture.session.idle"))}</li>`;
  } else {
    list.innerHTML = messages.map((msg) => {
      const cls = captureRoleClass(msg.role);
      const roleLabel = msg.role ? escapeHTML(msg.role) : "";
      const body = renderCaptureBubbleBody(msg);
      const meta = msg.meta ? `<div class="tf-msg-meta">${escapeHTML(msg.meta)}</div>` : "";
      const thoughtHook = msg.thoughtId ? ` data-thought-id="${escapeHTML(msg.thoughtId)}"` : "";
      const kindHook = msg.kind ? ` data-kind="${escapeHTML(msg.kind)}"` : "";
      return `<li class="tf-msg ${cls}" data-role="${roleLabel}"${kindHook}${thoughtHook}>${body}${meta}</li>`;
    }).join("");
  }
  list.scrollTop = list.scrollHeight;
  renderCaptureLockIndicator();
 bindSetReviewButtons();
}

function captureValidationMessage(issue) {
  const profile = profileByID(state.capture.archivePreview?.document_profile?.profile_id, state.capture.archivePreview?.document_profile?.version);
  const label = (profile?.inputs || []).find((input) => input.key === issue.section)?.label || issue.section || "";
  if (issue.code === "input_required") return t("capture.archive.missing_input", { name: label });
  if (issue.code === "research_evidence_missing") return t("capture.archive.sources_required");
  if (issue.code === "thoughtflow.archive.conflicts_unresolved") return t("capture.archive.resolve_issues");
  if (issue.code === "thoughtflow.profile.source_content_missing") return t("capture.archive.source_content_missing");
  return `${t("capture.archive.check_failed")} (${issue.code || ""}): ${issue.message || ""}`;
}

// Derive feedback from the durable operation so reconnect/reload cannot lose it.
function captureArchiveStatusMessage() {
  const sp = state.capture.activeScratchpad;
  const operation = sp?.archive_operation;
  const statuses = ["preparing", "saving", "saved", "draft_saved", "needs_input", "blocked", "ready", "interrupted", "conflicted", "failed"];
  if (!sp || sp.session_id !== state.capture.sessionId || !statuses.includes(operation?.state)) return null;
  const issues = (sp.archive_preview?.validation?.issues || []).filter((issue) => issue.severity !== "warning");
  return {
    role: "system",
    kind: "archive_status",
    text: t(`capture.archive.state.${operation.state}`),
    error: [operation.error, ...issues.map(captureValidationMessage)].filter(Boolean).join("\n"),
    thoughtId: operation.state === "saved" ? sp.committed_thought_id : operation.state === "draft_saved" ? sp.draft_thought_id : "",
  };
}

let captureArchivePollKey = "";
function resumeCaptureArchivePolling() {
  const sp = state.capture.activeScratchpad;
  if(sp?.document_set_run_id&&captureDocumentSet?.run?.run_id!==sp.document_set_run_id){
   pollCaptureDocumentSet(sp.session_id,sp.document_set_run_id).catch(error=>toast(error.message));
  }
  if (!sp?.archive_operation || !["preparing", "saving"].includes(sp.archive_operation.state)) return;
  const scope = captureRequestScope();
  const key = `${scope.sessionId}:${sp.archive_operation.id}:${captureSelectionEpoch}`;
  if (captureArchivePollKey === key) return;
  captureArchivePollKey = key;
  const poll = async () => {
    if (!scope.isCurrent() || captureArchivePollKey !== key) return;
    try {
      const detail = await captureAPI(scope, `/api/capture/sessions/${encodeURIComponent(scope.sessionId)}`);
      if (!scope.isCurrent() || captureArchivePollKey !== key) return;
      state.capture.activeScratchpad = detail;
      state.capture.archivePreview = detail.archive_preview || null;
      if (detail.committed_thought_id) state.capture.activeThoughtId = detail.committed_thought_id;
      renderCaptureConversation();
      if (["preparing", "saving"].includes(detail.archive_operation?.state)) { window.setTimeout(poll, 1000); return; }
      captureArchivePollKey = "";
      if (detail.committed_thought_id) refreshActiveCaptureThought();
    } catch (_) { if (scope.isCurrent()) window.setTimeout(poll, 2000); }
  };
  window.setTimeout(poll, 1000);
}

function renderCaptureLockIndicator() {
  const indicator = $("#capture-lock-indicator");
  if (!indicator) return;
  const sessionId = state.capture.sessionId;
  const thoughtId = state.capture.activeThoughtId;
  if (!sessionId || !thoughtId) {
    indicator.hidden = true;
    return;
  }
  const lockApi = (typeof window !== "undefined") ? window.tflowSessionLock : null;
  if (!lockApi) {
    indicator.hidden = true;
    return;
  }
  const holder = lockApi.getHolder(thoughtId);
  if (holder && holder.sessionId !== sessionId) {
    state.capture.lockedBy = holder.sessionId;
    indicator.hidden = false;
  } else {
    state.capture.lockedBy = "";
    indicator.hidden = true;
  }
}

function captureSessionStorageKey() { return "tflow.capture.sessions"; }

function clearLegacyCaptureSessionStorage() {
  try {
    window.localStorage?.removeItem(captureSessionStorageKey());
  } catch (_) { /* ignore */ }
}

function loadCaptureSessions() {
  clearLegacyCaptureSessionStorage();
  return [];
}

function saveCaptureSessions() {
  clearLegacyCaptureSessionStorage();
}

function rememberCaptureSession(session) {
  if (!session) return;
  const list = state.capture.sessions || [];
  const filtered = list.filter((item) => item.sessionId !== session.sessionId);
  filtered.unshift({
    sessionId: session.sessionId,
    thoughtId: session.thoughtId || "",
    title: session.title || "",
    topic: session.topic || "",
    updatedAt: session.updatedAt || new Date().toISOString(),
    source: session.source || "server",
  });
  state.capture.sessions = filtered.slice(0, 12);
  renderDashboardCaptureSessions();
  if (isCaptureSessionsDrawerOpen()) {
    renderCaptureSessionsDrawer();
  }
}

function isCaptureSessionsDrawerOpen() {
  const drawer = $("#capture-sessions-drawer");
  return Boolean(drawer && !drawer.hidden && drawer.classList.contains("open"));
}

function captureSessionLabel(session) {
  return session.title || session.topic || session.thoughtId || t("capture.drawer.untitled_session");
}

function renderCaptureSessionItem(session) {
  const label = captureSessionLabel(session);
  const archived = session.thoughtId ? `<span class="tf-pill tf-pill--success">${escapeHTML(t("capture.drawer.archived"))}</span>` : `<span class="tf-pill tf-pill--draft">${escapeHTML(t("capture.drawer.draft"))}</span>`;
  return `<li class="tf-sessions-item" data-session-id="${escapeHTML(session.sessionId)}">
    <button class="tf-btn tf-sessions-open" type="button" data-session-id="${escapeHTML(session.sessionId)}">
      <span class="tf-sessions-label">${escapeHTML(label)}</span>
      <span class="tf-sessions-meta">${archived} <span class="tf-text-secondary">${escapeHTML((session.updatedAt || "").slice(0, 19).replace("T", " "))}</span></span>
    </button>
    <button class="tf-btn tf-sessions-delete" type="button" data-session-id="${escapeHTML(session.sessionId)}" aria-label="${escapeHTML(t("capture.drawer.delete"))}" title="${escapeHTML(t("capture.drawer.delete"))}">
      <span aria-hidden="true">×</span>
    </button>
  </li>`;
}

function bindCaptureSessionListActions(list) {
  if (!list) return;
  list.querySelectorAll(".tf-sessions-open").forEach((button) => {
    button.addEventListener("click", () => switchCaptureSession(button.dataset.sessionId));
  });
  list.querySelectorAll(".tf-sessions-delete").forEach((button) => {
    button.addEventListener("click", (event) => {
      if (event && typeof event.stopPropagation === "function") event.stopPropagation();
      deleteCaptureSession(button.dataset.sessionId).catch((error) => toast(error.message));
    });
  });
}

function renderCaptureSessionsDrawer() {
  const list = $("#capture-sessions-list");
  if (!list) return;
  // Capture history is server-owned. The browser keeps only the
  // in-memory list used by the currently open drawer; no historical
  // session content or summaries are written to localStorage.
  redrawCaptureSessionsList();
  refreshCaptureSessionsFromServer();
}

async function refreshCaptureSessionsFromServer() {
  let response;
  try {
    response = await api("/api/capture/sessions", { method: "GET" });
  } catch (_) {
    return null;
  }
  response = response || {};
  const summaries = response.summaries || response.Summaries || [];
  if (Array.isArray(summaries)) {
    state.capture.sessions = summaries
      .filter((summary) => summary && summary.session_id)
      .map((summary) => ({
        sessionId: summary.session_id,
        thoughtId: summary.committed_thought_id || "",
        title: summary.title || "",
        updatedAt: summary.updated_at || "",
        source: "server",
      }))
      .slice(0, 24);
    redrawCaptureSessionsList();
    renderDashboardCaptureSessions();
  }
  // The server also exposes last_active_session_id — the scratchpad
  // the boot path should land the user on. We surface it through
  // this same fetch so callers (boot) don't have to issue a second
  // round trip.
  return response.last_active_session_id || response.LastActiveSessionID || "";
}

// rehydrateActiveScratchpad asks the server for the scratchpad
// the boot path should re-open, then mirrors it into local state.
// The server decides which scratchpad qualifies (most recently
// updated *uncommitted* one) so a tab refresh, a server restart,
// or a cross-device open all land the user in the same
// conversation without losing input.
//
// We skip rehydration when the user is already in a session — for
// example, the in-flight capture from a hot reload shouldn't be
// overridden by a stale "last active" from disk.
async function rehydrateActiveScratchpad() {
  const scope = captureRequestScope();
  if (state.capture.activeScratchpad && state.capture.activeScratchpad.session_id) {
    return;
  }
  let lastActiveID = "";
  try {
    lastActiveID = await refreshCaptureSessionsFromServer();
    if (!scope.isCurrent()) return;
  } catch (_) {
    return;
  }
  if (!lastActiveID) return;
  let detail;
  try {
    detail = await api(`/api/capture/sessions/${encodeURIComponent(lastActiveID)}`);
    if (!scope.isCurrent()) return;
  } catch (_) {
    return;
  }
  if (!detail || !detail.session_id) return;
  // Server may have marked the scratchpad committed in the gap
  // between list and get — drop it instead of opening an empty
  // bubble the user can't chat into.
  if (detail.committed_thought_id && !detail.content?.trim() && (!detail.archive_operation || detail.archive_operation.state === "saved")) return;
  state.capture.activeScratchpad = detail;
  state.capture.archivePreview = detail.archive_preview || null;
  state.capture.sessionId = detail.session_id;
  state.capture.activeThoughtId = detail.draft_thought_id || detail.committed_thought_id || "";
  resumeCaptureArchivePolling();
  // Restore the chat history verbatim so the user sees the same
  // composer state they had when they refreshed.
  state.capture.messages = Array.isArray(detail.messages) ? detail.messages.slice() : [];
  upsertCaptureContextMessage();
  if (typeof renderCaptureConversation === "function") {
    renderCaptureConversation();
  }
}

function redrawCaptureSessionsList() {
  const list = $("#capture-sessions-list");
  if (!list) return;
  const sessions = state.capture.sessions || [];
  const markup = sessions.length === 0
    ? `<li class="tf-empty">${escapeHTML(t("empty.no_capture"))}</li>`
    : sessions.map(renderCaptureSessionItem).join("");
  // Preserve focus and an in-progress pointer click when a background refresh
  // confirms the same list. Replacing buttons between down/up drops the click.
  if (list.__captureSessionsMarkup === markup) return;
  list.__captureSessionsMarkup = markup;
  list.innerHTML = markup;
  bindCaptureSessionListActions(list);
}

function openCaptureSessionsDrawer() {
  const drawer = $("#capture-sessions-drawer");
  if (!drawer) return;
  drawer.hidden = false;
  drawer.classList.add("open");
  drawer.setAttribute("aria-hidden", "false");
  const toggle = $("#capture-sessions-toggle");
  if (toggle) toggle.setAttribute("aria-expanded", "true");
  const panel = drawer.querySelector(".tf-drawer-panel");
  if (panel) trapFocus(panel, toggle);
  renderCaptureSessionsDrawer();
}

function closeCaptureSessionsDrawer() {
  const drawer = $("#capture-sessions-drawer");
  if (!drawer) return;
  drawer.classList.remove("open");
  drawer.setAttribute("aria-hidden", "true");
  drawer.hidden = true;
  const toggle = $("#capture-sessions-toggle");
  if (toggle) toggle.setAttribute("aria-expanded", "false");
  if (activeFocusRelease) {
    activeFocusRelease();
    activeFocusRelease = null;
  }
}

function switchCaptureSession(sessionId) {
  const session = (state.capture.sessions || []).find((item) => item.sessionId === sessionId);
  if (!session) return;
  beginCaptureSelection();
  state.capture.activeScratchpad = null;
  state.capture.activeSnapshot = null;
  state.capture.archivePreview = null;
  state.capture.sessionId = session.sessionId;
  state.capture.activeThoughtId = session.thoughtId || "";
  state.capture.messages = Array.isArray(session.messages) ? session.messages.slice() : [];
  const scope = captureRequestScope();
  api(`/api/capture/sessions/${encodeURIComponent(session.sessionId)}`)
    .then((detail) => {
      if (!scope.isCurrent()) return;
      state.capture.activeScratchpad = detail;
      state.capture.archivePreview = detail.archive_preview || null;
      resumeCaptureArchivePolling();
      state.capture.messages = Array.isArray(detail.messages) ? detail.messages.slice() : state.capture.messages;
      upsertCaptureContextMessage();
      renderCaptureConversation();
    })
    .catch(() => {});
  closeCaptureSessionsDrawer();
  // Re-acquire the lock for the new active thought in this session.
  if (state.capture.activeThoughtId && window.tflowSessionLock) {
    const acquired = window.tflowSessionLock.acquire(state.capture.activeThoughtId, session.sessionId);
    if (!acquired) renderCaptureLockIndicator();
  }
  renderCaptureConversation();
  refreshActiveCaptureThought();
}

async function deleteCaptureSession(sessionId) {
  const id = (sessionId || "").trim();
  if (!id) return;
  await api(`/api/capture/sessions/${encodeURIComponent(id)}`, { method: "DELETE" });
  const deleted = (state.capture.sessions || []).find((item) => item.sessionId === id);
  state.capture.sessions = (state.capture.sessions || []).filter((item) => item.sessionId !== id);
  renderDashboardCaptureSessions();
  saveCaptureSessions();
  if (state.capture.sessionId === id) {
    captureSelectionEpoch++;
 captureSetEpoch++;
    if (state.capture.activeThoughtId && window.tflowSessionLock) {
      window.tflowSessionLock.release(state.capture.activeThoughtId, id);
    }
    state.capture.sessionId = "";
    state.capture.activeThoughtId = "";
    state.capture.activeSnapshot = null;
    state.capture.activeScratchpad = null;
    state.capture.archivePreview = null;
    state.capture.messages = [];
    renderCaptureConversation();
  }
  redrawCaptureSessionsList();
  toast(t("capture.drawer.deleted", { title: captureSessionLabel(deleted || { sessionId: id }) }));
}

function refreshActiveCaptureThought() {
  if (!state.capture.activeThoughtId) return Promise.resolve(null);
  return refreshThoughtSnapshot(state.capture.activeThoughtId);
}

// refreshThoughtSnapshot fetches the latest /api/thoughts/:id payload
// and stores it in thoughtSnapshots keyed by thoughtId. Callers can be
// any SSE handler that sees a thought.* event for any thoughtId-bound
// bubble — not just the active thought. Returns the snapshot, or null
// when the fetch fails / payload is malformed. Re-renders the
// conversation so every bubble that references this thoughtId picks
// up the new chip / summary / expansion sections.
function refreshThoughtSnapshot(thoughtId) {
  const id = (thoughtId || "").trim();
  if (!id) return Promise.resolve(null);
  return api(`/api/thoughts/${encodeURIComponent(id)}`)
    .then((snapshot) => {
      if (!snapshot || !snapshot.thought || !snapshot.thought.id) return null;
      state.capture.thoughtSnapshots.set(snapshot.thought.id, snapshot);
      if (state.capture.activeThoughtId === snapshot.thought.id) {
        state.capture.activeSnapshot = snapshot;
      }
      upsertThoughtRecord(snapshot.thought);
      renderCaptureConversation();
      return snapshot;
    })
    .catch(() => null);
}

function captureConversationTracksThought(thoughtId) {
  const id = (thoughtId || "").trim();
  if (!id) return false;
  if (state.capture.activeThoughtId === id) return true;
  return (state.capture.messages || []).some((msg) => msg.thoughtId === id);
}

async function refreshActiveScratchpadContext({ attempts = 3, delayMs = 650, sessionId = state.capture.sessionId, messageKey = "", expectedMessageCount = 0 } = {}) {
  if (!sessionId) return;
  const scope = captureRequestScope();
  for (let attempt = 0; attempt < attempts; attempt++) {
    await new Promise((resolve) => window.setTimeout(resolve, delayMs));
    if (!scope.isCurrent()) return;
    let detail;
    try {
      detail = await api(`/api/capture/sessions/${encodeURIComponent(sessionId)}`);
    } catch (_) {
      return;
    }
    if (!scope.isCurrent() || !detail || detail.session_id !== sessionId || state.capture.sessionId !== sessionId) return;
    const messageCount = Array.isArray(detail.messages) ? detail.messages.length : 0;
    if (expectedMessageCount > 0 && messageCount > 0 && messageCount < expectedMessageCount) return;
    state.capture.activeScratchpad = detail;
    state.capture.archivePreview = detail.archive_preview || null;
    resumeCaptureArchivePolling();
    if (expectedMessageCount === 0 || messageCount > expectedMessageCount) {
      replaceCaptureMessagesFromScratchpad(detail);
    }
    upsertCaptureContextMessage({ scratchpad: detail, sessionId, messageKey });
    renderCaptureConversation();
  }
}

function classifyCaptureInput(text) {
  const urlMatch = /(https?:\/\/[^\s]+|www\.[^\s]+)/i.exec(text || "");
  if (urlMatch) {
    return { type: "url", url: urlMatch[1], content: text.replace(urlMatch[1], "").trim() };
  }
  return { type: "text", content: text };
}

async function submitCaptureComposer(event) {
  if (event) event.preventDefault();
  const input = $("#capture-composer-input");
  const send = $("#capture-composer-send");
  if (!input) return;
  const text = (input.value || "").trim();
  if (!text) {
    toast(t("toast.capture_content_required"));
    return;
  }
  appendCaptureMessage({ role: "user", text });
  input.value = "";
  setButtonLoading(send, true, t("capture.composer.sending"));
  try {
    await stageScratchpadTurn(text);
  } catch (error) {
    appendCaptureMessage({ role: "system", text: error.message || t("toast.request_failed") });
    toast(error.message || t("toast.request_failed"));
  } finally {
    setButtonLoading(send, false);
  }
}

function handleCaptureComposerKeydown(event) {
  if (!event || event.key !== "Enter") return;
  if (event.shiftKey || event.ctrlKey || event.metaKey || event.altKey || event.isComposing) return;
  const input = event.target || $("#capture-composer-input");
  if (!input || input.id !== "capture-composer-input") return;
  if (typeof event.preventDefault === "function") event.preventDefault();
  const form = input.form || $("#capture-composer");
  if (form && typeof form.requestSubmit === "function") {
    form.requestSubmit();
    return;
  }
  submitCaptureComposer().catch((error) => toast(error.message));
}

// stageScratchpadTurn routes a user turn into the current capture
// session. Plain text is appended through /api/capture/sessions*;
// recognized commands update context, open a new session, or request
// backend-managed generation and archiving.
async function stageScratchpadTurn(text) {
  const parsed = parseCaptureCommand(text);
  if (parsed) {
    await dispatchCaptureCommand(text);
    return;
  }
  await appendSessionMessage(text);
}

async function appendSessionMessage(text) {
  captureSelectionEpoch++;
 captureSetEpoch++;
  const scope = captureRequestScope();
  const turnMessageKey = latestCaptureUserTurnKey();
  let scratchpad;
  const sessionId = state.capture.sessionId || state.capture.activeScratchpad?.session_id || "";
  if (sessionId) {
    scratchpad = await captureAPI(scope,`/api/capture/sessions/${encodeURIComponent(sessionId)}/messages`, {
      method: "POST",
      body: JSON.stringify({ role: "user", text }),
    });
    if (!scope.isCurrent()) return;
  } else {
    scratchpad = await captureAPI(scope,"/api/capture/sessions", {
      method: "POST",
      body: JSON.stringify({ content: text }),
    });
    if (!scope.isCurrent()) return;
  }
  if (!scratchpad || !scratchpad.session_id) throw new Error(t("toast.request_failed"));
  const previousThoughtId = state.capture.activeThoughtId || "";
  state.capture.activeScratchpad = scratchpad;
  state.capture.sessionId = scratchpad.session_id;
  state.capture.archivePreview = scratchpad.archive_preview || null;
  const linkedThoughtId = scratchpad.committed_thought_id || "";
  state.capture.activeThoughtId = linkedThoughtId;
  const expectedMessageCount = Array.isArray(scratchpad.messages) ? scratchpad.messages.length : 0;
  upsertCaptureContextMessage({ scratchpad, pending: true, messageKey: turnMessageKey });
  refreshActiveScratchpadContext({
    sessionId: scratchpad.session_id,
    messageKey: turnMessageKey,
    expectedMessageCount,
  }).catch(() => {});
  // When the scratchpad transitions from "no committed thought" to
  // "anchored to thought-X" (e.g. reopening a previously-archived
  // session and then dropping another message, or the legacy
  // composer-immediately-commits flow), surface the thought anchor
  // bubble + a fresh snapshot so the chat picks up the
  // refine/index/topic chips in place.
  if (linkedThoughtId && previousThoughtId !== linkedThoughtId) {
    appendCaptureMessage({ role: "ai", thoughtId: linkedThoughtId, text: t("capture.command.committed", { id: linkedThoughtId }) });
    refreshThoughtSnapshot(linkedThoughtId);
  }
  rememberCaptureSession({
    sessionId: scratchpad.session_id,
    thoughtId: linkedThoughtId,
    title: scratchpad.session_context?.candidate_title || scratchpad.title || scratchpad.session_id,
    messages: state.capture.messages,
  });
  renderCaptureConversation();
}

function renderCaptureThoughtCard(thought, jobs) {
  // Backward-compatible entry point. New callers prefer
  // renderCaptureThoughtCardFromSnapshot(snapshot) which has access to
  // the refine + expand fields; this thin wrapper handles the legacy
  // startCaptureThought call which only has a partial response.
  const snapshot = { thought: thought || {}, jobs: jobs || [] };
  return renderCaptureThoughtCardFromSnapshot(snapshot);
}

// renderCaptureThoughtCardFromSnapshot is the rich AI bubble for the
// active capture thought. It reuses the same section shape as the
// thoughts-page preview so the user sees the same fields in both
// surfaces: AI refine (summary / key_points / ai_tags) and the
// post-refine expansion pipeline (related / near-topics / url-followups /
// plan). The function is idempotent — calling it on the same snapshot
// produces identical HTML — so SSE updates can re-run it freely.
//
// All sections stay out of the bubble when their field is empty or
// missing; that way freshly-captured thoughts collapse to a title + id
// + actions skeleton and grow as the pipeline lands. A small "pending"
// hint is shown only while ALL four expansion sections are still empty
// AND the thought has not been marked expanded yet, so we don't keep
// nagging once the pipeline is at least partially through.
function renderCaptureThoughtCardFromSnapshot(snapshot) {
  const thought = (snapshot && snapshot.thought) || {};
  const content = (snapshot && snapshot.content) || {};
  const jobs = (snapshot && snapshot.jobs) || [];
  const title = escapeHTML(thought.display_title || thought.user_title || thought.extracted_title || thought.id || "");
  const id = escapeHTML(thought.id || "");
  const captureStatus = escapeHTML(thought.capture_status || "accepted");
  const refineStatus = thought.refine_status;
  const indexStatus = thought.index_status;
  const topicStatus = thought.topic_status;
  const summary = typeof thought.summary === "string" ? thought.summary.trim() : "";
  const keyPoints = Array.isArray(thought.key_points) ? thought.key_points.filter((kp) => typeof kp === "string" && kp.trim()) : [];
  const aiTags = Array.isArray(thought.ai_tags) ? thought.ai_tags.filter((tag) => typeof tag === "string" && tag.trim()) : [];
  const userTags = Array.isArray(thought.user_tags) ? thought.user_tags.filter((tag) => typeof tag === "string" && tag.trim()) : [];
  const relatedIDs = Array.isArray(thought.related_thought_ids) ? thought.related_thought_ids : [];
  const suggestedTopicIDs = Array.isArray(thought.suggested_topic_ids) ? thought.suggested_topic_ids : [];
  const urlFollowups = Array.isArray(thought.url_followups) ? thought.url_followups : [];
  const expansionPlan = typeof thought.expansion_plan === "string" ? thought.expansion_plan.trim() : "";
  const expandError = (thought.errors || []).find((e) => typeof e?.code === "string" && e.code.startsWith("thoughtflow.expand."));
  const profileRef = thought.document_profile || {};
  const archivedBody = typeof content.ai_notes === "string" ? content.ai_notes.trim() : "";
  const isTypedDocument = Boolean(profileRef.profile_id) && profileRef.family !== "note";

  const warning = thought.capture_status === "duplicate_warned"
    ? `<div class="tf-alert tf-alert-warning">${escapeHTML((thought.errors || []).map((error) => error.message || error.code).join("; ") || t("capture.duplicate.default"))}</div>`
    : "";

  const statusChips = [
    `<span class="tf-chip" data-status="capture-${captureStatus}">${escapeHTML(t("capture.card.status_capture"))}: ${captureStatus}</span>`,
  ];
  if (refineStatus) statusChips.push(`<span class="tf-chip" data-status="refine-${escapeHTML(refineStatus)}">${escapeHTML(t("capture.card.status_refine"))}: ${escapeHTML(refineStatus)}</span>`);
  if (indexStatus) statusChips.push(`<span class="tf-chip" data-status="index-${escapeHTML(indexStatus)}">${escapeHTML(t("capture.card.status_index"))}: ${escapeHTML(indexStatus)}</span>`);
  if (topicStatus) statusChips.push(`<span class="tf-chip" data-status="topic-${escapeHTML(topicStatus)}">${escapeHTML(t("capture.card.status_topic"))}: ${escapeHTML(topicStatus)}</span>`);

  const refineBlock = (summary || keyPoints.length > 0 || aiTags.length > 0)
    ? `<div class="tf-capture-section">
        <div class="tf-capture-section-title">${escapeHTML(t("capture.card.section_refine"))}</div>
        ${summary ? `<div class="tf-capture-summary">${escapeHTML(summary)}</div>` : ""}
        ${keyPoints.length > 0
          ? `<ul class="tf-capture-keypoints">${keyPoints.map((kp) => `<li>${escapeHTML(kp)}</li>`).join("")}</ul>`
          : ""}
        ${(aiTags.length > 0 || userTags.length > 0)
          ? `<div class="tf-capture-tags">${[...userTags.map((tag) => `<span class="tf-chip tf-chip-user" data-tag="user">${escapeHTML(tag)}</span>`), ...aiTags.map((tag) => `<span class="tf-chip tf-chip-ai" data-tag="ai">${escapeHTML(tag)}</span>`)].join("")}</div>`
          : ""}
      </div>`
    : "";

  const documentBlock = isTypedDocument && archivedBody
    ? `<details class="tf-capture-expansion tf-capture-archived-document" open>
        <summary>${escapeHTML(t("capture.card.section_document"))}<span class="tf-chip">${escapeHTML(profileRef.profile_id)} · v${escapeHTML(profileRef.version || 1)}</span></summary>
        <div class="markdown-rendered">${renderMarkdown(archivedBody)}</div>
      </details>`
    : "";

  const expansionSections = buildCaptureExpansionSections(thought, {
    relatedIDs, suggestedTopicIDs, urlFollowups, expansionPlan, expandError,
  });

  return `<div class="tf-suggestion-card">
    <strong class="tf-capture-title">${title || id}</strong>
    <div class="topic-meta">${id}${captureStatus ? ` · ${captureStatus}` : ""}</div>
    <div class="tf-capture-status-row">${statusChips.join("")}</div>
    ${warning}
    ${documentBlock}
    ${refineBlock}
    ${expansionSections}
    <div class="tf-action-row">
      <a class="tf-btn" href="#/notes?id=${encodeURIComponent(thought.id || "")}">${escapeHTML(t("capture.result.view_thought"))}</a>
      <a class="tf-btn" href="#/search">${escapeHTML(t("capture.result.search_related"))}</a>
    </div>
    ${renderJobLinks(jobs)}
  </div>`;
}

// buildCaptureExpansionSections renders the post-refine expansion
// pipeline as collapsible <details> blocks. Each section is omitted
// while its field is empty so the bubble stays compact before the
// pipeline lands. A "pending" hint is shown only when ALL four sections
// are still empty — once any one lands we stop pestering the user.
function buildCaptureExpansionSections(thought, { relatedIDs, suggestedTopicIDs, urlFollowups, expansionPlan, expandError }) {
  const anyLanded = relatedIDs.length > 0 || suggestedTopicIDs.length > 0 || urlFollowups.length > 0 || Boolean(expansionPlan);
  const blocks = [];
  if (relatedIDs.length > 0) {
    blocks.push(`<details class="tf-capture-expansion" open>
      <summary>${escapeHTML(t("thoughts.section_related"))} <span class="tf-capture-count">${relatedIDs.length}</span></summary>
      <ul class="tf-capture-related">${relatedIDs.map((rid) => `<li><a href="#/notes?id=${encodeURIComponent(rid)}">${escapeHTML(rid)}</a></li>`).join("")}</ul>
    </details>`);
  }
  if (suggestedTopicIDs.length > 0) {
    blocks.push(`<details class="tf-capture-expansion" open>
      <summary>${escapeHTML(t("thoughts.section_near_topics"))} <span class="tf-capture-count">${suggestedTopicIDs.length}</span></summary>
      <ul class="tf-capture-topics">${suggestedTopicIDs.map((tid) => `<li>${escapeHTML(tid)}</li>`).join("")}</ul>
    </details>`);
  }
  if (urlFollowups.length > 0) {
    const lines = urlFollowups.map((item) => {
      const url = escapeHTML(item.url || "");
      const itemTitle = item && typeof item.title === "string" && item.title.trim() ? escapeHTML(item.title) : url;
      return `<li><a href="${url}" target="_blank" rel="noopener">${itemTitle}</a></li>`;
    }).join("");
    blocks.push(`<details class="tf-capture-expansion" open>
      <summary>${escapeHTML(t("thoughts.section_url_followups"))} <span class="tf-capture-count">${urlFollowups.length}</span></summary>
      <ul class="tf-capture-followups">${lines}</ul>
    </details>`);
  }
  if (expansionPlan) {
    blocks.push(`<details class="tf-capture-expansion" open>
      <summary>${escapeHTML(t("thoughts.section_expansion_plan"))}</summary>
      <div class="tf-capture-plan">${renderMarkdown(expansionPlan)}</div>
    </details>`);
  }
  if (!anyLanded && (thought.refine_status === "refined" || thought.refine_status === "expanding")) {
    blocks.push(`<div class="tf-capture-pending">${escapeHTML(t("thoughts.expansion_pending"))}</div>`);
  }
  if (expandError) {
    blocks.push(`<div class="tf-alert tf-alert-warning">${escapeHTML(t("thoughts.expansion_failed"))}</div>`);
  }
  if (blocks.length === 0) return "";
  return `<div class="tf-capture-expansion-stack">${blocks.join("")}</div>`;
}

const CAPTURE_COMMANDS = [
  {
    name: "rename",
    match: (text) => {
      const m = /^(?:rename(?:\s+to)?|set title(?:\s+to)?|把标题?改[成为]?|把标题?设为?)\s+(.+)$/i.exec(text);
      return m ? { kind: "rename", title: m[1].trim() } : null;
    },
  },
  {
    name: "add_tag",
    match: (text) => {
      const m = /^(?:add tags?(?:\s+to)?|tag(?:ged)? with|加上?标签?|添加标签?)\s+(.+)$/i.exec(text);
      if (!m) return null;
      const tags = m[1].split(/[,，]/).map((tag) => tag.trim()).filter(Boolean);
      return { kind: "add_tag", tags };
    },
  },
  {
    name: "append_note",
    match: (text) => {
      const m = /^(?:append note|note:|AI Notes? (?:加|加一句|添加)|AI 笔记(?:加|添加))\s+(.+)$/i.exec(text);
      return m ? { kind: "append_note", paragraph: m[1].trim() } : null;
    },
  },
  {
    name: "move_topic",
    match: (text) => {
      const en = /^(?:move to topic|attach to topic|add to topic)\s+(.+)$/i.exec(text);
      if (en) return { kind: "move_topic", topicRef: en[1].trim() };
      const cn = /^(?:归到|归入|放到?|加入)\s*(.+?)(?:专题)?$/.exec(text);
      if (cn) return { kind: "move_topic", topicRef: cn[1].trim() };
      return null;
    },
  },
  {
    name: "refine_again",
    match: (text) => (/^(?:refine again|re-?refine|重新精炼|重新 refine|再次精炼)$/i.test(text)
      ? { kind: "refine_again" }
      : null),
  },
  {
    name: "commit",
    // `/save` is the explicit command channel. Natural language requests
    // such as "归档至新文件" stay in the conversation so the LLM can decide
    // archive_intent/archive_strategy from full context.
    match: (text) => parseSaveCommand(text),
  },
  {
    name: "new_session",
    // "新会话" / "重开" / "清空" / "new session" / "reset" — same
    // single-line anchor as commit. The match opens an explicit
    // fresh /api/capture/sessions resource.
    match: (text) => (/^(新会话|重开|清空|new session|reset)\s*$/i.test(text)
      ? { kind: "new_session" }
      : null),
  },
];

function parseSaveCommand(text) {
  const value = String(text || "").trim();
  const match = /^\/save(?:\s+(.+))?$/i.exec(value);
  if (!match) return null;
  const args = String(match[1] || "").trim().toLowerCase();
  if (!args) return { kind: "commit" };
  if (/^(new|file|note|thought|as\s+new|new\s+(?:file|note|thought|record))$/.test(args)) {
    return { kind: "commit", strategy: "new" };
  }
  if (/^(update|original|current|same|overwrite|replace|update_thought)$/.test(args)) {
    return { kind: "commit", strategy: "update_thought" };
  }
  if (/^(supplement|appendix|followup|follow-up|linked)$/.test(args)) {
    return { kind: "commit", strategy: "supplement" };
  }
  return { kind: "commit" };
}

function parseCaptureCommand(text) {
  for (const entry of CAPTURE_COMMANDS) {
    const result = entry.match(text);
    if (result) return result;
  }
  return null;
}

async function dispatchCaptureCommand(text) {
  const parsed = parseCaptureCommand(text);
  if (!parsed) {
    appendCaptureMessage({ role: "system", text: t("capture.command.noted") });
    rememberCaptureSession({
      sessionId: state.capture.sessionId,
      thoughtId: state.capture.activeThoughtId,
      title: state.capture.activeSnapshot?.thought?.display_title || state.capture.activeThoughtId,
      messages: state.capture.messages,
    });
    return;
  }

  // Scratchpad-stage routing: when activeThoughtId is empty, the
  // scratchpad has not been committed yet. We route commands to the
  // scratchpad endpoints so the chat can rename / add_tag / etc. on
  // the draft, or fire commit / new_session to transition to a real
  // thought. Once committed, activeThoughtId is set and the legacy
  // thought-stage routing takes over.
  if (!state.capture.activeThoughtId) {
    await dispatchScratchpadCommand(parsed, text);
    return;
  }

  await dispatchThoughtCommand(parsed);
}

// dispatchScratchpadCommand handles every command kind when the
// scratchpad has not been committed. The scratchpad is the source
// of truth in this stage; the backend's scratchpad service
// accumulates commands into Draft and projects the latest values
// into Title / Tags so the chat UI can render them immediately.
async function dispatchScratchpadCommand(parsed, text) {
  if (parsed.kind === "commit") {
    await requestCaptureArchive({ intent: "llm", strategy: parsed.strategy || "" });
    return;
  }
  if (parsed.kind === "new_session") {
    await openNewCaptureSession();
    return;
  }
  // Map rename / add_tag / append_note / move_topic / refine_again
  // onto the scratchpad's Draft shape.
  const draft = {
    title_set: "",
    tags_added: [],
    tags_removed: [],
    notes_appended: [],
    topic_ids: [],
    refine_requested: false,
  };
  if (parsed.kind === "rename") {
    draft.title_set = parsed.title;
  } else if (parsed.kind === "add_tag") {
    draft.tags_added = parsed.tags;
  } else if (parsed.kind === "append_note") {
    draft.notes_appended = [parsed.paragraph];
  } else if (parsed.kind === "move_topic") {
    const match = await resolveScratchpadTopic(parsed.topicRef);
    if (!match) {
      appendCaptureMessage({ role: "system", text: t("toast.select_topic_first") });
      return;
    }
    draft.topic_ids = [match.id];
  } else if (parsed.kind === "refine_again") {
    appendCaptureMessage({ role: "system", text: t("capture.command.commit_first") });
    return;
  } else {
    appendCaptureMessage({ role: "system", text: t("capture.command.unknown", { text }) });
    return;
  }
  await patchScratchpad(draft);
}

// dispatchThoughtCommand is the legacy PATCH-thought path. It runs
// after the scratchpad has been committed, so every command
// applies to the real thought file (and triggers a git commit).
async function dispatchThoughtCommand(parsed) {
  const thoughtId = state.capture.activeThoughtId;
  if (parsed.kind === "rename") {
    await patchActiveThought({ title: parsed.title });
    return;
  }
  if (parsed.kind === "add_tag") {
    const existing = (state.capture.activeSnapshot?.thought?.user_tags) || [];
    const merged = Array.from(new Set([...existing, ...parsed.tags]));
    await patchActiveThought({ tags: merged });
    return;
  }
  if (parsed.kind === "append_note") {
    await patchActiveThought({ ai_notes_append: parsed.paragraph });
    return;
  }
  if (parsed.kind === "move_topic") {
    const topics = state.topics || [];
    const ref = parsed.topicRef.toLowerCase();
    const match = topics.find((topic) => topic.id === parsed.topicRef) ||
      topics.find((topic) => String(topic.name || "").toLowerCase() === ref);
    if (!match) {
      appendCaptureMessage({ role: "system", text: t("toast.select_topic_first") });
      return;
    }
    await patchActiveThought({ topic_ids: [match.id] });
    return;
  }
  if (parsed.kind === "refine_again") {
    await retryRefineForActive();
    return;
  }
  if (parsed.kind === "commit") {
    await requestCaptureArchive({ intent: "llm", strategy: parsed.strategy || "" });
    return;
  }
  if (parsed.kind === "new_session") {
    await openNewCaptureSession();
    return;
  }
  appendCaptureMessage({ role: "system", text: t("capture.command.unknown", { text }) });
}

// patchScratchpad projects chat-time commands into session_context.
// The new capture API treats context as the editable draft surface:
// archive preview reads CandidateTitle / CandidateTags /
// CandidateBody directly, so there is no separate scratchpad PATCH
// endpoint in the current PRD flow.
async function patchScratchpad(draft) {
  captureSelectionEpoch++;
 captureSetEpoch++;
  const scope = captureRequestScope();
  const sp = state.capture.activeScratchpad;
  if (!sp) {
    appendCaptureMessage({ role: "system", text: t("toast.request_failed") });
    return;
  }
  const ctx = mergeSessionContextDraft(sp.session_context || {}, draft, sp);
  const next = await captureAPI(scope,`/api/capture/sessions/${encodeURIComponent(scope.sessionId)}/context`, {
    method: "POST",
    body: JSON.stringify(ctx),
  });
    if (!scope.isCurrent()) return;
  state.capture.activeScratchpad = next;
  state.capture.archivePreview = next.archive_preview || null;
  appendCaptureMessage({
    role: "ai",
    text: formatScratchpadFeedback(draft),
  });
  upsertCaptureContextMessage({ scratchpad: next });
  renderCaptureConversation();
}

function mergeSessionContextDraft(ctx, draft, sp) {
  const next = {
    topic: ctx.topic || "",
    goal: ctx.goal || "",
    confirmed_facts: Array.isArray(ctx.confirmed_facts) ? ctx.confirmed_facts.slice() : [],
    open_questions: Array.isArray(ctx.open_questions) ? ctx.open_questions.slice() : [],
    conflicts: Array.isArray(ctx.conflicts) ? ctx.conflicts.slice() : [],
    candidate_title: ctx.candidate_title || sp.title || "",
    candidate_tags: Array.isArray(ctx.candidate_tags) ? ctx.candidate_tags.slice() : [],
    assistant_reply: ctx.assistant_reply || "",
    candidate_summary: ctx.candidate_summary || "",
    candidate_body: ctx.candidate_body || sp.content || "",
    source_links: Array.isArray(ctx.source_links) ? ctx.source_links.slice() : [],
    related_thought_ids: Array.isArray(ctx.related_thought_ids) ? ctx.related_thought_ids.slice() : [],
    suggested_topic_ids: Array.isArray(ctx.suggested_topic_ids) ? ctx.suggested_topic_ids.slice() : [],
    archive_intent: ctx.archive_intent || sp.archive_intent || "none",
    archive_strategy: ctx.archive_strategy || sp.archive_strategy || "new",
  };
  if (draft.title_set) next.candidate_title = draft.title_set;
  if (Array.isArray(draft.tags_added) && draft.tags_added.length) {
    next.candidate_tags = Array.from(new Set([...next.candidate_tags, ...draft.tags_added]));
  }
  if (Array.isArray(draft.notes_appended) && draft.notes_appended.length) {
    next.candidate_body = [next.candidate_body, ...draft.notes_appended].filter(Boolean).join("\n\n");
  }
  if (Array.isArray(draft.topic_ids) && draft.topic_ids.length) {
    next.suggested_topic_ids = Array.from(new Set([...next.suggested_topic_ids, ...draft.topic_ids]));
  }
  return next;
}

function formatScratchpadFeedback(draft) {
  if (draft.title_set) return t("capture.command.scratchpad_renamed", { title: draft.title_set });
  if (draft.tags_added && draft.tags_added.length) {
    return t("capture.command.scratchpad_tagged", { tags: draft.tags_added.join(", ") });
  }
  if (draft.notes_appended && draft.notes_appended.length) {
    return t("capture.command.scratchpad_note_added");
  }
  if (draft.topic_ids && draft.topic_ids.length) {
    return t("capture.command.scratchpad_topic_added");
  }
  return t("capture.command.scratchpad_updated");
}

// resolveScratchpadTopic matches a topic by id or by name. The
// scratchpad-stage routing uses the same lookup as the thought
// stage, so the user can refer to topics by either identifier.
async function resolveScratchpadTopic(topicRef) {
  if (!topicRef) return null;
  const topics = state.topics || [];
  const ref = String(topicRef).toLowerCase();
  return topics.find((topic) => topic.id === topicRef) ||
    topics.find((topic) => String(topic.name || "").toLowerCase() === ref) ||
    null;
}

async function requestCaptureArchive({ intent = "llm", strategy = "" } = {}) {
  captureSelectionEpoch++;
 captureSetEpoch++;
  const scope = captureRequestScope();
  if (!state.capture.sessionId) {
    appendCaptureMessage({ role: "system", text: t("toast.request_failed") });
    return;
  }
  try {
    await captureAPI(scope,`/api/capture/sessions/${encodeURIComponent(scope.sessionId)}/intent`, {
      method: "POST",
      body: JSON.stringify({ intent }),
    });
    if (!scope.isCurrent()) return;
    if (strategy === "update_thought" || strategy === "supplement") {
      const targetThoughtId = state.capture.activeScratchpad?.source_thought_id ||
        state.capture.archivePreview?.thought_id ||
        state.capture.activeThoughtId ||
        state.capture.activeScratchpad?.committed_thought_id ||
        "";
      if (targetThoughtId) {
        const staged = await captureAPI(scope,`/api/capture/sessions/${encodeURIComponent(scope.sessionId)}/strategy`, {
          method: "POST",
          body: JSON.stringify({ strategy, thought_id: targetThoughtId }),
        });
    if (!scope.isCurrent()) return;
        if (staged && staged.session_id) {
          state.capture.activeScratchpad = staged;
        }
      }
    }
    const suffix = strategy ? `?strategy=${encodeURIComponent(strategy)}` : "";
    const result = await captureAPI(scope,`/api/capture/sessions/${encodeURIComponent(scope.sessionId)}/archive/preview${suffix}`, {
      method: "POST", body: JSON.stringify({ commit_after_preview: true }),
    });
    if (!scope.isCurrent()) return;
    if (result.scratchpad) state.capture.activeScratchpad = result.scratchpad;
    state.capture.archivePreview = result.preview || null;
    if (state.capture.activeScratchpad) {
      state.capture.activeScratchpad.archive_preview = state.capture.archivePreview;
    }
    const operation = result.operation || result.scratchpad?.archive_operation;
    if (operation && state.capture.activeScratchpad) state.capture.activeScratchpad.archive_operation = operation;
    const savedID = result.scratchpad?.committed_thought_id;
    if (operation?.state === "saved" && savedID) {
      state.capture.activeThoughtId = savedID;
      if (!(state.capture.messages || []).some((message) => message.thoughtId === savedID)) {
        appendCaptureMessage({ role: "system", thoughtId: savedID, text: t("capture.archive.state.saved") });
      }
      await refreshActiveCaptureThought();
      if (!scope.isCurrent()) return;
    } else if (!operation) {
      appendCaptureMessage({ role: "system", text: t("capture.archive.not_saved") });
    }
    renderCaptureConversation();
    resumeCaptureArchivePolling();
  } catch (error) {
    if (!scope.isCurrent()) return;
    appendCaptureMessage({ role: "system", text: error.message || t("toast.request_failed") });
  }
}

// openNewCaptureSession mints a fresh capture session and optionally
// deletes the previous draft. The local state is reset to match the
// new session so the chat composer is in sync.
async function openNewCaptureSession() {
  beginCaptureSelection();
  captureSelectionEpoch++;
 captureSetEpoch++;
  const scope = captureRequestScope();
  let next;
  try {
    next = await captureAPI(scope,"/api/capture/sessions", {
      method: "POST",
      body: JSON.stringify({ prev_session_id: state.capture.sessionId }),
    });
  } catch (error) {
    if (!scope.isCurrent()) return;
    appendCaptureMessage({ role: "system", text: error.message || t("toast.request_failed") });
    return;
  }
  if (!scope.isCurrent()) return;
  state.capture.sessionId = (next && (next.session_id || next.SessionID)) || newCaptureSessionId();
  state.capture.activeThoughtId = "";
  state.capture.activeSnapshot = null;
  state.capture.activeScratchpad = next || null;
  state.capture.archivePreview = null;
  state.capture.suggestion = null;
  state.capture.messages = [];
  appendCaptureMessage({ role: "system", text: t("capture.command.new_session_started") });
  refreshActiveCaptureThought();
}

async function patchActiveThought(patch) {
  captureSelectionEpoch++;
 captureSetEpoch++;
  const scope = captureRequestScope();
  const thoughtId = state.capture.activeThoughtId;
  if (!thoughtId) return;
  const sessionId = state.capture.sessionId;
  if (!sessionId) return;
  // The refiner holds the thought lock during its LLM call (typically
  // 1-5s). If we land a PATCH in that window, retry once after a short
  // delay before surfacing a "AI 正在处理" message. We deliberately do
  // not retry on the generic "another session" code: that means a real
  // conflict (different browser tab) and silently winning the race
  // would clobber the other session's edits.
  let snapshot;
  let lastError;
  for (let attempt = 0; attempt < 2; attempt++) {
    try {
      snapshot = await captureAPI(scope,`/api/thoughts/${encodeURIComponent(thoughtId)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-Session-Id": sessionId },
        body: JSON.stringify(patch),
      });
    if (!scope.isCurrent()) return;
      lastError = null;
      break;
    } catch (error) {
    if (!scope.isCurrent()) return;
      lastError = error;
      if (error && error.code === "thoughtflow.capture.refining" && attempt === 0) {
        await new Promise((resolve) => setTimeout(resolve, 1200));
    if (!scope.isCurrent()) return;
        continue;
      }
      break;
    }
  }
  if (lastError) {
    if (lastError.code === "thoughtflow.capture.refining") {
      appendCaptureMessage({ role: "system", text: t("capture.session.refining") });
    } else if (lastError.code === "thoughtflow.capture.locked") {
      appendCaptureMessage({ role: "system", text: t("capture.session.locked") });
    } else {
      appendCaptureMessage({ role: "system", text: lastError.message || t("toast.request_failed") });
    }
    return;
  }
  state.capture.activeSnapshot = snapshot;
  // Feedback is a separate, plain-text confirmation message. We
  // deliberately do NOT bind it to the active thoughtId: that would
  // re-render it from the snapshot card and bury the per-command
  // acknowledgment. The original rich card message (appended in
  // startCaptureThought with thoughtId) re-renders in place from the
  // fresh snapshot below via refreshActiveCaptureThought.
  appendCaptureMessage({
    role: "ai",
    text: formatPatchFeedback(patch, snapshot),
  });
  rememberCaptureSession({
    sessionId,
    thoughtId,
    title: snapshot?.thought?.display_title || snapshot?.thought?.user_title || thoughtId,
    messages: state.capture.messages,
  });
  // The AI bubble bound to this thoughtId will re-render from the
  // freshest snapshot, picking up any newly-cleared errors and the
  // updated title / tags without needing a separate system message.
  refreshActiveCaptureThought();
}

// formatPatchFeedback turns a successful PATCH payload into a one-line
// ai-side confirmation that names the field the user just changed. The
// thoughtId-bound bubble re-renders below with the full snapshot, so
// this message exists only to acknowledge the action.
function formatPatchFeedback(patch, snapshot) {
  if (patch && Object.prototype.hasOwnProperty.call(patch, "title")) {
    return t("capture.feedback.renamed", { title: patch.title });
  }
  if (patch && Object.prototype.hasOwnProperty.call(patch, "tags")) {
    const tags = Array.isArray(patch.tags) ? patch.tags.join(", ") : String(patch.tags || "");
    return t("capture.feedback.tags_added", { tags });
  }
  if (patch && Object.prototype.hasOwnProperty.call(patch, "ai_notes_append")) {
    return t("capture.feedback.note_appended");
  }
  if (patch && Object.prototype.hasOwnProperty.call(patch, "topic_ids")) {
    const topics = state.topics || [];
    const match = topics.find((topic) => Array.isArray(patch.topic_ids) && patch.topic_ids[0] === topic.id);
    return t("capture.feedback.moved_to_topic", { topic: match?.name || (Array.isArray(patch.topic_ids) ? patch.topic_ids[0] : "") });
  }
  return t("capture.session.saved_path", { id: snapshot?.thought?.id || "" });
}

async function retryRefineForActive() {
  const thoughtId = state.capture.activeThoughtId;
  if (!thoughtId) return;
  try {
    const job = await api(`/api/thoughts/${encodeURIComponent(thoughtId)}/retry-refine`, { method: "POST" });
    appendCaptureMessage({ role: "system", text: t("capture.feedback.refine_queued") });
  } catch (error) {
    appendCaptureMessage({ role: "system", text: error.message || t("toast.request_failed") });
  }
}

function newCaptureSession() {
  beginCaptureSelection();
  if (state.capture.activeThoughtId && window.tflowSessionLock) {
    window.tflowSessionLock.release(state.capture.activeThoughtId, state.capture.sessionId);
  }
  state.capture.sessionId = newCaptureSessionId();
  state.capture.activeThoughtId = "";
  state.capture.activeSnapshot = null;
  state.capture.activeScratchpad = null;
  state.capture.archivePreview = null;
  state.capture.messages = [];
  state.capture.suggestion = null;
  renderCaptureConversation();
  const input = $("#capture-composer-input");
  if (input) input.focus();
}

async function takeoverCaptureLock() {
  const thoughtId = state.capture.activeThoughtId;
  if (!thoughtId) return;
  const ok = await confirmAction(t("capture.session.takeover"), t("capture.session.takeover_confirm"));
  if (!ok) return;
  if (window.tflowSessionLock) {
    window.tflowSessionLock.release(thoughtId, state.capture.lockedBy);
    window.tflowSessionLock.acquire(thoughtId, state.capture.sessionId);
  }
  state.capture.lockedBy = "";
  renderCaptureLockIndicator();
}

function bindCaptureSessionLock() {
  const api = (typeof window !== "undefined") ? window.tflowSessionLock : null;
  if (!api) return;
  const bus = (typeof window !== "undefined" && window.tflowBus) ? window.tflowBus : null;
  api.on(({ event, payload }) => {
    if (event === "lock:acquired" && payload?.thoughtId === state.capture.activeThoughtId) {
      if (payload.sessionId !== state.capture.sessionId) {
        state.capture.lockedBy = payload.sessionId;
        renderCaptureLockIndicator();
      }
    }
    if (event === "lock:released" && payload?.thoughtId === state.capture.activeThoughtId) {
      state.capture.lockedBy = "";
      renderCaptureLockIndicator();
    }
  });
  if (bus && typeof bus.on === "function") {
    bus.on((message) => {
      if (!message || typeof message !== "object") return;
      if (message.event === "lock:acquired" && message.payload?.thoughtId === state.capture.activeThoughtId) {
        if (message.payload.sessionId !== state.capture.sessionId) {
          state.capture.lockedBy = message.payload.sessionId;
          renderCaptureLockIndicator();
        }
      }
      if (message.event === "lock:released" && message.payload?.thoughtId === state.capture.activeThoughtId) {
        state.capture.lockedBy = "";
        renderCaptureLockIndicator();
      }
    });
  }
}

async function runSearch(event) {
  const epoch = ++searchRequestEpoch;
  if (event) event.preventDefault();
  const queryText = $("#search-query").value.trim();
  const tags = csv($("#search-tags").value);
  if (!queryText && tags.length === 0) {
    state.lastResults = [];
    renderSearchIdle();
    if (state.route?.page === "search") syncHash();
    return;
  }
  const query = new URLSearchParams();
  query.set("q", queryText);
  query.set("page", "1");
  query.set("page_size", "20");
  if (tags.length > 0) query.set("tags", tags.join(","));
  const response = await api(`/api/search?${query.toString()}`);
  if (epoch !== searchRequestEpoch) return;
  state.lastResults = response.results || [];
  renderResults(response);
  // URL reflects the submitted query, not the typing-in-progress value.
  if (state.route?.page === "search") syncHash();
}

function resetSearchFilters() {
  $("#search-tags").value = "";
  runSearch().catch((error) => toast(error.message));
}

function renderSearchIdle() {
  const list = $("#search-results");
  if (!list) return;
  list.innerHTML = `<div class="topic-meta">${escapeHTML(t("search.idle"))}</div>`;
  updateSelectionControls();
}

function renderResults(response) {
  const list = $("#search-results");
  const results = response.results || [];
  if (results.length === 0) {
    list.innerHTML = `<div class="topic-meta">${escapeHTML(t("empty.no_matching"))}</div>`;
    updateSelectionControls();
    return;
  }
  list.innerHTML = results
    .map((item) => renderSearchResultItem(item, { selected: state.selectedThoughts.has(item.thought_id), activeTopicId: state.activeTopicId }))
    .join("");
  list.querySelectorAll("[data-select-id]").forEach((input) => {
    input.addEventListener("change", () => {
      if (input.checked) state.selectedThoughts.add(input.dataset.selectId);
      else state.selectedThoughts.delete(input.dataset.selectId);
      updateSelectionControls();
    });
  });
  list.querySelectorAll("[data-preview-id]").forEach((button) => {
    button.addEventListener("click", () => previewThought(button.dataset.previewId, { drawer: true }).catch((error) => toast(error.message)));
  });
  list.querySelectorAll("[data-open-id]").forEach((button) => {
    button.addEventListener("click", () => {
      window.location.hash = `#/notes?id=${encodeURIComponent(button.dataset.openId)}`;
    });
  });
  list.querySelectorAll("[data-compose-source-id]").forEach((button) => {
    button.addEventListener("click", () => {
      addToComposeSources([{
        source_type: "search_result",
        source_id: button.dataset.composeSourceId,
        snippet: (state.lastResults || []).find((item) => (item.source_id || item.thought_id) === button.dataset.composeSourceId)?.snippet || "",
        title: button.dataset.composeSourceTitle || "",
      }], "search_result");
      openComposeSources();
    });
  });
  list.querySelectorAll("[data-copy-thought-id]").forEach((button) => {
    button.addEventListener("click", () => {
      copyThoughtLink(button.dataset.copyThoughtId || "");
    });
  });
  updateSelectionControls();
}

function renderSearchResultItem(item, options = {}) {
  const checked = options.selected ? "checked" : "";
  const tags = [...(item.tags || []), ...(item.topics || [])]
    .slice(0, 5)
    .map((tag) => `<span class="pill">${escapeHTML(tag)}</span>`)
    .join("");
  const thoughtID = item.thought_id || item.id || "";
  const title = item.title || thoughtID;
  const sourceID = item.source_id || thoughtID;
  const actions = new Set(Array.isArray(item.actions)
    ? item.actions
    : ["preview", "open_note", "add_to_compose", "copy_path"]);
  const canPreview = actions.has("preview") && thoughtID;
  const canOpen = actions.has("open_note") && thoughtID;
  const canAddSources = actions.has("add_to_compose") && sourceID;
  const canShare = publicThoughtSharingEnabled() && thoughtID && actions.has("copy_path");
  const contentURL = thoughtID ? buildThoughtContentURL(thoughtID) : "";
  // SearchResultView 投影只暴露 content-facing 字段: thought_id / title /
  // snippet / tags / topics / source / source_id / path_hint / actions。
  // 仅在公开内容功能启用后显示可抓取的内容 URL（/p/thoughts/{id}）。
  return `
    <article class="result-item">
      <div class="result-row">
        <input type="checkbox" data-select-id="${escapeHTML(thoughtID)}" ${checked} aria-label="${escapeHTML(t("search.result.select_aria"))}">
        <div>
          <strong>${canPreview ? `<button class="link-button" data-preview-id="${escapeHTML(thoughtID)}" type="button">${escapeHTML(title)}</button>` : escapeHTML(title)}</strong>
          <div class="result-meta">${escapeHTML(item.snippet || "")}</div>
          <div class="score-line">
            ${tags}
          </div>
          <div class="tf-action-row">
            ${canOpen ? `<button class="mini-button" data-open-id="${escapeHTML(thoughtID)}" type="button">${escapeHTML(t("search.result.open"))}</button>` : ""}
            ${canAddSources ? `<button class="mini-button" data-compose-source-id="${escapeHTML(sourceID)}" data-compose-source-title="${escapeHTML(title)}" type="button">${escapeHTML(t("search.result.add_source"))}</button>` : ""}
            ${canShare ? `<button class="mini-button" data-copy-thought-id="${escapeHTML(thoughtID)}" type="button">${escapeHTML(t("search.result.copy_share_link"))}</button><code>${escapeHTML(contentURL)}</code>` : ""}
          </div>
        </div>
      </div>
    </article>
  `;
}

function publicThoughtSharingEnabled() {
  return state?.status?.public_thoughts_enabled === true;
}

// publicBaseURL returns the configured share origin when available, otherwise
// the browser's current origin. Trailing slashes are stripped so callers can
// safely append path segments.
function publicBaseURL() {
  const configured = typeof state?.status?.public_base_url === "string"
    ? state.status.public_base_url.trim()
    : "";
  if (configured) return configured.replace(/\/+$/, "");
  if (typeof window !== "undefined" && window.location?.origin && window.location.origin !== "null") {
    return String(window.location.origin).replace(/\/+$/, "");
  }
  return "";
}

// buildThoughtContentURL builds the absolute public content URL that external
// tools (web search / crawlers / RAG ingest) can GET for full Thought body:
//   {publicBase}/p/thoughts/{thoughtId}
// Returns "" when neither a base origin nor a thought id is available.
function buildThoughtContentURL(thoughtId) {
  const id = String(thoughtId || "").trim();
  if (!id || !publicThoughtSharingEnabled()) return "";
  const base = publicBaseURL();
  const path = `/p/thoughts/${encodeURIComponent(id)}`;
  return base ? `${base}${path}` : path;
}

function copyThoughtLink(thoughtId) {
  const link = buildThoughtContentURL(thoughtId);
  if (!link) return;
  copyText(link, t("toast.share_link_copied"));
}

function copyText(text, successToast) {
  if (!text) return;
  if (typeof navigator !== "undefined" && navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(text).then(() => toast(successToast || text)).catch(() => toast(text));
    return;
  }
  toast(text);
}

function updateSelectionControls() {
  const count = state.selectedThoughts.size;
  const selectedCount = $("#selected-count");
  if (selectedCount) selectedCount.textContent = t("search.selected_count", { n: count });
  const add = $("#add-selected-compose");
  const clear = $("#clear-selected");
  if (add) add.disabled = count === 0;
  if (clear) clear.disabled = count === 0;
}

function addToComposeSources(sources, sourceType = "thought") {
  const normalised = (sources || [])
    .filter(Boolean)
    .map((entry) => {
      if (typeof entry === "string") return { source_type: sourceType, source_id: entry };
      return { source_type: entry.source_type || sourceType, source_id: entry.source_id, title: entry.title };
    })
    .filter((entry) => entry.source_id);
  if (normalised.length === 0) return;
  state.composeSources = new Map(state.composeSources);
  for (const source of normalised) {
    const key = `${source.source_type}::${source.source_id}`;
    if (state.composeSources.has(key)) continue;
    state.composeSources.set(key, {
      source_type: source.source_type,
      source_id: source.source_id,
      title: source.title || "",
    });
  }
  persistSources();
  renderComposeSources();
  toast(t("toast.sources_add", { n: state.composeSources.size }));
}

function openComposeSources() {
  navigateHash(buildRouteHash("compose", {}, { tab: "sources" }), { force: true });
}

function removeComposeSource(sourceType, sourceID) {
  const key = `${sourceType || "thought"}::${sourceID || ""}`;
  if (!state.composeSources.has(key)) return;
  state.composeSources = new Map(state.composeSources);
  state.composeSources.delete(key);
  persistSources();
  renderComposeSources();
}

function clearSearchSelection() {
  state.selectedThoughts.clear();
  renderResults({ results: state.lastResults });
  persistRouteDebounced();
}

function clearComposeSources() {
  state.composeSources.clear();
  persistSources();
  renderComposeSources();
}

function renderComposeSources() {
  const sources = Array.from(state.composeSources.values());
  const ids = sources.map((s) => s.source_id);
  const list = $("#compose-source-list");
  const create = $("#open-compose-create");
  const createFromSources = $("#open-compose-create-sources");
  if (create) create.disabled = sources.length === 0;
  if (createFromSources) createFromSources.disabled = sources.length === 0;
  if (list) {
    if (sources.length === 0) {
      list.innerHTML = `<div class="tf-empty">${escapeHTML(t("compose.empty_sources"))}</div>`;
    } else {
      list.innerHTML = sources
        .map((source) => {
          const sourceTypeLabel = t(`compose.source_type.${source.source_type}`) || source.source_type;
          return `<span class="pill" data-source-type="${escapeHTML(source.source_type)}" data-source-id="${escapeHTML(source.source_id)}" title="${escapeHTML(sourceTypeLabel)}">${escapeHTML(source.title || source.source_id)}</span>`;
        })
        .join("");
    }
  }
  // /compose-sources-list 在 Compose 页面 sources tab 单独展示一份,
  // 复用同源数据 + 同样的 source_type 标签,避免两份内容漂移。
  const tabList = $("#compose-sources-list");
  const tabCount = $("#compose-source-count-sources");
  const tabClear = $("#clear-compose-sources-tab");
  if (tabCount) {
    tabCount.setAttribute("data-n", String(sources.length));
    tabCount.textContent = t("compose.source_count", { n: sources.length });
  }
  if (tabClear) tabClear.disabled = sources.length === 0;
  if (tabList) {
    if (sources.length === 0) {
      tabList.innerHTML = `<div class="tf-empty">${escapeHTML(t("compose.empty_sources"))}</div>`;
    } else {
      tabList.innerHTML = sources.map(renderComposeSourcesItem).join("");
      if (typeof tabList.querySelectorAll === "function") {
        tabList.querySelectorAll("[data-compose-source-preview]").forEach((button) => {
          button.addEventListener("click", () => previewThought(button.dataset.composeSourcePreview, { drawer: true }).catch((error) => toast(error.message)));
        });
        tabList.querySelectorAll("[data-compose-source-remove]").forEach((button) => {
          button.addEventListener("click", () => removeComposeSource(button.dataset.sourceType, button.dataset.sourceId));
        });
      }
    }
  }
}

function renderComposeSourcesItem(source) {
  const sourceType = source?.source_type || "thought";
  const sourceID = source?.source_id || "";
  const sourceTypeLabel = t(`compose.source_type.${sourceType}`) || sourceType;
  const title = source?.title || sourceID;
  const canPreview = (sourceType === "thought" || sourceType === "search_result") && sourceID;
  return `<article class="tf-source-queue-item" data-source-type="${escapeHTML(sourceType)}" data-source-id="${escapeHTML(sourceID)}">
    <div class="tf-source-queue-main">
      <div class="tf-source-queue-title-row">
        <strong>${escapeHTML(title)}</strong>
        <span class="pill">${escapeHTML(sourceTypeLabel)}</span>
      </div>
      <div class="topic-meta">${escapeHTML(t("compose.source_id"))}: <code>${escapeHTML(sourceID)}</code></div>
    </div>
    <div class="tf-action-row">
      ${canPreview ? `<button class="mini-button" data-compose-source-preview="${escapeHTML(sourceID)}" type="button">${escapeHTML(t("search.result.preview"))}</button>` : ""}
      <button class="mini-button" data-compose-source-remove="${escapeHTML(sourceID)}" data-source-type="${escapeHTML(sourceType)}" data-source-id="${escapeHTML(sourceID)}" type="button">${escapeHTML(t("compose.source_remove"))}</button>
    </div>
  </article>`;
}

async function previewThought(thoughtId, options = {}) {
  const epoch = ++thoughtRequestEpoch;
  if (!thoughtId) {
    renderEmptyThoughtPanels();
    return;
  }
  const snapshot = await api(`/api/thoughts/${encodeURIComponent(thoughtId)}`);
  if (epoch !== thoughtRequestEpoch) return;
  state.activeThoughtId = thoughtId;
  state.activeThoughtSnapshot = snapshot;
  upsertThoughtRecord(snapshot.thought, { render: false });
  renderThoughtPanels(snapshot);
  if (options.syncRoute) {
    const query = { id: thoughtId };
    const activeTab = document.querySelector("#page-thoughts .tab.active")?.dataset.tab || "";
    if (activeTab && activeTab !== defaultNotesTabForQuery(query)) query.tab = activeTab;
    const hash = buildRouteHash("thoughts", { thoughtId }, query);
    if (window.location.hash !== hash) window.location.hash = hash;
    else syncHash();
  }
  if (options.drawer) openDrawer("thought-drawer");
}

function bindThoughtReopenButtons(root = document) {
  root.querySelectorAll("[data-reopen-capture]").forEach((button) => {
    if (button.dataset.reopenBound === "1") return;
    button.dataset.reopenBound = "1";
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      reopenThoughtInCapture(button.dataset.reopenCapture).catch((error) => toast(error.message));
    });
  });
}

let thoughtImportBusy = false;
let thoughtReaderEpoch = 0;
let thoughtReaderSnapshot = null;

async function importThoughtFiles() {
  if (thoughtImportBusy) return;
  const input = $("#thought-import-files");
  const files = Array.from(input?.files || []);
  const output = $("#thought-import-results");
  if (!files.length) { output.textContent = t("notes.import.select_files"); return; }
  thoughtImportBusy = true;
  const button = $("#thought-import-start");
  button.disabled = true;
  input.disabled = true;
  const results = [];
  const render = () => { output.innerHTML = results.join("") + `<p>${escapeHTML(t("notes.import.progress", { n: results.length, total: files.length }))}</p>`; };
  render();
  try {
    for (const file of files) {
      try {
        if (!/\.(md|markdown)$/i.test(file.name) || !file.size || file.size > 32 * 1024 * 1024) throw new Error(t("notes.import.invalid"));
        const body = new FormData();
        body.append("file", file, file.name);
        // Keep the File bytes intact; decoding via File.text() would strip BOM.
        const result = await api("/api/thoughts/import", { method: "POST", body });
        const thought = result.thought;
        upsertThoughtRecord(thought);
        const href = buildRouteHash("thoughts", { thoughtId: thought.id }, { id: thought.id, tab: "notes-detail" });
        results.push(`<p><a href="${escapeHTML(href)}">${escapeHTML(file.name)}</a> — ${escapeHTML(t("notes.import.saved"))}</p>`);
      } catch (error) {
        const message = error.code === "thoughtflow.capture.import_invalid" || error.code === "thoughtflow.capture.import_too_large"
          ? t("notes.import.invalid") : error.status ? error.message : `${error.message} ${t("notes.import.check_before_retry")}`;
        results.push(`<p class="tf-import-error">${escapeHTML(file.name)} — ${escapeHTML(message)}</p>`);
      }
      render();
    }
  } finally {
    input.value = "";
    input.disabled = false;
    button.disabled = false;
    thoughtImportBusy = false;
  }
}

function thoughtReadingMarkdown(snapshot) {
  const thought = snapshot.thought || {};
  const content = snapshot.content || {};
  const sections = [`# ${thoughtDisplayTitle(thought) || thought.id || ""}`];
  if (thought.summary) sections.push(`## ${t("thoughts.preview_summary")}\n\n${thought.summary}`);
  if (thought.key_points?.length) sections.push(`## ${t("notes.detail.key_points")}\n\n${thought.key_points.map(value => `- ${value}`).join("\n")}`);
  const seen = new Set();
  for (const [key, label] of [["ai_notes", "notes.detail.ai_notes"], ["original", "thoughts.preview_original"], ["extracted_content", "thoughts.preview_extracted"], ["links", "thoughts.preview_links"]]) {
    const value = content[key];
    if (!value || seen.has(value)) continue;
    seen.add(value);
    sections.push(`## ${t(label)}\n\n${value}`);
  }
  sections.push(appendExpansionSections(thought));
  return sections.filter(Boolean).join("\n\n");
}

function bindThoughtReadButtons(root = document) {
  root.querySelectorAll("[data-read-thought]").forEach(button => {
    if (button.dataset.readerBound === "1") return;
    button.dataset.readerBound = "1";
    button.addEventListener("click", event => {
      event.stopPropagation();
      openThoughtReader(button.dataset.readThought).catch(error => toast(error.message));
    });
  });
}

async function openThoughtReader(id) {
  const epoch = ++thoughtReaderEpoch;
  const dialog = $("#thought-reader");
  thoughtReaderSnapshot = null;
  $("#thought-reader-title").textContent = t("notes.reader.loading");
  $("#thought-reader-content").textContent = t("notes.reader.loading");
  $("#thought-reader-toc").innerHTML = "";
  $("#thought-reader-original").hidden = true;
  $("#thought-reader-notice").hidden = true;
  $("#thought-reader-download").removeAttribute("href");
  if (!dialog.open) dialog.showModal();
  try {
    const snapshot = await api(`/api/thoughts/${encodeURIComponent(id)}`);
    if (epoch !== thoughtReaderEpoch || !dialog.open) return;
    thoughtReaderSnapshot = snapshot;
    $("#thought-reader-title").textContent = thoughtDisplayTitle(snapshot.thought) || id;
    $("#thought-reader-download").href = `/api/thoughts/${encodeURIComponent(id)}/markdown`;
    $("#thought-reader-original").hidden = !snapshot.thought.markdown_import;
    $("#thought-reader-original").href = `/api/thoughts/${encodeURIComponent(id)}/import-source`;
    $("#thought-reader-notice").hidden = !snapshot.thought.markdown_import;
    await renderThoughtReaderView("reading");
  } catch (error) {
    if (epoch === thoughtReaderEpoch && dialog.open) $("#thought-reader-content").textContent = error.message;
  }
}

async function renderThoughtReaderView(mode) {
  if (!thoughtReaderSnapshot) return;
  const epoch = ++thoughtReaderEpoch;
  const snapshot = thoughtReaderSnapshot;
  const content = $("#thought-reader-content");
  const toc = $("#thought-reader-toc");
  document.querySelectorAll("[data-reader-view]").forEach(button => button.setAttribute("aria-pressed", String(button.dataset.readerView === mode)));
  toc.innerHTML = "";
  toc.hidden = mode !== "reading";
  content.parentElement.classList.toggle("no-toc", mode !== "reading");
  if (mode === "information") {
    content.innerHTML = `<pre class="tf-reader-source">${escapeHTML(JSON.stringify({ thought: snapshot.thought, resource_bindings: snapshot.resource_bindings || [], jobs: snapshot.jobs || [], git_commits: snapshot.git_commits || [] }, null, 2))}</pre>`;
  } else if (mode === "source") {
    content.textContent = t("notes.reader.loading");
    try {
      const response = await fetch(`/api/thoughts/${encodeURIComponent(snapshot.thought.id)}/markdown`);
      if (!response.ok) throw new Error(t("notes.reader.load_failed"));
      const raw = await response.text();
      if (epoch !== thoughtReaderEpoch || !$("#thought-reader").open) return;
      content.innerHTML = `<pre class="tf-reader-source">${escapeHTML(raw)}</pre>`;
    } catch (error) {
      if (epoch === thoughtReaderEpoch) content.textContent = error.message;
    }
  } else {
    content.innerHTML = renderObjectMarkdown(thoughtReadingMarkdown(snapshot), snapshot.thought.id, snapshot.resource_bindings);
    content.querySelectorAll("h1,h2,h3").forEach((heading, index) => {
      heading.id = `thought-reader-section-${index}`;
      const button = document.createElement("button");
      button.type = "button";
      button.textContent = heading.textContent;
      button.className = "tf-reader-toc-item";
      button.addEventListener("click", () => heading.scrollIntoView({ block: "start" }));
      toc.appendChild(button);
    });
  }
  if (epoch === thoughtReaderEpoch) content.scrollTop = 0;
}

function bindThoughtDeleteButtons(root = document) {
  root.querySelectorAll("[data-delete-thought]").forEach((button) => {
    if (button.dataset.deleteBound === "1") return;
    button.dataset.deleteBound = "1";
    button.addEventListener("click", (event) => {
      event.stopPropagation();
      deleteThought(button.dataset.deleteThought, button.dataset.deleteTitle).catch((error) => toast(error.message));
    });
  });
}

async function deleteThought(thoughtId, title = "") {
  const id = String(thoughtId || "").trim();
  if (!id) return;
  const confirmed = await confirmAction(
    t("thoughts.action.delete_title"),
    t("thoughts.action.delete_confirm", { title: title || id }),
  );
  if (!confirmed) return;
  await api(`/api/thoughts/${encodeURIComponent(id)}`, {
    method: "DELETE",
    headers: { "X-Session-Id": `delete-${Date.now()}` },
  });
  state.notes = (state.notes || []).filter((thought) => thought.id !== id);
  state.selectedThoughts.delete(id);
  for (const [key, source] of Array.from(state.composeSources.entries())) {
    if (source?.source_id === id) state.composeSources.delete(key);
  }
  persistSources();
  broadcastSourcesChange();
  state.activeThoughtId = "";
  state.activeThoughtSnapshot = null;
  state.capture.thoughtSnapshots.delete(id);
  closeDrawer("thought-drawer");
  renderEmptyThoughtPanels();
  renderThoughtsList();
  renderComposeSources();
  renderSidebarBadges();
  navigateHash(buildRouteHash("thoughts", {}, {}), { force: true });
  toast(t("thoughts.action.delete_done"));
}

function applyScratchpadToCaptureState(sp, options = {}) {
  if (!sp || !sp.session_id) return;
  beginCaptureSelection();
  state.capture.activeScratchpad = sp;
  state.capture.archivePreview = sp.archive_preview || null;
  state.capture.sessionId = sp.session_id;
  state.capture.activeThoughtId = options.thoughtId || sp.source_thought_id || sp.committed_thought_id || "";
  state.capture.activeSnapshot = null;
  state.capture.messages = Array.isArray(sp.messages) ? sp.messages.slice() : [];
  upsertCaptureContextMessage({ scratchpad: sp, sessionId: sp.session_id });
  rememberCaptureSession({
    sessionId: sp.session_id,
    thoughtId: state.capture.activeThoughtId,
    title: sp.title || state.capture.activeThoughtId || sp.session_id,
    messages: state.capture.messages,
    source: "server",
  });
  if (state.capture.activeThoughtId && window.tflowSessionLock) {
    window.tflowSessionLock.acquire(state.capture.activeThoughtId, sp.session_id);
  }
  renderCaptureConversation();
  refreshActiveCaptureThought();
 if(sp.document_set_run_id)pollCaptureDocumentSet(sp.session_id,sp.document_set_run_id).catch(error=>toast(error.message));
}

async function reopenThoughtInCapture(thoughtId) {
  beginCaptureSelection();
  captureSelectionEpoch++;
 captureSetEpoch++;
  const scope = captureRequestScope();
  const id = String(thoughtId || "").trim();
  if (!id) return;
  const result = await captureAPI(scope,`/api/thoughts/${encodeURIComponent(id)}/reopen-session`, {
    method: "POST",
    body: JSON.stringify({}),
  });
  if (!scope.isCurrent()) return;
  const sp = result.scratchpad || result.Scratchpad || null;
  if (!sp || !sp.session_id) throw new Error(t("toast.request_failed"));
  applyScratchpadToCaptureState(sp, { thoughtId: id });
  window.location.hash = "#/capture";
  toast(t("thoughts.action.reopen_capture_done"));
}

// appendExpansionSections renders the post-refine expansion pipeline
// output (related_thought_ids, suggested_topic_ids, url_followups,
// expansion_plan) as a tail of the thought preview. Each section
// stays out of the markdown when its field is empty, so the preview
// collapses cleanly to the original/extracted content while the
// expansion is still in flight. Expansion errors (recorded in
// thought.errors) are surfaced as a small italic hint so the user
// sees partial-failure feedback without blocking the rest of the
// preview.
function appendExpansionSections(thought) {
  const relatedIDs = Array.isArray(thought.related_thought_ids) ? thought.related_thought_ids : [];
  const suggestedTopicIDs = Array.isArray(thought.suggested_topic_ids) ? thought.suggested_topic_ids : [];
  const urlFollowups = Array.isArray(thought.url_followups) ? thought.url_followups : [];
  const expansionPlan = typeof thought.expansion_plan === "string" ? thought.expansion_plan.trim() : "";
  const expandError = (thought.errors || []).find((e) => typeof e?.code === "string" && e.code.startsWith("thoughtflow.expand."));

  const blocks = [];
  if (relatedIDs.length > 0) {
    blocks.push(`## ${t("thoughts.section_related")}\n${relatedIDs.map((id) => `- \`${id}\``).join("\n")}`);
  } else if (suggestedTopicIDs.length === 0 && urlFollowups.length === 0 && !expansionPlan) {
    // Show a pending hint only when ALL four sections are still empty —
    // once any one of them lands we assume the pipeline is at least
    // partially through and stop pestering the user.
    blocks.push(`*${t("thoughts.expansion_pending")}*`);
  }
  if (suggestedTopicIDs.length > 0) {
    blocks.push(`## ${t("thoughts.section_near_topics")}\n${suggestedTopicIDs.map((id) => `- \`${id}\``).join("\n")}`);
  }
  if (urlFollowups.length > 0) {
    const lines = urlFollowups.map((item) => {
      const title = item && typeof item.title === "string" && item.title.trim() ? item.title : item.url;
      return `- [${title}](${item.url})`;
    });
    blocks.push(`## ${t("thoughts.section_url_followups")}\n${lines.join("\n")}`);
  }
  if (expansionPlan) {
    blocks.push(`## ${t("thoughts.section_expansion_plan")}\n${expansionPlan}`);
  }
  if (expandError) {
    blocks.push(`*${t("thoughts.expansion_failed")}*`);
  }
  return blocks.join("\n\n");
}

async function retryRefine() {
  if (!state.activeThoughtId) {
    toast(t("toast.open_thought_first"));
    return;
  }
  const job = await api(`/api/thoughts/${encodeURIComponent(state.activeThoughtId)}/retry-refine`, { method: "POST", body: "{}" });
  toast(t("toast.retry_refine_queued", { id: job.id }));
  // PR3: the old /jobs page is gone. The job is findable via the
  // /api/jobs/:id endpoint for anyone with the id; the toast is the
  // only acknowledgement surfaced in the UI. A future iteration could
  // surface the running job in the notes runtime card.
  state.activeJobId = job.id;
}

async function createComposeDraft(event) {
  event.preventDefault();
  if (state.composeGenerating) {
    toast(t("toast.compose_generate_inflight"));
    return;
  }
  const sources = Array.from(state.composeSources.values());
  if (sources.length === 0) {
    toast(t("toast.add_sources_first"));
    return;
  }
  const selectedThoughtIds = sources
    .filter((source) => source.source_type === "thought")
    .map((source) => source.source_id);
  const profileSelect = $("#compose-format");
  const selectedProfileID = profileSelect.value;
  const selectedProfileVersion = Number(profileSelect.selectedOptions?.[0]?.dataset.version || 0);
  const submit = event?.submitter || $("#compose-form button[type='submit']");
  const previousDisabled = submit ? submit.disabled : false;
  state.composeGenerating = true;
  setComposeGenerateBusy(true);
  if (submit) submit.disabled = true;
  try {
    // POST returns a queued job immediately; the draft body arrives later
    // via compose.draft_created / job.updated and loadComposeDrafts.
    const job = await api("/api/compose/drafts", {
      method: "POST",
      body: JSON.stringify({
        sources,
        selected_thought_ids: selectedThoughtIds,
        goal: $("#compose-goal").value.trim(),
        profile_id: selectedProfileID,
        profile_version: selectedProfileVersion,
 parameters: composeDocumentParameters(),
      }),
    });
    state.composeActiveJobId = job?.id || "";
    state.activeJobId = job?.id || state.activeJobId;
    toast(t("toast.compose_generate_queued", { id: job?.id || "" }));
    closeDrawer("compose-create-drawer");
    await loadComposeDrafts();
    navigateHash(buildRouteHash("compose", {}, { tab: "writing" }), { force: true });
    // If the server already finished (fast local provider) clear the busy
    // flag; otherwise SSE or polling for this exact job will clear it.
    if (job?.status === "succeeded" || job?.status === "failed" || job?.status === "canceled") {
      clearComposeGenerationLock();
    } else {
      scheduleComposeGenerationPoll();
    }
  } catch (error) {
    clearComposeGenerationLock();
    if (submit) submit.disabled = previousDisabled;
    throw error;
  }
}

function clearComposeGenerationLock() {
  if (state.composeGenerationPollTimer) {
    window.clearTimeout(state.composeGenerationPollTimer);
    state.composeGenerationPollTimer = null;
  }
  state.composeGenerating = false;
  state.composeActiveJobId = "";
  setComposeGenerateBusy(false);
}

function scheduleComposeGenerationPoll() {
  if (!state.composeGenerating || state.composeGenerationPollTimer) return;
  state.composeGenerationPollTimer = window.setTimeout(async () => {
    state.composeGenerationPollTimer = null;
    if (!state.composeGenerating) return;
    try {
      await loadComposeDrafts();
      const active = (state.composeDrafts || []).find((draft) => draft.job_id === state.composeActiveJobId);
      if (!active || active.status !== "generating") {
        clearComposeGenerationLock();
        return;
      }
    } catch (_) {
      // Keep the lock and retry: a transient list failure must not allow a
      // duplicate submit while the server-side job remains active.
    }
    scheduleComposeGenerationPoll();
  }, 1500);
}

function setComposeGenerateBusy(busy) {
  const buttons = [
    $("#open-compose-create"),
    $("#open-compose-create-sources"),
    $("#compose-form button[type='submit']"),
  ].filter(Boolean);
  for (const button of buttons) {
    if (busy) {
      button.disabled = true;
      button.setAttribute("aria-busy", "true");
    } else {
      button.removeAttribute("aria-busy");
      // re-enable create buttons only when sources remain available
      if (button.id === "open-compose-create" || button.id === "open-compose-create-sources") {
        button.disabled = state.composeSources.size === 0;
      } else {
        button.disabled = false;
      }
    }
  }
}

function composeReaderState() {
  if (!state.composeReader || typeof state.composeReader !== "object") {
    state.composeReader = {
      activeDraftID: "",
      mode: "preview",
      localContent: "",
      loadedContent: "",
      status: "",
    };
  }
  return state.composeReader;
}

function isComposeReaderDirty() {
  const reader = composeReaderState();
  if (!reader.activeDraftID) return false;
  return String(reader.localContent || "") !== String(reader.loadedContent || "");
}

function syncComposeLocalContentFromEditor() {
  const reader = composeReaderState();
  if (reader.mode !== "edit") return;
  const output = $("#compose-output");
  if (output) reader.localContent = output.value;
}

async function confirmDiscardComposeEdits(actionLabel = "") {
  if (!isComposeReaderDirty()) return true;
  const message = actionLabel
    ? t("compose.unsaved_confirm_action", { action: actionLabel })
    : t("compose.unsaved_confirm");
  return confirmAction(t("compose.unsaved_title"), message);
}

function clearComposeReader() {
  composeRequestEpoch++;
  state.composeDraft = null;
  state.composeReader = {
    activeDraftID: "",
    mode: "preview",
    localContent: "",
    loadedContent: "",
    status: "",
  };
  renderComposeReader();
}

function setComposeReaderMode(mode) {
  const reader = composeReaderState();
  if (!reader.activeDraftID) return;
  const next = mode === "edit" ? "edit" : "preview";
  if (reader.mode === "edit" && next === "preview") {
    syncComposeLocalContentFromEditor();
  }
  // saved drafts are read-only; generating/failed cannot enter edit.
  const status = reader.status || state.composeDraft?.status || "draft";
  if (next === "edit" && status !== "draft") return;
  reader.mode = next;
  renderComposeReader();
}

function composeDraftMetaLabel(draft) {
  if (!draft) return "";
  const status = draft.status || "draft";
  const statusLabel = t(`compose.status.${status}`) || status;
  const profile = profileByID(draft.document_profile?.profile_id || "", draft.document_profile?.version || 0);
  const formatLabel = profile ? profileLabel(profile) : (draft.format || t("compose.format.summary"));
  const when = fmtDate(draft.updated_at || draft.created_at);
  return `${statusLabel} · ${formatLabel} · ${when}`;
}

function renderComposeReader() {
  const reader = composeReaderState();
  const draft = state.composeDraft;
  const empty = $("#compose-reader-empty");
  const loading = $("#compose-reader-loading");
  const preview = $("#compose-preview");
  const editor = $("#compose-output");
  const saveResult = $("#compose-save-result");
  const title = $("#compose-main-title");
  const meta = $("#compose-main-meta");
  const btnPreview = $("#compose-mode-preview");
  const btnEdit = $("#compose-mode-edit");
  const btnSave = $("#save-compose");

  const hasDraft = Boolean(draft && reader.activeDraftID);
  const status = hasDraft ? (draft.status || reader.status || "draft") : "";
  const canEdit = status === "draft";
  const canSave = status === "draft" && String(reader.localContent || "").trim() !== "";
  const mode = reader.mode === "edit" && canEdit ? "edit" : "preview";
  if (reader.mode !== mode) reader.mode = mode;

  if (title) {
    title.textContent = hasDraft
      ? (draft.goal || draft.id || t("compose.reader_empty_title"))
      : t("compose.reader_empty_title");
  }
  if (meta) {
    meta.textContent = hasDraft ? composeDraftMetaLabel(draft) : t("compose.reader_empty_hint");
  }

  if (btnPreview) {
    btnPreview.disabled = !hasDraft || status === "generating";
    btnPreview.setAttribute("aria-pressed", mode === "preview" ? "true" : "false");
  }
  if (btnEdit) {
    btnEdit.disabled = !canEdit;
    btnEdit.setAttribute("aria-pressed", mode === "edit" ? "true" : "false");
  }
  if (btnSave) btnSave.disabled = !canSave;

  const showEmpty = !hasDraft;
  const showLoading = hasDraft && status === "generating";
  const showEditor = hasDraft && !showLoading && mode === "edit" && canEdit;
  const showPreview = hasDraft && !showLoading && !showEditor;

  if (empty) empty.hidden = !showEmpty;
  if (loading) {
    loading.hidden = !showLoading;
    if (showLoading) loading.textContent = t("compose.generating_body");
  }
  if (preview) {
    preview.hidden = !showPreview;
    if (showPreview) {
      const body = String(reader.localContent || "");
      if (!body.trim() && status === "failed") {
        preview.innerHTML = `<div class="tf-empty">${escapeHTML(t("compose.failed_body"))}</div>`;
      } else if (!body.trim()) {
        preview.innerHTML = `<div class="tf-empty">${escapeHTML(t("compose.empty"))}</div>`;
      } else {
        preview.innerHTML = renderMarkdown(body);
      }
    } else {
      preview.innerHTML = "";
    }
  }
  if (editor) {
    editor.hidden = !showEditor;
    if (showEditor) {
      if (editor.value !== reader.localContent) editor.value = reader.localContent;
    }
  }
  if (saveResult) {
    // The saved Thought link belongs to the currently rendered draft. Never
    // retain a previous draft's link after switching or clearing the reader.
    const savedThoughtID = hasDraft && status === "saved" ? String(draft.saved_thought_id || "").trim() : "";
    if (savedThoughtID) {
      saveResult.hidden = false;
      saveResult.innerHTML = `<a class="tf-btn" href="#/notes?id=${encodeURIComponent(savedThoughtID)}">${escapeHTML(t("compose.view_saved"))}</a>`;
    } else {
      saveResult.hidden = true;
      saveResult.innerHTML = "";
    }
  }
}

async function deleteComposeDraft(draftId, title = "") {
  const id = String(draftId || "").trim();
  if (!id) return;
  if (composeReaderState().activeDraftID === id) {
    syncComposeLocalContentFromEditor();
    const discard = await confirmDiscardComposeEdits(t("compose.delete"));
    if (!discard) return;
  }
  const confirmed = await confirmAction(
    t("compose.delete_title"),
    t("compose.delete_confirm", { title: title || id }),
  );
  if (!confirmed) return;
  await api(`/api/compose/drafts/${encodeURIComponent(id)}`, { method: "DELETE" });
  const deletedActiveJob = (state.composeDrafts || []).some((draft) => draft.id === id && draft.job_id === state.composeActiveJobId);
  state.composeDrafts = (state.composeDrafts || []).filter((draft) => draft.id !== id);
  if (state.composeDraft?.id === id || composeReaderState().activeDraftID === id) {
    clearComposeReader();
  }
  if (deletedActiveJob) {
    clearComposeGenerationLock();
  }
  renderComposeDrafts();
  renderSidebarBadges();
  toast(t("compose.delete_done"));
}

async function loadComposeDrafts() {
  state.composeDrafts = await api("/api/compose/drafts");
  renderComposeDrafts();
  renderSidebarBadges();
  const reader = composeReaderState();
  if (reader.activeDraftID && !isComposeReaderDirty()) {
    const latest = (state.composeDrafts || []).find((d) => d.id === reader.activeDraftID);
    if (latest && (latest.status || "") !== (reader.status || "")) {
      // Status advanced (e.g. generating → draft); reload full body once.
      loadComposeDraft(reader.activeDraftID, { force: true }).catch(() => {});
    } else if (latest) {
      reader.status = latest.status || reader.status;
      if (state.composeDraft?.id === latest.id) state.composeDraft = { ...state.composeDraft, ...latest };
      renderComposeReader();
    }
  }
}

async function loadComposeDraft(draftId, options = {}) {
  const epoch = ++composeRequestEpoch;
  if (!draftId) return;
  const force = Boolean(options.force);
  const reader = composeReaderState();
  if (!force && reader.activeDraftID && reader.activeDraftID !== draftId) {
    syncComposeLocalContentFromEditor();
    const discard = await confirmDiscardComposeEdits(t("compose.switch_draft_action"));
    if (!discard || epoch !== composeRequestEpoch) return;
  } else if (!force && reader.activeDraftID === draftId && isComposeReaderDirty()) {
    // Re-clicking the same draft with dirty edits: keep local content.
    renderComposeDrafts();
    renderComposeReader();
    return;
  }
  const localAtRequest = composeReaderState().localContent;
  const draft = await api(`/api/compose/drafts/${encodeURIComponent(draftId)}`);
  syncComposeLocalContentFromEditor();
  if (epoch !== composeRequestEpoch || composeReaderState().localContent !== localAtRequest) return;
  state.composeDraft = draft;
  const content = renderComposeDraft(draft);
  const status = draft.status || "draft";
  state.composeReader = {
    activeDraftID: draft.id || draftId,
    mode: status === "draft" || status === "saved" ? "preview" : "preview",
    localContent: content,
    loadedContent: content,
    status,
  };
  if ($("#compose-goal")) $("#compose-goal").value = draft.goal || "";
  syncComposeProfileSelect(draft.document_profile?.profile_id || "", draft.document_profile?.version || 0);
  if (status === "generating") {
    toast(t("toast.compose_generate_inflight"));
  } else if (status === "failed") {
    toast(t("toast.compose_generate_failed"));
  }
  renderComposeDrafts();
  renderComposeReader();
}

function renderComposeDraft(draft) {
  return draft.content || "";
}

function composeTitleFromContent(content, draft = {}) {
  const heading = String(content || "")
    .split("\n")
    .map((line) => line.trim())
    .find((line) => /^#{1,6}\s+\S/.test(line));
  if (heading) return heading.replace(/^#{1,6}\s+/, "").trim();
  return String(draft.goal || "").split("\n", 1)[0].trim();
}

async function saveComposeDraft() {
  if (!state.composeDraft) {
    toast(t("toast.generate_writing_first"));
    return;
  }
  syncComposeLocalContentFromEditor();
  const reader = composeReaderState();
  if ((state.composeDraft.status || "draft") !== "draft") {
    toast(t("toast.compose_save_unavailable"));
    return;
  }
  const content = String(reader.localContent || "");
  if (!content.trim()) {
    toast(t("toast.writing_content_required"));
    return;
  }
  const confirmed = await confirmAction(t("compose.confirm_title"), t("compose.confirm_message"));
  if (!confirmed) return;
  const result = await api(`/api/compose/drafts/${encodeURIComponent(state.composeDraft.id)}/save`, {
    method: "POST",
    body: JSON.stringify({
      content,
      title: composeTitleFromContent(content, state.composeDraft),
      tags: [],
    }),
  });
  toast(t("toast.saved", { id: result.thought.id }));
  state.selectedThoughts.clear();
  await restoreSources();
  state.composeDraft = {
    ...state.composeDraft,
    Content: content,
    content,
    Status: "saved",
    status: "saved",
    SavedThoughtID: result.thought.id,
    saved_thought_id: result.thought.id,
  };
  state.composeReader = {
    ...reader,
    mode: "preview",
    localContent: content,
    loadedContent: content,
    status: "saved",
  };
  renderComposeReader();
  await loadComposeDrafts();
  window.setTimeout(() => runSearch().catch((error) => toast(error.message)), 1000);
}

async function refreshTopic() {
  if (!state.activeTopicId) return;
 if(state.activeTopicDetail?.topic?.mode==="collection"){await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/index/rebuild`,{method:"POST"});await openTopic(state.activeTopicId);return;}
  const confirmed = await confirmAction(t("topics.refresh"), t("topics.refresh") + ".");
  if (!confirmed) return;
  const job = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/refresh`, { method: "POST", body: "{}" });
  toast(t("toast.refresh_queued", { id: job.id }));
  // PR3: jobs page is gone; the toast is the only acknowledgement.
  state.activeJobId = job.id;
}

const DOMAIN_EVENT_TYPES = [
 "object.placement_changed","topic.migration_completed","topic.migration_failed","thought.restored","topic.catalog_updated","topic.search_policy_updated","thought.search_policy_updated","thought.ownership_changed","topic.resource_updated","topic.snapshot_created","topic.mode_changed","topic.deleted",
 "document_set.progress", "document_set.awaiting_review", "document_set.committed", "document_set.failed", "document_set.finalized",
 "document.revision_created", "document.validation_completed", "document.finalized", "document.deleted",
  "thought.captured",
  "thought.deleted",
  "thought.refine_started",
  "thought.refined",
  "thought.refine_failed",
  "thought.patched",
  "thought.expanded",
  "scratchpad.context_enrich_requested",
  "scratchpad.context_updated",
  "scratchpad.committed",
  "search.index_updated",
  "search.index_failed",
  "search.reindex_started",
  "search.reindex_finished",
  "topic.created",
  "topic.matched",
  "topic.updated",
  "topic.refresh_started",
  "topic.refresh_failed",
  "git.commit_requested",
  "git.commit_succeeded",
  "git.commit_failed",
  "job.updated",
  "compose.draft_created",
  "compose.draft_saved",
  "compose.draft_deleted",
];

const CAPTURE_EVENT_TYPES = new Set([
  "scratchpad.committed",
  "scratchpad.context_updated",
  "thought.captured",
  "thought.refined",
  "thought.refine_failed",
  "thought.patched",
  "thought.expanded",
]);

function connectEvents() {
  // The settings drawer owns the full event stream. The overview is backed
  // by persisted capture history, so it never receives internal SSE events.
  const drawerList = $("#settings-drawer-event-list");
  renderEventEmptyState(drawerList);
  const source = new EventSource("/api/events");
  source.onmessage = (event) => appendEvent("message", event.data);
  DOMAIN_EVENT_TYPES.forEach((type) => {
    source.addEventListener(type, (event) => {
      appendEvent(type, event.data);
 if(type.startsWith("document.")) handleDocumentEvent(event.data);
 if((type.startsWith("topic.")||type.startsWith("document.")||type==="thought.ownership_changed")&&collectionWorkspace&&$("#collection-controls")){let notice=$("#collection-change-notice");if(!notice){notice=document.createElement("p");notice.id="collection-change-notice";notice.setAttribute("role","status");$("#collection-controls").appendChild(notice);}notice.textContent=t("workspace.changed");}
      if (type === "topic.updated") loadTopics().catch(() => {});
      if (type === "thought.captured") loadMetrics().catch(() => {});
      if (type === "thought.deleted") {
        let deleted; try { deleted = JSON.parse(event.data).resource_id; } catch (_) {}
        if (deleted) {
          thoughtRequestEpoch++;
          state.capture.thoughtSnapshots.delete(deleted);
          state.selectedThoughts.delete(deleted);
          if (state.activeThoughtId === deleted) { state.activeThoughtId = ""; state.activeThoughtSnapshot = null; renderEmptyThoughtPanels(); }
          if (state.capture.activeThoughtId === deleted) { beginCaptureSelection(); state.capture.activeThoughtId = ""; state.capture.activeSnapshot = null; }
        }
      }
      if (type.startsWith("thought.")) loadThoughts().catch(() => {});
      if (type === "compose.draft_created" || type === "compose.draft_saved" || type === "compose.draft_deleted" || type === "job.updated") {
        handleComposeEvent(type, event.data);
      }
      // scratchpad.* and thought.* events are routed to a single
      // capture-aware dispatcher. The dispatcher re-fetches the
      // relevant scratchpad / thought payload and re-renders the
      // conversation in place — including per-bubble thought snapshots
      // for multi-thought sessions. SSE is the source of truth; the
      // 1.95 s refreshActiveScratchpadContext poll is a best-effort
      // fast path that loses gracefully when LLM round-trips take
      // 30-90 s.
      if (CAPTURE_EVENT_TYPES.has(type) || type.startsWith("document_set.")) {
        handleCaptureEvent(type, event.data);
      }
    });
  });
  source.onerror = () => {
    if (!hasEventItems(drawerList)) {
      appendEvent("events", t("toast.sse_reconnecting"));
    }
  };
}

// handleComposeEvent refreshes the writing list when a generate job
// progresses or a draft is materialised. It also clears the local
// composeGenerating lock once the active job leaves a non-terminal state.
function handleComposeEvent(type, rawData) {
  let payload = null;
  try { payload = JSON.parse(rawData); } catch (_) { payload = null; }
  const eventPayload = payload?.payload || payload || {};
  if (type === "job.updated") {
    const job = eventPayload;
    const jobType = job?.type || "";
    const jobID = job?.id || "";
    if (jobType && jobType !== "compose_generate" && jobID !== state.composeActiveJobId) {
      // Unrelated jobs still may not need a compose refresh.
    } else if (jobType === "compose_generate" || jobID === state.composeActiveJobId || !jobType) {
      loadComposeDrafts().catch(() => {});
      const status = job?.status || "";
      if (status === "succeeded" || status === "failed" || status === "canceled") {
        if (jobID && jobID === state.composeActiveJobId) {
          clearComposeGenerationLock();
          if (status === "succeeded") {
            toast(t("toast.compose_generate_ready"));
            // The server consumes only this request's sources after it
            // materialises the draft. Re-read the shared queue so the UI
            // keeps any sources added while generation was running.
            restoreSources().catch(() => {});
            // Prefer the finished draft if resource_id is present.
            if (job?.resource_id) {
              loadComposeDraft(job.resource_id).catch(() => {});
            }
          } else if (status === "failed") {
            toast(t("toast.compose_generate_failed"));
          }
        }
      }
    }
    return;
  }
  // draft_created / draft_saved / draft_deleted — refresh the list.
  if (type === "compose.draft_deleted") {
    const deletedID = eventPayload?.draft_id || payload?.resource_id || "";
    if (deletedID && (state.composeDraft?.id === deletedID || composeReaderState().activeDraftID === deletedID)) {
      clearComposeReader();
    }
    loadComposeDrafts().then(() => {
      const stillGenerating = (state.composeDrafts || []).some((d) => d.status === "generating");
      if (!stillGenerating) clearComposeGenerationLock();
      renderComposeReader();
    }).catch(() => {});
    return;
  }
  loadComposeDrafts().catch(() => {});
  const draft = eventPayload;
  if (draft && draft.status && draft.status !== "generating") {
    if (!state.composeActiveJobId || draft.job_id === state.composeActiveJobId) {
      clearComposeGenerationLock();
      if (draft.status === "draft" && draft.id) {
        // Auto-open the freshly generated draft when the user is on compose.
        if (state.route?.page === "compose") {
          loadComposeDraft(draft.id).catch(() => {});
        }
      }
    }
  }
}

// handleCaptureEvent routes scratchpad and thought SSE events into the
// capture conversation. It is the single source of truth for keeping
// state.capture.* in sync with the backend: the per-thoughtId snapshot
// cache, the active thoughtId, assistant replies, and the
// anchor bubble list. Side effects are idempotent — multiple events
// for the same thought collapse to the latest snapshot, and re-renders
// are full re-renders so we accept a small re-render churn cost.
async function handleCaptureEvent(type, rawData) {
 if(type.startsWith("document_set.")){
  let event;try{event=typeof rawData==="string"?JSON.parse(rawData):rawData;}catch{return;}
  const scope=captureRequestScope(),epoch=captureSetEpoch,visible=captureDocumentSet;const id=event.payload?.run_id||event.run_id;if(!id||!scope.sessionId)return;
  const eventRequest=++captureSetEventRequest;
  const sp=await api(`/api/capture/sessions/${encodeURIComponent(scope.sessionId)}`);
  if(!scope.isCurrent()||epoch!==captureSetEpoch||sp.document_set_run_id!==id||eventRequest<captureSetQualifiedEvent)return;
  if(captureDocumentSet!==visible&&captureDocumentSet?.run?.run_id!==id)return;
  // Only an event qualified by the persisted session may supersede a run GET.
  // Keep arrival ordering when authority lookups themselves return out of order.
  captureSetQualifiedEvent=eventRequest;
  const sequence=++captureSetRequest;
  const run=await api(`/api/document-set-runs/${encodeURIComponent(id)}`);
  if(!scope.isCurrent()||epoch!==captureSetEpoch||sequence!==captureSetRequest||run.plan.capture.session_id!==scope.sessionId)return;
  if(captureDocumentSet!==visible&&captureDocumentSet?.run?.run_id!==id)return;
  acceptCaptureSetRun(scope.sessionId,run);return;
 }

  let payload = null;
  try { payload = JSON.parse(rawData); } catch (_) { return; }
  if (!payload) return;

  const resourceID = payload.resource_id || "";
  const eventPayload = payload.payload || {};
  const sessionID =
    eventPayload.session_id ||
    (type === "scratchpad.context_updated" || type === "scratchpad.committed" ? resourceID : "");

  // 1. scratchpad.context_updated — the capture-context LLM has
  // updated session_context, or the user/auto-pipeline has rewritten
  // the context block. Re-fetch the scratchpad and re-render.
  if (type === "scratchpad.context_updated") {
    if (!state.capture.sessionId || state.capture.sessionId !== sessionID) return;
    const scope = captureRequestScope();
    let detail = null;
    try {
      detail = await captureAPI(scope, `/api/capture/sessions/${encodeURIComponent(scope.sessionId)}`);
    } catch (_) { return; }
    if (!scope.isCurrent() || !detail || detail.session_id !== scope.sessionId) return;
    state.capture.activeScratchpad = detail;
    state.capture.archivePreview = detail.archive_preview || null;
    resumeCaptureArchivePolling();
    replaceCaptureMessagesFromScratchpad(detail);
    upsertCaptureContextMessage();
    renderCaptureConversation();
    return;
  }

  // 2. scratchpad.committed — a thought has been committed from this
  // scratchpad session. Switch the active thoughtId, anchor a new
  // bubble (de-duped against existing messages), and re-fetch the
  // thought snapshot so the rich card appears.
  if (type === "scratchpad.committed") {
    if (!state.capture.sessionId || eventPayload.session_id !== state.capture.sessionId) return;
    const thoughtID = eventPayload.thought_id || resourceID;
    const mode = eventPayload.mode || "";
    if (thoughtID && state.capture.activeThoughtId !== thoughtID) {
      state.capture.activeThoughtId = thoughtID;
      const alreadyAnchored = (state.capture.messages || []).some(
        (m) => m.thoughtId === thoughtID
      );
      if (!alreadyAnchored) {
        appendCaptureMessage({
          role: "ai",
          thoughtId: thoughtID,
          text: t("capture.session.thought_committed", { id: thoughtID }),
        });
      }
      refreshThoughtSnapshot(thoughtID);
    }
    if (mode === "repeat" || mode === "update") {
      refreshActiveCaptureThought();
    }
    return;
  }

  // 3. thought.* — re-fetch the thought snapshot, write to the per-
  // thoughtId cache, and let the conversation re-render. The notes-
  // page preview is refreshed in parallel when the open thought
  // matches (preserves the pre-SSE-routing behaviour).
  if (
    type === "thought.captured" ||
    type === "thought.refined" ||
    type === "thought.patched" ||
    type === "thought.expanded" ||
    type === "thought.refine_failed"
  ) {
    if (!resourceID) return;
    const trackedByCapture = captureConversationTracksThought(resourceID);
    if (state.activeThoughtId === resourceID) {
      // Notes-page preview is keyed on the global state.activeThoughtId,
      // not state.capture.* — so any thought.* update for the open note
      // should refresh the Notes surfaces in place.
      previewThought(resourceID).catch((error) => toast(error.message));
    }
    if (!trackedByCapture) return;
    if (type === "thought.refine_failed") {
      appendCaptureMessage({
        role: "system",
        text: t("capture.session.refine_failed", { id: resourceID }),
      });
    }
    if (type === "thought.expanded") {
      appendCaptureMessage({
        role: "system",
        text: t("capture.session.expanded", { id: resourceID }),
      });
    }
    if (
      type === "thought.captured" ||
      type === "thought.refined" ||
      type === "thought.patched" ||
      type === "thought.expanded"
    ) {
      await refreshThoughtSnapshot(resourceID);
    }
    return;
  }
}

function appendEvent(type, data) {
  // Keep operational events out of the overview. They remain available in
  // the settings drawer for diagnostics and in the notes runtime card.
  const drawerList = $("#settings-drawer-event-list");
  const item = document.createElement("article");
  item.className = "event-item";
  let parsed = data;
  let resourceID = "";
  try {
    const value = JSON.parse(data);
    parsed = `${value.resource_type || ""}:${value.resource_id || ""}`;
    resourceID = value.resource_id || "";
  } catch (_error) {
    parsed = data;
  }
  item.dataset.eventType = type;
  item.dataset.resourceId = resourceID;
  item.innerHTML = `<strong>${escapeHTML(type)}</strong><div class="event-meta">${escapeHTML(parsed)}</div>`;
  prependEventItem(drawerList, item);
  applyEventFilter();
}

function hasEventItems(list) {
  return Boolean(list && Array.from(list.children || []).some((child) => child?.classList?.contains?.("event-item")));
}

function renderEventEmptyState(list) {
  if (!list || hasEventItems(list)) return;
  list.innerHTML = `<div class="tf-empty" data-empty="true">${escapeHTML(t("settings.events.empty"))}</div>`;
  if (list.dataset) list.dataset.empty = "true";
}

function clearEventEmptyState(list) {
  if (!list || !list.dataset || list.dataset.empty !== "true") return;
  list.innerHTML = "";
  delete list.dataset.empty;
}

function prependEventItem(list, item, limit = 60) {
  if (!list || !item) return;
  clearEventEmptyState(list);
  list.prepend(item);
  while (list.children.length > limit) list.removeChild(list.lastChild);
}

function applyEventFilter() {
  const type = ($("#settings-drawer-event-type")?.value || "").trim().toLowerCase();
  const resource = ($("#settings-drawer-event-resource")?.value || "").trim().toLowerCase();
  document.querySelectorAll("#settings-drawer-event-list .event-item").forEach((item) => {
    const matchesType = !type || String(item.dataset.eventType || "").toLowerCase().includes(type);
    const matchesResource = !resource || String(item.dataset.resourceId || item.textContent || "").toLowerCase().includes(resource);
    item.hidden = !(matchesType && matchesResource);
  });
}

function resetEventFilter() {
  $("#settings-drawer-event-type").value = "";
  $("#settings-drawer-event-resource").value = "";
  applyEventFilter();
}

function activateTab(name, scope = document) {
  const root = scope || document;
  root.querySelectorAll(".tab").forEach((tab) => tab.classList.toggle("active", tab.dataset.tab === name));
  root.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.toggle("active", panel.id === `tab-${name}`));
}

function renderRoute(route = state.route) {
  document.querySelectorAll(".tf-page").forEach((page) => {
    page.classList.toggle("active", page.dataset.page === route.page);
  });
  document.querySelectorAll("[data-nav]").forEach((item) => {
    item.className = navItemClass(route, item.dataset.nav);
    const current = navItemAriaCurrent(route, item.dataset.nav);
    if (current) item.setAttribute("aria-current", current);
    else item.removeAttribute("aria-current");
  });
}

async function applyRoute(hash = window.location.hash, options = {}) {
 if(documentEditor?.dirty && documentEditor.routeHash!==hash && !options.keepEditor && !window.confirm(t("docset.unsaved"))){history.replaceState(null,"",documentEditor.routeHash||"#/notes");return;}

  const serial = ++routeApplySerial;
  const route = parseRoute(hash);
  state.route = route;
 ++topicRequestEpoch; ++collectionReadEpoch;
  // Restore DOM inputs / state from the URL query before rendering, so the
  // first render of the page reflects the URL — no flicker of defaults.
  restoreRoutePage(route.page, route.query);
  renderRoute(route);
  await refreshRouteData(route, options);
  if (serial !== routeApplySerial) return;
  if (route.page === "topics" && route.params.topicId) {
    await openTopic(route.params.topicId);
    if (serial !== routeApplySerial) return;
    const topicTab = normalizeTopicsTabName(route.query.tab);
    activateTab(topicTab, $("#page-topics"));
  }
  if (route.page === "thoughts" && route.params.thoughtId) {
    await previewThought(route.params.thoughtId);
  }
  // Live routes: capture, search, topics (incl. detail), notes, compose.
  // settings and jobs are gone — the gear opens a drawer, jobs surface on
  // the notes runtime card. No additional loader needed here.
}

function bind() {
  $("#thought-import-start")?.addEventListener("click", () => importThoughtFiles());
  $("#thought-reader-close")?.addEventListener("click", () => $("#thought-reader")?.close());
  $("#thought-reader")?.addEventListener("close", () => { thoughtReaderEpoch++; thoughtReaderSnapshot = null; });
  $("#thought-reader-expand")?.addEventListener("click", (event) => {
    const dialog = $("#thought-reader");
    const expanded = !dialog.classList.contains("is-expanded");
    dialog.classList.toggle("is-expanded", expanded);
    event.currentTarget.setAttribute("aria-pressed", String(expanded));
  });
  document.querySelectorAll("[data-reader-view]").forEach(button => button.addEventListener("click", () => renderThoughtReaderView(button.dataset.readerView).catch(error => toast(error.message))));
  document.querySelectorAll("[data-nav]").forEach((item) => {
    item.addEventListener("click", handleNavClick);
  });
  $("#capture-form")?.addEventListener("submit", (event) => captureThought(event).catch((error) => toast(error.message)));
  $("#capture-composer")?.addEventListener("submit", (event) => submitCaptureComposer(event).catch((error) => toast(error.message)));
  $("#capture-composer-input")?.addEventListener("keydown", handleCaptureComposerKeydown);
  $("#capture-new-session")?.addEventListener("click", () => newCaptureSession());
  $("#capture-sessions-toggle")?.addEventListener("click", () => {
    const drawer = $("#capture-sessions-drawer");
    if (!drawer) return;
    if (drawer.hidden) openCaptureSessionsDrawer();
    else closeCaptureSessionsDrawer();
  });
  $("#capture-sessions-close")?.addEventListener("click", () => closeCaptureSessionsDrawer());
  $("#capture-lock-takeover")?.addEventListener("click", () => takeoverCaptureLock().catch((error) => toast(error.message)));
  bindCaptureSessionLock();
  $("#topic-form").addEventListener("submit", (event) => createTopic(event).catch((error) => toast(error.message)));
  $("#topic-edit-form").addEventListener("submit", (event) => saveTopicRules(event).catch((error) => toast(error.message)));
  $("#search-form").addEventListener("submit", (event) => runSearch(event).catch((error) => toast(error.message)));
  $("#search-query").addEventListener("input", persistRouteDebounced);
  $("#search-tags").addEventListener("input", persistRouteDebounced);
  $("#reset-search").addEventListener("click", () => { resetSearchFilters(); persistRouteDebounced(); });
  $("#thought-filter")?.addEventListener("input", () => renderThoughtsList());
  $("#refresh-thoughts")?.addEventListener("click", () => loadThoughts().catch((error) => toast(error.message)));
  $("#compose-form").addEventListener("submit", (event) => createComposeDraft(event).catch((error) => toast(error.message)));
  $("#settings-drawer-event-type")?.addEventListener("input", () => applyEventFilter());
  $("#settings-drawer-event-resource")?.addEventListener("input", () => applyEventFilter());
  $("#settings-drawer-reset-event-filter")?.addEventListener("click", () => resetEventFilter());
  $("#settings-help-visible")?.addEventListener("change", (event) => setHelpVisible(event.target.checked));
  $("#notes-runtime-refresh")?.addEventListener("click", () => refreshNotesRuntime().catch((error) => toast(error.message)));
  $("#open-captures-from-dashboard")?.addEventListener("click", () => {
    navigateHash(buildRouteHash("capture"), { force: true });
    openCaptureSessionsDrawer();
  });
  $("#save-compose").addEventListener("click", () => saveComposeDraft().catch((error) => toast(error.message)));
  $("#compose-mode-preview")?.addEventListener("click", () => setComposeReaderMode("preview"));
  $("#compose-mode-edit")?.addEventListener("click", () => setComposeReaderMode("edit"));
  $("#compose-output")?.addEventListener("input", () => {
    const reader = composeReaderState();
    if (reader.mode === "edit") {
      reader.localContent = $("#compose-output")?.value || "";
      const save = $("#save-compose");
      if (save) save.disabled = (reader.status || state.composeDraft?.status || "draft") !== "draft" || !String(reader.localContent || "").trim();
    }
  });
  $("#refresh-topics").addEventListener("click", () => loadTopics().catch((error) => toast(error.message)));
  $("#topic-filter").addEventListener("input", () => { renderTopics(); persistRouteDebounced(); });
  $("#reset-topic-filter").addEventListener("click", () => { resetTopicFilters(); persistRouteDebounced(); });
  $("#refresh-topic").addEventListener("click", () => refreshTopic().catch((error) => toast(error.message)));
  // Reindex entry removed — the search page no longer exposes a reindex
  // button. /api/system/reindex stays available for ops use; the Web UI
  // surfaces index health through the settings drawer instead.
  $("#open-create-topic").addEventListener("click", () => openDrawer("topic-create-drawer"));
  $("#workspace-import-file").addEventListener("change",importWorkspaceBackup);
  $("#workspace-recovery").addEventListener("click",loadWorkspaceRecovery);
  $("#open-topic-rules").addEventListener("click", () => openDrawer("topic-rules-drawer"));
  $("#open-compose-create").addEventListener("click", () => openDrawer("compose-create-drawer"));
  $("#open-compose-create-sources")?.addEventListener("click", () => openDrawer("compose-create-drawer"));
  $("#refresh-compose").addEventListener("click", () => loadComposeDrafts().catch((error) => toast(error.message)));
  $("#add-selected-compose").addEventListener("click", () => {
    addToComposeSources(Array.from(state.selectedThoughts));
    openComposeSources();
  });
  $("#clear-selected").addEventListener("click", clearSearchSelection);
  $("#clear-compose-sources-tab").addEventListener("click", clearComposeSources);
  $("#drawer-add-compose").addEventListener("click", () => addToComposeSources([state.activeThoughtId]));
  $("#drawer-read-full")?.addEventListener("click", () => openThoughtReader(state.activeThoughtId).catch(error => toast(error.message)));
  $("#retry-refine").addEventListener("click", () => retryRefine().catch((error) => toast(error.message)));
  $("#confirm-cancel").addEventListener("click", () => closeConfirm(false));
  $("#confirm-ok").addEventListener("click", () => closeConfirm(true));
  // PR3: dashboard summary cards open the settings drawer at the relevant
  // tab instead of navigating to a dedicated /settings page.
  document.querySelectorAll("[data-open-drawer]").forEach((card) => {
    card.addEventListener("click", () => {
      const drawer = card.dataset.openDrawer;
      const tab = card.dataset.drawerTab || "";
      openSettingsDrawer(drawer, tab);
    });
  });
  document.querySelectorAll("[data-close-drawer]").forEach((button) => {
    button.addEventListener("click", () => closeDrawer(button.dataset.closeDrawer));
  });
  document.querySelectorAll(".tab").forEach((tab) => tab.addEventListener("click", handleTabClick));
  document.querySelectorAll("#settings-language [data-locale]").forEach((button) => {
    button.addEventListener("click", () => {
      tSetLocale(button.dataset.locale);
      rerenderForLocale();
    });
  });
  $("#open-settings")?.addEventListener("click", () => {
    // PR3: the gear button opens the settings drawer at the general tab.
    // The drawer hosts language switching, models, sync, index, and the
    // event stream — the same content used to live on a top-level page.
    openSettingsDrawer("settings-drawer", "settings-drawer-general");
  });
  tOnLocaleChange(() => {
    syncLanguageSwitcher();
    rerenderForLocale();
  });
  window.addEventListener("hashchange", () => applyRoute().catch((error) => toast(error.message)));
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      // Close the topmost open drawer/modal. Confirms go last so they
      // don't get hidden by a drawer that opened on top.
      const openDrawers = document.querySelectorAll(".tf-drawer.open");
      if (openDrawers.length > 0) {
        closeDrawer(openDrawers[openDrawers.length - 1].id);
      } else {
        closeConfirm(false);
      }
    }
  });
  document.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      $("#search-query").focus();
    }
  });
}

// PR3: open the settings drawer and optionally jump to a specific tab. The
// drawer is reused as the single home for system status, language, models,
// sync, index, metrics, and the live event stream. Metrics load lazily on
// first open since the request is the heaviest of the bunch and most users
// never look at them.
function openSettingsDrawer(drawerID = "settings-drawer", tabID = "") {
  if (tabID) activateTab(tabID, $(`#${drawerID}`));
  openDrawer(drawerID);
  if (!state.metricsLoadedOnce) {
    state.metricsLoadedOnce = true;
    loadMetrics().catch(() => {});
  }
}

function syncLanguageSwitcher() {
  const locale = tGetLocale();
  document.querySelectorAll("#settings-language [data-locale]").forEach((button) => {
    const active = button.dataset.locale === locale;
    button.setAttribute("aria-pressed", String(active));
    button.classList.toggle("active", active);
  });
}

function rerenderForLocale() {
  tApply(document);
  // re-render dynamic content that holds its own text
  try { renderThoughtsList(); } catch (_) {}
  try { renderThoughtPanels(); } catch (_) {}
  try { renderTopics(); } catch (_) {}
  try { renderComposeSources(); } catch (_) {}
  try { renderAboutDocumentProfiles(); } catch (_) {}
  try { syncComposeProfileSelect(state.composeDraft?.document_profile?.profile_id || "", state.composeDraft?.document_profile?.version || 0); } catch (_) {}
  try { renderCaptureConversation(); } catch (_) {}
  try { updateSelectionControls(); } catch (_) {}
  if (state.status) {
    try { renderTopbarStatus(state.status); } catch (_) {}
    try { renderSettingsStatus(state.status); } catch (_) {}
  }
  if (state.composeDrafts) {
    try { renderComposeDrafts(); } catch (_) {}
  }
  try { renderComposeReader(); } catch (_) {}
}

async function boot() {
 window.addEventListener("beforeunload",event=>{if(documentEditor?.dirty){event.preventDefault();event.returnValue="";}});
  if (typeof tInit === "function") tInit();
  // Apply static translations to data-i18n elements BEFORE the async loaders
  // run, so the initial paint shows the chosen locale. Dynamic elements
  // (e.g. #system-status, which is set by loadStatus) must not carry
  // data-i18n, otherwise this call would overwrite the live text on every
  // boot and any subsequent locale switch.
  tApply(document);
  syncLanguageSwitcher();
  applyHelpVisibility();
  initTflowBus();
  if (tflowBus) {
    tflowBus.on((message) => {
      if (!message || typeof message !== "object") return;
      if (message.kind === "compose:sources_changed") {
        restoreSources().catch((error) => toast(error.message));
      }
    });
  }
  await restoreSources().catch((error) => toast(error.message));
  await loadDocumentProfiles().catch((error) => {
    state.documentProfiles = [];
    syncComposeProfileSelect();
    renderAboutDocumentProfiles();
    toast(error.message);
  });
  state.capture.sessions = loadCaptureSessions();
  renderDashboardCaptureSessions();
  // Sweep any stale cross-tab session locks before we render. Per-key
  // getHolder() only fires for thoughts the user actually opens, but
  // a tab that crashed (or was force-killed) can leave lock entries
  // behind for up to 90s. Cleaning them up here keeps the lock
  // indicator from flashing "another session is editing" on the next
  // visit just because the previous holder is long gone.
  if (window.tflowSessionLock && typeof window.tflowSessionLock.sweepStaleLocks === "function") {
    try { window.tflowSessionLock.sweepStaleLocks(); } catch (_error) { /* ignore */ }
  }
  bind();
 $("#capture-generate-set")?.addEventListener("click",()=>openCaptureDocumentSet().catch(error=>toast(error.message)));
  // Render the source counter once the page is reachable. Done here so the
  // rehydrated count shows even before the user opens the compose page.
  if (typeof renderComposeSources === "function") {
    try { renderComposeSources(); } catch (_error) { /* noop before render */ }
  }
  if (typeof renderCaptureConversation === "function") {
    try { renderCaptureConversation(); } catch (_error) { /* noop before render */ }
  }
  if (!window.location.hash) window.location.hash = "#/overview";
  state.route = parseRoute(window.location.hash);
  renderRoute();
  await loadStatus();
  await loadPrivacyReport();
  await loadTopics();
  await loadComposeDrafts();
  await loadThoughts();
  await loadMetrics();
  await applyRoute();
  // Land the user back in the most recent uncommitted capture
  // session if there is one, so a refresh doesn't dump them onto
  // an empty composer. The server's last_active_session_id is the
  // authority; the browser does not persist capture session history.
  await rehydrateActiveScratchpad();
  connectEvents();
  window.setInterval(() => loadDocumentProfiles().catch(() => {}), 5000);
  window.__thoughtflowReady = true;
}



// A dialog owns its local editing epoch. Late GET/validation responses cannot
// overwrite edits or make a report for another revision appear current.
let documentEditor = null;
function renderComposeDocumentInputs() {
 const node = $("#compose-document-inputs");
 const select = $("#compose-format");
 if (!node || !select) return;
 const profile = profileByID(select.value, Number(select.selectedOptions?.[0]?.dataset?.version || 0));
 node.innerHTML = (profile?.inputs || []).map(input => `<label>${escapeHTML(input.label || input.key)}${input.required ? " *" : ""}<input data-document-parameter="${escapeHTML(input.key)}" value="${escapeHTML(input.default || "")}" ${input.required ? "required" : ""}></label>`).join("");
 select.onchange = renderComposeDocumentInputs;
}
function composeDocumentParameters() {
 const values = {};
 document.querySelectorAll("#compose-document-inputs [data-document-parameter]").forEach(input => { values[input.dataset.documentParameter] = input.value.trim(); });
 return values;
}
function bindDocumentButtons(root = document) {
 root.querySelectorAll("[data-document-open]").forEach(button => {
  if (button.dataset.documentBound) return;
  button.dataset.documentBound = "1";
  button.addEventListener("click", () => openDocumentEditor(button.dataset.documentOpen).catch(error => toast(error.message)));
 });
}
function documentCanFinalize(editor) {
 const snapshot = editor.snapshot;
 return !editor.dirty && !editor.busy && !editor.serverChanged && snapshot.freshness === "current" && snapshot.report && !["failed", "running", "not_run"].includes(snapshot.report.status)
   && !(snapshot.report.issues || []).some(issue => issue.severity === "error" && !issue.human_confirmation);
}
let documentOpenEpoch=0;
async function openDocumentEditor(id) {
 if(documentEditor?.busy)return;
 if(documentEditor?.dirty&&!window.confirm(t("docset.unsaved")))return;
 const openEpoch=++documentOpenEpoch;
 const thought = await api(`/api/thoughts/${encodeURIComponent(id)}`);
 if(openEpoch!==documentOpenEpoch)return;
 if(!thought.thought?.document){return openPlainThoughtEditor(thought);}
 const snapshot = await api(`/api/documents/${encodeURIComponent(id)}`);
 if(openEpoch!==documentOpenEpoch)return;
 if(documentEditor?.dirty && !window.confirm(t("docset.unsaved")))return;
 documentEditor?.dialog?.remove();
 const dialog = document.createElement("dialog");
 dialog.className = "tf-document-dialog";
 dialog.innerHTML = `<form method="dialog"><button class="tf-btn" type="submit">${escapeHTML(t("common.close"))}</button></form>
 <h2>${escapeHTML(t("document.open"))}</h2><div data-document-status role="status"></div>
 <div class="tf-document-layout"><div><div data-document-inputs class="tf-document-inputs"></div>
 <details><summary>${escapeHTML(t("document.sources"))}</summary><p>${escapeHTML(t("document.evidence_private"))}</p><div data-document-sources></div><button class="tf-btn" data-document-add-source>${escapeHTML(t("document.add_source"))}</button></details>
 <label>${escapeHTML(t("document.body"))}<textarea data-document-body rows="24" spellcheck="false"></textarea></label></div>
 <aside data-document-issues></aside></div>
 <div class="tf-action-row"><button class="tf-btn" data-document-save>${escapeHTML(t("document.save"))}</button>
 <select data-document-review-mode><option value="semantic">${escapeHTML(t("document.review_semantic"))}</option><option value="manual">${escapeHTML(t("document.review_manual"))}</option></select>
 <button class="tf-btn" data-document-check>${escapeHTML(t("document.check"))}</button>
 <select data-document-delivery><option value="proposal">${escapeHTML(t("document.proposal"))}</option><option value="ready_for_use">${escapeHTML(t("document.ready_for_use"))}</option></select>
 <button class="tf-btn" data-document-finalize>${escapeHTML(t("document.finalize"))}</button>
 <button class="tf-btn" data-document-repair>${escapeHTML(t("document.repair"))}</button>
 <a class="tf-btn" data-document-draft-export>${escapeHTML(t("document.export_draft"))}</a>
 <a class="tf-btn" data-document-final-export>${escapeHTML(t("document.export_final"))}</a></div>`;
 document.body.appendChild(dialog);
 const editor = { id, snapshot, dialog, dirty:false, busy:false, epoch:0, serverChanged:false };
 editor.routeHash=window.location.hash;documentEditor = editor;
 for(const source of snapshot.source_manifest.sources || [])appendDocumentSource(editor,source);
 dialog.querySelector("[data-document-add-source]").addEventListener("click",()=>{appendDocumentSource(editor,{source_id:`S${dialog.querySelectorAll("[data-document-source]").length+1}`,type:"external",verification_status:"unverified"});editor.dirty=true;editor.epoch++;renderDocumentStatus(editor);});
 dialog.querySelector("[data-document-body]").value = snapshot.body;
 const requiredInputs=profileByID(snapshot.contract.profile_ref.profile_id,snapshot.contract.profile_ref.version)?.inputs || [];
 const defaults=Object.fromEntries(requiredInputs.map(input=>[input.key,input.default || ""]));
 const parameters = {...defaults,...snapshot.contract.parameters, title:snapshot.contract.title, purpose:snapshot.contract.purpose, audience:snapshot.contract.audience, language:snapshot.contract.language};
 dialog.querySelector("[data-document-inputs]").innerHTML = Object.entries(parameters).filter(([key]) => !key.startsWith("_")).map(([key,value]) => `<label>${escapeHTML(key)}<input data-document-input="${escapeHTML(key)}" value="${escapeHTML(value || "")}"></label>`).join("");
 dialog.addEventListener("input", event => {
  if(event.target.matches("[data-document-confirm],[data-document-reason],[data-document-delivery]"))editor.operationID=null;
  if (!event.target.matches("[data-document-body],[data-document-input],[data-document-source] input,[data-document-source] textarea")) return;
  editor.epoch++; editor.dirty=true; renderDocumentStatus(editor);
 });
 dialog.addEventListener("keydown",event=>{if(event.key!=="Escape")return;event.preventDefault();event.stopPropagation();if(!editor.busy&&(!editor.dirty||window.confirm(t("docset.unsaved"))))dialog.close();});
 dialog.addEventListener("cancel", event=>{if(editor.busy || (editor.dirty&&!window.confirm(t("docset.unsaved"))))event.preventDefault();});
 dialog.querySelector('form[method="dialog"]').addEventListener("submit",event=>{if(editor.busy || (editor.dirty&&!window.confirm(t("docset.unsaved"))))event.preventDefault();});
 dialog.addEventListener("close", () => { if (documentEditor === editor) documentEditor=null;dialog.remove(); });
 const action = (selector,fn) => dialog.querySelector(selector).addEventListener("click", () => documentEditorAction(editor,fn).catch(error => toast(error.message)));
 action("[data-document-save]", () => saveDocumentEditor(editor));
 action("[data-document-check]", async () => {
  if (editor.dirty) await saveDocumentEditor(editor);
  if(editor.dirty) return;
  const revision = editor.snapshot.head.working_revision_id;
  const report = await api(`/api/documents/${encodeURIComponent(id)}/validate`, {method:"POST",headers:{"If-Match":String(editor.snapshot.head.generation)},body:JSON.stringify({revision_id:revision,review_mode:dialog.querySelector("[data-document-review-mode]").value,operation_id:crypto.randomUUID()})});
  if (editor.snapshot.head.working_revision_id === report.revision_id) {editor.snapshot.report=report;editor.snapshot.freshness="current";}
 });
 action("[data-document-finalize]", async () => {
  if (!documentCanFinalize({...editor,busy:false})) return;
  const confirmations = [...dialog.querySelectorAll("[data-document-confirm]")].filter(node => node.checked).map(node => ({issue_id:node.dataset.documentConfirm,reason:dialog.querySelector(`[data-document-reason="${node.dataset.documentConfirm}"]`).value.trim()}));
  const required = (editor.snapshot.report.issues || []).filter(issue => issue.human_confirmation);
  if (required.some(issue => !confirmations.some(c => c.issue_id===issue.id && c.reason))) throw new Error(t("document.confirm_required"));
  const request={revision_id:editor.snapshot.head.working_revision_id,report_id:editor.snapshot.report.report_id,operation_id:editor.operationID || (editor.operationID=crypto.randomUUID()),delivery_level:dialog.querySelector("[data-document-delivery]").value,confirmations};
  await api(`/api/documents/${encodeURIComponent(id)}/finalize`,{method:"POST",headers:{"If-Match":String(editor.snapshot.head.generation)},body:JSON.stringify(request)});
  const next=await api(`/api/documents/${encodeURIComponent(id)}`);if(!editor.dirty){editor.snapshot=next;editor.serverChanged=false;}
 });
 action("[data-document-repair]", async () => {
  if(editor.dirty || editor.snapshot.freshness!=="current") return;
  const selected=[...dialog.querySelectorAll("[data-document-repair-issue]")].filter(node=>node.checked);
  const sections=[...new Set(selected.map(node=>node.dataset.section).filter(Boolean))];
  if(!selected.length||!sections.length)throw new Error(t("document.select_issues"));
  const epoch=editor.epoch;
  const before=editor.snapshot.body;
  const next=await api(`/api/documents/${encodeURIComponent(id)}/repair`,{method:"POST",headers:{"If-Match":String(editor.snapshot.head.generation)},body:JSON.stringify({revision_id:editor.snapshot.head.working_revision_id,report_id:editor.snapshot.report.report_id,issue_ids:selected.map(node=>node.dataset.documentRepairIssue),sections})});
  if(editor.epoch===epoch){showDocumentRepairDiff(editor,before,next.body);editor.serverChanged=false;editor.snapshot=next;dialog.querySelector("[data-document-body]").value=next.body;editor.operationID=null;}else{editor.serverChanged=true;}
 });
 renderDocumentStatus(editor);dialog.showModal();
}
async function documentEditorAction(editor,fn) {
 if(editor.busy)return;editor.busy=true;renderDocumentStatus(editor);
 try{await fn();}finally{editor.busy=false;renderDocumentStatus(editor);}
}
async function saveDocumentEditor(editor) {
 const epoch=editor.epoch;
 const contract=JSON.parse(JSON.stringify(editor.snapshot.contract));
 editor.dialog.querySelectorAll("[data-document-input]").forEach(input=>{contract.parameters[input.dataset.documentInput]=input.value;});
 for(const key of ["purpose","audience","language","title"])contract[key]=contract.parameters[key] || "";
 const body=editor.dialog.querySelector("[data-document-body]").value;
 const next=await api(`/api/documents/${encodeURIComponent(editor.id)}/revisions`,{method:"POST",headers:{"If-Match":String(editor.snapshot.head.generation)},body:JSON.stringify({body,contract,...documentSourceInputs(editor)})});
 editor.snapshot=next;editor.operationID=null;editor.serverChanged=false;
 editor.dialog.querySelectorAll("[data-document-source]").forEach(row=>{const id=row.querySelector("[data-source-field=source_id]").value.trim();const source=(next.source_manifest.sources || []).find(item=>item.source_id===id);if(source)row._source={...source};});
 if(editor.epoch===epoch)editor.dirty=false;
}
function renderDocumentStatus(editor) {
 if(!editor.dialog?.isConnected)return;
 const {snapshot,dialog}=editor;
 const current=editor.dirty || editor.serverChanged ? t("document.stale") : snapshot.freshness==="current" ? t(`document.${snapshot.report?.status || "not_run"}`) : t("document.not_run");
 dialog.querySelector("[data-document-status]").textContent=`${current} · ${snapshot.head.working_revision_id} · ${t("document.last_final")}: ${snapshot.head.finalized_revision_id || "—"} · ${snapshot.finalization ? t(`document.method.${snapshot.finalization.review_method}`) : ""} · ${t("document.source_state")}: ${t(`document.source.${snapshot.source_freshness || "snapshot"}`)}`;
 dialog.querySelector("[data-document-finalize]").disabled=!documentCanFinalize(editor);
 dialog.querySelector("[data-document-save]").disabled=editor.busy || editor.serverChanged;
 dialog.querySelector("[data-document-check]").disabled=editor.busy || editor.serverChanged;
 dialog.querySelector("[data-document-repair]").disabled=editor.busy||editor.dirty||snapshot.freshness!=="current";
 const draft=dialog.querySelector("[data-document-draft-export]");draft.href=`/api/documents/${encodeURIComponent(editor.id)}/revisions/${encodeURIComponent(snapshot.head.working_revision_id)}/export?mode=draft`;
 const final=dialog.querySelector("[data-document-final-export]");final.hidden=!snapshot.head.finalized_revision_id;
 final.href=`/api/documents/${encodeURIComponent(editor.id)}/revisions/${encodeURIComponent(snapshot.head.finalized_revision_id || "")}/export?mode=final`;
 // Keep entered confirmation reasons during busy/status updates.
 const container=dialog.querySelector("[data-document-issues]");const reportID=snapshot.report?.report_id || "";
 if(container.dataset.reportId===reportID)return;container.dataset.reportId=reportID;
 container.innerHTML=["structure","evidence","consistency","usability"].map(group=>`<section><h3>${escapeHTML(t(`document.group.${group}`))}</h3>${(snapshot.report?.issues || []).filter(issue=>issue.group===group).map(issue=>`<article class="tf-document-issue"><strong>${escapeHTML(issue.code)}</strong><p>${escapeHTML(issue.basis)}</p><blockquote>${escapeHTML(issue.evidence || "")}</blockquote><p>${escapeHTML(issue.suggestion || "")}</p><button class="mini-button" data-document-location="${Number(issue.line || 1)}">${escapeHTML(t("document.locate"))}</button><label><input type="checkbox" data-document-repair-issue="${escapeHTML(issue.id)}" data-section="${escapeHTML(issue.section || "")}">${escapeHTML(t("document.repair_select"))}</label>${issue.human_confirmation ? `<label><input type="checkbox" data-document-confirm="${escapeHTML(issue.id)}">${escapeHTML(t("document.confirm"))}</label><input data-document-reason="${escapeHTML(issue.id)}" placeholder="${escapeHTML(t("document.reason"))}">` : ""}</article>`).join("")}</section>`).join("");
 container.querySelectorAll("[data-document-location]").forEach(button=>button.addEventListener("click",()=>{const area=dialog.querySelector("[data-document-body]");const line=Number(button.dataset.documentLocation);const start=area.value.split("\n").slice(0,line-1).join("\n").length+(line>1?1:0);area.focus();area.setSelectionRange(start,area.value.indexOf("\n",start)<0?area.value.length:area.value.indexOf("\n",start));}));
}
function appendDocumentSource(editor,source) {
 const row=document.createElement("fieldset");row.dataset.documentSource=source.source_id || "";row._source={...source};
 row.innerHTML=`<label>${escapeHTML(t("document.source_id"))}<input data-source-field="source_id" value="${escapeHTML(source.source_id || "")}"></label><label>${escapeHTML(t("document.locator"))}<input data-source-field="locator" value="${escapeHTML(source.locator || "")}"></label><label>${escapeHTML(t("document.source_version"))}<input data-source-field="source_version" value="${escapeHTML(source.source_version || "")}"></label><label>${escapeHTML(t("document.evidence"))}<textarea data-source-evidence rows="3"></textarea></label>`;
 editor.dialog.querySelector("[data-document-sources]").appendChild(row);
}
function documentSourceInputs(editor) {
 const sources=[],evidence={};editor.dialog.querySelectorAll("[data-document-source]").forEach(row=>{const source={...row._source};row.querySelectorAll("[data-source-field]").forEach(input=>source[input.dataset.sourceField]=input.value.trim());const excerpt=row.querySelector("[data-source-evidence]").value;if(excerpt)evidence[source.source_id]=excerpt;sources.push(source);});
 return {source_manifest:{schema_version:1,sources},evidence};
}
function showDocumentRepairDiff(editor,before,after) {
 let node=editor.dialog.querySelector("[data-document-diff]");if(!node){node=document.createElement("details");node.dataset.documentDiff="";editor.dialog.appendChild(node);}
 node.innerHTML=`<summary>${escapeHTML(t("document.repair_diff"))}</summary><div class="tf-document-layout"><pre>${escapeHTML(before)}</pre><pre>${escapeHTML(after)}</pre></div>`;
}
function handleDocumentEvent(raw) {
 let event;try{event=typeof raw==="string"?JSON.parse(raw):raw;}catch{return;}
 const payload=event?.payload || event;const editor=documentEditor;
 if(!editor||payload.document_id!==editor.id||Number(payload.generation)<=editor.snapshot.head.generation)return;
 // A notification never replaces text being edited. Reopen to inspect the new HEAD.
 editor.serverChanged=true;renderDocumentStatus(editor);
}

async function importWorkspaceBackup(event) {
 const file=event.target.files?.[0];if(!file)return;
 if(file.size>512*1024*1024){toast(t("workspace.file_too_large"));return;}
 const operation=workspaceOperationID();const output=$("#workspace-recovery-result");
 output.textContent=`${t("workspace.loading")} · ${operation}`;
 try{const response=await fetch(`/api/topic-imports?operation_id=${encodeURIComponent(operation)}`,{method:"POST",body:file,headers:{"Content-Type":"application/zip"}});if(!response.ok)throw new Error(`${response.status}: ${await response.text()}`);const value=(await response.json()).data;await loadTopics();navigateHash(buildRouteHash("topics",{topicId:value.topic_id},{tab:"documents"}),{force:true});output.textContent=operation;}catch(error){output.textContent=`${operation}: ${error.message}`;}finally{event.target.value="";}
}
async function loadWorkspaceRecovery() {
 const output=$("#workspace-recovery-result");
 try{const operations=await api("/api/workspace/operations");const pending=operations.filter(op=>op.state!=="completed");output.innerHTML=pending.map(op=>`<p>${escapeHTML(op.kind)} · ${escapeHTML(op.state)} · ${escapeHTML(op.id)} · ${escapeHTML(op.error||"")}${["migration","import"].includes(op.kind)?`<button class="tf-btn" data-workspace-retry="${escapeHTML(op.id)}">${escapeHTML(t("workspace.retry"))}</button>`:""}</p>`).join("")||escapeHTML(t("workspace.none"));output.querySelectorAll("[data-workspace-retry]").forEach(button=>button.onclick=async()=>{try{await api(`/api/operations/${encodeURIComponent(button.dataset.workspaceRetry)}/retry`,{method:"POST"});await loadTopics();await loadWorkspaceRecovery();}catch(error){toast(error.message);}});}catch(error){output.textContent=error.message;}
}

function renderAggregateConversion(topic) {
 const root=$("#topic-document");
 const panel=document.createElement("details");panel.className="collection-management";
 panel.innerHTML=`<summary>${escapeHTML(t("workspace.preview_conversion"))}</summary><p>${escapeHTML(t("workspace.conversion_notice"))}</p><button class="tf-btn" data-preview-conversion>${escapeHTML(t("workspace.collection"))}</button><div data-conversion-plan></div>`;
 root.appendChild(panel);
 panel.querySelector("[data-preview-conversion]").onclick=async()=>{try{const plan=await api(`/api/topics/${encodeURIComponent(topic)}/mode-conversions/preview`,{method:"POST",body:JSON.stringify({mode:"collection"})});if(state.activeTopicId!==topic)return;panel.querySelector("[data-conversion-plan]").innerHTML=`<pre>${escapeHTML(JSON.stringify(plan,null,2))}</pre><button class="tf-btn" data-confirm-conversion>${escapeHTML(t("workspace.confirm_conversion"))}</button>`;panel.querySelector("[data-confirm-conversion]").onclick=async()=>{try{await api(`/api/topics/${encodeURIComponent(topic)}/mode-conversions`,{method:"POST",body:JSON.stringify({operation_id:workspaceOperationID(),preview_id:plan.preview_id,plan_hash:plan.plan_hash})});await reloadCurrentCollection(topic);}catch(error){toast(error.message);}};}catch(error){toast(error.message);}};
}

// Collection requests use a selection epoch in addition to the stable topic and
// entry IDs, so A -> B -> A cannot accept a response from the first A.
function workspaceOperationID() {
  return `op-${globalThis.crypto?.randomUUID?.() || `${Date.now()}-${Math.random().toString(16).slice(2)}`}`;
}
function resourceContentHref(value) {
  const id = String(value).slice("resource://".length);
  if (
    !/^[A-Za-z0-9][A-Za-z0-9_.-]{0,159}$/.test(id) ||
    !markdownResourceContext
  )
    return "";
  const { links, ...context } = markdownResourceContext;
  if (documentSetRenderContext?.member) {
 context.set_id=documentSetRenderContext.id;
 context.set_revision=documentSetRenderContext.revision;
 context.member_key=documentSetRenderContext.member;
 }
 const query = new URLSearchParams(context);
  return `/api/resources/${encodeURIComponent(id)}/content?${query}`;
}
function collectionLocalKey(topic) {
  return `thoughtflow:collection:${collectionWorkspace?.workspace_key || window.location.origin}:${topic}:v1`;
}
function collectionPreferences(topic) {
  try {
    return JSON.parse(
      window.localStorage.getItem(collectionLocalKey(topic)) || "{}",
    );
  } catch {
    return {};
  }
}
function saveCollectionPreference(topic, patch) {
  const old = collectionPreferences(topic);
  window.localStorage.setItem(
    collectionLocalKey(topic),
    JSON.stringify({ ...old, ...patch }),
  );
}
function collectionRoute(entry, view = "final", revision = "", section = "") {
  return buildRouteHash(
    "topics",
    { topicId: state.activeTopicId },
    {
      tab: "documents",
      entry,
      view,
      ...(revision ? { revision } : {}),
      ...(section ? { section } : {}),
    },
  );
}
async function openCollectionWorkspace(topic, topicEpoch) {
 if (state.route?.query?.references !== "1") {
  try { const set = await api(`/api/document-sets/${encodeURIComponent(topic)}`);
   if(topicEpoch !== topicRequestEpoch || state.route?.params?.topicId !== topic)return;
   if(set?.head?.working_set_revision){await renderDocumentSetWorkspace(topic,set,topicEpoch);return;}
  } catch(error) { if(error.status && error.status !== 404) throw error; }
 }

  const data = await api(`/api/topics/${encodeURIComponent(topic)}/catalog`);
  if (
    topicEpoch !== topicRequestEpoch ||
    state.route?.params?.topicId !== topic
  )
    return;
  collectionWorkspace = data;
  const entries = data.entries || [];
  let selected = state.route?.query?.entry;
  if (!selected) {
    const prefs = collectionPreferences(topic);
    selected = entries.some((v) => v.entry.id === prefs.entry)
      ? prefs.entry
      : entries[0]?.entry.id;
  }
  renderCollectionWorkspace(selected);
  if (selected)
    await readCollectionEntry(
      topic,
      selected,
      state.route?.query?.view || "final",
      state.route?.query?.revision || "",
    );
}
function collectionStatus(v) {
  return [
    v.binding_status,
    v.delivery_status,
    ...(v.has_new_draft && v.work_status !== "new_draft" ? ["new_draft"] : []),
    v.work_status,
    v.version_status,
    v.availability,
  ]
    .filter(Boolean)
    .map((key) => t(`workspace.status.${key}`))
    .join(" · ");
}
function collectionVisibleEntries() {
  const filter = ($("#collection-filter")?.value || "").trim().toLowerCase();
  const status = $("#collection-status")?.value || "";
  const prefs = collectionPreferences(state.activeTopicId);
  const list = $("#collection-list")?.value || "all";
  const filtered = (collectionWorkspace?.entries || []).filter(
    (v) =>
      (!filter ||
        `${v.entry.title} ${v.original_title || ""} ${v.entry.role} ${v.entry.binding.thought_id || ""}`
          .toLowerCase()
          .includes(filter)) &&
      (!status ||
        [
          v.binding_status,
          v.delivery_status,
          v.work_status,
          v.availability,
          ...(v.has_new_draft ? ["new_draft"] : []),
        ].includes(status)) &&
      (list === "all" || (prefs[list] || []).includes(v.entry.id)),
  );
  return (collectionWorkspace?.catalog?.groups || []).flatMap(group =>
    filtered.filter(v => v.entry.group_id === group.id));
}
function collectionReadableEntries(view) {
 return collectionVisibleEntries().filter(v =>
   v.entry.binding.kind !== "planned" && v.binding_status !== "missing" &&
   v.availability === "available" &&
   (view === "draft" || Boolean(v.selected_revision_id)));
}
function collectionCatalogHTML(selected) {
  const data = collectionWorkspace;
  const visible = collectionVisibleEntries();
  const prefs = collectionPreferences(state.activeTopicId);
  return data.catalog.groups
    .map(
      (group) =>
        `<details class="collection-group" data-group="${escapeHTML(group.id)}" ${prefs.collapsed?.includes(group.id) ? "" : "open"}><summary>${escapeHTML(group.title)}</summary>${visible
          .filter((v) => v.entry.group_id === group.id)
          .map(
            (v) =>
              `<a class="collection-entry ${v.entry.id === selected ? "active" : ""}" aria-current="${v.entry.id === selected ? "page" : "false"}" href="${escapeHTML(collectionRoute(v.entry.id))}" data-entry="${escapeHTML(v.entry.id)}"><strong>${v.entry.required ? "* " : ""}${escapeHTML(v.entry.title)}</strong><small>${escapeHTML(collectionStatus(v))}</small></a>`,
          )
          .join("")}</details>`,
    )
    .join("");
}
function renderCollectionWorkspace(selected) {
  const data = collectionWorkspace;
  const root = $("#topic-document");
  if (!data || !root) return;
  root.innerHTML = `<div class="collection-toolbar"><strong>${escapeHTML(data.topic.name)}</strong><span>${escapeHTML(t("workspace.progress", { done: data.delivered_count, total: data.required_count }))}</span><button class="tf-btn" id="collection-toggle" aria-expanded="true">${escapeHTML(t("workspace.catalog"))}</button></div>
 <div class="collection-reader"><nav class="collection-catalog" aria-label="${escapeHTML(t("workspace.catalog"))}"><label>${escapeHTML(t("workspace.filter"))}<input type="search" id="collection-filter" value="${escapeHTML(collectionPreferences(state.activeTopicId).filter || "")}"></label><label>${escapeHTML(t("workspace.status_filter"))}<select id="collection-status"><option value="">${escapeHTML(t("workspace.all"))}</option>${["planned", "missing", "ready_for_use", "new_draft", "unavailable"].map((k) => `<option value="${k}">${escapeHTML(t(`workspace.status.${k}`))}</option>`).join("")}</select></label><label>${escapeHTML(t("workspace.saved_navigation"))}<select id="collection-list">${["all", "favorites", "recent"].map((k) => `<option value="${k}">${escapeHTML(t(`workspace.${k}`))}</option>`).join("")}</select></label><button class="tf-btn collection-close" id="collection-close">${escapeHTML(t("common.close"))}</button><div id="collection-entries"></div></nav>
 <section class="collection-content"><div id="collection-controls"></div><div id="collection-body" role="region" aria-live="polite" tabindex="-1">${escapeHTML(t("workspace.select_entry"))}</div><div class="collection-navigation"><button class="tf-btn" id="collection-prev">${escapeHTML(t("workspace.previous"))}</button><button class="tf-btn" id="collection-next">${escapeHTML(t("workspace.next"))}</button></div></section></div>
 <details class="collection-search"><summary>${escapeHTML(t("workspace.search"))}</summary><form id="collection-search-form"><label>${escapeHTML(t("workspace.search_working"))}<input type="search" id="collection-search-query" required></label><button class="tf-btn">${escapeHTML(t("workspace.search"))}</button></form><div id="collection-search-results"></div></details>
 <details class="collection-management"><summary>${escapeHTML(t("workspace.manage"))}</summary><p>${escapeHTML(t("workspace.manage_hint"))}</p><div id="collection-editor"></div></details>`;
  $("#collection-status").value =
    collectionPreferences(state.activeTopicId).status || "";
  $("#collection-list").value =
    collectionPreferences(state.activeTopicId).list || "all";
  const renderNav = () => {
    saveCollectionPreference(state.activeTopicId, {
      filter: $("#collection-filter").value,
      status: $("#collection-status").value,
      list: $("#collection-list").value,
    });
    $("#collection-entries").innerHTML = collectionCatalogHTML(
      state.route?.query?.entry || selected,
    );
    $("#collection-entries")
      .querySelectorAll("details")
      .forEach((el) =>
        el.addEventListener("toggle", () => {
          const collapsed = [
            ...$("#collection-entries").querySelectorAll("details:not([open])"),
          ].map((g) => g.dataset.group);
          saveCollectionPreference(state.activeTopicId, { collapsed });
        }),
      );
  };
  renderNav();
  $("#collection-filter").addEventListener("input", renderNav);
  $("#collection-status").addEventListener("change", renderNav);
  $("#collection-list").addEventListener("change", renderNav);
  const nav = $(".collection-catalog");
  const narrow = window.matchMedia?.("(max-width:760px)").matches;
  const setNav = (open) => {
    nav.hidden = !open;
    $("#collection-toggle").setAttribute("aria-expanded", String(open));
    if (narrow) {
      nav.setAttribute("role", "dialog");
      nav.setAttribute("aria-modal", String(open));
    }
    if (open) $("#collection-filter").focus();
    else $("#collection-toggle").focus();
  };
  nav.hidden = Boolean(narrow);
  $("#collection-toggle").setAttribute("aria-expanded", String(!nav.hidden));
  $("#collection-toggle").onclick = () => setNav(nav.hidden);
  $("#collection-close").onclick = () => setNav(false);
  nav.addEventListener("keydown", (e) => {
    if (e.key === "Escape") {
      e.preventDefault();
      setNav(false);
    }
    if (narrow && e.key === "Tab") {
      const focusable = [
        ...nav.querySelectorAll("button,input,select,a[href],summary"),
      ].filter((el) => el.getClientRects().length);
      const first = focusable[0],
        last = focusable.at(-1);
      if (e.shiftKey && document.activeElement === first) {
        e.preventDefault();
        last?.focus();
      } else if (!e.shiftKey && document.activeElement === last) {
        e.preventDefault();
        first?.focus();
      }
    }
  });
  $("#collection-prev").onclick = () => navigateCollection(-1);
  $("#collection-next").onclick = () => navigateCollection(1);
  $("#collection-search-form").onsubmit = searchCollection;
  renderCollectionEditor();
}
function navigateCollection(offset) {
  const view = state.route?.query?.view || "final";
  const visible = collectionReadableEntries(view);
  const selected =
    state.route?.query?.entry ||
    collectionPreferences(state.activeTopicId).entry;
  const index = visible.findIndex((v) => v.entry.id === selected);
  const next = visible[index + offset];
  if (next) navigateHash(collectionRoute(next.entry.id, view), { force: true });
}
async function readCollectionEntry(topic, entry, view, revision) {
  const epoch = ++collectionReadEpoch;
  let resolvedRevision = revision;
  const topicEpoch = topicRequestEpoch;
  const item = collectionWorkspace?.entries.find((v) => v.entry.id === entry);
  const body = $("#collection-body");
  if (!body) return;
  if (!item) {
    body.textContent = t("workspace.entry_missing");
    return;
  }
  const token = () =>
    epoch === collectionReadEpoch &&
    topicEpoch === topicRequestEpoch &&
    state.route?.params?.topicId === topic;
  const controls = $("#collection-controls");
  controls.innerHTML = `<h2>${escapeHTML(item.entry.title)}</h2><p>${escapeHTML(collectionStatus(item))}</p><div class="collection-actions"><a class="tf-btn" href="${escapeHTML(collectionRoute(entry, "final"))}">${escapeHTML(t("workspace.final"))}</a><a class="tf-btn" href="${escapeHTML(collectionRoute(entry, "draft"))}">${escapeHTML(t("workspace.draft"))}</a><button class="tf-btn" id="collection-copy">${escapeHTML(t("workspace.copy_link"))}</button>${item.entry.binding.kind === "thought" ? `<button class="tf-btn" id="collection-edit-document">${escapeHTML(t("document.open"))}</button>` : ""}<button class="tf-btn" id="collection-favorite">${escapeHTML(t("workspace.favorite"))}</button></div>`;
  if ($("#collection-edit-document"))
    $("#collection-edit-document").onclick = () =>
      openDocumentEditor(item.entry.binding.thought_id).catch((error) =>
        toast(error.message),
      );
  $("#collection-copy").disabled = true;
  $("#collection-copy").onclick = () =>
    token() && resolvedRevision && copyText(
      `${window.location.origin}${window.location.pathname || "/"}${collectionRoute(entry, view, resolvedRevision)}`,
    );
  $("#collection-favorite").onclick = () => {
    const prefs = collectionPreferences(topic);
    const favorites = new Set(prefs.favorites || []);
    if (favorites.has(entry)) favorites.delete(entry);
    else favorites.add(entry);
    saveCollectionPreference(topic, { favorites: [...favorites] });
    $("#collection-favorite").setAttribute(
      "aria-pressed",
      String(favorites.has(entry)),
    );
  };
  body.textContent = t("workspace.loading");
  try {
    const snap = await api(
      `/api/topics/${encodeURIComponent(topic)}/entries/${encodeURIComponent(entry)}/content?${new URLSearchParams({ view, ...(revision ? { revision } : {}) })}`,
    );
    if (!token()) return;
    resolvedRevision = snap.revision?.revision_id || "";
    $("#collection-copy").disabled = !resolvedRevision;
    markdownResourceContext =
      item.entry.binding.kind === "snapshot"
        ? { topic_id: topic, snapshot_id: item.entry.binding.snapshot_id }
        : { object_id: item.entry.binding.thought_id };
    markdownResourceContext.links = Object.fromEntries(
      (snap.resource_bindings || [])
        .filter((b) => !b.private && b.original_locator)
        .map((b) => [b.original_locator, b.resource_id]),
    );
    const html = renderMarkdown(snap.body || "");
    markdownResourceContext = null;
    body.innerHTML = `<p class="collection-version">${escapeHTML(t(view === "draft" ? "workspace.working_version" : "workspace.final_version"))} · ${escapeHTML(snap.revision?.revision_id || t("workspace.status.ordinary_note"))}</p>${snap.offline_status === "partial" ? `<p role="status">${escapeHTML(t("workspace.offline_partial"))}</p>` : ""}<div class="markdown-rendered">${html}</div><details><summary>${escapeHTML(t("workspace.information"))}</summary><dl><dt>${escapeHTML(t("workspace.original_title"))}</dt><dd>${escapeHTML(item.original_title || "")}</dd><dt>${escapeHTML(t("workspace.profile"))}</dt><dd>${escapeHTML(item.profile?.profile_id || "")}</dd><dt>${escapeHTML(t("workspace.location"))}</dt><dd>${escapeHTML(item.location?.placement?.thought_path || "")}</dd><dt>${escapeHTML(t("workspace.revision"))}</dt><dd>${escapeHTML(snap.revision?.revision_id || "")}</dd><dt>${escapeHTML(t("workspace.validation"))}</dt><dd>${escapeHTML(item.validation_status)}</dd></dl><form id="collection-version-form"><label>${escapeHTML(t("workspace.revision"))}<input id="collection-version-id" value="${escapeHTML(resolvedRevision)}"></label><button class="tf-btn">${escapeHTML(t("workspace.open_version"))}</button></form></details><p class="topic-meta">${escapeHTML(t("workspace.local_link_notice"))}</p>`;
    $("#collection-version-form").onsubmit = (e) => {
      e.preventDefault();
      navigateHash(
        collectionRoute(entry, view, $("#collection-version-id").value.trim()),
        { force: true },
      );
    };
    saveCollectionPreference(topic, {
      entry,
      view,
      revision: resolvedRevision,
      recent: [
        entry,
        ...(collectionPreferences(topic).recent || []).filter(
          (v) => v !== entry,
        ),
      ].slice(0, 20),
    });

    const headingIDs = new Map();
    body.querySelectorAll("h1,h2,h3,h4,h5,h6").forEach((heading) => {
      const base =
        heading.textContent
          .trim()
          .toLowerCase()
          .replace(/[^\p{L}\p{N} _-]/gu, "")
          .replace(/[ _]+/g, "-") || "section";
      const n = headingIDs.get(base) || 0;
      headingIDs.set(base, n + 1);
      heading.id = n ? `${base}-${n}` : base;
    });
    const headings = [...body.querySelectorAll("h1,h2,h3,h4,h5,h6")];
    if (headings.length) {
      const toc = document.createElement("details");
      toc.className = "collection-toc";
      toc.innerHTML = `<summary>${escapeHTML(t("workspace.sections"))}</summary>${headings.map((h) => `<p><a href="${escapeHTML(collectionRoute(entry, view, resolvedRevision, h.id))}">${escapeHTML(h.textContent)}</a> <button class="tf-btn" data-copy-section="${escapeHTML(h.id)}">${escapeHTML(t("workspace.copy_link"))}</button></p>`).join("")}`;
      body.prepend(toc);
      toc
        .querySelectorAll("[data-copy-section]")
        .forEach(
          (button) =>
            (button.onclick = () =>
              copyText(
                `${window.location.origin}${window.location.pathname || "/"}${collectionRoute(entry, view, resolvedRevision, button.dataset.copySection)}`,
              )),
        );
    }
    const section = state.route?.query?.section;
    if (section) {
      const target = [...body.querySelectorAll("[id]")].find(
        (el) => el.id === section,
      );
      target?.scrollIntoView();
    }
    body.querySelectorAll('a[href^="#"]').forEach((link) =>
      link.addEventListener("click", (e) => {
        const href = link.getAttribute("href");
        if (!token() || href.startsWith("#/")) return;
        e.preventDefault();
        let section = href.slice(1);
        try { section = decodeURIComponent(section); } catch {}
        navigateHash(
          collectionRoute(
            entry,
            view,
            resolvedRevision,
            section,
          ),
          { force: true },
        );
      }),
    );
  } catch (error) {
    if (token()) {
      body.innerHTML = `<div class="tf-empty" role="alert">${escapeHTML(t(view === "final" ? "workspace.final_unavailable" : "workspace.load_failed"))}<p>${escapeHTML(error.message)}</p></div>`;
    }
  }
}
async function searchCollection(event) {
  event.preventDefault();
  const topic = state.activeTopicId;
  const epoch = ++collectionSearchEpoch;
  const selection = collectionReadEpoch;
  const topicEpoch = topicRequestEpoch;
  const node = $("#collection-search-results");
  const current = () => topic === state.activeTopicId && epoch === collectionSearchEpoch && selection === collectionReadEpoch && topicEpoch === topicRequestEpoch && node === $("#collection-search-results");
  try {
  const result = await api(
    `/api/search?${new URLSearchParams({ scope: "topic", topic_id: topic, q: $("#collection-search-query").value, limit: "100" })}`,
  );
  if (topic !== state.activeTopicId || epoch !== collectionSearchEpoch || selection !== collectionReadEpoch || topicEpoch !== topicRequestEpoch || node !== $("#collection-search-results")) return;
  $("#collection-search-results").innerHTML =
    (result.results || [])
      .map((hit) => {
        const entry = collectionWorkspace.entries.find(
          (v) => hit.snapshot_id ? v.entry.id === hit.entry_id && v.entry.binding.snapshot_id === hit.snapshot_id : v.entry.binding.thought_id === hit.thought_id,
        );
        return `<article><strong>${escapeHTML(hit.title)}</strong> · ${escapeHTML(t(hit.version_kind === "snapshot" ? "workspace.snapshot_version" : "workspace.working_version"))}<p>${escapeHTML(hit.snippet)}</p>${entry ? `<a href="${escapeHTML(collectionRoute(entry.entry.id, hit.version_kind === "snapshot" ? "final" : "draft", hit.indexed_revision_id || ""))}">${escapeHTML(t("workspace.open_document"))}</a>` : ""}</article>`;
      })
      .join("") || escapeHTML(t("workspace.no_results"));
  } catch (error) { if (current() && node) node.textContent = error.message; }

}
function renderCollectionEditor() {
  const data = collectionWorkspace;
  const editor = $("#collection-editor");
  if (!editor) return;
  const groupOptions = data.catalog.groups
    .map(
      (g) =>
        `<option value="${escapeHTML(g.id)}">${escapeHTML(g.title)}</option>`,
    )
    .join("");
  editor.innerHTML = `<form id="collection-settings"><label>${escapeHTML(t("workspace.name"))}<input name="name" required value="${escapeHTML(data.topic.name)}"></label><label>${escapeHTML(t("workspace.description"))}<textarea name="description">${escapeHTML(data.topic.description || "")}</textarea></label><label><input name="topic_global_visible" type="checkbox" ${data.topic.search_policy?.topic_global_visible ? "checked" : ""}>${escapeHTML(t("workspace.topic_global"))}</label><label><input name="owned_default" type="checkbox" ${data.topic.search_policy?.owned_thoughts_global_default ? "checked" : ""}>${escapeHTML(t("workspace.owned_global"))}</label><p>${escapeHTML(t("workspace.policy_notice"))}</p><button class="tf-btn">${escapeHTML(t("workspace.save"))}</button></form>
 <form id="collection-threshold"><label>${escapeHTML(t("workspace.delivery_threshold"))}<select name="level">${["proposal", "ready_for_use"].map((k) => `<option value="${k}" ${data.catalog.required_delivery_level === k ? "selected" : ""}>${escapeHTML(t(`workspace.status.${k}`))}</option>`).join("")}</select></label><button class="tf-btn">${escapeHTML(t("workspace.save"))}</button></form>
 <div>${data.catalog.groups.map((g) => `<form data-edit-group="${escapeHTML(g.id)}"><label>${escapeHTML(t("workspace.group"))}<input name="title" required value="${escapeHTML(g.title)}"></label><button class="tf-btn">${escapeHTML(t("workspace.save"))}</button><button type="button" class="tf-btn" data-group-move="-1">↑</button><button type="button" class="tf-btn" data-group-move="1">↓</button><button type="button" class="tf-btn" data-group-remove>${escapeHTML(t("workspace.remove_group"))}</button></form>`).join("")}</div>
 <form id="collection-group-add"><label>${escapeHTML(t("workspace.group"))}<input name="title" required></label><button class="tf-btn">${escapeHTML(t("workspace.add_group"))}</button></form>
 <form id="collection-entry-add"><label>${escapeHTML(t("workspace.name"))}<input name="title" required></label><label>${escapeHTML(t("workspace.group"))}<select name="group">${groupOptions}</select></label><label>${escapeHTML(t("workspace.thought_id_optional"))}<input name="thought_id"></label><label><input type="checkbox" name="required">${escapeHTML(t("workspace.required"))}</label><button class="tf-btn">${escapeHTML(t("workspace.add_entry"))}</button></form>
 <div class="collection-edit-entries">${data.catalog.entries.map((entry) => `<form class="collection-edit-entry" data-entry-id="${escapeHTML(entry.id)}"><input name="title" aria-label="${escapeHTML(t("workspace.name"))}" value="${escapeHTML(entry.title)}" required><input name="role" aria-label="${escapeHTML(t("workspace.role"))}" value="${escapeHTML(entry.role || "document")}"><select name="group" aria-label="${escapeHTML(t("workspace.group"))}">${data.catalog.groups.map((g) => `<option value="${escapeHTML(g.id)}" ${g.id === entry.group_id ? "selected" : ""}>${escapeHTML(g.title)}</option>`).join("")}</select><label><input name="required" type="checkbox" ${entry.required ? "checked" : ""}>${escapeHTML(t("workspace.required"))}</label><button class="tf-btn">${escapeHTML(t("workspace.save"))}</button><button class="tf-btn" type="button" data-move="-1">↑</button><button class="tf-btn" type="button" data-move="1">↓</button><button class="tf-btn" type="button" data-remove>${escapeHTML(t("workspace.unlink"))}</button>${entry.binding.kind === "thought" ? `<label>${escapeHTML(t("workspace.version_policy"))}<select name="version_policy"><option value="latest_finalized">${escapeHTML(t("workspace.latest_final"))}</option><option value="pinned_finalized" ${entry.binding.version_policy === "pinned_finalized" ? "selected" : ""}>${escapeHTML(t("workspace.pinned_final"))}</option></select></label><input name="revision_id" aria-label="${escapeHTML(t("workspace.revision"))}" value="${escapeHTML(entry.binding.revision_id || "")}"><input name="finalization_id" aria-label="${escapeHTML(t("workspace.finalization"))}" value="${escapeHTML(entry.binding.finalization_id || "")}">` : ""}</form>`).join("")}</div>
 <form id="collection-draft"><label>${escapeHTML(t("workspace.planned_entry"))}<select name="entry">${data.catalog.entries
   .filter((e) => e.binding.kind === "planned")
   .map(
     (e) =>
       `<option value="${escapeHTML(e.id)}">${escapeHTML(e.title)}</option>`,
   )
   .join(
     "",
   )}</select></label><label>${escapeHTML(t("workspace.profile"))}<select name="profile">${renderDocumentProfileOptions()}</select></label><div data-role-inputs></div><label>${escapeHTML(t("workspace.initial_body"))}<textarea name="content" required></textarea></label><button class="tf-btn">${escapeHTML(t("workspace.create_draft"))}</button></form>
 <h4>${escapeHTML(t("workspace.uncatalogued"))}</h4><div>${data.uncatalogued.map((l) => `<p>${escapeHTML(l.object_id)} · ${escapeHTML(l.placement.thought_path)}<button class="tf-btn" data-bind-owned="${escapeHTML(l.object_id)}">${escapeHTML(t("workspace.bind"))}</button></p>`).join("") || escapeHTML(t("workspace.none"))}</div>
 <h4>${escapeHTML(t("workspace.deleted"))}</h4><p>${escapeHTML(t("workspace.restore_notice"))}</p>${(data.deleted||[]).map(l=>`<p>${escapeHTML(l.object_id)} <button class="tf-btn" data-restore-thought="${escapeHTML(l.object_id)}" data-generation="${l.generation}">${escapeHTML(t("workspace.restore"))}</button></p>`).join("")}
 <form id="collection-placement"><label>${escapeHTML(t("workspace.thought_id"))}<input name="thought_id" required></label><label>${escapeHTML(t("workspace.search_policy"))}<select name="global_search">${["inherit", "include", "exclude"].map((k) => `<option value="${k}">${escapeHTML(t(`workspace.${k}`))}</option>`).join("")}</select></label><button class="tf-btn">${escapeHTML(t("workspace.save_policy"))}</button></form>
 <form id="collection-migrate"><label>${escapeHTML(t("workspace.thought_id"))}<input name="thought_id" required></label><label>${escapeHTML(t("workspace.target_topic"))}<input name="target" value="${escapeHTML(state.activeTopicId)}"></label><button class="tf-btn">${escapeHTML(t("workspace.preview_migration"))}</button></form><div id="collection-migration-preview"></div>
 <form id="collection-resource"><label>${escapeHTML(t("workspace.thought_id"))}<input name="thought_id" required></label><label>${escapeHTML(t("workspace.attachment"))}<input type="file" name="file" accept=".png,.jpg,.jpeg,.gif,.webp,.pdf,.txt,.md" required></label><button class="tf-btn">${escapeHTML(t("workspace.upload"))}</button></form><div id="collection-upload-result"></div>
 <form id="collection-snapshot"><label>${escapeHTML(t("workspace.thought_id"))}<input name="thought_id" required></label><label>${escapeHTML(t("workspace.revision"))}<input name="revision" required></label><label>${escapeHTML(t("workspace.finalization"))}<input name="finalization" required></label><label>${escapeHTML(t("workspace.name"))}<input name="title" required></label><button class="tf-btn">${escapeHTML(t("workspace.create_snapshot"))}</button></form>
 <form id="collection-convert"><select name="mode"><option value="aggregate">${escapeHTML(t("workspace.aggregate"))}</option></select><button class="tf-btn">${escapeHTML(t("workspace.preview_conversion"))}</button></form><div id="collection-convert-preview"></div>
 <button class="tf-btn" id="collection-activity">${escapeHTML(t("workspace.activity"))}</button><div id="collection-activity-list"></div><button class="tf-btn" id="collection-delete">${escapeHTML(t("workspace.delete_empty"))}</button>
 <div class="collection-actions"><button class="tf-btn" data-export-kind="delivery">${escapeHTML(t("workspace.export_delivery"))}</button><button class="tf-btn" data-export-kind="backup">${escapeHTML(t("workspace.export_backup"))}</button><label><input id="collection-private" type="checkbox">${escapeHTML(t("workspace.include_private"))}</label><button class="tf-btn" id="collection-rebuild">${escapeHTML(t("workspace.rebuild"))}</button></div><div id="collection-export-result"></div><form id="collection-import"><label>${escapeHTML(t("workspace.import_backup"))}<input name="file" type="file" accept=".zip" required></label><button class="tf-btn">${escapeHTML(t("workspace.import_backup"))}</button></form><div id="collection-management-error" role="alert"></div>`;
  const topic = state.activeTopicId;
  const actionRequests = new Map();
  const action = (fn) => async (event) => {
    event.preventDefault();
    const epoch = topicRequestEpoch, selection = collectionReadEpoch;
    const target = event.currentTarget || event.target;
    const key = target?.dataset?.exportKind ? "export" : target?.id || target;
    const request = (actionRequests.get(key) || 0) + 1;
    actionRequests.set(key, request);
    const current = () => topic === state.activeTopicId &&
      epoch === topicRequestEpoch && selection === collectionReadEpoch &&
      editor === $("#collection-editor") && actionRequests.get(key) === request;
    if (!current()) return;
    const scopedAPI = async (...args) => {
      if (!current()) throw new Error("stale_workspace_action");
      const value = await api(...args);
      if (!current()) throw new Error("stale_workspace_action");
      return value;
    };
    try {
      await fn(event, scopedAPI, current);
    } catch (error) {
      if (current()) $("#collection-management-error").textContent = error.message;
    }
  };
  $("#collection-threshold").onsubmit = action(async (e, api, current) => {
    const c = structuredClone(data.catalog);
    c.required_delivery_level = e.target.elements.level.value;
    await saveCollectionCatalog(c, topic, api);
  });
  editor.querySelectorAll("[data-edit-group]").forEach((form) => {
    form.onsubmit = action(async (_event, api, current) => {
      const c = structuredClone(data.catalog);
      c.groups.find((g) => g.id === form.dataset.editGroup).title =
        form.elements.title.value;
      await saveCollectionCatalog(c, topic, api);
    });
    form.querySelectorAll("[data-group-move]").forEach(
      (button) =>
        (button.onclick = action(async (_event, api, current) => {
          const c = structuredClone(data.catalog);
          const i = c.groups.findIndex((g) => g.id === form.dataset.editGroup),
            j = i + Number(button.dataset.groupMove);
          if (j < 0 || j >= c.groups.length) return;
          [c.groups[i], c.groups[j]] = [c.groups[j], c.groups[i]];
          await saveCollectionCatalog(c, topic, api);
        })),
    );
    form.querySelector("[data-group-remove]").onclick = action(async (_event, api, current) => {
      const c = structuredClone(data.catalog);
      if (c.entries.some((e) => e.group_id === form.dataset.editGroup))
        throw new Error(t("workspace.group_not_empty"));
      c.groups = c.groups.filter((g) => g.id !== form.dataset.editGroup);
      await saveCollectionCatalog(c, topic, api);
    });
  });
  $("#collection-snapshot").onsubmit = action(async (e, api, current) => {
    const f = e.target;
    const snap = await api(
      `/api/topics/${encodeURIComponent(topic)}/snapshots`,
      {
        method: "POST",
        body: JSON.stringify({
          operation_id: workspaceOperationID(),
          thought_id: f.elements.thought_id.value.trim(),
          revision_id: f.elements.revision.value.trim(),
          finalization_id: f.elements.finalization.value.trim(),
        }),
      },
    );
    const c = structuredClone(data.catalog);
    c.entries.push({
      id: workspaceOperationID(),
      group_id: c.groups[0].id,
      title: f.elements.title.value,
      role: "reference",
      required: false,
      binding: { kind: "snapshot", snapshot_id: snap.id },
    });
    await saveCollectionCatalog(c, topic, api);
  });
  $("#collection-convert").onsubmit = action(async (e, api, current) => {
    const plan = await api(
      `/api/topics/${encodeURIComponent(topic)}/mode-conversions/preview`,
      {
        method: "POST",
        body: JSON.stringify({ mode: e.target.elements.mode.value }),
      },
    );
    $("#collection-convert-preview").innerHTML =
      `<p>${escapeHTML(t("workspace.conversion_notice"))}</p><pre>${escapeHTML(JSON.stringify(plan, null, 2))}</pre><button class="tf-btn" id="collection-convert-confirm">${escapeHTML(t("workspace.confirm_conversion"))}</button>`;
    $("#collection-convert-confirm").onclick = action(async (_event, api, current) => {
      await api(`/api/topics/${encodeURIComponent(topic)}/mode-conversions`, {
        method: "POST",
        body: JSON.stringify({
          operation_id: workspaceOperationID(),
          preview_id: plan.preview_id,
          plan_hash: plan.plan_hash,
        }),
      });
      await reloadCurrentCollection(topic);
    });
  });
  $("#collection-activity").onclick = action(async (_event, api, current) => {
    const activities = await api(
      `/api/topics/${encodeURIComponent(topic)}/activity`,
    );
    $("#collection-activity-list").innerHTML = activities
      .map(
        (op) =>
          `<p>${escapeHTML(op.kind)} · ${escapeHTML(op.state)} · ${escapeHTML(op.id)} ${escapeHTML(op.error || "")}${op.state === "failed" ? `<button class="tf-btn" data-retry-operation="${escapeHTML(op.id)}">${escapeHTML(t("workspace.retry"))}</button>` : ""}</p>`,
      )
      .join("");
    $("#collection-activity-list")
      .querySelectorAll("[data-retry-operation]")
      .forEach(
        (button) =>
          (button.onclick = action(async (_event, api, current) => {
            await api(
              `/api/operations/${encodeURIComponent(button.dataset.retryOperation)}/retry`,
              { method: "POST" },
            );
            await reloadCurrentCollection(topic);
          })),
      );
  });
  $("#collection-delete").onclick = action(async (_event, api, current) => {
    await api(`/api/topics/${encodeURIComponent(topic)}`, {
      method: "DELETE",
      body: JSON.stringify({
        operation_id: workspaceOperationID(),
        expected_generation: data.topic.generation,
        expected_catalog_generation: data.catalog.generation,
      }),
    });
    navigateHash("#/topics", { force: true });
  });
  $("#collection-settings").onsubmit = action(async (e, api, current) => {
    const f = e.target;
    await api(`/api/topics/${encodeURIComponent(topic)}/settings`, {
      method: "PATCH",
      body: JSON.stringify({
        operation_id: workspaceOperationID(),
        expected_generation: data.topic.generation,
        name: f.elements.name.value,
        description: f.elements.description.value,
        search_policy: {
          topic_global_visible: f.elements.topic_global_visible.checked,
          owned_thoughts_global_default: f.elements.owned_default.checked,
        },
      }),
    });
    await reloadCurrentCollection(topic);
  });
  $("#collection-group-add").onsubmit = action(async (e, api, current) => {
    const c = structuredClone(data.catalog);
    c.groups.push({
      id: workspaceOperationID(),
      title: e.target.elements.title.value,
    });
    await saveCollectionCatalog(c, topic, api);
  });
  $("#collection-entry-add").onsubmit = action(async (e, api, current) => {
    const f = e.target;
    const c = structuredClone(data.catalog);
    const id = f.elements.thought_id.value.trim();
    c.entries.push({
      id: workspaceOperationID(),
      group_id: f.elements.group.value,
      title: f.elements.title.value,
      required: f.elements.required.checked,
      role: "document",
      binding: id
        ? {
            kind: "thought",
            thought_id: id,
            version_policy: "latest_finalized",
          }
        : { kind: "planned" },
    });
    await saveCollectionCatalog(c, topic, api);
  });
  editor.querySelectorAll(".collection-edit-entry").forEach((form) => {
    form.onsubmit = action(async (_event, api, current) => {
      const c = structuredClone(data.catalog);
      const entry = c.entries.find((v) => v.id === form.dataset.entryId);
      entry.title = form.elements.title.value;
      entry.role = form.elements.role.value;
      entry.group_id = form.elements.group.value;
      entry.required = form.elements.required.checked;
      if (entry.binding.kind === "thought") {
        entry.binding.version_policy = form.elements.version_policy.value;
        entry.binding.revision_id = form.elements.revision_id.value;
        entry.binding.finalization_id = form.elements.finalization_id.value;
        if (entry.binding.version_policy === "latest_finalized") {
          delete entry.binding.revision_id;
          delete entry.binding.finalization_id;
        }
      }
      await saveCollectionCatalog(c, topic, api);
    });
    form.querySelectorAll("[data-move]").forEach(
      (button) =>
        (button.onclick = action(async (_event, api, current) => {
          const c = structuredClone(data.catalog);
          const i = c.entries.findIndex((v) => v.id === form.dataset.entryId);
          const j = i + Number(button.dataset.move);
          if (j < 0 || j >= c.entries.length) return;
          [c.entries[i], c.entries[j]] = [c.entries[j], c.entries[i]];
          await saveCollectionCatalog(c, topic, api);
        })),
    );
    form.querySelector("[data-remove]").onclick = action(async (_event, api, current) => {
      const c = structuredClone(data.catalog);
      c.entries = c.entries.filter((v) => v.id !== form.dataset.entryId);
      await saveCollectionCatalog(c, topic, api);
    });
  });
  editor.querySelectorAll("[data-bind-owned]").forEach(
    (button) =>
      (button.onclick = action(async (_event, api, current) => {
        const c = structuredClone(data.catalog);
        c.entries.push({
          id: workspaceOperationID(),
          group_id: c.groups[0].id,
          title: button.dataset.bindOwned,
          role: "document",
          required: false,
          binding: {
            kind: "thought",
            thought_id: button.dataset.bindOwned,
            version_policy: "latest_finalized",
          },
        });
        await saveCollectionCatalog(c, topic, api);
      })),
  );
  editor.querySelectorAll("[data-restore-thought]").forEach(button=>button.onclick=action(async(_event, api, current)=>{await api(`/api/thoughts/${encodeURIComponent(button.dataset.restoreThought)}/restore`,{method:"POST",body:JSON.stringify({operation_id:workspaceOperationID(),expected_generation:Number(button.dataset.generation)})});await reloadCurrentCollection(topic);}));
  $("#collection-placement").onsubmit = action(async (e, api, current) => {
    const f = e.target;
    const id = f.elements.thought_id.value.trim();
    const l = await api(`/api/thoughts/${encodeURIComponent(id)}/placement`);
    await api(`/api/thoughts/${encodeURIComponent(id)}/search-policy`, {
      method: "PATCH",
      body: JSON.stringify({
        operation_id: workspaceOperationID(),
        expected_generation: l.generation,
        global_search: f.elements.global_search.value,
      }),
    });
    await reloadCurrentCollection(topic);
  });
  $("#collection-migrate").onsubmit = action(async (e, api, current) => {
    const f = e.target;
    const p = await api(
      `/api/topics/${encodeURIComponent(topic)}/migrations/preview`,
      {
        method: "POST",
        body: JSON.stringify({
          object_id: f.elements.thought_id.value.trim(),
          target_topic_id: f.elements.target.value.trim(),
        }),
      },
    );
    const node = $("#collection-migration-preview");
    node.innerHTML = `<p>${escapeHTML(p.source.placement.thought_path)} → ${escapeHTML(p.target.placement.thought_path)}</p><p>${escapeHTML(t("workspace.global_change", { before: String(p.before_global), after: String(p.after_global) }))}</p><p>${p.total_size} bytes · ${p.files.length} files</p><pre>${escapeHTML(p.files.map((file) => file.path).join("\n"))}</pre><button class="tf-btn" id="collection-confirm-migration">${escapeHTML(t("workspace.confirm_migration"))}</button>`;
    $("#collection-confirm-migration").disabled = Boolean(
      p.conflicts?.length || p.missing?.length,
    );
    $("#collection-confirm-migration").onclick = action(async (_event, api, current) => {
      await api(`/api/topics/${encodeURIComponent(topic)}/migrations`, {
        method: "POST",
        body: JSON.stringify({
          operation_id: workspaceOperationID(),
          preview_id: p.id,
          plan_hash: p.plan_hash,
          expected_object_generation: p.source.generation,
        }),
      });
      await reloadCurrentCollection(topic);
    });
  });
  $("#collection-resource").onsubmit = action(async (e, api, current) => {
    const f = e.target;
    const id = f.elements.thought_id.value.trim();
    const file = f.elements.file.files[0];
    if (file.size > 32 * 1024 * 1024)
      throw new Error(t("workspace.file_too_large"));
    const l = await api(`/api/thoughts/${encodeURIComponent(id)}/placement`);
    const bytes = new Uint8Array(await file.arrayBuffer());
    let binary = "";
    for (let i = 0; i < bytes.length; i += 8192)
      binary += String.fromCharCode(...bytes.subarray(i, i + 8192));
    const resource = await api(
      `/api/topics/${encodeURIComponent(topic)}/resources`,
      {
        method: "POST",
        body: JSON.stringify({
          operation_id: workspaceOperationID(),
          object_id: id,
          expected_generation: l.generation,
          name: file.name,
          content: btoa(binary),
        }),
      },
    );
    $("#collection-upload-result").textContent = `resource://${resource.id}`;
  });
  editor.querySelectorAll("[data-export-kind]").forEach(
    (button) =>
      (button.onclick = action(async (_event, api, current) => {
        const result = await api(
          `/api/topics/${encodeURIComponent(topic)}/exports`,
          {
            method: "POST",
            body: JSON.stringify({
              operation_id: workspaceOperationID(),
              export_kind: button.dataset.exportKind,
              include_private_evidence:
                button.dataset.exportKind === "backup" &&
                $("#collection-private").checked,
            }),
          },
        );
        $("#collection-export-result").innerHTML =
          `<a href="${escapeHTML(result.download_url)}" download>${escapeHTML(t("workspace.download"))}</a> · ${escapeHTML(result.manifest.evidence_review)}${result.manifest.offline_status === "partial" ? `<p role="status">${escapeHTML(t("workspace.offline_partial"))}</p>` : ""}`;
      })),
  );
  $("#collection-rebuild").onclick = action(async (_event, api, current) => {
    await api(`/api/topics/${encodeURIComponent(topic)}/index/rebuild`, {
      method: "POST",
    });
    await reloadCurrentCollection(topic);
  });
  $("#collection-import").onsubmit = action(async (e, api, current) => {
    const file = e.target.elements.file.files[0];
    const response = await fetch(
      `/api/topic-imports?operation_id=${encodeURIComponent(workspaceOperationID())}`,
      {
        method: "POST",
        body: file,
        headers: { "Content-Type": "application/zip" },
      },
    );
    if (!current()) return;
    if (!response.ok) throw new Error(await response.text());
    if (!current()) return;
    await loadTopics();
  });
  const roleForm=$("#collection-draft");const updateRoleInputs=()=>{const selected=roleForm.elements.profile.selectedOptions[0];const p=profileByID(roleForm.elements.profile.value,Number(selected?.dataset.version));const root=roleForm.querySelector("[data-role-inputs]");if(root)root.innerHTML=(p?.inputs||[]).map(input=>`<label>${escapeHTML(input.label||input.key)}<textarea data-role-input="${escapeHTML(input.key)}" ${input.required?"required":""}></textarea></label>`).join("");};if(roleForm?.elements?.profile){roleForm.elements.profile.onchange=updateRoleInputs;updateRoleInputs();}
 $("#collection-draft").onsubmit = action(async (e, api, current) => {
    const f = e.target;
    const profiles = await api("/api/document-profiles");
    const list = Array.isArray(profiles)
      ? profiles
      : profiles.items || profiles.profiles || [];
    const profile = list.find(
      (p) => (p.ref || p).profile_id === f.elements.profile.value && Number((p.ref||p).version)===Number(f.elements.profile.selectedOptions[0]?.dataset.version),
    );
    if (!profile) throw new Error(t("workspace.profile_missing"));
    await api(
      `/api/topics/${encodeURIComponent(topic)}/entries/${encodeURIComponent(f.elements.entry.value)}/drafts`,
      {
        method: "POST",
        body: JSON.stringify({
          operation_id: workspaceOperationID(),
          expected_generation: data.catalog.generation,
          profile: profile.ref || profile,
          parameters: Object.fromEntries([...f.querySelectorAll("[data-role-input]")].map(input=>[input.dataset.roleInput,input.value])),
          content: f.elements.content.value,
        }),
      },
    );
    await reloadCurrentCollection(topic);
  });
}
async function reloadCurrentCollection(topic) {
  if (state.route?.params?.topicId === topic) await openTopic(topic);
}
async function saveCollectionCatalog(catalog, topic = state.activeTopicId, request = api) {
  try {
    await request(`/api/topics/${encodeURIComponent(topic)}/catalog`, {
      method: "PUT",
      body: JSON.stringify({
        operation_id: workspaceOperationID(),
        expected_generation: catalog.generation,
        required_delivery_level: catalog.required_delivery_level,
        groups: catalog.groups,
        entries: catalog.entries,
      }),
    });
    await reloadCurrentCollection(topic);
  } catch (error) {
    throw new Error(`${t("workspace.conflict_preserve")} ${error.message}`);
  }
}


// A set route always carries the exact immutable set revision. Async actions
// additionally bind to topic epoch and the particular installed DOM root.

function setRoute(id,revision,member="",section="") {
 return buildRouteHash("topics",{topicId:id},{tab:"documents",set_revision:revision,...(member?{member}:{}),...(section?{section}:{})});
}
function captureDocumentSetMessage(){
 const session=currentCaptureSession();
 const data=captureDocumentSet;
 if(!data || data.session!==session)return null;
 const run=data.run;const plan=run?.plan||data.plan;
 const status=run?.state||"planning";
 let html=`<div class="tf-msg-body"><strong>${escapeHTML(t("docset.title"))}</strong><p>${escapeHTML(t(`docset.state.${status}`))}</p>`;
 if(plan){html+=`<p>${escapeHTML(plan.profile?.name||"")} · ${plan.members?.length||0} ${escapeHTML(t("docset.files"))}</p><details><summary>${escapeHTML(t("docset.plan"))}</summary><ul>${(plan.members||[]).map(m=>`<li>${escapeHTML(m.title)} — ${escapeHTML(m.role)}</li>`).join("")}</ul></details>`;}
 if(run){html+=`<ul>${(run.members||[]).map(m=>`<li>${escapeHTML(m.member_key)}: ${escapeHTML(m.state)}${m.error?` — ${escapeHTML(m.error)}`:""}</li>`).join("")}</ul>`;if(run.state==="committed"){html+=`<a href="${escapeHTML(setRoute(run.plan.set_id,run.candidate.set_revision_id))}">${escapeHTML(t("docset.open"))}</a>`;}else if(run.state==="awaiting_review"){html+=`<button class="tf-btn" data-set-review-run="${escapeHTML(run.run_id)}">${escapeHTML(t("docset.review"))}</button>`;}}
 if(run){if(run.state==="committed"&&run.error)html+=`<button class="tf-btn" data-set-recover-run="${escapeHTML(run.run_id)}">${escapeHTML(t("docset.recover"))}</button>`;if(["queued","planning","generating","checking","committing","ready"].includes(run.state))html+=`<button class="tf-btn" data-set-cancel-run="${escapeHTML(run.run_id)}">${escapeHTML(t("docset.cancel"))}</button>`;if(["failed_retryable","failed_terminal","cancelled","awaiting_review"].includes(run.state))html+=`<button class="tf-btn" data-set-retry-run="${escapeHTML(run.run_id)}">${escapeHTML(t("docset.retry"))}</button>`;if(run.reconciliations?.length)html+=`<details><summary>${escapeHTML(t("docset.diff"))}</summary>${run.reconciliations.map(c=>`<h4>${escapeHTML(c.member_key)}</h4><div class="tf-set-diff"><pre>${escapeHTML(c.base)}</pre><pre>${escapeHTML(c.generated)}</pre><pre>${escapeHTML(c.manual)}</pre></div>`).join("")}</details>`;}
 if(data.error||run?.error)html+=`<p role="alert">${escapeHTML(data.error||run.error)}</p>`;
 const questions=plan?.shared_design?.open_questions||[];if(questions.some(q=>q.blocking))html+=`<ul>${questions.filter(q=>q.blocking).map(q=>`<li>${escapeHTML(q.message)}</li>`).join("")}</ul>`;
 return {role:"system",kind:"document_set",html:html+"</div>"};
}
function currentCaptureSession(){return state.capture.activeScratchpad?.session_id||state.capture.activeSessionId||state.capture.sessionId;}
async function openCaptureDocumentSet(){
 const session=currentCaptureSession();if(!session){toast(t("docset.need_session"));return;}
 const epoch=++captureSetEpoch;const profiles=await api("/api/document-set-profiles");if(epoch!==captureSetEpoch||currentCaptureSession()!==session)return;
 const dialog=document.createElement("dialog");dialog.className="tf-document-dialog";
 dialog.innerHTML=`<form><h2>${escapeHTML(t("docset.generate"))}</h2><label>${escapeHTML(t("workspace.profile"))}<select name="profile">${profiles.map((p,i)=>`<option value="${i}">${escapeHTML(p.name)}</option>`).join("")}</select></label><p data-budget></p><p>${escapeHTML(t("docset.generation_notice"))}</p><label>${escapeHTML(t("docset.markdown_import"))}<input data-set-files type="file" accept=".md,text/markdown" multiple></label><button class="tf-btn tf-btn-primary" type="submit">${escapeHTML(t("docset.generate"))}</button><button class="tf-btn" data-cancel type="button">${escapeHTML(t("common.close"))}</button></form>`;
 document.body.appendChild(dialog);const select=dialog.querySelector("select");const budget=()=>{const p=profiles[Number(select.value)];dialog.querySelector("[data-budget]").textContent=`${t("docset.budget")}: ${p.limits.max_members} ${t("docset.files")}, ${p.limits.max_calls} ${t("docset.calls")}`;};select.onchange=budget;budget();dialog.querySelector("[data-cancel]").onclick=()=>dialog.close();dialog.onclose=()=>dialog.remove();
 dialog.querySelector("form").onsubmit=async event=>{
  event.preventDefault();const p=profiles[Number(select.value)];const files=[...(dialog.querySelector("[data-set-files]")?.files||[])];dialog.close();captureDocumentSet={session};renderCaptureConversation();
  try{
   const sp=await api(`/api/capture/sessions/${encodeURIComponent(session)}`);if(epoch!==captureSetEpoch||currentCaptureSession()!==session)return;
   const existing=sp.document_set_id;let base=0;if(existing){try{base=(await api(`/api/document-sets/${encodeURIComponent(existing)}`)).head.generation;}catch(error){if(error.status&&error.status!==404)throw error;}}
   const plan=await api(`/api/capture/sessions/${encodeURIComponent(session)}/document-set-plans`,{method:"POST",body:JSON.stringify({operation_id:crypto.randomUUID(),...(existing?{set_id:existing}:{}),name:p.name,expected_generation:base,capture_generation:sp.input_generation,profile_id:p.profile_id,profile_version:p.version,profile_hash:p.content_hash})});
   if(epoch!==captureSetEpoch||currentCaptureSession()!==session)return;captureDocumentSet={session,plan};renderCaptureConversation();if(plan.shared_design.open_questions?.some(q=>q.blocking))return;
   let markdownPreview="";if(files.length){markdownPreview=await previewSetMarkdown(session,plan,files,epoch);if(!markdownPreview)return;}
   const run=await api(`/api/capture/sessions/${encodeURIComponent(session)}/document-set-runs`,{method:"POST",body:JSON.stringify({operation_id:crypto.randomUUID(),set_id:plan.set_id,plan_id:plan.plan_id,plan_hash:plan.plan_hash,...(markdownPreview?{markdown_preview_id:markdownPreview}:{}),expected_generation:base,capture_generation:plan.capture.generation})});
   if(epoch!==captureSetEpoch||currentCaptureSession()!==session)return;await pollCaptureDocumentSet(session,run.run_id,epoch);
  }catch(error){if(epoch===captureSetEpoch&&currentCaptureSession()===session){captureDocumentSet={session,error:error.message};renderCaptureConversation();}}
 };
 dialog.showModal();
}
async function pollCaptureDocumentSet(session,id,epoch=++captureSetEpoch){
 const sequence=++captureSetRequest;
 const run=await api(`/api/document-set-runs/${encodeURIComponent(id)}`);
 if(epoch!==captureSetEpoch||currentCaptureSession()!==session)return;
 if(sequence===captureSetRequest)acceptCaptureSetRun(session,run);
 // A newer event response may win this request; keep the polling fallback alive.
 if(sequence!==captureSetRequest||["queued","planning","generating","checking","committing","ready"].includes(captureDocumentSet?.run?.state||run.state))window.setTimeout(()=>{if(epoch===captureSetEpoch&&currentCaptureSession()===session)pollCaptureDocumentSet(session,captureDocumentSet?.run?.run_id||id,epoch).catch(error=>toast(error.message));},1200);
}
function acceptCaptureSetRun(session,run){
 const current=captureDocumentSet?.session===session?captureDocumentSet.run:null;
 if(current?.run_id===run.run_id&&Number(run.state_version||0)<Number(current.state_version||0))return false;
 captureDocumentSet={session,run};renderCaptureConversation();bindSetReviewButtons();return true;
}
function setVersionMember(revision,key){
 return revision.members.some(member=>member.member_key===key)?key:(revision.members[0]?.member_key||"");
}
function bindSetReviewButtons(){
 document.querySelectorAll("[data-set-cancel-run],[data-set-retry-run],[data-set-recover-run]").forEach(button=>{button.onclick=async()=>{const session=currentCaptureSession(),epoch=++captureSetEpoch;const id=button.dataset.setCancelRun||button.dataset.setRetryRun||button.dataset.setRecoverRun;button.disabled=true;try{let run;if(button.dataset.setRecoverRun){run=await api(`/api/document-set-runs/${encodeURIComponent(id)}/recover`,{method:"POST",body:"{}"});}else if(button.dataset.setCancelRun){run=await api(`/api/document-set-runs/${encodeURIComponent(id)}/cancel`,{method:"POST",body:"{}"});}else{const old=await api(`/api/document-set-runs/${encodeURIComponent(id)}`);const uncertain=old.calls.some(c=>["running","uncertain"].includes(c.state));if(uncertain&&!window.confirm(t("docset.uncertain")))return;run=await api(`/api/document-set-runs/${encodeURIComponent(id)}/retry`,{method:"POST",body:JSON.stringify({operation_id:crypto.randomUUID(),expected_generation:old.plan.base_generation,acknowledge_uncertain:uncertain})});}if(epoch!==captureSetEpoch||currentCaptureSession()!==session)return;await pollCaptureDocumentSet(session,run.run_id,epoch);}catch(error){if(epoch===captureSetEpoch&&currentCaptureSession()===session)toast(error.message);}finally{if(button.isConnected)button.disabled=false;}};});
 document.querySelectorAll("[data-set-review-run]").forEach(button=>{button.onclick=()=>openSetReview(button.dataset.setReviewRun).catch(error=>toast(error.message));});
}
async function openSetReview(id){
 const run=await api(`/api/document-set-runs/${encodeURIComponent(id)}`);if(!run.candidate)return;
 const r=run.candidate;const report=r.validation;
 const dialog=document.createElement("dialog");dialog.className="tf-document-dialog";
 dialog.innerHTML=`<form><h2>${escapeHTML(t("docset.review"))}</h2><p>${escapeHTML(t("docset.manual_notice"))}</p><ul>${report.issues.map(i=>`<li>${escapeHTML(i.code)}: ${escapeHTML(i.message)}</li>`).join("")}</ul><div class="tf-document-set-review">${r.members.map(a=>`<details><summary>${escapeHTML(a.member_key)}</summary><div class="markdown-rendered">${renderMarkdown(a.body)}</div></details>`).join("")}</div>${report.checklist.map((item,index)=>`<fieldset data-review-item="${index}"><legend>${escapeHTML(item.id)}</legend><label>${escapeHTML(t("docset.reason"))}<textarea name="reason" required></textarea></label>${item.members.map(key=>`<label>${escapeHTML(key)} · ${escapeHTML(t("docset.section"))}<input data-member="${escapeHTML(key)}" name="section" required></label><label>${escapeHTML(t("docset.evidence"))}<textarea name="evidence" required></textarea></label>`).join("")}</fieldset>`).join("")}${report.issues.filter(i=>i.confirmable).map(i=>`<label>${escapeHTML(i.message)} · ${escapeHTML(t("docset.reason"))}<textarea data-risk="${escapeHTML(i.id)}" required></textarea></label>`).join("")}<button class="tf-btn" type="submit" ${report.structural_status!=="passed"||report.issues.some(i=>i.severity==="error")?"disabled":""}>${escapeHTML(t("docset.submit_review"))}</button><button class="tf-btn" type="button" data-close>${escapeHTML(t("common.close"))}</button><p data-error role="alert"></p></form>`;
 document.body.appendChild(dialog);let reviewDirty=false;dialog.addEventListener("input",()=>reviewDirty=true);const closeReview=()=>!reviewDirty||window.confirm(t("docset.unsaved"));dialog.querySelector("[data-close]").onclick=()=>{if(closeReview())dialog.close();};dialog.oncancel=event=>{if(!closeReview())event.preventDefault();};dialog.onclose=()=>dialog.remove();
 dialog.querySelector("form").onsubmit=async event=>{event.preventDefault();const evidence=[...dialog.querySelectorAll("[data-review-item]")].map(field=>{const item=report.checklist[Number(field.dataset.reviewItem)];return{check_id:item.id,verdict:"passed",reason:field.querySelector('[name="reason"]').value,locations:[...field.querySelectorAll('[name="section"]')].map((input,index)=>({member_key:input.dataset.member,section:input.value,evidence:field.querySelectorAll('[name="evidence"]')[index].value}))};});
  try{await api(`/api/document-sets/${encodeURIComponent(run.plan.set_id)}/reviews`,{method:"POST",body:JSON.stringify({operation_id:crypto.randomUUID(),expected_generation:run.plan.base_generation,candidate_manifest_hash:report.candidate_manifest_hash,run_id:id,reviewer:"workspace user",evidence,decisions:report.issues.filter(i=>i.confirmable).map(i=>({issue_id:i.id,reason:dialog.querySelector(`[data-risk="${CSS.escape(i.id)}"]`).value,locations:i.locations}))})});dialog.close();if(currentCaptureSession()===run.plan.capture.session_id)pollCaptureDocumentSet(currentCaptureSession(),id).catch(error=>toast(error.message));}catch(error){dialog.querySelector("[data-error]").textContent=error.message;}
 };dialog.showModal();
}
async function renderDocumentSetWorkspace(id,data,topicEpoch){
 const root=$("#topic-document");if(!root)return;const h=data.head;const rev=state.route?.query?.set_revision || h.finalized_set_revision || h.working_set_revision;
 const r=data.revisions.find(v=>v.set_revision_id===rev);if(!r)throw new Error(t("docset.version_missing"));
 const token=()=>topicEpoch===topicRequestEpoch&&state.route?.params?.topicId===id&&$("#topic-document")===root&&root.dataset.setRevision===rev;
 root.dataset.setRevision=rev;
 let savedPosition=null;try{savedPosition=JSON.parse(localStorage.getItem(`thoughtflow.documentset-reading.${id}`)||"null");}catch{}
 let key=state.route?.query?.member || (savedPosition?.revision===rev?savedPosition.member:"") || r.members[0]?.member_key;const selected=r.members.find(a=>a.member_key===key);if(!selected)throw new Error(t("workspace.entry_missing"));
 root.innerHTML=`<section class="tf-document-set"><div class="collection-toolbar"><h2>${escapeHTML(r.plan.profile.name)}</h2><label>${escapeHTML(t("docset.version"))}<select data-set-version>${data.revisions.map(v=>`<option value="${escapeHTML(v.set_revision_id)}" ${v.set_revision_id===rev?"selected":""}>${escapeHTML(v.set_revision_id)}${v.set_revision_id===h.finalized_set_revision?` · ${escapeHTML(t("docset.final"))}`:v.set_revision_id===h.working_set_revision?` · ${escapeHTML(t("docset.working"))}`:""}</option>`).join("")}</select></label></div><p>${escapeHTML(t("docset.level"))}: ${escapeHTML(h.finalized_set_revision===rev?h.finalization?.delivery_level:"working")} · ${escapeHTML(r.validation.semantic_status)}</p><div class="tf-set-actions"><button class="tf-btn" data-set-menu>${escapeHTML(t("workspace.catalog"))}</button><button class="tf-btn" data-set-finalize ${h.working_set_revision!==rev?"disabled":""}>${escapeHTML(t("docset.finalize"))}</button>${["delivery","diagnostic","backup"].map(kind=>`<button class="tf-btn" data-set-export="${kind}">${escapeHTML(t(`docset.export.${kind}`))}</button>`).join("")}<a class="tf-btn" href="${escapeHTML(buildRouteHash("topics",{topicId:id},{tab:"documents",references:"1"}))}">${escapeHTML(t("docset.references"))}</a></div><div class="collection-reader"><nav data-set-nav><ul>${r.members.map(a=>`<li><a href="${escapeHTML(setRoute(id,rev,a.member_key))}" ${a.member_key===key?'aria-current="page"':""}>${escapeHTML(a.contract.title)}</a></li>`).join("")}</ul></nav><article><div class="tf-set-actions"><button class="tf-btn" data-set-prev>${escapeHTML(t("workspace.previous"))}</button><button class="tf-btn" data-set-next>${escapeHTML(t("workspace.next"))}</button><button class="tf-btn" data-set-copy>${escapeHTML(t("workspace.copy_link"))}</button><button class="tf-btn" data-set-edit>${escapeHTML(t("document.open"))}</button></div><div class="markdown-rendered" data-set-body data-member="${escapeHTML(key)}" data-section="${escapeHTML(state.route?.query?.section||"")}"></div></article></div><details><summary>${escapeHTML(t("docset.shared"))}</summary><pre>${escapeHTML(JSON.stringify(r.plan.shared_design,null,2))}</pre></details><details><summary>${escapeHTML(t("docset.report"))}</summary><ul>${(r.validation.issues||[]).map(issue=>`<li>${escapeHTML(issue.message)} ${(issue.locations||[]).map(location=>`<a href="${escapeHTML(setRoute(id,rev,location.member_key,location.section))}">${escapeHTML(location.member_key)} · ${escapeHTML(location.section)}</a><blockquote>${escapeHTML(location.evidence||"")}</blockquote>`).join("")}</li>`).join("")}</ul><pre>${escapeHTML(JSON.stringify(r.validation,null,2))}</pre></details><form data-set-search><input type="search" name="q" required aria-label="${escapeHTML(t("workspace.search"))}"><button class="tf-btn">${escapeHTML(t("workspace.search"))}</button></form><div data-set-results></div></section>`;
 const previous=documentSetRenderContext;documentSetRenderContext={id,revision:rev,member:key,keys:new Set(r.members.map(a=>a.member_key))};try{root.querySelector("[data-set-body]").innerHTML=renderObjectMarkdown(selected.body,selected.document_id,selected.resource_bindings?.bindings||[]);}finally{documentSetRenderContext=previous;}
 const setBody=root.querySelector("[data-set-body]");const headingIDs=new Map();setBody.querySelectorAll("h1,h2,h3,h4,h5,h6").forEach(heading=>{const base=heading.textContent.trim().toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu,"").trim().replace(/[\s_]+/g,"-")||"section";const n=headingIDs.get(base)||0;headingIDs.set(base,n+1);heading.id=n?`${base}-${n}`:base;});
 const headings=[...setBody.querySelectorAll("h1,h2,h3,h4,h5,h6")];if(headings.length){const toc=document.createElement("details");toc.className="collection-toc";toc.innerHTML=`<summary>${escapeHTML(t("workspace.sections"))}</summary>${headings.map(heading=>`<p><a href="${escapeHTML(setRoute(id,rev,key,heading.id))}">${escapeHTML(heading.textContent)}</a></p>`).join("")}`;setBody.prepend(toc);}
 setBody.querySelectorAll('a[href^="#"]').forEach(link=>link.onclick=event=>{const href=link.getAttribute("href");if(!token()||href.startsWith("#/"))return;event.preventDefault();let section=href.slice(1);try{section=decodeURIComponent(section);}catch{}navigateHash(setRoute(id,rev,key,section));});
 const section=state.route?.query?.section || (!state.route?.query?.member&&savedPosition?.revision===rev?savedPosition.section:"");try{localStorage.setItem(`thoughtflow.documentset-reading.${id}`,JSON.stringify({revision:rev,member:key,section:section||""}));}catch{}
 if(section){requestAnimationFrame(()=>{if(token())root.querySelector(`[id="${CSS.escape(section)}"]`)?.scrollIntoView({block:"start"});});}
 root.querySelector("[data-set-version]").onchange=event=>{
  if(!token())return;const target=data.revisions.find(v=>v.set_revision_id===event.target.value);if(!target)return;
  navigateHash(setRoute(id,target.set_revision_id,setVersionMember(target,key)),{force:true});
 };
 const index=r.members.findIndex(a=>a.member_key===key);for(const [name,offset]of [["prev",-1],["next",1]]){const button=root.querySelector(`[data-set-${name}]`);button.disabled=!r.members[index+offset];button.onclick=()=>token()&&navigateHash(setRoute(id,rev,r.members[index+offset].member_key),{force:true});}
 root.querySelector("[data-set-menu]").onclick=()=>{const nav=root.querySelector("[data-set-nav]");nav.hidden=!nav.hidden;};
 root.querySelector("[data-set-copy]").onclick=()=>token()&&copyText(`${window.location.origin}${window.location.pathname||"/"}${setRoute(id,rev,key,section||"")}`);
 root.querySelector("[data-set-edit]").onclick=()=>token()&&openDocumentEditor(selected.document_id).catch(error=>toast(error.message));
 root.querySelector("[data-set-finalize]").onclick=async()=>{if(!token())return;try{await api(`/api/document-sets/${encodeURIComponent(id)}/finalizations`,{method:"POST",body:JSON.stringify({operation_id:crypto.randomUUID(),expected_generation:h.generation,set_revision_id:rev,set_revision_hash:r.set_revision_hash,approved_by:"workspace user",delivery_level:"ready_for_use"})});if(token())navigateHash(setRoute(id,rev,key),{force:true});}catch(error){toast(error.message);}};
 root.querySelectorAll("[data-set-export]").forEach(button=>{button.onclick=async()=>{if(!token())return;button.disabled=true;try{const response=await fetch(`/api/document-sets/${encodeURIComponent(id)}/exports`,{method:"POST",headers:{"Content-Type":"application/json"},body:JSON.stringify({operation_id:crypto.randomUUID(),expected_generation:h.generation,set_revision_id:rev,kind:button.dataset.setExport})});if(!response.ok)throw new Error(`${response.status}: ${await response.text()}`);const blob=await response.blob();if(!token())return;const url=URL.createObjectURL(blob);const a=document.createElement("a");a.href=url;a.download=`${id}-${button.dataset.setExport}.zip`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}catch(error){if(token())toast(error.message);}finally{if(token())button.disabled=false;}};});
 root.querySelector("[data-set-search]").onsubmit=async event=>{event.preventDefault();const epoch=++setSearchEpoch;try{const results=await api(`/api/document-sets/${encodeURIComponent(id)}/search?${new URLSearchParams({set_revision:rev,q:event.target.q.value})}`);if(!token()||epoch!==setSearchEpoch)return;root.querySelector("[data-set-results]").innerHTML=results.map(a=>`<p><a href="${escapeHTML(setRoute(id,rev,a.member_key))}">${escapeHTML(a.contract.title)}</a></p>`).join("");}catch(error){if(token()&&epoch===setSearchEpoch)toast(error.message);}};
}
async function openPlainThoughtEditor(snapshot){
 if(documentEditor?.busy)return;
 if(documentEditor?.dirty&&!window.confirm(t("docset.unsaved")))return;
 documentEditor?.dialog?.remove();const dialog=document.createElement("dialog");dialog.className="tf-document-dialog";const id=snapshot.thought.id;
 const original=snapshot.content.ai_notes||snapshot.content.original||snapshot.content.extracted_content||"";
 dialog.innerHTML=`<form><h2>${escapeHTML(snapshot.thought.display_title||id)}</h2><textarea rows="26" name="body"></textarea><button class="tf-btn" type="submit">${escapeHTML(t("document.save"))}</button><button class="tf-btn" type="button" data-convert>${escapeHTML(t("docset.convert"))}</button><button class="tf-btn" type="button" data-close>${escapeHTML(t("common.close"))}</button><p role="alert"></p></form>`;document.body.appendChild(dialog);const textarea=dialog.querySelector("textarea");textarea.value=original;
 const editor={id,dialog,session:crypto.randomUUID(),dirty:false,busy:false,contentHash:snapshot.content_hash};editor.routeHash=window.location.hash;documentEditor=editor;dialog.querySelector("[data-convert]").onclick=()=>{if(editor.dirty){toast(t("docset.save_first"));return;}openThoughtConversion(id,editor).catch(error=>{dialog.querySelector("p").textContent=error.message;});};textarea.oninput=()=>{editor.dirty=true;};const canClose=()=>!editor.busy&&(!editor.dirty||window.confirm(t("docset.unsaved")));dialog.querySelector("[data-close]").onclick=()=>{if(canClose())dialog.close();};dialog.addEventListener("keydown",event=>{if(event.key!=="Escape")return;event.preventDefault();event.stopPropagation();if(canClose())dialog.close();});dialog.oncancel=event=>{if(!canClose())event.preventDefault();};dialog.onclose=()=>{if(documentEditor===editor)documentEditor=null;dialog.remove();};
 dialog.querySelector("form").onsubmit=async event=>{event.preventDefault();if(editor.busy)return;editor.busy=true;const body=textarea.value;try{const saved=await api(`/api/thoughts/${encodeURIComponent(id)}`,{method:"PATCH",headers:{"X-Session-ID":editor.session},body:JSON.stringify({body,expected_content_hash:editor.contentHash})});editor.contentHash=saved.content_hash;if(textarea.value===body)editor.dirty=false;}catch(error){dialog.querySelector("p").textContent=error.message;}finally{editor.busy=false;}};dialog.showModal();
}

async function previewSetMarkdown(session, plan, files, epoch) {
  const contents = await Promise.all(files.map(async file => ({path:file.webkitRelativePath || file.name, content:await file.text()})));
  if (epoch !== captureSetEpoch || currentCaptureSession() !== session) return "";
  const dialog = document.createElement("dialog"); dialog.className = "tf-document-dialog";
  dialog.innerHTML = `<form><h2>${escapeHTML(t("docset.markdown_import"))}</h2><p>${escapeHTML(t("docset.original_notice"))}</p>${contents.map((file,index)=>`<label>${escapeHTML(file.path)} → <select data-file="${index}">${plan.members.map(member=>`<option value="${escapeHTML(member.key)}" ${file.path.replace(/\.md$/i,"").replaceAll("/","-")===member.filename.replace(/\.md$/i,"")?"selected":""}>${escapeHTML(member.title)} · ${escapeHTML(member.document_id)}</option>`).join("")}</select></label>`).join("")}<pre data-result></pre><button class="tf-btn" type="submit">${escapeHTML(t("docset.preview"))}</button><button class="tf-btn" type="button" data-accept disabled>${escapeHTML(t("docset.generate"))}</button><button class="tf-btn" type="button" data-close>${escapeHTML(t("common.close"))}</button><p data-error role="alert"></p></form>`;
  document.body.appendChild(dialog);
  return new Promise(resolve => {
    let preview = null, settled = false;
    const finish = value => {if(settled)return;settled=true;resolve(value);dialog.close();};
    dialog.onclose=()=>{if(!settled)resolve("");dialog.remove();};
    dialog.querySelector("[data-close]").onclick=()=>finish("");
    dialog.addEventListener("change",()=>{preview=null;dialog.querySelector("[data-accept]").disabled=true;});
    dialog.querySelector("form").onsubmit=async event=>{
      event.preventDefault(); const submit=dialog.querySelector('[type="submit"]');submit.disabled=true;
      try {
        const mappings=contents.map((file,index)=>({...file,member_key:dialog.querySelector(`[data-file="${index}"]`).value}));
        preview=await api(`/api/capture/sessions/${encodeURIComponent(session)}/document-set-markdown-previews`,{method:"POST",body:JSON.stringify({operation_id:crypto.randomUUID(),set_id:plan.set_id,plan_id:plan.plan_id,plan_hash:plan.plan_hash,files:mappings})});
        if(epoch!==captureSetEpoch||currentCaptureSession()!==session){finish("");return;}
        dialog.querySelector("[data-result]").textContent=preview.mappings.map(m=>`${m.path} → ${m.member_key} → ${m.document_id}\n${m.original_hash}`).join("\n\n")+"\n"+preview.warnings.join("\n");
        dialog.querySelector("[data-accept]").disabled=false;
      } catch(error){dialog.querySelector("[data-error]").textContent=error.message;}
      finally{submit.disabled=false;}
    };
    dialog.querySelector("[data-accept]").onclick=()=>{if(preview&&epoch===captureSetEpoch&&currentCaptureSession()===session)finish(preview.preview_id);else finish("");};
    dialog.showModal();
  });
}
async function openThoughtConversion(id, editor) {
  const data=await api(`/api/thoughts/${encodeURIComponent(id)}/document-conversions`);
  if(documentEditor!==editor||editor.dirty)return;
  const dialog=document.createElement("dialog");dialog.className="tf-document-dialog";
  dialog.innerHTML=`<form><h2>${escapeHTML(t("docset.convert"))}</h2><p>${escapeHTML(t("docset.conversion_notice"))}</p><select name="profile">${data.profiles.map((p,i)=>`<option value="${i}">${escapeHTML(profileLabel(p))}</option>`).join("")}</select><div data-inputs></div><button class="tf-btn" type="submit">${escapeHTML(t("docset.convert"))}</button><button class="tf-btn" type="button" data-close>${escapeHTML(t("common.close"))}</button><p data-error role="alert"></p></form>`;
  document.body.appendChild(dialog);
  const select=dialog.querySelector("select");
  const inputs=()=>{const p=data.profiles[Number(select.value)];dialog.querySelector("[data-inputs]").innerHTML=(p?.inputs||[]).map(input=>`<label>${escapeHTML(input.label||input.key)}<textarea data-param="${escapeHTML(input.key)}" ${input.required?"required":""}></textarea></label>`).join("");};select.onchange=inputs;inputs();
  dialog.querySelector("[data-close]").onclick=()=>dialog.close();dialog.onclose=()=>dialog.remove();
  dialog.querySelector("form").onsubmit=async event=>{event.preventDefault();if(documentEditor!==editor||editor.dirty)return;const button=dialog.querySelector('[type="submit"]');button.disabled=true;try{const profile=data.profiles[Number(select.value)];const parameters=Object.fromEntries([...dialog.querySelectorAll("[data-param]")].map(input=>[input.dataset.param,input.value]));await api(`/api/thoughts/${encodeURIComponent(id)}/document-conversions`,{method:"POST",body:JSON.stringify({operation_id:crypto.randomUUID(),expected_content_hash:data.expected_content_hash,profile_ref:profileRef(profile),parameters})});dialog.close();editor.dialog.close();await openDocumentEditor(id);}catch(error){dialog.querySelector("[data-error]").textContent=error.message;}finally{button.disabled=false;}};
  dialog.showModal();
}

boot().catch((error) => toast(error.message + (error.stack ? "\n" + error.stack.split("\n").slice(0, 3).join("\n") : "")));
