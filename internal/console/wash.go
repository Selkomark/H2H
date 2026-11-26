package console

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// While the frame glows, the whole page is given a faint background of the
// same light: each cell takes the colours of the top and bottom of the
// frame above and below it, spots of light included. It covers the page
// and not a part of it, because a cell can only be coloured whole, and the
// lines run through the middle of theirs: an area bounded by one would stop
// short of it or spill past it.

const (
	// washStrength is how much of the light shows over the terminal's
	// background, and washSpot how much more where a spot of light is.
	washStrength = 0.045
	washSpot     = 0.045
	// washStep is how far apart two shades must be to be told apart.
	// Neighbours closer than that are drawn as one, which keeps a frame small.
	washStep = 3
)

// wash fills the rows of the page. top and bottom give the place on the way
// round the frame of each column of its first and last lines.
func (g Glow) wash(rows []string, width int, top, bottom func(x int) float64, since time.Duration) {
	back, light := hexLight(g.Back), hexLight(g.Light)
	strength := min(1, float64(since)/float64(glowFadeIn))
	// The light on a column is the same on every row but for the blend, so
	// it is worked out once.
	above, below := make([]lightColor, width), make([]lightColor, width)
	spotAbove, spotBelow := make([]float64, width), make([]float64, width)
	for x := range width {
		above[x], spotAbove[x] = g.at(top(x), since)
		below[x], spotBelow[x] = g.at(bottom(x), since)
	}
	step := func(v float64) int { return min(255, int(v/washStep+.5)*washStep) }
	for y := range rows {
		down := float64(y) / float64(max(1, len(rows)-1))
		rows[y] = underlay(rows[y], func(x int) string {
			x = min(x, width-1)
			spot := spotAbove[x] + (spotBelow[x]-spotAbove[x])*down
			c := blend(blend(above[x], below[x], down), light, 0.5*spot)
			mixed := blend(back, c, strength*(washStrength+washSpot*spot))
			return fmt.Sprintf("%d;%d;%d", step(mixed.r), step(mixed.g), step(mixed.b))
		})
	}
}

// styleSeqRE matches the sequence that sets how text looks.
var styleSeqRE = regexp.MustCompile("^\x1b\\[([0-9;:]*)m")

// otherSeqRE matches any other control sequence, which takes no column.
var otherSeqRE = regexp.MustCompile("^\x1b(\\[[0-9;:?<>=]*[ -/]*[@-~]|\\][^\x07\x1b]*(\x07|\x1b\\\\))")

// hasBackground follows one text-style sequence: whether, after it, text
// has a background of its own. reversed counts, since it fills the cell.
func hasBackground(params string, filled, reversed bool) (bool, bool) {
	fields := strings.Split(params, ";")
	for i := 0; i < len(fields); i++ {
		n, _ := strconv.Atoi(fields[i])
		switch {
		case n == 0:
			filled, reversed = false, false
		case n == 7:
			reversed = true
		case n == 27:
			reversed = false
		case n == 49:
			filled = false
		case n >= 40 && n <= 47, n >= 100 && n <= 107:
			filled = true
		case n == 38 || n == 48:
			filled = filled || n == 48
			// The numbers that name the colour are not codes.
			if i+1 < len(fields) && fields[i+1] == "2" {
				i += 4
			} else {
				i += 2
			}
		}
	}
	return filled, reversed
}

// underlay gives the cells of a drawn row the background that under
// returns for their column, as "R;G;B". A cell with a background of its
// own, such as the cursor, keeps it, and the text is not changed.
func underlay(row string, under func(x int) string) string {
	var b strings.Builder
	filled, reversed := false, false
	current := "" // the background this has switched on
	off := func() {
		if current != "" {
			b.WriteString("\x1b[49m")
			current = ""
		}
	}
	x := 0
	for i := 0; i < len(row); {
		if row[i] == 0x1b {
			if m := styleSeqRE.FindStringSubmatch(row[i:]); m != nil {
				off()
				filled, reversed = hasBackground(m[1], filled, reversed)
				b.WriteString(m[0])
				i += len(m[0])
				continue
			}
			if m := otherSeqRE.FindString(row[i:]); m != "" {
				b.WriteString(m)
				i += len(m)
				continue
			}
		}
		var r rune
		size := 1
		for _, first := range row[i:] {
			r, size = first, len(string(first))
			break
		}
		if filled || reversed {
			off()
		} else if want := under(x); want != current {
			fmt.Fprintf(&b, "\x1b[48;2;%sm", want)
			current = want
		}
		b.WriteString(row[i : i+size])
		x += ansi.StringWidth(string(r))
		i += size
	}
	off()
	return b.String()
}
