# 002 — Compare connections, mirror volumes, and value rules

> Working document. **Not committed** — stays untracked and out of the PR.

## Context

`seedstorm serve` can hold several live connections (`SessionRegistry`), but every
page acts on exactly one: the cookie session. Three workflows are missing.

1. **Compare two databases.** There is no way to put two connections side by side
   and see, per table, row counts and on-disk size. The gaps command only looks at
   one database.
2. **Seed a target to look like a source.** For stress tests the question is
   "populate DB 2 with roughly the volume of DB 1". Today that means reading counts
   by hand and typing a `table=rows` override per table.
3. **Control generated values.** Seeding is fully automatic. There is no way to say
   "every email column gets a `loadtest+` prefix" or "`users.status` is always
   `active`" without hand-editing a schema YAML faker string, and faker strings cannot
   express a prefix/template at all. Tagged values matter: they make seeded data
   recognisable and easy to clean up.

A hidden blocker for (2): re-seeding a populated table collides. Composite FK
primary keys are enumerated from combination 0, `sequence` UNIQUE columns restart at
1, and integer PKs start at `count+1` (wrong when ids are sparse). A mirror "top up"
run against a target that already holds data fails on the second run. CLAUDE.md lists
this as gotcha #4.

## Requirements

### Value rules
- **R1** A rule set is a YAML document: ordered pattern rules (`table` glob, `column`
  glob) plus explicit per-table column rules and optional per-table `rows`.
- **R2** Each rule has exactly one action: `template`, `faker`, `value`, `oneOf`, `null`.
- **R3** Template tokens: any catalog generator (`{{email}}`, `{{number(1,9)}}`),
  `{{auto}}` (the value seedstorm would have generated), `{{seq}}` (1-based row index
  in this run), `{{table}}`, `{{column}}`, `{{run}}` (short id shared by one run).
- **R4** Precedence: explicit table column rule > first matching pattern rule > default.
- **R5** PK, FK and generated columns are protected: pattern rules skip them; explicit
  rules on them are validation errors. `null` on a NOT NULL column: explicit → error,
  pattern → skipped.
- **R6** Output is coerced to the column type (numeric/bool parse); a non-parseable
  value fails with an error naming table, column and value. String output respects
  varchar length.
- **R7** A rule overriding an enum column disables enum top-up for that column.
- **R8** Validation reports errors (block) and warnings (unknown table/column, UNIQUE
  column template without `{{seq}}`/`{{auto}}`/`{{uuid}}`).
- **R9** Rules work for `seed`, `gaps`, `generate` (CLI `--rules`) and all web runs.
- **R10** Web: named rule sets persist on disk (`~/.config/seedstorm/rules.yaml`,
  0600, atomic). Builder page: generator palette with live samples, ordered pattern
  rules (drag to reorder), per-table column view showing default vs effective
  generator and rule source, live sample rows, YAML import/export.

### Compare
- **R11** Compare two connections (live or saved) across engines: per table source
  rows, target rows, delta, size bytes, status (`same`, `differs`, `source_only`,
  `target_only`), and column-name differences.
- **R12** Count mode `exact` (COUNT(*)) or `estimate` (pg `reltuples`, mysql
  `TABLE_ROWS`) for large databases.
- **R13** Compare is read-only on both sides.
- **R14** CLI `seedstorm compare` with `--format table|json`.

### Mirror
- **R15** Plan per table: `want = ceil(source × scale)`, capped by `max-rows`.
  Mode `topup` inserts `max(0, want − target)`; mode `reset` truncates then inserts
  `want`.
- **R16** Reset truncates the selected tables plus all FK descendants (CASCADE parity
  across engines) and lists them before running.
- **R17** Hard FK parents with zero rows after the plan get `parent-rows` (default 10).
- **R18** Source-only tables are skipped with a reason; target-only tables untouched.
- **R19** Only the target is written. Source and target must differ.
- **R20** Dry run returns the plan (order, per-table insert, truncate list) plus a few
  sample rows per table with rules applied; nothing is written.
- **R21** Top-up is repeatable: new rows never collide with existing target rows
  (integer PK after existing max, composite keys skip existing ones, `sequence`
  UNIQUE columns continue past the existing max).
- **R22** CLI `seedstorm mirror`; web Compare page runs it as a streamed job and
  re-compares when done.

## Design

- `internal/rules` — `RuleSet` model, Load/Save/Parse, glob matching, `Validate`,
  `Explain` (per-column effective plan), `Compile` → `faker.Overrides`.
- `internal/faker` — `GenerateOptions.Overrides` (table → column → func(row, auto)),
  applied after row generation (same safety argument as `assignUniqueSequences`:
  rule columns are never referenced). `Catalog()`, `Evaluate(expr)`, `BuildSchema`.
  Existing-row awareness when `conn != nil`.
- `internal/db` — `GetTableSizes`, `GetEstimatedRowCounts`.
- `internal/compare` — `Snapshot`, `Diff` → `Report`, `PlanMirror` → `MirrorPlan`.
- `internal/seeder` — shared insert loop (batched, progress) used by CLI and web mirror.
- `internal/web` — `RuleStore`, `/rules`, `/compare`, `/api/rules*`, `/api/generators`,
  `/api/compare`, `/api/mirror`; `rulesId` on seed/gaps/generate requests.
- `internal/cli` — `compare.go`, `mirror.go`, `--rules` flag.

## Test plan (eval-driven)

Evals are scenario tests against real Postgres + MySQL containers, written first:
- Seeding twice without truncate succeeds on the 28-table schema (fails today).
- Binary: clone-schema src→tgt, `seed --rules` on src (assert tagged values in DB),
  `compare --format json`, `mirror --dry-run` (target untouched), `mirror` topup →
  zero deltas, `mirror --scale 2` topup → doubled, `mirror --mode reset --scale 0.5`,
  source counts never change.
- Web: two sessions, compare job, mirror dry-run + run, rules CRUD + preview.
- Unit: template parse/eval, glob precedence, protection, coercion, plan math
  (scale/caps/topup/reset/descendants/parents/skips), diff statuses, existing-key
  helpers, store atomicity.
- UI: Playwright drive of rules builder and compare/mirror flows; breakpoint sweep.

## Rollout
Additive. New flags default off; no change to existing command output except the
schema YAML gaining `unique: true` on UNIQUE columns.
