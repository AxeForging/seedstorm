# Plan — 004 safe scans, feedback, form state, tuning, relationship shapes

Spec: `docs/specs/004-feedback-state-and-relationship-shapes.md` (untracked, never committed).
Branch: `feat/safe-scans-feedback-shapes` (local only: no `git add`, no commit, no push until the user says so).

Rules while implementing:
- Each bug fix: failing test first, then the fix. Break the guard once (mutation) and see the test fail.
- Each milestone ends green: `go test -race ./internal/...`, lint, integration on pg15 + mysql8.0 for touched areas.
- Resource discipline: one dev DB pair (docker compose), one `serve`, one browser; throwaway containers removed.
- Update this file as steps complete; append discovered steps under the milestone.

## M0 — Baselines on unchanged code
- [x] Build `main` binary into scratchpad (`seedstorm-base`) for later comparisons
- [x] `make dev-up`; unchanged code: unit `go test -race ./...` all ok (7s cached); integration `-race` full suite **PASS in 986s** (16.5 min)
- [x] e2e baseline on unchanged code: 11/11 passed (23.7s); current code 13/13 (with failed-compare + production journeys)
- [x] Record reproducible outputs (`generate --seed 7 --rows 300`, Keycloak + 36-table schema × pg/mysql × with/without profile; 8 files, run twice identical) → `scratchpad/m0/out-base/SHA256SUMS`, compare with `scratchpad/m0/baseline.sh <bin> <dir>`
- [ ] Record benchmark rows (writers 1/4/8, gen-workers 1/4, memory) on this machine → scratchpad

## M0.5 — Bugs found in review and probes (failing test first)
- [x] SSE: rename terminal `error` event → `failure`, SSE `id:` + `?after=`/`Last-Event-ID`, lock-safe job state (unit tests failing first + mutation checked); client settles on failure/connection loss; compare `runJob` no longer swallows fetch errors
- [x] e2e: failed compare ends with its reason — passes on new code, fails (spins 15s) on the base binary
- [x] FK values honour the referenced column (UNIQUE column or one column of a composite key): pools keyed `table.column`, fork/merge/release/redraw aware; unit test on shared + fork paths; M0 hashes unchanged
- [x] Integration test: seed pg + mysql with an FK to a UNIQUE non-key column and to a composite-key column unique on its own — PASS
- [x] Postgres FK/PK introspection via `pg_constraint` (composite FKs paired correctly, same-named constraints, SELECT-only roles) — schema `public` only stays a documented limitation
- [x] `integration/introspect_constraints_test.go` — PASS (owner + SELECT-only role)
- [x] Generator cap from cgroup CPU quota instead of `NumCPU`: `internal/tuning` (cgroup v1/v2 parse, floor of quota) used inside `seeder` (shared engine), web and CLI (log line when lowered); unit tests
- [x] Unknown counts never treated as zero: `db.CountTables` per-table outcomes; `seeder.GapTables/KnownEmpty` shared by CLI + web gaps; TUI picker; graph + /api/counts partial; `compare.StatusUnknown` + totals; mirror `ReasonTargetUnknown` and unknown parents never refilled; compare page labels; tests failing first
- [x] Estimate mode: zero/missing estimate still counted exactly for tables ≤256MB (keeps `TestCompareEstimates_*`), larger tables stay unknown instead of scanned; production flag tightens later (M1)
- [x] Postgres partitioned tables (D16): partitions hidden from introspection/columns/sizes/estimates (parent = sum of leaves); `daterange`/`datetimerange` generators (+catalog); key faker inside bounds (range contiguity, list, hash/default free); `faker.CheckSeedable` refuses expression/multi-column/text-range keys before truncation (CLI seed, web seed, mirror, NewStream); unit tests
- [x] `integration/partitions_test.go` — PASS after two fixes it found: index partitions (NULL bound) excluded; list key inside a composite PK → `schema.Column.PartitionKey` honoured on the PK path. Expression key refused before truncation (rows kept)
- [x] Workspace skips `loadCloneTargetAccess` unless clone mode (loads when switching to clone)

## M1 — Safeguards + reliability core
- [x] `internal/safego` (recover → `PanicError` with id, stack to log only), used by job runner, writer workers + dispatchers + writes, parallel and sequential generators, Fill pieces, COPY writer; tests: driver panic in writer, value-rule panic on parallel generator, Fill piece panic (crashed the test binary before), panicking job fails alone
- [x] HTTP recover middleware → JSON 500 `{error, errorId}` (`web/recover.go`, wired in `Server.Handler` and `ListenAndServe`)
- [x] `internal/runerr` (`Error{Side, Phase, Table}`) used by seeder write/generate/truncate, mirror count/truncate/fill (target side), compare/mirror connect (source/target), CLI endpoints; partial results: web seed + gaps return `{partial, tableCounts, written, notWritten, failure, nextStep}` with the error (unit test), mirror result gets `failure`, CLI logs what was written before the error
- [ ] Render `failure`/`nextStep` in the web strip and TUI (M2)
- [x] `db.ReadOnce` + `ReadLimits` (read-only tx per statement, PG `SET LOCAL` timeouts, MySQL session limits reset before the conn returns to the pool or conn discarded, `KILL QUERY` on cancel) + `ReadOutcomeOf` (timed out/locked/cancelled/failed); `CountTablesWithin` with concurrency; integration: write refused, server timeout without leak, lock timeout, MySQL cancel kills query (driver-alone leak confirmed; mutation without KILL fails) — PASS both engines
- [x] MySQL `KILL QUERY` on cancel (in ReadOnce)
- [x] CLI `signal.NotifyContext` (first Ctrl+C cancels so MySQL KILL + summary run; second exits now), exit codes 1/70/130, stack only with `--log-level debug`; integration: panic→70 naming write · users, error→1, SIGINT→130 within 5s
- [~] Route existing reads through ReadOnce: counts (lock timeout 2s), estimates, columns, sizes, identity, access, table preview — done; faker preloads/redraws take ctx (`NewStreamContext`, cancelled Fill returns ctx error); introspection + objects move with the `Introspect(ctx, conn…)` refactor (M2)
- [x] Production flag: `SavedConnection.Production` (form checkbox + badge + picker labels + `/api/connections`), un-flag needs typed label (form + API), `Session.SavedID` + database-key match (ad-hoc/DSN sessions recognised), synchronous 409 `production_confirm` guard on seed/gaps fill/mirror/clone (dry runs allowed), `ui.postRun` typed-label dialog + retry; CLI `--production`/`SEEDSTORM_PRODUCTION` + `--allow-production` on seed/gaps/mirror/clone-schema. Tests: web unit, integration CLI, e2e dialog (cancel writes nothing, typed label seeds)
- [ ] TUI: production prompt (today the CLI refuses before the TUI opens unless `--allow-production`)
- [x] `internal/faultinject` (`-tags faultinject`, `SEEDSTORM_FAULT=point:table:panic|error|hang`), points: write (concurrent + sequential), generate, copy, job, introspect; default binary has no injection code (test)
- [x] loadsim harness scaffold: tag `integration && loadsim` (no churn in existing files), `loadsim_profiles_test.go` (cloudsql-micro from the reference instance, cloudsql-2vcpu docs/estimated), container helper (limits, IOPS device, headroom skip, OOM via memory.events, cleanup), `make test-loadsim`; assertion 7 PASS on micro pg + mysql
- [x] Tests: ReadScope integration, production guard (web unit + CLI binary + e2e), panic isolation (unit + `serve` binary: a panicking job fails naming write · table while another job finishes; no stack at default log level)
- [ ] Tests still to add: short transactions and concurrency cap observed on the server; connections back to baseline after a panic

## M2 — Feedback and failure handling
- [x] Run strip (`[data-run-strip]`, shared state in app.js) with running/quiet/reconnecting/lost + failure location + next step; `fetchJSON` + `postRun` helpers; global error notice; `GET /api/jobs` (session jobs, phase, progress, bootId) + `resumeRun` reattach + server-restart message; SSE `ping` event every 15s; job eviction (keep 50 finished); jobs owned by session. Unit tests: job list, eviction, keepalive. (Decision: kept one job log panel per page; a second job type in M3/M6 renders into its own strip state if needed.)
- [x] Compare strip outside `#cmp-results`; preflight `connectBoth` (both sides at once, ping with timeout, per-side log line, `target · connect:` error) — unit test (dead target fails in 300ms, source not queried); sides counted concurrently in `seeder.Snapshots`; failure text in the strip names the side (chips = strip)
- [x] Workspace + run-form strips; uncaught fetches fixed (run starts via postRun, detail/peek/preview/modal/schema columns, profiles save/delete/YAML export+import)
- [x] `db.IntrospectConn(ctx, conn, dbType, onTable)` (all catalog queries take ctx; `Introspect` pings with a 10s timeout); `Session.Schema` single-flight on the session connection without holding the lock (unit test: 12 concurrent calls = one introspection); RawTables uses the session connection (kept uncached: clone needs fresh target state)
- [x] CLI progress logs: compare/snapshot/mirror counting (throttled per side), introspect per table, gaps scan, clone-schema start/end, generate per table; connect timeout 10s with the database label in the error (seed, gaps, introspect, compare/mirror endpoints); exit codes done in M1
- [ ] profile validate introspection log (minor)
- [x] TUI: seed/gaps progress via events channel (phase, rows, %, rate/ETA, tables, elapsed), failure view keeps written tables + not written + sync errors; mirror preview in background with loading state, truncate progress shown, partial results printed after error/abort; clone spinner + elapsed, output printed after the screen closes; `tea.ErrProgramPanic` → exit 70. Model tests added.
- [ ] Tests: unit + integration (dead target, progress lines, exit codes), e2e (failure shows where, reattach, server restart), TUI model tests

## M3 — Workspace speed + form state
- [x] `/api/graph` structure only + session-cached counts (`countsTakenAt`); `/api/counts` single-flight per session (`Session.Counts`, read-only, lock timeout, 2 at a time), `?refresh=1`, `X-Counts-Taken-At`; invalidated after seed/gaps fill/mirror target runs; page draws the graph first and fills counts with a status line (`ws-counts-status`: counting / from N min ago / could not be counted); estimates never used for `counted`. Unit test (graph runs 0 queries, cache, refresh, invalidate)
- [x] `data-conn-key` in layout
- [ ] Layout cache: measure dagre on the 150-table fixture first; implement only if it is a visible part of the return time
- [x] `readStore`/`writeStore` in `ui`; workspace form memory per connection (rows, tuning, profile, dry run, clone options; never truncate/disable-FK/drop), profile restored after options load + insights; compare memory (picks URL > stored > default, counts mode, scale, mirror settings; never reset mode); `resumeRun` also shows a job that finished while away (once)
- [x] Job eviction in `jobs.Manager` (M2)
- [x] Tests: unit; e2e `memory.spec.ts` (settings survive navigation, truncate not kept, reattach to a running dry run) — full e2e 15/15
- [x] Checkpoint commit `098e41f` (M0.5–M3), specs/.agents excluded

## M4 — Tuning + clamps + per-server budget
- [x] `internal/tuning`: `Recommend` (writers from vCPU/connections/memory/storage/HA/production, generators, chunk, batch, reasons), growth check, `ClampWriters`, `DetectHost` (cgroup CPU + memory); `db.DetectServer`/`ConnectionUsage` (read-only) + integration test both engines
- [x] Run-start clamp inside `seeder.Seed` and `Fill` (free connections, `OnNotice`, `SetMaxOpenConns(writers+generators+1)`), unit test with a busy server; HA input (dialog + CLI)
- [ ] Per-server running-writes registry in web (message naming the other run) — optional; the clamp already accounts for other runs' connections
- [x] `/api/tuning` + Recommend dialog (shape remembered per connection, reasons, growth, caveat, Apply) with unit + e2e tests; CLI `seedstorm tune` (host line, reasons, disk) and `--workers auto` on seed/gaps/mirror (logged choice, validation) with integration test
- [ ] TUI prefill of writers from the recommendation (TUI still uses the default 4)
- [x] loadsim assertions 1–4 PASS: container limits detected (2 CPUs/512MB, gen-workers 16→2), few free connections clamp (50→N, completes), full disk (tmpfs) fails naming table + explanation (`db.Explain`: disk full / connection closed, incl. 57P03), 1M rows under 256MB container; measure matrix test (`SEEDSTORM_LOADSIM_MEASURE`)

## M5 — Single-connection snapshots
- [x] `/api/snapshot` job (read-only, per-table progress, unknown counted) + encode of a snapshot without a report; workspace Snapshot counts dialog (YAML/JSON preview, copy, download, Compare with another database → imported source) and Calibrate from a file (compare opens import, target = this connection); unit + e2e
- [x] CLI snapshot progress (M2)
- [x] Checkpoint commit `290da0a` (M4–M5)
- Preliminary measure (6k rows, micro @300 IOPS): mysql best at 2 writers (2167 rows/s), pg flat ~55k rows/s — too small; bigger matrix in M8

## M6 — Relationship capture
- [x] `db.LeadingIndexed`, relationship scan (CASE buckets, histogram, estimate mode), outcomes, cancel, concurrency, index gate, big-table warning (`internal/relations`, `db/relations.go`)
- [x] Snapshot v2 (`relationships:`), v1 unchanged; `introspect/snapshot/compare --relationships` (+ `--scan-unindexed`, `--read-timeout`; mode follows `--counts`; introspect writes estimated counts + exact shapes); `compare.DiffShapes` + `RenderShapeDrift`; `seeder.Endpoint.Shapes` / `CompareShapes`
- [x] Web: Analyze relationships job (`/api/relationships`, session shape cache filled per edge, polled badges on graph edges + Table detail, production → estimates/1 query unless confirmed), compare Relationships section (`/api/compare/relationships`, drift table, kept in saved report), export with/without (compare export + workspace snapshot). Unit + e2e `relationships.spec.ts` (mutation checked); UI swept 320/768/1440; full e2e 18/18
- [x] ServerIdentity + cluster identity check (warn when unavailable); replica awareness — `seeder.RelateServers` (shared server notice in CLI log + plan modal, replica target refused, unknown when unreadable), `ScanNotice` replica/primary in relationship scans; unit + integration
- [x] Tests: integration hand-computed shapes both engines, unindexed gate, cancel keeps finished edges, timeout edge, binary snapshot→compare drift + counts-only refusal

## M7 — Shaped seeding (go/no-go gate before starting)
- [x] Gate: M0–M6 green (unit, lint, e2e 18/18, M0 hashes identical; full integration rerun in progress)
- [x] Profile `relationships:` (rules v2 only when present, store saves FormatVersion), validation (structure + schema, not-shaped reasons), `RelationshipsFromShapes`, profiles page Relationships section (import from counts file, edit, remove)
- [x] Plan function: `faker.DeriveShapedRows` (`--shape-rows`, `SeedOptions.DerivedRows`, keeps enum coverage), feasibility adjustments in `dealDegrees` with notes (max raised, zero share lowered)
- [x] Degree dealer (`faker/shapes.go`: Fenwick slots, exact nullShare, undo on retry, return on UNIQUE drop, fork per table, dealers freed per table, overflow uniform + reported; Fill rounds via ShapeTotals; mirror `--shape-like-source` / web "Shape like source"); achieved shape measured + logged after seed/gaps/mirror
- [x] Junction tables marked not shaped (reason reported); achieved shape reported
- [x] Tests: unit tolerance/max/null/reproducible/unrelated-shape-identical/infeasible/undo (mutation checked); integration binary seed both engines, mirror like source, memory 325k rows peak 124MB; e2e plan + profile import. Limit: parents above poolLimit (500k) shaped over the sample (approximate, warned)

## M8 — Docs + full validation
- [x] README, docs/commands.md, docs/profiles.md, docs/development.md, docs/benchmarks.md, CLAUDE.md, CI: `loadsim` job (probe + skip summary; tests pick profiles, one job), `loadsim-measure.yml` manual only (no schedule)
- [x] Full: unit -race, integration -race (pg15/mysql8.0) ok 1042s, e2e 18/18, loadsim 5 pass, M0 hashes identical, micro measure rerun; UI checked 320/768/1440 on new surfaces
- [x] Push, PR #43, CI green (fixed on the way: MySQL datetime key rounding, full-disk eval proof, estimate eval timing)
