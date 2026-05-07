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

Integration tests run the full pipeline against a 28-table real-world schema on both MySQL and PostgreSQL, covering:

| Edge case | Tables |
|-----------|--------|
| Self-referential FK | `categories`, `departments`, `employees` |
| Near-cycle (nullable FK breaks it) | `departments.head_employee_id ↔ employees.department_id` |
| Deep FK chain (5 levels) | `return_requests → order_items → orders → users` |
| Many-to-many junctions | `product_tags`, `project_assignments`, `wishlist_items` |
| Multiple enums per table | `support_tickets` (status + priority) |
| 3-FK tables | `support_tickets`, `departments` |
| Dual JSONB columns | `audit_logs` |
| UNIQUE constraint → `uuid` faker | `users.email`, `users.username`, `coupons.code` |
| CHECK IN constraint → `randomstring` faker | `users.role` (admin/user/guest) |
| CHECK range constraint → `number(min,max)` faker | `products.rating` (1–5) |

Tests verify:
- All 28 tables receive exactly the requested number of rows
- 38 FK relationships have zero orphans
- 6 value constraints hold (ratings 1–5, prices > 0, quantities ≥ 1, salaries > 0)
- Enum values, UNIQUE columns, and CHECK constraints are auto-detected correctly

```bash
make dev-up
make test-integration

# Or directly
cd integration && go test -v -tags integration -count=1 ./... -timeout 300s
```

Expected output:

```
=== RUN   TestPostgresIntegration/introspect_and_seed
    integration_test.go:XXX: === Seed Summary (postgres) ===
          companies            25 rows
          brands               25 rows
          ...
          audit_logs           25 rows
          Total: 700 rows across 28 tables (4.43s)
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
| `integration` | Full 28-table suite on Postgres 15 + MySQL 8 |

The integration job in CI uses `--timeout 120s`. Use `300s` locally when running both engines back-to-back.

### Supported database versions

| Postgres | MySQL | Notes |
|----------|-------|-------|
| 13-alpine | 5.7 | CHECK-constraint subtests skipped (MySQL 5.7 does not expose `information_schema.CHECK_CONSTRAINTS`) |
| 13-alpine | 8.0 | |
| 15-alpine | 8.0 | default |
| 17-alpine | 8.4 | |

Override locally:

```bash
POSTGRES_VERSION=17-alpine MYSQL_VERSION=8.4 make dev-up
```

---

## Environment variables

| Variable | Description |
|----------|-------------|
| `SEEDSTORM_DSN` | Default connection string |
| `SEEDSTORM_DB` | Default database type (`postgres` or `mysql`) |
| `GEMINI_API_KEY` | Gemini API key for `ai-enrich` |
| `SEEDSTORM_AI_MODEL` | Gemini model override (default: `gemini-2.5-flash`) |
| `SEEDSTORM_LOG_LEVEL` | Log level: `debug`, `info`, `warn`, `error` |

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
