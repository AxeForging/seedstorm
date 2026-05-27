package tui

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/goccy/go-yaml"

	dbpkg "github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/faker"
	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
)

type genStep int

const (
	genStepPicker genStep = iota
	genStepConfig
	genStepRows
	genStepExecute
)

// genConfigModel extends config with format and output fields.
type genConfigModel struct {
	rowsInput textinput.Model
	outInput  textinput.Model
	formatIdx int // 0=yaml 1=json 2=sql
	focused   int // 0=rows 1=format 2=output
	done      bool
	back      bool
	quitting  bool
}

var genFormats = []string{"yaml", "json", "sql"}

func newGenConfig(rows int, format, outPath string) genConfigModel {
	ri := textinput.New()
	ri.SetValue(fmt.Sprintf("%d", rows))
	ri.CharLimit = 10
	ri.Width = 12
	ri.Focus()

	oi := textinput.New()
	oi.SetValue(outPath)
	oi.CharLimit = 200
	oi.Width = 40
	oi.Placeholder = "stdout (leave empty)"

	fmtIdx := 0
	for i, f := range genFormats {
		if f == strings.ToLower(format) {
			fmtIdx = i
		}
	}

	return genConfigModel{
		rowsInput: ri,
		outInput:  oi,
		formatIdx: fmtIdx,
	}
}

func (m genConfigModel) Rows() int {
	n, err := strconv.Atoi(strings.TrimSpace(m.rowsInput.Value()))
	if err != nil || n < 1 {
		return 10
	}
	return n
}
func (m genConfigModel) Format() string  { return genFormats[m.formatIdx] }
func (m genConfigModel) OutPath() string { return strings.TrimSpace(m.outInput.Value()) }

func (m genConfigModel) Update(msg tea.Msg) (genConfigModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "down", "j":
			m.rowsInput.Blur()
			m.outInput.Blur()
			m.focused = (m.focused + 1) % 3
			switch m.focused {
			case 0:
				m.rowsInput.Focus()
			case 2:
				m.outInput.Focus()
			}
			return m, nil
		case "shift+tab", "up", "k":
			m.rowsInput.Blur()
			m.outInput.Blur()
			m.focused = (m.focused + 2) % 3
			switch m.focused {
			case 0:
				m.rowsInput.Focus()
			case 2:
				m.outInput.Focus()
			}
			return m, nil
		case "left", "h":
			if m.focused == 1 {
				m.formatIdx = (m.formatIdx + len(genFormats) - 1) % len(genFormats)
				return m, nil
			}
		case "right", "l":
			if m.focused == 1 {
				m.formatIdx = (m.formatIdx + 1) % len(genFormats)
				return m, nil
			}
		case " ":
			if m.focused == 1 {
				m.formatIdx = (m.formatIdx + 1) % len(genFormats)
				return m, nil
			}
		case "enter":
			m.done = true
			return m, nil
		case "b":
			if m.focused == 1 { // only works on format selector (not text inputs)
				m.back = true
				return m, nil
			}
		case "esc":
			m.back = true
			return m, nil
		case "q", "ctrl+c":
			m.quitting = true
			return m, nil
		}
	}

	// Update focused text input
	switch m.focused {
	case 0:
		var cmd tea.Cmd
		m.rowsInput, cmd = m.rowsInput.Update(msg)
		return m, cmd
	case 2:
		var cmd tea.Cmd
		m.outInput, cmd = m.outInput.Update(msg)
		return m, cmd
	}
	return m, nil
}

func (m genConfigModel) View() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Configure generation"))
	sb.WriteString("\n\n")

	fields := []struct {
		label string
		view  string
	}{
		{"Rows per table", m.rowsInput.View()},
		{"Format", m.formatView()},
		{"Output file", m.outInput.View()},
	}

	for i, f := range fields {
		cursor := "  "
		if i == m.focused {
			cursor = cursorStyle.Render("▸ ")
		}
		label := f.label
		if i == m.focused {
			label = lipgloss.NewStyle().Bold(true).Render(label)
		}
		sb.WriteString(fmt.Sprintf("%s%s: %s\n", cursor, label, f.view))
	}

	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  tab/↑↓ navigate • ←→/space cycle format • enter confirm • b back • q quit"))
	return sb.String()
}

func (m genConfigModel) formatView() string {
	var parts []string
	for i, f := range genFormats {
		if i == m.formatIdx {
			parts = append(parts, selectedStyle.Render("["+f+"]"))
		} else {
			parts = append(parts, dimStyle.Render(" "+f+" "))
		}
	}
	return strings.Join(parts, " ")
}

// generateDoneMsg is sent when generate completes.
type generateDoneMsg struct {
	output  string
	outPath string
	tables  []dryRunTable
	total   int
	err     error
}

// GenModel is the top-level TUI model for the generate command.
type GenModel struct {
	ctx       context.Context
	step      genStep
	schema    *schema.Schema
	graph     *graph.Graph
	sortedAll []string
	dbType    string

	picker    tablePickerModel
	genConfig genConfigModel
	volumes   tableRowsModel
	execute   executeModel

	quitting bool
	err      error
	width    int
	height   int
}

// RunGenerate launches the interactive TUI for the generate command.
func RunGenerate(ctx context.Context, s *schema.Schema, dbType, format, outPath string, defaultRows int) error {
	g := graph.Build(s)
	sortedAll, err := g.TopologicalSort()
	if err != nil {
		return err
	}

	items := make([]tableItem, len(sortedAll))
	for i, name := range sortedAll {
		items[i] = tableItem{name: name, parents: g.Parents(name), selected: true}
	}

	m := GenModel{
		ctx:       ctx,
		step:      genStepPicker,
		schema:    s,
		graph:     g,
		sortedAll: sortedAll,
		dbType:    dbType,
		picker:    newTablePicker(items, 40),
		genConfig: newGenConfig(defaultRows, format, outPath),
		height:    40,
		width:     80,
	}

	p := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen())
	finalModel, err := p.Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}

	fm := finalModel.(GenModel)
	if fm.quitting {
		return fmt.Errorf("aborted by user")
	}
	if fm.err != nil {
		return fm.err
	}
	return nil
}

func (m GenModel) Init() tea.Cmd { return nil }

func (m GenModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
	case genStepPicker:
		return m.updatePicker(msg)
	case genStepConfig:
		return m.updateGenConfig(msg)
	case genStepRows:
		return m.updateRows(msg)
	case genStepExecute:
		return m.updateExecute(msg)
	}
	return m, nil
}

func (m GenModel) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.picker, cmd = m.picker.Update(msg)

	if m.picker.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.picker.done {
		explicit := m.picker.explicitlySelected()
		resolved, autoSelected := ResolveDeps(m.graph, explicit, m.sortedAll)
		for i := range m.picker.items {
			if autoSelected[m.picker.items[i].name] {
				m.picker.items[i].autoSelected = true
				m.picker.items[i].selected = true
			}
		}
		if len(resolved) == 0 {
			m.picker.done = false
			return m, cmd
		}
		m.step = genStepConfig
	}
	return m, cmd
}

func (m GenModel) updateGenConfig(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.genConfig, cmd = m.genConfig.Update(msg)

	if m.genConfig.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.genConfig.back {
		m.genConfig.back = false
		m.picker.done = false
		m.step = genStepPicker
		return m, nil
	}
	if m.genConfig.done {
		selected := m.picker.selectedTables()
		var tables []string
		for _, t := range m.sortedAll {
			if selected[t] {
				tables = append(tables, t)
			}
		}
		m.volumes = newTableRows(tables, m.genConfig.Rows(), nil, m.height)
		m.step = genStepRows
	}
	return m, cmd
}

func (m GenModel) updateRows(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd
	m.volumes, cmd = m.volumes.Update(msg)

	if m.volumes.quitting {
		m.quitting = true
		return m, tea.Quit
	}
	if m.volumes.back {
		m.volumes.back = false
		m.genConfig.done = false
		m.step = genStepConfig
		return m, nil
	}
	if m.volumes.done {
		m.execute = newExecute(len(m.volumes.tables), false)
		m.execute.dryRun = true // generate is always a "dry run" (no DB)
		m.step = genStepExecute

		return m, tea.Batch(m.execute.spinner.Tick, startGenerate(m.schema, m.volumes.tables, m.genConfig.Rows(), m.volumes.TableRows(), m.genConfig.Format(), m.genConfig.OutPath(), m.dbType))
	}
	return m, cmd
}

func (m GenModel) updateExecute(msg tea.Msg) (tea.Model, tea.Cmd) {
	var cmd tea.Cmd

	switch msg := msg.(type) {
	case generateDoneMsg:
		m.execute.done = true
		if msg.err != nil {
			m.execute.err = msg.err
			m.err = msg.err
		} else {
			m.execute.dryRunTables = msg.tables
			m.execute.dryRunTotal = msg.total
			m.execute.dryRunLines = len(msg.tables) * 3
		}
		return m, nil
	default:
		m.execute, cmd = m.execute.Update(msg)
	}

	return m, cmd
}

func (m GenModel) View() string {
	var sb strings.Builder

	steps := []string{"Tables", "Config", "Volumes", "Generate"}
	var breadcrumbs []string
	for i, name := range steps {
		if genStep(i) == m.step {
			breadcrumbs = append(breadcrumbs, activeStepStyle.Render(fmt.Sprintf("● %s", name)))
		} else if genStep(i) < m.step {
			breadcrumbs = append(breadcrumbs, successStyle.Render(fmt.Sprintf("✓ %s", name)))
		} else {
			breadcrumbs = append(breadcrumbs, stepIndicatorStyle.Render(fmt.Sprintf("○ %s", name)))
		}
	}
	sb.WriteString(headerBorder.Render("  seedstorm generate interactive  " + strings.Join(breadcrumbs, "  ")))
	sb.WriteString("\n")

	switch m.step {
	case genStepPicker:
		sb.WriteString(m.picker.View())
	case genStepConfig:
		sb.WriteString(m.genConfig.View())
	case genStepRows:
		sb.WriteString(m.volumes.View())
	case genStepExecute:
		sb.WriteString(m.execute.View())
	}
	return sb.String()
}

// startGenerate generates data and optionally writes to file.
func startGenerate(s *schema.Schema, tables []string, rows int, tableRows map[string]int, format, outPath, dbType string) tea.Cmd {
	return func() tea.Msg {
		data, err := faker.GenerateFilteredWithCounts(s, tables, tables, rows, 0, tableRows, nil, dbType)
		if err != nil {
			return generateDoneMsg{err: fmt.Errorf("generation failed: %w", err)}
		}

		// Build output
		var output string
		switch strings.ToLower(format) {
		case "json":
			b, err := json.MarshalIndent(data, "", "  ")
			if err != nil {
				return generateDoneMsg{err: fmt.Errorf("JSON marshal failed: %w", err)}
			}
			output = string(b)
		case "sql":
			var sb strings.Builder
			for _, tableName := range tables {
				for _, row := range data[tableName] {
					query, _ := dbpkg.BuildInsert(tableName, row, dbType)
					sb.WriteString(query)
					sb.WriteString(";\n")
				}
			}
			output = sb.String()
		default: // yaml
			b, err := yaml.Marshal(data)
			if err != nil {
				return generateDoneMsg{err: fmt.Errorf("YAML marshal failed: %w", err)}
			}
			output = string(b)
		}

		// Write to file if path given
		if outPath != "" {
			if err := os.WriteFile(outPath, []byte(output), 0o644); err != nil {
				return generateDoneMsg{err: fmt.Errorf("failed to write %s: %w", outPath, err)}
			}
		}

		// Build preview tables
		var previewTables []dryRunTable
		total := 0
		for _, tableName := range tables {
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
			previewTables = append(previewTables, dt)
			total += len(rows)
		}

		return generateDoneMsg{
			output:  output,
			outPath: outPath,
			tables:  previewTables,
			total:   total,
		}
	}
}
