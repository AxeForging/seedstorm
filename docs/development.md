# Development & Testing

Local setup, test strategy, CI pipeline, environment variables, and Makefile targets.

## Local setup

Requires Docker.

```bash
# Start MySQL 8 + PostgreSQL 15
make dev-up

# Build binary
make build
./bin/seedstorm --help

# Stop DBs
make dev-down
```

### Run against local databases

```bash
# PostgreSQL
./bin/seedstorm introspect \
  --db postgres \
  --dsn "postgres://seedstorm:seedstorm@localhost:5432/testdb" \
  --out schema.yaml

./bin/seedstorm seed \
  --db postgres \
  --dsn "postgres://seedstorm:seedstorm@localhost:5432/testdb" \
  --schema schema.yaml \
  --rows 50

# MySQL
./bin/seedstorm introspect \
  --db mysql \
  --dsn "seedstorm:seedstorm@tcp(localhost:3306)/testdb" \
  --out schema.yaml

./bin/seedstorm seed \
  --db mysql \
  --dsn "seedstorm:seedstorm@tcp(localhost:3306)/testdb" \
  --schema schema.yaml \
  --rows 50
```

---

## Tests

### Unit tests

```bash
make test
# or
go test ./... -v
```

243+ tests across `faker`, `graph`, `db`, `ai`, `cli`, and `tui` packages. Every function with logic has a test alongside it.

### Integration tests

Integration tests run the full pipeline against a 29-table real-world schema on both MySQL and PostgreSQL, covering:

| Edge case | Tables |
|-----------|--------|
| Self-referential FK | `categories`, `departments`, `employees` |
| Near-cycle (nullable FK breaks it) | `departments.head_employee_id ↔ employees.department_id` |
| Hard self-reference | `hard_self_employees.manager_id → hard_self_employees.id` |
| Deep FK chain (5 levels) | `return_requests → order_items → orders → users` |
| Many-to-many junctions | `product_tags`, `project_assignments`, `wishlist_items` |
| Multiple enums per table | `support_tickets` (status + priority) |
| 3-FK tables | `support_tickets`, `departments` |
| Dual JSONB columns | `audit_logs` |
| UNIQUE constraint → `uuid` faker | `users.email`, `users.username`, `coupons.code` |
| CHECK IN constraint → `randomstring` faker | `users.role` (admin/user/guest) |
| CHECK range constraint → `number(min,max)` faker | `products.rating` (1–5) |

Tests verify:
- All 29 tables receive rows, with enum-coverage tables allowed to exceed the base request so every enum value is represented
- 39 FK relationships have zero orphans, including nullable and non-nullable self-references
- 6 value constraints hold (ratings 1–5, prices > 0, quantities ≥ 1, salaries > 0)
- Enum values, UNIQUE columns, and CHECK constraints are auto-detected correctly

Scenario evals drive the built `seedstorm` binary (and the web server) against
scratch databases they create and drop, so they test exactly what a user runs:

| Eval | Proves |
|------|--------|
| `TestSeed_TwiceWithoutTruncateAppendsRows` | Re-seeding a populated DB appends (sparse ids, UNIQUE sequences, composite keys) |
| `TestMirror_BinaryEndToEnd` | `seed --profile`, `compare`, `mirror` dry-run / top-up / 2x top-up / reset, confirmation, same-DB refusal, source untouched |
| `TestSeederFill_*` | Rejected rows are regenerated, impossible tables fail fast, key exhaustion is reported, `--stop-on-error` |
| `TestKeycloak_SeedCloneAndMirror` | The same workflow on Keycloak's real 87-table schema (`integration/fixtures/`), Postgres, MySQL, and MySQL → Postgres |
| `TestSeed_LargeRunsStreamWithFlatMemory` | `seed` and `gaps --fill` of 300k rows stay under 150MB peak memory with unique values, no orphans and children spread over parents |
| `TestSeed_WideTableStaysUnderThePlaceholderLimit` | An 80-column table seeds at the default batch size without exceeding 65,535 placeholders |
| `TestGenerateExport_LargeFilesStreamWithFlatMemory` | `generate` and `export` of 300k rows stay under 150MB peak (they used 1.6GB and 2.1GB) |
| `TestExport_SQLLoadsIntoEachEngineWithExactValues` | Exported and generated SQL runs on each engine and round-trips quotes, backslashes, newlines, unicode and NULL |
| `TestSequences_*` | The application's own inserts succeed after `seed`, `gaps --fill`, `mirror` top-up and reset (Postgres sequences advanced) |
| `TestCompareEstimates_*` | `--counts estimate` never reports unknown or stale-zero counts |
| `TestWebCompareMirrorAndProfiles_realDatabases` | Web API: profiles, explain samples, compare and mirror jobs, saved-connection targets |
| `TestSeed_ConcurrentWritersMatchSequentialAndKeepForeignKeys` | `--workers 8` writes the 36-table schema with the same volumes as `--workers 1`, every FK enforced by the database (caught parallel MySQL `TRUNCATE` breaking FK checks) |
| `TestSeed_WideSchemaWithCrossReferences` | A generated 150-table schema (FK fan-out, junctions, self-references, near-cycles) seeds and re-seeds with 8 writers; `wideSchemaDDL` is also the fixture for reviewing the workspace graph at scale |
| `TestSeed_ProfileIgnoreListIsHonoured` | Ignored tables stay empty; an ignored populated parent is referenced; an ignored empty required parent refuses the run before writing |
| `TestAccess_Postgres` / `TestAccess_MySQL` | Privilege reports for limited users, group roles and MySQL roles match what the server enforces, including Postgres 13's `CREATE` on `public` through `PUBLIC` |
| `TestSnapshot_BinaryEndToEnd` / `TestSnapshot_CrossEngine` | `snapshot` files (YAML, JSON, hand-written) as compare/mirror sources, readable errors for malformed files |
| `TestSeed_ConcurrentGenerationKeepsEveryConstraint` | `--gen-workers 4 --workers 8` on the 36-table, 150-table and Keycloak schemas: every FK, junction key and UNIQUE the database enforces holds, twice in a row |
| `TestGenerate_SameSeedWritesIdenticalData` | Two `generate --seed 42` runs write byte-identical files, and another seed differs (caught table order following map iteration, dates ending at "now", unseeded sampling) |
| `TestSeed_WideRowsStayUnderAMemoryBound` | ~20KB rows seed under 300MB on both engines with 1 and 8 writers (one old-style chunk alone was ~400MB) |
| `TestSeed_ManyTablesDoNotKeepEveryKeyPool` | 100 tables × 30k rows peak 82MB; keeping every key pool peaked at 262MB |
| `TestWebJobs_SeedProgressIsTruthful` / `…CancelIsPromptAndLeavesAUsableServer` / `…GapsFillAndMirrorReportProgress` / `…ConcurrentSeedJobs` | In-process server on real DBs: progress is monotonic and ends at done == total == `COUNT(*)`, one `Table written` per table, cancel ends within 15s with no queries left, concurrent jobs with viewers joining and leaving stay correct. Run with `-race` (found two job-manager races) |
| `TestCloneSchema_Objects` | `clone-schema --objects all`: view on view, function, procedure and trigger work on the clone; nothing extra without the flags |

Scratch databases on MySQL are created as `root` (`SEEDSTORM_MYSQL_ROOT_PASSWORD`, default `root`).

```bash
make dev-up
make test-integration

# Or directly (the race detector matters: the web-job tests run the server in-process)
cd integration && go test -race -v -tags integration -count=1 ./... -timeout 1500s
```

### End-to-end tests (Playwright)

The web UI has Playwright journeys in `e2e/` (pnpm). They build the binary, create `ss_e2e_*` scratch databases on the compose Postgres and MySQL (honouring `SEEDSTORM_PG_PORT` / `SEEDSTORM_MYSQL_PORT`), start `seedstorm serve` on a free port with its config in a temp dir, and tear everything down afterwards.

```bash
make dev-up
make test-e2e                          # all journeys
make test-e2e ARGS=tests/compare.spec.ts
cd e2e && SEEDSTORM_E2E_BIN=../bin/seedstorm pnpm exec playwright test   # reuse a built binary
SEEDSTORM_E2E_KEEP=1 make test-e2e     # keep the scratch databases for debugging
```

| Spec | Journey |
|------|---------|
| `workspace` | 150-table graph opens readable (zoom ≥ 0.3), search match bar steps through matches, Only matches / Show full graph, Navigator and minimap |
| `seed` | Select a deep table (auto-locked parents), 20k rows with 4 writers: progress never decreases and ends at N / N, every table ends done, SQL counts match |
| `clone` | Clone with views, routines and triggers; the trigger fires, the function and view-on-view work on the target |
| `compare` | Compare, normal-sized Advanced checkbox, export YAML → import file → report restored after navigating away, readable import error, mirror from imported counts with SQL check |
| `access` | SELECT-only MySQL user: read-only badge, banner, warning chips |
| `profiles` | Ignore glob with live matches, saved to disk, Ignored tab in the workspace, exported YAML |
| `mobile` | 390 and 320 wide with search and a wide preview open: no overlap, no sideways scroll |

Specs find elements only through `data-testid` names kept in `e2e/support/selectors.ts`: when you change markup a journey uses, keep or move the test id and update that file. Each journey checks what the user sees and, when it writes, the database via SQL. Static assets are embedded in the binary, so the suite always runs against a fresh build.

Expected output:

```
=== RUN   TestPostgresIntegration/introspect_and_seed
    integration_test.go:XXX: === Seed Summary (postgres) ===
          companies            25 rows
          brands               25 rows
          ...
          audit_logs           25 rows
          Total: 1600+ rows across 29 tables (11.30s)
--- PASS: TestPostgresIntegration (6.87s)
```

---

## CI pipeline

All tests run automatically on every PR via GitHub Actions (`.github/workflows/pr.yml`).

| Job | What it checks |
|-----|---------------|
| `title` | Conventional Commits format |
| `review` | AI code review via reviewforge (Gemini) |
| `validate` | Directory/file structure via structlint |
| `test` | `go test ./...` + `make build` |
| `lint` | `golangci-lint` |
| `integration` | Full 29-table suite, scenario evals and schema-clone tests with `-race` on each Postgres/MySQL pair |
| `e2e` | Playwright journeys against a built binary (Postgres 15, MySQL 8.0) |

The integration job in CI uses `-race -timeout 1500s` across the database-version matrix. Use the same timeout locally when running both engines back-to-back.

Throughput and memory numbers, and how to reproduce them, are in [benchmarks.md](benchmarks.md).

### Supported database versions

| Postgres | MySQL | Notes |
|----------|-------|-------|
| 13-alpine | 5.7 | CHECK-constraint subtests skipped (MySQL 5.7 does not expose `information_schema.CHECK_CONSTRAINTS`) |
| 13-alpine | 8.0 | |
| 15-alpine | 8.0 | default |
| 17-alpine | 8.4 | |

To run another pair locally, start throwaway containers on spare ports and point the tests at them. Do **not** switch `POSTGRES_VERSION` / `MYSQL_VERSION` on the existing `docker compose` containers: they reuse the data volumes, an older server refuses (Postgres) or damages (MySQL 5.7 over an 8.0 data directory) them.

```bash
docker run -d --name ss-pg13 -e POSTGRES_USER=seedstorm -e POSTGRES_PASSWORD=seedstorm -e POSTGRES_DB=testdb -p 5413:5432 postgres:13-alpine
docker run -d --name ss-my57 -e MYSQL_ROOT_PASSWORD=root -e MYSQL_USER=seedstorm -e MYSQL_PASSWORD=seedstorm -e MYSQL_DATABASE=testdb -p 3357:3306 mysql:5.7
cd integration && SEEDSTORM_PG_PORT=5413 SEEDSTORM_MYSQL_PORT=3357 go test -race -tags integration -count=1 ./... -timeout 1500s
docker rm -f ss-pg13 ss-my57
```

The engine-specific suites (`TestMySQLIntegration`, `TestMySQLGaps`, `TestMySQLSchemaCloneDDL` and their Postgres twins) still connect to the default ports; CI runs them against every pair.

---

## Environment variables

| Variable | Description |
|----------|-------------|
| `SEEDSTORM_DSN` | Default connection string |
| `SEEDSTORM_DB` | Default database type (`postgres` or `mysql`) |
| `GEMINI_API_KEY` | Gemini API key for `ai-enrich` |
| `SEEDSTORM_AI_MODEL` | Gemini model override (default: `gemini-2.5-flash`) |
| `SEEDSTORM_LOG_LEVEL` | Log level: `debug`, `info`, `warn`, `error` |
| `SEEDSTORM_PROFILE` | Default `--profile` for `seed`, `gaps`, `generate`, `mirror` |
| `SEEDSTORM_PROFILES` | Path of the saved-profile store (default `~/.config/seedstorm/profiles.yaml`) |
| `SEEDSTORM_SOURCE_DSN` / `SEEDSTORM_TARGET_DSN` | Defaults for `compare`, `mirror`, `clone-schema` |
| `SEEDSTORM_PG_HOST` / `SEEDSTORM_PG_PORT` / `SEEDSTORM_MYSQL_HOST` / `SEEDSTORM_MYSQL_PORT` | Integration tests: where the databases listen |

---

## Makefile targets

```
make build          Build for current platform → bin/seedstorm
make build-all      Build for linux/darwin amd64+arm64 → dist/
make test           Run unit tests
make test-integration  Run integration tests (requires make dev-up)
make lint           Run golangci-lint
make fmt            Format with gofumpt
make tidy           go mod tidy
make dev-up         Start local MySQL + PostgreSQL via Docker Compose
make dev-down       Stop local databases
make clean          Remove bin/ and dist/
make ci             tidy → lint → test → build
```

---

## PR conventions

All PR titles must follow [Conventional Commits](https://www.conventionalcommits.org/):

```
<type>[scope][!]: <description>
```

Allowed types: `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `build`, `ci`, `perf`, `revert`.

The CI `title` job enforces this — it will comment and fail if the format is wrong.
