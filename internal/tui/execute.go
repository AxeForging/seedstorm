package tui

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/runerr"
	"github.com/AxeForging/seedstorm/internal/safego"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

// tableSeededMsg is sent when a table finishes seeding.
type tableSeededMsg struct {
	table string
	rows  int
}

// seedProgressMsg reports a written piece with run totals (seeder.Progress).
type seedProgressMsg seeder.Progress

// seedPhaseMsg names a step before rows are written (connecting, truncating).
type seedPhaseMsg string

// seedDoneMsg is sent when the entire seed operation completes.
type seedDoneMsg struct {
	totalRows int
	elapsed   time.Duration
	tables    []string
	rowsMap   map[string]int
	err       error
	syncErr   error
}

// dryRunDoneMsg is sent when dry-run generation completes.
type dryRunDoneMsg struct {
	tables []dryRunTable
	total  int
	err    error
}

type dryRunTable struct {
	name    string
	rows    int
	sample  map[string]interface{} // first row as preview
	columns []string               // sorted column names
}

type executeModel struct {
	spinner         spinner.Model
	currentTable    string
	completedTables int
	totalTables     int
	totalRows       int
	elapsed         time.Duration
	done            bool
	dryRun          bool
	dryRunTables    []dryRunTable
	dryRunTotal     int
	dryRunScroll    int
	dryRunLines     int // total line count for scroll clamping
	err             error
	quitting        bool
	seededTables    []string
	seededRows      map[string]int
	height          int
	// events carries progress from the running seed; nil before it starts.
	events    chan tea.Msg
	startedAt time.Time
	phase     string
	progress  seeder.Progress
	meter     *seeder.Meter
	estimate  seeder.Estimate
	syncErr   error
}

func newExecute(totalTables int, dryRun bool) executeModel {
	s := spinner.New()
	s.Spinner = spinner.Dot
	s.Style = selectedStyle
	return executeModel{
		spinner:     s,
		totalTables: totalTables,
		dryRun:      dryRun,
		seededRows:  make(map[string]int),
		startedAt:   time.Now(),
	}
}

func (m executeModel) Update(msg tea.Msg) (executeModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "q", "ctrl+c":
			m.quitting = true
			return m, nil
		case "up", "k":
			if m.dryRunScroll > 0 {
				m.dryRunScroll--
			}
			return m, nil
		case "down", "j":
			maxScroll := m.dryRunLines - m.dryRunVisible()
			if maxScroll < 0 {
				maxScroll = 0
			}
			if m.dryRunScroll < maxScroll {
				m.dryRunScroll++
			}
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case tableSeededMsg:
		m.currentTable = msg.table
		m.completedTables++
		m.seededTables = append(m.seededTables, msg.table)
		m.seededRows[msg.table] = msg.rows
		return m, waitSeed(m.events)
	case seedPhaseMsg:
		m.phase = string(msg)
		return m, waitSeed(m.events)
	case seedProgressMsg:
		p := seeder.Progress(msg)
		m.phase = ""
		m.progress = p
		m.currentTable = p.Table
		if m.meter == nil {
			m.meter = seeder.NewMeter(m.startedAt)
		}
		m.estimate = m.meter.Observe(time.Now(), p.RowsDone, p.RowsTotal)
		return m, waitSeed(m.events)
	case seedDoneMsg:
		m.done = true
		m.totalRows = msg.totalRows
		m.elapsed = msg.elapsed
		m.seededTables = msg.tables
		m.seededRows = msg.rowsMap
		if m.seededRows == nil {
			m.seededRows = map[string]int{}
		}
		m.completedTables = len(msg.tables)
		m.err = msg.err
		m.syncErr = msg.syncErr
		return m, nil
	case dryRunDoneMsg:
		m.done = true
		m.dryRunTables = msg.tables
		m.dryRunTotal = msg.total
		m.err = msg.err
		// Pre-compute line count: each table = 3 lines (name + sample + blank)
		m.dryRunLines = len(msg.tables) * 3
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

// dryRunVisible returns how many content lines fit in the scrollable area.
// Reserves space for: breadcrumb(2) + title(2) + summary(2) + footer(4) = ~10 lines.
func (m executeModel) dryRunVisible() int {
	h := m.height
	if h < 20 {
		h = 40
	}
	return h - 10
}

func (m executeModel) View() string {
	var sb strings.Builder

	if m.dryRun {
		sb.WriteString(titleStyle.Render("Dry Run — Preview"))
		sb.WriteString("\n\n")
		if !m.done {
			fmt.Fprintf(&sb, "  %s Generating data...\n", m.spinner.View())
			return sb.String()
		}
		if m.err != nil {
			sb.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", m.err)))
			sb.WriteString("\n\n")
			sb.WriteString(helpStyle.Render("  q quit"))
			return sb.String()
		}

		// Build all content lines
		var lines []string
		for _, dt := range m.dryRunTables {
			lines = append(lines, fmt.Sprintf("  %s  %s",
				selectedStyle.Render(fmt.Sprintf("%-28s", dt.name)),
				dimStyle.Render(fmt.Sprintf("%d rows", dt.rows))))

			if dt.sample != nil {
				maxPreview := 3
				if len(dt.columns) < maxPreview {
					maxPreview = len(dt.columns)
				}
				var previews []string
				for _, col := range dt.columns[:maxPreview] {
					val := dt.sample[col]
					valStr := fmt.Sprintf("%v", val)
					if len(valStr) > 30 {
						valStr = valStr[:27] + "..."
					}
					if val == nil {
						valStr = "NULL"
					}
					previews = append(previews, dimStyle.Render(fmt.Sprintf("%s=%s", col, valStr)))
				}
				if len(dt.columns) > maxPreview {
					previews = append(previews, dimStyle.Render(fmt.Sprintf("+%d more", len(dt.columns)-maxPreview)))
				}
				lines = append(lines, "    "+strings.Join(previews, "  "))
			}
			lines = append(lines, "")
		}

		// Scrollable content area
		visible := m.dryRunVisible()
		if visible > len(lines) {
			visible = len(lines)
		}
		scroll := m.dryRunScroll
		maxScroll := len(lines) - visible
		if maxScroll < 0 {
			maxScroll = 0
		}
		if scroll > maxScroll {
			scroll = maxScroll
		}
		if scroll < 0 {
			scroll = 0
		}
		end := scroll + visible
		if end > len(lines) {
			end = len(lines)
		}
		for _, line := range lines[scroll:end] {
			sb.WriteString(line)
			sb.WriteString("\n")
		}

		// ── Sticky footer — always visible ──
		sb.WriteString("\n")
		sb.WriteString(strings.Repeat("─", 60))
		sb.WriteString("\n")
		sb.WriteString(successStyle.Render(fmt.Sprintf("  %d tables • %d total rows", len(m.dryRunTables), m.dryRunTotal)))
		if len(lines) > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  (scroll %d/%d)", scroll+visible, len(lines))))
		}
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render("  ↑/↓ scroll • q quit"))
		return sb.String()
	}

	sb.WriteString(titleStyle.Render("Seeding database"))
	sb.WriteString("\n\n")

	if m.done {
		if m.err != nil {
			sb.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v\n", m.err)))
			sb.WriteString("\n")
			var notWritten []string
			for _, t := range m.seededTables {
				if n := m.seededRows[t]; n > 0 {
					fmt.Fprintf(&sb, "    %-30s %d rows\n", t, n)
				} else {
					notWritten = append(notWritten, t)
				}
			}
			if len(notWritten) > 0 {
				sb.WriteString(dimStyle.Render("    not written: " + strings.Join(notWritten, ", ")))
				sb.WriteString("\n")
			}
		} else {
			sb.WriteString(successStyle.Render(fmt.Sprintf("  Seeding complete! %d rows across %d tables in %s\n",
				m.totalRows, m.completedTables, m.elapsed.Round(time.Millisecond))))
			sb.WriteString("\n")
			for _, t := range m.seededTables {
				fmt.Fprintf(&sb, "    %-30s %d rows\n", t, m.seededRows[t])
			}
		}
		if m.syncErr != nil {
			sb.WriteString(errorStyle.Render(fmt.Sprintf("\n  Sequences not advanced: %v (application inserts may reuse seeded ids)\n", m.syncErr)))
		}
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render("  q quit"))
	} else {
		elapsed := time.Since(m.startedAt).Round(100 * time.Millisecond)
		switch {
		case m.phase != "":
			fmt.Fprintf(&sb, "  %s %s  · %s\n", m.spinner.View(), m.phase, elapsed)
		case m.progress.RowsTotal > 0:
			pct := m.progress.RowsDone * 100 / m.progress.RowsTotal
			fmt.Fprintf(&sb, "  %s Seeding %s  %s / %s rows (%d%%)  · %d/%d tables  · %s\n",
				m.spinner.View(), m.currentTable, groupThousands(m.progress.RowsDone), groupThousands(m.progress.RowsTotal),
				pct, m.completedTables, m.totalTables, elapsed)
			if est := m.estimate.String(); est != "" {
				sb.WriteString(dimStyle.Render("    " + est))
				sb.WriteString("\n")
			}
		case m.currentTable != "":
			fmt.Fprintf(&sb, "  %s Seeding %s  · %d/%d tables  · %s\n", m.spinner.View(), m.currentTable, m.completedTables, m.totalTables, elapsed)
		default:
			fmt.Fprintf(&sb, "  %s Starting (%d tables)  · %s\n", m.spinner.View(), m.totalTables, elapsed)
		}
		sb.WriteString(helpStyle.Render("\n  ctrl+c aborts (rows already inserted stay)"))
	}

	return sb.String()
}

// startSeed runs the seed in a goroutine; progress, finished tables and the
// final result arrive on events (read with waitSeed).
func startSeed(ctx context.Context, s *seedParams, events chan tea.Msg) tea.Cmd {
	return runSeedInto(ctx, s, s.tables, s.truncate, events)
}

// runSeedInto seeds tables (preloading keys of preload) and reports on events.
func runSeedInto(ctx context.Context, s *seedParams, preload []string, truncate bool, events chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()
		send := func(msg tea.Msg) {
			select {
			case events <- msg:
			default: // the view keeps up; dropping a tick only skips a redraw
			}
		}
		done := seedDoneMsg{tables: s.tables}
		err := safego.Run("seed", func() error {
			batchSize := max(s.batchSize, 1)
			send(seedPhaseMsg("Connecting"))
			conn, err := sql.Open(s.dbType, s.dsn)
			if err != nil {
				return runerr.At(runerr.PhaseConnect, "", fmt.Errorf("failed to open connection: %w", err))
			}
			defer conn.Close()
			pctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			err = conn.PingContext(pctx)
			cancel()
			if err != nil {
				return runerr.At(runerr.PhaseConnect, "", fmt.Errorf("database did not answer: %w", err))
			}
			if truncate {
				send(seedPhaseMsg("Truncating tables"))
				if err := db.TruncateConcurrently(ctx, conn, s.dbType, s.tables, seeder.DefaultWorkers, nil); err != nil {
					return runerr.At(runerr.PhaseTruncate, "", fmt.Errorf("truncate failed: %w", err))
				}
			}
			send(seedPhaseMsg("Generating the first rows"))
			// Move Postgres sequences past the inserted ids, even if an insert fails.
			defer func() {
				if _, err := db.SyncSequences(context.WithoutCancel(ctx), conn, s.dbType, s.tables); err != nil {
					done.syncErr = err
				}
			}()
			opts := s.seedOptions(batchSize, false, nil)
			opts.OnProgress = func(p seeder.Progress) { send(seedProgressMsg(p)) }
			opts.OnTable = func(p seeder.Progress) { send(tableSeededMsg{table: p.Table, rows: int(p.Inserted)}) }
			res, err := seeder.Seed(ctx, conn, s.dbType, s.schema, preload, s.tables, opts)
			done.totalRows, done.rowsMap = res.Total, res.Counts
			return err
		})
		done.err = err
		done.elapsed = time.Since(start)
		events <- done
		return nil
	}
}

// waitSeed reads the next message of a running seed.
func waitSeed(events chan tea.Msg) tea.Cmd {
	if events == nil {
		return nil
	}
	return func() tea.Msg { return <-events }
}

// groupThousands formats n as 12,345.
func groupThousands(n int64) string {
	s := fmt.Sprint(n)
	if n < 0 {
		return "-" + groupThousands(-n)
	}
	var out []byte
	for i, c := range []byte(s) {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, c)
	}
	return string(out)
}

// startDryRun returns a tea.Cmd that generates data and builds a summary.
func startDryRun(s *seedParams) tea.Cmd {
	return func() tea.Msg { return dryRunSummary(s, s.tables) }
}

// seedOptions maps the flow's parameters onto a seeder run.
func (s *seedParams) seedOptions(batchSize int, dryRun bool, onRows func(string, []map[string]interface{}) error) seeder.SeedOptions {
	return seeder.SeedOptions{
		Rows: s.rows, EnumRows: s.enumRows, TableRows: s.tableRows, BatchSize: batchSize, DryRun: dryRun,
		Workers:  seeder.DefaultWorkers,
		Generate: faker.GenerateOptions{SelfRefDepth: s.selfRefDepth, Overrides: s.overrides},
		OnRows:   onRows,
	}
}

// dryRunSummary generates every row without a database, keeping only counts
// and each table's first row, so a preview of any size uses flat memory.
func dryRunSummary(s *seedParams, preload []string) dryRunDoneMsg {
	samples := map[string]map[string]interface{}{}
	res, err := seeder.Seed(context.Background(), nil, s.dbType, s.schema, preload, s.tables, s.seedOptions(0, true, sampleFirstRows(samples, nil)))
	if err != nil {
		return dryRunDoneMsg{err: err}
	}
	return dryRunDoneMsg{tables: previewTables(s.tables, res.Counts, samples), total: res.Total}
}

// previewTables summarises a run for the review screen: each table's row count
// and first row.
func previewTables(tables []string, counts map[string]int, samples map[string]map[string]interface{}) []dryRunTable {
	out := make([]dryRunTable, 0, len(tables))
	for _, tableName := range tables {
		dt := dryRunTable{name: tableName, rows: counts[tableName]}
		if sample, ok := samples[tableName]; ok {
			dt.sample = sample
			cols := make([]string, 0, len(sample))
			for c := range sample {
				cols = append(cols, c)
			}
			sort.Strings(cols)
			dt.columns = cols
		}
		out = append(out, dt)
	}
	return out
}

// sampleFirstRows returns an OnRows hook that keeps each table's first row in
// samples and passes every chunk on to next, if set.
func sampleFirstRows(samples map[string]map[string]interface{}, next func([]map[string]interface{}) error) func(string, []map[string]interface{}) error {
	return func(table string, rows []map[string]interface{}) error {
		if _, ok := samples[table]; !ok && len(rows) > 0 {
			samples[table] = rows[0]
		}
		if next == nil {
			return nil
		}
		return next(rows)
	}
}
