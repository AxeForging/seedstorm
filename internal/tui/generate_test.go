package tui

import (
	"testing"

	"github.com/AxeForging/seedstorm/internal/graph"
	"github.com/AxeForging/seedstorm/internal/schema"
	tea "github.com/charmbracelet/bubbletea"
)

func buildGenModel() GenModel {
	s := makeSchema(map[string]map[string]schema.Column{
		"users":    {"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "name"}},
		"products": {"id": {Type: "integer", PK: true}, "name": {Type: "varchar", Faker: "productname"}},
	})
	g := graph.Build(s)
	sorted, _ := g.TopologicalSort()

	items := make([]tableItem, len(sorted))
	for i, name := range sorted {
		items[i] = tableItem{name: name, parents: g.Parents(name), selected: true}
	}

	return GenModel{
		step:      genStepPicker,
		schema:    s,
		graph:     g,
		sortedAll: sorted,
		dbType:    "pgx",
		picker:    newTablePicker(items, 40),
		genConfig: newGenConfig(10, "yaml", ""),
		height:    40,
		width:     80,
	}
}

func sendGenKey(m tea.Model, key string) tea.Model {
	return sendKey(m, key)
}

func getGen(m tea.Model) GenModel {
	return m.(GenModel)
}

func TestGen_startsAtPicker(t *testing.T) {
	m := buildGenModel()
	if m.step != genStepPicker {
		t.Fatal("should start at picker")
	}
}

func TestGen_pickerToConfig(t *testing.T) {
	m := buildGenModel()
	m2 := sendGenKey(m, "enter")
	if getGen(m2).step != genStepConfig {
		t.Fatalf("enter should advance to config, got step %d", getGen(m2).step)
	}
}

func TestGen_configToExecute(t *testing.T) {
	m := buildGenModel()
	m2 := sendGenKey(m, "enter")  // → config
	m3 := sendGenKey(m2, "enter") // → execute
	if getGen(m3).step != genStepExecute {
		t.Fatalf("enter on config should advance to execute, got step %d", getGen(m3).step)
	}
}

func TestGen_backFromConfigToPicker(t *testing.T) {
	m := buildGenModel()
	m2 := sendGenKey(m, "enter")
	// Use esc to go back (b is captured by text input when rows field is focused)
	m3 := sendGenKey(m2, "esc")
	if getGen(m3).step != genStepPicker {
		t.Fatal("esc should go back to picker")
	}
}

func TestGen_quitFromPicker(t *testing.T) {
	m := buildGenModel()
	m2 := sendGenKey(m, "q")
	if !getGen(m2).quitting {
		t.Error("q should set quitting")
	}
}

func TestGen_quitFromConfig(t *testing.T) {
	m := buildGenModel()
	m2 := sendGenKey(m, "enter")
	m3 := sendGenKey(m2, "q")
	if !getGen(m3).quitting {
		t.Error("q should set quitting")
	}
}

func TestGen_selectNoneThenEnterStays(t *testing.T) {
	m := buildGenModel()
	m2 := sendGenKey(m, "n")      // deselect all
	m3 := sendGenKey(m2, "enter") // should stay
	if getGen(m3).step != genStepPicker {
		t.Fatal("empty selection should stay on picker")
	}
}

func TestGen_viewShowsGenerateHeader(t *testing.T) {
	m := buildGenModel()
	view := m.View()
	if !stringContains(view, "generate") {
		t.Error("view should contain 'generate' in header")
	}
}

func TestGen_viewShowsAllStepNames(t *testing.T) {
	m := buildGenModel()
	view := m.View()
	if !stringContains(view, "Tables") || !stringContains(view, "Config") || !stringContains(view, "Generate") {
		t.Error("breadcrumb should show all 3 step names")
	}
}

// ── genConfigModel tests ────────────────────────────────────────────────────

func TestGenConfig_defaultValues(t *testing.T) {
	c := newGenConfig(10, "json", "/tmp/out.json")
	if c.Rows() != 10 {
		t.Errorf("Rows() = %d, want 10", c.Rows())
	}
	if c.Format() != "json" {
		t.Errorf("Format() = %q, want json", c.Format())
	}
	if c.OutPath() != "/tmp/out.json" {
		t.Errorf("OutPath() = %q, want /tmp/out.json", c.OutPath())
	}
}

func TestGenConfig_formatCycles(t *testing.T) {
	c := newGenConfig(10, "yaml", "")
	if c.Format() != "yaml" {
		t.Fatal("should start at yaml")
	}
	// Cycle forward
	c.formatIdx = (c.formatIdx + 1) % len(genFormats)
	if c.Format() != "json" {
		t.Errorf("expected json, got %s", c.Format())
	}
	c.formatIdx = (c.formatIdx + 1) % len(genFormats)
	if c.Format() != "sql" {
		t.Errorf("expected sql, got %s", c.Format())
	}
	c.formatIdx = (c.formatIdx + 1) % len(genFormats)
	if c.Format() != "yaml" {
		t.Errorf("expected yaml (wrapped), got %s", c.Format())
	}
}

func TestGenConfig_invalidRowsFallback(t *testing.T) {
	c := newGenConfig(10, "yaml", "")
	c.rowsInput.SetValue("abc")
	if c.Rows() != 10 {
		t.Errorf("invalid rows should fallback to 10, got %d", c.Rows())
	}
}

func TestGenConfig_emptyOutPathMeansStdout(t *testing.T) {
	c := newGenConfig(10, "yaml", "")
	if c.OutPath() != "" {
		t.Errorf("empty out should mean stdout, got %q", c.OutPath())
	}
}
