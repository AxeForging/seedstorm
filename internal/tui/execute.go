package tui

import (
	"context"
	"database/sql"
	"fmt"
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

// dryRunDoneMsg is sent when dry-run SQL generation completes.
type dryRunDoneMsg struct {
	sql string
	err error
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
	dryRunOutput    string
	err             error
	quitting        bool
	seededTables    []string
	seededRows      map[string]int
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
		if msg.String() == "q" || msg.String() == "ctrl+c" {
			m.quitting = true
			return m, nil
		}
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
		m.dryRunOutput = msg.sql
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
		sb.WriteString(titleStyle.Render("Dry Run — SQL Output"))
		sb.WriteString("\n\n")
		if m.done {
			if m.err != nil {
				sb.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v", m.err)))
			} else {
				sb.WriteString(m.dryRunOutput)
			}
			sb.WriteString("\n")
			sb.WriteString(helpStyle.Render("  q quit"))
		} else {
			sb.WriteString(fmt.Sprintf("  %s Generating SQL...\n", m.spinner.View()))
		}
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

// startDryRun returns a tea.Cmd that generates dry-run SQL output.
func startDryRun(s *seedParams) tea.Cmd {
	return func() tea.Msg {
		data, err := faker.Generate(s.schema, s.tables, s.rows, s.enumRows, nil, s.dbType)
		if err != nil {
			return dryRunDoneMsg{err: fmt.Errorf("data generation failed: %w", err)}
		}

		var sb strings.Builder
		for _, tableName := range s.tables {
			for _, row := range data[tableName] {
				query, _ := db.BuildInsert(tableName, row, s.dbType)
				sb.WriteString(query)
				sb.WriteString(";\n")
			}
		}

		return dryRunDoneMsg{sql: sb.String()}
	}
}
