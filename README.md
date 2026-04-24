# seedstorm

Dynamic database seeder with schema self-discovery, FK-aware ordering, and AI enrichment. Works with MySQL 8+ and PostgreSQL 13+.

## Table of Contents

- [Features](#features)
- [Workflow](#workflow)
- [Installation](#installation)
- [Quick Start](#quick-start)
- [Commands](#commands)
  - [`introspect`](#introspect) — discover schema from a live DB
  - [`ai-enrich`](#ai-enrich) — AI-powered semantic faker mapping
  - [`seed`](#seed) — generate and insert fake data
  - [`gaps`](#gaps) — find and fill empty tables
  - [`generate`](#generate) — generate data without a DB connection
  - [`export`](#export) — convert data between formats
  - [`version`](#version) / [`completion`](#completion)
- [Examples](EXAMPLES.md) — end-to-end walkthroughs
- [Schema YAML Format](#schema-yaml-format)
- [Local Development](#local-development)
- [Testing](#testing)
- [Environment Variables](#environment-variables)
- [Makefile Targets](#makefile-targets)

---

## Features

- **Schema self-discovery** — introspects tables, columns, types, PKs, FKs, enum values, UNIQUE constraints, and CHECK constraints from a live database; no manual schema editing required
- **Constraint-aware faker mapping** — automatically detects UNIQUE constraints (→ `uuid`), CHECK IN constraints (→ `randomstring(a,b,c)`), and CHECK range constraints (→ `number(min,max)`) for both PostgreSQL and MySQL; seed data always satisfies your constraints out of the box
- **FK-aware seeding** — topological sort guarantees parent tables are seeded before their children; handles self-referential FKs, near-cycles (nullable FK edges), junction tables with composite PK+FK columns, and deep multi-level chains without loops or infinite retries
- **Semantic faker** — maps column names (`email`, `first_name`, `price`, `city`…) and DB types to realistic `gofakeit` generators automatically
- **Automatic enum coverage** — after standard row generation, every enum value is guaranteed to appear at least `--rows` times; each column is handled independently so multi-enum tables (e.g. `status` + `priority`) get full coverage without a cartesian product
- **AI enrichment** — Gemini reads your full schema and rewrites faker hints for business-meaningful data (`product.name` → `productname`, `order.notes` → `sentence`, etc.); supply `--prompt` for a domain hint so the AI has richer context
- **Truncate before seeding** — `--truncate` clears all tables in FK-safe order before inserting; prompts for confirmation unless `--yes` is passed
- **Dry-run mode** — prints a formatted seed plan (table order + FK dependencies) followed by INSERT SQL, without touching the database
- **Gap analysis** — `gaps` command shows which tables are empty vs. populated with row counts and FK context; `--fill` seeds only the empty tables, leaving existing data untouched
- **Generate without inserting** — export fake data as YAML, JSON, or SQL
- **Pretty logs** — colored, timestamped zerolog output at each pipeline step

## Workflow

```
1. introspect  →  schema.yaml           discover schema from live DB
2. ai-enrich   →  schema.enriched.yaml  AI-powered semantic faker mapping
3. seed        →  database              insert FK-ordered fake rows
4. gaps        →  report / --fill       detect and fill unpopulated tables
```

---

## Installation

```bash
go install github.com/AxeForging/seedstorm/cmd/seedstorm@latest
```

Or download a binary from [Releases](https://github.com/AxeForging/seedstorm/releases).

---

## Quick Start

```bash
# 1. Start local databases
make dev-up

# 2. Discover schema from your database
seedstorm introspect \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --out schema.yaml

# 3. (Optional) AI-enrich faker mappings for domain-meaningful data
#    Use --prompt to give the AI context about your application domain
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
  --prompt "Mexican taco shop — products are tacos, burritos, and salsas" \
  --out schema.enriched.yaml

# 4. Seed 100 rows per table
seedstorm seed \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.enriched.yaml \
  --rows 100
```

Sample output:

```
23:33:54 INFO   Loading schema path=schema.enriched.yaml
23:33:54 INFO   Building dependency graph
23:33:54 INFO   Seed order resolved order="companies → brands → categories → users → departments → products → employees → ..."
23:33:54 INFO   Connecting to database db=postgres
23:33:54 INFO   Generating fake data rows=100
23:33:54 INFO   Seeding table table=companies rows=100
23:33:54 INFO   Seeding table table=brands rows=100
...
23:33:55 INFO   Seeding complete tables=28 total_rows=2800 duration=1.4s
```

---

## Commands

### `introspect`

Connects to a database and outputs a `schema.yaml` with all tables, columns, types, PKs, FKs, and enum values.

```bash
# PostgreSQL
seedstorm introspect \
  --db postgres \
  --dsn "postgres://user:pass@localhost:5432/mydb" \
  --out schema.yaml

# MySQL
seedstorm introspect \
  --db mysql \
  --dsn "user:pass@tcp(localhost:3306)/mydb" \
  --out schema.yaml

# Via env vars
SEEDSTORM_DB=postgres SEEDSTORM_DSN="postgres://..." seedstorm introspect
```

| Flag | Default | Description |
|------|---------|-------------|
| `--db` / `$SEEDSTORM_DB` | `postgres` | Database type: `postgres` or `mysql` |
| `--dsn` / `$SEEDSTORM_DSN` | — | Connection string (required) |
| `--out` / `-o` | `schema.yaml` | Output file path |

---

### `ai-enrich`

Sends the schema to Gemini and rewrites `faker` hints with domain-aware generators based on table and column names.

```bash
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
  --out schema.enriched.yaml

# Supply an application domain hint for richer AI context
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
  --prompt "HR management system" \
  --out schema.enriched.yaml

# Use a specific model
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
  --model gemini-2.5-flash \
  --out schema.enriched.yaml
```

The `--prompt` hint is injected into every Gemini call as `Application domain / context: <hint>`, helping the model choose more realistic fakers. For example, with `--prompt "TacoShop"`:
- `products.name` → `productname` instead of `word`
- `orders.delivery_address` → `street` instead of `sentence`
- `categories.name` → `productname` (food category) instead of `word`

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Input schema file |
| `--out` / `-o` | `schema.enriched.yaml` | Output enriched schema |
| `--model` / `-m` / `$SEEDSTORM_AI_MODEL` | `gemini-2.5-flash` | Gemini model to use |
| `--prompt` | — | Optional application domain hint (e.g. `"TacoShop"`, `"HR management system"`) |

---

### `seed`

Reads a schema, generates fake data, and inserts it into the database in FK-safe order.

```bash
# Seed 50 rows per table
seedstorm seed \
  --db mysql \
  --dsn "root:pass@tcp(localhost:3306)/mydb" \
  --schema schema.yaml \
  --rows 50

# Dry-run: print seed plan + SQL without executing
seedstorm seed \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.yaml \
  --dry-run

# Seed enum tables with N rows per enum value
seedstorm seed \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --enum-rows 10
```

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Schema file |
| `--db` / `$SEEDSTORM_DB` | `postgres` | Database type |
| `--dsn` / `$SEEDSTORM_DSN` | — | Connection string (required) |
| `--rows` / `-r` | `100` | Rows per table |
| `--enum-rows` | `0` | Rows per enum value (0 = use `--rows`) |
| `--disable-fk` | false | Skip FK ordering |
| `--dry-run` / `-n` | false | Print seed plan + SQL, do not execute |
| `--truncate` | false | Truncate all tables before seeding (prompts for confirmation) |
| `--yes` / `-y` | false | Skip confirmation prompt (use with `--truncate`) |
| `--batch-size` | `100` | Number of rows per INSERT statement (batched multi-row VALUES) |
| `--seed` | `0` | Random seed for reproducible generation (0 = random) |
| `--interactive` / `-i` | false | Launch interactive TUI to select tables and configure seeding |

---

### `gaps`

Connects to the database, queries row counts for every table in the schema, and prints a gap analysis report. Use `--fill` to seed only the empty tables — already-populated tables are never touched.

```bash
# Show which tables are empty
seedstorm gaps \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.yaml

# Fill empty tables with 50 rows each (prompts for confirmation)
seedstorm gaps \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.yaml \
  --fill --rows 50

# Preview SQL without executing
seedstorm gaps \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.yaml \
  --fill --dry-run

# Skip confirmation prompt
seedstorm gaps ... --fill --yes
```

Sample output (gap analysis report):

```
Gap Analysis
────────────────────────────────────────────────────────────
  Table                    Rows  Status
  ───────────────────────  ────  ────────────────────────────────────────
  brands                    100  populated
  users                     100  populated
  categories                  0  EMPTY → would seed 50 rows
  products                    0  EMPTY → would seed 50 rows  [FK → categories (0 rows, filling), brands (100 rows)]
  orders                      0  EMPTY → would seed 50 rows  [FK → users (100 rows)]

  Gaps: 3 table(s) empty · Would seed: 150 rows total
```

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Schema file |
| `--db` / `$SEEDSTORM_DB` | `postgres` | Database type |
| `--dsn` / `$SEEDSTORM_DSN` | — | Connection string (required) |
| `--rows` / `-r` | `100` | Rows per empty table (when `--fill` is set) |
| `--enum-rows` | `0` | Rows per enum value for empty enum tables (0 = use `--rows`) |
| `--fill` | false | Seed all empty tables |
| `--dry-run` / `-n` | false | Print SQL without executing (requires `--fill`) |
| `--yes` / `-y` | false | Skip confirmation prompt |
| `--batch-size` | `100` | Number of rows per INSERT statement (batched multi-row VALUES) |
| `--interactive` / `-i` | false | Launch interactive TUI to select empty tables and configure filling |

---

### `generate`

Generates fake data without connecting to a database. Outputs YAML, JSON, or SQL.

```bash
seedstorm generate --schema schema.yaml --rows 10 --format json --out data.json
seedstorm generate --schema schema.yaml --rows 5  --format sql  --db postgres
seedstorm generate --schema schema.yaml --rows 20 --format yaml
```

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Schema file |
| `--rows` / `-r` | `100` | Rows per table |
| `--format` / `-f` | `yaml` | Output format: `yaml`, `json`, `sql` |
| `--out` / `-o` | stdout | Output file (omit for stdout) |
| `--db` | `postgres` | DB type (affects SQL placeholder style) |
| `--seed` | `0` | Random seed for reproducible generation (0 = random) |
| `--interactive` / `-i` | false | Launch interactive TUI to select tables and configure generation |

---

### `export`

Converts a previously generated data file to another format.

```bash
seedstorm export --data data.yaml --format sql --out seed.sql
seedstorm export --data data.yaml --format csv --out data.csv
```

---

### `version`

```bash
seedstorm version
# version: v1.2.0  commit: abc1234  date: 2026-01-15  builtBy: goreleaser
```

### `completion`

```bash
seedstorm completion bash  >> ~/.bashrc
seedstorm completion zsh   >> ~/.zshrc
seedstorm completion fish  >> ~/.config/fish/completions/seedstorm.fish
```

---

## Examples

See **[EXAMPLES.md](EXAMPLES.md)** for complete end-to-end walkthroughs:

- **Basic seeding** (no AI) — introspect, seed, verify
- **AI-enriched seeding** — introspect, ai-enrich with domain context, seed domain-realistic data
- **Partial seeding with gaps** — seed some tables, use `gaps` to fill the rest
- **Reproducible generation** — use `--seed` for deterministic output
- **Dry-run inspection** — preview the seed plan and SQL before executing

---

## Schema YAML Format

The schema file produced by `introspect` (and consumed by `seed`/`generate`) looks like:

```yaml
tables:
  users:
    columns:
      id:
        type: integer
        pk: true
      email:
        type: varchar
        faker: email
      first_name:
        type: varchar
        faker: firstname
      created_at:
        type: timestamp
        faker: datetime
  orders:
    columns:
      id:
        type: integer
        pk: true
      user_id:
        type: integer
        fk: users.id        # FK resolved automatically
      status:
        type: varchar
        faker: randomstring(pending,processing,shipped,delivered)
      total_amount:
        type: numeric
        faker: price(10,500)
```

### Faker Hints Reference

| Hint | Generates |
|------|-----------|
| `email` | `user@example.com` |
| `firstname` / `lastname` / `name` | Person names |
| `username` | `CoolUser42` |
| `phone` | `+1-555-0123` |
| `city` / `country` / `state` / `zip` | Geographic data |
| `street` | Street address |
| `url` | `https://example.com/path` |
| `uuid` | `550e8400-e29b-41d4-a716-446655440000` |
| `price(min,max)` | `42.99` |
| `number(min,max)` | Integer in range |
| `sentence` | Short sentence |
| `paragraph(N)` | N-paragraph text |
| `bool` | `true` / `false` |
| `datetime` / `date` / `time` | Temporal values |
| `json` | `{"key":"word","value":"word"}` |
| `ipv4` | `192.168.1.42` |
| `company` | Company name |
| `productname` | Product name |
| `randomstring(a,b,c)` | Random pick from list |

---

## Local Development

```bash
# Start MySQL 8 + PostgreSQL 15 (Docker required)
make dev-up

# Build binary
make build
./bin/seedstorm --help

# Full dev cycle against local DBs
make dev-up

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

# Stop DBs
make dev-down
```

---

## Testing

### Unit tests

```bash
make test
# or
go test ./... -v
```

### Integration tests

Integration tests spin up a 28-table real-world schema against both MySQL and PostgreSQL, covering every FK edge case a production company schema throws at a seeder:

| Edge case | Tables |
|-----------|--------|
| Self-referential FK | `categories`, `departments`, `employees` |
| Near-cycle (nullable FK breaks it) | `departments.head_employee_id ↔ employees.department_id` |
| Deep FK chain (5 levels) | `return_requests → order_items → orders → users` |
| Many-to-many junctions | `product_tags`, `project_assignments`, `wishlist_items` |
| Multiple enums per table | `support_tickets` (status + priority) |
| 3-FK tables | `support_tickets`, `departments` |
| Dual JSONB columns | `audit_logs` |
| Supplier + procurement chain | `suppliers → purchase_orders → purchase_order_items` |
| Warehouse + inventory | `companies → warehouses → inventory` |
| UNIQUE constraint → `uuid` faker | `users.email`, `users.username`, `coupons.code` |
| CHECK IN constraint → `randomstring` faker | `users.role` (admin/user/guest) |
| CHECK range constraint → `number(min,max)` faker | `products.rating` (1–5) |

Tests verify:

- All 28 tables receive exactly the requested number of rows
- 38 FK relationships have zero orphans
- 6 value constraints hold (ratings 1–5, prices > 0, quantities ≥ 1, salaries > 0)
- All FK relationships are auto-discovered by `introspect`
- Enum values are correctly discovered for all enum columns
- UNIQUE columns are auto-detected and assigned `uuid` faker (no duplicates)
- CHECK IN constraints are auto-detected and assigned `randomstring(...)` faker
- CHECK range constraints are auto-detected and assigned `number(min,max)` faker
- Post-seed data validity: all seeded values satisfy the actual DB constraints
- Self-referential tables always have at least one root node (NULL parent)
- Deep 5-level FK chains are fully intact

```bash
# Requires running databases (make dev-up)
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
          categories           25 rows
          suppliers            25 rows
          tags                 25 rows
          users                25 rows
          departments          25 rows
          products             25 rows
          employees            25 rows
          ...
          audit_logs           25 rows
          Total: 700 rows across 28 tables (4.43s)
...
--- PASS: TestPostgresIntegration (6.87s)
```

### CI

All tests (unit, integration, lint, structlint) run automatically on every PR via GitHub Actions. The integration job runs as a matrix across the supported database versions (below), so every PR exercises each pair.

### Supported database versions

The integration suite is run against these combinations on every PR:

| Postgres | MySQL |
|----------|-------|
| 13-alpine | 8.0 |
| 15-alpine | 8.0 |
| 17-alpine | 8.4 |

Locally, override the image tag via env vars when bringing the stack up:

```bash
POSTGRES_VERSION=17-alpine MYSQL_VERSION=8.4 make dev-up
```

Defaults are `postgres:15-alpine` and `mysql:8.0`. MySQL 8.0.16+ is required for CHECK constraint introspection.

---

## Environment Variables

| Variable | Description |
|----------|-------------|
| `SEEDSTORM_DSN` | Default connection string |
| `SEEDSTORM_DB` | Default database type (`postgres` or `mysql`) |
| `GEMINI_API_KEY` | Gemini API key for `ai-enrich` |
| `SEEDSTORM_AI_MODEL` | Gemini model override (default: `gemini-2.5-flash`) |
| `SEEDSTORM_LOG_LEVEL` | Log level: `debug`, `info`, `warn`, `error` |

---

## Makefile Targets

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
