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
- **`goblin diff "<old>" "<new>"`** — before you commit a crontab edit, see
  exactly what shifts: line up the upcoming runs of two schedules and mark which
  ones are **added** (`+`), **removed** (`-`), or **unchanged** (`=`). Compare a
  fixed number of upcoming runs (`-n`) or everything inside a time window
  (`--window 7d`, `--window 48h`); identical schedules are called out as a
  no-op. `--tz`, `--json` (with added/removed/unchanged buckets and a
  `summary.identical` flag for review tooling/agents). ✅ *available now*
- **`goblin lint <crontab>`** — reads a whole crontab (file or stdin) and flags
  dead expressions, too-frequent jobs, same-instant collisions between jobs, and
  (with `--tz`) schedules that land in a daylight-saving gap/overlap
  (`--tz`, `--json`, `--ci`). ✅ *available now*
- **`goblin stagger <crontab>`** — break up "thundering herds": when several
  jobs fire on the exact same minute (the classic `0 9 * * *` pile-up), spread
  them deterministically across a window (`--max-spread`) so they stop
  stampeding the box together. Prints the rewritten crontab by default (a dry
  run); `--write --yes` overwrites the file in place — never without that
  explicit confirmation. `--json` for agents. ✅ *available now*
- **`goblin from "every weekday at 6:30pm"`** — plain English → a cron
  expression. Deterministic and fully offline (a hand-rolled rule grammar, no
  LLM, no network); `--json` for agents. ✅ *available now*
- **`goblin convert --from quartz "0 0 9 ? * MON-FRI"`** — translate a schedule
  from another dialect into standard 5-field cron. Handles Quartz's seconds
  field, `?` marker, optional year, and 1-7 (SUN-SAT) weekday numbering, plus
  systemd `OnCalendar` timers (`--from systemd "Mon..Fri 09:00"`, including the
  `daily`/`weekly`/`monthly`/`quarterly`/`yearly` shorthands). `--from k8s`
  validates a Kubernetes CronJob schedule: it expands the robfig/cron `@`-macros
  a CronJob accepts (`@daily`, `@hourly`, `@weekly`, ...), passes plain 5-field
  cron through, and refuses schedules the apiserver rejects — vixie-only
  `@reboot` and Quartz specials pasted in from Java — pointing you at the right
  fix. Only lossless conversions succeed; sub-minute precision, a specific year,
  Quartz's `L`/`W`/`#`, and systemd's `~` are refused with a specific error
  instead of a silent mistranslation. `--json` for agents. ✅ *available now*
- **`goblin doctor`** — lint the crontab you actually have installed: reads it
  via `crontab -l` and runs the same rules as `goblin lint` (`--json`, `--ci`,
  `--user`). A user with no crontab is reported calmly and exits zero.
  ✅ *available now*
- **`goblin completion <shell>`** — generate a tab-completion script for bash,
  zsh, fish, or PowerShell. Each subcommand's `--help` has the exact install
  line. ✅ *available now*
- **`goblin` (live TUI)** — run with no arguments in a terminal to open a live
  preview: type a cron expression and watch the plain English, the next fire
  times, and a week-view heatmap update on every keystroke, with inline lint
  warnings. ✅ *available now*

Prebuilt binaries for Linux, macOS, and Windows ship on every tagged release
(plus `go install` from source) — see [Install](#install).

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
  jobs (warning) — the "thundering herd" seed. With `--tz` it also runs the
  **DST-danger** rule (from the v0.2 backlog): jobs whose wall-clock time falls
  in a spring-forward gap are flagged as silently skipped (warning), and jobs in
  a fall-back overlap are noted as ambiguous (info). `--json` for a stable report
  and `--ci` for a non-zero exit in pipelines.
- **M5 (TUI preview pane)** — done. Running `goblin` with no arguments in a
  terminal opens a live [bubbletea](https://github.com/charmbracelet/bubbletea)
  preview: an input box parses your expression as you type and three panels
  update in real time — the plain-English description, the next fire times (with
  a relative "in 3h 20m" hint), and a week×hour heatmap of fire density. Invalid
  input shows a gentle error instead of crashing, never-firing expressions say
  so, and dead/too-frequent schedules surface inline goblin warnings. Use
  `--tz`, `--no-color`, or `--no-tui` (and piping/redirecting keeps the old
  text greeting for scripts).
- **M6 (English → cron + polish)** — done. `goblin from "<phrase>"`
  turns plain English into a 5-field cron expression with a small, deterministic,
  fully offline rule grammar (no LLM, no network). It covers the common cases
  — "every 15 minutes", "every day at 9am", "every weekday at 6:30pm",
  "weekends at noon", "every monday at 8am", "first of the month at 9am",
  "every january at midnight", named times of day ("every morning",
  "every weekday evening", "at night"), count-per-period phrasings ("once a
  day", "twice a day", "once an hour"), multi-day/-month intervals ("every 3
  days", "every other day", "every other month"), calendar cadences
  ("quarterly", "yearly"), and lists of times that share a minute ("every day
  at 9am and 5pm") — prints the cron line first (so it pipes), echoes
  a plain-English readback plus the next fire, and rejects anything outside the
  grammar rather than guessing (it won't fake an impossible cadence like
  bi-weekly). `--json` for agents. `goblin doctor` now lints
  your installed crontab (`crontab -l`) with the same engine,
  `goblin completion <shell>` emits tab-completion scripts for bash/zsh/fish/
  PowerShell, and a [goreleaser](https://goreleaser.com) pipeline ships
  prebuilt binaries (Linux/macOS/Windows, amd64 + arm64) and checksums on every
  tagged release — the release tag is stamped into `goblin --version`.

That's the v0.1 milestone arc complete. See
[`PLAN.md`](./PLAN.md) for the roadmap and the v0.2+ backlog (the DST danger
report has since landed in `goblin lint --tz`, the thundering-herd
auto-stagger in `goblin stagger`, dialect translation now covers Quartz,
systemd `OnCalendar`, and Kubernetes CronJob schedules in `goblin convert`,
and `goblin diff` shows how fire times shift between two schedules; richer
dialect coverage and the rest remain).

## Install

Three ways to get the `goblin` binary, easiest first.

### Prebuilt binary (recommended)

Grab a release archive for your platform from the
[**Releases**](https://github.com/rwrife/cron-goblin/releases) page
(Linux, macOS, and Windows; both `amd64` and `arm64`), unpack it, and put
`goblin` on your `PATH`. Every archive is listed in `checksums.txt` (SHA-256)
so you can verify the download.

```bash
# Linux/macOS example (swap in the version + your os/arch):
VERSION=v0.1.0
OS=linux            # or: darwin
ARCH=amd64          # or: arm64
curl -sSL "https://github.com/rwrife/cron-goblin/releases/download/${VERSION}/cron-goblin_${VERSION}_${OS}_${ARCH}.tar.gz" \
  | tar -xz goblin
sudo install goblin /usr/local/bin/goblin
goblin --version    # cron-goblin v0.1.0
```

Windows archives are `.zip`; extract `goblin.exe` and drop it somewhere on your
`PATH`.

### `go install`

If you have a Go 1.24+ toolchain:

```bash
go install github.com/rwrife/cron-goblin/cmd/goblin@latest
# installs `goblin` into $(go env GOPATH)/bin
```

(`@latest` tracks the newest tag; pin a specific one with `@v0.1.0`. Binaries
built this way report the module version, or `(devel)` for an untagged build.)

### Build from source

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

./goblin diff "0 9 * * *" "30 9 * * *"         # what shifts if 9:00 -> 9:30 daily (+/-/= timeline)
./goblin diff -n 20 "*/15 * * * *" "*/30 * * * *"  # compare the next 20 runs of each
./goblin diff --window 7d "0 9 * * *" "0 8 * * *"  # compare every run in the next 7 days
./goblin diff --json "0 0 * * *" "0 0 * * 1-5" # machine-readable diff for review tooling/agents

./goblin explain --json "0 0 13 * 5"   # machine-readable summary for scripts/agents
./goblin explain --quiet "30 6 * * 1-5" # no goblin grumbling on stderr

./goblin from "every 15 minutes"        # -> */15 * * * *  (English -> cron)
./goblin from "every weekday at 6:30pm" # -> 30 18 * * 1-5
./goblin from --json "daily at 9am"     # machine-readable result for agents
./goblin from "every blue moon"         # honest error instead of a wrong guess

./goblin convert --from quartz "0 0 9 ? * MON-FRI"  # Quartz -> 0 9 * * MON-FRI
./goblin convert --from quartz "0 0 9 ? * 2-6"      # 2-6 (SUN-SAT) -> 1-5 weekdays
./goblin convert --from quartz --json "0 30 2 * * ?" # machine-readable result
./goblin convert --from quartz "30 0 12 * * ?"      # honest error: cron has no seconds
./goblin convert --from systemd "Mon..Fri 09:00"    # OnCalendar -> 0 9 * * MON,TUE,WED,THU,FRI
./goblin convert --from systemd weekly              # shorthand -> 0 0 * * MON
./goblin convert --from k8s "@daily"                # CronJob macro -> 0 0 * * *
./goblin convert --from k8s "@reboot"               # honest error: k8s has no boot event

./goblin lint /etc/crontab              # lint a crontab file
crontab -l | ./goblin lint -            # lint your own crontab via stdin
./goblin lint --json crontab.txt        # stable JSON report for scripts/agents
./goblin lint --tz America/New_York crontab.txt   # also flag DST gap/overlap hazards
./goblin lint --ci crontab.txt          # non-zero exit if any warning/error

./goblin stagger crontab.txt            # preview a spread for same-minute pile-ups
./goblin stagger --max-spread 30 crontab.txt   # spread each herd within 30 minutes
./goblin stagger --json crontab.txt     # machine-readable stagger plan for agents
./goblin stagger --write --yes crontab.txt     # rewrite the file in place (confirmed)

./goblin doctor                         # lint the crontab you actually have installed
./goblin doctor --json                  # stable JSON report for scripts/agents
./goblin doctor --ci                    # non-zero exit if any warning/error

./goblin completion bash > /etc/bash_completion.d/goblin   # bash tab-completion
./goblin completion zsh  > "${fpath[1]}/_goblin"           # zsh
./goblin completion fish > ~/.config/fish/completions/goblin.fish  # fish
./goblin completion powershell | Out-String | Invoke-Expression   # PowerShell
./goblin completion bash --help         # exact per-shell install instructions
./goblin --version                      # cron-goblin 0.1.0-dev
```

Fire times honor cron's classic day-of-month/day-of-week OR-rule and are
computed against your chosen timezone's wall clock, so daylight-saving
transitions are handled correctly (missing hours are skipped; repeated hours
fire once).

## License

MIT (see `LICENSE` once added).
