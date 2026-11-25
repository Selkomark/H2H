package console

import (
	"slices"
	"strings"
	"testing"
)

func TestHeedNoticesGoInTheTranscriptAndTheAlertReplacesWorking(t *testing.T) {
	m := newTestModel(func(string) bool { return false })
	var notices []string
	alert := ""
	m.o.Heed = func() ([]string, string) {
		n := notices
		notices = nil // each notice is handed over once
		return n, alert
	}

	// A notice arrives in the middle of a line of an answer: it is a
	// message of its own, so it starts a line.
	m.print("half a line")
	notices, alert = []string{"validation-2 (copilot) is waiting for your answer."}, "validation-2 is waiting for you"
	m.apply(m.snapshot())
	if at := strings.Index(m.transcript, "validation-2"); !slices.Contains(m.starts, at) || !slices.Contains(m.lines, "validation-2 (copilot) is waiting for your answer."+strings.Repeat(" ", m.view.Width-len("validation-2 (copilot) is waiting for your answer."))) {
		t.Errorf("transcript = %q, starts %v; want the notice on a line of its own", m.transcript, m.starts)
	}
	m.apply(m.snapshot())
	if n := strings.Count(m.transcript, "is waiting for your answer"); n != 1 {
		t.Errorf("the notice is in the transcript %d times, want once", n)
	}

	// While a command runs, the alert is shown in place of "working",
	// above the input, which stays open.
	m.busy = true
	if v := m.View(); !strings.Contains(v, "validation-2 is waiting for you") || strings.Contains(v, "working") || !strings.Contains(v, promptText) {
		t.Errorf("busy view does not show the alert in place of working:\n%s", v)
	}
	alert = ""
	m.apply(m.snapshot())
	if v := m.View(); !strings.Contains(v, "working") {
		t.Errorf("with nothing waiting the busy view should say working:\n%s", v)
	}
}
