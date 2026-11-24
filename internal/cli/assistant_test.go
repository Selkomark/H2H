package cli

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/Selkomark/H2H/internal/keeper"
	"github.com/Selkomark/H2H/internal/session"
)

func TestStreamWriterHidesCommandLines(t *testing.T) {
	tests := []struct {
		name   string
		pieces []string
		want   string
	}{
		{"plain text, in pieces", []string{"Hel", "lo the", "re.\nSecond", " line"}, "Hello there.\nSecond line\n"},
		{"a command line is left out", []string{"Starting.\n", "H2H-RUN: start ui\n", "Done.\n"}, "Starting.\nDone.\n"},
		{"a command line that arrives in pieces", []string{"Starting.\nH2", "H-RU", "N: ps\nDone.\n"}, "Starting.\nDone.\n"},
		{"a line that only starts like one", []string{"H2", "H runs it.\n"}, "H2H runs it.\n"},
		{"indented, and last without a line break", []string{"ok\n  H2H-RUN: ps"}, "ok\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var out bytes.Buffer
			w := &streamWriter{out: &out, hide: runMarker}
			for _, p := range tt.pieces {
				if n, err := w.Write([]byte(p)); n != len(p) || err != nil {
					t.Fatalf("Write = %d, %v", n, err)
				}
			}
			w.Close()
			if out.String() != tt.want {
				t.Errorf("shown %q, want %q", out.String(), tt.want)
			}
		})
	}
	// Text that has arrived is shown before the answer ends.
	var out bytes.Buffer
	w := &streamWriter{out: &out}
	_, _ = w.Write([]byte("first words"))
	if out.String() != "first words" {
		t.Errorf("before the end, shown %q; want the text so far", out.String())
	}
}

func TestRunLinesAndPermitted(t *testing.T) {
	reply := "I will start both.\nH2H-RUN: start backend\n  H2H-RUN: /ps\nH2H-RUN:\nAnd then report."
	if got := runLines(reply); len(got) != 2 || strings.Join(got[0], " ") != "start backend" || strings.Join(got[1], " ") != "ps" {
		t.Errorf("runLines = %v", got)
	}
	tests := []struct {
		call      string
		ok, asks  bool
		whyHas    string
		forReason string
	}{
		{"ps", true, false, "", ""},
		{"logs ui-1", true, false, "", ""},
		{"ask ui what is left?", true, false, "", ""},
		{"ask ui-1 add the login page", true, false, "", ""},
		{"sessions", true, false, "", ""},
		{"start ui", true, true, "", ""},
		{"stop ui-1", true, true, "", ""},
		{"rm ui-1", true, true, "", ""},
		{"install --update", true, true, "", ""},
		{"sessions rm all", true, true, "", ""},
		{"config", false, false, "/config", ""},
		{"config --manual", false, false, "/config", ""},
		{"attach ui-1", false, false, "not a command you can run", ""},
		{"resume 20261124-115037", false, false, "not a command you can run", ""},
		{"logs -f ui-1", false, false, "without -f", ""},
		{"exit", false, false, "not a command you can run", ""},
	}
	for _, tt := range tests {
		ok, asks, why := permitted(strings.Fields(tt.call))
		if ok != tt.ok || asks != tt.asks || !strings.Contains(why, tt.whyHas) {
			t.Errorf("permitted(%q) = %v, %v, %q; want %v, %v, reason with %q", tt.call, ok, asks, why, tt.ok, tt.asks, tt.whyHas)
		}
	}
	// Every command it may run exists.
	var names []string
	for _, c := range commands() {
		names = append(names, c.name)
	}
	for name := range assistantCommands {
		if !slices.Contains(names, name) {
			t.Errorf("the default harness may run %q, which is not a command", name)
		}
	}
}

func TestForHarness(t *testing.T) {
	if got := forHarness("\x1b[32mgreen\x1b[0m line\r\n\n\n\nnext\x07"); got != "green line\n\nnext" {
		t.Errorf("forHarness = %q", got)
	}
	if got := forHarness(""); got != "(it printed nothing)" {
		t.Errorf("forHarness of nothing = %q", got)
	}
	long := forHarness(strings.Repeat("x", maxResult+500) + "END")
	if !strings.HasPrefix(long, "(the start is left out)") || !strings.HasSuffix(long, "END") {
		t.Errorf("a long result keeps %d characters and not its end", len(long))
	}
}

// assistantHarness puts a stand-in for the default harness on PATH. Its
// replies are the files reply-1, reply-2 and so on in dir, one per run,
// and it records each prompt it was given as prompt-N.
func assistantHarness(t *testing.T, replies ...string) (conf, dir string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	for n, r := range replies {
		if err := os.WriteFile(filepath.Join(dir, "reply-"+string(rune('1'+n))), []byte(r), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\nn=$(cat " + dir + "/count 2>/dev/null || echo 0); n=$((n+1)); echo $n > " + dir + "/count\n" +
		"printf '%s' \"$2\" > " + dir + "/prompt-$n\ncat " + dir + "/reply-$n\n"
	if err := os.WriteFile(filepath.Join(dir, "h2h-helper"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return "default_harness = \"helper\"\n[harness.helper]\ncommand = \"h2h-helper\"\nask_args = [\"ask\"]\n[domain.ui]\nharness = \"helper\"\ndoes = \"The web frontend\"\n", dir
}

func TestConverseRunsCommandsForTheHarness(t *testing.T) {
	conf, dir := assistantHarness(t,
		"Let me look.\nH2H-RUN: ps\nH2H-RUN: config --manual\nH2H-RUN: stop ui-9\n",
		"Nothing is running, and the config is yours to change with /config.\n")
	e, out := doctorEnv(t, conf)
	ui := &scriptedUI{} // declines what it is asked
	e.ui = ui

	if err := e.converse(context.Background(), "what is running?"); err != nil {
		t.Fatalf("converse: %v\n%s", err, out)
	}
	shown := out.String()
	for _, want := range []string{"Let me look.", "> /ps", "No harnesses running", "looking at the results", "Nothing is running"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the user is not shown %q:\n%s", want, shown)
		}
	}
	if strings.Contains(shown, runMarker) || strings.Contains(shown, "> /config") || strings.Contains(shown, "> /stop") {
		t.Errorf("the user is shown a command line meant for H2H, or a command that was not run:\n%s", shown)
	}
	if ui.asked != "helper wants to run:\n  /stop ui-9\nRun it?" {
		t.Errorf("the user was asked %q; want only the command that changes something", ui.asked)
	}
	second, err := os.ReadFile(filepath.Join(dir, "prompt-2"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Message from the user: what is running?", "Result of /ps:\nNo harnesses running", "Result of /config --manual: not run: you cannot change the config",
		"Result of /stop ui-9: not run: the user declined.", "The web frontend"} {
		if !strings.Contains(string(second), want) {
			t.Errorf("the second prompt lacks %q:\n%s", want, second)
		}
	}
}

func TestConverseRunsWhatTheUserAgreesTo(t *testing.T) {
	conf, dir := assistantHarness(t, "Stopping it.\nH2H-RUN: stop ui-9\n", "It was not there.\n")
	e, out := doctorEnv(t, conf)
	e.ui = &scriptedUI{yes: true}
	if err := e.converse(context.Background(), "stop ui-9"); err != nil {
		t.Fatal(err)
	}
	second, _ := os.ReadFile(filepath.Join(dir, "prompt-2"))
	if !strings.Contains(out.String(), "> /stop ui-9") || !strings.Contains(string(second), `Result of /stop ui-9:`) || !strings.Contains(string(second), `no session "ui-9"`) {
		t.Errorf("the command was not run, or its error not passed on:\n%s\n---\n%s", out, second)
	}
}

func TestConverseStopsAfterItsRounds(t *testing.T) {
	replies := make([]string, maxRounds+1)
	for n := range replies {
		replies[n] = "Again.\nH2H-RUN: ps\n"
	}
	conf, dir := assistantHarness(t, replies...)
	e, out := doctorEnv(t, conf)
	e.ui = &scriptedUI{}
	if err := e.converse(context.Background(), "loop"); err != nil {
		t.Fatal(err)
	}
	count, _ := os.ReadFile(filepath.Join(dir, "count"))
	if strings.TrimSpace(string(count)) != "6" || !strings.Contains(out.String(), "Stopped after 6 rounds") {
		t.Errorf("ran %s times; want it stopped after %d:\n%s", count, maxRounds, out)
	}
}

func TestAskHarness(t *testing.T) {
	conf, dir := assistantHarness(t, "The login page is left.\n", "Plain answer.\n")
	e, out := doctorEnv(t, conf)
	e.ui = &scriptedUI{}
	if err := e.askHarness(context.Background(), []string{"ui", "what", "is", "left?"}); err != nil {
		t.Fatal(err)
	}
	prompt, _ := os.ReadFile(filepath.Join(dir, "prompt-1"))
	if !strings.Contains(out.String(), "No harness is running for ui, so helper, owner of ui answers in a fresh read-only run") || !strings.Contains(out.String(), "The login page is left.") {
		t.Errorf("output:\n%s", out)
	}
	for _, want := range []string{"owner of the ui domain", `yours="true"`, "Question: what is left?", "Do not change any file"} {
		if !strings.Contains(string(prompt), want) {
			t.Errorf("the prompt lacks %q:\n%s", want, prompt)
		}
	}
	// A harness by name is asked without a role.
	if err := e.askHarness(context.Background(), []string{"helper", "hello"}); err != nil {
		t.Fatal(err)
	}
	if prompt, _ := os.ReadFile(filepath.Join(dir, "prompt-2")); strings.Contains(string(prompt), "h2h_roles") {
		t.Errorf("a harness asked by name was given a role:\n%s", prompt)
	}
	for _, args := range [][]string{nil, {"ui"}, {"nobody", "hello"}} {
		if err := e.askHarness(context.Background(), args); err == nil {
			t.Errorf("ask %v succeeded, want an error", args)
		}
	}
}

func TestAskDelegatesToARunningHarness(t *testing.T) {
	e, out := doctorEnv(t, "default_harness = \"fake\"\n[harness.fake]\ncommand = \"sh\"\nargs = [\"-c\", \"cat > typed.txt\"]\nask_args = [\"-c\", \"echo fresh\"]\n[domain.ui]\nharness = \"fake\"\n")
	e.ui = &scriptedUI{}
	info, err := session.Create(e.root, "ui")
	if err != nil {
		t.Fatal(err)
	}
	info.Harness, info.Domain, info.Workspace = "fake", "ui", t.TempDir()
	info.Started = time.Now().Add(-time.Minute) // long enough ago to be ready for typing
	if err := session.Save(e.root, info); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- keeper.Run(e.root, info.ID) }()
	for deadline := time.Now().Add(5 * time.Second); ; time.Sleep(20 * time.Millisecond) {
		if now, _ := session.Load(e.root, info.ID); now.State == session.Running {
			info = now
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the session did not start")
		}
	}
	defer func() {
		_ = syscall.Kill(-info.HarnessPID, syscall.SIGKILL)
		<-done
	}()

	// The harness in the session answers once it has been asked.
	dir, err := session.ReportDir(e.root, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir) // it lives under /tmp, outside the test's own directory
	go func() {
		for {
			if _, err := os.Stat(filepath.Join(dir, "1.asked")); err == nil {
				break
			}
			time.Sleep(20 * time.Millisecond)
		}
		env := map[string]string{"H2H_ROOT": e.root, "H2H_SESSION": info.ID}
		_ = report([]string{"1"}, strings.NewReader("The login page is left.\n"), func(k string) string { return env[k] })
	}()

	// Asked by its domain, the running harness gets the request, not a fresh run.
	if err := e.askHarness(context.Background(), []string{"ui", "what", "is", "left?"}); err != nil {
		t.Fatalf("ask: %v\n%s", err, out)
	}
	if !strings.Contains(out.String(), "Asked ui-1 in its own session") || !strings.Contains(out.String(), "The login page is left.") || strings.Contains(out.String(), "fresh") {
		t.Errorf("want the running harness asked and its report shown:\n%s", out)
	}
	typed, err := os.ReadFile(filepath.Join(info.Workspace, "typed.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Request 1", "what is left?", "h2h-report 1 <<'H2H_ANSWER'"} {
		if !strings.Contains(string(typed), want) {
			t.Errorf("what was typed into the session lacks %q:\n%q", want, typed)
		}
	}

	// A report is only taken for a request that was made, from a session.
	env := map[string]string{"H2H_ROOT": e.root, "H2H_SESSION": info.ID}
	getenv := func(k string) string { return env[k] }
	for _, args := range [][]string{{"7", "answer"}, {"x", "answer"}, nil, {"1", " "}} {
		if err := report(args, strings.NewReader(""), getenv); err == nil {
			t.Errorf("report %q succeeded, want it refused", args)
		}
	}
	if err := report([]string{"1", "x"}, strings.NewReader(""), func(string) string { return "" }); err == nil {
		t.Error("a report from outside a session succeeded")
	}
}

func TestAskDoesNotTypeIntoAHarnessThatDoesNotTrustTheProject(t *testing.T) {
	t.Setenv("HOME", t.TempDir()) // the harness has recorded no trusted folder
	e, _ := doctorEnv(t, "default_harness = \"fake\"\n[harness.fake]\ncommand = \"sh\"\n"+
		"trust_file = \"~/fake.json\"\ntrust_key = [\"trustedFolders\"]\ntrust_value = \"{project}\"\n")
	info := session.Info{ID: "fake-1", Harness: "fake", State: session.Running, Started: time.Now().Add(-time.Minute)}
	err := e.askRunning(context.Background(), info, "anything")
	if err == nil || !strings.Contains(err.Error(), "has not been told to trust this project") || !strings.Contains(err.Error(), "attach fake-1") {
		t.Errorf("askRunning = %v; want it refused, pointing at attach", err)
	}
	if dir, derr := session.ReportDir(e.root, "fake-1"); derr == nil {
		if entries, _ := os.ReadDir(dir); len(entries) != 0 {
			t.Errorf("a request was made although nothing could be typed: %v", entries)
		}
		_ = os.RemoveAll(dir)
	}
}
