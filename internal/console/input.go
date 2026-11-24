package console

import (
	"io"
	"reflect"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	// maxInputRows is how tall an input grows before it scrolls.
	maxInputRows = 8
	// continuation is the prompt of every line of an input after its first.
	continuation = "  "

	// With this mode on, a terminal reports Shift+Enter as its own key
	// instead of as a plain Enter. Terminals that do not know the mode
	// ignore it, and there the other ways to break a line still work.
	reportShiftEnter = "\x1b[>4;1m"
	forgetShiftEnter = "\x1b[>4;0m"
)

// newInput makes the box a user types a prompt into. It takes several
// lines: Enter sends, and Shift+Enter, Alt+Enter, Ctrl+J or a backslash
// before Enter break the line. Pasted text keeps its line breaks.
func newInput(th Theme, placeholder string) textarea.Model {
	in := textarea.New()
	in.Placeholder = placeholder
	in.ShowLineNumbers = false
	in.CharLimit = 0
	in.MaxHeight = maxInputRows
	in.SetHeight(1)
	in.SetPromptFunc(lipgloss.Width(promptText), func(line int) string {
		if line == 0 {
			return promptText
		}
		return continuation
	})
	// The box is part of the page: no highlighted line, no frame of its own.
	plain := lipgloss.NewStyle()
	in.FocusedStyle.Base, in.FocusedStyle.CursorLine, in.FocusedStyle.Text = plain, plain, plain
	in.FocusedStyle.Prompt, in.FocusedStyle.Placeholder = th.Accent, th.Dim
	in.FocusedStyle.EndOfBuffer = plain
	in.BlurredStyle = in.FocusedStyle
	// Enter is the console's to decide: it sends, or with a modifier
	// breaks the line.
	in.KeyMap.InsertNewline.SetEnabled(false)
	in.Focus()
	return in
}

// breaksLine reports whether a key press asks for a new line instead of
// sending what was typed: Alt+Enter, or Ctrl+J. Shift+Enter arrives
// another way; see shiftEnter.
func breaksLine(k tea.KeyMsg) bool {
	return (k.Type == tea.KeyEnter && k.Alt) || k.Type == tea.KeyCtrlJ
}

// shiftEnter reports whether msg is Shift+Enter as a terminal reports it
// once asked to: a key the interface library does not know, which it
// passes on as an unrecognised sequence.
func shiftEnter(msg tea.Msg) bool {
	v := reflect.ValueOf(msg)
	if !v.IsValid() || v.Kind() != reflect.Slice || v.Type().Elem().Kind() != reflect.Uint8 || v.Type().Name() != "unknownCSISequenceMsg" {
		return false
	}
	switch string(v.Bytes()) {
	case "\x1b[27;2;13~", "\x1b[13;2u":
		return true
	}
	return false
}

// endsWithBackslash reports whether Enter should break the line because
// the text typed so far ends in a backslash, and returns the text without
// it. It is the way to break a line that works in every terminal.
func endsWithBackslash(value string) (string, bool) {
	return strings.CutSuffix(value, "\\")
}

// inputRows is how many rows an input needs to show its text at a width,
// between one and maxInputRows.
func inputRows(value string, width int) int {
	// Lines wrap at words, as the input itself wraps them, so the rows
	// are counted from the wrapped text and not from its length.
	text := max(1, width-lipgloss.Width(promptText))
	wrap := lipgloss.NewStyle().Width(text)
	rows := 0
	for _, line := range strings.Split(value, "\n") {
		rows += lipgloss.Height(wrap.Render(line))
		// A line that fills its last row to the edge needs one more, for
		// the cursor after its last character.
		if w := lipgloss.Width(line); w > 0 && w%text == 0 {
			rows++
		}
	}
	return min(max(1, rows), maxInputRows)
}

// fitInput sizes an input to its text.
func fitInput(in *textarea.Model, width int) {
	in.SetWidth(max(lipgloss.Width(promptText)+1, width))
	in.SetHeight(inputRows(in.Value(), width))
}

// setShiftEnter switches the terminal's reporting of Shift+Enter on or off.
func setShiftEnter(out io.Writer, on bool) {
	if out == nil {
		return
	}
	if on {
		_, _ = io.WriteString(out, reportShiftEnter)
	} else {
		_, _ = io.WriteString(out, forgetShiftEnter)
	}
}
