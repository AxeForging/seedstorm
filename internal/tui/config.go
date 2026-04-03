package tui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

type configModel struct {
	fields   []configField
	focused  int
	done     bool
	back     bool
	quitting bool
}

type configField struct {
	label    string
	input    textinput.Model
	isToggle bool
	toggled  bool
}

func newConfig(rows, batchSize, enumRows int, truncate bool) configModel {
	fields := []configField{
		makeNumericField("Rows per table", rows),
		makeNumericField("Batch size", batchSize),
		makeNumericField("Enum rows (0 = use rows)", enumRows),
		{label: "Truncate before seeding", isToggle: true, toggled: truncate},
	}
	fields[0].input.Focus()
	return configModel{fields: fields}
}

func makeNumericField(label string, value int) configField {
	ti := textinput.New()
	ti.SetValue(strconv.Itoa(value))
	ti.CharLimit = 10
	ti.Width = 12
	return configField{label: label, input: ti}
}

func (m configModel) Rows() int { return m.intVal(0, 100) }
func (m configModel) BatchSize() int {
	v := m.intVal(1, 100)
	if v < 1 {
		return 1 // prevent infinite loop in batch insert
	}
	return v
}
func (m configModel) EnumRows() int  { return m.intVal(2, 0) }
func (m configModel) Truncate() bool { return m.fields[3].toggled }

func (m configModel) intVal(idx, fallback int) int {
	if idx >= len(m.fields) {
		return fallback
	}
	v, err := strconv.Atoi(strings.TrimSpace(m.fields[idx].input.Value()))
	if err != nil || v < 0 {
		return fallback
	}
	return v
}

func (m configModel) Update(msg tea.Msg) (configModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "tab", "down", "j":
			m.fields[m.focused].input.Blur()
			m.focused = (m.focused + 1) % len(m.fields)
			if !m.fields[m.focused].isToggle {
				m.fields[m.focused].input.Focus()
			}
			return m, nil
		case "shift+tab", "up", "k":
			m.fields[m.focused].input.Blur()
			m.focused = (m.focused - 1 + len(m.fields)) % len(m.fields)
			if !m.fields[m.focused].isToggle {
				m.fields[m.focused].input.Focus()
			}
			return m, nil
		case " ":
			if m.fields[m.focused].isToggle {
				m.fields[m.focused].toggled = !m.fields[m.focused].toggled
				return m, nil
			}
		case "enter":
			m.done = true
			return m, nil
		case "backspace":
			if m.fields[m.focused].isToggle {
				m.back = true
				return m, nil
			}
		case "b":
			m.back = true
			return m, nil
		case "q", "esc", "ctrl+c":
			m.quitting = true
			return m, nil
		}
	}

	// Update the focused text input
	if m.focused < len(m.fields) && !m.fields[m.focused].isToggle {
		var cmd tea.Cmd
		m.fields[m.focused].input, cmd = m.fields[m.focused].input.Update(msg)
		return m, cmd
	}

	return m, nil
}

func (m configModel) View() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("Configure seeding options"))
	sb.WriteString("\n\n")

	for i, f := range m.fields {
		cursor := "  "
		if i == m.focused {
			cursor = cursorStyle.Render("▸ ")
		}

		if f.isToggle {
			toggle := "[ ]"
			if f.toggled {
				toggle = selectedStyle.Render("[✓]")
			}
			label := f.label
			if i == m.focused {
				label = lipgloss.NewStyle().Bold(true).Render(label)
			}
			sb.WriteString(fmt.Sprintf("%s%s %s\n", cursor, toggle, label))
		} else {
			label := f.label
			if i == m.focused {
				label = lipgloss.NewStyle().Bold(true).Render(label)
			}
			sb.WriteString(fmt.Sprintf("%s%s: %s\n", cursor, label, f.input.View()))
		}
	}

	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  tab/↑↓ navigate • space toggle • enter confirm • b back • q quit"))

	return sb.String()
}
