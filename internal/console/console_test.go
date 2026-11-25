package console

import (
	"context"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

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

func (fakeUI) Write(b []byte) (int, error)              { return len(b), nil }
func (fakeUI) Confirm(string) bool                      { return false }
func (fakeUI) Choose(string, []Option) ([]string, bool) { return nil, false }
func (fakeUI) Foreground(fn func() error) error         { return fn() }

func newTestModel(handle func(line string) bool) *model {
	plain := lipgloss.NewStyle()
	m := newModel(Options{
		Theme:     Theme{Accent: plain, Dim: plain, Frame: lipgloss.NoColor{}},
		Title:     "H2H",
		Commands:  testCommands,
		Arguments: testArguments,
		Views: []View{{
			Name:    "Sessions",
			Command: "/ps",
			Table: func() Table {
				return Table{Header: "SESSION TABLE", Rows: []Row{{ID: "claude-1", Text: "claude-1 row"}, {ID: "codex-1", Text: "codex-1 row"}}}
			},
			Actions: []Action{
				{Key: "enter", Label: "attach", Line: func(id string) string { return "/attach " + id }},
				{Key: "s", Label: "stop", Line: func(id string) string { return "/stop " + id }, Confirm: "Stop %s?"},
			},
		}},
		Handle: func(_ context.Context, _ UI, line string) bool { return handle(line) },
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
	typeText(m, "/sto")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !m.busy {
		t.Fatal("after enter: not busy")
	}
	run(m, cmd)
	if len(ran) != 1 || ran[0] != "/stop" {
		t.Errorf("handler got %q, want [/stop]", ran)
	}
	if m.busy || m.input.Value() != "" {
		t.Errorf("after the command: busy = %v, line = %q; want idle and empty", m.busy, m.input.Value())
	}
	if !strings.Contains(m.transcript, "> /stop") {
		t.Errorf("transcript = %q; want the typed line echoed", m.transcript)
	}
}

func TestViewCommandSwitchesTab(t *testing.T) {
	var ran []string
	m := newTestModel(func(line string) bool { ran = append(ran, line); return false })
	if strings.Contains(m.View(), "SESSION TABLE") {
		t.Fatal("the sessions view is showing before it was asked for")
	}
	typeText(m, "/ps")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if len(ran) != 0 {
		t.Errorf("handler got %q; a view command must not run anything", ran)
	}
	if m.tab != 1 || !strings.Contains(m.View(), "SESSION TABLE") {
		t.Errorf("after /ps: tab = %d; want the sessions view on screen:\n%s", m.tab, m.View())
	}

	// Any other line brings the transcript back, so its output is seen.
	typeText(m, "hello")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if m.tab != 0 || len(ran) != 1 {
		t.Errorf("after another line: tab = %d, handler got %q; want the transcript and one call", m.tab, ran)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.tab != 1 {
		t.Errorf("after shift+tab: tab = %d, want 1", m.tab)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if m.tab != 0 {
		t.Errorf("after shift+tab twice: tab = %d, want 0", m.tab)
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
	}{{"y", true}, {"n", false}, {"x", false}, {"enter", false}, {"left", true}} {
		m := newTestModel(func(string) bool { return false })
		reply := make(chan bool, 1)
		m.Update(confirmMsg{question: "Apply?", reply: reply})
		if v := m.View(); !strings.Contains(v, "Apply?") || !strings.Contains(v, "Yes") || !strings.Contains(v, "╭") {
			t.Errorf("view does not show the question in a box with its buttons:\n%s", v)
		}
		switch tt.key {
		case "enter": // No has the focus
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		case "left": // moves the focus to Yes
			m.Update(tea.KeyMsg{Type: tea.KeyLeft})
			m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		case "x": // not an answer: the question stays until Esc
			typeText(m, tt.key)
			if m.dialog == nil {
				t.Errorf("the key %q answered the question", tt.key)
			}
			m.Update(tea.KeyMsg{Type: tea.KeyEsc})
		default:
			typeText(m, tt.key)
		}
		if got := <-reply; got != tt.want {
			t.Errorf("answering %q: got %v, want %v", tt.key, got, tt.want)
		}
		if m.input.Value() != "" {
			t.Errorf("the answer %q leaked into the input line", tt.key)
		}
	}
}

func TestChoose(t *testing.T) {
	options := []Option{{ID: "claude", Label: "claude  1.0 → 1.1", Checked: true}, {ID: "codex", Label: "codex  2.0 → 2.1", Checked: true}}
	space, down, enter := tea.KeyMsg{Type: tea.KeySpace}, tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyEnter}
	tests := []struct {
		name   string
		keys   []tea.KeyMsg
		want   string
		wantOK bool
	}{
		{"confirm as offered", []tea.KeyMsg{enter}, "claude,codex", true},
		{"untick the first", []tea.KeyMsg{space, enter}, "codex", true},
		{"untick the second", []tea.KeyMsg{down, space, enter}, "claude", true},
		{"a clears when all are ticked", []tea.KeyMsg{{Type: tea.KeyRunes, Runes: []rune("a")}, enter}, "", true},
		{"esc cancels", []tea.KeyMsg{{Type: tea.KeyEsc}}, "", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			m := newTestModel(func(string) bool { return false })
			reply := make(chan choice, 1)
			m.Update(chooseMsg{question: "Would you like to update?", options: options, reply: reply})
			if v := m.View(); !strings.Contains(v, "Would you like to update?") || !strings.Contains(v, "› [x] claude  1.0 → 1.1") {
				t.Fatalf("view does not show the options ticked:\n%s", v)
			}
			for _, k := range tt.keys {
				m.Update(k)
			}
			got := <-reply
			if strings.Join(got.ids, ",") != tt.want || got.ok != tt.wantOK {
				t.Errorf("got %v, %v; want %q, %v", got.ids, got.ok, tt.want, tt.wantOK)
			}
			if !options[0].Checked || !options[1].Checked {
				t.Error("the dialog changed the caller's options")
			}
		})
	}
}

func TestViewSelectionAndActions(t *testing.T) {
	var ran []string
	m := newTestModel(func(line string) bool { ran = append(ran, line); return false })
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if !strings.Contains(m.View(), "› claude-1 row") {
		t.Fatalf("the first row is not selected:\n%s", m.View())
	}

	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(m.View(), "› codex-1 row") {
		t.Fatalf("down did not select the second row:\n%s", m.View())
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	run(m, cmd)
	if len(ran) != 1 || ran[0] != "/attach codex-1" {
		t.Fatalf("enter ran %q, want [/attach codex-1]", ran)
	}
	if m.tab != 1 {
		t.Errorf("after an action: tab = %d, want to stay on the view", m.tab)
	}

	// An action with a question runs only on yes.
	typeText(m, "s")
	if !strings.Contains(m.View(), "Stop codex-1?") {
		t.Fatalf("s did not ask before stopping:\n%s", m.View())
	}
	typeText(m, "n")
	if len(ran) != 1 {
		t.Fatalf("declined, but ran %q", ran)
	}
	typeText(m, "s")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("y")})
	run(m, cmd)
	if len(ran) != 2 || ran[1] != "/stop codex-1" {
		t.Errorf("confirmed stop ran %q, want /stop codex-1 last", ran)
	}

	// Other keys are typed as usual, and typing takes the keys back.
	typeText(m, "/")
	if m.input.Value() != "/" {
		t.Errorf("typing / on a view: line = %q, want %q", m.input.Value(), "/")
	}
	typeText(m, "s")
	if m.input.Value() != "/s" || m.dialog != nil {
		t.Errorf("s while typing: line = %q, dialog = %v; want it typed", m.input.Value(), m.dialog)
	}
}

func TestViewFitsTheScreen(t *testing.T) {
	for _, tab := range []int{0, 1} {
		m := newTestModel(func(string) bool { return false })
		m.tab = tab
		m.print(strings.Repeat("a long line of output that has to wrap somewhere ", 20) + "\n")
		typeText(m, "/")
		lines := strings.Split(m.View(), "\n")
		if len(lines) != 24 {
			t.Errorf("tab %d: view has %d lines, want exactly the screen height of 24", tab, len(lines))
		}
		for n, l := range lines {
			if w := lipgloss.Width(l); w > 80 {
				t.Errorf("tab %d: line %d is %d wide, want at most 80: %q", tab, n, w, l)
			}
		}
		if !strings.HasPrefix(lines[0], "╭") || !strings.HasPrefix(lines[len(lines)-2], "╰") {
			t.Errorf("tab %d: the page is not framed:\n%s", tab, m.View())
		}
	}
}

func TestSessionIsSavedAndRestored(t *testing.T) {
	var saved State
	o := Options{
		Theme:  Theme{Accent: lipgloss.NewStyle(), Dim: lipgloss.NewStyle(), Frame: lipgloss.NoColor{}},
		Intro:  "welcome\n\n",
		Save:   func(s State) { saved = s },
		Handle: func(_ context.Context, ui UI, line string) bool { return false },
	}
	m := newModel(o, fakeUI{})
	m.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, line := range []string{"/one", "what next?"} {
		typeText(m, line)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.print("answer to " + line + "\n")
		run(m, cmd)
	}
	if strings.Join(saved.History, "|") != "/one|what next?" || len(saved.Starts) != 3 || !strings.Contains(saved.Transcript, "answer to what next?") {
		t.Fatalf("saved %+v; want both lines, three messages and the page", saved)
	}

	// A new console carries on from what was saved.
	o.Restore, o.Intro = &saved, "Resumed.\n\n"
	r := newModel(o, fakeUI{})
	r.Update(tea.WindowSizeMsg{Width: 80, Height: 24})
	for _, want := range []string{"welcome", "> /one", "answer to what next?", "Resumed."} {
		if !strings.Contains(r.View(), want) {
			t.Errorf("the resumed page lacks %q:\n%s", want, r.View())
		}
	}
	if len(r.starts) != 4 {
		t.Errorf("resumed with %d messages, want the three saved and the note", len(r.starts))
	}
	r.Update(tea.KeyMsg{Type: tea.KeyUp})
	if r.input.Value() != "what next?" {
		t.Errorf("up after resuming recalled %q, want the last line of the saved session", r.input.Value())
	}

	// Message starts that do not fit the page are dropped, not trusted.
	o.Restore = &State{Transcript: "short\n", Starts: []int{0, 3, 3, 900, 2}}
	if b := newModel(o, fakeUI{}); len(b.starts) != 3 || b.starts[1] != 3 {
		t.Errorf("starts = %v, want 0, 3 and the note", b.starts)
	}
}

func (fakeUI) Load(State, string) {}

func TestLoadSwitchesThePage(t *testing.T) {
	var ran []string
	m := newTestModel(func(line string) bool { ran = append(ran, line); return false })
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab}) // on a view, as when a row is chosen there
	typeText(m, "/old")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.print("old output\n")

	// The command running swaps in another session before it finishes.
	m.Update(loadMsg{state: State{Transcript: "> /first\nearlier output\n\n", History: []string{"/first"}, Starts: []int{0}}, note: "Resumed.\n\n"})
	run(m, cmd)

	v := m.View()
	if m.tab != 0 || !strings.Contains(v, "earlier output") || !strings.Contains(v, "Resumed.") || strings.Contains(v, "old output") {
		t.Fatalf("tab %d; want the transcript showing only the loaded session:\n%s", m.tab, v)
	}
	if len(m.starts) != 2 || m.notice != "" {
		t.Errorf("starts = %v, notice = %q; want the loaded message and the note", m.starts, m.notice)
	}
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "/first" {
		t.Errorf("up recalled %q, want the loaded session's line", m.input.Value())
	}
}

func (fakeUI) Chat(string) Chat { return nil }

func TestChatBox(t *testing.T) {
	cancelled := false
	m := newTestModel(func(string) bool { return false })
	m.busy, m.cancel = true, func() { cancelled = true } // a command is running the conversation
	m.Update(chatOpenMsg{title: "Set up H2H"})
	m.Update(chatWaitMsg{note: "claude is thinking"})
	if v := m.View(); !strings.Contains(v, "Set up H2H") || !strings.Contains(v, "claude is thinking") {
		t.Fatalf("the box does not show its title and what it waits for:\n%s", v)
	}

	// Typing before a question is asked goes nowhere.
	typeText(m, "early")
	m.Update(chatSayMsg{text: "Which harness should own the UI?"})
	reply := make(chan chatAnswer, 1)
	m.Update(chatAskMsg{reply: reply})
	if v := m.View(); !strings.Contains(v, "Which harness should own the UI?") || strings.Contains(v, "early") {
		t.Fatalf("the question is not shown, or early typing was kept:\n%s", v)
	}
	typeText(m, "claude")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if got := <-reply; got.text != "claude" || !got.ok {
		t.Errorf("answer = %+v, want claude", got)
	}
	if v := m.View(); !strings.Contains(v, "> claude") || m.input.Value() != "" {
		t.Errorf("the answer is not in the conversation, or it leaked into the page's input:\n%s", v)
	}

	// Esc while a question is open cancels it and the command behind it.
	m.Update(chatAskMsg{reply: reply})
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if got := <-reply; got.ok || !cancelled {
		t.Errorf("esc: answer %+v, cancelled %v; want the question refused and the command cancelled", got, cancelled)
	}

	// Closing takes the box away, and a late question gets no for an answer.
	m.Update(chatCloseMsg{})
	if m.chat != nil || strings.Contains(m.View(), "Set up H2H") {
		t.Error("the box is still on the page after closing")
	}
	m.Update(chatAskMsg{reply: reply})
	if got := <-reply; got.ok {
		t.Error("a question after closing was answered")
	}
}

func TestViewsStayUsableWhileACommandRuns(t *testing.T) {
	var mu sync.Mutex // the two commands run at once
	var ran []string
	release := make(chan struct{})
	m := newTestModel(func(line string) bool {
		mu.Lock()
		ran = append(ran, line)
		mu.Unlock()
		if line == "delegate the work" {
			<-release // a command that takes its time
		}
		return false
	})
	typeText(m, "delegate the work")
	_, long := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	finished := make(chan tea.Msg, 1)
	go func() {
		for _, c := range long().(tea.BatchMsg) {
			if msg, ok := c().(doneMsg); ok {
				finished <- msg
			}
		}
	}()

	for deadline := time.Now().Add(2 * time.Second); time.Now().Before(deadline); time.Sleep(5 * time.Millisecond) {
		mu.Lock()
		started := len(ran) == 1
		mu.Unlock()
		if started {
			break
		}
	}

	// While it runs, the Harnesses view is navigated and acted on.
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if !strings.Contains(m.View(), "› codex-1 row") {
		t.Fatalf("down did not move the selection while a command runs:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "enter attach") {
		t.Errorf("the view's keys are not shown while a command runs:\n%s", m.View())
	}
	_, side := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if side == nil {
		t.Fatal("enter on a row did nothing while a command runs")
	}
	m.Update(side()) // the action finishes first
	mu.Lock()
	if len(ran) != 2 || !slices.Contains(ran, "/attach codex-1") {
		t.Fatalf("ran %q, want the action run beside the first command", ran)
	}
	mu.Unlock()
	if !m.busy {
		t.Error("the page stopped waiting although the first command is still running")
	}
	// The input is open while the command runs.
	typeText(m, "x")
	if m.input.Value() != "x" {
		t.Errorf("typing while a command runs: line = %q, want %q", m.input.Value(), "x")
	}

	close(release)
	m.Update(<-finished)
	if m.busy || len(m.running) != 0 {
		t.Errorf("after both finished: busy %v, running %v", m.busy, m.running)
	}
}

func TestCtrlCStopsEveryRunningCommand(t *testing.T) {
	stopped := 0
	m := newTestModel(func(string) bool { return false })
	m.o.Handle = func(ctx context.Context, _ UI, _ string) bool {
		<-ctx.Done()
		stopped++
		return false
	}
	typeText(m, "first")
	_, a := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	_, b := m.Update(tea.KeyMsg{Type: tea.KeyEnter}) // attach, beside it
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab}) // back on the transcript
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	for _, c := range a().(tea.BatchMsg) {
		if msg, ok := c().(doneMsg); ok {
			m.Update(msg)
		}
	}
	m.Update(b())
	if stopped != 2 || m.busy {
		t.Errorf("ctrl+c stopped %d of 2 commands, busy %v", stopped, m.busy)
	}
}

func TestNoticeHoldsOnlyItsOwnActionsOutput(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	// A long command is running and printing: the assistant, say.
	typeText(m, "delegate the work")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	long := m.commands

	// Beside it, a row's action is run from the view.
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	action := m.commands
	if action == long {
		t.Fatal("the action did not get a number of its own")
	}
	m.Update(outputMsg{from: action, text: "Attaching to claude-1.\n"})
	m.Update(outputMsg{from: long, text: "BUILT: a long report from another harness\n"})
	m.Update(outputMsg{from: action, text: "Left claude-1 running.\n"})
	m.Update(outputMsg{from: long, text: "> /ask development what is left?\n"})
	m.Update(doneMsg{from: action})

	if want := "Attaching to claude-1.\nLeft claude-1 running."; m.notice != want {
		t.Errorf("notice = %q, want %q", m.notice, want)
	}
	if v := m.View(); strings.Contains(v, "BUILT") || strings.Contains(v, "/ask development") || !strings.Contains(v, "Left claude-1 running.") {
		t.Errorf("the view shows another command's output under its rows:\n%s", v)
	}
	// All of it is still on the Console page.
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	if v := m.View(); !strings.Contains(v, "BUILT: a long report") || !strings.Contains(v, "Left claude-1 running.") {
		t.Errorf("the transcript lost output:\n%s", v)
	}
	// The long command ending does not replace the notice.
	m.Update(doneMsg{from: long})
	if !strings.Contains(m.notice, "Left claude-1 running.") {
		t.Errorf("after the other command ended, notice = %q", m.notice)
	}
}

func TestRequestFromOutsideIsPutToTheUser(t *testing.T) {
	var answered []string
	pending := &Request{
		ID:       "ui-1/1",
		Question: "ui-1 wants to run this command:\n\nmake deploy",
		Options:  []Option{{ID: "once", Label: "Allow once"}, {ID: "always", Label: "Allow for the session"}, {ID: "deny", Label: "Deny"}},
	}
	pending.Answer = func(option string) { answered = append(answered, option) }
	m := newTestModel(func(string) bool { return false })
	m.o.Requests = func() *Request { return pending }

	typeText(m, "/sta") // the user is in the middle of typing
	m.Update(m.snapshot())
	if v := m.View(); m.dialog == nil || !strings.Contains(v, "make deploy") || !strings.Contains(v, "› Deny") {
		t.Fatalf("the request is not shown with the focus on Deny:\n%s", v)
	}
	// Their next Enter must not allow anything.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(answered) != 1 || answered[0] != "deny" {
		t.Fatalf("Enter answered %q, want deny", answered)
	}
	if m.input.Value() != "/sta" {
		t.Errorf("the line being typed is %q, want it kept", m.input.Value())
	}

	// Picking an option gives its ID, and Esc gives none.
	m.Update(m.snapshot())
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(m.snapshot())
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if strings.Join(answered, ",") != "deny,once," {
		t.Errorf("answers = %q, want deny, once and none", answered)
	}

	// While another question is open, the request waits.
	reply := make(chan bool, 1)
	m.Update(confirmMsg{question: "Apply?", reply: reply})
	m.Update(m.snapshot())
	if m.dialog == nil || m.dialog.question != "Apply?" {
		t.Errorf("the request replaced a question that was open:\n%s", m.View())
	}
}

func (fakeUI) Clear() {}

func TestClearEmptiesThePage(t *testing.T) {
	var saved State
	m := newTestModel(func(string) bool { return false })
	m.o.Save = func(s State) { saved = s }
	for _, line := range []string{"/one", "/two"} {
		typeText(m, line)
		_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
		m.print("output of " + line + "\n")
		run(m, cmd)
	}
	wheel(m, tea.MouseButtonWheelUp)

	// The command that clears is itself on the page until it runs.
	typeText(m, "/cls")
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.Update(clearMsg{})
	run(m, cmd)

	if v := m.View(); strings.Contains(v, "output of") || strings.Contains(v, "> /cls") || m.transcript != "" {
		t.Fatalf("the page is not empty after clearing: %q\n%s", m.transcript, v)
	}
	if len(m.starts) != 1 || m.unseen != 0 || m.sel != nil {
		t.Errorf("left over from the old page: starts %v, unseen %d", m.starts, m.unseen)
	}
	if saved.Transcript != "" || strings.Join(saved.History, "|") != "/one|/two|/cls" {
		t.Errorf("saved %q with history %q; want an empty page and the entered lines kept", saved.Transcript, saved.History)
	}
	// What was entered before is still in reach, and the page works on.
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if m.input.Value() != "/two" {
		t.Errorf("up after clearing recalled %q", m.input.Value())
	}
	m.setLine("")
	typeText(m, "/three")
	_, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	m.print("output of /three\n")
	run(m, cmd)
	if v := m.View(); !strings.Contains(v, "> /three") || !strings.Contains(v, "output of /three") {
		t.Errorf("the page does not carry on after clearing:\n%s", v)
	}
}

func TestChatShowsDrawnBlocksUnwrapped(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	m.busy = true
	m.Update(chatOpenMsg{title: "Set up H2H"})
	var widths []int
	m.Update(chatShowMsg{draw: func(width int) string {
		widths = append(widths, width)
		return "╭──────┬────────╮\n│ api  │ claude │\n╰──────┴────────╯\n" + strings.Repeat("row\n", 30)
	}})
	if v := m.View(); !strings.Contains(v, "│ api  │ claude │") || !strings.Contains(v, "╭──────┬────────╮") {
		t.Fatalf("a drawn block is not shown as drawn, from its top:\n%s", v)
	}
	if len(widths) == 0 || widths[0] != m.chat.width {
		t.Errorf("the block was drawn for widths %v, want the box's %d", widths, m.chat.width)
	}
	// Once the other side speaks, the conversation follows its end.
	m.Update(chatSayMsg{text: "What should change?"})
	if v := m.View(); !strings.Contains(v, "What should change?") || len(strings.Split(v, "\n")) != 24 {
		t.Errorf("the message is not in view, or the page changed size:\n%s", v)
	}
	// A long answer wraps in the input and leaves the conversation in view.
	reply := make(chan chatAnswer, 1)
	m.Update(chatAskMsg{reply: reply})
	typeText(m, strings.Repeat("rename the domain and keep the rest ", 6))
	v := m.View()
	if m.chat.input.Height() < 2 || !strings.Contains(v, "What should change?") || len(strings.Split(v, "\n")) != 24 {
		t.Errorf("a long answer: input of %d rows; want it wrapped with the question still in view:\n%s", m.chat.input.Height(), v)
	}
}
