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
  topics: [],
  activeTopicId: "",
  selectedThoughts: new Set(),
  composeBasket: new Set(),
  lastResults: [],
  composeDraft: null,
  composeDrafts: [],
  activeThoughtId: "",
  activeThoughtSnapshot: null,
  activeTopicDetail: null,
  weaveProposal: null,
  weaveProposals: [],
  status: null,
  metrics: null,
  pendingConfirm: null,
  capture: {
    sessionId: "",
    activeThoughtId: "",
    activeSnapshot: null,
    activeScratchpad: null,
    archivePreview: null,
    messages: [],
    sessions: [],
    suggestion: null,
    lockedBy: "",
  },
};

// Cross-tab bus built on BroadcastChannel where supported, falling back
// to a localStorage 'storage' event listener. Used to keep the compose
// basket and (Phase 9) session locks in sync across tabs.
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
      // fall through to storage-event bus
    }
  }
  // Fallback: piggyback on localStorage 'storage' events. Only fires in
  // OTHER tabs, which is exactly the cross-tab behaviour we want.
  const handlers = new Set();
  const storageHandler = (event) => {
    if (event.key !== "tflow.bus") return;
    let payload = null;
    try { payload = JSON.parse(event.newValue || "null"); } catch (_) { return; }
    if (!payload) return;
    for (const handler of handlers) {
      try { handler(payload); } catch (_) { /* ignore */ }
    }
  };
  window.addEventListener("storage", storageHandler);
  tflowBus = {
    post(message) {
      try { window.localStorage.setItem("tflow.bus", JSON.stringify({ ...message, _ts: Date.now() })); } catch (_) {}
    },
    on(handler) {
      handlers.add(handler);
      return () => handlers.delete(handler);
    },
    close() { window.removeEventListener("storage", storageHandler); },
  };
  return tflowBus;
}

// Wire basket updates onto the bus so other tabs see add/remove/clear.
function broadcastBasketChange() {
  if (tflowBus) {
    tflowBus.post({ kind: "basket:changed", ids: Array.from(state.composeBasket) });
  }
}

let markdownParser = null;

const $ = (selector) => document.querySelector(selector);

function parseRoute(hash) {
  const raw = String(hash || "").replace(/^#\/?/, "");
  const [pathPart, queryPart = ""] = raw.split("?");
  const parts = pathPart.split("/").filter(Boolean);
  const query = Object.fromEntries(new URLSearchParams(queryPart).entries());
  if (parts.length === 0) return { page: "dashboard", nav: "overview", params: {}, query };
  // /topics/{id}/review is folded into the topic detail tab ?tab=proposals.
  if (parts[0] === "topics" && parts[1] && parts[2] === "review") {
    const detailQuery = { ...query, tab: "proposals" };
    return { page: "topics", nav: "topics", params: { topicId: parts[1] }, query: detailQuery };
  }
  if (parts[0] === "topics" && parts[1]) {
    const detailQuery = query.tab ? query : { ...query, tab: "detail" };
    return { page: "topics", nav: "topics", params: { topicId: parts[1] }, query: detailQuery };
  }
  if (parts[0] === "notes") {
    return { page: "thoughts", nav: "notes", params: { thoughtId: query.id || "" }, query };
  }
  if (parts[0] === "compose") {
    return { page: "compose", nav: "compose", params: {}, query };
  }
  if (parts[0] === "overview") {
    return { page: "dashboard", nav: "overview", params: {}, query };
  }
  const known = new Set(["capture", "search", "topics"]);
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
    const tid = $("#search-topic-id")?.value.trim();
    if (tid) q.topic_id = tid;
    const tags = $("#search-tags")?.value.trim();
    if (tags) q.tags = tags;
    if (state.selectedThoughts.size > 0) q.selected = Array.from(state.selectedThoughts).join(",");
    return q;
  },
  topics: () => {
    const q = {};
    const f = $("#topic-filter")?.value.trim();
    if (f) q.keyword = f;
    if ($("#topic-auto-filter")?.checked) q.auto_weave = "true";
    if (state.route?.params?.topicId) {
      const active = document.querySelector(`#page-topics .tab.active`);
      if (active && active.dataset.tab && active.dataset.tab !== "topics-list") q.tab = active.dataset.tab;
    }
    return q;
  },
  thoughts: () => {
    const q = {};
    const id = $("#thought-id")?.value.trim();
    if (id) q.id = id;
    const active = document.querySelector(`#page-thoughts .tab.active`);
    if (active && active.dataset.tab && active.dataset.tab !== "notes-all") q.tab = active.dataset.tab;
    return q;
  },
};

// Reverse of PAGE_SERIALIZERS — read a query object and apply it to inputs /
// state. Unknown keys are silently ignored so older URLs don't crash on a
// forward port. Returns nothing; mutates DOM and state in place.
function restoreRoutePage(page, query) {
  if (!query || typeof query !== "object") return;
  if (page === "search") {
    if (typeof query.q === "string") $("#search-query").value = query.q;
    if (typeof query.topic_id === "string") $("#search-topic-id").value = query.topic_id;
    if (typeof query.tags === "string") $("#search-tags").value = query.tags;
    if (typeof query.selected === "string" && query.selected) {
      state.selectedThoughts = new Set(query.selected.split(",").filter(Boolean));
    }
  } else if (page === "topics") {
    if (typeof query.keyword === "string") $("#topic-filter").value = query.keyword;
    if (query.auto_weave === "true") $("#topic-auto-filter").checked = true;
    if (state.route?.params?.topicId) {
      const tab = typeof query.tab === "string" ? query.tab : "topics-detail";
      activateTab(tab, $("#page-topics"));
    }
  } else if (page === "thoughts") {
    if (typeof query.id === "string") $("#thought-id").value = query.id;
    if (typeof query.tab === "string") activateTab(query.tab, $("#page-thoughts"));
  }
  // topic-review, compose handled by their loaders (proposal / draft IDs
  // come back via API calls and are stored on state).
}

function buildRouteHash(page, params = {}, query = {}) {
  // Map internal page names back to user-facing segment names. The page
  // identifiers (used in `data-page` and route.page) preserve the historical
  // names so existing renderers and CSS hooks keep working; only the
  // top-level hash segment is rewritten to the new wording.
  const pageToSegment = {
    dashboard: "overview",
    thoughts: "notes",
    compose: "compose",
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

// Minimal localStorage wrapper that survives blocked storage and absent
// environments (vm tests). Returns the default if read fails.
function loadFromStorage(key, fallback) {
  try {
    const raw = window.localStorage?.getItem(key);
    if (!raw) return fallback;
    return JSON.parse(raw);
  } catch (_error) {
    return fallback;
  }
}

function saveToStorage(key, value) {
  try {
    window.localStorage?.setItem(key, JSON.stringify(value));
    return true;
  } catch (_error) {
    return false;
  }
}

const BASKET_STORAGE_KEY = "tflow.basket";

function persistBasket() {
  saveToStorage(BASKET_STORAGE_KEY, {
    ids: Array.from(state.composeBasket),
    updated_at: new Date().toISOString(),
  });
}

function restoreBasket() {
  const stored = loadFromStorage(BASKET_STORAGE_KEY, null);
  if (stored && Array.isArray(stored.ids)) {
    state.composeBasket = new Set(stored.ids.filter(Boolean));
  }
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
  $("#confirm-title").textContent = title;
  $("#confirm-message").textContent = message;
  if (activeFocusRelease) {
    try { activeFocusRelease(); } catch (_) { /* ignore */ }
    activeFocusRelease = null;
  }
  modal.classList.add("open");
  modal.setAttribute("aria-hidden", "false");
  activeFocusRelease = trapFocus(modal, document.activeElement);
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

async function api(path, options = {}) {
  const response = await fetch(path, {
    headers: { "Content-Type": "application/json", ...(options.headers || {}) },
    ...options,
  });
  const payload = await response.json().catch(() => ({}));
  if (!response.ok || payload.error) {
    const message = payload.error?.message || response.statusText || t("toast.request_failed");
    const requestID = payload.request_id ? `${payload.request_id}: ` : "";
    throw new Error(`${requestID}${message}`);
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

function createComposeBasket(initial = []) {
  const values = new Set(initial.filter(Boolean));
  return {
    add(id) {
      if (id) values.add(id);
      return Array.from(values);
    },
    addMany(ids) {
      for (const id of ids || []) if (id) values.add(id);
      return Array.from(values);
    },
    clear() {
      values.clear();
      return [];
    },
    values() {
      return Array.from(values);
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

function cleanMarkdownHref(value) {
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
    token.attrSet("target", "_blank");
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

function renderDiff(lines) {
  if (!lines || lines.length === 0) {
    return `<div class="topic-meta">${escapeHTML(t("diff.no_changes"))}</div>`;
  }
  return lines
    .map((line) => {
      const op = line.op || "context";
      const marker = op === "add" ? "+" : op === "remove" ? "-" : " ";
      return `<div class="diff-line ${escapeHTML(op)}"><span>${marker}</span><code>${escapeHTML(line.text || "")}</code></div>`;
    })
    .join("");
}

function renderWeaveProposals() {
  const list = $("#weave-proposals");
  if (!list) return;
  if (!state.activeTopicId) {
    list.innerHTML = `<div class="topic-meta">${escapeHTML(t("topics.select_first"))}</div>`;
    return;
  }
  if (!state.weaveProposals || state.weaveProposals.length === 0) {
    list.innerHTML = `<div class="topic-meta">${escapeHTML(t("topics.weave_proposals_none"))}</div>`;
    return;
  }
  list.innerHTML = state.weaveProposals
    .map((proposal) => {
      const active = state.weaveProposal?.id === proposal.id ? " active" : "";
      const status = proposal.status || "pending";
      const hunkCount = proposal.patch?.hunks?.length || 0;
      return `
        <article class="approval-item${active}" data-proposal-id="${escapeHTML(proposal.id)}">
          <strong>${escapeHTML(proposal.thought_id || proposal.id)}</strong>
          <div class="topic-meta">
            <span class="pill">${escapeHTML(status)}</span>
            <span>${t("topics.patch_hunks", { n: hunkCount })}</span>
            <span>${escapeHTML(fmtDate(proposal.updated_at || proposal.created_at))}</span>
          </div>
        </article>
      `;
    })
    .join("");
  list.querySelectorAll("[data-proposal-id]").forEach((item) => {
    item.addEventListener("click", () => loadWeaveProposal(item.dataset.proposalId).catch((error) => toast(error.message)));
  });
}

function renderComposeDrafts() {
  const list = $("#compose-drafts");
  if (!list) return;
  if (!state.composeDrafts || state.composeDrafts.length === 0) {
    list.innerHTML = `<div class="topic-meta">${escapeHTML(t("compose.drafts_empty"))}</div>`;
    return;
  }
  list.innerHTML = state.composeDrafts
    .map((draft) => {
      const active = state.composeDraft?.id === draft.id ? " active" : "";
      const status = draft.status || "draft";
      return `
        <article class="approval-item${active}" data-compose-id="${escapeHTML(draft.id)}">
          <strong>${escapeHTML(draft.goal || draft.id)}</strong>
          <div class="topic-meta">
            <span class="pill">${escapeHTML(status)}</span>
            <span>${escapeHTML(draft.format || t("compose.format.summary"))}</span>
            <span>${escapeHTML(fmtDate(draft.updated_at || draft.created_at))}</span>
          </div>
        </article>
      `;
    })
    .join("");
  list.querySelectorAll("[data-compose-id]").forEach((item) => {
    item.addEventListener("click", () => loadComposeDraft(item.dataset.composeId).catch((error) => toast(error.message)));
  });
}

async function loadStatus() {
  try {
    const status = await api("/api/system/status");
    state.status = status;
    $("#system-status").textContent = `${status.workspace.id} / ${status.status}`;
    $("#dashboard-workspace").textContent = status.workspace?.status || status.status;
    $("#dashboard-llm").textContent = status.llm?.status || t("toast.unknown");
    $("#dashboard-embedding").textContent = status.embedding?.status || t("toast.unknown");
    $("#dashboard-git").textContent = status.git?.status || t("toast.unknown");
    $("#dashboard-search").textContent = status.duckdb?.status || t("toast.unknown");
    // PR3: status tiles now live in the settings drawer; renderSettingsStatus
    // applies them. The dashboard cards above keep the lightweight summary.
    renderTopbarStatus(status);
    renderSettingsStatus(status);
    const dashboardAlert = $("#dashboard-alert");
    if (dashboardAlert) {
      dashboardAlert.hidden = status.status === "ready";
      dashboardAlert.textContent = status.status === "ready" ? "" : t("dashboard.status_alert", { status: status.status });
    }
  } catch (error) {
    $("#system-status").textContent = "degraded";
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
    notes: formatBadgeCount(state.metrics?.values?.thoughtflow_capture_total),
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
  // Copy the last few events from the dashboard summary so opening the
  // notes runtime card feels live. The dashboard already receives every
  // event; the clone is cheap and avoids a second SSE connection.
  if (eventsNode) {
    const source = $("#dashboard-events");
    const items = source ? Array.from(source.children).slice(0, 6) : [];
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
  const autoOnly = Boolean($("#topic-auto-filter")?.checked);
  const filteredTopics = state.topics.filter((topic) => {
    const text = `${topic.name || ""} ${topic.description || ""} ${topic.id || ""}`.toLowerCase();
    const matchesText = !textFilter || text.includes(textFilter);
    const matchesAuto = !autoOnly || topic.auto_weave !== false;
    return matchesText && matchesAuto;
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
    state.weaveProposals = [];
    state.weaveProposal = null;
    renderWeaveProposals();
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
          <div class="topic-actions">
            <button class="mini-button" data-topic-open="${escapeHTML(topic.id)}" type="button">${escapeHTML(t("topics.open"))}</button>
          </div>
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
  list.querySelectorAll("[data-topic-open]").forEach((button) => {
    button.addEventListener("click", () => navigateTopic(button.dataset.topicOpen));
  });
}

function resetTopicFilters() {
  $("#topic-filter").value = "";
  $("#topic-auto-filter").checked = false;
  renderTopics();
}

function navigateTopic(topicId, review = false) {
  if (!topicId) return;
  // PR2: topic detail and review are tabs under #/topics. The legacy
  // /review segment is preserved in parseRoute for back-compat, but new
  // navigation writes ?tab=proposals into the same /topics/{id} URL.
  const tab = review ? "proposals" : "detail";
  window.location.hash = `#/topics/${encodeURIComponent(topicId)}?tab=${tab}`;
}

async function openTopic(topicId) {
  if (!topicId) return;
  const detail = await api(`/api/topics/${encodeURIComponent(topicId)}`);
  state.activeTopicId = topicId;
  state.activeTopicDetail = detail;
  // PR2: topic detail / proposals / rules are tabs inside the topics
  // page; enable them once a topic is loaded and populate the document
  // panel. Tab activation follows the URL `?tab=` query so deep-links
  // land on the right pane.
  ["topics-tab-detail", "topics-tab-proposals", "topics-tab-rules"].forEach((id) => {
    const el = document.getElementById(id);
    if (el) el.disabled = false;
  });
  $("#topic-document").innerHTML = renderMarkdown(detail.document || t("topics.document_empty"));
  $("#refresh-topic").disabled = false;
  $("#open-topic-rules").disabled = false;
  populateTopicEditor(detail.topic);
  renderTopicMembers(detail.members || []);
  renderTopicRules(detail.topic);
  renderTopics();
  await loadWeaveProposals(topicId);
  await loadTopicCandidates(topicId);
}

async function loadTopicCandidates(topicId = state.activeTopicId) {
  if (!topicId) return [];
  try {
    const candidates = await api(`/api/topics/${encodeURIComponent(topicId)}/candidates`);
    renderTopicCandidatesInto(candidates || []);
    return candidates || [];
  } catch (error) {
    // The candidate list is best-effort observability — a transient
    // failure should not break the topic detail view. Surface the
    // error in the candidate panel so the user can retry.
    const node = $("#topic-candidates");
    if (node) node.innerHTML = `<div class="tf-empty">${escapeHTML(t("toast.request_failed"))}: ${escapeHTML(error.message)}</div>`;
    return [];
  }
}

function renderTopicCandidates(candidates) {
  if (!Array.isArray(candidates) || candidates.length === 0) {
    return `<div class="tf-empty">${escapeHTML(t("topics.candidates_empty"))}</div>`;
  }
  // 候选影响区只读展示 in-flight 影响,任何接受 / 合并动作都走 Weave Proposals
  // tab 并先展示 diff,符合 todo 第 4.4 节「确认候选或接受 weave 前必须
  // 展示写入内容或 diff」。
  const items = candidates.map(renderTopicCandidateImpact).join("");
  const header = `<div class="tf-card-header"><h3>${escapeHTML(t("topics.candidates_title"))}</h3><span class="tf-text-secondary">${escapeHTML(t("topics.candidates_hint"))}</span></div>`;
  return `${header}<div class="approval-list">${items}</div>`;
}

function renderTopicCandidatesInto(candidates) {
  const node = $("#topic-candidates");
  if (!node) return;
  node.innerHTML = renderTopicCandidates(candidates);
  // Clicking a candidate jumps to the Weave Proposals tab where the diff
  // / written content is shown before the user can accept.
  node.querySelectorAll("[data-candidate-source]").forEach((el) => {
    el.addEventListener("click", () => {
      activateTab("topics-proposals", $("#page-topics"));
      loadWeaveProposals(state.activeTopicId).catch((error) => toast(error.message));
    });
  });
}

function renderTopicCandidateImpact(candidate) {
  const sourceLabel = t(`topics.candidate_source.${candidate.source}`) || candidate.source;
  const status = candidate.status ? `<span class="pill">${escapeHTML(candidate.status)}</span>` : "";
  const reasons = Array.isArray(candidate.reasons) && candidate.reasons.length > 0
    ? `<div class="topic-meta">${candidate.reasons.map((reason) => escapeHTML(reason)).join(" · ")}</div>`
    : "";
  const referenceID = candidate.thought_id || candidate.draft_id || candidate.session_id || candidate.candidate_id || "";
  return `
    <article class="result-item" data-candidate-source="${escapeHTML(candidate.source)}" data-candidate-id="${escapeHTML(candidate.candidate_id)}" data-candidate-ref="${escapeHTML(referenceID)}" style="cursor: pointer;">
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
              <button class="mini-button" data-basket-id="${escapeHTML(member.thought_id || member.id)}" type="button">${escapeHTML(t("search.result.add_basket"))}</button>
            </div>
          </div>
        </div>
      </article>
    `)
    .join("");
  node.querySelectorAll("[data-preview-id]").forEach((button) => {
    button.addEventListener("click", () => previewThought(button.dataset.previewId, { drawer: true }).catch((error) => toast(error.message)));
  });
  node.querySelectorAll("[data-basket-id]").forEach((button) => {
    button.addEventListener("click", () => addToComposeBasket([button.dataset.basketId]));
  });
}

function renderTopicRules(topic) {
  const node = $("#topic-rules-summary");
  if (!node || !topic) return;
  const rules = topic.rules || {};
  const keywords = rules.keywords || {};
  const tags = rules.tags || {};
  const semantic = rules.semantic || {};
  node.innerHTML = renderDescription([
    [t("topics.rule.keywords_any"), joinCSV(keywords.any) || t("topics.rule.none")],
    [t("topics.rule.keywords_all"), joinCSV(keywords.all) || t("topics.rule.none")],
    [t("topics.rule.keywords_exclude"), joinCSV(keywords.exclude) || t("topics.rule.none")],
    [t("topics.rule.tags_any"), joinCSV(tags.any) || t("topics.rule.none")],
    [t("topics.rule.manual_include"), joinCSV(rules.manual_include) || t("topics.rule.none")],
    [t("topics.rule.manual_exclude"), joinCSV(rules.manual_exclude) || t("topics.rule.none")],
    [t("topics.rule.semantic"), semantic.enabled ? t("topics.rule.semantic_enabled", { threshold: semantic.threshold || 0.75 }) : t("topics.rule.semantic_disabled")],
    [t("topics.rule.auto_weave"), topic.auto_weave === false ? t("topics.rule.auto_weave_disabled") : t("topics.rule.auto_weave_enabled")],
    [t("topics.rule.outline"), outlineText(topic.outline) || t("topics.rule.none")],
  ]);
}

async function loadWeaveProposals(topicId = state.activeTopicId) {
  if (!topicId) {
    state.weaveProposals = [];
    renderWeaveProposals();
    return;
  }
  state.weaveProposals = await api(`/api/topics/${encodeURIComponent(topicId)}/weave-proposals`);
  renderWeaveProposals();
}

function populateTopicEditor(topic) {
  const fields = [
    "edit-topic-name",
    "edit-topic-description",
    "edit-keywords-any",
    "edit-keywords-all",
    "edit-keywords-exclude",
    "edit-tags-any",
    "edit-manual-include",
    "edit-manual-exclude",
    "edit-semantic",
    "edit-threshold",
    "edit-auto-weave",
    "edit-outline",
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
  const semantic = rules.semantic || {};
  $("#edit-topic-name").value = topic.name || "";
  $("#edit-topic-description").value = topic.description || "";
  $("#edit-keywords-any").value = joinCSV(keywords.any);
  $("#edit-keywords-all").value = joinCSV(keywords.all);
  $("#edit-keywords-exclude").value = joinCSV(keywords.exclude);
  $("#edit-tags-any").value = joinCSV(tags.any);
  $("#edit-manual-include").value = joinCSV(rules.manual_include);
  $("#edit-manual-exclude").value = joinCSV(rules.manual_exclude);
  $("#edit-semantic").checked = Boolean(semantic.enabled);
  $("#edit-threshold").value = Number.isFinite(semantic.threshold) && semantic.threshold > 0 ? semantic.threshold : 0.75;
  $("#edit-auto-weave").checked = topic.auto_weave !== false;
  $("#edit-outline").value = outlineText(topic.outline);
}

async function createTopic(event) {
  event.preventDefault();
  const name = $("#topic-name").value.trim();
  if (!name) {
    toast(t("toast.topic_name_required"));
    return;
  }
  const semanticEnabled = $("#topic-semantic").checked;
  const threshold = Number.parseFloat($("#topic-threshold").value || "0.75");
  const topic = await api("/api/topics", {
    method: "POST",
    body: JSON.stringify({
      name,
      description: $("#topic-description").value.trim(),
      rules: {
        keywords: {
          any: csv($("#topic-keywords").value),
          all: csv($("#topic-keywords-all").value),
          exclude: csv($("#topic-keywords-exclude").value),
        },
        tags: { any: csv($("#topic-tags").value) },
        semantic: { enabled: semanticEnabled, threshold },
        manual_include: csv($("#topic-manual-include").value),
        manual_exclude: csv($("#topic-manual-exclude").value),
      },
      outline: outlineFromText($("#topic-outline").value),
      auto_weave: $("#topic-auto-weave").checked,
    }),
  });
  event.target.reset();
  $("#topic-threshold").value = "0.75";
  $("#topic-auto-weave").checked = true;
  $("#topic-outline").value = "Notes\nOpen Questions";
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
  const threshold = Number.parseFloat($("#edit-threshold").value || "0.75");
  const topic = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}`, {
    method: "PUT",
    body: JSON.stringify({
      name: $("#edit-topic-name").value.trim(),
      description: $("#edit-topic-description").value.trim(),
      rules: {
        keywords: {
          any: csv($("#edit-keywords-any").value),
          all: csv($("#edit-keywords-all").value),
          exclude: csv($("#edit-keywords-exclude").value),
        },
        tags: { any: csv($("#edit-tags-any").value) },
        semantic: { enabled: $("#edit-semantic").checked, threshold },
        manual_include: csv($("#edit-manual-include").value),
        manual_exclude: csv($("#edit-manual-exclude").value),
      },
      outline: outlineFromText($("#edit-outline").value),
      auto_weave: $("#edit-auto-weave").checked,
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
  if (msg.thoughtId && msg.thoughtId === state.capture.activeThoughtId && state.capture.activeSnapshot) {
    return renderCaptureThoughtCardFromSnapshot(state.capture.activeSnapshot);
  }
  if (msg.html) return msg.html;
  if (msg.text) return `<div class="tf-msg-body">${escapeHTML(msg.text)}</div>`;
  return "";
}

function renderCaptureConversation() {
  const list = $("#capture-conversation");
  if (!list) return;
  const messages = state.capture.messages || [];
  if (messages.length === 0) {
    list.innerHTML = `<li class="tf-msg tf-msg-system tf-msg-empty">${escapeHTML(t("capture.session.idle"))}</li>`;
  } else {
    list.innerHTML = messages.map((msg) => {
      const cls = captureRoleClass(msg.role);
      const roleLabel = msg.role ? escapeHTML(msg.role) : "";
      const body = renderCaptureBubbleBody(msg);
      const meta = msg.meta ? `<div class="tf-msg-meta">${escapeHTML(msg.meta)}</div>` : "";
      return `<li class="tf-msg ${cls}" data-role="${roleLabel}">${body}${meta}</li>`;
    }).join("");
  }
  list.scrollTop = list.scrollHeight;
  const finish = $("#capture-finish");
  if (finish) finish.disabled = !state.capture.sessionId;
  const previewButton = $("#capture-refresh-preview");
  if (previewButton) previewButton.disabled = !state.capture.sessionId;
  renderCaptureContextPanel();
  renderArchivePreviewPanel();
  renderCaptureLockIndicator();
}

function renderCaptureContextPanel() {
  const node = $("#capture-context-panel");
  if (!node) return;
  const sp = state.capture.activeScratchpad || {};
  const ctx = sp.session_context || sp.SessionContext || {};
  const rows = [];
  if (ctx.topic) rows.push(`<div><span>${escapeHTML(t("capture.context.topic"))}</span><strong>${escapeHTML(ctx.topic)}</strong></div>`);
  if (ctx.goal) rows.push(`<div><span>${escapeHTML(t("capture.context.goal"))}</span><strong>${escapeHTML(ctx.goal)}</strong></div>`);
  if (ctx.candidate_title) rows.push(`<div><span>${escapeHTML(t("capture.context.candidate_title"))}</span><strong>${escapeHTML(ctx.candidate_title)}</strong></div>`);
  if (Array.isArray(ctx.candidate_tags) && ctx.candidate_tags.length) {
    rows.push(`<div><span>${escapeHTML(t("capture.context.tags"))}</span><strong>${ctx.candidate_tags.map((tag) => `<span class="tf-chip">${escapeHTML(tag)}</span>`).join(" ")}</strong></div>`);
  }
  if (ctx.candidate_summary) rows.push(`<div><span>${escapeHTML(t("capture.context.summary"))}</span><p>${escapeHTML(ctx.candidate_summary)}</p></div>`);
  if (Array.isArray(ctx.open_questions) && ctx.open_questions.length) {
    rows.push(`<div><span>${escapeHTML(t("capture.context.questions"))}</span><ul>${ctx.open_questions.map((item) => `<li>${escapeHTML(item)}</li>`).join("")}</ul></div>`);
  }
  if (Array.isArray(ctx.conflicts) && ctx.conflicts.length) {
    rows.push(`<div><span>${escapeHTML(t("capture.context.conflicts"))}</span><ul>${ctx.conflicts.map((item) => `<li>${escapeHTML(item)}</li>`).join("")}</ul></div>`);
  }
  node.innerHTML = rows.length ? rows.join("") : escapeHTML(t("capture.context.empty"));
}

function renderArchivePreviewPanel() {
  const node = $("#capture-archive-preview");
  if (!node) return;
  const preview = state.capture.archivePreview;
  if (!preview) {
    node.innerHTML = escapeHTML(t("capture.archive.preview_empty"));
    return;
  }
  const tags = Array.isArray(preview.tags) && preview.tags.length
    ? `<div class="tf-capture-tags">${preview.tags.map((tag) => `<span class="tf-chip">${escapeHTML(tag)}</span>`).join("")}</div>`
    : "";
  const links = Array.isArray(preview.source_links) && preview.source_links.length
    ? `<ul>${preview.source_links.map((link) => `<li><a href="${escapeHTML(link)}" target="_blank" rel="noreferrer">${escapeHTML(link)}</a></li>`).join("")}</ul>`
    : "";
  const diff = preview.diff
    ? `<details class="tf-capture-expansion" open>
        <summary>${escapeHTML(t("capture.archive.diff"))}</summary>
        <div class="tf-description-list">
          <div><span>${escapeHTML(t("capture.archive.changed_fields"))}</span><strong>${escapeHTML((preview.diff.changed_fields || []).join(", ") || "-")}</strong></div>
        </div>
        ${renderDiff(preview.diff.before || "", preview.diff.after || "")}
      </details>`
    : "";
  node.innerHTML = `<div class="tf-capture-preview-body">
    <strong>${escapeHTML(preview.title || t("capture.archive.untitled"))}</strong>
    <div class="topic-meta">${escapeHTML(t("capture.archive.strategy"))}: ${escapeHTML(preview.strategy || "new")}</div>
    ${tags}
    <div class="markdown-rendered">${renderMarkdown(preview.body || "")}</div>
    ${links ? `<div class="tf-capture-section"><div class="tf-capture-section-title">${escapeHTML(t("capture.archive.source_links"))}</div>${links}</div>` : ""}
    ${diff}
  </div>`;
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

function loadCaptureSessions() {
  try {
    const raw = localStorage.getItem(captureSessionStorageKey());
    if (!raw) return [];
    const parsed = JSON.parse(raw);
    if (!Array.isArray(parsed)) return [];
    return parsed;
  } catch (_) { return []; }
}

function saveCaptureSessions() {
  try {
    localStorage.setItem(captureSessionStorageKey(), JSON.stringify(state.capture.sessions || []));
  } catch (_) { /* ignore */ }
}

function rememberCaptureSession(session) {
  if (!session) return;
  const list = state.capture.sessions || [];
  const filtered = list.filter((item) => item.sessionId !== session.sessionId);
  filtered.unshift({ ...session, updatedAt: new Date().toISOString() });
  state.capture.sessions = filtered.slice(0, 12);
  saveCaptureSessions();
  renderCaptureSessionsDrawer();
}

function renderCaptureSessionsDrawer() {
  const list = $("#capture-sessions-list");
  if (!list) return;
  // Local "sessions" cache is augmented with the server-side
  // scratchpad summaries (which may include scratchpads from other
  // tabs / a prior server lifetime). The remote list is the
  // source of truth; the local list is for fast client-side
  // boot. We render them in two sections so the user can see
  // what's been freshly staged on the server vs what's only in
  // localStorage.
  const local = state.capture.sessions || [];
  if (local.length === 0) {
    list.innerHTML = `<li class="tf-empty">${escapeHTML(t("empty.no_capture"))}</li>`;
  } else {
    list.innerHTML = local.map((session) => {
      const label = session.title || session.thoughtId || session.sessionId;
      const archived = session.thoughtId ? `<span class="tf-pill tf-pill--success">${escapeHTML(t("capture.drawer.archived"))}</span>` : `<span class="tf-pill tf-pill--draft">${escapeHTML(t("capture.drawer.draft"))}</span>`;
      return `<li class="tf-sessions-item" data-session-id="${escapeHTML(session.sessionId)}">
        <button class="tf-btn tf-sessions-open" type="button" data-session-id="${escapeHTML(session.sessionId)}">
          <span class="tf-sessions-label">${escapeHTML(label)}</span>
          <span class="tf-sessions-meta">${archived} <span class="tf-text-secondary">${escapeHTML((session.updatedAt || "").slice(0, 19).replace("T", " "))}</span></span>
        </button>
      </li>`;
    }).join("");
  }
  list.querySelectorAll(".tf-sessions-open").forEach((button) => {
    button.addEventListener("click", () => switchCaptureSession(button.dataset.sessionId));
  });
  // Always re-fetch the server-side list to keep the drawer in
  // sync with cross-tab scratchpads and crashes that left orphan
  // files behind. The fetch is best-effort: failure leaves the
  // local cache rendered.
  refreshCaptureSessionsFromServer();
}

async function refreshCaptureSessionsFromServer() {
  let response;
  try {
    response = await api("/api/capture/sessions", { method: "GET" });
  } catch (_) {
    return null;
  }
  const summaries = response.summaries || response.Summaries || [];
  if (Array.isArray(summaries) && summaries.length > 0) {
    // Project server summaries into the local session shape so the
    // existing switchCaptureSession / rememberCaptureSession code
    // path can route to them.
    const existing = new Map((state.capture.sessions || []).map((item) => [item.sessionId, item]));
    for (const summary of summaries) {
      if (!summary || !summary.session_id) continue;
      const id = summary.session_id;
      if (existing.has(id)) continue;
      state.capture.sessions.unshift({
        sessionId: id,
        thoughtId: summary.committed_thought_id || "",
        title: summary.title || id,
        updatedAt: summary.updated_at || new Date().toISOString(),
        source: "server",
      });
    }
    state.capture.sessions = (state.capture.sessions || []).slice(0, 24);
    saveCaptureSessions();
    // Re-render only the list (skip the recursive server fetch).
    redrawCaptureSessionsList();
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
  if (state.capture.activeScratchpad && state.capture.activeScratchpad.session_id) {
    return;
  }
  let lastActiveID = "";
  try {
    lastActiveID = await refreshCaptureSessionsFromServer();
  } catch (_) {
    return;
  }
  if (!lastActiveID) return;
  let detail;
  try {
    detail = await api(`/api/capture/sessions/${encodeURIComponent(lastActiveID)}`);
  } catch (_) {
    return;
  }
  if (!detail || !detail.session_id) return;
  // Server may have marked the scratchpad committed in the gap
  // between list and get — drop it instead of opening an empty
  // bubble the user can't chat into.
  if ((detail.committed_thought_id || "").trim() !== "") return;
  state.capture.activeScratchpad = detail;
  state.capture.archivePreview = detail.archive_preview || null;
  state.capture.sessionId = detail.session_id;
  // Restore the chat history verbatim so the user sees the same
  // composer state they had when they refreshed.
  state.capture.messages = Array.isArray(detail.messages) ? detail.messages.slice() : [];
  if (typeof renderCaptureConversation === "function") {
    renderCaptureConversation();
  }
}

function redrawCaptureSessionsList() {
  const list = $("#capture-sessions-list");
  if (!list) return;
  const sessions = state.capture.sessions || [];
  if (sessions.length === 0) {
    list.innerHTML = `<li class="tf-empty">${escapeHTML(t("empty.no_capture"))}</li>`;
    return;
  }
  list.innerHTML = sessions.map((session) => {
    const label = session.title || session.thoughtId || session.sessionId;
    const archived = session.thoughtId ? `<span class="tf-pill tf-pill--success">${escapeHTML(t("capture.drawer.archived"))}</span>` : `<span class="tf-pill tf-pill--draft">${escapeHTML(t("capture.drawer.draft"))}</span>`;
    return `<li class="tf-sessions-item" data-session-id="${escapeHTML(session.sessionId)}">
      <button class="tf-btn tf-sessions-open" type="button" data-session-id="${escapeHTML(session.sessionId)}">
        <span class="tf-sessions-label">${escapeHTML(label)}</span>
        <span class="tf-sessions-meta">${archived} <span class="tf-text-secondary">${escapeHTML((session.updatedAt || "").slice(0, 19).replace("T", " "))}</span></span>
      </button>
    </li>`;
  }).join("");
  list.querySelectorAll(".tf-sessions-open").forEach((button) => {
    button.addEventListener("click", () => switchCaptureSession(button.dataset.sessionId));
  });
}

function openCaptureSessionsDrawer() {
  const drawer = $("#capture-sessions-drawer");
  if (!drawer) return;
  drawer.hidden = false;
  const toggle = $("#capture-sessions-toggle");
  if (toggle) toggle.setAttribute("aria-expanded", "true");
  const panel = drawer.querySelector(".tf-drawer-panel");
  if (panel) trapFocus(panel, toggle);
  renderCaptureSessionsDrawer();
}

function closeCaptureSessionsDrawer() {
  const drawer = $("#capture-sessions-drawer");
  if (!drawer) return;
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
  state.capture.sessionId = session.sessionId;
  state.capture.activeThoughtId = session.thoughtId || "";
  state.capture.messages = Array.isArray(session.messages) ? session.messages.slice() : [];
  api(`/api/capture/sessions/${encodeURIComponent(session.sessionId)}`)
    .then((detail) => {
      state.capture.activeScratchpad = detail;
      state.capture.archivePreview = detail.archive_preview || null;
      state.capture.messages = Array.isArray(detail.messages) ? detail.messages.slice() : state.capture.messages;
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

function refreshActiveCaptureThought() {
  if (!state.capture.activeThoughtId) return;
  api(`/api/thoughts/${encodeURIComponent(state.capture.activeThoughtId)}`)
    .then((snapshot) => {
      state.capture.activeSnapshot = snapshot;
      renderCaptureConversation();
    })
    .catch(() => { /* offline / 404 — leave the cache in place */ });
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

// stageScratchpadTurn routes a user turn into the current capture
// session. Plain text is appended through /api/capture/sessions*;
// recognized commands update context, open a new session, or build
// an archive preview.
async function stageScratchpadTurn(text) {
  const parsed = parseCaptureCommand(text);
  if (parsed) {
    await dispatchCaptureCommand(text);
    return;
  }
  await appendSessionMessage(text);
}

async function appendSessionMessage(text) {
  let scratchpad;
  if (!state.capture.activeScratchpad || !state.capture.activeScratchpad.session_id) {
    const headers = state.capture.sessionId ? { "X-Session-Id": state.capture.sessionId } : {};
    scratchpad = await api("/api/capture/sessions", {
      method: "POST",
      headers,
      body: JSON.stringify({ content: text }),
    });
  } else {
    scratchpad = await api(`/api/capture/sessions/${encodeURIComponent(state.capture.sessionId)}/messages`, {
      method: "POST",
      body: JSON.stringify({ role: "user", text }),
    });
  }
  if (!scratchpad || !scratchpad.session_id) throw new Error(t("toast.request_failed"));
  const previousThoughtId = state.capture.activeThoughtId || "";
  state.capture.activeScratchpad = scratchpad;
  state.capture.sessionId = scratchpad.session_id;
  state.capture.archivePreview = scratchpad.archive_preview || null;
  const linkedThoughtId = scratchpad.committed_thought_id || "";
  state.capture.activeThoughtId = linkedThoughtId;
  rememberCaptureSession({
    sessionId: scratchpad.session_id,
    thoughtId: linkedThoughtId,
    title: scratchpad.session_context?.candidate_title || scratchpad.title || scratchpad.session_id,
    messages: state.capture.messages,
  });
  appendCaptureMessage({ role: "system", text: t("capture.command.noted") });
  // When the scratchpad transitions from "no committed thought" to
  // "anchored to thought-X" (e.g. reopening a previously-archived
  // session and then dropping another message, or the legacy
  // composer-immediately-commits flow), surface the thought anchor
  // bubble + a fresh snapshot so the chat picks up the
  // refine/index/topic chips in place.
  if (linkedThoughtId && previousThoughtId !== linkedThoughtId) {
    appendCaptureMessage({ role: "ai", thoughtId: linkedThoughtId, text: t("capture.command.committed", { id: linkedThoughtId }) });
    refreshActiveCaptureThought();
  }
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

  const expansionSections = buildCaptureExpansionSections(thought, {
    relatedIDs, suggestedTopicIDs, urlFollowups, expansionPlan, expandError,
  });

  return `<div class="tf-suggestion-card">
    <strong class="tf-capture-title">${title || id}</strong>
    <div class="topic-meta">${id}${captureStatus ? ` · ${captureStatus}` : ""}</div>
    <div class="tf-capture-status-row">${statusChips.join("")}</div>
    ${warning}
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
      const cn = /^(?:归到?|放到?|加入)\s*(.+?)(?:专题)?$/.exec(text);
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
    // "归档" / "保存" / "提交" / "落档" / "commit" / "save" — single
    // whole-line match. Anchored with ^...$ so a sentence like
    // "我想 commit 一段代码" is NOT misclassified as the commit
    // command; it falls through to the text body and is appended to
    // the scratchpad as a normal user turn.
    match: (text) => (/^(归档|保存|提交|落档|commit|save|存档)\s*$/i.test(text)
      ? { kind: "commit" }
      : null),
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
    await previewArchive({ intent: "llm" });
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
    await previewArchive({ intent: "llm" });
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
  const sp = state.capture.activeScratchpad;
  if (!sp) {
    appendCaptureMessage({ role: "system", text: t("toast.request_failed") });
    return;
  }
  const ctx = mergeSessionContextDraft(sp.session_context || {}, draft, sp);
  const next = await api(`/api/capture/sessions/${encodeURIComponent(state.capture.sessionId)}/context`, {
    method: "POST",
    body: JSON.stringify(ctx),
  });
  state.capture.activeScratchpad = next;
  state.capture.archivePreview = next.archive_preview || null;
  appendCaptureMessage({
    role: "ai",
    text: formatScratchpadFeedback(draft),
  });
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

async function previewArchive({ intent = "menu", strategy = "" } = {}) {
  if (!state.capture.sessionId) {
    appendCaptureMessage({ role: "system", text: t("toast.request_failed") });
    return;
  }
  try {
    await api(`/api/capture/sessions/${encodeURIComponent(state.capture.sessionId)}/intent`, {
      method: "POST",
      body: JSON.stringify({ intent }),
    });
    const suffix = strategy ? `?strategy=${encodeURIComponent(strategy)}` : "";
    const result = await api(`/api/capture/sessions/${encodeURIComponent(state.capture.sessionId)}/archive/preview${suffix}`, {
      method: "GET",
    });
    state.capture.archivePreview = result.preview || null;
    if (state.capture.activeScratchpad) {
      state.capture.activeScratchpad.archive_preview = state.capture.archivePreview;
    }
    appendCaptureMessage({ role: "ai", text: t("capture.archive.preview_ready") });
    renderCaptureConversation();
    const ok = await confirmAction(t("capture.archive.confirm_title"), t("capture.archive.confirm_message"));
    if (ok) await commitScratchpad();
  } catch (error) {
    appendCaptureMessage({ role: "system", text: error.message || t("toast.request_failed") });
  }
}

async function commitScratchpad() {
  if (!state.capture.sessionId) {
    appendCaptureMessage({ role: "system", text: t("toast.request_failed") });
    return;
  }
  let result;
  try {
    const preview = state.capture.archivePreview || {};
    result = await api(`/api/capture/sessions/${encodeURIComponent(state.capture.sessionId)}/archive`, {
      method: "POST",
      body: JSON.stringify({ strategy: preview.strategy || "new", thought_id: preview.thought_id || "", confirmed: true }),
    });
  } catch (error) {
    appendCaptureMessage({ role: "system", text: error.message || t("toast.request_failed") });
    return;
  }
  const thoughtId = result.thought_id || result.ThoughtID || (result.thought && (result.thought.id || result.thought.ID)) || "";
  if (thoughtId) {
    state.capture.activeThoughtId = thoughtId;
    if (window.tflowSessionLock) {
      window.tflowSessionLock.acquire(thoughtId, state.capture.sessionId);
    }
    appendCaptureMessage({ role: "ai", thoughtId, text: t("capture.command.committed", { id: thoughtId }) });
  } else {
    appendCaptureMessage({ role: "ai", text: t("capture.command.committed_no_id") });
  }
  state.capture.archivePreview = null;
  if (state.capture.activeScratchpad) {
    state.capture.activeScratchpad.committed_thought_id = thoughtId;
    state.capture.activeScratchpad.archive_preview = null;
  }
  rememberCaptureSession({
    sessionId: state.capture.sessionId,
    thoughtId,
    title: thoughtId,
    messages: state.capture.messages,
  });
  refreshActiveCaptureThought();
}

// openNewCaptureSession mints a fresh capture session and optionally
// deletes the previous draft. The local state is reset to match the
// new session so the chat composer is in sync.
async function openNewCaptureSession() {
  let next;
  try {
    next = await api("/api/capture/sessions", {
      method: "POST",
      body: JSON.stringify({ prev_session_id: state.capture.sessionId }),
    });
  } catch (error) {
    appendCaptureMessage({ role: "system", text: error.message || t("toast.request_failed") });
    return;
  }
  state.capture.sessionId = (next && (next.session_id || next.SessionID)) || newCaptureSessionId();
  state.capture.activeThoughtId = "";
  state.capture.activeSnapshot = null;
  state.capture.activeScratchpad = next || null;
  state.capture.archivePreview = null;
  state.capture.suggestion = null;
  state.capture.messages = [];
  if (window.tflowSessionLock && state.capture.activeThoughtId) {
    window.tflowSessionLock.release(state.capture.activeThoughtId, state.capture.sessionId);
  }
  appendCaptureMessage({ role: "system", text: t("capture.command.new_session_started") });
  refreshActiveCaptureThought();
}

async function patchActiveThought(patch) {
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
      snapshot = await api(`/api/thoughts/${encodeURIComponent(thoughtId)}`, {
        method: "PATCH",
        headers: { "Content-Type": "application/json", "X-Session-Id": sessionId },
        body: JSON.stringify(patch),
      });
      lastError = null;
      break;
    } catch (error) {
      lastError = error;
      if (error && error.code === "thoughtflow.capture.refining" && attempt === 0) {
        await new Promise((resolve) => setTimeout(resolve, 1200));
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

function finishCaptureSession() {
  const thoughtId = state.capture.activeThoughtId;
  if (window.tflowSessionLock) {
    window.tflowSessionLock.release(thoughtId, state.capture.sessionId);
  }
  state.capture.sessionId = "";
  state.capture.activeThoughtId = "";
  state.capture.activeSnapshot = null;
  state.capture.activeScratchpad = null;
  state.capture.archivePreview = null;
  state.capture.messages = [];
  appendCaptureMessage({ role: "system", text: t("capture.session.closed") });
  renderCaptureConversation();
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
  if (event) event.preventDefault();
  const query = new URLSearchParams();
  query.set("q", $("#search-query").value.trim());
  query.set("page", "1");
  query.set("page_size", "20");
  const topicID = $("#search-topic-id").value.trim() || state.activeTopicId;
  if (topicID) query.set("topic_id", topicID);
  const tags = csv($("#search-tags").value);
  if (tags.length > 0) query.set("tags", tags.join(","));
  const response = await api(`/api/search?${query.toString()}`);
  state.lastResults = response.results || [];
  renderResults(response);
  // URL reflects the submitted query, not the typing-in-progress value.
  if (state.route?.page === "search") syncHash();
}

function resetSearchFilters() {
  $("#search-tags").value = "";
  $("#search-topic-id").value = "";
  runSearch().catch((error) => toast(error.message));
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
  list.querySelectorAll("[data-basket-id]").forEach((button) => {
    button.addEventListener("click", () => addToComposeBasket([button.dataset.basketId]));
  });
  list.querySelectorAll("[data-copy-path]").forEach((button) => {
    button.addEventListener("click", () => copyPath(button.dataset.copyPath));
  });
  list.querySelectorAll("[data-weave-id]").forEach((button) => {
    button.addEventListener("click", () => previewWeave(button.dataset.weaveId).catch((error) => toast(error.message)));
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
  // SearchResultView 投影只暴露 thought_id / title / snippet / score / tags /
  // topics / path,不再展示 explain 与 keyword/semantic/recency 拆分。
  return `
    <article class="result-item">
      <div class="result-row">
        <input type="checkbox" data-select-id="${escapeHTML(thoughtID)}" ${checked} aria-label="${escapeHTML(t("search.result.select_aria"))}">
        <div>
          <strong><button class="link-button" data-preview-id="${escapeHTML(thoughtID)}" type="button">${escapeHTML(item.title || thoughtID)}</button></strong>
          <div class="result-meta">${escapeHTML(item.snippet || "")}</div>
          <div class="score-line">
            ${item.score !== undefined ? `<span class="pill green">${t("search.score_label")} ${score(item.score)}</span>` : ""}
            ${tags}
          </div>
          <div class="tf-action-row">
            <button class="mini-button" data-open-id="${escapeHTML(thoughtID)}" type="button">${escapeHTML(t("search.result.open"))}</button>
            <button class="mini-button" data-basket-id="${escapeHTML(thoughtID)}" type="button">${escapeHTML(t("search.result.add_basket"))}</button>
            <button class="mini-button" data-weave-id="${escapeHTML(thoughtID)}" ${options.activeTopicId ? "" : "disabled"} type="button">${escapeHTML(t("search.result.review_weave"))}</button>
            ${item.path ? `<button class="mini-button" data-copy-path="${escapeHTML(item.path)}" type="button">${escapeHTML(t("search.result.copy_path"))}</button><code>${escapeHTML(item.path)}</code>` : ""}
          </div>
        </div>
      </div>
    </article>
  `;
}

function copyPath(path) {
  if (!path) return;
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(path).then(() => toast(t("toast.path_copied"))).catch(() => toast(path));
    return;
  }
  toast(path);
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

function addToComposeBasket(thoughtIds) {
  for (const thoughtId of thoughtIds || []) {
    if (thoughtId) state.composeBasket.add(thoughtId);
  }
  persistBasket();
  broadcastBasketChange();
  renderComposeBasket();
  toast(t("toast.basket_add", { n: state.composeBasket.size }));
}

function clearSearchSelection() {
  state.selectedThoughts.clear();
  renderResults({ results: state.lastResults });
  persistRouteDebounced();
}

function clearComposeBasket() {
  state.composeBasket.clear();
  persistBasket();
  broadcastBasketChange();
  renderComposeBasket();
}

function renderComposeBasket() {
  const ids = Array.from(state.composeBasket);
  const count = $("#compose-source-count");
  const list = $("#compose-source-list");
  const clear = $("#clear-compose-basket");
  if (count) {
    const rendered = t("compose.source_count", { n: ids.length });
    // Keep the data-n attribute in sync so a later tApply() doesn't reset
    // the count back to the static value baked into the HTML.
    count.setAttribute("data-n", String(ids.length));
    count.textContent = rendered;
  }
  if (clear) clear.disabled = ids.length === 0;
  if (list) {
    list.innerHTML = ids.length === 0
      ? escapeHTML(t("compose.empty_sources"))
      : ids.map((id) => `<span class="pill">${escapeHTML(id)}</span>`).join("");
  }
}

async function previewThought(thoughtId, options = {}) {
  const snapshot = await api(`/api/thoughts/${encodeURIComponent(thoughtId)}`);
  state.activeThoughtId = thoughtId;
  state.activeThoughtSnapshot = snapshot;
  const thought = snapshot.thought;
  const content = snapshot.content || {};
  const sections = [
    `# ${thought.display_title || thought.user_title || thought.id}`,
    "",
    `status: ${thought.refine_status} / ${thought.index_status} / ${thought.topic_status}`,
    `path: ${thought.path}`,
    "",
    `## ${t("thoughts.preview_summary")}`,
    thought.summary || t("compose.empty"),
    "",
    `## ${t("thoughts.preview_original")}`,
    content.original || "",
    "",
    content.extracted_content ? `## ${t("thoughts.preview_extracted")}\n${content.extracted_content}` : "",
    content.links ? `## ${t("thoughts.preview_links")}\n${content.links}` : "",
    (snapshot.jobs || []).length > 0 ? `## ${t("thoughts.preview_jobs")}\n${(snapshot.jobs || []).map((job) => `- ${job.id} (${job.status})`).join("\n")}` : "",
  ];
  // Post-refine expansion: surface 4 sub-sections written by the
  // expander module. Each section is omitted when the corresponding
  // field is empty so freshly-captured thoughts don't show skeleton
  // placeholders before the pipeline finishes. A dedicated "pending"
  // hint is shown only while the expansion is still running.
  sections.push(appendExpansionSections(thought));
  const html = renderMarkdown(sections.filter(Boolean).join("\n"));
  $("#thought-preview").innerHTML = html;
  const drawer = $("#thought-drawer-content");
  if (drawer) drawer.innerHTML = html;
  $("#drawer-add-compose").disabled = false;
  $("#retry-refine").disabled = false;
  if (options.drawer) openDrawer("thought-drawer");
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

async function loadThoughtByID(event) {
  if (event) event.preventDefault();
  const thoughtID = $("#thought-id").value.trim();
  if (!thoughtID) {
    toast(t("toast.thought_id_required"));
    return;
  }
  window.location.hash = `#/notes?id=${encodeURIComponent(thoughtID)}`;
  await previewThought(thoughtID);
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
  const thoughtIds = Array.from(state.composeBasket);
  if (thoughtIds.length === 0) {
    toast(t("toast.add_sources_first"));
    return;
  }
  const sources = thoughtIds.map((id) => ({ source_type: "thought", source_id: id }));
  const draft = await api("/api/compose/drafts", {
    method: "POST",
    body: JSON.stringify({
      sources,
      selected_thought_ids: thoughtIds,
      goal: $("#compose-goal").value.trim(),
      format: $("#compose-format").value,
    }),
  });
  state.composeDraft = draft;
  $("#compose-output").value = renderComposeDraft(draft);
  $("#save-compose").disabled = (draft.status || "draft") !== "draft";
  closeDrawer("compose-create-drawer");
  await loadComposeDrafts();
  window.location.hash = "#/compose";
}

async function loadComposeDrafts() {
  state.composeDrafts = await api("/api/compose/drafts");
  renderComposeDrafts();
  renderSidebarBadges();
}

async function loadComposeDraft(draftId) {
  if (!draftId) return;
  const draft = await api(`/api/compose/drafts/${encodeURIComponent(draftId)}`);
  state.composeDraft = draft;
  for (const source of draft.sources || []) {
    if (source && source.source_type === "thought" && source.source_id) {
      state.composeBasket.add(source.source_id);
    }
  }
  persistBasket();
  renderComposeBasket();
  $("#compose-goal").value = draft.goal || "";
  $("#compose-format").value = draft.format || t("compose.format.summary");
  $("#compose-output").value = renderComposeDraft(draft);
  $("#save-compose").disabled = (draft.status || "draft") !== "draft";
  renderComposeDrafts();
}

async function previewWeave(thoughtId) {
  if (!state.activeTopicId) {
    toast(t("toast.select_topic_first"));
    return;
  }
  const proposal = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/weave-preview`, {
    method: "POST",
    body: JSON.stringify({ thought_id: thoughtId }),
  });
  state.weaveProposal = proposal;
  $("#weave-review-title").textContent = t("topics.weave_title", { id: proposal.thought_id });
  $("#weave-diff").innerHTML = renderDiff(proposal.diff || []);
  $("#weave-document").value = proposal.proposed_document || "";
  $("#accept-weave").disabled = false;
  await loadWeaveProposals(state.activeTopicId);
  navigateTopic(state.activeTopicId, true);
}

async function loadWeaveProposal(proposalId) {
  if (!state.activeTopicId || !proposalId) return;
  const proposal = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/weave-proposals/${encodeURIComponent(proposalId)}`);
  state.weaveProposal = proposal;
  $("#weave-review-title").textContent = t("topics.weave_title", { id: proposal.thought_id });
  $("#weave-diff").innerHTML = renderDiff(proposal.diff || []);
  $("#weave-document").value = proposal.accepted_document || proposal.proposed_document || "";
  $("#accept-weave").disabled = (proposal.status || "pending") !== "pending";
  renderWeaveProposals();
  navigateTopic(state.activeTopicId, true);
}

async function acceptWeave() {
  if (!state.weaveProposal) {
    toast(t("toast.create_weave_first"));
    return;
  }
  const confirmed = await confirmAction(t("topics.weave_confirm_title"), t("topics.weave_confirm_message"));
  if (!confirmed) return;
  const document = $("#weave-document").value.trim();
  if (!document) {
    toast(t("toast.proposed_document_required"));
    return;
  }
  const detail = await api(`/api/topics/${encodeURIComponent(state.weaveProposal.topic_id)}/weave-accept`, {
    method: "POST",
    body: JSON.stringify({
      proposal_id: state.weaveProposal.id,
      thought_id: state.weaveProposal.thought_id,
      document,
    }),
  });
  toast(t("toast.weave_accepted"));
  state.weaveProposal = null;
  $("#accept-weave").disabled = true;
  $("#weave-diff").innerHTML = `<div class="topic-meta">${escapeHTML(t("empty.pending_weave"))}</div>`;
  $("#weave-document").value = "";
  await loadTopics();
  await loadWeaveProposals(detail.topic.id);
  navigateTopic(detail.topic.id);
}

function renderComposeDraft(draft) {
  const links = (draft.source_links || []).filter(Boolean);
  let content = draft.content || "";
  const missing = links.filter((link) => !content.includes(link));
  if (missing.length === 0) return content;
  return `${content}\n\n### Sources\n\n${missing.map((link) => `- [[${link}]]`).join("\n")}`;
}

async function saveComposeDraft() {
  if (!state.composeDraft) {
    toast(t("toast.create_draft_first"));
    return;
  }
  const confirmed = await confirmAction(t("compose.confirm_title"), t("compose.confirm_message"));
  if (!confirmed) return;
  const content = $("#compose-output").value.trim();
  const result = await api(`/api/compose/drafts/${encodeURIComponent(state.composeDraft.id)}/save`, {
    method: "POST",
    body: JSON.stringify({
      content,
      title: state.composeDraft.goal || $("#compose-goal").value.trim(),
      tags: [],
    }),
  });
  toast(t("toast.saved", { id: result.thought.id }));
  state.selectedThoughts.clear();
  state.composeBasket.clear();
  renderComposeBasket();
  $("#compose-save-result").innerHTML = `<a class="tf-btn" href="#/notes?id=${encodeURIComponent(result.thought.id)}">${escapeHTML(t("compose.view_saved"))}</a>`;
  $("#save-compose").disabled = true;
  state.composeDraft = null;
  await loadComposeDrafts();
  window.setTimeout(() => runSearch().catch((error) => toast(error.message)), 1000);
}

async function refreshTopic() {
  if (!state.activeTopicId) return;
  const confirmed = await confirmAction(t("topics.refresh"), t("topics.refresh") + ".");
  if (!confirmed) return;
  const job = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/refresh`, { method: "POST", body: "{}" });
  toast(t("toast.refresh_queued", { id: job.id }));
  // PR3: jobs page is gone; the toast is the only acknowledgement.
  state.activeJobId = job.id;
}

function connectEvents() {
  // PR3: events flow into the settings drawer (full stream with filters)
  // and the dashboard summary. The notes runtime card reuses the same
  // DOM as the dashboard summary by reading #dashboard-events via cloning.
  const drawerList = $("#settings-drawer-event-list");
  const dashboardList = $("#dashboard-events");
  const source = new EventSource("/api/events");
  source.onmessage = (event) => appendEvent("message", event.data);
  [
    "thought.captured",
    "thought.refined",
    "thought.refine_failed",
    "thought.patched",
    "thought.expanded",
    "search.index_updated",
    "topic.updated",
    "topic.matched",
    "git.commit_succeeded",
    "git.commit_failed",
    "job.updated",
  ].forEach((type) => {
    source.addEventListener(type, (event) => {
      appendEvent(type, event.data);
      if (type === "topic.updated") loadTopics().catch(() => {});
      if (type === "thought.captured") loadMetrics().catch(() => {});
      if ((type === "thought.refined" || type === "thought.patched" || type === "thought.refine_failed")
          && state.capture.activeThoughtId) {
        try {
          const payload = JSON.parse(event.data);
          if (payload?.resource_id === state.capture.activeThoughtId) {
            if (type === "thought.refine_failed") {
              appendCaptureMessage({ role: "system", text: t("toast.retry_refine_queued", { id: payload?.resource_id || "" }).replace("已加入重试队列", "refine 失败") });
            } else {
              appendCaptureMessage({ role: "system", text: t("capture.session.saved_path", { id: payload?.resource_id || "" }) });
            }
            refreshActiveCaptureThought();
          }
        } catch (_) { /* malformed event payload */ }
      }
      if (type === "thought.expanded") {
        try {
          const payload = JSON.parse(event.data);
          const expandedID = payload?.resource_id;
          if (!expandedID) return;
          // Refresh the open thought preview if it matches — both the
          // notes-page inline preview and the drawer-backed preview
          // read the same `state.activeThoughtId`, so a single
          // re-render covers both.
          if (state.activeThoughtId === expandedID) {
            previewThought(expandedID).catch((error) => toast(error.message));
          }
          // The capture conversation owns its own copy of the snapshot
          // and re-renders thoughtId-bound bubbles in place from the
          // freshest snapshot — so a thought.expanded event simply
          // triggers refreshActiveCaptureThought which fetches and
          // re-renders the ai bubble to surface the new sections.
          if (state.capture.activeThoughtId === expandedID) {
            refreshActiveCaptureThought();
            appendCaptureMessage({ role: "system", text: t("capture.session.expanded", { id: expandedID }) });
          }
        } catch (_) { /* malformed event payload */ }
      }
    });
  });
  source.onerror = () => {
    if (drawerList && drawerList.children.length === 0 && dashboardList && dashboardList.children.length === 0) {
      appendEvent("events", t("toast.sse_reconnecting"));
    }
  };
}

function appendEvent(type, data) {
  // PR3: events are appended to the dashboard summary and to the settings
  // drawer event tab. The notes runtime card is populated on demand by
  // refreshNotesRuntime() so it doesn't churn on every SSE tick.
  const drawerList = $("#settings-drawer-event-list");
  const dashboardList = $("#dashboard-events");
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
  prependEventItem(dashboardList, item.cloneNode(true), 8);
  applyEventFilter();
}

function prependEventItem(list, item, limit = 60) {
  if (!list || !item) return;
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

async function applyRoute(hash = window.location.hash) {
  const route = parseRoute(hash);
  state.route = route;
  // Restore DOM inputs / state from the URL query before rendering, so the
  // first render of the page reflects the URL — no flicker of defaults.
  restoreRoutePage(route.page, route.query);
  renderRoute(route);
  if (route.page === "topics" && route.params.topicId) {
    if (state.activeTopicId !== route.params.topicId || !state.activeTopicDetail) {
      await openTopic(route.params.topicId);
    }
    if (route.query.tab === "proposals") await loadWeaveProposals(route.params.topicId);
  }
  if (route.page === "thoughts" && route.params.thoughtId) {
    $("#thought-id").value = route.params.thoughtId;
    await previewThought(route.params.thoughtId);
  }
  // Live routes: capture, search, topics (incl. detail), notes, compose.
  // settings and jobs are gone — the gear opens a drawer, jobs surface on
  // the notes runtime card. No additional loader needed here.
}

function bind() {
  $("#capture-form")?.addEventListener("submit", (event) => captureThought(event).catch((error) => toast(error.message)));
  $("#capture-composer")?.addEventListener("submit", (event) => submitCaptureComposer(event).catch((error) => toast(error.message)));
  $("#capture-new-session")?.addEventListener("click", () => newCaptureSession());
  $("#capture-finish")?.addEventListener("click", () => finishCaptureSession());
  $("#capture-refresh-preview")?.addEventListener("click", () => previewArchive({ intent: "menu" }));
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
  $("#search-topic-id").addEventListener("input", persistRouteDebounced);
  $("#search-tags").addEventListener("input", persistRouteDebounced);
  $("#search-from").addEventListener("input", persistRouteDebounced);
  $("#search-to").addEventListener("input", persistRouteDebounced);
  $("#search-sort").addEventListener("change", persistRouteDebounced);
  $("#search-explain").addEventListener("change", persistRouteDebounced);
  $("#reset-search").addEventListener("click", () => { resetSearchFilters(); persistRouteDebounced(); });
  $("#thought-form").addEventListener("submit", (event) => loadThoughtByID(event).catch((error) => toast(error.message)));
  $("#thought-id").addEventListener("input", persistRouteDebounced);
  $("#compose-form").addEventListener("submit", (event) => createComposeDraft(event).catch((error) => toast(error.message)));
  $("#settings-drawer-event-type")?.addEventListener("input", () => applyEventFilter());
  $("#settings-drawer-event-resource")?.addEventListener("input", () => applyEventFilter());
  $("#settings-drawer-reset-event-filter")?.addEventListener("click", () => resetEventFilter());
  $("#notes-runtime-refresh")?.addEventListener("click", () => refreshNotesRuntime().catch((error) => toast(error.message)));
  $("#open-events-from-dashboard")?.addEventListener("click", () => {
    openSettingsDrawer("settings-drawer-events");
  });
  $("#save-compose").addEventListener("click", () => saveComposeDraft().catch((error) => toast(error.message)));
  $("#accept-weave").addEventListener("click", () => acceptWeave().catch((error) => toast(error.message)));
  $("#refresh-topics").addEventListener("click", () => loadTopics().catch((error) => toast(error.message)));
  $("#topic-filter").addEventListener("input", () => { renderTopics(); persistRouteDebounced(); });
  $("#topic-auto-filter").addEventListener("change", () => { renderTopics(); persistRouteDebounced(); });
  $("#reset-topic-filter").addEventListener("click", () => { resetTopicFilters(); persistRouteDebounced(); });
  $("#refresh-topic").addEventListener("click", () => refreshTopic().catch((error) => toast(error.message)));
  // Reindex entry removed — the search page no longer exposes a reindex
  // button. /api/system/reindex stays available for ops use; the Web UI
  // surfaces index health through the settings drawer instead.
  $("#open-create-topic").addEventListener("click", () => openDrawer("topic-create-drawer"));
  $("#open-topic-rules").addEventListener("click", () => openDrawer("topic-rules-drawer"));
  $("#open-compose-create").addEventListener("click", () => openDrawer("compose-create-drawer"));
  $("#refresh-compose").addEventListener("click", () => loadComposeDrafts().catch((error) => toast(error.message)));
  $("#add-selected-compose").addEventListener("click", () => {
    addToComposeBasket(Array.from(state.selectedThoughts));
    window.location.hash = "#/compose";
  });
  $("#clear-selected").addEventListener("click", clearSearchSelection);
  $("#clear-compose-basket").addEventListener("click", clearComposeBasket);
  $("#drawer-add-compose").addEventListener("click", () => addToComposeBasket([state.activeThoughtId]));
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
  document.querySelectorAll(".tab").forEach((tab) => tab.addEventListener("click", () => {
    activateTab(tab.dataset.tab, tab.closest(".tf-card"));
    persistRouteDebounced();
    // PR3: the notes runtime card lives behind a collapsible details
    // element on the Runtime tab. Refresh its data the first time the
    // tab opens, then again on the explicit Refresh button.
    if (tab.dataset.tab === "notes-runtime") {
      refreshNotesRuntime().catch(() => {});
    }
  }));
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
  try { renderTopics(); } catch (_) {}
  try { renderComposeBasket(); } catch (_) {}
  try { updateSelectionControls(); } catch (_) {}
  if (state.status) {
    try { renderTopbarStatus(state.status); } catch (_) {}
    try { renderSettingsStatus(state.status); } catch (_) {}
  }
  if (state.weaveProposals || !state.activeTopicId) {
    try { renderWeaveProposals(); } catch (_) {}
  }
  if (state.composeDrafts) {
    try { renderComposeDrafts(); } catch (_) {}
  }
}

async function boot() {
  if (typeof tInit === "function") tInit();
  // Apply static translations to data-i18n elements BEFORE the async loaders
  // run, so the initial paint shows the chosen locale. Dynamic elements
  // (e.g. #system-status, which is set by loadStatus) must not carry
  // data-i18n, otherwise this call would overwrite the live text on every
  // boot and any subsequent locale switch.
  tApply(document);
  syncLanguageSwitcher();
  initTflowBus();
  if (tflowBus) {
    tflowBus.on((message) => {
      if (!message || typeof message !== "object") return;
      if (message.kind === "basket:changed" && Array.isArray(message.ids)) {
        // Another tab updated the basket — adopt the new ids and re-render
        // if they differ from what we have. Avoids an infinite ping-pong
        // because addTo/clear do not fire on the receiving side.
        const current = Array.from(state.composeBasket).sort();
        const incoming = Array.from(message.ids).sort();
        if (current.length === incoming.length && current.every((id, i) => id === incoming[i])) return;
        state.composeBasket = new Set(message.ids.filter(Boolean));
        renderComposeBasket();
      }
    });
  }
  restoreBasket();
  state.capture.sessions = loadCaptureSessions();
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
  // Render the basket counter once the page is reachable. Done here so the
  // rehydrated count shows even before the user opens the compose page.
  if (typeof renderComposeBasket === "function") {
    try { renderComposeBasket(); } catch (_error) { /* noop before render */ }
  }
  if (typeof renderCaptureConversation === "function") {
    try { renderCaptureConversation(); } catch (_error) { /* noop before render */ }
  }
  if (!window.location.hash) window.location.hash = "#/overview";
  state.route = parseRoute(window.location.hash);
  renderRoute();
  await loadStatus();
  await loadTopics();
  await loadComposeDrafts();
  await loadMetrics();
  await runSearch();
  await applyRoute();
  // Land the user back in the most recent uncommitted capture
  // session if there is one, so a refresh doesn't dump them onto
  // an empty composer. The server's last_active_session_id is the
  // authority — we only fall back to the local cache if the
  // server explicitly says there's nothing to rehydrate.
  await rehydrateActiveScratchpad();
  connectEvents();
}

boot().catch((error) => toast(error.message));
