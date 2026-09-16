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
│   │   ├── progress.go         # --workers flag, progress log lines, profile ignore
│   │   ├── profile.go          # profile command + shared --profile flag/loading
│   │   ├── endpoints.go        # --source-*/--target-* flags and connection opening
│   │   └── helpers.go          # Shared helpers (buildInsert, normalizeDBType)
│   ├── db/                     # Database drivers and introspection
│   │   ├── db.go               # Introspect() dispatcher (postgres / mysql)
│   │   ├── postgres.go         # PostgreSQL schema introspection + constraint parsing
│   │   ├── mysql.go            # MySQL schema introspection + constraint parsing
│   │   ├── truncate.go         # Truncate helper (FK-safe order)
│   │   ├── counts.go           # GetTableRowCounts helper (used by gaps)
│   │   ├── stats.go            # Table sizes, estimated counts, column lists, DB identity
│   │   ├── copy.go             # CopyRows: Postgres COPY for a chunk of rows
│   │   ├── sequences.go        # SyncSequences: move Postgres sequences past inserted ids
│   │   ├── access.go           # InspectAccess: the connected user's privileges (pg has_*_privilege, mysql SHOW GRANTS)
│   │   ├── objects.go          # Views, routines, triggers for clone-schema (BuildCloneDDL)
│   │   ├── transient.go        # IsTransient: deadlock / lock-timeout errors a writer may retry
│   │   └── types.go            # Shared db types (Table, Column, FK, …)
│   ├── faker/
│   │   ├── faker.go            # Generate / GenerateFiltered — core data generation
│   │   ├── mapper.go           # Column name → faker hint heuristics
│   │   ├── existing.go         # Awareness of rows already in the DB (keys, id and sequence continuation)
│   │   ├── overrides.go        # Column overrides (value rules), CoerceValue, ValueKind
│   │   ├── catalog.go          # Generator catalog, Evaluate, BuildSchema
│   │   ├── stream.go           # Stream: generation state kept across chunks (NewStream, Generate)
│   │   ├── keys.go             # keySet: exact map, then scalable Bloom filter
│   │   ├── pools.go            # PK pool reservoir sampling and capping
│   │   └── *_test.go           # Unit tests alongside production files
│   ├── graph/
│   │   ├── graph.go            # Dependency graph (Build, TopologicalSort, RenderPlan)
│   │   ├── ignore.go           # ApplyIgnore: drop ignored tables, refuse empty required parents
│   │   └── graph_test.go       # Unit tests
│   ├── rules/                  # Seed profile rules: model, templates, resolve/validate/compile
│   ├── profiles/               # Saved profile store (profiles.yaml) + Resolve(file|name)
│   ├── compare/                # Snapshots, Diff, PlanMirror, snapshot files (Encode/ParseSnapshot), renderers (never writes)
│   ├── dataio/                 # Streaming data documents: writers (yaml/json/sql/csv), ReadTables
│   ├── seeder/                 # Seed (strict chunked seed/gaps), Fill (resilient mirror inserts), MirrorJob, Preview,
│   │                           # writer.go (FK-gated concurrent writes), meter.go (rate/ETA)
│   ├── fsutil/                 # WriteFileAtomic for on-disk stores
│   ├── tui/                    # Bubble Tea flows (seed, gaps, generate, clone, mirror)
│   ├── web/                    # serve: handlers_*.go per area, templates/, static/ (page.js + page.css per page)
│   ├── ai/ai.go                # Gemini enrichment (prompt building, response parsing)
│   ├── schema/schema.go        # Schema YAML types and loader
│   ├── build/info.go           # Version info injected at build time
│   └── logging/logging.go      # Zerolog setup
├── integration/
│   ├── integration_test.go     # Integration tests (build tag: integration)
│   ├── binary_test.go          # Builds the binary; scratch DB helpers per engine
│   ├── *_test.go               # Scenario evals: reseed, mirror, seeder, keycloak, compare_web, parallel_seed,
│   │                           # wide_schema (150 generated tables), access, snapshot, clone_objects
│   ├── fixtures/               # Keycloak schema dumps (real-world 87-table stress schema)
│   ├── schema_postgres.sql     # 28-table schema for Postgres integration tests
│   └── schema_mysql.sql        # 28-table schema for MySQL integration tests
├── README.md                   # User-facing documentation (keep in sync with code)
├── Makefile                    # Build, test, lint, dev-up/down targets
├── compose.yaml                # Local MySQL + PostgreSQL via Docker Compose
└── .github/workflows/pr.yml    # CI: title, review, structlint, unit tests, lint, integration
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
  go test ./internal/...                          # unit tests
  make dev-up
  cd integration && go test -v -tags integration -count=1 ./... -timeout 900s
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
| `title` | Conventional Commits format |
| `review` | AI code review via reviewforge (Gemini) |
| `validate` | Directory/file structure via structlint |
| `test` | `go test ./...` + `make build` |
| `lint` | `golangci-lint` |
| `integration` | Full suite + scenario evals on Postgres 13/15/17 × MySQL 5.7/8.0/8.4 |

The integration job in CI uses `-timeout 900s` (the suite takes ~5 minutes with the Keycloak and mirror evals). Use the same locally.

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