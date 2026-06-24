// rule_dead.go implements the dead-expression rule: a schedule that can never
// fire is dead code in your crontab. The canonical example is `0 0 30 2 *`
// (February 30th), but any combination with no matching date within the engine
// horizon qualifies (e.g. an impossible month/day pairing).
package lint

import (
	"fmt"
	"time"

	"github.com/rwrife/cron-goblin/internal/nextrun"
)

// deadExpressionRule flags entries whose schedule never fires. It defers the
// actual "does it fire?" decision to internal/nextrun, the trusted engine, so
// lint and `goblin next` always agree about what is dead.
type deadExpressionRule struct{}

// Name returns the stable rule code.
func (deadExpressionRule) Name() string { return "dead-expression" }

// Check returns an error-severity Finding for every parseable entry that the
// nextrun engine reports as never firing. Entries that failed to parse are
// skipped here — the malformed-line error is raised separately in Lint.
func (deadExpressionRule) Check(entries []Entry) []Finding {
	var out []Finding
	// A single reference instant keeps the check deterministic across the run.
	from := time.Now()

	for _, e := range entries {
		if e.ParseErr != nil {
			continue
		}
		if _, err := nextrun.Next(e.Schedule, from, time.UTC); err == nextrun.ErrNeverFires {
			out = append(out, Finding{
				Rule:     "dead-expression",
				Severity: SeverityError,
				Message: fmt.Sprintf(
					"`%s` never fires — no matching date exists (checked %d years ahead); this line is dead code",
					e.Schedule.Raw, nextrun.DefaultHorizonYears),
				Lines: []int{e.Line},
			})
		}
	}
	return out
}
