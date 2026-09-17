package cli

import (
	"context"
	"database/sql"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/fsutil"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// syncSequences moves Postgres sequences past the ids a run inserted so the
// application's next default id does not collide. It runs even after a failed
// insert, for whatever made it in, and only warns if it cannot.
func syncSequences(ctx context.Context, conn *sql.DB, dbType string, tables []string) {
	adjusted, err := db.SyncSequences(ctx, conn, dbType, tables)
	for _, a := range adjusted {
		logging.Log.Info().Str("table", a.Table).Str("column", a.Column).Int64("from", a.From).Int64("to", a.To).Msg("Advanced sequence past seeded ids")
	}
	if err != nil {
		logging.Log.Warn().Err(err).Msg("Could not advance sequences; application inserts may reuse seeded ids")
	}
}

// printDryRunSQL prints each generated row as a runnable INSERT in a dry run.
func printDryRunSQL(dryRun bool, dbType string) func(string, []map[string]interface{}) error {
	if !dryRun {
		return nil
	}
	return func(table string, rows []map[string]interface{}) error {
		for _, row := range rows {
			fmt.Println(db.RenderInsert(table, []map[string]interface{}{row}, dbType))
		}
		return nil
	}
}

// openOutput returns where a command writes its data: stdout, or a file that
// only replaces outPath when commit(true) is called. commit(false) discards an
// unfinished file and is safe to defer.
func openOutput(outPath string) (io.Writer, func(ok bool) error, error) {
	if outPath == "" {
		return os.Stdout, func(bool) error { return nil }, nil
	}
	f, err := fsutil.CreateAtomic(outPath, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to write output: %w", err)
	}
	return f, func(ok bool) error {
		if !ok {
			f.Abort()
			return nil
		}
		return f.Commit()
	}, nil
}

// logWarning reports rows the generator could not produce (finite key space,
// multi-column UNIQUE groups) instead of letting counts silently shrink.
func logWarning(w faker.GenerationWarning) {
	logging.Log.Warn().
		Str("table", w.Table).
		Int("requested", w.Requested).
		Int("generated", w.Generated).
		Msg(w.Reason)
}

// normalizeDBType converts user-facing db names to driver names.
func normalizeDBType(dbType string) string {
	if dbType == "postgres" || dbType == "postgresql" {
		return "pgx"
	}
	return dbType
}

// buildInsert delegates to db.BuildInsert.
func buildInsert(tableName string, row map[string]interface{}, dbType string) (string, []interface{}) {
	return db.BuildInsert(tableName, row, dbType)
}

// buildBatchInsert delegates to db.BuildBatchInsert.
func buildBatchInsert(tableName string, rows []map[string]interface{}, dbType string) (string, []interface{}) {
	return db.BuildBatchInsert(tableName, rows, dbType)
}

// logPartialRun says what a failed run wrote before it stopped, so the user
// knows which tables hold new rows and which were never reached.
func logPartialRun(res seeder.SeedResult, order []string) {
	var written, notWritten []string
	for _, t := range order {
		if n := res.Counts[t]; n > 0 {
			written = append(written, fmt.Sprintf("%s (%d)", t, n))
		} else {
			notWritten = append(notWritten, t)
		}
	}
	ev := logging.Log.Warn().Int("rows_written", res.Total)
	if len(written) > 0 {
		ev = ev.Str("written", strings.Join(written, ", "))
	}
	if len(notWritten) > 0 {
		ev = ev.Str("not_written", strings.Join(notWritten, ", "))
	}
	ev.Msg("Run stopped before finishing")
}
