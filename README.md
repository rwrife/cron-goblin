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

## What it does (planned)

- **`goblin explain "<expr>"`** — plain-English description + the next few runs.
- **`goblin next "<expr>" -n 20`** — the next N fire times in your timezone.
- **`goblin lint <crontab>`** — flags dead expressions, every-minute jobs, and
  same-minute collisions between jobs.
- **`goblin from "every weekday at 6:30pm"`** — plain English → a cron expression.
- **TUI mode** — live-edit a schedule and watch the next runs + a week heatmap
  update as you type.
- **`--json`** on everything — so your scripts (and AI agents) can call it safely.

## Status

🚧 Early. See [`PLAN.md`](./PLAN.md) for the full roadmap (M1–M6) and backlog.

## Install

Coming with M6 (`go install` / prebuilt binaries). For now:

```bash
git clone https://github.com/rwrife/cron-goblin
cd cron-goblin
go run ./cmd/goblin
```

## License

MIT (see `LICENSE` once added).
