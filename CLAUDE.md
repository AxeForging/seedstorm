# CLAUDE.md — seedstorm

AI assistant instructions for working with the seedstorm codebase.

## Project Overview

seedstorm is a Go CLI tool that seeds databases with realistic fake data. It introspects a live database, resolves FK insertion order via topological sort, generates fake rows using `gofakeit`, and inserts them. It supports PostgreSQL and MySQL, with optional AI-powered (Gemini) faker enrichment.

**Repository:** https://github.com/AxeForging/seedstorm
**Organization:** https://github.com/AxeForging

---

## Directory Structure

```
seedstorm/
├── cmd/seedstorm/main.go       # Binary entry point — initialises app and runs CLI
├── internal/
│   ├── app/app.go              # Wires all CLI commands together
│   ├── cli/                    # One file per command (seed, gaps, generate, …)
│   │   ├── root.go             # Root CLI definition; registers all subcommands here
│   │   ├── seed.go             # seed command
│   │   ├── gaps.go             # gaps command
│   │   ├── generate.go         # generate command
│   │   ├── enrich.go           # ai-enrich command
│   │   ├── introspect.go       # introspect command
│   │   ├── export.go           # export command
│   │   ├── compare.go          # compare command
│   │   ├── mirror.go           # mirror command
│   │   ├── snapshot.go         # snapshot command (row counts to JSON/YAML)
│   │   ├── tune.go             # tune command, --workers auto
│   │   ├── production.go       # --production / --allow-production write guard
│   │   ├── relationships.go    # --relationships scan flags, --shape-rows, achieved-shape logs
│   │   ├── progress.go         # --workers flag, progress log lines, profile ignore
│   │   ├── profile.go          # profile command + shared --profile flag/loading
│   │   ├── endpoints.go        # --source-*/--target-* flags and connection opening
│   │   └── helpers.go          # Shared helpers (buildInsert, normalizeDBType)
│   ├── db/                     # Database drivers and introspection
│   │   ├── db.go               # Introspect() dispatcher (postgres / mysql)
│   │   ├── postgres.go         # PostgreSQL schema introspection + constraint parsing
│   │   ├── mysql.go            # MySQL schema introspection + constraint parsing
│   │   ├── truncate.go         # Truncate helper (FK-safe order)
│   │   ├── counts.go           # CountTables / CountTablesWithin: per-table outcomes, unknown never 0
│   │   ├── read_scope.go       # ReadOnce: read-only tx, statement/lock timeouts, MySQL KILL QUERY on cancel
│   │   ├── relations.go        # DegreeHistogram (CASE buckets), LeadingIndexed, EstimateDegrees
│   │   ├── server_info.go      # DetectServer (capacity, replica, server id), ConnectionUsage
│   │   ├── explain.go          # Explain: disk full / connection lost errors in plain words
│   │   ├── partitions.go       # Postgres partitioned tables (bounds, leaves)
│   │   ├── stats.go            # Table sizes, estimated counts, column lists, DB identity
│   │   ├── copy.go             # CopyRows: Postgres COPY for a chunk of rows
│   │   ├── sequences.go        # SyncSequences: move Postgres sequences past inserted ids
│   │   ├── access.go           # InspectAccess: the connected user's privileges (pg has_*_privilege, mysql SHOW GRANTS)
│   │   ├── objects.go          # Views, routines, triggers for clone-schema (BuildCloneDDL)
│   │   ├── transient.go        # IsTransient: deadlock / lock-timeout errors a writer may retry
│   │   └── types.go            # Shared db types (Table, Column, FK, …)
│   ├── faker/
│   │   ├── faker.go            # Generate / GenerateFiltered — core data generation (generator methods)
│   │   ├── random.go           # randomSource (global seeded or private per generator), SeedRandom, wrappers
│   │   ├── mapper.go           # Column name → faker hint heuristics
│   │   ├── existing.go         # Awareness of rows already in the DB (keys, id and sequence continuation)
│   │   ├── overrides.go        # Column overrides (value rules), CoerceValue, ValueKind
│   │   ├── catalog.go          # Generator catalog, Evaluate, BuildSchema
│   │   ├── stream.go           # Stream: generation state kept across chunks (NewStream, Generate)
│   │   ├── keys.go             # keySet: exact map, then scalable Bloom filter
│   │   ├── pools.go            # PK pool reservoir sampling and capping
│   │   ├── shapes.go           # Relationship shapes: degree dealer (Fenwick slots), DeriveShapedRows
│   │   ├── references.go       # Pools for FKs to non-key columns ("table.column")
│   │   ├── partitions.go       # Partition-key generators, CheckSeedable
│   │   └── *_test.go           # Unit tests alongside production files
│   ├── graph/
│   │   ├── graph.go            # Dependency graph (Build, TopologicalSort, RenderPlan)
│   │   ├── ignore.go           # ApplyIgnore: drop ignored tables, refuse empty required parents
│   │   └── graph_test.go       # Unit tests
│   ├── rules/                  # Seed profile rules: model, templates, resolve/validate/compile
│   ├── profiles/               # Saved profile store (profiles.yaml) + Resolve(file|name)
│   ├── compare/                # Snapshots, Diff, PlanMirror, snapshot files v1/v2 (Encode/ParseSnapshot), DiffShapes, renderers (never writes)
│   ├── relations/              # Relationship shapes: Scan (index gate, per-edge timeout, cancel keeps finished), Shape
│   ├── tuning/                 # Recommend, ClampWriters, DetectHost (cgroup CPU/memory)
│   ├── safego/                 # Run/Recover: a panic becomes an error with an id
│   ├── runerr/                 # Located errors: side · phase · table
│   ├── faultinject/            # SEEDSTORM_FAULT points (build tag faultinject only)
│   ├── dataio/                 # Streaming data documents: writers (yaml/json/sql/csv), ReadTables
│   ├── seeder/                 # Seed (strict chunked seed/gaps), Fill (resilient mirror inserts), MirrorJob, Preview,
│   │                           # writer.go (FK-gated concurrent writes), meter.go (rate/ETA),
│   │                           # seed.go generateTables (parallel generation) + poolReleases + clampToServer,
│   │                           # relationships.go (Endpoint.Shapes, CompareShapes, MeasureShapes), servers.go (RelateServers)
│   ├── fsutil/                 # WriteFileAtomic for on-disk stores
│   ├── tui/                    # Bubble Tea flows (seed, gaps, generate, clone, mirror)
│   ├── web/                    # serve: handlers_*.go per area, templates/, static/ (page.js + page.css per page),
│   │                           # jobs.go (list, reattach, eviction), production.go, preflight.go, shapes.go (session shape cache)
│   ├── ai/ai.go                # Gemini enrichment (prompt building, response parsing)
│   ├── schema/schema.go        # Schema YAML types and loader
│   ├── build/info.go           # Version info injected at build time
│   └── logging/logging.go      # Zerolog setup
├── integration/
│   ├── integration_test.go     # Integration tests (build tag: integration)
│   ├── binary_test.go          # Builds the binary; scratch DB helpers per engine
│   ├── *_test.go               # Scenario evals: reseed, mirror, seeder, keycloak, compare_web, parallel_seed,
│   │                           # wide_schema (150 generated tables), access, snapshot, clone_objects,
│   │                           # web_jobs (+helpers), reproducible, seed_scale (memory bounds), read_scope, production,
│   │                           # reliability (faultinject), relationships, shaped_seed, partitions, tune, servers
│   ├── loadsim_*_test.go       # Resource-limited evals in Cloud SQL-shaped containers (tags: integration loadsim)
│   ├── fixtures/               # Keycloak schema dumps (real-world 87-table stress schema)
│   ├── schema_postgres.sql     # 28-table schema for Postgres integration tests
│   └── schema_mysql.sql        # 28-table schema for MySQL integration tests
├── e2e/                        # Playwright journeys for `serve` (pnpm; make test-e2e)
│   ├── playwright.config.ts    # chromium, 1 worker; global setup builds the binary and starts serve
│   ├── support/                # global.setup/teardown, databases.fixture (ss_e2e_* DBs), wide-schema.fixture,
│   │                           # db.helpers (SQL assertions), test.fixture, selectors.ts (every data-testid)
│   └── tests/<area>.spec.ts    # workspace, seed, clone, compare, access, profiles, mobile, memory, production, tuning, snapshot, relationships
├── docs/benchmarks.md          # Measured throughput, memory and layout numbers behind the defaults
├── README.md                   # User-facing documentation (keep in sync with code)
├── Makefile                    # Build, test, lint, dev-up/down targets
├── docker-compose.yaml         # Local MySQL + PostgreSQL via Docker Compose
└── .github/workflows/          # pr.yml: title, review, gauntlet (structlint, unit -race), lint, integration, loadsim, e2e;
                                # loadsim-measure.yml: manual throughput measurement
```

---

## Development Rules

### 1. README must stay in sync

**Any change to a command's flags, behaviour, or output format requires a README update in the same PR.**

Specifically:
- New flag added → add a row to the flag table for that command in `README.md`
- Flag removed or renamed → update the table and any examples
- New command added → add a full section (description, usage examples, flag table, sample output)
- Output format changed (e.g. dry-run, gaps report) → update the sample output block
- Workflow step changed → update the Workflow section
- Integration test timeout changed → update the `cd integration && go test … --timeout` example

### 2. Tests are mandatory

- **Unit tests** — every new function in `internal/` that contains logic must have a unit test alongside it (`*_test.go` in the same package). New faker generation paths, graph functions, constraint parsers, and AI prompt builders all fall in this category.
- **Integration tests** — new commands or seeding behaviour that touches the DB must be covered in `integration/integration_test.go` (build tag `integration`). The 28-table schema covers deep FK chains, junction tables, near-cycles, enums, and constraints — extend it rather than simplify.
- **Run before pushing:**
  ```bash
  go test -race ./internal/...                    # unit tests
  make dev-up
  cd integration && go test -race -v -tags integration -count=1 ./... -timeout 1500s
  make test-e2e                                   # when the web UI changed
  ```

### 3. Adding a new command

Checklist:
1. Create `internal/cli/<name>.go` with `<name>Cmd() *cli.Command`
2. Register it in `internal/cli/root.go`
3. Add a `### \`<name>\`` section to `README.md` with description, usage examples, flag table, and sample output
4. Add the command to the Workflow section in `README.md` if it fits there
5. Write unit tests for any non-trivial logic it calls
6. Write integration tests for any DB interactions
7. Update `.structlint.yaml` `requiredPaths` if the file is structurally required

### 4. PR titles — Conventional Commits

All PR titles must follow the pattern `<type>[scope][!]: <description>`.
Allowed types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `build`, `ci`, `perf`, `revert`.
The CI `title` job enforces this — it will comment and fail if the format is wrong.

---

## Architecture Notes

### FK-safe insertion order

`graph.Build(s)` constructs a DAG where an edge `A → B` means "A must be seeded before B". Nullable FK columns are excluded from edges (they break near-cycles — the column is seeded as NULL on first pass). `TopologicalSort()` uses Kahn's algorithm. Cycles in non-nullable FKs return an error; the user can bypass with `--disable-fk`.

### File naming

One concern per file, named after it: a CLI command lives in `internal/cli/<command>.go`, web handlers in `internal/web/handlers_<area>.go`, page assets in `static/<page>.js` + `static/<page>.css` with `templates/<page>.html.tmpl`. Tests sit next to the file they test (`existing.go` → `existing_test.go`). Scenario evals that drive the binary live in `integration/<scenario>_test.go`.

### Shared engines, thin surfaces

CLI, TUI and web never re-implement logic: value rules compile in `internal/rules` into `faker.Overrides`; comparisons and mirror plans come from `internal/compare`; every insert goes through `internal/seeder` — `Seed` for seed and gaps (stops at the first refused insert, and with `DryRun` for generate), `Fill` for mirror (regenerates refused rows); data files are written and read through `internal/dataio`. A new surface calls these packages; a new behaviour lands there first, with tests.

### Value rules (seed profiles)

`rules.RuleSet.Compile(schema, runID)` yields per-column `faker.ColumnOverride` funcs. `GenerateFilteredWithOptions` applies them **after** PKs, FKs, self-references and sequences are final — rule targets are never PK/FK/generated columns, so nothing references the values being replaced. Resolution: explicit table column rule > first compatible pattern rule > automatic. `GenerateOptions.RowOffset` keeps `{{seq}}` increasing when a table is generated in chunks.

### Concurrent writes (`--workers`)

Generation stays single-threaded (the `faker.Stream` is stateful and cheap); only writes are concurrent. `seeder.writer` gates each table on every FK parent in the run (nullable included) finishing its writes, keeps a self-referencing table's chunks sequential and in order, and splits other chunks across workers. Tables wait in their own dispatcher goroutine, never in a worker, and a row budget blocks the generator so memory stays flat. `Workers <= 1` keeps the old strictly sequential path. Progress (`OnProgress`) fires per written piece with run totals; `seeder.Meter` turns it into rate/ETA.

Hooks that log from the writer (`OnTable`, `OnProgress`) run on writer goroutines while the generator logs too: anything a runner shares between them must be safe for concurrent use (the web `jobWriter` locks its line buffer; an unguarded one panicked mid-run).

### Parallel generation (`--gen-workers`) and memory bounds

`generateTables` (seeder) generates tables on `faker.Stream` forks (`ForkTable` / `MergeTable`): a fork copies the table's own state and its parents' key pools, which are final by then. A table starts only after every earlier table it references **and** every earlier table that references it has generated — the second rule keeps nullable near-cycles NULL exactly as in order (a fork would otherwise see keys whose rows its writer does not wait for). Each fork uses `UseOwnRandom` (a private unlocked gofakeit source): sharing the global source made 4 generators slower than 1. It is off for dry runs, when `OnRows` is set, and when `Reproducible` (`--seed`) is set.

Memory is bounded by bytes, not rows: `GenerateOptions.ChunkBytes` (default `DefaultChunkBytes`, 32MB) sizes chunks from finished rows (value rules can widen them) after a first chunk of at most `firstChunkRows`; the writer queue holds about one chunk of `db.RowsMemory`; `poolReleases` frees a table's key pool once nothing left in the run reads it. Numbers: docs/benchmarks.md.

### Reproducible `--seed`

Everything that draws randomness must go through the generator's `randomSource` and walk tables and columns in sorted order: `graph.Build` sorts, `findEnumColumn` sorts, date ranges end at `reproducibleEnd` when seeded, pool sampling and UNIQUE repair use the source. `TestGenerate_SameSeedWritesIdenticalData` guards it.

Do not parallelise MySQL `TRUNCATE`: concurrent truncates of FK-linked tables (FK checks off) made later inserts fail FK checks against rows that existed. `db.TruncateConcurrently` keeps MySQL on one pinned connection.

### Mirror and resilient inserts

`seeder.Fill` fills one table at a time in fixed chunks (`DefaultChunkRows`) from one `faker.Stream`: parents are complete before a child starts, so their PK pools and the table's stored keys are read once and kept across chunks. Postgres chunks go through `COPY` (`db.CopyRows`); a refused COPY falls back to batched INSERTs. After the run, `db.SyncSequences` moves Postgres sequences past the inserted ids. A rejected batch is retried row by row; rejected rows are regenerated; `MaxRowFailures` refused rows in a row with none accepted, `maxZeroRounds` rounds that generate nothing, or an exhausted key space end the table with a `partial`/`failed` result instead of looping. `PrepareMirror` refuses when `db.Identity` says source and target are the same database.

### Composite PK safety

All three generation paths (`generateStandardRows`, `generateEnumRows`, `topUpEnumCoverage`) use a `seenKeys` set (`newSeen` over the stored `keySet`) and a 200-attempt retry loop before returning an error. This prevents silent duplicate composite PK inserts into junction tables. If you add a new generation path, it must include the same guard.

### Enum top-up

After standard row generation, `topUpEnumCoverage` guarantees every enum value (detected via `randomstring(a,b,c)` faker) appears at least `--rows` times — independently per column, not as a cartesian product. Pools larger than `maxEnumTopUpValues` (12) are treated as AI example lists and skipped to avoid row explosion.

### GenerateFiltered vs Generate

`Generate` is the standard path (all tables). `GenerateFiltered(allTables, targetTables)` preloads PKs from `allTables` but only generates rows for `targetTables` — used by the `gaps` command to seed empty tables while resolving FKs to already-populated parents. Do not change `Generate`'s signature; it delegates to `GenerateFiltered`.

### Dry-run output

`--dry-run` in the `seed` command first prints `graph.RenderPlan(s, sortedTables, rows)` — a numbered table showing FK dependency order — then prints raw INSERT SQL. If you add flags that affect generation, make sure `RenderPlan` still reflects the actual plan.

---

## CI Pipeline (pr.yml)

| Job | What it checks |
|-----|---------------|
| `pr-title` | Conventional Commits format |
| `review` | AI code review via reviewforge (Gemini) |
| `gauntlet` | structlint, dupehound, unit tests with `-race` |
| `lint` | `golangci-lint` |
| `integration` | Full suite + scenario evals on Postgres 13/15/17 × MySQL 5.7/8.0/8.4, with the race detector |
| `loadsim` | Resource-limited evals (Cloud SQL-shaped containers); outcomes only, never timings |
| `e2e` | Playwright journeys (`e2e/`) against a built binary on Postgres 15 + MySQL 8.0 |

The integration job in CI uses `-race -timeout 1500s` (the race detector roughly doubles the ~5 minute suite). Use the same locally.

---

## Key Dependencies

| Package | Role |
|---------|------|
| `github.com/urfave/cli/v3` | CLI framework |
| `github.com/brianvoe/gofakeit/v6` | Fake data generation |
| `github.com/jackc/pgx/v5/stdlib` | PostgreSQL driver |
| `github.com/go-sql-driver/mysql` | MySQL driver |
| `github.com/goccy/go-yaml` | YAML parsing |
| `github.com/rs/zerolog` | Structured logging |
| `google.golang.org/genai` | Gemini AI client |

---

## Common Gotchas

1. **Map iteration order** — Go maps are unordered. `buildInsert` iterates `row map[string]interface{}` — columns and placeholders are built in the same loop so they stay aligned, but order varies per call. This is fine for named-column INSERTs.

2. **Self-referential FKs** — `graph.Build` skips self-referential edges. `generateValue` returns `nil` for a self-ref FK column when no PKs exist yet (first row), producing a NULL root. This is intentional.

3. **Nullable FK = NULL on first pass** — A nullable FK column is seeded as NULL if the referenced table has no PKs yet (e.g., `departments.head_employee_id → employees` when departments is seeded first). This is correct; a second seed pass would fill them.

4. **Existing rows are part of the key space** — When a connection is given, `loadExistingState` loads the primary keys already stored (skipped for a single integer or uuid key, which cannot collide), integer ids continue after the largest existing id (`nextSequentialPK`), `sequence` UNIQUE columns continue past their maximum, and composite-FK junction enumeration skips stored combinations. Key values are compared through `keyValue`, which normalises driver types (MySQL `[]byte`, `time.Time`) to the generator's.

5. **Constraint introspection** — Postgres and MySQL serialize CHECK constraints differently. Both parsers live in `db/postgres.go` and `db/mysql.go` respectively. Add tests to `db/constraint_test.go` whenever you touch constraint parsing.

6. **MySQL ignores column-level `REFERENCES`** — In test DDL write table-level `FOREIGN KEY (...) REFERENCES ...`; an inline `REFERENCES` creates no constraint on MySQL, so introspection sees no FK.

7. **Name hints must fit the type** — `semanticFits` stops a column named `update_time` stored as `bigint` from getting a clock-time string. Add a case to `TestMapColumnToFaker_SemanticNameNeverOverridesAnIncompatibleType` when adding semantic mappings.

8. **Multi-column UNIQUE** — `schema.Table.Unique` comes from unique indexes. `enforceUniqueGroups` runs after rules are applied: it regenerates a free column (not key, not ruled) of a repeated tuple, or drops the row and rebuilds the PK pool with `rebuildPKPool` so children never reference it. Self-references are backfilled after this step for the same reason.

9. **Memory must not grow with table size** — `faker.Stream` holds everything generation needs between chunks, and every structure is bounded: PK pools are reservoir samples of `poolLimit` values with the largest id kept last (`nextSequentialPK` reads it), re-drawn every `poolLimit` child rows so children spread over the whole parent; key sets (`keySet`) switch from a map to a scalable Bloom filter past `DefaultExactKeys` (a false positive only skips a free value, never lets a duplicate through). Junction enumeration resumes from `Stream.cursor`. `Stream.GenerateChunks` is the only generation loop: one-shot `Generate` is a single unbounded chunk, and enum coverage counts across chunks. Inserts go through `db.SplitBatches` (row count, 65,535 placeholders, ~1MB). Do not reintroduce per-chunk database re-reads, unbounded maps, or code that collects a whole table before writing it.

10. **SQL text uses literals** — SQL that is printed or saved (generate, export, dry runs) goes through `db.RenderInsert`, which escapes per engine (MySQL treats backslash as an escape, Postgres does not). `db.BuildInsert`/`BuildBatchInsert` return placeholders for bound execution only; printing their query drops the values.

11. **Version differences in tests** — Postgres before 15 grants `CREATE` on schema `public` to `PUBLIC`; MySQL 5.7 has no enforced CHECK constraints and no roles. Tests that assert privileges or constraints must set the state they expect (e.g. `REVOKE CREATE ON SCHEMA public FROM PUBLIC`) instead of relying on a server default. Run other engine versions in throwaway containers on spare ports (docs/development.md), never by changing the image of the compose containers, whose data volumes an older server cannot open.

12. **Graph layout at scale** — `runBestLayout` (app.js) packs each dagre dependency level into rows sized to the canvas; dagre alone spreads a 30-table level over thousands of pixels. Check layout changes against the 150-table `wideSchemaDDL` fixture as well as a small schema.

---

## Lessons: do / don't

**Concurrency**
- Do lock anything two goroutines touch, callbacks passed to the seeder included.
- Do run `internal/web`, `internal/seeder`, `internal/faker` and the web-job integration tests with `-race`.
- Don't run MySQL `TRUNCATE` concurrently (breaks later FK checks).
- Don't share one random source between generators (lock contention).

**Tests**
- Do break the guarded code once (a compiling mutation) and see the test fail.
- Do assert database state or what the user sees, not that a call happened.
- Don't hardcode counts that depend on chunking or enum coverage.
- Do measure a child's memory with VmHWM, not rusage (it counts the parent).
- Do record seeded outputs before a perf change and compare hashes after.

**Databases**
- Don't switch compose DB versions over existing volumes; use throwaway containers.
- Don't rely on server defaults (PG < 15 `CREATE` on `public`, MySQL 5.7 CHECK/roles).
- Do give scratch databases unique `ss_<area>_*` names.

**Reads and failures**
- Do route new reads through `db.ReadOnce` (read-only, lock timeout, real cancel); never report an unknown count as 0.
- Do wrap goroutines in `safego.Run` and errors in `runerr.At`/`OnSide` so failures name side, phase and table.
- Don't trust the MySQL driver to stop a cancelled query: it keeps running without `KILL QUERY`.

**Relationship shapes**
- Do keep the unshaped path free of extra random draws (`--seed` hashes must not change).
- Do return dealt slots when a row is retried or dropped; never loop to fit a shape, adjust and warn.

**Web UI**
- Do check 320–1920 widths with the interaction active (search, dialogs, runs).
- Do test graph changes on `wideSchemaDDL(150)`, not only small schemas.
- Do select elements by `data-testid` from `e2e/support/selectors.ts`.
- Do use `minmax(0, 1fr)` / `min-width: 0` for grid children holding tables.

**Claims**
- Do put performance claims in docs/benchmarks.md with how to reproduce them.
- Don't call something validated until it ran end to end (CLI, browser, DB).
