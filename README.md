# seedstorm

Dynamic database seeder with schema self-discovery, FK-aware ordering, and AI enrichment. Works with MySQL 8+ and PostgreSQL 13+.

<img src="docs/gifs/basic-seed.gif" alt="seedstorm introspect + seed demo" width="720" />

## Install

**Go install**

```bash
go install github.com/AxeForging/seedstorm/cmd/seedstorm@latest
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

## Features

- **Schema self-discovery** — introspects tables, columns, PKs, FKs, enum values, UNIQUE and CHECK constraints, generated columns, comments, defaults, and indexes; no manual editing required
- **FK-aware seeding** — topological sort guarantees parent tables are seeded before children; handles nullable and non-nullable self-referential FKs with bounded depth, near-cycles, junction tables, and deep multi-level chains
- **Constraint-aware faker mapping** — UNIQUE → `uuid`, CHECK IN → `randomstring(a,b,c)`, CHECK range → `number(min,max)`; seed data always satisfies your constraints
- **Semantic faker** — maps column names (`email`, `first_name`, `price`, `city`…) to realistic `gofakeit` generators automatically
- **Enum coverage** — every enum value appears at least `--rows` times, independently per column
- **AI enrichment** — Gemini rewrites faker hints for domain-meaningful data; supply `--prompt` for richer context
- **Gap analysis** — `gaps` shows which tables are empty with row counts and FK context; `--fill` seeds only the empty ones
- **Schema clone for test DBs** — copy schema-only structure from one connected Postgres/MySQL database into another matching local target, preserving compatible table metadata before seeding it with safe fake data
- **Interactive TUI** — wizard for table selection, global config, self-reference depth, per-table row volumes, and review before seeding
- **Web UI** — `seedstorm serve` exposes an interactive graph workspace with click-to-select tables, self-reference depth, per-table row overrides, truncate-only runs (`Rows = 0` + `truncate`), live SSE job logs, schema clone between connected DBs, multi-DB session switcher, and connection presets in `localStorage`
- **Dry-run** — preview the seed plan and INSERT SQL without touching the database
- **Export** — generate fake data as YAML, JSON, or SQL without a live connection

## Docs

| Document | Contents |
|----------|----------|
| [Command Reference](docs/commands.md) | All flags, examples, and sample output for every command |
| [Schema YAML Format](docs/schema.md) | Schema file format, column fields, faker hints reference |
| [Development & Testing](docs/development.md) | Local setup, unit + integration tests, CI, env vars, Makefile |
| [Examples](EXAMPLES.md) | End-to-end walkthroughs with GIF demos |
