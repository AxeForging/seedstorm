package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func makeItems(names ...string) []tableItem {
	items := make([]tableItem, len(names))
	for i, name := range names {
		items[i] = tableItem{name: name, selected: true}
	}
	return items
}

func TestTablePicker_spaceTogglesSelection(t *testing.T) {
	m := newTablePicker(makeItems("users", "orders", "products"), 40)
	// All start selected; toggle first one off
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if m.items[0].selected {
		t.Error("space should deselect the current item")
	}
	// Toggle it back on
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if !m.items[0].selected {
		t.Error("space should re-select the current item")
	}
}

func TestTablePicker_cannotDeselectAutoSelected(t *testing.T) {
	items := makeItems("parent", "child")
	items[0].autoSelected = true // locked dependency
	m := newTablePicker(items, 40)
	m.cursor = 0
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}})
	if !m.items[0].selected {
		t.Error("auto-selected items should not be togglable")
	}
}

func TestTablePicker_selectAll(t *testing.T) {
	items := makeItems("a", "b", "c")
	items[0].selected = false
	items[1].selected = false
	m := newTablePicker(items, 40)

	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}})
	for _, item := range m.items {
		if !item.selected {
			t.Errorf("'a' should select all, but %s is deselected", item.name)
		}
	}
}

func TestTablePicker_selectNone(t *testing.T) {
	m := newTablePicker(makeItems("a", "b", "c"), 40)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}})
	for _, item := range m.items {
		if item.selected {
			t.Errorf("'n' should deselect all, but %s is selected", item.name)
		}
	}
}

func TestTablePicker_cursorNavigation(t *testing.T) {
	m := newTablePicker(makeItems("a", "b", "c"), 40)
	if m.cursor != 0 {
		t.Fatal("cursor should start at 0")
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.cursor)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 2 {
		t.Errorf("cursor should be 2, got %d", m.cursor)
	}
	// Should not go past last item
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 2 {
		t.Errorf("cursor should stay at 2, got %d", m.cursor)
	}
	// Go back up
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.cursor != 1 {
		t.Errorf("cursor should be 1, got %d", m.cursor)
	}
}

func TestTablePicker_enterSetsDone(t *testing.T) {
	m := newTablePicker(makeItems("a"), 40)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.done {
		t.Error("enter should set done=true")
	}
}

func TestTablePicker_qSetsQuitting(t *testing.T) {
	m := newTablePicker(makeItems("a"), 40)
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}})
	if !m.quitting {
		t.Error("q should set quitting=true")
	}
}

func TestTablePicker_selectedTables(t *testing.T) {
	items := makeItems("a", "b", "c")
	items[1].selected = false
	m := newTablePicker(items, 40)
	sel := m.selectedTables()
	if sel["b"] {
		t.Error("b should not be selected")
	}
	if !sel["a"] || !sel["c"] {
		t.Error("a and c should be selected")
	}
}

func TestTablePicker_explicitlySelected_excludesAuto(t *testing.T) {
	items := makeItems("parent", "child")
	items[0].autoSelected = true
	m := newTablePicker(items, 40)
	explicit := m.explicitlySelected()
	if explicit["parent"] {
		t.Error("auto-selected items should not appear in explicitlySelected")
	}
	if !explicit["child"] {
		t.Error("child should be in explicitlySelected")
	}
}
