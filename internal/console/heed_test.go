package console

import (
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

	// A notice arrives in the middle of a line of an answer: it gets a
	// line of its own.
	m.print("half a line")
	notices, alert = []string{"validation-2 (copilot) is waiting for your answer."}, "validation-2 is waiting for you"
	m.apply(m.snapshot())
	if !strings.Contains(m.transcript, "half a line\nvalidation-2 (copilot) is waiting for your answer.\n") {
		t.Errorf("transcript = %q; want the notice on a line of its own", m.transcript)
	}
	m.apply(m.snapshot())
	if n := strings.Count(m.transcript, "is waiting for your answer"); n != 1 {
		t.Errorf("the notice is in the transcript %d times, want once", n)
	}

	// While a command runs, the alert is shown in place of "working".
	m.busy = true
	if v := m.View(); !strings.Contains(v, "validation-2 is waiting for you") || strings.Contains(v, "working") {
		t.Errorf("busy view does not show the alert in place of working:\n%s", v)
	}
	alert = ""
	m.apply(m.snapshot())
	if v := m.View(); !strings.Contains(v, "working") {
		t.Errorf("with nothing waiting the busy view should say working:\n%s", v)
	}
}
