package tui

import (
	"fmt"
	"testing"
	"time"

	"github.com/AxeForging/seedstorm/internal/schema"
	tea "github.com/charmbracelet/bubbletea"
)

// ── helpers ─────────────────────────────────────────────────────────────────

func sendExecKey(m executeModel, key string) executeModel {
	msg := tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	switch key {
	case "up":
		msg = tea.KeyMsg{Type: tea.KeyUp}
	case "down":
		msg = tea.KeyMsg{Type: tea.KeyDown}
	case "ctrl+c":
		msg = tea.KeyMsg{Type: tea.KeyCtrlC}
	}
	updated, _ := m.Update(msg)
	return updated
}

// ── newExecute ───────────────────────────────────────────────────────────────

func TestExecute_initialState(t *testing.T) {
	m := newExecute(5, false)
	if m.totalTables != 5 {
		t.Errorf("totalTables = %d, want 5", m.totalTables)
	}
	if m.dryRun {
		t.Error("dryRun should be false by default")
	}
	if m.done || m.quitting || m.err != nil {
		t.Error("fresh model should not be done, quitting, or errored")
	}
}

func TestExecute_initialState_dryRun(t *testing.T) {
	m := newExecute(3, true)
	if !m.dryRun {
		t.Error("dryRun should be true when requested")
	}
}

// ── Update — messages ────────────────────────────────────────────────────────

func TestExecute_windowSizeMsg(t *testing.T) {
	m := newExecute(1, false)
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 120, Height: 50})
	if updated.height != 50 {
		t.Errorf("height = %d, want 50", updated.height)
	}
}

func TestExecute_tableSeededMsg(t *testing.T) {
	m := newExecute(3, false)
	updated, _ := m.Update(tableSeededMsg{table: "users", rows: 42})
	if updated.currentTable != "users" {
		t.Errorf("currentTable = %q, want 'users'", updated.currentTable)
	}
	if updated.completedTables != 1 {
		t.Errorf("completedTables = %d, want 1", updated.completedTables)
	}
	if updated.seededRows["users"] != 42 {
		t.Errorf("seededRows[users] = %d, want 42", updated.seededRows["users"])
	}
	if len(updated.seededTables) != 1 || updated.seededTables[0] != "users" {
		t.Errorf("seededTables = %v, want [users]", updated.seededTables)
	}
}

func TestExecute_tableSeededMsg_accumulates(t *testing.T) {
	m := newExecute(3, false)
	updated, _ := m.Update(tableSeededMsg{table: "users", rows: 10})
	updated, _ = updated.Update(tableSeededMsg{table: "orders", rows: 20})
	if updated.completedTables != 2 {
		t.Errorf("completedTables = %d, want 2", updated.completedTables)
	}
	if updated.seededRows["orders"] != 20 {
		t.Errorf("seededRows[orders] = %d, want 20", updated.seededRows["orders"])
	}
}

func TestExecute_seedDoneMsg_success(t *testing.T) {
	m := newExecute(2, false)
	msg := seedDoneMsg{
		totalRows: 200,
		elapsed:   500 * time.Millisecond,
		tables:    []string{"users", "orders"},
		rowsMap:   map[string]int{"users": 100, "orders": 100},
	}
	updated, _ := m.Update(msg)
	if !updated.done {
		t.Error("done should be true after seedDoneMsg")
	}
	if updated.totalRows != 200 {
		t.Errorf("totalRows = %d, want 200", updated.totalRows)
	}
	if updated.completedTables != 2 {
		t.Errorf("completedTables = %d, want 2", updated.completedTables)
	}
	if updated.err != nil {
		t.Errorf("err should be nil, got %v", updated.err)
	}
}

func TestExecute_seedDoneMsg_error(t *testing.T) {
	m := newExecute(1, false)
	msg := seedDoneMsg{err: fmt.Errorf("connection refused")}
	updated, _ := m.Update(msg)
	if !updated.done {
		t.Error("done should be true even on error")
	}
	if updated.err == nil {
		t.Error("err should be set")
	}
}

func TestExecute_dryRunDoneMsg_success(t *testing.T) {
	m := newExecute(2, true)
	tables := []dryRunTable{
		{name: "users", rows: 5, columns: []string{"id", "name"}, sample: map[string]interface{}{"id": 1, "name": "Alice"}},
		{name: "orders", rows: 5, columns: []string{"id"}, sample: map[string]interface{}{"id": 1}},
	}
	updated, _ := m.Update(dryRunDoneMsg{tables: tables, total: 10})
	if !updated.done {
		t.Error("done should be true after dryRunDoneMsg")
	}
	if updated.dryRunTotal != 10 {
		t.Errorf("dryRunTotal = %d, want 10", updated.dryRunTotal)
	}
	if len(updated.dryRunTables) != 2 {
		t.Errorf("dryRunTables len = %d, want 2", len(updated.dryRunTables))
	}
}

func TestExecute_dryRunDoneMsg_lineCount(t *testing.T) {
	m := newExecute(3, true)
	tables := make([]dryRunTable, 4)
	for i := range tables {
		tables[i] = dryRunTable{name: fmt.Sprintf("t%d", i), rows: 1}
	}
	updated, _ := m.Update(dryRunDoneMsg{tables: tables, total: 4})
	// Each table = 3 lines (name + sample + blank)
	if updated.dryRunLines != 4*3 {
		t.Errorf("dryRunLines = %d, want %d", updated.dryRunLines, 4*3)
	}
}

func TestExecute_dryRunDoneMsg_error(t *testing.T) {
	m := newExecute(1, true)
	updated, _ := m.Update(dryRunDoneMsg{err: fmt.Errorf("bad schema")})
	if !updated.done {
		t.Error("done should be true even on dry-run error")
	}
	if updated.err == nil {
		t.Error("err should be set")
	}
}

// ── Update — keys ────────────────────────────────────────────────────────────

func TestExecute_qKeyQuitting(t *testing.T) {
	m := newExecute(1, false)
	updated := sendExecKey(m, "q")
	if !updated.quitting {
		t.Error("q should set quitting=true")
	}
}

func TestExecute_ctrlCQuitting(t *testing.T) {
	m := newExecute(1, false)
	updated := sendExecKey(m, "ctrl+c")
	if !updated.quitting {
		t.Error("ctrl+c should set quitting=true")
	}
}

func TestExecute_scrollUp_atZero_stays(t *testing.T) {
	m := newExecute(1, true)
	m.dryRunScroll = 0
	updated := sendExecKey(m, "up")
	if updated.dryRunScroll != 0 {
		t.Errorf("scroll should stay at 0, got %d", updated.dryRunScroll)
	}
}

func TestExecute_scrollUp_decrements(t *testing.T) {
	m := newExecute(1, true)
	m.dryRunScroll = 3
	updated := sendExecKey(m, "up")
	if updated.dryRunScroll != 2 {
		t.Errorf("scroll = %d, want 2", updated.dryRunScroll)
	}
}

func TestExecute_scrollDown_advances(t *testing.T) {
	m := newExecute(1, true)
	m.dryRunLines = 20
	m.height = 40 // dryRunVisible() = 40-10 = 30; maxScroll = 20-30 = -10 → clamped to 0
	// With 20 lines and 30 visible, no room to scroll
	updated := sendExecKey(m, "down")
	if updated.dryRunScroll != 0 {
		t.Errorf("scroll should stay at 0 when content fits, got %d", updated.dryRunScroll)
	}
}

func TestExecute_scrollDown_clamped(t *testing.T) {
	m := newExecute(1, true)
	m.dryRunLines = 50
	m.height = 20 // dryRunVisible() = 20-10 = 10; maxScroll = 50-10 = 40
	m.dryRunScroll = 40
	updated := sendExecKey(m, "down")
	// Already at max
	if updated.dryRunScroll != 40 {
		t.Errorf("scroll should stay clamped at 40, got %d", updated.dryRunScroll)
	}
}

// ── dryRunVisible ────────────────────────────────────────────────────────────

func TestDryRunVisible_normalHeight(t *testing.T) {
	m := newExecute(1, true)
	m.height = 40
	if got := m.dryRunVisible(); got != 30 {
		t.Errorf("dryRunVisible() = %d, want 30", got)
	}
}

func TestDryRunVisible_shortHeight_clampedTo30(t *testing.T) {
	m := newExecute(1, true)
	m.height = 10 // < 20, so h is set to 40 → 40-10 = 30
	if got := m.dryRunVisible(); got != 30 {
		t.Errorf("dryRunVisible() with small height = %d, want 30", got)
	}
}

func TestDryRunVisible_exactlyAtThreshold(t *testing.T) {
	m := newExecute(1, true)
	m.height = 20 // exactly 20 → 20-10 = 10
	if got := m.dryRunVisible(); got != 10 {
		t.Errorf("dryRunVisible() = %d, want 10", got)
	}
}

// ── View ─────────────────────────────────────────────────────────────────────

func TestExecute_view_seedInProgress(t *testing.T) {
	m := newExecute(3, false)
	m.currentTable = "products"
	m.completedTables = 1
	view := m.View()
	if !stringContains(view, "Seeding database") {
		t.Error("in-progress seed view should contain 'Seeding database'")
	}
	if !stringContains(view, "products") {
		t.Error("in-progress view should show current table name")
	}
}

func TestExecute_view_seedDone(t *testing.T) {
	m := newExecute(2, false)
	m.done = true
	m.totalRows = 200
	m.completedTables = 2
	m.elapsed = 350 * time.Millisecond
	m.seededTables = []string{"users", "orders"}
	m.seededRows = map[string]int{"users": 100, "orders": 100}
	view := m.View()
	if !stringContains(view, "complete") {
		t.Error("done view should contain 'complete'")
	}
	if !stringContains(view, "200") {
		t.Error("done view should show total row count")
	}
	if !stringContains(view, "users") || !stringContains(view, "orders") {
		t.Error("done view should list seeded tables")
	}
}

func TestExecute_view_seedError(t *testing.T) {
	m := newExecute(1, false)
	m.done = true
	m.err = fmt.Errorf("insert failed: duplicate key")
	view := m.View()
	if !stringContains(view, "Error") {
		t.Error("error view should contain 'Error'")
	}
	if !stringContains(view, "duplicate key") {
		t.Error("error view should contain error message")
	}
}

func TestExecute_view_dryRunInProgress(t *testing.T) {
	m := newExecute(2, true)
	view := m.View()
	if !stringContains(view, "Dry Run") {
		t.Error("dry-run view should contain 'Dry Run'")
	}
	if !stringContains(view, "Generating") {
		t.Error("in-progress dry-run view should contain 'Generating'")
	}
}

func TestExecute_view_dryRunDone(t *testing.T) {
	m := newExecute(2, true)
	m.done = true
	m.dryRunTables = []dryRunTable{
		{name: "users", rows: 5, columns: []string{"id", "name"}, sample: map[string]interface{}{"id": 1, "name": "Alice"}},
		{name: "orders", rows: 5, columns: []string{"id"}, sample: map[string]interface{}{"id": 1}},
	}
	m.dryRunTotal = 10
	m.dryRunLines = 6
	m.height = 40
	view := m.View()
	if !stringContains(view, "users") {
		t.Error("dry-run done view should list table names")
	}
	if !stringContains(view, "10") {
		t.Error("dry-run done view should show total row count")
	}
}

func TestExecute_view_dryRunError(t *testing.T) {
	m := newExecute(1, true)
	m.done = true
	m.err = fmt.Errorf("generation failed")
	view := m.View()
	if !stringContains(view, "Error") {
		t.Error("dry-run error view should contain 'Error'")
	}
}

func TestExecute_view_dryRunSample_truncatesLongValues(t *testing.T) {
	m := newExecute(1, true)
	m.done = true
	longVal := "this_is_a_very_long_value_that_should_be_truncated_in_the_preview_display"
	m.dryRunTables = []dryRunTable{
		{name: "t", rows: 1, columns: []string{"col"}, sample: map[string]interface{}{"col": longVal}},
	}
	m.dryRunTotal = 1
	m.dryRunLines = 3
	m.height = 40
	view := m.View()
	if stringContains(view, longVal) {
		t.Error("view should truncate long sample values, but showed full value")
	}
	if !stringContains(view, "...") {
		t.Error("truncated value should end with '...'")
	}
}

func TestExecute_view_dryRunSample_nilValue(t *testing.T) {
	m := newExecute(1, true)
	m.done = true
	m.dryRunTables = []dryRunTable{
		{name: "t", rows: 1, columns: []string{"col"}, sample: map[string]interface{}{"col": nil}},
	}
	m.dryRunTotal = 1
	m.dryRunLines = 3
	m.height = 40
	view := m.View()
	if !stringContains(view, "NULL") {
		t.Error("nil sample value should render as NULL")
	}
}

// ── startDryRun ──────────────────────────────────────────────────────────────

func TestStartDryRun_returnsPopulatedMsg(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"users": {
			"id":   {Type: "integer", PK: true},
			"name": {Type: "varchar", Faker: "name"},
		},
	})
	params := &seedParams{
		schema: s,
		tables: []string{"users"},
		rows:   3,
		dbType: "pgx",
	}
	cmd := startDryRun(params)
	msg := cmd()

	dm, ok := msg.(dryRunDoneMsg)
	if !ok {
		t.Fatalf("expected dryRunDoneMsg, got %T", msg)
	}
	if dm.err != nil {
		t.Fatalf("startDryRun returned error: %v", dm.err)
	}
	if len(dm.tables) != 1 {
		t.Fatalf("expected 1 table, got %d", len(dm.tables))
	}
	if dm.tables[0].name != "users" {
		t.Errorf("table name = %q, want 'users'", dm.tables[0].name)
	}
	if dm.tables[0].rows != 3 {
		t.Errorf("rows = %d, want 3", dm.tables[0].rows)
	}
	if dm.total != 3 {
		t.Errorf("total = %d, want 3", dm.total)
	}
}

func TestStartDryRun_populatesSample(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"tags": {
			"id":   {Type: "integer", PK: true},
			"name": {Type: "varchar", Faker: "word"},
		},
	})
	params := &seedParams{schema: s, tables: []string{"tags"}, rows: 5, dbType: "pgx"}
	msg := startDryRun(params)()

	dm := msg.(dryRunDoneMsg)
	if dm.tables[0].sample == nil {
		t.Error("sample should be populated for tables with rows")
	}
	if len(dm.tables[0].columns) == 0 {
		t.Error("columns should be populated")
	}
}

func TestStartDryRun_multipleTables(t *testing.T) {
	s := makeSchema(map[string]map[string]schema.Column{
		"a": {"id": {Type: "integer", PK: true}},
		"b": {"id": {Type: "integer", PK: true}},
		"c": {"id": {Type: "integer", PK: true}},
	})
	params := &seedParams{
		schema: s,
		tables: []string{"a", "b", "c"},
		rows:   2,
		dbType: "pgx",
	}
	msg := startDryRun(params)()
	dm := msg.(dryRunDoneMsg)
	if dm.err != nil {
		t.Fatalf("unexpected error: %v", dm.err)
	}
	if len(dm.tables) != 3 {
		t.Errorf("expected 3 tables, got %d", len(dm.tables))
	}
	if dm.total != 6 {
		t.Errorf("total = %d, want 6", dm.total)
	}
}
