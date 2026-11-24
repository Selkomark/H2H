package console

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// unknownCSISequenceMsg stands in for the interface library's own type of
// that name, which is how it passes on a key it does not know. The
// console tells Shift+Enter by the type's name and its bytes.
type unknownCSISequenceMsg []byte

func TestInputTakesSeveralLines(t *testing.T) {
	breaks := []struct {
		name string
		do   func(m *model)
	}{
		{"shift+enter, as xterm reports it", func(m *model) { m.Update(unknownCSISequenceMsg("\x1b[27;2;13~")) }},
		{"shift+enter, as kitty reports it", func(m *model) { m.Update(unknownCSISequenceMsg("\x1b[13;2u")) }},
		{"alt+enter", func(m *model) { m.Update(tea.KeyMsg{Type: tea.KeyEnter, Alt: true}) }},
		{"ctrl+j", func(m *model) { m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ}) }},
		{"a backslash, then enter", func(m *model) { typeText(m, "\\"); m.Update(tea.KeyMsg{Type: tea.KeyEnter}) }},
	}
	for _, tt := range breaks {
		t.Run(tt.name, func(t *testing.T) {
			var ran []string
			m := newTestModel(func(line string) bool { ran = append(ran, line); return false })
			typeText(m, "first line")
			tt.do(m)
			if len(ran) != 0 {
				t.Fatalf("the line was sent instead of broken: %q", ran)
			}
			typeText(m, "second line")
			if m.input.Value() != "first line\nsecond line" {
				t.Fatalf("value = %q", m.input.Value())
			}
			v := m.View()
			if !strings.Contains(v, "> first line") || !strings.Contains(v, "  second line") || len(strings.Split(v, "\n")) != 24 {
				t.Errorf("want both lines shown and the page still 24 rows:\n%s", v)
			}
			_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
			run(m, cmd)
			if len(ran) != 1 || ran[0] != "first line\nsecond line" {
				t.Errorf("sent %q, want both lines as one prompt", ran)
			}
			if m.input.Value() != "" || m.inputHeight() != 1 {
				t.Errorf("after sending: value %q, %d rows", m.input.Value(), m.inputHeight())
			}
			if !strings.Contains(m.transcript, "> first line\n  second line") {
				t.Errorf("the transcript does not show the prompt as typed:\n%s", m.transcript)
			}
		})
	}
	// Another unknown key is not a line break.
	m := newTestModel(func(string) bool { return false })
	typeText(m, "x")
	m.Update(unknownCSISequenceMsg("\x1b[27;5;13~")) // ctrl+enter
	if m.input.Value() != "x" {
		t.Errorf("an unrelated key changed the input to %q", m.input.Value())
	}
}

func TestInputPasteAndArrows(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	typeText(m, "/old")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.busy = false // the command is over

	// Pasted text keeps its line breaks.
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("one\ntwo\nthree"), Paste: true})
	if m.input.Value() != "one\ntwo\nthree" || m.inputHeight() != 3 {
		t.Fatalf("pasted value = %q, %d rows", m.input.Value(), m.inputHeight())
	}
	// Up moves through the lines, and reaches the history from the first.
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "one\ntwo\nthree" || m.input.Line() != 0 {
		t.Fatalf("after two ups: value %q, on line %d; want the cursor on the first line", m.input.Value(), m.input.Line())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "/old" {
		t.Errorf("up on the first line recalled %q, want the earlier line", m.input.Value())
	}

	// A long input stops growing and leaves the page its rows.
	m.setLine(strings.Repeat("line\n", 30))
	if got := m.inputHeight(); got != maxInputRows {
		t.Errorf("a 30-line input takes %d rows, want %d", got, maxInputRows)
	}
	if rows := len(strings.Split(m.View(), "\n")); rows != 24 {
		t.Errorf("the page is %d rows with a tall input", rows)
	}
}

func TestChatAnswerTakesSeveralLines(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	m.busy = true
	m.Update(chatOpenMsg{title: "Set up H2H"})
	reply := make(chan chatAnswer, 1)
	m.Update(chatAskMsg{reply: reply})
	typeText(m, "two domains:")
	m.Update(unknownCSISequenceMsg("\x1b[27;2;13~"))
	typeText(m, "ui and api")
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlJ})
	typeText(m, "done")
	if v := m.View(); !strings.Contains(v, "> two domains:") || !strings.Contains(v, "  ui and api") || len(strings.Split(v, "\n")) != 24 {
		t.Fatalf("the answer is not shown on its lines, or the page changed size:\n%s", v)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-reply; got.text != "two domains:\nui and api\ndone" || !got.ok {
		t.Errorf("answer = %q", got.text)
	}
}

func TestInputRows(t *testing.T) {
	tests := []struct {
		value string
		width int
		want  int
	}{
		{"", 80, 1},
		{"short", 80, 1},
		{"a\nb", 80, 2},
		{strings.Repeat("x", 100), 52, 3}, // fifty columns of text a row
		{strings.Repeat("\n", 40), 80, maxInputRows},
	}
	for _, tt := range tests {
		if got := inputRows(tt.value, tt.width); got != tt.want {
			t.Errorf("inputRows(%d chars, %d) = %d, want %d", len(tt.value), tt.width, got, tt.want)
		}
	}
}
