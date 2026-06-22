package parse

import (
	"errors"
	"testing"
)

// TestParseValid is a golden table of well-formed expressions, asserting the
// normalized value sets and Star flags for each field.
func TestParseValid(t *testing.T) {
	cases := []struct {
		name string
		expr string
		want Schedule
	}{
		{
			name: "all stars",
			expr: "* * * * *",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: fullRange(0, 59), Star: true, Raw: "*"},
				Hour:   FieldSpec{Field: Hour, Values: fullRange(0, 23), Star: true, Raw: "*"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: fullRange(0, 6), Star: true, Raw: "*"},
			},
		},
		{
			name: "single values",
			expr: "30 6 1 1 1",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{30}, Raw: "30"},
				Hour:   FieldSpec{Field: Hour, Values: []int{6}, Raw: "6"},
				DOM:    FieldSpec{Field: DOM, Values: []int{1}, Raw: "1"},
				Month:  FieldSpec{Field: Month, Values: []int{1}, Raw: "1"},
				DOW:    FieldSpec{Field: DOW, Values: []int{1}, Raw: "1"},
			},
		},
		{
			name: "step over star",
			expr: "*/15 * * * *",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{0, 15, 30, 45}, Raw: "*/15"},
				Hour:   FieldSpec{Field: Hour, Values: fullRange(0, 23), Star: true, Raw: "*"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: fullRange(0, 6), Star: true, Raw: "*"},
			},
		},
		{
			name: "range and list",
			expr: "0 9-17 1,15 * 1-5",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{0}, Raw: "0"},
				Hour:   FieldSpec{Field: Hour, Values: fullRange(9, 17), Raw: "9-17"},
				DOM:    FieldSpec{Field: DOM, Values: []int{1, 15}, Raw: "1,15"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: fullRange(1, 5), Raw: "1-5"},
			},
		},
		{
			name: "stepped range",
			expr: "0 0-12/3 * * *",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{0}, Raw: "0"},
				Hour:   FieldSpec{Field: Hour, Values: []int{0, 3, 6, 9, 12}, Raw: "0-12/3"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: fullRange(0, 6), Star: true, Raw: "*"},
			},
		},
		{
			name: "named months and days",
			expr: "0 0 * JAN-MAR MON",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{0}, Raw: "0"},
				Hour:   FieldSpec{Field: Hour, Values: []int{0}, Raw: "0"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: []int{1, 2, 3}, Raw: "JAN-MAR"},
				DOW:    FieldSpec{Field: DOW, Values: []int{1}, Raw: "MON"},
			},
		},
		{
			name: "sunday as 7 normalizes to 0",
			expr: "0 0 * * 7",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{0}, Raw: "0"},
				Hour:   FieldSpec{Field: Hour, Values: []int{0}, Raw: "0"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: []int{0}, Raw: "7"},
			},
		},
		{
			name: "from-value step",
			expr: "0 9/4 * * *",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{0}, Raw: "0"},
				Hour:   FieldSpec{Field: Hour, Values: []int{9, 13, 17, 21}, Raw: "9/4"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: fullRange(0, 6), Star: true, Raw: "*"},
			},
		},
		{
			name: "extra whitespace tolerated",
			expr: "  5   4  *  *  *  ",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{5}, Raw: "5"},
				Hour:   FieldSpec{Field: Hour, Values: []int{4}, Raw: "4"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: fullRange(0, 6), Star: true, Raw: "*"},
			},
		},
		{
			name: "list of star-disqualifies-star",
			expr: "* 1,2 * * *",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: fullRange(0, 59), Star: true, Raw: "*"},
				Hour:   FieldSpec{Field: Hour, Values: []int{1, 2}, Raw: "1,2"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: fullRange(1, 12), Star: true, Raw: "*"},
				DOW:    FieldSpec{Field: DOW, Values: fullRange(0, 6), Star: true, Raw: "*"},
			},
		},
		{
			name: "case-insensitive names",
			expr: "0 0 * dec sun",
			want: Schedule{
				Minute: FieldSpec{Field: Minute, Values: []int{0}, Raw: "0"},
				Hour:   FieldSpec{Field: Hour, Values: []int{0}, Raw: "0"},
				DOM:    FieldSpec{Field: DOM, Values: fullRange(1, 31), Star: true, Raw: "*"},
				Month:  FieldSpec{Field: Month, Values: []int{12}, Raw: "dec"},
				DOW:    FieldSpec{Field: DOW, Values: []int{0}, Raw: "sun"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Parse(tc.expr)
			if err != nil {
				t.Fatalf("Parse(%q) unexpected error: %v", tc.expr, err)
			}
			assertField(t, "minute", got.Minute, tc.want.Minute)
			assertField(t, "hour", got.Hour, tc.want.Hour)
			assertField(t, "dom", got.DOM, tc.want.DOM)
			assertField(t, "month", got.Month, tc.want.Month)
			assertField(t, "dow", got.DOW, tc.want.DOW)
		})
	}
}

// TestParseErrors is a golden table of malformed expressions. Each must fail
// with an *Error pointing at the expected field.
func TestParseErrors(t *testing.T) {
	cases := []struct {
		name      string
		expr      string
		wantField Field
	}{
		{"too few fields", "* * * *", Minute},
		{"too many fields", "0 0 * * * *", Minute},
		{"empty", "", Minute},
		{"whitespace only", "   ", Minute},
		{"minute out of range", "60 0 * * *", Minute},
		{"hour out of range", "0 24 * * *", Hour},
		{"dom zero", "0 0 0 * *", DOM},
		{"month out of range", "0 0 * 13 *", Month},
		{"dow out of range", "0 0 * * 8", DOW},
		{"zero step", "*/0 * * * *", Minute},
		{"negative step", "*/-1 * * * *", Minute},
		{"missing step", "*/ * * * *", Minute},
		{"inverted range", "5-2 * * * *", Minute},
		{"non-numeric", "foo * * * *", Minute},
		{"bad month name", "0 0 * FOO *", Month},
		{"empty comma term", "1,,3 * * * *", Minute},
		{"bad step value", "*/x * * * *", Minute},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse(tc.expr)
			if err == nil {
				t.Fatalf("Parse(%q) expected error, got nil", tc.expr)
			}
			var pe *Error
			if !errors.As(err, &pe) {
				t.Fatalf("Parse(%q) error is %T, want *parse.Error", tc.expr, err)
			}
			if pe.Field != tc.wantField {
				t.Errorf("Parse(%q) error field = %v, want %v (msg: %s)",
					tc.expr, pe.Field, tc.wantField, pe.Msg)
			}
			if pe.Error() == "" {
				t.Errorf("Parse(%q) error message is empty", tc.expr)
			}
		})
	}
}

// TestParseDeterministic ensures the same input always yields identical output.
func TestParseDeterministic(t *testing.T) {
	const expr = "*/15 9-17 1,15 JAN-JUN MON-FRI"
	a, err := Parse(expr)
	if err != nil {
		t.Fatalf("Parse error: %v", err)
	}
	b, _ := Parse(expr)
	if a.Raw != b.Raw {
		t.Fatal("Raw differs across identical parses")
	}
	for i, fa := range a.Fields() {
		fb := b.Fields()[i]
		if len(fa.Values) != len(fb.Values) {
			t.Fatalf("field %d value count differs", i)
		}
	}
}

// --- test helpers -----------------------------------------------------------

func fullRange(lo, hi int) []int {
	out := make([]int, 0, hi-lo+1)
	for v := lo; v <= hi; v++ {
		out = append(out, v)
	}
	return out
}

func assertField(t *testing.T, name string, got, want FieldSpec) {
	t.Helper()
	if got.Star != want.Star {
		t.Errorf("%s: Star = %v, want %v", name, got.Star, want.Star)
	}
	if want.Raw != "" && got.Raw != want.Raw {
		t.Errorf("%s: Raw = %q, want %q", name, got.Raw, want.Raw)
	}
	if len(got.Values) != len(want.Values) {
		t.Errorf("%s: Values = %v, want %v", name, got.Values, want.Values)
		return
	}
	for i := range got.Values {
		if got.Values[i] != want.Values[i] {
			t.Errorf("%s: Values = %v, want %v", name, got.Values, want.Values)
			return
		}
	}
}
