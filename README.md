<p align="center">
  <img src="docs/assets/seedstorm-logo-wordmark.png" alt="seedstorm" width="780" />
</p>

# seedstorm

Dynamic database seeder with schema self-discovery, FK-aware ordering, and AI enrichment. Works with MySQL 8+ and PostgreSQL 13+.

<img src="docs/gifs/basic-seed.gif" alt="seedstorm introspect + seed demo" width="720" />

## Install

**Checksum-verified release**

```bash
curl -fsSL https://raw.githubusercontent.com/AxeForging/seedstorm/main/install.sh | sh
```

**Linux / macOS — download binary**

```bash
# macOS ARM64 (Apple Silicon)
curl -L https://github.com/AxeForging/seedstorm/releases/latest/download/seedstorm-darwin-arm64.tar.gz | tar xz
sudo mv seedstorm /usr/local/bin/

# macOS AMD64
curl -L https://github.com/AxeForging/seedstorm/releases/latest/download/seedstorm-darwin-amd64.tar.gz | tar xz
sudo mv seedstorm /usr/local/bin/

# Linux AMD64
curl -L https://github.com/AxeForging/seedstorm/releases/latest/download/seedstorm-linux-amd64.tar.gz | tar xz
sudo mv seedstorm /usr/local/bin/

# Linux ARM64
curl -L https://github.com/AxeForging/seedstorm/releases/latest/download/seedstorm-linux-arm64.tar.gz | tar xz
sudo mv seedstorm /usr/local/bin/
```

All releases and checksums at [github.com/AxeForging/seedstorm/releases](https://github.com/AxeForging/seedstorm/releases).

PR and local quality gates are orchestrated by Gauntlet. Structlint enforces the
whole repository structure; Dupehound annotations are scoped to changed lines:

```sh
gauntlet check
gauntlet check --staged --format agent
```

## Quick Start

```bash
# 1. Discover schema from your database
seedstorm introspect \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --out schema.yaml

# 2. (Optional) AI-enrich faker mappings for domain-meaningful data
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
  --prompt "Mexican taco shop" \
  --out schema.enriched.yaml

# 3. Seed 100 rows per table
seedstorm seed \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.enriched.yaml \
  --rows 100

# 4. Fill any empty tables added later
seedstorm gaps \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.yaml \
  --fill --rows 100
```

## Web UI

Prefer a visual workflow? `serve` launches an interactive graph studio — introspect
the live schema, click to select tables, set per-table row volumes, seed in
dependency order with live job logs, then browse the populated data:

```bash
seedstorm serve            # http://127.0.0.1:8080
```

Connections are saved on the machine (`~/.config/seedstorm/connections.yaml`, mode
`0600`), so `serve` opens on a list of your databases instead of an empty form.
Each one can be tested before you commit to it, and any driver parameter can be
added as a `name` / `value` pair — `allowCleartextPasswords=1` for MySQL cleartext
auth, `tls=skip-verify`, `sslmode=verify-full`, `foreign_key_checks=0` — from both
the structured fields and a raw connection string. Passwords are stored only if you
tick the box. See [docs/commands.md](docs/commands.md#serve) for the full flow.

<img src="docs/gifs/web-ui.gif" alt="seedstorm web UI — introspect the schema graph, seed, and browse live data" width="820" />

## Compare & mirror

Put two databases side by side, then make the second one look like the first in
volume, with fake data. Useful for stress tests: "give staging production's row
counts", or "5x production, capped at 2M rows per table".

```bash
# Row counts, sizes and column drift per table (read-only on both sides)
seedstorm compare --source-dsn "$PROD" --target-dsn "$STAGE" --only-diff

# Plan and sample rows, nothing written
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" --scale 5 --max-rows 2000000 --dry-run

# Run it: top up the target (repeatable), or --mode reset to truncate and refill
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" --scale 5 --max-rows 2000000
```

Engines can differ (MySQL volumes onto Postgres works). Only the target is written;
the command refuses when source and target are the same database. Rows the database
rejects are regenerated, and a table that cannot be filled is reported without
stopping the rest. In the web UI, **Compare** shows every table as a source/target
gauge and runs the same plan behind a review dialog.

<img src="docs/assets/compare-mirror.webp" alt="Compare page: per-table source and target gauges with the mirror panel" width="820" />

## Seed profiles

Shape generated values without touching the schema: tag every email so seeded
rows are easy to find and delete, pin a status, empty a column. Everything a profile
does not mention keeps seedstorm's automatic generator.

```yaml
# loadtest.yaml
name: loadtest
rules:
  - column: "*email*"
    template: "lt+{{seq}}.{{run}}@example.test"   # {{auto}}, {{seq}}, {{run}}, any generator
  - column: "*_name"
    template: "LT {{auto}}"
tables:
  users:
    columns:
      role: { value: guest }
      phone: { setNull: true }
```

```bash
seedstorm seed --dsn "$DSN" --schema schema.yaml --profile loadtest.yaml
seedstorm mirror --source-dsn "$PROD" --target-dsn "$STAGE" --profile loadtest   # saved in the web UI
```

Rules never touch keys or generated columns and skip columns whose type cannot take
their value. The web UI's **Profiles** page builds them with a generator palette,
live example values and sample rows. See [docs/profiles.md](docs/profiles.md).

## Features

- **Schema self-discovery** — introspects tables, columns, PKs, FKs, enum values, UNIQUE and CHECK constraints, generated columns, comments, defaults, and indexes; no manual editing required
- **FK-aware seeding** — topological sort guarantees parent tables are seeded before children; handles nullable and non-nullable self-referential FKs with bounded depth, near-cycles, junction tables, and deep multi-level chains
- **Constraint-aware faker mapping** — UNIQUE → `uuid` (string columns) or a collision-free `sequence` (numeric/temporal columns), CHECK IN → `randomstring(a,b,c)`, CHECK range → `number(min,max)`; seed data always satisfies your column types and constraints
- **Semantic faker** — maps column names (`email`, `first_name`, `price`, `city`…) to realistic `gofakeit` generators automatically
- **Enum coverage** — every enum value appears at least `--rows` times, independently per column
- **AI enrichment** — Gemini rewrites faker hints for domain-meaningful data; supply `--prompt` for richer context
- **Gap analysis** — `gaps` shows which tables are empty with row counts and FK context; `--fill` seeds only the empty ones
- **Compare two databases** — `compare` reports per-table rows, size, delta and column drift between any two connections, across engines
- **Mirror volumes** — `mirror` seeds a target to a source's row counts at any scale (top-up or reset), with a reviewable plan, sample rows, FK-aware parents, and resilient inserts that report what could not be filled
- **Seed profiles** — value rules (templates with `{{auto}}`/`{{seq}}`/`{{run}}`, fixed values, lists, NULL) applied by column pattern or per column; saved from the web UI and reused with `--profile` in the CLI and TUI
- **Re-seed safely** — seeding a populated table appends: ids, UNIQUE sequences and composite keys continue past existing rows
- **Schema clone for test DBs** — copy schema-only structure from one connected Postgres/MySQL database into another matching local target, preserving compatible table metadata before seeding it with safe fake data
- **Interactive TUI** — wizard for table selection, global config, self-reference depth, per-table row volumes, and review before seeding
- **Web UI** — `seedstorm serve` exposes an interactive graph workspace with click-to-select tables, self-reference depth, per-table row overrides, truncate-only runs (`Rows = 0` + `truncate`), live SSE job logs with per-table truncate/insert progress, schema clone between connected DBs, and a multi-DB session switcher
- **Saved connections** — connections persist on the machine and survive a restart, with test-before-connect, opt-in password storage, and driver-aware connection parameters (with JDBC-to-Go translation) for both Postgres and MySQL
- **Dry-run** — preview the seed plan and INSERT SQL without touching the database
- **Export** — generate fake data as YAML, JSON, SQL or CSV without a live connection, streamed in flat memory; SQL files load as-is

## Docs

| Document | Contents |
|----------|----------|
| [Command Reference](docs/commands.md) | All flags, examples, and sample output for every command |
| [Schema YAML Format](docs/schema.md) | Schema file format, column fields, faker hints reference |
| [Seed Profiles](docs/profiles.md) | Value rules: actions, template tokens, resolution, validation |
| [Development & Testing](docs/development.md) | Local setup, unit + integration tests, CI, env vars, Makefile |
| [Examples](EXAMPLES.md) | End-to-end walkthroughs with GIF demos |
