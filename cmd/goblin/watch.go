// watch.go implements `goblin watch`: a tiny always-on panel that shows a
// live-updating countdown to the next fire for one or many schedules, re-sorting
// so the soonest job floats to the top. It's the "is my 3am job about to go
// off?" glance — the runtime *feel* without being a monitor. Like the rest of
// cron-goblin it never executes anything; it just counts down against the wall
// clock using the same nextrun engine that powers `next`/`export`/`diff`.
// (Backlog: watch mode.)
package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"sort"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/lint"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// watchJob is a single schedule being watched: its raw expression and the
// parsed form the countdown is computed from. Lines that failed to parse are
// dropped before we get here, so Schedule is always valid.
type watchJob struct {
	Raw      string
	Schedule parse.Schedule
}

// watchRow is a single rendered line in a frame: a job, its next fire time in
// the display zone, and whether it fires at all within the horizon. When
// NeverFires is true, Next is the zero value and the row sinks to the bottom.
type watchRow struct {
	Raw        string
	Next       time.Time
	NeverFires bool
}

// newWatchCmd builds the `watch` subcommand.
func newWatchCmd() *cobra.Command {
	var (
		expr     string
		tz       string
		interval time.Duration
		once     bool
		quiet    bool
	)

	cmd := &cobra.Command{
		Use:   "watch [crontab-file]",
		Short: "Live countdown to the next fire across one or many schedules",
		Long: "Read one or many schedules (a crontab file, stdin when omitted or '-',\n" +
			"or a single inline expression via --expr) and show a live-updating\n" +
			"countdown to each job's next fire, re-sorted so the soonest is on top.\n\n" +
			"It is design-time only: nothing is ever executed. We just count down\n" +
			"against the wall clock using the same fire-time engine as `next`.\n\n" +
			"The panel redraws in place every second (configurable with --interval)\n" +
			"until you Ctrl-C. Jobs that can never fire are shown as `never` and\n" +
			"sink to the bottom rather than erroring. Use --once to print a single\n" +
			"frame and exit (the script/CI-friendly path).",
		Example: "  goblin watch --expr \"*/5 * * * *\"\n" +
			"  crontab -l | goblin watch\n" +
			"  goblin watch --tz America/New_York crontab.txt\n" +
			"  goblin watch --once --expr \"0 9 * * 1-5\"   # one frame, then exit",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if interval <= 0 {
				return fmt.Errorf("interval must be positive, got %s", interval)
			}

			loc, err := loadLocation(tz)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
				return err
			}

			jobs, err := collectWatchJobs(cmd, expr, args)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line("watch"))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}
			if len(jobs) == 0 {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line("watch"))
				}
				fmt.Fprintln(cmd.ErrOrStderr(),
					"error: nothing to watch — pass --expr, a crontab file, or pipe one in")
				return fmt.Errorf("no schedules to watch")
			}

			// Persona once, up front, on stderr (honoring --quiet) so it never
			// interleaves with the redrawn frames on stdout.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(jobs[0].Raw))
			}

			out := cmd.OutOrStdout()

			// --once is the deterministic, CI-safe path: render exactly one
			// frame for "now" and return. No ANSI, no loop, no signal handling.
			if once {
				fmt.Fprint(out, renderFrame(jobs, time.Now(), loc, false))
				return nil
			}

			return runWatchLoop(cmd.Context(), out, jobs, loc, interval)
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().StringVar(&expr, "expr", "",
		"watch a single inline cron expression instead of a file/stdin")
	cmd.Flags().StringVar(&tz, "tz", "",
		"timezone for the displayed fire times (IANA name, e.g. America/New_York; default: local)")
	cmd.Flags().DurationVar(&interval, "interval", time.Second,
		"how often to redraw the countdown (e.g. 1s, 500ms)")
	cmd.Flags().BoolVar(&once, "once", false,
		"print a single frame and exit (the script/CI-friendly path)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}

// collectWatchJobs resolves the schedules to watch from the flags/args. Exactly
// one source is allowed: --expr (a single inline expression) OR a crontab file/
// stdin positional. Passing both is a usage error. When --expr is empty and no
// positional is given, we read stdin (so `crontab -l | goblin watch` works).
//
// Crontab parsing reuses the lint package so comments, blank lines, and env
// assignments are skipped consistently with `goblin lint`. Malformed lines are
// surfaced as an error rather than silently dropped.
func collectWatchJobs(cmd *cobra.Command, expr string, args []string) ([]watchJob, error) {
	path := ""
	if len(args) == 1 {
		path = args[0]
	}

	if expr != "" {
		if path != "" {
			return nil, fmt.Errorf("use either --expr or a crontab file, not both")
		}
		sched, err := parse.Parse(expr)
		if err != nil {
			return nil, err
		}
		return []watchJob{{Raw: sched.Raw, Schedule: sched}}, nil
	}

	src, reader, closer, err := openCrontab(cmd, path)
	if err != nil {
		return nil, err
	}
	if closer != nil {
		defer closer.Close()
	}

	entries, err := lint.ParseCrontab(reader)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", src, err)
	}

	var jobs []watchJob
	for _, e := range entries {
		if e.ParseErr != nil {
			return nil, fmt.Errorf("line %d: could not parse cron fields: %w", e.Line, e.ParseErr)
		}
		jobs = append(jobs, watchJob{Raw: e.Schedule.Raw, Schedule: e.Schedule})
	}
	return jobs, nil
}

// computeRows evaluates each job's next fire strictly after `now` in loc and
// returns rows sorted soonest-first. Never-fires jobs get NeverFires=true and
// sort to the bottom; ties (including two never-fires jobs) fall back to the
// raw expression so ordering is stable and deterministic.
func computeRows(jobs []watchJob, now time.Time, loc *time.Location) []watchRow {
	rows := make([]watchRow, 0, len(jobs))
	for _, j := range jobs {
		next, err := nextrun.Next(j.Schedule, now, loc)
		if err != nil {
			rows = append(rows, watchRow{Raw: j.Raw, NeverFires: true})
			continue
		}
		rows = append(rows, watchRow{Raw: j.Raw, Next: next})
	}

	sort.SliceStable(rows, func(i, j int) bool {
		a, b := rows[i], rows[j]
		if a.NeverFires != b.NeverFires {
			// Firing jobs come before never-fires ones.
			return !a.NeverFires
		}
		if !a.NeverFires && !a.Next.Equal(b.Next) {
			return a.Next.Before(b.Next)
		}
		return a.Raw < b.Raw
	})
	return rows
}

// renderFrame builds one full frame of text: a header, one aligned row per job
// (next-fire | countdown | expression) sorted soonest-first, and a footer hint.
// When clear is true it is prefixed with ANSI cursor-home + clear-screen so a
// live loop redraws in place; the --once path passes false for clean, capturable
// output. The trailing newline keeps successive frames from running together.
func renderFrame(jobs []watchJob, now time.Time, loc *time.Location, clear bool) string {
	rows := computeRows(jobs, now, loc)

	var b strings.Builder
	if clear {
		// ESC[H moves the cursor home; ESC[2J clears the screen. Together they
		// give a flicker-light in-place redraw without a full TUI framework.
		b.WriteString("\x1b[H\x1b[2J")
	}

	fmt.Fprintf(&b, "goblin watch — %d job(s) — %s\n",
		len(rows), now.In(loc).Format("2006-01-02 15:04:05 MST"))
	fmt.Fprintln(&b, strings.Repeat("─", 60))

	// Compute the column width for the next-fire timestamps so the countdown
	// and expression columns line up regardless of zone abbreviation length.
	const header0, header1, header2 = "NEXT FIRE", "IN", "EXPRESSION"
	fireWidth := len(header0)
	countWidth := len(header1)
	fireCol := make([]string, len(rows))
	countCol := make([]string, len(rows))
	for i, r := range rows {
		if r.NeverFires {
			fireCol[i] = "never"
			countCol[i] = "—"
		} else {
			fireCol[i] = r.Next.In(loc).Format("2006-01-02 15:04:05 MST")
			countCol[i] = humanizeCountdown(r.Next.Sub(now))
		}
		if len(fireCol[i]) > fireWidth {
			fireWidth = len(fireCol[i])
		}
		if len(countCol[i]) > countWidth {
			countWidth = len(countCol[i])
		}
	}

	fmt.Fprintf(&b, "%-*s  %-*s  %s\n", fireWidth, header0, countWidth, header1, header2)
	for i, r := range rows {
		fmt.Fprintf(&b, "%-*s  %-*s  %s\n", fireWidth, fireCol[i], countWidth, countCol[i], r.Raw)
	}

	fmt.Fprintln(&b, strings.Repeat("─", 60))
	fmt.Fprintln(&b, "(never = no fire within the search horizon · Ctrl-C to exit)")
	return b.String()
}

// humanizeCountdown renders a duration until the next fire as a compact
// "in Xh Ym Zs" string, dropping leading zero units. Durations are rounded down
// to whole seconds (the countdown ticks in seconds). A non-positive duration —
// the fire moment has arrived between ticks — reads as "now".
func humanizeCountdown(d time.Duration) string {
	secs := int64(d / time.Second)
	if secs <= 0 {
		return "now"
	}

	days := secs / 86400
	secs %= 86400
	hours := secs / 3600
	secs %= 3600
	mins := secs / 60
	secs %= 60

	parts := make([]string, 0, 4)
	if days > 0 {
		parts = append(parts, fmt.Sprintf("%dd", days))
	}
	if hours > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if mins > 0 || len(parts) > 0 {
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	parts = append(parts, fmt.Sprintf("%ds", secs))
	return "in " + strings.Join(parts, " ")
}

// runWatchLoop drives the live, in-place countdown until the context is
// cancelled or the process receives an interrupt (Ctrl-C). It draws an initial
// frame immediately, then redraws every `interval`. On exit it restores the
// cursor and prints a newline so the shell prompt lands cleanly.
//
// This path is intentionally kept out of the unit-tested surface (it loops on a
// ticker and signals); the deterministic rendering it relies on lives in
// renderFrame/computeRows/humanizeCountdown, which are covered directly.
func runWatchLoop(parent context.Context, out io.Writer, jobs []watchJob, loc *time.Location, interval time.Duration) error {
	if parent == nil {
		parent = context.Background()
	}
	ctx, stop := signal.NotifyContext(parent, os.Interrupt)
	defer stop()

	draw := func() { fmt.Fprint(out, renderFrame(jobs, time.Now(), loc, true)) }

	draw() // paint immediately so there's no blank first interval

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			// Show the cursor again (in case a future frame hides it) and drop
			// to a fresh line so the prompt isn't glued to the last frame.
			fmt.Fprint(out, "\x1b[?25h\n")
			return nil
		case <-ticker.C:
			draw()
		}
	}
}
