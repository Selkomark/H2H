package console

import (
	"fmt"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	// The scrollbar in the transcript's right padding column. Its track is
	// left blank: a line there would double the frame beside it.
	scrollThumb, scrollTrack = "┃", " "

	jumpLatest = " ↓ latest · end "
	jumpNew    = " ↓ %d new %s · end "
)

// scrollable says whether the transcript is on screen by itself and longer
// than the rows it has, which is when the scroll position is worth showing.
func (m *model) scrollable() bool {
	return m.tab == 0 && m.dialog == nil && m.chat == nil && len(m.lines) > m.view.Height
}

// thumb returns the rows of the transcript the scrollbar's thumb covers, as
// the first row and the one after the last. Its size is the share of the
// transcript on screen, and its place the share that lies above.
func (m *model) thumb() (from, to int) {
	rows, total := m.view.Height, len(m.lines)
	size := max(1, rows*rows/total)
	last := total - rows // the offset at the bottom
	at := min(max(0, m.view.YOffset), last)
	// Rounded down, so the thumb reaches the last row only at the bottom.
	// It leaves the first row as soon as the view leaves the top, where
	// there is a row to move to: the ends are what the user reads it for.
	from = (rows - size) * at / last
	if at > 0 && from == 0 && rows-size > 1 {
		from = 1
	}
	return from, from + size
}

// scrollbar returns the cell drawn right of each row of the transcript, or
// nil when the whole transcript fits and there is no position to show.
func (m *model) scrollbar() []string {
	if !m.scrollable() {
		return nil
	}
	from, to := m.thumb()
	cells := make([]string, m.view.Height)
	for row := range cells {
		cells[row] = m.o.Theme.Dim.Render(scrollTrack)
		if row >= from && row < to {
			cells[row] = m.o.Theme.Accent.Render(scrollThumb)
		}
	}
	return cells
}

// jumpLabel returns the label that offers to go back to the latest output,
// and the cells of the screen it takes: its row and its first column. ok is
// false when it is not shown. The drawing and the click both ask here, so
// the button is always where it looks to be.
func (m *model) jumpLabel() (text string, x, y int, ok bool) {
	// Suggestions shorten the transcript from below, which hides its end
	// without the user having scrolled away from it.
	if !m.scrollable() || m.view.AtBottom() || len(m.shown()) > 0 {
		return "", 0, 0, false
	}
	text = jumpLatest
	if m.unseen == 1 {
		text = fmt.Sprintf(jumpNew, 1, "line")
	} else if m.unseen > 1 {
		text = fmt.Sprintf(jumpNew, m.unseen, "lines")
	}
	width := lipgloss.Width(text)
	if width > m.view.Width {
		return "", 0, 0, false
	}
	// Right-aligned in the text columns, which leaves the scrollbar free.
	return text, contentX + m.view.Width - width, middleY + m.view.Height - 1, true
}

// onJumpLabel says whether a point on screen is on the jump label.
func (m *model) onJumpLabel(x, y int) bool {
	text, left, row, ok := m.jumpLabel()
	return ok && y == row && x >= left && x < left+lipgloss.Width(text)
}

// overlayJumpLabel draws the jump label over the end of the transcript's
// last row. rows are the drawn rows, padding included, so the label goes on
// after the hover background and covers it.
func (m *model) overlayJumpLabel(rows []string) {
	text, x, y, ok := m.jumpLabel()
	if !ok || y-middleY >= len(rows) {
		return
	}
	// A drawn row starts one column left of the text, at the frame's padding.
	from := x - contentX + 1
	to := from + lipgloss.Width(text)
	l := rows[y-middleY]
	rows[y-middleY] = ansi.Cut(l, 0, from) + m.o.Theme.Accent.Reverse(true).Render(text) + ansi.Cut(l, to, lipgloss.Width(l))
}

// jump shows the latest output again.
func (m *model) jump() {
	m.view.GotoBottom()
	m.unseen = 0
}

// settle forgets the count of unseen lines once the view is back at the
// bottom, however it got there: the wheel, a key, a resize or a jump.
func (m *model) settle() {
	if m.view.AtBottom() {
		m.unseen = 0
	}
}
