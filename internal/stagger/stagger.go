// Package stagger breaks up "thundering herds": clusters of crontab jobs that
// all fire on the very same minute (the classic `0 9 * * *` × N pile-up that
// stampedes a box at the top of the hour). It proposes a deterministic, evenly
// spaced spread of those jobs across a bounded window so the load is smeared
// out instead of spiking.
//
// Like the rest of cron-goblin's logic packages, stagger is pure and
// humorless: it reads a crontab, returns a structured Plan describing which
// lines move and to what, and can render a rewritten crontab. It never touches
// the filesystem and never "applies" anything — the command layer owns I/O and
// the (confirmed) write. The grumpy commentary lives in internal/goblin.
//
// # What counts as a herd
//
// Two jobs are in the same herd when their schedules are identical in every
// field AND their minute field is a single fixed value (a literal like `0` or
// `30`, not `*`, a list, a range, or a step). That deliberately narrow rule is
// what makes a rewrite safe and obvious: spreading the minute keeps each job on
// the same hour, day, month, and weekday it already had — only the minute-past
// changes, so the human intent ("run these every morning at 9") is preserved
// while the exact-same-instant collision is removed.
//
// Jobs whose minute is already complex (`*/15`, `0,30`, `9-17`) are left
// alone: rewriting them would change their cadence, not merely de-collide
// them, which is out of scope for a stagger.
package stagger

import (
	"fmt"
	"sort"
	"strings"

	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/rwrife/cron-goblin/internal/parse"
)

// DefaultMaxSpread is the default width, in minutes, of the window a herd is
// spread across. One hour is the natural unit for the canonical top-of-the-hour
// pile-up: it keeps every job within the same clock hour it already fired in
// while giving 60 distinct slots to scatter into.
const DefaultMaxSpread = 59

// Move records that a single crontab line's minute field is being changed from
// FromMinute to ToMinute as part of de-herding. Line is the 1-based source line
// number; Command is the job's command (for friendly reporting); Original and
// Rewritten are the full line text before and after.
type Move struct {
	Line       int    `json:"line"`
	Command    string `json:"command"`
	FromMinute int    `json:"from_minute"`
	ToMinute   int    `json:"to_minute"`
	Original   string `json:"original"`
	Rewritten  string `json:"rewritten"`
}

// Herd is one cluster of jobs that all fired on the same minute and the moves
// proposed to spread them out. Signature is the human-readable schedule they
// shared (e.g. "0 9 * * *"). A Herd always has at least two Moves; the first
// job in line order keeps its original minute (it anchors the spread) and the
// rest are pushed to later minutes within the window.
type Herd struct {
	Signature string `json:"signature"`
	Moves     []Move `json:"moves"`
}

// Plan is the result of analyzing a crontab for thundering herds. Herds lists
// every cluster found (possibly empty). MaxSpread echoes the window width used.
// A Plan with no herds means the crontab is already well spread.
type Plan struct {
	Herds     []Herd `json:"herds"`
	MaxSpread int    `json:"max_spread"`
}

// Empty reports whether the plan proposes no changes at all.
func (p Plan) Empty() bool { return len(p.Herds) == 0 }

// MovedLines returns the total number of crontab lines the plan would rewrite
// (the anchor line of each herd does not count as moved).
func (p Plan) MovedLines() int {
	n := 0
	for _, h := range p.Herds {
		for _, m := range h.Moves {
			if m.FromMinute != m.ToMinute {
				n++
			}
		}
	}
	return n
}

// entry pairs a parsed lint.Entry with its single fixed minute, for the subset
// of lines that are eligible to be staggered.
type fixedEntry struct {
	e      lint.Entry
	minute int
}

// Analyze reads a crontab from src and returns a Plan that spreads each
// detected thundering herd across a window of maxSpread minutes. A maxSpread of
// 0 or less uses DefaultMaxSpread. The input is never modified; use
// Plan-driven Rewrite to produce new crontab text.
func Analyze(src string, maxSpread int) (Plan, error) {
	if maxSpread <= 0 {
		maxSpread = DefaultMaxSpread
	}
	entries, err := lint.ParseCrontab(strings.NewReader(src))
	if err != nil {
		return Plan{}, err
	}

	// Bucket eligible lines by their non-minute schedule signature. Only jobs
	// whose minute is a single literal value are eligible; everything else is
	// left untouched.
	buckets := map[string][]fixedEntry{}
	var order []string // preserve first-seen signature order for determinism
	for _, e := range entries {
		if e.ParseErr != nil {
			continue
		}
		min, ok := fixedMinute(e.Schedule)
		if !ok {
			continue
		}
		sig := nonMinuteSignature(e.Schedule)
		if _, seen := buckets[sig]; !seen {
			order = append(order, sig)
		}
		buckets[sig] = append(buckets[sig], fixedEntry{e: e, minute: min})
	}

	var herds []Herd
	for _, sig := range order {
		group := buckets[sig]
		if len(group) < 2 {
			continue // a lone job is not a herd
		}
		// Stable ordering: by source line. The lowest line anchors the spread.
		sort.Slice(group, func(i, j int) bool { return group[i].e.Line < group[j].e.Line })

		anchor := group[0].minute
		targets := spreadMinutes(anchor, len(group), maxSpread)

		moves := make([]Move, 0, len(group))
		for i, fe := range group {
			to := targets[i]
			rewritten := replaceMinuteToken(fe.e.Raw, to)
			moves = append(moves, Move{
				Line:       fe.e.Line,
				Command:    fe.e.Command,
				FromMinute: fe.minute,
				ToMinute:   to,
				Original:   fe.e.Raw,
				Rewritten:  rewritten,
			})
		}
		herds = append(herds, Herd{
			Signature: humanSignature(group[0].e.Schedule),
			Moves:     moves,
		})
	}

	return Plan{Herds: herds, MaxSpread: maxSpread}, nil
}

// fixedMinute returns the single literal minute of a schedule and true when the
// minute field is exactly one concrete value written as a plain number (not
// "*", a list, a range, or a step). A field qualifies when it has exactly one
// matched value and its raw text is the decimal form of that value.
func fixedMinute(s parse.Schedule) (int, bool) {
	f := s.Minute
	if f.Star || len(f.Values) != 1 {
		return 0, false
	}
	if strings.TrimSpace(f.Raw) != fmt.Sprintf("%d", f.Values[0]) {
		return 0, false
	}
	return f.Values[0], true
}

// nonMinuteSignature builds a key that is identical for jobs sharing every
// field except the minute. Built from the parsed (normalized) hour/dom/month/
// dow value sets so that equivalent-but-differently-written fields still group
// together (e.g. two jobs at hour `9` collide regardless of spacing).
func nonMinuteSignature(s parse.Schedule) string {
	return strings.Join([]string{
		fieldKey(s.Hour),
		fieldKey(s.DOM),
		fieldKey(s.Month),
		fieldKey(s.DOW),
	}, "|")
}

// fieldKey renders a field's normalized matched values as a stable string,
// distinguishing a literal "*" (Star) from an enumerated set that happens to
// cover the full range, since cron's day OR-rule treats those differently.
func fieldKey(f parse.FieldSpec) string {
	if f.Star {
		return "*"
	}
	parts := make([]string, len(f.Values))
	for i, v := range f.Values {
		parts[i] = fmt.Sprintf("%d", v)
	}
	return strings.Join(parts, ",")
}

// humanSignature renders the shared schedule of a herd for reports, using each
// field's original raw text so it reads the way the user wrote it. The minute
// is shown as the anchor's literal, since that's the value the herd collides
// on.
func humanSignature(s parse.Schedule) string {
	return strings.Join([]string{
		strings.TrimSpace(s.Minute.Raw),
		strings.TrimSpace(s.Hour.Raw),
		strings.TrimSpace(s.DOM.Raw),
		strings.TrimSpace(s.Month.Raw),
		strings.TrimSpace(s.DOW.Raw),
	}, " ")
}

// spreadMinutes returns n target minutes, evenly spaced starting at anchor and
// spanning at most `width` minutes, clamped to the valid 0-59 range. The first
// slot is always the anchor (so the herd's earliest job stays put). Spacing is
// deterministic: floor(width / (n-1)) between consecutive jobs, which keeps the
// spread inside the window and avoids drifting past it for large herds.
//
// When the herd is bigger than the window can hold with whole-minute spacing
// (n-1 > width), jobs are packed one minute apart from the anchor and clamped
// at 59; ties are then nudged forward so targets stay distinct where the range
// allows. This is a best-effort spread — the goal is to break the exact-instant
// collision, not to guarantee uniqueness past minute 59.
func spreadMinutes(anchor, n, width int) []int {
	if n <= 0 {
		return nil
	}
	out := make([]int, n)
	out[0] = clampMinute(anchor)
	if n == 1 {
		return out
	}

	step := width / (n - 1)
	if step < 1 {
		step = 1
	}
	for i := 1; i < n; i++ {
		out[i] = clampMinute(anchor + step*i)
	}

	// Nudge any collisions/back-steps forward so targets are non-decreasing and
	// distinct while the 0-59 range still has room.
	for i := 1; i < n; i++ {
		if out[i] <= out[i-1] && out[i-1] < 59 {
			out[i] = out[i-1] + 1
		}
		out[i] = clampMinute(out[i])
	}
	return out
}

// clampMinute pins a minute into the legal 0-59 range.
func clampMinute(m int) int {
	if m < 0 {
		return 0
	}
	if m > 59 {
		return 59
	}
	return m
}

// replaceMinuteToken returns line with only its first whitespace-delimited
// token (the minute field) replaced by the decimal form of newMin, preserving
// the original leading whitespace and the rest of the line verbatim. This is
// what lets a rewrite keep comments, spacing, and the command exactly as the
// user wrote them.
func replaceMinuteToken(line string, newMin int) string {
	// Capture and re-emit leading whitespace, then swap the first field.
	i := 0
	for i < len(line) && (line[i] == ' ' || line[i] == '\t') {
		i++
	}
	lead := line[:i]
	rest := line[i:]

	// rest begins with the minute token; find its end.
	j := 0
	for j < len(rest) && rest[j] != ' ' && rest[j] != '\t' {
		j++
	}
	tail := rest[j:] // whitespace + remaining fields/command
	return fmt.Sprintf("%s%d%s", lead, newMin, tail)
}

// Rewrite applies a Plan to the original crontab text and returns the new text.
// Only lines named by a Move are changed (their minute token swapped); every
// other line — comments, blanks, env assignments, untouched jobs — is preserved
// byte-for-byte, including the original trailing-newline style. src must be the
// same text the Plan was produced from so line numbers line up.
func (p Plan) Rewrite(src string) string {
	// Index moves by line number for O(1) lookup.
	byLine := map[int]Move{}
	for _, h := range p.Herds {
		for _, m := range h.Moves {
			if m.FromMinute != m.ToMinute {
				byLine[m.Line] = m
			}
		}
	}

	lines := strings.Split(src, "\n")
	for idx := range lines {
		lineNo := idx + 1
		if m, ok := byLine[lineNo]; ok {
			lines[idx] = m.Rewritten
		}
	}
	return strings.Join(lines, "\n")
}
