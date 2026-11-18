package coverage

import (
	"reflect"
	"strings"
	"testing"
	"time"
	_ "time/tzdata"
)

func TestAnalyzeGoldenCases(t *testing.T) {
	from := time.Date(2026, 6, 22, 0, 0, 0, 0, time.UTC) // Monday
	wednesday := from.Add(2*24*time.Hour + 9*time.Hour)
	for _, tc := range []struct {
		name, src, status string
		covered           int
		gaps              []Gap
		redundancies      []Redundancy
	}{
		{
			name:   "fully covered by union",
			src:    "0 9 * * 1-2 first\n0 9 * * 3-5 second\n",
			status: "covered", covered: 5,
			gaps: []Gap{}, redundancies: []Redundancy{},
		},
		{
			name:   "middle of week gap",
			src:    "0 9 * * 1-2,4-5 job\n",
			status: "under-covered", covered: 4,
			gaps: []Gap{{Time: wednesday}}, redundancies: []Redundancy{},
		},
		{
			name:   "triple redundant minute",
			src:    "# jobs\n0 9 * * 1-5 all\n0 9 * * 3 second\n0 9 * * 3 third\n",
			status: "over-covered", covered: 5,
			gaps:         []Gap{},
			redundancies: []Redundancy{{Time: wednesday, Count: 3, Lines: []int{2, 3, 4}}},
		},
		{
			name:   "gap and redundancy coexist",
			src:    "0 9 * * 1-2,4-5 all\n0 9 * * 1 duplicate\n",
			status: "under-covered", covered: 4,
			gaps:         []Gap{{Time: wednesday}},
			redundancies: []Redundancy{{Time: from.Add(9 * time.Hour), Count: 2, Lines: []int{1, 2}}},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Analyze(tc.src, "every weekday at 9am", from, DefaultWindow, time.UTC)
			if err != nil {
				t.Fatal(err)
			}
			want := Report{
				Want: "0 9 * * 1-5", Timezone: "UTC", From: from, To: from.Add(DefaultWindow),
				Status: tc.status, Expected: 5, Covered: tc.covered,
				Gaps: tc.gaps, Redundancies: tc.redundancies,
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("report = %+v\nwant = %+v", got, want)
			}
		})
	}
}

func TestAnalyzeWindowAndExactMatching(t *testing.T) {
	from := time.Date(2026, 6, 22, 9, 0, 30, 0, time.UTC)
	for _, tc := range []struct {
		name, src, want string
		window          time.Duration
		expected, gaps  int
	}{
		{"empty crontab", "", "* * * * *", 2 * time.Minute, 2, 2},
		{"strict start inclusive end", "* * * * * job", "* * * * *", 90 * time.Second, 2, 0},
		{"off target fires do not cover", "1,16,31,46 * * * * job", "*/15 * * * *", time.Hour, 4, 4},
		{"sparse outside seven days", "", "0 0 1 * *", DefaultWindow, 0, 0},
		{"thirty day horizon", "", "0 0 1 * *", 30 * 24 * time.Hour, 1, 1},
		{"dead job leaves gaps", "0 0 30 2 * dead", "0 9 * * *", 24 * time.Hour, 1, 1},
		{"metadata and off target duplicates", "# comment\nSHELL=/bin/sh\n* * * * * all\n1 * * * * a\n1 * * * * b\n", "0 * * * *", time.Hour, 1, 0},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rep, err := Analyze(tc.src, tc.want, from, tc.window, nil)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Expected != tc.expected || len(rep.Gaps) != tc.gaps || len(rep.Redundancies) != 0 {
				t.Fatalf("unexpected report: %+v", rep)
			}
		})
	}
}

func TestAnalyzeRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		src, want string
		window    time.Duration
		errorText string
	}{
		{"", "nonsense", time.Hour, "--want"},
		{"# header\nbroken job\n", "* * * * *", time.Hour, "line 2"},
		{"70 * * * * job", "* * * * *", time.Hour, "line 1"},
		{"", "* * * * *", 0, "positive"},
		{"", "* * * * *", -time.Hour, "positive"},
	} {
		_, err := Analyze(tc.src, tc.want, time.Now(), tc.window, nil)
		if err == nil || !strings.Contains(err.Error(), tc.errorText) {
			t.Errorf("error = %v, want %q", err, tc.errorText)
		}
	}
}

func TestAnalyzeTimezoneAndDST(t *testing.T) {
	loc, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, start, want string
		window            time.Duration
		expected          int
	}{
		{"local weekday", "2026-06-22T12:59:00Z", "0 9 * * 1", time.Hour, 1},
		{"spring missing hour", "2026-03-08T00:00:00-05:00", "30 2 * * *", 24 * time.Hour, 0},
		{"fall repeated hour fires once", "2026-11-01T00:00:00-04:00", "30 1 * * *", 25 * time.Hour, 1},
		{"fall dense iterator advances", "2026-11-01T00:00:00-04:00", "* * * * *", 4 * time.Hour, 180},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from, err := time.Parse(time.RFC3339, tc.start)
			if err != nil {
				t.Fatal(err)
			}
			rep, err := Analyze(tc.want+" job\n", tc.want, from, tc.window, loc)
			if err != nil {
				t.Fatal(err)
			}
			if rep.Expected != tc.expected || rep.Covered != tc.expected || len(rep.Gaps) != 0 {
				t.Fatalf("unexpected report: %+v", rep)
			}
		})
	}
}
