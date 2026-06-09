const state = {
  topics: [],
  activeTopicId: "",
  selectedThoughts: new Set(),
  lastResults: [],
  synthesisDraft: null,
  synthesisDrafts: [],
  activeTopicDetail: null,
  weaveProposal: null,
  weaveProposals: [],
};

const $ = (selector) => document.querySelector(selector);

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
    throw new Error(message);
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
  text = text.replace(/`([^`]+)`/g, "<code>$1</code>");
  text = text.replace(/\*\*([^*]+)\*\*/g, "<strong>$1</strong>");
  return text;
}

function renderMarkdown(value) {
  const lines = String(value || "").split(/\r?\n/);
  const html = [];
  let inList = false;
  let inCode = false;
  const closeList = () => {
    if (inList) {
      html.push("</ul>");
      inList = false;
    }
  };
  for (const rawLine of lines) {
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
    const listItem = line.match(/^[-*]\s+(.+)$/);
    if (listItem) {
      if (!inList) {
        html.push("<ul>");
        inList = true;
      }
      html.push(`<li>${renderInlineMarkdown(listItem[1])}</li>`);
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
      return `
        <article class="approval-item${active}" data-proposal-id="${escapeHTML(proposal.id)}">
          <strong>${escapeHTML(proposal.thought_id || proposal.id)}</strong>
          <div class="topic-meta">
            <span class="pill">${escapeHTML(status)}</span>
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
    $("#system-status").textContent = `${status.workspace.id} / ${status.status}`;
  } catch (error) {
    $("#system-status").textContent = "degraded";
  }
}

async function loadTopics() {
  const topics = await api("/api/topics");
  state.topics = topics || [];
  renderTopics();
  if (!state.activeTopicId && state.topics.length > 0) {
    await openTopic(state.topics[0].id);
  }
}

function renderTopics() {
  const list = $("#topic-list");
  if (state.topics.length === 0) {
    state.activeTopicId = "";
    state.activeTopicDetail = null;
    populateTopicEditor(null);
    $("#topic-title").textContent = "Topic Workspace";
    $("#topic-document").innerHTML = renderMarkdown("Select a topic from the left.");
    $("#rebuild-topic").disabled = true;
    state.weaveProposals = [];
    state.weaveProposal = null;
    renderWeaveProposals();
    list.innerHTML = '<div class="topic-meta">No topics yet.</div>';
    return;
  }
  list.innerHTML = state.topics
    .map((topic) => {
      const active = topic.id === state.activeTopicId ? " active" : "";
      return `
        <article class="topic-item${active}" data-topic-id="${escapeHTML(topic.id)}">
          <strong>${escapeHTML(topic.name)}</strong>
          <div class="topic-meta">${topic.member_count || 0} thoughts · ${topic.word_count || 0} words</div>
          <div class="topic-meta">${escapeHTML(topic.description || "No description")}</div>
        </article>
      `;
    })
    .join("");
  list.querySelectorAll(".topic-item").forEach((item) => {
    item.addEventListener("click", () => openTopic(item.dataset.topicId));
  });
}

async function openTopic(topicId) {
  if (!topicId) return;
  const detail = await api(`/api/topics/${encodeURIComponent(topicId)}`);
  state.activeTopicId = topicId;
  state.activeTopicDetail = detail;
  $("#topic-title").textContent = detail.topic.name;
  $("#topic-document").innerHTML = renderMarkdown(detail.document || "No topic document.");
  $("#rebuild-topic").disabled = false;
  populateTopicEditor(detail.topic);
  renderTopics();
  await loadWeaveProposals(topicId);
  await runSearch();
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
        keywords: { any: csv($("#topic-keywords").value) },
        tags: { any: csv($("#topic-tags").value) },
        semantic: { enabled: semanticEnabled, threshold },
      },
      outline: [{ title: "Notes" }, { title: "Open Questions" }],
    }),
  });
  event.target.reset();
  $("#topic-threshold").value = "0.75";
  toast("Topic created");
  await loadTopics();
  await openTopic(topic.id);
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
  await loadTopics();
  await openTopic(topic.id);
}

async function captureThought(event) {
  event.preventDefault();
  const type = new FormData(event.target).get("type") || "text";
  const raw = $("#capture-content").value.trim();
  if (!raw) {
    toast("Capture content is required");
    return;
  }
  const command = {
    type,
    title: $("#capture-title").value.trim(),
    tags: csv($("#capture-tags").value),
  };
  if (type === "url") {
    command.url = raw;
  } else {
    command.content = raw;
  }
  const result = await api("/api/thoughts", {
    method: "POST",
    body: JSON.stringify(command),
  });
  $("#capture-content").value = "";
  toast(`Captured ${result.thought.id}`);
  window.setTimeout(() => {
    runSearch().catch((error) => toast(error.message));
    loadTopics().catch(() => {});
  }, 1000);
}

async function runSearch(event) {
  if (event) event.preventDefault();
  const query = new URLSearchParams();
  query.set("q", $("#search-query").value.trim());
  query.set("mode", $("#search-mode").value);
  query.set("page", "1");
  query.set("page_size", "20");
  if (state.activeTopicId) query.set("topic_id", state.activeTopicId);
  const tags = csv($("#search-tags").value);
  if (tags.length > 0) query.set("tags", tags.join(","));
  const response = await api(`/api/search?${query.toString()}`);
  state.lastResults = response.items || [];
  renderResults(response);
}

function renderResults(response) {
  const list = $("#search-results");
  if (!response.items || response.items.length === 0) {
    list.innerHTML = '<div class="topic-meta">No matching thoughts.</div>';
    return;
  }
  list.innerHTML = response.items
    .map((item) => {
      const checked = state.selectedThoughts.has(item.thought_id) ? "checked" : "";
      const tags = [...(item.tags || []), ...(item.topics || [])]
        .slice(0, 5)
        .map((tag) => `<span class="pill">${escapeHTML(tag)}</span>`)
        .join("");
      return `
        <article class="result-item">
          <div class="result-row">
            <input type="checkbox" data-select-id="${escapeHTML(item.thought_id)}" ${checked} aria-label="Select thought">
            <div>
              <strong><button class="link-button" data-preview-id="${escapeHTML(item.thought_id)}">${escapeHTML(item.title || item.thought_id)}</button></strong>
              <div class="result-meta">${escapeHTML(item.snippet || "")}</div>
              <div class="score-line">
                <span class="pill green">score ${score(item.score)}</span>
                <span class="pill">kw ${score(item.keyword_score)}</span>
                <span class="pill">sem ${score(item.semantic_score)}</span>
                ${tags}
                <button class="mini-button" data-weave-id="${escapeHTML(item.thought_id)}" ${state.activeTopicId ? "" : "disabled"}>Review weave</button>
              </div>
            </div>
          </div>
        </article>
      `;
    })
    .join("");
  list.querySelectorAll("[data-select-id]").forEach((input) => {
    input.addEventListener("change", () => {
      if (input.checked) state.selectedThoughts.add(input.dataset.selectId);
      else state.selectedThoughts.delete(input.dataset.selectId);
    });
  });
  list.querySelectorAll("[data-preview-id]").forEach((button) => {
    button.addEventListener("click", () => previewThought(button.dataset.previewId));
  });
  list.querySelectorAll("[data-weave-id]").forEach((button) => {
    button.addEventListener("click", () => previewWeave(button.dataset.weaveId).catch((error) => toast(error.message)));
  });
}

async function previewThought(thoughtId) {
  const snapshot = await api(`/api/thoughts/${encodeURIComponent(thoughtId)}`);
  const thought = snapshot.thought;
  const content = snapshot.content || {};
  $("#thought-preview").innerHTML = renderMarkdown([
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
  ]
    .filter(Boolean)
    .join("\n"));
}

async function createSynthesis(event) {
  event.preventDefault();
  const thoughtIds = Array.from(state.selectedThoughts);
  if (thoughtIds.length === 0) {
    toast("Select search results first");
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
  await loadSynthesisDrafts();
  activateTab("synthesis");
}

async function loadSynthesisDrafts() {
  state.synthesisDrafts = await api("/api/synthesis");
  renderSynthesisDrafts();
}

async function loadSynthesisDraft(draftId) {
  if (!draftId) return;
  const draft = await api(`/api/synthesis/${encodeURIComponent(draftId)}`);
  state.synthesisDraft = draft;
  $("#synthesis-goal").value = draft.goal || "";
  $("#synthesis-format").value = draft.format || "summary";
  $("#synthesis-output").value = renderSynthesisDraft(draft);
  $("#save-synthesis").disabled = (draft.status || "draft") !== "draft";
  renderSynthesisDrafts();
  activateTab("synthesis");
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
  activateTab("review");
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
  activateTab("review");
}

async function acceptWeave() {
  if (!state.weaveProposal) {
    toast("Create a weave preview first");
    return;
  }
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
  await openTopic(detail.topic.id);
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
  $("#save-synthesis").disabled = true;
  state.synthesisDraft = null;
  await loadSynthesisDrafts();
  window.setTimeout(() => runSearch().catch((error) => toast(error.message)), 1000);
}

async function rebuildTopic() {
  if (!state.activeTopicId) return;
  const job = await api(`/api/topics/${encodeURIComponent(state.activeTopicId)}/rebuild`, { method: "POST", body: "{}" });
  toast(`Rebuild queued: ${job.id}`);
}

async function reindex() {
  const job = await api("/api/system/reindex", { method: "POST", body: "{}" });
  toast(`Reindex queued: ${job.id}`);
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
  const item = document.createElement("article");
  item.className = "event-item";
  let parsed = data;
  try {
    const value = JSON.parse(data);
    parsed = `${value.resource_type || ""}:${value.resource_id || ""}`;
  } catch (_error) {
    parsed = data;
  }
  item.innerHTML = `<strong>${escapeHTML(type)}</strong><div class="event-meta">${escapeHTML(parsed)}</div>`;
  list.prepend(item);
  while (list.children.length > 60) list.removeChild(list.lastChild);
}

function activateTab(name) {
  document.querySelectorAll(".tab").forEach((tab) => tab.classList.toggle("active", tab.dataset.tab === name));
  document.querySelectorAll(".tab-panel").forEach((panel) => panel.classList.toggle("active", panel.id === `tab-${name}`));
}

function bind() {
  $("#capture-form").addEventListener("submit", (event) => captureThought(event).catch((error) => toast(error.message)));
  $("#topic-form").addEventListener("submit", (event) => createTopic(event).catch((error) => toast(error.message)));
  $("#topic-edit-form").addEventListener("submit", (event) => saveTopicRules(event).catch((error) => toast(error.message)));
  $("#search-form").addEventListener("submit", (event) => runSearch(event).catch((error) => toast(error.message)));
  $("#synthesis-form").addEventListener("submit", (event) => createSynthesis(event).catch((error) => toast(error.message)));
  $("#save-synthesis").addEventListener("click", () => saveSynthesis().catch((error) => toast(error.message)));
  $("#accept-weave").addEventListener("click", () => acceptWeave().catch((error) => toast(error.message)));
  $("#refresh-topics").addEventListener("click", () => loadTopics().catch((error) => toast(error.message)));
  $("#rebuild-topic").addEventListener("click", () => rebuildTopic().catch((error) => toast(error.message)));
  $("#reindex-button").addEventListener("click", () => reindex().catch((error) => toast(error.message)));
  document.querySelectorAll(".tab").forEach((tab) => tab.addEventListener("click", () => activateTab(tab.dataset.tab)));
  document.addEventListener("keydown", (event) => {
    if ((event.ctrlKey || event.metaKey) && event.key.toLowerCase() === "k") {
      event.preventDefault();
      $("#search-query").focus();
    }
  });
}

async function boot() {
  bind();
  await loadStatus();
  await loadTopics();
  await loadSynthesisDrafts();
  await runSearch();
  connectEvents();
}

boot().catch((error) => toast(error.message));
