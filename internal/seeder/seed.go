package seeder

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// SeedOptions describes a plain seed run: seed and gaps --fill in the CLI, TUI
// and web UI.
type SeedOptions struct {
	// Rows per table, unless TableRows gives one (see faker.TableRowCount).
	Rows      int
	EnumRows  int
	TableRows map[string]int
	// BatchSize is the most rows per INSERT; SplitBatches may send fewer.
	BatchSize int
	// ChunkRows bounds rows held in memory at once (0: DefaultChunkRows).
	ChunkRows int
	// DryRun generates rows and hands them to OnRows without writing.
	DryRun   bool
	Generate faker.GenerateOptions
	// OnRows sees every chunk before it is written (dry-run output, previews).
	OnRows func(table string, rows []map[string]interface{}) error
	// OnTableStart announces a table before its first row is generated.
	OnTableStart func(table string) error
	// OnTable reports each finished table.
	OnTable func(p Progress)
}

// SeedResult counts the rows seeded per table.
type SeedResult struct {
	Counts map[string]int `json:"counts"`
	Total  int            `json:"total"`
}

// Seed generates rows for tables (in FK order) chunk by chunk and inserts each
// chunk before generating the next, so memory stays flat for any row count.
// Unlike Fill it is strict: the first refused insert stops the run with an
// error, like seed and gaps always have. preload lists every table whose
// stored keys foreign keys may reference. conn may be nil for a dry run that
// should not read the database.
func Seed(ctx context.Context, conn *sql.DB, dbType string, sc *schema.Schema, preload, tables []string, opts SeedOptions) (SeedResult, error) {
	res := SeedResult{Counts: make(map[string]int, len(tables))}
	if opts.BatchSize <= 0 {
		opts.BatchSize = DefaultBatchSize
	}
	stream, err := faker.NewStream(sc, preload, tables, conn, dbType, opts.Generate.Overrides)
	if err != nil {
		return res, fmt.Errorf("data generation failed: %w", err)
	}
	for i, tableName := range tables {
		if opts.OnTableStart != nil {
			if err := opts.OnTableStart(tableName); err != nil {
				return res, err
			}
		}
		count, overridden := faker.TableRowCount(tableName, opts.Rows, opts.TableRows)
		err := stream.GenerateChunks(tableName, count, opts.EnumRows, overridden, chunkSize(opts.ChunkRows), opts.Generate, func(rows []map[string]interface{}) error {
			if err := ctx.Err(); err != nil {
				return err
			}
			if opts.OnRows != nil {
				if err := opts.OnRows(tableName, rows); err != nil {
					return err
				}
			}
			if !opts.DryRun {
				if err := insertStrict(ctx, conn, dbType, tableName, rows, opts.BatchSize); err != nil {
					return err
				}
			}
			res.Counts[tableName] += len(rows)
			res.Total += len(rows)
			return nil
		})
		if err != nil {
			return res, err
		}
		if opts.OnTable != nil {
			opts.OnTable(Progress{Table: tableName, TableIndex: i + 1, Tables: len(tables), Inserted: int64(res.Counts[tableName]), Requested: int64(res.Counts[tableName])})
		}
	}
	return res, nil
}

// insertStrict writes rows through COPY on Postgres, or batched INSERTs, and
// fails at the first refused batch.
func insertStrict(ctx context.Context, conn *sql.DB, dbType, tableName string, rows []map[string]interface{}, batchSize int) error {
	if db.CopyRows(ctx, conn, dbType, tableName, rows) == nil {
		return nil
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	// COPY is all or nothing, so nothing was written: batches report the
	// database's own error for the offending rows.
	for _, batch := range db.SplitBatches(rows, batchSize) {
		query, values := db.BuildBatchInsert(tableName, batch, dbType)
		if _, err := conn.ExecContext(ctx, query, values...); err != nil {
			return fmt.Errorf("insert into %s failed: %w", tableName, err)
		}
	}
	return nil
}
