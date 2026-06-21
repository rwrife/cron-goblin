// Package goblin holds the cron-goblin persona: short, grumpy one-liners.
//
// Personality lives here and *only* here. Logic packages (parse, nextrun,
// lint, ...) stay humorless and deterministic; they hand this package a
// situation and get back snark. That separation keeps the funny stuff from
// leaking into anything that needs to be testable and boring.
package goblin

import (
	"hash/fnv"
)

// greetings are the grumpy lines printed when the goblin is summoned with no
// particular task. Kept short on purpose — the goblin is busy and annoyed.
var greetings = []string{
	"You woke me up. This had better be about cron.",
	"Ah. Another crontab nobody understands. Including you.",
	"I live in your crontab and I have opinions.",
	"Cron gibberish in, plain English out. Try not to waste my time.",
	"Let me guess: you wrote `*/17 3 * * 1-5` and forgot what it does.",
}

// Greeting returns a stable grumpy greeting. The same seed always yields the
// same line so output is deterministic (good for tests and `--json`); pass 0
// for the canonical greeting.
func Greeting(seed uint64) string {
	if len(greetings) == 0 {
		return "..."
	}
	return greetings[seed%uint64(len(greetings))]
}

// Line picks a deterministic grumpy line keyed by an arbitrary string (e.g. a
// cron expression or a lint code). The same key always returns the same line,
// so the goblin is consistently rude about the same input.
func Line(key string) string {
	h := fnv.New64a()
	_, _ = h.Write([]byte(key))
	return Greeting(h.Sum64())
}
