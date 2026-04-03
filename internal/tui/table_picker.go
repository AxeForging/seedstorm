package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type tableItem struct {
	name         string
	parents      []string // hard FK parents
	selected     bool
	autoSelected bool // locked — pulled in as a dependency
}

type tablePickerModel struct {
	items    []tableItem
	cursor   int
	height   int // terminal height for scrolling
	offset   int // scroll offset
	done     bool
	quitting bool
}

func newTablePicker(items []tableItem, termHeight int) tablePickerModel {
	return tablePickerModel{
		items:  items,
		height: termHeight,
	}
}

func (m tablePickerModel) Update(msg tea.Msg) (tablePickerModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
				if m.cursor < m.offset {
					m.offset = m.cursor
				}
			}
		case "down", "j":
			if m.cursor < len(m.items)-1 {
				m.cursor++
				visible := m.visibleRows()
				if m.cursor >= m.offset+visible {
					m.offset = m.cursor - visible + 1
				}
			}
		case " ":
			item := &m.items[m.cursor]
			if item.autoSelected {
				// Can't deselect auto-selected dependencies
				break
			}
			item.selected = !item.selected
		case "a":
			// Toggle all: if all selected, deselect all; otherwise select all
			allSelected := true
			for _, item := range m.items {
				if !item.selected && !item.autoSelected {
					allSelected = false
					break
				}
			}
			for i := range m.items {
				m.items[i].selected = !allSelected
				m.items[i].autoSelected = false
			}
		case "n":
			for i := range m.items {
				m.items[i].selected = false
				m.items[i].autoSelected = false
			}
		case "enter":
			m.done = true
		case "q", "esc", "ctrl+c":
			m.quitting = true
		}
	case tea.WindowSizeMsg:
		m.height = msg.Height
	}

	return m, nil
}

func (m tablePickerModel) visibleRows() int {
	h := m.height
	if h < 20 {
		h = 40 // fallback if WindowSizeMsg hasn't arrived
	}
	// Reserve ~8 lines for header/breadcrumb + title + footer/help
	available := h - 8
	if available > len(m.items) {
		available = len(m.items)
	}
	return available
}

func (m tablePickerModel) View() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("Select tables to seed"))
	sb.WriteString("\n\n")

	visible := m.visibleRows()
	end := m.offset + visible
	if end > len(m.items) {
		end = len(m.items)
	}

	for i := m.offset; i < end; i++ {
		item := m.items[i]

		cursor := "  "
		if i == m.cursor {
			cursor = cursorStyle.Render("▸ ")
		}

		checkbox := "[ ]"
		nameRendered := item.name
		if item.autoSelected {
			checkbox = autoSelectedStyle.Render("[●]")
			nameRendered = autoSelectedStyle.Render(item.name)
		} else if item.selected {
			checkbox = selectedStyle.Render("[✓]")
			nameRendered = selectedStyle.Render(item.name)
		}

		deps := ""
		if len(item.parents) > 0 {
			deps = dimStyle.Render(fmt.Sprintf("  → %s", strings.Join(item.parents, ", ")))
		}

		line := fmt.Sprintf("%s%s %s%s", cursor, checkbox, nameRendered, deps)

		if i == m.cursor {
			line = lipgloss.NewStyle().Bold(true).Render(line)
		}

		sb.WriteString(line)
		sb.WriteString("\n")
	}

	// Scroll indicator
	if len(m.items) > visible {
		sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  showing %d-%d of %d tables", m.offset+1, end, len(m.items))))
		sb.WriteString("\n")
	}

	selectedCount := 0
	for _, item := range m.items {
		if item.selected || item.autoSelected {
			selectedCount++
		}
	}
	sb.WriteString(dimStyle.Render(fmt.Sprintf("\n  %d of %d tables selected", selectedCount, len(m.items))))

	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  ↑/↓ navigate • space toggle • a all • n none • enter confirm • q quit"))

	return sb.String()
}

func (m tablePickerModel) selectedTables() map[string]bool {
	result := make(map[string]bool)
	for _, item := range m.items {
		if item.selected || item.autoSelected {
			result[item.name] = true
		}
	}
	return result
}

func (m tablePickerModel) explicitlySelected() map[string]bool {
	result := make(map[string]bool)
	for _, item := range m.items {
		if item.selected && !item.autoSelected {
			result[item.name] = true
		}
	}
	return result
}
