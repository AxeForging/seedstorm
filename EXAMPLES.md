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

Or use interactive mode to visually pick which empty tables to fill:

```bash
seedstorm gaps \
  --db postgres \
  --dsn "postgres://..." \
  --schema schema.yaml \
  --interactive
```

<details>
<summary>Gaps interactive walkthrough</summary>

The gaps TUI shows all tables with their current row counts. Empty tables are pre-selected for filling, populated tables are shown for context. Select/deselect which empties to fill, configure rows, review, then execute or dry-run.

```
  seedstorm gaps interactive  ● Tables  ○ Config  ○ Review  ○ Execute

  Select empty tables to fill

    [✓] users (50 rows)           ← populated, shown for context
    [✓] orders                    ← empty, selected to fill
    [✓] order_items               → orders
    [ ] audit_logs                → users  (deselected — skip this one)

  3 of 4 tables selected
  ↑/↓ navigate • space toggle • enter confirm • q quit
```

Auto-dependency resolution works the same as `seed -i` — selecting a child auto-selects its required parents.

</details>

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

## 6. Interactive Mode

Launch a TUI wizard to visually select tables, configure options, review the plan, and execute — all without memorizing flags.

```bash
seedstorm seed \
  --db postgres \
  --dsn "postgres://user:pass@localhost/mydb" \
  --schema schema.yaml \
  --interactive
```

The wizard has 4 steps with a breadcrumb at the top showing your progress. You can go back to any previous step with `b`.

<details>
<summary>Step 1 — Table Picker</summary>

Select which tables to seed. Tables are shown in FK-safe order with their dependencies. Selecting a child table **auto-selects its required parents** (shown with `●` — locked, can't be deselected while the child needs them).

```
  seedstorm interactive  ● Tables  ○ Config  ○ Review  ○ Execute

  Select tables to seed

  ▸ [✓] tags
    [✓] users
    [✓] companies
    [●] categories                → brands  (auto-selected: needed by products)
    [ ] audit_logs                → users
    [✓] products                  → categories, brands
    [✓] orders                    → users, coupons
    [ ] employees                 → departments
    [✓] wishlists                 → users

  7 of 28 tables selected
  ↑/↓ navigate • space toggle • a all • n none • enter confirm • q quit
```

| Key | Action |
|-----|--------|
| `↑`/`↓` or `j`/`k` | Navigate |
| `space` | Toggle selected/deselected |
| `a` | Select all / deselect all (toggles) |
| `n` | Deselect all |
| `enter` | Confirm and go to Config |
| `q` / `esc` | Quit |

</details>

<details>
<summary>Step 2 — Config</summary>

Set seeding parameters. Tab between fields, space to toggle the truncate checkbox.

```
  seedstorm interactive  ✓ Tables  ● Config  ○ Review  ○ Execute

  Configure seeding options

  ▸ Rows per table: [50]
    Batch size: [100]
    Enum rows (0 = use rows): [0]
    [ ] Truncate before seeding

  tab/↑↓ navigate • space toggle • enter confirm • b back • q quit
```

| Field | What it controls |
|-------|-----------------|
| Rows per table | How many rows to generate for each selected table |
| Batch size | Rows per INSERT statement (higher = faster, default 100) |
| Enum rows | Rows per enum value for enum tables (0 = use rows count) |
| Truncate | Delete all existing data before seeding (shows warning in review) |

</details>

<details>
<summary>Step 3 — Review</summary>

See the full seed plan before executing. Shows table order, FK dependencies, and all config values.

```
  seedstorm interactive  ✓ Tables  ✓ Config  ● Review  ○ Execute

  Review seed plan

  Tables:     7
  Rows/table: 50
  Batch size: 100

  #  Table          Dependencies
  ─────────────────────────────────────────
  1  tags           —
  2  users          —
  3  companies      —
  4  categories     brands
  5  products       categories, brands
  6  orders         users, coupons
  7  wishlists      users

  enter execute • d dry-run • b back • q quit
```

If truncate is enabled, you'll see a red warning: `Truncate: YES — all existing data will be deleted`.

</details>

<details>
<summary>Step 4a — Dry Run (press <code>d</code> on Review)</summary>

Preview what would be generated without touching the database. Shows each table with row count and a sample of the first row's data. Scrollable with arrow keys, sticky footer with totals.

```
  seedstorm interactive  ✓ Tables  ✓ Config  ✓ Review  ● Execute

  Dry Run — Preview

  Would generate 350 rows across 7 tables

  tags                          50 rows
    id=1  name=Technology  +0 more

  users                         150 rows
    created_at=2016-09-02  email=abc@example.com  first_name=Reina  +5 more

  companies                     50 rows
    id=1  industry=Finance  name=TechCorp  +3 more

  products                      50 rows
    id=1  name=Wireless Headphones  price=249.99  +4 more

  ────────────────────────────────────────────────────────────
  7 tables • 350 total rows  (scroll 21/21)
  ↑/↓ scroll • q quit
```

</details>

<details>
<summary>Step 4b — Execute (press <code>enter</code> on Review)</summary>

Seeds the database with a spinner during execution, then shows a per-table summary.

```
  seedstorm interactive  ✓ Tables  ✓ Config  ✓ Review  ● Execute

  Seeding database

  Seeding complete! 350 rows across 7 tables in 156ms

    tags                           50 rows
    users                          150 rows
    companies                      50 rows
    categories                     50 rows
    products                       50 rows
    orders                         50 rows
    wishlists                      50 rows

  q quit
```

</details>

<details>
<summary>Common workflows with interactive mode</summary>

```bash
# Seed only specific tables (select interactively)
seedstorm seed --db postgres --dsn "..." --schema schema.yaml -i

# Preview what AI-enriched data looks like before committing
seedstorm seed --db postgres --dsn "..." --schema schema.enriched.yaml -i
# → select tables → config → review → press 'd' for dry-run

# Re-seed with truncate (interactive confirmation instead of --yes)
seedstorm seed --db postgres --dsn "..." --schema schema.yaml -i
# → select tables → config → enable truncate → review (shows warning) → execute
```

</details>

All existing CLI flags still work without `--interactive` — scripts and CI are unaffected.

---

## 9. MySQL Workflow

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

## 7. Generate with Interactive Mode

Use interactive mode to visually select tables, pick output format, and preview generated data — no flags to memorize.

```bash
seedstorm generate --schema schema.yaml --interactive
```

<details>
<summary>Generate interactive walkthrough</summary>

A 3-step wizard: **Tables → Config → Generate**

**Step 1 — Table picker:** Same as `seed -i` — select which tables to include.

**Step 2 — Config:** Set rows, choose format (yaml/json/sql with `←`/`→`), and optionally set an output file path.

```
  seedstorm generate interactive  ✓ Tables  ● Config  ○ Generate

  Configure generation

  ▸ Rows per table: [10]
    Format: [yaml]  json   sql
    Output file: [data.json]

  tab/↑↓ navigate • ←→/space cycle format • enter confirm • esc back • q quit
```

**Step 3 — Generate:** Shows a scrollable preview of the generated data per table with sample values. If an output file was set, the data is written to it.

</details>

---

## 8. Export Formats

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
