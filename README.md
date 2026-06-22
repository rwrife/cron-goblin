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

- **`goblin explain "<expr>"`** — plain-English description of a cron expression
  (with `--json` for scripts/agents). ✅ *available now*

Planned next:

- **`goblin next "<expr>" -n 20`** — the next N fire times in your timezone.
- **`goblin lint <crontab>`** — flags dead expressions, every-minute jobs, and
  same-minute collisions between jobs.
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

Next: `next` (M3), `lint` (M4), the TUI (M5), and English → cron (M6). See
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

./goblin explain --json "0 0 13 * 5"   # machine-readable summary for scripts/agents
./goblin explain --quiet "30 6 * * 1-5" # no goblin grumbling on stderr
./goblin --version                      # cron-goblin 0.1.0-dev
```

Note: `explain` reports the next runs as a placeholder until the M3 fire-time
engine lands.

## License

MIT (see `LICENSE` once added).
