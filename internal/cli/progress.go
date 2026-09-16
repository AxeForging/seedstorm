package cli

import (
	"context"
	"database/sql"
	"strings"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// progressInterval is the most often a run logs a progress line.
const progressInterval = 2 * time.Second

func workersFlag() cli.Flag {
	return &cli.IntFlag{
		Name:  "workers",
		Usage: "Connections writing at once; tables still write after the tables they reference (1 = one at a time)",
		Value: seeder.DefaultWorkers,
	}
}

// progressLogger logs rows written, rate and ETA at most every progressInterval,
// plus a line per finished table, so a long table is never silent.
func progressLogger(now func() time.Time) (onProgress, onTable func(seeder.Progress)) {
	meter := seeder.NewMeter(now())
	var last time.Time
	var est seeder.Estimate
	onProgress = func(p seeder.Progress) {
		t := now()
		est = meter.Observe(t, p.RowsDone, p.RowsTotal)
		if t.Sub(last) < progressInterval {
			return
		}
		last = t
		logging.Log.Info().
			Str("table", p.Table).
			Str("table_rows", seeder.CompactCount(p.Inserted)+"/"+seeder.CompactCount(p.Requested)).
			Msg("Progress " + est.String())
	}
	onTable = func(p seeder.Progress) {
		logging.Log.Info().Str("table", p.Table).Int64("rows", p.Inserted).Msg("Table written")
	}
	return onProgress, onTable
}

// applyIgnore drops the profile's ignored tables from a run order. A kept table
// that needs an ignored, empty parent stops the run with an error naming both.
func (lp loadedProfile) applyIgnore(ctx context.Context, conn *sql.DB, dbType string, order []string) ([]string, error) {
	if lp.rules == nil {
		return order, nil
	}
	ignored := lp.rules.IgnoredSet(lp.schema)
	if len(ignored) == 0 {
		return order, nil
	}
	kept, err := graph.ApplyIgnore(lp.schema, order, ignored, populatedCheck(ctx, conn, dbType))
	if err != nil {
		return nil, err
	}
	var names []string
	for _, it := range lp.rules.IgnoredTables(lp.schema) {
		names = append(names, it.Table)
	}
	logging.Log.Info().Str("tables", strings.Join(names, ", ")).Msg("Ignoring tables listed by the profile")
	return kept, nil
}

// populatedCheck reports whether a table holds rows. Without a connection
// (generate) nothing is stored, so every table is empty.
func populatedCheck(ctx context.Context, conn *sql.DB, dbType string) func(string) (bool, error) {
	return func(table string) (bool, error) {
		if conn == nil {
			return false, nil
		}
		counts, err := db.GetTableRowCounts(ctx, conn, dbType, []string{table})
		if err != nil {
			return false, err
		}
		return counts[table] > 0, nil
	}
}
