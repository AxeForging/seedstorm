package cli

import (
	"context"
	"database/sql"
	"strings"
	"sync"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/logging"
	"github.com/AxeForging/seedstorm/internal/seeder"
	"github.com/AxeForging/seedstorm/internal/tuning"
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

func genWorkersFlag() cli.Flag {
	return &cli.IntFlag{
		Name:  "gen-workers",
		Usage: "Tables generated at once on separate cores (needs --workers > 1; seed ignores it with --seed so runs stay reproducible)",
		Value: 1,
	}
}

// genWorkers reads --gen-workers, lowered to the cores this process may use
// (a container CPU quota counts, not only the host's cores) with a log line
// saying so.
func genWorkers(cmd *cli.Command) int {
	requested := cmd.Int("gen-workers")
	n := tuning.ClampGenerators(requested)
	if requested > n {
		logging.Log.Info().Int("requested", requested).Int("using", n).Msg("Generators limited to the CPUs available")
	}
	return n
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

// stepLogger logs a table-by-table step (counting, introspecting) at most every
// progressInterval, plus its last table, so a long step is never silent.
func stepLogger(what string, now func() time.Time) func(done, total int, table string) {
	var last time.Time
	return func(done, total int, table string) {
		t := now()
		if done < total && t.Sub(last) < progressInterval {
			return
		}
		last = t
		logging.Log.Info().Int("done", done).Int("total", total).Str("table", table).Msg(what)
	}
}

// sideStepLogger is stepLogger for a two-sided step, one throttle per side.
func sideStepLogger(what string, now func() time.Time) func(side string, done, total int, table string) {
	loggers := map[string]func(int, int, string){}
	var mu sync.Mutex
	return func(side string, done, total int, table string) {
		mu.Lock()
		l, ok := loggers[side]
		if !ok {
			l = stepLogger(what+" ("+side+")", now)
			loggers[side] = l
		}
		mu.Unlock()
		l(done, total, table)
	}
}
