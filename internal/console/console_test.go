package console

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var testCommands = []Suggestion{
	{Text: "/ps", Help: "List sessions"},
	{Text: "/start", Usage: "[harness]", Help: "Start a session"},
	{Text: "/stop", Usage: "[session]", Help: "Stop a session"},
	{Text: "/attach", Usage: "[session]", Help: "Open a session"},
}

func testArguments(command string) []Suggestion {
	if command == "attach" || command == "stop" {
		return []Suggestion{{Text: "claude-1"}, {Text: "codex-1"}}
	}
	return nil
}

func texts(s []Suggestion) string {
	out := make([]string, len(s))
	for i, x := range s {
		out[i] = x.Text
	}
	return strings.Join(out, ",")
}

func TestSuggest(t *testing.T) {
	tests := []struct {
		line        string
		want        string
		forArgument bool
	}{
		{"", "", false},
		{"hello", "", false},
		{"/", "/ps,/start,/stop,/attach", false},
		{"/s", "/start,/stop", false},
		{"/x", "", false},
		{"/attach ", "claude-1,codex-1", true},
		{"/attach co", "codex-1", true},
		{"/attach codex-1 ", "", true},
		{"/ps ", "", true},
	}
	for _, tt := range tests {
		got, forArgument := suggest(tt.line, testCommands, testArguments)
		if texts(got) != tt.want || forArgument != tt.forArgument {
			t.Errorf("suggest(%q) = %q, %v; want %q, %v", tt.line, texts(got), forArgument, tt.want, tt.forArgument)
		}
	}
}

func TestAccept(t *testing.T) {
	if got := accept("/s", Suggestion{Text: "/start"}, false); got != "/start " {
		t.Errorf("accepting a command = %q, want %q", got, "/start ")
	}
	if got := accept("/attach co", Suggestion{Text: "codex-1"}, true); got != "/attach codex-1 " {
		t.Errorf("accepting an argument = %q, want %q", got, "/attach codex-1 ")
	}
}

func TestSanitize(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"plain", "hello\n", "hello\n"},
		{"colour is kept", "\x1b[1;36mhi\x1b[0m", "\x1b[1;36mhi\x1b[0m"},
		{"cursor movement is removed", "a\x1b[2Ab\x1b[?25lc\x1b[Hd", "abcd"},
		{"window title is removed", "\x1b]0;title\x07text", "text"},
		{"alternate screen is removed", "\x1b[?1049hx\x1b[?1049l", "x"},
		{"other escapes are removed", "a\x1b=b\x1b7c", "abc"},
		{"carriage returns become newlines", "a\r\nb\rc", "a\nb\nc"},
		{"control characters are removed", "a\x07b\x00c\td", "abc\td"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := sanitize(tt.in); got != tt.want {
				t.Errorf("sanitize(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// fakeUI lets a model run without a program behind it.
type fakeUI struct{}

func (fakeUI) Write(b []byte) (int, error)      { return len(b), nil }
func (fakeUI) Confirm(string) bool              { return false }
func (fakeUI) Foreground(fn func() error) error { return fn() }

func newTestModel(handle func(line string) bool) *model {
	plain := lipgloss.NewStyle()
	m := newModel(Options{
		Theme:     Theme{Accent: plain, Dim: plain, Frame: lipgloss.NoColor{}},
		Title:     "H2H",
		Commands:  testCommands,
		Arguments: testArguments,
		Handle:    func(_ context.Context, _ UI, line string) bool { return handle(line) },
	}, fakeUI{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	return m
}

func typeText(m *model, s string) {
	m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)})
}

// run executes a command returned by Update and feeds its messages back,
// skipping timers.
func run(m *model, cmd tea.Cmd) {
	if cmd == nil {
		return
	}
	switch msg := cmd().(type) {
	case tea.BatchMsg:
		for _, c := range msg {
			run(m, c)
		}
	case doneMsg:
		m.Update(msg)
	}
}

func TestTypingShowsSuggestionsAndTabCompletes(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	typeText(m, "/s")
	if got := texts(m.shown()); got != "/start,/stop" {
		t.Fatalf("after typing /s: suggestions = %q, want /start,/stop", got)
	}
	if !strings.Contains(m.View(), "Start a session") {
		t.Errorf("view does not show the suggestion list:\n%s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyTab})
	if got := m.input.Value(); got != "/stop " {
		t.Fatalf("after down and tab: line = %q, want %q", got, "/stop ")
	}
	if got := texts(m.shown()); got != "claude-1,codex-1" {
		t.Errorf("after completing /stop: suggestions = %q, want the sessions", got)
	}
}

func TestEnterRunsHighlightedCommand(t *testing.T) {
	var ran []string
	m := newTestModel(func(line string) bool { ran = append(ran, line); return false })
	typeText(m, "/p")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy {
		t.Fatal("after enter: not busy")
	}
	run(m, cmd)
	if len(ran) != 1 || ran[0] != "/ps" {
		t.Errorf("handler got %q, want [/ps]", ran)
	}
	if m.busy || m.input.Value() != "" {
		t.Errorf("after the command: busy = %v, line = %q; want idle and empty", m.busy, m.input.Value())
	}
	if !strings.Contains(m.transcript, "> /ps") {
		t.Errorf("transcript = %q; want the typed line echoed", m.transcript)
	}
}

func TestFreeTextIsSentAsIs(t *testing.T) {
	var ran []string
	m := newTestModel(func(line string) bool { ran = append(ran, line); return false })
	typeText(m, "what is running?")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if len(ran) != 1 || ran[0] != "what is running?" {
		t.Errorf("handler got %q", ran)
	}
}

func TestHistory(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	for _, line := range []string{"first", "second"} {
		typeText(m, line)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		run(m, cmd)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "first" {
		t.Errorf("after up twice: line = %q, want %q", got, "first")
	}
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if got := m.input.Value(); got != "" {
		t.Errorf("after down twice: line = %q, want empty", got)
	}
}

func TestCtrlCClearsThenNeedsTwoPressesToExit(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	typeText(m, "half a thought")
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil || m.input.Value() != "" {
		t.Fatalf("first ctrl+c: line = %q, cmd = %v; want the line cleared and no exit", m.input.Value(), cmd)
	}
	if _, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC}); cmd != nil {
		t.Fatal("ctrl+c on an empty line exited at once")
	}
	if !strings.Contains(m.View(), exitHint) {
		t.Errorf("view does not warn before exiting:\n%s", m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd == nil {
		t.Fatal("second ctrl+c on an empty line did not exit")
	}
	if _, ok := cmd().(tea.QuitMsg); !ok {
		t.Error("second ctrl+c did not produce a quit")
	}
}

func TestConfirm(t *testing.T) {
	for _, tt := range []struct {
		key  string
		want bool
	}{{"y", true}, {"n", false}, {"x", false}} {
		m := newTestModel(func(string) bool { return false })
		reply := make(chan bool, 1)
		m.Update(confirmMsg{question: "Apply?", reply: reply})
		if !strings.Contains(m.View(), "Apply?") {
			t.Errorf("view does not show the question:\n%s", m.View())
		}
		typeText(m, tt.key)
		if got := <-reply; got != tt.want {
			t.Errorf("answering %q: got %v, want %v", tt.key, got, tt.want)
		}
		if m.input.Value() != "" {
			t.Errorf("the answer %q leaked into the input line", tt.key)
		}
	}
}

func TestViewFitsTheScreen(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	m.print(strings.Repeat("a long line of output that has to wrap somewhere ", 20) + "\n")
	typeText(m, "/")
	lines := strings.Split(m.View(), "\n")
	if len(lines) != 24 {
		t.Errorf("view has %d lines, want exactly the screen height of 24", len(lines))
	}
	for n, l := range lines {
		if w := lipgloss.Width(l); w > 80 {
			t.Errorf("line %d is %d wide, want at most 80: %q", n, w, l)
		}
	}
}
