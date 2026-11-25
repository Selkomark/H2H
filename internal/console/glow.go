package console

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// While a command runs, the lines of the page's frame glow: colours flow
// round it, with two spots of light travelling over them, and the page takes
// the same light faintly as its background. It says "working" without taking
// a row. A theme without glow colours, as for a terminal without colour,
// gets a spinner and the word.

// Glow is the colours of that light. Colours are written as #RRGGBB.
type Glow struct {
	Colors []string // flowing round the frame, the last running into the first
	Light  string   // the spots that travel over them
	Back   string   // the terminal's background, which the light fades in from
}

const (
	// glowFrame is the time between two frames of the glow.
	glowFrame = 40 * time.Millisecond
	// spinnerHold is how many of those frames one frame of the spinner
	// stays for, so that it keeps its own pace.
	spinnerHold = 2

	glowTurn   = 6 * time.Second // the colours go once round the frame
	lightTurn  = 4 * time.Second // so does each spot of light
	glowBreath = 2 * time.Second // the light swells and fades
	glowFadeIn = 350 * time.Millisecond

	glowCycles = 2    // how many times the colours repeat round the frame
	lightWidth = 0.04 // the spread of a spot, as a share of the way round
	// A row is about twice as tall as a column is wide.
	rowLength = 2
)

// heavy turns the frame's lines into their heavier forms, which have no
// rounded corners.
var heavy = strings.NewReplacer("─", "━", "│", "┃", "╭", "┏", "╮", "┓", "╰", "┗", "╯", "┛", "├", "┣", "┤", "┫")

// newSpinner is the console's one clock for what moves while a command
// runs. With a glow it ticks at the glow's pace.
func newSpinner(th Theme) spinner.Model {
	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	if th.Glow != nil {
		var frames []string
		for _, f := range spinner.MiniDot.Frames {
			for range spinnerHold {
				frames = append(frames, f)
			}
		}
		sp.Spinner = spinner.Spinner{Frames: frames, FPS: glowFrame}
	}
	sp.Style = th.Accent
	return sp
}

// glowing reports whether the page's frame is lit: a command is
// running and the input is on screen, not a question waiting for the user.
func (m *model) glowing() bool {
	return m.o.Theme.Glow != nil && m.busy && m.dialog == nil && m.chat == nil
}

type lightColor struct{ r, g, b float64 }

func hexLight(s string) lightColor {
	var r, g, b int
	_, _ = fmt.Sscanf(s, "#%02x%02x%02x", &r, &g, &b)
	return lightColor{float64(r), float64(g), float64(b)}
}

func blend(a, b lightColor, t float64) lightColor {
	t = min(max(t, 0), 1)
	return lightColor{a.r + (b.r-a.r)*t, a.g + (b.g-a.g)*t, a.b + (b.b-a.b)*t}
}

func (c lightColor) color() lipgloss.Color {
	return lipgloss.Color(fmt.Sprintf("#%02X%02X%02X", int(c.r+.5), int(c.g+.5), int(c.b+.5)))
}

// at is the light at a place on the way round the frame, from 0 to 1, a
// time after the glow began: its colour, and how brightly a spot is on it.
func (g Glow) at(place float64, since time.Duration) (c lightColor, spot float64) {
	turn := func(d time.Duration) float64 { return float64(since%d) / float64(d) }
	wrap := func(x float64) float64 { return x - math.Floor(x) }

	along := wrap(place*glowCycles-turn(glowTurn)) * float64(len(g.Colors))
	n := int(along) % len(g.Colors)
	f := along - math.Floor(along)
	f = f * f * (3 - 2*f) // eases from one colour into the next
	c = blend(hexLight(g.Colors[n]), hexLight(g.Colors[(n+1)%len(g.Colors)]), f)

	// Two spots go opposite ways, and flare where they cross.
	for _, centre := range []float64{turn(lightTurn), wrap(0.5 - turn(lightTurn))} {
		d := math.Abs(wrap(place-centre+0.5) - 0.5)
		spot += math.Exp(-d * d / (2 * lightWidth * lightWidth))
	}
	return c, min(spot, 1)
}

// glowFrameOf lights the lines of the page's frame, its outline and the
// rules between its sections, and draws them heavier while they are lit.
// The whole page is also given a faint background; see wash. page is the
// drawn rows of the frame, width its columns, and side one side of it as it
// is drawn unlit. below is the row under the frame, which is filled too and
// returned as the last row.
func (g Glow) glowFrameOf(page []string, width int, side, below string, since time.Duration) []string {
	height := len(page)
	if width < 2 || height < 2 {
		return append(page, below)
	}
	// The way round: along the top, down the right, back along the bottom
	// and up the left.
	way := float64(2*width + 2*(height-2)*rowLength)
	rightAt := func(y int) float64 { return float64(width+(y-1)*rowLength) / way }
	leftAt := func(y int) float64 { return float64(2*width+(height-2)*rowLength+(height-2-y)*rowLength) / way }
	topAt := func(x int) float64 { return float64(x) / way }
	bottomAt := func(x int) float64 { return float64(width+(height-2)*rowLength+(width-1-x)) / way }

	back := hexLight(g.Back)
	strength := min(1, float64(since)/float64(glowFadeIn))
	breath := 0.5 + 0.5*math.Sin(2*math.Pi*float64(since%glowBreath)/float64(glowBreath))
	cell := func(text string, place float64) string {
		c, spot := g.at(place, since)
		// The line itself burns a little whiter than its colour.
		c = blend(c, hexLight(g.Light), 0.12+0.85*spot+0.14*breath)
		line := blend(back, c, 0.25+0.75*strength)
		return lipgloss.NewStyle().Bold(true).Foreground(line.color()).Render(heavy.Replace(text))
	}
	rule := func(runes []rune, place func(x int) float64) string {
		var b strings.Builder
		for x, r := range runes {
			b.WriteString(cell(string(r), place(x)))
		}
		return b.String()
	}

	out := make([]string, height)
	for y, row := range page {
		plain := []rune(ansi.Strip(row))
		switch {
		case y == 0:
			out[y] = rule(plain, topAt)
		case y == height-1:
			out[y] = rule(plain, bottomAt)
		case len(plain) > 0 && plain[0] == '├':
			// A rule between two sections runs from the light on one side
			// to the light on the other, the shorter way round, so that no
			// seam shows where they join.
			from, to := leftAt(y), rightAt(y)
			span := to - from
			span -= math.Round(span)
			along := func(x int) float64 {
				at := from + span*float64(x)/float64(max(1, width-1))
				return at - math.Floor(at)
			}
			out[y] = rule(plain, along)
		case strings.HasPrefix(row, side) && strings.HasSuffix(row, side) && len(row) >= 2*len(side):
			out[y] = cell("│", leftAt(y)) + row[len(side):len(row)-len(side)] + cell("│", rightAt(y))
		default:
			out[y] = row
		}
	}
	// The row below is filled out to the width, or the light would stop
	// where its text does.
	out = append(out, below+strings.Repeat(" ", max(0, width-ansi.StringWidth(below))))
	g.wash(out, width, topAt, bottomAt, since)
	return out
}
