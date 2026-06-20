package goblin

import "testing"

func TestGreetingDeterministic(t *testing.T) {
	for _, seed := range []uint64{0, 1, 2, 99, 12345} {
		if Greeting(seed) != Greeting(seed) {
			t.Fatalf("Greeting(%d) is not deterministic", seed)
		}
	}
}

func TestGreetingNonEmpty(t *testing.T) {
	if Greeting(0) == "" {
		t.Fatal("Greeting(0) returned empty string")
	}
}

func TestGreetingWrapsIndex(t *testing.T) {
	// A seed far larger than the slice length must still index safely.
	if got := Greeting(1 << 40); got == "" {
		t.Fatal("Greeting with large seed returned empty string")
	}
}

func TestLineDeterministic(t *testing.T) {
	if Line("*/17 3 * * 1-5") != Line("*/17 3 * * 1-5") {
		t.Fatal("Line is not deterministic for the same key")
	}
}
