# Examples

End-to-end walkthroughs for common seedstorm workflows. All examples assume local databases via `make dev-up` (MySQL 8 + PostgreSQL 15).

---

## 1. Basic Seeding (no AI)

Introspect a live database, then seed it with 50 rows per table using automatic faker mapping.

```bash
# Discover schema
seedstorm introspect \
  --db postgres \
  --dsn "postgres://seedstorm:seedstorm@localhost:5432/mydb" \
  --out schema.yaml

# Seed 50 rows per table
seedstorm seed \
  --db postgres \
  --dsn "postgres://seedstorm:seedstorm@localhost:5432/mydb" \
  --schema schema.yaml \
  --rows 50
```

<details>
<summary>Sample output</summary>

```
14:12:15 INFO   Loading schema path=schema.yaml
14:12:15 INFO   Building dependency graph
14:12:15 INFO   Seed order resolved order="tags → users → companies → categories → brands → ..."
14:12:15 INFO   Connecting to database db=postgres
14:12:15 INFO   Generating fake data rows=50
14:12:15 INFO   Seeding table table=tags rows=50
14:12:15 INFO   Seeding table table=users rows=150
14:12:15 INFO   Seeding table table=companies rows=50
...
14:12:16 INFO   Seeding complete tables=28 total_rows=1515 duration=316ms
```

</details>

What you get without AI:
- `email` columns → realistic emails (`user@example.com`)
- `first_name` / `last_name` → real names
- `price` / `amount` → numeric values in default ranges
- `city` / `country` → geographic data
- Generic columns (`name`, `description`) → random words/sentences

---

## 2. AI-Enriched Seeding

Add Gemini enrichment for domain-aware data. The AI reads your full schema and generates realistic values based on what each column *means* in context.

```bash
# Step 1: Introspect
seedstorm introspect \
  --db postgres \
  --dsn "postgres://seedstorm:seedstorm@localhost:5432/mydb" \
  --out schema.yaml

# Step 2: AI-enrich with domain context
GEMINI_API_KEY=your-key-here seedstorm ai-enrich \
  --schema schema.yaml \
  --prompt "E-commerce marketplace with products, orders, reviews, wishlists, and user accounts" \
  --model gemini-2.5-flash \
  --out schema.enriched.yaml

# Step 3: Seed with enriched schema
seedstorm seed \
  --db postgres \
  --dsn "postgres://seedstorm:seedstorm@localhost:5432/mydb" \
  --schema schema.enriched.yaml \
  --rows 100 \
  --truncate --yes
```

<details>
<summary>What AI enrichment changes</summary>

Before (automatic mapping):
```yaml
products:
  columns:
    name:
      faker: word                    # generic
    price:
      faker: price(1,1000)           # wide default range
categories:
  columns:
    name:
      faker: word                    # generic
departments:
  columns:
    name:
      faker: word                    # generic
employees:
  columns:
    salary:
      faker: price(1,1000)           # unrealistic
```

After AI enrichment with `--prompt "E-commerce marketplace"`:
```yaml
products:
  columns:
    name:
      faker: randomstring(Wireless Bluetooth Headphones,4K Smart LED TV,Ergonomic Office Chair,...)
    price:
      faker: price(5.00,1500.00)     # realistic product price range
categories:
  columns:
    name:
      faker: randomstring(Electronics,Clothing,Home & Kitchen,Books,Sports & Outdoors,...)
departments:
  columns:
    name:
      faker: randomstring(Marketing,Sales,Customer Service,Product Development,Logistics,...)
employees:
  columns:
    salary:
      faker: price(40000,180000)     # realistic annual salary
```

</details>

<details>
<summary>Sample seeded data (Postgres)</summary>

```sql
SELECT name, price, stock, rating FROM products LIMIT 5;
```
```
name                              | price   | stock | rating
----------------------------------+---------+-------+-------
External SSD 1TB                  |  281.74 |   374 |      2
Smart Home Hub                    |  827.08 |   254 |      4
Smartwatch with Heart Rate Monitor|  192.56 |   308 |      1
Electric Toothbrush Sonic         |  523.06 |   355 |      3
Gaming Keyboard Mechanical        | 1082.81 |   252 |      3
```

```sql
SELECT name, budget FROM departments LIMIT 5;
```
```
name                | budget
--------------------+-----------
Product Development |  59538.02
IT                  | 361178.57
Analytics           | 216423.88
Operations          | 410614.50
Logistics           | 294827.33
```

</details>

---

## 3. Partial Seeding with Gaps

Seed some tables first, then use `gaps` to find and fill empty tables without touching existing data.

```bash
# Seed the full schema
seedstorm seed \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --rows 20

# Later, check what's empty (maybe new tables were added)
seedstorm gaps \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml
```

<details>
<summary>Sample gap report</summary>

```
Gap Analysis
────────────────────────────────────────────────────────────
  Table                    Rows  Status
  ───────────────────────  ────  ────────────────────────────────────────
  brands                     20  populated
  users                      60  populated
  categories                 20  populated
  products                   20  populated
  new_feature_flags           0  EMPTY → would seed 50 rows
  new_ab_tests                0  EMPTY → would seed 50 rows  [FK → users (60 rows)]

  Gaps: 2 table(s) empty · Would seed: 100 rows total
```

</details>

```bash
# Fill only the empty tables (existing data is untouched)
seedstorm gaps \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --fill --rows 50 --yes
```

---

## 4. Reproducible Generation

Use `--seed` for deterministic output — same schema + same seed = identical data every time.

```bash
# Generate twice with the same seed
seedstorm generate --schema schema.yaml --rows 5 --seed 42 --format json --out run1.json
seedstorm generate --schema schema.yaml --rows 5 --seed 42 --format json --out run2.json

# Files are identical
diff run1.json run2.json  # no output = identical
```

Useful for:
- **Testing** — predictable fixture data for CI
- **Debugging** — reproduce exact data that caused an issue
- **Demos** — consistent sample data across environments

---

## 5. Dry-Run Inspection

Preview the seed plan (table ordering + FK dependencies) and generated SQL before executing anything.

```bash
seedstorm seed \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --rows 5 \
  --dry-run
```

<details>
<summary>Sample dry-run output</summary>

```
Seed Plan (5 rows per table)
═══════════════════════════════════════════════════════════════
  #   Table                    Dependencies
  ──  ───────────────────────  ──────────────────────────────
  1   tags                     (none)
  2   users                    (none)
  3   companies                (none)
  4   categories               categories? (self-ref, nullable)
  5   brands                   companies
  6   wishlists                users
  7   addresses                users
  8   orders                   users, coupons
  ...

--- SQL ---
INSERT INTO "tags" ("id", "name") VALUES ($1, $2);
INSERT INTO "tags" ("id", "name") VALUES ($1, $2);
...
```

</details>

---

## 6. MySQL Workflow

seedstorm works identically with MySQL — just change `--db` and the DSN format.

```bash
# Introspect MySQL
seedstorm introspect \
  --db mysql \
  --dsn "seedstorm:seedstorm@tcp(localhost:3306)/mydb" \
  --out schema.yaml

# AI-enrich (same command, DB-agnostic)
GEMINI_API_KEY=xxx seedstorm ai-enrich \
  --schema schema.yaml \
  --prompt "SaaS project management tool" \
  --out schema.enriched.yaml

# Seed MySQL
seedstorm seed \
  --db mysql \
  --dsn "seedstorm:seedstorm@tcp(localhost:3306)/mydb" \
  --schema schema.enriched.yaml \
  --rows 100 --truncate --yes
```

---

## 7. Export Formats

Generate data without a database connection and export to different formats.

```bash
# JSON (good for API mocking)
seedstorm generate --schema schema.yaml --rows 10 --format json --out data.json

# SQL (good for migrations or CI fixtures)
seedstorm generate --schema schema.yaml --rows 10 --format sql --db postgres --out seed.sql

# YAML (default, human-readable inspection)
seedstorm generate --schema schema.yaml --rows 10 --format yaml --out data.yaml

# Convert between formats
seedstorm export --data data.yaml --format csv --out data.csv
seedstorm export --data data.yaml --format sql --db mysql --out seed.sql
```
