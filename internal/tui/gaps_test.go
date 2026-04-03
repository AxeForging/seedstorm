package tui

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
	tea "github.com/charmbracelet/bubbletea"
)

func buildGapsModel() GapsModel {
	s := makeSchema(map[string]map[string]schema.Column{
		"users":  {"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "name"}},
		"orders": {"id": {Type: "integer", PK: true}, "user_id": {Type: "integer", FK: "users.id"}},
		"items":  {"id": {Type: "integer", PK: true}, "order_id": {Type: "integer", FK: "orders.id"}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	// users has 50 rows (populated), orders and items are empty (gaps)
	counts := map[string]int64{"users": 50, "orders": 0, "items": 0}

	items := make([]tableItem, len(sorted))
	for i, name := range sorted {
		item := tableItem{name: name, parents: g.Parents(name)}
		if counts[name] == 0 {
			item.selected = true
		}
		items[i] = item
	}

	return GapsModel{
		step:      gapsStepPicker,
		schema:    s,
		graph:     g,
		sortedAll: sorted,
		dbType:    "pgx",
		dsn:       "test://unused",
		counts:    counts,
		picker:    newGapsPicker(items, counts, 40),
		config:    newConfig(20, 100, 0, false),
		height:    40,
		width:     80,
	}
}

func sendGapsKey(m tea.Model, key string) tea.Model {
	return sendKey(m, key) // reuse from wizard_test.go
}

func getGaps(m tea.Model) GapsModel {
	return m.(GapsModel)
}

func TestGaps_startsAtPicker(t *testing.T) {
	m := buildGapsModel()
	if m.step != gapsStepPicker {
		t.Fatal("should start at picker")
	}
}

func TestGaps_pickerToConfig(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")
	if getGaps(m2).step != gapsStepConfig {
		t.Fatalf("enter should advance to config, got step %d", getGaps(m2).step)
	}
}

func TestGaps_configToReview(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")  // → config
	m3 := sendGapsKey(m2, "enter") // → review
	if getGaps(m3).step != gapsStepReview {
		t.Fatalf("enter should advance to review, got step %d", getGaps(m3).step)
	}
}

func TestGaps_backFromConfigToPicker(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")
	m3 := sendGapsKey(m2, "b")
	if getGaps(m3).step != gapsStepPicker {
		t.Fatal("b should go back to picker")
	}
}

func TestGaps_backFromReviewToConfig(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")
	m3 := sendGapsKey(m2, "enter")
	m4 := sendGapsKey(m3, "b")
	if getGaps(m4).step != gapsStepConfig {
		t.Fatal("b should go back to config")
	}
}

func TestGaps_quitFromPicker(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "q")
	if !getGaps(m2).quitting {
		t.Error("q should set quitting")
	}
}

func TestGaps_quitFromConfig(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")
	m3 := sendGapsKey(m2, "q")
	if !getGaps(m3).quitting {
		t.Error("q should set quitting")
	}
}

func TestGaps_quitFromReview(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")
	m3 := sendGapsKey(m2, "enter")
	m4 := sendGapsKey(m3, "q")
	if !getGaps(m4).quitting {
		t.Error("q should set quitting")
	}
}

func TestGaps_dryRunFromReview(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")
	m3 := sendGapsKey(m2, "enter")
	m4 := sendGapsKey(m3, "d")
	gm := getGaps(m4)
	if gm.step != gapsStepExecute {
		t.Fatalf("d should advance to execute, got step %d", gm.step)
	}
	if !gm.execute.dryRun {
		t.Error("d should set dryRun")
	}
}

func TestGaps_reviewNeverShowsTruncate(t *testing.T) {
	m := buildGapsModel()
	m2 := sendGapsKey(m, "enter")
	m3 := sendGapsKey(m2, "enter")
	gm := getGaps(m3)
	if gm.review.truncate {
		t.Error("gaps should never set truncate")
	}
}

func TestGaps_viewShowsGapsHeader(t *testing.T) {
	m := buildGapsModel()
	view := m.View()
	if !stringContains(view, "gaps") {
		t.Error("view should contain 'gaps' in header")
	}
}

func TestCleanTableName(t *testing.T) {
	tests := []struct {
		in, want string
	}{
		{"users (50 rows)", "users"},
		{"orders", "orders"},
		{"product_tags (123 rows)", "product_tags"},
		{"no_parens_here", "no_parens_here"},
	}
	for _, tt := range tests {
		got := cleanTableName(tt.in)
		if got != tt.want {
			t.Errorf("cleanTableName(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
