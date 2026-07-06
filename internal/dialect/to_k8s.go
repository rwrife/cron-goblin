// to_k8s.go implements standard 5-field cron -> Kubernetes CronJob schedule.
// A CronJob's `.spec.schedule` is standard 5-field cron, so this direction is a
// validated passthrough with one convenience: exact matches for the common
// robfig/cron `@`-macros are collapsed to the macro form a CronJob also accepts,
// so the output reads the way people write manifests.
//
//	goblin convert --from cron --to k8s "*/5 * * * *"  -> */5 * * * *
//	goblin convert --from cron --to k8s "0 0 * * *"    -> @daily
//	goblin convert --from cron --to k8s "0 0 * * 0"    -> @weekly
//	goblin convert --from cron --to k8s "0 0 1 1 *"    -> @yearly
//
// The macro collapse is opt-in via --k8s-macros on the command; by default the
// literal 5-field form is emitted so the result is unambiguous and diffs stay
// stable. Either way the value is one the apiserver accepts.
package dialect

import "strings"

// cronToK8sMacro maps a canonical 5-field cron expression to the robfig/cron
// macro a Kubernetes CronJob accepts. It is the inverse of the recognized
// entries in k8sMacros (see k8s.go). Only fully-canonical forms match, so the
// collapse never changes a schedule's meaning. @annually/@midnight are omitted
// deliberately: they are aliases of @yearly/@daily, and we emit the primary
// spelling.
var cronToK8sMacro = map[string]string{
	"0 0 1 1 *": "@yearly",
	"0 0 1 * *": "@monthly",
	"0 0 * * 0": "@weekly",
	"0 0 * * *": "@daily",
	"0 * * * *": "@hourly",
}

// ToK8s validates a standard 5-field cron expression as a Kubernetes CronJob
// schedule and returns it. Callers should pass a shape-valid, normalized cron
// string (convert.go round-trips through parse.Parse first). When macros is
// true and the expression is an exact canonical match for a robfig/cron macro,
// the macro form (@daily, @hourly, ...) is returned instead of the 5-field
// spelling; otherwise the (trimmed) 5-field expression is returned unchanged.
//
// A CronJob schedule is standard cron, so there is nothing to refuse here — the
// upstream parse.Parse has already rejected anything malformed, and Quartz-only
// specials never reach this point because they cannot be produced by the parser.
func ToK8s(expr string, macros bool) (string, error) {
	trimmed := strings.TrimSpace(expr)
	if trimmed == "" {
		return "", &ConvertError{Dialect: K8s, Msg: "empty schedule"}
	}
	if macros {
		if macro, ok := cronToK8sMacro[trimmed]; ok {
			return macro, nil
		}
	}
	return trimmed, nil
}
