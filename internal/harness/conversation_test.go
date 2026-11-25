package harness

import (
	"bytes"
	"context"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
)

// talker is a stand-in harness that prints each of its arguments as a line
// of JSON text, then an event that reports a conversation ID.
func talker() config.Harness {
	script := `for a in "$@"; do printf '{"text":"%s\\n"}\n' "$a"; done; printf '{"thread":{"id":"T-7"}}\n'`
	return config.Harness{Command: "sh", StreamArgs: []string{"-c", script, "sh"}, StreamText: "text"}
}

func TestTurn(t *testing.T) {
	run := func(h config.Harness, id string) (string, string) {
		t.Helper()
		var out, errs bytes.Buffer
		got, err := Turn(context.Background(), h, []string{"PATH=/bin:/usr/bin"}, t.TempDir(), id, "the prompt", &out, &errs)
		if err != nil {
			t.Fatalf("Turn: %v\n%s", err, errs.String())
		}
		return got, out.String()
	}
	h := talker()

	// A harness that cannot carry a conversation on answers once.
	if id, out := run(h, ""); id != "" || out != "the prompt\n" {
		t.Errorf("without resume_args: id %q, output %q", id, out)
	}

	// H2H chooses the ID of a new conversation, and passes it back to go on.
	named := h
	named.ConversationArgs = append(append([]string{}, h.StreamArgs...), "--new", "{conversation}")
	named.ResumeArgs = append(append([]string{}, h.StreamArgs...), "--resume", "{conversation}")
	id, out := run(named, "")
	if !uuidLike(id) || out != "--new\n"+id+"\nthe prompt\n" {
		t.Errorf("a new conversation: id %q, output %q", id, out)
	}
	if again, out := run(named, id); again != id || out != "--resume\n"+id+"\nthe prompt\n" {
		t.Errorf("going on: id %q, output %q", again, out)
	}
	if other, _ := run(named, ""); other == id {
		t.Error("two new conversations got the same ID")
	}

	// A harness that chooses its own ID reports it in its output.
	reporting := h
	reporting.ConversationKey = "thread.id"
	reporting.ResumeArgs = named.ResumeArgs
	if id, out := run(reporting, ""); id != "T-7" || out != "the prompt\n" {
		t.Errorf("a reported ID: id %q, output %q", id, out)
	}
	if id, out := run(reporting, "T-7"); id != "T-7" || out != "--resume\nT-7\nthe prompt\n" {
		t.Errorf("going on with a reported ID: id %q, output %q", id, out)
	}

	// The ID can be part of an argument, for a harness that wants a prefix.
	prefixed := named
	prefixed.ResumeArgs = append(append([]string{}, h.StreamArgs...), "--session", "ses_{conversation}")
	if _, out := run(prefixed, "abc"); out != "--session\nses_abc\nthe prompt\n" {
		t.Errorf("a prefixed ID: output %q", out)
	}
}

func uuidLike(s string) bool {
	if len(s) != 36 || s[14] != '4' {
		return false
	}
	for i, r := range s {
		dash := i == 8 || i == 13 || i == 18 || i == 23
		if dash != (r == '-') || (!dash && !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f')) {
			return false
		}
	}
	return true
}
