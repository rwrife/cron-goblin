package lint

import "testing"

func TestFilterRulesDropsNamed(t *testing.T) {
	base := DefaultRules()
	filtered := FilterRules(base, []string{"too-frequent"})

	for _, r := range filtered {
		if r.Name() == "too-frequent" {
			t.Fatalf("too-frequent should have been filtered out")
		}
	}
	if len(filtered) != len(base)-1 {
		t.Fatalf("filtered len = %d, want %d", len(filtered), len(base)-1)
	}
}

func TestFilterRulesEmptyDisableReturnsCopy(t *testing.T) {
	base := DefaultRules()
	filtered := FilterRules(base, nil)
	if len(filtered) != len(base) {
		t.Fatalf("empty disable changed rule count: %d vs %d", len(filtered), len(base))
	}
}

func TestFilterRulesUnknownNameIgnored(t *testing.T) {
	base := DefaultRules()
	filtered := FilterRules(base, []string{"no-such-rule"})
	if len(filtered) != len(base) {
		t.Fatalf("unknown disable name changed rule count: %d vs %d", len(filtered), len(base))
	}
}
