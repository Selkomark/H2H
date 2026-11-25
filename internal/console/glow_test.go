package console

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

var testGlow = &Glow{Colors: []string{"#FF3D9A", "#A855F7", "#4C7DFF", "#22D3EE", "#FF8A5B"}, Light: "#FFFFFF", Back: "#1C1C1C"}

// While a command runs the page's frame is lit and moves, no row
// is spent saying "working", and nothing changes size.
func TestTheFrameGlowsWhileBusy(t *testing.T) {
	old := lipgloss.ColorProfile()
	lipgloss.SetColorProfile(termenv.TrueColor)
	defer lipgloss.SetColorProfile(old)

	m := newTestModel(func(string) bool { return false })
	m.o.Theme.Glow = testGlow
	idle := m.View()
	m.busy, m.glowFor = true, time.Second
	first := m.View()
	m.glowFor += 500 * time.Millisecond
	second := m.View()

	if first == idle || first == second {
		t.Error("the frame should light up and move while busy")
	}
	if strings.Contains(first, "working") {
		t.Errorf("the glow replaces the word:\n%s", first)
	}
	a, b := strings.Split(idle, "\n"), strings.Split(first, "\n")
	if len(a) != len(b) {
		t.Fatalf("%d rows busy, %d idle", len(b), len(a))
	}
	for n := range a[:len(a)-1] { // the hint under the page may differ
		if lipgloss.Width(a[n]) != lipgloss.Width(b[n]) {
			t.Errorf("row %d is %d wide busy and %d idle", n, lipgloss.Width(b[n]), lipgloss.Width(a[n]))
		}
	}
	// Every row of the frame is lit.
	lit, later := strings.Split(first, "\n"), strings.Split(second, "\n")
	for n := range lit[:len(lit)-1] {
		if lit[n] == later[n] {
			t.Errorf("row %d of the frame does not move: %q", n, ansi.Strip(lit[n]))
		}
	}
	// The whole page is filled, lines and the hint under it included, so
	// no part of it has an edge to fall short of or spill past.
	for n, row := range lit {
		if !strings.HasPrefix(row, "\x1b[") || !strings.Contains(row, "48;2;") {
			t.Errorf("row %d is not filled: %q", n, row)
		}
		if w := lipgloss.Width(row); w != lipgloss.Width(lit[0]) {
			t.Errorf("row %d is %d wide, want the width of the page", n, w)
		}
	}
	if !strings.Contains(lit[0], "┏") || !strings.Contains(lit[len(lit)-2], "┗") {
		t.Error("the frame should be drawn with heavy lines")
	}
	for n := range a[:len(a)-1] {
		framed := func(r rune) bool {
			return strings.ContainsRune(" │├┤─╭╮╰╯┃┣┫━┏┓┗┛", r)
		}
		was, is := strings.TrimFunc(ansi.Strip(a[n]), framed), strings.TrimFunc(ansi.Strip(b[n]), framed)
		if was != is && !strings.Contains(was, promptText) {
			t.Errorf("row %d changed its text: %q, was %q", n, is, was)
		}
	}
	m.alert = "codex-1 is waiting for you"
	if !strings.Contains(ansi.Strip(m.View()), m.alert) {
		t.Error("a harness that waits must still be told of")
	}
	m.busy = false
	if m.glowing() {
		t.Error("the glow should end with the work")
	}
}

// The cursor, and any other background, keeps its own under the fill, and
// no text changes.
func TestTheFillLeavesTextAndCursor(t *testing.T) {
	row := " > hi \x1b[7mC\x1b[0m there "
	rows := []string{row}
	at := func(x int) float64 { return float64(x) / 100 }
	testGlow.wash(rows, ansi.StringWidth(row), at, at, time.Second)
	if ansi.Strip(rows[0]) != ansi.Strip(row) {
		t.Errorf("text = %q, want %q", ansi.Strip(rows[0]), ansi.Strip(row))
	}
	if !strings.HasPrefix(rows[0], "\x1b[48;2;") || !strings.Contains(rows[0], "\x1b[7mC\x1b[0m") {
		t.Errorf("want the row filled from its first cell and the cursor left alone: %q", rows[0])
	}
}
