package dialect

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/rwrife/cron-goblin/internal/parse"
)

// FromEventBridge translates AWS rule cron to standard cron. Calendar-relative
// L/W/# and restricted years cannot survive the standard-cron intermediate.
func FromEventBridge(expr string) (string, error) {
	text := strings.TrimSpace(expr)
	if strings.HasPrefix(text, "cron(") && strings.HasSuffix(text, ")") {
		text = text[5 : len(text)-1]
	}
	fields := strings.Fields(text)
	if len(fields) != 6 {
		return "", &ConvertError{Dialect: EventBridge, Msg: "expected 6 fields (min hour day-of-month month day-of-week year), optionally wrapped in cron()"}
	}
	if (fields[2] == "?") == (fields[4] == "?") {
		return "", &ConvertError{Dialect: EventBridge, Field: "day-of-month/day-of-week", Msg: "requires `?` in exactly one day field; both specified (including two `*` fields) is invalid"}
	}
	// Unlike some Quartz deployments, AWS never accepts weekday zero. Validate
	// before using the shared renumbering, which deliberately tolerates zero.
	if fields[4] != "?" {
		for _, term := range strings.Split(fields[4], ",") {
			body := strings.SplitN(term, "/", 2)[0]
			for _, token := range strings.Split(body, "-") {
				if n, err := strconv.Atoi(token); err == nil && (n < 1 || n > 7) {
					return "", &ConvertError{Dialect: EventBridge, Field: "day-of-week", Msg: "weekdays must be 1-7 (SUN-SAT), never 0"}
				}
			}
			if strings.HasSuffix(term, "/") {
				return "", &ConvertError{Dialect: EventBridge, Field: "day-of-week", Msg: "missing step after '/'"}
			}
		}
	}
	cron, err := FromQuartz("0 " + strings.Join(fields, " "))
	if err != nil {
		return "", eventBridgeError(err)
	}
	sched, err := parse.Parse(cron)
	if err != nil {
		return "", &ConvertError{Dialect: EventBridge, Msg: err.Error()}
	}
	return sched.Raw, nil
}

// ToEventBridge emits the six fields that go inside AWS's cron() wrapper. Timezone
// interpretation remains the caller's responsibility: AWS rules use UTC only.
func ToEventBridge(expr string) (string, error) {
	sched, err := parse.Parse(expr)
	if err != nil {
		return "", &ConvertError{Dialect: EventBridge, Msg: err.Error()}
	}
	fields := strings.Fields(sched.Raw)
	// A cron range ending in the Sunday alias 7 can wrap after renumbering.
	// Expand that field first rather than emitting an inverted AWS range.
	if strings.Contains(fields[4], "7") {
		values := make([]string, len(sched.DOW.Values))
		for i, value := range sched.DOW.Values {
			values[i] = strconv.Itoa(value)
		}
		fields[4] = strings.Join(values, ",")
	}
	quartz, err := ToQuartz(strings.Join(fields, " "))
	if err != nil {
		return "", eventBridgeError(err)
	}
	return strings.TrimPrefix(quartz, "0 ") + " *", nil
}

func eventBridgeError(err error) error {
	var ce *ConvertError
	if errors.As(err, &ce) {
		return &ConvertError{Dialect: EventBridge, Field: ce.Field, Lossy: ce.Lossy,
			Msg: strings.ReplaceAll(ce.Msg, "Quartz", "EventBridge")}
	}
	return fmt.Errorf("eventbridge conversion: %w", err)
}
