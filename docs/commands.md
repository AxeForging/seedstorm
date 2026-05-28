# Command Reference

Every seedstorm command, with all flags and examples.

## Table of Contents

- [`introspect`](#introspect) — discover schema from a live DB
- [`ai-enrich`](#ai-enrich) — AI-powered semantic faker mapping
- [`seed`](#seed) — generate and insert fake data
- [`gaps`](#gaps) — find and fill empty tables
- [`generate`](#generate) — generate data without a DB connection
- [`export`](#export) — convert data between formats
- [`clone-schema`](#clone-schema) — copy schema structure into another DB
- [`serve`](#serve) — local web UI for every feature
- [`version`](#version) / [`completion`](#completion)

---

## `introspect`

Connects to a database and outputs a `schema.yaml` with all tables, columns, types, PKs, FKs, enum values, UNIQUE constraints, and CHECK constraints.

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

## `ai-enrich`

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

<img src="gifs/ai-enrich.gif" alt="ai-enrich demo" width="720" />

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Input schema file |
| `--out` / `-o` | `schema.enriched.yaml` | Output enriched schema |
| `--model` / `-m` / `$SEEDSTORM_AI_MODEL` | `gemini-2.5-flash` | Gemini model to use |
| `--prompt` | — | Optional application domain hint (e.g. `"TacoShop"`, `"HR management system"`) |

---

## `seed`

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

# Bound generated self-referential chains to 2 levels
seedstorm seed \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --self-ref-depth 2

# Override specific table volumes from a scripted run
seedstorm seed \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --rows 20 \
  --table-rows users=200,orders=500 \
  --table-rows order_items=1000

# Interactive TUI — pick tables, configure options, review, then seed
seedstorm seed \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --interactive
```

The interactive TUI includes a **Volumes** step after global config. Each selected table starts with the `--rows` value, and you can override individual tables before review, dry-run, or execution.

<img src="gifs/seed-interactive.gif" alt="seed interactive TUI demo" width="720" />

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Schema file |
| `--db` / `$SEEDSTORM_DB` | `postgres` | Database type |
| `--dsn` / `$SEEDSTORM_DSN` | — | Connection string (required) |
| `--rows` / `-r` | `100` | Rows per table |
| `--table-rows` | — | Per-table row override, repeatable or comma-separated (`table=rows`) |
| `--enum-rows` | `0` | Rows per enum value (0 = use `--rows`) |
| `--self-ref-depth` | `2` | Maximum generated depth for self-referential FK chains |
| `--disable-fk` | false | Skip FK ordering |
| `--dry-run` / `-n` | false | Print seed plan + SQL, do not execute |
| `--truncate` | false | Truncate all tables before seeding (prompts for confirmation) |
| `--yes` / `-y` | false | Skip confirmation prompt (use with `--truncate`) |
| `--batch-size` | `100` | Number of rows per INSERT statement |
| `--seed` | `0` | Random seed for reproducible generation (0 = random) |
| `--interactive` / `-i` | false | Launch interactive TUI |

---

## `gaps`

Connects to the database, queries row counts for every table in the schema, and prints a gap analysis report. Use `--fill` to seed only the empty tables — already-populated tables are never touched.

```bash
# Show which tables are empty
seedstorm gaps \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.yaml

# Fill empty tables with 50 rows each
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

# Interactive TUI
seedstorm gaps \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --interactive
```

Interactive gap fill also includes the **Volumes** step, so empty child tables can receive higher or lower row counts than their auto-required parents.

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

<img src="gifs/gaps.gif" alt="gaps demo" width="720" />

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Schema file |
| `--db` / `$SEEDSTORM_DB` | `postgres` | Database type |
| `--dsn` / `$SEEDSTORM_DSN` | — | Connection string (required) |
| `--rows` / `-r` | `100` | Rows per empty table (when `--fill` is set) |
| `--table-rows` | — | Per-table row override for fill, repeatable or comma-separated (`table=rows`) |
| `--enum-rows` | `0` | Rows per enum value for empty enum tables (0 = use `--rows`) |
| `--self-ref-depth` | `2` | Maximum generated depth for self-referential FK chains |
| `--fill` | false | Seed all empty tables |
| `--dry-run` / `-n` | false | Print SQL without executing (requires `--fill`) |
| `--yes` / `-y` | false | Skip confirmation prompt |
| `--batch-size` | `100` | Number of rows per INSERT statement |
| `--interactive` / `-i` | false | Launch interactive TUI |

---

## `generate`

Generates fake data without connecting to a database. Outputs YAML, JSON, or SQL.

```bash
seedstorm generate --schema schema.yaml --rows 10 --format json --out data.json
seedstorm generate --schema schema.yaml --rows 5  --format sql  --db postgres
seedstorm generate --schema schema.yaml --rows 20 --format yaml
seedstorm generate --schema schema.yaml --rows 20 --self-ref-depth 3
seedstorm generate --schema schema.yaml --rows 20 --table-rows users=200,orders=500

# Interactive TUI
seedstorm generate --schema schema.yaml --interactive
```

In interactive mode, the **Volumes** step can override row counts per selected table while `--rows` remains the default.

<img src="gifs/generate.gif" alt="generate demo" width="720" />

| Flag | Default | Description |
|------|---------|-------------|
| `--schema` / `-s` | `schema.yaml` | Schema file |
| `--rows` / `-r` | `100` | Rows per table |
| `--table-rows` | — | Per-table row override, repeatable or comma-separated (`table=rows`) |
| `--self-ref-depth` | `2` | Maximum generated depth for self-referential FK chains |
| `--format` / `-f` | `yaml` | Output format: `yaml`, `json`, `sql` |
| `--out` / `-o` | stdout | Output file (omit for stdout) |
| `--db` | `postgres` | DB type (affects SQL placeholder style) |
| `--seed` | `0` | Random seed for reproducible generation (0 = random) |
| `--interactive` / `-i` | false | Launch interactive TUI |

---

## `export`

Converts a previously generated data file to another format.

```bash
seedstorm export --data data.yaml --format sql --out seed.sql
seedstorm export --data data.yaml --format csv --out data.csv
```

<img src="gifs/export.gif" alt="export demo" width="720" />

---

## `clone-schema`

Copies schema-only table structure from a source database into a target database of the same engine. This is designed for local/test database setup before running `seed`; it recreates compatible table metadata seedstorm understands: tables, columns, exact introspected column DDL types, nullability, defaults, stored generated columns, PKs, FKs, single-column UNIQUE constraints, multi-column indexes, enum values, simple CHECK constraints, and table/column comments.

```bash
seedstorm clone-schema \
  --source-db postgres \
  --source-dsn "postgres://user:pass@prod.example/app" \
  --target-db postgres \
  --target-dsn "postgres://seedstorm:seedstorm@localhost:5432/testdb"

# Replace existing target tables
seedstorm clone-schema \
  --source-db mysql \
  --source-dsn "user:pass@tcp(staging.example:3306)/app" \
  --target-db mysql \
  --target-dsn "seedstorm:seedstorm@tcp(localhost:3306)/testdb" \
  --drop-existing

# Preview generated DDL
seedstorm clone-schema \
  --source-db postgres \
  --source-dsn "postgres://..." \
  --target-db postgres \
  --target-dsn "postgres://..." \
  --dry-run
```

| Flag | Default | Description |
|------|---------|-------------|
| `--source-db` / `$SEEDSTORM_SOURCE_DB` | `postgres` | Source database type |
| `--source-dsn` / `$SEEDSTORM_SOURCE_DSN` | — | Source connection string (required) |
| `--target-db` / `$SEEDSTORM_TARGET_DB` | `postgres` | Target database type |
| `--target-dsn` / `$SEEDSTORM_TARGET_DSN` | — | Target connection string (required) |
| `--drop-existing` | false | Drop target tables before creating the cloned schema |
| `--dry-run` / `-n` | false | Print generated DDL, do not execute |
| `--interactive` / `-i` | false | Confirm the clone in the terminal UI |

Boundaries: `clone-schema` is same-engine only. It does not attempt cross-engine translation, and it does not clone views, triggers, functions/procedures, partial/expression indexes, grants, ownership, or non-public/non-current schemas.

---

## `serve`

Starts a local web UI that exposes every seedstorm feature behind an interactive graph workspace. The UI is bundled into the binary via `go:embed` — no extra files to ship.

```bash
seedstorm serve                              # listens on 127.0.0.1:8080
seedstorm serve --addr 127.0.0.1:9000        # custom port
SEEDSTORM_ADDR=127.0.0.1:9000 seedstorm serve
```

What the UI gives you:

- **Workspace** — Cytoscape DAG of every table; click to select, non-nullable parents auto-lock as a dependency closure (mirrors the TUI). The selected-table panel lets you override row counts per table for **Seed**, **Fill empty**, and workspace **Generate** runs while `Rows` remains the default. `Self-ref` controls bounded generated depth for self-referential FK chains. Live SSE log stream + status pill.
- **Seed controls** — `Rows = 0` with `truncate` enabled is a truncate-only run for the selected scope, including auto-required parents. No rows are generated afterward.
- **Connection management** — multi-session: hold several DBs open in one browser and switch from a topbar dropdown. Saved connection presets in `localStorage` with optional password (eye-icon reveal, closed by default). Passwords are kept in process memory only on the server. The workspace can clone schema from the active connection into another matching connected database.
- **Standalone tools** — `/generate`, `/enrich`, `/export` mirror the CLI commands as forms.

| Flag | Default | Description |
|------|---------|-------------|
| `--addr` / `$SEEDSTORM_ADDR` | `127.0.0.1:8080` | Address to listen on (`host:port`) |

> AI Enrich requires `GEMINI_API_KEY` to be set in the environment of the seedstorm process.

---

## `version`

```bash
seedstorm version
# version: v1.2.0  commit: abc1234  date: 2026-01-15  builtBy: goreleaser
```

## `completion`

```bash
seedstorm completion bash  >> ~/.bashrc
seedstorm completion zsh   >> ~/.zshrc
seedstorm completion fish  >> ~/.config/fish/completions/seedstorm.fish
```
