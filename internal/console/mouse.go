package console

import (
	"fmt"
	"io"
	"strings"
	"time"

	"github.com/atotto/clipboard"
	"github.com/aymanbagabas/go-osc52/v2"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Where the parts of the page are on screen, counted from the top-left
// corner: the frame and its padding come first on every row.
const (
	contentX   = 2 // the first column inside the frame
	tabRowY    = 2 // the row of tabs, under the top border and the title
	middleY    = 4 // the first row of the middle section
	wheelLines = 3 // rows the transcript moves for one notch of the wheel
	hintSep    = " · "

	// Half blocks: a cell whose lower or upper half is filled.
	lowerHalf, upperHalf = "▄", "▀"
)

// cell is a position in the transcript: a line of its wrapped text and a
// column in that line.
type cell struct{ line, col int }

func (c cell) before(o cell) bool { return c.line < o.line || (c.line == o.line && c.col < o.col) }

// selection is the text the user has dragged over in the transcript.
type selection struct {
	anchor, head cell
	dragging     bool
}

// span returns the selection's first and last cell, in reading order.
func (s *selection) span() (first, last cell) {
	if s.head.before(s.anchor) {
		return s.head, s.anchor
	}
	return s.anchor, s.head
}

// columns returns the part of a line the selection covers, as the first
// column and the one after the last. ok is false when it covers none.
func (s *selection) columns(line, width int) (from, to int, ok bool) {
	first, last := s.span()
	if line < first.line || line > last.line {
		return 0, 0, false
	}
	from, to = 0, width
	if line == first.line {
		from = first.col
	}
	if line == last.line {
		to = min(width, last.col+1)
	}
	return from, to, from < to
}

// systemClipboard copies text to the clipboard. Where the system has no
// clipboard program, as over SSH, it asks the terminal to do it instead.
func systemClipboard(out io.Writer) func(string) error {
	return func(text string) error {
		if err := clipboard.WriteAll(text); err == nil {
			return nil
		}
		_, err := osc52.New(text).WriteTo(out)
		return err
	}
}

// The console can lose the mouse without being told. The terminal stops
// reporting it when the mode is switched off, which Bubble Tea does when it
// hands the terminal to an attached harness or an editor and does not undo
// when it takes the terminal back, and which a terminal may do by itself
// when the machine sleeps or its window is restored. So the console asks
// for the mouse again at every moment it may have been lost. Asking when it
// already has the mouse changes nothing.
//
// None of this reaches the terminal while a harness or an editor has it:
// Bubble Tea runs those inside its own loop, which handles no message
// until they return.

type (
	// mouseTickMsg is the timer's turn to ask for the mouse.
	mouseTickMsg struct{}
	// foregroundDoneMsg says the console has the terminal back.
	foregroundDoneMsg struct{}
)

// foregroundDone is what runs when a harness or an editor gives the
// terminal back: it answers the command that asked for the terminal, and
// tells the console to take the mouse again.
func foregroundDone(reply chan<- error) tea.ExecCallback {
	return func(err error) tea.Msg {
		reply <- err
		return foregroundDoneMsg{}
	}
}

// claimMouse asks the terminal to report the mouse to the console.
func claimMouse() tea.Msg { return tea.EnableMouseAllMotion() }

// pollMouse asks for the mouse again after a while.
func (m *model) pollMouse() tea.Cmd {
	return tea.Tick(mouseInterval, func(time.Time) tea.Msg { return mouseTickMsg{} })
}

// dropMouse forgets what the mouse was in the middle of: a drag whose end
// was never reported must not go on following the pointer.
func (m *model) dropMouse() {
	if m.sel != nil {
		m.sel.dragging = false
	}
	m.hoverRow = -1
}

// mouse handles the wheel, clicks and drags. Everything the mouse does
// can also be done from the keyboard.
func (m *model) mouse(e tea.MouseMsg) (tea.Model, tea.Cmd) {
	switch e.Button {
	case tea.MouseButtonWheelUp:
		m.wheel(-1)
		return m, nil
	case tea.MouseButtonWheelDown:
		m.wheel(1)
		return m, nil
	case tea.MouseButtonLeft:
	default:
		if e.Action != tea.MouseActionMotion && e.Action != tea.MouseActionRelease {
			return m, nil
		}
	}

	switch e.Action {
	case tea.MouseActionPress:
		return m, m.press(e.X, e.Y)
	case tea.MouseActionMotion:
		if m.sel != nil && m.sel.dragging {
			if e.Button != tea.MouseButtonLeft {
				// The pointer moves with no button held, so the release
				// was lost. The selection ends where it was.
				m.sel.dragging = false
			} else {
				m.sel.head = m.cellAt(e.X, e.Y)
			}
		}
		m.hoverRow = -1
		if row := e.Y - middleY; row >= 0 && row < m.view.Height && e.X >= contentX && e.X < contentX+m.view.Width {
			m.hoverRow = row
		}
	case tea.MouseActionRelease:
		if m.sel != nil && m.sel.dragging {
			m.sel.dragging = false
			m.sel.head = m.cellAt(e.X, e.Y)
			m.copySelection()
		}
	}
	return m, nil
}

// wheel scrolls whatever is in the middle of the page.
func (m *model) wheel(step int) {
	switch {
	case m.dialog == nil && m.chat != nil:
		m.chat.view.SetYOffset(m.chat.view.YOffset + step*wheelLines)
	case m.dialog != nil && len(m.dialog.options) > 0:
		m.dialog.cursor = min(max(0, m.dialog.cursor+step), len(m.dialog.options)-1)
	case m.dialog != nil && m.tab != 0: // a view's rows stay put under its question
	case m.tab == 0:
		m.view.SetYOffset(m.view.YOffset + step*wheelLines)
	case m.views[m.tab-1].Graph != nil: // a drawing has no rows to step through
	default:
		m.moveRow(step, false)
	}
}

// press handles the left button going down at a point on screen.
func (m *model) press(x, y int) tea.Cmd {
	m.sel, m.flash = nil, ""
	inner := max(1, m.width-chromeWidth)
	if d := m.dialog; d != nil {
		left, top := m.dialogOrigin(inner)
		d.click(x-contentX-left, y-middleY-top, inner)
		cmd := m.settleDialog()
		m.layout() // a box that opened more of its text takes more rows
		return cmd
	}
	if m.chat != nil {
		return nil // the conversation takes the keyboard and the wheel only
	}

	shown := m.shown()
	switch {
	case y == tabRowY:
		if n, ok := m.tabAt(x - contentX); ok {
			m.tab = n
			m.apply(m.snapshot())
		}
	case y == m.height-1 && m.tab > 0 && m.input.Value() == "":
		// The hint under a view names its actions, and each is a button.
		n, ok := partAt(m.viewHint(), x-1)
		if _, isGraph := m.graph(); isGraph {
			if ok && n == 1 { // "enter inspect"
				m.inspect()
			}
			return nil
		}
		if ok && n >= 1 && n <= len(m.o.Views[m.tab-1].Actions) {
			return m.runAction(m.o.Views[m.tab-1].Actions[n-1])
		}
	case y < middleY || y >= middleY+m.middleHeight():
	case y >= middleY+m.middleHeight()-len(shown):
		// A suggestion: take it, as Tab does.
		m.selected = y - (middleY + m.middleHeight() - len(shown)) + m.suggestionOffset()
		m.setLine(accept(m.input.Value(), m.offers[m.selected], m.forArgument))
	case m.onJumpLabel(x, y):
		m.jump()
	case m.tab == 0:
		at := m.cellAt(x, y)
		m.sel = &selection{anchor: at, head: at, dragging: true}
	case m.views[m.tab-1].Graph != nil:
		// A box of the graph: a click selects it, and a click on the
		// selected one inspects it.
		if d, ok := m.graph(); ok {
			if id, hit := d.nodeAt(x-contentX-graphLeft, y-middleY-graphTop); hit {
				if id == m.graphSelection(d) && m.selectedID[m.tab-1] == id {
					m.inspect()
				}
				m.selectedID[m.tab-1] = id
			}
		}
	default:
		// A row of the view, under its header.
		if rows := m.views[m.tab-1].Rows; y-middleY-1 >= 0 && y-middleY-1 < len(rows) {
			m.selectedID[m.tab-1] = rows[y-middleY-1].ID
			m.notice = ""
		}
	}
	return nil
}

// cellAt returns the transcript position under a point on screen, keeping
// to the transcript when the point is outside it.
func (m *model) cellAt(x, y int) cell {
	row := min(max(0, y-middleY), max(0, m.view.Height-1))
	return cell{line: m.view.YOffset + row, col: min(max(0, x-contentX), max(0, m.view.Width-1))}
}

// tabAt returns the tab drawn at a column of the tab row.
func (m *model) tabAt(x int) (int, bool) {
	at := 0
	for n, name := range m.tabNames() {
		if w := lipgloss.Width(name) + 2*tabPad; x >= at && x < at+w {
			return n, true
		} else {
			at += w + lipgloss.Width(tabSep)
		}
	}
	return 0, false
}

// partAt returns which part of a hint line, split at its separators, is at
// a column.
func partAt(hint string, x int) (int, bool) {
	at := 0
	for n, part := range strings.Split(hint, hintSep) {
		if w := lipgloss.Width(part); x >= at && x < at+w {
			return n, true
		} else {
			at += w + lipgloss.Width(hintSep)
		}
	}
	return 0, false
}

// copySelection puts the selected text on the clipboard and says so. A
// click that selected nothing is not a selection.
func (m *model) copySelection() {
	if m.sel.anchor == m.sel.head {
		m.sel = nil
		return
	}
	text := m.selectedText()
	if strings.TrimSpace(text) == "" {
		m.sel = nil
		return
	}
	if err := m.copy(text); err != nil {
		m.flash = "could not copy: " + err.Error()
		return
	}
	m.flash = fmt.Sprintf("copied %d characters to the clipboard", len([]rune(text)))
}

// selectedText returns the selected part of the transcript as plain text.
func (m *model) selectedText() string {
	first, last := m.sel.span()
	var out []string
	for n := first.line; n <= last.line && n < len(m.lines); n++ {
		from, to, ok := m.sel.columns(n, m.view.Width)
		if !ok {
			out = append(out, "")
			continue
		}
		out = append(out, strings.TrimRight(ansi.Strip(ansi.Cut(m.lines[n], from, to)), " "))
	}
	return strings.Join(out, "\n")
}

// hovered returns the lines of the message under the mouse, as the first
// line and the one after the last. The blank lines that end a message are
// not part of it, so the gap between two messages belongs to neither.
func (m *model) hovered() (from, to int, ok bool) {
	line := m.view.YOffset + m.hoverRow
	if m.hoverRow < 0 || m.tab != 0 || m.dialog != nil || m.chat != nil || line >= len(m.lines) {
		return 0, 0, false
	}
	from, to = 0, len(m.lines)
	for _, start := range m.sections {
		if start > line {
			to = start
			break
		}
		from = start
	}
	for to > from && strings.TrimSpace(ansi.Strip(m.lines[to-1])) == "" {
		to--
	}
	return from, to, line < to
}

// tint gives a line a background colour from end to end. Styled text
// inside the line switches all colour off where it ends, so the
// background is switched back on after each of those.
func tint(line string, c lipgloss.TerminalColor) string {
	const mark, reset = "\x00", "\x1b[0m"
	on, _, found := strings.Cut(lipgloss.NewStyle().Background(c).Render(mark), mark)
	if !found || on == "" {
		return line // a terminal without colour
	}
	return on + strings.ReplaceAll(line, reset, reset+on) + reset
}

// transcriptView draws the transcript with its padding, the selection
// shown in reverse and the message under the mouse on a faint background.
// A transcript longer than its rows gets a scrollbar in the right padding
// column and, away from its end, a label that jumps back there.
func (m *model) transcriptView() string {
	lines := strings.Split(m.view.View(), "\n")
	mark := lipgloss.NewStyle().Reverse(true)
	for row, l := range lines {
		if m.sel == nil {
			break
		}
		from, to, ok := m.sel.columns(m.view.YOffset+row, m.view.Width)
		if !ok {
			continue
		}
		width := lipgloss.Width(l)
		if width < to { // a short line is selected up to the column the drag reached
			l += strings.Repeat(" ", to-width)
			width = to
		}
		lines[row] = ansi.Cut(l, 0, from) + mark.Render(ansi.Strip(ansi.Cut(l, from, to))) + ansi.Cut(l, to, width)
	}
	// The background goes on last, so it also runs behind the selection.
	// It is put on the line together with the page's padding on each side,
	// so it reaches the frame instead of stopping a column short of it.
	//
	// Above and below the message the background continues for half a
	// row, drawn with half blocks on the blank lines next to it, so the
	// text does not sit against the edge of its background.
	from, to, hovering := m.hovered()
	hovering = hovering && m.o.Theme.Hover != nil
	edge := lipgloss.NewStyle().Foreground(m.o.Theme.Hover)
	bar := m.scrollbar()
	for row, l := range lines {
		n := m.view.YOffset + row
		blank := strings.TrimSpace(ansi.Strip(l)) == ""
		// The scrollbar stands in the right padding column, and so inside
		// the hover background rather than beside it.
		right, halves, beside := " ", m.view.Width+2, ""
		if row < len(bar) {
			right, halves, beside = bar[row], m.view.Width+1, bar[row]
		}
		l = " " + l + strings.Repeat(" ", max(0, m.view.Width-lipgloss.Width(l))) + right
		switch {
		case !hovering:
		case n >= from && n < to:
			l = tint(l, m.o.Theme.Hover)
		case n == from-1 && blank:
			l = edge.Render(strings.Repeat(lowerHalf, halves)) + beside
		case n == to && blank:
			l = edge.Render(strings.Repeat(upperHalf, halves)) + beside
		}
		lines[row] = l
	}
	m.overlayJumpLabel(lines)
	return strings.Join(lines, "\n")
}

// gutter puts the page's padding, one column, on each side of every line.
// The middle section draws its own padding so that a background can cover it.
func gutter(text string) string {
	lines := strings.Split(text, "\n")
	for n, l := range lines {
		lines[n] = " " + l + " "
	}
	return strings.Join(lines, "\n")
}

// centered places a box in the middle of an area of the given size, and
// returns where its top-left corner landed.
func centered(box string, width, height int) (text string, left, top int) {
	lines := strings.Split(box, "\n")
	left = max(0, (width-lipgloss.Width(box))/2)
	top = max(0, (height-len(lines))/2)
	out := make([]string, 0, top+len(lines))
	for range top {
		out = append(out, "")
	}
	for _, l := range lines {
		out = append(out, strings.Repeat(" ", left)+l)
	}
	return strings.Join(out, "\n"), left, top
}

// minAboveDialog is how many rows of the page must stay visible above a
// dialog for it to sit at the bottom instead of covering the page.
const minAboveDialog = 4

// dialogPlace says where the dialog on screen goes inside the middle
// section. It sits at the bottom, under what the page was showing, because
// that is what the question is about: the changes to apply, or the work
// that would be lost. docked is false on a screen too small for both,
// where the dialog takes the middle of the section by itself.
func (m *model) dialogPlace(inner int) (box string, left, top int, docked bool) {
	box = m.dialog.view(m.o.Theme, inner)
	rows := lipgloss.Height(box)
	if m.middleHeight()-rows < minAboveDialog {
		_, left, top = centered(box, inner, m.middleHeight())
		return box, left, top, false
	}
	return box, max(0, (inner-lipgloss.Width(box))/2), m.middleHeight() - rows, true
}

// dialogOrigin is where the dialog on screen starts inside the middle
// section.
func (m *model) dialogOrigin(inner int) (left, top int) {
	_, left, top, _ = m.dialogPlace(inner)
	return left, top
}
