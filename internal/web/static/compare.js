// Compare & mirror page: pick two connections, read their volumes as a
// per-table gauge, then plan (dry run) and run a mirror onto the target.
(function () {
  "use strict";

  const app = document.getElementById("compare-app");
  if (!app) return;
  const ui = () => window.seedstorm.ui;
  const $ = (id) => document.getElementById(id);

  const state = {
    report: null,
    filter: "all",
    search: "",
    scale: 1,
    excluded: new Set(), // tables the user unticked for the mirror
    pairKey: "",         // source|target the exclusions belong to
    plan: null,
    busy: false,
  };

  // ── connection pickers ──────────────────────────────────────────────
  async function loadPickers() {
    const [live, saved] = await Promise.all([ui().fetchConnections(), ui().fetchSavedConnections()]);
    const options = [];
    const liveKeys = new Set();
    for (const c of live) {
      liveKeys.add(ui().connectionKey(c.info));
      options.push({ value: "id:" + c.id, label: ui().connectionLabel(c.info), group: "Connected", active: c.active, dbType: c.info.dbType });
    }
    for (const c of saved) {
      if (liveKeys.has(ui().connectionKey(c))) continue;
      const locked = !c.hasPassword && !c.dsn;
      options.push({
        value: "saved:" + c.id,
        label: ui().connectionLabel(c) + (locked ? " — connect once to store its password" : ""),
        group: "Saved", disabled: locked, dbType: c.dbType,
      });
    }
    fillSelect($("cmp-source"), options);
    fillSelect($("cmp-target"), options);
    const params = new URLSearchParams(location.search);
    const active = options.find((o) => o.active);
    const other = options.find((o) => !o.active && !o.disabled);
    setSelect($("cmp-source"), params.get("source") || active?.value);
    setSelect($("cmp-target"), params.get("target") || other?.value);

    const usable = options.filter((o) => !o.disabled).length;
    const hint = $("cmp-hint");
    if (usable < 2) {
      hint.hidden = false;
      hint.innerHTML = 'Only one connection is available. <a href="/connect?mode=form">Add another connection</a> to compare against it.';
    }
    syncPickers();
  }

  function fillSelect(select, options) {
    select.innerHTML = "";
    const groups = {};
    for (const o of options) {
      if (!groups[o.group]) {
        groups[o.group] = document.createElement("optgroup");
        groups[o.group].label = o.group;
        select.appendChild(groups[o.group]);
      }
      const opt = document.createElement("option");
      opt.value = o.value;
      opt.textContent = o.label;
      opt.disabled = !!o.disabled;
      groups[o.group].appendChild(opt);
    }
  }

  function setSelect(select, value) {
    if (value && [...select.options].some((o) => o.value === value && !o.disabled)) select.value = value;
  }

  function refOf(select) {
    const [kind, id] = String(select.value || "").split(/:(.+)/);
    if (kind === "id") return { id };
    if (kind === "saved") return { savedId: id };
    return {};
  }

  function syncPickers() {
    const same = $("cmp-source").value && $("cmp-source").value === $("cmp-target").value;
    $("cmp-run").disabled = !$("cmp-source").value || !$("cmp-target").value;
    const hint = $("cmp-hint");
    if (same) {
      hint.hidden = false;
      hint.textContent = "Source and target are the same connection: compare works, but a mirror will be refused.";
    } else if (hint.textContent.startsWith("Source and target")) {
      hint.hidden = true;
    }
    const url = new URL(location.href);
    url.searchParams.set("source", $("cmp-source").value);
    url.searchParams.set("target", $("cmp-target").value);
    history.replaceState(null, "", url);
  }

  function targetLabel() {
    return $("cmp-target").selectedOptions[0]?.textContent || "target";
  }

  // ── jobs ────────────────────────────────────────────────────────────
  function runJob(endpoint, body) {
    return new Promise(async (resolve, reject) => {
      const res = await fetch(endpoint, { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      const j = await res.json().catch(() => ({}));
      if (!res.ok) return reject(new Error(j.error || res.statusText));
      $("job-name").textContent = j.name;
      ui().streamJob(j.id, j.name, { onEnd: (job) => resolve(job) });
    });
  }

  function setBusy(busy, label) {
    state.busy = busy;
    app.classList.toggle("is-busy", busy);
    const run = $("cmp-run");
    run.disabled = busy;
    run.textContent = busy && label ? label : "Compare";
    $("cmp-plan").disabled = busy || !state.report;
  }

  async function compare() {
    if (state.busy) return;
    setBusy(true, "Comparing…");
    const counts = app.querySelector('input[name="counts"]:checked').value;
    try {
      const job = await runJob("/api/compare", { source: refOf($("cmp-source")), target: refOf($("cmp-target")), counts });
      if (job.status !== "done") throw new Error(job.error || "compare " + job.status);
      state.report = job.result.report;
      // Keep the user's table picks when re-comparing the same pair (e.g. after a run).
      const pairKey = $("cmp-source").value + "|" + $("cmp-target").value;
      if (pairKey !== state.pairKey) state.excluded.clear();
      state.pairKey = pairKey;
      render();
    } catch (err) {
      showOutcome("error", "Compare failed", err.message);
      $("cmp-results").hidden = false;
      $("cmp-logs").open = true;
    } finally {
      setBusy(false);
    }
  }

  // ── report rendering ────────────────────────────────────────────────
  // Counts use -1 for "unknown"; deltas are signed, so they format separately.
  const fmt = (n) => (n == null || n < 0 ? "?" : Number(n).toLocaleString());
  const signed = (n) => (n > 0 ? "+" : n < 0 ? "−" : "") + Math.abs(n).toLocaleString();
  function bytes(n) {
    if (n == null || n < 0) return "?";
    const units = ["B", "KB", "MB", "GB", "TB"];
    let i = 0;
    let v = n;
    while (v >= 1024 && i < units.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v : v.toFixed(1)) + " " + units[i];
  }
  // Estimated counts carry "~" so they are never mistaken for exact ones.
  const countText = (stat) => (stat.estimated ? "~" : "") + fmt(stat.rows);
  const statTitle = (stat) => `${stat.estimated ? "Estimated from database statistics · " : ""}${bytes(stat.bytes)}`;
  const drift = (row) => (row.missingColumns?.length || 0) + (row.extraColumns?.length || 0) > 0;
  const missingSide = (row) => row.status === "source_only" || row.status === "target_only";

  function matchesFilter(row) {
    if (state.search && !row.table.toLowerCase().includes(state.search)) return false;
    switch (state.filter) {
      case "differs": return row.status === "differs";
      case "missing": return missingSide(row);
      case "drift": return drift(row);
    }
    return true;
  }

  function render() {
    const r = state.report;
    $("cmp-empty").hidden = true;
    $("cmp-results").hidden = false;
    $("cmp-outcome").hidden = true;
    renderStats(r);
    const counts = { all: r.rows.length, differs: 0, missing: 0, drift: 0 };
    for (const row of r.rows) {
      if (row.status === "differs") counts.differs++;
      if (missingSide(row)) counts.missing++;
      if (drift(row)) counts.drift++;
    }
    app.querySelectorAll("[data-count]").forEach((b) => { b.textContent = counts[b.dataset.count]; });
    renderGauges();
    $("cmp-writes").innerHTML = `Writes only to <strong>${ui().escapeHTML(targetLabel())}</strong>. The source is read.`;
    $("cmp-plan").disabled = false;
  }

  function hasEstimates(r) {
    return r.rows.some((row) => row.source?.estimated || row.target?.estimated);
  }

  function renderStats(r) {
    const t = r.totals;
    const pct = t.sourceRows > 0 ? Math.round((t.targetRows / t.sourceRows) * 100) : null;
    const attention = t.sourceOnly + t.targetOnly + t.columnDrift;
    const tiles = [
      { label: "tables matched", value: fmt(t.same + t.differs), note: `${fmt(t.same)} same · ${fmt(t.differs)} differ` },
      { label: "rows", value: `${fmt(t.sourceRows)} → ${fmt(t.targetRows)}`, note: (pct == null ? "source is empty" : `target holds ${pct}% of source`) + (hasEstimates(r) ? " · ~ = estimated" : ""), meter: pct },
      { label: "size", value: `${bytes(t.sourceBytes)} → ${bytes(t.targetBytes)}`, note: r.source.dbType === "mysql" || r.target.dbType === "mysql" ? "MySQL sizes are cached estimates" : "data + indexes" },
      { label: "needs a look", value: fmt(attention), note: `${t.sourceOnly} source-only · ${t.targetOnly} target-only · ${t.columnDrift} column drift`, warn: attention > 0 },
    ];
    $("cmp-stats").innerHTML = tiles.map((tile, i) => `
      <div class="cmp-stat${tile.warn ? " warn" : ""}" style="--i:${i}">
        <span class="cmp-stat-label">${tile.label}</span>
        <strong class="cmp-stat-value" title="${ui().escapeHTML(tile.value)}">${ui().escapeHTML(tile.value)}</strong>
        ${tile.meter != null ? `<span class="cmp-meter"><i style="width:${Math.min(tile.meter, 100)}%"></i></span>` : ""}
        <span class="cmp-stat-note">${ui().escapeHTML(tile.note)}</span>
      </div>`).join("");
  }

  function renderGauges() {
    const rows = state.report.rows.filter(matchesFilter);
    let max = 1;
    for (const row of state.report.rows) {
      max = Math.max(max, row.source?.rows || 0, row.target?.rows || 0);
    }
    const esc = ui().escapeHTML;
    $("cmp-gauges-empty").hidden = rows.length > 0;
    $("cmp-gauges").innerHTML = rows.map((row, i) => {
      const src = row.source?.rows ?? null;
      const tgt = row.target?.rows ?? null;
      // Square-root scale keeps small tables visible next to huge ones.
      const width = (n) => (n == null || n <= 0 ? 0 : Math.max(2, Math.sqrt(n / max) * 100));
      const mirrorable = row.status === "same" || row.status === "differs";
      const checked = mirrorable && !state.excluded.has(row.table);
      const delta = row.delta || 0;
      const statusText = { same: "same", differs: "differs", source_only: "not on target", target_only: "not on source" }[row.status];
      const driftBadge = drift(row)
        ? `<span class="badge drift" title="Missing on target: ${esc((row.missingColumns || []).join(", ") || "none")}\nExtra on target: ${esc((row.extraColumns || []).join(", ") || "none")}">columns ≠</span>`
        : "";
      return `
        <li class="cmp-gauge status-${row.status}" style="--i:${Math.min(i, 30)}">
          <span class="cmp-g-check"><input type="checkbox" data-table="${esc(row.table)}" ${checked ? "checked" : ""} ${mirrorable ? "" : "disabled"} aria-label="Include ${esc(row.table)} in the mirror"></span>
          <span class="cmp-g-name">
            <code title="${esc(row.table)}">${esc(row.table)}</code>
            <span class="cmp-g-status">${statusText}</span>${driftBadge}
          </span>
          <span class="cmp-g-src">
            <span class="cmp-g-num" title="${src == null ? "" : statTitle(row.source)}">${src == null ? "—" : countText(row.source)}</span>
            <span class="cmp-bar src"><i style="width:${width(src)}%"></i></span>
          </span>
          <span class="cmp-g-tgt">
            <span class="cmp-bar tgt"><i style="width:${width(tgt)}%"></i></span>
            <span class="cmp-g-num" title="${tgt == null ? "" : statTitle(row.target)}">${tgt == null ? "—" : countText(row.target)}</span>
          </span>
          <span class="cmp-g-delta ${delta < 0 ? "neg" : delta > 0 ? "pos" : ""}">${signed(delta)}</span>
        </li>`;
    }).join("");
    syncScope();
  }

  function mirrorTables() {
    const all = state.report.rows.filter((r) => r.status === "same" || r.status === "differs");
    const picked = all.filter((r) => !state.excluded.has(r.table));
    return { all, picked };
  }

  function syncScope() {
    if (!state.report) return;
    const { all, picked } = mirrorTables();
    const checkAll = $("cmp-check-all");
    checkAll.checked = picked.length === all.length && all.length > 0;
    checkAll.indeterminate = picked.length > 0 && picked.length < all.length;
    const scope = $("cmp-scope");
    if (picked.length === all.length) {
      scope.textContent = `All ${all.length} tables present on both sides.`;
    } else {
      scope.textContent = `${picked.length} of ${all.length} tables selected. Required parents are added when they are empty.`;
    }
    $("cmp-plan").disabled = state.busy || picked.length === 0;
  }

  // ── mirror planning & running ───────────────────────────────────────
  function mirrorRequest(dryRun) {
    const { all, picked } = mirrorTables();
    return {
      source: refOf($("cmp-source")),
      target: refOf($("cmp-target")),
      counts: app.querySelector('input[name="counts"]:checked').value,
      mode: app.querySelector('input[name="mode"]:checked').value,
      scale: state.scale,
      maxRows: Number($("cmp-max-rows").value || 0),
      parentRows: Number($("cmp-parent-rows").value || 0),
      tables: picked.length === all.length ? [] : picked.map((r) => r.targetTable || r.table),
      profileId: $("cmp-profile").value,
      batchSize: Number($("cmp-batch").value || 0),
      selfRefDepth: Number($("cmp-selfref").value || 0),
      stopOnError: $("cmp-stop").checked,
      previewRows: 3,
      dryRun,
    };
  }

  async function preview() {
    if (state.busy) return;
    setBusy(true);
    $("cmp-plan").textContent = "Planning…";
    try {
      const job = await runJob("/api/mirror", mirrorRequest(true));
      if (job.status !== "done") throw new Error(job.error || "plan " + job.status);
      state.plan = job.result;
      state.report = job.result.report; // counts are fresh from the plan run
      render();
      openModal(job.result);
    } catch (err) {
      showOutcome("error", "Could not plan the mirror", err.message);
    } finally {
      $("cmp-plan").textContent = "Preview plan";
      setBusy(false);
    }
  }

  function openModal(result) {
    const plan = result.plan;
    const esc = ui().escapeHTML;
    const reset = plan.mode === "reset";
    $("cmp-modal-title").textContent = plan.totalInsert > 0
      ? `Insert ${fmt(plan.totalInsert)} rows into ${plan.entries.length} tables`
      : "The target already matches";
    $("cmp-modal-meta").textContent = `${plan.mode === "reset" ? "Reset" : "Top up"} · ${plan.scale}× source · onto ${result.target} · run ${result.runId}`;

    const truncate = reset && plan.truncate?.length
      ? `<div class="cmp-callout danger"><strong>Truncates ${plan.truncate.length} target tables first</strong><span>${plan.truncate.map(esc).join(", ")}</span></div>`
      : "";
    const issues = (result.issues || []).length
      ? `<div class="cmp-callout"><strong>Profile notes</strong><span>${result.issues.map((i) => esc(`${i.path}: ${i.message}`)).join("<br>")}</span></div>`
      : "";
    const rows = plan.entries.map((e, i) => `
      <tr>
        <td class="num">${i + 1}</td>
        <td><code>${esc(e.table)}</code></td>
        <td class="num">${fmt(e.sourceRows)}</td>
        <td class="num">${fmt(e.targetRows)}</td>
        <td class="num strong">+${fmt(e.insert)}</td>
        <td><span class="reason reason-${esc(e.reason.replace(/\s+/g, "-"))}">${esc(e.reason)}</span></td>
      </tr>`).join("");
    $("cmp-pane-plan").innerHTML = truncate + issues + (plan.entries.length
      ? `<p class="muted small">Tables fill in foreign-key order: parents first.</p>
         <div class="cmp-table-wrap"><table class="cmp-plan-table">
          <thead><tr><th>#</th><th>table</th><th class="num">source</th><th class="num">target</th><th class="num">insert</th><th>why</th></tr></thead>
          <tbody>${rows}</tbody></table></div>`
      : `<p class="empty-hint muted">Nothing to insert. Raise the volume or switch to Reset.</p>`);

    const skipped = plan.skipped || [];
    $("cmp-skip-count").textContent = skipped.length;
    $("cmp-pane-skipped").innerHTML = skipped.length
      ? `<ul class="cmp-skips">${skipped.map((s) => `<li><code>${esc(s.table)}</code><span>${esc(s.reason)}${s.detail ? ` · ${esc(s.detail)}` : ""}</span></li>`).join("")}</ul>`
      : `<p class="empty-hint muted">Every selected table is part of the plan.</p>`;

    if (result.previewError) {
      $("cmp-pane-samples").innerHTML = `<div class="cmp-callout"><strong>Samples unavailable</strong><span>${esc(result.previewError)}</span></div>`;
    } else {
      const preview = result.preview || {};
      $("cmp-pane-samples").innerHTML = plan.order.map((table) => sampleTable(table, preview[table] || [])).join("")
        || `<p class="empty-hint muted">No rows planned.</p>`;
    }

    const confirmWrap = $("cmp-confirm-wrap");
    confirmWrap.hidden = !reset || !plan.truncate?.length;
    $("cmp-confirm").checked = false;
    $("cmp-confirm-text").textContent = `I understand ${plan.truncate?.length || 0} tables on ${result.target} are emptied first.`;
    const exec = $("cmp-execute");
    exec.hidden = plan.totalInsert === 0;
    exec.textContent = `Run mirror · ${fmt(plan.totalInsert)} rows`;
    exec.classList.toggle("danger", reset);
    exec.disabled = reset && !confirmWrap.hidden;
    activateTab("plan");
    $("cmp-modal").hidden = false;
    document.body.classList.add("modal-open");
    exec.hidden ? app.querySelector("#cmp-modal [data-close]").focus() : exec.focus();
  }

  function sampleTable(table, rows) {
    const esc = ui().escapeHTML;
    if (!rows.length) return "";
    // id and *_id first, then alphabetical: keys orient the reader.
    const rank = (c) => (c === "id" ? 0 : c.endsWith("_id") ? 1 : 2);
    const cols = Object.keys(rows[0]).sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
    const cell = (v) => {
      if (v === null || v === undefined) return '<span class="null-pill">NULL</span>';
      const s = typeof v === "object" ? JSON.stringify(v) : String(v);
      return esc(s.length > 60 ? s.slice(0, 57) + "…" : s);
    };
    return `<section class="cmp-sample"><h3><code>${esc(table)}</code></h3>
      <div class="cmp-table-wrap"><table class="preview-table"><thead><tr>${cols.map((c) => `<th>${esc(c)}</th>`).join("")}</tr></thead>
      <tbody>${rows.map((r) => `<tr>${cols.map((c) => `<td>${cell(r[c])}</td>`).join("")}</tr>`).join("")}</tbody></table></div></section>`;
  }

  function activateTab(name) {
    app.querySelectorAll(".cmp-tab").forEach((b) => b.classList.toggle("active", b.dataset.tab === name));
    app.querySelectorAll(".cmp-pane").forEach((p) => { p.hidden = p.dataset.pane !== name; });
  }

  function closeModal() {
    $("cmp-modal").hidden = true;
    document.body.classList.remove("modal-open");
  }

  async function execute() {
    if (state.busy) return;
    closeModal();
    setBusy(true);
    $("cmp-plan").textContent = "Mirroring…";
    showOutcome("running", "Mirror running", "Rows are being inserted into the target. Follow progress in the job log.");
    $("cmp-logs").open = true;
    try {
      const job = await runJob("/api/mirror", mirrorRequest(false));
      const run = job.result?.run || { tables: [], inserted: 0, missing: 0 };
      const problems = (run.tables || []).filter((t) => t.status !== "ok");
      if (job.status !== "done") {
        showOutcome("error", `Mirror ${job.status}`, job.error || "", problems);
      } else if (problems.length) {
        showOutcome("warn", `Inserted ${fmt(run.inserted)} rows · ${fmt(run.missing)} could not be generated`, "Other tables were filled; these need attention:", problems);
      } else {
        showOutcome("ok", `Inserted ${fmt(run.inserted)} rows`, "Every planned table was filled. Counts below are refreshed.");
      }
    } catch (err) {
      showOutcome("error", "Mirror failed to start", err.message);
    } finally {
      $("cmp-plan").textContent = "Preview plan";
      setBusy(false);
    }
    const keep = document.getElementById("cmp-outcome").innerHTML;
    await compare();
    $("cmp-outcome").innerHTML = keep;
    $("cmp-outcome").hidden = false;
  }

  function showOutcome(kind, title, detail, problems) {
    const esc = ui().escapeHTML;
    const box = $("cmp-outcome");
    box.hidden = false;
    box.className = "cmp-outcome " + kind;
    const list = (problems || []).map((p) =>
      `<li><code>${esc(p.table)}</code><span>${esc(p.status)} · ${fmt(p.inserted)}/${fmt(p.requested)}</span><small>${esc(p.error || "")}</small></li>`).join("");
    box.innerHTML = `<strong>${esc(title)}</strong>${detail ? `<p>${esc(detail)}</p>` : ""}${list ? `<ul>${list}</ul>` : ""}`;
  }

  // ── wiring ──────────────────────────────────────────────────────────
  function setScale(value) {
    const n = Number(value);
    if (!(n > 0)) return;
    state.scale = n;
    $("cmp-scale").value = String(n);
    $("cmp-scale-label").textContent = n === 1 ? "1× source" : n < 1 ? `${Math.round(n * 100)}% of source` : `${n}× source`;
    app.querySelectorAll("[data-scale]").forEach((b) => b.classList.toggle("active", Number(b.dataset.scale) === n));
  }

  async function loadProfiles() {
    try {
      const data = await fetch("/api/profiles", { cache: "no-store" }).then((r) => r.json());
      const select = $("cmp-profile");
      for (const p of data.profiles || []) {
        const opt = document.createElement("option");
        opt.value = p.id;
        opt.textContent = p.rules.name;
        select.appendChild(opt);
      }
    } catch (_) { /* optional */ }
  }

  document.addEventListener("DOMContentLoaded", () => {
    $("cmp-form").addEventListener("submit", (ev) => { ev.preventDefault(); compare(); });
    $("cmp-source").addEventListener("change", syncPickers);
    $("cmp-target").addEventListener("change", syncPickers);
    $("cmp-swap").addEventListener("click", () => {
      const s = $("cmp-source").value;
      $("cmp-source").value = $("cmp-target").value;
      $("cmp-target").value = s;
      syncPickers();
      if (state.report) compare();
    });
    $("cmp-filters").addEventListener("click", (ev) => {
      const chip = ev.target.closest("[data-filter]");
      if (!chip) return;
      state.filter = chip.dataset.filter;
      app.querySelectorAll("[data-filter]").forEach((b) => b.classList.toggle("active", b === chip));
      renderGauges();
    });
    $("cmp-search").addEventListener("input", (ev) => { state.search = ev.target.value.trim().toLowerCase(); renderGauges(); });
    $("cmp-gauges").addEventListener("change", (ev) => {
      const table = ev.target.dataset?.table;
      if (!table) return;
      ev.target.checked ? state.excluded.delete(table) : state.excluded.add(table);
      syncScope();
    });
    $("cmp-check-all").addEventListener("change", (ev) => {
      state.excluded.clear();
      if (!ev.target.checked) mirrorTables().all.forEach((r) => state.excluded.add(r.table));
      renderGauges();
    });
    $("cmp-scale-presets").addEventListener("click", (ev) => {
      const chip = ev.target.closest("[data-scale]");
      if (chip) setScale(chip.dataset.scale);
    });
    $("cmp-scale").addEventListener("input", (ev) => setScale(ev.target.value));
    app.querySelectorAll('input[name="mode"]').forEach((r) => r.addEventListener("change", () => {
      $("cmp-mirror").classList.toggle("mode-reset", r.value === "reset" && r.checked);
    }));
    $("cmp-plan").addEventListener("click", preview);
    $("cmp-execute").addEventListener("click", execute);
    $("cmp-confirm").addEventListener("change", (ev) => { $("cmp-execute").disabled = !ev.target.checked; });
    app.querySelectorAll("[data-close]").forEach((el) => el.addEventListener("click", closeModal));
    app.querySelectorAll(".cmp-tab").forEach((b) => b.addEventListener("click", () => activateTab(b.dataset.tab)));
    document.addEventListener("keydown", (ev) => { if (ev.key === "Escape" && !$("cmp-modal").hidden) closeModal(); });
    loadPickers();
    loadProfiles();
  });
})();
