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

Planned next:

- **`goblin from "every weekday at 6:30pm"`** — plain English → a cron expression.
- **TUI mode** — live-edit a schedule and watch the next runs + a week heatmap
  update as you type.
- **`--json`** on everything — so your scripts (and AI agents) can call it safely.

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

Next: the TUI (M5) and English → cron (M6). See
[`PLAN.md`](./PLAN.md) for the full roadmap and backlog.

## Install

Prebuilt binaries and `go install` arrive with M6. For now, build from source
(Go 1.22+):

```bash
git clone https://github.com/rwrife/cron-goblin
cd cron-goblin
go build -o goblin ./cmd/goblin

./goblin explain "*/15 9-17 * * 1-5"
# Every 15 minutes during the hours 09:00–17:00 on weekdays (Monday through Friday)
# ...followed by the next few fire times.

./goblin next "*/15 * * * *" -n 20             # next 20 fire times (local TZ, ISO)
./goblin next --tz America/New_York "0 9 * * 1-5"  # 9am weekdays, New York time
./goblin next --json "0 0 13 * 5"             # machine-readable: fires the 13th OR any Friday
./goblin next "0 0 30 2 *"                    # "never fires" — February 30th doesn't exist

./goblin explain --json "0 0 13 * 5"   # machine-readable summary for scripts/agents
./goblin explain --quiet "30 6 * * 1-5" # no goblin grumbling on stderr

./goblin lint /etc/crontab              # lint a crontab file
crontab -l | ./goblin lint -            # lint your own crontab via stdin
./goblin lint --json crontab.txt        # stable JSON report for scripts/agents
./goblin lint --ci crontab.txt          # non-zero exit if any warning/error
./goblin --version                      # cron-goblin 0.1.0-dev
```

Fire times honor cron's classic day-of-month/day-of-week OR-rule and are
computed against your chosen timezone's wall clock, so daylight-saving
transitions are handled correctly (missing hours are skipped; repeated hours
fire once).

## License

MIT (see `LICENSE` once added).
