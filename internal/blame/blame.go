// Package blame annotates a crontab inline, git-blame style. Where lint
// surfaces problems, blame explains *everything*: it walks a crontab line by
// line, preserving original order, comments, and blank lines, and attaches to
// each schedule-bearing line a plain-English description plus its next fire
// time.
//
// Like the other logic packages, blame is pure and humorless: it produces
// structured Rows. Presentation (alignment, the trailing "# ..." comment) and
// the grumpy persona live in cmd/goblin. This keeps the annotation logic
// deterministic and trivially testable.
package blame

import (
	"bufio"
	"io"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
)

// Kind classifies a source line so callers can render it appropriately.
type Kind int

const (
	// KindOther is a non-schedule line echoed untouched: a blank line, a
	// comment, or an environment assignment (SHELL=, PATH=, MAILTO=...).
	KindOther Kind = iota
	// KindSchedule is a parseable schedule line; English and Next are populated.
	KindSchedule
	// KindUnparseable is a line that looked like it wanted to be a schedule but
	// could not be parsed; it is passed through with a note.
	KindUnparseable
)

// Row is one annotated crontab line. Line is the 1-based source line number.
// Raw is the original text (sans trailing newline). For KindSchedule, Schedule,
// Command, English and Next are populated; Dead is true when the expression can
// never fire (Next is then the zero Time). For KindUnparseable, Note explains
// why. Kind selects which of these fields are meaningful.
type Row struct {
	Line    int
	Raw     string
	Kind    Kind
	Command string
	English string
	Next    time.Time
	Dead    bool
	Note    string
}

// Annotate walks the crontab in r and returns one Row per source line, in
// order, preserving blanks and comments. Schedule lines are described via
// internal/explain and get their next fire time (strictly after `now`) via
// internal/nextrun, evaluated in loc (nil → UTC). now lets tests pin the clock;
// production callers pass time.Now().
func Annotate(r io.Reader, now time.Time, loc *time.Location) ([]Row, error) {
	if loc == nil {
		loc = time.UTC
	}
	var rows []Row
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		trimmed := strings.TrimSpace(raw)

		// Blanks, comments, and environment assignments pass through untouched.
		if trimmed == "" || strings.HasPrefix(trimmed, "#") || isEnvAssignment(trimmed) {
			rows = append(rows, Row{Line: lineNo, Raw: raw, Kind: KindOther})
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 5 {
			rows = append(rows, Row{
				Line: lineNo,
				Raw:  raw,
				Kind: KindUnparseable,
				Note: "not a schedule (fewer than 5 fields)",
			})
			continue
		}

		exprText := strings.Join(fields[:5], " ")
		command := strings.TrimSpace(strings.Join(fields[5:], " "))
		sched, perr := parse.Parse(exprText)
		if perr != nil {
			rows = append(rows, Row{
				Line: lineNo,
				Raw:  raw,
				Kind: KindUnparseable,
				Note: perr.Error(),
			})
			continue
		}

		row := Row{
			Line:    lineNo,
			Raw:     raw,
			Kind:    KindSchedule,
			Command: command,
			English: explain.Explain(sched),
		}
		next, nerr := nextrun.Next(sched, now, loc)
		if nerr != nil {
			row.Dead = true
		} else {
			row.Next = next
		}
		rows = append(rows, row)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return rows, nil
}

// isEnvAssignment reports whether a (trimmed) crontab line is a NAME=value
// environment assignment rather than a schedule. It mirrors the same check in
// internal/lint so blame and lint agree on what counts as a schedule line.
func isEnvAssignment(line string) bool {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return false
	}
	name := line[:eq]
	if strings.IndexFunc(name, func(r rune) bool { return r == ' ' || r == '\t' }) >= 0 {
		return false
	}
	for _, r := range name {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}
