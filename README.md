# cron-goblin 👹⏰

> A grumpy little gremlin that guards your crontab. It translates cron gibberish
> into plain English, previews exactly when your jobs fire, and shrieks when two
> of them are about to collide at 3am.

`cron-goblin` is a single-binary terminal tool for anyone who has ever typed
`*/17 3 * * 1-5`, hit save, and immediately wondered what they just agreed to.

It is **design-time, offline, and account-free**. It does not run, monitor, or
supervise your jobs — it helps you *author and sanity-check* schedules before they
ship. Think of it as a linter + preview pane for your crontab, wrapped in a goblin
with opinions.

## Why

Cron is the one corner of developer tooling everybody still does by superstition.
Websites can explain a single expression; libraries can translate one string to
English. cron-goblin works on your **whole crontab at once** — previewing real
fire times in your timezone and **linting across jobs** to catch overlaps,
every-minute loops, and expressions that never fire.

## What it does

- **`goblin explain "<expr>"`** — plain-English description of a cron expression,
  now with a preview of the upcoming fire times (`--json` for scripts/agents).
  ✅ *available now*
- **`goblin next "<expr>" -n 20`** — the next N fire times in your timezone
  (`--tz`, `--json`); reports expressions that never fire. ✅ *available now*
- **`goblin lint <crontab>`** — reads a whole crontab (file or stdin) and flags
  dead expressions, too-frequent jobs, and same-instant collisions between jobs
  (`--json`, `--ci`). ✅ *available now*
- **`goblin from "every weekday at 6:30pm"`** — plain English → a cron
  expression. Deterministic and fully offline (a hand-rolled rule grammar, no
  LLM, no network); `--json` for agents. ✅ *available now*
- **`goblin doctor`** — lint the crontab you actually have installed: reads it
  via `crontab -l` and runs the same rules as `goblin lint` (`--json`, `--ci`,
  `--user`). A user with no crontab is reported calmly and exits zero.
  ✅ *available now*
- **`goblin` (live TUI)** — run with no arguments in a terminal to open a live
  preview: type a cron expression and watch the plain English, the next fire
  times, and a week-view heatmap update on every keystroke, with inline lint
  warnings. ✅ *available now*

Planned next:

- **`--no-color` everywhere, shell completions, and prebuilt release
  binaries** — the remaining M6 polish pass.

## Status

🚧 Early, but moving.

- **M1 (scaffold)** — done. The binary builds, runs, and greets you.
- **M2 (parse + explain)** — done. `goblin explain` turns a standard 5-field
  cron expression into plain English, with a normalized parser (`*`, `,`, `-`,
  `/`, named months/days) and a `--json` mode.
- **M3 (next fire-time engine)** — done. `goblin next` lists the next N fire
  times in any timezone (`--tz`), honoring cron's day-of-month/day-of-week
  OR-rule and DST, and reporting expressions that never fire. `explain` now
  shows real upcoming runs too.
- **M4 (lint + collision detection)** — done. `goblin lint` reads a crontab
  (file or stdin) and runs pluggable rules: dead expressions (error),
  too-frequent/every-minute jobs (warning), and same-instant collisions across
  jobs (warning) — the "thundering herd" seed. `--json` for a stable report and
  `--ci` for a non-zero exit in pipelines.
- **M5 (TUI preview pane)** — done. Running `goblin` with no arguments in a
  terminal opens a live [bubbletea](https://github.com/charmbracelet/bubbletea)
  preview: an input box parses your expression as you type and three panels
  update in real time — the plain-English description, the next fire times (with
  a relative "in 3h 20m" hint), and a week×hour heatmap of fire density. Invalid
  input shows a gentle error instead of crashing, never-firing expressions say
  so, and dead/too-frequent schedules surface inline goblin warnings. Use
  `--tz`, `--no-color`, or `--no-tui` (and piping/redirecting keeps the old
  text greeting for scripts).
- **M6 (English → cron + polish)** — in progress. `goblin from "<phrase>"`
  turns plain English into a 5-field cron expression with a small, deterministic,
  fully offline rule grammar (no LLM, no network). It covers the common cases
  — "every 15 minutes", "every day at 9am", "every weekday at 6:30pm",
  "weekends at noon", "every monday at 8am", "first of the month at 9am",
  "every january at midnight" — prints the cron line first (so it pipes), echoes
  a plain-English readback plus the next fire, and rejects anything outside the
  grammar rather than guessing. `--json` for agents. `goblin doctor` now lints
  your installed crontab (`crontab -l`) with the same engine. Shell completions
  and release binaries are the remaining M6 work.

Next: finish the M6 polish (completions, `goreleaser`). See
[`PLAN.md`](./PLAN.md) for the full roadmap and backlog.

## Install

Prebuilt binaries and `go install` arrive with M6. For now, build from source
(Go 1.24+):

```bash
git clone https://github.com/rwrife/cron-goblin
cd cron-goblin
go build -o goblin ./cmd/goblin

./goblin                                # live TUI preview (in a terminal): type an
                                        # expression, watch next runs + heatmap update
./goblin "0 9 * * 1-5"                  # open the TUI pre-filled with an expression
./goblin --tz America/New_York          # TUI with fire times in New York
./goblin --no-color                     # TUI without ANSI color

./goblin explain "*/15 9-17 * * 1-5"
# Every 15 minutes during the hours 09:00–17:00 on weekdays (Monday through Friday)
# ...followed by the next few fire times.

./goblin next "*/15 * * * *" -n 20             # next 20 fire times (local TZ, ISO)
./goblin next --tz America/New_York "0 9 * * 1-5"  # 9am weekdays, New York time
./goblin next --json "0 0 13 * 5"             # machine-readable: fires the 13th OR any Friday
./goblin next "0 0 30 2 *"                    # "never fires" — February 30th doesn't exist

./goblin explain --json "0 0 13 * 5"   # machine-readable summary for scripts/agents
./goblin explain --quiet "30 6 * * 1-5" # no goblin grumbling on stderr

./goblin from "every 15 minutes"        # -> */15 * * * *  (English -> cron)
./goblin from "every weekday at 6:30pm" # -> 30 18 * * 1-5
./goblin from --json "daily at 9am"     # machine-readable result for agents
./goblin from "every blue moon"         # honest error instead of a wrong guess

./goblin lint /etc/crontab              # lint a crontab file
crontab -l | ./goblin lint -            # lint your own crontab via stdin
./goblin lint --json crontab.txt        # stable JSON report for scripts/agents
./goblin lint --ci crontab.txt          # non-zero exit if any warning/error

./goblin doctor                         # lint the crontab you actually have installed
./goblin doctor --json                  # stable JSON report for scripts/agents
./goblin doctor --ci                    # non-zero exit if any warning/error
./goblin --version                      # cron-goblin 0.1.0-dev
```

Fire times honor cron's classic day-of-month/day-of-week OR-rule and are
computed against your chosen timezone's wall clock, so daylight-saving
transitions are handled correctly (missing hours are skipped; repeated hours
fire once).

## License

MIT (see `LICENSE` once added).
