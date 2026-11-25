package cli

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"

	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/session"
)

// styles is the visual vocabulary of h2h's output. lipgloss drops all
// styling when the output is not a terminal or NO_COLOR is set, and brings
// the colours down to what the terminal can show.
type styles struct {
	title  lipgloss.Style // headings
	accent lipgloss.Style // names the user can type
	dim    lipgloss.Style // secondary text
	good   lipgloss.Style
	warn   lipgloss.Style
	bad    lipgloss.Style
	// vivid is false for output that is not shown to a person, which gets
	// no gradients.
	vivid bool
}

// The colours of H2H's own interface. Each has a variant for light and for
// dark terminals, so none disappears into the background.
var (
	// accentColor is a vivid mint green, for the text the user can act on.
	accentColor = lipgloss.AdaptiveColor{Light: "#0E8F52", Dark: "#4DEB9A"}
	// secondColor is the teal the accent runs into where a gradient is drawn.
	secondColor = lipgloss.AdaptiveColor{Light: "#0B7C93", Dark: "#38D6E8"}
	// hoverColor is the faint background of the message under the mouse.
	hoverColor = lipgloss.AdaptiveColor{Light: "#E6EFE8", Dark: "#26302A"}
	// frameColor is for borders and rules: white on a dark terminal.
	frameColor = lipgloss.AdaptiveColor{Light: "#3A3A3A", Dark: "#FFFFFF"}
	// States, brighter than the terminal's own palette usually is.
	goodColor = lipgloss.AdaptiveColor{Light: "#0E8F52", Dark: "#4DEB9A"}
	warnColor = lipgloss.AdaptiveColor{Light: "#A86A00", Dark: "#FFC857"}
	badColor  = lipgloss.AdaptiveColor{Light: "#C2334D", Dark: "#FF6B81"}
)

// The bar shown while a harness has the terminal. It has its own fixed
// colours, the same on any background, because its job is to look unlike
// whatever the harness draws above it.
var (
	barStart = rgb{0x2F, 0xE0, 0x8C} // green, at the left
	barEnd   = rgb{0x2B, 0xC4, 0xE6} // teal, at the right
	barGlow  = rgb{0xF2, 0xFF, 0xF8} // the light that travels along it
	barInk   = lipgloss.Color("#062218")
)

const (
	// barSweep is how long the light takes to cross the bar.
	barSweep = 3 * time.Second
	// barGlowWidth is the spread of the light, as a share of the bar.
	barGlowWidth = 0.07
)

func newStyles() styles {
	return styles{
		title:  lipgloss.NewStyle().Bold(true),
		accent: lipgloss.NewStyle().Foreground(accentColor),
		dim:    lipgloss.NewStyle().Faint(true),
		good:   lipgloss.NewStyle().Foreground(goodColor),
		warn:   lipgloss.NewStyle().Foreground(warnColor),
		bad:    lipgloss.NewStyle().Foreground(badColor),
		vivid:  true,
	}
}

// plainStyles renders text unchanged, for output that is not shown to a
// person, such as the context given to a harness.
func plainStyles() styles {
	s := lipgloss.NewStyle()
	return styles{title: s, accent: s, dim: s, good: s, warn: s, bad: s}
}

// rgb is a colour as three channels, for blending.
type rgb struct{ r, g, b uint8 }

func (c rgb) color() lipgloss.Color {
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", c.r, c.g, c.b))
}

// mix blends a towards b: t of 0 is a, t of 1 is b.
func mix(a, b rgb, t float64) rgb {
	t = min(max(t, 0), 1)
	ch := func(x, y uint8) uint8 { return uint8(math.Round(float64(x) + (float64(y)-float64(x))*t)) }
	return rgb{ch(a.r, b.r), ch(a.g, b.g), ch(a.b, b.b)}
}

// parseHex reads a colour written as #RRGGBB.
func parseHex(hex string) rgb {
	var c rgb
	_, _ = fmt.Sscanf(hex, "#%02x%02x%02x", &c.r, &c.g, &c.b)
	return c
}

// shade picks the variant of an adaptive colour for this terminal.
func shade(c lipgloss.AdaptiveColor) rgb {
	if lipgloss.HasDarkBackground() {
		return parseHex(c.Dark)
	}
	return parseHex(c.Light)
}

// gradient colours text from the accent colour into the second one, a
// character at a time. It is for a few short words that carry the brand.
func (st styles) gradient(text string) string {
	runes := []rune(text)
	if !st.vivid || len(runes) < 2 {
		return st.accent.Render(text)
	}
	from, to := shade(accentColor), shade(secondColor)
	var out strings.Builder
	for n, r := range runes {
		c := mix(from, to, float64(n)/float64(len(runes)-1))
		out.WriteString(lipgloss.NewStyle().Bold(true).Foreground(c.color()).Render(string(r)))
	}
	return out.String()
}

// glow is the light of the console's frame while a command runs, or
// nil where there is no colour to show it with.
func (st styles) glow() *console.Glow {
	if !st.vivid || lipgloss.ColorProfile() == termenv.Ascii {
		return nil
	}
	// Pink, violet, blue, cyan and a warm orange.
	g := &console.Glow{
		Colors: []string{"#FF3D9A", "#A855F7", "#4C7DFF", "#22D3EE", "#FF8A5B"},
		Light:  "#FFFFFF", Back: "#1C1C1C",
	}
	if !lipgloss.HasDarkBackground() {
		g.Colors = []string{"#D6246E", "#7E22CE", "#2563EB", "#0891B2", "#EA580C"}
		g.Light, g.Back = "#F0ABFC", "#FFFFFF"
	}
	return g
}

// barColor is the colour of the bar at a position along it, from 0 at the
// left to 1 at the right, at a moment of its sweep, from 0 to 1. The light
// starts and ends off the bar, so it enters and leaves smoothly.
func barColor(pos, sweep float64) rgb {
	centre := -4*barGlowWidth + sweep*(1+8*barGlowWidth)
	d := (pos - centre) / barGlowWidth
	return mix(mix(barStart, barEnd, pos), barGlow, 0.6*math.Exp(-d*d/2))
}

// bar renders a full-width strip of text, for the row h2h keeps on screen
// while a harness has the rest of the terminal. A band of light crosses it
// every few seconds, so it cannot be mistaken for part of the harness.
// since is how long the bar has been up.
func (st styles) bar(text string, width int, since time.Duration) string {
	cells := []rune(text)
	for len(cells) < width {
		cells = append(cells, ' ')
	}
	cells = cells[:max(0, width)]
	sweep := float64(since%barSweep) / float64(barSweep)
	var out strings.Builder
	colorAt := func(at int) rgb { return barColor(float64(at)/float64(max(1, width-1)), sweep) }
	// Each cell has its own colour, so the light moves smoothly. Neighbours
	// that come out the same share one colour change.
	for at := 0; at < len(cells); {
		c, end := colorAt(at), at+1
		for end < len(cells) && colorAt(end) == c {
			end++
		}
		out.WriteString(lipgloss.NewStyle().Bold(true).Foreground(barInk).Background(c.color()).Render(string(cells[at:end])))
		at = end
	}
	return out.String()
}

// state picks the style for a session state.
func (st styles) state(i session.Info) lipgloss.Style {
	switch {
	case i.State == session.Running && i.Attention.Kind != session.NoAttention:
		return st.warn // waiting, or perhaps waiting, for the user
	case i.State == session.Running:
		return st.good
	case i.State == session.Starting:
		return st.warn
	case i.State == session.Exited && i.ExitCode != nil && *i.ExitCode == 0 && i.Error == "":
		return st.dim
	}
	return st.bad
}

// diff colours the rows of a lineDiff.
func (st styles) diff(d string) string {
	lines := strings.Split(strings.TrimSuffix(d, "\n"), "\n")
	for n, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			lines[n] = st.good.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[n] = st.bad.Render(l)
		default:
			lines[n] = st.dim.Render(l)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// table lays out rows in aligned columns and returns one line for the
// header and one per row. Cells are padded before they are styled, so
// colour codes never disturb the alignment.
func table(header []string, rows [][]string, headStyle lipgloss.Style, cellStyle func(row, col int) lipgloss.Style) []string {
	width := make([]int, len(header))
	for c, h := range header {
		width[c] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for c, cell := range r {
			width[c] = max(width[c], lipgloss.Width(cell))
		}
	}
	pad := func(s string, c int) string {
		if c == len(header)-1 {
			return s
		}
		return s + strings.Repeat(" ", width[c]-lipgloss.Width(s)+2)
	}
	var line strings.Builder
	for c, h := range header {
		line.WriteString(headStyle.Render(pad(h, c)))
	}
	lines := []string{line.String()}
	for r, row := range rows {
		line.Reset()
		for c, cell := range row {
			line.WriteString(cellStyle(r, c).Render(pad(cell, c)))
		}
		lines = append(lines, line.String())
	}
	return lines
}
