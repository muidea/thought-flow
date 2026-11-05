// The library renders authoritative saved-content projections. It never groups
// a paginated Thought list or substitutes mutable copies for frozen versions.
(function (global) {
  "use strict";
  const enc = encodeURIComponent;
  function route(id, selection = {}, member = "", section = "") {
    const q = new URLSearchParams({set: id});
    if (selection.kind) q.set("kind", selection.kind);
    if (selection.id) q.set("version", selection.id);
    if (member) q.set("member", member);
    if (section) q.set("section", section);
    return `#/notes?${q}`;
  }
  function targetMember(detail, key) {
    return [...(detail.members || []), ...(detail.sources || [])].find(m => m.key === key) || null;
  }
  function versionMember(detail, key) {
    return targetMember(detail, key)?.key || detail.members?.[0]?.key || "";
  }
  function create(deps) {
    const {api, t, escapeHTML: e, markdown, navigate, toast} = deps;
    let listSerial = 0, readSerial = 0, topicSerial = 0, homeSerial = 0;
    let query = {layout: "grouped", kind: "all", state: "", q: "", sort: "updated", page: 1};
    const text = key => e(t(`library.${key}`));
    const label = value => e(t(`library.state.${value}`));
    const link = (href, title, cls = "") => `<a class="${cls}" href="${e(href)}">${title}</a>`;
    const button = (attr, title) => `<button class="tf-btn" type="button" ${attr}>${title}</button>`;
    const icon = kind => `<svg class="workspace-icon" viewBox="0 0 20 20" fill="none" stroke="currentColor" stroke-width="1.4" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">${({content:'<path d="M5 3h8l3 3v11H5zM12 3v4h4M8 10h5M8 13h5"/>',sets:'<path d="m3 6 7-3 7 3-7 3zM3 10l7 3 7-3M3 14l7 3 7-3"/>',notes:'<path d="M4 3h12v14H4zM7 3v14M10 7h3M10 11h3"/>',drafts:'<circle cx="10" cy="10" r="7"/><path d="M10 6v4l3 2"/>',arrow:'<path d="M4 10h12M11 5l5 5-5 5"/>'})[kind]||''}</svg>`;
    const meta = item => `<div class="library-meta"><span class="pill">${label(item.state)}</span> ${item.kind === "thought" ? "" : t("library.member_count", {n: item.members?.length || 0, sources: item.source_count ?? item.sources?.length ?? 0})}${item.new_version ? ` · ${text("new_version")}` : ""}</div>`;
    function card(item, compact = false) {
      const href = item.kind === "thought" ? `#/notes?id=${enc(item.id)}` : route(item.set_id || item.id, item.selection, item.member_key);
      if(compact === true)return `<article class="library-card workspace-result-card" ${item.kind === "thought" ? `data-thought-id="${e(item.id)}"` : `data-library-set="${e(item.set_id || item.id)}"`}>
        <div class="workspace-result-body"><div class="workspace-result-type"><span>${icon(item.kind==="thought"?"content":"sets")}${text(item.kind==="thought"?"single":"set")}</span><span class="workspace-state workspace-state-${e(item.state)}">${label(item.state)}</span></div>
        <h3>${link(href,e(item.title))}</h3><p class="workspace-result-description">${e(item.description||"")}</p>
        ${item.members?.length?`<div class="workspace-role-tags">${[...new Set(item.members.map(m=>m.role).filter(Boolean))].slice(0,5).map(role=>`<span>${e(t(`workspace_ui.role.${role.split(":")[0]}`))}</span>`).join("")}</div>`:""}
        ${item.new_version?`<p class="workspace-result-notice">${text("new_version")}</p>`:""}${/^failed/.test(item.task_state||"")?`<p class="workspace-result-notice">${text("task_failed")}</p>`:""}
        <div class="workspace-result-meta"><span>${item.kind==="thought"?text("single"):e(t("library.member_count",{n:item.members?.length||0,sources:item.source_count??0}))}</span><time>${e(deps.date(item.updated_at))}</time></div>
        ${item.selection?.id?`<span class="workspace-fixed-version" title="${e(item.selection.id)}">${text("version")} · ${e(item.selection.id)}</span>`:""}</div>
        <footer>${link(href,`${text(item.kind==="thought"?"read":"open")}${icon("arrow")}`,"workspace-text-link")}${item.members?.length?`<details><summary>${text("expand")}</summary><ol>${item.members.map(m=>`<li>${link(route(item.set_id||item.id,item.selection,m.key),e(m.title))}</li>`).join("")}</ol></details>`:""}</footer></article>`;
      return `<article class="library-card result-item" ${item.kind === "thought" ? `data-thought-id="${e(item.id)}"` : `data-library-set="${e(item.set_id || item.id)}"`}>
        <div class="library-eyebrow">${text(item.kind === "thought" ? "single" : "set")}</div>
        <h3>${link(href,e(item.title))}</h3>${meta(item)}${/failed|cancelled/.test(item.task_state||"")?`<p class="library-meta">${text("task_failed")}</p>`:""}<p>${e(item.description || "")}</p>
        ${item.members?.length?`<div class="workspace-role-tags">${[...new Set(item.members.map(m=>m.role).filter(Boolean))].slice(0,5).map(role=>`<span>${e(t(`workspace_ui.role.${role}`))}</span>`).join("")}</div>`:""}
        <div class="library-meta">${e(deps.date(item.updated_at))}</div>
        ${item.members?.length && item.kind !== "member" ? `<details><summary>${text("expand")}</summary><ol>${item.members.map(m=>`<li>${link(route(item.id,item.selection,m.key),e(m.title))}</li>`).join("")}</ol></details>` : ""}
        ${link(href,text(item.kind === "thought" ? "read" : "open"),"tf-btn")}
        ${item.kind==="thought"?`<details><summary>${text("more")}</summary>${button(`data-read-thought="${e(item.id)}"`,text("full_reader"))}${deps.noteActions?.(item.id)||""}</details>`:""}
      </article>`;
    }
    function stats(counts) {
      return `<div class="workspace-summary-band">${[["content","all","content"],["sets","document_set","sets"],["notes","thought","independent"],["drafts","document_set","draft_sets"]].map(([key,kind,label])=>`<a href="#/notes?kind=${kind}${key==="drafts"?"&state=draft":""}" class="workspace-stat"><span class="workspace-stat-icon">${icon(key)}</span><span class="workspace-stat-copy"><strong>${Number(counts[key]||0)}</strong><span>${text(label)}</span></span></a>`).join("")}</div>`;
    }
    function attentionMarkup(items) {
      return items.map((a,i)=>`<article class="workspace-attention-item"><h4>${e(a.kind==="questions"?t("overview.question_count",{n:a.question_count}):t("overview.failed_task"))}</h4><p>${e(a.title)}</p><p>${e(t(a.kind==="questions"?"overview.questions_help":a.selection?.id?"overview.saved_remains":"overview.failed_no_content"))}</p><div class="workspace-attention-actions">${a.selection?.id?link(route(a.set_id,a.selection),e(t("overview.view_content")),"workspace-text-link"):a.session_id?button(`data-home-session="${e(a.session_id)}"`,e(t("workspace_ui.continue"))):""}${a.run_id?button(`data-home-task="${i}"`,text("task_record")):""}</div><div data-home-task-record="${i}"></div></article>`).join("")||`<p class="workspace-home-empty">${e(t("overview.no_attention"))}</p>`;
    }
    async function home(root) {
      if(!root)return;
      const serial=++homeSerial;
      const current=()=>serial===homeSerial&&deps.isHomeCurrent();
      const regions=deps.homeRegions?.()||{};
      const loading=`<p class="workspace-home-empty" role="status">${text("loading")}</p>`;
      root.innerHTML=loading;
      if(regions.stats)regions.stats.innerHTML=loading;
      if(regions.attention)regions.attention.innerHTML=loading;
      if(regions.count)regions.count.textContent="";
      try {
        const result=await api("/api/content-library?page_size=6&overview=1");
        if(!current())return;
        deps.counts?.(result.counts);
        if(regions.stats)regions.stats.innerHTML=stats(result.counts);
        root.innerHTML=(regions.stats?"":stats(result.counts))+`<section class="workspace-recent"><header class="tf-card-header"><h3>${e(t("workspace_ui.recent"))}</h3>${link("#/notes",`${e(t("overview.view_all"))} →`,"workspace-text-link")}</header><div class="library-grid">${result.items.map(item=>card(item,true)).join("")||`<div class="tf-empty"><p>${text("empty")}</p><a class="tf-btn primary" href="#/capture">${e(t("workspace_ui.from_chat"))}</a><a class="tf-btn" href="#/notes?import=1">${e(t("notes.import.title"))}</a></div>`}</div></section>`;
        if(regions.attention){
          regions.attention.innerHTML=attentionMarkup(result.attention||[]);
          regions.attention.querySelectorAll("[data-home-session]").forEach(b=>b.onclick=()=>deps.openSession?.(b.dataset.homeSession));
          regions.attention.querySelectorAll("[data-home-task]").forEach(b=>b.onclick=async()=>{
            const a=result.attention[Number(b.dataset.homeTask)];b.disabled=true;
            try{const run=await api(`/api/document-set-runs/${enc(a.run_id)}`);if(!current())return;const node=regions.attention.querySelector(`[data-home-task-record="${b.dataset.homeTask}"]`);node.innerHTML=`<details open><summary>${text("task_record")}</summary><p>${e(t(`docset.state.${run.state}`))}</p><p>${e(run.error||"")}</p><ol>${(run.calls||[]).map(c=>`<li>${e(c.kind)} · ${e(c.state)}</li>`).join("")}</ol></details>`;}catch(error){if(current())toast(error.message);}finally{if(current())b.disabled=false;}
          });
        }
        if(regions.count)regions.count.textContent=t("overview.attention_count",{n:result.attention_total||0});
        deps.homeLoaded?.(result);
        deps.bindNoteActions?.(root);
      }catch(error){if(current()){
        const failure=`<p role="alert">${e(error.message)}</p>${button("data-reload-home",text("retry"))}`;
        root.innerHTML=failure;root.querySelector("[data-reload-home]").onclick=()=>home(root);
        if(regions.stats)regions.stats.innerHTML=`<p class="workspace-home-empty">${e(t("overview.counts_unavailable"))}</p>`;
        if(regions.attention){regions.attention.innerHTML=failure;regions.attention.querySelector("[data-reload-home]").onclick=()=>home(root);}
      }}
    }
    async function list(root, initial) {
      if (!root) return;
      if (initial) query = {...query, ...initial};
      const serial = ++listSerial;
      const params = new URLSearchParams({...query, page_size: 24});
      try {
        const result = await api(`/api/content-library?${params}`);
        if (serial !== listSerial) return;
        deps.counts?.(result.counts);
        const summary=document.querySelector("#workspace-library-stats");if(summary)summary.innerHTML=stats(result.counts);
        const count = document.querySelector("#thought-list-count");
        if (count) { count.removeAttribute("data-i18n-template"); count.textContent=t(query.layout === "flat" ? "library.document_total" : "library.content_total",{n:result.total}); }
        root.innerHTML = `<div class="library-toolbar" data-library-filters>
          <div role="group" aria-label="${text("filter")}">${[["all","all"],["thought","single"],["document_set","sets"]].map(([v,k])=>`<button type="button" class="tf-btn" data-library-kind="${v}" aria-pressed="${query.kind===v}">${text(k)}</button>`).join("")}</div>
          ${query.state?button("data-clear-state",`${text("draft_sets")} ×`):""}<label>${text("layout")}<select data-layout><option value="grouped">${text("grouped")}</option><option value="flat">${text("flat")}</option></select></label>
          <label>${text("sort")}<select data-sort><option value="updated">${text("updated")}</option><option value="title">${text("title_sort")}</option></select></label>
        </div><p class="library-meta">${e(t("library.inventory",{sets:result.counts.sets,notes:result.counts.notes,documents:result.counts.documents}))}</p>
        <div class="library-grid">${result.items.map(card).join("") || `<p class="tf-empty">${text("empty")}</p>`}</div>
        <div class="library-pagination">${button(`data-page="${result.page-1}" ${result.page<=1?"disabled":""}`,text("previous_page"))}<span>${result.page} / ${Math.max(1,Math.ceil(result.total/result.page_size))}</span>${button(`data-page="${result.page+1}" ${result.page*result.page_size>=result.total?"disabled":""}`,text("next_page"))}</div>`;
        deps.bindNoteActions?.(root);
        root.querySelector("[data-layout]").value=query.layout;
        root.querySelector("[data-sort]").value=query.sort;
        const change = patch => {query={...query,...patch}; deps.listQuery?.(query); list(root);};
        root.querySelectorAll("[data-library-kind]").forEach(b=>b.onclick=()=>change({kind:b.dataset.libraryKind,state:"",page:1}));
        const clearState=root.querySelector("[data-clear-state]");if(clearState)clearState.onclick=()=>change({state:"",page:1});
        root.querySelector("[data-layout]").onchange=ev=>change({layout:ev.target.value,page:1});
        root.querySelector("[data-sort]").onchange=ev=>change({sort:ev.target.value,page:1});
        root.querySelectorAll("[data-page]").forEach(b=>b.onclick=()=>change({page:Number(b.dataset.page)}));
      } catch (error) { if(serial!==listSerial)return;root.innerHTML=`<p role="alert">${e(error.message)}</p>${button("data-reload",text("retry"))}`;root.querySelector?.("[data-reload]")?.addEventListener("click",()=>list(root)); }
    }
    const detailURL = (id,sel) => `/api/content-library/sets/${enc(id)}${sel?.kind?`?${new URLSearchParams({kind:sel.kind,version:sel.id})}`:""}`;
    async function show(root, id, q = {}) {
      const serial=++readSerial;
      const current=()=>serial===readSerial && root.isConnected && deps.isSetCurrent(id);
      root.innerHTML=`<p role="status">${text("loading")}</p>`;
      try {
        const d=await api(detailURL(id,{kind:q.kind,id:q.version}));
        if(!current())return;
        // Normalize default links once to an exact saved version. Refresh and
        // browser history must never silently switch the visible body.
        if(!q.version)deps.fixRoute?.(route(id,d.selection,q.member,q.section));
        const m=q.member?targetMember(d,q.member):null;
        if(q.member&&!m)throw new Error(t("workspace.entry_missing"));
        const fixed=(member="",section="")=>route(id,d.selection,member,section);
        const versions=`<label>${text("version")}<select data-library-version>${d.versions.map((v,i)=>`<option value="${i}" ${v.id===d.selection.id&&v.kind===d.selection.kind?"selected":""}>${e(deps.date(v.created_at))} · ${label(v.state)} · ${v.count} · ${e(v.id)}</option>`).join("")}</select></label>`;
        const sources=d.sources||[],members=d.members||[];
        const sourceRecords=(d.source_records||[]).map(s=>`<li>${e(s.locator)} <small>${e(s.verification_status)}</small></li>`).join("");
        const nav=`<nav aria-label="${text("directory")}" class="library-members">${link(fixed(),text("overview"))}<ol>${members.map(a=>`<li>${link(fixed(a.key),e(a.title),a.key===m?.key?"active":"")}</li>`).join("")}</ol>${sources.length?`<h4>${text("sources")}</h4><ul>${sources.map(a=>`<li>${link(fixed(a.key),e(a.title),a.key===m?.key?"active":"")}</li>`).join("")}</ul>`:""}</nav>`;
        root.innerHTML=`<div class="library-shell"><div class="library-toolbar">${link("#/notes",text("back"),"tf-btn")}${link(fixed(),e(d.title),"library-breadcrumb")}${versions}${button("data-copy-fixed",text("copy"))}</div>
          ${q.fallback==="1"?`<p role="status">${text("member_fallback")}</p>`:""}
          ${d.new_version?`<p class="library-notice">${text("new_version")} ${link(route(id,d.versions[0]),text("view_new"))}</p>`:""}
          ${/failed|cancelled/.test(d.task_state||"")?`<p class="library-notice" role="status">${text("task_failed")} ${e(t(`docset.state.${d.task_state}`))}</p>`:""}
          ${m?`<div class="library-mobile-controls">${button("data-directory-toggle",text("directory"))}${button("data-toc-toggle",text("sections"))}</div><div class="library-reader">${nav}<article class="library-paper"><div class="library-eyebrow">${e(m.role)} · ${label(d.state)}</div><h1>${e(m.title)}</h1><p>${text("frozen_notice")}</p><div class="tf-action-row">${button("data-download-member",text("download"))}${button("data-add-member-source",e(t("workspace_ui.add_source")))}${m.available?link(`#/notes?id=${enc(m.thought_id)}`,text("copy_note"),"tf-btn"):""}${m.modified?`<span class="pill">${text("modified")}</span>`:""}</div><div class="markdown-rendered" data-library-body data-section="${e(q.section||"")}"></div><footer class="library-pagination" data-library-pagination></footer></article><nav class="library-toc" aria-label="${text("sections")}" data-library-toc></nav></div>`:
          `<header class="library-overview-header"><div class="library-eyebrow">${text("set")}</div><h1>${e(d.title)}</h1><p>${e(d.description)}</p>${meta(d)}<div class="tf-action-row">${members[0]?link(fixed(resumeMember(d)),text("start_reading"),"tf-btn primary"):""}${button("data-add-set-sources",e(t("workspace_ui.add_set_sources")))}${link(`/api/content-library/sets/${enc(id)}/markdown?${new URLSearchParams({kind:d.selection.kind,version:d.selection.id})}`,text("download_all"),"tf-btn")}</div></header>
          <div class="library-overview-grid"><section><h2>${text("directory")}</h2><ol class="library-directory">${members.map((a,i)=>`<li>${link(fixed(a.key),`<span>${String(i+1).padStart(2,"0")}</span><strong>${e(a.title)}</strong><small>${e(a.role)}</small>`)}</li>`).join("")}</ol></section><aside><section class="library-status"><h3>${text("status")}</h3><p>${text("saved")} · ${label(d.state)}</p><p>${text("validation")}: ${e(d.validation? t(`library.validation.${d.validation}`):t("library.validation.not_reviewed"))}</p><p>${d.state==="finalized"?text("approved"):text("not_approved")}</p></section><section><h3>${text("questions")}</h3>${d.questions?.length?`<ul>${d.questions.map(a=>`<li>${e(a)}</li>`).join("")}</ul>`:`<p>${text("no_questions")}</p>`}</section><section><h3>${text("sources")}</h3><ul>${sources.map(a=>`<li>${link(fixed(a.key),e(a.title))}</li>`).join("")}</ul>${sourceRecords?`<ul>${sourceRecords}</ul>`:""}${!sources.length&&!sourceRecords?`<p>${text("no_sources")}</p>`:""}</section></aside></div>`}
          <details class="library-secondary"><summary>${text("tasks")}</summary><p>${e(d.task_state?t(`docset.state.${d.task_state}`):t("library.no_task"))}</p><code>${e(d.task_run_id||"")}</code>${d.task_run_id?button("data-task-details",text("task_record")):""}<div data-task-record></div></details>
          ${d.selection.kind==="revision"?`<details class="library-secondary" data-management><summary>${text("manage")}</summary><div class="library-management" data-management-body></div></details>`:""}</div>`;
        root.querySelector("[data-copy-fixed]").onclick=()=>deps.copy(`${location.origin}${location.pathname}${fixed(m?.key,q.section)}`);
        root.querySelector("[data-library-version]").onchange=async ev=>{
          const v=d.versions[Number(ev.target.value)];if(!v||!current())return;
          try {const target=await api(detailURL(id,v));if(!current())return;
            const key=m?versionMember(target,m.key):"";
            navigate(route(id,v,key)+(m&&key!==m.key?"&fallback=1":""));
          }catch(error){if(current())toast(error.message);}
        };
        root.querySelector("[data-add-set-sources]")?.addEventListener("click",()=>deps.addSources({set_id:id,selection:d.selection}));
        root.querySelector("[data-add-member-source]")?.addEventListener("click",()=>deps.addSources({set_id:id,selection:d.selection,member:m.key}));
        if(d.task_run_id)root.querySelector("[data-task-details]").onclick=async()=>{try{const run=await api(`/api/document-set-runs/${enc(d.task_run_id)}`);if(!current())return;const node=root.querySelector("[data-task-record]");node.innerHTML=`<p>${e(run.error||t(`docset.state.${run.state}`))}</p><ol>${(run.calls||[]).map(c=>`<li>${e(c.kind)} · ${e(c.member_key||"")} · ${e(c.state)} ${e(c.error||"")}</li>`).join("")}</ol>`;}catch(error){if(current())toast(error.message);}};
        const management=root.querySelector("[data-management]");
        if(management)management.ontoggle=()=>{if(management.open&&!management.dataset.loaded){management.dataset.loaded="1";deps.tools(d,root.querySelector("[data-management-body]"),current).catch(error=>{if(current()){management.dataset.loaded="";root.querySelector("[data-management-body]").textContent=error.message;}});}};
        if(!m){deps.reading?.({title:d.title,href:fixed(),kind:"set"});return;}
        const body=root.querySelector("[data-library-body]");
        body.innerHTML=markdown(m.body,m.thought_id,m.resources||[],{id,selection:d.selection,member:m.key,members});
        const used=new Map();const headings=[...body.querySelectorAll("h1,h2,h3,h4,h5,h6")];
        for(const h of headings){const base=h.textContent.trim().toLowerCase().replace(/[^\p{L}\p{N}\s_-]/gu,"").replace(/[\s_]+/g,"-")||"section";const n=used.get(base)||0;used.set(base,n+1);h.id=n?`${base}-${n}`:base;}
        const toc=root.querySelector("[data-library-toc]");toc.innerHTML=`<h3>${text("sections")}</h3>${headings.map(h=>link(fixed(m.key,h.id),e(h.textContent))).join("")}`;
        body.querySelectorAll('a[href^="#"]').forEach(a=>a.onclick=ev=>{const href=a.getAttribute("href");if(href.startsWith("#/"))return;ev.preventDefault();let section=href.slice(1);try{section=decodeURIComponent(section);}catch{}navigate(fixed(m.key,section));});
        const index=members.findIndex(a=>a.key===m.key);
        root.querySelector("[data-library-pagination]").innerHTML=(index>0?link(fixed(members[index-1].key),text("previous")):link(fixed(),text("overview")))+(index>=0&&index<members.length-1?link(fixed(members[index+1].key),text("next")):link(fixed(),text("overview")));
        root.querySelector("[data-download-member]").onclick=()=>download(m);
        root.querySelector("[data-directory-toggle]").onclick=()=>openNavigation(root.querySelector(".library-members"),t("library.directory"));
        root.querySelector("[data-toc-toggle]").onclick=()=>openNavigation(toc,t("library.sections"));
        deps.reading?.({title:`${d.title} · ${m.title}`,href:fixed(m.key,q.section),kind:"set"});
        try{localStorage.setItem(`thoughtflow.library-reading.${id}`,JSON.stringify({selection:d.selection,member:m.key}));}catch{}
        if(q.section)requestAnimationFrame(()=>{if(current())headings.find(h=>h.id===q.section)?.scrollIntoView({block:"start"});});
      }catch(error){if(current()){root.innerHTML=`${link("#/notes",text("back"),"tf-btn")}<p role="alert">${e(error.message)}</p>${button("data-retry-reading",text("retry"))}`;root.querySelector("[data-retry-reading]").onclick=()=>show(root,id,q);}}
    }
    function openNavigation(nav,title){
      const dialog=document.createElement("dialog");dialog.className="library-nav-dialog";
      dialog.innerHTML=`<header><h2>${e(title)}</h2>${button("data-close-navigation",e(t("common.close")))}</header><nav>${nav.innerHTML}</nav>`;
      nav.closest(".library-shell").appendChild(dialog);
      dialog.querySelector("[data-close-navigation]").onclick=()=>dialog.close();
      dialog.querySelectorAll("a").forEach(a=>a.addEventListener("click",event=>{event.preventDefault();const href=a.getAttribute("href");dialog.close();navigate(href,{force:true});}));
      dialog.onclose=()=>dialog.remove();dialog.showModal();
    }
    function resumeMember(d){try{const p=JSON.parse(localStorage.getItem(`thoughtflow.library-reading.${d.id}`));if(p?.selection?.id===d.selection.id&&p.selection.kind===d.selection.kind&&targetMember(d,p.member))return p.member;}catch{}return d.members?.[0]?.key||"";}
    function download(m){const url=URL.createObjectURL(new Blob([m.body],{type:"text/markdown;charset=utf-8"}));const a=document.createElement("a");a.href=url;a.download=m.filename||`${m.key.replace(/[^\p{L}\p{N}_-]/gu,"-")}.md`;a.click();setTimeout(()=>URL.revokeObjectURL(url),1000);}
    async function topic(root,id,isCurrent){
      const serial=++topicSerial;const current=()=>serial===topicSerial&&isCurrent();
      root.innerHTML=`<p role="status">${text("loading")}</p>`;
      try{
        const data=await api(`/api/topics/${enc(id)}/content-library`);if(!current())return;
        root.innerHTML=`<h2>${text("sets")}</h2><div class="library-grid">${data.sets.map(row=>`<section>${row.item.unavailable?`<p role="alert">${e(row.item.title)} · ${text("unavailable")}</p>`:card(row.item)}<details class="workspace-reference-policy"><summary>${text("reference")}</summary><label>${text("reference")}<select data-reference="${e(row.set_id)}">${["latest_archive","latest_working","latest_finalized","pinned"].map(policy=>`<option value="${policy}" ${row.policy===policy?"selected":""}>${text(policy)}</option>`).join("")}</select></label>${row.item.versions?.length?`<label>${text("version")}<select data-reference-version="${e(row.set_id)}">${row.item.versions.map((v,i)=>`<option value="${i}" ${v.kind===row.item.selection.kind&&v.id===row.item.selection.id?"selected":""}>${e(deps.date(v.created_at))} · ${label(v.state)} · ${e(v.id)}</option>`).join("")}</select></label>`:""}${button(`data-reference-save="${e(row.set_id)}"`,text("save_reference"))}<p role="status" data-reference-message="${e(row.set_id)}"></p></details></section>`).join("")||`<p>${text("empty")}</p>`}</div>
          <details class="library-secondary"><summary>${text("add_set")}</summary><form data-add-reference><input name="set_id" type="hidden" required><button type="button" class="tf-btn" data-pick-reference>${e(t("workspace_ui.choose_set"))}</button><p data-picked-reference></p><label>${text("reference")}<select name="policy">${["latest_archive","latest_working","latest_finalized"].map(p=>`<option value="${p}">${text(p)}</option>`).join("")}</select></label><button class="tf-btn">${text("add_set")}</button><p role="alert"></p></form></details>
          <h2>${text("independent")}</h2><div class="library-grid">${data.notes.map(card).join("")||`<p>${text("empty")}</p>`}</div><h2>${text("sources")}</h2><ul>${data.sources.map(i=>`<li>${i.kind==="reference"?`${e(i.title)} · ${e(i.description)}`:link(i.set_id?route(i.set_id,i.selection,i.member_key):`#/notes?id=${enc(i.id)}`,e(i.title))}</li>`).join("")}</ul>`;
        const save=async entry=>{const entries=data.links.entries.filter(a=>a.set_id!==entry.set_id);entries.push(entry);await api(`/api/topics/${enc(id)}/content-links`,{method:"PUT",body:JSON.stringify({operation_id:crypto.randomUUID(),expected_generation:data.links.generation,entries})});if(current())await topic(root,id,isCurrent);};
        root.querySelectorAll("[data-reference-save]").forEach(b=>b.onclick=async()=>{const row=data.sets.find(a=>a.set_id===b.dataset.referenceSave);const select=[...root.querySelectorAll("[data-reference]")].find(a=>a.dataset.reference===row.set_id);const v=[...root.querySelectorAll("[data-reference-version]")].find(a=>a.dataset.referenceVersion===row.set_id);const entry={set_id:row.set_id,policy:select.value,selection:{kind:"",id:""}};if(entry.policy==="pinned"){const selected=row.item.versions?.[Number(v?.value)];if(!selected)return;entry.selection={kind:selected.kind,id:selected.id};}b.disabled=true;try{await save(entry);}catch(error){if(current()){b.disabled=false;[...root.querySelectorAll("[data-reference-message]")].find(a=>a.dataset.referenceMessage===row.set_id).textContent=error.message;}}});
        root.querySelector("[data-pick-reference]").onclick=async()=>{const picked=await deps.pickSet();if(!picked||!current())return;const form=root.querySelector("[data-add-reference]");form.elements.set_id.value=picked.id;root.querySelector("[data-picked-reference]").textContent=picked.title;};
        root.querySelector("[data-add-reference]").onsubmit=async ev=>{ev.preventDefault();const f=ev.target;const b=f.querySelector("button");b.disabled=true;try{if(!f.set_id.value.trim())throw new Error(t("workspace_ui.select_first"));await save({set_id:f.set_id.value.trim(),policy:f.policy.value,selection:{kind:"",id:""}});}catch(error){if(current()){b.disabled=false;f.querySelector('[role="alert"]').textContent=error.message;}}};
      }catch(error){if(current()){root.innerHTML=`<p role="alert">${e(error.message)}</p>${button("data-retry-topic",text("retry"))}`;root.querySelector("[data-retry-topic]").onclick=()=>topic(root,id,isCurrent);}}
    }
    return {list, home, stats, show, topic, invalidate(){listSerial++;readSerial++;topicSerial++;homeSerial++;}, card};
  }
  global.ThoughtFlowLibrary={create,route,targetMember,versionMember};
})(globalThis);
