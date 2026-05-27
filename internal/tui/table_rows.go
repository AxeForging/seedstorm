package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tableRowsModel struct {
	tables      []string
	defaultRows int
	inputs      []textinput.Model
	cursor      int
	done        bool
	back        bool
	quitting    bool
	height      int
	offset      int
}

func newTableRows(tables []string, defaultRows int, existing map[string]int, height int) tableRowsModel {
	inputs := make([]textinput.Model, len(tables))
	for i, tableName := range tables {
		ti := textinput.New()
		ti.CharLimit = 10
		ti.Width = 10
		value := defaultRows
		if existing != nil && existing[tableName] > 0 {
			value = existing[tableName]
		}
		ti.SetValue(strconv.Itoa(value))
		if i == 0 {
			ti.Focus()
		}
		inputs[i] = ti
	}
	return tableRowsModel{
		tables:      tables,
		defaultRows: defaultRows,
		inputs:      inputs,
		height:      height,
	}
}

func (m tableRowsModel) Update(msg tea.Msg) (tableRowsModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "down", "j":
			m.move(1)
			return m, nil
		case "shift+tab", "up", "k":
			m.move(-1)
			return m, nil
		case "enter":
			m.done = true
			return m, nil
		case "b", "esc":
			m.back = true
			return m, nil
		case "q", "ctrl+c":
			m.quitting = true
			return m, nil
		}
	case tea.WindowSizeMsg:
		m.height = msg.Height
	}

	if len(m.inputs) == 0 || m.cursor >= len(m.inputs) {
		return m, nil
	}
	var cmd tea.Cmd
	m.inputs[m.cursor], cmd = m.inputs[m.cursor].Update(msg)
	return m, cmd
}

func (m *tableRowsModel) move(delta int) {
	if len(m.inputs) == 0 {
		return
	}
	m.inputs[m.cursor].Blur()
	m.cursor = (m.cursor + delta + len(m.inputs)) % len(m.inputs)
	m.inputs[m.cursor].Focus()
	visible := m.visibleRows()
	if m.cursor < m.offset {
		m.offset = m.cursor
	}
	if m.cursor >= m.offset+visible {
		m.offset = m.cursor - visible + 1
	}
}

func (m tableRowsModel) visibleRows() int {
	h := m.height
	if h < 20 {
		h = 40
	}
	available := h - 8
	if available < 1 {
		available = 1
	}
	if available > len(m.tables) {
		return len(m.tables)
	}
	return available
}

func (m tableRowsModel) TableRows() map[string]int {
	out := make(map[string]int)
	for i, tableName := range m.tables {
		n, err := strconv.Atoi(strings.TrimSpace(m.inputs[i].Value()))
		if err != nil || n < 1 {
			n = m.defaultRows
		}
		if n != m.defaultRows {
			out[tableName] = n
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func (m tableRowsModel) EffectiveRows(tableName string) int {
	for i, t := range m.tables {
		if t != tableName {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(m.inputs[i].Value()))
		if err != nil || n < 1 {
			return m.defaultRows
		}
		return n
	}
	return m.defaultRows
}

func (m tableRowsModel) View() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Set table volumes"))
	sb.WriteString("\n\n")
	sb.WriteString(dimStyle.Render(fmt.Sprintf("  Default rows/table: %d. Edit only the tables that need a different volume.", m.defaultRows)))
	sb.WriteString("\n\n")

	visible := m.visibleRows()
	end := m.offset + visible
	if end > len(m.tables) {
		end = len(m.tables)
	}
	nameWidth := 0
	for _, tableName := range m.tables {
		if len(tableName) > nameWidth {
			nameWidth = len(tableName)
		}
	}

	for i := m.offset; i < end; i++ {
		cursor := "  "
		name := m.tables[i]
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
			name = lipgloss.NewStyle().Bold(true).Render(name)
		}
		sb.WriteString(fmt.Sprintf("%s%-*s  %s rows\n", cursor, nameWidth, name, m.inputs[i].View()))
	}

	if len(m.tables) > visible {
		sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  showing %d-%d of %d tables", m.offset+1, end, len(m.tables))))
		sb.WriteString("\n")
	}
	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  tab/↑↓ navigate • type row count • enter confirm • b back • q quit"))
	return sb.String()
}
