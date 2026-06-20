# cron-goblin 👹⏰

A grumpy little gremlin that lives in your crontab. It translates cron gibberish
into plain English, previews exactly when jobs will fire, and **shrieks when two
of your jobs are about to collide at 3am**.

---

## 1. Pitch

`cron-goblin` is a single-binary terminal tool for people who write cron schedules
and immediately forget what `*/17 3 * * 1-5` actually means. Type a schedule (cron
*or* plain English), and the goblin shows you the next 20 fire times, a tiny
week-view heatmap, and warnings: overlapping jobs, suspicious every-minute loops,
DST landmines, and "this never fires" dead expressions. It reads your whole
crontab and **lints it like a linter lints code** — because scheduling is the one
corner of dev tooling everyone still does by superstition.

## 2. Trend inspiration

The 2026 developer-tools story is loud and consistent:

- **The "terminal renaissance."** TUIs are where the exciting tooling is being
  built again — multiple round-ups in 2026 frame the terminal as the hot surface
  for new dev tools. (e.g. "Terminal Renaissance: Modern TUI Tools Reshaping
  Developer Workflows" — https://1337skills.com/blog/2026-03-09-terminal-renaissance-modern-tui-tools-reshaping-developer-workflows/ ;
  "Best Terminal Tools for Developers in 2026" — https://dev.to/raxxostudios/best-terminal-tools-for-developers-in-2026-4jn1 )
- **Agents are doing infra now.** "Heroku for AI coding agents" pitches (e.g.
  InsForge, YC P26 — https://github.com/InsForge/InsForge ) show agents deploying
  and *scheduling* work. Agents need **machine-readable, explainable** scheduling
  primitives, not a human eyeballing crontab. A `--json` cron explainer/linter is
  exactly the kind of safe primitive an agent should call before it writes a cron
  line.
- **Cron is perennially confusing.** Every "modern CLI tools 2026" list celebrates
  readable, friendly reinventions of crusty Unix utilities — yet cron itself is
  still raw. The pain is evergreen and unglamorous, which is why it stays unsolved.

Cron is the rare overlap: boring enough that nobody's made it *fun*, central
enough that everybody touches it, and structured enough that an agent benefits
from a clean `--json` interface.

## 3. Why it's different

There are existing pieces, and I'm deliberately not rebuilding them:

- **crontab.guru** — a website that explains one expression. cron-goblin is a
  **local TUI** that works offline, handles your *whole crontab at once*, previews
  real fire times in your timezone, and **lints across jobs** (collision/overlap
  detection — guru can't see your other jobs).
- **`cronstrue` / cron-descriptor libraries** — translate one cron string to
  English. That's *one feature* here (`explain`), not the product. cron-goblin adds
  next-run preview, a week heatmap, collision/herd detection, dead-expression
  detection, DST warnings, plain-English → cron, and an agent-friendly `--json`
  mode.
- **`cronitor` / `healthchecks.io`** — runtime monitoring SaaS that pings you when
  a job *fails to run*. cron-goblin is **design-time**: it helps you author and
  sanity-check schedules *before* they ship. No account, no network, no daemon.
- Versus this workspace's own catalog: every sibling tool is git-hygiene
  (`stash-stash`, `ship-log`), data-forensics (`schema-seance`, `link-coroner`),
  or AI-agent-defense (`canary-cage`). **Nothing touches scheduling.** New lane.

The fresh angle: treat **a crontab as a program that deserves a linter and a
preview pane**, wrapped in a goblin with opinions.

## 4. MVP scope (v0.1)

Smallest useful thing — a one-shot CLI (TUI comes in M5):

- `goblin explain "<cron expr>"` → plain-English description + next 5 fire times.
- Parse standard 5-field cron, including `*`, `,`, `-`, `/`, and named months/days
  (`JAN`, `MON`).
- `goblin next "<cron expr>" -n 20` → list the next N fire datetimes (local TZ,
  ISO output).
- `goblin lint <file>` → read a crontab file, flag: every-minute jobs, dead
  expressions (e.g. `0 0 30 2 *` → Feb 30, never fires), and overlapping start
  times between jobs.
- `--json` flag on every command for agent/script consumption.
- Goblin "voice": short grumpy commentary on stderr (toggle with `--quiet`).

## 5. Tech stack

Boring, fast, single-binary:

- **Go** — single static binary, trivial `go install`, great for CLIs/TUIs, no
  runtime to ship. Best fit for "download and run."
- **`github.com/robfig/cron/v3`** parser for the schedule spec (battle-tested),
  with a thin wrapper so we can extend to non-standard fields later.
- **`bubbletea` + `lipgloss`** (Charm) for the M5 TUI — the de-facto 2026 TUI
  stack, matches the "terminal renaissance" the tool rides on.
- **`cobra`** for subcommands/flags — standard, predictable.
- Plain English → cron via a **small hand-rolled rule grammar** (no LLM dependency;
  must work offline and deterministically). Covers the common 80%: "every day at
  9am", "every 15 minutes", "weekdays at 6:30pm", "first of the month".
- Stdlib `time` for all datetime math; no external date libs.

Justification: zero-dependency-at-runtime, deterministic, offline-first, and the
Charm stack is exactly what the trend audience expects from a 2026 TUI.

## 6. Architecture

```
cron-goblin/
  cmd/goblin/main.go      # cobra root, wires subcommands
  internal/parse/         # cron string -> normalized Schedule struct
  internal/nextrun/       # Schedule -> iterator of fire times (TZ-aware)
  internal/explain/       # Schedule -> human English
  internal/english/       # English -> cron expr (rule grammar)
  internal/lint/          # rules: dead-expr, every-minute, collisions, DST
  internal/goblin/        # the persona: grumpy lines, severity -> snark
  internal/tui/           # bubbletea app (M5): input + preview + heatmap
  internal/render/        # week heatmap + table rendering (lipgloss)
```

Key modules:
- **parse** — the trusted core; everything else consumes a normalized `Schedule`.
- **nextrun** — pure, well-tested iterator (golden tests against known schedules).
- **lint** — pluggable `Rule` interface so new checks are one file each.
- **goblin** — keeps personality *out* of logic; maps lint severity to one-liners.

## 7. Milestones (each shippable)

1. **M1 — Scaffold + hello-world.** Go module, cobra root, `goblin` prints a
   greeting + version; CI (`go build`/`go vet`); README usage stub. Ships a binary
   that runs.
2. **M2 — Parse + `explain`.** Standard 5-field parser (names, ranges, steps) and
   `goblin explain` producing plain English. Golden tests.
3. **M3 — `next` fire-time engine.** TZ-aware iterator; `goblin next -n N`; ISO and
   `--json` output. Handle dead expressions (no future fire) gracefully.
4. **M4 — `lint` + collision detection.** Read a crontab file; rules for
   every-minute, dead-expr, and same-minute overlaps across jobs; severity levels;
   `--json` report.
5. **M5 — TUI preview pane.** bubbletea app: live-edit a schedule, see next runs +
   a week heatmap update as you type. The "renaissance" centerpiece.
6. **M6 — English → cron + polish.** `goblin from "every weekday at 6:30pm"` rule
   grammar, `--quiet`, shell completions, `goblin doctor` for the current user's
   crontab, release workflow (`goreleaser`) + install docs.

## 8. Backlog / future features (v0.2+)

1. **DST danger report** — flag schedules that double-fire or skip on clock changes
   for a chosen timezone.
2. **"Thundering herd" detector** — warn when N jobs share a minute and suggest a
   jittered spread (e.g. auto-stagger like the tool-lab cron staggering itself).
3. **Quartz / k8s CronJob / systemd-timer dialects** — translate between formats.
4. **`goblin diff old new`** — show how fire times change between two schedules.
5. **Natural-language *out*** — speak a schedule aloud-style summary for changelogs.
6. **Crontab "blame"** — annotate each line with last-edited + next-run inline.
7. **Watch mode** — `goblin watch` live-counts down to the next fire across jobs.
8. **Calendar export** — dump the next month of fire times to `.ics`.
9. **Config profiles** — per-project default timezone + lint ruleset in `.goblinrc`.
10. **Plugin rules** — load extra lint rules from a user dir.
11. **Editor integration** — `$EDITOR` hook so `crontab -e` pipes through goblin lint.
12. **Severity budget / `--ci`** — non-zero exit on warnings for pipelines.

## 9. Out of scope

- **Runtime monitoring / alerting** — we don't run, ping, or supervise your jobs.
  That's cronitor/healthchecks territory; cron-goblin is design-time only.
- **A scheduler / job runner** — we never execute commands. We read and explain.
- **A hosted service, web app, accounts, or telemetry.** Local, offline, no network.
- **Six-field / seconds-precision cron** in v0.1 (standard 5-field first; dialects
  are backlog).
- **LLM-powered English parsing** — must stay deterministic and offline; rules only.
- **GUI / desktop app** — terminal is the whole point.
