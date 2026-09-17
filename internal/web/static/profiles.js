// Seed profile builder: a generator palette, ordered column-pattern rules, and a
// table explorer that shows what each column will get plus live sample rows.
// The server is the single source of truth for matching (/api/profiles/explain);
// this file only edits the rule document and renders what the server resolves.
(function () {
  "use strict";

  const app = document.getElementById("profiles-app");
  if (!app) return;
  const $ = (id) => document.getElementById(id);
  const ui = () => window.seedstorm.ui;
  const esc = (v) => window.seedstorm.ui.escapeHTML(v == null ? "" : v);

  const KINDS = [
    { kind: "template", label: "Template", hint: "Text with {{tokens}}: {{auto}} keeps the generated value" },
    { kind: "faker", label: "Generator", hint: "One generator from the palette" },
    { kind: "value", label: "Fixed value", hint: "The same value on every row" },
    { kind: "oneOf", label: "One of", hint: "Random pick from a comma-separated list" },
    { kind: "setNull", label: "NULL", hint: "Leave the column empty (nullable columns only)" },
  ];

  const PRESETS = {
    emails: { name: "tag emails", table: "", column: "*email*", template: "seed+{{seq}}.{{run}}@example.test" },
    names: { name: "prefix names", table: "", column: "*name", template: "seed_{{auto}}" },
    notes: { name: "empty notes", table: "", column: "*note*", setNull: true },
  };

  const state = {
    profiles: [],
    id: "",
    doc: blankDoc(),
    dirty: false,
    generators: [],
    tokens: [],
    tables: [],
    table: "",
    explain: null,
    lastTemplate: null, // last focused template input, target for palette clicks
    dragRule: null,
    explainTimer: null,
    explainSeq: 0,
  };

  function blankDoc() {
    return { version: 1, name: "", description: "", rules: [], tables: {}, ignore: [] };
  }

  // ── action helpers ──────────────────────────────────────────────────
  function kindOf(action) {
    if (action.template) return "template";
    if (action.faker) return "faker";
    if (action.value !== undefined && action.value !== null) return "value";
    if (action.oneOf && action.oneOf.length) return "oneOf";
    if (action.setNull) return "setNull";
    return "template";
  }

  function setAction(target, kind, raw) {
    delete target.template; delete target.faker; delete target.value; delete target.oneOf; delete target.setNull;
    switch (kind) {
      case "template": target.template = raw; break;
      case "faker": target.faker = raw; break;
      case "value": target.value = raw; break;
      case "oneOf": target.oneOf = String(raw).split(",").map((s) => s.trim()).filter(Boolean); break;
      case "setNull": target.setNull = true; break;
    }
  }

  function rawOf(action) {
    switch (kindOf(action)) {
      case "template": return action.template || "";
      case "faker": return action.faker || "";
      case "value": return action.value == null ? "" : String(action.value);
      case "oneOf": return (action.oneOf || []).join(", ");
    }
    return "";
  }

  // Renders the kind picker + value input used by both rule cards and the
  // column dialog. onChange(kind, raw) fires on every edit.
  function actionEditor(action, onChange) {
    const wrap = document.createElement("div");
    wrap.className = "pf-action";
    const kind = kindOf(action);
    const select = document.createElement("select");
    select.className = "pf-kind";
    select.setAttribute("aria-label", "Rule type");
    for (const k of KINDS) {
      const opt = document.createElement("option");
      opt.value = k.kind;
      opt.textContent = k.label;
      opt.selected = k.kind === kind;
      select.appendChild(opt);
    }
    const input = document.createElement("input");
    input.className = "pf-value";
    input.type = "text";
    input.spellcheck = false;
    input.autocomplete = "off";
    input.setAttribute("aria-label", "Rule value");
    input.value = rawOf(action);
    const hint = document.createElement("span");
    hint.className = "pf-action-hint";

    const sync = () => {
      const k = select.value;
      const meta = KINDS.find((x) => x.kind === k);
      input.hidden = k === "setNull";
      input.dataset.kind = k;
      input.placeholder = { template: "seed_{{auto}}", faker: "email", value: "active", oneOf: "admin, user, guest" }[k] || "";
      input.setAttribute("list", k === "faker" ? "pf-generator-list" : "");
      hint.textContent = meta.hint;
    };
    select.addEventListener("change", () => {
      sync();
      if (select.value === "faker" && !input.value.trim()) input.value = "email";
      onChange(select.value, input.value);
    });
    input.addEventListener("input", () => onChange(select.value, input.value));
    input.addEventListener("focus", () => { if (select.value === "template") state.lastTemplate = input; });
    enableDrop(input, () => onChange(select.value, input.value), select);
    sync();
    wrap.append(select, input, hint);
    return wrap;
  }

  function insertAtCaret(input, text) {
    const start = input.selectionStart ?? input.value.length;
    const end = input.selectionEnd ?? input.value.length;
    input.value = input.value.slice(0, start) + text + input.value.slice(end);
    const caret = start + text.length;
    input.focus();
    input.setSelectionRange(caret, caret);
  }

  function enableDrop(input, changed, kindSelect) {
    input.addEventListener("dragover", (ev) => {
      if (!ev.dataTransfer.types.includes("text/x-seedstorm-token")) return;
      ev.preventDefault();
      input.classList.add("drop-ready");
    });
    input.addEventListener("dragleave", () => input.classList.remove("drop-ready"));
    input.addEventListener("drop", (ev) => {
      const token = ev.dataTransfer.getData("text/x-seedstorm-token");
      if (!token) return;
      ev.preventDefault();
      input.classList.remove("drop-ready");
      if (kindSelect.value !== "template") {
        // Dropping a generator onto a non-template rule turns it into one.
        kindSelect.value = "template";
        kindSelect.dispatchEvent(new Event("change"));
        input.value = "";
      }
      insertAtCaret(input, `{{${token}}}`);
      changed();
    });
  }

  // ── document edits ──────────────────────────────────────────────────
  function markDirty() {
    state.dirty = true;
    renderStatus();
    scheduleExplain();
  }

  function addRule(rule) {
    state.doc.rules.push(rule || { name: "", table: "", column: "", template: "" });
    renderRules();
    markDirty();
    const cards = $("pf-rule-list").querySelectorAll(".pf-rule");
    const last = cards[cards.length - 1];
    last?.querySelector(rule ? ".pf-value" : ".pf-column-glob")?.focus();
    last?.scrollIntoView({ block: "nearest", behavior: "smooth" });
  }

  function moveRule(from, to) {
    const rules = state.doc.rules;
    if (to < 0 || to >= rules.length || from === to) return;
    const [r] = rules.splice(from, 1);
    rules.splice(to, 0, r);
    renderRules();
    markDirty();
  }

  // ── rendering ───────────────────────────────────────────────────────
  function renderAll() {
    $("pf-name").value = state.doc.name || "";
    $("pf-description").value = state.doc.description || "";
    renderRules();
    renderIgnore();
    renderShapes();
    renderStatus();
    renderSelect();
    scheduleExplain(0);
  }

  // ── ignored tables ──────────────────────────────────────────────────
  // ignoredBy lists the tables each glob matched on the active connection,
  // resolved by the server (the same matcher runs use).
  function ignoredBy() {
    const out = new Map();
    for (const it of state.explain?.ignored || []) {
      if (!out.has(it.pattern)) out.set(it.pattern, []);
      out.get(it.pattern).push(it.table);
    }
    return out;
  }

  function isIgnored(table) {
    return (state.explain?.ignored || []).some((it) => it.table === table);
  }

  function renderIgnore() {
    const globs = state.doc.ignore || [];
    const matches = ignoredBy();
    $("pf-ignore-empty").hidden = globs.length > 0;
    $("pf-ignore-list").innerHTML = globs.map((glob, i) => {
      const tables = matches.get(glob) || [];
      const hits = state.explain
        ? (tables.length ? tables.slice(0, 8).map((t) => `<code data-testid="pf-ignore-hit">${esc(t)}</code>`).join(" ") + (tables.length > 8 ? ` +${tables.length - 8}` : "") : '<span class="pf-warn-text">matches no table on this connection</span>')
        : '<span class="muted">…</span>';
      return `<li class="pf-ignore-item" data-testid="pf-ignore-item">
        <code class="pf-ignore-glob" data-testid="pf-ignore-glob">${esc(glob)}</code>
        <span class="pf-ignore-hits small" data-testid="pf-ignore-hits">${hits}</span>
        <button type="button" class="btn-ghost pf-ignore-remove" data-ignore-index="${i}" aria-label="Stop ignoring ${esc(glob)}">×</button>
      </li>`;
    }).join("");
    $("pf-ignore-tables").innerHTML = state.tables.map((t) => `<option value="${esc(t)}">`).join("");
    const ignored = isIgnored(state.table);
    $("pf-table-ignore").checked = ignored;
    const pattern = (state.explain?.ignored || []).find((it) => it.table === state.table)?.pattern;
    const exact = (state.doc.ignore || []).some((g) => g.toLowerCase() === (state.table || "").toLowerCase());
    // A table ignored by a wider glob can only be un-ignored by editing that glob.
    $("pf-table-ignore").disabled = !state.table || (ignored && !exact);
    const note = $("pf-ignored-note");
    note.hidden = !ignored;
    note.textContent = ignored ? `Ignored by ${pattern}: runs never write ${state.table}, so its column rules below never apply.` : "";
  }

  // ── relationships ───────────────────────────────────────────────────
  const SHAPE_FIELDS = [["min", 1, 1], ["avg", 0.01, 0], ["max", 1, 1], ["zeroShare", 0.01, 0], ["nullShare", 0.01, 0]];
  function renderShapes() {
    const rels = state.doc.relationships || {};
    const keys = Object.keys(rels).sort();
    $("pf-shapes-empty").hidden = keys.length > 0;
    $("pf-shapes-list").innerHTML = keys.map((key) => {
      const r = rels[key];
      const inputs = SHAPE_FIELDS.map(([f, step, minv]) => `<label class="pf-shape-field"><span>${f === "zeroShare" ? "no children" : f === "nullShare" ? "NULL" : f}</span><input type="number" inputmode="decimal" step="${step}" min="${minv}" ${f.endsWith("Share") ? 'max="0.99"' : ""} value="${esc(r[f] ?? "")}" data-shape-key="${esc(key)}" data-shape-field="${f}"></label>`).join("");
      return `<li class="pf-ignore-item pf-shape-item" data-testid="pf-shape-item">
        <code class="pf-ignore-glob">${esc(key)}</code>
        <span class="pf-shape-fields">${inputs}${r.histogram?.length ? `<span class="muted small">${r.histogram.length} buckets</span>` : ""}</span>
        <button type="button" class="btn-ghost pf-ignore-remove" data-shape-remove="${esc(key)}" aria-label="Remove ${esc(key)}">×</button>
      </li>`;
    }).join("");
  }

  async function importShapes(file) {
    if (!file) return;
    const status = $("pf-shapes-status");
    try {
      const data = await file.text();
      const res = await fetch("/api/profiles/relationships", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ data }) });
      const out = await res.json();
      if (!res.ok) throw new Error(out.error || res.statusText);
      state.doc.relationships = { ...(state.doc.relationships || {}), ...out.relationships };
      const n = Object.keys(out.relationships).length;
      status.textContent = `Imported ${n} ${n === 1 ? "relationship" : "relationships"}` + (out.skipped.length ? ` · skipped ${out.skipped.length} without a measured max: ${out.skipped.join(", ")}` : "");
      renderShapes();
      markDirty();
      scheduleExplain(0);
    } catch (err) {
      status.textContent = "Could not import: " + (err.message || err);
    }
  }

  function addIgnore(glob) {
    const value = String(glob || "").trim();
    if (!value) return;
    state.doc.ignore ||= [];
    if (state.doc.ignore.some((g) => g.toLowerCase() === value.toLowerCase())) return;
    state.doc.ignore.push(value);
    renderIgnore();
    markDirty();
  }

  function removeIgnore(index) {
    (state.doc.ignore || []).splice(index, 1);
    renderIgnore();
    markDirty();
  }

  function renderStatus() {
    const el = $("pf-status");
    const saved = state.profiles.find((p) => p.id === state.id);
    if (state.dirty) {
      el.className = "pf-status small dirty";
      el.textContent = saved ? "Unsaved changes" : "Not saved yet";
    } else if (saved) {
      el.className = "pf-status small saved";
      el.textContent = `Saved ${new Date(saved.updatedAt).toLocaleString()} · seedstorm seed --profile ${saved.rules.name}`;
    } else {
      el.className = "pf-status small";
      el.textContent = "";
    }
    $("pf-save").disabled = !state.dirty;
    $("pf-delete").disabled = !state.id;
    $("pf-duplicate").disabled = !state.doc.name;
  }

  function renderSelect() {
    const select = $("pf-select");
    select.innerHTML = "";
    const blank = document.createElement("option");
    blank.value = "";
    blank.textContent = state.id ? "+ new profile" : (state.doc.name ? `${state.doc.name} (unsaved)` : "New profile (unsaved)");
    select.appendChild(blank);
    for (const p of state.profiles) {
      const opt = document.createElement("option");
      opt.value = p.id;
      opt.textContent = p.rules.name;
      select.appendChild(opt);
    }
    select.value = state.id;
  }

  function renderRules() {
    const list = $("pf-rule-list");
    list.innerHTML = "";
    $("pf-rules-empty").hidden = state.doc.rules.length > 0;
    $("pf-presets").hidden = state.doc.rules.length > 2;
    const coverage = state.explain?.counts?.rules || [];
    state.doc.rules.forEach((rule, index) => {
      const li = document.createElement("li");
      li.className = "pf-rule";
      li.dataset.index = index;

      const handle = document.createElement("button");
      handle.type = "button";
      handle.className = "pf-handle";
      handle.draggable = true;
      handle.title = "Drag to reorder (first match wins)";
      handle.setAttribute("aria-label", `Rule ${index + 1}: drag to reorder`);
      handle.innerHTML = `<span class="pf-order">${index + 1}</span><span class="pf-grip" aria-hidden="true">⋮⋮</span>`;
      handle.addEventListener("dragstart", (ev) => {
        state.dragRule = index;
        ev.dataTransfer.effectAllowed = "move";
        ev.dataTransfer.setData("text/x-seedstorm-rule", String(index));
        li.classList.add("dragging");
      });
      handle.addEventListener("dragend", () => li.classList.remove("dragging"));
      li.addEventListener("dragover", (ev) => {
        if (state.dragRule === null || !ev.dataTransfer.types.includes("text/x-seedstorm-rule")) return;
        ev.preventDefault();
        li.classList.add("drop-target");
      });
      li.addEventListener("dragleave", () => li.classList.remove("drop-target"));
      li.addEventListener("drop", (ev) => {
        if (state.dragRule === null) return;
        ev.preventDefault();
        const from = state.dragRule;
        state.dragRule = null;
        moveRule(from, index);
      });

      const match = document.createElement("div");
      match.className = "pf-match";
      match.innerHTML = `
        <label class="pf-glob"><span>table</span><input class="pf-table-glob" type="text" placeholder="*" spellcheck="false" autocomplete="off"></label>
        <span class="pf-dot" aria-hidden="true">.</span>
        <label class="pf-glob"><span>column</span><input class="pf-column-glob" type="text" placeholder="*email*" spellcheck="false" autocomplete="off" required></label>`;
      const tableInput = match.querySelector(".pf-table-glob");
      const columnInput = match.querySelector(".pf-column-glob");
      tableInput.value = rule.table || "";
      columnInput.value = rule.column || "";
      tableInput.addEventListener("input", () => { rule.table = tableInput.value.trim(); markDirty(); });
      columnInput.addEventListener("input", () => { rule.column = columnInput.value.trim(); markDirty(); });

      const editor = actionEditor(rule, (kind, raw) => { setAction(rule, kind, raw); markDirty(); });

      const side = document.createElement("div");
      side.className = "pf-rule-side";
      const hits = coverage[index];
      const badge = document.createElement("span");
      badge.className = "pf-coverage" + (hits === 0 ? " none" : "");
      badge.textContent = hits == null ? "…" : hits === 1 ? "1 column" : `${hits} columns`;
      badge.title = hits === 0 ? "Matches no column on this connection (check the globs or rule order)" : "Columns this rule rewrites on the active connection";
      const up = iconButton("↑", "Move up", () => moveRule(index, index - 1));
      const down = iconButton("↓", "Move down", () => moveRule(index, index + 1));
      up.disabled = index === 0;
      down.disabled = index === state.doc.rules.length - 1;
      const remove = iconButton("✕", "Remove rule", () => {
        state.doc.rules.splice(index, 1);
        renderRules();
        markDirty();
      });
      remove.classList.add("pf-remove");
      side.append(badge, up, down, remove);

      const example = document.createElement("div");
      example.className = "pf-example";
      example.setAttribute("aria-live", "polite");
      li.append(handle, match, editor, side, example);
      list.appendChild(li);
    });
  }

  function iconButton(text, label, onClick) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn-ghost pf-icon";
    b.textContent = text;
    b.title = label;
    b.setAttribute("aria-label", label);
    b.addEventListener("click", onClick);
    return b;
  }

  function renderPalette() {
    const body = $("pf-palette-body");
    const q = $("pf-gen-search").value.trim().toLowerCase();
    const groups = [{ title: "Tokens", items: state.tokens.map((t) => ({ token: t.token, label: `{{${t.token}}}`, sample: t.description, builtin: true })) }];
    const byCat = {};
    for (const g of state.generators) {
      (byCat[g.category] ||= []).push({ token: g.expr, label: g.expr, sample: g.sample, description: g.description });
    }
    Object.keys(byCat).sort().forEach((cat) => groups.push({ title: cat, items: byCat[cat] }));
    body.innerHTML = "";
    let shown = 0;
    for (const group of groups) {
      const items = group.items.filter((i) => !q || `${i.label} ${i.sample} ${i.description || ""} ${group.title}`.toLowerCase().includes(q));
      if (!items.length) continue;
      const section = document.createElement("section");
      section.className = "pf-gen-group";
      section.innerHTML = `<h3>${esc(group.title)}</h3>`;
      const chips = document.createElement("div");
      chips.className = "pf-gen-chips";
      for (const item of items) {
        shown++;
        const chip = document.createElement("button");
        chip.type = "button";
        chip.className = "pf-gen" + (item.builtin ? " builtin" : "");
        chip.draggable = true;
        chip.title = item.description ? `${item.description} · e.g. ${item.sample}` : item.sample;
        chip.innerHTML = `<code>${esc(item.label)}</code><span>${esc(item.builtin ? item.sample : item.sample)}</span>`;
        chip.addEventListener("dragstart", (ev) => {
          ev.dataTransfer.setData("text/x-seedstorm-token", item.token);
          ev.dataTransfer.setData("text/plain", `{{${item.token}}}`);
          ev.dataTransfer.effectAllowed = "copy";
        });
        chip.addEventListener("click", () => insertToken(item.token));
        chips.appendChild(chip);
      }
      section.appendChild(chips);
      body.appendChild(section);
    }
    if (!shown) body.innerHTML = `<p class="muted small empty-hint">No generator matches “${esc(q)}”.</p>`;

    let list = $("pf-generator-list");
    if (!list) {
      list = document.createElement("datalist");
      list.id = "pf-generator-list";
      document.body.appendChild(list);
    }
    list.innerHTML = state.generators.map((g) => `<option value="${esc(g.expr)}">${esc(g.description)}</option>`).join("");
  }

  // A palette click inserts into the last template the user touched; without
  // one it starts a new template rule so the click is never lost.
  function insertToken(token) {
    const input = state.lastTemplate;
    if (input && document.body.contains(input) && input.dataset.kind === "template") {
      insertAtCaret(input, `{{${token}}}`);
      input.dispatchEvent(new Event("input"));
      flash(input);
      return;
    }
    addRule({ name: "", table: "", column: "", template: `{{${token}}}` });
    const cards = $("pf-rule-list").querySelectorAll(".pf-rule");
    const card = cards[cards.length - 1];
    card?.querySelector(".pf-column-glob")?.focus();
    setStatusNote("Added a rule with that generator. Now choose which columns it applies to.");
  }

  function flash(el) {
    el.classList.remove("flash-insert");
    void el.offsetWidth;
    el.classList.add("flash-insert");
  }

  function setStatusNote(text) {
    const el = $("pf-status");
    el.className = "pf-status small note";
    el.textContent = text;
    setTimeout(renderStatus, 3500);
  }

  // ── explain (server-side resolution + samples) ──────────────────────
  function scheduleExplain(delay) {
    clearTimeout(state.explainTimer);
    state.explainTimer = setTimeout(explain, delay ?? 350);
  }

  async function explain() {
    const seq = ++state.explainSeq;
    const { doc, sentFrom } = cleanDoc();
    const body = { rules: doc, table: state.table, rows: 5 };
    let data;
    try {
      const res = await fetch("/api/profiles/explain", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) });
      data = await res.json();
      if (!res.ok) throw new Error(data.error || res.statusText);
    } catch (err) {
      if (seq === state.explainSeq) $("pf-samples-body").innerHTML = `<p class="pf-error small">Preview failed: ${esc(err.message)}</p>`;
      return;
    }
    if (seq !== state.explainSeq) return; // a newer edit is already on its way
    // Remap per-rule results from "rules sent" to "cards in the editor".
    const coverage = [];
    (data.counts?.rules || []).forEach((n, i) => { coverage[sentFrom[i]] = n; });
    data.counts = { ...(data.counts || {}), rules: coverage };
    const examples = [];
    (data.examples || []).forEach((ex, i) => { examples[sentFrom[i]] = ex; });
    data.examples = examples;
    data.issues = (data.issues || []).map((issue) => {
      const m = /^rules\[(\d+)\]$/.exec(issue.path);
      return m ? { ...issue, path: `rules[${sentFrom[Number(m[1])]}]` } : issue;
    });
    state.explain = data;
    if (!state.tables.length && data.tables?.length) {
      state.tables = data.tables;
      renderTables();
      if (!state.table) {
        state.table = pickDefaultTable(data.tables);
        $("pf-table").value = state.table;
        scheduleExplain(0);
      }
    }
    renderCoverage();
    renderIssues(data.issues || []);
    renderColumns(data);
    renderSamples(data);
    renderIgnore();
    renderTables();
  }

  function pickDefaultTable(tables) {
    return tables.find((t) => /^users?$/i.test(t)) || tables.find((t) => /user|customer|account/i.test(t)) || tables[0] || "";
  }

  // Coverage badges and examples update in place so typing in a card never
  // loses focus.
  function renderCoverage() {
    const coverage = state.explain?.counts?.rules || [];
    const examples = state.explain?.examples || [];
    $("pf-rule-list").querySelectorAll(".pf-rule").forEach((li) => {
      const index = Number(li.dataset.index);
      const hits = coverage[index];
      const badge = li.querySelector(".pf-coverage");
      if (badge) {
        badge.textContent = hits == null ? "…" : hits === 1 ? "1 column" : `${hits} columns`;
        badge.classList.toggle("none", hits === 0);
      }
      const box = li.querySelector(".pf-example");
      if (!box) return;
      const ex = examples[index];
      if (!ex || !ex.column) {
        box.innerHTML = hits === 0 ? '<span class="muted">No column on this connection matches yet.</span>' : "";
        return;
      }
      box.innerHTML = ex.error
        ? `<span class="pf-error">${esc(ex.table)}.${esc(ex.column)}: ${esc(ex.error)}</span>`
        : `<span class="muted">e.g. <code>${esc(ex.table)}.${esc(ex.column)}</code></span>${ex.values.map((v) => `<code class="pf-example-value" title="${esc(v)}">${esc(v)}</code>`).join("")}`;
    });
  }

  function renderIssues(issues) {
    const box = $("pf-issues");
    box.hidden = issues.length === 0;
    const errors = issues.filter((i) => i.severity === "error").length;
    box.className = "pf-issues" + (errors ? " has-errors" : "");
    box.innerHTML = `<strong>${errors ? `${errors} error${errors > 1 ? "s" : ""} to fix before saving` : "Heads up"}</strong><ul>${issues.map((i) =>
      `<li class="${esc(i.severity)}"><code>${esc(humanPath(i.path))}</code> ${esc(i.message)}</li>`).join("")}</ul>`;
  }

  function humanPath(path) {
    const m = /^rules\[(\d+)\]$/.exec(path);
    if (m) return `rule ${Number(m[1]) + 1}`;
    return path.replace(/^tables\./, "").replace(".columns.", ".");
  }

  function renderTables() {
    const select = $("pf-table");
    select.innerHTML = state.tables.map((t) => {
      const rules = state.doc.tables?.[t];
      let marks = rules && (rules.rows || Object.keys(rules.columns || {}).length) ? " · customized" : "";
      if (isIgnored(t)) marks += " · ignored";
      return `<option value="${esc(t)}">${esc(t)}${marks}</option>`;
    }).join("");
    select.value = state.table;
  }

  function renderColumns(data) {
    const body = $("pf-columns");
    const rows = $("pf-table-rows");
    rows.value = state.doc.tables?.[state.table]?.rows || "";
    if (!data.columns) {
      body.innerHTML = "";
      return;
    }
    body.innerHTML = data.columns.map((c) => {
      const flags = [
        c.pk ? '<span class="badge pk">PK</span>' : "",
        c.fk ? `<span class="badge fk" title="references ${esc(c.fk)}">FK</span>` : "",
        c.unique ? '<span class="badge unique">UNIQUE</span>' : "",
        c.nullable ? '<span class="badge nullable">NULL</span>' : "",
      ].join("");
      const source = c.protected
        ? `<span class="pf-src protected" title="${esc(c.protected)}">🔒 kept</span>`
        : `<span class="pf-src ${esc(c.source.kind)}">${esc(c.source.kind === "default" ? "automatic" : c.source.kind === "column" ? "column rule" : c.source.label)}</span>`;
      const skipped = (c.skipped || []).map((s) => `${s.label}: ${s.reason}`).join("\n");
      const action = c.protected
        ? ""
        : `<button type="button" class="btn-ghost pf-col-edit" data-column="${esc(c.column)}">${c.source.kind === "column" ? "Edit" : "Set"}</button>`;
      // The automatic generator only matters to the reader when a rule replaces it.
      const replaced = c.source.kind !== "default" && !c.protected
        ? `<div class="pf-default">was <code>${esc(c.default || "db default")}</code></div>` : "";
      return `<tr class="${c.protected ? "is-protected" : ""} src-${esc(c.source.kind)}">
        <td><code class="pf-col-name">${esc(c.column)}</code><div class="pf-col-type">${esc(c.type)} ${flags}</div></td>
        <td>${source}${replaced}<div class="pf-effective" title="${esc(c.effective)}">${esc(c.protected ? "" : c.effective)}</div>${skipped ? `<div class="pf-skipped" title="${esc(skipped)}">${(c.skipped || []).length} rule(s) skipped here</div>` : ""}</td>
        <td class="pf-col-actions">${action}</td>
      </tr>`;
    }).join("");
  }

  function renderSamples(data) {
    const box = $("pf-samples-body");
    if (!state.table) {
      box.innerHTML = '<p class="muted small empty-hint">Pick a table to preview.</p>';
      return;
    }
    if (data.sampleError) {
      box.innerHTML = `<p class="pf-error small">${esc(data.sampleError)}</p>`;
      return;
    }
    const rows = data.samples || [];
    if (!rows.length) {
      box.innerHTML = '<p class="muted small empty-hint">No sample rows.</p>';
      return;
    }
    const ruled = new Set((data.columns || []).filter((c) => c.source.kind !== "default" && !c.protected).map((c) => c.column));
    const rank = (c) => (c === "id" ? 0 : c.endsWith("_id") ? 1 : ruled.has(c) ? 2 : 3);
    const cols = Object.keys(rows[0]).sort((a, b) => rank(a) - rank(b) || a.localeCompare(b));
    const cell = (v) => (v === "NULL" ? '<span class="null-pill">NULL</span>' : esc(v.length > 48 ? v.slice(0, 45) + "…" : v));
    box.innerHTML = `<div class="pf-table-wrap"><table class="preview-table"><thead><tr>${cols.map((c) =>
      `<th class="${ruled.has(c) ? "ruled" : ""}">${esc(c)}</th>`).join("")}</tr></thead><tbody>${rows.map((r) =>
      `<tr>${cols.map((c) => `<td class="${ruled.has(c) ? "ruled" : ""}" title="${esc(r[c])}">${cell(r[c])}</td>`).join("")}</tr>`).join("")}</tbody></table></div>`;
  }

  // ── column rule dialog ──────────────────────────────────────────────
  function openColumnDialog(column) {
    const plan = (state.explain?.columns || []).find((c) => c.column === column);
    if (!plan) return;
    const existing = state.doc.tables?.[state.table]?.columns?.[column];
    const draft = existing ? { ...existing } : (plan.action ? { ...plan.action } : { template: "{{auto}}" });
    $("pf-column-title").textContent = `${state.table}.${column}`;
    $("pf-column-meta").textContent = `${plan.type}${plan.nullable ? " · nullable" : " · NOT NULL"}${plan.unique ? " · UNIQUE" : ""} · default ${plan.default || "db default"}`;
    const editorBox = $("pf-column-editor");
    editorBox.innerHTML = "";
    const preview = document.createElement("div");
    preview.className = "pf-example pf-dialog-example";
    preview.setAttribute("aria-live", "polite");
    let timer = null;
    const refresh = () => {
      clearTimeout(timer);
      timer = setTimeout(() => previewColumnAction(draft, column, preview), 200);
    };
    editorBox.appendChild(actionEditor(draft, (kind, raw) => { setAction(draft, kind, raw); refresh(); }));
    editorBox.appendChild(preview);
    previewColumnAction(draft, column, preview);
    $("pf-column-clear").hidden = !existing;
    const dialog = $("pf-column-dialog");
    dialog.dataset.column = column;
    dialog._draft = draft;
    dialog.showModal();
    editorBox.querySelector(".pf-value:not([hidden])")?.focus();
  }

  async function previewColumnAction(action, column, box) {
    try {
      const res = await fetch("/api/profiles/example", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ action, table: state.table, column }) });
      const data = await res.json();
      if (data.error) {
        box.innerHTML = `<span class="pf-error">${esc(data.error)}</span>`;
        $("pf-column-apply").disabled = true;
        return;
      }
      $("pf-column-apply").disabled = false;
      box.innerHTML = `<span class="muted">writes</span>${(data.values || []).map((v) => `<code class="pf-example-value">${esc(v)}</code>`).join("")}`;
    } catch (_) { /* preview is best effort */ }
  }

  function applyColumnRule(column, draft) {
    const tables = (state.doc.tables ||= {});
    const t = (tables[state.table] ||= {});
    const cols = (t.columns ||= {});
    if (draft) {
      cols[column] = draft;
    } else {
      delete cols[column];
      if (!Object.keys(cols).length) delete t.columns;
      if (!t.columns && !t.rows) delete tables[state.table];
    }
    renderTables();
    markDirty();
  }

  // ── persistence ─────────────────────────────────────────────────────
  // cleanDoc drops half-typed rules the server would reject, so live previews
  // keep working while a rule is being written. Saving sends the full doc.
  // It also returns which editor index each sent rule came from, so coverage
  // and issue paths map back onto the right card.
  function cleanDoc() {
    const doc = JSON.parse(JSON.stringify(state.doc));
    const sentFrom = [];
    doc.ignore = (doc.ignore || []).map((g) => String(g).trim()).filter(Boolean);
    doc.rules = doc.rules.filter((r, i) => {
      const complete = r.column && (r.setNull || rawOf(r) !== "" || kindOf(r) === "value");
      if (complete) sentFrom.push(i);
      return complete;
    });
    return { doc, sentFrom };
  }

  async function loadProfiles() {
    const data = await fetch("/api/profiles", { cache: "no-store" }).then((r) => r.json()).catch(() => ({ profiles: [] }));
    state.profiles = data.profiles || [];
  }

  function openProfile(id) {
    const p = state.profiles.find((x) => x.id === id);
    state.id = p ? p.id : "";
    state.doc = p ? JSON.parse(JSON.stringify(p.rules)) : blankDoc();
    state.doc.rules ||= [];
    state.doc.tables ||= {};
    state.doc.ignore ||= [];
    state.dirty = false;
    state.lastTemplate = null;
    const url = new URL(location.href);
    p ? url.searchParams.set("id", p.id) : url.searchParams.delete("id");
    history.replaceState(null, "", url);
    renderTables();
    renderAll();
  }

  function confirmDiscard() {
    return !state.dirty || window.confirm("Discard unsaved changes to this profile?");
  }

  async function save() {
    state.doc.name = $("pf-name").value.trim();
    state.doc.description = $("pf-description").value.trim();
    if (!state.doc.name) {
      $("pf-name").focus();
      setStatusNote("Give the profile a name first; the CLI uses it with --profile.");
      return;
    }
    let data;
    try {
      data = await ui().fetchJSON("/api/profiles", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ id: state.id, rules: state.doc }) });
    } catch (err) {
      const el = $("pf-status");
      el.className = "pf-status small error";
      el.textContent = "Not saved: " + (err.message || err);
      return;
    }
    await loadProfiles();
    state.id = data.id;
    state.dirty = false;
    openProfile(data.id);
  }

  async function remove() {
    const p = state.profiles.find((x) => x.id === state.id);
    if (!p || !window.confirm(`Delete profile “${p.rules.name}”? Runs that reference it by name will stop working.`)) return;
    try {
      await ui().fetchJSON("/api/profiles?id=" + encodeURIComponent(p.id), { method: "DELETE" });
    } catch (err) {
      const el = $("pf-status");
      el.className = "pf-status small error";
      el.textContent = "Not deleted: " + (err.message || err);
      return;
    }
    await loadProfiles();
    openProfile(state.profiles[0]?.id || "");
  }

  async function openYAML(mode) {
    const dialog = $("pf-yaml-dialog");
    const text = $("pf-yaml-text");
    $("pf-yaml-error").hidden = true;
    dialog.dataset.mode = mode;
    if (mode === "export") {
      let data;
      try {
        data = await ui().fetchJSON("/api/profiles/yaml", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ rules: state.doc }) });
      } catch (err) {
        data = {};
        $("pf-yaml-error").hidden = false;
        $("pf-yaml-error").textContent = "Could not render the YAML: " + (err.message || err);
      }
      text.value = data.yaml || "";
      text.readOnly = true;
      $("pf-yaml-eyebrow").textContent = "export";
      $("pf-yaml-title").textContent = state.doc.name ? `${state.doc.name}.yaml` : "profile.yaml";
      $("pf-yaml-hint").textContent = `Save as a file and run: seedstorm seed --profile ${state.doc.name || "profile"}.yaml  (or by name once saved)`;
      $("pf-yaml-load").hidden = true;
      $("pf-yaml-copy").hidden = false;
      $("pf-yaml-file-wrap").hidden = true;
      const link = $("pf-yaml-download");
      link.hidden = false;
      if (link.dataset.url) URL.revokeObjectURL(link.dataset.url);
      link.dataset.url = URL.createObjectURL(new Blob([text.value], { type: "text/yaml" }));
      link.href = link.dataset.url;
      link.download = profileFilename(state.doc.name);
    } else {
      text.value = "";
      text.readOnly = false;
      text.placeholder = "name: loadtest\nrules:\n  - column: \"*email*\"\n    template: \"lt+{{seq}}@example.test\"";
      $("pf-yaml-eyebrow").textContent = "import";
      $("pf-yaml-title").textContent = "Paste a profile";
      $("pf-yaml-hint").textContent = "Loads into the builder so you can review it before saving.";
      $("pf-yaml-load").hidden = false;
      $("pf-yaml-copy").hidden = true;
      $("pf-yaml-download").hidden = true;
      $("pf-yaml-file-wrap").hidden = false;
      $("pf-yaml-file").value = "";
    }
    dialog.showModal();
    if (mode !== "export") text.focus();
  }

  // profileFilename turns "Load test (EU)" into "load-test-eu.yaml".
  function profileFilename(name) {
    const base = String(name || "profile").toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "");
    return (base || "profile") + ".yaml";
  }

  // A chosen or dropped file loads straight into the builder; errors show in
  // the dialog with the file's text left in place to fix.
  async function importProfileFile(file) {
    if (!file) return;
    if (file.size > 1024 * 1024) {
      $("pf-yaml-error").hidden = false;
      $("pf-yaml-error").textContent = "That file is larger than 1MB; a profile is usually a few KB.";
      return;
    }
    $("pf-yaml-text").value = await file.text();
    await loadYAML();
  }

  async function loadYAML() {
    const err = $("pf-yaml-error");
    let data;
    try {
      data = await ui().fetchJSON("/api/profiles/yaml", { method: "POST", headers: { "Content-Type": "application/json" }, body: JSON.stringify({ yaml: $("pf-yaml-text").value }) });
    } catch (e) {
      err.hidden = false;
      err.textContent = e.message || String(e);
      return;
    }
    const existing = state.profiles.find((p) => p.rules.name && p.rules.name.toLowerCase() === String(data.rules.name || "").toLowerCase());
    state.id = existing ? existing.id : "";
    state.doc = data.rules;
    state.doc.rules ||= [];
    state.doc.tables ||= {};
    state.doc.ignore ||= [];
    $("pf-yaml-dialog").close();
    renderTables();
    renderAll();
    markDirty();
    setStatusNote(existing ? `Loaded over “${existing.rules.name}”. Save to replace it.` : "Loaded. Review and save.");
  }

  // ── wiring ──────────────────────────────────────────────────────────
  document.addEventListener("DOMContentLoaded", async () => {
    const [gens] = await Promise.all([
      fetch("/api/generators").then((r) => r.json()).catch(() => ({ generators: [], tokens: [] })),
      loadProfiles(),
    ]);
    state.generators = gens.generators || [];
    state.tokens = gens.tokens || [];
    renderPalette();
    // Below the three-column layout the palette sits under the work: start it
    // folded so it does not push the page down.
    if (window.matchMedia("(max-width: 1320px)").matches) $("pf-palette").querySelector("details").open = false;
    const wanted = new URLSearchParams(location.search).get("id");
    openProfile(state.profiles.some((p) => p.id === wanted) ? wanted : (state.profiles[0]?.id || ""));

    $("pf-gen-search").addEventListener("input", renderPalette);
    $("pf-name").addEventListener("input", () => {
      state.doc.name = $("pf-name").value;
      if (!state.id) renderSelect();
      markDirty();
    });
    $("pf-description").addEventListener("input", () => { state.doc.description = $("pf-description").value; state.dirty = true; renderStatus(); });
    $("pf-add-rule").addEventListener("click", () => addRule());
    $("pf-presets").addEventListener("click", (ev) => {
      const chip = ev.target.closest("[data-preset]");
      if (chip) addRule({ ...PRESETS[chip.dataset.preset] });
    });
    $("pf-save").addEventListener("click", save);
    $("pf-delete").addEventListener("click", remove);
    $("pf-new").addEventListener("click", () => { if (confirmDiscard()) openProfile(""); });
    $("pf-duplicate").addEventListener("click", () => {
      const copy = JSON.parse(JSON.stringify(state.doc));
      copy.name = `${copy.name || "profile"} copy`;
      state.id = "";
      state.doc = copy;
      renderAll();
      markDirty();
      $("pf-name").select();
    });
    $("pf-select").addEventListener("change", (ev) => {
      if (!confirmDiscard()) { ev.target.value = state.id; return; }
      openProfile(ev.target.value);
    });
    $("pf-import").addEventListener("click", () => openYAML("import"));
    $("pf-export").addEventListener("click", () => openYAML("export"));
    $("pf-yaml-load").addEventListener("click", loadYAML);
    $("pf-yaml-file").addEventListener("change", (ev) => importProfileFile(ev.target.files?.[0]));
    const fileWrap = $("pf-yaml-file-wrap");
    fileWrap.addEventListener("dragover", (ev) => { ev.preventDefault(); fileWrap.classList.add("dragging"); });
    fileWrap.addEventListener("dragleave", () => fileWrap.classList.remove("dragging"));
    fileWrap.addEventListener("drop", (ev) => {
      ev.preventDefault();
      fileWrap.classList.remove("dragging");
      importProfileFile(ev.dataTransfer?.files?.[0]);
    });
    $("pf-yaml-copy").addEventListener("click", async () => {
      await window.seedstorm.ui.copyText($("pf-yaml-text").value);
      $("pf-yaml-copy").textContent = "Copied";
      setTimeout(() => { $("pf-yaml-copy").textContent = "Copy"; }, 1500);
    });
    $("pf-table").addEventListener("change", (ev) => { state.table = ev.target.value; scheduleExplain(0); });
    $("pf-table-rows").addEventListener("input", (ev) => {
      const n = Number(ev.target.value || 0);
      const tables = (state.doc.tables ||= {});
      if (n > 0) (tables[state.table] ||= {}).rows = n;
      else if (tables[state.table]) {
        delete tables[state.table].rows;
        if (!tables[state.table].columns) delete tables[state.table];
      }
      renderTables();
      markDirty();
    });
    $("pf-resample").addEventListener("click", () => scheduleExplain(0));
    $("pf-ignore-form").addEventListener("submit", (ev) => {
      ev.preventDefault();
      addIgnore($("pf-ignore-input").value);
      $("pf-ignore-input").value = "";
    });
    $("pf-ignore-list").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-ignore-index]");
      if (btn) removeIgnore(Number(btn.dataset.ignoreIndex));
    });
    $("pf-shapes-file").addEventListener("change", (ev) => { importShapes(ev.target.files?.[0]); ev.target.value = ""; });
    $("pf-shapes-list").addEventListener("click", (ev) => {
      const btn = ev.target.closest("[data-shape-remove]");
      if (!btn) return;
      delete state.doc.relationships[btn.dataset.shapeRemove];
      if (!Object.keys(state.doc.relationships).length) delete state.doc.relationships;
      renderShapes();
      markDirty();
    });
    $("pf-shapes-list").addEventListener("change", (ev) => {
      const input = ev.target.closest("[data-shape-field]");
      if (!input) return;
      const rel = state.doc.relationships?.[input.dataset.shapeKey];
      if (!rel) return;
      const n = Number(input.value);
      if (input.value === "" || !Number.isFinite(n)) delete rel[input.dataset.shapeField];
      else rel[input.dataset.shapeField] = input.dataset.shapeField === "min" || input.dataset.shapeField === "max" ? Math.round(n) : n;
      markDirty();
    });
    $("pf-table-ignore").addEventListener("change", (ev) => {
      if (!state.table) return;
      if (ev.target.checked) {
        addIgnore(state.table);
        return;
      }
      const i = (state.doc.ignore || []).findIndex((g) => g.toLowerCase() === state.table.toLowerCase());
      if (i >= 0) removeIgnore(i);
    });
    $("pf-columns").addEventListener("click", (ev) => {
      const btn = ev.target.closest(".pf-col-edit");
      if (btn) openColumnDialog(btn.dataset.column);
    });
    $("pf-column-form").addEventListener("submit", (ev) => {
      const dialog = $("pf-column-dialog");
      if (ev.submitter?.value === "apply") applyColumnRule(dialog.dataset.column, dialog._draft);
    });
    $("pf-column-clear").addEventListener("click", () => {
      const dialog = $("pf-column-dialog");
      applyColumnRule(dialog.dataset.column, null);
      dialog.close();
    });
    window.addEventListener("beforeunload", (ev) => {
      if (state.dirty) { ev.preventDefault(); ev.returnValue = ""; }
    });
  });

  window.seedstormProfiles = { state };
})();
