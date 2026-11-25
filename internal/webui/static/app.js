const state = {
  view: "board",
  slices: [],
  runs: [],
  stats: null,
  report: null,
  activeSlice: null,
  timer: null,
};

const LANES = [
  { key: "backlog", title: "Backlog", states: ["spec", "locked"] },
  { key: "progress", title: "In progress", states: ["claimed", "red"] },
  { key: "verify", title: "Verify & review", states: ["green", "in_review"] },
  { key: "human", title: "Ready for human", states: ["ready_for_human"], highlight: true },
  { key: "merged", title: "Merged", states: ["merged"] },
];

const el = (id) => document.getElementById(id);

function node(tag, cls, text) {
  const n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined && text !== null) n.textContent = String(text);
  return n;
}

function initTheme() {
  const saved = localStorage.getItem("mill_theme");
  if (saved === "light" || saved === "dark") {
    document.documentElement.setAttribute("data-theme", saved);
  }
  el("theme-btn").addEventListener("click", () => {
    const cur = document.documentElement.getAttribute("data-theme");
    const next = cur === "dark" ? "light" : "dark";
    document.documentElement.setAttribute("data-theme", next);
    localStorage.setItem("mill_theme", next);
  });
}

async function apiFetch(path, opts = {}) {
  const res = await fetch(path, {
    credentials: "same-origin",
    headers: { "Content-Type": "application/json", ...(opts.headers || {}) },
    ...opts,
  });
  if (res.status === 401) {
    showLogin();
    throw new Error("unauthorized");
  }
  if (!res.ok) {
    let msg = `HTTP ${res.status}`;
    try {
      const body = await res.json();
      if (body && body.error) msg = body.error;
    } catch (_) {}
    throw new Error(msg);
  }
  return res.json();
}

function showLogin() {
  stopPolling();
  el("app-view").hidden = true;
  el("login-view").hidden = false;
  el("token-input").focus();
}

function showApp() {
  el("login-view").hidden = true;
  el("app-view").hidden = false;
  startPolling();
}

function formatLease(sl) {
  if (!sl.owner) return { text: "Unclaimed", warn: false };
  if (!sl.lease_expires_at) return { text: sl.owner, warn: false };
  const diffMin = Math.round((Date.parse(sl.lease_expires_at) - Date.now()) / 60000);
  if (diffMin <= 0) return { text: `${sl.owner} · lease expired`, warn: true };
  return { text: `${sl.owner} · ${diffMin}m left`, warn: diffMin <= 5 };
}

function verdictBadge(v) {
  const val = v || "pending";
  return node("span", `verdict verdict-${val}`, val);
}

function currentFilters() {
  return {
    repo: el("repo-filter").value,
    query: el("search-input").value.trim().toLowerCase(),
  };
}

function matchesFilter(sl, { repo, query }) {
  if (repo && sl.repo !== repo) return false;
  if (!query) return true;
  const hay = [sl.id, sl.title, sl.repo, sl.owner, sl.branch, ...(sl.claims || [])]
    .filter(Boolean)
    .join(" ")
    .toLowerCase();
  return hay.includes(query);
}

function syncRepoOptions() {
  const sel = el("repo-filter");
  const prev = sel.value;
  const repos = [...new Set(state.slices.map((s) => s.repo).filter(Boolean))].sort();
  sel.replaceChildren(node("option", "", "All repositories"));
  sel.firstChild.value = "";
  for (const r of repos) {
    const opt = node("option", "", r);
    opt.value = r;
    sel.appendChild(opt);
  }
  sel.value = repos.includes(prev) ? prev : "";
}

function renderKPIs() {
  const s = state.stats || { total_slices: 0, green_runs: 0, red_runs: 0, total_amendments: 0, total_findings: 0 };
  el("kpi-slices").textContent = String(s.total_slices);
  const judged = s.green_runs + s.red_runs;
  el("kpi-pass-rate").textContent = judged > 0 ? `${Math.round((s.green_runs / judged) * 100)}%` : "—";
  el("kpi-amendments").textContent = String(s.total_amendments);
  el("kpi-findings").textContent = String(s.total_findings);
}

function renderBoard() {
  const grid = el("board-grid");
  const filters = currentFilters();
  const visible = state.slices.filter((sl) => matchesFilter(sl, filters));
  const lanes = LANES.map((laneDef) => {
    const items = visible.filter((sl) => laneDef.states.includes(sl.state));
    const lane = node("section", laneDef.highlight ? "lane lane-human" : "lane");
    const head = node("div", "lane-head");
    head.append(node("h2", "lane-title", laneDef.title), node("span", "lane-count", items.length));
    lane.appendChild(head);
    if (items.length === 0) {
      lane.appendChild(node("p", "empty-note", "No slices"));
    } else {
      for (const sl of items) lane.appendChild(buildSliceCard(sl));
    }
    return lane;
  });
  grid.replaceChildren(...lanes);
}

function buildSliceCard(sl) {
  const btn = node("button", "slice-card");
  btn.type = "button";
  const top = node("div", "card-top");
  top.append(node("span", "mono", sl.id), verdictBadge(sl.last_verdict));

  const title = node("div", "card-title", sl.title);
  const metaText = [sl.repo, ...(sl.claims || [])].filter(Boolean).join(" · ");
  const meta = node("div", "card-meta mono", metaText);

  const lease = formatLease(sl);
  const leaseEl = node("div", lease.warn ? "card-meta card-lease-warn" : "card-meta", `${sl.state} · ${lease.text}`);

  btn.append(top, title, meta, leaseEl);
  btn.addEventListener("click", () => openSliceDrawer(sl));
  return btn;
}

function renderLedger() {
  const { query, repo } = currentFilters();
  const sliceMap = new Map(state.slices.map((s) => [s.id, s]));
  const tbody = el("ledger-tbody");
  const rows = [];

  for (const r of state.runs) {
    const sl = sliceMap.get(r.slice_id);
    if (repo && (!sl || sl.repo !== repo)) continue;
    if (query) {
      const hay = [r.slice_id, r.harness, r.model, r.phase, r.verdict, r.notes].join(" ").toLowerCase();
      if (!hay.includes(query)) continue;
    }
    const tr = node("tr");
    const sliceCell = node("td");
    const link = node("button", "row-btn", r.slice_id);
    link.type = "button";
    link.addEventListener("click", () => {
      if (sl) openSliceDrawer(sl);
    });
    sliceCell.appendChild(link);

    const vCell = node("td");
    vCell.appendChild(verdictBadge(r.verdict));

    tr.append(
      node("td", "mono", `#${r.id}`),
      sliceCell,
      node("td", "", r.harness),
      node("td", "mono", r.model || "—"),
      node("td", "", r.phase),
      vCell,
      node("td", "num", `${r.duration_sec}s`),
      node("td", "num", r.amendments),
      node("td", "num", r.footprint_findings),
      node("td", "mono", r.req_diff_direction || "—"),
      node("td", "", r.notes || "—")
    );
    rows.push(tr);
  }
  if (rows.length === 0) {
    const tr = node("tr");
    const td = node("td", "empty-note", "No matching run events");
    td.colSpan = 11;
    tr.appendChild(td);
    rows.push(tr);
  }
  tbody.replaceChildren(...rows);
}

function renderHotspots() {
  const rep = state.report;
  const hBody = el("hotspots-tbody");
  const pBody = el("patterns-tbody");
  if (!rep || !rep.hotspots || rep.hotspots.length === 0) {
    el("hotspots-meta").textContent = "No run ledger events recorded yet.";
    const tr1 = node("tr");
    const td1 = node("td", "empty-note", "No hotspot slices");
    td1.colSpan = 6;
    tr1.appendChild(td1);
    hBody.replaceChildren(tr1);

    const tr2 = node("tr");
    const td2 = node("td", "empty-note", "No failure patterns");
    td2.colSpan = 3;
    tr2.appendChild(td2);
    pBody.replaceChildren(tr2);
    return;
  }

  el("hotspots-meta").textContent = `${rep.total_events} events · ${rep.red_events} red · ${rep.slice_ids.length} slices analyzed`;
  const sliceMap = new Map(state.slices.map((s) => [s.id, s]));

  hBody.replaceChildren(
    ...rep.hotspots.map((h) => {
      const tr = node("tr");
      const idCell = node("td");
      const btn = node("button", "row-btn", h.slice_id);
      btn.type = "button";
      btn.addEventListener("click", () => {
        const sl = sliceMap.get(h.slice_id);
        if (sl) openSliceDrawer(sl);
      });
      idCell.appendChild(btn);
      tr.append(
        idCell,
        node("td", "", h.title || "—"),
        node("td", "num", h.total_runs),
        node("td", "num", h.red_runs),
        node("td", "num", h.amendments),
        node("td", "num", h.footprint_findings)
      );
      return tr;
    })
  );

  const patterns = rep.patterns || [];
  if (patterns.length === 0) {
    const tr = node("tr");
    const td = node("td", "empty-note", "No recurring failure patterns");
    td.colSpan = 3;
    tr.appendChild(td);
    pBody.replaceChildren(tr);
  } else {
    pBody.replaceChildren(
      ...patterns.map((p) => {
        const tr = node("tr");
        tr.append(
          node("td", "mono", p.key),
          node("td", "num", p.count),
          node("td", "mono", (p.slice_ids || []).join(", "))
        );
        return tr;
      })
    );
  }
}

async function openSliceDrawer(sl) {
  state.activeSlice = sl;
  el("drawer-id").textContent = sl.id;
  el("drawer-subtitle").textContent = sl.title;

  const meta = el("drawer-meta");
  const lease = formatLease(sl);
  const rows = [
    ["Repository", sl.repo],
    ["State", sl.state],
    ["Verdict", sl.last_verdict],
    ["Branch", sl.branch || "—"],
    ["Intent lock", sl.intent_lock || "—"],
    ["Claims", (sl.claims || []).join(", ") || "—"],
    ["Lease", `${lease.text} (attempt ${sl.attempt})`],
  ];
  const nodes = [];
  for (const [k, v] of rows) {
    nodes.push(node("dt", "", k), node("dd", "mono", v));
  }
  meta.replaceChildren(...nodes);

  const timeline = el("drawer-runs");
  timeline.replaceChildren(node("p", "empty-note", "Loading runs…"));
  el("slice-drawer").showModal();

  try {
    const runs = await apiFetch(`/runs?slice_id=${encodeURIComponent(sl.id)}&limit=50`);
    if (runs.length === 0) {
      timeline.replaceChildren(node("p", "empty-note", "No runs recorded for this slice."));
      return;
    }
    timeline.replaceChildren(
      ...runs.map((r) => {
        const item = node("div", "run-item");
        const top = node("div", "card-top");
        top.append(node("span", "mono", `#${r.id} · ${r.harness} (${r.phase})`), verdictBadge(r.verdict));
        const info = node("div", "card-meta", `duration=${r.duration_sec}s · amendments=${r.amendments} · findings=${r.footprint_findings} · req_diff=${r.req_diff_direction || "none"}`);
        item.append(top, info);
        if (r.notes) item.appendChild(node("div", "", r.notes));
        return item;
      })
    );
  } catch (err) {
    timeline.replaceChildren(node("p", "form-error", err.message));
  }
}

function renderActiveView() {
  renderKPIs();
  renderBoard();
  renderLedger();
  renderHotspots();
}

function switchView(next) {
  state.view = next;
  for (const btn of document.querySelectorAll(".tab-btn")) {
    btn.classList.toggle("is-active", btn.dataset.view === next);
  }
  el("board-panel").hidden = next !== "board";
  el("ledger-panel").hidden = next !== "ledger";
  el("hotspots-panel").hidden = next !== "hotspots";
}

async function refreshAll() {
  el("sync-status").textContent = "Syncing…";
  try {
    const [stats, slices, runs, report] = await Promise.all([
      apiFetch("/stats"),
      apiFetch("/slices"),
      apiFetch("/runs?limit=100"),
      apiFetch("/observer/report?limit=100"),
    ]);
    state.stats = stats;
    state.slices = slices || [];
    state.runs = runs || [];
    state.report = report;
    syncRepoOptions();
    renderActiveView();
    el("sync-status").textContent = `Synced ${new Date().toLocaleTimeString()}`;
  } catch (err) {
    if (err.message !== "unauthorized") {
      el("sync-status").textContent = `Error: ${err.message}`;
    }
  }
}

function startPolling() {
  stopPolling();
  state.timer = setInterval(() => {
    if (!document.hidden) refreshAll();
  }, 10000);
}

function stopPolling() {
  if (state.timer) {
    clearInterval(state.timer);
    state.timer = null;
  }
}

function bindEvents() {
  el("login-form").addEventListener("submit", async (e) => {
    e.preventDefault();
    const errEl = el("login-error");
    errEl.hidden = true;
    const token = el("token-input").value.trim();
    try {
      await apiFetch("/auth/login", { method: "POST", body: JSON.stringify({ token }) });
      el("token-input").value = "";
      showApp();
      await refreshAll();
    } catch (err) {
      errEl.textContent = err.message;
      errEl.hidden = false;
    }
  });

  el("logout-btn").addEventListener("click", async () => {
    await fetch("/auth/logout", { method: "POST", credentials: "same-origin" });
    showLogin();
  });

  el("refresh-btn").addEventListener("click", refreshAll);
  el("repo-filter").addEventListener("change", renderActiveView);
  el("search-input").addEventListener("input", renderActiveView);

  for (const btn of document.querySelectorAll(".tab-btn")) {
    btn.addEventListener("click", () => switchView(btn.dataset.view));
  }

  const drawer = el("slice-drawer");
  el("drawer-close").addEventListener("click", () => drawer.close());
  drawer.addEventListener("click", (e) => {
    if (e.target === drawer) drawer.close();
  });

  el("copy-claim-cmd").addEventListener("click", async () => {
    if (!state.activeSlice) return;
    await navigator.clipboard.writeText(`mill slice claim --id ${state.activeSlice.id} --owner claude@mac --ttl 30m`);
    el("copy-claim-cmd").textContent = "Copied";
    setTimeout(() => (el("copy-claim-cmd").textContent = "Copy claim CLI"), 1200);
  });

  el("copy-branch-btn").addEventListener("click", async () => {
    if (!state.activeSlice || !state.activeSlice.branch) return;
    await navigator.clipboard.writeText(state.activeSlice.branch);
    el("copy-branch-btn").textContent = "Copied";
    setTimeout(() => (el("copy-branch-btn").textContent = "Copy branch"), 1200);
  });

  el("copy-report-btn").addEventListener("click", async () => {
    if (!state.report) return;
    await navigator.clipboard.writeText(JSON.stringify(state.report, null, 2));
    el("copy-report-btn").textContent = "Copied JSON";
    setTimeout(() => (el("copy-report-btn").textContent = "Copy report JSON"), 1400);
  });

  document.addEventListener("keydown", (e) => {
    if (e.key === "/" && !el("app-view").hidden && document.activeElement.tagName !== "INPUT") {
      e.preventDefault();
      el("search-input").focus();
    }
  });
}

async function boot() {
  initTheme();
  bindEvents();
  try {
    await apiFetch("/auth/session");
    showApp();
    await refreshAll();
  } catch (_) {
    showLogin();
  }
}

boot();
