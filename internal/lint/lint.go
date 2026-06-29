// Package lint treats a crontab like a program and checks it. It reads a
// crontab file (or stdin), parses each schedule via internal/parse, and runs a
// set of pluggable rules over the result: dead expressions that never fire,
// jobs that fire too often, and groups of jobs that pile onto the same minute
// (the "thundering herd" seed).
//
// Like the rest of cron-goblin's logic packages, lint is pure and humorless:
// it produces structured Findings. The grumpy commentary lives in
// internal/goblin and the presentation in cmd/goblin. That separation keeps
// the rules deterministic and trivially testable.
//
// # Adding a rule
//
// A rule is any value implementing Rule. Each lives in its own file
// (rule_*.go) and is registered in DefaultRules. A rule receives the whole set
// of parsed crontab Entries so cross-job checks (like collisions) are natural,
// and returns zero or more Findings. Rules must not mutate the entries.
package lint

import (
	"bufio"
	"io"
	"sort"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// Severity ranks how much a Finding should worry you. Higher is worse. The
// ordering matters: Report.Worst and the --ci exit code key off it.
type Severity int

const (
	// SeverityInfo is a neutral observation; nothing is wrong.
	SeverityInfo Severity = iota
	// SeverityWarning is a likely-bad smell (too frequent, colliding) that the
	// user probably wants to know about but that is still valid cron.
	SeverityWarning
	// SeverityError is a definite defect, e.g. an expression that can never
	// fire. The crontab line is effectively dead code.
	SeverityError
)

// severityName maps a Severity to its lowercase label, used in human output
// and the stable JSON report.
var severityName = map[Severity]string{
	SeverityInfo:    "info",
	SeverityWarning: "warning",
	SeverityError:   "error",
}

// String returns the lowercase severity label ("info"/"warning"/"error").
func (s Severity) String() string {
	if name, ok := severityName[s]; ok {
		return name
	}
	return "info"
}

// Entry is one schedule-bearing line from a crontab. Line is the 1-based source
// line number (handy for "crontab:12" style diagnostics). Command is whatever
// followed the 5 cron fields, preserved verbatim so reports can point at the
// offending job. Schedule is the parsed form; ParseErr is non-nil when the
// cron fields on this line could not be parsed, in which case Schedule is the
// zero value.
type Entry struct {
	Line     int
	Raw      string // the full original line (sans trailing newline)
	Schedule parse.Schedule
	Command  string
	ParseErr error
}

// Finding is a single lint result tied to a rule and (optionally) one or more
// crontab lines. It is the structured currency rules produce and the report
// renders.
type Finding struct {
	Rule     string   // stable rule code, e.g. "dead-expression"
	Severity Severity // how bad it is
	Message  string   // human explanation, no persona
	Lines    []int    // 1-based source lines this finding refers to (may be empty)
}

// Rule is a single lint check. Name is a short, stable, kebab-case code used in
// reports and JSON (so scripts can match on it). Check receives every parsed
// Entry — including ones that failed to parse, so a rule may inspect ParseErr —
// and returns any Findings it discovers. Rules must be deterministic and must
// not mutate the entries.
type Rule interface {
	Name() string
	Check(entries []Entry) []Finding
}

// DefaultRules is the rule set run by Lint when no custom set is supplied. New
// rules are added here (and in their own rule_*.go file). Order is cosmetic;
// findings are sorted before reporting.
//
// This timezone-agnostic set excludes the DST-danger rule, which only has
// meaning relative to a specific zone; see DefaultRulesTZ.
func DefaultRules() []Rule {
	return []Rule{
		deadExpressionRule{},
		tooFrequentRule{minMinutes: defaultMinInterval},
		collisionRule{},
	}
}

// DefaultRulesTZ is DefaultRules plus the DST-danger rule bound to loc, using
// `now` to pick which years' transitions to examine. When loc is nil or UTC the
// DST rule contributes nothing, so the result is equivalent to DefaultRules.
// LintWithLocation uses this to make `goblin lint --tz` zone-aware while the
// plain Lint entrypoint stays UTC-only and unchanged.
func DefaultRulesTZ(loc *time.Location, now time.Time) []Rule {
	return append(DefaultRules(), newDSTDangerRule(loc, now))
}

// Report is the aggregate result of linting a crontab: every Finding, plus the
// count of schedule-bearing entries examined. It is what cmd/goblin renders
// (human or JSON) and what the --ci exit code is derived from.
type Report struct {
	Findings []Finding
	Entries  int // number of schedule-bearing crontab lines parsed
}

// Worst returns the highest Severity across all findings, or SeverityInfo when
// there are none. The --ci flag uses this to decide the process exit code.
func (r Report) Worst() Severity {
	worst := SeverityInfo
	for _, f := range r.Findings {
		if f.Severity > worst {
			worst = f.Severity
		}
	}
	return worst
}

// Counts tallies findings by severity. Useful for summary lines and tests.
func (r Report) Counts() (info, warning, errors int) {
	for _, f := range r.Findings {
		switch f.Severity {
		case SeverityError:
			errors++
		case SeverityWarning:
			warning++
		default:
			info++
		}
	}
	return
}

// Lint parses crontab text and runs the given rules over it. Passing a nil or
// empty rules slice uses DefaultRules. Findings are returned sorted by first
// line, then by descending severity, then by rule name, so output is stable
// regardless of rule execution order.
//
// Lines that fail to parse become Entries with a non-nil ParseErr and a
// synthesized "parse-error" Finding so the user is told their crontab has a
// malformed line (rather than that line silently vanishing).
func Lint(r io.Reader, rules []Rule) (Report, error) {
	return lintWith(r, rules)
}

// LintWithLocation is Lint with DST-awareness: it runs DefaultRulesTZ(loc, now)
// so the DST-danger rule can flag schedules that fall in a daylight-saving
// transition window for loc. A nil or UTC loc makes it behave exactly like
// Lint with default rules (no DST findings). It is the entrypoint `goblin lint
// --tz` uses.
func LintWithLocation(r io.Reader, loc *time.Location, now time.Time) (Report, error) {
	return lintWith(r, DefaultRulesTZ(loc, now))
}

// lintWith is the shared core behind Lint and LintWithLocation: parse the
// crontab, surface malformed lines, run the rules, and sort. Lint delegates
// here after defaulting its rule set.
func lintWith(r io.Reader, rules []Rule) (Report, error) {
	entries, err := ParseCrontab(r)
	if err != nil {
		return Report{}, err
	}
	if len(rules) == 0 {
		rules = DefaultRules()
	}

	var findings []Finding
	for _, e := range entries {
		if e.ParseErr != nil {
			findings = append(findings, Finding{
				Rule:     "parse-error",
				Severity: SeverityError,
				Message:  "could not parse cron fields: " + e.ParseErr.Error(),
				Lines:    []int{e.Line},
			})
		}
	}
	for _, rule := range rules {
		findings = append(findings, rule.Check(entries)...)
	}
	sortFindings(findings)

	scheduled := 0
	for _, e := range entries {
		if e.ParseErr == nil {
			scheduled++
		}
	}
	return Report{Findings: findings, Entries: scheduled}, nil
}

// sortFindings orders findings deterministically: by first referenced line
// (findings with no lines sort last), then most-severe first, then rule name.
func sortFindings(fs []Finding) {
	sort.SliceStable(fs, func(i, j int) bool {
		li, lj := firstLine(fs[i]), firstLine(fs[j])
		if li != lj {
			return li < lj
		}
		if fs[i].Severity != fs[j].Severity {
			return fs[i].Severity > fs[j].Severity
		}
		return fs[i].Rule < fs[j].Rule
	})
}

// firstLine returns a finding's smallest line number, or a large sentinel when
// it references no lines (so such findings sort to the end).
func firstLine(f Finding) int {
	if len(f.Lines) == 0 {
		return 1 << 30
	}
	min := f.Lines[0]
	for _, l := range f.Lines[1:] {
		if l < min {
			min = l
		}
	}
	return min
}

// ParseCrontab reads a crontab from r and returns one Entry per
// schedule-bearing line. Blank lines, comment lines (starting with '#'), and
// environment-assignment lines (NAME=value with no spaces around '=') are
// skipped — they carry no schedule. Each remaining line is split into the five
// cron fields plus a trailing command; the fields are handed to parse.Parse.
//
// A line whose cron fields don't parse still yields an Entry, with ParseErr
// set, so callers can report it rather than dropping it silently.
func ParseCrontab(r io.Reader) ([]Entry, error) {
	var entries []Entry
	sc := bufio.NewScanner(r)
	// Crontab lines are short, but commands can be long; give the scanner room.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	lineNo := 0
	for sc.Scan() {
		lineNo++
		raw := sc.Text()
		trimmed := strings.TrimSpace(raw)

		if trimmed == "" || strings.HasPrefix(trimmed, "#") {
			continue
		}
		if isEnvAssignment(trimmed) {
			continue
		}

		fields := strings.Fields(trimmed)
		if len(fields) < 5 {
			// Too few tokens to be a schedule line; record the parse failure so
			// the user learns about the malformed line.
			_, perr := parse.Parse(trimmed)
			entries = append(entries, Entry{
				Line:     lineNo,
				Raw:      raw,
				ParseErr: perr,
			})
			continue
		}

		exprText := strings.Join(fields[:5], " ")
		command := strings.TrimSpace(strings.Join(fields[5:], " "))

		sched, perr := parse.Parse(exprText)
		entries = append(entries, Entry{
			Line:     lineNo,
			Raw:      raw,
			Schedule: sched,
			Command:  command,
			ParseErr: perr,
		})
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return entries, nil
}

// isEnvAssignment reports whether a (trimmed) crontab line is a NAME=value
// environment assignment rather than a schedule. Real crontabs use these for
// SHELL=, PATH=, MAILTO=, etc. We treat a line as an assignment when it has an
// '=' before any whitespace and a non-empty, identifier-like name on the left.
func isEnvAssignment(line string) bool {
	eq := strings.IndexByte(line, '=')
	if eq <= 0 {
		return false
	}
	// Anything before the '=' must not contain whitespace (otherwise it's a
	// schedule whose command happens to include '=').
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
