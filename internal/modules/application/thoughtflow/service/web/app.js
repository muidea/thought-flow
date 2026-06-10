const state = {
  route: { page: "dashboard", nav: "dashboard", params: {}, query: {} },
  topics: [],
  activeTopicId: "",
  selectedThoughts: new Set(),
  synthesisBasket: new Set(),
  lastResults: [],
  synthesisDraft: null,
  synthesisDrafts: [],
  activeThoughtId: "",
  activeThoughtSnapshot: null,
  activeTopicDetail: null,
  weaveProposal: null,
  weaveProposals: [],
  status: null,
  metrics: null,
  pendingConfirm: null,
};

let markdownParser = null;

const $ = (selector) => document.querySelector(selector);

function parseRoute(hash) {
  const raw = String(hash || "").replace(/^#\/?/, "");
  const [pathPart, queryPart = ""] = raw.split("?");
  const parts = pathPart.split("/").filter(Boolean);
  const query = Object.fromEntries(new URLSearchParams(queryPart).entries());
  if (parts.length === 0) return { page: "dashboard", nav: "dashboard", params: {}, query };
  if (parts[0] === "topics" && parts[1] && parts[2] === "review") {
    return { page: "topic-review", nav: "topics", params: { topicId: parts[1] }, query };
  }
  if (parts[0] === "topics" && parts[1]) {
    return { page: "topic-detail", nav: "topics", params: { topicId: parts[1] }, query };
  }
  if (parts[0] === "thoughts") {
    return { page: "thoughts", nav: "thoughts", params: { thoughtId: query.id || "" }, query };
  }
  if (parts[0] === "jobs") {
    return { page: "jobs", nav: "jobs", params: { jobId: query.id || "" }, query };
  }
  const known = new Set(["dashboard", "capture", "search", "topics", "synthesis", "settings"]);
  if (known.has(parts[0])) return { page: parts[0], nav: parts[0], params: {}, query };
  return { page: "dashboard", nav: "dashboard", params: {}, query };
}

function navItemClass(route, nav) {
  return route.nav === nav ? "tf-menu-item active" : "tf-menu-item";
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

function openDrawer(id) {
  const drawer = $(`#${id}`);
  if (!drawer) return;
  drawer.classList.add("open");
  drawer.setAttribute("aria-hidden", "false");
}

function closeDrawer(id) {
  const drawer = $(`#${id}`);
  if (!drawer) return;
  drawer.classList.remove("open");
  drawer.setAttribute("aria-hidden", "true");
}

function setButtonLoading(button, loading, loadingLabel) {
  if (!button) return;
  if (loading) {
    button.dataset.label = button.textContent;
    button.textContent = loadingLabel || "Loading";
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
  modal.classList.add("open");
  modal.setAttribute("aria-hidden", "false");
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
    const message = payload.error?.message || response.statusText || "Request failed";
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
  if (!value) return "never";
  const parsed = new Date(value);
  if (Number.isNaN(parsed.getTime())) return "unknown";
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

function createSynthesisBasket(initial = []) {
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
    return '<div class="topic-meta">No document changes.</div>';
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
    list.innerHTML = '<div class="topic-meta">Select a topic to see weave proposals.</div>';
    return;
  }
  if (!state.weaveProposals || state.weaveProposals.length === 0) {
    list.innerHTML = '<div class="topic-meta">No weave proposals for this topic.</div>';
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
            <span>${hunkCount} patch hunks</span>
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

function renderSynthesisDrafts() {
  const list = $("#synthesis-drafts");
  if (!list) return;
  if (!state.synthesisDrafts || state.synthesisDrafts.length === 0) {
    list.innerHTML = '<div class="topic-meta">No synthesis drafts.</div>';
    return;
  }
  list.innerHTML = state.synthesisDrafts
    .map((draft) => {
      const active = state.synthesisDraft?.id === draft.id ? " active" : "";
      const status = draft.status || "draft";
      return `
        <article class="approval-item${active}" data-synthesis-id="${escapeHTML(draft.id)}">
          <strong>${escapeHTML(draft.goal || draft.id)}</strong>
          <div class="topic-meta">
            <span class="pill">${escapeHTML(status)}</span>
            <span>${escapeHTML(draft.format || "summary")}</span>
            <span>${escapeHTML(fmtDate(draft.updated_at || draft.created_at))}</span>
          </div>
        </article>
      `;
    })
    .join("");
  list.querySelectorAll("[data-synthesis-id]").forEach((item) => {
    item.addEventListener("click", () => loadSynthesisDraft(item.dataset.synthesisId).catch((error) => toast(error.message)));
  });
}

async function loadStatus() {
  try {
    const status = await api("/api/system/status");
    state.status = status;
    $("#system-status").textContent = `${status.workspace.id} / ${status.status}`;
    $("#workspace-summary").textContent = status.workspace.root_path || status.workspace.id || "local";
    $("#dashboard-workspace").textContent = status.workspace?.status || status.status;
    $("#dashboard-ai").textContent = status.ai?.status || "unknown";
    $("#dashboard-git").textContent = status.git?.status || "unknown";
    $("#dashboard-search").textContent = status.duckdb?.status || "unknown";
    $("#settings-workspace").textContent = status.workspace?.root_path || status.workspace?.status || "unknown";
    $("#settings-duckdb").textContent = status.duckdb?.path || status.duckdb?.status || "unknown";
    $("#settings-ai").textContent = `${status.ai?.status || "unknown"} · ${status.ai?.chat_model || "local"}`;
    $("#settings-git").textContent = status.git?.error || status.git?.status || "unknown";
    renderTopbarStatus(status);
    renderSettingsStatus(status);
    const dashboardAlert = $("#dashboard-alert");
    if (dashboardAlert) {
      dashboardAlert.hidden = status.status === "ready";
      dashboardAlert.textContent = status.status === "ready" ? "" : `System status is ${status.status}. Open Settings for details.`;
    }
  } catch (error) {
    $("#system-status").textContent = "degraded";
    const dashboardAlert = $("#dashboard-alert");
    if (dashboardAlert) {
      dashboardAlert.hidden = false;
      dashboardAlert.textContent = error.message;
    }
    const alert = $("#settings-degraded");
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
  } catch (error) {
    const node = $("#settings-metrics-json");
    if (node) node.innerHTML = `<div class="tf-alert tf-alert-warning">${escapeHTML(error.message)}</div>`;
  }
}

function renderTopbarStatus(status) {
  const topbar = $("#topbar-status");
  if (!topbar || !status) return;
  const items = [
    ["Workspace", status.workspace?.status],
    ["AI", status.ai?.status],
    ["Git", status.git?.status],
    ["Search", status.duckdb?.status],
  ];
  topbar.innerHTML = items
    .map(([label, value]) => `<span class="${statusBadge(value)}">${escapeHTML(label)} · ${escapeHTML(value || "unknown")}</span>`)
    .join("");
}

function renderSettingsStatus(status) {
  const alert = $("#settings-degraded");
  if (alert) {
    const degraded = status.status && status.status !== "ready";
    alert.hidden = !degraded;
    alert.textContent = degraded ? `System status is ${status.status}. Check workspace, AI, Git, index, background, and events details below.` : "";
  }
  const index = $("#settings-index-detail");
  if (index) {
    index.innerHTML = renderDescription([
      ["DuckDB status", status.duckdb?.status || "unknown"],
      ["DuckDB path", status.duckdb?.path || "unknown"],
      ["Background", status.background?.status || "unknown"],
      ["Events", status.events?.status || "unknown"],
    ]);
  }
  const git = $("#settings-git-detail");
  if (git) {
    git.innerHTML = renderDescription([
      ["Status", status.git?.status || "unknown"],
      ["Repository", status.git?.repository || status.workspace?.root_path || "unknown"],
      ["Dirty", status.git?.dirty === undefined ? "unknown" : String(status.git.dirty)],
      ["Error", status.git?.error || "none"],
    ]);
  }
}

function renderMetrics(metrics) {
  const node = $("#settings-metrics-json");
  if (!node) return;
  const values = metrics.values || {};
  const rows = Object.keys(values)
    .sort()
    .map((key) => [key, String(values[key])]);
  node.innerHTML = rows.length > 0 ? renderDescription(rows) : '<div class="tf-empty">No metrics exposed yet.</div>';
}

async function loadTopics() {
  const topics = await api("/api/topics");
  state.topics = topics || [];
  renderTopics();
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
    $("#topic-title").textContent = "Topic Workspace";
    $("#topic-document").innerHTML = renderMarkdown("Select a topic from the left.");
    $("#rebuild-topic").disabled = true;
    $("#open-topic-rules").disabled = true;
    $("#topic-members").innerHTML = '<div class="tf-empty">No topic selected.</div>';
    $("#topic-rules-summary").innerHTML = "No topic selected.";
    state.weaveProposals = [];
    state.weaveProposal = null;
    renderWeaveProposals();
    list.innerHTML = '<div class="topic-meta">No topics yet.</div>';
    return;
  }
  if (filteredTopics.length === 0) {
    list.innerHTML = '<div class="topic-meta">No topics match the current filters.</div>';
    return;
  }
  list.innerHTML = filteredTopics
    .map((topic) => {
      const active = topic.id === state.activeTopicId ? " active" : "";
      return `
        <article class="topic-item${active}" data-topic-id="${escapeHTML(topic.id)}">
          <strong>${escapeHTML(topic.name)}</strong>
          <div class="topic-meta">${topic.member_count || 0} thoughts · ${topic.word_count || 0} words</div>
          <div class="topic-meta">${escapeHTML(topic.description || "No description")}</div>
          <div class="topic-actions">
            <button class="mini-button" data-topic-open="${escapeHTML(topic.id)}" type="button">Open</button>
            <button class="mini-button" data-topic-review="${escapeHTML(topic.id)}" type="button">Review</button>
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
  list.querySelectorAll("[data-topic-review]").forEach((button) => {
    button.addEventListener("click", () => navigateTopic(button.dataset.topicReview, true));
  });
}

function resetTopicFilters() {
  $("#topic-filter").value = "";
  $("#topic-auto-filter").checked = false;
  renderTopics();
}

function navigateTopic(topicId, review = false) {
  if (!topicId) return;
  window.location.hash = review ? `#/topics/${encodeURIComponent(topicId)}/review` : `#/topics/${encodeURIComponent(topicId)}`;
}

async function openTopic(topicId) {
  if (!topicId) return;
  const detail = await api(`/api/topics/${encodeURIComponent(topicId)}`);
  state.activeTopicId = topicId;
  state.activeTopicDetail = detail;
  $("#topic-title").textContent = detail.topic.name;
  $("#topic-document").innerHTML = renderMarkdown(detail.document || "No topic document.");
  $("#rebuild-topic").disabled = false;
  $("#open-topic-rules").disabled = false;
  $("#open-topic-review").href = `#/topics/${encodeURIComponent(topicId)}/review`;
  $("#back-topic-detail").href = `#/topics/${encodeURIComponent(topicId)}`;
  populateTopicEditor(detail.topic);
  renderTopicMembers(detail.members || []);
  renderTopicRules(detail.topic);
  renderTopics();
  await loadWeaveProposals(topicId);
}

function renderTopicMembers(members) {
  const node = $("#topic-members");
  if (!node) return;
  if (!members || members.length === 0) {
    node.innerHTML = '<div class="tf-empty">No members matched this topic yet.</div>';
    return;
  }
  node.innerHTML = members
    .map((member) => `
      <article class="result-item">
        <div class="result-row">
          <div>
            <strong>${escapeHTML(member.title || member.thought_id || member.id)}</strong>
            <div class="result-meta">${escapeHTML(member.match_type || "match")} · score ${score(member.score)}</div>
            <div class="score-line">
              <button class="mini-button" data-preview-id="${escapeHTML(member.thought_id || member.id)}" type="button">Preview</button>
              <button class="mini-button" data-basket-id="${escapeHTML(member.thought_id || member.id)}" type="button">Add to synthesis</button>
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
    button.addEventListener("click", () => addToSynthesisBasket([button.dataset.basketId]));
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
    ["Keywords any", joinCSV(keywords.any) || "none"],
    ["Keywords all", joinCSV(keywords.all) || "none"],
    ["Keywords exclude", joinCSV(keywords.exclude) || "none"],
    ["Tags any", joinCSV(tags.any) || "none"],
    ["Manual include", joinCSV(rules.manual_include) || "none"],
    ["Manual exclude", joinCSV(rules.manual_exclude) || "none"],
    ["Semantic", semantic.enabled ? `enabled (${semantic.threshold || 0.75})` : "disabled"],
    ["Auto weave", topic.auto_weave === false ? "disabled" : "enabled"],
    ["Outline", outlineText(topic.outline) || "none"],
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
    toast("Topic name is required");
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
  toast("Topic created");
  closeDrawer("topic-create-drawer");
  await loadTopics();
  navigateTopic(topic.id);
}

async function saveTopicRules(event) {
  event.preventDefault();
  if (!state.activeTopicId) {
    toast("Select a topic first");
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
  toast("Topic rules saved");
  closeDrawer("topic-rules-drawer");
  await loadTopics();
  navigateTopic(topic.id);
}

async function captureThought(event) {
  event.preventDefault();
  const submit = $("#capture-submit");
  const type = new FormData(event.target).get("type") || "text";
  const content = $("#capture-content").value.trim();
  const url = $("#capture-url").value.trim();
  if (type === "text" && !content) {
    toast("Capture content is required");
    return;
  }
  if (type === "url" && !url) {
    toast("Capture URL is required");
    return;
  }
  const command = {
    type,
    title: $("#capture-title").value.trim(),
    tags: csv($("#capture-tags").value),
    topic_hints: csv($("#capture-topic-hints").value),
  };
  if (type === "url") {
    command.url = url;
    if (content) command.content = content;
  } else {
    command.content = content;
  }
  setButtonLoading(submit, true, "Capturing");
  try {
    const result = await api("/api/thoughts", {
      method: "POST",
      body: JSON.stringify(command),
    });
    $("#capture-content").value = "";
    $("#capture-url").value = "";
    renderCaptureResult(result);
    toast(`Captured ${result.thought.id}`);
    window.setTimeout(() => {
      runSearch().catch((error) => toast(error.message));
      loadTopics().catch(() => {});
    }, 1000);
  } finally {
    setButtonLoading(submit, false);
  }
}

function renderCaptureResult(result) {
  const node = $("#capture-result");
  if (!node || !result?.thought) return;
  const thought = result.thought;
  const jobs = result.jobs || result.Jobs || [];
  const warning = thought.capture_status === "duplicate_warned"
    ? `<div class="tf-alert tf-alert-warning">${escapeHTML((thought.errors || []).map((error) => error.message || error.code).join("; ") || "Possible duplicate content.")}</div>`
    : "";
  node.innerHTML = `
    <div class="tf-result">
      <strong>${escapeHTML(thought.display_title || thought.title || thought.id)}</strong>
      <div class="topic-meta">${escapeHTML(thought.id)} · ${escapeHTML(thought.status || "accepted")} · ${escapeHTML(thought.path || "")}</div>
      ${warning}
      <div class="tf-action-row">
        <a class="tf-btn" href="#/thoughts?id=${encodeURIComponent(thought.id)}">View thought</a>
        <a class="tf-btn" href="#/search">Search related</a>
        <button class="tf-btn" id="continue-capture" type="button">Continue capture</button>
      </div>
      ${renderJobLinks(jobs)}
    </div>
  `;
  $("#continue-capture")?.addEventListener("click", () => {
    $("#capture-title").focus();
  });
}

function renderJobLinks(jobs) {
  if (!jobs || jobs.length === 0) return '<div class="tf-empty">No background jobs returned.</div>';
  return `<div class="tf-job-links">${jobs
    .map((job) => `<a class="${statusBadge(job.status)}" href="#/jobs?id=${encodeURIComponent(job.id)}">${escapeHTML(job.type || "job")} · ${escapeHTML(job.status || "queued")}</a>`)
    .join("")}</div>`;
}

async function runSearch(event) {
  if (event) event.preventDefault();
  const query = new URLSearchParams();
  query.set("q", $("#search-query").value.trim());
  query.set("mode", $("#search-mode").value);
  query.set("page", "1");
  query.set("page_size", "20");
  const topicID = $("#search-topic-id").value.trim() || state.activeTopicId;
  if (topicID) query.set("topic_id", topicID);
  const tags = csv($("#search-tags").value);
  if (tags.length > 0) query.set("tags", tags.join(","));
  if ($("#search-from").value) query.set("from", $("#search-from").value);
  if ($("#search-to").value) query.set("to", $("#search-to").value);
  if ($("#search-sort").value) query.set("sort", $("#search-sort").value);
  if ($("#search-explain").checked) query.set("explain", "true");
  const response = await api(`/api/search?${query.toString()}`);
  state.lastResults = response.items || [];
  renderResults(response);
}

function resetSearchFilters() {
  $("#search-tags").value = "";
  $("#search-topic-id").value = "";
  $("#search-from").value = "";
  $("#search-to").value = "";
  $("#search-sort").value = "";
  $("#search-explain").checked = false;
  runSearch().catch((error) => toast(error.message));
}

function renderResults(response) {
  const list = $("#search-results");
  if (!response.items || response.items.length === 0) {
    list.innerHTML = '<div class="topic-meta">No matching thoughts.</div>';
    updateSelectionControls();
    return;
  }
  list.innerHTML = response.items
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
      window.location.hash = `#/thoughts?id=${encodeURIComponent(button.dataset.openId)}`;
    });
  });
  list.querySelectorAll("[data-basket-id]").forEach((button) => {
    button.addEventListener("click", () => addToSynthesisBasket([button.dataset.basketId]));
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
  const explain = item.explain
    ? `<details class="tf-explain"><summary>Score details</summary>${renderDescription([
        ["Formula", item.explain.score_formula || ""],
        ["Mode", item.explain.mode || ""],
        ["Sort", item.explain.sort || ""],
        ["Keyword source", item.explain.keyword_source || ""],
        ["Semantic source", item.explain.semantic_source || ""],
        ["Keyword weight", item.explain.weights?.keyword === undefined ? "" : String(item.explain.weights.keyword)],
        ["Semantic weight", item.explain.weights?.semantic === undefined ? "" : String(item.explain.weights.semantic)],
        ["Recency weight", item.explain.weights?.recency === undefined ? "" : String(item.explain.weights.recency)],
      ])}</details>`
    : "";
  return `
    <article class="result-item">
      <div class="result-row">
        <input type="checkbox" data-select-id="${escapeHTML(thoughtID)}" ${checked} aria-label="Select thought">
        <div>
          <strong><button class="link-button" data-preview-id="${escapeHTML(thoughtID)}" type="button">${escapeHTML(item.title || thoughtID)}</button></strong>
          <div class="result-meta">${escapeHTML(item.snippet || "")}</div>
          <div class="score-line">
            <span class="pill green">score ${score(item.score)}</span>
            <span class="pill">kw ${score(item.keyword_score)}</span>
            <span class="pill">sem ${score(item.semantic_score)}</span>
            <span class="pill">rec ${score(item.recency_score)}</span>
            ${tags}
          </div>
          <div class="tf-action-row">
            <button class="mini-button" data-open-id="${escapeHTML(thoughtID)}" type="button">Open</button>
            <button class="mini-button" data-basket-id="${escapeHTML(thoughtID)}" type="button">Add to synthesis</button>
            <button class="mini-button" data-weave-id="${escapeHTML(thoughtID)}" ${options.activeTopicId ? "" : "disabled"} type="button">Review weave</button>
            ${item.path ? `<button class="mini-button" data-copy-path="${escapeHTML(item.path)}" type="button">Copy path</button><code>${escapeHTML(item.path)}</code>` : ""}
          </div>
          ${explain}
        </div>
      </div>
    </article>
  `;
}

function copyPath(path) {
  if (!path) return;
  if (navigator.clipboard?.writeText) {
    navigator.clipboard.writeText(path).then(() => toast("Path copied")).catch(() => toast(path));
    return;
  }
  toast(path);
}

function updateSelectionControls() {
  const count = state.selectedThoughts.size;
  const selectedCount = $("#selected-count");
  if (selectedCount) selectedCount.textContent = `${count} selected`;
  const add = $("#add-selected-synthesis");
  const clear = $("#clear-selected");
  if (add) add.disabled = count === 0;
  if (clear) clear.disabled = count === 0;
}

function addToSynthesisBasket(thoughtIds) {
  for (const thoughtId of thoughtIds || []) {
    if (thoughtId) state.synthesisBasket.add(thoughtId);
  }
  renderSynthesisBasket();
  toast(`${state.synthesisBasket.size} sources in synthesis basket`);
}

function clearSearchSelection() {
  state.selectedThoughts.clear();
  renderResults({ items: state.lastResults });
}

function clearSynthesisBasket() {
  state.synthesisBasket.clear();
  renderSynthesisBasket();
}

function renderSynthesisBasket() {
  const ids = Array.from(state.synthesisBasket);
  const count = $("#synthesis-source-count");
  const list = $("#synthesis-source-list");
  const clear = $("#clear-synthesis-basket");
  if (count) count.textContent = `${ids.length} selected sources`;
  if (clear) clear.disabled = ids.length === 0;
  if (list) {
    list.innerHTML = ids.length === 0
      ? "No sources selected."
      : ids.map((id) => `<span class="pill">${escapeHTML(id)}</span>`).join("");
  }
}

async function previewThought(thoughtId, options = {}) {
  const snapshot = await api(`/api/thoughts/${encodeURIComponent(thoughtId)}`);
  state.activeThoughtId = thoughtId;
  state.activeThoughtSnapshot = snapshot;
  const thought = snapshot.thought;
  const content = snapshot.content || {};
  const html = renderMarkdown([
    `# ${thought.display_title || thought.user_title || thought.id}`,
    "",
    `status: ${thought.refine_status} / ${thought.index_status} / ${thought.topic_status}`,
    `path: ${thought.path}`,
    "",
    "## Summary",
    thought.summary || "No summary yet.",
    "",
    "## Original",
    content.original || "",
    "",
    content.extracted_content ? `## Extracted Content\n${content.extracted_content}` : "",
    content.links ? `## Links\n${content.links}` : "",
    (snapshot.jobs || []).length > 0 ? `## Jobs\n${(snapshot.jobs || []).map((job) => `- ${job.id} (${job.status})`).join("\n")}` : "",
  ]
    .filter(Boolean)
    .join("\n"));
  $("#thought-preview").innerHTML = html;
  const drawer = $("#thought-drawer-content");
  if (drawer) drawer.innerHTML = html;
  $("#drawer-add-synthesis").disabled = false;
  $("#retry-refine").disabled = false;
  if (options.drawer) openDrawer("thought-drawer");
}

async function loadThoughtByID(event) {
  if (event) event.preventDefault();
  const thoughtID = $("#thought-id").value.trim();
  if (!thoughtID) {
    toast("Thought ID is required");
    return;
  }
  window.location.hash = `#/thoughts?id=${encodeURIComponent(thoughtID)}`;
  await previewThought(thoughtID);
}

async function retryRefine() {
  if (!state.activeThoughtId) {
    toast("Open a thought first");
    return;
  }
  const job = await api(`/api/thoughts/${encodeURIComponent(state.activeThoughtId)}/retry-refine`, { method: "POST", body: "{}" });
  renderJobDetail(job, $("#thought-drawer-content"));
  toast(`Retry refine queued: ${job.id}`);
  window.location.hash = `#/jobs?id=${encodeURIComponent(job.id)}`;
}

async function createSynthesis(event) {
  event.preventDefault();
  const thoughtIds = Array.from(state.synthesisBasket);
  if (thoughtIds.length === 0) {
    toast("Add sources to synthesis first");
    return;
  }
  const draft = await api("/api/synthesis", {
    method: "POST",
    body: JSON.stringify({
      thought_ids: thoughtIds,
      goal: $("#synthesis-goal").value.trim(),
      format: $("#synthesis-format").value,
    }),
  });
  state.synthesisDraft = draft;
  $("#synthesis-output").value = renderSynthesisDraft(draft);
  $("#save-synthesis").disabled = (draft.status || "draft") !== "draft";
  closeDrawer("synthesis-create-drawer");
  await loadSynthesisDrafts();
  window.location.hash = "#/synthesis";
}

async function loadSynthesisDrafts() {
  state.synthesisDrafts = await api("/api/synthesis");
  renderSynthesisDrafts();
}

async function loadSynthesisDraft(draftId) {
  if (!draftId) return;
  const draft = await api(`/api/synthesis/${encodeURIComponent(draftId)}`);
  state.synthesisDraft = draft;
  for (const thoughtId of draft.thought_ids || []) state.synthesisBasket.add(thoughtId);
  renderSynthesisBasket();
  $("#synthesis-goal").value = draft.goal || "";
  $("#synthesis-format").value = draft.format || "summary";
  $("#synthesis-output").value = renderSynthesisDraft(draft);
  $("#save-synthesis").disabled = (draft.status || "draft") !== "draft";
  renderSynthesisDrafts();
}

async function previewWeave(thoughtId) {
  if (!state.activeTopicId) {
    toast("Select a topic first");
    return;
  }
  const proposal = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/weave-preview`, {
    method: "POST",
    body: JSON.stringify({ thought_id: thoughtId }),
  });
  state.weaveProposal = proposal;
  $("#weave-review-title").textContent = `Weave ${proposal.thought_id}`;
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
  $("#weave-review-title").textContent = `Weave ${proposal.thought_id}`;
  $("#weave-diff").innerHTML = renderDiff(proposal.diff || []);
  $("#weave-document").value = proposal.accepted_document || proposal.proposed_document || "";
  $("#accept-weave").disabled = (proposal.status || "pending") !== "pending";
  renderWeaveProposals();
  navigateTopic(state.activeTopicId, true);
}

async function acceptWeave() {
  if (!state.weaveProposal) {
    toast("Create a weave preview first");
    return;
  }
  const confirmed = await confirmAction("Accept weave proposal", "This writes the proposed document to the topic index.md.");
  if (!confirmed) return;
  const document = $("#weave-document").value.trim();
  if (!document) {
    toast("Proposed document is required");
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
  toast("Weave accepted");
  state.weaveProposal = null;
  $("#accept-weave").disabled = true;
  $("#weave-diff").innerHTML = '<div class="topic-meta">No pending weave review.</div>';
  $("#weave-document").value = "";
  await loadTopics();
  await loadWeaveProposals(detail.topic.id);
  navigateTopic(detail.topic.id);
}

function renderSynthesisDraft(draft) {
  const links = (draft.source_links || []).filter(Boolean);
  let content = draft.content || "";
  const missing = links.filter((link) => !content.includes(link));
  if (missing.length === 0) return content;
  return `${content}\n\n### Sources\n\n${missing.map((link) => `- [[${link}]]`).join("\n")}`;
}

async function saveSynthesis() {
  if (!state.synthesisDraft) {
    toast("Create a draft first");
    return;
  }
  const confirmed = await confirmAction("Save synthesis as thought", "This creates a new thought with the current draft content and source links.");
  if (!confirmed) return;
  const content = $("#synthesis-output").value.trim();
  if (!content) {
    toast("Draft content is required");
    return;
  }
  const result = await api("/api/synthesis/save", {
    method: "POST",
    body: JSON.stringify({
      draft_id: state.synthesisDraft.id,
      thought_ids: state.synthesisDraft.thought_ids || [],
      goal: state.synthesisDraft.goal || $("#synthesis-goal").value.trim(),
      format: state.synthesisDraft.format || $("#synthesis-format").value,
      content,
      source_links: state.synthesisDraft.source_links || [],
    }),
  });
  toast(`Saved ${result.thought.id}`);
  state.selectedThoughts.clear();
  state.synthesisBasket.clear();
  renderSynthesisBasket();
  $("#synthesis-save-result").innerHTML = `<a class="tf-btn" href="#/thoughts?id=${encodeURIComponent(result.thought.id)}">View saved thought</a>`;
  $("#save-synthesis").disabled = true;
  state.synthesisDraft = null;
  await loadSynthesisDrafts();
  window.setTimeout(() => runSearch().catch((error) => toast(error.message)), 1000);
}

async function rebuildTopic() {
  if (!state.activeTopicId) return;
  const confirmed = await confirmAction("Rebuild topic", "This queues a topic rebuild job and may refresh the topic document.");
  if (!confirmed) return;
  const job = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/rebuild`, { method: "POST", body: "{}" });
  toast(`Rebuild queued: ${job.id}`);
  window.location.hash = `#/jobs?id=${encodeURIComponent(job.id)}`;
}

async function reindex() {
  const confirmed = await confirmAction("Reindex workspace", "This queues a workspace reindex job for the search database.");
  if (!confirmed) return;
  const job = await api("/api/system/reindex", { method: "POST", body: "{}" });
  toast(`Reindex queued: ${job.id}`);
  window.location.hash = `#/jobs?id=${encodeURIComponent(job.id)}`;
}

async function loadJob(event) {
  if (event) event.preventDefault();
  const jobID = $("#job-id").value.trim();
  if (!jobID) {
    toast("Job ID is required");
    return;
  }
  const job = await api(`/api/jobs/${encodeURIComponent(jobID)}`);
  renderJobDetail(job, $("#job-detail"));
}

function renderJobDetail(job, node = $("#job-detail")) {
  if (!node || !job) return;
  node.innerHTML = `
    <div class="tf-result">
      <strong>${escapeHTML(job.id)}</strong>
      <div class="topic-meta">${escapeHTML(job.type || "job")} · ${escapeHTML(job.resource_type || "")}:${escapeHTML(job.resource_id || "")}</div>
      <div class="${statusBadge(job.status)}">${escapeHTML(job.status || "unknown")}</div>
      ${renderDescription([
        ["Message", job.message || ""],
        ["Attempt", `${job.attempt || 0}/${job.max_attempts || 1}`],
        ["Progress", `${Math.round((job.progress || 0) * 100)}%`],
        ["Created", fmtDate(job.created_at)],
        ["Started", fmtDate(job.started_at)],
        ["Finished", fmtDate(job.finished_at)],
        ["Error code", job.error?.code || ""],
        ["Error message", job.error?.message || ""],
        ["Retryable", job.error?.retryable === undefined ? "" : String(job.error.retryable)],
      ])}
    </div>
  `;
}

function connectEvents() {
  const list = $("#event-list");
  const source = new EventSource("/api/events");
  source.onmessage = (event) => appendEvent("message", event.data);
  [
    "thought.captured",
    "thought.refined",
    "thought.refine_failed",
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
    });
  });
  source.onerror = () => {
    if (list.children.length === 0) appendEvent("events", "SSE reconnecting");
  };
}

function appendEvent(type, data) {
  const list = $("#event-list");
  const jobsList = $("#jobs-event-list");
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
  prependEventItem(list, item);
  prependEventItem(jobsList, item.cloneNode(true));
  prependEventItem(dashboardList, item.cloneNode(true), 8);
  applyEventFilter();
}

function prependEventItem(list, item, limit = 60) {
  if (!list || !item) return;
  list.prepend(item);
  while (list.children.length > limit) list.removeChild(list.lastChild);
}

function applyEventFilter() {
  const type = ($("#event-type-filter")?.value || "").trim().toLowerCase();
  const resource = ($("#event-resource-filter")?.value || "").trim().toLowerCase();
  document.querySelectorAll("#jobs-event-list .event-item").forEach((item) => {
    const matchesType = !type || String(item.dataset.eventType || "").toLowerCase().includes(type);
    const matchesResource = !resource || String(item.dataset.resourceId || item.textContent || "").toLowerCase().includes(resource);
    item.hidden = !(matchesType && matchesResource);
  });
}

function resetEventFilter() {
  $("#event-type-filter").value = "";
  $("#event-resource-filter").value = "";
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
  });
}

async function applyRoute(hash = window.location.hash) {
  const route = parseRoute(hash);
  state.route = route;
  renderRoute(route);
  if ((route.page === "topic-detail" || route.page === "topic-review") && route.params.topicId) {
    if (state.activeTopicId !== route.params.topicId || !state.activeTopicDetail) {
      await openTopic(route.params.topicId);
    }
    if (route.page === "topic-review") await loadWeaveProposals(route.params.topicId);
  }
  if (route.page === "thoughts" && route.params.thoughtId) {
    $("#thought-id").value = route.params.thoughtId;
    await previewThought(route.params.thoughtId);
  }
  if (route.page === "jobs" && route.params.jobId) {
    $("#job-id").value = route.params.jobId;
    await loadJob();
  }
  if (route.page === "settings") {
    await loadMetrics();
  }
}

function bind() {
  $("#capture-form").addEventListener("submit", (event) => captureThought(event).catch((error) => toast(error.message)));
  document.querySelectorAll('input[name="type"]').forEach((input) => {
    input.addEventListener("change", () => {
      const isURL = new FormData($("#capture-form")).get("type") === "url";
      $("#capture-url-row").hidden = !isURL;
    });
  });
  $("#topic-form").addEventListener("submit", (event) => createTopic(event).catch((error) => toast(error.message)));
  $("#topic-edit-form").addEventListener("submit", (event) => saveTopicRules(event).catch((error) => toast(error.message)));
  $("#search-form").addEventListener("submit", (event) => runSearch(event).catch((error) => toast(error.message)));
  $("#reset-search").addEventListener("click", resetSearchFilters);
  $("#thought-form").addEventListener("submit", (event) => loadThoughtByID(event).catch((error) => toast(error.message)));
  $("#synthesis-form").addEventListener("submit", (event) => createSynthesis(event).catch((error) => toast(error.message)));
  $("#job-form").addEventListener("submit", (event) => loadJob(event).catch((error) => toast(error.message)));
  $("#event-type-filter").addEventListener("input", applyEventFilter);
  $("#event-resource-filter").addEventListener("input", applyEventFilter);
  $("#reset-event-filter").addEventListener("click", resetEventFilter);
  $("#save-synthesis").addEventListener("click", () => saveSynthesis().catch((error) => toast(error.message)));
  $("#accept-weave").addEventListener("click", () => acceptWeave().catch((error) => toast(error.message)));
  $("#refresh-topics").addEventListener("click", () => loadTopics().catch((error) => toast(error.message)));
  $("#topic-filter").addEventListener("input", renderTopics);
  $("#topic-auto-filter").addEventListener("change", renderTopics);
  $("#reset-topic-filter").addEventListener("click", resetTopicFilters);
  $("#rebuild-topic").addEventListener("click", () => rebuildTopic().catch((error) => toast(error.message)));
  $("#reindex-button").addEventListener("click", () => reindex().catch((error) => toast(error.message)));
  $("#open-create-topic").addEventListener("click", () => openDrawer("topic-create-drawer"));
  $("#open-topic-rules").addEventListener("click", () => openDrawer("topic-rules-drawer"));
  $("#open-synthesis-create").addEventListener("click", () => openDrawer("synthesis-create-drawer"));
  $("#refresh-synthesis").addEventListener("click", () => loadSynthesisDrafts().catch((error) => toast(error.message)));
  $("#add-selected-synthesis").addEventListener("click", () => {
    addToSynthesisBasket(Array.from(state.selectedThoughts));
    window.location.hash = "#/synthesis";
  });
  $("#clear-selected").addEventListener("click", clearSearchSelection);
  $("#clear-synthesis-basket").addEventListener("click", clearSynthesisBasket);
  $("#drawer-add-synthesis").addEventListener("click", () => addToSynthesisBasket([state.activeThoughtId]));
  $("#retry-refine").addEventListener("click", () => retryRefine().catch((error) => toast(error.message)));
  $("#confirm-cancel").addEventListener("click", () => closeConfirm(false));
  $("#confirm-ok").addEventListener("click", () => closeConfirm(true));
  document.querySelectorAll("[data-dashboard-link]").forEach((card) => {
    card.addEventListener("click", () => {
      window.location.hash = card.dataset.dashboardLink;
    });
  });
  document.querySelectorAll("[data-close-drawer]").forEach((button) => {
    button.addEventListener("click", () => closeDrawer(button.dataset.closeDrawer));
  });
  document.querySelectorAll(".tab").forEach((tab) => tab.addEventListener("click", () => activateTab(tab.dataset.tab, tab.closest(".tf-card"))));
  window.addEventListener("hashchange", () => applyRoute().catch((error) => toast(error.message)));
  document.addEventListener("keydown", (event) => {
    if (event.key === "Escape") {
      document.querySelectorAll(".tf-drawer.open").forEach((drawer) => closeDrawer(drawer.id));
      closeConfirm(false);
    }
  });
  document.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      $("#search-query").focus();
    }
  });
}

async function boot() {
  bind();
  if (!window.location.hash) window.location.hash = "#/dashboard";
  state.route = parseRoute(window.location.hash);
  renderRoute();
  await loadStatus();
  await loadTopics();
  await loadSynthesisDrafts();
  await runSearch();
  await applyRoute();
  connectEvents();
}

boot().catch((error) => toast(error.message));
