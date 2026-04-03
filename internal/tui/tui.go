package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

type step int

const (
	stepPicker step = iota
	stepConfig
	stepReview
	stepExecute
)

// seedParams holds everything needed to execute the seed operation.
type seedParams struct {
	schema    *schema.Schema
	tables    []string
	rows      int
	enumRows  int
	batchSize int
	truncate  bool
	dbType    string
	dsn       string
}

// Model is the top-level TUI model orchestrating the wizard steps.
type Model struct {
	step      step
	schema    *schema.Schema
	graph     *graph.Graph
	sortedAll []string
	dbType    string
	dsn       string

	picker  tablePickerModel
	config  configModel
	review  reviewModel
	execute executeModel

	quitting bool
	err      error
	width    int
	height   int
}

// Run launches the interactive TUI and returns when the user completes or aborts.
func Run(ctx context.Context, s *schema.Schema, dbType, dsn string, defaultRows, defaultBatchSize, defaultEnumRows int, defaultTruncate bool) error {
	g := graph.Build(s)
	sortedAll, err := g.TopologicalSort()
	if err != nil {
		return err
	}

	// Build table items with FK parent info
	items := make([]tableItem, len(sortedAll))
	for i, name := range sortedAll {
		parents := g.Parents(name)
		items[i] = tableItem{
			name:     name,
			parents:  parents,
			selected: true, // start with all selected
		}
	}

	m := Model{
		step:      stepPicker,
		schema:    s,
		graph:     g,
		sortedAll: sortedAll,
		dbType:    dbType,
		dsn:       dsn,
		picker:    newTablePicker(items, 24),
		config:    newConfig(defaultRows, defaultBatchSize, defaultEnumRows, defaultTruncate),
		height:    24,
		width:     80,
	}

	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	fm := finalModel.(Model)
	if fm.quitting {
		return fmt.Errorf("aborted by user")
	}
	if fm.err != nil {
		return fm.err
	}
	return nil
}

func (m Model) Init() tea.Cmd {
	return nil
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.picker.height = msg.Height
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" {
			m.quitting = true
			return m, tea.Quit
		}
	}

	switch m.step {
	case stepPicker:
		return m.updatePicker(msg)
	case stepConfig:
		return m.updateConfig(msg)
	case stepReview:
		return m.updateReview(msg)
	case stepExecute:
		return m.updateExecute(msg)
	}

	return m, nil
}

func (m Model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)

	if m.picker.quitting {
		m.quitting = true
		return m, tea.Quit
	}

	if m.picker.done {
		// Run auto-dependency resolution
		explicit := m.picker.explicitlySelected()
		resolved, autoSelected := ResolveDeps(m.schema, m.graph, explicit, m.sortedAll)

		// Update picker items to reflect auto-selections
		for i := range m.picker.items {
			name := m.picker.items[i].name
			if autoSelected[name] {
				m.picker.items[i].autoSelected = true
				m.picker.items[i].selected = true
			}
		}

		if len(resolved) == 0 {
			m.picker.done = false // stay on picker if nothing selected
			return m, cmd
		}

		m.step = stepConfig
	}

	return m, cmd
}

func (m Model) updateConfig(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.config, cmd = m.config.Update(msg)

	if m.config.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.config.back {
		m.config.back = false
		m.picker.done = false
		m.step = stepPicker
		return m, nil
	}
	if m.config.done {
		// Build review data
		selected := m.picker.selectedTables()
		var tables []string
		for _, t := range m.sortedAll {
			if selected[t] {
				tables = append(tables, t)
			}
		}
		parents := make(map[string][]string)
		for _, t := range tables {
			parents[t] = m.graph.Parents(t)
		}

		m.review = newReview(tables, parents,
			m.config.Rows(), m.config.EnumRows(), m.config.BatchSize(), m.config.Truncate())
		m.step = stepReview
	}

	return m, cmd
}

func (m Model) updateReview(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.review, cmd = m.review.Update(msg)

	if m.review.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.review.back {
		m.review.back = false
		m.config.done = false
		m.step = stepConfig
		return m, nil
	}
	if m.review.done {
		params := &seedParams{
			schema:    m.schema,
			tables:    m.review.tables,
			rows:      m.review.rows,
			enumRows:  m.review.enumRows,
			batchSize: m.review.batch,
			truncate:  m.review.truncate,
			dbType:    m.dbType,
			dsn:       m.dsn,
		}

		m.execute = newExecute(len(m.review.tables), m.review.dryRun)
		m.step = stepExecute

		if m.review.dryRun {
			return m, tea.Batch(m.execute.spinner.Tick, startDryRun(params))
		}
		return m, tea.Batch(m.execute.spinner.Tick, startSeed(context.Background(), params))
	}

	return m, cmd
}

func (m Model) updateExecute(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.execute, cmd = m.execute.Update(msg)

	if m.execute.quitting || (m.execute.done && m.execute.err == nil) {
		// On q or successful completion, check if there's a quit key press
		if msg, ok := msg.(tea.KeyMsg); ok && (msg.String() == "q" || msg.String() == "esc") {
			return m, tea.Quit
		}
	}
	if m.execute.err != nil {
		m.err = m.execute.err
	}

	return m, cmd
}

func (m Model) View() string {
	var sb strings.Builder

	// Step breadcrumb
	steps := []string{"Tables", "Config", "Review", "Execute"}
	var breadcrumbs []string
	for i, name := range steps {
		if step(i) == m.step {
			breadcrumbs = append(breadcrumbs, activeStepStyle.Render(fmt.Sprintf("● %s", name)))
		} else if step(i) < m.step {
			breadcrumbs = append(breadcrumbs, successStyle.Render(fmt.Sprintf("✓ %s", name)))
		} else {
			breadcrumbs = append(breadcrumbs, stepIndicatorStyle.Render(fmt.Sprintf("○ %s", name)))
		}
	}

	header := headerBorder.Render(
		"  seedstorm interactive  " + strings.Join(breadcrumbs, "  "),
	)
	sb.WriteString(header)
	sb.WriteString("\n")

	switch m.step {
	case stepPicker:
		sb.WriteString(m.picker.View())
	case stepConfig:
		sb.WriteString(m.config.View())
	case stepReview:
		sb.WriteString(m.review.View())
	case stepExecute:
		sb.WriteString(m.execute.View())
	}

	return sb.String()
}
