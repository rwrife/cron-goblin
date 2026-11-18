package dialect

import (
	"strconv"
	"strings"
	"testing"
)

func TestJenkinsGoldenResolution(t *testing.T) {
	// Golden values from Jenkins' own CronTabTest.hashedMinute/hashSkips:
	// https://github.com/jenkinsci/jenkins/blob/master/core/src/test/java/hudson/scheduler/CronTabTest.java
	for _, tt := range []struct{ expr, job, cron string }{
		{"H * * * *", "stuff", "56 * * * *"},
		{"@hourly", "junk", "20 * * * *"},
		{"H H(12-13) * * *", "stuff", "56 13 * * *"},
		{"H/15 * * * *", "stuff", "11,26,41,56 * * * *"},
		{"H(0-15)/3 * * * *", "junk", "2,5,8,11,14 * * * *"},
		{"H(0-3)/4 * * * *", "junk", "2 * * * *"},
		{"H/1 * * * *", "stuff", "56 * * * *"},
		{"0 9 * * 1-7", "", "0 9 * * *"},
		{"0 9 15 * 0-7", "", "0 9 15 * *"},
		{"0 9 * * 5-7", "", "0 9 * * 5,6,0"},
		{"0 9 * * 7", "", "0 9 * * 0"},
	} {
		t.Run(tt.expr+"/"+tt.job, func(t *testing.T) {
			got, err := FromJenkins(tt.expr, tt.job)
			if err != nil || got != tt.cron {
				t.Fatalf("FromJenkins = %q, %v; want %q", got, err, tt.cron)
			}
		})
	}
}

func TestJenkinsJitterRoundTrips(t *testing.T) {
	for _, tt := range []struct{ cron, jenkins, resolved string }{
		{"0 * * * *", "H * * * *", "56 * * * *"},
		{"*/15 * * * *", "H/15 * * * *", "11,26,41,56 * * * *"},
		{"5,20,35,50 * * * *", "H/15 * * * *", "11,26,41,56 * * * *"},
		{"0 9 * * MON-FRI", "H 9 * * 1,2,3,4,5", "56 9 * * 1,2,3,4,5"},
		{"* * * * *", "* * * * *", "* * * * *"},
	} {
		t.Run(tt.cron, func(t *testing.T) {
			jittered, err := ToJenkins(tt.cron)
			if err != nil || jittered != tt.jenkins {
				t.Fatalf("ToJenkins = %q, %v; want %q", jittered, err, tt.jenkins)
			}
			resolved, err := FromJenkins(jittered, "stuff")
			if err != nil || resolved != tt.resolved {
				t.Fatalf("FromJenkins = %q, %v; want %q", resolved, err, tt.resolved)
			}
			again, err := ToJenkins(resolved)
			if err != nil || again != jittered {
				t.Fatalf("jitter round trip = %q, %v; want %q", again, err, jittered)
			}
		})
	}
}

func TestJenkinsRefusals(t *testing.T) {
	for _, expr := range []string{
		"H(60-70) * * * *", "H(4-2) * * * *", "H/0 * * * *",
		"H/61 * * * *", "H(0-3)/15 * * * *", "H() * * * *",
		"H(999999999999999999999-9999999999999999999999) * * * *",
		"H H H * H", "0 9 15 * 1", "0 9 * * MON", "5/15 * * * *",
		"0 9 * * 0-8", "0 9 * * */0", "0 9 * * 1-",
		"TZ=UTC H * * * *", "@reboot",
	} {
		t.Run(expr, func(t *testing.T) {
			if _, err := FromJenkins(expr, "stuff"); err == nil {
				t.Fatalf("expected refusal for %q", expr)
			}
		})
	}
	if _, err := FromJenkins("H/15 * * * *", ""); err == nil || !strings.Contains(err.Error(), "--job-name") {
		t.Fatalf("missing seed diagnostic = %v", err)
	}
	for _, expr := range []string{"0 9 15 * MON", "0,10,40 * * * *", "*/17 * * * *"} {
		if _, err := ToJenkins(expr); err == nil {
			t.Errorf("expected jitter refusal for %q", expr)
		}
	}
}

func TestJenkinsHashBoundsAndJobNames(t *testing.T) {
	for _, job := range []string{"folder/job", "folder/other", "job", "unicode-\u03bb"} {
		cron, err := FromJenkins("H H H H *", job)
		if err != nil {
			t.Fatal(err)
		}
		// FromJenkins validates bounds, but DOM has an additional Jenkins limit.
		fields := strings.Fields(cron)
		dom, err := strconv.Atoi(fields[2])
		if err != nil {
			t.Fatal(err)
		}
		if dom < 1 || dom > 28 {
			t.Fatalf("hashed day %d outside Jenkins 1-28 limit", dom)
		}
		again, err := FromJenkins("H H H H *", job)
		if err != nil || again != cron {
			t.Fatalf("resolution changed between calls: %q -> %q (%v)", cron, again, err)
		}
	}
}
