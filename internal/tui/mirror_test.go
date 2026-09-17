package tui

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/AxeForging/seedstorm/internal/compare"
	"github.com/AxeForging/seedstorm/internal/seeder"
)

func mirrorJob(mode compare.MirrorMode, insert int64) *seeder.MirrorJob {
	plan := compare.MirrorPlan{Mode: mode, Scale: 1}
	if insert > 0 {
		plan.Order = []string{"users"}
		plan.Entries = []compare.PlanEntry{{Table: "users", SourceRows: insert, Want: insert, Insert: insert, Reason: compare.ReasonMatch}}
		plan.TotalInsert = insert
	}
	if mode == compare.ModeReset {
		plan.Truncate = []string{"users", "orders"}
	}
	return &seeder.MirrorJob{
		Report: compare.Report{Source: compare.SnapshotInfo{Label: "prod"}, Target: compare.SnapshotInfo{Label: "stage"}},
		Plan:   plan,
	}
}

func press(m mirrorModel, key string) (mirrorModel, tea.Cmd) {
	var msg tea.KeyMsg
	switch key {
	case "enter":
		msg = tea.KeyMsg{Type: tea.KeyEnter}
	case "esc":
		msg = tea.KeyMsg{Type: tea.KeyEsc}
	default:
		msg = tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(key)}
	}
	next, cmd := m.Update(msg)
	return next.(mirrorModel), cmd
}

func TestMirrorModel_ReviewShowsPlanAndStartsOnConfirm(t *testing.T) {
	m := newMirrorModel(context.Background(), mirrorJob(compare.ModeTopUp, 40), seeder.Options{}, 3, false)
	view := m.View()
	for _, want := range []string{"prod → stage", "users", "y/enter insert 40 rows"} {
		if !strings.Contains(view, want) {
			t.Fatalf("review view missing %q:\n%s", want, view)
		}
	}
	m, cmd := press(m, "y")
	if m.phase != mirrorRunning || cmd == nil || m.events == nil {
		t.Fatalf("phase = %v, cmd nil = %v; confirm should start the run", m.phase, cmd == nil)
	}
	// Keys other than ctrl+c are ignored while rows are being written.
	if m, _ = press(m, "q"); m.phase != mirrorRunning || m.aborted {
		t.Fatal("q interrupted a running mirror")
	}
}

func TestMirrorModel_ResetNeedsASecondConfirmation(t *testing.T) {
	m := newMirrorModel(context.Background(), mirrorJob(compare.ModeReset, 10), seeder.Options{}, 3, false)
	m, cmd := press(m, "y")
	if m.phase != mirrorConfirmReset || cmd != nil {
		t.Fatalf("first y: phase = %v", m.phase)
	}
	if view := m.View(); !strings.Contains(view, "TRUNCATE 2 table(s) on stage") {
		t.Fatalf("confirm view:\n%s", view)
	}
	m, _ = press(m, "esc")
	if m.phase != mirrorReview || m.aborted {
		t.Fatalf("esc should return to review, phase = %v aborted = %v", m.phase, m.aborted)
	}
	m, _ = press(m, "y")
	m, cmd = press(m, "y")
	if m.phase != mirrorRunning || cmd == nil {
		t.Fatalf("second y: phase = %v", m.phase)
	}
}

func TestMirrorModel_DryRunAndNothingToDoNeverWrite(t *testing.T) {
	dry := newMirrorModel(context.Background(), mirrorJob(compare.ModeTopUp, 5), seeder.Options{}, 3, true)
	if dry, _ = press(dry, "enter"); dry.phase != mirrorReview {
		t.Fatal("dry run started a write")
	}
	if !strings.Contains(dry.View(), "dry run") {
		t.Fatal("dry run hint missing")
	}
	if _, cmd := press(dry, "q"); cmd == nil {
		t.Fatal("q should quit")
	}

	done := newMirrorModel(context.Background(), mirrorJob(compare.ModeTopUp, 0), seeder.Options{}, 3, false)
	if done, _ = press(done, "y"); done.phase != mirrorReview {
		t.Fatal("empty plan started a run")
	}
	if !strings.Contains(done.View(), "already matches") {
		t.Fatalf("view:\n%s", done.View())
	}
}

func TestMirrorModel_QuitBeforeConfirmIsAnAbort(t *testing.T) {
	m := newMirrorModel(context.Background(), mirrorJob(compare.ModeTopUp, 5), seeder.Options{}, 3, false)
	m, cmd := press(m, "q")
	if !m.aborted || cmd == nil {
		t.Fatal("q before confirming must abort")
	}
}

func TestMirrorModel_ProgressAndDoneMessages(t *testing.T) {
	m := newMirrorModel(context.Background(), mirrorJob(compare.ModeTopUp, 5), seeder.Options{}, 3, false)
	m, _ = press(m, "y")
	if view := m.View(); !strings.Contains(view, "Preparing 5 rows into 1 tables") {
		t.Fatalf("view before the first tick:\n%s", view)
	}
	next, _ := m.Update(mirrorProgressMsg{Table: "users", TableIndex: 1, Tables: 1, Inserted: 3, Requested: 5})
	m = next.(mirrorModel)
	if !strings.Contains(m.View(), "3 / 5 rows") {
		t.Fatalf("progress view:\n%s", m.View())
	}
	next, cmd := m.Update(mirrorDoneMsg{result: seeder.Result{Inserted: 5}})
	m = next.(mirrorModel)
	if m.phase != mirrorDone || m.result.Inserted != 5 || cmd == nil {
		t.Fatalf("done: phase = %v result = %+v", m.phase, m.result)
	}
}

// Sample rows read the target database. Generating them inside Update froze the
// whole screen; now the key returns at once with a loading state and the rows
// arrive as a message.
func TestMirrorModel_PreviewLoadsWithoutBlockingTheScreen(t *testing.T) {
	m := newMirrorModel(context.Background(), mirrorJob(compare.ModeTopUp, 40), seeder.Options{}, 3, true)
	m, cmd := press(m, "p")
	if cmd == nil {
		t.Fatal("p did not start loading the preview in the background")
	}
	if !m.previewLoading || !strings.Contains(m.View(), "Generating sample rows") {
		t.Fatalf("no loading state:\n%s", m.View())
	}
	next, _ := m.Update(mirrorPreviewMsg("Sample rows (up to 3 per table, nothing written)\n\nusers: []"))
	m = next.(mirrorModel)
	if m.previewLoading || !strings.Contains(m.View(), "nothing written") {
		t.Fatalf("preview not shown after it arrived:\n%s", m.View())
	}
	if m, cmd = press(m, "p"); cmd != nil || m.showPreview {
		t.Fatal("toggling back to the plan should not reload anything")
	}
}
