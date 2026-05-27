package tui

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

// gapsStep tracks the wizard state for the gaps command.
type gapsStep int

const (
	gapsStepPicker gapsStep = iota
	gapsStepConfig
	gapsStepRows
	gapsStepReview
	gapsStepExecute
)

// GapsModel is the top-level TUI model for the gaps command.
type GapsModel struct {
	ctx       context.Context
	step      gapsStep
	schema    *schema.Schema
	graph     *graph.Graph
	sortedAll []string
	dbType    string
	dsn       string
	counts    map[string]int64

	picker  tablePickerModel
	config  configModel
	volumes tableRowsModel
	review  reviewModel
	execute executeModel

	quitting bool
	err      error
	width    int
	height   int
}

// RunGaps launches the interactive TUI for the gaps command.
func RunGaps(ctx context.Context, s *schema.Schema, dbType, dsn string, counts map[string]int64, defaultRows, defaultBatchSize, defaultEnumRows int) error {
	g := graph.Build(s)
	sortedAll, err := g.TopologicalSort()
	if err != nil {
		return err
	}

	// Build items: empty tables are selectable, populated are shown but disabled
	var items []tableItem
	for _, name := range sortedAll {
		count := counts[name]
		parents := g.Parents(name)
		item := tableItem{
			name:    name,
			parents: parents,
		}
		if count == 0 {
			item.selected = true // empty tables default to selected
		}
		// Populated tables are not shown in picker (only empty ones matter for gaps)
		items = append(items, item)
	}

	m := GapsModel{
		ctx:       ctx,
		step:      gapsStepPicker,
		schema:    s,
		graph:     g,
		sortedAll: sortedAll,
		dbType:    dbType,
		dsn:       dsn,
		counts:    counts,
		picker:    newGapsPicker(items, counts, 40),
		config:    newConfig(defaultRows, defaultBatchSize, defaultEnumRows, false),
		height:    40,
		width:     80,
	}

	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	fm := finalModel.(GapsModel)
	if fm.quitting {
		return fmt.Errorf("aborted by user")
	}
	if fm.err != nil {
		return fm.err
	}
	return nil
}

// newGapsPicker creates a table picker annotated with row counts.
func newGapsPicker(items []tableItem, counts map[string]int64, height int) tablePickerModel {
	// Annotate parents with row counts
	for i := range items {
		count := counts[items[i].name]
		if count > 0 {
			items[i].name = fmt.Sprintf("%s (%d rows)", items[i].name, count)
			items[i].selected = false
			items[i].autoSelected = false
		}
	}
	return newTablePicker(items, height)
}

func (m GapsModel) Init() tea.Cmd { return nil }

func (m GapsModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.picker.height = msg.Height
		m.volumes.height = msg.Height
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
	}

	switch m.step {
	case gapsStepPicker:
		return m.updatePicker(msg)
	case gapsStepConfig:
		return m.updateConfig(msg)
	case gapsStepRows:
		return m.updateRows(msg)
	case gapsStepReview:
		return m.updateReview(msg)
	case gapsStepExecute:
		return m.updateExecute(msg)
	}
	return m, nil
}

func (m GapsModel) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)

	if m.picker.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.picker.done {
		explicit := m.picker.explicitlySelected()
		// Strip row count annotations from names for resolution
		cleanSelected := make(map[string]bool)
		for name := range explicit {
			cleanSelected[cleanTableName(name)] = true
		}

		resolved, autoSelected := ResolveDeps(m.graph, cleanSelected, m.sortedAll)
		// Update picker items with auto-selections
		for i := range m.picker.items {
			cleanName := cleanTableName(m.picker.items[i].name)
			if autoSelected[cleanName] {
				m.picker.items[i].autoSelected = true
				m.picker.items[i].selected = true
			}
		}

		if len(resolved) == 0 {
			m.picker.done = false
			return m, cmd
		}
		m.step = gapsStepConfig
	}
	return m, cmd
}

func (m GapsModel) updateConfig(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.config, cmd = m.config.Update(msg)

	if m.config.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.config.back {
		m.config.back = false
		m.picker.done = false
		m.step = gapsStepPicker
		return m, nil
	}
	if m.config.done {
		selected := m.picker.selectedTables()
		cleanSelected := make(map[string]bool)
		for name := range selected {
			cleanSelected[cleanTableName(name)] = true
		}
		resolved, _ := ResolveDeps(m.graph, cleanSelected, m.sortedAll)

		m.volumes = newTableRows(resolved, m.config.Rows(), nil, m.height)
		m.step = gapsStepRows
	}
	return m, cmd
}

func (m GapsModel) updateRows(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.volumes, cmd = m.volumes.Update(msg)

	if m.volumes.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.volumes.back {
		m.volumes.back = false
		m.config.done = false
		m.step = gapsStepConfig
		return m, nil
	}
	if m.volumes.done {
		parents := make(map[string][]string)
		for _, t := range m.volumes.tables {
			parents[t] = m.graph.Parents(t)
		}
		m.review = newReview(m.volumes.tables, parents,
			m.config.Rows(), m.config.EnumRows(), m.config.BatchSize(), false, m.volumes.TableRows())
		m.step = gapsStepReview
	}
	return m, cmd
}

func (m GapsModel) updateReview(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.review, cmd = m.review.Update(msg)

	if m.review.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.review.back {
		m.review.back = false
		m.volumes.done = false
		m.step = gapsStepRows
		return m, nil
	}
	if m.review.done {
		params := &seedParams{
			schema:    m.schema,
			tables:    m.review.tables,
			rows:      m.review.rows,
			enumRows:  m.review.enumRows,
			tableRows: m.review.tableRows,
			batchSize: m.review.batch,
			truncate:  false, // gaps never truncates
			dbType:    m.dbType,
			dsn:       m.dsn,
		}
		m.execute = newExecute(len(m.review.tables), m.review.dryRun)
		m.step = gapsStepExecute

		if m.review.dryRun {
			return m, tea.Batch(m.execute.spinner.Tick, startGapsDryRun(params, m.sortedAll))
		}
		return m, tea.Batch(m.execute.spinner.Tick, startGapsFill(m.ctx, params, m.sortedAll))
	}
	return m, cmd
}

func (m GapsModel) updateExecute(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.execute, cmd = m.execute.Update(msg)
	if m.execute.err != nil {
		m.err = m.execute.err
	}
	return m, cmd
}

func (m GapsModel) View() string {
	var sb strings.Builder

	steps := []string{"Tables", "Config", "Volumes", "Review", "Execute"}
	var breadcrumbs []string
	for i, name := range steps {
		if gapsStep(i) == m.step {
			breadcrumbs = append(breadcrumbs, activeStepStyle.Render(fmt.Sprintf("● %s", name)))
		} else if gapsStep(i) < m.step {
			breadcrumbs = append(breadcrumbs, successStyle.Render(fmt.Sprintf("✓ %s", name)))
		} else {
			breadcrumbs = append(breadcrumbs, stepIndicatorStyle.Render(fmt.Sprintf("○ %s", name)))
		}
	}
	sb.WriteString(headerBorder.Render("  seedstorm gaps interactive  " + strings.Join(breadcrumbs, "  ")))
	sb.WriteString("\n")

	switch m.step {
	case gapsStepPicker:
		sb.WriteString(titleStyle.Render("Select empty tables to fill"))
		sb.WriteString("\n")
		sb.WriteString(m.picker.View())
	case gapsStepConfig:
		sb.WriteString(m.config.View())
	case gapsStepRows:
		sb.WriteString(m.volumes.View())
	case gapsStepReview:
		sb.WriteString(m.review.View())
	case gapsStepExecute:
		sb.WriteString(m.execute.View())
	}
	return sb.String()
}

// startGapsFill seeds only the selected gap tables, pre-loading PKs from all tables.
func startGapsFill(ctx context.Context, s *seedParams, allSorted []string) tea.Cmd {
	return func() tea.Msg {
		start := time.Now()

		batchSize := s.batchSize
		if batchSize < 1 {
			batchSize = 1
		}

		conn, err := sql.Open(s.dbType, s.dsn)
		if err != nil {
			return seedDoneMsg{err: fmt.Errorf("failed to open connection: %w", err)}
		}
		defer conn.Close()

		if err := conn.PingContext(ctx); err != nil {
			return seedDoneMsg{err: fmt.Errorf("failed to ping database: %w", err)}
		}

		data, err := faker.GenerateFilteredWithCounts(s.schema, allSorted, s.tables, s.rows, s.enumRows, s.tableRows, conn, s.dbType)
		if err != nil {
			return seedDoneMsg{err: fmt.Errorf("data generation failed: %w", err)}
		}

		totalRows := 0
		rowsMap := make(map[string]int, len(s.tables))
		for _, tableName := range s.tables {
			tableRows := data[tableName]
			for i := 0; i < len(tableRows); i += batchSize {
				end := i + batchSize
				if end > len(tableRows) {
					end = len(tableRows)
				}
				query, values := db.BuildBatchInsert(tableName, tableRows[i:end], s.dbType)
				if _, err := conn.ExecContext(ctx, query, values...); err != nil {
					return seedDoneMsg{err: fmt.Errorf("insert into %s failed: %w", tableName, err)}
				}
			}
			rowsMap[tableName] = len(tableRows)
			totalRows += len(tableRows)
		}

		return seedDoneMsg{
			totalRows: totalRows,
			elapsed:   time.Since(start),
			tables:    s.tables,
			rowsMap:   rowsMap,
		}
	}
}

// startGapsDryRun generates data for gap tables and returns a preview.
func startGapsDryRun(s *seedParams, allSorted []string) tea.Cmd {
	return func() tea.Msg {
		data, err := faker.GenerateFilteredWithCounts(s.schema, allSorted, s.tables, s.rows, s.enumRows, s.tableRows, nil, s.dbType)
		if err != nil {
			return dryRunDoneMsg{err: fmt.Errorf("data generation failed: %w", err)}
		}

		var tables []dryRunTable
		total := 0
		for _, tableName := range s.tables {
			rows := data[tableName]
			dt := dryRunTable{name: tableName, rows: len(rows)}
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

// cleanTableName strips the " (N rows)" annotation from gap picker display names.
func cleanTableName(name string) string {
	if idx := strings.Index(name, " ("); idx != -1 && strings.HasSuffix(name, " rows)") {
		return name[:idx]
	}
	return name
}
