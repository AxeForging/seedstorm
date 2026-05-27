package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func sendRowsKey(m tableRowsModel, key string) tableRowsModel {
	var msg tea.Msg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "backspace":
		msg = tea.KeyMsg{Type: tea.KeyBackspace}
	case "b":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, _ := m.Update(msg)
	return updated
}

func TestTableRowsDefaultsProduceNoOverrides(t *testing.T) {
	m := newTableRows([]string{"users", "orders"}, 10, nil, 40)

	if got := m.TableRows(); got != nil {
		t.Fatalf("TableRows() = %+v, want nil when every table uses default", got)
	}
	if got := m.EffectiveRows("orders"); got != 10 {
		t.Fatalf("EffectiveRows(orders) = %d, want 10", got)
	}
}

func TestTableRowsCapturesOnlyChangedTables(t *testing.T) {
	m := newTableRows([]string{"users", "orders"}, 10, nil, 40)
	m = sendRowsKey(m, "down")
	m.inputs[m.cursor].SetValue("25")

	got := m.TableRows()
	if len(got) != 1 || got["orders"] != 25 {
		t.Fatalf("TableRows() = %+v, want only orders=25", got)
	}
	if got := m.EffectiveRows("orders"); got != 25 {
		t.Fatalf("EffectiveRows(orders) = %d, want 25", got)
	}
}

func TestTableRowsInvalidInputFallsBackToDefault(t *testing.T) {
	m := newTableRows([]string{"users"}, 10, nil, 40)
	m.inputs[m.cursor].SetValue("0")

	if got := m.TableRows(); got != nil {
		t.Fatalf("TableRows() = %+v, want nil for invalid override", got)
	}
	if got := m.EffectiveRows("users"); got != 10 {
		t.Fatalf("EffectiveRows(users) = %d, want fallback 10", got)
	}
}
