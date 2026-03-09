# seedstorm

Dynamic database seeder with schema self-discovery and AI enrichment. Works with MySQL 5.7+ and PostgreSQL 10+.

## Features

- **Schema discovery** — introspects tables, columns, types, PKs, FKs, and enum values automatically
- **FK-aware ordering** — topological sort ensures parent tables are seeded before children
- **Semantic faker** — maps column names and types to realistic gofakeit generators
- **AI enrichment** — uses Gemini to produce domain-aware faker mappings from business context
- **Multiple outputs** — seed directly into DB, or export as YAML, JSON, or SQL

## Workflow

```
1. introspect  →  schema.yaml          (discover schema)
2. ai-enrich   →  schema.enriched.yaml (AI-powered faker mapping)
3. seed        →  database             (insert fake data)
```

## Commands

### introspect

```bash
seedstorm introspect --db mysql    --dsn "root:pass@tcp(localhost:3306)/mydb" --out schema.yaml
seedstorm introspect --db postgres --dsn "postgres://user:pass@localhost/mydb" --out schema.yaml
```

### ai-enrich

```bash
GEMINI_API_KEY=xxx seedstorm ai-enrich --schema schema.yaml --out schema.enriched.yaml
```

### seed

```bash
seedstorm seed --db mysql --dsn "root:pass@tcp(localhost:3306)/mydb" --schema schema.enriched.yaml --rows 100
seedstorm seed --db postgres --dsn "postgres://user:pass@localhost/mydb" --schema schema.yaml --dry-run
```

Flags:
- `--rows N` — rows per table (default 100)
- `--enum-rows N` — rows per enum value for tables with enum columns
- `--disable-fk` — skip FK ordering (useful for databases with circular references)
- `--dry-run` — print SQL without executing

### generate

```bash
seedstorm generate --schema schema.yaml --rows 10 --format json --out data.json
seedstorm generate --schema schema.yaml --rows 5  --format sql  --db postgres
```

### export

```bash
seedstorm export --data data.yaml --format sql --out seed.sql
seedstorm export --data data.yaml --format csv --out data.csv
```

## Schema YAML Format

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
      name:
        type: varchar
        faker: name
  orders:
    columns:
      id:
        type: integer
        pk: true
      user_id:
        type: integer
        fk: users.id
      total:
        type: decimal
        faker: price(10,500)
```

## Local Development

```bash
# Start MySQL + PostgreSQL
make dev-up

# MySQL connection string
DSN="seedstorm:seedstorm@tcp(localhost:3306)/testdb"

# PostgreSQL connection string
DSN="postgres://seedstorm:seedstorm@localhost:5432/testdb"
```

## Installation

```bash
go install github.com/AxeForging/seedstorm/cmd/seedstorm@latest
```

## Build

```bash
make build       # current platform
make build-all   # linux/darwin amd64+arm64
make test
make lint
```

## Environment Variables

| Variable | Description |
|----------|-------------|
| `GEMINI_API_KEY` | Gemini API key for ai-enrich |
| `SEEDSTORM_DSN` | Default data source name |
| `SEEDSTORM_DB` | Default database type |
| `SEEDSTORM_LOG_LEVEL` | Log level: debug, info, warn, error |
