package tui

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/AxeForging/seedstorm/internal/db"
	"github.com/AxeForging/seedstorm/internal/safego"
)

type cloneModel struct {
	ctx        context.Context
	sourceType string
	sourceDSN  string
	targetType string
	targetDSN  string
	opts       db.CloneOptions
	running    bool
	spinner    spinner.Model
	startedAt  time.Time
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
	sp := spinner.New()
	sp.Spinner = spinner.Dot
	sp.Style = selectedStyle
	m := cloneModel{
		spinner:    sp,
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
	// Printed after the screen is released, so it stays in the terminal.
	if fm.opts.DryRun {
		fmt.Println(strings.Join(fm.result.Statements, ";\n") + ";")
	} else {
		fmt.Printf("Cloned %d tables (%d statements).\n", fm.result.Tables, len(fm.result.Statements))
	}
	for _, skipped := range fm.result.Skipped {
		fmt.Printf("Not cloned: %s %s (%s)\n", skipped.Kind, skipped.Name, skipped.Reason)
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
			m.startedAt = time.Now()
			return m, tea.Batch(m.spinner.Tick, m.run())
		}
	case spinner.TickMsg:
		if !m.running {
			return m, nil
		}
		var cmd tea.Cmd
		m.spinner, cmd = m.spinner.Update(msg)
		return m, cmd
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
	fmt.Fprintf(&sb, "  Source: %s\n", m.sourceType)
	fmt.Fprintf(&sb, "  Target: %s\n", m.targetType)
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
		fmt.Fprintf(&sb, "  %s Cloning schema: reading the source, then running DDL on the target · %s\n", m.spinner.View(), time.Since(m.startedAt).Round(100*time.Millisecond))
		sb.WriteString(helpStyle.Render("  ctrl+c stops (statements already run stay)"))
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
		var result db.CloneResult
		err := safego.Run("clone schema", func() (err error) {
			result, err = db.CloneSchema(m.ctx, m.sourceType, m.sourceDSN, m.targetType, m.targetDSN, m.opts)
			return err
		})
		return cloneDoneMsg{result: result, err: err}
	}
}
