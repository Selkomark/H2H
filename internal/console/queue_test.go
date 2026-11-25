package console

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// gated is a handler whose lines run until they are let go, so a test can
// act while they run.
type gated struct {
	mu   sync.Mutex
	ran  []string
	gate map[string]chan struct{}
}

func newGated() *gated { return &gated{gate: map[string]chan struct{}{}} }

func (g *gated) handle(ctx context.Context, _ UI, line string) bool {
	g.mu.Lock()
	g.ran = append(g.ran, line)
	ch := make(chan struct{})
	g.gate[line] = ch
	g.mu.Unlock()
	select {
	case <-ch:
	case <-ctx.Done():
	}
	return false
}

func (g *gated) lines() []string {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]string{}, g.ran...)
}

// started waits for the line to start running.
func (g *gated) started(t *testing.T, line string) chan struct{} {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		g.mu.Lock()
		ch, ok := g.gate[line]
		g.mu.Unlock()
		if ok {
			return ch
		}
		if time.Now().After(deadline) {
			t.Fatalf("%q never started", line)
		}
		time.Sleep(time.Millisecond)
	}
}

// release lets the line go, once it has started.
func (g *gated) release(t *testing.T, line string) {
	t.Helper()
	close(g.started(t, line))
}

// start runs what Update returned in the background, as the program would,
// and hands back the messages saying a command finished.
func start(cmd tea.Cmd, done chan<- tea.Msg) {
	if cmd == nil {
		return
	}
	go func() {
		switch msg := cmd().(type) {
		case tea.BatchMsg:
			for _, c := range msg {
				start(c, done)
			}
		case doneMsg:
			done <- msg
		}
	}()
}

func enter(m *model, line string, done chan<- tea.Msg) {
	typeText(m, line)
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	start(cmd, done)
}

func wait(t *testing.T, done <-chan tea.Msg) tea.Msg {
	t.Helper()
	select {
	case msg := <-done:
		return msg
	case <-time.After(2 * time.Second):
		t.Fatal("no command finished")
		return nil
	}
}

func queuedModel(t *testing.T) (*model, *gated, chan tea.Msg) {
	g := newGated()
	m := newTestModel(func(string) bool { return false })
	m.o.Handle = g.handle
	return m, g, make(chan tea.Msg, 8)
}

func TestTheInputStaysOpenWhileTheHarnessAnswers(t *testing.T) {
	m, g, done := queuedModel(t)
	enter(m, "first question", done)
	if !m.busy {
		t.Fatal("not busy after sending a message")
	}
	typeText(m, "more")
	if m.input.Value() != "more" {
		t.Errorf("line = %q while busy, want what was typed", m.input.Value())
	}
	v := m.View()
	if !strings.Contains(v, "working") || !strings.Contains(v, promptText+"more") {
		t.Errorf("the view should show working above an open input:\n%s", v)
	}
	g.release(t, "first question")
	m.Update(wait(t, done))
}

func TestMessagesQueueAndGoTogetherWhenTheAnswerEnds(t *testing.T) {
	m, g, done := queuedModel(t)
	enter(m, "first", done)
	enter(m, "second", done)
	enter(m, "third", done)
	g.started(t, "first")
	if got := g.lines(); len(got) != 1 {
		t.Fatalf("ran %q while the first was answered, want only the first", got)
	}
	if len(m.queued) != 2 {
		t.Fatalf("queued = %q, want two messages", m.queued)
	}
	if v := m.View(); !strings.Contains(v, "queued: second") || !strings.Contains(v, "queued: third") || !strings.Contains(v, "2 queued") {
		t.Errorf("the queued messages are not shown:\n%s", v)
	}

	g.release(t, "first")
	_, cmd := m.Update(wait(t, done))
	start(cmd, done)
	if len(m.queued) != 0 || m.talking == 0 {
		t.Fatalf("after the answer: queued %q, talking %d; want the queue sent", m.queued, m.talking)
	}
	g.release(t, "second\nthird")
	m.Update(wait(t, done))
	if got := g.lines(); len(got) != 2 || got[1] != "second\nthird" {
		t.Errorf("ran %q, want the queued messages joined into one", got)
	}
	if strings.Count(m.transcript, promptText) != 2 {
		t.Errorf("transcript = %q; want two messages on the page", m.transcript)
	}
}

func TestEscDropsTheQueueAndUpTakesItBack(t *testing.T) {
	m, g, done := queuedModel(t)
	enter(m, "first", done)
	enter(m, "second", done)
	enter(m, "third", done)

	m.Update(tea.KeyMsg{Type: tea.KeyUp})
	if got := m.input.Value(); got != "second\nthird" || len(m.queued) != 0 {
		t.Fatalf("after up: line %q, queued %q; want the queue back in the input", got, m.queued)
	}
	_, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	start(cmd, done)
	if len(m.queued) != 1 {
		t.Fatalf("queued %q after sending the edited text, want it queued again", m.queued)
	}

	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if len(m.queued) != 0 || !strings.Contains(m.View(), "dropped 1 queued message") {
		t.Errorf("after esc: queued %q; want it dropped and said so:\n%s", m.queued, m.View())
	}
	g.release(t, "first")
	_, cmd = m.Update(wait(t, done))
	if cmd != nil || len(g.lines()) != 1 {
		t.Errorf("ran %q after the answer, want nothing more", g.lines())
	}
}

func TestCtrlCStopsTheAnswerAndReturnsTheQueue(t *testing.T) {
	m, g, done := queuedModel(t)
	enter(m, "first", done)
	enter(m, "second", done)
	m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	_, cmd := m.Update(wait(t, done))
	if cmd != nil || m.busy || len(g.lines()) != 1 {
		t.Errorf("after ctrl+c: busy %v, ran %q; want the answer stopped and nothing sent", m.busy, g.lines())
	}
	if m.input.Value() != "second" {
		t.Errorf("line = %q, want the queued message back in the input", m.input.Value())
	}
}

func TestACommandRunsAtOnceWhileTheHarnessAnswers(t *testing.T) {
	m, g, done := queuedModel(t)
	enter(m, "a question", done)
	g.started(t, "a question")
	m.Update(outputMsg{from: m.talking, text: "The answer begins"})
	asking := m.talking
	enter(m, "/stop claude-1", done)
	g.started(t, "/stop claude-1")
	if got := g.lines(); len(got) != 2 || got[1] != "/stop claude-1" || len(m.queued) != 0 {
		t.Fatalf("ran %q, queued %q; want the command run at once", got, m.queued)
	}
	stopping := m.commands

	// What each prints stays with its own line on the page.
	m.Update(outputMsg{from: stopping, text: "Stopped claude-1.\n"})
	m.Update(outputMsg{from: asking, text: " and goes on.\n"})
	if !strings.Contains(m.transcript, "The answer begins and goes on.") || !strings.Contains(m.transcript, "/stop claude-1\nStopped claude-1.") {
		t.Errorf("the outputs are mixed:\n%s", m.transcript)
	}
	g.release(t, "/stop claude-1")
	m.Update(wait(t, done))
	if !m.busy || m.talking != asking {
		t.Errorf("busy %v, talking %d after the command; want the answer still running", m.busy, m.talking)
	}
	// Once the command is done, the answer moves below it, where the page
	// ends.
	m.Update(outputMsg{from: asking, text: "Done.\n"})
	if s := m.transcript; strings.Index(s, "Stopped claude-1.") > strings.Index(s, "The answer begins") {
		t.Errorf("the running answer did not move to the end:\n%s", s)
	}
	g.release(t, "a question")
	m.Update(wait(t, done))
}

func TestQuestionsFromTwoCommandsAreAskedInTurn(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	first, second := make(chan bool, 1), make(chan bool, 1)
	m.Update(confirmMsg{question: "Stop claude-1?", reply: first})
	m.Update(confirmMsg{question: "Delete codex-1?", reply: second})
	if v := m.View(); !strings.Contains(v, "Stop claude-1?") || strings.Contains(v, "Delete codex-1?") {
		t.Fatalf("want the first question alone on screen:\n%s", v)
	}
	typeText(m, "y")
	if got := <-first; !got {
		t.Error("the first question did not get its yes")
	}
	if v := m.View(); !strings.Contains(v, "Delete codex-1?") {
		t.Fatalf("the second question did not follow:\n%s", v)
	}
	typeText(m, "n")
	if got := <-second; got {
		t.Error("the second question did not get its no")
	}
	if m.dialog != nil {
		t.Error("a question is still on screen after both were answered")
	}
}
