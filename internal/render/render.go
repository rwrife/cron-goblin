// Package render turns schedule data into compact terminal visuals: a
// week-by-hour fire-density heatmap and a next-runs table. It is the M5
// presentation layer that the TUI (internal/tui) draws, kept separate so the
// rendering is pure, deterministic, and unit-testable without a running
// bubbletea program.
//
// Everything here is a pure function of its inputs plus a *time.Location: the
// same fire times rendered for the same width and zone always produce the same
// string. Styling uses lipgloss, but the layout (which cell is which hour, how
// density buckets into glyphs) is plain arithmetic so tests can assert on the
// structure rather than ANSI codes.
package render

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
)

// dowShort labels the seven heatmap rows, Sunday first to match time.Weekday's
// numbering (Sunday == 0).
var dowShort = [7]string{"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"}

// heatRamp maps a per-cell fire count to a glyph. Index 0 is "no fires"; the
// remaining glyphs grow denser. The ramp is ASCII-with-blocks so it renders
// legibly even on terminals that mangle exotic Unicode.
var heatRamp = []rune{'·', '▏', '▍', '▆', '█'}

// Palette holds the lipgloss styles render uses. Callers may supply their own
// (e.g. a no-color palette) so the same layout works with or without ANSI.
type Palette struct {
	Header lipgloss.Style // column/row headers
	Empty  lipgloss.Style // a cell with zero fires
	Low    lipgloss.Style // a lightly-loaded cell
	High   lipgloss.Style // a heavily-loaded cell
	Label  lipgloss.Style // generic labels (titles, captions)
	Warn   lipgloss.Style // warnings / errors
	Faint  lipgloss.Style // de-emphasized text
}

// DefaultPalette returns the standard colored palette. Colors are chosen from
// the 256-color cube so they degrade sensibly on basic terminals.
func DefaultPalette() Palette {
	return Palette{
		Header: lipgloss.NewStyle().Foreground(lipgloss.Color("245")),
		Empty:  lipgloss.NewStyle().Foreground(lipgloss.Color("238")),
		Low:    lipgloss.NewStyle().Foreground(lipgloss.Color("78")),
		High:   lipgloss.NewStyle().Foreground(lipgloss.Color("203")),
		Label:  lipgloss.NewStyle().Foreground(lipgloss.Color("252")),
		Warn:   lipgloss.NewStyle().Foreground(lipgloss.Color("214")),
		Faint:  lipgloss.NewStyle().Foreground(lipgloss.Color("240")),
	}
}

// NoColorPalette returns a palette whose styles emit no ANSI color, for
// --no-color / non-color terminals. Layout is identical; only the paint is
// stripped.
func NoColorPalette() Palette {
	plain := lipgloss.NewStyle()
	return Palette{
		Header: plain, Empty: plain, Low: plain, High: plain,
		Label: plain, Warn: plain, Faint: plain,
	}
}

// HeatGrid is a 7×24 matrix of fire counts: rows are weekdays (Sunday==0),
// columns are hours (0..23). It is produced by BuildHeatGrid and consumed by
// Heatmap; exposing it lets tests assert on the raw density independent of
// glyphs and color.
type HeatGrid [7][24]int

// Max returns the largest single-cell count in the grid, used to scale the
// glyph ramp. A zero grid returns 0.
func (g HeatGrid) Max() int {
	max := 0
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			if g[d][h] > max {
				max = g[d][h]
			}
		}
	}
	return max
}

// Total returns the sum of all cells (total fires represented).
func (g HeatGrid) Total() int {
	sum := 0
	for d := 0; d < 7; d++ {
		for h := 0; h < 24; h++ {
			sum += g[d][h]
		}
	}
	return sum
}

// BuildHeatGrid buckets fire times into the weekday×hour grid using their
// wall-clock in loc (nil → UTC). Each time contributes one to the cell for its
// weekday and hour; minutes within the hour are aggregated. This makes the
// heatmap a density view ("how busy is Monday at 09:00") rather than a literal
// minute timeline.
func BuildHeatGrid(times []time.Time, loc *time.Location) HeatGrid {
	if loc == nil {
		loc = time.UTC
	}
	var g HeatGrid
	for _, t := range times {
		lt := t.In(loc)
		d := int(lt.Weekday())
		h := lt.Hour()
		if d >= 0 && d < 7 && h >= 0 && h < 24 {
			g[d][h]++
		}
	}
	return g
}

// glyphFor picks a ramp glyph for a cell count given the grid's max. Zero maps
// to the empty glyph; positive counts scale linearly across the remaining
// ramp so the busiest cell always shows the densest glyph.
func glyphFor(count, max int) rune {
	if count <= 0 {
		return heatRamp[0]
	}
	if max <= 0 {
		return heatRamp[len(heatRamp)-1]
	}
	// Buckets 1..len-1 across the (0,max] range.
	steps := len(heatRamp) - 1
	idx := 1 + (count-1)*(steps-1)/maxInt(1, max-1)
	if idx < 1 {
		idx = 1
	}
	if idx > steps {
		idx = steps
	}
	return heatRamp[idx]
}

// Heatmap renders a HeatGrid as a labeled week×hour block. The header row marks
// hours 0,6,12,18 for orientation; each weekday row shows 24 glyphs. When the
// grid is entirely empty a single explanatory line is returned instead so the
// pane never looks broken for a never-firing schedule.
func Heatmap(g HeatGrid, p Palette) string {
	if g.Total() == 0 {
		return p.Faint.Render("(no fires in the previewed window)")
	}
	max := g.Max()

	var b strings.Builder

	// Hour axis: a sparse ruler so the row isn't a wall of digits.
	axis := make([]rune, 24)
	for h := 0; h < 24; h++ {
		switch h {
		case 0, 6, 12, 18:
			axis[h] = '|'
		default:
			axis[h] = ' '
		}
	}
	b.WriteString(p.Header.Render("     " + string(axis)))
	b.WriteByte('\n')
	b.WriteString(p.Header.Render("     0     6     12    18   "))
	b.WriteByte('\n')

	for d := 0; d < 7; d++ {
		b.WriteString(p.Header.Render(fmt.Sprintf("%-4s ", dowShort[d])))
		for h := 0; h < 24; h++ {
			cnt := g[d][h]
			glyph := string(glyphFor(cnt, max))
			switch {
			case cnt == 0:
				b.WriteString(p.Empty.Render(glyph))
			case cnt*2 >= max+1:
				b.WriteString(p.High.Render(glyph))
			default:
				b.WriteString(p.Low.Render(glyph))
			}
		}
		b.WriteByte('\n')
	}

	b.WriteString(p.Faint.Render(fmt.Sprintf("peak %d fire(s)/hour · %d total in window", max, g.Total())))
	return strings.TrimRight(b.String(), "\n")
}

// NextRuns renders up to limit fire times as a compact list with a relative
// "in …" hint, evaluated against now. Passing limit <= 0 shows them all. When
// runs is empty a never-fires line is returned. The reference now is a
// parameter (not time.Now) so output is deterministic in tests.
func NextRuns(runs []time.Time, now time.Time, loc *time.Location, limit int, p Palette) string {
	if loc == nil {
		loc = time.UTC
	}
	if len(runs) == 0 {
		return p.Warn.Render("never fires — no matching date ahead")
	}
	if limit <= 0 || limit > len(runs) {
		limit = len(runs)
	}

	var b strings.Builder
	for i := 0; i < limit; i++ {
		t := runs[i].In(loc)
		rel := humanizeUntil(t.Sub(now))
		line := fmt.Sprintf("%s  %s", t.Format("Mon 2006-01-02 15:04 MST"), p.Faint.Render("("+rel+")"))
		b.WriteString(p.Label.Render(line))
		if i < limit-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

// humanizeUntil renders a forward duration as a short "in 3h 20m" style hint.
// Past or zero durations render as "now". It keeps at most two units so the
// hint stays compact.
func humanizeUntil(d time.Duration) string {
	if d <= 0 {
		return "now"
	}
	d = d.Round(time.Minute)
	days := int(d / (24 * time.Hour))
	d -= time.Duration(days) * 24 * time.Hour
	hours := int(d / time.Hour)
	d -= time.Duration(hours) * time.Hour
	mins := int(d / time.Minute)

	parts := make([]string, 0, 2)
	switch {
	case days > 0:
		parts = append(parts, fmt.Sprintf("%dd", days))
		if hours > 0 {
			parts = append(parts, fmt.Sprintf("%dh", hours))
		}
	case hours > 0:
		parts = append(parts, fmt.Sprintf("%dh", hours))
		if mins > 0 {
			parts = append(parts, fmt.Sprintf("%dm", mins))
		}
	default:
		parts = append(parts, fmt.Sprintf("%dm", mins))
	}
	return "in " + strings.Join(parts, " ")
}

// Warnings renders lint messages (already de-personified strings) as a short
// bulleted block. Each entry is one line; an empty slice yields an empty
// string so callers can omit the section entirely. Messages are sorted for
// stable output.
func Warnings(msgs []string, p Palette) string {
	if len(msgs) == 0 {
		return ""
	}
	sorted := append([]string(nil), msgs...)
	sort.Strings(sorted)
	var b strings.Builder
	for i, m := range sorted {
		b.WriteString(p.Warn.Render("⚠ " + m))
		if i < len(sorted)-1 {
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}
