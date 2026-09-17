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
- [`compare`](#compare) — row counts, sizes and column drift between two DBs
- [`mirror`](#mirror) — seed a target so its volumes follow a source
- [`snapshot`](#snapshot) — save row counts (and relationship shapes) to a file for compare/mirror
- [`tune`](#tune) — recommend writers and generators for a database
- [`profile`](#profile) — manage seed profiles (value rules)
- [`serve`](#serve) — local web UI for every feature
- [`version`](#version) / [`completion`](#completion)

**Exit codes:** `0` success · `1` the command failed (the message names the side, phase and table, e.g. `error: target · write · orders: …`, and a partial run lists what was written) · `70` internal error (a panic, reported with an id; the stack is in `--log-level debug`) · `130` interrupted with Ctrl+C (running queries are cancelled on the server first).

**Unreachable databases** fail within 10 seconds naming the database (`app@db:5432 did not answer`), instead of waiting for the operating system's TCP timeout.

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
| `--relationships` | — | Also measure every foreign key's shape (read-only, exact) and write it with estimated table counts to this [snapshot](#snapshot) file |
| `--scan-unindexed` | false | With `--relationships`: also scan keys that lead no index (full table scans); otherwise those are estimated |
| `--read-timeout` | `60s` | With `--relationships`: server-side time limit per foreign key; a slower one is reported as `timed out` and the rest continue |

Introspection logs a line per table on large schemas, so a slow catalog shows progress.

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
| `--batch-size` | `1000` | Most rows per INSERT statement; smaller batches are sent to stay under 65,535 parameters and ~1MB (Postgres uses COPY) |
| `--seed` | `0` | Random seed: the same seed writes byte-identical data (table order, values, dates end at 2026-01-01); 0 = random |
| `--workers` | `4` | Connections writing at once. A table writes only after every table it references; self-referencing tables write in order (`1` = one at a time) |
| `--gen-workers` | `1` | Tables generated at once on separate cores. Only helps when the database takes rows faster than one core generates them (hundreds of thousands per second, see [benchmarks](benchmarks.md)); ignored with `--seed` so runs stay reproducible |
| `--interactive` / `-i` | false | Launch interactive TUI |
| `--profile` / `-p` / `$SEEDSTORM_PROFILE` | — | [Seed profile](profiles.md): rules file or saved profile name; its `ignore:` tables are never written and its `relationships:` shape foreign keys |
| `--shape-rows` | false | Derive the row count of each shaped child table from its parents (parents × share with children × avg); `--table-rows` still wins |
| `--production` / `$SEEDSTORM_PRODUCTION` | false | The database is production: writes are refused unless `--allow-production` is also given (dry runs still work) |
| `--allow-production` | false | Confirm a write to a database marked `--production` |

`--workers auto` picks writers from the database's free connections (see [`tune`](#tune)). Whatever you ask for, a run never opens more connections than the server has free: it lowers the writers and says so (`Using 3 writers instead of 8: the server has 95 of 100 connections in use`).

With a profile that has `relationships:`, foreign keys follow those shapes instead of picking parents evenly: each parent gets a number of children drawn from the histogram, no parent exceeds `max`, the share of parents without children and of NULL keys is kept, and the run logs the achieved shape next to the target afterwards:

```
info   Rows derived from relationship shapes rows=14000 table=orders
info   Relationship shape (target → table now) avg="4.00 → 4.00" max="25 → 25" relationship=orders.account_id without_children="30% → 30%"
```

Shapes that cannot fit the planned rows are adjusted with a warning instead of looping (`14000 rows over 500 parents do not fit max 25: max raised to 28`). Self-references and junction keys are not shaped (reported). Parent tables above 500,000 rows are shaped over the sampled parents, so the shape is approximate there.

Any `--rows` is safe: rows are generated and written 20,000 at a time, Postgres takes each chunk through `COPY`, and memory stays flat (600k rows on Postgres: 7s, under 100MB). A dry run prints the SQL the same way.

Memory is bounded by bytes, not rows: chunks are sized from the measured width of finished rows (about 32MB each) and each table's key pool is freed once no remaining table references it, so wide rows and many tables stay flat too.

Generation is rarely the bottleneck; writes are. `--workers` writes unrelated tables (and pieces of one large table) on several connections while generation continues, and a log line reports progress every 2 seconds:

```
info   Progress 120.0k/1.5M rows (7.8%) · 42.1k rows/s · ETA 33s table=booking table_rows=120.0k/1.5M
info   Table written rows=1543690 table=booking
```

Measured on the 87-table Keycloak schema (local Docker): 174k rows into MySQL 41s → 14.5s with 4 workers; 1.74M rows into Postgres 41s → 19s. Raise it for a database with headroom, lower it for a small or remote one.

Seeding a table that already has rows (no `--truncate`) appends: integer ids continue after the largest existing id, UNIQUE sequences continue past their current maximum, and composite keys and multi-column UNIQUE tuples skip combinations already stored. Rows that cannot be made distinct are dropped with a warning (`no more distinct values for UNIQUE (…)`) rather than failing the insert.

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
| `--batch-size` | `1000` | Most rows per INSERT statement; smaller batches are sent to stay under 65,535 parameters and ~1MB (Postgres uses COPY) |
| `--workers` | `4` | Connections writing at once. A table writes only after every table it references; self-referencing tables write in order (`1` = one at a time) |
| `--gen-workers` | `1` | Tables generated at once on separate cores. Only helps when the database takes rows faster than one core generates them (hundreds of thousands per second, see [benchmarks](benchmarks.md)); ignored with `--seed` so runs stay reproducible |
| `--interactive` / `-i` | false | Launch interactive TUI |
| `--profile` / `-p` / `$SEEDSTORM_PROFILE` | — | [Seed profile](profiles.md): rules file or saved profile name; its `ignore:` tables are never filled |
| `--production` / `--allow-production` | false | As for [`seed`](#seed) |

---

## `generate`

Generates fake data without connecting to a database. Outputs YAML, JSON, SQL or CSV.

Rows stream into the output as they are generated, so any volume writes in flat memory (300k rows: ~100MB peak in every format). With `--out`, the file only replaces an existing one once it is complete. SQL output is runnable as-is: values are literals escaped for the chosen `--db`.

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
| `--format` / `-f` | `yaml` | Output format: `yaml`, `json`, `sql`, `csv` |
| `--out` / `-o` | stdout | Output file (omit for stdout) |
| `--db` | `postgres` | SQL dialect: identifier quoting and literal escaping |
| `--seed` | `0` | Random seed for reproducible generation (0 = random) |
| `--interactive` / `-i` | false | Launch interactive TUI |
| `--profile` / `-p` / `$SEEDSTORM_PROFILE` | — | [Seed profile](profiles.md): rules file or saved profile name |

---

## `export`

Converts a previously generated data file (YAML or JSON) to another format. The input is read and the output written a chunk at a time, so files of any size convert in flat memory; block-style YAML (what `generate` writes) and JSON stream, other YAML layouts are parsed whole. SQL statements carry literal values, `--batch-size` rows each.

```bash
seedstorm export --data data.yaml --format sql --db mysql --out seed.sql
seedstorm export --data data.json --format csv --out data.csv
```

| Flag | Default | Description |
|------|---------|-------------|
| `--data` / `-d` | — | Input data file (YAML or JSON) |
| `--format` / `-f` | `sql` | Output format: `sql`, `csv`, `json`, `yaml` |
| `--out` / `-o` | stdout | Output file (omit for stdout) |
| `--db` | `postgres` | SQL dialect: identifier quoting and literal escaping |
| `--batch-size` | `100` | Rows per INSERT statement; smaller batches are written to stay under 65,535 parameters and ~1MB |

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

# Also views, functions/procedures and triggers
seedstorm clone-schema --source-dsn "$SRC" --target-dsn "$TGT" --objects all --drop-existing
```

By default only tables, foreign keys, indexes and comments are cloned. `--views`, `--routines` and `--triggers` (or `--objects all`) add views, functions/procedures and triggers, created after the tables in the order routines → views → triggers; views built on other views are retried until they succeed. On PostgreSQL (schema `public`) materialized views are created `WITH NO DATA` and function bodies are not validated during the clone. On MySQL, `DEFINER` clauses and the source schema name in view definitions are removed, so objects belong to the user running the clone. Objects whose definition the source user cannot read (a MySQL routine owned by someone else without `SHOW_ROUTINE`) are skipped and listed as warnings. With binary logging on, MySQL needs `log_bin_trust_function_creators=1` or a SUPER user to create functions and triggers.

| Flag | Default | Description |
|------|---------|-------------|
| `--source-db` / `$SEEDSTORM_SOURCE_DB` | `postgres` | Source database type |
| `--source-dsn` / `$SEEDSTORM_SOURCE_DSN` | — | Source connection string (required) |
| `--target-db` / `$SEEDSTORM_TARGET_DB` | `postgres` | Target database type |
| `--target-dsn` / `$SEEDSTORM_TARGET_DSN` | — | Target connection string (required) |
| `--drop-existing` | false | Drop target tables (and the requested object kinds) before creating the cloned schema |
| `--views` | false | Also clone views (PostgreSQL materialized views too) |
| `--routines` | false | Also clone functions and procedures |
| `--triggers` | false | Also clone triggers |
| `--objects` | — | Comma-separated kinds to clone: `views`, `routines`, `triggers`, or `all` |
| `--dry-run` / `-n` | false | Print generated DDL, do not execute |
| `--interactive` / `-i` | false | Confirm the clone in the terminal UI |
| `--production` / `--allow-production` | false | As for [`seed`](#seed): marks the target as production |

Boundaries: `clone-schema` is same-engine only. It does not attempt cross-engine translation, and it does not clone partial/expression indexes, grants, ownership, events, or non-public/non-current schemas.

---

## `compare`

Reads every table on two databases and reports row counts, on-disk size (data + indexes), the difference, and column-name drift. Both sides are only read. Engines can differ: tables match by name, ignoring case (MySQL `USER_ENTITY` ↔ Postgres `user_entity`).

```bash
seedstorm compare \
  --source-db postgres --source-dsn "postgres://user:pass@prod.example/app" \
  --target-db postgres --target-dsn "postgres://seedstorm:seedstorm@localhost:5432/stage"

# Only tables that differ, planner estimates instead of COUNT(*) on huge tables
seedstorm compare --source-dsn "$PROD" --target-dsn "$STAGE" --only-diff --counts estimate

# Machine-readable report
seedstorm compare --source-dsn "$PROD" --target-dsn "$STAGE" --format json

# Source from a counts file instead of a connection (see snapshot)
seedstorm compare --source-snapshot prod-counts.yaml --target-dsn "$STAGE"

# Also compare children per parent for every foreign key
seedstorm compare --source-dsn "$PROD" --target-dsn "$STAGE" --relationships
```

Sample output:

```
Compare  source: app@prod.example:5432 (postgres)  →  target: stage@localhost:5432 (postgres)  · exact counts

TABLE                 SOURCE ROWS  TARGET ROWS  DELTA  SOURCE SIZE  TARGET SIZE  STATUS
addresses             40           0            -40    32.0 KB      16.0 KB      differs
brands                40           20           -20    24.0 KB      24.0 KB      differs
employees             160          80           -80    72.0 KB      64.0 KB      differs

36 tables · same 0 · differs 36 · source only 0 · target only 0 · column drift 0
rows 3856 → 1888 · size 1.7 MB → 1.3 MB
```

With `--relationships`:

```
Relationships (children per parent: avg / p95 / max · parents without children)
RELATIONSHIP               SOURCE                TARGET               STATUS
orders.user_id → users     5.15 / 15 / 34 · 19%  2.00 / 2 / 2 · 0%    differs
reviews.order_id → orders  ~1.00 / ? / ? · 70%   0.00 / 0 / 0 · 100%  differs
2 relationships · same 0 · differs 2 · source only 0 · target only 0 · unknown 0
```

`~` marks estimates (planner statistics). A relationship that timed out, was locked or failed shows its outcome instead of numbers and counts as `unknown`.

| Flag | Default | Description |
|------|---------|-------------|
| `--source-db` / `$SEEDSTORM_SOURCE_DB` | `postgres` | Source database type |
| `--source-dsn` / `$SEEDSTORM_SOURCE_DSN` | — | Source connection string; required unless `--source-snapshot` is given |
| `--source-snapshot` | — | Read source row counts from a [`snapshot`](#snapshot) file (JSON/YAML) instead of connecting |
| `--target-db` / `$SEEDSTORM_TARGET_DB` | `postgres` | Target database type |
| `--target-dsn` / `$SEEDSTORM_TARGET_DSN` | — | Target connection string (required) |
| `--counts` | `exact` | `exact` (COUNT(*)) or `estimate` (Postgres `reltuples`, MySQL `TABLE_ROWS`); tables with no or zero statistics are counted exactly and estimated counts are marked `~` |
| `--format` / `-f` | `table` | `table` or `json` |
| `--only-diff` | false | Hide tables (and relationships) that match |
| `--relationships` | false | Also compare every foreign key's shape on both sides (a `--source-snapshot` must include relationships) |
| `--scan-unindexed` | false | With `--relationships`: also scan keys that lead no index |
| `--read-timeout` | `60s` | With `--relationships`: server-side time limit per foreign key |

All reads are safe on a busy database: each runs in a read-only transaction, waits at most 2 seconds for a lock (a table locked by DDL is reported `locked`, not waited on), and is cancelled on the server when you press Ctrl+C. A count that could not be read is `unknown` (`?`), never 0. Both sides are read at the same time; a side that fails is named (`target · count: …`).

Statuses: `same`, `differs`, `source_only`, `target_only`. MySQL sizes come from cached statistics and are approximate.

---

## `mirror`

Seeds a **target** so each table reaches the source's row count times `--scale`. Typical use: make a staging or load-test database look like production in volume, with fake data. The source is only read; rows are generated from the target's own schema, in FK order, optionally shaped by a [seed profile](profiles.md).

```bash
# Preview: plan + sample rows, nothing written
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" --dry-run

# Top up the target to match production
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE"

# Stress test at 5x production, capped per table, with tagged values
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" \
  --scale 5 --max-rows 2000000 --profile loadtest

# Rebuild a few tables at 10% (truncates them and their FK dependents first)
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" \
  --mode reset --scale 0.1 --tables users,orders --yes

# Cross-engine: MySQL production volumes onto a Postgres target
seedstorm mirror --source-db mysql --source-dsn "$MYSQL_PROD" \
  --target-db postgres --target-dsn "$PG_STAGE"

# Review, preview samples and confirm in the terminal UI
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" --interactive

# Give the target production's children per parent, not an even spread
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" --shape-like-source
seedstorm mirror --source-snapshot prod-with-relationships.yaml --target-dsn "$STAGE" --shape-like-source
```

Sample dry run:

```
Mirror plan · mode topup · scale 2x · 580 rows into 3 tables

#  TABLE        SOURCE  TARGET  WANT  INSERT  WHY
1  users        120     60      240   180     match source
2  orders       300     150     500   350     capped by max rows
3  order_items  900     450     500   50      capped by max rows

Sample rows (up to 2 per table, run 1bddc1, nothing written):
users:
- email: lt+1.1bddc1@example.test
  first_name: LT Gunner
  role: guest
  ...
```

How the plan is built:

- **topup** (default) inserts `max(0, ceil(source × scale) − target)`; tables that already have enough rows are listed as skipped. Running it again is safe: new rows continue after existing ids and keys.
- **reset** truncates the selected tables **and every table that references them** (any FK, nullable included) before inserting `ceil(source × scale)`. The truncate list is printed and needs `--yes` or a typed confirmation.
- A required (non-nullable FK) parent that would stay empty gets `--parent-rows` rows.
- Tables missing on the target, or whose source count is unknown, are skipped with a reason. Per-table `rows` in a profile do not apply: volumes come from the source.
- The run refuses when source and target are the same database, however the DSNs are spelled. With `--source-snapshot` that check cannot run, and seedstorm warns about it: double-check `--target-dsn`.
- Tables a profile lists under `ignore:` are skipped (`ignored by profile`) and never truncated; a table that needs an ignored, empty parent is skipped with the reason.
- A target that refuses every write is refused before anything is planned: a Postgres standby, or MySQL with `super_read_only`. MySQL `read_only` alone only warns, since a user with `SUPER` can still write. When source and target are databases on the same server, the plan says so: reading one and writing the other share its CPU, memory and disk.
- `--shape-like-source` shapes the target's foreign keys like the source's (see [`seed`](#seed)); the shapes come from the snapshot file's relationships, or are measured read-only on the source first. Keys this schema cannot shape (junction keys, key columns, self-references) are named with their reason and spread evenly — on Keycloak's 87 tables, 29 of 67 measured keys are shaped and the other 38 are reported.

Large volumes are safe to run: rows are generated and written 20,000 at a time, Postgres takes each chunk through `COPY`, and memory stays flat whatever the table size (a 7.2M-row mirror peaks around 150MB). Existing keys are read once per table; tables with more than 500,000 parents reference a rotating sample of them, so children still spread over the whole parent table. After the run, Postgres sequences behind SERIAL/IDENTITY columns are moved past the inserted ids, so the application's own inserts keep working (`advanced orders.id sequence 0 → 2000000`).

What happens when the database refuses rows: a refused `COPY` falls back to batched INSERTs; a rejected batch is retried row by row, and rejected rows are regenerated with fresh values. A table where 25 rows in a row are refused with none accepted, or whose key space is exhausted, is reported as `partial` or `failed` and the run moves on to the next table. The command exits non-zero if any table is incomplete. `--stop-on-error` aborts at the first rejection instead.

```
TABLE        REQUESTED  INSERTED  REJECTED  MISSING  STATUS
users        180        180       0         0        ok
even_stock   200        200       189       0        ok
pairs        10         4         0         6        partial

inserted 384 rows · missing 6
  pairs (partial): no more rows possible: finite FK primary-key combinations
```

| Flag | Default | Description |
|------|---------|-------------|
| `--source-db` / `--source-dsn` / `--target-db` / `--target-dsn` | — | As for `compare` |
| `--source-snapshot` | — | Source row counts from a [`snapshot`](#snapshot) file instead of a connection |
| `--counts` | `exact` | `exact` or `estimate` row counts |
| `--mode` | `topup` | `topup` or `reset` |
| `--scale` | `1` | Multiply source volumes (`0.1`, `2`, …) |
| `--max-rows` | `0` | Cap any single table's volume (0 = no cap) |
| `--parent-rows` | `10` | Rows for an empty required parent |
| `--tables` | all | Limit to these tables, repeatable or comma-separated |
| `--profile` / `-p` | — | [Seed profile](profiles.md) file or saved name |
| `--batch-size` | `1000` | Most rows per INSERT statement (see `seed`) |
| `--workers` | `4` | Connections writing one table's chunk at once (tables still fill one after another) |
| `--self-ref-depth` | `2` | Maximum depth for self-referential FK chains |
| `--dry-run` / `-n` | false | Print the plan and sample rows only |
| `--preview-rows` | `3` | Sample rows per table in a dry run |
| `--format` / `-f` | `table` | `table` or `json` |
| `--stop-on-error` | false | Abort at the first rejected insert |
| `--yes` / `-y` | false | Skip the `reset` confirmation |
| `--seed` | `0` | Random seed for reproducible generation |
| `--interactive` / `-i` | false | Review, preview and confirm in the TUI |
| `--shape-like-source` | false | Shape target foreign keys like the source's |
| `--scan-unindexed` / `--read-timeout` | false / `60s` | With `--shape-like-source` on a live source, as for `compare --relationships` |
| `--production` / `--allow-production` | false | As for [`seed`](#seed): marks the target as production |

`--workers auto` is accepted as for `seed`.

---

## `snapshot`

Saves every table's row count, on-disk size and column names from one database (read-only) to a versioned file. Use the file as the source of `compare` or `mirror` when you cannot (or should not) connect to that database at mirror time — export production counts once, apply them to staging later.

```bash
seedstorm snapshot --db postgres --dsn "$PROD_DSN" --out prod-counts.yaml
seedstorm snapshot --db mysql --dsn "$DSN" --counts estimate --format json > counts.json
seedstorm compare --source-snapshot prod-counts.yaml --target-dsn "$STAGE_DSN"
seedstorm mirror  --source-snapshot prod-counts.yaml --target-dsn "$STAGE_DSN" --scale 0.1

# Counts plus every foreign key's shape (writes version 2)
seedstorm snapshot --dsn "$PROD_DSN" --relationships --out prod-with-relationships.yaml
```

```yaml
kind: seedstorm.table-counts
version: 1
label: app@db.example:5432
dbType: pgx
countMode: exact
takenAt: "2026-09-16T10:00:00Z"
tables:
  orders:
    rows: 5000
    bytes: 409600
    columns: [id, user_id, total]
  users:
    rows: 1200
    bytes: 65536
```

A hand-written file with only counts works too:

```yaml
tables:
  users: 1200
  orders: 5000
```

With `--relationships` the file is `version: 2` and adds a `relationships:` list: per foreign key the parent and child counts, NULL keys, share of parents without children, min / avg / p50 / p95 / max children per parent and a histogram (exact buckets 1–16, then powers of two). Relationship scans run read-only with a 60-second limit per key, two at a time, cheapest tables first; keys that lead no index are estimated unless `--scan-unindexed` is given, and a key that times out is recorded as `timed out` while the others continue. A file without relationships stays `version: 1`, readable by older seedstorm versions.

Tables match the target case-insensitively, so a Postgres snapshot works against MySQL. `-1` means unknown, and mirror skips that table. The web UI's **Compare** page exports either side of a comparison in the same format and imports files or pasted text as a source.

| Flag | Default | Description |
|------|---------|-------------|
| `--db` / `$SEEDSTORM_DB` | `postgres` | Database type: `mysql` or `postgres` |
| `--dsn` / `$SEEDSTORM_DSN` | — | Connection string (required) |
| `--counts` | `exact` | `exact` (COUNT(*)) or `estimate` (planner statistics) |
| `--format` / `-f` | `yaml` | `yaml` or `json` |
| `--out` / `-o` | stdout | File to write |
| `--relationships` | false | Also measure every foreign key's shape (read-only; exact or estimate per `--counts`) |
| `--scan-unindexed` | false | With `--relationships`: also scan keys that lead no index |
| `--read-timeout` | `60s` | With `--relationships`: server-side time limit per foreign key |

---

## `tune`

Recommends `--workers` and `--gen-workers` for one database from what it reports (free connections, buffers, used space; read-only) and what SQL cannot tell (vCPU, memory, disk), and checks that the rows fit on the disk.

```bash
seedstorm tune --dsn "$DSN" --vcpu 1 --memory-mb 629 --storage network-ssd --storage-gb 10 --rows 2000000 --avg-row-bytes 300
```

```
Host         16 CPUs, 31188MB memory (this machine or container, running seedstorm)
Database     postgres 15.18, 6/100 connections in use
Writers      2   (--workers)
Generators   1   (--gen-workers)

Why:
  - writers 2: 1 vCPU on the database, 2 writers each
  - generators 1: one generator keeps up with this database

Disk: About 1.3GB of the 10.0GB free will be used.
```

The recommendation is an estimate from benchmarks on one machine: watch the rate during the first minute and adjust. Production, shared or high-availability databases get fewer writers. The web UI's **Recommend** dialog (workspace → Tuning) runs the same rules.

| Flag | Default | Description |
|------|---------|-------------|
| `--db` / `--dsn` | — | As for `seed` |
| `--vcpu` / `--memory-mb` | — | The database's CPU and memory |
| `--storage` | — | `local-ssd`, `network-ssd` or `hdd` |
| `--storage-gb` / `--iops` | — | Disk size and provisioned IOPS of a network disk |
| `--rows` / `--avg-row-bytes` | — / `256` | Rows the run will write, for the disk check |
| `--shared` / `--ha` / `--production` | false | Other workloads use it / synchronous replica / production: stay conservative |

---

## `profile`

Manages [seed profiles](profiles.md) saved in `~/.config/seedstorm/profiles.yaml` (override with `$SEEDSTORM_PROFILES`), the same store the web UI uses.

```bash
seedstorm profile list
seedstorm profile show loadtest > loadtest.yaml
seedstorm profile import loadtest.yaml [--name other-name]
seedstorm profile validate loadtest.yaml --db postgres --dsn "$DSN"
seedstorm profile delete loadtest
```

`validate` exits non-zero on errors; with `--dsn` it also checks column rules against the live schema.

---

## `serve`

Starts a local web UI that exposes every seedstorm feature behind an interactive graph workspace. The UI is bundled into the binary via `go:embed` — no extra files to ship.

```bash
seedstorm serve                              # listens on 127.0.0.1:8080
seedstorm serve --addr 127.0.0.1:9000        # custom port
SEEDSTORM_ADDR=127.0.0.1:9000 seedstorm serve
```

What the UI gives you:

- **Workspace** — Cytoscape DAG of every table; click to select, non-nullable parents auto-lock as a dependency closure (mirrors the TUI). The selected-table panel lets you override row counts per table for **Seed**, **Fill empty**, and workspace **Generate** runs while `Rows` remains the default. **Tuning** holds batch size, enum rows, self-reference depth, **Writers** (connections writing at once, default 4) and **Generators** (tables generated at once, default 1). Runs stream rows written / planned, rate and ETA, and each table lights up while it writes.
- **Graph layout** — tables flow left to right by dependency level, and each level is packed (wrapped into columns when it holds many tables) to fit the canvas, so a 150-table schema opens at a usable zoom instead of a thin unreadable column.
- **Graph navigation** — search highlights and counts matches and zooms to them as you type (**Zoom to matches**, on by default). When matches are spread too far apart to read at once, the view goes to the best match at a readable zoom and a match bar steps through the rest (`‹` `›`, or `Enter` / `Shift+Enter`); **Only matches** lays out just the matches and the tables they link to, and **Show full graph** restores the full layout. **Navigator** (opt-in, remembered) keeps labels readable by clamping the zoom and adds a minimap: click or drag it to move around a large schema.
- **Privileges** — the connection pill shows the connected user's access (`full access`, `limited`, `read-only`), read from `has_table_privilege` on Postgres and `SHOW GRANTS` on MySQL (TRUNCATE there needs DROP). The workspace lists what is missing, marks tables without INSERT in the graph, and flags a run mode (or the clone target) the user cannot perform before you start it. Granted privileges only: row-level security, triggers and constraints can still refuse a write.
- **Header** — the connection pill counts live connections.
- **Seed controls** — `Rows = 0` with `truncate` enabled is a truncate-only run for the selected scope, including auto-required parents. No rows are generated afterward.
- **Connection management** — connections are saved on the machine in `~/.config/seedstorm/connections.yaml` (`$XDG_CONFIG_HOME` is honoured; file `0600`, directory `0700`), so they survive a restart of `serve` and a change of browser. With one or more saved, `/connect` opens on a chooser offering **Connect**, **Edit**, **Duplicate** and **Delete** per connection, with **Add connection** for a new one. Add, edit and duplicate open in a dialog on the chooser, and **Save** is a separate action from **Connect** — you can rename or repoint a connection, even an unreachable one, without opening a session against it. Every action also works as a plain page when JavaScript is unavailable. Connections previously kept in `localStorage` are imported automatically on first load.
- **Passwords** — never stored unless you tick *Also store the password*, which writes it unencrypted to that same file and says so at the point of opt-in. A connection saved without its password prompts for one inline in the chooser rather than being unusable. Live sessions keep passwords in process memory only.
- **Test connection** — pings the database from the form without creating a session or navigating away, reporting the driver, target and round-trip time on success and the driver's verbatim error on failure. Nothing you typed is lost, password included.
- **Driver parameters** — add any number of `name` / `value` pairs, merged into the DSN whether you use the structured fields or a raw connection string. Suggestion chips and autocomplete are driver-aware (MySQL: `allowCleartextPasswords`, `tls`, `allowFallbackToPlaintext`, …; Postgres: `connect_timeout`, `application_name`, `search_path`, …). Parameters borrowed from JDBC are flagged as you type with the Go equivalent — `allowPublicKeyRetrieval` is reported as unnecessary (go-sql-driver fetches the server key itself), `useSSL` maps to `tls`, `currentSchema` to `search_path`. Unknown MySQL names reach the server as `SET name = value`, which makes session variables such as `foreign_key_checks=0` work; unknown Postgres names become runtime parameters. When the driver's error names a parameter, the result offers a one-click button to add or remove it.
- **Multi-session** — hold several DBs open at once and switch from the topbar dropdown; a saved connection that is already live offers **Switch to** instead of a second connect. The workspace can clone schema from the active connection into another matching connected database.
- **Compare & mirror** — `/compare` puts any two connections (live or saved, engines may differ) side by side: per-table mirrored gauges of source and target rows, size, delta and column drift, filterable. The mirror panel plans a top-up or reset at any scale for the ticked tables, shows the plan, truncate list, skipped tables and sample rows in a review dialog, and runs only after you confirm (reset needs an extra acknowledgement). Counts refresh when the run ends. The last comparison of each pair is kept in the browser and restored when you come back, marked with its age. **Export counts** downloads or copies either side as a [`snapshot`](#snapshot) file (JSON or YAML); **Import counts** takes a file, a drop or pasted text and uses it as the source for compare and mirror.
- **Seed profiles** — `/profiles` builds [value rules](profiles.md) with a generator palette (click or drag tokens into a template), ordered column patterns with live example values and match counts, and a table explorer that shows what every column will get plus sample rows from the active connection. Profiles save to disk, import (file, drop or paste) and export (download or copy) as YAML, and are selectable in the workspace action bar and on Compare. **Ignored tables** lists globs never written, with the tables each one matches; the workspace greys those tables out and lists them in an **Ignored** tab.
- **Runs you can leave** — every page shows a run strip with the phase, progress, elapsed time and whether the server is still answering (`running`, `quiet`, `reconnecting`, `lost`). Leave a page while a seed, compare or mirror runs and come back: the run is reattached with live progress, or its result is shown if it finished. A failure names the side, phase and table and lists what completed; if `serve` restarted, the page says the run was lost instead of spinning.
- **Remembered settings** — rows, tuning, profile and compare options are kept per connection in the browser. Truncate, disable-FK and drop options are never remembered.
- **Fast return to the workspace** — the graph draws from the cached schema first; row counts fill in afterwards (cached per connection, **↻** recounts) and a table that cannot be counted says so.
- **Production connections** — tick *Production database* on a saved connection: writes to it (seed, fill empty, mirror, clone into it) need its label typed back, and relationship scans read planner estimates, one query at a time, unless you confirm an exact scan.
- **Recommend** — the Tuning panel's dialog takes vCPU, memory, storage type and size, and recommends writers and generators with the reasons (`tune` on the command line).
- **Snapshot counts** — saves the active connection's row counts (optionally with its relationships) to YAML or JSON, or hands them to Compare as an imported source; **Calibrate from a file** opens Compare with this connection as target.
- **Analyze relationships** — measures children per parent for every foreign key in the background (exact or estimate, unindexed keys opt-in); each arrow in the graph gets an `avg · max` badge as its key finishes, and the table panel shows the details. On Compare, **Compare relationships** shows the same per key for both sides, the export can include them, and **Shape relationships like the source** makes a mirror follow them.
- **Standalone tools** — `/generate`, `/enrich`, `/export` mirror the CLI commands as forms.
- **Bounded resources** — text a job returns to the browser is capped at 20MB: a dry run's SQL stops there with a note of the rows left out, while generate and export refuse and point at the CLI `--out` flags, since a cut document would be broken. Job requests are limited to 21MB, and a connection's pooled database connections close after two idle minutes (the next query reopens one).

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
