// single.go provides lint helpers for checking ONE schedule in isolation —
// the mode the M5 TUI needs as the user live-edits a single expression. The
// file-oriented Lint entrypoint runs cross-job rules (like collision) that are
// meaningless for a lone expression; CheckSchedule runs only the rules that
// make sense for a single line and returns their de-personified messages.
package lint

import (
	"time"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// singleEntryRules is the subset of DefaultRules that draw a conclusion from a
// single crontab line on its own. Collision is intentionally excluded: it only
// has meaning across two or more jobs sharing a minute, which can't happen with
// one live-edited expression.
func singleEntryRules() []Rule {
	return []Rule{
		deadExpressionRule{},
		tooFrequentRule{minMinutes: defaultMinInterval},
	}
}

// CheckSchedule runs the single-line lint rules over one parsed schedule and
// returns the resulting Findings, sorted most-severe first. It is the
// entrypoint the TUI uses to surface inline warnings (dead expression, overly
// tight cadence) as the user types, without reaching into unexported rule
// types. The schedule is treated as crontab line 1.
//
// The result mirrors what `goblin lint` would report for a one-line crontab
// containing this expression (minus the cross-job collision rule), so the TUI
// and the CLI never disagree about a given expression.
func CheckSchedule(s parse.Schedule) []Finding {
	return checkScheduleWith(s, singleEntryRules())
}

// CheckScheduleTZ is CheckSchedule plus the DST-danger rule bound to loc, using
// `now` to pick which years' transitions to consider. It lets the TUI warn —
// live, as the user types — that the current expression lands in a daylight-
// saving gap or overlap for the preview timezone. A nil or UTC loc adds no DST
// findings, so the result matches CheckSchedule.
func CheckScheduleTZ(s parse.Schedule, loc *time.Location, now time.Time) []Finding {
	rules := append(singleEntryRules(), newDSTDangerRule(loc, now))
	return checkScheduleWith(s, rules)
}

// checkScheduleWith runs the given rules over a single schedule (treated as
// crontab line 1) and returns the sorted findings. Shared by CheckSchedule and
// CheckScheduleTZ.
func checkScheduleWith(s parse.Schedule, rules []Rule) []Finding {
	entry := Entry{Line: 1, Raw: s.Raw, Schedule: s}
	var findings []Finding
	for _, rule := range rules {
		findings = append(findings, rule.Check([]Entry{entry})...)
	}
	sortFindings(findings)
	return findings
}

// Messages extracts the human messages from a slice of Findings, preserving
// order. It is a convenience for renderers that only want the text (e.g. the
// TUI's warning pane), keeping Finding's structure available for callers that
// need severity/lines.
func Messages(findings []Finding) []string {
	msgs := make([]string, 0, len(findings))
	for _, f := range findings {
		msgs = append(msgs, f.Message)
	}
	return msgs
}
