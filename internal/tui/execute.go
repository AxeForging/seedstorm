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
)

// tableSeededMsg is sent when a table finishes seeding.
type tableSeededMsg struct {
	table string
	rows  int
}

// seedDoneMsg is sent when the entire seed operation completes.
type seedDoneMsg struct {
	totalRows int
	elapsed   time.Duration
	err       error
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
	err             error
	quitting        bool
	seededTables    []string
	seededRows      map[string]int
	height          int
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
			m.dryRunScroll++
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case tableSeededMsg:
		m.currentTable = msg.table
		m.completedTables++
		m.seededTables = append(m.seededTables, msg.table)
		m.seededRows[msg.table] = msg.rows
	case seedDoneMsg:
		m.done = true
		m.totalRows = msg.totalRows
		m.elapsed = msg.elapsed
		m.err = msg.err
		return m, nil
	case dryRunDoneMsg:
		m.done = true
		m.dryRunTables = msg.tables
		m.dryRunTotal = msg.total
		m.err = msg.err
		return m, nil
	case spinner.TickMsg:
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m executeModel) View() string {
	var sb strings.Builder

	if m.dryRun {
		sb.WriteString(titleStyle.Render("Dry Run — Preview"))
		sb.WriteString("\n\n")
		if !m.done {
			sb.WriteString(fmt.Sprintf("  %s Generating data...\n", m.spinner.View()))
			return sb.String()
		}
		if m.err != nil {
			sb.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", m.err)))
			sb.WriteString("\n\n")
			sb.WriteString(helpStyle.Render("  q quit"))
			return sb.String()
		}

		sb.WriteString(successStyle.Render(fmt.Sprintf("  Would generate %d rows across %d tables\n", m.dryRunTotal, len(m.dryRunTables))))
		sb.WriteString("\n")

		// Build all lines first, then apply scroll
		var lines []string
		for _, dt := range m.dryRunTables {
			lines = append(lines, fmt.Sprintf("  %s  %s",
				selectedStyle.Render(fmt.Sprintf("%-28s", dt.name)),
				dimStyle.Render(fmt.Sprintf("%d rows", dt.rows))))

			// Show sample row preview (first 3 columns)
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

		// Scrollable view — header takes ~6 lines, footer ~2
		h := m.height
		if h < 20 {
			h = 40 // fallback if WindowSizeMsg hasn't arrived yet
		}
		visible := h - 8
		if visible > len(lines) {
			visible = len(lines)
		}
		if m.dryRunScroll > len(lines)-visible {
			m.dryRunScroll = len(lines) - visible
		}
		if m.dryRunScroll < 0 {
			m.dryRunScroll = 0
		}
		end := m.dryRunScroll + visible
		if end > len(lines) {
			end = len(lines)
		}
		for _, line := range lines[m.dryRunScroll:end] {
			sb.WriteString(line)
			sb.WriteString("\n")
		}

		if len(lines) > visible {
			sb.WriteString(dimStyle.Render(fmt.Sprintf("  ↑/↓ scroll (%d-%d of %d lines)", m.dryRunScroll+1, end, len(lines))))
			sb.WriteString("\n")
		}

		sb.WriteString(helpStyle.Render("  ↑/↓ scroll • q quit"))
		return sb.String()
	}

	sb.WriteString(titleStyle.Render("Seeding database"))
	sb.WriteString("\n\n")

	if m.done {
		if m.err != nil {
			sb.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v\n", m.err)))
		} else {
			sb.WriteString(successStyle.Render(fmt.Sprintf("  Seeding complete! %d rows across %d tables in %s\n",
				m.totalRows, m.completedTables, m.elapsed.Round(time.Millisecond))))
		}
		sb.WriteString("\n")
		for _, t := range m.seededTables {
			sb.WriteString(fmt.Sprintf("    %-30s %d rows\n", t, m.seededRows[t]))
		}
		sb.WriteString("\n")
		sb.WriteString(helpStyle.Render("  q quit"))
	} else {
		pct := 0
		if m.totalTables > 0 {
			pct = m.completedTables * 100 / m.totalTables
		}
		sb.WriteString(fmt.Sprintf("  %s Seeding %s (%d/%d tables, %d%%)\n",
			m.spinner.View(), m.currentTable, m.completedTables, m.totalTables, pct))
	}

	return sb.String()
}

// startSeed returns a tea.Cmd that runs the seed operation in a goroutine.
func startSeed(ctx context.Context, s *seedParams) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()

		conn, err := sql.Open(s.dbType, s.dsn)
		if err != nil {
			return seedDoneMsg{err: fmt.Errorf("failed to open connection: %w", err)}
		}
		defer conn.Close()

		if err := conn.PingContext(ctx); err != nil {
			return seedDoneMsg{err: fmt.Errorf("failed to ping database: %w", err)}
		}

		if s.truncate {
			if err := db.Truncate(ctx, conn, s.dbType, s.tables); err != nil {
				return seedDoneMsg{err: fmt.Errorf("truncate failed: %w", err)}
			}
		}

		data, err := faker.Generate(s.schema, s.tables, s.rows, s.enumRows, conn, s.dbType)
		if err != nil {
			return seedDoneMsg{err: fmt.Errorf("data generation failed: %w", err)}
		}

		totalRows := 0
		for _, tableName := range s.tables {
			tableRows := data[tableName]
			// Batch insert
			for i := 0; i < len(tableRows); i += s.batchSize {
				end := i + s.batchSize
				if end > len(tableRows) {
					end = len(tableRows)
				}
				query, values := db.BuildBatchInsert(tableName, tableRows[i:end], s.dbType)
				if _, err := conn.ExecContext(ctx, query, values...); err != nil {
					return seedDoneMsg{err: fmt.Errorf("insert into %s failed: %w", tableName, err)}
				}
			}
			totalRows += len(tableRows)
			// Send progress — but since we're in a Cmd, we can't send intermediate
			// messages. The progress will show after completion.
		}

		return seedDoneMsg{
			totalRows: totalRows,
			elapsed:   time.Since(start),
		}
	}
}

// startDryRun returns a tea.Cmd that generates data and builds a summary.
func startDryRun(s *seedParams) tea.Cmd {
	return func() tea.Msg {
		data, err := faker.Generate(s.schema, s.tables, s.rows, s.enumRows, nil, s.dbType)
		if err != nil {
			return dryRunDoneMsg{err: fmt.Errorf("data generation failed: %w", err)}
		}

		var tables []dryRunTable
		total := 0
		for _, tableName := range s.tables {
			rows := data[tableName]
			dt := dryRunTable{
				name: tableName,
				rows: len(rows),
			}

			// Sorted column names + first row sample
			if len(rows) > 0 {
				dt.sample = rows[0]
				cols := make([]string, 0, len(rows[0]))
				for c := range rows[0] {
					cols = append(cols, c)
				}
				sort.Strings(cols)
				dt.columns = cols
			}

			tables = append(tables, dt)
			total += len(rows)
		}

		return dryRunDoneMsg{tables: tables, total: total}
	}
}
