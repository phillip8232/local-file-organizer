"use strict";

// ---------- helpers ----------

const $ = (id) => document.getElementById(id);

// h builds DOM nodes; text is always inserted as text, never as HTML.
function h(tag, attrs, ...kids) {
  const el = document.createElement(tag);
  for (const [k, v] of Object.entries(attrs || {})) {
    if (v == null || v === false) continue;
    if (k === "class") el.className = v;
    else if (k.startsWith("on")) el.addEventListener(k.slice(2), v);
    else if (k === "checked" || k === "disabled" || k === "open") el[k] = !!v;
    else el.setAttribute(k, v === true ? "" : v);
  }
  for (const kid of kids.flat()) {
    if (kid == null || kid === false) continue;
    el.append(kid instanceof Node ? kid : document.createTextNode(String(kid)));
  }
  return el;
}

async function api(method, path, body) {
  const res = await fetch(path, {
    method,
    headers: { "X-Organizer": "1", "Content-Type": "application/json" },
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  let data = null;
  try { data = await res.json(); } catch { /* empty body */ }
  if (!res.ok) throw new Error((data && data.error) || res.statusText);
  return data;
}

function human(n) {
  if (!n) return "0 B";
  const u = ["B", "KB", "MB", "GB", "TB"];
  let i = 0;
  while (n >= 1024 && i < u.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(n < 10 ? 1 : 0)) + " " + u[i];
}
const num = (n) => Number(n || 0).toLocaleString();
const dateNs = (ns) => ns ? new Date(ns / 1e6).toLocaleDateString(undefined, { year: "numeric", month: "short", day: "numeric" }) : "";
const dateS = (s) => s ? new Date(s * 1000).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" }) : "never";

function splitPath(p) {
  const i = Math.max(p.lastIndexOf("/"), p.lastIndexOf("\\"));
  return i < 0 ? ["", p] : [p.slice(0, i + 1), p.slice(i + 1)];
}
function pathNode(p) {
  const [dir, base] = splitPath(p);
  return h("span", { class: "path", title: p }, h("span", { class: "dir" }, dir), base);
}
function revealBtn(path) {
  return h("button", { class: "link small", type: "button", title: "Show in folder",
    onclick: () => api("POST", "/api/reveal", { path }).catch(showError) }, "Show");
}

function showError(err) {
  const el = $("error");
  el.textContent = err ? String(err.message || err) : "";
  el.hidden = !err;
}

// dialog shows a modal and resolves true when OK is pressed.
function dialog(title, body, { ok = "OK", cancel = "Cancel", danger = false } = {}) {
  const d = $("dialog");
  $("dialog-title").textContent = title;
  $("dialog-body").replaceChildren(...[body].flat());
  $("dialog-ok").textContent = ok;
  $("dialog-ok").className = danger ? "danger" : "primary";
  $("dialog-cancel").textContent = cancel;
  $("dialog-cancel").hidden = cancel === null;
  return new Promise((resolve) => {
    d.addEventListener("close", () => resolve(d.returnValue === "ok"), { once: true });
    d.returnValue = "cancel";
    d.showModal();
  });
}

function resultBody(res, verb) {
  const parts = [h("p", {}, `${verb}: ${num(res.done)}. Skipped: ${num(res.skipped)}. Failed: ${num(res.failed)}. (${human(res.bytes)})`)];
  if (res.messages && res.messages.length) parts.push(h("div", { class: "messages" }, res.messages.map((m) => h("div", {}, m))));
  return parts;
}

// ---------- state ----------

const state = {
  tab: "cleanup",
  status: null,
  cleanup: null,   // { plan, report }
  sel: new Set(),  // selected cleanup action ids
  groups: {},      // dup group id -> { keep, del: Set(paths) }
  sort: null,      // { plan }
  sortSel: new Set(),
  metas: [],       // functions that refresh cleanup section counts
  sortMetas: [],   // same for the sort sections
  balance: null,   // { plan, drives, note, skipped }
  balSel: new Set(),
  balMetas: [],
  drives: [],      // current drive list for the bars
  wasScanning: false,
};

const CATEGORIES = [
  { key: "empty-dir", title: "Empty folders", note: "Hold nothing but empty folders or OS files like Thumbs.db." },
  { key: "empty-file", title: "Empty files", note: "Zero bytes." },
  { key: "junk", title: "Junk", note: "Partial downloads (.crdownload, .part) and Office lock files, all older than a day." },
  { key: "old-download", title: "Old downloads", note: "Files sitting directly in Downloads, untouched for a long time. Not selected; tick what you want gone." },
  { key: "large", title: "Largest files", note: "For review only. Not selected." },
];
const PAGE = 200;

// ---------- status & scanning ----------

async function refreshStatus() {
  try {
    const s = await api("GET", "/api/status");
    state.status = s;
    const st = s.stats;
    $("status-line").textContent = st.lastScan
      ? `${num(st.files)} files indexed (${human(st.bytes)}) · last scan ${dateS(st.lastScan)} · ${s.roots.length} folders`
      : `Not scanned yet · ${s.roots.length} folders: ${s.roots.map((r) => splitPath(r)[1] || r).join(", ")}`;
    const pill = $("holding-pill");
    pill.hidden = !s.holding.items;
    pill.textContent = `Holding: ${num(s.holding.items)} items · ${human(s.holding.bytes)}`;
    const scanning = s.busy === "scan";
    const working = s.busy === "apply" || s.busy === "undo";
    $("scan-btn").disabled = !!s.busy;
    $("scan-box").hidden = !scanning && !working;
    $("scan-cancel").hidden = !scanning;
    if (scanning && s.scan) showScan(s.scan);
    if (working && s.progress) showWork(s.busy, s.progress);
    if (s.lastError) showError(s.lastError);
    if (state.wasScanning && !scanning) {
      state.wasScanning = false;
      if (!s.lastError && state.tab === "cleanup") buildCleanup();
    }
    if (scanning) state.wasScanning = true;
    setTimeout(refreshStatus, scanning || working ? 700 : 8000);
  } catch (e) {
    showError(e);
    setTimeout(refreshStatus, 5000);
  }
}

function showScan(p) {
  const bar = $("scan-bar");
  if (p.phase === "prehash" || p.phase === "hashing") {
    const label = p.phase === "prehash" ? "Sampling possible duplicates" : "Confirming duplicates";
    $("scan-text").textContent = `${label}: ${num(p.hashed)} of ${num(p.toHash)}`;
    bar.classList.remove("indeterminate");
    bar.style.width = (p.toHash ? Math.round((100 * p.hashed) / p.toHash) : 100) + "%";
  } else {
    $("scan-text").textContent = `Reading folders: ${num(p.files)} files, ${human(p.bytes)}`;
    bar.classList.add("indeterminate");
    bar.style.width = "";
  }
}

// showWork shows an apply or undo in progress, by bytes when copying.
function showWork(job, p) {
  const bar = $("scan-bar");
  bar.classList.remove("indeterminate");
  const verb = job === "undo" ? "Undoing" : "Working";
  let text = `${verb}: ${num(p.itemsDone)} of ${num(p.items)} items`;
  let frac = p.items ? p.itemsDone / p.items : 0;
  if (p.bytes > 0) {
    text += ` · ${human(p.bytesDone)} of ${human(p.bytes)} copied`;
    frac = p.bytesDone / p.bytes;
  }
  if (p.current) text += ` · ${splitPath(p.current)[1]}`;
  $("scan-text").textContent = text;
  bar.style.width = Math.min(100, Math.round(frac * 100)) + "%";
}

// waitForJob resolves with the result of the background apply or undo that
// was just started.
async function waitForJob(prevId) {
  for (;;) {
    await new Promise((r) => setTimeout(r, 700));
    let s;
    try { s = await api("GET", "/api/status"); } catch { continue; }
    if (s.busy === "apply" || s.busy === "undo") {
      $("scan-box").hidden = false;
      $("scan-cancel").hidden = true;
      if (s.progress) showWork(s.busy, s.progress);
      continue;
    }
    if (s.lastResult && s.lastResult.id > prevId) {
      $("scan-box").hidden = true;
      return s.lastResult;
    }
  }
}

async function lastJobId() {
  try {
    const s = await api("GET", "/api/status");
    return s.lastResult ? s.lastResult.id : 0;
  } catch { return 0; }
}

async function startScan() {
  showError(null);
  try {
    await api("POST", "/api/scan");
    state.wasScanning = true;
    $("scan-box").hidden = false;
    $("scan-btn").disabled = true;
    showScan({ phase: "walking", files: 0, bytes: 0 });
  } catch (e) { showError(e); }
}

// ---------- tabs ----------

function setTab(tab) {
  state.tab = tab;
  for (const b of document.querySelectorAll(".tabs button")) b.setAttribute("aria-selected", String(b.dataset.tab === tab));
  for (const t of ["cleanup", "sort", "drives", "history", "rules"]) $("tab-" + t).hidden = t !== tab;
  if (tab === "history") loadHistory();
  if (tab === "drives" && !state.balance) loadDrives();
  if (tab === "rules") loadRules();
  updateApproveBar();
}

// ---------- clean up ----------

async function buildCleanup() {
  showError(null);
  const btn = $("build-cleanup");
  btn.disabled = true;
  try {
    const data = await api("POST", "/api/plan/cleanup");
    state.cleanup = data;
    state.sel = new Set(data.plan.actions.filter((a) => a.selected).map((a) => a.id));
    state.groups = {};
    for (const g of data.plan.groups || []) {
      const keep = g.files[g.keep].path;
      state.groups[g.id] = { keep, del: new Set(g.files.map((f) => f.path).filter((p) => p !== keep)) };
    }
    renderCleanup();
  } catch (e) { showError(e); } finally { btn.disabled = false; }
}

function card(n, label) {
  return h("div", { class: "card" }, h("div", { class: "num" }, n), h("div", { class: "lbl" }, label));
}

function renderCleanup() {
  const { plan, report } = state.cleanup;
  state.metas = [];
  const byCat = {};
  for (const a of plan.actions) (byCat[a.category] ||= []).push(a);
  const groups = plan.groups || [];
  const sum = (xs) => xs.reduce((t, a) => t + (a.size || 0), 0);

  $("cleanup-summary").replaceChildren(
    card(num(groups.length), `duplicate groups · ${human(report.dupWaste)} to reclaim`),
    card(num((byCat["empty-dir"] || []).length), "empty folders"),
    card(num((byCat["empty-file"] || []).length), "empty files"),
    card(num((byCat["junk"] || []).length), `junk files · ${human(sum(byCat["junk"] || []))}`),
    card(num((byCat["old-download"] || []).length), `old downloads · ${human(sum(byCat["old-download"] || []))}`),
  );

  const body = [];
  if (!groups.length && !plan.actions.length) {
    body.push(h("div", { class: "empty" }, report.stats.lastScan ? "Nothing to clean up. Nice." : "Scan first, then look for things to clean up."));
  }
  if (groups.length) body.push(dupSection(groups));
  for (const c of CATEGORIES) if (byCat[c.key]) body.push(catSection(c, byCat[c.key]));
  const left = [];
  if (report.projects && report.projects.length) left.push(`${num(report.projects.length)} project folders`);
  if (report.stats.placeholders) left.push(`${num(report.stats.placeholders)} cloud or online-only files`);
  if (left.length) body.push(h("p", { class: "muted" }, `Left alone on purpose: ${left.join(" and ")}.`));
  $("cleanup-body").replaceChildren(...body);
  updateApproveBar();
}

function sectionShell(title, note, items, toggleAll, metaFn, badge, bucket = state.metas) {
  const all = h("input", { type: "checkbox", "aria-label": "Select all in " + title,
    onclick: (e) => e.stopPropagation(), onchange: (e) => { toggleAll(e.target.checked); } });
  const meta = h("span", { class: "section-meta" });
  const refresh = () => {
    const m = metaFn();
    meta.textContent = `${num(m.sel)} of ${num(m.total)} selected · ${human(m.bytes)}`;
    all.checked = m.sel > 0 && m.sel === m.total;
    all.indeterminate = m.sel > 0 && m.sel < m.total;
  };
  bucket.push(refresh);
  const d = h("details", { class: "section", open: true },
    h("summary", {}, all, h("span", { class: "section-title" }, title), badge ? h("span", { class: "badge" }, badge) : null, meta),
    note ? h("p", { class: "section-note" }, note) : null,
    items);
  refresh();
  return d;
}

// paged appends rows to list PAGE at a time.
function paged(list, total, renderRange) {
  let shown = 0;
  const more = h("div", { class: "more" });
  const step = () => {
    const end = Math.min(total, shown + PAGE);
    renderRange(shown, end);
    shown = end;
    more.replaceChildren(shown < total
      ? h("button", { type: "button", class: "small", onclick: step }, `Show ${num(Math.min(PAGE, total - shown))} more of ${num(total - shown)}`)
      : "");
  };
  step();
  return more;
}

function catSection(c, actions) {
  const list = h("div", { class: "list" });
  const boxes = new Map();
  const more = paged(list, actions.length, (from, to) => {
    for (const a of actions.slice(from, to)) {
      const cb = h("input", { type: "checkbox", checked: state.sel.has(a.id), "aria-label": "Delete " + a.src,
        onchange: (e) => { e.target.checked ? state.sel.add(a.id) : state.sel.delete(a.id); updateCounts(); } });
      boxes.set(a.id, cb);
      list.append(h("div", { class: "item" }, cb, pathNode(a.src),
        h("span", { class: "meta" }, a.isDir ? "folder" : `${human(a.size)} · ${dateNs(a.mtime)}`), revealBtn(a.src)));
    }
  });
  const toggleAll = (on) => {
    for (const a of actions) on ? state.sel.add(a.id) : state.sel.delete(a.id);
    for (const [id, cb] of boxes) cb.checked = state.sel.has(id);
    updateCounts();
  };
  const metaFn = () => {
    let sel = 0, bytes = 0;
    for (const a of actions) if (state.sel.has(a.id)) { sel++; bytes += a.size || 0; }
    return { sel, total: actions.length, bytes };
  };
  return sectionShell(c.title, c.note, [list, more], toggleAll, metaFn,
    c.key === "old-download" || c.key === "large" ? "review" : null);
}

function dupSection(groups) {
  const list = h("div", { class: "list" });
  const renders = new Map();
  const renderGroup = (g, el) => {
    const choice = state.groups[g.id];
    const rows = g.files.map((f) => {
      const isKeep = f.path === choice.keep;
      const radio = h("input", { type: "radio", name: "keep-" + g.id, checked: isKeep, "aria-label": "Keep " + f.path,
        onchange: () => {
          const old = choice.keep;
          choice.keep = f.path;
          choice.del.delete(f.path);
          choice.del.add(old);
          renderGroup(g, el);
          updateCounts();
        } });
      const cb = h("input", { type: "checkbox", checked: !isKeep && choice.del.has(f.path), disabled: isKeep,
        "aria-label": "Delete " + f.path,
        onchange: (e) => { e.target.checked ? choice.del.add(f.path) : choice.del.delete(f.path); updateCounts(); } });
      return h("div", { class: "item" + (isKeep ? " keep" : "") },
        h("span", { class: "keep-label" }, radio, cb), pathNode(f.path),
        h("span", { class: "meta" }, isKeep ? "keep" : dateNs(f.mtime)), revealBtn(f.path));
    });
    el.replaceChildren(
      h("div", { class: "group-head" }, `${human(g.size)} × ${g.files.length} copies`,
        h("span", { class: "muted" }, `· saves ${human(g.size * choice.del.size)}`)),
      ...rows);
  };
  const more = paged(list, groups.length, (from, to) => {
    for (const g of groups.slice(from, to)) {
      const el = h("div", { class: "group" });
      renderGroup(g, el);
      renders.set(g.id, () => renderGroup(g, el));
      list.append(el);
    }
  });
  const toggleAll = (on) => {
    for (const g of groups) {
      const c = state.groups[g.id];
      c.del = new Set(on ? g.files.map((f) => f.path).filter((p) => p !== c.keep) : []);
    }
    for (const r of renders.values()) r();
    updateCounts();
  };
  const metaFn = () => {
    let sel = 0, total = 0, bytes = 0;
    for (const g of groups) {
      const n = state.groups[g.id].del.size;
      sel += n; total += g.files.length - 1; bytes += n * g.size;
    }
    return { sel, total, bytes };
  };
  return sectionShell("Duplicates",
    "Identical content, confirmed with a full BLAKE3 hash. One copy in each group is always kept (the radio button); it's re-checked before anything is deleted.",
    [list, more], toggleAll, metaFn);
}

function cleanupSelection() {
  if (!state.cleanup) return { count: 0, bytes: 0, byCat: {} };
  const byCat = {};
  let count = 0, bytes = 0;
  const add = (cat, size) => { byCat[cat] ||= { n: 0, bytes: 0 }; byCat[cat].n++; byCat[cat].bytes += size; count++; bytes += size; };
  for (const a of state.cleanup.plan.actions) if (state.sel.has(a.id)) add(a.category, a.size || 0);
  for (const g of state.cleanup.plan.groups || []) for (let i = 0; i < state.groups[g.id].del.size; i++) add("duplicate", g.size);
  return { count, bytes, byCat };
}

function updateCounts() {
  for (const f of [...state.metas, ...state.sortMetas, ...state.balMetas]) f();
  updateApproveBar();
}

function updateApproveBar() {
  const bar = $("approve-bar");
  if (state.tab === "cleanup" && state.cleanup) {
    const s = cleanupSelection();
    bar.hidden = false;
    $("approve-text").textContent = `${num(s.count)} items selected · ${human(s.bytes)}`;
    $("approve-btn").disabled = s.count === 0;
  } else if (state.tab === "sort" && state.sort) {
    bar.hidden = false;
    $("approve-text").textContent = `${num(state.sortSel.size)} moves selected`;
    $("approve-btn").disabled = state.sortSel.size === 0;
  } else if (state.tab === "drives" && state.balance) {
    const bytes = state.balance.plan.actions.filter((a) => state.balSel.has(a.id)).reduce((t, a) => t + a.size, 0);
    bar.hidden = false;
    $("approve-text").textContent = `${num(state.balSel.size)} items selected · ${human(bytes)}`;
    $("approve-btn").disabled = state.balSel.size === 0;
  } else {
    bar.hidden = true;
  }
}

const CAT_LABEL = { duplicate: "duplicates", "empty-dir": "empty folders", "empty-file": "empty files", junk: "junk files",
  "old-download": "old downloads", large: "large files" };

async function approve() {
  showError(null);
  const days = state.status ? state.status.holdingDays : 30;
  if (state.tab === "cleanup") {
    const s = cleanupSelection();
    const ok = await dialog("Delete selected items?", [
      h("ul", {}, Object.entries(s.byCat).map(([c, v]) => h("li", {}, `${num(v.n)} ${CAT_LABEL[c] || c} (${human(v.bytes)})`))),
      h("p", {}, `They move to the holding area for ${days} days, then to the OS trash. You can undo the whole batch from History.`),
      h("p", { class: "muted" }, "Each item is re-checked first. Anything that changed since the scan is skipped."),
    ], { ok: `Delete ${num(s.count)} items` });
    if (!ok) return;
    const groups = {};
    for (const [id, c] of Object.entries(state.groups)) if (c.del.size) groups[id] = { keep: c.keep, delete: [...c.del] };
    await runApply(state.cleanup.plan.id, { ids: [...state.sel], groups }, "Deleted");
    state.cleanup = null;
    $("cleanup-summary").replaceChildren();
    $("cleanup-body").replaceChildren(h("div", { class: "empty" }, "Done. Rescanning so the next plan is up to date…"));
  } else if (state.tab === "sort") {
    const ok = await dialog("Move selected files?", [
      h("p", {}, `${num(state.sortSel.size)} files will be moved into their new folders. Existing files are never overwritten; a clash gets a name like "file (2)".`),
      h("p", { class: "muted" }, "You can undo the whole batch from History."),
    ], { ok: `Move ${num(state.sortSel.size)} files` });
    if (!ok) return;
    await runApply(state.sort.plan.id, { ids: [...state.sortSel], groups: {} }, "Moved");
    state.sort = null;
    $("sort-body").replaceChildren(h("div", { class: "empty" }, "Done. Rescanning…"));
  } else if (state.tab === "drives") {
    const acts = state.balance.plan.actions.filter((a) => state.balSel.has(a.id));
    const bytes = acts.reduce((t, a) => t + a.size, 0);
    const minutes = Math.max(1, Math.round(bytes / (500 * 1024 * 1024) / 60));
    const ok = await dialog("Move selected folders to another drive?", [
      h("p", {}, `${num(acts.length)} items (${human(bytes)}) will be copied to the other drive. Every file is checked against the original, and only then is the original removed.`),
      h("p", {}, `This can take a while: roughly ${minutes} minute${minutes === 1 ? "" : "s"} on fast SSDs. You can keep using the page; progress shows at the top.`),
      h("p", { class: "muted" }, "Undo in History copies everything back the same careful way."),
    ], { ok: `Move ${human(bytes)}` });
    if (!ok) return;
    await runApply(state.balance.plan.id, { ids: [...state.balSel], groups: {} }, "Moved");
    state.balance = null;
    $("balance-body").replaceChildren(h("div", { class: "empty" }, "Done."));
    loadDrives();
  }
  updateApproveBar();
  startScan();
}

// runApply starts a batch in the background and shows its result when it
// finishes.
async function runApply(planId, approval, verb) {
  $("approve-btn").disabled = true;
  try {
    const prev = await lastJobId();
    await api("POST", `/api/plan/${planId}/apply`, approval);
    const job = await waitForJob(prev);
    if (job.error) throw new Error(job.error);
    await dialog(`Batch #${job.result.batch} finished`, resultBody(job.result, verb), { ok: "Close", cancel: null });
  } catch (e) { showError(e); }
}

// ---------- sort ----------

async function buildSort() {
  showError(null);
  const btn = $("build-sort");
  btn.disabled = true;
  try {
    state.sort = await api("POST", "/api/plan/sort");
    state.sortSel = new Set(state.sort.plan.actions.filter((a) => a.selected).map((a) => a.id));
    renderSort();
  } catch (e) { showError(e); } finally { btn.disabled = false; }
}

function renderSort() {
  state.sortMetas = [];
  const actions = state.sort.plan.actions;
  if (!actions.length) {
    $("sort-body").replaceChildren(h("div", { class: "empty" }, "Nothing to sort with the current rules."));
    updateApproveBar();
    return;
  }
  const byRule = new Map();
  for (const a of actions) {
    const key = a.rule || a.reason;
    if (!byRule.has(key)) byRule.set(key, []);
    byRule.get(key).push(a);
  }
  const sections = [];
  for (const [rule, acts] of byRule) {
    const list = h("div", { class: "list" });
    const boxes = new Map();
    const more = paged(list, acts.length, (from, to) => {
      for (const a of acts.slice(from, to)) {
        const cb = h("input", { type: "checkbox", checked: state.sortSel.has(a.id), "aria-label": "Move " + a.src,
          onchange: (e) => { e.target.checked ? state.sortSel.add(a.id) : state.sortSel.delete(a.id); updateCounts(); } });
        boxes.set(a.id, cb);
        const [dstDir, dstName] = splitPath(a.dst);
        const renamed = dstName !== splitPath(a.src)[1];
        list.append(h("div", { class: "item" }, cb, pathNode(a.src), h("span", { class: "meta" }, human(a.size)), revealBtn(a.src),
          h("span", { class: "dest" }, "→ " + dstDir, renamed ? h("strong", { class: "new-name" }, dstName) : null)));
      }
    });
    const toggleAll = (on) => {
      for (const a of acts) on ? state.sortSel.add(a.id) : state.sortSel.delete(a.id);
      for (const [id, cb] of boxes) cb.checked = state.sortSel.has(id);
      updateCounts();
    };
    const metaFn = () => {
      let sel = 0, bytes = 0;
      for (const a of acts) if (state.sortSel.has(a.id)) { sel++; bytes += a.size; }
      return { sel, total: acts.length, bytes };
    };
    sections.push(sectionShell(rule, "", [list, more], toggleAll, metaFn, null, state.sortMetas));
  }
  $("sort-body").replaceChildren(...sections);
  updateApproveBar();
}

// ---------- drives ----------

async function loadDrives() {
  try {
    const d = await api("GET", "/api/drives");
    state.drives = d.drives || [];
    renderDriveBars();
  } catch (e) { showError(e); }
}

// renderDriveBars draws each drive now, after the ticked moves, and its
// equal-share target.
function renderDriveBars() {
  const plan = state.balance;
  const drives = plan ? plan.drives : state.drives;
  const after = {};
  for (const d of drives) after[d.root] = d.total - d.free;
  if (plan) {
    for (const a of plan.plan.actions) {
      if (!state.balSel.has(a.id)) continue;
      after[a.fromVol] -= a.size;
      after[a.toVol] += a.size;
    }
  }
  const pct = (n, t) => (t ? (100 * n) / t : 0);
  const bars = drives.map((d) => {
    const used = d.total - d.free;
    const nowEl = h("div", { class: "now" });
    nowEl.style.width = pct(used, d.total).toFixed(1) + "%";
    const afterEl = h("div", { class: "after" });
    afterEl.style.width = pct(after[d.root], d.total).toFixed(1) + "%";
    const kids = [nowEl, afterEl];
    if (plan) {
      const t = h("div", { class: "target", title: "Equal share" });
      t.style.left = pct(d.target, d.total).toFixed(1) + "%";
      kids.push(t);
    }
    const label = `${d.root}${d.label ? " " + d.label : ""}${d.system ? " (system)" : ""}`;
    const change = plan && after[d.root] !== used ? ` → ${pct(after[d.root], d.total).toFixed(0)}%` : "";
    return h("div", { class: "drive" },
      h("div", { class: "drive-head" }, h("strong", {}, label),
        h("span", { class: "muted" }, `${human(used)} of ${human(d.total)} · ${pct(used, d.total).toFixed(0)}%${change} · ${human(d.free)} free`)),
      h("div", { class: "drive-bar" }, ...kids));
  });
  if (plan) {
    bars.push(h("div", { class: "legend" }, h("span", {}, h("i", { class: "l-now" }), "now"),
      h("span", {}, h("i", { class: "l-after" }), "after the ticked moves"), h("span", {}, h("i", { class: "l-target" }), "equal share")));
  }
  $("drive-bars").replaceChildren(...bars);
}

async function buildBalance() {
  showError(null);
  const btn = $("build-balance");
  btn.disabled = true;
  try {
    state.balance = await api("POST", "/api/plan/balance");
    state.balSel = new Set(state.balance.plan.actions.filter((a) => a.selected).map((a) => a.id));
    renderBalance();
  } catch (e) { showError(e); } finally { btn.disabled = false; }
}

function renderBalance() {
  state.balMetas = [];
  const { plan, note, skipped } = state.balance;
  const body = [];
  if (note) body.push(h("p", { class: "note" }, note));
  const byRoute = new Map();
  for (const a of plan.actions) {
    const key = `${a.fromVol} → ${a.toVol}`;
    if (!byRoute.has(key)) byRoute.set(key, []);
    byRoute.get(key).push(a);
  }
  for (const [route, acts] of byRoute) {
    const list = h("div", { class: "list" });
    const boxes = new Map();
    for (const a of acts) {
      const cb = h("input", { type: "checkbox", checked: state.balSel.has(a.id), "aria-label": "Move " + a.src,
        onchange: (e) => { e.target.checked ? state.balSel.add(a.id) : state.balSel.delete(a.id); updateCounts(); renderDriveBars(); } });
      boxes.set(a.id, cb);
      list.append(h("div", { class: "item" }, cb, pathNode(a.src),
        h("span", { class: "meta" }, `${human(a.size)} · ${num(a.count)} file${a.count === 1 ? "" : "s"}`), revealBtn(a.src),
        h("span", { class: "dest" }, "→ " + a.dst + "  ·  " + a.reason)));
    }
    const toggleAll = (on) => {
      for (const a of acts) on ? state.balSel.add(a.id) : state.balSel.delete(a.id);
      for (const [id, cb] of boxes) cb.checked = state.balSel.has(id);
      updateCounts();
      renderDriveBars();
    };
    const metaFn = () => {
      let sel = 0, bytes = 0;
      for (const a of acts) if (state.balSel.has(a.id)) { sel++; bytes += a.size; }
      return { sel, total: acts.length, bytes };
    };
    body.push(sectionShell(route, "Whole folders keep their place: the same path on the other drive.", [list], toggleAll, metaFn, null, state.balMetas));
  }
  if (!plan.actions.length && !note) body.push(h("div", { class: "empty" }, "Nothing to move."));
  if (skipped && skipped.length) {
    body.push(h("details", { class: "section" }, h("summary", {}, h("span", { class: "section-title" }, "Left alone"),
      h("span", { class: "section-meta" }, `${skipped.length} big folders`)),
      h("div", { class: "list" }, skipped.map((s) => h("div", { class: "item" }, h("span", {}), h("span", { class: "path" }, s))))));
  }
  $("balance-body").replaceChildren(...body);
  renderDriveBars();
  updateApproveBar();
}

// ---------- history ----------

async function loadHistory() {
  try {
    const data = await api("GET", "/api/history");
    const hd = data.holding;
    const days = h("input", { type: "number", min: "0", value: String(hd.days), class: "small", "aria-label": "Days" });
    days.size = 4;
    $("holding-box").replaceChildren(
      h("p", {}, hd.items
        ? `Holding area: ${num(hd.items)} deleted items, ${human(hd.bytes)}. They go to the OS trash after ${hd.days} days, oldest held since ${dateS(hd.oldest)}.`
        : `The holding area is empty. Deleted items wait here ${hd.days} days before going to the OS trash.`),
      hd.items ? h("div", { class: "row" }, "Send items held at least", days, "days to the OS trash",
        h("button", { type: "button", class: "small", onclick: () => purge(Number(days.value)) }, "Send now")) : null,
    );
    const list = data.batches.map(batchRow);
    $("history-body").replaceChildren(...(list.length ? list : [h("div", { class: "empty" }, "Nothing has been changed yet.")]));
  } catch (e) { showError(e); }
}

function batchRow(b) {
  const counts = Object.entries(b.counts).map(([k, v]) => `${num(v)} ${k}`).join(", ");
  const steps = h("div", { class: "steps", hidden: true });
  const undoable = (b.counts.done || 0) + (b.counts.purged || 0) > 0 && !b.state.startsWith("undone");
  return h("div", { class: "batch" },
    h("div", { class: "batch-head" },
      h("strong", {}, "#" + b.id), h("span", { class: "when" }, dateS(b.created)),
      h("span", {}, b.summary), h("span", { class: "state " + b.state.replace(/\s+/g, "-") }, b.state),
      h("span", { class: "muted" }, counts),
      h("span", { class: "actions" },
        h("button", { type: "button", class: "small", onclick: async () => {
          if (steps.hidden) {
            const d = await api("GET", `/api/batch/${b.id}`).catch(showError);
            if (!d) return;
            steps.replaceChildren(...d.steps.map((s) => h("div", { class: "step" },
              h("span", {}, s.state), h("span", {}, s.op),
              h("span", { class: "path" }, s.src + (s.dst ? "  →  " + s.dst : "") + (s.error ? "  (" + s.error + ")" : "")))));
          }
          steps.hidden = !steps.hidden;
        } }, "Details"),
        undoable ? h("button", { type: "button", class: "small", onclick: () => undo(b) }, "Undo") : null)),
    steps);
}

async function undo(b) {
  const ok = await dialog(`Undo batch #${b.id}?`,
    h("p", {}, "Everything this batch moved or deleted goes back to where it was. If a name is taken now, the file comes back as \"name (2)\"."),
    { ok: "Undo" });
  if (!ok) return;
  try {
    const prev = await lastJobId();
    await api("POST", `/api/batch/${b.id}/undo`);
    const job = await waitForJob(prev);
    if (job.error) throw new Error(job.error);
    await dialog(`Batch #${b.id} undone`, resultBody(job.result, "Restored"), { ok: "Close", cancel: null });
  } catch (e) { showError(e); }
  loadHistory();
  refreshStatusOnce();
}

async function purge(days) {
  if (!(days >= 0)) return;
  const ok = await dialog("Send to the OS trash?",
    h("p", {}, `Items held at least ${days} days leave the holding area and go to the OS trash, where you can still restore them. Undo from History won't bring them back after this.`),
    { ok: "Send to trash" });
  if (!ok) return;
  try {
    const res = await api("POST", "/api/purge", { days });
    await dialog("Holding area emptied", resultBody(res, "Sent to the OS trash"), { ok: "Close", cancel: null });
  } catch (e) { showError(e); }
  loadHistory();
  refreshStatusOnce();
}

async function refreshStatusOnce() {
  try {
    const s = await api("GET", "/api/status");
    const pill = $("holding-pill");
    pill.hidden = !s.holding.items;
    pill.textContent = `Holding: ${num(s.holding.items)} items · ${human(s.holding.bytes)}`;
  } catch { /* the poller will report it */ }
}

// ---------- rules ----------

async function loadRules() {
  try {
    const d = await api("GET", "/api/rules");
    $("rules-text").value = d.text;
    $("rules-msg").textContent = d.error ? "Problem: " + d.error : "";
  } catch (e) { showError(e); }
}

async function saveRules() {
  try {
    await api("PUT", "/api/rules", { text: $("rules-text").value });
    $("rules-msg").textContent = "Saved. Preview the result on the Sort tab.";
    state.sort = null;
  } catch (e) {
    $("rules-msg").textContent = "Not saved: " + e.message;
  }
}

// ---------- wiring ----------

for (const b of document.querySelectorAll(".tabs button")) b.addEventListener("click", () => setTab(b.dataset.tab));
$("scan-btn").addEventListener("click", startScan);
$("scan-cancel").addEventListener("click", () => api("POST", "/api/scan/cancel").catch(showError));
$("build-cleanup").addEventListener("click", buildCleanup);
$("build-sort").addEventListener("click", buildSort);
$("build-balance").addEventListener("click", buildBalance);
$("approve-btn").addEventListener("click", approve);
$("rules-save").addEventListener("click", saveRules);
refreshStatus();
