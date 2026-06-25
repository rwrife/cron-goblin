// state.go holds the *pure* core of the TUI: given a raw expression string and
// a reference time, compute everything the view needs (parse result, English,
// next runs, heatmap, lint warnings). Keeping this separate from the bubbletea
// plumbing means the interesting logic is unit-testable without spinning up a
// terminal program — tests feed an expression and assert on the resulting
// preview struct.
package tui

import (
	"time"

	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/rwrife/cron-goblin/internal/render"
)

// previewRuns is how many upcoming fire times the TUI computes. It feeds both
// the next-runs list (truncated for display) and the heatmap density, so it is
// large enough to populate a week-view meaningfully but bounded so live typing
// stays snappy.
const previewRuns = 35

// preview is the fully-derived state for one expression at one instant. Every
// field is a pure function of (expr, now, loc); the view renders it without
// any further computation. Valid distinguishes "blank input" and "parse error"
// (both Valid=false) from a usable schedule.
type preview struct {
	Expr     string          // the raw input it was computed from
	Valid    bool            // true when Expr parsed into a schedule
	Err      error           // non-nil parse error when Valid is false and Expr != ""
	Sched    parse.Schedule  // the parsed schedule (zero value when !Valid)
	English  string          // plain-English description (empty when !Valid)
	Runs     []time.Time     // up to previewRuns upcoming fire times
	Never    bool            // true when a valid schedule has no future fire
	Grid     render.HeatGrid // weekday×hour density of Runs
	Warnings []string        // inline lint messages (dead/too-frequent)
}

// computePreview derives the full preview for an expression. A blank or
// whitespace-only expression yields a non-valid, error-free preview (the view
// shows a hint, not a complaint). A non-blank expression that fails to parse
// yields Valid=false with Err set. A good expression is fully populated.
//
// now and loc are parameters (not time.Now/time.Local) so the function is
// deterministic and testable; the live program passes the real clock and zone.
func computePreview(expr string, now time.Time, loc *time.Location) preview {
	if loc == nil {
		loc = time.UTC
	}
	p := preview{Expr: expr}

	if isBlank(expr) {
		return p
	}

	sched, err := parse.Parse(expr)
	if err != nil {
		p.Err = err
		return p
	}

	p.Valid = true
	p.Sched = sched
	p.English = explain.Explain(sched)
	p.Runs = nextrun.NextN(sched, now, previewRuns, loc)
	p.Never = len(p.Runs) == 0
	p.Grid = render.BuildHeatGrid(p.Runs, loc)
	p.Warnings = lint.Messages(lint.CheckSchedule(sched))
	return p
}

// isBlank reports whether s is empty or only whitespace.
func isBlank(s string) bool {
	for _, r := range s {
		if r != ' ' && r != '\t' && r != '\n' && r != '\r' {
			return false
		}
	}
	return true
}
