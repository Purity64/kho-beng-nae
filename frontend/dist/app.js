// Kho-beng-nae frontend — vanilla JS, talks to the Go backend through the
// Wails-injected window.go.main.App bindings and window.runtime event bus.

const CAT_ORDER = ["success", "redirect", "auth", "client_err", "server_err", "other"];
const CAT_LABEL = {
  success: "Success (2xx)",
  redirect: "Redirect (3xx)",
  auth: "Auth / Forbidden (401·403)",
  client_err: "Other 4xx",
  server_err: "Server error (5xx)",
  other: "Other",
};

const state = {
  sessions: [],       // list cache
  current: null,      // current detail session object (with hits)
  dirty: false,
  timer: { running: false, startedAt: null },
  timerHandle: null,
};

function fmtTime(ms) {
  const s = ms / 1000;
  if (s < 60) return s.toFixed(1) + "s";
  const m = Math.floor(s / 60);
  return m + "m " + Math.round(s % 60) + "s";
}

function renderTime() {
  let ms = state.current?.elapsedMs || 0;
  if (state.timer.running && state.timer.startedAt) ms = Date.now() - state.timer.startedAt;
  const el = document.getElementById("m-time");
  if (el) el.textContent = fmtTime(ms);
}

function startTick(startedAtMs) {
  state.timer = { running: true, startedAt: startedAtMs || Date.now() };
  clearInterval(state.timerHandle);
  renderTime();
  state.timerHandle = setInterval(renderTime, 200);
}

function stopTick(elapsedMs) {
  state.timer = { running: false, startedAt: null };
  clearInterval(state.timerHandle);
  if (state.current && typeof elapsedMs === "number") state.current.elapsedMs = elapsedMs;
  renderTime();
}

// ---- helpers --------------------------------------------------------------

const $ = (id) => document.getElementById(id);
const el = (tag, cls, txt) => {
  const e = document.createElement(tag);
  if (cls) e.className = cls;
  if (txt != null) e.textContent = txt;
  return e;
};

function App() {
  return window.go.main.App;
}

function fmtSize(n) {
  if (n < 0) return "?";
  if (n < 1024) return n + " B";
  if (n < 1024 * 1024) return (n / 1024).toFixed(1) + " KB";
  return (n / 1024 / 1024).toFixed(1) + " MB";
}

// Wait until the Wails runtime + bindings are present.
function ready(fn) {
  if (window.go && window.go.main && window.runtime) return fn();
  setTimeout(() => ready(fn), 40);
}

// ---- API ------------------------------------------------------------------

async function loadWordlists() {
  try {
    const wls = (await App().ListWordlists()) || [];
    const sel = $("wl-select");
    sel.innerHTML = "";
    let active = null;
    for (const wl of wls) {
      const o = document.createElement("option");
      o.value = wl.name;
      o.textContent = wl.builtin ? `${wl.name}` : wl.name;
      if (wl.active) { o.selected = true; active = wl; }
      sel.appendChild(o);
    }
    $("wl-count").textContent = active ? `${active.count}` : "0";
    $("wl-del").style.visibility = active && active.builtin ? "hidden" : "visible";
  } catch (e) { /* ignore */ }
}

function setWLStatus(msg) { $("wl-status").textContent = msg || ""; }

async function loadSessions() {
  state.sessions = (await App().ListSessions()) || [];
  renderList();
}

function readParams() {
  return {
    mode: $("p-mode").value,
    recurse: $("p-recurse").checked,
    maxDepth: parseInt($("p-depth").value || "2", 10),
    retries: parseInt($("p-retries").value || "2", 10),
    startConcurrency: parseInt($("p-start").value || "10", 10),
    maxConcurrency: parseInt($("p-max").value || "100", 10),
    timeoutSeconds: parseInt($("p-timeout").value || "8", 10),
    skipTlsVerify: $("p-skiptls").checked,
    crawl: $("p-crawl").checked,
    mineJs: $("p-js").checked,
    useRobots: $("p-robots").checked,
    crawlDepth: parseInt($("p-crawldepth").value || "3", 10),
    maxPages: parseInt($("p-maxpages").value || "200", 10),
  };
}

// ---- list view ------------------------------------------------------------

function renderList() {
  const grid = $("session-list");
  grid.innerHTML = "";
  const list = state.sessions;
  $("no-sessions").style.display = list.length ? "none" : "block";

  for (const s of list) {
    const card = el("div", "s-card");
    card.appendChild(el("div", "s-name", s.name || s.baseUrl));
    card.appendChild(el("div", "s-url", s.baseUrl));
    const foot = el("div", "s-foot");
    foot.appendChild(el("span", "s-count", `${(s.hits || []).length} hits`));
    const st = el("span", "pill state", s.state);
    st.dataset.state = s.state;
    foot.appendChild(st);
    card.appendChild(foot);
    card.onclick = () => openDetail(s.id);
    grid.appendChild(card);
  }
}

// ---- detail view ----------------------------------------------------------

async function openDetail(id) {
  const s = await App().GetSession(id);
  state.current = s;
  s.hits = s.hits || [];
  s.endpoints = s.endpoints || [];
  $("view-list").classList.add("hidden");
  $("view-detail").classList.remove("hidden");
  renderDetailHead();
  renderMetrics(s.metrics || {});
  renderResults();
  renderEndpoints();
  setTab("paths");
  // timer
  if (s.state === "running") {
    const started = s.startedAt ? Date.parse(s.startedAt) : Date.now();
    startTick(started);
  } else {
    stopTick(s.elapsedMs || 0);
  }
}

function setTab(name) {
  document.querySelectorAll(".tab").forEach((t) => t.classList.toggle("active", t.dataset.tab === name));
  document.querySelectorAll(".tab-panel").forEach((p) => p.classList.toggle("hidden", p.dataset.panel !== name));
}

function renderEndpoints() {
  const box = $("endpoints");
  const eps = state.current?.endpoints || [];
  $("tab-endpoints-n").textContent = eps.length;
  box.innerHTML = "";
  if (!eps.length) {
    box.appendChild(el("p", "empty", "No endpoints discovered yet."));
    return;
  }
  const list = el("div", "ep-list");
  const sorted = [...eps].sort((a, b) => a.path.localeCompare(b.path));
  for (const e of sorted) {
    const row = el("div", "ep");
    const path = el("span", "ep-path", "/" + e.path);
    row.appendChild(path);
    row.appendChild(el("span", "src src-" + e.source, e.source));
    row.onclick = () => App().OpenURL(e.url);
    list.appendChild(row);
  }
  box.appendChild(list);
}

function backToList() {
  state.current = null;
  $("view-detail").classList.add("hidden");
  $("view-list").classList.remove("hidden");
  loadSessions();
}

function renderDetailHead() {
  const s = state.current;
  $("d-name").textContent = s.name || s.baseUrl;
  const url = $("d-url");
  url.textContent = s.baseUrl;
  url.onclick = (e) => { e.preventDefault(); App().OpenURL(s.baseUrl); };
  $("d-proto").textContent = s.protocol || "—";
  const st = $("d-state");
  st.textContent = s.state;
  st.dataset.state = s.state;

  const running = s.state === "running";
  const phase = $("d-phase");
  if (running && s.phase) {
    phase.textContent = s.phase;
    phase.dataset.phase = s.phase;
    phase.style.display = "";
  } else {
    phase.style.display = "none";
  }

  $("btn-run").disabled = running;
  $("btn-stop").disabled = !running;
}

function renderMetrics(m) {
  $("m-conc").textContent = m.concurrency ?? "–";
  $("m-inflight").textContent = m.inFlight ?? "–";
  $("m-total").textContent = m.total ?? 0;
  $("m-errors").textContent = m.errors ?? 0;
  $("m-retries").textContent = m.retries ?? 0;
  $("m-rate").textContent = Math.round((m.lastErrRate || 0) * 100) + "%";
  $("m-hits").textContent = (state.current?.hits || []).length;

  // req/sec: live rate while running, final average once stopped.
  const running = state.current?.state === "running";
  const rps = running ? (m.ratePerSec || 0) : (m.avgPerSec || 0);
  $("m-rps").textContent = rps >= 100 ? Math.round(rps) : rps.toFixed(1);
}

function renderResults() {
  const box = $("results");
  box.innerHTML = "";
  const hits = state.current?.hits || [];
  $("tab-paths-n").textContent = hits.length;
  if (!hits.length) {
    box.appendChild(el("p", "empty", "No hits yet. Press Run to start scanning."));
    return;
  }
  // group by category
  const groups = {};
  for (const h of hits) (groups[h.category] ||= []).push(h);

  for (const cat of CAT_ORDER) {
    const items = groups[cat];
    if (!items || !items.length) continue;

    const group = el("div", "cat-group");
    const head = el("div", "cat-head");
    const dot = el("span", "cat-dot dot-" + cat);
    head.appendChild(dot);
    head.appendChild(el("span", "cat-name", CAT_LABEL[cat] || cat));
    head.appendChild(el("span", "cat-count", `· ${items.length}`));
    const listEl = el("div", "cat-list");
    head.onclick = () => { listEl.style.display = listEl.style.display === "none" ? "" : "none"; };
    group.appendChild(head);

    // sort by depth then path
    items.sort((a, b) => (a.depth - b.depth) || a.path.localeCompare(b.path));
    for (const h of items) {
      const row = el("div", "hit");
      const st = el("span", "status st-" + h.category, String(h.status));
      row.appendChild(st);
      const path = el("span", "path");
      path.textContent = "/" + h.path;
      if (h.depth > 0) path.style.paddingLeft = (h.depth * 14) + "px";
      row.appendChild(path);
      if (h.isDir) row.appendChild(el("span", "dir-badge", "dir"));
      row.appendChild(el("span", "size", fmtSize(h.size)));
      row.onclick = () => App().OpenURL(h.url);
      listEl.appendChild(row);
    }
    group.appendChild(listEl);
    box.appendChild(group);
  }
}

// throttle result re-renders while hits stream in
function scheduleResults() {
  if (state.dirty) return;
  state.dirty = true;
  requestAnimationFrame(() => {
    state.dirty = false;
    renderResults();
    renderMetrics(state.current?.metrics || {});
  });
}

// ---- events ---------------------------------------------------------------

function wireRuntimeEvents() {
  const R = window.runtime;
  R.EventsOn("session:hit", (d) => {
    if (!state.current || d.id !== state.current.id) return;
    state.current.hits.push(d.hit);
    scheduleResults();
  });
  R.EventsOn("session:metrics", (d) => {
    if (!state.current || d.id !== state.current.id) return;
    state.current.metrics = d.metrics;
    renderMetrics(d.metrics);
  });
  R.EventsOn("session:state", (d) => {
    if (state.current && d.id === state.current.id) {
      state.current.state = d.state;
      state.current.error = d.error;
      if (d.state !== "running") state.current.phase = null;
      renderDetailHead();
      renderMetrics(state.current.metrics || {});
    }
    // keep list cache fresh too
    const s = state.sessions.find((x) => x.id === d.id);
    if (s) s.state = d.state;
  });
  R.EventsOn("session:endpoint", (d) => {
    if (!state.current || d.id !== state.current.id) return;
    (state.current.endpoints ||= []).push(d.endpoint);
    scheduleEndpoints();
  });
  R.EventsOn("session:phase", (d) => {
    if (!state.current || d.id !== state.current.id) return;
    state.current.phase = d.phase;
    renderDetailHead();
  });
  R.EventsOn("session:timer", (d) => {
    if (!state.current || d.id !== state.current.id) return;
    if (d.running) startTick(d.startedAt);
    else stopTick(d.elapsedMs);
  });
  R.EventsOn("wordlist:loading", (d) => setWLStatus(`loading ${d.name}…`));
  R.EventsOn("wordlist:ready", (d) => { setWLStatus(""); loadWordlists(); });
}

function scheduleEndpoints() {
  if (state.epDirty) return;
  state.epDirty = true;
  requestAnimationFrame(() => {
    state.epDirty = false;
    renderEndpoints();
  });
}

// ---- controls -------------------------------------------------------------

async function createSession() {
  const url = $("in-url").value.trim();
  const msg = $("create-msg");
  msg.className = "msg";
  if (!url) { msg.textContent = "Enter a URL first."; msg.className = "msg error"; return; }
  try {
    const name = $("in-name").value.trim();
    await App().CreateSession(name, url, readParams());
    $("in-url").value = "";
    $("in-name").value = "";
    msg.textContent = "Created.";
    await loadSessions();
  } catch (e) {
    msg.textContent = "Error: " + e;
    msg.className = "msg error";
  }
}

async function runCurrent() {
  if (!state.current) return;
  state.current.hits = [];
  state.current.endpoints = [];
  renderResults();
  renderEndpoints();
  await App().StartSession(state.current.id, readParams());
}

async function stopCurrent() {
  if (state.current) await App().StopSession(state.current.id);
}

async function deleteCurrent() {
  if (!state.current) return;
  await App().DeleteSession(state.current.id);
  backToList();
}

// ---- boot -----------------------------------------------------------------

ready(() => {
  wireRuntimeEvents();
  loadWordlists();
  loadSessions();

  $("btn-create").onclick = createSession;
  $("btn-back").onclick = backToList;
  $("btn-run").onclick = runCurrent;
  $("btn-stop").onclick = stopCurrent;
  $("btn-del").onclick = deleteCurrent;
  $("in-url").addEventListener("keydown", (e) => { if (e.key === "Enter") createSession(); });
  document.querySelectorAll(".tab").forEach((t) => { t.onclick = () => setTab(t.dataset.tab); });

  // wordlist controls
  $("wl-select").onchange = async (e) => {
    setWLStatus("loading…");
    try { await App().SelectWordlist(e.target.value); } catch (err) { setWLStatus("error"); }
  };
  $("wl-load").onclick = async () => {
    setWLStatus("importing…");
    try { await App().PickWordlist(); } catch (err) { /* cancelled */ }
    setWLStatus(""); loadWordlists();
  };
  $("wl-del").onclick = async () => {
    const name = $("wl-select").value;
    if (!name) return;
    try { await App().DeleteWordlist(name); } catch (err) { setWLStatus("error"); }
    loadWordlists();
  };
  $("wl-add-toggle").onclick = () => {
    const row = $("wl-add-row");
    row.classList.toggle("hidden");
    if (!row.classList.contains("hidden")) $("wl-add-input").focus();
  };
  $("wl-add-cancel").onclick = () => $("wl-add-row").classList.add("hidden");
  const doAdd = async () => {
    const text = $("wl-add-input").value.trim();
    if (!text) return;
    setWLStatus("adding…");
    try { await App().AppendWords(text); $("wl-add-input").value = ""; $("wl-add-row").classList.add("hidden"); }
    catch (err) { setWLStatus("error"); }
    loadWordlists();
  };
  $("wl-add-go").onclick = doAdd;
  $("wl-add-input").addEventListener("keydown", (e) => { if (e.key === "Enter") doAdd(); });
});
