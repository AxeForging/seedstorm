// seedstorm UI glue: connection presets, simple form runners, and the
// unified workspace (interactive graph + side panel + live job stream).
(function () {
  "use strict";

  const LEGACY_PRESET_KEY = "seedstorm.connections.v1";
  const PRESET_MIGRATED_KEY = "seedstorm.presetsMigrated.v1";
  const GENERATED_DRAFT_KEY = "seedstorm.generatedData.v1";
  const GRAPH_ROUTE_KEY = "seedstorm.graphRoute.v1";

  const apiHeaders = { "Content-Type": "application/json", "X-Seedstorm-Request": "1" };

  // ── saved connections (server-side, ~/.config/seedstorm/connections.yaml) ──
  async function fetchSavedConnections() {
    try {
      const res = await fetch("/api/saved-connections");
      if (!res.ok) return [];
      const conns = await res.json();
      return Array.isArray(conns) ? conns : [];
    } catch (_) {
      return [];
    }
  }

  async function deleteSavedConnection(id) {
    const res = await fetch("/api/saved-connections?id=" + encodeURIComponent(id), {
      method: "DELETE",
      headers: apiHeaders,
    });
    if (!res.ok) {
      const j = await res.json().catch(() => ({}));
      throw new Error(j.error || res.statusText);
    }
  }

  // Connections used to live in this browser's localStorage. Move them into the
  // server store once so they survive a browser change, and keep the originals
  // so an older build still finds them.
  async function migrateLegacyPresets() {
    let raw;
    try {
      if (localStorage.getItem(PRESET_MIGRATED_KEY)) return;
      raw = localStorage.getItem(LEGACY_PRESET_KEY);
    } catch (_) { return; }
    if (!raw) return;
    let presets;
    try { presets = JSON.parse(raw) || {}; } catch (_) { return; }
    const connections = Object.keys(presets).map((name) => {
      const p = presets[name] || {};
      return {
        label: name,
        dbType: p.dbType || "postgres",
        host: p.dsn ? "" : (p.host || ""),
        port: p.dsn ? 0 : Number(p.port || 0) || 0,
        dbName: p.dsn ? "" : (p.dbName || ""),
        user: p.dsn ? "" : (p.user || ""),
        ssl: p.ssl || "",
        dsn: p.dsn || "",
        password: p.password || "",
      };
    });
    if (!connections.length) return;
    try {
      await fetch("/api/saved-connections/import", {
        method: "POST",
        headers: apiHeaders,
        body: JSON.stringify({ connections }),
      });
      localStorage.setItem(PRESET_MIGRATED_KEY, String(Date.now()));
    } catch (_) { /* try again next load */ }
  }

  function connectionKey(info) {
    info = info || {};
    const dbType = String(info.dbType || "").toLowerCase();
    const dsn = String(info.dsn || "").trim();
    if (dsn && !info.host && !info.dbName && !info.user) return `dsn:${dbType}:${dsn}`;
    return [
      dbType,
      String(info.host || "").toLowerCase(),
      String(info.port || ""),
      String(info.dbName || ""),
      String(info.user || ""),
    ].join("|");
  }

  function connectionLabel(info) {
    info = info || {};
    const name = info.label || info.dbName || "database";
    const host = info.host || "connection";
    const port = info.port ? `:${info.port}` : "";
    return `${name} @ ${host}${port}`;
  }

  async function fetchConnections() {
    try {
      const res = await fetch("/api/connections");
      if (!res.ok) return [];
      const conns = await res.json();
      return Array.isArray(conns) ? conns : [];
    } catch (_) {
      return [];
    }
  }

  // ── connect form: driver parameters, testing, saving ───────────────────
  let paramCatalog = { driver: "postgres", known: [], suggestions: [], translations: {}, unknownNote: "" };

  function paramRow(name, value) {
    const row = document.createElement("div");
    row.className = "param-row";
    row.innerHTML =
      '<input name="paramName" list="param-names" placeholder="name" autocomplete="off">' +
      '<input name="paramValue" placeholder="value" autocomplete="off">' +
      '<button type="button" class="btn-ghost param-remove" aria-label="Remove parameter">\u00d7</button>';
    row.querySelector('[name="paramName"]').value = name || "";
    row.querySelector('[name="paramValue"]').value = value || "";
    return row;
  }

  function addParamRow(name, value, focus) {
    const rows = document.getElementById("param-rows");
    if (!rows) return null;
    const existing = [...rows.querySelectorAll('[name="paramName"]')]
      .find((el) => el.value.trim().toLowerCase() === String(name || "").toLowerCase());
    if (name && existing) {
      const row = existing.closest(".param-row");
      row.querySelector('[name="paramValue"]').value = value || "";
      annotateParamRow(row);
      if (focus) row.querySelector('[name="paramValue"]').focus();
      return row;
    }
    const row = paramRow(name, value);
    rows.appendChild(row);
    annotateParamRow(row);
    if (focus) row.querySelector(name ? '[name="paramValue"]' : '[name="paramName"]').focus();
    return row;
  }

  function removeParamRow(name) {
    const rows = document.getElementById("param-rows");
    if (!rows) return;
    [...rows.querySelectorAll('[name="paramName"]')]
      .filter((el) => el.value.trim().toLowerCase() === String(name || "").toLowerCase())
      .forEach((el) => el.closest(".param-row").remove());
  }

  // annotateParamRow mirrors the server's validation so the form can flag a
  // parameter as you type. The server re-checks on test and connect.
  function annotateParamRow(row) {
    if (!row) return;
    const nameEl = row.querySelector('[name="paramName"]');
    const name = (nameEl?.value || "").trim();
    row.querySelector(".param-note")?.remove();
    row.classList.remove("warn", "err");
    if (!name) return;
    const translation = paramCatalog.translations?.[name.toLowerCase()];
    const note = document.createElement("p");
    note.className = "param-note";
    if (translation) {
      row.classList.add("err");
      note.textContent = translation.message;
      if (translation.suggest?.name) {
        const fix = document.createElement("button");
        fix.type = "button";
        fix.className = "btn-ghost param-fix";
        fix.textContent = "Use " + translation.suggest.name;
        fix.addEventListener("click", () => {
          const value = row.querySelector('[name="paramValue"]').value;
          nameEl.value = translation.suggest.name;
          if (translation.suggest.value) {
            row.querySelector('[name="paramValue"]').value = translation.suggest.value;
          } else if (!value) {
            row.querySelector('[name="paramValue"]').value = "";
          }
          annotateParamRow(row);
        });
        note.appendChild(document.createTextNode(" "));
        note.appendChild(fix);
      } else {
        const drop = document.createElement("button");
        drop.type = "button";
        drop.className = "btn-ghost param-fix";
        drop.textContent = "Remove";
        drop.addEventListener("click", () => row.remove());
        note.appendChild(document.createTextNode(" "));
        note.appendChild(drop);
      }
    } else {
      // A curated suggestion is blessed even when the driver does not parse it
      // itself (application_name and foreign_key_checks are meant to reach the
      // server), so it gets its own help rather than the generic warning.
      const suggestion = (paramCatalog.suggestions || []).find((s) => s.name === name);
      if (suggestion) {
        if (!suggestion.help) return;
        note.textContent = suggestion.help;
      } else if (!(paramCatalog.known || []).includes(name)) {
        row.classList.add("warn");
        note.textContent = paramCatalog.unknownNote || "";
        if (!note.textContent) return;
      } else {
        return;
      }
    }
    row.appendChild(note);
  }

  function renderParamChips() {
    const chips = document.getElementById("param-chips");
    const list = document.getElementById("param-names");
    if (list) {
      list.innerHTML = "";
      (paramCatalog.known || []).forEach((name) => {
        const opt = document.createElement("option");
        opt.value = name;
        list.appendChild(opt);
      });
    }
    const intro = document.getElementById("param-intro");
    if (intro) intro.textContent = paramCatalog.unknownNote || "";
    if (!chips) return;
    chips.innerHTML = "";
    (paramCatalog.suggestions || []).slice(0, 6).forEach((s) => {
      const chip = document.createElement("button");
      chip.type = "button";
      chip.className = "param-chip";
      chip.textContent = "+ " + s.name;
      chip.title = s.help || "";
      chip.addEventListener("click", () => addParamRow(s.name, s.value, true));
      chips.appendChild(chip);
    });
  }

  async function loadParamCatalog(dbType) {
    try {
      const res = await fetch("/api/params?dbType=" + encodeURIComponent(dbType || ""));
      if (res.ok) paramCatalog = await res.json();
    } catch (_) { /* keep the previous catalog */ }
    renderParamChips();
    document.querySelectorAll("#param-rows .param-row").forEach(annotateParamRow);
  }

  function renderTestResult(result) {
    const box = document.getElementById("test-result");
    if (!box) return;
    box.hidden = false;
    box.className = "test-result span-2 " + (result.ok ? "ok" : "err");
    box.innerHTML = "";
    const line = document.createElement("p");
    line.className = "test-line";
    if (result.ok) {
      line.textContent = `Connected to ${result.target || "the database"} via ${result.driver} in ${result.elapsedMs}ms.`;
    } else {
      line.textContent = result.error || "Connection failed.";
    }
    box.appendChild(line);
    if (result.hint) {
      const hint = document.createElement("p");
      hint.className = "test-hint";
      hint.textContent = result.hint.note || "";
      const btn = document.createElement("button");
      btn.type = "button";
      btn.className = "btn-ghost param-fix";
      if (result.hint.add) {
        btn.textContent = `Add ${result.hint.add.name}=${result.hint.add.value}`;
        btn.addEventListener("click", () => {
          addParamRow(result.hint.add.name, result.hint.add.value, false);
          btn.disabled = true;
        });
      } else if (result.hint.remove) {
        btn.textContent = `Remove ${result.hint.remove}`;
        btn.addEventListener("click", () => {
          removeParamRow(result.hint.remove);
          btn.disabled = true;
        });
      }
      if (btn.textContent) {
        hint.appendChild(document.createTextNode(" "));
        hint.appendChild(btn);
      }
      box.appendChild(hint);
    }
    (result.issues || []).forEach((issue) => {
      const p = document.createElement("p");
      p.className = "param-issue " + issue.level;
      p.textContent = issue.message;
      box.appendChild(p);
    });
  }

  function setupConnectForm() {
    const form = document.getElementById("connect-form");
    if (!form) return;
    const pwInput = document.getElementById("conn-password");
    const eyeBtn = document.getElementById("toggle-password");
    const dbType = document.getElementById("conn-dbtype");
    const port = form.querySelector('[name="port"]');
    const rawDSN = document.getElementById("conn-dsn");
    const saveBox = document.getElementById("conn-save");
    const savePwBox = document.getElementById("conn-save-password");
    const defaultPorts = { postgres: "5432", mysql: "3306" };

    const syncRawDSNMode = () => {
      if (!rawDSN) return;
      const usingRaw = rawDSN.value.trim() !== "";
      ["host", "port", "dbName", "user"].forEach((name) => {
        const el = form.querySelector(`[name="${name}"]`);
        if (el) {
          el.required = !usingRaw;
          el.closest(".field")?.classList.toggle("superseded", usingRaw);
        }
      });
    };

    const syncSaveMode = () => {
      if (!saveBox || !savePwBox) return;
      savePwBox.disabled = !saveBox.checked;
      document.getElementById("save-password-row")?.classList.toggle("disabled", !saveBox.checked);
    };

    if (eyeBtn && pwInput) {
      eyeBtn.addEventListener("click", () => {
        const revealed = eyeBtn.dataset.revealed === "true";
        eyeBtn.dataset.revealed = revealed ? "false" : "true";
        pwInput.type = revealed ? "password" : "text";
        eyeBtn.setAttribute("aria-label", revealed ? "Reveal password" : "Hide password");
      });
    }

    // MySQL has no sslmode: TLS is a driver parameter there, so say so rather
    // than leaving a control that silently does nothing.
    const syncDriverMode = () => {
      if (!dbType) return;
      const isMySQL = dbType.value === "mysql";
      const ssl = document.getElementById("conn-ssl");
      const sslLabel = document.getElementById("ssl-label");
      if (ssl) {
        ssl.disabled = isMySQL;
        ssl.closest(".field")?.classList.toggle("superseded", isMySQL);
      }
      if (sslLabel) {
        sslLabel.textContent = isMySQL ? "SSL mode — MySQL uses the tls parameter" : "SSL mode";
      }
    };

    if (dbType) {
      dbType.addEventListener("change", () => {
        const next = defaultPorts[dbType.value];
        const known = Object.values(defaultPorts).includes(port?.value);
        if (port && next && (port.value === "" || known)) port.value = next;
        syncDriverMode();
        loadParamCatalog(dbType.value);
      });
      syncDriverMode();
    }
    if (rawDSN) rawDSN.addEventListener("input", syncRawDSNMode);
    if (saveBox) saveBox.addEventListener("change", syncSaveMode);

    document.getElementById("param-add")?.addEventListener("click", () => addParamRow("", "", true));
    document.getElementById("param-rows")?.addEventListener("click", (ev) => {
      if (ev.target.classList.contains("param-remove")) ev.target.closest(".param-row").remove();
    });
    document.getElementById("param-rows")?.addEventListener("input", (ev) => {
      if (ev.target.name === "paramName") annotateParamRow(ev.target.closest(".param-row"));
    });

    const testBtn = document.getElementById("test-connection");
    testBtn?.addEventListener("click", async () => {
      const original = testBtn.textContent;
      testBtn.disabled = true;
      testBtn.textContent = "Testing…";
      try {
        // URLSearchParams keeps this a urlencoded post; a bare FormData would
        // send multipart, which the plain form handlers do not read.
        const body = new URLSearchParams(new FormData(form));
        const res = await fetch("/connect/test", { method: "POST", body });
        renderTestResult(await res.json());
      } catch (err) {
        renderTestResult({ ok: false, error: err.message || String(err) });
      } finally {
        testBtn.disabled = false;
        testBtn.textContent = original;
      }
    });

    form.addEventListener("seedstorm:refresh", () => {
      syncRawDSNMode();
      syncSaveMode();
      syncDriverMode();
      loadParamCatalog(dbType?.value || "postgres");
    });

    syncRawDSNMode();
    syncSaveMode();
    loadParamCatalog(dbType?.value || "postgres");
  }

  // ── connection dialog ─────────────────────────────────────────────────
  // Editing a stored connection stays on the chooser: the same form opens in a
  // dialog, so you can rename or repoint a connection without connecting to it.
  function setupConnectionDialog() {
    const dialog = document.getElementById("connection-dialog");
    if (!dialog || typeof dialog.showModal !== "function") return;
    const form = dialog.querySelector("#connect-form");
    const title = document.getElementById("connection-dialog-title");
    const connectBtn = dialog.querySelector("#submit-connect");
    const saveBtn = dialog.querySelector("#submit-save");

    const setField = (name, value) => {
      const el = form.querySelector(`[name="${name}"]`);
      if (el) el.value = value ?? "";
    };

    const fill = (conn, mode) => {
      const c = conn || {};
      setField("id", mode === "duplicate" ? "" : (c.id || ""));
      setField("label", mode === "duplicate" ? `${c.label} copy` : (c.label || ""));
      setField("dbType", c.dbType || "postgres");
      setField("host", c.host || "localhost");
      setField("port", c.port || (c.dbType === "mysql" ? 3306 : 5432));
      setField("dbName", c.dbName || "");
      setField("user", c.user || "");
      setField("dsn", c.dsn || "");
      setField("ssl", c.ssl || "disable");
      setField("password", "");

      const rows = form.querySelector("#param-rows");
      if (rows) rows.innerHTML = "";
      (c.params || []).forEach((p) => addParamRow(p.name, p.value, false));

      const save = form.querySelector("#conn-save");
      const savePw = form.querySelector("#conn-save-password");
      if (save) save.checked = true;
      if (savePw) savePw.checked = mode === "add" ? false : !!c.hasPassword;

      const dsnBlock = form.querySelector("#adv-dsn");
      if (dsnBlock) dsnBlock.open = !!c.dsn;
      const result = form.querySelector("#test-result");
      if (result) result.hidden = true;

      // Editing an existing connection makes saving the primary action;
      // adding one usually ends in connecting.
      const editing = mode === "edit";
      connectBtn?.classList.toggle("btn-primary", !editing);
      connectBtn?.classList.toggle("btn-ghost", editing);
      saveBtn?.classList.toggle("btn-primary", editing);
      saveBtn?.classList.toggle("btn-ghost", !editing);
      if (title) {
        title.textContent = mode === "edit" ? "Edit connection"
          : mode === "duplicate" ? "Duplicate connection" : "Add connection";
      }
      form.dispatchEvent(new Event("seedstorm:refresh"));
    };

    const open = async (mode, id) => {
      let conn = null;
      if (id) conn = (await fetchSavedConnections()).find((c) => c.id === id) || null;
      fill(conn, mode);
      dialog.showModal();
      form.querySelector(mode === "add" ? '[name="host"]' : '[name="label"]')?.focus();
    };

    document.getElementById("add-connection")?.addEventListener("click", (ev) => {
      ev.preventDefault();
      open("add", "");
    });
    document.querySelectorAll("[data-edit-connection]").forEach((el) => {
      el.addEventListener("click", (ev) => { ev.preventDefault(); open("edit", el.dataset.editConnection); });
    });
    document.querySelectorAll("[data-duplicate-connection]").forEach((el) => {
      el.addEventListener("click", (ev) => { ev.preventDefault(); open("duplicate", el.dataset.duplicateConnection); });
    });
    document.getElementById("connection-dialog-close")?.addEventListener("click", () => dialog.close());
    document.getElementById("back-to-saved")?.addEventListener("click", (ev) => {
      if (dialog.open) { ev.preventDefault(); dialog.close(); }
    });
    dialog.addEventListener("click", (ev) => {
      if (ev.target === dialog) dialog.close();
    });
  }

  function setupSavedChooser() {
    document.querySelectorAll("[data-delete-connection]").forEach((btn) => {
      btn.addEventListener("click", async () => {
        const id = btn.dataset.deleteConnection;
        if (!window.confirm(`Delete the saved connection "${btn.dataset.label || id}"? The database is not touched.`)) return;
        btn.disabled = true;
        try {
          await deleteSavedConnection(id);
          window.location.reload();
        } catch (err) {
          btn.disabled = false;
          window.alert("Delete failed: " + (err.message || err));
        }
      });
    });
  }

  // Saved connections in the header menu, minus the ones already live.
  async function setupConnectionMenuSaved() {
    const list = document.getElementById("conn-preset-list");
    if (!list) return;
    const [saved, live] = await Promise.all([fetchSavedConnections(), fetchConnections()]);
    const liveKeys = new Set(live.map((c) => connectionKey(c.info)));
    const rest = saved.filter((c) => !liveKeys.has(connectionKey(c)));
    if (!rest.length) return;
    const heading = document.createElement("div");
    heading.className = "conn-menu-heading";
    heading.textContent = "Saved connections";
    list.appendChild(heading);
    rest.forEach((c) => {
      const needsSecret = !c.hasPassword && !c.dsn;
      const row = document.createElement(needsSecret ? "div" : "form");
      row.className = "conn-menu-row";
      const text =
        `<span class="conn-dot ${escapeHTML(c.dbType || "postgres")}"></span>` +
        `<div class="conn-menu-text"><strong>${escapeHTML(c.label)}</strong>` +
        `<span class="muted small">${escapeHTML(c.dbName || c.dsn || "saved connection")}</span></div>`;
      if (needsSecret) {
        row.innerHTML = `<a class="conn-menu-btn" href="/connect?mode=chooser">${text}<span class="muted small">enter password</span></a>`;
      } else {
        row.method = "post";
        row.action = "/connect/saved";
        row.innerHTML =
          `<input type="hidden" name="id" value="${escapeHTML(c.id)}">` +
          `<button type="submit" class="conn-menu-btn">${text}</button>`;
      }
      list.appendChild(row);
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
    hydrateExportDraft(form, endpoint);
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
          renderJobResult(out, r, job.name || j.name || "run");
        },
      });
    });
  }

  function hydrateExportDraft(form, endpoint) {
    if (endpoint !== "/api/export") return;
    const input = form.querySelector('[name="dataYaml"]');
    if (!input || input.value.trim()) return;
    let draft = null;
    try { draft = JSON.parse(sessionStorage.getItem(GENERATED_DRAFT_KEY) || "null"); }
    catch (_) { draft = null; }
    if (!draft || !draft.yaml) return;
    input.value = draft.yaml;
    const note = document.createElement("div");
    note.className = "handoff-note";
    note.innerHTML = '<strong>Generated data loaded.</strong><span class="muted small">Review it, choose a format, then export.</span>';
    form.insertBefore(note, form.firstChild);
  }

  function renderJobResult(out, result, jobName) {
    out.innerHTML = "";
    const shell = document.createElement("section");
    shell.className = "result-shell";

    const summary = resultSummaryItems(result, jobName);
    if (summary.length > 0) {
      const grid = document.createElement("div");
      grid.className = "result-summary";
      summary.forEach((item) => {
        const card = document.createElement("div");
        card.className = "result-stat";
        const value = document.createElement("strong");
        value.textContent = item.value;
        const label = document.createElement("span");
        label.textContent = item.label;
        card.append(value, label);
        grid.appendChild(card);
      });
      shell.appendChild(grid);
    }

    if (Array.isArray(result.order) && result.order.length > 0) {
      shell.appendChild(renderTableList("Run order", result.order));
    } else if (Array.isArray(result.tables) && result.tables.length > 0) {
      shell.appendChild(renderTableList("Generated tables", result.tables));
    }

    if (Array.isArray(result.gapTables)) {
      shell.appendChild(renderTableList("Empty tables", result.gapTables, "No empty tables found."));
    }

    if (Array.isArray(result.warnings) && result.warnings.length > 0) {
      shell.appendChild(renderWarnings(result.warnings));
    }

    const output = typeof result.output === "string" ? result.output : (typeof result.yaml === "string" ? result.yaml : "");
    if (output) {
      shell.appendChild(renderOutputPanel(output, result, jobName));
      if ((result.format || "yaml").toLowerCase() === "yaml" && jobName === "generate") {
        persistGeneratedDraft(output);
      }
    }

    out.appendChild(shell);
  }

  function resultSummaryItems(result, jobName) {
    const items = [];
    const fmt = result.format ? String(result.format).toUpperCase() : "";
    if (result.dryRun) items.push({ label: "mode", value: "Dry-run" });
    else items.push({ label: "run", value: jobName });
    if (fmt) items.push({ label: "format", value: fmt });
    if (typeof result.totalRows === "number") items.push({ label: "rows", value: formatCount(result.totalRows) });
    if (typeof result.tables === "number") items.push({ label: "tables", value: String(result.tables) });
    else if (Array.isArray(result.tables)) items.push({ label: "tables", value: String(result.tables.length) });
    if (Array.isArray(result.auto) && result.auto.length > 0) items.push({ label: "auto-required", value: String(result.auto.length) });
    if (typeof result.durationMs === "number") items.push({ label: "duration", value: result.durationMs < 1000 ? `${result.durationMs}ms` : `${(result.durationMs / 1000).toFixed(1)}s` });
    return items;
  }

  function renderWarnings(warnings) {
    const wrap = document.createElement("div");
    wrap.className = "result-warnings";
    const title = document.createElement("strong");
    title.textContent = "Run notes";
    wrap.appendChild(title);
    warnings.forEach((w) => {
      const row = document.createElement("p");
      const table = w.table || "table";
      const requested = typeof w.requested === "number" ? formatCount(w.requested) : "requested";
      const generated = typeof w.generated === "number" ? formatCount(w.generated) : "generated";
      const reason = w.reason || "generation limit";
      row.textContent = `${table}: generated ${generated} of ${requested} rows because of ${reason}.`;
      wrap.appendChild(row);
    });
    return wrap;
  }

  function renderTableList(title, tables, emptyText) {
    const wrap = document.createElement("div");
    wrap.className = "result-list";
    const head = document.createElement("div");
    head.className = "result-list-head";
    const strong = document.createElement("strong");
    strong.textContent = title;
    const count = document.createElement("span");
    count.className = "muted small";
    count.textContent = `${tables.length} ${tables.length === 1 ? "table" : "tables"}`;
    head.append(strong, count);
    wrap.appendChild(head);
    if (tables.length === 0) {
      const empty = document.createElement("p");
      empty.className = "muted small empty-hint";
      empty.textContent = emptyText || "Nothing to show.";
      wrap.appendChild(empty);
      return wrap;
    }
    const row = document.createElement("div");
    row.className = "result-table-chips";
    tables.forEach((tableName) => {
      const chip = document.createElement("span");
      chip.textContent = tableName;
      row.appendChild(chip);
    });
    wrap.appendChild(row);
    return wrap;
  }

  function renderOutputPanel(output, result, jobName) {
    const panel = document.createElement("div");
    panel.className = "result-output";
    const toolbar = document.createElement("div");
    toolbar.className = "result-output-toolbar";
    const title = document.createElement("div");
    title.className = "result-output-title";
    const label = document.createElement("strong");
    label.textContent = result.dryRun ? "SQL preview" : "Output";
    const meta = document.createElement("span");
    meta.className = "muted small";
    meta.textContent = `${formatBytes(output.length)} · ${(result.format || "txt").toUpperCase()}`;
    title.append(label, meta);

    const actions = document.createElement("div");
    actions.className = "row";
    const view = document.createElement("button");
    view.className = "btn-primary";
    view.type = "button";
    view.textContent = "View full";
    view.addEventListener("click", () => openResultModal(output, result, jobName));
    const copy = document.createElement("button");
    copy.className = "btn-ghost";
    copy.type = "button";
    copy.textContent = "Copy";
    copy.addEventListener("click", async () => {
      await copyText(output);
      copy.textContent = "Copied";
      setTimeout(() => { copy.textContent = "Copy"; }, 1400);
    });
    const dl = document.createElement("a");
    dl.className = "btn-ghost";
    dl.href = "data:text/plain;charset=utf-8," + encodeURIComponent(output);
    dl.download = `seedstorm-${jobName}.${result.format || "txt"}`;
    dl.textContent = "Download";
    actions.append(view, copy, dl);

    if ((result.format || "").toLowerCase() === "yaml" && jobName === "generate") {
      const exportBtn = document.createElement("button");
      exportBtn.className = "btn-ghost";
      exportBtn.type = "button";
      exportBtn.textContent = "Export this";
      exportBtn.addEventListener("click", () => {
        persistGeneratedDraft(output);
        window.location.href = "/export";
      });
      actions.appendChild(exportBtn);
    }

    toolbar.append(title, actions);
    const pre = document.createElement("pre");
    pre.className = "job-log result-pre";
    pre.textContent = output;
    panel.append(toolbar, pre);
    return panel;
  }

  function openResultModal(output, result, jobName) {
    let modal = document.getElementById("result-modal");
    if (!modal) {
      modal = document.createElement("div");
      modal.id = "result-modal";
      modal.className = "result-modal";
      modal.hidden = true;
      modal.innerHTML = `
        <div class="result-modal-backdrop" data-result-modal-close></div>
        <section class="result-modal-panel" role="dialog" aria-modal="true" aria-labelledby="result-modal-title">
          <header class="result-modal-head">
            <div>
              <span class="eyebrow" id="result-modal-kind">output preview</span>
              <h2 id="result-modal-title">Output</h2>
              <p class="muted small" id="result-modal-meta"></p>
            </div>
            <div class="row">
              <button class="btn-ghost" id="result-modal-copy" type="button">Copy</button>
              <a class="btn-ghost" id="result-modal-download">Download</a>
              <button class="btn-ghost" id="result-modal-close" type="button">Close</button>
            </div>
          </header>
          <nav class="result-modal-tabs" aria-label="Output views">
            <button class="result-modal-tab active" type="button" data-result-tab="overview">Overview</button>
            <button class="result-modal-tab" type="button" data-result-tab="tables">Tables</button>
            <button class="result-modal-tab" type="button" data-result-tab="output">SQL / output</button>
          </nav>
          <div class="result-modal-body">
            <section class="result-modal-pane active" data-result-pane="overview" id="result-modal-overview"></section>
            <section class="result-modal-pane" data-result-pane="tables" id="result-modal-tables"></section>
            <section class="result-modal-pane" data-result-pane="output">
              <pre class="result-modal-pre" id="result-modal-output"></pre>
            </section>
          </div>
        </section>
      `;
      document.body.appendChild(modal);
      modal.querySelector("[data-result-modal-close]")?.addEventListener("click", closeResultModal);
      modal.querySelector("#result-modal-close")?.addEventListener("click", closeResultModal);
      modal.querySelectorAll("[data-result-tab]").forEach((tab) => {
        tab.addEventListener("click", () => activateResultTab(modal, tab.dataset.resultTab));
      });
    }

    const format = result.format || "txt";
    modal.dataset.output = output;
    modal.querySelector("#result-modal-kind").textContent = result.dryRun ? "dry-run sql" : `${jobName} output`;
    modal.querySelector("#result-modal-title").textContent = result.dryRun ? "Dry-run SQL preview" : "Generated output";
    modal.querySelector("#result-modal-meta").textContent = `${formatBytes(output.length)} · ${String(format).toUpperCase()}`;
    modal.querySelector("#result-modal-overview").innerHTML = renderResultOverview(result, output, jobName);
    modal.querySelector("#result-modal-tables").innerHTML = renderResultTables(result);
    modal.querySelector("#result-modal-output").textContent = output;
    activateResultTab(modal, "overview");
    const dl = modal.querySelector("#result-modal-download");
    dl.href = "data:text/plain;charset=utf-8," + encodeURIComponent(output);
    dl.download = `seedstorm-${jobName}.${format}`;
    const copy = modal.querySelector("#result-modal-copy");
    copy.onclick = async () => {
      await copyText(output);
      copy.textContent = "Copied";
      setTimeout(() => { copy.textContent = "Copy"; }, 1400);
    };
    modal.hidden = false;
    document.body.classList.add("modal-open");
  }

  function activateResultTab(modal, tabName) {
    modal.querySelectorAll("[data-result-tab]").forEach((tab) => {
      tab.classList.toggle("active", tab.dataset.resultTab === tabName);
    });
    modal.querySelectorAll("[data-result-pane]").forEach((pane) => {
      pane.classList.toggle("active", pane.dataset.resultPane === tabName);
    });
  }

  function renderResultOverview(result, output, jobName) {
    const order = Array.isArray(result.order) ? result.order : (Array.isArray(result.tables) ? result.tables : []);
    const auto = new Set(Array.isArray(result.auto) ? result.auto : []);
    const explicit = Math.max(0, order.length - auto.size);
    const rows = typeof result.totalRows === "number" ? formatCount(result.totalRows) : "n/a";
    const relationText = auto.size > 0
      ? `${auto.size} parent ${auto.size === 1 ? "table was" : "tables were"} added because selected tables depend on them.`
      : "No extra parent tables were required for this run scope.";
    const primary = result.dryRun ? "No database writes will run from this preview." : "This output is ready to copy or download.";
    const cards = [
      ["Run", result.dryRun ? "Dry-run" : jobName],
      ["Rows", rows],
      ["Tables", String(order.length || result.tables || 0)],
      ["Output", `${formatBytes(output.length)} · ${(result.format || "txt").toUpperCase()}`],
    ].map(([label, value]) => `
      <div class="result-modal-card">
        <strong>${escapeHTML(value)}</strong>
        <span>${escapeHTML(label)}</span>
      </div>
    `).join("");
    return `
      <div class="result-modal-grid">${cards}</div>
      <div class="result-modal-callout">
        <strong>${escapeHTML(primary)}</strong>
        <span>${escapeHTML(relationText)}</span>
      </div>
      <div class="result-modal-flow">
        <div><strong>${explicit}</strong><span>explicit or default target tables</span></div>
        <i></i>
        <div><strong>${auto.size}</strong><span>auto-required FK parents</span></div>
        <i></i>
        <div><strong>${order.length}</strong><span>tables in execution order</span></div>
      </div>
    `;
  }

  function renderResultTables(result) {
    const order = Array.isArray(result.order) ? result.order : (Array.isArray(result.tables) ? result.tables : []);
    const auto = new Set(Array.isArray(result.auto) ? result.auto : []);
    const counts = result.tableCounts || {};
    if (order.length === 0) {
      return '<p class="muted small empty-hint">No table list was returned for this run.</p>';
    }
    const rows = order.map((tableName, idx) => {
      const kind = auto.has(tableName) ? "required parent" : "target";
      const rowCount = typeof counts[tableName] === "number" ? formatCount(counts[tableName]) : "n/a";
      return `
        <tr>
          <td>${idx + 1}</td>
          <td><code>${escapeHTML(tableName)}</code></td>
          <td><span class="result-kind ${auto.has(tableName) ? "auto" : ""}">${kind}</span></td>
          <td>${rowCount}</td>
        </tr>
      `;
    }).join("");
    return `
      <table class="result-modal-table">
        <thead><tr><th>#</th><th>Table</th><th>Relationship role</th><th>Rows</th></tr></thead>
        <tbody>${rows}</tbody>
      </table>
    `;
  }

  function closeResultModal() {
    const modal = document.getElementById("result-modal");
    if (modal) modal.hidden = true;
    if (!document.getElementById("table-modal") || document.getElementById("table-modal").hidden) {
      document.body.classList.remove("modal-open");
    }
  }

  function persistGeneratedDraft(yaml) {
    try {
      sessionStorage.setItem(GENERATED_DRAFT_KEY, JSON.stringify({ yaml, createdAt: Date.now() }));
    } catch (_) {}
  }

  async function copyText(text) {
    if (navigator.clipboard?.writeText) {
      await navigator.clipboard.writeText(text);
      return;
    }
    const ta = document.createElement("textarea");
    ta.value = text;
    ta.style.position = "fixed";
    ta.style.opacity = "0";
    document.body.appendChild(ta);
    ta.select();
    document.execCommand("copy");
    ta.remove();
  }

  function formatBytes(chars) {
    if (chars >= 1_000_000) return (chars / 1_000_000).toFixed(1) + " MB";
    if (chars >= 1_000) return (chars / 1_000).toFixed(1) + " KB";
    return `${chars} B`;
  }

  // ── Workspace ─────────────────────────────────────────────────────────
  const ws = {
    cy: null,
    selected: new Set(),  // explicit user picks
    auto: new Set(),      // auto-locked transitive parents
    parents: {},          // table → [hard parents]
    children: {},         // table → [hard children]
    tableRows: {},        // table → explicit row count override
    nodes: [],            // raw graph payload
    edges: [],
    mode: "seed",
    edgeRoute: loadGraphRoute(),
    activeJob: null,
    activeTable: null,
    search: "",
    preview: { limit: 25, offset: 0 },
    modal: { table: "", limit: 50, offset: 0 },
    peek: new Set(),
    schemaColumns: {},
    connections: [],
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
        updateCloneControls();
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
    document.getElementById("cfg-rows")?.addEventListener("input", () => refreshSelectionUI());
    document.getElementById("ws-fit")?.addEventListener("click", () => fitGraph());
    document.getElementById("ws-zoom-in")?.addEventListener("click", () => zoomGraph(1.18));
    document.getElementById("ws-zoom-out")?.addEventListener("click", () => zoomGraph(0.84));
    document.querySelectorAll("[data-route]").forEach((b) => {
      b.addEventListener("click", () => setEdgeRoute(b.dataset.route));
      b.classList.toggle("active", b.dataset.route === ws.edgeRoute);
    });
    setupTableModal();
    document.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") { closeTableModal(); closeResultModal(); }
      if (ev.target && ["INPUT", "TEXTAREA", "SELECT"].includes(ev.target.tagName)) return;
      if (ev.key === "/") {
        ev.preventDefault();
        document.getElementById("ws-search")?.focus();
      }
      if (ev.key.toLowerCase() === "f") fitGraph();
    });
    // Run
    document.getElementById("ws-run").addEventListener("click", runMode);

    loadCloneTargets();
    loadGraph();
  }

  async function loadGraph() {
    setGraphLoading("Loading schema", "Introspecting tables and relationships from the active connection.");
    try {
      const res = await fetch("/api/graph", { cache: "no-store" });
      setGraphLoading("Loading schema", "Received schema response; parsing graph payload.");
      const data = await res.json();
      if (!res.ok) throw new Error(data.error || res.statusText);
      initGraph(data);
    } catch (err) {
      setGraphLoading("Graph failed", err.message || String(err), true);
    }
  }

  function setGraphLoading(title, detail, failed) {
    const box = document.getElementById("ws-graph-loading");
    if (!box) return;
    box.hidden = false;
    box.dataset.state = failed ? "failed" : "loading";
    const titleEl = box.querySelector(".ws-loading-title");
    const detailEl = box.querySelector(".ws-loading-detail");
    if (titleEl) titleEl.textContent = title;
    if (detailEl) detailEl.textContent = detail || "";
  }

  function clearGraphLoading() {
    const box = document.getElementById("ws-graph-loading");
    if (box) box.hidden = true;
  }

  async function loadCloneTargets() {
    const target = document.getElementById("cfg-clone-target");
    if (!target) return;
    ws.connections = await fetchConnections();
    const active = ws.connections.find(c => c.active);
    target.innerHTML = "";
    const activeInfo = active?.info || {};
    const activeKey = connectionKey(activeInfo);
    const seen = new Set([activeKey]);
    ws.connections.filter(c => !c.active && (!active || c.info.dbType === active.info.dbType)).forEach((c) => {
      const key = connectionKey(c.info);
      if (seen.has(key)) return;
      seen.add(key);
      const opt = document.createElement("option");
      opt.value = c.id;
      opt.dataset.kind = "connection";
      opt.textContent = connectionLabel(c.info);
      target.appendChild(opt);
    });
    const saved = await fetchSavedConnections();
    saved.forEach((c) => {
      const key = connectionKey(c);
      if (active && c.dbType !== activeInfo.dbType) return;
      if (seen.has(key)) return;
      seen.add(key);
      const opt = document.createElement("option");
      opt.value = c.id;
      opt.dataset.kind = "saved";
      opt.textContent = connectionLabel(c);
      if (!c.hasPassword && !c.dsn) {
        opt.disabled = true;
        opt.textContent += " (needs password — connect it first)";
      }
      target.appendChild(opt);
    });
    if (!target.options.length) {
      const opt = document.createElement("option");
      opt.value = "";
      opt.dataset.kind = "empty";
      opt.textContent = "connect another matching database";
      target.appendChild(opt);
    }
    updateCloneControls();
  }

  function updateCloneControls() {
    const clone = ws.mode === "clone";
    const cloneCfg = document.getElementById("ws-clone-config");
    if (cloneCfg) cloneCfg.hidden = !clone;
    document.querySelectorAll(".ws-config, .ws-risk").forEach((el) => {
      el.hidden = clone;
    });
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
    setGraphLoading("Building graph", `${(data.nodes || []).length} tables and ${(data.edges || []).length} relationships loaded.`);
    ws.nodes = data.nodes || [];
    ws.edges = data.edges || [];
    // Compute hard FK parents per table (from non-nullable edges).
    ws.parents = {};
    ws.children = {};
    for (const n of ws.nodes) {
      ws.parents[n.id] = [];
      ws.children[n.id] = [];
    }
    for (const e of ws.edges) {
      if (!e.nullable) {
        ws.parents[e.target].push(e.source);
        ws.children[e.source].push(e.target);
      }
    }

    const elements = [
      ...ws.nodes.map(n => ({ data: nodeData(n) })),
      ...ws.edges.map((e, idx) => ({
        data: {
          id: e.id,
          source: e.source,
          target: e.target,
          label: e.column,
          nullable: e.nullable,
          routeLane: idx % 7,
          routeColor: routeColorFor(e.source),
        },
      })),
    ];

    setGraphLoading("Rendering graph", "Creating nodes and relationships.");
    ws.cy = cytoscape({
      container: document.getElementById("cy"),
      elements,
      style: cyStyle(),
      layout: { name: "preset" },
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
    applyEdgeRoute();
    setGraphLoading("Laying out graph", "Positioning tables by dependency level.");
    const layout = ws.cy.layout(dagreLayout());
    layout.on("layoutstop", () => {
      fitGraph();
      clearGraphLoading();
    });
    layout.run();
    updateStats();
    refreshSelectionUI();
  }

  function nodeData(n) {
    return {
      id: n.id,
      label: n.label,
      displayLabel: n.label,
      count: n.count,
      counted: n.counted,
      countLabel: n.counted ? formatCount(n.count) : "?",
    };
  }

  function routeColorFor(seed) {
    const colors = ["#79d8b3", "#d8b56f", "#7ca7ff", "#df8cc8", "#8dd6e8", "#c2d16b", "#b196ff"];
    let hash = 0;
    for (let i = 0; i < seed.length; i++) hash = ((hash << 5) - hash) + seed.charCodeAt(i);
    return colors[Math.abs(hash) % colors.length];
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
          "label": "data(displayLabel)",
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
          "text-wrap": "wrap",
          "text-max-width": 150,
          "line-height": 1.25,
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
        selector: "edge.route-smooth",
        style: {
          "curve-style": "unbundled-bezier",
        },
      },
      {
        selector: "edge.route-step",
        style: {
          "curve-style": "taxi",
          "taxi-direction": "auto",
          "taxi-turn": "42px",
          "taxi-turn-min-distance": "16px",
        },
      },
      {
        selector: "edge.route-straight",
        style: {
          "curve-style": "bezier",
          "control-point-step-size": 42,
        },
      },
      {
        selector: "edge[?nullable]",
        style: { "line-style": "dashed", "line-color": "#4a5169", "target-arrow-color": "#4a5169" },
      },
    ];
  }

  function loadGraphRoute() {
    try {
      const saved = localStorage.getItem(GRAPH_ROUTE_KEY);
      if (["straight", "smooth", "step"].includes(saved)) return saved;
    } catch (_) {}
    return "straight";
  }

  function setEdgeRoute(route) {
    if (!["straight", "smooth", "step"].includes(route)) return;
    ws.edgeRoute = route;
    try { localStorage.setItem(GRAPH_ROUTE_KEY, route); } catch (_) {}
    document.querySelectorAll("[data-route]").forEach((b) => {
      b.classList.toggle("active", b.dataset.route === route);
    });
    applyEdgeRoute();
  }

  function applyEdgeRoute() {
    if (!ws.cy) return;
    const smoothOffsets = [-96, -64, -32, 32, 64, 96, 128];
    const taxiTurns = [24, 44, 64, 84, 104, 124, 144];
    ws.cy.batch(() => {
      ws.cy.edges()
        .removeClass("route-straight route-smooth route-step")
        .addClass("route-" + ws.edgeRoute);
      ws.cy.edges().forEach((edge) => {
        edge.removeStyle("curve-style line-color target-arrow-color control-point-distances control-point-weights control-point-step-size taxi-direction taxi-turn taxi-turn-min-distance");
        const lane = Number(edge.data("routeLane") || 0);
        if (ws.edgeRoute === "smooth") {
          const color = edge.data("routeColor");
          edge.style({
            "curve-style": "unbundled-bezier",
            "line-color": color,
            "target-arrow-color": color,
            "control-point-distances": smoothOffsets[lane] + "px",
            "control-point-weights": lane % 2 === 0 ? "0.42" : "0.58",
          });
        }
        if (ws.edgeRoute === "step") {
          const color = edge.data("routeColor");
          edge.style({
            "curve-style": "taxi",
            "line-color": color,
            "target-arrow-color": color,
            "taxi-direction": "rightward",
            "taxi-turn": taxiTurns[lane] + "px",
            "taxi-turn-min-distance": "18px",
          });
        }
      });
    });
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
    ws.tableRows = {};
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
        n.data("displayLabel", nodeDisplayLabel(id));
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
      name.className = "ws-sel-name";
      name.textContent = item.id;
      const volume = document.createElement("label");
      volume.className = "ws-sel-rows";
      volume.title = "Rows to create for this table in this run";
      const volumeText = document.createElement("span");
      const childCount = selectedChildCount(item.id);
      volumeText.textContent = childCount > 0 ? `${childCount} child${childCount === 1 ? "" : "ren"}` : "rows";
      const volumeInput = document.createElement("input");
      volumeInput.type = "number";
      volumeInput.min = "1";
      volumeInput.inputMode = "numeric";
      volumeInput.placeholder = String(defaultRows());
      volumeInput.value = ws.tableRows[item.id] ? String(ws.tableRows[item.id]) : "";
      volumeInput.addEventListener("click", (ev) => ev.stopPropagation());
      volumeInput.addEventListener("input", (ev) => {
        ev.stopPropagation();
        const n = Number(ev.target.value || 0);
        if (n > 0) ws.tableRows[item.id] = n;
        else delete ws.tableRows[item.id];
        syncNodeRowLabels();
        updateRunScope();
      });
      volume.append(volumeText, volumeInput);
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
          delete ws.tableRows[item.id];
          recomputeAuto();
          refreshSelectionUI();
        });
        actions.append(remove);
      }
      main.append(name, volume, actions);
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

  function defaultRows() {
    const value = Number(document.getElementById("cfg-rows")?.value);
    return Number.isFinite(value) && value >= 0 ? value : 20;
  }

  function selectedChildCount(tableName) {
    const effective = new Set([...ws.selected, ...ws.auto]);
    return (ws.children[tableName] || []).filter((child) => effective.has(child)).length;
  }

  function nodeDisplayLabel(id) {
    const override = ws.tableRows[id];
    const effective = ws.selected.has(id) || ws.auto.has(id);
    return effective && override > 0 ? `${id}\n${formatCount(override)} rows` : id;
  }

  function syncNodeRowLabels() {
    if (!ws.cy) return;
    ws.cy.batch(() => {
      ws.cy.nodes().forEach((n) => n.data("displayLabel", nodeDisplayLabel(n.id())));
    });
  }

  async function loadPeek(tableName, target) {
    target.hidden = false;
    target.innerHTML = '<p class="muted small">Loading rows...</p>';
    const q = new URLSearchParams({ table: tableName, limit: "5", offset: "0", _: String(Date.now()) });
    const res = await fetch("/api/table?" + q.toString(), { cache: "no-store" });
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
    const more = data.total > data.rows.length
      ? `<span>Showing first ${data.rows.length} of ${data.total} rows</span>`
      : `<span>${data.total} rows</span>`;
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
    const modeLabel = ws.mode === "gaps" ? "Fill empty" : (ws.mode === "generate" ? "Generate" : (ws.mode === "clone" ? "Clone schema" : "Seed"));
    if (scope) {
      const overrideCount = Object.keys(tableRowPayload()).length;
      const volumeText = overrideCount > 0
        ? ` · ${overrideCount} customized`
        : "";
      scope.textContent = ws.mode === "clone"
        ? "Run scope: full source schema"
        : effective === 0
        ? `Run scope: all ${total} tables`
        : `Run scope: ${effective} tables (${explicit} selected, ${auto} required)${volumeText}`;
    }
    if (run) {
      run.textContent = ws.mode === "clone"
        ? modeLabel
        : effective === 0 ? `${modeLabel} all tables` : `${modeLabel} ${effective} tables`;
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
      _: String(Date.now()),
    });
    const res = await fetch("/api/table?" + q.toString(), { cache: "no-store" });
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
      _: String(Date.now()),
    });
    const res = await fetch("/api/table?" + q.toString(), { cache: "no-store" });
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
    if (ws.mode === "clone") {
      return runCloneSchema();
    }
    const tables = [...ws.selected];
    const cfg = {
      rows: Number(document.getElementById("cfg-rows").value || 0),
      enumRows: Number(document.getElementById("cfg-enum").value || 0),
      batchSize: Number(document.getElementById("cfg-batch").value || 0),
      selfRefDepth: Number(document.getElementById("cfg-selfref-depth").value || 0),
      truncate: document.getElementById("cfg-truncate").checked,
      dryRun: document.getElementById("cfg-dryrun").checked,
      disableFK: document.getElementById("cfg-disablefk").checked,
      tables,
      tableRows: tableRowPayload(),
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

  async function runCloneSchema() {
    const target = document.getElementById("cfg-clone-target");
    const selected = target?.selectedOptions?.[0];
    const cfg = {
      dropExisting: !!document.getElementById("cfg-clone-drop")?.checked,
      dryRun: !!document.getElementById("cfg-clone-dryrun")?.checked,
    };
    if (selected?.dataset.kind === "connection") {
      cfg.targetId = selected.value;
    } else if (selected?.dataset.kind === "saved") {
      cfg.targetSavedId = selected.value;
    }
    activateTab("logs");
    resetPhases();
    document.getElementById("job-result").innerHTML = "";
    const res = await fetch("/api/clone-schema", {
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
      onEnd: (job) => {
        const out = document.getElementById("job-result");
        if (out) renderJobResult(out, job.result || {}, "clone-schema");
      },
    });
  }

  function tableRowPayload() {
    const effective = new Set([...ws.selected, ...ws.auto]);
    const out = {};
    for (const [tableName, rows] of Object.entries(ws.tableRows)) {
      if (rows > 0 && effective.has(tableName)) out[tableName] = rows;
    }
    return out;
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
    if (out) renderJobResult(out, job.result || {}, job.name || ws.mode);
    refreshCounts();
  }

  function refreshCounts() {
    if (!ws.cy) return;
    setGraphLoading("Refreshing counts", "Reading row counts for every table.");
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
      clearGraphLoading();
    }).catch((err) => {
      setGraphLoading("Count refresh failed", err.message || String(err), true);
    });
  }

  document.addEventListener("DOMContentLoaded", () => {
    setupConnectForm();
    setupConnectionDialog();
    setupSavedChooser();
    migrateLegacyPresets().then(setupConnectionMenuSaved);
    setupRunForm();
    setupWorkspace();
    document.addEventListener("keydown", (ev) => {
      if (ev.key === "Escape") closeResultModal();
    });
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
