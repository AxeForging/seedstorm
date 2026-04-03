package tui

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
	tea "github.com/charmbracelet/bubbletea"
)

// buildTestModel creates a full wizard Model with a small 3-table schema:
// users (root) → orders (FK users) → order_items (FK orders)
func buildTestModel() Model {
	s := makeSchema(map[string]map[string]schema.Column{
		"users":       {"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "name"}},
		"orders":      {"id": {Type: "integer", PK: true}, "user_id": {Type: "integer", FK: "users.id"}, "total": {Type: "numeric", Faker: "price(1,100)"}},
		"order_items": {"id": {Type: "integer", PK: true}, "order_id": {Type: "integer", FK: "orders.id"}, "qty": {Type: "integer", Faker: "number(1,10)"}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	items := make([]tableItem, len(sorted))
	for i, name := range sorted {
		items[i] = tableItem{name: name, parents: g.Parents(name), selected: true}
	}

	return Model{
		step:      stepPicker,
		schema:    s,
		graph:     g,
		sortedAll: sorted,
		dbType:    "pgx",
		dsn:       "test://unused",
		picker:    newTablePicker(items, 40),
		config:    newConfig(10, 50, 0, false),
		height:    40,
		width:     80,
	}
}

func sendKey(m tea.Model, key string) tea.Model {
	var msg tea.Msg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "space":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{' '}}
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "q":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'q'}}
	case "b":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'b'}}
	case "d":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'d'}}
	case "n":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'n'}}
	case "a":
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'a'}}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	updated, _ := m.Update(msg)
	return updated
}

func getModel(m tea.Model) Model {
	return m.(Model)
}

// ── Full wizard flow tests ──────────────────────────────────────────────────

func TestWizard_fullFlowToReview(t *testing.T) {
	m := buildTestModel()

	// Step 1: Picker — all selected, press enter to advance
	if getModel(tea.Model(m)).step != stepPicker {
		t.Fatal("should start at picker step")
	}
	m2 := sendKey(m, "enter")
	if getModel(m2).step != stepConfig {
		t.Fatalf("enter on picker should advance to config, got step %d", getModel(m2).step)
	}

	// Step 2: Config — press enter to advance
	m3 := sendKey(m2, "enter")
	if getModel(m3).step != stepReview {
		t.Fatalf("enter on config should advance to review, got step %d", getModel(m3).step)
	}

	// Verify review has all 3 tables
	rm := getModel(m3)
	if len(rm.review.tables) != 3 {
		t.Errorf("review should have 3 tables, got %d", len(rm.review.tables))
	}
}

func TestWizard_backFromConfigToPicker(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "enter") // picker → config
	m3 := sendKey(m2, "b")    // config → back to picker
	if getModel(m3).step != stepPicker {
		t.Fatalf("b on config should go back to picker, got step %d", getModel(m3).step)
	}
}

func TestWizard_backFromReviewToConfig(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "enter")  // picker → config
	m3 := sendKey(m2, "enter") // config → review
	m4 := sendKey(m3, "b")     // review → back to config
	if getModel(m4).step != stepConfig {
		t.Fatalf("b on review should go back to config, got step %d", getModel(m4).step)
	}
}

func TestWizard_quitFromPicker(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "q")
	if !getModel(m2).quitting {
		t.Error("q on picker should set quitting=true")
	}
}

func TestWizard_quitFromConfig(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "enter") // → config
	m3 := sendKey(m2, "q")
	if !getModel(m3).quitting {
		t.Error("q on config should set quitting=true")
	}
}

func TestWizard_quitFromReview(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "enter")  // → config
	m3 := sendKey(m2, "enter") // → review
	m4 := sendKey(m3, "q")
	if !getModel(m4).quitting {
		t.Error("q on review should set quitting=true")
	}
}

func TestWizard_deselectLeafReducesReviewCount(t *testing.T) {
	m := buildTestModel()
	// Navigate to last table (leaf = order_items, index 2) and deselect it
	m2 := sendKey(m, "down")   // cursor=1
	m3 := sendKey(m2, "down")  // cursor=2 (order_items)
	m4 := sendKey(m3, "space") // toggle off order_items
	m5 := sendKey(m4, "enter") // → config
	m6 := sendKey(m5, "enter") // → review
	rm := getModel(m6)
	if len(rm.review.tables) != 2 {
		t.Errorf("deselecting leaf should leave 2 tables, got %d: %v", len(rm.review.tables), rm.review.tables)
	}
}

func TestWizard_selectNoneThenEnterStaysOnPicker(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "n")      // deselect all
	m3 := sendKey(m2, "enter") // should NOT advance (nothing selected)
	if getModel(m3).step != stepPicker {
		t.Fatal("enter with no selection should stay on picker")
	}
}

func TestWizard_autoDepResolutionOnAdvance(t *testing.T) {
	m := buildTestModel()
	// Deselect all, then select only order_items (the leaf)
	m2 := sendKey(m, "n") // none
	// Navigate to order_items and select it
	// Tables are topo-sorted: users, orders, order_items — so order_items is at index 2
	m3 := sendKey(m2, "down")
	m4 := sendKey(m3, "down")
	m5 := sendKey(m4, "space") // select order_items
	m6 := sendKey(m5, "enter") // advance — should auto-select users + orders

	rm := getModel(m6)
	if rm.step != stepConfig {
		t.Fatalf("should advance to config, got step %d", rm.step)
	}
	// Check that auto-dependency pulled in parents
	autoFound := false
	for _, item := range rm.picker.items {
		if item.autoSelected {
			autoFound = true
		}
	}
	if !autoFound {
		t.Error("advancing with only leaf selected should auto-select parent tables")
	}
}

func TestWizard_reviewShowsCorrectRowCount(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "enter")  // → config (rows=10 from buildTestModel)
	m3 := sendKey(m2, "enter") // → review
	rm := getModel(m3)
	if rm.review.rows != 10 {
		t.Errorf("review should show 10 rows (from config default), got %d", rm.review.rows)
	}
}

func TestWizard_dryRunFromReview(t *testing.T) {
	m := buildTestModel()
	m2 := sendKey(m, "enter")  // → config
	m3 := sendKey(m2, "enter") // → review
	m4 := sendKey(m3, "d")     // dry-run

	rm := getModel(m4)
	if rm.step != stepExecute {
		t.Fatalf("d on review should advance to execute, got step %d", rm.step)
	}
	if !rm.execute.dryRun {
		t.Error("d should set dryRun=true on execute model")
	}
}

func TestWizard_viewRendersAllSteps(t *testing.T) {
	m := buildTestModel()

	// Picker view should contain "Select tables"
	view := m.View()
	if !contains(view, "Select tables") {
		t.Error("picker view should contain 'Select tables'")
	}

	// Config view should contain "Configure"
	m2 := sendKey(m, "enter")
	view2 := getModel(m2).View()
	if !contains(view2, "Configure") {
		t.Error("config view should contain 'Configure'")
	}

	// Review view should contain "Review"
	m3 := sendKey(m2, "enter")
	view3 := getModel(m3).View()
	if !contains(view3, "Review") {
		t.Error("review view should contain 'Review'")
	}
}

func TestWizard_breadcrumbShowsProgress(t *testing.T) {
	m := buildTestModel()
	view := m.View()
	if !contains(view, "Tables") || !contains(view, "Config") || !contains(view, "Review") || !contains(view, "Execute") {
		t.Error("breadcrumb should show all 4 step names")
	}
}

func contains(s, substr string) bool {
	return len(s) > 0 && len(substr) > 0 && stringContains(s, substr)
}

func stringContains(s, sub string) bool {
	for i := 0; i <= len(s)-len(sub); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
