package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

// hookInput is a permission request as Claude Code and Codex send it.
func hookInput(tool string, input map[string]any) string {
	data, _ := json.Marshal(map[string]any{"hook_event_name": "PermissionRequest", "tool_name": tool, "tool_input": input})
	return string(data)
}

// decisionOf reads the answer a hook would pass back.
func decisionOf(t *testing.T, out string) (behavior, message string) {
	t.Helper()
	var d struct {
		HookSpecificOutput struct {
			HookEventName string `json:"hookEventName"`
			Decision      struct {
				Behavior string `json:"behavior"`
				Message  string `json:"message"`
			} `json:"decision"`
		} `json:"hookSpecificOutput"`
	}
	if err := json.Unmarshal([]byte(out), &d); err != nil || d.HookSpecificOutput.HookEventName != "PermissionRequest" {
		t.Fatalf("not a permission answer: %q (%v)", out, err)
	}
	return d.HookSpecificOutput.Decision.Behavior, d.HookSpecificOutput.Decision.Message
}

func TestPermitSubject(t *testing.T) {
	tests := []struct {
		input       string
		tool, wants string
	}{
		{hookInput("Bash", map[string]any{"command": "git status --short", "description": "check"}), "Bash", "git status --short"},
		{hookInput("Edit", map[string]any{"file_path": "web/app.tsx", "old_string": "a"}), "Edit", "Edit web/app.tsx"},
		{hookInput("WebFetch", map[string]any{"url": "https://example.com"}), "WebFetch", "WebFetch https://example.com"},
		{hookInput("Task", map[string]any{}), "Task", "Task"},
		{"not json", "", ""},
		{`{"tool_input":{"command":"ls"}}`, "", ""},
	}
	for _, tt := range tests {
		if tool, subject := permitSubject([]byte(tt.input)); tool != tt.tool || subject != tt.wants {
			t.Errorf("permitSubject(%s) = %q, %q; want %q, %q", tt.input, tool, subject, tt.tool, tt.wants)
		}
	}
}

func TestPermitAnswersFromTheRules(t *testing.T) {
	conf := "default_harness = \"x\"\n[harness.x]\ncommand = \"sh\"\n[permissions]\nallow = [\"git status*\"]\ndeny = [\"git push*\"]\n"
	e, _ := doctorEnv(t, conf)
	env := map[string]string{"H2H_ROOT": e.root, "H2H_SESSION": "x-1"}
	getenv := func(k string) string { return env[k] }
	dir, err := session.PermitDir(e.root, "x-1")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)

	ask := func(command string) (string, string) {
		var out bytes.Buffer
		if err := permit(strings.NewReader(hookInput("Bash", map[string]any{"command": command})), &out, getenv); err != nil {
			t.Fatalf("permit %q: %v", command, err)
		}
		return decisionOf(t, out.String())
	}
	if b, _ := ask("git status --short"); b != "allow" {
		t.Errorf("an allowed command got %q", b)
	}
	if b, m := ask("git push origin main"); b != "deny" || !strings.Contains(m, "not allowed by the project's permission rules") {
		t.Errorf("a denied command got %q, %q", b, m)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("a request the rules settle was put in the queue: %v", entries)
	}

	// What no rule covers waits for the user, who is asked in the console.
	answer := func(choice string) {
		for {
			if r := e.pendingPermit(); r != nil {
				if !strings.Contains(r.Question, "x-1 wants to run this command:") || !strings.Contains(r.Question, "make deploy") || len(r.Options) != 3 {
					t.Errorf("the question is %q with %d options", r.Question, len(r.Options))
				}
				r.Answer(choice)
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	if err := session.Save(e.root, session.Info{ID: "x-1", Harness: "x", State: session.Running, KeeperPID: os.Getpid(), Started: time.Now()}); err != nil {
		if err := os.MkdirAll(session.Dir(e.root, "x-1"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := session.Save(e.root, session.Info{ID: "x-1", Harness: "x", State: session.Running, KeeperPID: os.Getpid(), Started: time.Now()}); err != nil {
			t.Fatal(err)
		}
	}
	go answer(answerOnce)
	if b, _ := ask("make deploy"); b != "allow" {
		t.Errorf("after the user allowed it once, got %q", b)
	}
	go answer(answerDeny)
	if b, m := ask("make deploy"); b != "deny" || !strings.Contains(m, "The user declined this") {
		t.Errorf("after the user declined, got %q, %q", b, m)
	}
	go answer("") // the user left the dialog
	if b, _ := ask("make deploy"); b != "deny" {
		t.Errorf("after the user left the dialog, got %q", b)
	}
	// Allowed for the rest of the session, it is not asked again.
	go answer(answerAlways)
	if b, _ := ask("make deploy"); b != "allow" {
		t.Errorf("after the user allowed it for the session, got %q", b)
	}
	if b, _ := ask("make deploy"); b != "allow" {
		t.Errorf("asked again for what was allowed for the session: %q", b)
	}
	if e.pendingPermit() != nil {
		t.Error("an answered request is still waiting")
	}
}

func TestPermitInAutoModeAllowsWhatIsNotDenied(t *testing.T) {
	e, _ := doctorEnv(t, "default_harness = \"x\"\n[harness.x]\ncommand = \"sh\"\n[permissions]\nmode = \"auto\"\ndeny = [\"git push*\"]\n")
	env := map[string]string{"H2H_ROOT": e.root, "H2H_SESSION": "x-1"}
	dir, _ := session.PermitDir(e.root, "x-1")
	defer os.RemoveAll(dir)
	for command, want := range map[string]string{"make deploy": "allow", "git push --force": "deny"} {
		var out bytes.Buffer
		if err := permit(strings.NewReader(hookInput("Bash", map[string]any{"command": command})), &out, func(k string) string { return env[k] }); err != nil {
			t.Fatal(err)
		}
		if b, _ := decisionOf(t, out.String()); b != want {
			t.Errorf("auto mode: %q got %q, want %q", command, b, want)
		}
	}
}

func TestPermitNeverAllowsWhatItCannotJudge(t *testing.T) {
	e, _ := doctorEnv(t, "default_harness = \"x\"\n[harness.x]\ncommand = \"sh\"\n")
	dir, _ := session.PermitDir(e.root, "x-1")
	defer os.RemoveAll(dir)
	var out bytes.Buffer
	inSession := func(k string) string { return map[string]string{"H2H_ROOT": e.root, "H2H_SESSION": "x-1"}[k] }
	if err := permit(strings.NewReader("not json"), &out, inSession); err == nil || out.Len() != 0 {
		t.Errorf("a request that cannot be read gave %q, %v; want an error and no answer", out.String(), err)
	}
	if err := permit(strings.NewReader(hookInput("Bash", map[string]any{"command": "ls"})), &out, func(string) string { return "" }); err == nil {
		t.Error("a request from outside a session was answered")
	}
	// A request nobody answers in time is refused, with what to do next.
	if got := awaitAnswer(dir, permitRequest{Subject: "ls", Until: time.Now().Add(300 * time.Millisecond)}); got != "" {
		t.Errorf("an unanswered request got %q", got)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("an unanswered request was left in the queue: %v", entries)
	}
}

func TestConfigPermissions(t *testing.T) {
	base := "default_harness = \"x\"\n[harness.x]\ncommand = \"sh\"\n"
	for conf, wantErr := range map[string]bool{
		"[permissions]\nmode = \"auto\"\n":                              false,
		"[permissions]\nmode = \"ask\"\nallow = [\"git *\"]\n":          false,
		"[permissions]\nmode = \"yes\"\n":                               true,
		"[permissions]\nallow = [\"\"]\n":                               true,
		"[harness.y]\ncommand = \"sh\"\npermit_args = [\"--hook\"]\n":   true,
		"[harness.y]\ncommand = \"sh\"\npermit_args = [\"{permit}\"]\n": false,
	} {
		e, _ := doctorEnv(t, base+conf)
		if _, err := os.Stat(e.root); err != nil {
			t.Fatal(err)
		}
		if _, err := loadConfig(e.root); (err != nil) != wantErr {
			t.Errorf("%q: error = %v, want error %v", conf, err, wantErr)
		}
	}
}

func TestIsReport(t *testing.T) {
	const path = "/proj/.h2h/sessions/ui-1/bin/h2h-report"
	tests := []struct {
		command string
		want    bool
	}{
		{"h2h-report 3 <<'H2H_ANSWER'\nThe login page is left.\nIt has two forms; one is done.\nH2H_ANSWER", true},
		{path + " 3 <<'H2H_ANSWER'\nanswer\nH2H_ANSWER\n", true},
		{"h2h-report 3 <<EOF\nanswer with $(dollars) and ; semicolons | inside\nEOF", true},
		{"h2h-report 3 'a short answer'", true},
		{"h2h-report 3 <<'H2H_ANSWER'\nanswer\nH2H_ANSWER\nrm -rf ~", false},
		{"h2h-report 3 x; rm -rf ~", false},
		{"h2h-report 3 x && git push", false},
		{"h2h-report 3 $(cat ~/.ssh/id_rsa)", false},
		{"h2h-report 3 <<'H2H_ANSWER'\nnever closed", false},
		{"h2h-report 3\nrm -rf ~", false},
		{"cat secrets | h2h-report 3", false},
		{"h2h-reporter 3 x", false},
		{"/elsewhere/h2h-report 3 x", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isReport(tt.command, path); got != tt.want {
			t.Errorf("isReport(%q) = %v, want %v", tt.command, got, tt.want)
		}
	}
}

func TestPortable(t *testing.T) {
	const root, work = "/home/u/code/app", "/home/u/code/app/.h2h/workspaces/ui-1"
	tests := []struct{ subject, want string }{
		{"Edit /home/u/code/app/.h2h/workspaces/ui-1/web/app.tsx", "Edit ./web/app.tsx"},
		{"Edit /home/u/code/app/web/app.tsx", "Edit {project}/web/app.tsx"},
		{"Edit /home/u/.zshrc", "Edit /home/u/.zshrc"},
		{"git -C /home/u/code/app status --short | head", "git -C {project} status --short | head"},
		{"cd /home/u/code/app/.h2h/workspaces/ui-1 && ls", "cd . && ls"},
		{"cat /home/u/code/app/.h2h/workspaces/ui-1/a /home/u/code/app/b", "cat ./a {project}/b"},
		{"ls /home/u/code/app-old /home/u/code/app.bak /home/u/code/app/.h2h/workspaces/ui-10/x", "ls /home/u/code/app-old /home/u/code/app.bak {project}/.h2h/workspaces/ui-10/x"},
		{"bun test --isolate", "bun test --isolate"},
		{`grep "x" '/home/u/code/app/.h2h/workspaces/ui-1'`, `grep "x" '.'`},
	}
	for _, tt := range tests {
		if got := portable(tt.subject, root, work); got != tt.want {
			t.Errorf("portable(%q) = %q, want %q", tt.subject, got, tt.want)
		}
	}
}

func TestPermitRulesDoNotDependOnWhereTheProjectIs(t *testing.T) {
	conf := "default_harness = \"x\"\n[harness.x]\ncommand = \"sh\"\n[permissions]\nallow = [\"Edit ./*\", \"git -C {project} status*\"]\ndeny = [\"Edit ./secrets/*\"]\n"
	e, _ := doctorEnv(t, conf)
	work := workspace.Path(e.root, "x-1")
	env := map[string]string{"H2H_ROOT": e.root, "H2H_SESSION": "x-1"}
	dir, _ := session.PermitDir(e.root, "x-1")
	defer os.RemoveAll(dir)
	ask := func(tool string, input map[string]any) string {
		var out bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- permit(strings.NewReader(hookInput(tool, input)), &out, func(k string) string { return env[k] })
		}()
		select {
		case err := <-done:
			if err != nil {
				t.Fatal(err)
			}
			b, _ := decisionOf(t, out.String())
			return b
		case <-time.After(700 * time.Millisecond):
			return "asked" // it went to the user, and is left to time out
		}
	}
	if got := ask("Edit", map[string]any{"file_path": work + "/web/app.tsx"}); got != "allow" {
		t.Errorf("an edit in the harness's working copy got %q", got)
	}
	if got := ask("Edit", map[string]any{"file_path": work + "/secrets/key.pem"}); got != "deny" {
		t.Errorf("an edit a rule denies got %q", got)
	}
	if got := ask("Bash", map[string]any{"command": "git -C " + e.root + " status --short"}); got != "allow" {
		t.Errorf("a command naming the project's folder got %q", got)
	}
	if got := ask("Edit", map[string]any{"file_path": e.root + "/web/app.tsx"}); got != "asked" {
		t.Errorf("an edit outside the working copy, in the main checkout, got %q", got)
	}
	if got := ask("Edit", map[string]any{"file_path": "/etc/hosts"}); got != "asked" {
		t.Errorf("an edit outside the project got %q", got)
	}
}
