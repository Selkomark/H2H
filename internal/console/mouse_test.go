package console

import (
	"fmt"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
)

func click(m *model, x, y int) tea.Cmd {
	_, cmd := m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	return cmd
}

func wheel(m *model, b tea.MouseButton) {
	m.Update(tea.MouseMsg{X: 10, Y: 10, Button: b, Action: tea.MouseActionPress})
}

// find returns the screen position of the first occurrence of text.
func find(t *testing.T, m *model, text string) (x, y int) {
	t.Helper()
	for y, line := range strings.Split(m.View(), "\n") {
		if i := strings.Index(line, text); i >= 0 {
			return len([]rune(line[:i])), y
		}
	}
	t.Fatalf("%q is not on screen:\n%s", text, m.View())
	return 0, 0
}

func TestWheelScrollsTheTranscript(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	for n := range 100 {
		m.print(fmt.Sprintf("line %d\n", n))
	}
	if !m.view.AtBottom() || strings.Contains(m.View(), "line 80\n") {
		t.Fatal("a long transcript does not start at its end")
	}
	bottom := m.view.YOffset
	wheel(m, tea.MouseButtonWheelUp)
	if m.view.YOffset != bottom-wheelLines {
		t.Errorf("wheel up: offset %d, want %d", m.view.YOffset, bottom-wheelLines)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftUp})
	if m.view.YOffset != bottom-wheelLines-1 {
		t.Errorf("shift+up: offset %d, want one line higher", m.view.YOffset)
	}
	for range 50 {
		wheel(m, tea.MouseButtonWheelDown)
	}
	if !m.view.AtBottom() {
		t.Error("wheel down did not stop at the end")
	}
	for range 50 {
		wheel(m, tea.MouseButtonWheelUp)
	}
	if m.view.YOffset != 0 || !strings.Contains(m.View(), "line 0") {
		t.Errorf("wheel up did not reach the start: offset %d", m.view.YOffset)
	}
}

func TestClickSwitchesTab(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	x, y := find(t, m, "Sessions")
	click(m, x+2, y)
	if m.tab != 1 {
		t.Fatalf("clicking the Sessions tab: tab = %d, want 1", m.tab)
	}
	x, y = find(t, m, transcriptTab)
	click(m, x, y)
	if m.tab != 0 {
		t.Errorf("clicking the Console tab: tab = %d, want 0", m.tab)
	}
	click(m, x+len(transcriptTab)+1, y) // the gap between the tabs
	if m.tab != 0 {
		t.Errorf("clicking between tabs changed the tab to %d", m.tab)
	}
}

func TestClickAnswersConfirm(t *testing.T) {
	for _, tt := range []struct {
		button string
		want   bool
	}{{"Yes", true}, {"No", false}} {
		m := newTestModel(func(string) bool { return false })
		reply := make(chan bool, 1)
		m.Update(confirmMsg{question: "Delete claude-1 and its workspace?", reply: reply})
		x, y := find(t, m, tt.button)
		click(m, x, y)
		select {
		case got := <-reply:
			if got != tt.want {
				t.Errorf("clicking %s answered %v", tt.button, got)
			}
		default:
			t.Errorf("clicking %s at %d,%d did not answer:\n%s", tt.button, x, y, m.View())
		}
	}

	// A click beside the buttons is not an answer.
	m := newTestModel(func(string) bool { return false })
	reply := make(chan bool, 1)
	m.Update(confirmMsg{question: "Apply?", reply: reply})
	x, y := find(t, m, "Apply?")
	click(m, x, y)
	if m.dialog == nil {
		t.Error("clicking the question answered it")
	}
}

func TestClickTicksOptionsAndConfirms(t *testing.T) {
	options := []Option{{ID: "claude", Label: "claude  1.0 → 1.1", Checked: true}, {ID: "codex", Label: "codex  2.0 → 2.1", Checked: true}}
	m := newTestModel(func(string) bool { return false })
	reply := make(chan choice, 1)
	m.Update(chooseMsg{question: "Would you like to update?", options: options, reply: reply})

	x, y := find(t, m, "claude  1.0")
	click(m, x, y)
	if !strings.Contains(m.View(), "[ ] claude") || !strings.Contains(m.View(), "[x] codex") {
		t.Fatalf("clicking an option did not untick it alone:\n%s", m.View())
	}
	x, y = find(t, m, "Confirm")
	click(m, x, y)
	if got := <-reply; strings.Join(got.ids, ",") != "codex" || !got.ok {
		t.Errorf("got %v, %v; want codex, true", got.ids, got.ok)
	}

	m.Update(chooseMsg{question: "Would you like to update?", options: options, reply: reply})
	x, y = find(t, m, "Cancel")
	click(m, x, y)
	if got := <-reply; got.ok {
		t.Errorf("clicking Cancel confirmed %v", got.ids)
	}
}

func TestClickOnAViewSelectsAndActs(t *testing.T) {
	var ran []string
	m := newTestModel(func(line string) bool { ran = append(ran, line); return false })
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	x, y := find(t, m, "codex-1 row")
	click(m, x, y)
	if !strings.Contains(m.View(), "› codex-1 row") {
		t.Fatalf("clicking a row did not select it:\n%s", m.View())
	}
	x, y = find(t, m, "enter attach")
	run(m, click(m, x+1, y))
	if len(ran) != 1 || ran[0] != "/attach codex-1" {
		t.Fatalf("clicking the attach hint ran %q, want [/attach codex-1]", ran)
	}
	x, y = find(t, m, "s stop")
	click(m, x, y)
	if m.dialog == nil || !strings.Contains(m.View(), "Stop codex-1?") {
		t.Errorf("clicking the stop hint did not ask first:\n%s", m.View())
	}
}

func TestClickTakesASuggestion(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	typeText(m, "/s")
	x, y := find(t, m, "/stop")
	click(m, x, y)
	if m.input.Value() != "/stop " {
		t.Errorf("clicking a suggestion: line = %q, want %q", m.input.Value(), "/stop ")
	}
}

func TestDragCopiesTheSelection(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	var copied string
	m.copy = func(text string) error { copied = text; return nil }
	m.print("first line here\nsecond line here\nthird\n")

	x, y := find(t, m, "line here") // on the first line
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: x + 5, Y: y + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionMotion})
	if m.sel == nil || !m.sel.dragging {
		t.Fatal("dragging did not start a selection")
	}
	m.Update(tea.MouseMsg{X: x + 5, Y: y + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if want := "line here\nsecond line"; copied != want {
		t.Errorf("copied %q, want %q", copied, want)
	}
	if !strings.Contains(m.View(), "copied 21 characters") {
		t.Errorf("the hint does not say what was copied:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "first line here") || !strings.Contains(m.View(), "second line here") {
		t.Errorf("marking the selection changed the text:\n%s", m.View())
	}

	// Dragging backwards selects the same text, and a plain click selects nothing.
	copied = ""
	m.Update(tea.MouseMsg{X: x + 5, Y: y + 1, Button: tea.MouseButtonLeft, Action: tea.MouseActionPress})
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonLeft, Action: tea.MouseActionRelease})
	if want := "line here\nsecond line"; copied != want {
		t.Errorf("dragging backwards copied %q, want %q", copied, want)
	}
	copied = ""
	click(m, x, y)
	if copied != "" || m.sel != nil {
		t.Errorf("a click copied %q and left a selection", copied)
	}
}

func hover(m *model, x, y int) {
	m.Update(tea.MouseMsg{X: x, Y: y, Button: tea.MouseButtonNone, Action: tea.MouseActionMotion})
}

func TestHoverFindsTheMessageUnderTheMouse(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	m.print("intro text\n\n")
	// Two commands, each printing two lines with a blank one between.
	for _, name := range []string{"one", "two"} {
		typeText(m, "/"+name+" x")
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.print(name + " first\n\n" + name + " last\n")
		run(m, cmd)
	}
	text := func() string {
		from, to, ok := m.hovered()
		if !ok {
			return ""
		}
		var out []string
		for _, l := range m.lines[from:to] {
			out = append(out, strings.TrimRight(l, " "))
		}
		return strings.Join(out, "|")
	}

	if got := text(); got != "" {
		t.Errorf("before the mouse moves, %q is hovered", got)
	}
	x, y := find(t, m, "one last")
	hover(m, x, y)
	if want := "> /one x|one first||one last"; text() != want {
		t.Errorf("hovering a line of the first command: %q, want %q", text(), want)
	}
	hover(m, x, y-3) // its prompt line
	if want := "> /one x|one first||one last"; text() != want {
		t.Errorf("hovering the prompt line: %q, want the same message", text())
	}
	hover(m, x, y+1) // the blank line between the two commands
	if got := text(); got != "" {
		t.Errorf("hovering the gap between messages: %q, want none", got)
	}
	x, y = find(t, m, "two first")
	hover(m, x, y)
	if want := "> /two x|two first||two last"; text() != want {
		t.Errorf("hovering the second command: %q, want %q", text(), want)
	}
	x, y = find(t, m, "intro text")
	hover(m, x, y)
	if text() != "intro text" {
		t.Errorf("hovering the intro: %q", text())
	}
	hover(m, 0, 0) // off the transcript
	if got := text(); got != "" {
		t.Errorf("with the mouse on the frame, %q is hovered", got)
	}
}

func TestTintKeepsTheBackgroundThroughStyledText(t *testing.T) {
	// Colour is forced on, as the tests otherwise run without any.
	r := lipgloss.DefaultRenderer()
	before := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	defer r.SetColorProfile(before)

	const reset = "\x1b[0m"
	got := tint("plain \x1b[32mgreen"+reset+" plain", lipgloss.Color("236"))
	on, _, _ := strings.Cut(got, "plain")
	if on == "" || !strings.Contains(on, "48;5;236") {
		t.Fatalf("the line does not start by switching the background on: %q", got)
	}
	if want := on + "plain \x1b[32mgreen" + reset + on + " plain" + reset; got != want {
		t.Errorf("got %q, want %q", got, want)
	}

	r.SetColorProfile(termenv.Ascii)
	if tint("text", lipgloss.Color("236")) != "text" {
		t.Error("tint on a terminal without colour changed the line")
	}
}

func TestDialogLeavesThePageVisibleAboveIt(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	for n := range 60 {
		m.print(fmt.Sprintf("change %d\n", n))
	}
	reply := make(chan bool, 1)
	m.Update(confirmMsg{question: "Apply this config?", reply: reply})

	lines := strings.Split(m.View(), "\n")
	last, question := -1, -1
	for y, l := range lines {
		if strings.Contains(l, "change 59") {
			last = y
		}
		if strings.Contains(l, "Apply this config?") {
			question = y
		}
	}
	if last < 0 || question < 0 || last > question {
		t.Fatalf("want the last line printed (row %d) visible above the question (row %d):\n%s", last, question, m.View())
	}
	if len(lines) != 24 {
		t.Errorf("the page is %d rows on a 24-row screen", len(lines))
	}

	// The page still scrolls behind the question, and the buttons still work.
	bottom := m.view.YOffset
	wheel(m, tea.MouseButtonWheelUp)
	m.Update(tea.KeyMsg{Type: tea.KeyPgUp})
	if m.view.YOffset >= bottom-wheelLines {
		t.Errorf("the page did not scroll under the dialog: offset %d, was %d", m.view.YOffset, bottom)
	}
	x, y := find(t, m, "Yes")
	click(m, x, y)
	select {
	case got := <-reply:
		if !got {
			t.Error("clicking Yes answered no")
		}
	default:
		t.Errorf("clicking Yes at %d,%d did not answer", x, y)
	}
	if m.view.Height != m.middleHeight() {
		t.Errorf("after the answer the page has %d rows, want all %d back", m.view.Height, m.middleHeight())
	}

	// On a screen too small for both, the dialog takes the middle alone.
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 16})
	m.Update(confirmMsg{question: "Apply this config?", reply: reply})
	if v := m.View(); !strings.Contains(v, "Apply this config?") || len(strings.Split(v, "\n")) != 16 {
		t.Errorf("on a small screen the dialog is missing or the page overflows:\n%s", v)
	}
}
