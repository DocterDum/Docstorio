// Docstorio web UI (DESIGN.md "UI"). Vanilla JS, no build step.
// Rule: every piece of dynamic text is set with textContent (via el()).
"use strict";

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

const $ = (id) => document.getElementById(id);
const enc = encodeURIComponent;

// el creates an element. attrs: "class", "text", "on<event>" handlers,
// true for boolean attributes; null/undefined/false are skipped. Children
// may be nodes or strings (strings become text nodes).
function el(tag, attrs, ...children) {
  const node = document.createElement(tag);
  for (const [key, value] of Object.entries(attrs || {})) {
    if (value === null || value === undefined || value === false) continue;
    if (key === "class") node.className = value;
    else if (key === "text") node.textContent = value;
    else if (key.startsWith("on")) node.addEventListener(key.slice(2), value);
    else node.setAttribute(key, value === true ? "" : String(value));
  }
  for (const child of children) {
    if (child !== null && child !== undefined && child !== false) node.append(child);
  }
  return node;
}

function button(label, onclick, cls) {
  return el("button", { type: "button", class: "btn " + (cls || ""), onclick }, label);
}

function badge(text, cls) {
  return el("span", { class: "badge " + (cls || "") }, text);
}

function formatBytes(n) {
  if (!(n >= 0)) return "-";
  const units = ["B", "KiB", "MiB", "GiB"];
  let i = 0;
  while (n >= 1024 && i < units.length - 1) { n /= 1024; i++; }
  return (i === 0 ? n : n.toFixed(1)) + " " + units[i];
}

// formatTime shows an RFC 3339 time in the browser's locale; "-" for
// missing or zero times.
function formatTime(s) {
  if (!s) return "-";
  const d = new Date(s);
  if (isNaN(d) || d.getFullYear() < 2000) return "-";
  return d.toLocaleString();
}

function formatDate(s) {
  if (!s) return "-";
  const d = new Date(s);
  if (isNaN(d) || d.getFullYear() < 2000) return "-";
  return d.toLocaleDateString();
}

function formatAge(seconds) {
  if (!(seconds >= 0)) return "";
  if (seconds < 90) return "just now";
  const minutes = Math.round(seconds / 60);
  if (minutes < 90) return minutes + " min ago";
  return Math.round(minutes / 60) + " h ago";
}

// safeURL returns url when it is http(s), otherwise null (no javascript: links).
function safeURL(url) {
  return typeof url === "string" && /^https?:\/\//i.test(url) ? url : null;
}

// ---------------------------------------------------------------------------
// API
// ---------------------------------------------------------------------------

class ApiError extends Error {
  constructor(status, body) {
    super((body && body.error) || "Request failed (HTTP " + status + ")");
    this.status = status;
    this.code = (body && body.code) || "";
    this.body = body || {};
  }
}

function parseBody(text) {
  if (!text) return null;
  try { return JSON.parse(text); } catch { return null; }
}

// api sends a JSON request and returns the decoded body (null when empty).
// Non-2xx answers throw ApiError with the server's {error, code}.
async function api(method, path, body) {
  const opts = { method, headers: {} };
  if (body !== undefined) {
    opts.headers["Content-Type"] = "application/json";
    opts.body = JSON.stringify(body);
  }
  let res;
  try {
    res = await fetch(path, opts);
  } catch {
    throw new ApiError(0, { error: "Cannot reach the manager", code: "network" });
  }
  const data = parseBody(await res.text());
  if (!res.ok) throw new ApiError(res.status, data || { error: res.statusText });
  return data;
}

// upload posts file as multipart "file" with a progress bar. XMLHttpRequest
// is used because fetch cannot report upload progress.
function upload(path, file, progress) {
  return new Promise((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open("POST", path);
    xhr.upload.onprogress = (e) => {
      if (e.lengthComputable) progress.value = e.loaded / e.total;
    };
    xhr.onload = () => {
      const data = parseBody(xhr.responseText);
      if (xhr.status >= 200 && xhr.status < 300) resolve(data);
      else reject(new ApiError(xhr.status, data || { error: xhr.statusText }));
    };
    xhr.onerror = () => reject(new ApiError(0, { error: "Upload failed: cannot reach the manager", code: "network" }));
    const form = new FormData();
    form.append("file", file, file.name);
    progress.value = 0;
    progress.hidden = false;
    xhr.send(form);
  }).finally(() => { progress.hidden = true; });
}

function errorText(err) {
  if (err.code === "busy") return "Busy: another operation is still running.";
  if (err.code === "credentials_required") return err.message + ". Set the factorio.com username and token on the Settings tab.";
  return err.message || String(err);
}

// ---------------------------------------------------------------------------
// Toasts, modal, clipboard
// ---------------------------------------------------------------------------

const toastBox = $("toasts");

function toast(message, kind) {
  const node = el("div", { class: "toast " + (kind || "") , role: kind === "error" ? "alert" : "status" }, message);
  placeToasts();
  toastBox.append(node);
  setTimeout(() => node.remove(), kind === "error" ? 8000 : 4000);
}

function toastErr(err) {
  toast(errorText(err), "error");
}

// placeToasts keeps the toast container inside the dialog while it is open:
// a showModal() dialog is in the top layer, so toasts outside it would be
// drawn underneath its backdrop.
function placeToasts() {
  const host = modal.open ? modal : document.body;
  if (toastBox.parentNode !== host) host.append(toastBox);
}

const modal = $("modal");
let modalOnClose = null;
let modalSeq = 0; // counts openModal calls, so late responses can tell they are stale

// openModal shows the single shared dialog with a title, a body node and
// footer buttons, replacing whatever it showed before. onClose runs once when
// the dialog closes or its content is replaced.
function openModal(title, body, actions, onClose) {
  runModalOnClose();
  modalSeq++;
  modalOnClose = onClose || null;
  modal.replaceChildren(
    el("header", { class: "modal-head" },
      el("h2", { id: "modal-title" }, title),
      el("button", { type: "button", class: "icon-btn", "aria-label": "Close", onclick: closeModal }, "×")),
    el("div", { class: "modal-body" }, body),
    el("footer", { class: "modal-foot" }, ...(actions || [])));
  if (!modal.open) modal.showModal();
  placeToasts();
}

function closeModal() {
  if (modal.open) modal.close();
}

function runModalOnClose() {
  const f = modalOnClose;
  modalOnClose = null;
  if (f) f();
}

modal.addEventListener("close", () => {
  runModalOnClose();
  modal.replaceChildren();
  placeToasts();
});

// copyText uses the Clipboard API where allowed (it needs a secure context,
// which a LAN http:// page is not) and falls back to execCommand.
async function copyText(text) {
  if (navigator.clipboard && window.isSecureContext) {
    try { await navigator.clipboard.writeText(text); return true; } catch { /* fall back */ }
  }
  const ta = el("textarea", { class: "offscreen", readonly: true });
  ta.value = text;
  document.body.append(ta);
  ta.select();
  let ok = false;
  try { ok = document.execCommand("copy"); } catch { ok = false; }
  ta.remove();
  return ok;
}

// ---------------------------------------------------------------------------
// Tabs
// ---------------------------------------------------------------------------

const TABS = ["dashboard", "mods", "saves", "config", "console", "settings"];
let currentTab = "";

function showTab(name) {
  if (!TABS.includes(name)) name = "dashboard";
  currentTab = name;
  for (const t of TABS) $("tab-" + t).hidden = t !== name;
  for (const a of document.querySelectorAll(".tabs a")) {
    if (a.dataset.tab === name) a.setAttribute("aria-current", "page");
    else a.removeAttribute("aria-current");
  }
  if (name === "dashboard" || name === "saves") refreshSaves();
  if (name === "mods") showModsSub(modsSub);
  if (name === "config") loadConfig();
  if (name === "console") startLogPolling();
  if (name === "settings") loadSettings();
}

window.addEventListener("hashchange", () => showTab(location.hash.slice(1)));

// ---------------------------------------------------------------------------
// Status (polled every 2 s) and dashboard
// ---------------------------------------------------------------------------

let serverStatus = null; // last /api/status answer
let statusOK = false;
let serverActionPending = false;
let saves = null; // last /api/saves answer, null until loaded

async function refreshStatus() {
  try {
    const s = await api("GET", "/api/status");
    const changed = !serverStatus || serverStatus.state !== s.state || serverStatus.save !== s.save;
    serverStatus = s;
    statusOK = true;
    if (changed && (currentTab === "dashboard" || currentTab === "saves")) refreshSaves();
  } catch {
    statusOK = false;
  }
  renderStatus();
}

function pollStatus() {
  refreshStatus().finally(() => setTimeout(pollStatus, 2000));
}

const STATE_LABELS = {
  stopped: "Stopped", starting: "Starting", running: "Running", stopping: "Stopping", failed: "Failed",
};

function renderStatus() {
  const pill = $("state-pill");
  if (!statusOK || !serverStatus) {
    pill.textContent = "Manager offline";
    pill.className = "pill state-offline";
    $("banner").hidden = true;
    return;
  }
  const s = serverStatus;
  const label = STATE_LABELS[s.state] || s.state;
  pill.textContent = label;
  pill.className = "pill state-" + s.state;

  const active = s.state === "starting" || s.state === "running";
  $("banner").hidden = !(s.restartRequired && active);
  $("banner-restart").disabled = serverActionPending || s.state !== "running";

  $("dash-state").textContent = label;
  $("dash-state").className = "state state-" + s.state;
  $("dash-save").textContent = s.save || "-";
  $("dash-started").textContent = active || s.state === "stopping" ? formatTime(s.startedAt) : "-";

  const startStop = $("btn-startstop");
  startStop.textContent = active || s.state === "stopping" ? "Stop" : "Start";
  startStop.className = "btn " + (active ? "btn-danger" : "btn-primary");
  startStop.disabled = serverActionPending || s.state === "stopping";
  $("btn-restart").disabled = serverActionPending || s.state !== "running";

  $("dash-address").textContent = location.hostname + ":" + s.gamePort;
  $("dash-fversion").textContent = s.factorioVersion || "-";
  $("dash-mversion").textContent = s.managerVersion || "-";
  $("dash-spaceage").textContent = s.spaceAge ? "Enabled" : "Disabled";

  const players = s.players || [];
  $("dash-player-count").textContent = s.state === "running" ? "(" + players.length + ")" : "";
  const list = $("dash-players");
  if (s.state !== "running") list.replaceChildren(el("li", { class: "muted" }, "Server is not running"));
  else if (players.length === 0) list.replaceChildren(el("li", { class: "muted" }, "Nobody is online"));
  else list.replaceChildren(...players.map((p) => el("li", null, p)));

  $("dash-error-panel").hidden = !(s.state === "failed" && s.lastError);
  $("dash-error").textContent = s.lastError || "";

  $("dash-create-panel").hidden = !(saves && saves.length === 0);
}

// serverAction calls start/stop/restart; save is optional ("Load this").
async function serverAction(action, save) {
  serverActionPending = true;
  renderStatus();
  const doing = { start: "Starting the server...", stop: "Stopping the server...", restart: "Restarting the server..." };
  toast(doing[action]);
  try {
    const s = await api("POST", "/api/server/" + action, save ? { save } : {});
    if (s && s.state) serverStatus = Object.assign({}, serverStatus, s);
    toast(action === "stop" ? "Server stopped" : "Server " + (STATE_LABELS[s && s.state] || "started").toLowerCase(), "ok");
  } catch (err) {
    toastErr(err);
  } finally {
    serverActionPending = false;
    await refreshStatus();
    refreshSaves();
  }
}

$("btn-startstop").addEventListener("click", () => {
  if (!serverStatus) return;
  const active = serverStatus.state === "starting" || serverStatus.state === "running";
  serverAction(active ? "stop" : "start");
});
$("btn-restart").addEventListener("click", () => serverAction("restart"));
$("banner-restart").addEventListener("click", () => serverAction("restart"));
$("btn-copy").addEventListener("click", async () => {
  const ok = await copyText($("dash-address").textContent);
  toast(ok ? "Address copied" : "Copy failed - select the address and copy it manually", ok ? "ok" : "error");
});

// ---------------------------------------------------------------------------
// Jobs: progress dialog polling /api/jobs/{id}?after=n every second
// ---------------------------------------------------------------------------

// showJob shows a job's output until it finishes. Closing the dialog only
// stops showing the output: the job is still polled, and onDone(job) runs
// once when it ends (job is null when it can no longer be read).
function showJob(id, title, onDone) {
  const stateLine = el("p", { class: "job-state" }, "Running...");
  const log = el("div", { class: "log job-log" });
  let after = 0;
  let shown = true;
  const finish = (job) => { if (onDone) onDone(job); };
  openModal(title, el("div", null, stateLine, log), [button("Close", closeModal)], () => {
    shown = false;
  });

  async function tick() {
    let job;
    try {
      job = await api("GET", "/api/jobs/" + enc(id) + "?after=" + after);
    } catch (err) {
      if (err.status === 404) {
        stateLine.textContent = "This operation is no longer available.";
        stateLine.className = "job-state muted";
        finish(null);
        return;
      }
      setTimeout(tick, 2000);
      return;
    }
    if (shown) appendLines(log, job.lines.map((l) => ({ text: l.text })), 0);
    if (job.last > after) after = job.last;
    if (job.state === "running") {
      setTimeout(tick, 1000);
      return;
    }
    if (job.state === "done") {
      stateLine.textContent = "Finished.";
      stateLine.className = "job-state ok";
      toast(title + ": finished", "ok");
    } else {
      stateLine.textContent = "Failed: " + (job.error || "unknown error");
      stateLine.className = "job-state error";
      toast(title + " failed: " + (job.error || "unknown error"), "error");
    }
    finish(job);
  }
  tick();
}

// ---------------------------------------------------------------------------
// Saves
// ---------------------------------------------------------------------------

async function refreshSaves() {
  try {
    saves = await api("GET", "/api/saves");
  } catch (err) {
    if (currentTab === "saves") toastErr(err);
    return;
  }
  renderSaves();
  renderStatus();
}

function renderSaves() {
  const list = $("saves-list");
  if (!saves) return;
  if (saves.length === 0) {
    list.replaceChildren(el("p", { class: "empty" }, "No saves yet. Create a world or upload a save."));
    return;
  }
  list.replaceChildren(...saves.map((s) => {
    const url = "/api/saves/" + enc(s.name) + "/download";
    return el("div", { class: "row" },
      el("div", { class: "row-main" },
        el("div", { class: "row-title" }, s.name,
          s.running ? badge("in use", "badge-ok") : null,
          s.next ? badge("loads next", "badge-info") : null),
        el("div", { class: "row-meta" }, formatBytes(s.size) + " · " + formatTime(s.modified))),
      el("div", { class: "row-actions" },
        el("button", { type: "button", class: "btn btn-small", disabled: s.running, onclick: () => loadSave(s.name) }, "Load this"),
        el("a", { class: "btn btn-small", href: url, download: s.name }, "Download"),
        el("button", { type: "button", class: "btn btn-small btn-danger", disabled: s.running, onclick: () => deleteSave(s.name) }, "Delete")));
  }));
}

function loadSave(name) {
  const active = serverStatus && (serverStatus.state === "starting" || serverStatus.state === "running");
  serverAction(active ? "restart" : "start", name);
}

async function deleteSave(name) {
  if (!confirm("Delete the save " + name + "? This cannot be undone.")) return;
  try {
    await api("DELETE", "/api/saves/" + enc(name));
    toast("Deleted " + name, "ok");
  } catch (err) {
    toastErr(err);
  }
  refreshSaves();
}

async function uploadSave(file) {
  const progress = $("saves-progress");
  try {
    await upload("/api/saves/upload", file, progress);
    toast("Uploaded " + file.name, "ok");
  } catch (err) {
    // The running save cannot be overwritten, so its 409 is shown as is.
    const exists = saves && saves.some((s) => s.name === file.name && !s.running);
    if (err.status === 409 && exists && confirm("A save named " + file.name + " already exists. Overwrite it?")) {
      try {
        await upload("/api/saves/upload?overwrite=1", file, progress);
        toast("Uploaded " + file.name, "ok");
      } catch (err2) {
        toastErr(err2);
      }
    } else if (!(err.status === 409 && exists)) {
      toastErr(err);
    }
  }
  refreshSaves();
}

$("saves-upload-btn").addEventListener("click", () => $("saves-upload").click());
$("saves-upload").addEventListener("change", (e) => {
  const file = e.target.files[0];
  e.target.value = "";
  if (file) uploadSave(file);
});
$("saves-create").addEventListener("click", openCreateWorld);
$("dash-create").addEventListener("click", openCreateWorld);

function openCreateWorld() {
  const name = el("input", { type: "text", required: true, spellcheck: "false", placeholder: "my-world" });
  const seed = el("input", { type: "number", min: "0", max: "4294967295", step: "1", placeholder: "random" });
  const mapGen = el("input", { type: "checkbox" });
  const mapSettings = el("input", { type: "checkbox" });
  const body = el("form", { class: "form", novalidate: true, onsubmit: (e) => { e.preventDefault(); submit(); } },
    el("label", { class: "field" }, "Name", name),
    el("label", { class: "field" }, "Seed (optional)", seed),
    el("label", { class: "check" }, mapGen, " Use config/map-gen-settings.json"),
    el("label", { class: "check" }, mapSettings, " Use config/map-settings.json"),
    el("p", { class: "muted small" }, "The world is created with the mods that are enabled now. The server must be stopped."),
    el("button", { type: "submit", hidden: true }));
  const createBtn = button("Create", () => submit(), "btn-primary");
  openModal("Create world", body, [button("Cancel", closeModal), createBtn]);
  name.focus();

  async function submit() {
    const req = {
      name: name.value.trim(),
      useMapGenSettings: mapGen.checked,
      useMapSettings: mapSettings.checked,
    };
    if (!req.name) { toast("Enter a name for the world", "error"); return; }
    if (seed.value.trim() !== "") {
      const n = Number(seed.value);
      if (!Number.isInteger(n) || n < 0 || n > 4294967295) {
        toast("The seed must be a whole number from 0 to 4294967295", "error");
        return;
      }
      req.seed = n;
    }
    createBtn.disabled = true;
    try {
      const r = await api("POST", "/api/saves", req);
      showJob(r.jobId, "Creating world", () => { refreshSaves(); refreshStatus(); });
    } catch (err) {
      createBtn.disabled = false;
      toastErr(err);
    }
  }
}

// ---------------------------------------------------------------------------
// Mods: installed
// ---------------------------------------------------------------------------

let modsSub = "installed";
let mods = null;
let browseLoaded = false;

function showModsSub(sub) {
  modsSub = sub;
  $("mods-installed").hidden = sub !== "installed";
  $("mods-browse").hidden = sub !== "browse";
  for (const b of document.querySelectorAll(".subtab")) {
    b.setAttribute("aria-selected", b.dataset.sub === sub ? "true" : "false");
  }
  if (sub === "installed") refreshMods();
  if (sub === "browse" && !browseLoaded) { browseLoaded = true; runSearch(); }
}

for (const b of document.querySelectorAll(".subtab")) {
  b.addEventListener("click", () => showModsSub(b.dataset.sub));
}

async function refreshMods() {
  if (!mods) $("mods-list").replaceChildren(el("p", { class: "empty" }, "Loading..."));
  try {
    mods = (await api("GET", "/api/mods")).mods;
  } catch (err) {
    toastErr(err);
    return;
  }
  renderMods();
}

function renderMods() {
  if (!mods) return;
  const filter = $("mods-filter").value.trim().toLowerCase();
  const shown = mods.filter((m) => !filter ||
    m.name.toLowerCase().includes(filter) || (m.title || "").toLowerCase().includes(filter));
  $("mods-update-all").hidden = !mods.some((m) => m.updateAvailable);
  const list = $("mods-list");
  if (shown.length === 0) {
    list.replaceChildren(el("p", { class: "empty" }, filter ? "No installed mod matches the filter." : "No mods installed."));
    return;
  }
  list.replaceChildren(...shown.map(modRow));
}

function modRow(m) {
  const toggle = el("input", {
    type: "checkbox", class: "switch", "aria-label": "Enable " + m.name,
    disabled: m.name === "base",
    title: m.name === "base" ? "base cannot be disabled" : null,
  });
  toggle.checked = m.enabled;
  toggle.addEventListener("change", () => setModEnabled(m, toggle.checked, toggle));

  const badges = [];
  if (m.builtin) badges.push(badge("built-in", "badge-info"));
  if (!m.compatible) badges.push(badge("needs Factorio " + (m.factorioVersion || "?"), "badge-danger"));
  if (m.missingDependencies.length) badges.push(badge("missing: " + m.missingDependencies.join(", "), "badge-danger"));
  if (m.updateAvailable) badges.push(badge("update " + m.updateAvailable, "badge-warn"));

  return el("div", { class: "row" + (m.enabled ? "" : " row-off") },
    toggle,
    el("div", { class: "row-main" },
      el("div", { class: "row-title" }, m.title || m.name, ...badges),
      el("div", { class: "row-meta" }, m.name + " " + m.version)),
    m.builtin ? null : el("div", { class: "row-actions" },
      button("Delete", () => deleteMod(m), "btn-small btn-danger")));
}

async function setModEnabled(m, on, toggle) {
  toggle.disabled = true;
  const path = "/api/mods/" + enc(m.name) + (on ? "/enable" : "/disable");
  try {
    if (on) {
      const r = await api("POST", path);
      toast("Enabled " + (r.enabled.length ? r.enabled.join(", ") : m.name), "ok");
    } else {
      let r;
      try {
        r = await api("POST", path, {});
      } catch (err) {
        const deps = err.body.dependents || [];
        if (err.status !== 409 || deps.length === 0) throw err;
        if (!confirm(m.name + " is required by: " + deps.join(", ") + ".\nDisable those mods too?")) {
          toggle.checked = true;
          return;
        }
        r = await api("POST", path, { cascade: true });
      }
      toast("Disabled " + (r.disabled.length ? r.disabled.join(", ") : m.name), "ok");
    }
  } catch (err) {
    toastErr(err);
  } finally {
    await refreshMods();
    refreshStatus();
  }
}

async function deleteMod(m) {
  if (!confirm("Delete " + m.name + " " + m.version + "? The mod zip is removed.")) return;
  try {
    await api("DELETE", "/api/mods/" + enc(m.name));
    toast("Deleted " + m.name, "ok");
  } catch (err) {
    toastErr(err);
  }
  refreshMods();
  refreshStatus();
}

async function uploadMod(file) {
  try {
    const r = await upload("/api/mods/upload", file, $("mods-progress"));
    toast("Added " + (r ? r.name + " " + r.version : file.name), "ok");
  } catch (err) {
    toastErr(err);
  }
  refreshMods();
  refreshStatus();
}

$("mods-filter").addEventListener("input", renderMods);
$("mods-upload-btn").addEventListener("click", () => $("mods-upload").click());
$("mods-upload").addEventListener("change", (e) => {
  const file = e.target.files[0];
  e.target.value = "";
  if (file) uploadMod(file);
});
$("mods-update-all").addEventListener("click", async () => {
  try {
    const r = await api("POST", "/api/mods/update", {});
    showJob(r.jobId, "Updating mods", () => { refreshMods(); refreshStatus(); });
  } catch (err) {
    toastErr(err);
  }
});

// ---------------------------------------------------------------------------
// Mods: browse the portal
// ---------------------------------------------------------------------------

const PAGE_SIZE = 24;
let portalPage = 1;
let portalTotal = 0;
let searchTimer = 0;
let searchSeq = 0;

function scheduleSearch() {
  clearTimeout(searchTimer);
  searchTimer = setTimeout(() => { portalPage = 1; runSearch(); }, 300);
}

async function runSearch() {
  const seq = ++searchSeq;
  const params = new URLSearchParams({
    q: $("portal-q").value.trim(),
    page: String(portalPage),
    pageSize: String(PAGE_SIZE),
    sort: $("portal-sort").value,
  });
  $("portal-info").textContent = "Searching... (the first search downloads the mod list from the portal)";
  let r;
  try {
    r = await api("GET", "/api/portal/search?" + params);
  } catch (err) {
    if (seq === searchSeq) $("portal-info").textContent = errorText(err);
    return;
  }
  if (seq !== searchSeq) return; // a newer search is running
  portalTotal = r.total;
  const age = formatAge(r.cacheAge);
  $("portal-info").textContent = r.total + " mods" + (age ? " · mod list updated " + age : "");
  const results = $("portal-results");
  if (r.results.length === 0) results.replaceChildren(el("p", { class: "empty" }, "No mods found."));
  else results.replaceChildren(...r.results.map(portalCard));
  const pages = Math.max(1, Math.ceil(r.total / PAGE_SIZE));
  $("portal-page").textContent = "Page " + portalPage + " of " + pages;
  $("portal-prev").disabled = portalPage <= 1;
  $("portal-next").disabled = portalPage >= pages;
}

// thumb is a lazy-loaded thumbnail over a letter placeholder; the image is
// dropped when the mod has none (404).
function thumb(name, title, cls) {
  const img = el("img", { src: "/api/portal/thumb/" + enc(name), alt: "", loading: "lazy", decoding: "async" });
  img.addEventListener("load", () => img.classList.add("loaded"));
  img.addEventListener("error", () => img.remove());
  return el("div", { class: "thumb " + (cls || "") },
    el("span", { class: "thumb-letter", "aria-hidden": "true" }, (title || name || "?").charAt(0).toUpperCase()),
    img);
}

function portalCard(m) {
  return el("button", { type: "button", class: "card", onclick: () => openDetails(m.name) },
    thumb(m.name, m.title),
    el("div", { class: "card-main" },
      el("div", { class: "card-title" }, m.title || m.name),
      el("div", { class: "card-owner" }, "by " + m.owner),
      el("div", { class: "card-summary" }, m.summary),
      el("div", { class: "card-meta" },
        m.downloads.toLocaleString() + " downloads · " + m.latestVersion + " · " + formatDate(m.updated),
        m.requiresSpaceAge ? badge("Space Age", "badge-info") : null,
        m.installed ? badge("installed " + m.installed, "badge-ok") : null)));
}

$("portal-q").addEventListener("input", scheduleSearch);
$("portal-sort").addEventListener("change", () => { portalPage = 1; runSearch(); });
$("portal-prev").addEventListener("click", () => { if (portalPage > 1) { portalPage--; runSearch(); } });
$("portal-next").addEventListener("click", () => {
  if (portalPage * PAGE_SIZE < portalTotal) { portalPage++; runSearch(); }
});
$("portal-refresh").addEventListener("click", async () => {
  try {
    await api("POST", "/api/portal/refresh");
    toast("Refreshing the mod list from the portal...");
    setTimeout(runSearch, 5000);
  } catch (err) {
    toastErr(err);
  }
});

async function openDetails(name) {
  openModal(name, el("p", { class: "muted" }, "Loading..."), [button("Close", closeModal)]);
  const mine = modalSeq;
  // stale: the loading dialog was closed or replaced by another one.
  const stale = () => !modal.open || modalSeq !== mine;
  let d;
  try {
    d = await api("GET", "/api/portal/mods/" + enc(name));
  } catch (err) {
    if (!stale()) openModal(name, el("p", { class: "error" }, errorText(err)), [button("Close", closeModal)]);
    return;
  }
  if (stale()) return;
  const links = [];
  if (safeURL(d.homepage)) links.push(el("a", { href: d.homepage, target: "_blank", rel: "noopener noreferrer" }, "Homepage"));
  if (safeURL(d.sourceUrl)) links.push(el("a", { href: d.sourceUrl, target: "_blank", rel: "noopener noreferrer" }, "Source"));
  links.push(el("a", { href: "https://mods.factorio.com/mod/" + enc(d.name), target: "_blank", rel: "noopener noreferrer" }, "Mod portal"));

  const releases = d.releases.slice().reverse().map((r) =>
    el("li", { class: "release" },
      el("div", null,
        el("strong", null, r.version), " for Factorio " + r.factorioVersion + " · " + formatDate(r.released),
        r.compatible ? null : badge("incompatible", "badge-danger")),
      r.dependencies.length ? el("div", { class: "muted small" }, r.dependencies.join(", ")) : null));

  const body = el("div", { class: "details" },
    el("div", { class: "details-head" },
      thumb(d.name, d.title, "thumb-large"),
      el("div", null,
        el("div", { class: "muted" }, "by " + d.owner),
        el("p", null, d.summary),
        d.installed ? badge("installed " + d.installed, "badge-ok") : null,
        el("div", { class: "links" }, ...links))),
    el("h3", null, "Description"),
    el("div", { class: "description" }, d.description || "No description."),
    el("h3", null, "Releases"),
    el("ul", { class: "releases" }, ...releases));

  const hasCompatible = d.releases.some((r) => r.compatible);
  const install = el("button", {
    type: "button", class: "btn btn-primary", disabled: !hasCompatible,
    title: hasCompatible ? null : "No release for this Factorio version",
    onclick: () => openPlan(d.name, d.title || d.name),
  }, "Install");
  openModal(d.title || d.name, body, [button("Close", closeModal), install]);
}

// openPlan shows the install plan for one mod; the recommended/optional
// checkboxes re-plan. Install starts the job and shows its progress.
function openPlan(name, title) {
  const req = { mods: [{ name }], includeOptional: false, includeRecommended: true };
  const recommended = el("input", { type: "checkbox" });
  recommended.checked = true;
  const optional = el("input", { type: "checkbox" });
  const view = el("div", { class: "plan" });
  const install = button("Install", doInstall, "btn-primary");
  const body = el("div", null,
    el("label", { class: "check" }, recommended, " Include recommended dependencies"),
    el("label", { class: "check" }, optional, " Include optional dependencies"),
    view);
  recommended.addEventListener("change", () => { req.includeRecommended = recommended.checked; load(); });
  optional.addEventListener("change", () => { req.includeOptional = optional.checked; load(); });
  openModal("Install " + title, body, [button("Cancel", closeModal), install]);

  let seq = 0;
  async function load() {
    const mine = ++seq;
    install.disabled = true;
    view.replaceChildren(el("p", { class: "muted" }, "Working out dependencies..."));
    let plan;
    try {
      plan = await api("POST", "/api/portal/plan", req);
    } catch (err) {
      if (mine === seq) view.replaceChildren(el("p", { class: "error" }, errorText(err)));
      return;
    }
    if (mine !== seq) return;
    renderPlan(plan);
    // A plan with conflicts is refused by the server.
    install.disabled = plan.conflicts.length > 0 || (plan.install.length === 0 && plan.enable.length === 0);
  }

  function section(heading, items, cls) {
    if (items.length === 0) return null;
    return el("div", { class: "plan-section " + (cls || "") },
      el("h3", null, heading),
      el("ul", { class: "plain-list" }, ...items.map((t) => el("li", null, t))));
  }

  function renderPlan(plan) {
    const nothing = plan.install.length === 0 && plan.enable.length === 0;
    const parts = [
      section("Problems", plan.conflicts, "plan-conflicts"),
      section("Download", plan.install.map((i) => i.name + " " + i.version + " (" + i.reason + ")")),
      section("Enable", plan.enable),
      section("Already installed", plan.alreadySatisfied),
      nothing ? el("p", { class: "muted" }, "Nothing to do: everything is already installed and enabled.") : null,
    ];
    view.replaceChildren(...parts.filter(Boolean));
  }

  async function doInstall() {
    install.disabled = true;
    try {
      const r = await api("POST", "/api/portal/install", req);
      showJob(r.jobId, "Installing " + title, () => {
        refreshMods();
        refreshStatus();
        if (browseLoaded) runSearch();
      });
    } catch (err) {
      install.disabled = false;
      toastErr(err);
    }
  }

  load();
}

// ---------------------------------------------------------------------------
// Config: server-settings form and raw JSON files
// ---------------------------------------------------------------------------

// ssInputs: one {field, input, initial} per editable server setting; initial
// is the input's starting state, so only changed keys are sent.
let ssInputs = [];
let cfgFiles = [];

async function loadConfig() {
  await Promise.all([loadServerSettings(), loadConfigFiles()]);
}

async function loadServerSettings() {
  let r;
  try {
    r = await api("GET", "/api/config/server-settings");
  } catch (err) {
    $("ss-fields").replaceChildren(el("p", { class: "error" }, errorText(err)));
    return;
  }
  ssInputs = [];
  $("ss-fields").replaceChildren(...r.fields.map(fieldRow).filter(Boolean));
}

function inputState(input) {
  return input.type === "checkbox" ? input.checked : input.value;
}

function fieldRow(f) {
  const text = f.value === null || f.value === undefined ? "" : String(f.value);
  let input;
  switch (f.type) {
    case "bool":
      input = el("input", { type: "checkbox" });
      input.checked = f.value === true;
      break;
    case "enum":
      input = el("select", null, ...(f.options || []).map((o) => el("option", { value: o }, o)));
      input.value = text;
      break;
    case "int":
      input = el("input", { type: "number", step: "1" });
      input.value = text;
      break;
    case "password":
      input = el("input", { type: "password", autocomplete: "new-password" });
      input.value = text;
      break;
    case "string":
      input = el("input", { type: "text" });
      input.value = text;
      break;
    default:
      return null; // other shapes are edited in the raw file editor
  }
  ssInputs.push({ field: f, input, initial: inputState(input) });
  const help = f.help ? el("div", { class: "muted small" }, f.help) : null;
  if (f.type === "bool") {
    return el("div", { class: "field-row" }, el("label", { class: "check" }, input, " " + f.key), help);
  }
  return el("div", { class: "field-row" }, el("label", { class: "field" }, f.key, input), help);
}

$("ss-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const values = {};
  for (const { field, input, initial } of ssInputs) {
    if (inputState(input) === initial) continue;
    if (field.type === "bool") values[field.key] = input.checked;
    else if (field.type === "int") {
      const v = Number(input.value);
      if (input.value.trim() === "" || !Number.isInteger(v)) {
        toast(field.key + " must be a whole number", "error");
        return;
      }
      values[field.key] = v;
    } else values[field.key] = input.value;
  }
  if (Object.keys(values).length === 0) { toast("No changes to save"); return; }
  try {
    await api("PUT", "/api/config/server-settings", { values });
    toast("Server settings saved", "ok");
  } catch (err) {
    toastErr(err);
  }
  loadConfig();
  refreshStatus();
});

async function loadConfigFiles() {
  try {
    cfgFiles = await api("GET", "/api/config/files");
  } catch (err) {
    toastErr(err);
    return;
  }
  const select = $("cfg-file");
  const current = select.value;
  select.replaceChildren(...cfgFiles.map((f) =>
    el("option", { value: f.name }, f.name + (f.exists ? "" : " (not created yet)"))));
  if (cfgFiles.some((f) => f.name === current)) select.value = current;
  loadConfigFile(select.value);
}

async function loadConfigFile(name) {
  if (!name) return;
  const area = $("cfg-content");
  let r;
  try {
    r = await api("GET", "/api/config/files/" + enc(name));
  } catch (err) {
    toastErr(err);
    return;
  }
  area.value = r.content;
  $("cfg-note").textContent = r.exists ? "" :
    "This file does not exist yet. Shown is the default content; saving creates the file.";
  validateConfig();
}

// validateConfig checks the editor content as JSON while typing; the server
// checks again on save and reports line and column.
function validateConfig() {
  const out = $("cfg-valid");
  try {
    JSON.parse($("cfg-content").value);
    out.textContent = "Valid JSON";
    out.className = "small ok";
  } catch (err) {
    out.textContent = "Invalid JSON: " + err.message;
    out.className = "small error";
  }
}

$("cfg-file").addEventListener("change", () => loadConfigFile($("cfg-file").value));
$("cfg-content").addEventListener("input", validateConfig);
$("cfg-save").addEventListener("click", async () => {
  const name = $("cfg-file").value;
  if (!name) return;
  try {
    await api("PUT", "/api/config/files/" + enc(name), { content: $("cfg-content").value });
    toast("Saved " + name, "ok");
    loadConfig();
    refreshStatus();
  } catch (err) {
    if (err.code === "invalid_json") {
      const out = $("cfg-valid");
      out.textContent = "Invalid JSON: " + err.message; // the message already gives line and column
      out.className = "small error";
    }
    toastErr(err);
  }
});

// ---------------------------------------------------------------------------
// Console: live log (polled every second while the tab is open) and RCON
// ---------------------------------------------------------------------------

const LOG_KEEP = 5000;
let logAfter = 0;
let logPolling = false;

// appendLines adds lines ({text, cls?}) to a log box, keeps at most `keep`
// lines (0 = no limit) and follows the end when it was scrolled to the end.
function appendLines(box, lines, keep) {
  if (lines.length === 0) return;
  const atEnd = box.scrollTop + box.clientHeight >= box.scrollHeight - 40;
  const frag = document.createDocumentFragment();
  for (const l of lines) frag.append(el("div", { class: "line " + (l.cls || lineClass(l.text)) }, l.text));
  box.append(frag);
  if (keep) while (box.childElementCount > keep) box.firstElementChild.remove();
  if (atEnd) box.scrollTop = box.scrollHeight;
}

function lineClass(text) {
  if (text.startsWith("[docstorio]")) return "line-mgr";
  if (/\bError\b/.test(text)) return "line-err";
  if (/\bWarning\b/.test(text)) return "line-warn";
  return "";
}

async function startLogPolling() {
  if (logPolling) return;
  logPolling = true;
  const box = $("log");
  while (currentTab === "console") {
    try {
      for (;;) {
        const r = await api("GET", "/api/logs?after=" + logAfter);
        if (r.last < logAfter) { // the manager restarted: numbering began again
          box.replaceChildren();
          logAfter = 0;
          continue;
        }
        appendLines(box, r.lines, LOG_KEEP);
        // Advance by what was received, so a backlog of more than one page
        // is fetched page by page.
        logAfter = r.lines.length ? r.lines[r.lines.length - 1].n : r.last;
        if (r.lines.length < 1000) break;
      }
    } catch { /* shown by the status pill; try again */ }
    await new Promise((resolve) => setTimeout(resolve, 1000));
  }
  logPolling = false;
}

const cmdHistory = [];
let historyPos = 0;

$("console-form").addEventListener("submit", async (e) => {
  e.preventDefault();
  const input = $("console-input");
  const command = input.value.trim();
  if (!command) return;
  if (cmdHistory[cmdHistory.length - 1] !== command) cmdHistory.push(command);
  historyPos = cmdHistory.length;
  input.value = "";
  const box = $("log");
  try {
    const r = await api("POST", "/api/console", { command });
    const out = (r.output || "").replace(/\n$/, "");
    appendLines(box, [{ text: "> " + command, cls: "line-cmd" }]
      .concat(out ? out.split("\n").map((t) => ({ text: t, cls: "line-out" })) : []), LOG_KEEP);
    box.scrollTop = box.scrollHeight;
  } catch (err) {
    toastErr(err);
  }
});

$("console-input").addEventListener("keydown", (e) => {
  const input = e.target;
  if (e.key === "ArrowUp" && historyPos > 0) {
    historyPos--;
    input.value = cmdHistory[historyPos];
    e.preventDefault();
  } else if (e.key === "ArrowDown" && historyPos < cmdHistory.length) {
    historyPos++;
    input.value = historyPos < cmdHistory.length ? cmdHistory[historyPos] : "";
    e.preventDefault();
  }
});

// ---------------------------------------------------------------------------
// Settings
// ---------------------------------------------------------------------------

let settings = null;

async function loadSettings() {
  try {
    settings = await api("GET", "/api/settings");
  } catch (err) {
    toastErr(err);
    return;
  }
  $("set-autostart").checked = settings.autostart;
  $("set-whitelist").checked = settings.whitelistEnabled;
  $("set-port").value = String(settings.gamePort);
  $("set-username").value = settings.factorioUsername;
  $("set-token").value = "";
  $("set-token").placeholder = settings.factorioTokenSet ? "Token is set - type to replace it" : "Not set";
  $("set-token-note").textContent = settings.factorioTokenSet ? "A token is stored. It is never shown." : "";
  $("set-token-clear").hidden = !settings.factorioTokenSet;
}

async function saveSettings(patch) {
  try {
    await api("PUT", "/api/settings", patch);
    toast("Settings saved", "ok");
  } catch (err) {
    toastErr(err);
  }
  loadSettings();
  refreshStatus();
}

$("settings-form").addEventListener("submit", (e) => {
  e.preventDefault();
  if (!settings) return;
  const patch = {};
  const port = Number($("set-port").value);
  if (!Number.isInteger(port) || port < 1 || port > 65535) {
    toast("The game port must be a number from 1 to 65535", "error");
    return;
  }
  if ($("set-autostart").checked !== settings.autostart) patch.autostart = $("set-autostart").checked;
  if ($("set-whitelist").checked !== settings.whitelistEnabled) patch.whitelistEnabled = $("set-whitelist").checked;
  if (port !== settings.gamePort) patch.gamePort = port;
  const username = $("set-username").value.trim();
  if (username !== settings.factorioUsername) patch.factorioUsername = username;
  // Clearing the token is left to the confirm-guarded Clear token button.
  const token = $("set-token").value.trim();
  if (token !== "") patch.factorioToken = token;
  if (Object.keys(patch).length === 0) { toast("No changes to save"); return; }
  saveSettings(patch);
});

$("set-token-clear").addEventListener("click", () => {
  if (!confirm("Clear the stored factorio.com token? Mod downloads will stop working until a new one is set.")) return;
  saveSettings({ factorioToken: "" });
});

// ---------------------------------------------------------------------------
// Start
// ---------------------------------------------------------------------------

showTab(location.hash.slice(1));
pollStatus();
