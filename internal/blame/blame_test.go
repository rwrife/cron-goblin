package blame

import (
	"strings"
	"testing"
	"time"
)

// pinned reference time for deterministic next-fire results.
func refNow(t *testing.T) time.Time {
	t.Helper()
	return time.Date(2026, 7, 10, 19, 20, 0, 0, time.UTC)
}

func TestAnnotateMixedCrontab(t *testing.T) {
	in := "# my crontab\n" +
		"SHELL=/bin/bash\n" +
		"\n" +
		"*/17 3 * * 1-5 /opt/report.sh\n" +
		"0 0 30 2 * /opt/never.sh\n" +
		"not a cron line\n" +
		"0 9 * * * /opt/daily.sh\n"

	rows, err := Annotate(strings.NewReader(in), refNow(t), time.UTC)
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	if len(rows) != 7 {
		t.Fatalf("want 7 rows, got %d", len(rows))
	}

	// Line 1 comment, line 2 env, line 3 blank -> all KindOther.
	for _, i := range []int{0, 1, 2} {
		if rows[i].Kind != KindOther {
			t.Errorf("row %d: want KindOther, got %v", i+1, rows[i].Kind)
		}
	}

	// Line 4: valid schedule.
	r4 := rows[3]
	if r4.Kind != KindSchedule {
		t.Fatalf("row 4: want KindSchedule, got %v", r4.Kind)
	}
	if r4.Dead {
		t.Errorf("row 4: unexpectedly dead")
	}
	if r4.English == "" {
		t.Errorf("row 4: empty english")
	}
	// */17 3 * * 1-5 from Fri 2026-07-10 19:20 -> next weekday 03:00-hour slot
	// is Mon 2026-07-13 03:00.
	wantNext := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	if !r4.Next.Equal(wantNext) {
		t.Errorf("row 4 next: want %s, got %s", wantNext, r4.Next)
	}

	// Line 5: dead expression (Feb 30).
	r5 := rows[4]
	if r5.Kind != KindSchedule || !r5.Dead {
		t.Errorf("row 5: want dead schedule, got kind=%v dead=%v", r5.Kind, r5.Dead)
	}
	if !r5.Next.IsZero() {
		t.Errorf("row 5: dead line should have zero Next, got %s", r5.Next)
	}

	// Line 6: unparseable.
	r6 := rows[5]
	if r6.Kind != KindUnparseable {
		t.Errorf("row 6: want KindUnparseable, got %v", r6.Kind)
	}
	if r6.Note == "" {
		t.Errorf("row 6: want a note")
	}

	// Line 7: valid daily.
	r7 := rows[6]
	if r7.Kind != KindSchedule || r7.Dead {
		t.Errorf("row 7: want live schedule, got kind=%v dead=%v", r7.Kind, r7.Dead)
	}
	wantDaily := time.Date(2026, 7, 11, 9, 0, 0, 0, time.UTC)
	if !r7.Next.Equal(wantDaily) {
		t.Errorf("row 7 next: want %s, got %s", wantDaily, r7.Next)
	}
}

func TestAnnotatePreservesOrderAndLineNumbers(t *testing.T) {
	in := "a\n\n0 0 * * *\n"
	rows, err := Annotate(strings.NewReader(in), refNow(t), nil)
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	for i, want := range []int{1, 2, 3} {
		if rows[i].Line != want {
			t.Errorf("row %d: want line %d, got %d", i, want, rows[i].Line)
		}
	}
}

func TestAnnotateNilLocDefaultsUTC(t *testing.T) {
	rows, err := Annotate(strings.NewReader("0 12 * * *\n"), refNow(t), nil)
	if err != nil {
		t.Fatalf("Annotate: %v", err)
	}
	if rows[0].Next.Location() != time.UTC {
		t.Errorf("nil loc: want UTC, got %s", rows[0].Next.Location())
	}
}
