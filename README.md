# seedstorm

Dynamic database seeder with schema self-discovery, FK-aware ordering, and AI enrichment. Works with MySQL 8+ and PostgreSQL 13+.

## Features

- **Schema self-discovery** — introspects tables, columns, types, PKs, FKs, and enum values from a live database
- **FK-aware seeding** — topological sort guarantees parent tables are seeded before their children
- **Semantic faker** — maps column names (`email`, `first_name`, `price`, `city`…) and DB types to realistic `gofakeit` generators automatically
- **AI enrichment** — Gemini reads your full schema and rewrites faker hints for business-meaningful data (`product.name` → `productname`, `order.notes` → `sentence`, etc.)
- **Dry-run mode** — prints INSERT SQL without touching the database
- **Generate without inserting** — export fake data as YAML, JSON, or SQL
- **Pretty logs** — colored, timestamped zerolog output at each pipeline step

## Workflow

```
1. introspect  →  schema.yaml           discover schema from live DB
2. ai-enrich   →  schema.enriched.yaml  AI-powered semantic faker mapping
3. seed        →  database              insert FK-ordered fake rows
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
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
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
23:33:54 INFO   Seed order resolved order="brands → categories → users → products → orders → order_items"
23:33:54 INFO   Connecting to database db=postgres
23:33:54 INFO   Generating fake data rows=100
23:33:54 INFO   Seeding table table=brands rows=100
23:33:54 INFO   Seeding table table=categories rows=100
...
23:33:55 INFO   Seeding complete tables=6 total_rows=600 duration=892ms
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

# Use a specific model
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
  --model gemini-2.5-flash \
  --out schema.enriched.yaml
```

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Input schema file |
| `--out` / `-o` | `schema.enriched.yaml` | Output enriched schema |
| `--model` / `-m` / `$SEEDSTORM_AI_MODEL` | `gemini-2.5-flash` | Gemini model to use |
| `--db` / `$SEEDSTORM_DB` | `postgres` | DB type (for type-mapping context) |

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

# Dry-run: print SQL without executing
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
| `--dry-run` / `-n` | false | Print SQL, do not execute |

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

Integration tests spin up a 15-table e-commerce schema (brands, categories, products, orders, shipments, payments, reviews, wishlists, …) against both MySQL and PostgreSQL. They verify:

- All 15 tables receive rows
- All 17 FK relationships have zero orphans
- Value constraints hold (ratings 1–5, prices > 0, quantities ≥ 1)
- All FK relationships are auto-discovered by `introspect`
- Enum values are correctly discovered

```bash
# Requires running databases (make dev-up)
make dev-up
make test-integration

# Or directly
cd integration && go test -v -tags integration -count=1 ./... -timeout 120s
```

Expected output:

```
=== RUN   TestPostgresIntegration/introspect_and_seed
    integration_test.go:181: === Seed Summary (postgres) ===
          brands               25 rows
          categories           25 rows
          coupons              25 rows
          tags                 25 rows
          users                25 rows
          products             25 rows
          wishlists            25 rows
          addresses            25 rows
          reviews              25 rows
          product_tags         25 rows
          wishlist_items       25 rows
          orders               25 rows
          order_items          25 rows
          payments             25 rows
          shipments            25 rows
          Total: 375 rows across 15 tables (2.38s)
...
--- PASS: TestPostgresIntegration (3.52s)
```

### CI

All tests (unit, integration, lint, structlint) run automatically on every PR via GitHub Actions with MySQL 8 and PostgreSQL 15 service containers. No infrastructure needed in CI.

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
