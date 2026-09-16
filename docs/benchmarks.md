# Benchmarks

Measurements behind seedstorm's defaults. All numbers are from one developer
machine (16 cores, local Docker: Postgres 15, MySQL 8.0), so read them as ratios,
not guarantees: a remote or busier database changes the absolute numbers, and
usually makes concurrent writers matter more (round trips dominate).

Reproduce any row with the commands shown; scratch databases come from the
integration fixtures (`integration/fixtures/keycloak_*.sql`, `wideSchemaDDL` in
`integration/wide_schema_test.go`).

## Where the time goes

Generating rows is rarely the bottleneck; the database accepting them is.

| Measurement | Rows/s |
|-------------|-------:|
| Generation only, 1 core (3M rows, 150-table schema, CSV to disk) | ~310k → **~490k** after the single-core speedups |
| Generation only, in-memory writer, 32 independent tables — 1 generator | 446k |
| same — 2 generators | 827k |
| same — 4 generators | 1.30M |
| same — 8 generators | **1.79M** |
| Seed into local Postgres (COPY), 150 tables, 8 writers | ~110k–170k |
| Seed into local MySQL (batched INSERT), Keycloak 87 tables, 4 writers | ~12k |

```bash
go test -run XXX -bench BenchmarkSeed_GenWorkers -benchtime 3x ./internal/seeder/
```

**What that means for `--gen-workers`:** it multiplies generation, so it only
shortens a seed when the database can take rows faster than one core produces
them (hundreds of thousands per second). Against a single local database the
writers are the limit; raise `--workers` first.

## Concurrent writers (`--workers`)

| Run | 1 writer | 4 writers (default) | 8 writers |
|-----|---------:|--------------------:|----------:|
| MySQL, Keycloak 87 tables × 2,000 rows (inserts only) | 41 s | **14.5 s** | 9 s |
| Postgres, Keycloak 87 tables × 20,000 rows (1.74M) | 41 s | **19 s** | — |

```bash
seedstorm seed --db mysql --dsn "$DSN" --schema keycloak.yaml --rows 2000 --truncate --yes --workers 4
```

MySQL `TRUNCATE` stays on one connection (~15 s for 87 tables): running it
concurrently made later inserts fail foreign-key checks.

## Memory

Chunks and the write queue are bounded by measured row memory (a queued row
costs at least half an average row of a full chunk, so narrow rows queue at most
about two chunks), and each table's key pool is freed once no remaining table
reads it. Peak resident memory of the seedstorm process (`/usr/bin/time -f %M`):

| Run | Before | After |
|-----|-------:|------:|
| 300k narrow rows (2 tables), 1 writer | 93 MB (main) | **82–89 MB** |
| same, 4 writers | 157 MB | **108–117 MB** |
| 60k rows of ~6 KB, Postgres, 1 writer | 321 MB | **113 MB** |
| same, 8 writers | 377 MB | **123–135 MB** |
| 30k rows of ~20 KB, Postgres, 8 writers | ≈400 MB per chunk | **162 MB** |
| 22k rows of ~20 KB, MySQL, 8 writers | — | **225 MB** |
| 100 tables × 30k rows (6 columns), 8 writers | 272 MB | **68–90 MB** |
| 150 cross-referenced tables × 10k rows, 8 writers | 224 MB | **163 MB** |

Live heap stays flat as rows grow (MySQL, 8 writers: max 106 MB live at 30k
rows, 82 MB at 90k); peak RSS varies with garbage-collector timing. Each extra
generator adds about a chunk's worth of memory.

The integration tests `TestSeed_WideRowsStayUnderAMemoryBound`,
`TestSeed_ManyTablesDoNotKeepEveryKeyPool` and
`TestSeed_LargeRunsStreamWithFlatMemory` hold these bounds. They read the
process's own `VmHWM`: rusage's max RSS also counts the test process at fork,
which under `-race` made every run report 212 MB.

## Reproducibility

`--seed` writes byte-identical data on every run. The generation speedups were
checked against recorded outputs: 6 files (150-table schema, Keycloak on
Postgres and MySQL, each with and without a value-rule profile, 300 rows per
table, `--seed 7`) have the same SHA-256 before and after.
`TestGenerate_SameSeedWritesIdenticalData` keeps the guarantee.

## Workspace graph

Initial zoom of the whole graph (higher is more readable; labels read well from
about 0.72):

| Schema | Before | After |
|--------|-------:|------:|
| 150 cross-referenced tables | 0.15 | **0.40** |
| 36 tables | — | 1.06 |
| 2 tables | 4.85 (fit without a cap) | **1.50** |

Spread search matches ("booking", 9 matches over the 150-table graph) used to
fit at zoom 0.16; the view now goes to the best match at 1.1 and **Only
matches** lays out the 41 matching and neighbouring tables together.
