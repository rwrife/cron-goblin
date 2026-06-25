package tui

import (
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// fixedNow returns a deterministic clock for model tests.
func fixedNow() time.Time { return time.Date(2026, 6, 22, 8, 0, 0, 0, time.UTC) }

// newTestModel builds a Model wired to UTC, no color, and the fixed clock, then
// applies a window size so layout-dependent code paths run.
func newTestModel(initial string) Model {
	m := NewModel(Options{
		Initial:  initial,
		Location: time.UTC,
		NoColor:  true,
		Now:      fixedNow,
	})
	updated, _ := m.Update(tea.WindowSizeMsg{Width: 80, Height: 30})
	return updated.(Model)
}

// typeRunes feeds each rune of s to the model as a key message, returning the
// model after all keystrokes — mimicking a user typing into the input.
func typeRunes(m Model, s string) Model {
	for _, r := range s {
		updated, _ := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
		m = updated.(Model)
	}
	return m
}

func TestModel_BlankInputShowsHint(t *testing.T) {
	m := newTestModel("")
	view := m.View()
	if !strings.Contains(view, "Start typing") {
		t.Errorf("blank model should show a hint, got:\n%s", view)
	}
	// No "Next runs" section before anything is typed.
	if strings.Contains(view, "Next runs") {
		t.Errorf("blank model should not show next runs, got:\n%s", view)
	}
}

func TestModel_TypingUpdatesPreviewLive(t *testing.T) {
	m := newTestModel("")

	// Type a complete weekday-9am expression character by character.
	m = typeRunes(m, "0 9 * * 1-5")
	view := m.View()

	if !strings.Contains(view, "Next runs") {
		t.Fatalf("after typing a valid expression, view should show next runs:\n%s", view)
	}
	if !strings.Contains(strings.ToLower(view), "weekday") {
		t.Errorf("view should describe weekdays in English:\n%s", view)
	}
	// The first fire (Mon 09:00) should appear in the next-runs list.
	if !strings.Contains(view, "2026-06-22 09:00") {
		t.Errorf("view should list the first fire time:\n%s", view)
	}
	if !strings.Contains(view, "Week heatmap") {
		t.Errorf("view should render the heatmap section:\n%s", view)
	}
}

func TestModel_InvalidWhileTypingShowsErrorNotCrash(t *testing.T) {
	m := newTestModel("")
	// A single token is not yet a 5-field expression.
	m = typeRunes(m, "0 9")
	view := m.View()
	if !strings.Contains(view, "✗") {
		t.Errorf("partial/invalid expression should show an error marker:\n%s", view)
	}
	// It must NOT show next runs for an unparseable expression.
	if strings.Contains(view, "Next runs") {
		t.Errorf("invalid expression must not show next runs:\n%s", view)
	}
}

func TestModel_ErrorThenValidRecovers(t *testing.T) {
	m := newTestModel("0 9") // invalid (too few fields)
	if !strings.Contains(m.View(), "✗") {
		t.Fatal("precondition: should start in error state")
	}
	// Finish the expression; the error should clear and runs appear.
	m = typeRunes(m, " * * 1-5")
	view := m.View()
	if strings.Contains(view, "✗") {
		t.Errorf("completing the expression should clear the error:\n%s", view)
	}
	if !strings.Contains(view, "Next runs") {
		t.Errorf("valid expression should show next runs after recovery:\n%s", view)
	}
}

func TestModel_NeverFiresSurfaced(t *testing.T) {
	m := newTestModel("0 0 30 2 *") // Feb 30
	view := m.View()
	if !strings.Contains(view, "never fires") {
		t.Errorf("Feb 30 should surface a never-fires note in the view:\n%s", view)
	}
}

func TestModel_EscQuits(t *testing.T) {
	m := newTestModel("0 9 * * *")
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if cmd == nil {
		t.Fatal("esc should return a quit command")
	}
	if !updated.(Model).quitting {
		t.Error("esc should set quitting")
	}
	// The quitting view is a short sign-off, not the full frame.
	if strings.Contains(updated.(Model).View(), "Week heatmap") {
		t.Error("quitting view should not render the full frame")
	}
}

func TestModel_InitialExpressionPrepopulates(t *testing.T) {
	m := newTestModel("0 9 * * 1-5")
	view := m.View()
	if !strings.Contains(view, "Next runs") {
		t.Errorf("initial expression should populate the first frame:\n%s", view)
	}
}
