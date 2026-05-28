package tui

import (
	"context"
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AxeForging/seedstorm/internal/db"
)

type cloneModel struct {
	ctx        context.Context
	sourceType string
	sourceDSN  string
	targetType string
	targetDSN  string
	opts       db.CloneOptions
	running    bool
	done       bool
	confirmed  bool
	result     db.CloneResult
	err        error
}

type cloneDoneMsg struct {
	result db.CloneResult
	err    error
}

// RunClone presents a small confirmation UI around schema cloning.
func RunClone(ctx context.Context, sourceType, sourceDSN, targetType, targetDSN string, opts db.CloneOptions) error {
	m := cloneModel{
		ctx:        ctx,
		sourceType: sourceType,
		sourceDSN:  sourceDSN,
		targetType: targetType,
		targetDSN:  targetDSN,
		opts:       opts,
	}
	finalModel, err := tea.NewProgram(m, tea.WithContext(ctx)).Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	fm := finalModel.(cloneModel)
	if fm.err != nil {
		return fm.err
	}
	if !fm.confirmed {
		return fmt.Errorf("aborted by user")
	}
	return nil
}

func (m cloneModel) Init() tea.Cmd { return nil }

func (m cloneModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "ctrl+c", "esc", "q", "n":
			return m, tea.Quit
		case "y", "enter":
			if m.running || m.done {
				return m, nil
			}
			m.confirmed = true
			m.running = true
			return m, m.run()
		}
	case cloneDoneMsg:
		m.running = false
		m.done = true
		m.result = msg.result
		m.err = msg.err
		return m, tea.Quit
	}
	return m, nil
}

func (m cloneModel) View() string {
	var sb strings.Builder
	sb.WriteString(titleStyle.Render("Clone schema"))
	sb.WriteString("\n\n")
	sb.WriteString(fmt.Sprintf("  Source: %s\n", m.sourceType))
	sb.WriteString(fmt.Sprintf("  Target: %s\n", m.targetType))
	if m.opts.DropExisting {
		sb.WriteString(errorStyle.Render("  Target tables will be dropped first.\n"))
	} else {
		sb.WriteString("  Target must be empty.\n")
	}
	if m.opts.DryRun {
		sb.WriteString("  Dry-run: DDL will be printed by the command.\n")
	}
	sb.WriteString("\n")
	if m.running {
		sb.WriteString("  Cloning schema...\n")
		return sb.String()
	}
	if m.done {
		if m.err != nil {
			sb.WriteString(errorStyle.Render(fmt.Sprintf("  Error: %v\n", m.err)))
		} else {
			sb.WriteString(successStyle.Render(fmt.Sprintf("  Complete: %d tables, %d statements\n", m.result.Tables, len(m.result.Statements))))
		}
		return sb.String()
	}
	sb.WriteString(helpStyle.Render("  y/enter confirm • n/q/esc abort"))
	return sb.String()
}

func (m cloneModel) run() tea.Cmd {
	return func() tea.Msg {
		result, err := db.CloneSchema(m.ctx, m.sourceType, m.sourceDSN, m.targetType, m.targetDSN, m.opts)
		if m.opts.DryRun && err == nil {
			fmt.Println(strings.Join(result.Statements, ";\n") + ";")
		}
		return cloneDoneMsg{result: result, err: err}
	}
}
