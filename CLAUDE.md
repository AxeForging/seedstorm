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
│   │   └── helpers.go          # Shared helpers (buildInsert, normalizeDBType)
│   ├── db/                     # Database drivers and introspection
│   │   ├── db.go               # Introspect() dispatcher (postgres / mysql)
│   │   ├── postgres.go         # PostgreSQL schema introspection + constraint parsing
│   │   ├── mysql.go            # MySQL schema introspection + constraint parsing
│   │   ├── truncate.go         # Truncate helper (FK-safe order)
│   │   ├── counts.go           # GetTableRowCounts helper (used by gaps)
│   │   └── types.go            # Shared db types (Table, Column, FK, …)
│   ├── faker/
│   │   ├── faker.go            # Generate / GenerateFiltered — core data generation
│   │   ├── mapper.go           # Column name → faker hint heuristics
│   │   └── *_test.go           # Unit tests alongside production files
│   ├── graph/
│   │   ├── graph.go            # Dependency graph (Build, TopologicalSort, RenderPlan)
│   │   └── graph_test.go       # Unit tests
│   ├── ai/ai.go                # Gemini enrichment (prompt building, response parsing)
│   ├── schema/schema.go        # Schema YAML types and loader
│   ├── build/info.go           # Version info injected at build time
│   └── logging/logging.go      # Zerolog setup
├── integration/
│   ├── integration_test.go     # Integration tests (build tag: integration)
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
  cd integration && go test -v -tags integration -count=1 ./... -timeout 300s
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

### Composite PK safety

All three generation paths (`generateStandardRows`, `generateEnumRows`, `topUpEnumCoverage`) use a `seenKeys` map and a 200-attempt retry loop before returning an error. This prevents silent duplicate composite PK inserts into junction tables. If you add a new generation path, it must include the same guard.

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
| `integration` | Full 28-table suite on Postgres 15 + MySQL 8 |

The integration job in CI uses `--timeout 120s`. Locally use `300s` when running both engines back-to-back.

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

4. **seenKeys does not include existing DB rows** — The composite PK collision check only tracks rows generated in the current run. Re-seeding a partially populated table (without `--truncate`) can still collide with pre-existing rows for composite-PK junction tables. This is a known limitation to address in a future PR.

5. **Constraint introspection** — Postgres and MySQL serialize CHECK constraints differently. Both parsers live in `db/postgres.go` and `db/mysql.go` respectively. Add tests to `db/constraint_test.go` whenever you touch constraint parsing.
