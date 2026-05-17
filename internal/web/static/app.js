// seedstorm UI glue: connection presets, simple form runners, and the
// unified workspace (interactive graph + side panel + live job stream).
(function () {
  "use strict";

  const PRESET_KEY = "seedstorm.connections.v1";

  // ── localStorage presets for the connection form ───────────────────────
  function loadPresets() {
    try { return JSON.parse(localStorage.getItem(PRESET_KEY) || "{}"); }
    catch (_) { return {}; }
  }
  function savePresets(p) { localStorage.setItem(PRESET_KEY, JSON.stringify(p)); }

  function setupConnectForm() {
    const form = document.getElementById("connect-form");
    if (!form) return;
    const picker = document.getElementById("preset-picker");
    const deleteBtn = document.getElementById("preset-delete");
    const includePw = document.getElementById("preset-include-pw");
    const pwInput = document.getElementById("conn-password");
    const eyeBtn = document.getElementById("toggle-password");
    const dbType = form.querySelector('[name="dbType"]');
    const port = form.querySelector('[name="port"]');
    const defaultPorts = { postgres: "5432", mysql: "3306" };

    // Eye toggle: closed by default, click reveals
    if (eyeBtn && pwInput) {
      eyeBtn.addEventListener("click", () => {
        const revealed = eyeBtn.dataset.revealed === "true";
        eyeBtn.dataset.revealed = revealed ? "false" : "true";
        pwInput.type = revealed ? "password" : "text";
        eyeBtn.setAttribute("aria-label", revealed ? "Reveal password" : "Hide password");
      });
    }

    const presets = loadPresets();
    Object.keys(presets).sort().forEach((name) => {
      const opt = document.createElement("option");
      opt.value = name; opt.textContent = name;
      if (presets[name].password) opt.textContent += " · 🔒";
      picker.appendChild(opt);
    });
    picker.addEventListener("change", () => {
      const p = loadPresets()[picker.value];
      deleteBtn.disabled = !picker.value;
      if (!p) return;
      for (const [k, v] of Object.entries(p)) {
        const el = form.querySelector(`[name="${k}"]`);
        if (el) el.value = v;
      }
      // Reset eye to closed after auto-fill, regardless of whether password
      // was loaded — never show secrets without an explicit click.
      if (eyeBtn) {
        eyeBtn.dataset.revealed = "false";
        if (pwInput) pwInput.type = "password";
      }
    });
    if (dbType && port) {
      dbType.addEventListener("change", () => {
        const next = defaultPorts[dbType.value];
        const known = Object.values(defaultPorts).includes(port.value);
        if (next && (port.value === "" || known)) port.value = next;
      });
    }
    deleteBtn.addEventListener("click", () => {
      const all = loadPresets();
      delete all[picker.value];
      savePresets(all);
      picker.querySelector(`option[value="${picker.value}"]`)?.remove();
      picker.value = "";
      deleteBtn.disabled = true;
    });
    form.addEventListener("submit", () => {
      const nameInput = document.getElementById("preset-name");
      const name = nameInput ? nameInput.value.trim() : "";
      if (!name) return;
      const data = new FormData(form);
      const preset = {};
      ["dbType", "host", "port", "dbName", "user", "ssl"].forEach((k) => {
        preset[k] = data.get(k) || "";
      });
      if (includePw && includePw.checked) {
        preset.password = data.get("password") || "";
      }
      const all = loadPresets();
      all[name] = preset;
      savePresets(all);
    });
  }

  // ── shared job streaming ──────────────────────────────────────────────
  let elapsedTimer = null;
  function startElapsed() {
    const el = document.getElementById("job-elapsed");
    if (!el) return;
    const start = Date.now();
    el.textContent = "0.0s";
    if (elapsedTimer) clearInterval(elapsedTimer);
    elapsedTimer = setInterval(() => {
      const s = (Date.now() - start) / 1000;
      el.textContent = s < 60 ? s.toFixed(1) + "s" : (s / 60).toFixed(1) + "m";
    }, 100);
  }
  function stopElapsed() {
    if (elapsedTimer) { clearInterval(elapsedTimer); elapsedTimer = null; }
  }
  function setStatus(status) {
    const pill = document.getElementById("job-status");
    if (!pill) return;
    pill.textContent = status;
    pill.className = "status-pill " + status;
    if (status === "running") startElapsed();
    else stopElapsed();
  }

  // Phase accordion: each emitted "phase" event opens a new <details> block.
  // Log lines append into the current phase; lines emitted before any phase
  // event land in an implicit "log" phase so older flows keep working.
  const phases = { container: null, current: null, started: 0, defaultLabel: "log" };

  function resetPhases() {
    const c = document.getElementById("job-phases");
    if (c) c.innerHTML = "";
    phases.container = c;
    phases.current = null;
    phases.started = 0;
    const wrap = document.getElementById("job-progress-wrap");
    if (wrap) wrap.hidden = true;
    const label = document.getElementById("job-progress-label");
    if (label) label.textContent = "";
    const bar = document.getElementById("job-progress");
    if (bar) bar.value = 0;
  }
  function startPhase(name) {
    if (!phases.container) phases.container = document.getElementById("job-phases");
    if (!phases.container) return null;
    if (phases.current) {
      phases.current.dataset.state = "done";
      phases.current.removeAttribute("open");
      const dur = ((Date.now() - Number(phases.current.dataset.startedAt)) / 1000).toFixed(1);
      const meta = phases.current.querySelector(".job-phase-dur");
      if (meta) meta.textContent = dur + "s";
    }
    const det = document.createElement("details");
    det.className = "job-phase";
    det.dataset.phase = name;
    det.dataset.state = "running";
    det.dataset.startedAt = Date.now();
    det.open = true;
    det.innerHTML =
      '<summary>' +
        '<span class="job-phase-dot" aria-hidden="true"></span>' +
        '<span class="job-phase-name"></span>' +
        '<span class="job-phase-meta muted small">' +
          '<span class="job-phase-count">0 lines</span>' +
          ' · <span class="job-phase-dur">…</span>' +
        '</span>' +
      '</summary>' +
      '<pre class="job-phase-log"></pre>';
    det.querySelector(".job-phase-name").textContent = name;
    phases.container.appendChild(det);
    phases.current = det;
    phases.started++;
    return det;
  }
  function ensurePhase() {
    if (phases.current) return phases.current;
    return startPhase(phases.defaultLabel);
  }
  function appendLog(text) {
    const det = ensurePhase();
    if (!det) return;
    const pre = det.querySelector(".job-phase-log");
    pre.textContent += text + "\n";
    const counter = det.querySelector(".job-phase-count");
    if (counter) {
      const n = (pre.textContent.match(/\n/g) || []).length;
      counter.textContent = n + (n === 1 ? " line" : " lines");
    }
    if (det.open) pre.scrollTop = pre.scrollHeight;
  }
  function setProgress(done, total, label) {
    const wrap = document.getElementById("job-progress-wrap");
    const bar = document.getElementById("job-progress");
    const lab = document.getElementById("job-progress-label");
    if (!wrap || !bar || !lab) return;
    wrap.hidden = false;
    const pct = total > 0 ? Math.round((done / total) * 100) : 0;
    bar.value = pct;
    bar.max = 100;
    const phase = phases.current ? phases.current.dataset.phase : "";
    const tail = label ? " · " + label : "";
    lab.textContent = (phase ? phase + " · " : "") + done + " / " + total + tail;
  }
  function finalizeLastPhase(status) {
    if (!phases.current) return;
    phases.current.dataset.state = (status === "done") ? "ok" : status;
    const meta = phases.current.querySelector(".job-phase-dur");
    if (meta) {
      const dur = ((Date.now() - Number(phases.current.dataset.startedAt)) / 1000).toFixed(1);
      meta.textContent = dur + "s";
    }
  }

  // Parse the SSE data prefix `[seq] payload` so we can route by event type
  // without losing the seq counter (currently unused by the UI but logged).
  function stripSeq(s) {
    const m = /^\[(\d+)\]\s?(.*)$/.exec(s);
    return m ? m[2] : s;
  }
  function streamJob(jobId, jobName, hooks) {
    const cancel = document.getElementById("job-cancel");
    setStatus("running");
    resetPhases();
    if (cancel) {
      cancel.disabled = false;
      cancel.onclick = () => fetch(`/api/jobs/${jobId}/cancel`, { method: "POST" });
    }
    const expandAll = document.getElementById("job-expand-all");
    if (expandAll) {
      expandAll.onclick = () => {
        const open = expandAll.dataset.open === "true";
        document.querySelectorAll(".job-phase").forEach((d) => { d.open = !open; });
        expandAll.dataset.open = (!open).toString();
      };
    }
    const es = new EventSource(`/api/jobs/${jobId}/stream`);
    es.addEventListener("log", (e) => {
      const text = stripSeq(e.data);
      appendLog(text);
      hooks?.onLog?.(text);
    });
    es.addEventListener("phase", (e) => {
      const text = stripSeq(e.data);
      startPhase(text);
    });
    es.addEventListener("progress", (e) => {
      // payload: `[seq] done/total label`
      const m = /^\[\d+\]\s?(\d+)\/(\d+)\s?(.*)$/.exec(e.data);
      if (!m) return;
      setProgress(Number(m[1]), Number(m[2]), m[3]);
    });
    es.addEventListener("status", (e) => setStatus(e.data));
    es.addEventListener("error", (e) => {
      if (e.data) appendLog("ERROR: " + e.data);
    });
    es.addEventListener("end", () => {
      es.close();
      if (cancel) cancel.disabled = true;
      fetch(`/api/jobs/${jobId}`).then(r => r.json()).then((j) => {
        setStatus(j.status);
        finalizeLastPhase(j.status);
        hooks?.onEnd?.(j);
      });
    });
    es.onerror = () => { es.close(); };
  }

  // ── simple run-form (used by /generate, /enrich, /export pages) ───────
  function setupRunForm() {
    const form = document.getElementById("run-form");
    if (!form) return;
    const endpoint = form.dataset.endpoint;
    document.getElementById("job-clear")?.addEventListener("click", () => {
      resetPhases();
      document.getElementById("job-result").innerHTML = "";
    });
    form.addEventListener("submit", async (ev) => {
      ev.preventDefault();
      const data = new FormData(form);
      const payload = {};
      for (const [k, v] of data.entries()) {
        const el = form.querySelector(`[name="${k}"]`);
        if (el && el.type === "checkbox") payload[k] = el.checked;
        else if (el && el.type === "number") payload[k] = v === "" ? 0 : Number(v);
        else payload[k] = v;
      }
      form.querySelectorAll('input[type="checkbox"]').forEach((el) => {
        if (!(el.name in payload)) payload[el.name] = false;
      });
      const res = await fetch(endpoint, {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify(payload),
      });
      const j = await res.json();
      document.getElementById("job-panel").hidden = false;
      if (!res.ok) { resetPhases(); appendLog("ERROR: " + (j.error || res.statusText)); return; }
      streamJob(j.id, j.name, {
        onEnd: (job) => {
          const r = job.result || {};
          const out = document.getElementById("job-result");
          if (!out) return;
          if (typeof r.output === "string") {
            const pre = document.createElement("pre");
            pre.className = "job-log"; pre.textContent = r.output;
            out.appendChild(pre);
            const dl = document.createElement("a");
            dl.className = "btn-ghost";
            dl.href = "data:text/plain;charset=utf-8," + encodeURIComponent(r.output);
            dl.download = `seedstorm-${j.name}.${r.format || "txt"}`;
            dl.textContent = "Download";
            out.appendChild(dl);
          }
          if (typeof r.yaml === "string") {
            const pre = document.createElement("pre");
            pre.className = "job-log"; pre.textContent = r.yaml;
            out.appendChild(pre);
          }
        },
      });
    });
  }

  // ── Workspace ─────────────────────────────────────────────────────────
  const ws = {
    cy: null,
    selected: new Set(),  // explicit user picks
    auto: new Set(),      // auto-locked transitive parents
    parents: {},          // table → [hard parents]
    nodes: [],            // raw graph payload
    edges: [],
    mode: "seed",
    activeJob: null,
    activeTable: null,
    search: "",
    preview: { limit: 25, offset: 0 },
    modal: { table: "", limit: 50, offset: 0 },
    peek: new Set(),
    schemaColumns: {},
  };

  function setupWorkspace() {
    const cyEl = document.getElementById("cy");
    if (!cyEl || typeof cytoscape === "undefined") return;
    if (typeof cytoscapeDagre !== "undefined" && !cytoscape.__dagreRegistered) {
      cytoscape.use(cytoscapeDagre);
      cytoscape.__dagreRegistered = true;
    }

    // Tabs
    document.querySelectorAll(".ws-tab").forEach((b) => {
      b.addEventListener("click", () => activateTab(b.dataset.tab));
    });
    // Mode pills — recompute auto since gaps mode skips populated parents.
    document.querySelectorAll(".ws-mode-pill").forEach((b) => {
      b.addEventListener("click", () => {
        document.querySelectorAll(".ws-mode-pill").forEach(x => x.classList.remove("active"));
        b.classList.add("active");
        ws.mode = b.dataset.mode;
        recomputeAuto();
        refreshSelectionUI();
      });
    });
    // Toolbar
    document.querySelector('[data-act="all"]').addEventListener("click", () => selectAll());
    document.querySelector('[data-act="none"]').addEventListener("click", () => clearSelection());
    document.querySelector('[data-act="empty"]').addEventListener("click", () => selectEmpty());
    document.querySelector('[data-act="invert"]').addEventListener("click", () => invertSelection());
    document.querySelector('[data-act="refresh"]').addEventListener("click", () => refreshCounts());
    document.getElementById("ws-search")?.addEventListener("input", (ev) => applySearch(ev.target.value));
    document.getElementById("ws-search")?.addEventListener("keydown", (ev) => {
      if (ev.key === "Enter") {
        ev.preventDefault();
        focusFirstSearchHit();
      }
    });
    document.getElementById("ws-fit")?.addEventListener("click", () => fitGraph());
    document.getElementById("ws-zoom-in")?.addEventListener("click", () => zoomGraph(1.18));
    document.getElementById("ws-zoom-out")?.addEventListener("click", () => zoomGraph(0.84));
    setupTableModal();
    document.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") closeTableModal();
      if (ev.target && ["INPUT", "TEXTAREA", "SELECT"].includes(ev.target.tagName)) return;
      if (ev.key === "/") {
        ev.preventDefault();
        document.getElementById("ws-search")?.focus();
      }
      if (ev.key.toLowerCase() === "f") fitGraph();
    });
    // Run
    document.getElementById("ws-run").addEventListener("click", runMode);

    fetch("/api/graph").then(r => r.json()).then(initGraph);
  }

  function activateTab(name) {
    document.querySelectorAll(".ws-tab").forEach((b) => {
      b.classList.toggle("active", b.dataset.tab === name);
    });
    document.querySelectorAll(".ws-tab-body").forEach((b) => {
      b.hidden = b.dataset.tab !== name;
    });
  }

  function initGraph(data) {
    ws.nodes = data.nodes || [];
    ws.edges = data.edges || [];
    // Compute hard FK parents per table (from non-nullable edges).
    ws.parents = {};
    for (const n of ws.nodes) ws.parents[n.id] = [];
    for (const e of ws.edges) {
      if (!e.nullable) ws.parents[e.target].push(e.source);
    }

    const elements = [
      ...ws.nodes.map(n => ({ data: nodeData(n) })),
      ...ws.edges.map(e => ({ data: { id: e.id, source: e.source, target: e.target, label: e.column, nullable: e.nullable } })),
    ];

    ws.cy = cytoscape({
      container: document.getElementById("cy"),
      elements,
      style: cyStyle(),
      layout: dagreLayout(),
      wheelSensitivity: 0.3,
    });

    ws.cy.on("tap", "node", (ev) => toggleSelect(ev.target.id()));
    ws.cy.on("cxttap", "node", (ev) => {
      ev.preventDefault?.();
      showDetail(ev.target.id());
    });
    ws.cy.on("mouseover", "node", (ev) => {
      ws.cy.batch(() => {
        ev.target.predecessors().addClass("hover-anc");
        ev.target.addClass("hover-node");
      });
    });
    ws.cy.on("mouseout", "node", () => {
      ws.cy.batch(() => {
        ws.cy.elements(".hover-anc").removeClass("hover-anc");
        ws.cy.elements(".hover-node").removeClass("hover-node");
      });
    });

    document.getElementById("ws-count-total").textContent = String(ws.nodes.length);
    updateStats();
    refreshSelectionUI();
  }

  function nodeData(n) {
    return {
      id: n.id,
      label: n.label,
      count: n.count,
      counted: n.counted,
      countLabel: n.counted ? formatCount(n.count) : "?",
    };
  }

  function formatCount(n) {
    if (n >= 1_000_000) return (n / 1_000_000).toFixed(1) + "M";
    if (n >= 1_000)     return (n / 1_000).toFixed(1) + "k";
    return String(n);
  }

  function dagreLayout() {
    return { name: "dagre", rankDir: "LR", nodeSep: 22, rankSep: 70, edgeSep: 12 };
  }

  function cyStyle() {
    return [
      {
        selector: "node",
        style: {
          "background-color": "#1d2230",
          "border-color": "#3b465f",
          "border-width": 1.5,
          "label": "data(label)",
          "color": "#e6e9f2",
          "font-size": 12,
          "text-valign": "center",
          "text-halign": "center",
          "padding": "10px",
          "shape": "round-rectangle",
          "width": "label",
          "height": "label",
          "transition-property": "border-color background-color",
          "transition-duration": 150,
        },
      },
      // count badge using overlay node label trick
      {
        selector: "node[count > 0]",
        style: { "border-color": "#5fd28e" },
      },
      {
        selector: "node[count = 0][?counted]",
        style: { "border-color": "#5a6079", "background-color": "#171b25" },
      },
      {
        selector: "node.selected",
        style: {
          "border-color": "#7c9eff",
          "border-width": 2.5,
          "background-color": "#22305b",
        },
      },
      {
        selector: "node.auto",
        style: {
          "border-color": "#b196ff",
          "border-width": 2,
          "border-style": "dashed",
          "background-color": "#2a2440",
        },
      },
      {
        selector: "node.seeding",
        style: {
          "border-color": "#ffcc66",
          "border-width": 3,
          "background-color": "#3a2f17",
        },
      },
      {
        selector: "node.done",
        style: {
          "border-color": "#5fd28e",
          "border-width": 2.5,
          "background-color": "#1c3a28",
        },
      },
      {
        selector: "node.hover-anc",
        style: { "border-color": "#7c9eff", "border-width": 2 },
      },
      {
        selector: "node.hover-node",
        style: { "border-color": "#b196ff" },
      },
      {
        selector: "node.search-hit",
        style: {
          "border-color": "#ffcc66",
          "border-width": 3,
          "background-color": "#352f1d",
        },
      },
      {
        selector: "node.search-dim",
        style: { "opacity": 0.28 },
      },
      {
        selector: "edge.search-dim",
        style: { "opacity": 0.2 },
      },
      {
        selector: "edge",
        style: {
          "width": 1.4,
          "line-color": "#3b465f",
          "target-arrow-color": "#3b465f",
          "target-arrow-shape": "triangle",
          "curve-style": "bezier",
          "arrow-scale": 0.9,
        },
      },
      {
        selector: "edge[?nullable]",
        style: { "line-style": "dashed", "line-color": "#4a5169", "target-arrow-color": "#4a5169" },
      },
    ];
  }

  // ── selection mechanics ───────────────────────────────────────────────
  function toggleSelect(id) {
    if (ws.auto.has(id) && !ws.selected.has(id)) {
      // Auto-locked: clicking promotes it to explicit so the user can deselect.
      ws.selected.add(id);
      recomputeAuto();
      refreshSelectionUI();
      return;
    }
    if (ws.selected.has(id)) ws.selected.delete(id);
    else ws.selected.add(id);
    recomputeAuto();
    refreshSelectionUI();
  }

  function isPopulated(id) {
    const n = ws.nodes.find(x => x.id === id);
    return !!(n && n.counted && n.count > 0);
  }

  // Auto-lock dependency closure. In gaps mode populated parents are skipped:
  // their existing rows already satisfy FKs, so they don't need to be filled.
  function recomputeAuto() {
    const auto = new Set();
    const queue = [...ws.selected];
    while (queue.length) {
      const t = queue.shift();
      for (const p of (ws.parents[t] || [])) {
        if (ws.selected.has(p) || auto.has(p)) continue;
        if (ws.mode === "gaps" && isPopulated(p)) continue;
        auto.add(p);
        queue.push(p);
      }
    }
    ws.auto = auto;
  }

  function selectAll() {
    ws.selected = new Set(ws.nodes.map(n => n.id));
    ws.auto = new Set();
    refreshSelectionUI();
  }
  function clearSelection() {
    ws.selected = new Set();
    ws.auto = new Set();
    refreshSelectionUI();
  }
  function selectEmpty() {
    ws.selected = new Set(ws.nodes.filter(n => n.counted && n.count === 0).map(n => n.id));
    recomputeAuto();
    refreshSelectionUI();
  }
  function invertSelection() {
    const next = new Set();
    for (const n of ws.nodes) {
      if (!ws.selected.has(n.id)) next.add(n.id);
    }
    ws.selected = next;
    recomputeAuto();
    refreshSelectionUI();
  }

  function refreshSelectionUI() {
    if (!ws.cy) return;
    ws.cy.batch(() => {
      ws.cy.nodes().forEach((n) => {
        const id = n.id();
        n.removeClass("selected auto");
        if (ws.selected.has(id)) n.addClass("selected");
        else if (ws.auto.has(id)) n.addClass("auto");
      });
    });

    document.getElementById("ws-count-selected").textContent = String(ws.selected.size);
    document.getElementById("ws-count-auto").textContent = String(ws.auto.size);
    updateRunScope();

    const list = document.getElementById("ws-selected-list");
    const empty = document.getElementById("ws-selected-empty");
    list.innerHTML = "";
    if (ws.selected.size === 0 && ws.auto.size === 0) {
      empty.hidden = false;
      return;
    }
    empty.hidden = true;
    // Show in topological order using node ordering provided by /api/graph
    // (ws.nodes is already alpha-sorted; the runner re-sorts topologically server-side).
    // Compose: explicit picks first, then auto-locked, both alpha-sorted within group.
    const ordered = [
      ...[...ws.selected].sort().map(id => ({ id, kind: "sel" })),
      ...[...ws.auto].sort().map(id => ({ id, kind: "auto" })),
    ];
    for (const item of ordered) {
      const li = document.createElement("li");
      li.className = "ws-sel-item " + item.kind;
      if (ws.peek.has(item.id)) li.classList.add("open");
      const main = document.createElement("div");
      main.className = "ws-sel-main";
      const name = document.createElement("span");
      name.textContent = item.id;
      const actions = document.createElement("span");
      actions.className = "ws-sel-actions";
      const tag = document.createElement("span");
      tag.className = "ws-sel-tag";
      tag.textContent = item.kind === "sel" ? "selected" : "auto";
      const peek = document.createElement("button");
      peek.className = "ws-sel-view";
      peek.type = "button";
      peek.textContent = ws.peek.has(item.id) ? "Hide" : "Peek";
      peek.title = "Expand a compact row preview";
      const inspect = document.createElement("button");
      inspect.className = "ws-sel-view";
      inspect.type = "button";
      inspect.textContent = "Open";
      inspect.title = "Open a large row preview";
      actions.append(tag, peek, inspect);
      if (item.kind === "sel") {
        const remove = document.createElement("button");
        remove.className = "ws-sel-view danger";
        remove.type = "button";
        remove.textContent = "Remove";
        remove.title = "Unselect this table";
        remove.addEventListener("click", (ev) => {
          ev.stopPropagation();
          ws.selected.delete(item.id);
          ws.peek.delete(item.id);
          recomputeAuto();
          refreshSelectionUI();
        });
        actions.append(remove);
      }
      main.append(name, actions);
      li.append(main);
      const preview = document.createElement("div");
      preview.className = "ws-sel-peek";
      preview.hidden = !ws.peek.has(item.id);
      li.append(preview);
      main.addEventListener("click", () => togglePeek(item.id));
      peek.addEventListener("click", (ev) => {
        ev.stopPropagation();
        togglePeek(item.id);
      });
      inspect.addEventListener("click", (ev) => {
        ev.stopPropagation();
        openTableModal(item.id);
      });
      list.appendChild(li);
      if (ws.peek.has(item.id)) loadPeek(item.id, preview);
    }
  }

  function togglePeek(tableName) {
    if (ws.peek.has(tableName)) ws.peek.delete(tableName);
    else ws.peek.add(tableName);
    refreshSelectionUI();
  }

  async function loadPeek(tableName, target) {
    target.hidden = false;
    target.innerHTML = '<p class="muted small">Loading rows...</p>';
    const q = new URLSearchParams({ table: tableName, limit: "5", offset: "0" });
    const res = await fetch("/api/table?" + q.toString());
    const data = await res.json();
    if (!res.ok) {
      target.innerHTML = `<p class="muted small">Preview failed: ${escapeHTML(data.error || res.statusText)}</p>`;
      return;
    }
    if (!data.rows || data.rows.length === 0) {
      target.innerHTML = '<p class="muted small">No rows yet.</p>';
      return;
    }
    const columns = (data.columns || []).slice(0, 3);
    const cards = data.rows.map((row) => {
      const cells = columns.map((c) => {
        const value = row[c] || "";
        return `<span><strong>${escapeHTML(c)}</strong>${escapeHTML(value)}</span>`;
      }).join("");
      return `<div class="ws-peek-row">${cells}</div>`;
    }).join("");
    const more = data.total > data.rows.length ? `<span>${data.rows.length} of ${data.total}</span>` : `<span>${data.total} rows</span>`;
    target.innerHTML = `
      <div class="ws-peek-meta">${more}<button type="button" class="ws-peek-open">Open table</button></div>
      ${cards}
    `;
    target.querySelector(".ws-peek-open")?.addEventListener("click", (ev) => {
      ev.stopPropagation();
      openTableModal(tableName);
    });
  }

  function updateStats() {
    const total = ws.nodes.length;
    const counted = ws.nodes.filter(n => n.counted);
    const empty = counted.filter(n => n.count === 0).length;
    const populated = counted.filter(n => n.count > 0).length;
    const set = (id, value) => {
      const el = document.getElementById(id);
      if (el) el.textContent = String(value);
    };
    set("ws-stat-tables", total);
    set("ws-stat-empty", empty);
    set("ws-stat-populated", populated);
  }

  function updateRunScope() {
    const total = ws.nodes.length;
    const explicit = ws.selected.size;
    const auto = ws.auto.size;
    const effective = explicit + auto;
    const scope = document.getElementById("ws-scope");
    const run = document.getElementById("ws-run");
    const modeLabel = ws.mode === "gaps" ? "Fill empty" : (ws.mode === "generate" ? "Generate" : "Seed");
    if (scope) {
      scope.textContent = effective === 0
        ? `Run scope: all ${total} tables`
        : `Run scope: ${effective} tables (${explicit} selected, ${auto} required)`;
    }
    if (run) {
      run.textContent = effective === 0 ? `${modeLabel} all tables` : `${modeLabel} ${effective} tables`;
    }
  }

  function applySearch(raw) {
    ws.search = (raw || "").trim().toLowerCase();
    if (!ws.cy) return;
    ws.cy.batch(() => {
      ws.cy.nodes().removeClass("search-hit search-dim");
      ws.cy.edges().removeClass("search-dim");
      if (!ws.search) return;
      ws.cy.nodes().forEach((n) => {
        if (n.id().toLowerCase().includes(ws.search)) n.addClass("search-hit");
        else n.addClass("search-dim");
      });
      ws.cy.edges().forEach((e) => {
        if (!e.source().hasClass("search-hit") && !e.target().hasClass("search-hit")) e.addClass("search-dim");
      });
    });
  }

  function focusFirstSearchHit() {
    if (!ws.cy || !ws.search) return;
    const hit = ws.cy.nodes(".search-hit")[0];
    if (!hit) return;
    ws.cy.animate({ center: { eles: hit }, zoom: Math.max(ws.cy.zoom(), 1.1) }, { duration: 220 });
    showDetail(hit.id());
  }

  function fitGraph() {
    if (!ws.cy) return;
    const eles = ws.search ? ws.cy.nodes(".search-hit") : ws.cy.elements();
    ws.cy.animate({ fit: { eles: eles.length ? eles : ws.cy.elements(), padding: 42 } }, { duration: 220 });
  }

  function zoomGraph(factor) {
    if (!ws.cy) return;
    ws.cy.animate({ zoom: ws.cy.zoom() * factor, center: { eles: ws.cy.elements() } }, { duration: 160 });
  }

  // ── detail tab ────────────────────────────────────────────────────────
  function showDetail(tableName) {
    activateTab("detail");
    ws.activeTable = tableName;
    ws.preview.offset = 0;
    const target = document.getElementById("ws-detail");
    target.innerHTML = "<p class='muted small'>loading...</p>";
    fetch("/api/schema").then(r => r.json()).then((sc) => {
      const t = (sc.tables && sc.tables[tableName]) || (sc.Tables && sc.Tables[tableName]);
      if (!t) { target.innerHTML = "<p class='muted small'>not in schema</p>"; return; }
      const entries = Object.entries(t.columns || t.Columns);
      ws.schemaColumns[tableName] = Object.fromEntries(entries.map(([col, c]) => [col, {
        nullable: !!(c.nullable || c.Nullable),
        fk: c.fk || c.FK || "",
        pk: !!(c.pk || c.PK),
      }]));
      const nullableCount = entries.filter(([, c]) => c.nullable || c.Nullable).length;
      const fkCount = entries.filter(([, c]) => c.fk || c.FK).length;
      const rows = entries.map(([col, c]) => {
        const flags = [];
        if (c.pk || c.PK) flags.push('<span class="badge pk">PK</span>');
        if (c.fk || c.FK) flags.push(`<span class="badge fk">FK -> ${escapeHTML(c.fk || c.FK)}</span>`);
        if (c.nullable || c.Nullable) flags.push('<span class="badge nullable">nullable</span>');
        return `<tr><td><code>${escapeHTML(col)}</code> ${flags.join(" ")}</td><td><span class="type">${escapeHTML(c.type || c.Type || "")}</span></td></tr>`;
      }).join("");
      target.innerHTML = `
        <div class="detail-head">
          <div>
            <h3>${escapeHTML(tableName)}</h3>
            <p class="muted small">Columns and live data preview</p>
            <div class="detail-stats">
              <span>${entries.length} columns</span>
              <span>${nullableCount} nullable</span>
              <span>${fkCount} FK</span>
            </div>
          </div>
          <button class="btn-ghost" id="preview-refresh" type="button">Refresh rows</button>
        </div>
        <table class="cols schema-cols"><tbody>${rows}</tbody></table>
        <div class="preview-panel">
          <div class="preview-toolbar">
            <div>
              <strong>Rows</strong>
              <span class="muted small" id="preview-meta">loading...</span>
            </div>
            <label class="field-tight inline">
              <span>Limit</span>
              <select id="preview-limit">
                <option value="10">10</option>
                <option value="25" selected>25</option>
                <option value="50">50</option>
                <option value="100">100</option>
              </select>
            </label>
            <label class="field-tight inline preview-toggle">
              <input id="preview-hide-null" type="checkbox">
              hide NULL-only columns
            </label>
          </div>
          <div id="preview-table" class="preview-table-wrap">
            <p class="muted small empty-hint">Loading rows...</p>
          </div>
          <div class="preview-pager">
            <button class="btn-ghost" id="preview-prev" type="button">Previous</button>
            <span class="muted small" id="preview-page"></span>
            <button class="btn-ghost" id="preview-next" type="button">Next</button>
          </div>
        </div>
      `;
      document.getElementById("preview-refresh")?.addEventListener("click", () => loadPreview(tableName));
      document.getElementById("preview-limit")?.addEventListener("change", (ev) => {
        ws.preview.limit = Number(ev.target.value || 25);
        ws.preview.offset = 0;
        loadPreview(tableName);
      });
      document.getElementById("preview-hide-null")?.addEventListener("change", () => loadPreview(tableName));
      document.getElementById("preview-prev")?.addEventListener("click", () => {
        ws.preview.offset = Math.max(0, ws.preview.offset - ws.preview.limit);
        loadPreview(tableName);
      });
      document.getElementById("preview-next")?.addEventListener("click", () => {
        ws.preview.offset += ws.preview.limit;
        loadPreview(tableName);
      });
      loadPreview(tableName);
    });
  }

  function setupTableModal() {
    document.getElementById("table-modal-close")?.addEventListener("click", closeTableModal);
    document.querySelector("[data-modal-close]")?.addEventListener("click", closeTableModal);
    document.getElementById("table-modal-refresh")?.addEventListener("click", () => loadModalPreview());
    document.getElementById("table-modal-limit")?.addEventListener("change", (ev) => {
      ws.modal.limit = Number(ev.target.value || 50);
      ws.modal.offset = 0;
      loadModalPreview();
    });
    document.getElementById("table-modal-hide-null")?.addEventListener("change", () => loadModalPreview());
    document.getElementById("table-modal-prev")?.addEventListener("click", () => {
      ws.modal.offset = Math.max(0, ws.modal.offset - ws.modal.limit);
      loadModalPreview();
    });
    document.getElementById("table-modal-next")?.addEventListener("click", () => {
      ws.modal.offset += ws.modal.limit;
      loadModalPreview();
    });
  }

  async function openTableModal(tableName) {
    ws.modal.table = tableName;
    ws.modal.offset = 0;
    const modal = document.getElementById("table-modal");
    const title = document.getElementById("table-modal-title");
    if (title) title.textContent = tableName;
    if (modal) modal.hidden = false;
    document.body.classList.add("modal-open");
    await ensureSchemaColumns(tableName);
    loadModalPreview();
  }

  function closeTableModal() {
    const modal = document.getElementById("table-modal");
    if (modal) modal.hidden = true;
    document.body.classList.remove("modal-open");
  }

  async function ensureSchemaColumns(tableName) {
    if (ws.schemaColumns[tableName]) return;
    const sc = await fetch("/api/schema").then(r => r.json());
    const t = (sc.tables && sc.tables[tableName]) || (sc.Tables && sc.Tables[tableName]);
    if (!t) return;
    const entries = Object.entries(t.columns || t.Columns);
    ws.schemaColumns[tableName] = Object.fromEntries(entries.map(([col, c]) => [col, {
      nullable: !!(c.nullable || c.Nullable),
      fk: c.fk || c.FK || "",
      pk: !!(c.pk || c.PK),
    }]));
  }

  async function loadModalPreview() {
    const tableName = ws.modal.table;
    const box = document.getElementById("table-modal-body");
    const meta = document.getElementById("table-modal-meta");
    const page = document.getElementById("table-modal-page");
    const prev = document.getElementById("table-modal-prev");
    const next = document.getElementById("table-modal-next");
    const note = document.getElementById("table-modal-note");
    if (!box || !tableName) return;
    box.innerHTML = "<p class='muted small empty-hint'>Loading rows...</p>";
    const q = new URLSearchParams({
      table: tableName,
      limit: String(ws.modal.limit),
      offset: String(ws.modal.offset),
    });
    const res = await fetch("/api/table?" + q.toString());
    const data = await res.json();
    if (!res.ok) {
      box.innerHTML = `<p class="muted small empty-hint">Preview failed: ${escapeHTML(data.error || res.statusText)}</p>`;
      return;
    }
    const start = data.total === 0 ? 0 : data.offset + 1;
    const end = Math.min(data.offset + data.rows.length, data.total);
    if (meta) meta.textContent = `${start}-${end} of ${data.total} rows`;
    if (page) page.textContent = data.total === 0 ? "No rows" : `Page ${Math.floor(data.offset / data.limit) + 1}`;
    if (prev) prev.disabled = data.offset <= 0;
    if (next) next.disabled = data.offset + data.limit >= data.total;
    renderPreviewTable(box, data, tableName, !!document.getElementById("table-modal-hide-null")?.checked, note);
  }

  async function loadPreview(tableName) {
    const box = document.getElementById("preview-table");
    const meta = document.getElementById("preview-meta");
    const page = document.getElementById("preview-page");
    const prev = document.getElementById("preview-prev");
    const next = document.getElementById("preview-next");
    if (!box) return;
    box.innerHTML = "<p class='muted small empty-hint'>Loading rows...</p>";
    const q = new URLSearchParams({
      table: tableName,
      limit: String(ws.preview.limit),
      offset: String(ws.preview.offset),
    });
    const res = await fetch("/api/table?" + q.toString());
    const data = await res.json();
    if (!res.ok) {
      box.innerHTML = `<p class="muted small empty-hint">Preview failed: ${escapeHTML(data.error || res.statusText)}</p>`;
      return;
    }
    const start = data.total === 0 ? 0 : data.offset + 1;
    const end = Math.min(data.offset + data.rows.length, data.total);
    if (meta) meta.textContent = `${start}-${end} of ${data.total}`;
    if (page) page.textContent = data.total === 0 ? "No rows" : `Page ${Math.floor(data.offset / data.limit) + 1}`;
    if (prev) prev.disabled = data.offset <= 0;
    if (next) next.disabled = data.offset + data.limit >= data.total;
    const hideNull = document.getElementById("preview-hide-null")?.checked;
    const note = document.getElementById("preview-null-note");
    if (note) note.remove();
    const inlineNote = { textContent: "" };
    renderPreviewTable(box, data, tableName, !!hideNull, inlineNote);
    if (inlineNote.textContent && meta) meta.insertAdjacentHTML("afterend", `<span class="muted small preview-null-note" id="preview-null-note">${escapeHTML(inlineNote.textContent)}</span>`);
  }

  function renderPreviewTable(box, data, tableName, hideNull, noteEl) {
    if (!data.rows || data.rows.length === 0) {
      box.innerHTML = '<p class="muted small empty-hint">This table has no rows yet.</p>';
      if (noteEl) noteEl.textContent = "";
      return;
    }
    const visibleColumns = hideNull ? data.columns.filter((c) => data.rows.some((row) => row[c] !== "NULL")) : data.columns;
    const metaBits = [];
    const schema = ws.schemaColumns[tableName] || {};
    const hidden = data.columns.length - visibleColumns.length;
    if (hidden > 0) metaBits.push(`${hidden} NULL-only columns hidden`);
    const nullableVisible = visibleColumns.filter((c) => schema[c]?.nullable).length;
    if (nullableVisible > 0) metaBits.push(`${nullableVisible} nullable columns visible`);
    if (noteEl) noteEl.textContent = metaBits.join(" · ");
    if (visibleColumns.length === 0) {
      box.innerHTML = '<p class="muted small empty-hint">All visible rows are NULL-only for this page.</p>';
      return;
    }
    const head = visibleColumns.map(c => {
      const nullable = schema[c]?.nullable ? '<span class="badge nullable">nullable</span>' : "";
      return `<th>${escapeHTML(c)} ${nullable}</th>`;
    }).join("");
    const body = data.rows.map((row) => {
      const cells = visibleColumns.map((c) => `<td title="${escapeHTML(row[c] || "")}">${formatPreviewCell(row[c])}</td>`).join("");
      return `<tr>${cells}</tr>`;
    }).join("");
    box.innerHTML = `<table class="preview-table"><thead><tr>${head}</tr></thead><tbody>${body}</tbody></table>`;
  }

  function formatPreviewCell(value) {
    if (value === "NULL") return '<span class="null-pill">NULL</span>';
    return escapeHTML(value || "");
  }

  function escapeHTML(value) {
    return String(value)
      .replaceAll("&", "&amp;")
      .replaceAll("<", "&lt;")
      .replaceAll(">", "&gt;")
      .replaceAll('"', "&quot;")
      .replaceAll("'", "&#39;");
  }

  // ── run dispatcher ────────────────────────────────────────────────────
  async function runMode() {
    const tables = [...ws.selected];
    const cfg = {
      rows: Number(document.getElementById("cfg-rows").value || 0),
      enumRows: Number(document.getElementById("cfg-enum").value || 0),
      batchSize: Number(document.getElementById("cfg-batch").value || 0),
      truncate: document.getElementById("cfg-truncate").checked,
      dryRun: document.getElementById("cfg-dryrun").checked,
      disableFK: document.getElementById("cfg-disablefk").checked,
      tables,
    };
    let endpoint = "/api/seed";
    if (ws.mode === "gaps") { endpoint = "/api/gaps"; cfg.fill = true; }
    if (ws.mode === "generate") {
      endpoint = "/api/generate";
      cfg.format = "yaml";
    }
    activateTab("logs");
    resetPhases();
    document.getElementById("job-result").innerHTML = "";
    if (ws.cy) ws.cy.nodes().removeClass("seeding done failed");

    const res = await fetch(endpoint, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify(cfg),
    });
    const j = await res.json();
    if (!res.ok) {
      appendLog("ERROR: " + (j.error || res.statusText));
      return;
    }
    streamJob(j.id, j.name, {
      onLog: (line) => onLogPulse(line),
      onEnd: (job) => onJobEnd(job),
    });
  }

  function onLogPulse(line) {
    if (!ws.cy) return;
    // zerolog console writer renders `Seeding table` and `Filling table` with key=value pairs.
    const m = line.match(/Seeding table.*?table=(\w+)|Filling table.*?table=(\w+)/);
    if (m) {
      const t = m[1] || m[2];
      const node = ws.cy.getElementById(t);
      if (node) {
        ws.cy.nodes(".seeding").removeClass("seeding").addClass("done");
        node.addClass("seeding");
      }
    }
  }

  function onJobEnd(job) {
    if (ws.cy) ws.cy.nodes(".seeding").removeClass("seeding").addClass("done");
    const out = document.getElementById("job-result");
    const r = job.result || {};
    if (typeof r.output === "string") {
      const pre = document.createElement("pre");
      pre.className = "job-log"; pre.textContent = r.output;
      out.appendChild(pre);
    }
    if (Array.isArray(r.gapTables)) {
      const div = document.createElement("div");
      div.innerHTML = `<strong>Empty tables:</strong> ${r.gapTables.length === 0 ? "<em>none</em>" : r.gapTables.join(", ")}`;
      out.appendChild(div);
    }
    refreshCounts();
  }

  function refreshCounts() {
    if (!ws.cy) return;
    fetch("/api/counts").then(r => r.json()).then((counts) => {
      ws.cy.batch(() => {
        ws.cy.nodes().forEach((n) => {
          const id = n.id();
          if (id in counts) {
            n.data("count", counts[id]);
            n.data("counted", true);
            n.data("countLabel", formatCount(counts[id]));
          }
        });
      });
      // Keep the JS-side mirror in sync so isPopulated() sees fresh counts.
      for (const n of ws.nodes) {
        if (n.id in counts) {
          n.count = counts[n.id];
          n.counted = true;
        }
      }
      updateStats();
      recomputeAuto();
      refreshSelectionUI();
    });
  }

  document.addEventListener("DOMContentLoaded", () => {
    setupConnectForm();
    setupRunForm();
    setupWorkspace();
  });

  // Lightweight debug surface — useful for poking from the console and for
  // automated UI tests. Not used by the app itself.
  window.seedstorm = {
    state: ws,
    select: (id) => { toggleSelect(id); },
    selectAll, clearSelection, selectEmpty, invertSelection, refreshCounts,
    showDetail,
    activateTab,
    setMode: (m) => {
      ws.mode = m;
      document.querySelectorAll(".ws-mode-pill").forEach(b => b.classList.toggle("active", b.dataset.mode === m));
      recomputeAuto();
      refreshSelectionUI();
    },
    run: runMode,
  };
})();
