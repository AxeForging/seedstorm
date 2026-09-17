# 003 — Workspace insights, access checks, counts snapshots, object clone, scale

> Working document. **Not committed** — stays untracked and out of the PR.

## Context

Feedback from using `serve` against a real 128-table staging database:

1. The header does not say how many connections are live.
2. Nothing tells you whether the connected user can actually do what a tab offers
   (insert, truncate, create tables). You find out when a run fails.
3. Search highlights matches but leaves the viewport where it was; a 128-table graph
   renders as an unreadable web with no way to navigate it at a readable zoom.
4. Row counts cannot leave the tool: no export to a file/clipboard, no import, no
   compare of an imported counts file against a live target.
5. Schema clone copies tables, FKs, indexes and comments only — no views, functions,
   procedures or triggers.
6. Seeding is silent: a 1.5M-row table printed "Generating fake data rows=1543690"
   and nothing else for 9 minutes. 128 tables × 2k rows took 330s.
7. Profiles work in the workspace but there is no way to keep tables out of a run
   (ignored tables) or to see which tables are ignored.
8. Compare loses its report when you navigate away (e.g. to Profiles) and back.
9. Layout: the seed action bar is twice the height of the clone one; the compare
   "Advanced" checkbox is huge.

### Measured (local docker, Keycloak fixture, 87 tables × 2000 rows, MySQL 8)

- 38s wall, **5% CPU** — generation is ~2s; the rest is waiting on INSERTs, one
  statement at a time on one connection. TRUNCATE of 87 tables alone: 16s.
- Rows are already streamed (20k-row chunks, flat memory). The engine does not hold a
  whole table; it just never reports inside a table and never writes concurrently.

## Requirements

### R1 Scale & progress (seeder)
- `SeedOptions.Workers` (default 4; 1 = today's strictly sequential behaviour).
  Generation stays single-threaded (the Stream is stateful and cheap); writes go to a
  pool of workers.
- Correctness: a table's rows are written only after every FK parent in the run has
  finished writing (nullable FKs included). Tables with a self-referencing FK write
  their batches in generation order. Unrelated tables write concurrently; batches of a
  table without a self-FK write concurrently.
- Memory stays bounded: submitting blocks when in-flight rows exceed a budget. No
  deadlock: gating happens in a per-table dispatcher, never inside a worker.
- Strict semantics kept: the first refused insert cancels the run and is returned.
- Progress per written batch: table, table index, rows written for the table and for
  the run, rows requested for both. Web shows rows done/total, rate, ETA and the
  current tables; CLI logs a progress line at most every 2s.
- `Fill` (mirror) writes each chunk's sub-batches concurrently (not for self-FK
  tables), keeps its reject/regenerate logic, and reports run totals.
- MySQL truncate runs on pinned connections (FK checks off per connection) with the
  same worker count.
- Log text stops claiming "Generating fake data rows=N" as a separate step.

### R2 Access (grants) introspection
- `db.InspectAccess(ctx, conn, dbType)` → user, superuser/all flag, database-level
  CREATE, per-table SELECT/INSERT/UPDATE/DELETE/TRUNCATE.
  - Postgres: `has_table_privilege`, `has_database_privilege`, `has_schema_privilege`,
    `rolsuper`.
  - MySQL: `SHOW GRANTS` (expanded with `USING` roles when roles are granted),
    parsed by a pure function (global `*.*`, `db.*` incl. `%`/`_` wildcards and
    escaped `\_`, `db.table`; `ALL [PRIVILEGES]`; TRUNCATE needs DROP).
- `GET /api/access` (active session, cached; `?refresh=1`).
- UI: access badge on the connection pill (full / limited / read-only); mode pills
  show a warning chip with the missing privilege for the run scope; graph nodes the
  user cannot insert into get a distinct style; clone target and compare mirror
  target are checked too.

### R3 Graph navigation
- Search auto-zoom (toggle, default on, remembered): typing fits the viewport to the
  matches (debounced); clearing restores the full fit.
- Navigator mode (opt-in toggle, remembered): zoom is clamped to a readable minimum,
  Fit centres at that zoom, and a minimap (canvas) shows every node and the viewport;
  click/drag the minimap to pan.

### R4 Header
- Connection pill shows the number of live connections.

### R5 Counts snapshots
- `compare.EncodeSnapshot(snap, "json"|"yaml")`, `compare.ParseSnapshot(data)` with a
  `kind: seedstorm.table-counts` + `version: 1` envelope; parse accepts JSON or YAML
  and rejects anything else with a clear error.
- CLI: `seedstorm snapshot` (one endpoint → file/stdout), `compare` and `mirror`
  accept `--source-snapshot <file>` in place of a live source.
- Web: export source/target counts after a compare (download JSON/YAML, copy);
  import (file or paste) into the source picker; compare and mirror run with an
  imported source. Mirror from a snapshot cannot check "same database" — it says so.

### R6 Clone database objects
- `CloneOptions.Objects` {Views, Routines, Triggers}. Default off (today's output
  unchanged byte for byte).
- Postgres (schema `public`): views + materialized views (`pg_get_viewdef`),
  functions + procedures (`pg_get_functiondef`, not extension-owned, not aggregates),
  triggers (`pg_get_triggerdef`, non-internal). Functions run with
  `check_function_bodies = off`; views are created in dependency order (retry with
  savepoints until no progress, then report the failures).
- MySQL: views (`SHOW CREATE VIEW`, `DEFINER` removed, source schema qualifier
  removed), functions/procedures (`SHOW CREATE FUNCTION/PROCEDURE`, `DEFINER`
  removed), triggers (`SHOW CREATE TRIGGER`). Objects whose body is not visible to
  the user are reported as skipped, never silently dropped.
- Order: tables → FKs → indexes → comments → routines → views → triggers.
  `DropExisting` drops the same object kinds first.
- CLI flags `--views --routines --triggers` (`--objects all`), web checkboxes, TUI
  unchanged except passing options through.

### R7 Ignored tables (profiles)
- `RuleSet.Ignore []string` table globs. Ignored tables are never written by seed,
  gaps, generate or mirror. A run whose selection *requires* an ignored empty parent
  fails with a message naming both tables; a populated ignored parent is referenced.
- Profiles page: "Ignored tables" section (add/remove globs, live match list).
- Workspace: ignored tables greyed in the graph and listed in an "Ignored" tab with
  the matching glob. Profile row counts show on nodes when a profile is selected.

### R8 Compare persistence and layout
- Last report per source/target pair persisted in `localStorage`; restored with
  "compared N min ago — counts may have changed" and a Re-compare button.
- Seed action bar single row at desktop widths; compare advanced checkbox normal size.

## Test plan
- Unit: writer pool ordering (child never before parent, self-FK order, unrelated
  tables overlap), strict error cancels, progress totals; MySQL grants parser table
  tests; snapshot encode/parse round-trip + rejects; object DDL ordering/definer
  stripping; ignore glob resolution; access handler.
- Integration (binary-driven where the CLI exposes it): parallel seed of the 28-table
  schema and Keycloak on both engines (counts + FK integrity); limited users on both
  engines → access report; clone with objects on both engines (view-on-view,
  function, procedure, trigger fires on the clone); snapshot export → compare/mirror
  from file; ignore list on seed.
- Browser: workspace (search zoom, navigator/minimap, access badges, ignored tab,
  progress), compare (import/export, persistence), 1440 + 390 widths.

## Rollout
Single PR `feat: …`. Defaults preserve behaviour except Workers=4 (faster, same
result) — documented in README.
