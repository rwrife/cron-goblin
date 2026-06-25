// Package tui is the M5 "terminal renaissance" centerpiece: a live cron
// preview. The user types a 5-field cron expression and watches the plain
// English, the next fire times, a week-view heatmap, and inline lint warnings
// update on every keystroke. Invalid input never crashes the view — it shows a
// gentle error and keeps the last good preview's frame intact.
//
// The program is a thin bubbletea wrapper around the pure core in state.go: all
// the real computation (parse → explain → next-runs → heatmap → lint) lives in
// computePreview, so this file is mostly input handling and layout. That split
// keeps the logic testable without a TTY and keeps bubbletea concerns out of
// the rest of cron-goblin.
package tui

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/render"
)

// displayRuns is how many of the computed runs the next-runs list shows. The
// heatmap still uses the full previewRuns window for density; the list is
// trimmed so it fits a typical pane without scrolling.
const displayRuns = 8

// Options configures a TUI program. Initial seeds the input box (e.g. an
// expression passed on the command line); Location is the timezone fire times
// are shown in; NoColor strips ANSI styling; Now overrides the reference clock
// for deterministic tests (nil → time.Now at start).
type Options struct {
	Initial  string
	Location *time.Location
	NoColor  bool
	Now      func() time.Time
}

// Model is the bubbletea model for the live preview. It owns the text input,
// the current derived preview, and layout state (terminal size). It is exported
// so tests can drive Update/View directly without a running program.
type Model struct {
	input   textinput.Model
	loc     *time.Location
	now     func() time.Time
	palette render.Palette
	styles  styles

	width  int
	height int

	pv       preview
	greeting string
	quitting bool
}

// styles bundles the lipgloss styles for the chrome around the render package's
// output (titles, the input frame, help footer). Heatmap/run/warning colors
// come from render.Palette so the two stay visually consistent.
type styles struct {
	title   lipgloss.Style
	section lipgloss.Style
	box     lipgloss.Style
	help    lipgloss.Style
	errText lipgloss.Style
	snark   lipgloss.Style
}

func newStyles(noColor bool) styles {
	if noColor {
		plain := lipgloss.NewStyle()
		box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).Padding(0, 1)
		return styles{title: plain, section: plain, box: box, help: plain, errText: plain, snark: plain}
	}
	return styles{
		title:   lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("213")),
		section: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111")),
		box:     lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("63")).Padding(0, 1),
		help:    lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
		errText: lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		snark:   lipgloss.NewStyle().Italic(true).Foreground(lipgloss.Color("245")),
	}
}

// NewModel builds the initial model from Options. It primes the input with any
// initial expression and computes the first preview so the very first frame is
// already populated (no empty flash before the first keystroke).
func NewModel(opts Options) Model {
	loc := opts.Location
	if loc == nil {
		loc = time.Local
	}
	nowFn := opts.Now
	if nowFn == nil {
		nowFn = time.Now
	}

	ti := textinput.New()
	ti.Placeholder = "*/15 9-17 * * 1-5"
	ti.Prompt = "cron ❯ "
	ti.CharLimit = 256
	ti.SetValue(opts.Initial)
	ti.Focus()
	ti.CursorEnd()

	palette := render.DefaultPalette()
	if opts.NoColor {
		palette = render.NoColorPalette()
	}

	m := Model{
		input:    ti,
		loc:      loc,
		now:      nowFn,
		palette:  palette,
		styles:   newStyles(opts.NoColor),
		greeting: goblin.Greeting(0),
	}
	m.pv = computePreview(m.input.Value(), m.now(), m.loc)
	return m
}

// Init implements tea.Model. We blink the input cursor; everything else is
// driven by key/resize messages.
func (m Model) Init() tea.Cmd { return textinput.Blink }

// Update implements tea.Model. It handles quit keys and window resizes itself,
// delegates text editing to the textinput component, and — whenever the input
// value might have changed — recomputes the preview so the view is always in
// sync with what's typed.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Size the input to the available width, leaving room for the prompt
		// and the box border/padding.
		m.input.Width = maxInt(10, msg.Width-len(m.input.Prompt)-6)
		return m, nil

	case tea.KeyMsg:
		switch msg.Type {
		case tea.KeyCtrlC, tea.KeyEsc:
			m.quitting = true
			return m, tea.Quit
		}
		// Ctrl+U clears the line quickly; otherwise let textinput handle it.
		before := m.input.Value()
		var cmd tea.Cmd
		m.input, cmd = m.input.Update(msg)
		if m.input.Value() != before {
			m.pv = computePreview(m.input.Value(), m.now(), m.loc)
		}
		return m, cmd
	}

	// Non-key messages (blink ticks, etc.) still flow to the input.
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// View implements tea.Model. It assembles the frame top-to-bottom: title, the
// input box, the live result (English + next runs + heatmap, or an error), the
// inline warnings, and a help footer. The whole result is a plain string;
// bubbletea handles the terminal writes.
func (m Model) View() string {
	if m.quitting {
		return m.styles.snark.Render("👹 fine, leave. your crontab is still a mess.") + "\n"
	}

	var b strings.Builder

	b.WriteString(m.styles.title.Render("cron-goblin 👹⏰  live preview"))
	b.WriteByte('\n')
	b.WriteString(m.styles.snark.Render(m.greeting))
	b.WriteString("\n\n")

	b.WriteString(m.styles.box.Render(m.input.View()))
	b.WriteString("\n\n")

	b.WriteString(m.body())

	b.WriteString("\n\n")
	b.WriteString(m.styles.help.Render("type to edit · esc/ctrl+c to quit"))
	b.WriteByte('\n')
	return b.String()
}

// body renders the result region for the current preview: a hint when the
// input is blank, a gentle error when it doesn't parse, or the full live view
// (English, next runs, heatmap, warnings) when it does.
func (m Model) body() string {
	pv := m.pv

	// Blank input: invite, don't scold.
	if isBlank(pv.Expr) {
		return m.styles.help.Render(
			"Start typing a 5-field cron expression above.\n" +
				"Try: */15 9-17 * * 1-5   ·   0 9 * * 1-5   ·   0 0 1 * *")
	}

	// Parse error: show it without tearing down the frame.
	if !pv.Valid {
		msg := "incomplete or invalid expression"
		if pv.Err != nil {
			msg = pv.Err.Error()
		}
		return m.styles.errText.Render("✗ "+msg) + "\n" +
			m.styles.help.Render("(keep typing — 5 fields: minute hour day-of-month month day-of-week)")
	}

	var b strings.Builder

	// English description.
	b.WriteString(m.styles.section.Render("English"))
	b.WriteByte('\n')
	b.WriteString("  " + pv.English)
	b.WriteString("\n\n")

	// Next runs.
	b.WriteString(m.styles.section.Render(fmt.Sprintf("Next runs (%s)", m.loc)))
	b.WriteByte('\n')
	runsBlock := render.NextRuns(pv.Runs, m.now(), m.loc, displayRuns, m.palette)
	b.WriteString(indent(runsBlock, "  "))
	b.WriteString("\n\n")

	// Week heatmap.
	b.WriteString(m.styles.section.Render("Week heatmap"))
	b.WriteByte('\n')
	b.WriteString(render.Heatmap(pv.Grid, m.palette))

	// Inline lint warnings, if any.
	if w := render.Warnings(pv.Warnings, m.palette); w != "" {
		b.WriteString("\n\n")
		b.WriteString(m.styles.section.Render("Goblin says"))
		b.WriteByte('\n')
		b.WriteString(w)
	}

	return b.String()
}

// indent prefixes every line of s with the given pad. Used to inset the
// render package's blocks under their section headers.
func indent(s, pad string) string {
	if s == "" {
		return s
	}
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = pad + lines[i]
	}
	return strings.Join(lines, "\n")
}

// Run starts the bubbletea program with the given options, reading from in and
// writing to out. It blocks until the user quits. in/out are parameters so the
// caller (cmd/goblin) can wire os.Stdin/os.Stdout and so tests can substitute
// pipes. A nil in/out falls back to bubbletea's defaults.
func Run(opts Options, in io.Reader, out io.Writer) error {
	teaOpts := []tea.ProgramOption{tea.WithAltScreen()}
	if in != nil {
		teaOpts = append(teaOpts, tea.WithInput(in))
	}
	if out != nil {
		teaOpts = append(teaOpts, tea.WithOutput(out))
	}
	p := tea.NewProgram(NewModel(opts), teaOpts...)
	_, err := p.Run()
	return err
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
