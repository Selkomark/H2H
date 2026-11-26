package cli

import (
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Selkomark/H2H/internal/session"
)

// heeded remembers which waits the console has told the user about, so each
// is told once. The console reads it from its own timer and after each
// command, so it is guarded.
type heeded struct {
	mu   sync.Mutex
	told map[string]time.Time // session ID: when the wait that was told began
}

// heed returns, for the console, a line for each harness that has started
// to wait for the user since it last looked, and a short line for the one
// that has waited longest, to show in place of "working" while a command
// runs. It returns nothing when no harness waits.
func (e *env) heed() (notices []string, alert string) {
	infos, err := session.List(e.root)
	if err != nil || e.heeded == nil {
		return nil, ""
	}
	e.heeded.mu.Lock()
	defer e.heeded.mu.Unlock()
	if e.heeded.told == nil {
		e.heeded.told = map[string]time.Time{}
	}
	var first session.Info
	waiting := map[string]bool{}
	for _, i := range infos {
		a := i.Attention
		if i.State != session.Running || a.Kind == session.NoAttention {
			continue
		}
		waiting[i.ID] = true
		if first.ID == "" || a.Since.Before(first.Attention.Since) {
			first = i
		}
		// A wait the hooks confirm after the silence was told is told
		// again, since it now says what the harness waits for. So is a
		// change between a request not taken and silence.
		key := a.Since.Add(time.Duration(a.Kind) * time.Nanosecond)
		if told, ok := e.heeded.told[i.ID]; ok && told.Equal(key) {
			continue
		}
		e.heeded.told[i.ID] = key
		notices = append(notices, e.waitNotice(i))
	}
	// A harness that carries on may wait again later, and is told again.
	for id := range e.heeded.told {
		if !waiting[id] {
			delete(e.heeded.told, id)
		}
	}
	if first.ID != "" {
		verb, then := "is waiting for you", " · answer it with "
		switch first.Attention.Kind {
		case session.Quiet:
			verb = "may be waiting for you"
		case session.NotTaken:
			verb, then = "did not get your request", " · see why with "
		}
		alert = e.st.warn.Render(first.ID+" "+verb) + e.st.dim.Render(then) + e.st.accent.Render("attach "+first.ID)
	}
	return notices, alert
}

// waitNotice tells the user that a harness waits for them, what for if it
// said, and how to answer.
func (e *env) waitNotice(i session.Info) string {
	who := i.ID + " (" + i.Harness + ")"
	open := e.st.dim.Render(" Open it with ") + e.st.accent.Render("/attach "+i.ID) + e.st.dim.Render(".")
	if i.Attention.Kind == session.NotTaken {
		return e.st.warn.Render(fmt.Sprintf("%s did not take request %d: nothing reached its prompt. Something else on its screen, such as a question it asks when it starts, may have had the keys.", who, i.Attention.Request)) +
			e.st.dim.Render(" Look with ") + e.st.accent.Render("/attach "+i.ID) + e.st.dim.Render(", then ask again.")
	}
	if i.Attention.Kind == session.Quiet {
		return e.st.warn.Render(fmt.Sprintf("%s has been quiet for %s with a request open, so it may be waiting for your answer in its own terminal.", who, session.QuietAfter)) + open
	}
	text := who + " is waiting for your answer in its own terminal"
	if i.Attention.Why != "" {
		text += ": " + i.Attention.Why
	}
	if !strings.HasSuffix(text, ".") && !strings.HasSuffix(text, "?") && !strings.HasSuffix(text, "!") {
		text += "."
	}
	return e.st.warn.Render(text) + open
}
