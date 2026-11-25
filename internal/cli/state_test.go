package cli

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/session"
)

// What each harness's hook gives h2h-state, as they sent it in the real
// checks recorded in docs/findings.md.
const (
	claudeNotice  = `{"session_id":"x","hook_event_name":"Notification","notification_type":"permission_prompt","message":"Claude needs your permission to use Write"}`
	copilotNotice = `{"sessionId":"x","hook_event_name":"Notification","notification_type":"permission_prompt","title":"Permission needed","message":"Edit file: /p/tests/test_e2e.py"}`
	copilotIdle   = `{"sessionId":"x","notification_type":"agent_idle","message":"Copilot is done"}`
	codexRequest  = `{"session_id":"x","hook_event_name":"PermissionRequest","tool_name":"Bash","tool_input":{"command":"touch /etc/x"}}`
)

func TestStateRecordsWaitsAndGoes(t *testing.T) {
	root, id, done := runningSession(t)
	t.Cleanup(func() {
		_ = session.ClearWait(root, id)
		e := &env{root: root, out: &bytes.Buffer{}, errw: &bytes.Buffer{}, ui: &scriptedUI{yes: true}, st: plainStyles()}
		_ = e.stop(context.Background(), []string{id})
		<-done
	})
	getenv := func(k string) string { return map[string]string{"H2H_ROOT": root, "H2H_SESSION": id}[k] }
	why := func() (session.AttentionKind, string) {
		info, err := session.Load(root, id)
		if err != nil {
			t.Fatal(err)
		}
		return info.Attention.Kind, info.Attention.Why
	}

	tests := []struct {
		name, input string
		args        []string
		kind        session.AttentionKind
		why         string
	}{
		{"claude's notification", claudeNotice, []string{"wait"}, session.Waiting, "Claude needs your permission to use Write"},
		{"copilot's notification", copilotNotice, []string{"wait", "permission_prompt", "elicitation_dialog"}, session.Waiting, "Permission needed: Edit file: /p/tests/test_e2e.py"},
		{"another kind of notification is left alone", copilotIdle, []string{"wait", "permission_prompt"}, session.NoAttention, ""},
		{"codex's permission request", codexRequest, []string{"wait"}, session.Waiting, "Permission needed: touch /etc/x"},
		{"input that is not JSON still waits", "not json", []string{"wait"}, session.Waiting, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_ = session.ClearWait(root, id)
			if err := state(tt.args, strings.NewReader(tt.input), getenv); err != nil {
				t.Fatal(err)
			}
			if kind, why := why(); kind != tt.kind || why != tt.why {
				t.Errorf("after %v: %v %q, want %v %q", tt.args, kind, why, tt.kind, tt.why)
			}
			if err := state([]string{"go"}, strings.NewReader(`{"hook_event_name":"PostToolUse"}`), getenv); err != nil {
				t.Fatal(err)
			}
			if kind, _ := why(); kind != session.NoAttention {
				t.Errorf("after go: %v, want no attention", kind)
			}
		})
	}

	if err := state([]string{"wait"}, strings.NewReader(""), func(string) string { return "" }); err == nil {
		t.Error("state outside an H2H session succeeded, want an error")
	}
	if err := state([]string{"maybe"}, strings.NewReader(""), getenv); err == nil {
		t.Error("an unknown action succeeded, want an error")
	}
}

func TestConsoleTellsOfAWaitOnceAndShowsItEverywhere(t *testing.T) {
	root, id, done := runningSession(t)
	out := &bytes.Buffer{}
	e := &env{root: root, out: out, errw: out, ui: &scriptedUI{yes: true}, st: plainStyles(), heeded: &heeded{}}
	t.Cleanup(func() {
		_ = session.ClearWait(root, id)
		_ = e.stop(context.Background(), []string{id})
		<-done
	})

	if notices, alert := e.heed(); len(notices) != 0 || alert != "" {
		t.Fatalf("nothing waits, but heed = %q, %q", notices, alert)
	}
	if err := session.SetWait(root, id, "Permission needed: Edit file: /p/a.txt"); err != nil {
		t.Fatal(err)
	}
	notices, alert := e.heed()
	want := id + " (fake) is waiting for your answer in its own terminal: Permission needed: Edit file: /p/a.txt. Open it with /attach " + id + "."
	if len(notices) != 1 || notices[0] != want {
		t.Errorf("notices = %q\nwant [%q]", notices, want)
	}
	if alert != id+" is waiting for you · answer it with attach "+id {
		t.Errorf("alert = %q", alert)
	}
	if notices, _ := e.heed(); len(notices) != 0 {
		t.Errorf("the same wait was told again: %q", notices)
	}

	// Everywhere a state is shown.
	if tab := e.sessionsView(); tab.Count != "1/1" || !tab.Attention {
		t.Errorf("Harnesses tab counts %q, attention %v; want 1/1 asking for the user", tab.Count, tab.Attention)
	}
	_ = e.ps(context.Background(), nil)
	if !strings.Contains(out.String(), "waiting for you") {
		t.Errorf("ps = %q, want the session shown waiting for you", out.String())
	}
	if rows := e.sessionsView().Rows; len(rows) != 1 || !strings.Contains(rows[0].Text, "waiting for you") {
		t.Errorf("Harnesses tab = %+v", rows)
	}

	// A request is not typed into a harness at a prompt.
	if err := e.askRunning(context.Background(), mustLoad(t, root, id), "hello"); err == nil || !strings.Contains(err.Error(), "would land in that prompt") {
		t.Errorf("ask while waiting = %v; want it refused", err)
	}

	// Once it carries on and stops again, it is told again.
	_ = session.ClearWait(root, id)
	if notices, alert := e.heed(); len(notices) != 0 || alert != "" {
		t.Errorf("after go: %q, %q; want nothing", notices, alert)
	}
	if tab := e.sessionsView(); tab.Count != "1/1" || tab.Attention {
		t.Errorf("after go, the Harnesses tab counts %q, attention %v; want 1/1 and none", tab.Count, tab.Attention)
	}
	if err := session.SetWait(root, id, ""); err != nil {
		t.Fatal(err)
	}
	if notices, _ := e.heed(); len(notices) != 1 || !strings.HasSuffix(notices[0], "in its own terminal. Open it with /attach "+id+".") {
		t.Errorf("second wait: %q", notices)
	}
}

func mustLoad(t *testing.T, root, id string) session.Info {
	t.Helper()
	info, err := session.Load(root, id)
	if err != nil {
		t.Fatal(err)
	}
	return info
}
