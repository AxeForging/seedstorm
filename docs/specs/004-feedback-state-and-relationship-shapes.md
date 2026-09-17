# 004 — Safe scans, feedback everywhere, kept form state, tuning advice, relationship shapes

> Working document. **Not committed** — stays untracked and out of the PR.

## Context (feedback, 2026-09-17)

1. Counts export only exists after a compare; the nav "Export" tab is a rows converter, not counts.
2. Compare gives little visible feedback while running (no obvious progress / elapsed).
3. Workspace reloads everything when you leave the tab and come back.
4. Progress for schema introspection and comparisons where possible.
5. Profile select, tuning, etc. reset on tab change (workspace and compare).
6. Capture relationship shapes (per FK: min / avg / max children per parent, every level)
   so seeding can simulate them.
7. Unreachable connections in compare must say so, not fail silently / hang.
8. Export counts (and relationship shapes) from a single connection; import them to see
   unmatched tables and calibrate a seed.
9. Review web, TUI and CLI: every slow step shows feedback, every failure explains itself.

## Findings (read from code)

### Web
- The app is multi-page (server-rendered templates). Navigating away and back reloads the page.
- `Session.Schema` is cached server-side (`internal/web/session.go:229`), but `/api/graph`
  runs an exact `COUNT(*)` on every table on every load (`handlers_api.go:58-70`,
  `db/counts.go:12`), then the client lays out the whole graph again. That is the slow reload.
- Workspace persists only graph route/view (`app.js:9-10`). Rows, tuning (writers,
  generators, batch, enum, self-ref), profile and run flags are not persisted. Compare persists
  reports and imports, not pickers/profile/mode/scale (only URL params, `compare.js:63`).
- Compare already streams phases, per-table progress and elapsed time, but they live inside the
  collapsed `<details id="cmp-logs">` (`compare.html.tmpl:148`). The button only says "Comparing…".
- Compare can hang with the button stuck on "Comparing…":
  - **Every failed job, not only network errors (root cause, to reproduce with an e2e test first):**
    the server sends a *named* SSE event `error` before `end` (`handlers_jobs.go:79,101`). In the
    browser a named `error` event also triggers `EventSource.onerror`, which closes the stream
    (`app.js:713`) → `end` never arrives → `onEnd` never runs → `runJob` never settles.
  - `runJob` (`compare.js:161`) wraps an `async` executor in `new Promise`: a `fetch` rejection
    is swallowed.
  - The job log and progress live inside `#cmp-results`, hidden until a report exists
    (`compare.html.tmpl:44`) → the first compare shows no progress at all.
  - More uncaught fetches: `end` handler fetch (`app.js:707`), `runMode`/`runCloneSchema`
    (2757, 2793), `showDetail` (2485), `loadPeek`/`loadPreview`/`loadModalPreview`/
    `ensureSchemaColumns` (1937, 2662, 2633, 2606), cancel (674), profiles.js save/remove/YAML
    (729, 746, 757, 810).
  - Server: SSE handler reads `job.Status`/`job.Err` without the lock (`handlers_jobs.go:77,99`;
    `job.State()` exists); a slow subscriber drops events silently (`jobs.go:250`); no SSE `id:`
    so a reconnect replays the whole backlog; `jobs.Manager` never evicts finished jobs.
- The shared job view uses fixed element IDs and one `elapsedTimer` (`app.js:533-714`): only one
  job can be displayed per page; a second job (counts, relationships) would wipe a running seed's log.
- Source is counted fully before the target is touched (`seeder/mirror.go:90-97`): a dead target
  is discovered only after the whole source count.
- `db.Introspect(dbType, dsn)` opens a new pool and calls `db.Ping()` with no context or timeout
  (`db/db.go:9-18`), while `Session.Schema` holds the session mutex. A black-holed host blocks
  every request of that session. Session open itself pings with a 5s timeout (`session.go:97`).
- Errors land in `#cmp-outcome` inside the results aside, below the fold on narrow screens.
- `setupRunForm` (generate/enrich/export): network error on `fetch` or a non-JSON response throws
  uncaught (`app.js:739-746`).

### CLI (subagent audit, spot-checked)
- No connect timeout or signal cancellation: `main` runs with `context.Background()`
  (`cmd/seedstorm/main.go:15`).
- `compare` logs nothing while connecting/counting; passes `nil` progress to `Snapshots`
  (`cli/compare.go:39`). `snapshot` passes `nil` to `Take`. `mirror` never sets `OnCount`.
- `introspect`, `clone-schema`, `generate`, `export`, `profile validate`: silent between a start
  line and the end (clone-schema prints nothing at all).
- `db.Introspect`, `GetTableRowCounts`, `LoadObjects`/`CloneSchema`, stream preload
  (`loadExistingState`) and `SyncSequences` take no progress callback (introspect: no ctx either).
- seed/gaps ping errors don't name the endpoint.

### TUI
- Seed/gaps: `tableSeededMsg` is handled but never sent (`tui/execute.go:20,104`); no
  `OnProgress`/`OnTable` wired, so the view sits at "0/N tables, 0%" for the whole run.
  `SyncSequences` errors are dropped.
- Mirror: pressing `p` runs `job.Preview` (DB reads) synchronously in `Update`
  (`tui/mirror.go:127`) — the UI freezes. Partial results are lost on error / ctrl+c.
- Clone: static "Cloning schema..." with no spinner; `fmt.Println` while rendering in dry-run.

### Seeding today
- One pick site: `generateValue` (`faker/faker.go:522-541`) takes `pks[rnd.Number(0,len-1)]` from
  the parent's PK pool. Children per parent follow a Poisson-like spread. No min, no long tail.
- **Bugs found (fix first, TDD):**
  - The referenced column is ignored (`refCol` unused): an FK to a UNIQUE non-PK column gets PK values.
  - Nullable FKs are never NULL while the parent pool is non-empty (null share today = 0).
  - Composite-PK parents: all PK columns' values go into one flat pool (`faker.go:504-510,157`).
  - Composite FKs are stored as independent single-column FKs (`db/types.go:18`, `catalog.go:105`);
    on Postgres the FK query joins `table_constraints`/`key_column_usage`/`constraint_column_usage`
    by name only (`db/postgres.go:80-94`): a multi-column FK cross-joins (last writer wins, wrong
    column pairing) and two tables with a same-named constraint contaminate each other. It also
    hides constraints from roles that only have SELECT (information_schema visibility) and only
    reads schema `public`.
- Other paths: junction tables with an all-FK PK walk parent pools like an odometer
  (`enumerateCompositeFKPKRows`, `faker.go:377-442`, cursor resume, never redrawn `stream.go:291`)
  → every parent gets the same degree. Link tables with a surrogate PK + UNIQUE(fk,fk) take the
  standard path; duplicates are **dropped** (`unique.go:85-135`, FK columns are never "free") and
  the PK pool rebuilt. Self-references are overwritten per chunk by `backfillSelfReferences`
  (`faker.go:573-644`): parent = latest row under the depth limit → a star restarting each chunk.
  Enum top-up adds rows after standard rows (`stream.go:238-248`). Fill regenerates refused rows
  with new picks (`seeder.go:201-268`). In-run parent pools are re-shuffled by `capPool` each chunk
  (`pools.go:69`) — biased towards recent parents (inference).
- Any `tableRows` entry sets `hasRowOverride`, which disables enum rows and top-up
  (`stream.go:197,225`).
- Reads with no ctx on the write target: `scanPKs` (`faker.go:151`), `existing.go:197,233`.
- `requestGenWorkers` caps at `runtime.NumCPU()` (`web/runners.go:724`), not the container quota.
- No `SetMaxOpenConns` anywhere; real connections per run ≈ writers + generators (pool redraws) +
  truncate + sequence sync. Web gaps uses the shared session pool (`runners.go:482`); the TUI
  hardcodes `DefaultWorkers` (`tui/execute.go:310`); the CLI has no worker caps.

## Feasibility

| # | Item | Feasible | Size | Notes |
|---|------|----------|------|-------|
| 1 | Where counts export lives | yes | S | Clarify naming; add single-connection snapshot (see 8) |
| 2 | Compare progress/elapsed visible | yes | S | Data already streamed; surface inline + fix hangs |
| 3 | Workspace fast return | yes | M | No SPA rewrite: split structure / counts, cache counts & layout |
| 4 | Introspection & count progress | yes | M | Needs ctx + per-table callback in `db.Introspect`; throttled, ~0 cost |
| 5 | Keep form state across tabs | yes | S | localStorage per-viewer convenience |
| 6 | Relationship shapes | capture: yes (M); seeding: yes with limits (L) | M+L | Exact capture scans child tables; seeding must stay streaming/bounded |
| 7 | Connection failure feedback | yes | S | Preflight both sides concurrently with timeout |
| 8 | Snapshot from one connection + import to calibrate | yes | M | Reuse `compare.Take` / compare-from-snapshot |
| 9 | Feedback audit web/TUI/CLI | yes | M | List below |

## Design

### D0 Production safeguards (built first in the branch; every later read goes through it)
Today "read-only" is a word in help text: counts, compare, snapshot, gaps scan, access checks and
introspection run plain statements on the pool with no transaction mode and no server-side limit.

One entry point in `internal/db` (`safe_read.go`), used by `GetTableRowCounts`,
`GetEstimatedRowCounts`, `ListTableColumns`/`GetTableSizes`, `compare.Take`, `InspectAccess`,
`Introspect`, and later the relationship scan:

```go
type ReadLimits struct {
    StatementTimeout time.Duration // server-side; 0 = none (existing reads), 60s relationship scans / production
    LockTimeout      time.Duration // default 2s: never queue behind DDL
    Concurrency      int           // statements at once; default 2
}
func ReadScope(ctx context.Context, conn *sql.DB, dbType string, lim ReadLimits,
    each func(ctx context.Context, q Querier) error) error
```

1. **Read-only transaction, server-enforced** — `BeginTx(ctx, &sql.TxOptions{ReadOnly: true})`
   (pgx → `BEGIN READ ONLY`, go-sql-driver/mysql → `START TRANSACTION READ ONLY`). Any write is
   refused by the server, whatever the code does.
2. **Short statements** — one transaction per table / edge, committed immediately. No
   long-lived snapshot holding back vacuum (Postgres xmin) or InnoDB purge.
3. **Server-side statement timeout** — Postgres `SET LOCAL statement_timeout`; MySQL
   `SET SESSION max_execution_time` on the pinned conn (5.7.8+, SELECT only), reset on release.
   A timeout marks that table/edge `timed out`; the run continues.
4. **Lock timeout (no DDL pile-ups)** — a read waiting behind an `ALTER TABLE` makes every later
   writer queue behind it (Postgres lock queue, MySQL metadata locks). Postgres
   `SET LOCAL lock_timeout`; MySQL `SET SESSION lock_wait_timeout`. Result: `skipped: table locked`.
5. **Real cancel** — Postgres: pgx sends a cancel request on ctx cancel. MySQL: the driver closes
   the socket but the server may keep running the query (to prove with a failing test first);
   `ReadScope` records `CONNECTION_ID()` per pinned conn and on ctx cancel runs
   `KILL QUERY <id>` from a side connection (users may kill their own threads, no extra grant).
6. **Concurrency cap + cheap first** — bounded worker count; tables/edges ordered by estimated rows.
7. **Scan gates** — exact scans of unindexed FKs skipped by default (D7); warning above the
   big-table threshold with estimated rows, index state and timeout shown before start.
8. **Production flag on connections** — `SavedConnection.Production bool` (and `--production`
   / `SEEDSTORM_PRODUCTION` in the CLI):
   - Read defaults tighten: concurrency 1, relationships in `estimate` mode unless confirmed.
   - Writes (seed, gaps, mirror target, truncate, clone target, generate with a connection) are
     refused unless the user types the connection label (web) or passes `--allow-production`
     (CLI). The TUI asks for the label.
   - Badge in the connection pill / pickers.
9. **Replica aware** — Postgres `pg_is_in_recovery()`: shown as "replica"; a query cancelled by a
   replication conflict is reported per table/edge as `cancelled by server`, not a run failure.
10. **Visible in the UI/CLI** — every scan result carries its outcome (`ok`, `estimated`,
   `timed out`, `locked`, `cancelled`, `skipped: unindexed`) so partial data is never mistaken for complete.

Writes (seed/mirror inserts) do **not** use `ReadScope` or statement timeouts; they keep their own
path, gated only by the production flag.

### D1 Inline run status (web, all pages with jobs)
- Shared `runStatus` strip rendered next to the primary action (compare, workspace seed/gaps/
  generate/clone, mirror modal, run-form pages): phase label, `done/total · table`, progress bar,
  elapsed, Cancel. The job log stays collapsible below.
- `runJob` / `streamJob` always settle: catch `fetch` errors, and on `EventSource` error poll
  `GET /api/jobs/{id}` once; if still running reconnect, else call `onEnd`.
- Errors render in the strip (top of the page area, `role="alert"`), not only in the aside.

### D2 Connection preflight (web + CLI)
- Compare/mirror `connect` phase pings source and target **concurrently** with a timeout
  (`connectTimeout`), emitting a per-side event: `source: ok 12ms` / `target: unreachable (dial tcp …: i/o timeout)`.
- UI shows a chip per side (checking / ok / failed + message). Failure ends the job before counting.
- Count both sides concurrently after preflight (independent connections).
- `db.Introspect(ctx, dbType, dsn, onTable)` — ping with timeout; `Session.Schema` must not hold
  the session mutex across network I/O (single-flight instead).
- CLI: `signal.NotifyContext` in main; `openEndpoint` pings with a timeout and names the side.

### D3 Workspace fast return
- `/api/graph` returns structure only (cached schema) — instant.
- Counts load after the graph renders: estimate first (planner stats, instant), then exact
  counts streamed as a job with per-table progress (badges fill in). Session caches exact counts
  with `takenAt`; runs started from this server invalidate the affected tables. UI shows "counts
  from 3 min ago · Refresh".
- Cache node positions in sessionStorage keyed by session id + schema hash, skip dagre when they
  match.
- Introspection progress (first load / refresh schema): job with `introspect i/n · table`.

### D4 Kept form state
- `seedstorm.workspaceForm.v1` (per connection key): rows, writers, generators, batch, enum,
  self-ref, profile id, dry-run. **Not persisted:** truncate, disable-FK, clone drop (destructive
  toggles always start off).
- `seedstorm.compareForm.v1`: source, target, counts mode, scale, profile, mirror mode.
- A remembered profile/connection that no longer exists falls back to default with a notice.
- All reads/writes in try/catch; page works without storage.

### D5 Single-connection snapshots
- Web: "Snapshot counts" action on the workspace (active connection) → job with progress →
  export dialog (YAML/JSON, copy/download). Compare export dialog also offers "source only" without
  a target.
- Import in workspace: "Calibrate from file" → matches tables against the live schema:
  matched, file-only, db-only; per-table rows prefilled from the file (scale slider), run =
  mirror-from-snapshot (existing engine). No new insert path.
- CLI already has `snapshot`; add count progress logs.

### D6 Scan levels (introspect / snapshot / workspace)
One vocabulary on every surface — `--scan schema|counts|relationships` (each level includes the
previous one):
- `schema` — today's introspection: tables, columns, PK/FK, constraints, indexes. Default for
  `introspect`, workspace first load.
- `counts` — + row counts and sizes (`--counts exact|estimate`). Default for `snapshot`, compare.
- `relationships` — + relationship shapes for every FK edge (D7). Opt-in.
Web: workspace loads `schema` then `counts` (D3); "Analyze relationships" starts a background
`relationships` job; graph edges fill in with shape badges as each edge finishes.

### D7 Relationship shapes — capture (decided: exact, warned, non-blocking)
Per FK edge `child.col → parent.pk` (self-refs included):
- `parents` (row count), `children`, `nullShare` (nullable FK), `zeroShare` (parents with no child),
  and over parents with ≥1 child: `min`, `max`, `avg`, `p50`, `p95`, plus a **histogram**
  (exact buckets 1..16, then powers of two up to max). Percentiles are read from the histogram.
- Link tables (PK = two FKs): both sides (groups per user, users per group).
- Self-references: children per node and depth distribution (recursive CTE, capped at a max
  depth and cycle-safe; MySQL 5.7 walks levels from Go with the same cap).
- Every edge has its own shape, so a chain (org → users → user_groups → …) is covered to the last level.

One query per edge, aggregated in the database (no row transfer), same SQL on both engines incl.
MySQL 5.7:
```sql
SELECT bucket, COUNT(*) AS parents, MIN(c), MAX(c), SUM(c)
FROM (SELECT fk, COUNT(*) AS c FROM child WHERE fk IS NOT NULL GROUP BY fk) per_parent
GROUP BY bucket   -- bucket computed from c
```
Two modes, like counts (`--counts exact|estimate`):
- `estimate` — no scan, instant, marked `~`. Postgres `pg_stats`: `n_distinct` of the FK column →
  avg children per parent; `most_common_vals/freqs` → approximate max and the head of the
  histogram. MySQL: index `CARDINALITY` → avg only (max unknown).
- `exact` — the query above.
A max-only query (`GROUP BY fk ORDER BY c DESC LIMIT 1`) is **not** cheaper: finding the largest
group still counts every group. The cost is the scan/aggregate; the outer bucket GROUP BY over
per-parent counts is small. So exact always returns the full shape.

Index gate (decided):
- FK column is the leading column of an index → index-only / streaming aggregate, run it.
- No index → full table scan + hash aggregate: warn and skip by default, show the estimate
  instead; the user opts in per edge (web) or with `--scan-unindexed` (CLI).
- MySQL InnoDB always indexes FK columns; unindexed FKs are a Postgres case.

Non-blocking and performant:
- Plain SELECTs: Postgres AccessShare / InnoDB consistent reads never block writers.
- Runs as a background job, cancellable, results streamed edge by edge (UI stays usable, partial
  results kept on cancel). Concurrency capped (default 2 queries) so production is not hammered.
- Order: cheap edges first (estimated child rows from planner stats, already available via
  `db.GetEstimatedRowCounts`).
- Warn before running for big child tables (estimate over a threshold, e.g. 5M rows) even when
  indexed; unindexed edges follow the index gate above (`db.Table.Indexes`).
- Optional per-edge statement timeout (`statement_timeout` / `MAX_EXECUTION_TIME`): a timed-out
  edge is reported `timed out`, the run continues.
- Session-cached; saved in snapshot **version 2** (`relationships:`); version 1 still parses.
- Compare shows source vs target shape drift per edge.

### D8 Relationship shapes — seeding (decided: all FKs shaped, bounded)
Profile gets `relationships:` (`child.col: {min, avg, max, zeroShare, nullShare, histogram}`),
editable on the profiles page and importable from a snapshot.

Why every FK in a table can be shaped: without a UNIQUE across FK columns, the columns are
independent. Each shaped column gets its own degree multiset (sum = child rows) shuffled
independently, so several shaped FKs in one table are all exact.

Plan phase (before any generation, deterministic, never loops):
- Row counts are derived top-down in topological order:
  `rows(child) = parents × (1 − zeroShare) × avg / (1 − nullShare)`. Explicit `rows` wins.
  When a table's shaped FKs imply different counts, pick one (explicit > largest parent side),
  rescale the other edges' avg inside [min, max] and report the change.
- Feasibility checks with automatic adjustment + warning, never a retry loop:
  - `parents × min > rows` or `parents × max < rows` → clamp rows or min/max, report which.
  - Single-column UNIQUE on the FK (1:1) → max forced to 1.
  - Link table with UNIQUE pair: bipartite degree sequences must be realizable (Gale–Ryser check);
    if not, the closest realizable sequences are used and the drift is reported.
  - Near-cycle nullable edges generated before their parent: NULL on the first pass as today;
    shape reported as not applied.
- Dry run / plan shows per edge: target shape, adjustments, expected rows.

Generation:
- Degree drawn per parent from the histogram (exact shape, bounded [min,max]), children take keys
  from a shuffled window (bounded buffer), so rows stay streaming and memory flat.
- Parent pool ≤ `poolLimit`: degrees are drawn for the pool; when a parent table is larger,
  integer contiguous ids generated in this run are walked by range (exact), otherwise pool rounds
  are used and max is enforced per round (approximate — reported).
- Unique-pair link tables: pairs dealt from both degree sequences with a bounded repair (swap)
  pass; leftover pairs dropped, never an unbounded retry. Existing 200-attempt guards and
  `MaxRowFailures` / `maxZeroRounds` still end a stuck table with a partial result.
- Keeps: `--seed` reproducibility (randomSource, sorted order), gen-worker forks, chunking,
  composite-PK `seenKeys` guard, existing rows (top-up reads stored children per parent only when
  the edge is shaped; warned as a scan).
- After a run: achieved shape measured (same query as D7, on seeded rows) and printed next to the target.

Known limits (documented, warned):
- Correlation between FKs is not kept (users with many orders are not the ones with many reviews).
- Shapes over pool rounds on huge non-integer-key parents are approximate.
- Top-up into a populated table only shapes the added children.

### D10 Relationships in compare and export; production safety
Compare:
- Counts compare stays the fast default. "Compare relationships" is a second, explicit step on the
  results page (and `compare --scan relationships`): both sides scanned in the background (or one
  side from a v2 snapshot), per-edge drift view (avg / p95 / max / zeroShare source vs target).
- Mirror plan can then offer "shape like source" (feeds D8).

Export:
- One file format (snapshot v2); `relationships:` is optional. Export dialog / workspace snapshot
  / `snapshot --scan counts|relationships` choose whether to include it. A counts-only file stays
  small and instant; a v2 file without relationships is valid.
- Export from compare can include shapes only if they were scanned (no hidden scan on export).

Production safety: all relationship scans run through D0 `ReadScope` (read-only transaction,
short statements, server-side statement and lock timeouts, real cancel, concurrency cap, index
gate, production flag, replica awareness). Nothing relationship-specific bypasses it.

### D11 CLI / TUI feedback
- Shared throttled count/introspect progress logger (`cli/progress.go`) for compare, snapshot,
  mirror (`OnCount`), introspect, gaps scan, clone-schema, profile validate.
- clone-schema phase logs + per-statement progress; generate per-table lines.
- TUI seed/gaps: wire `OnProgress`/`OnTable` through a channel (as mirror does): rows, rate, ETA,
  elapsed; surface `SyncSequences` errors.
- TUI mirror: preview in a `tea.Cmd` with loading state; `onTruncate` wired; partial results
  printed on error/abort. Clone: spinner + phase.

### D15 Reliability: failure isolation, panics, "what failed", reattach
Checked in code (2026-09-17): **there is no `recover()` anywhere** in `internal/` or `cmd/`.
Goroutines that can panic: the job runner (`web/jobs.go:107`), writer workers
(`seeder/writer.go:73`), parallel generators (`seeder/seed.go:220`), Fill pieces
(`seeder/seeder.go:329`), the COPY CSV writer (`db/copy.go:41`). `net/http` recovers panics only
in the handler goroutine, so **a panic in any of these kills the whole `serve` process**: every
live connection session, every running job in every tab, the HTTP server. The browser only sees
the stream drop (and with the SSE bug, stays stuck). There is no `GET /api/jobs` list and no SSE
keepalive: after a tab change or reload a running job is invisible, and "slow" cannot be told from "dead".
CLI `main` prints `error: …` and exits 1 for every failure (`cmd/seedstorm/main.go:15`). TUI runs
Bubble Tea v1.3.10: it recovers panics in `Update`/`View` and in `Cmd` goroutines and restores the
terminal, but prints a raw stack to stdout; panics in goroutines the seeder starts inside a Cmd are
not recovered (see assumption 10).

Design:
1. **Panic isolation** — one helper (`internal/safego`): `Go(name, fn)` / `Run(name, fn) error`
   recovers into `PanicError{Where, Value, Stack}`. Every goroutine above uses it. A panic in a
   writer or generator cancels that run's context (errgroup semantics) and ends the run `failed`;
   the server, other jobs and other sessions keep going. HTTP middleware recovers handlers into a
   JSON 500 `{error, errorId}` (today the connection is just dropped).
2. **Every failure says where** — `RunError{Side (source|target), Phase (connect|introspect|count|
   plan|truncate|generate|write|sync), Table, Chunk, Cause, ErrorID}` built by the seeder/compare/
   db layers (not by surfaces). Web strip, CLI and TUI render the same fields:
   `Failed · target · write · orders (chunk 3/12): duplicate key value violates "orders_pkey"`.
   Panics show `Internal error in <phase>/<table> (id 7f3a…)`; the stack goes to the server log /
   `--log-level debug`, never to the UI.
3. **What happened before the failure** — failed and cancelled runs always return partial results:
   tables finished, rows written per table, tables not touched, sequences synced or not. Shown in
   the web result, printed by the CLI, kept on the TUI result screen (mirror TUI loses them today).
   Includes a next step where one exists ("re-run with Fill gaps to continue", "check the target
   connection", "table locked by another session").
4. **Cleanup on every exit path** — run pools closed, per-server budget released, `KILL QUERY` on
   cancel, pinned MySQL conns discarded, progress timers stopped: all in `defer`, covered by a test
   that checks `pg_stat_activity` / PROCESSLIST returns to its baseline after a panic.
5. **Jobs are findable again** — `GET /api/jobs?scope=session` lists running and recent jobs
   (name, status, phase, progress, started, error summary). Each page stores its last job id and
   reattaches on load (SSE `?after=seq`), so leaving the workspace mid-seed and coming back shows
   the run still going. Each response carries a server boot id: after a restart the UI says
   "server restarted — job <name> was lost", instead of spinning forever.
6. **Slow vs dead** — SSE keepalive comment every 15s. Client states: `running` (updates
   arriving), `quiet` ("no update for 30s — last: counting orders; still connected"),
   `reconnecting`, `lost` (server gone / restarted). Long waits say why when the database knows:
   Postgres `wait_event_type = Lock` / MySQL `Waiting for table metadata lock` → "waiting on a
   lock held by session <pid>", not a failure.
7. **Frontend never fails silently** — one `api()` helper for every fetch (AbortController
   timeout, non-JSON guard, error into the nearest status area); global `error` /
   `unhandledrejection` handlers show a dismissible notice ("A page error happened; running jobs
   are not affected. Reload is safe.") and log the details to the console.
8. **CLI / TUI** — `main` recovers panics: `internal error in <command>: <value> (run with
   --log-level debug for the stack)`, exit code **70** (internal), 1 (run failed), 130 (interrupted);
   partial summary printed first. TUI: failures render in the view with the same fields; the
   partial result screen stays until a key press. Seeder goroutines are covered by `safego` (item 1),
   so their panics become a `RunError` message in the view instead of a crash. Bubble Tea's own
   recovery stays on for panics in our `Update`/`View`/`Cmd` code; the TUI entry points map
   `tea.ErrProgramPanic` to exit 70 with the friendly message (the raw stack Bubble Tea prints is
   kept only with `--log-level debug`, by running with `tea.WithoutCatchPanics()` plus our own
   recover that restores the terminal via `p.Kill()`/`ReleaseTerminal`; otherwise accepted as is).

Fault injection for tests (no mocks of internal code): `internal/faultinject`, compiled only with
`-tags faultinject`; points `generate`, `write`, `copy`, `job`, `introspect`, each with
`panic|error|hang`, selected by `SEEDSTORM_FAULT=write:orders:panic`. The default build contains
no injection code (checked by a test that greps the default binary's symbols).

### D16 Partitioned tables (Postgres) — found by the probe
- Introspection reads `pg_class.relkind = 'p'` (partitioned parent) and `relispartition`
  (partition) plus bounds via `pg_get_expr(relpartbound, oid)`.
- Graph, counts, compare, snapshot and seeding treat the **parent** as the table; partitions are
  hidden (listed under the parent in the detail panel) and never counted separately.
- Seeding a partitioned parent: the partition key column is generated **inside the union of the
  partition bounds** (RANGE: min/max of bounds; LIST: one of the listed values; HASH: any value;
  DEFAULT partition present: unconstrained). Unsupported bound shapes (multi-column keys,
  expressions) → the table is refused before generation with a clear message
  ("events is partitioned by an expression; add a value rule for created_at") instead of failing
  on the first insert. A profile value rule on the key column always wins.
- MySQL partitioning is transparent to INSERT (rows are routed) and `information_schema` lists one
  table → no change; verify once in loadsim.
- Tests (integration, pg13/15/17): range, list, hash, default partition; counts equal the parent
  count; seed succeeds and every row lands in a partition; expression key → refused before writing.

### D12 Tuning recommendations
Today: writers default 4 (`seeder.DefaultWorkers`), capped at 32 (`web/runners.go:720`); generators
capped at `runtime.NumCPU()`; chunk 32MB; batches ≤ 1MB (`db/insert.go:95`). Nothing looks at the
database's free connections or the machine's memory.

Inputs — detected where possible, asked only when not:
- **seedstorm host (detected):** usable cores (`runtime.GOMAXPROCS`, cgroup-aware in Go 1.25),
  memory limit (cgroup `memory.max`, else `/proc/meminfo` / OS total). Shown, editable.
- **Database (detected over SQL, D0 read scope):** engine/version, `max_connections` and
  connections in use, replica/production flag, MySQL `max_allowed_packet`.
- **Database server (asked, not visible over SQL):** vCPU, memory, **storage type** (local NVMe /
  SSD / network SSD e.g. gp3 with optional provisioned IOPS / HDD) and **storage size**, plus
  "shared with other workloads?". Presets (small / medium / large) and "don't know" (conservative).
- **Database used space (detected):** Postgres `pg_database_size`, MySQL `data_length +
  index_length`. Free disk is not readable over SQL, so free = storage size − used (stated as
  approximate).
- **Run shape:** total rows (from rows/profile/plan), average row bytes (from the schema), engine
  write path (Postgres COPY vs MySQL batched INSERT).

Rules (pure function `internal/tuning`, grounded in docs/benchmarks.md, each value with a reason):
- Calibration note (probe 2026-09-17, cloudsql-micro MySQL at 300 IOPS): 1/2/4 writers →
  101/87/76s. The rule constants below are starting points, fixed only after the measure job.
- **Writers** = min(DB vCPU (×2 when not shared), free connections − headroom (max(5, 20% of
  `max_connections`)), memory-based cap (MySQL: (memory − buffer pool − log buffer − overhead) /
  per-writer estimate; Postgres: (memory − shared_buffers) / (work_mem + ~10MB per backend)),
  storage cap, 32). Connection limits alone are not trusted: Cloud SQL MySQL micro allows 280
  connections on 629MB. Production flag: min(that, 4) and halved when shared.
- **Generators:** 1 unless the target can take more than one core produces (~450k rows/s:
  Postgres COPY with ≥ 8 writers on a non-shared DB); then ≤ host cores − 1, and memory-capped
  (each generator ≈ one chunk).
- **Chunk bytes:** (generators + 2) × chunk ≤ 25% of host memory limit; default 32MB when it fits.
- **Batch:** bytes ≤ min(1MB, `max_allowed_packet` / 4) on MySQL.
- **Storage type caps writers** (writes wait on fsync/WAL, not CPU): HDD ≤ 2; network SSD ≤
  IOPS-based cap (≈ provisioned IOPS / 500, min 2) when IOPS given, else ≤ 4; local SSD/NVMe no
  extra cap.
- **Storage size → growth check:** expected growth ≈ rows × avg row bytes × (1 + index factor from
  the schema's indexes) + WAL/binlog headroom (×1.5 while the run is in flight). Warn at > 50% of
  free space, **refuse to start above 90%** unless confirmed (typed label on production).
- **Truncate:** MySQL stays on one connection regardless (existing rule, explained).

Surfaces:
- Web: "Recommend" in the Tuning panel → dialog (detected host + DB, size inputs, recommended
  values with one-line reasons, **Apply**). Remembered per connection (D4). Values stay editable.
- CLI: `--workers auto` / `--gen-workers auto`, and `seedstorm tune --dsn …` printing the table.
- TUI: tuning step pre-filled with the recommendation and its reasons.

Warning always shown: "Estimates from benchmarks on one machine; a remote or busy database changes
them. Watch the first minute's rate." Rate/ETA from `seeder.Meter` is shown next to it during the run.

Hard safeguards (enforced at run start, not only advised):
- Writers are clamped to free connections − headroom (re-read at start); the run logs the clamp.
- Chunk bytes clamped so the queue fits under the memory limit.
- Disk growth check above 90% of free space refuses to start without confirmation (only when a
  storage size was given; otherwise a warning that it could not be checked).
- Advice alone is not a safeguard; the clamps are.

Later (not in this PR): adaptive `auto` that raises writers while rows/s keeps improving and backs
off when per-batch latency grows.

### D13 Resource-limited test harness (loadsim)
Spike on this host (2026-09-17: Docker, cgroup v2, overlay2 on btrfs, NVMe):
- `--cpus=2 --memory=512m` → container sees `cpu.max 200000 100000`, `memory.max 536870912`,
  but `nproc` still says 16. Go 1.25 `runtime.GOMAXPROCS(0)` follows the CPU quota;
  `runtime.NumCPU()` does not. **Bug found:** `requestGenWorkers` caps at `runtime.NumCPU()`
  (`web/runners.go:724`) → 16 generators allowed in a 2-CPU container.
- Storage size: tmpfs `tmpfs-size=16MB` → `No space left on device` at 16MB. Works; tmpfs is RAM
  (charged to the container's memory limit) so it simulates *full*, not *slow*.
- Slow storage: `--device-write-iops /dev/nvme0n1:50` → 200 direct 4k writes took 3.9s (applied).
  `--device-write-bps …:5mb` → 20MB written in 0.01s direct / 0.12s with fsync (**not** applied
  reliably on btrfs+overlay2). Use IOPS limits; re-check bps on GitHub runners (ext4) before relying on it.
- `--storage-opt size=` unavailable (overlay2 needs xfs+pquota).

Harness:
- `integration/loadsim_*_test.go`, build tag `loadsim`, `make test-loadsim`. Throwaway containers on
  spare ports (never the compose ones), unique `ss_loadsim_*` DBs, always removed in `t.Cleanup`.
- Profiles (DB container), modelled on managed Cloud SQL shapes:
  - `cloudsql-micro` — 1 vCPU / 629MB (`--cpus=1 --memory=629m`): the cheapest shared-core
    instance (db-f1-micro, 0.614 GiB). Shared-core CPU on Cloud SQL is a burstable fraction of a
    core; `--cpus=1` is the optimistic side. The measure job also runs `--cpus=0.5` as the
    pessimistic side (throughput only, no gating).
  - `cloudsql-2vcpu` — 2 vCPU / 7.5GB (`--cpus=2 --memory=7680m`): db-custom-2-7680, the common
    2-vCPU default shape. Newer N2/N4 series use 1 vCPU : 8GB (2 vCPU / 16GB) — does not fit next to
    the binary on a 16GB runner, so it runs only locally / in the measure job when memory allows.
  - `medium` 4 vCPU / 15GB — local only (does not fit a standard runner alongside the binary).
  - Server settings that Cloud SQL derives from memory are set explicitly per profile, not left
    to container defaults, recorded in `integration/loadsim_profiles.go` with source + date.

  Reference instance (read 2026-09-17, Cloud SQL console): **MySQL 8.4.10, Enterprise edition,
  1 vCPU, 628.74 MB, 10 GB SSD.**

  | Setting | cloudsql-micro (1 vCPU / 629MB, 10GB SSD) | cloudsql-2vcpu (2 vCPU / 7.5GB) | Source |
  |---|---|---|---|
  | PG `max_connections` | 25 | 400 | Cloud SQL PG flags table (tiny ≈0.5GB → 25; 7.5–15GB → 400) |
  | PG `shared_buffers` | 33% of memory ≈ 207MB | ≈ 2.5GB | Cloud SQL PG memory best practices |
  | PG `effective_cache_size` | 40% ≈ 251MB | ≈ 3GB | Cloud SQL PG flags |
  | PG `work_mem` / `maintenance_work_mem` / `temp_buffers` | 4MB / 64MB / 8MB | same | Cloud SQL PG docs |
  | MySQL settings | see the reference block below (read from the instance) | not published, no instance → **estimated** (buffer pool ~72% ≈ 5.4GB per docs; other values as micro) | micro: `SHOW VARIABLES` on the reference instance; 2vcpu: Cloud SQL MySQL memory docs |
  | Storage | 10GB SSD: **300 IOPS, 4.8MB/s** (Cloud SQL storage doc: 30 IOPS/GB, 0.48MB/s per GB) | 100GB assumed → 3000 IOPS | Cloud SQL storage options |

  MySQL reference values (`SHOW VARIABLES`, 2026-09-17, MySQL 8.4.10, 1 vCPU / 628.74MB), passed to
  the container as `mysqld` flags:
  ```
  --innodb-buffer-pool-size=53477376      # 51MB — only ~8.5% of memory
  --innodb-flush-log-at-trx-commit=1
  --innodb-flush-method=O_DIRECT
  --innodb-io-capacity=5000 --innodb-io-capacity-max=10000
  --innodb-log-buffer-size=67108864       # 64MB, larger than the buffer pool
  --innodb-redo-log-capacity=104857600    # 100MB
  --max-allowed-packet=33554432           # 32MB
  --max-connections=280
  --performance-schema=OFF
  --table-open-cache=4000 --thread-cache-size=10
  --tmp-table-size=16777216 --max-heap-table-size=16777216
  --sort-buffer-size=262144 --join-buffer-size=262144
  ```
  What this means for seedstorm (feeds D12 rules and loadsim assertions):
  - **`max_connections` is not a capacity signal on small Cloud SQL MySQL.** 280 connections on
    629MB: each connection thread costs ≈1MB stack + per-query buffers, so ~280 busy writers would
    exhaust memory long before the connection limit. The recommender caps writers by **memory**
    (≈ (memory − buffer pool − log buffer − ~150MB server overhead) / per-writer estimate) and by
    storage IOPS, and uses free connections only as an upper bound.
  - **Tiny buffer pool (51MB) + 300 IOPS:** inserts into indexed tables leave the buffer pool
    quickly and hit disk. Initial expectation was 1–2 writers; the probe measured 4 writers still
    faster than 2 (76s vs 87s at 300 IOPS), so the recommendation comes from the measure job
    (writers 1/2/4/8 on this profile), not from this guess.
  - `innodb_io_capacity` 5000 is far above the disk's 300 IOPS: background flushing can fall
    behind under sustained inserts → watch for stalls; the "stuck" detector must not fire on a
    slow-but-progressing run (loadsim assertion 6 on this profile).
  - `max_allowed_packet` 32MB: the existing 1MB batch cap is well inside it; no change.
  - Growth check: 10GB disk minus data minus 100MB redo; binlogs (point-in-time recovery) also
    use this disk on Cloud SQL → headroom factor kept at ×1.5.
  - `innodb_flush_method=O_DIRECT` is not supported on tmpfs: only the `full` (tmpfs) variant
    overrides it to `fsync`, and says so; every other test keeps O_DIRECT like the instance.

  Storage note: Compute Engine docs describe PD-SSD as 6,000 baseline IOPS + 30/GiB, which
  contradicts the Cloud SQL page for small disks. The profile uses the Cloud SQL numbers (the
  pessimistic side, `--device-write-iops 300`); the measure job also runs without the IOPS cap as
  the optimistic side. Byte-rate throttling did not apply on the dev host, so 4.8MB/s is only
  enforced where the probe shows bps throttling works.

  PG profiles have no reference instance: values come from the documentation above and are
  marked `source: docs` in the profile file.
- Storage
  variants `fast` (default volume), `slow` (`--device-write-iops`), `full` (tmpfs of a fixed size),
  `few-connections` (`-c max_connections=20` / `--max-connections=20`).
- The seedstorm binary also runs inside a limited container (`--cpus`, `--memory`), mounted read-only.
- Resource discipline: one profile at a time, skip with a message when `free -m` available < 4GB.

Deterministic assertions (can gate PRs, small sizes, no timing):
1. Detection: binary in `--cpus=2 --memory=512m` → `seedstorm tune` reports 2 cores / 512MB;
   `--gen-workers 16` is clamped to 2 (fails today — TDD for the NumCPU bug).
2. Disk full: Postgres/MySQL data on a 256MB tmpfs, seed past it → run fails fast with a clear
   "database out of disk" error naming the table, exit non-zero, no hang; with `--db-storage 256MB`
   the growth check refuses before writing.
3. Connection cap: `max_connections=20` with 5 already held → `--workers 50` clamps and completes.
4. seedstorm memory bound: binary under `--memory=256m` seeds a wide 1M-row run → exit 0, not 137 (OOM).
5. Database dies mid-run (`docker kill` after first progress line) → CLI exits with a connection
   error within the connect timeout; web job ends `error`; nothing hangs.
6. Slow storage (50 IOPS) → run completes and progress lines keep coming (no false "stuck").
7. D0 safeguards on a `cloudsql-micro` DB: statement timeout and cancel behave as on unconstrained servers.

Measurements (not assertions; manual / nightly `workflow_dispatch`):
- Matrix profile × writers × generators → rows/s, peak memory, DB CPU (`docker stats`) → a
  table in docs/benchmarks.md, with the reproduce command.
- Recommender check: the recommended writers for each profile land within ~20% of the best
  measured rows/s and never in the region where rows/s drops. Rules in D12 are adjusted from this data.

MySQL on tmpfs: InnoDB `O_DIRECT` is not supported on tmpfs — set `--innodb-flush-method=fsync`
explicitly (to verify per version, 8.4 changed the default).

### D14 More than one database, instance or run
Found while reviewing the plan: most sections assume one run against one database on its own server.

- **Source and target on the same server (different databases).** Common on Cloud SQL (one
  instance, `app` and `app_staging`). `db.Identity` refuses only the *same database*; today it
  already reads the server part (`@@server_uuid`, `pg_postmaster_start_time()`), so add
  `db.ServerIdentity` and mark the pair **shared server**: the tuning budget (writers, memory,
  IOPS) is split between the scan on the source and the writes on the target, the relationship
  scan on the source runs with concurrency 1 during a mirror, and the UI says "same server".
- **Concurrent jobs in one `serve`.** `jobs.Manager.Start` has no limit (`web/jobs.go:92`): two tabs
  can start two seeds (or a seed and a relationship scan) on the same server, each clamping
  writers as if alone. Add a per-server budget (keyed by `ServerIdentity`): writers and scan
  concurrency are reserved at job start and released at the end; a second job gets what is left
  and says so ("2 writers: another run on this server holds 2"). Not across separate processes
  (documented).
- **High availability / regional instances.** Synchronous replication lowers write IOPS/throughput
  (Cloud SQL storage docs: regional SSD write numbers are lower). Tuning input "HA / regional"
  halves the storage cap; not detectable over SQL, asked in the dialog.
- **Read replicas.** Relationship and count scans are best pointed at a replica (`pg_is_in_recovery()`,
  MySQL `@@read_only` / `@@super_read_only` shown). Seeding a primary with replicas: replica lag
  grows with write rate → production-flagged runs log a note; lag itself is not measured (needs
  replica access).
- **Connection poolers / proxies** (Cloud SQL Auth Proxy, Managed Connection Pooling, PgBouncer,
  ProxySQL): `max_connections` read through a pooler is the server's, not the pool's. D0 uses only
  transaction-scoped settings on Postgres (`SET LOCAL`) — safe with transaction pooling. MySQL
  session variables (`max_execution_time`, `lock_wait_timeout`) need a pinned connection → use the
  per-statement optimizer hint `/*+ MAX_EXECUTION_TIME(n) */` for SELECTs and set session values
  inside the pinned conn only; document that multiplexing proxies may drop session variables.
- Loadsim: assertion 8 — two concurrent seeds on one `serve` against the same server never exceed
  the budget (count sessions from `pg_stat_activity` / PROCESSLIST); assertion 9 — mirror between
  two databases on one server is marked shared and uses the split budget.

## Code review corrections (2026-09-17 — authoritative where a D-section says otherwise)

Deep read of faker/seeder/graph, db/compare/rules/profiles/CLI/TUI, web/static/templates/e2e/
integration/CI. Each item names what changes in the design.

### D0 safeguards
- **MySQL cancel confirmed from driver source:** go-sql-driver/mysql v1.9.3 only closes the socket
  on ctx cancel (no KILL) → `KILL QUERY` is required, no spike needed. pgx v5.7.6 sends a
  CancelRequest (best effort) and discards the connection.
- **No MySQL session variables on pooled connections:** they survive commit and would leak into
  later faker scans. Statement timeout = per-SELECT hint `/*+ MAX_EXECUTION_TIME(ms) */`;
  `lock_wait_timeout` only on a dedicated `sql.Conn` that is closed, never returned to the pool.
  (MySQL estimate path already pins a Conn and sets a session var, `stats.go:76-82` — same fix.)
- **CLI signals:** `signal.NotifyContext` with a short grace window so `KILL QUERY` runs before
  exit; second Ctrl+C forces exit. KILL may fail behind multiplexing proxies (reported).
- **Error classification** in a new `db.ReadOutcome(err, ctx)`: Postgres 57014 (timeout vs cancel via
  `ctx.Err()`), 55P03 lock not available; MySQL 3024 timeout, 1317 killed, 1205 lock wait. Do not
  reuse `IsTransient` (it treats 40001/1205 as retryable → retry loops).
- **Scope additions:** faker preload scans (`scanPKs`, `existing.go:197,233`) and table preview
  COUNT (`handlers_api.go:230`) get ctx; preloads get **no** statement timeout (seeding must not
  break). `SyncSequences` (setval) and clone DDL are writes → outside `ReadScope`.
- **Estimate mode:** `compare.Take` falls back to exact COUNT(*) for every estimate ≤ 0
  (`compare.go:89-97`) — on production connections and in explicit estimate mode, a zero/missing
  estimate becomes *unknown* instead of a scan.
- **Production flag scope:** saved connections **and** ad-hoc DSN sessions (CLI `--production`),
  TUI, web gaps and preview paths. Web guard: `Session.SavedID` set in `resolveConnection` /
  `handleConnectSaved` + connection-key match (sessions are reused by DSN, `session.go:90`), including
  raw clone targets (`targetDsn`, `runners.go:178-189`). Checked **synchronously before
  `jobs.Start`** (409 with the reason), not inside the job. Write endpoints: `/api/seed`,
  `/api/gaps` (fill), `/api/mirror`, `/api/clone-schema`. Not writes: generate, export, enrich.
  `Save` replaces the whole record (`store.go:164`) → carry `production` through `connectForm`/
  `saved()`; un-flagging requires the typed label.

### D1 run status
- Fix the SSE root cause: rename the server event (e.g. `failure`), send SSE `id:` = seq and
  support `?after=seq` on reconnect; read job state via `job.State()`.
- `createRunView(root)` in app.js exported through `window.seedstorm.ui`: per-root elements and
  timer, so several jobs render at once. Strip locations: workspace `.ws-run-wrap`
  (`workspace.html.tmpl:238`), compare inside `#cmp-form` next to `#cmp-run` (outside
  `#cmp-results`), run-form pages via the `joblog` partial (`layout.html.tmpl:99`). The mirror modal
  closes before the run, so its run shows in the compare strip.
- Job eviction in `jobs.Manager` (finished jobs older than N minutes / max count).
- Strip must fit 320px (`mobile.spec` asserts the action bar does not overlap).

### D2 introspection
- Signature `Introspect(ctx, conn *sql.DB, dbType, onPhase)` built on the existing
  `introspectWithConn` (`clone.go:258`); progress has phases (global catalog queries run before the
  per-table loop). Cache raw `[]db.Table` (with indexes) in the session; `RawTables` stops
  re-introspecting on every call. `Session.Schema` stops holding `s.mu` across network I/O
  (single-flight).

### D3 workspace
- There are no visible count badges: counts drive node borders, `isPopulated` (gaps auto-parents,
  access warnings), `selectEmpty`, stats, ignored tab, minimap. `/api/graph` keeps its fields and
  returns `counted:false` until counts arrive; **estimates never set `counted`** (they would feed
  `isPopulated`).
- Counts load is single-flight per session (not a new job per visit — jobs never evicted today).
- Layout cache key: `connectionKey` + schema hash + canvas size bucket, in localStorage (session
  ids change on restart; sessionStorage is per tab). Connection key rendered server-side as
  `data-conn-key` in the layout.
- Skip `loadCloneTargetAccess` unless clone mode is active: it resolves saved connections, which
  opens and pings them (up to 5s) and silently creates live sessions on every workspace load.

### D4 form state
- Compare `mode=reset` is destructive → not persisted. Precedence: URL > stored > default.
- Restore after profile options load (async); then call `loadProfileInsights`, `syncTuningSummary`,
  `refreshSelectionUI` (no change event fires on programmatic set).
- Move `readStore`/`writeStore` from compare.js into `ui` (a copy would trip dupehound).
- localStorage size: reports/imports already store whole documents; relationship shapes add to it
  → per-key size cap with graceful fallback.

### D5 snapshots
- New `/api/snapshot` job (source-only; `/api/snapshots/encode` needs a report). "Calibrate from
  file" hands off to `/compare?source=snap:<id>&target=id:<session>` instead of a new plan UI.

### D6 scan levels
- `introspect` writes schema YAML and `snapshot` writes a counts file: one `--scan` vocabulary does
  not fit both. Decision: `introspect` keeps its output and gains `--relationships <file>` (writes a
  v2 snapshot with shapes only); `snapshot` gains `--relationships`; `compare --relationships`
  adds the shape step. `--counts exact|estimate` stays as is. The web keeps "levels" as UI wording only.

### D7 relationship capture
- **Index gate:** introspection excludes PK indexes, partial indexes and single-column unique
  indexes (`postgres.go:414,436,437`; `mysql.go:364,379`), and `schema.Schema` drops most index
  data → a link table's `PK(user_id, group_id)` would look unindexed. Add `db.LeadingIndexed`
  (catalog query including PK/unique/partial, leading column only) for the gate.
- **Same SQL on both engines is not possible** (no shared LOG2): bucket with a `CASE` ladder.
  An index does not guarantee a cheap aggregate (visibility map, planner) — the server-side
  timeout is the real guard.
- **Estimate mode:** Postgres `pg_stats.null_frac` → nullShare; `parents − n_distinct` → zeroShare;
  MCV → approximate max (unknown when MCV list is empty). MySQL cardinality only when the FK is the
  leading column of an index.
- **Scope v1:** single-column FKs only. Composite FKs are reported as "not captured" until
  introspection represents them (M0.5 fixes the Postgres query; a composite model is a larger change).
- Postgres partitioned tables: parents and partitions both appear (inference) → capture on
  partitions only / parent only, not both; counts doubled today (check in M0.5).
- Hot standby: `n_live_tup` is 0 → estimates rely on `reltuples`.
- Graph: self-reference edges are not drawn (`handlers_api.go:129`) and edge ids are ordinal →
  key shapes by `child.column`; edge badges are new styling (no labels today).

### D8 shaped seeding (scope reduced by the code — see "What cannot be shaped in v1")
- **Pick site:** wrap `generateValue`'s FK pick for standard and enum rows only.
- **Slots:** retries (`faker.go:343`, `rollbackLastRowPKs` 458) and UNIQUE drops (`stream.go:269`)
  must return dealt slots; otherwise the achieved sum is ≤ target and is reported as such.
- **Degree state** lives in `Stream`, keyed by `child.col`, copied in `ForkTable` and merged in
  `MergeTable` (`stream.go:347,378`). Never mutate shared pool backing arrays (forks share them;
  `nextSequentialPK` relies on "largest id last", `existing.go:281`).
- **Row derivation** is a pure plan function run before `Seed`/`RenderPlan`, passed as its own
  option — **not** `tableRows` (that disables enum coverage). `RenderPlanWithCounts` and
  `seedTally` read the same derived map. Mirror never derives rows (counts come from the source).
- **Enum top-up:** exact sums only when top-up is off; otherwise deal to the requested rows and give
  top-up rows uniform picks (reported).
- **nullShare** is a new draw (today nullable FKs are never NULL when a pool exists).
- **Parents above `poolLimit`:** exact range walk only on the same Stream, single integer PK, no
  UNIQUE drops. On Fill/mirror (new Stream per table, parents read from the DB) and for parents
  with stored rows: approximate, reported.
- **Top-up into populated tables** must not read per-parent counts into memory (CLAUDE.md gotcha 9):
  bucketed counts only.
- **What cannot be shaped in v1** (reported per edge as "not shaped", with the reason):
  - self-references (per-chunk backfill references only rows in the same chunk);
  - composite FKs and parents with a composite PK (no parent identity in the pool);
  - junction tables whose PK is all FKs *until* the odometer enumeration is replaced by a
    Gale–Ryser dealer (planned in M7; if it misses the M7 exit criteria, these stay "not shaped");
  - UNIQUE groups containing an FK column: shaped with drops reported (achieved ≤ target).
  This narrows the earlier decision "every FK can be shaped" — the independence claim holds only
  for single-column FKs outside composite PKs / UNIQUE groups.

### D12 / D14 tuning, clamps, identity
- Clamp enforced once inside `seeder.Seed`/`Fill` plus `SetMaxOpenConns(writers + generators + 1)`
  on the run pool; truncate and pool-redraw connections counted. Web gaps (shared session pool),
  TUI (hardcoded workers) and CLI (no caps) go through the same path.
- **Replica vs primary passes `refuseSameDatabase`** today (different server ids) — the exact
  setup D14 recommends for scans. Add a cluster identity check (Postgres
  `pg_control_system().system_identifier`; MySQL replication source/group ids) — privileges on
  managed services to be checked in a spike; if unavailable, warn instead of refusing.
- Postgres identity includes postmaster start time → changes on restart (budget keys reset; fine).
  ProxySQL read/write split can route the two identity queries to different backends (documented).

### D13 loadsim / tests
- Build tags `integration || loadsim` on shared helper files; profile table in
  `loadsim_profiles_test.go` (a non-test file would create a real package).
- Binary for containers built with `CGO_ENABLED=0` (`binary_test.go:36` builds with CGO).
- Memory inside a container: `memory.events` `oom_kill` / `State.OOMKilled` for OOM (not `memory.peak`, which includes page cache and sits at the limit on healthy runs); `memory.stat` anon for process memory; not VmHWM polling.
- Job terminal status is `failed` (not `error`) in assertions.
- Loadsim adds ~2h of runner time per push → path filters (`internal/seeder`, `internal/faker`,
  `internal/db`, `internal/tuning`, `integration/loadsim*`) plus always on the final pre-merge run.
- e2e: saved-connection store is shared across specs → production-flag specs clean up; stale
  embedded assets when `SEEDSTORM_E2E_BIN` points at an old binary.

### Docs (same PR)
README: Web UI, Compare & mirror, Seed profiles. docs/commands.md: introspect, seed, gaps,
clone-schema, compare, mirror, snapshot, serve, new `tune` (+ TOC). docs/profiles.md:
`relationships:`. docs/development.md: evals table, e2e table, CI table (fix), env vars
(`SEEDSTORM_PRODUCTION`), Makefile targets (`test-loadsim` also in `.PHONY`). docs/benchmarks.md.
CLAUDE.md: structure (new files), CI table, compose file name. structlint needs no change.

### Assumptions list (updated)
1. ~~MySQL query keeps running after driver cancel~~ — confirmed from driver source.
2. ~~Go 1.25 `GOMAXPROCS` follows the container CPU quota~~ — **checked 2026-09-17** (static probe
   in `docker run --cpus`): host `NumCPU=16 GOMAXPROCS=16`; `--cpus=2` → `NumCPU=16
   GOMAXPROCS=2`; `--cpus=0.5` → `GOMAXPROCS=2`. Go rounds the quota up with a **minimum of 2**, so
   host detection reads cgroup `cpu.max` for the real (fractional) quota and uses `GOMAXPROCS` only
   as the upper bound for generators. `cloudsql-micro`-sized seedstorm containers (1 CPU) would
   otherwise be reported as 2 cores.
3. Byte-rate throttling on GitHub runners — probe.
4. ~~MySQL 8.4 under 629MB with the reference flags~~ — **checked 2026-09-17** (throwaway
   `mysql:8.4`, `--cpus=1 --memory=629m --memory-swap=629m`, all reference flags, 28-table
   `integration/schema_mysql.sql`, current `main` binary built `CGO_ENABLED=0`):
   - Starts fine; idle `memory.peak` 368MB.
   - `seed --rows 5000 --truncate` (local NVMe, no IOPS cap): workers 1 → 29.6s, 4 → 14.9s,
     8 → 14s (4 more runs, all exit 0). DB `memory.peak` reached the 629MB limit (page cache
     reclaimed: `memory.events max 24`, `oom_kill 0`). seedstorm RSS 95–151MB.
   - **One 8-writer run exited 1 after 6s and did not reproduce in 4 retries; its error text was
     lost** (output truncated in the probe). Unexplained intermittent failure → loadsim runs each
     cloudsql-micro scenario repeatedly and keeps full logs as artifacts.
   - With the disk capped at the instance's 300 IOPS (`--device-write-iops/--device-read-iops`),
     `--rows 2000 --truncate`: workers 1 → 101s, 2 → 87s, 4 → 76s; DB peak 311–447MB, no OOM.
     **4 writers still beat 2** on this shape → the D12 expectation "1–2 writers" was too
     pessimistic; the rule is calibrated from the measure job instead of fixed in advance.
   - For OOM assertions use `memory.events` `oom_kill` / `State.OOMKilled`, not `memory.peak`
     (it includes page cache and sits at the limit on a healthy run).
5. Runner size — probe.
6. ~~SSE named `error` event closes the stream in Chromium~~ — **reproduced 2026-09-17** in
   headless Chromium 153 (Playwright 1.63 from `e2e/node_modules`) with the exact bytes of
   `writeSSE` and the `app.js:713` handler: sequence `log, status:failed, error:<msg>` then
   `onerror(readyState=1)` → stream closed, **`end` never received**. Same stream with the event
   renamed `failure`: `log, status:failed, failure:<msg>, end` → handled. Fix confirmed as designed
   (D1); the e2e regression test is still written first in M0.5.
7. Cluster identity queries allowed on Cloud SQL roles — spike.
8. ~~Whether gauntlet runs unit tests in CI~~ — **checked 2026-09-17:** gauntlet's Go stack gates
   are `gofumpt, govet, golangci, gotest, gobuild` (`internal/stack/stack.registry.go:13`, v0.1.0
   module source; CI pins v0.1.1), and `gotest` runs `go test -json -race ./...`, whole-run, never
   diff-suppressed. **Unit tests do run in CI** (inside the `gauntlet` job; the old `test` job was
   folded into it in #38). No `unit` job needed; CLAUDE.md/development.md CI tables still need fixing.
9. ~~Postgres partitioned tables doubling counts~~ — **checked 2026-09-17** (throwaway
   `postgres:17-alpine`, `events` partitioned by range on `created_at` with 2 partitions, 1000 rows):
   - `snapshot` (exact and estimate) and `introspect` list `events`, `events_2025` and
     `events_2026` as three tables → row totals doubled (2000 for 1000 rows); all three carry the FK.
   - **`seed` fails:** `insert into events failed: ERROR: no partition of relation "events" found
     for row (SQLSTATE 23514)` — generated dates fall outside every partition range. Partitions
     would also be seeded directly as separate tables.
   → New bug for M0.5 (see D16).
10. ~~Bubble Tea v1.3.10 recovers panics inside `tea.Cmd` goroutines~~ — **checked in module
    source 2026-09-17:** panics in `Update`/`View` (`tea.go:634`, `recoverFromPanic`) and in `Cmd`,
    `Batch` and `Sequence` goroutines (`tea.go:356,508,534,552`, `recoverFromGoPanic`) are recovered
    by default: the terminal is restored and `Run` returns `ErrProgramPanic`, **but it prints the
    raw panic value and a stack trace to stdout**. Not covered: goroutines started *inside* a Cmd
    by our own code — the seeder's writer workers and parallel generators — which still crash the
    process with the terminal in alt-screen mode.

## Delivery — one PR (decided)

PR title: `feat: safe scans, live feedback, tuning advice and relationship-shaped seeding`
(not `!`: every change is additive or opt-in, see compatibility contracts; if any contract below
has to break during implementation, the title becomes `feat!:` and the PR body says what).

### Build order inside the branch
Local milestones, each ending green (`go test -race ./internal/...`, integration on pg15 +
mysql8.0, lint) before the next starts. The branch is squashed when merged.

| M | Scope | Why this order |
|---|-------|----------------|
| M0 | Baselines on `main` (below) | Something to compare every later milestone against |
| M0.5 | Bugs found in review and probes, each reproduced by a failing test first: SSE `error`/`onerror` collision (reproduced in Chromium); FK ignores referenced column; Postgres FK introspection via `pg_constraint` (`conkey`/`confkey`, all schemas the role sees); generator cap `NumCPU` → cgroup quota; unknown-as-zero consumers; partitioned tables (D16) | Later milestones build on correct FKs, counts and job streaming |
| M1 | D0 safeguards + D15.1–D15.4 (panic isolation, `RunError`, partial results, cleanup) + fault-injection build tag + D13 harness scaffold | Nothing else is safe to build while one panic kills the server |
| M2 | D1, D2, D11, D15.5–D15.8 (job list + reattach, keepalive/slow-vs-dead, `api()` helper, CLI exit codes) | Fixes hangs and invisible jobs before adding longer-running scans |
| M3 | D3, D4 workspace speed + form state | Uses M1 reads and M2 progress |
| M4 | D14 per-server budget + HA input, D12 tuning + run-start clamps + D13 assertions 1–4 | Needs M1 detection reads |
| M5 | D5 single-connection snapshots | Uses M1–M2 |
| M6 | D6, D7, D10 scan levels, relationship capture, compare/export (+ D14 replica/shared-server scan rules) | Uses M1 gates + M5 files |
| M7 | D8 shaped seeding | Only after capture is stable |
| M8 | Docs, CLAUDE.md, README, benchmarks, full matrix + e2e + loadsim | Single end-to-end validation |

### M0 baselines (recorded before any change)
- Seeded outputs with `--seed 7`: the 6 recorded files (150-table schema, Keycloak pg/mysql,
  with and without a profile) → SHA-256 list saved in the scratchpad.
- Benchmarks rows from docs/benchmarks.md re-measured on this machine (writers 1/4/8, gen-workers
  1/4, memory table) → numbers to beat or match.
- Full integration suite, e2e journeys, `-race` unit tests: all green, durations noted.

### Compatibility contracts (regression guards, each with a test)
1. **Same seed, same data:** without a `relationships:` profile, `--seed` output is byte-identical
   to M0 hashes (uniform FK picks untouched). Checked at M3, M4, M7, M8.
2. **CLI:** existing flags keep their meaning and defaults; new flags are additive. `--workers 4`
   stays the default; the run-start clamp lowers writers only when free connections < requested
   connections (writers + generators + 1) — never because of a safety margin — and logs it. The
   margin in D12 applies to *recommendations* only. Also: the unshaped path makes exactly the same
   `rnd` calls in the same order (test counts calls, not only hashes).
3. **Unknown is never zero:** a count that is `timed out` / `locked` is unknown. Every consumer
   that today reads a missing/unknown count as 0 changes, each with a test: `cli/gaps.go:163`,
   `web/runners.go:431,440,450,650`, `cli/progress.go:93`, `compare.Diff` (`knownRows`,
   `compare.go:195,204,237-242,269`), mirror plan (`compare/mirror.go:166,391` — an unknown
   **target** is planned as empty today), `web/handlers_api.go:67` (one failure drops all counts).
   `GetTableRowCounts` returns per-table outcomes instead of aborting on the first error.
4. **Timeouts on existing reads:** lock timeout on by default (reports, never queues behind DDL);
   statement timeout **off** by default for the existing counts/compare/snapshot paths, on by
   default only for relationship scans and production-flagged connections; `--read-timeout`
   configures it.
5. **Files:** v1 snapshot files still parse; a counts-only export is still written as `version: 1`
   with the same per-table fields (`ParseSnapshot` allowlists fields, `snapshot.go:36,153-160,
   261-266` — any new per-table field breaks older binaries; a failed count is written as
   `rows: -1`). `version: 2` only when `relationships:` (or outcomes) are present — older binaries
   refuse it cleanly. Profiles: `rules.Parse` is lenient (`rules.go:113`), so a profile with
   `relationships:` sets `version: 2` and older binaries refuse it (`rules.go:211`) instead of
   silently seeding unshaped. Documented: an older binary saving the profiles store or the saved
   connections store drops `relationships:` / `production:`.
6. **Web API:** JSON responses only gain fields; existing e2e journeys pass unchanged.
7. **Memory bounds:** existing scale tests (`TestSeed_WideRowsStayUnderAMemoryBound`,
   `TestSeed_ManyTablesDoNotKeepEveryKeyPool`, `TestSeed_LargeRunsStreamWithFlatMemory`) pass and
   a new one covers shaped seeding.
8. **Throughput:** M8 benchmark rows within noise (±10%) of M0 on the same machine for the
   unshaped path; any drop is explained in the PR or fixed.
9. **Production flag off = today:** a connection without the flag behaves as today for writes.
10. **Failures stay contained:** a panic or error in one job never stops the server, other jobs or
    other sessions; every failure names side, phase and table and lists what completed; database
    connections return to baseline afterwards.

### CI (public repo: standard GitHub-hosted runners are free and unlimited)
Limits taken into account:
- Standard `ubuntu-latest` is one shared VM per job (x64; arm64 images also exist). Expected size
  for public repos is 4 vCPU / 16GB RAM / ~14GB SSD — **not assumed**: a probe step prints
  `nproc`, `free -m`, `df -h`, cgroup version, root device (`findmnt`) into the job summary and
  the harness skips profiles that do not fit, with the reason in the summary.
- Shared, noisy VMs → **no timing assertions gate the PR**. Gating tests assert outcomes (clamped,
  refused, error named, completed, exit code), never rows/s.
- Byte-rate disk throttling did not apply on the dev host (btrfs); IOPS throttling did. On runners
  (ext4) the probe checks both; a throttle that does not apply skips its test visibly (summary
  line), it never passes silently.
- No larger runners (paid); no read replica available → replica behaviour stays unit-tested.
- Adding a workflow/job edits `.github/workflows` → pushing needs a token with `workflow` scope.

Jobs:
- Existing (as in `pr.yml`): `pr-title`, `review`, `gauntlet` (structlint + dupehound), `lint`,
  `integration` matrix of **4 pairs** (pg13/mysql5.7, pg13/8.0, pg15/8.0, pg17/8.4, `-race`),
  `e2e`. Unit tests run inside `gauntlet` (its `gotest` gate runs `go test -json -race ./...`,
  checked in the gauntlet source) — no extra job needed. CLAUDE.md's CI table and docs/development.md are
  stale (they list `test`/`validate`), and the compose file is `docker-compose.yaml` — fix both.
- New `loadsim` (PR gate): matrix `profile: [cloudsql-micro, cloudsql-2vcpu] × engine:
  [postgres-17, mysql-8.4]` (4 jobs, 30 min each), deterministic assertions 1–7 from D13 with the
  binary built in the job. Fits one runner: cloudsql-micro = DB 1 vCPU / 629MB, cloudsql-2vcpu =
  DB 2 vCPU / 7.5GB, binary container 1 vCPU / 256MB–1GB; `full` storage via tmpfs ≤ 512MB
  (tmpfs is charged to the DB container's memory, so micro's `full` variant raises the limit by the
  tmpfs size and says so).
- MySQL 8.4 on 629MB: may not start with default buffers → the profile's explicit
  `innodb_buffer_pool_size` (as on Cloud SQL) is what makes it fit; if it still OOMs, that is a
  finding for the PR, not a reason to raise the limit silently.
- New `loadsim-measure` (not a gate; `workflow_dispatch` + weekly schedule): profile × writers ×
  gen-workers throughput and memory, uploaded as a JSON artifact + step summary table. Runner
  numbers are for trends and recommender sanity only; docs/benchmarks.md keeps local numbers with
  the machine stated.

### Local validation before opening the PR
```bash
go test -race ./internal/...
make dev-up
cd integration && go test -race -v -tags integration -count=1 ./... -timeout 1500s
go test -v -tags loadsim -count=1 ./integration/... -timeout 1800s   # one profile at a time
make test-e2e
```
Plus the M0 hash comparison and benchmark rerun, and a manual pass of every web page at
320–1920 widths with runs active (progress strip, failure chips, dialogs), TUI flows and CLI
commands against a stopped database.

### Docs in the same PR
README (new flags/commands/output), docs/commands.md, docs/profiles.md (`relationships:`),
docs/benchmarks.md (tuning + loadsim numbers), docs/development.md (loadsim harness), CLAUDE.md
(new files: `db/safe_read.go`, `internal/tuning`, loadsim tests, CI table), `.structlint.yaml`.

### Go / no-go gates inside the branch
- **Before M7 (shaped seeding):** M0–M6 fully green (unit, integration matrix locally on 2 versions,
  e2e, loadsim assertions), contracts 1–9 checked. M7 is the research-heavy part (degree dealing
  while streaming, link-table realizability); it starts only from a stable base.
- **M7 exit:** shaped-seeding tests pass within tolerance on 10k/1M parents, memory bound holds,
  `--seed` reproducible. If M7 cannot meet that, it is reported to the user before continuing —
  never merged half-done behind a flag silently.

### Assumptions to prove first
See "Code review corrections → Assumptions list (updated)"; each is resolved before the milestone that depends on it.

### Risk of one large PR
Hard to review. Mitigations: PR body grouped by area with before/after examples
(compare failure, tuning dialog, shaped seed plan), milestone order kept visible in the body,
every contract above linked to its test.

## Test plan (highlights)

Safeguards (D0) — integration on real containers, both engines, the 4 CI version pairs,
each written failing first:
- Read-only: a write issued inside `ReadScope` is refused by the server (assert the engine error),
  table unchanged.
- Statement timeout: `SELECT pg_sleep(10)` / `SELECT SLEEP(10)` through `ReadScope` with a 500ms
  limit returns `timed out` in < 2s and the next table still runs.
- Lock timeout: another connection holds `ACCESS EXCLUSIVE` (pg) / an open `ALTER`-style MDL
  (mysql); the scan reports `locked` within the lock timeout, and a concurrent writer is **not**
  queued behind the scan.
- MySQL cancel: start a long `SELECT SLEEP(30)` scan, cancel ctx; assert via
  `information_schema.PROCESSLIST` that the query is gone within 2s (first prove it survives
  without `KILL QUERY`).
- Short transactions: during a multi-table scan, no transaction of the scan's session is older
  than one statement (`pg_stat_activity.xact_start` / `information_schema.INNODB_TRX`).
- Concurrency cap: with cap 1, `pg_stat_activity` / PROCESSLIST never shows two scan queries.
- Production flag (binary): `seed` against a `--production` target exits non-zero without
  `--allow-production` and inserts nothing; web API refuses without the typed label; reads still work.
- Replica: skipped in CI (no standby); unit test maps the conflict SQLSTATE to `cancelled by server`.

- Web unit: `runCompare` with an unreachable target (closed port on 127.0.0.1) fails in the
  connect phase within the timeout and names `target`; source is not counted first.
- Web unit: `Session.Schema` does not block `fromRequest` for another handler while introspecting.
- e2e: compare with a stopped target shows the failed chip and message, button re-enabled;
  progress strip visible while counting `wideSchemaDDL(150)`; leaving to Profiles and back keeps
  profile, writers and generators; truncate is not remembered.
- e2e: workspace return renders graph before counts; counts badges fill; "Refresh" re-counts.
- Integration (binary): `compare` against a dead port exits non-zero in < timeout+slack with
  `target:` in stderr; `snapshot` logs progress lines; `snapshot --relationships` on the 28-table
  schema matches hand-computed min/avg/max for a junction and a nullable FK.
- Unit: snapshot v1 still parses; v2 round-trips.
- Unit (seeding): shaped edge on 10k parents lands within tolerance of avg/p95/max, never above
  max, zero share respected; same `--seed` → identical rows; memory stays within bounds with
  `ChunkBytes` small (existing scale test pattern).
- TUI: model test that progress messages advance the seed view.
- Mutation check for each guard (break it once, see the test fail).

- Unit (plan): infeasible inputs (sum of mins > rows, max×parents < rows, unrealizable link-table
  sequences, 1:1 UNIQUE) finish in bounded steps with an adjustment report — no loop, table-driven.
- Integration: `introspect --scan relationships` cancelled mid-run keeps finished edges; a
  statement timeout marks one edge `timed out` and the run completes.

- Tuning (unit, table-driven): free connections 10 of 100 → writers ≤ 5; production + shared →
  ≤ 2; MySQL `max_allowed_packet` 4MB → batch ≤ 1MB; 512MB memory limit → chunk shrinks; host 2
  cores → generators 1; HDD → writers ≤ 2; gp3 3000 IOPS → writers ≤ 6; 1M rows × 2KB with 20GB
  free → ok, with 2GB free → refused. Integration (binary): `--workers 50` against a server with
  `max_connections` 20 clamps and logs it; run succeeds.

- Reliability (D15), binary built with `-tags faultinject`, each written failing first:
  - `serve`: two seeds in two sessions; `SEEDSTORM_FAULT=write:orders:panic` hits one → that job ends
    `failed` with `target · write · orders` and an error id; the other job completes; `GET /`
    still 200; `pg_stat_activity` / PROCESSLIST back to baseline; next run gets the full writer budget.
  - Same for `generate:<table>:panic` with `--gen-workers 4`, `copy:<table>:panic`, `job:panic`,
    `introspect:panic` (workspace shows the failure; other pages keep working).
  - `hang` faults: UI state goes `quiet` then Cancel ends the job `canceled` with partial results;
    no goroutine leak (`runtime.NumGoroutine` via a debug endpoint only in the faultinject build).
  - CLI: panic → exit 70, message names command and phase, partial summary printed; error → exit 1;
    Ctrl+C (SIGINT sent by the test) → exit 130 after `KILL QUERY` ran (processlist check).
  - e2e: failure shows side/phase/table and completed tables; leave the workspace mid-seed and
    return → the run is reattached with live progress; restart `serve` mid-run → "server
    restarted — job lost" message, no spinner; forced `unhandledrejection` shows the page notice.
  - TUI model tests: failure message rendered with the same fields. Binary-driven TUI test (PTY):
    `SEEDSTORM_FAULT=write:orders:panic` in `seed -i` → exit 70, terminal left in normal mode
    (no alt-screen escape pending), message names phase and table.
  - Default binary contains no fault-injection symbols.

## Decisions (2026-09-17)

- Destructive toggles (truncate, disable-FK, clone drop) are never persisted.
- Relationship capture is exact, with warnings for big / unindexed edges; background, cancellable,
  capped concurrency, per-edge timeout.
- Keep a histogram (percentiles derived from it).
- Every FK in a table can be shaped *where the code allows it*; the code review narrowed v1 (self-refs, composite FKs/PKs not shaped; junction tables depend on the M7 dealer). Infeasible shapes are adjusted in a bounded plan phase with warnings.
- Scan depth: `introspect`/`snapshot`/`compare` gain `--relationships`; no shared `--scan` flag (outputs differ).
- Loadsim DB profiles: `cloudsql-micro` (1 vCPU / 629MB) and `cloudsql-2vcpu` (2 vCPU / 7.5GB) gate the PR.
- Everything ships in **one PR**, built in milestones M0–M8 with compatibility contracts and CI
  loadsim profiles sized for standard public-repo runners.

## Open questions

- cloudsql-2vcpu MySQL values are estimated (no 2-vCPU instance); re-read if one becomes available.

- Big-table threshold default for the warning (proposed 5M estimated rows).
- Statement timeout for relationship scans / production connections: 60s per table/edge proposed (existing reads: off, `--read-timeout`).
- Production flag writes: typed-label confirmation enough, or also refuse truncate outright?
