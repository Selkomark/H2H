package console

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

// longModel is a test model whose transcript is far longer than the screen.
func longModel(t *testing.T) *model {
	t.Helper()
	m := newTestModel(func(string) bool { return false })
	for n := range 100 {
		m.print(fmt.Sprintf("line %d\n", n))
	}
	if !m.view.AtBottom() {
		t.Fatal("a long transcript does not start at its end")
	}
	return m
}

// thumbRows returns the rows of the transcript that show the scrollbar's
// thumb, read off the screen in the column the scrollbar is drawn in.
func thumbRows(m *model) []int {
	var rows []int
	screen := strings.Split(m.View(), "\n")
	for row := range m.view.Height {
		if cells := []rune(screen[middleY+row]); string(cells[contentX+m.view.Width]) == scrollThumb {
			rows = append(rows, row)
		}
	}
	return rows
}

// checkPage fails unless the page is exactly the size of the test screen.
func checkPage(t *testing.T, m *model) {
	t.Helper()
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 24 {
		t.Errorf("the page is %d rows on a 24-row screen", len(lines))
	}
	for y, l := range lines[:len(lines)-1] { // the hint under the page is as long as its text
		if w := lipgloss.Width(l); w != 80 {
			t.Errorf("row %d is %d columns on an 80-column screen: %q", y, w, l)
		}
	}
}

func TestNoScrollIndicatorWhenTheTranscriptFits(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	m.print("one\ntwo\nthree\n")
	if v := m.View(); strings.Contains(v, scrollThumb) || strings.Contains(v, " ↓ ") {
		t.Errorf("a transcript that fits shows a scroll indicator:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if _, _, _, ok := m.jumpLabel(); ok || m.scrollbar() != nil {
		t.Error("scrolling a transcript that fits brought up an indicator")
	}
	checkPage(t, m)
}

func TestScrollbarThumbFollowsTheView(t *testing.T) {
	m := longModel(t)
	rows := m.view.Height
	bottom := thumbRows(m)
	// 16 rows of 102 lines: the thumb is two rows, at the end.
	if len(bottom) != max(1, rows*rows/len(m.lines)) || bottom[len(bottom)-1] != rows-1 {
		t.Fatalf("at the bottom the thumb is on rows %v of %d", bottom, rows)
	}
	checkPage(t, m)

	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	middle := thumbRows(m)
	if len(middle) != len(bottom) || middle[0] >= bottom[0] || middle[0] == 0 {
		t.Errorf("scrolled up part of the way the thumb is on rows %v, was %v", middle, bottom)
	}

	for range 50 {
		wheel(m, tea.MouseButtonWheelUp)
	}
	if top := thumbRows(m); len(top) != len(bottom) || top[0] != 0 {
		t.Errorf("at the top the thumb is on rows %v", top)
	}
	wheel(m, tea.MouseButtonWheelDown)
	if near := thumbRows(m); len(near) == 0 || near[0] == 0 {
		t.Errorf("just off the top the thumb is still on rows %v", near)
	}
	checkPage(t, m)
}

func TestJumpLabelShowsAwayFromTheBottom(t *testing.T) {
	m := longModel(t)
	if strings.Contains(m.View(), " ↓ ") {
		t.Fatalf("at the bottom the page offers to jump there:\n%s", m.View())
	}
	wheel(m, tea.MouseButtonWheelUp)
	x, y := find(t, m, jumpLatest)
	if want := middleY + m.view.Height - 1; y != want {
		t.Errorf("the label is on row %d, want the transcript's last row %d", y, want)
	}
	if want := contentX + m.view.Width - lipgloss.Width(jumpLatest); x != want {
		t.Errorf("the label starts at column %d, want %d: right-aligned, beside the scrollbar", x, want)
	}
	checkPage(t, m)

	// Scrolling back down, by wheel or by key, takes it away again.
	wheel(m, tea.MouseButtonWheelDown)
	if strings.Contains(m.View(), " ↓ ") {
		t.Error("the label is still there after the wheel reached the bottom")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	find(t, m, jumpLatest)
	m.Update(tea.KeyMsg{Type: tea.KeyShiftDown})
	if strings.Contains(m.View(), " ↓ ") {
		t.Error("the label is still there after shift+down reached the bottom")
	}
}

func TestJumpLabelCountsNewLines(t *testing.T) {
	m := longModel(t)
	wheel(m, tea.MouseButtonWheelUp)
	at := m.view.YOffset

	m.Update(outputMsg{text: "new 1\n"})
	find(t, m, " ↓ 1 new line · end ")
	m.Update(outputMsg{text: "new 2\nnew 3\n"})
	find(t, m, " ↓ 3 new lines · end ")
	if m.view.YOffset != at {
		t.Errorf("output moved a view that was scrolled away: offset %d, was %d", m.view.YOffset, at)
	}
	checkPage(t, m)

	// Back at the bottom the count is spent, and stays so.
	for range 5 {
		wheel(m, tea.MouseButtonWheelDown)
	}
	if !m.view.AtBottom() || m.unseen != 0 {
		t.Fatalf("at the bottom again: at bottom %v, %d unseen", m.view.AtBottom(), m.unseen)
	}
	m.Update(outputMsg{text: "new 4\n"})
	if m.unseen != 0 || !m.view.AtBottom() {
		t.Errorf("output at the bottom: %d unseen, at bottom %v", m.unseen, m.view.AtBottom())
	}
	wheel(m, tea.MouseButtonWheelUp)
	find(t, m, jumpLatest)
}

func TestClickOnTheJumpLabelJumps(t *testing.T) {
	m := longModel(t)
	wheel(m, tea.MouseButtonWheelUp)
	m.Update(outputMsg{text: "more\n"})
	x, y := find(t, m, " ↓ ")

	// The cell left of the label is transcript, as before.
	click(m, x-2, y)
	if m.view.AtBottom() {
		t.Fatal("a click beside the label jumped")
	}
	// Its last cell is still the button.
	text, left, _, _ := m.jumpLabel()
	click(m, left+lipgloss.Width(text)-1, y)
	if !m.view.AtBottom() || m.unseen != 0 || m.sel != nil {
		t.Errorf("clicking the label: at bottom %v, %d unseen, selection %v", m.view.AtBottom(), m.unseen, m.sel)
	}
	if !strings.Contains(m.View(), "more") || strings.Contains(m.View(), " ↓ ") {
		t.Errorf("after the jump the page does not show the latest output alone:\n%s", m.View())
	}
}

func TestEndJumpsToTheLatestOutput(t *testing.T) {
	m := longModel(t)
	wheel(m, tea.MouseButtonWheelUp)
	m.Update(outputMsg{text: "more\n"})

	typeText(m, "hello")
	m.Update(tea.KeyMsg{Type: tea.KeyEsc}) // no suggestions in the way
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if m.view.AtBottom() {
		t.Fatal("End jumped while there was text in the input")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlEnd})
	if !m.view.AtBottom() || m.unseen != 0 {
		t.Errorf("ctrl+end with text in the input: at bottom %v, %d unseen", m.view.AtBottom(), m.unseen)
	}
	if m.input.Value() != "hello" {
		t.Errorf("the jump changed the input to %q", m.input.Value())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}) // clears the line
	wheel(m, tea.MouseButtonWheelUp)
	m.Update(outputMsg{text: "and more\n"})
	m.Update(tea.KeyMsg{Type: tea.KeyEnd})
	if !m.view.AtBottom() || m.unseen != 0 {
		t.Errorf("End with an empty input: at bottom %v, %d unseen", m.view.AtBottom(), m.unseen)
	}
	checkPage(t, m)
}

func TestScrollIndicatorsKeepToTheTranscript(t *testing.T) {
	m := longModel(t)
	wheel(m, tea.MouseButtonWheelUp)

	// On a view there is no transcript to place them on.
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if v := m.View(); strings.Contains(v, " ↓ ") || strings.Contains(v, scrollThumb) {
		t.Errorf("a view shows the transcript's scroll indicators:\n%s", v)
	}
	y := middleY + m.view.Height - 1
	click(m, contentX+m.view.Width-2, y)
	if m.view.AtBottom() {
		t.Error("a click on a view jumped the transcript")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})

	// Nor under a question.
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	m.dialog = newConfirm("Apply?")
	m.layout()
	if v := m.View(); strings.Contains(v, " ↓ ") || strings.Contains(v, scrollThumb) {
		t.Errorf("the indicators show under a dialog:\n%s", v)
	}
	checkPage(t, m)
}

func TestScrollIndicatorsLeaveHoverAndSelectionAlone(t *testing.T) {
	m := longModel(t)
	var copied string
	m.copy = func(text string) error { copied = text; return nil }
	wheel(m, tea.MouseButtonWheelUp)

	x, y := find(t, m, "line 90")
	hover(m, x, y)
	if _, _, ok := m.hovered(); !ok {
		t.Fatal("no message is hovered under the mouse")
	}
	checkPage(t, m)

	// With colour on, the background runs behind the scrollbar and the
	// label is drawn over it, and neither makes a row longer.
	r := lipgloss.DefaultRenderer()
	before := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	m.o.Theme.Hover = lipgloss.Color("236")
	checkPage(t, m)
	plain := ansi.Strip(m.View())
	if !strings.Contains(plain, jumpLatest) || !strings.Contains(plain, "line 90") {
		t.Errorf("the hover background hides the label or the text:\n%s", plain)
	}
	if !strings.Contains(m.View(), "48;5;236") {
		t.Error("the hovered message has no background")
	}
	r.SetColorProfile(before)
	m.o.Theme.Hover = nil

	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: x + 3, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if copied != "line" {
		t.Errorf("dragging copied %q, want %q", copied, "line")
	}
	if m.view.AtBottom() {
		t.Error("dragging over the transcript jumped to its end")
	}
	checkPage(t, m)
}

func TestTheLatestOutputStaysInViewWhenTheInputGrows(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	for n := range 60 {
		m.print(fmt.Sprintf("line %d\n", n))
	}
	// Typing several lines takes rows from the transcript.
	typeText(m, "first")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	typeText(m, "second")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	typeText(m, "third")
	if v := m.View(); !m.view.AtBottom() || !strings.Contains(v, "line 59") || strings.Contains(v, "latest") {
		t.Errorf("the view fell behind while the input grew, or offers to jump:\n%s", v)
	}
	// Output that arrives meanwhile is still followed.
	m.print("line 60\n")
	if !strings.Contains(m.View(), "line 60") || m.unseen != 0 {
		t.Errorf("new output is not followed while typing: unseen %d", m.unseen)
	}
	// A user who scrolled up is left where they are.
	wheel(m, tea.MouseButtonWheelUp)
	at := m.view.YOffset
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	typeText(m, "fourth")
	if m.view.AtBottom() || m.view.YOffset != at {
		t.Errorf("a scrolled view moved from %d to %d when the input grew", at, m.view.YOffset)
	}
}
