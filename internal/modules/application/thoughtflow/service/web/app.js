const state = {
  topics: [],
  activeTopicId: "",
  selectedThoughts: new Set(),
  lastResults: [],
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

async function loadStatus() {
  try {
    const status = await api("/api/system/status");
    $("#system-status").textContent = `${status.workspace.id} / ${status.ai.status}`;
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
  $("#topic-title").textContent = detail.topic.name;
  $("#topic-document").textContent = detail.document || "No topic document.";
  $("#rebuild-topic").disabled = false;
  renderTopics();
  await runSearch();
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
}

async function previewThought(thoughtId) {
  const snapshot = await api(`/api/thoughts/${encodeURIComponent(thoughtId)}`);
  const thought = snapshot.thought;
  const content = snapshot.content || {};
  $("#thought-preview").textContent = [
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
    .join("\n");
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
  $("#synthesis-output").textContent = `${draft.content}\n\nSources:\n${(draft.source_links || []).join("\n")}`;
  activateTab("synthesis");
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
  $("#search-form").addEventListener("submit", (event) => runSearch(event).catch((error) => toast(error.message)));
  $("#synthesis-form").addEventListener("submit", (event) => createSynthesis(event).catch((error) => toast(error.message)));
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
  await runSearch();
  connectEvents();
}

boot().catch((error) => toast(error.message));
