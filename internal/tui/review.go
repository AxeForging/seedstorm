package tui

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

type reviewModel struct {
	tables    []string
	parents   map[string][]string // table -> FK parent names
	rows      int
	enumRows  int
	tableRows map[string]int
	truncate  bool
	batch     int
	done      bool
	dryRun    bool
	back      bool
	quitting  bool
}

func newReview(tables []string, parents map[string][]string, rows, enumRows, batch int, truncate bool, tableRows ...map[string]int) reviewModel {
	overrides := map[string]int(nil)
	if len(tableRows) > 0 {
		overrides = tableRows[0]
	}
	return reviewModel{
		tables:    tables,
		parents:   parents,
		rows:      rows,
		enumRows:  enumRows,
		tableRows: overrides,
		truncate:  truncate,
		batch:     batch,
	}
}

func (m reviewModel) Update(msg tea.Msg) (reviewModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "enter", "y":
			m.done = true
		case "d":
			m.dryRun = true
			m.done = true
		case "b", "backspace":
			m.back = true
		case "q", "esc", "ctrl+c":
			m.quitting = true
		}
	}
	return m, nil
}

func (m reviewModel) View() string {
	var sb strings.Builder

	sb.WriteString(titleStyle.Render("Review seed plan"))
	sb.WriteString("\n\n")

	// Config summary
	sb.WriteString(fmt.Sprintf("  Tables:     %d\n", len(m.tables)))
	sb.WriteString(fmt.Sprintf("  Rows/table: %d\n", m.rows))
	if len(m.tableRows) > 0 {
		sb.WriteString(fmt.Sprintf("  Overrides:  %d table(s)\n", len(m.tableRows)))
	}
	if m.enumRows > 0 {
		sb.WriteString(fmt.Sprintf("  Enum rows:  %d\n", m.enumRows))
	}
	sb.WriteString(fmt.Sprintf("  Batch size: %d\n", m.batch))
	if m.truncate {
		sb.WriteString(errorStyle.Render("  Truncate:   YES — all existing data will be deleted") + "\n")
	}

	sb.WriteString("\n")

	// Table order
	numWidth := len(fmt.Sprintf("%d", len(m.tables)))
	tableWidth := 0
	for _, t := range m.tables {
		if len(t) > tableWidth {
			tableWidth = len(t)
		}
	}

	sb.WriteString(fmt.Sprintf("  %-*s  %-*s  %-8s  %s\n", numWidth, "#", tableWidth, "Table", "Rows", "Dependencies"))
	sb.WriteString(fmt.Sprintf("  %s\n", strings.Repeat("─", numWidth+2+tableWidth+2+8+2+30)))

	for i, table := range m.tables {
		deps := "—"
		if parents, ok := m.parents[table]; ok && len(parents) > 0 {
			deps = strings.Join(parents, ", ")
		}
		rows := m.rows
		if override := m.tableRows[table]; override > 0 {
			rows = override
		}
		sb.WriteString(fmt.Sprintf("  %-*d  %-*s  %-8d  %s\n", numWidth, i+1, tableWidth, table, rows, deps))
	}

	sb.WriteString("\n")
	sb.WriteString(helpStyle.Render("  enter execute • d dry-run • b back • q quit"))

	return sb.String()
}
