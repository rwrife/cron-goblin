// Package lintrules loads user-authored, declarative lint rules from a
// directory and adapts them to the internal/lint Rule interface. It lets orgs
// encode local scheduling policy — "no jobs during the 02:00–04:00 backup
// window", "batch jobs must not run every minute" — without shipping code.
//
// # Why declarative
//
// Rules are data (TOML), not code. That keeps cron-goblin's promise: offline,
// deterministic, and safe. A rule file can never execute anything; the worst a
// malformed rule can do is fail to load, and we make that fail loudly (naming
// the offending path) rather than silently drop it.
//
// # File format
//
// A rules directory contains one or more `*.toml` files. Each file holds one
// or more `[[rule]]` tables:
//
//	[[rule]]
//	name     = "no-backup-window"
//	severity = "error"                 # info | warning | error
//	message  = "jobs must not fire during the 02:00-04:00 backup window"
//	# --- match conditions (a job must satisfy ALL present conditions) ---
//	forbid_hours = [2, 3]              # flag if the job ever fires in these hours
//	# fields = { minute = "0", dow = "1-5" }  # exact cron-field text match
//	# max_per_hour = 4                 # flag if it fires more than N times/hour
//
// A [[rule]] with no conditions matches nothing (and is rejected at load, so a
// typo can't produce a rule that fires on every job).
//
// # Discovery
//
// Callers pass an explicit directory (the CLI's --rules-dir) or rely on the
// documented defaults (see DefaultDirs). LoadDir reads every *.toml file in
// lexical order so findings are stable. The adapted rules merge with the
// built-ins in internal/lint and appear in reports under their own name.
package lintrules

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/rwrife/cron-goblin/internal/nextrun"
)

// fileDoc is the top-level shape of a single rules TOML file: a list of rule
// tables under the `rule` key ([[rule]]).
type fileDoc struct {
	Rules []ruleSpec `toml:"rule"`
}

// ruleSpec is the on-disk form of one declarative rule. Fields map directly to
// the documented TOML keys. Pointers/slices distinguish "absent" from a
// zero-value so we can require at least one real condition.
type ruleSpec struct {
	Name        string            `toml:"name"`
	Severity    string            `toml:"severity"`
	Message     string            `toml:"message"`
	ForbidHours []int             `toml:"forbid_hours"`
	Fields      map[string]string `toml:"fields"`
	MaxPerHour  *int              `toml:"max_per_hour"`
}

// declRule is a compiled ruleSpec that satisfies lint.Rule. Compilation
// validates the spec once (at load) so Check is pure and cheap.
type declRule struct {
	name     string
	severity lint.Severity
	message  string

	forbidHours map[int]bool
	fields      map[string]string // normalized field name -> expected raw text
	maxPerHour  *int
}

// Name returns the stable rule code (the file's declared name).
func (r *declRule) Name() string { return r.name }

// sampleHorizonRuns is how many upcoming fire times a window/cadence check
// examines. Enough to observe a full day of a frequent schedule while staying
// cheap and deterministic.
const sampleHorizonRuns = 64

// Check evaluates the rule against each parseable entry. A job is flagged when
// it satisfies every present condition (logical AND). Entries that failed to
// parse are skipped — malformed *jobs* are the built-in parser's concern, not a
// policy rule's.
func (r *declRule) Check(entries []lint.Entry) []lint.Finding {
	from := time.Now()
	var out []lint.Finding
	for _, e := range entries {
		if e.ParseErr != nil {
			continue
		}
		if !r.matchFields(e) {
			continue
		}

		// Window / cadence conditions need sampled fire times; compute lazily
		// and only when such a condition is present.
		var runs []time.Time
		if len(r.forbidHours) > 0 || r.maxPerHour != nil {
			runs = nextrun.NextN(e.Schedule, from, sampleHorizonRuns, time.UTC)
		}

		if !r.matchForbidHours(runs) {
			continue
		}
		if !r.matchMaxPerHour(runs) {
			continue
		}

		out = append(out, lint.Finding{
			Rule:     r.name,
			Severity: r.severity,
			Message:  fmt.Sprintf("`%s`: %s", e.Schedule.Raw, r.message),
			Lines:    []int{e.Line},
		})
	}
	return out
}

// matchFields reports whether the entry's cron fields equal every expected
// field text in the rule. An empty field set matches (the condition is absent).
func (r *declRule) matchFields(e lint.Entry) bool {
	if len(r.fields) == 0 {
		return true
	}
	for name, want := range r.fields {
		if fieldRaw(e, name) != want {
			return false
		}
	}
	return true
}

// matchForbidHours reports whether the rule's forbidden-hours condition is
// satisfied (i.e. at least one sampled fire lands in a forbidden hour). Absent
// condition → true.
func (r *declRule) matchForbidHours(runs []time.Time) bool {
	if len(r.forbidHours) == 0 {
		return true
	}
	for _, t := range runs {
		if r.forbidHours[t.Hour()] {
			return true
		}
	}
	return false
}

// matchMaxPerHour reports whether any single clock hour in the sampled horizon
// contains more than the allowed number of fires. Absent condition → true.
func (r *declRule) matchMaxPerHour(runs []time.Time) bool {
	if r.maxPerHour == nil {
		return true
	}
	counts := map[string]int{}
	for _, t := range runs {
		key := t.Format("2006-01-02T15")
		counts[key]++
		if counts[key] > *r.maxPerHour {
			return true
		}
	}
	return false
}

// fieldRaw returns the raw text of a named cron field on an entry's schedule.
// Names are the documented lowercase field labels; an unknown name yields "".
func fieldRaw(e lint.Entry, name string) string {
	switch normalizeFieldName(name) {
	case "minute":
		return e.Schedule.Minute.Raw
	case "hour":
		return e.Schedule.Hour.Raw
	case "dom":
		return e.Schedule.DOM.Raw
	case "month":
		return e.Schedule.Month.Raw
	case "dow":
		return e.Schedule.DOW.Raw
	default:
		return ""
	}
}

// normalizeFieldName maps user-facing aliases to the canonical field label so
// `dayofweek`, `weekday`, and `dow` all resolve the same way.
func normalizeFieldName(name string) string {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "minute", "min":
		return "minute"
	case "hour", "hr":
		return "hour"
	case "dom", "dayofmonth", "day", "monthday":
		return "dom"
	case "month", "mon":
		return "month"
	case "dow", "dayofweek", "weekday":
		return "dow"
	default:
		return ""
	}
}

// compile validates a ruleSpec and turns it into a runnable declRule. It
// returns an error (naming the rule) for any problem: missing name/message,
// unknown severity, out-of-range hours, unknown field names, or a rule with no
// conditions at all. Loud failure is the whole point — a policy rule that
// silently matches nothing (or everything) is a footgun.
func compile(spec ruleSpec) (*declRule, error) {
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		return nil, fmt.Errorf("rule is missing a name")
	}
	if strings.TrimSpace(spec.Message) == "" {
		return nil, fmt.Errorf("rule %q is missing a message", name)
	}

	sev, err := parseSeverity(spec.Severity)
	if err != nil {
		return nil, fmt.Errorf("rule %q: %w", name, err)
	}

	r := &declRule{name: name, severity: sev, message: strings.TrimSpace(spec.Message)}

	if len(spec.ForbidHours) > 0 {
		r.forbidHours = map[int]bool{}
		for _, h := range spec.ForbidHours {
			if h < 0 || h > 23 {
				return nil, fmt.Errorf("rule %q: forbid_hours value %d out of range 0-23", name, h)
			}
			r.forbidHours[h] = true
		}
	}

	if len(spec.Fields) > 0 {
		r.fields = map[string]string{}
		for k, v := range spec.Fields {
			canon := normalizeFieldName(k)
			if canon == "" {
				return nil, fmt.Errorf("rule %q: unknown field %q (want minute/hour/dom/month/dow)", name, k)
			}
			r.fields[canon] = strings.TrimSpace(v)
		}
	}

	if spec.MaxPerHour != nil {
		if *spec.MaxPerHour < 0 {
			return nil, fmt.Errorf("rule %q: max_per_hour must be >= 0", name)
		}
		v := *spec.MaxPerHour
		r.maxPerHour = &v
	}

	if len(r.forbidHours) == 0 && len(r.fields) == 0 && r.maxPerHour == nil {
		return nil, fmt.Errorf("rule %q has no conditions (add forbid_hours, fields, or max_per_hour)", name)
	}
	return r, nil
}

// parseSeverity maps the TOML severity string to a lint.Severity. Empty
// defaults to warning (the common case for a policy nudge). Unknown values are
// rejected so a typo can't silently downgrade a rule.
func parseSeverity(s string) (lint.Severity, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "warning", "warn":
		return lint.SeverityWarning, nil
	case "info":
		return lint.SeverityInfo, nil
	case "error", "err":
		return lint.SeverityError, nil
	default:
		return 0, fmt.Errorf("unknown severity %q (want info/warning/error)", s)
	}
}

// LoadDir reads every `*.toml` file in dir (non-recursively, lexical order) and
// returns the compiled rules as lint.Rules. A missing directory is not an error
// — it yields no rules — so the CLI can point at a default location that may
// not exist. Any file that fails to read, parse, or compile returns an error
// naming the offending path; nothing is silently ignored.
//
// Rule names must be unique across the whole directory; a duplicate is an error
// (two rules sharing a name would make findings ambiguous).
func LoadDir(dir string) ([]lint.Rule, error) {
	if dir == "" {
		return nil, nil
	}
	info, err := os.Stat(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("rules dir %q: %w", dir, err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("rules dir %q is not a directory", dir)
	}

	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("rules dir %q: %w", dir, err)
	}

	var paths []string
	for _, e := range ents {
		if e.IsDir() {
			continue
		}
		if strings.EqualFold(filepath.Ext(e.Name()), ".toml") {
			paths = append(paths, filepath.Join(dir, e.Name()))
		}
	}
	sort.Strings(paths)

	seen := map[string]string{} // rule name -> source path
	var rules []lint.Rule
	for _, p := range paths {
		fileRules, err := loadFile(p)
		if err != nil {
			return nil, err
		}
		for _, r := range fileRules {
			if prev, dup := seen[r.Name()]; dup {
				return nil, fmt.Errorf("%s: duplicate rule name %q (already defined in %s)", p, r.Name(), prev)
			}
			seen[r.Name()] = p
			rules = append(rules, r)
		}
	}
	return rules, nil
}

// loadFile reads and compiles one rules TOML file. Errors name the path so a
// malformed rule points the user straight at it.
func loadFile(path string) ([]*declRule, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading rule file %q: %w", path, err)
	}
	var doc fileDoc
	md, err := toml.Decode(string(data), &doc)
	if err != nil {
		return nil, fmt.Errorf("parsing rule file %q: %w", path, err)
	}
	if undecoded := md.Undecoded(); len(undecoded) > 0 {
		keys := make([]string, len(undecoded))
		for i, k := range undecoded {
			keys[i] = k.String()
		}
		sort.Strings(keys)
		return nil, fmt.Errorf("rule file %q: unknown key(s): %s", path, strings.Join(keys, ", "))
	}
	if len(doc.Rules) == 0 {
		return nil, fmt.Errorf("rule file %q: no [[rule]] tables found", path)
	}

	out := make([]*declRule, 0, len(doc.Rules))
	for i, spec := range doc.Rules {
		r, err := compile(spec)
		if err != nil {
			return nil, fmt.Errorf("rule file %q (rule #%d): %w", path, i+1, err)
		}
		out = append(out, r)
	}
	return out, nil
}

// DefaultDirs returns the documented auto-discovery locations for user rules,
// most-specific first: a project-local `.goblin/rules`, then the user config
// dir `<config>/goblin/rules` (honoring XDG_CONFIG_HOME via os.UserConfigDir).
// The CLI walks these when --rules-dir is not given and plugins aren't disabled.
// Non-existent directories are harmless (LoadDir treats them as empty).
func DefaultDirs() []string {
	var dirs []string
	dirs = append(dirs, filepath.Join(".goblin", "rules"))
	if cfg, err := os.UserConfigDir(); err == nil && cfg != "" {
		dirs = append(dirs, filepath.Join(cfg, "goblin", "rules"))
	}
	return dirs
}

// LoadAll loads and concatenates rules from several directories in order,
// enforcing global name-uniqueness across all of them. It's the entry point the
// CLI uses for default discovery (DefaultDirs) so a project rule and a
// user-level rule can't silently share a name.
func LoadAll(dirs []string) ([]lint.Rule, error) {
	seen := map[string]bool{}
	var all []lint.Rule
	for _, d := range dirs {
		rules, err := LoadDir(d)
		if err != nil {
			return nil, err
		}
		for _, r := range rules {
			if seen[r.Name()] {
				return nil, fmt.Errorf("duplicate rule name %q across rules directories", r.Name())
			}
			seen[r.Name()] = true
			all = append(all, r)
		}
	}
	return all, nil
}
