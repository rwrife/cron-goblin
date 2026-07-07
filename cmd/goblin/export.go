// export.go implements `goblin export`: turn a cron expression into a portable
// iCalendar (.ics) file containing its next N fire times as calendar events.
// This is design-time only — we compute fire times with the same nextrun engine
// that powers `next`/`diff` and serialize them so a human can drop a schedule
// into Google/Apple/Outlook and eyeball a whole month at a glance. We never run
// anything; we just describe when it *would* run. (Backlog: calendar export.)
package main

import (
	"crypto/sha1"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/rwrife/cron-goblin/internal/explain"
	"github.com/rwrife/cron-goblin/internal/goblin"
	"github.com/rwrife/cron-goblin/internal/nextrun"
	"github.com/rwrife/cron-goblin/internal/parse"
	"github.com/spf13/cobra"
)

// icalProdID identifies the generator in the VCALENDAR header, per RFC 5545.
const icalProdID = "-//cron-goblin//goblin export//EN"

// icalCRLF is the line terminator iCalendar requires. Every content line — and
// every folded continuation — is separated by CRLF, not a bare LF.
const icalCRLF = "\r\n"

// newExportCmd builds the `export` subcommand.
func newExportCmd() *cobra.Command {
	var (
		count    int
		tz       string
		out      string
		summary  string
		duration time.Duration
		quiet    bool
	)

	cmd := &cobra.Command{
		Use:   "export <cron-expression>",
		Short: "Export the next N fire times to an .ics calendar file",
		Long: "Parse a standard 5-field cron expression and emit a standards-compliant\n" +
			"iCalendar (.ics) file whose events are the schedule's next N fire times.\n" +
			"Import it into Google Calendar, Apple Calendar, or Outlook to see an\n" +
			"entire month of a job at a glance.\n\n" +
			"Fire times are computed in the chosen timezone (--tz, default local) and\n" +
			"serialized as UTC (the unambiguous, universally-importable form). By\n" +
			"default each run is a one-minute point-in-time event; pass --duration to\n" +
			"give events a length. Expressions that never fire produce an empty but\n" +
			"valid calendar.\n\n" +
			"Output goes to stdout so it pipes; use -o to write a file instead.",
		Example: "  goblin export \"0 9 * * 1-5\" -n 20 > weekday-standup.ics\n" +
			"  goblin export --tz America/New_York \"30 2 * * 0\" -o backup.ics\n" +
			"  goblin export \"*/15 * * * *\" --duration 5m --summary \"cache warm\"",
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			expr := args[0]

			if count <= 0 {
				return fmt.Errorf("count (-n) must be positive, got %d", count)
			}
			if duration < 0 {
				return fmt.Errorf("duration must not be negative, got %s", duration)
			}

			loc, err := loadLocation(tz)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(tz))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: unknown timezone %q: %v\n", tz, err)
				return err
			}

			sched, err := parse.Parse(expr)
			if err != nil {
				if !quiet {
					fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(expr))
				}
				fmt.Fprintf(cmd.ErrOrStderr(), "error: %v\n", err)
				return err
			}

			english := explain.Explain(sched)
			title := summary
			if title == "" {
				title = english
			}

			now := time.Now()
			runs := nextrun.NextN(sched, now, count, loc)

			// Persona reaction on stderr, honoring --quiet, before we (maybe)
			// write the calendar to stdout so the two streams don't interleave.
			if !quiet {
				fmt.Fprintln(cmd.ErrOrStderr(), goblin.Line(expr))
				if len(runs) == 0 {
					fmt.Fprintf(cmd.ErrOrStderr(),
						"warning: %q never fires — writing an empty calendar.\n", sched.Raw)
				}
			}

			ics := buildICS(sched, english, title, loc, runs, duration, now)

			// -o writes a file and prints nothing to stdout; otherwise stream
			// the calendar so it can be piped/redirected.
			if out != "" {
				if err := os.WriteFile(out, []byte(ics), 0o644); err != nil {
					fmt.Fprintf(cmd.ErrOrStderr(), "error: writing %s: %v\n", out, err)
					return err
				}
				if !quiet {
					fmt.Fprintf(cmd.ErrOrStderr(), "Wrote %d event(s) to %s.\n", len(runs), out)
				}
				return nil
			}

			_, err = io.WriteString(cmd.OutOrStdout(), ics)
			return err
		},
		SilenceUsage:  true,
		SilenceErrors: true,
	}

	cmd.Flags().IntVarP(&count, "count", "n", 20, "how many upcoming runs to export as events")
	cmd.Flags().StringVar(&tz, "tz", "",
		"timezone the schedule fires in (IANA name, e.g. America/New_York; default: local)")
	cmd.Flags().StringVarP(&out, "out", "o", "", "write the calendar to this file instead of stdout")
	cmd.Flags().StringVar(&summary, "summary", "",
		"event title (default: the schedule's plain-English description)")
	cmd.Flags().DurationVar(&duration, "duration", 0,
		"give events this length (e.g. 15m, 1h); default: 1-minute point-in-time events")
	cmd.Flags().BoolVar(&quiet, "quiet", false, "silence the goblin's grumbling (stderr persona)")

	return cmd
}

// buildICS renders a full VCALENDAR string for the given fire times. Times are
// emitted in UTC (the "Z" form) so the file imports unambiguously into any
// calendar app regardless of the reader's local zone; the schedule's intended
// timezone is recorded in each event's DESCRIPTION for provenance.
func buildICS(
	sched parse.Schedule,
	english, title string,
	loc *time.Location,
	runs []time.Time,
	dur time.Duration,
	stamp time.Time,
) string {
	var b strings.Builder

	writeICSLine(&b, "BEGIN:VCALENDAR")
	writeICSLine(&b, "VERSION:2.0")
	writeICSLine(&b, "PRODID:"+icalProdID)
	writeICSLine(&b, "CALSCALE:GREGORIAN")
	writeICSLine(&b, "METHOD:PUBLISH")
	writeICSLine(&b, icalTextProp("X-WR-CALNAME", "cron-goblin: "+sched.Raw))

	dtstamp := icalUTC(stamp)
	// Base the UID on the schedule + fire instant so re-exports are stable and
	// importing twice updates rather than duplicates events.
	for _, t := range runs {
		start := t.UTC()
		writeICSLine(&b, "BEGIN:VEVENT")
		writeICSLine(&b, "UID:"+eventUID(sched.Raw, start))
		writeICSLine(&b, "DTSTAMP:"+dtstamp)
		writeICSLine(&b, "DTSTART:"+icalUTC(start))
		if dur > 0 {
			writeICSLine(&b, "DTEND:"+icalUTC(start.Add(dur)))
		} else {
			// Point-in-time: a one-minute window reads cleanly in most apps.
			writeICSLine(&b, "DTEND:"+icalUTC(start.Add(time.Minute)))
		}
		writeICSLine(&b, icalTextProp("SUMMARY", title))
		writeICSLine(&b, icalTextProp("DESCRIPTION",
			fmt.Sprintf("%s\nExpression: %s\nTimezone: %s\nGenerated by cron-goblin.",
				english, sched.Raw, loc.String())))
		writeICSLine(&b, "TRANSP:TRANSPARENT")
		writeICSLine(&b, "END:VEVENT")
	}

	writeICSLine(&b, "END:VCALENDAR")
	return b.String()
}

// eventUID derives a stable, unique-per-fire UID from the raw expression and the
// event's UTC instant. Same schedule + same instant → same UID across re-exports.
func eventUID(raw string, t time.Time) string {
	sum := sha1.Sum([]byte(raw + "@" + t.Format(time.RFC3339)))
	return fmt.Sprintf("%x@cron-goblin", sum[:8])
}

// icalUTC formats a time as an RFC 5545 UTC date-time: YYYYMMDDTHHMMSSZ.
func icalUTC(t time.Time) string {
	return t.UTC().Format("20060102T150405Z")
}

// icalTextProp builds a "NAME:VALUE" content line with the VALUE escaped per
// RFC 5545 text rules (backslash, semicolon, comma, and newlines). Folding to
// the 75-octet limit is applied later by writeICSLine.
func icalTextProp(name, value string) string {
	return name + ":" + escapeICSText(value)
}

// escapeICSText escapes a string for use as an iCalendar TEXT value: backslashes
// first, then commas/semicolons, and literal newlines become "\n".
func escapeICSText(s string) string {
	r := strings.NewReplacer(
		"\\", "\\\\",
		";", "\\;",
		",", "\\,",
		"\r\n", "\\n",
		"\n", "\\n",
		"\r", "\\n",
	)
	return r.Replace(s)
}

// writeICSLine folds a single logical content line to the 75-octet limit and
// writes it with CRLF terminators, per RFC 5545 section 3.1. Continuation lines
// begin with a single space. Folding is byte-based (octets), and we take care
// not to split a multi-byte UTF-8 rune across a fold boundary.
func writeICSLine(b *strings.Builder, line string) {
	const limit = 75
	if len(line) <= limit {
		b.WriteString(line)
		b.WriteString(icalCRLF)
		return
	}

	// First segment: up to `limit` octets. Subsequent segments reserve one
	// octet for the leading space, so they carry up to limit-1 payload octets.
	i := 0
	first := true
	for i < len(line) {
		max := limit
		if !first {
			max = limit - 1
		}
		end := i + max
		if end > len(line) {
			end = len(line)
		}
		// Back off if we'd split a UTF-8 continuation byte (0b10xxxxxx).
		for end > i && end < len(line) && line[end]&0xC0 == 0x80 {
			end--
		}
		if !first {
			b.WriteByte(' ')
		}
		b.WriteString(line[i:end])
		b.WriteString(icalCRLF)
		i = end
		first = false
	}
}
