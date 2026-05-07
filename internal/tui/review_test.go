package tui

import (
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func buildReview(tables []string, truncate bool) reviewModel {
	parents := map[string][]string{
		"orders":      {"users"},
		"order_items": {"orders"},
	}
	return newReview(tables, parents, 50, 0, 100, truncate)
}

// ── View ─────────────────────────────────────────────────────────────────────

func TestReview_view_showsTableCount(t *testing.T) {
	m := buildReview([]string{"users", "orders", "order_items"}, false)
	view := m.View()
	if !stringContains(view, "3") {
		t.Error("view should show table count '3'")
	}
}

func TestReview_view_showsRowsPerTable(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	view := m.View()
	if !stringContains(view, "50") {
		t.Error("view should show rows-per-table '50'")
	}
}

func TestReview_view_showsBatchSize(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	view := m.View()
	if !stringContains(view, "100") {
		t.Error("view should show batch size '100'")
	}
}

func TestReview_view_showsAllTableNames(t *testing.T) {
	tables := []string{"users", "orders", "order_items"}
	m := buildReview(tables, false)
	view := m.View()
	for _, name := range tables {
		if !stringContains(view, name) {
			t.Errorf("view should contain table name %q", name)
		}
	}
}

func TestReview_view_showsDepsForChild(t *testing.T) {
	m := buildReview([]string{"users", "orders"}, false)
	view := m.View()
	if !stringContains(view, "users") {
		t.Error("view should show 'users' as dependency for orders")
	}
}

func TestReview_view_rootShowsDash(t *testing.T) {
	m := newReview([]string{"users"}, map[string][]string{}, 10, 0, 50, false)
	view := m.View()
	if !stringContains(view, "—") {
		t.Error("root table with no deps should show '—' in deps column")
	}
}

func TestReview_view_truncateWarning_shown(t *testing.T) {
	m := buildReview([]string{"users"}, true)
	view := m.View()
	if !stringContains(view, "Truncate") {
		t.Error("view should show truncate warning when truncate=true")
	}
}

func TestReview_view_truncateWarning_hidden(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	view := m.View()
	if stringContains(view, "Truncate") {
		t.Error("view should not show truncate warning when truncate=false")
	}
}

func TestReview_view_showsHeader(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	view := m.View()
	if !stringContains(view, "Review") {
		t.Error("view should contain 'Review' header")
	}
}

func TestReview_view_showsKeyBindings(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	view := m.View()
	if !stringContains(view, "enter") {
		t.Error("view should show key binding hints including 'enter'")
	}
}

func TestReview_view_enumRowsShown_whenNonZero(t *testing.T) {
	m := newReview([]string{"users"}, map[string][]string{}, 10, 5, 50, false)
	view := m.View()
	if !stringContains(view, "Enum") {
		t.Error("view should show enum rows row when enumRows > 0")
	}
}

func TestReview_view_enumRowsHidden_whenZero(t *testing.T) {
	m := newReview([]string{"users"}, map[string][]string{}, 10, 0, 50, false)
	view := m.View()
	if stringContains(view, "Enum") {
		t.Error("view should not show enum rows when enumRows = 0")
	}
}

// ── Update ───────────────────────────────────────────────────────────────────

func TestReview_update_enterSetsDone(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	updated, _ := m.Update(keyMsg("enter"))
	if !updated.done {
		t.Error("enter should set done=true")
	}
	if updated.dryRun {
		t.Error("enter should not set dryRun")
	}
}

func TestReview_update_dSetsDryRun(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	updated, _ := m.Update(keyMsg("d"))
	if !updated.done {
		t.Error("d should set done=true")
	}
	if !updated.dryRun {
		t.Error("d should set dryRun=true")
	}
}

func TestReview_update_bSetsBack(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	updated, _ := m.Update(keyMsg("b"))
	if !updated.back {
		t.Error("b should set back=true")
	}
}

func TestReview_update_qSetsQuitting(t *testing.T) {
	m := buildReview([]string{"users"}, false)
	updated, _ := m.Update(keyMsg("q"))
	if !updated.quitting {
		t.Error("q should set quitting=true")
	}
}

func keyMsg(key string) tea.Msg {
	switch key {
	case "enter":
		return tea.KeyMsg{Type: tea.KeyEnter}
	default:
		return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
}
