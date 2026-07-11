// blame.go implements `goblin blame`: read a crontab and print it back
// annotated inline, git-blame style. Where `lint` surfaces problems, `blame`
// explains *everything* — each schedule line is echoed unchanged with a
// trailing `# <english> · next: <time>` comment so you can eyeball a whole
// crontab and instantly see what each cryptic line does and when it next runs.
//
// The line-walking, English, and next-fire logic all live in internal/blame
// (reusing internal/explain and internal/nextrun); this file is presentation:
// column alignment, the trailing comment, --tz/--json/--quiet plumbing.
package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/blame"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/spf13/cobra"
)

// blameRowJSON is the stable machine-readable shape emitted by `blame --json`,
// one object per source line. Next is an RFC3339 timestamp, or empty when the
// line never fires or isn't a schedule. Dead is true only for schedule lines
// whose expression can never fire.
type blameRowJSON struct {
	Line     int    `json:"line"`
	Raw      string `json:"raw"`
	Schedule string `json:"schedule"`
	English  string `json:"english"`
	Next     string `json:"next"`
	Dead     bool   `json:"dead"`
}

// newBlameCmd builds the `blame` subcommand.
func newBlameCmd() *cobra.Command {
	var (
		asJSON bool
		tz     string
		quiet  bool
	)

	cmd := &cobra.Command{
		Use:   "blame [crontab]",
		Short: "Annotate a crontab inline with meaning + next run",
		Long: "Read a crontab (file or stdin) and print it back annotated inline,\n" +
			"git-blame style: each schedule line is echoed unchanged with a\n" +
			"trailing `# <english> · next: <time>` comment. Comments and blank\n" +
			"lines are preserved; dead expressions show `next: never`.\n\n" +
			"Where `lint` lists problems, `blame` explains everything — the\n" +
			"readable \"what is this crontab even doing\" view. Pass --json for a\n" +
			"stable per-line array, and --tz to control the next-fire timezone.",
		Example: "  goblin blame crontab.txt\n" +
			"  cat crontab | goblin blame -\n" +
			"  goblin blame --tz America/New_York --json crontab.txt",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			path := ""
			if len(args) == 1 {
				path = args[0]
			}

			loc, err := loadLocation(tz)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
				return err
			}

			src, r, closer, err := openCrontab(cmd, path)
			if err != nil {
				return err
			}
			if closer != nil {
				defer closer.Close()
			}

			rows, err := blame.Annotate(r, time.Now(), loc)
			if err != nil {
				return err
			}

			if asJSON {
				out := make([]blameRowJSON, 0, len(rows))
				for _, row := range rows {
					jr := blameRowJSON{Line: row.Line, Raw: row.Raw}
					if row.Kind == blame.KindSchedule {
						jr.Schedule = scheduleText(row.Raw)
						jr.English = row.English
						jr.Dead = row.Dead
						if !row.Dead {
							jr.Next = row.Next.In(loc).Format(time.RFC3339)
						}
					}
					out = append(out, jr)
				}
				enc := json.NewEncoder(cmd.OutOrStdout())
				enc.SetIndent("", "  ")
				return enc.Encode(out)
			}

			// Human output: grumble on stderr, annotated crontab on stdout.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(src))
			}
			writeBlameHuman(cmd, rows, loc)
			return nil
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().BoolVar(&asJSON, "json", false, "emit a stable per-line JSON array")
	cmd.Flags().StringVar(&tz, "tz", "", "timezone for the next-fire column (default local)")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}

// writeBlameHuman renders the annotated crontab. Schedule lines are padded to a
// common width so the trailing `# ...` comments line up; every other line is
// echoed verbatim.
func writeBlameHuman(cmd *cobra.Command, rows []blame.Row, loc *time.Location) {
	// Compute the alignment column: the widest schedule raw line.
	width := 0
	for _, row := range rows {
		if row.Kind == blame.KindSchedule && len(row.Raw) > width {
			width = len(row.Raw)
		}
	}

	out := cmd.OutOrStdout()
	for _, row := range rows {
		switch row.Kind {
		case blame.KindSchedule:
			next := "never"
			marker := ""
			if row.Dead {
				marker = "⚠ dead · "
			} else {
				next = row.Next.In(loc).Format(time.RFC3339)
			}
			fmt.Fprintf(out, "%-*s  # %s%s · next: %s\n",
				width, row.Raw, marker, row.English, next)
		case blame.KindUnparseable:
			fmt.Fprintf(out, "%-*s  # ⚠ %s\n", width, row.Raw, row.Note)
		default:
			fmt.Fprintln(out, row.Raw)
		}
	}
}

// scheduleText reconstructs just the cron-expression portion of a schedule
// line: the raw line with the trailing command stripped off. It is used for the
// stable JSON `schedule` field.
func scheduleText(raw string) string {
	fields := strings.Fields(strings.TrimSpace(raw))
	if len(fields) < 5 {
		return strings.TrimSpace(raw)
	}
	return strings.Join(fields[:5], " ")
}
