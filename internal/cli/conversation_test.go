package cli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/Selkomark/H2H/internal/console"
)

// talkingHarness puts a stand-in for a default harness that keeps
// conversations on PATH. Each run records its arguments as args-N and its
// prompt as prompt-N in dir, and prints reply-N, which holds JSON lines.
// A run asked to resume a conversation whose ID starts with "gone" fails
// as a harness does that no longer has it, and every run fails while dir
// holds a file named fail.
func talkingHarness(t *testing.T) (dir string) {
	t.Helper()
	dir = t.TempDir()
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := `#!/bin/sh
d=` + dir + `
n=$(cat $d/count 2>/dev/null || echo 0); n=$((n+1)); echo $n > $d/count
printf '%s\n' "$@" > $d/args-$n
for a in "$@"; do last=$a; done
printf '%s' "$last" > $d/prompt-$n
prev=
for a in "$@"; do
	if [ "$prev" = "--resume" ]; then
		case "$a" in gone*) echo "No conversation found with ID $a" >&2; exit 1;; esac
	fi
	prev=$a
done
if [ -f $d/fail ]; then echo "usage limit reached" >&2; exit 1; fi
cat $d/reply-$n 2>/dev/null
`
	if err := os.WriteFile(filepath.Join(dir, "h2h-talker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

// The two kinds of harness that keep conversations: one that takes the ID
// from H2H, and one that chooses its own and reports it.
const (
	namingConfig = `default_harness = "namer"
[harness.namer]
command = "h2h-talker"
stream_args = ["ask"]
stream_text = "text"
conversation_args = ["ask", "--new", "{conversation}"]
resume_args = ["ask", "--resume", "{conversation}"]
`
	reportingConfig = `default_harness = "reporter"
[harness.reporter]
command = "h2h-talker"
stream_args = ["ask"]
stream_text = "text"
conversation_key = "thread.id"
resume_args = ["ask", "--resume", "{conversation}"]
`
)

// replies writes what the stand-in prints on its runs, in order.
func replies(t *testing.T, dir string, texts ...string) {
	t.Helper()
	for n, text := range texts {
		line, err := json.Marshal(map[string]string{"text": text})
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, filepath.Join(dir, "reply-"+string(rune('1'+n))), string(line)+"\n")
	}
}

func writeFile(t *testing.T, path, text string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readRun(t *testing.T, dir, kind string, n int) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(dir, kind+"-"+string(rune('0'+n))))
	if err != nil {
		t.Fatalf("run %d of the harness left no %s: %v", n, kind, err)
	}
	return string(data)
}

// consoleEnv is an environment as the console gives a line: in a console
// session, with its conversation.
func consoleEnv(t *testing.T, conf string) *env {
	t.Helper()
	e, _ := doctorEnv(t, conf)
	e.ui = &scriptedUI{}
	e.here = &openSession{root: e.root, current: newConsoleSession(e.root, time.Now())}
	e.conv = e.here
	return e
}

var uuidRE = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`)

func TestAConversationGoesOn(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "OK, heron.", "You said heron.")
	e := consoleEnv(t, namingConfig)

	if err := e.converse(context.Background(), "Remember the word heron."); err != nil {
		t.Fatal(err)
	}
	first := strings.Split(readRun(t, dir, "args", 1), "\n")
	if first[1] != "--new" || !uuidRE.MatchString(first[2]) {
		t.Fatalf("the first run was not a new conversation with an ID H2H chose: %q", first)
	}
	id := first[2]
	if got := e.currentTalk(); got.ID != id || got.Harness != "namer" || got.Wiring == "" {
		t.Errorf("the session keeps %+v; want the conversation %s with namer", got, id)
	}
	if p := readRun(t, dir, "prompt", 1); !strings.Contains(p, "You are the assistant") || !strings.Contains(p, "Message from the user: Remember the word heron.") {
		t.Errorf("the first message is not told everything:\n%s", p)
	}

	if err := e.converse(context.Background(), "go ahead"); err != nil {
		t.Fatal(err)
	}
	if args := readRun(t, dir, "args", 2); args != "ask\n--resume\n"+id+"\n"+readRun(t, dir, "prompt", 2)+"\n" {
		t.Errorf("the second run did not continue the conversation:\n%s", args)
	}
	p := readRun(t, dir, "prompt", 2)
	if strings.Contains(p, "You are the assistant") || strings.Contains(p, "How the config is wired up") {
		t.Errorf("the second message repeats what the conversation already holds:\n%s", p)
	}
	if !strings.Contains(p, "Harnesses right now:") || !strings.Contains(p, "Message from the user: go ahead") {
		t.Errorf("the second message lacks the message or how things stand:\n%s", p)
	}
}

func TestCommandsInAConversationGoBackToIt(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "Let me look.\nH2H-RUN: ps\n", "Nothing is running.")
	e := consoleEnv(t, namingConfig)
	if err := e.converse(context.Background(), "what is running?"); err != nil {
		t.Fatal(err)
	}
	id := e.currentTalk().ID
	if args := readRun(t, dir, "args", 2); !strings.Contains(args, "--resume\n"+id+"\n") {
		t.Errorf("the results were not given to the same conversation:\n%s", args)
	}
	p := readRun(t, dir, "prompt", 2)
	if !strings.HasPrefix(p, "What H2H's commands printed:") || !strings.Contains(p, "Result of /ps:") || strings.Contains(p, "Message from the user") {
		t.Errorf("after a command the conversation is told only what it printed:\n%s", p)
	}
}

func TestResumingASessionContinuesItsConversation(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "OK.", "Heron.", "Heron again.")
	e := consoleEnv(t, namingConfig)
	if err := e.converse(context.Background(), "Remember heron."); err != nil {
		t.Fatal(err)
	}
	id := e.currentTalk().ID
	// The console saves the session when the line finishes.
	e.here.save(console.State{History: []string{"Remember heron."}})

	// A later console, started with h2h --resume.
	saved, err := loadConsoleSession(e.root, e.hereID())
	if err != nil {
		t.Fatal(err)
	}
	if saved.Talk == nil || saved.Talk.ID != id {
		t.Fatalf("the saved session holds %+v; want the conversation %s", saved.Talk, id)
	}
	later := *e
	later.here = &openSession{root: e.root, current: saved, saved: true}
	later.conv = later.here
	if err := later.converse(context.Background(), "What word?"); err != nil {
		t.Fatal(err)
	}
	if args := readRun(t, dir, "args", 2); !strings.Contains(args, "--resume\n"+id+"\n") {
		t.Errorf("the resumed session did not continue its conversation:\n%s", args)
	}

	// Switching to the session from inside another console does the same.
	other := *e
	other.here = &openSession{root: e.root, current: newConsoleSession(e.root, time.Now().Add(time.Hour))}
	other.conv = other.here
	other.ui = &scriptedUI{}
	if err := other.resume(context.Background(), []string{saved.ID}); err != nil {
		t.Fatal(err)
	}
	if err := other.converse(context.Background(), "And again?"); err != nil {
		t.Fatal(err)
	}
	if args := readRun(t, dir, "args", 3); !strings.Contains(args, "--resume\n"+id+"\n") {
		t.Errorf("switching to the session did not continue its conversation:\n%s", args)
	}
}

func TestClearStartsANewConversation(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "OK.", "A new start.")
	e := consoleEnv(t, namingConfig)
	var out strings.Builder
	e.out = &out
	if err := e.clearTalk(context.Background(), nil); err != nil || !strings.Contains(out.String(), "There is no conversation with namer yet") {
		t.Errorf("clear with no conversation = %v, %q", err, out.String())
	}
	if err := e.converse(context.Background(), "Remember heron."); err != nil {
		t.Fatal(err)
	}
	old := e.currentTalk().ID
	out.Reset()
	if err := e.clearTalk(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if want := "Cleared the conversation with namer. Your next message starts a new one.\n"; out.String() != want {
		t.Errorf("clear said %q, want %q", out.String(), want)
	}
	if err := e.converse(context.Background(), "What word?"); err != nil {
		t.Fatal(err)
	}
	args := strings.Split(readRun(t, dir, "args", 2), "\n")
	if args[1] != "--new" || args[2] == old {
		t.Errorf("after clear the run was %q; want a new conversation", args)
	}
	if !strings.Contains(readRun(t, dir, "prompt", 2), "You are the assistant") {
		t.Error("the new conversation is not told everything")
	}

	// Outside the console there is no conversation to clear.
	e.conv = nil
	if err := e.clearTalk(context.Background(), nil); err == nil {
		t.Error("clear from the shell succeeded, want an error")
	}
}

func TestAChangedDefaultHarnessStartsANewConversation(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "Hello.")
	e := consoleEnv(t, namingConfig)
	var out strings.Builder
	e.out = &out
	e.keepTalk(talk{Harness: "claude", ID: "11111111-1111-4111-8111-111111111111"})
	if err := e.converse(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "The default harness is now namer, so this starts a new conversation with it.") {
		t.Errorf("the change was not mentioned:\n%s", out.String())
	}
	if args := readRun(t, dir, "args", 1); !strings.Contains(args, "--new\n") {
		t.Errorf("the run did not start a new conversation:\n%s", args)
	}
	if got := e.currentTalk(); got.Harness != "namer" {
		t.Errorf("the session keeps %+v", got)
	}
}

func TestALostConversationIsReplaced(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "", "Starting over.")
	e := consoleEnv(t, namingConfig)
	var out strings.Builder
	e.out = &out
	e.keepTalk(talk{Harness: "namer", ID: "gone-1"})
	if err := e.converse(context.Background(), "hello"); err != nil {
		t.Fatalf("converse: %v\n%s", err, out.String())
	}
	if !strings.Contains(out.String(), "The earlier conversation with namer could not be continued, so this starts a new one.") || !strings.Contains(out.String(), "Starting over.") {
		t.Errorf("the replacement was not said, or not answered:\n%s", out.String())
	}
	if got := e.currentTalk().ID; got == "gone-1" || !uuidRE.MatchString(got) {
		t.Errorf("the session keeps %q; want the new conversation", got)
	}
}

func TestAFailedTurnKeepsTheConversation(t *testing.T) {
	dir := talkingHarness(t)
	writeFile(t, filepath.Join(dir, "fail"), "")
	e := consoleEnv(t, namingConfig)
	kept := talk{Harness: "namer", ID: "22222222-2222-4222-8222-222222222222"}
	e.keepTalk(kept)
	err := e.converse(context.Background(), "hello")
	if err == nil || !strings.Contains(err.Error(), "usage limit reached") {
		t.Errorf("converse = %v; want the harness's reason", err)
	}
	if got := e.currentTalk(); got.ID != kept.ID {
		t.Errorf("after a failure the session keeps %+v; want the conversation it had", got)
	}

	// A first turn that fails leaves no conversation behind.
	e.keepTalk(talk{})
	if err := e.converse(context.Background(), "hello"); err == nil {
		t.Fatal("converse succeeded while the harness fails")
	}
	if got := e.currentTalk(); got.ID != "" {
		t.Errorf("a failed first turn left %+v", got)
	}
}

func TestAnInterruptedTurn(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "never shown")
	e := consoleEnv(t, namingConfig)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := e.converse(ctx, "hello"); err == nil || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("converse = %v; want it interrupted", err)
	}
	if got := e.currentTalk(); got.ID != "" {
		t.Errorf("an interrupted first turn left %+v; the next message must start anew", got)
	}
	kept := talk{Harness: "namer", ID: "33333333-3333-4333-8333-333333333333"}
	e.keepTalk(kept)
	_ = e.converse(ctx, "hello")
	if got := e.currentTalk(); got.ID != kept.ID {
		t.Errorf("an interrupted later turn left %+v; want the conversation kept", got)
	}
}

func TestAHarnessThatReportsItsOwnID(t *testing.T) {
	dir := talkingHarness(t)
	writeFile(t, filepath.Join(dir, "reply-1"), `{"type":"thread.started","thread":{"id":"T-42"}}`+"\n"+`{"text":"OK."}`+"\n")
	writeFile(t, filepath.Join(dir, "reply-2"), `{"text":"Heron."}`+"\n")
	e := consoleEnv(t, reportingConfig)
	if err := e.converse(context.Background(), "Remember heron."); err != nil {
		t.Fatal(err)
	}
	if args := readRun(t, dir, "args", 1); strings.Contains(args, "--new") || strings.Contains(args, "--resume") {
		t.Errorf("the first run was given an ID:\n%s", args)
	}
	if got := e.currentTalk().ID; got != "T-42" {
		t.Fatalf("the session keeps %q; want the ID the harness reported", got)
	}
	if err := e.converse(context.Background(), "What word?"); err != nil {
		t.Fatal(err)
	}
	if args := readRun(t, dir, "args", 2); !strings.Contains(args, "--resume\nT-42\n") {
		t.Errorf("the second run did not continue T-42:\n%s", args)
	}
}

func TestPipedLinesShareAConversation(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "OK.", "Heron.")
	e, _ := doctorEnv(t, namingConfig)
	e.ui = &scriptedUI{}
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := w.WriteString("Remember heron.\nWhat word?\n"); err != nil {
		t.Fatal(err)
	}
	w.Close()
	e.in = r
	if err := e.script(); err != nil {
		t.Fatal(err)
	}
	id := strings.Split(readRun(t, dir, "args", 1), "\n")[2]
	if args := readRun(t, dir, "args", 2); !strings.Contains(args, "--resume\n"+id+"\n") {
		t.Errorf("the second piped line did not continue the conversation:\n%s", args)
	}
}

func TestAnAnswerThatEndsAfterClearDoesNotBringItBack(t *testing.T) {
	o := &openSession{}
	o.setTalk(talk{Harness: "claude", ID: "old"})
	asked := o.talk() // a message is sent, in the old conversation

	o.endTalk() // /clear, typed while it is answered
	o.setTalk(talk{Harness: "claude", ID: "old", Gen: asked.Gen})
	if got := o.talk(); got.ID != "" {
		t.Errorf("after /clear the session keeps %+v; want no conversation", got)
	}

	// The next message starts the new conversation, which is kept.
	next := o.talk()
	o.setTalk(talk{Harness: "claude", ID: "new", Gen: next.Gen})
	if got := o.talk(); got.ID != "new" {
		t.Errorf("the session keeps %+v; want the new conversation", got)
	}

	// Switching to another saved session also leaves the answer behind.
	asked = o.talk()
	o.switchTo(consoleSession{ID: "other"})
	o.setTalk(talk{Harness: "claude", ID: "new", Gen: asked.Gen})
	if got := o.talk(); got.ID != "" {
		t.Errorf("the session switched to keeps %+v; want its own, none", got)
	}
}
