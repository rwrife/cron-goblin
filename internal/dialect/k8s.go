// k8s.go implements the Kubernetes CronJob -> standard 5-field cron slice of the
// dialect adapter. A Kubernetes CronJob's `.spec.schedule` is *almost* plain
// Unix cron, but not exactly: it is parsed by the robfig/cron v3 library, which
// (a) accepts a handful of `@`-prefixed macro shorthands standard vixie-cron
// does not spell the same way, and (b) rejects the vixie-only `@reboot` event
// that has no meaning in a cluster. It also does not understand Quartz's
// `?`/`L`/`W`/`#` specials, which people routinely paste in from Java schedulers
// and then wonder why their CronJob never applies.
//
// This file is the "validate and normalize a k8s schedule" converter. It keeps
// the same "convert or honestly refuse" contract as FromQuartz / FromSystemd:
//
//	goblin convert --from k8s "@daily"          -> 0 0 * * *
//	goblin convert --from k8s "@hourly"         -> 0 * * * *
//	goblin convert --from k8s "*/5 * * * *"     -> */5 * * * *   (already cron)
//	goblin convert --from k8s "@reboot"         -> error: k8s has no boot event
//	goblin convert --from k8s "0 0 12 ? * 6#3"  -> error: Quartz special, not k8s
//
// # Supported macros (robfig/cron v3, what k8s accepts)
//
//	@yearly (or @annually) -> 0 0 1 1 *
//	@monthly               -> 0 0 1 * *
//	@weekly                -> 0 0 * * 0
//	@daily (or @midnight)  -> 0 0 * * *
//	@hourly                -> 0 * * * *
//
// # Refused (returns *ConvertError)
//
//   - @reboot: vixie-cron only; a Kubernetes CronJob has no boot event, so the
//     apiserver rejects it. This is one of the most common k8s cron mistakes.
//   - @every <duration>: robfig accepts it, but it is not a cron schedule and
//     the CronJob controller does not honor it; refuse rather than mistranslate.
//   - Quartz-only specials (`?`, `L`, `W`, `#`) and 6/7-field expressions: not
//     valid k8s schedules; point the user at `--from quartz` instead.
//
// Everything else is handed to the trusted parser as ordinary 5-field cron, so
// out-of-range values and malformed fields still get the normal diagnostics.
package dialect

import (
	"errors"
	"fmt"
	"strings"
)

// k8sMacros maps the robfig/cron v3 predefined schedule macros that a Kubernetes
// CronJob accepts to their standard 5-field cron equivalent. These expansions
// match the robfig/cron documentation the CronJob controller relies on.
//
// Note the weekday encoding: @weekly is Sunday, spelled "0" here (cron's Sunday)
// to match robfig's `0 0 0 * * 0` semantics dropped to 5 fields.
var k8sMacros = map[string]string{
	"@yearly":   "0 0 1 1 *",
	"@annually": "0 0 1 1 *",
	"@monthly":  "0 0 1 * *",
	"@weekly":   "0 0 * * 0",
	"@daily":    "0 0 * * *",
	"@midnight": "0 0 * * *",
	"@hourly":   "0 * * * *",
}

// FromK8s validates and normalizes a Kubernetes CronJob schedule into a standard
// 5-field cron string. Like the other converters it returns the dialect-shaped
// translation and leaves final range validation to parse.Parse, so callers
// should still parse the result. It returns a *ConvertError for schedules a
// Kubernetes CronJob does not accept — the vixie-only `@reboot`, robfig's
// non-cron `@every` form, and Quartz-only specials / field counts — so the CLI
// can explain *why* the manifest would be rejected instead of guessing.
//
// A plain 5-field cron expression passes straight through (normalized by the
// parser downstream), preserving the pre-existing "k8s is cron" behavior.
func FromK8s(expr string) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", &ConvertError{Dialect: K8s, Msg: "empty schedule"}
	}

	// Macro shorthands (the `@`-prefixed forms). robfig/cron v3, which the
	// CronJob controller uses, understands these; standard 5-field cron does not,
	// so we expand them here rather than handing them to parse.Parse (which would
	// reject `@daily` as "expected 5 fields, got 1").
	if strings.HasPrefix(trimmed, "@") {
		return expandK8sMacro(trimmed)
	}

	// Guard against Quartz constructs that get pasted into k8s manifests. A k8s
	// schedule is 5-field cron: 6 or 7 fields means someone brought a Java Quartz
	// expression, and `?`/`L`/`W`/`#` are Quartz specials the CronJob parser does
	// not accept. Catch these with a k8s-flavored pointer to `--from quartz`
	// before the generic 5-field parser emits a less helpful field error.
	if err := rejectNonK8sShape(trimmed); err != nil {
		return "", err
	}

	// Otherwise it is (meant to be) ordinary 5-field cron. Return it untouched;
	// convert.go round-trips it through parse.Parse, which validates ranges and
	// normalizes whitespace, so this stays the single source of truth for shape.
	return trimmed, nil
}

// expandK8sMacro resolves an `@`-prefixed schedule macro. Recognized robfig/cron
// macros expand to 5-field cron; `@reboot` and `@every` are refused with a
// specific reason because a Kubernetes CronJob does not honor them.
func expandK8sMacro(tok string) (string, error) {
	lower := strings.ToLower(tok)

	if cron, ok := k8sMacros[lower]; ok {
		return cron, nil
	}

	// @reboot is a vixie-cron event ("run once at startup"). A Kubernetes CronJob
	// is time-scheduled and has no boot concept, so the apiserver rejects it.
	// This is the single most common k8s cron mistake, so name it explicitly.
	if lower == "@reboot" {
		return "", &ConvertError{
			Dialect: K8s,
			Field:   "schedule",
			Msg:     "@reboot is vixie-cron only; a Kubernetes CronJob has no boot event and will reject it",
		}
	}

	// @every <duration> is a robfig extension, but it is not a cron schedule and
	// the CronJob controller does not accept it. Refuse instead of guessing an
	// interval that would silently differ.
	if lower == "@every" || strings.HasPrefix(lower, "@every ") {
		return "", &ConvertError{
			Dialect: K8s,
			Field:   "schedule",
			Msg:     "@every <duration> is not a valid CronJob schedule; use a cron expression (e.g. */5 * * * *)",
		}
	}

	return "", &ConvertError{
		Dialect: K8s,
		Field:   "schedule",
		Msg: fmt.Sprintf("unknown schedule macro %q (supported: @yearly, @annually, @monthly, @weekly, @daily, @midnight, @hourly)",
			tok),
	}
}

// rejectNonK8sShape returns a *ConvertError when a non-macro schedule is clearly
// not valid k8s cron: it has the wrong number of fields (6/7, i.e. Quartz) or
// contains a Quartz-only special character (`?`, `L`, `W`, `#`). The goal is a
// helpful, k8s-specific redirect to `--from quartz` rather than the generic
// field-count error the standard parser would otherwise produce.
func rejectNonK8sShape(expr string) error {
	fields := strings.Fields(expr)
	if len(fields) == 6 || len(fields) == 7 {
		return &ConvertError{
			Dialect: K8s,
			Field:   "schedule",
			Msg: fmt.Sprintf("got %d fields; a Kubernetes CronJob schedule is standard 5-field cron. "+
				"If this is a Quartz expression, use --from quartz", len(fields)),
		}
	}

	// Scan for Quartz-only specials. Only the day-of-month (index 2) and
	// day-of-week (index 4) fields carry these in Quartz, so those are the ones
	// inspected. `?` is checked directly (it is never part of a standard field);
	// `L`/`W`/`#` reuse the Quartz converter's token-aware detection so weekday
	// names like WED/FRI do not trip the `W`/`L` heuristics. Any hit is reported
	// under the k8s dialect with a pointer at `--from quartz`.
	type slot struct {
		idx  int
		name string
	}
	for _, s := range []slot{{2, "day-of-month"}, {4, "day-of-week"}} {
		if s.idx >= len(fields) {
			continue
		}
		raw := fields[s.idx]
		if strings.Contains(raw, "?") {
			return quartzSpecialInK8s("?", s.name)
		}
		// rejectQuartzSpecials returns a *ConvertError naming the L/W/# special when
		// it finds one, using the same rules as the Quartz converter. We only need
		// to know *that* one was found and in which field, then reword it for k8s.
		if err := rejectQuartzSpecials(raw, s.name); err != nil {
			var ce *ConvertError
			special := "L/W/#"
			if errors.As(err, &ce) {
				special = quartzSpecialName(ce.Msg)
			}
			return quartzSpecialInK8s(special, s.name)
		}
	}

	return nil
}

// quartzSpecialInK8s builds the k8s-dialect refusal for a Quartz-only special
// character found in a schedule, pointing the user at the right converter.
func quartzSpecialInK8s(special, field string) error {
	return &ConvertError{
		Dialect: K8s,
		Field:   field,
		Msg: fmt.Sprintf("%q is a Quartz-only special a Kubernetes CronJob does not accept; use --from quartz",
			special),
	}
}

// quartzSpecialName extracts which special character (`L`, `W`, or `#`) a Quartz
// ConvertError message referred to, so the k8s refusal can name the same one.
// It falls back to a compact list when the message shape is unrecognized.
func quartzSpecialName(msg string) string {
	switch {
	case strings.Contains(msg, "Quartz # "):
		return "#"
	case strings.Contains(msg, "Quartz L "):
		return "L"
	case strings.Contains(msg, "Quartz W "):
		return "W"
	default:
		return "L/W/#"
	}
}
