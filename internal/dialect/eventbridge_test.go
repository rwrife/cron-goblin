package dialect

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/rwrife/cron-goblin/internal/parse"
)

func TestEventBridgeGoldenRoundTrips(t *testing.T) {
	for _, tt := range []struct{ cron, aws string }{
		{"30 2 * * *", "30 2 * * ? *"},
		{"0 9 * * 1-5", "0 9 ? * 2-6 *"},
		{"15 10 1 JAN *", "15 10 1 JAN ? *"},
		{"*/15 * * * 0,6", "*/15 * ? * 1,7 *"},
		{"0 9 * * SUN", "0 9 ? * SUN *"},
		{"0 9 * * */2", "0 9 ? * */2 *"},
		{"0 9 * * 7", "0 9 ? * 1 *"},
	} {
		t.Run(tt.cron, func(t *testing.T) {
			got, err := ToEventBridge(tt.cron)
			if err != nil || got != tt.aws {
				t.Fatalf("ToEventBridge = %q, %v; want %q", got, err, tt.aws)
			}
			back, err := FromEventBridge("cron(" + got + ")")
			if err != nil {
				t.Fatal(err)
			}
			before, _ := parse.Parse(tt.cron)
			after, err := parse.Parse(back)
			if err != nil {
				t.Fatal(err)
			}
			for i, field := range before.Fields() {
				if !reflect.DeepEqual(field.Values, after.Fields()[i].Values) {
					t.Errorf("round trip changed %s: %v -> %v", field.Field, field.Values, after.Fields()[i].Values)
				}
			}
		})
	}
}

func TestEventBridgeRefusals(t *testing.T) {
	for _, tt := range []struct{ expr, diagnostic string }{
		{"0 9 * * * *", "exactly one"},
		{"0 9 ? * ? *", "exactly one"},
		{"0 9 15 * 2 *", "exactly one"},
		{"0 9 ? * 0 *", "1-7"},
		{"0 9 ? * 8 *", "1-7"},
		{"0 9 ? * 2/ *", "missing step"},
		{"0 9 ? * 2//3 *", "day-of-week"},
		{"0 9 * * ? 2027", "year"},
		{"0 9 L * ? *", "L"},
		{"0 9 15W * ? *", "W"},
		{"0 9 LW * ? *", "L"},
		{"0 9 ? * 6L *", "L"},
		{"0 9 ? * 6#3 *", "#"},
		{"60 9 * * ? *", "minute"},
		{"0 24 * * ? *", "hour"},
		{"0 9 * 13 ? *", "month"},
		{"0 9 * * ?", "6 fields"},
	} {
		t.Run(tt.expr, func(t *testing.T) {
			_, err := FromEventBridge(tt.expr)
			var ce *ConvertError
			if !errors.As(err, &ce) || ce.Dialect != EventBridge || !strings.Contains(err.Error(), tt.diagnostic) {
				t.Fatalf("error = %v; want eventbridge diagnostic containing %q", err, tt.diagnostic)
			}
		})
	}
	if _, err := ToEventBridge("0 9 15 * MON"); err == nil {
		t.Fatal("two restricted cron day fields must be refused")
	}
}
