package tui

import (
	"context"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/goccy/go-yaml"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

type mirrorPhase int

const (
	mirrorReview mirrorPhase = iota
	mirrorConfirmReset
	mirrorRunning
	mirrorDone
)

type mirrorModel struct {
	ctx         context.Context
	job         *seeder.MirrorJob
	opts        seeder.Options
	previewRows int
	dryRun      bool

	phase       mirrorPhase
	showPreview bool
	preview     string
	offset      int
	height      int

	progress seeder.Progress
	events   chan tea.Msg
	result   seeder.Result
	err      error
	aborted  bool
}

type mirrorProgressMsg seeder.Progress

type mirrorDoneMsg struct {
	result seeder.Result
	err    error
}

// RunMirror reviews a prepared mirror in the terminal: the plan, optional
// sample rows, a confirmation (twice for reset), then live progress. It runs
// exactly the job the non-interactive command would.
func RunMirror(ctx context.Context, job *seeder.MirrorJob, opts seeder.Options, previewRows int, dryRun bool) error {
	m := newMirrorModel(ctx, job, opts, previewRows, dryRun)
	final, err := tea.NewProgram(m, tea.WithContext(ctx), tea.WithAltScreen()).Run()
	if err != nil {
		return fmt.Errorf("TUI error: %w", err)
	}
	fm := final.(mirrorModel)
	switch {
	case fm.err != nil:
		return fm.err
	case fm.aborted:
		return fmt.Errorf("aborted by user")
	case fm.phase == mirrorDone:
		seeder.RenderResult(os.Stdout, fm.result)
		if problems := fm.result.Problems(); len(problems) > 0 {
			return fmt.Errorf("mirror incomplete: %d table(s) missing %d rows", len(problems), fm.result.Missing)
		}
	}
	return nil
}

func newMirrorModel(ctx context.Context, job *seeder.MirrorJob, opts seeder.Options, previewRows int, dryRun bool) mirrorModel {
	return mirrorModel{ctx: ctx, job: job, opts: opts, previewRows: previewRows, dryRun: dryRun, height: 24}
}

func (m mirrorModel) Init() tea.Cmd { return nil }

func (m mirrorModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.height = msg.Height
	case mirrorProgressMsg:
		m.progress = seeder.Progress(msg)
		return m, waitMirror(m.events)
	case mirrorDoneMsg:
		m.phase = mirrorDone
		m.result = msg.result
		m.err = msg.err
		return m, tea.Quit
	case tea.KeyMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m mirrorModel) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	key := msg.String()
	if m.phase == mirrorRunning {
		if key == "ctrl+c" {
			m.aborted = true
			return m, tea.Quit
		}
		return m, nil
	}
	switch key {
	case "ctrl+c", "q", "esc":
		if m.phase == mirrorConfirmReset && key == "esc" {
			m.phase = mirrorReview
			return m, nil
		}
		if !m.dryRun {
			m.aborted = true
		}
		return m, tea.Quit
	case "up", "k":
		if m.offset > 0 {
			m.offset--
		}
	case "down", "j":
		m.offset++
	case "p":
		m.showPreview = !m.showPreview
		if m.showPreview && m.preview == "" {
			m.preview = m.renderPreview()
		}
		m.offset = 0
	case "y", "enter":
		if m.dryRun || m.job.Plan.TotalInsert == 0 {
			return m, nil
		}
		if m.job.Plan.Mode == compare.ModeReset && m.phase == mirrorReview {
			m.phase = mirrorConfirmReset
			return m, nil
		}
		m.phase = mirrorRunning
		m.events = make(chan tea.Msg, 64)
		return m, tea.Batch(m.start(), waitMirror(m.events))
	}
	return m, nil
}

func (m mirrorModel) start() tea.Cmd {
	events := m.events
	job, opts, ctx := m.job, m.opts, m.ctx
	return func() tea.Msg {
		opts.OnProgress = func(p seeder.Progress) {
			select {
			case events <- mirrorProgressMsg(p):
			default:
			}
		}
		result, err := job.Run(ctx, opts, nil)
		events <- mirrorDoneMsg{result: result, err: err}
		return nil
	}
}

func waitMirror(events chan tea.Msg) tea.Cmd {
	return func() tea.Msg { return <-events }
}

func (m mirrorModel) renderPreview() string {
	preview, err := m.job.Preview(m.previewRows, m.opts.Generate.SelfRefDepth)
	if err != nil {
		return "Sample rows unavailable: " + err.Error()
	}
	ordered := yaml.MapSlice{}
	for _, t := range m.job.Plan.Order {
		ordered = append(ordered, yaml.MapItem{Key: t, Value: preview[t]})
	}
	b, err := yaml.Marshal(ordered)
	if err != nil {
		return err.Error()
	}
	return fmt.Sprintf("Sample rows (up to %d per table, nothing written)\n\n%s", m.previewRows, b)
}

func (m mirrorModel) View() string {
	var sb strings.Builder
	sb.WriteString(headerBorder.Render(fmt.Sprintf("  seedstorm mirror  %s → %s", m.job.Report.Source.Label, m.job.Report.Target.Label)))
	sb.WriteString("\n")

	switch m.phase {
	case mirrorRunning:
		p := m.progress
		if p.Tables == 0 {
			fmt.Fprintf(&sb, "\n  Preparing %d rows into %d tables…\n", m.job.Plan.TotalInsert, len(m.job.Plan.Entries))
		} else {
			fmt.Fprintf(&sb, "\n  Filling %s  (%d/%d tables)  %d / %d rows\n", p.Table, p.TableIndex, p.Tables, p.Inserted, p.Requested)
		}
		sb.WriteString(helpStyle.Render("\n  ctrl+c aborts (rows already inserted stay)"))
		return sb.String()
	case mirrorDone:
		return sb.String()
	}

	var body strings.Builder
	if m.showPreview {
		body.WriteString(m.preview)
	} else {
		compare.RenderPlan(&body, m.job.Plan)
	}
	lines := strings.Split(body.String(), "\n")
	visible := m.height - 6
	if visible < 5 {
		visible = 5
	}
	offset := m.offset
	if maxOffset := len(lines) - visible; offset > maxOffset {
		offset = maxOffset
	}
	if offset < 0 {
		offset = 0
	}
	end := offset + visible
	if end > len(lines) {
		end = len(lines)
	}
	for _, line := range lines[offset:end] {
		sb.WriteString("  " + line + "\n")
	}

	switch {
	case m.phase == mirrorConfirmReset:
		sb.WriteString(errorStyle.Render(fmt.Sprintf("  Press y again to TRUNCATE %d table(s) on %s and refill them · esc to go back", len(m.job.Plan.Truncate), m.job.Report.Target.Label)))
	case m.dryRun:
		sb.WriteString(helpStyle.Render("  dry run · p toggle samples · ↑/↓ scroll · q quit"))
	case m.job.Plan.TotalInsert == 0:
		sb.WriteString(helpStyle.Render("  target already matches · p samples · q quit"))
	default:
		sb.WriteString(helpStyle.Render(fmt.Sprintf("  y/enter insert %d rows · p toggle samples · ↑/↓ scroll · q abort", m.job.Plan.TotalInsert)))
	}
	return sb.String()
}
