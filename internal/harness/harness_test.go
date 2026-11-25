package harness

import (
	"bytes"
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
)

func TestEnvKeepsOnlyDeclared(t *testing.T) {
	h := config.Harness{PassEnv: []string{"ANTHROPIC_API_KEY"}}
	environ := []string{
		"HOME=/home/u", "PATH=/bin", "LC_ALL=C", "XDG_CONFIG_HOME=/x",
		"ANTHROPIC_API_KEY=k", "AWS_SECRET_ACCESS_KEY=s", "OPENAI_API_KEY=o", "HOMEBREW=1",
	}
	got := Env(h, environ, "H2H_SESSION=a-1")
	want := []string{"HOME=/home/u", "PATH=/bin", "LC_ALL=C", "XDG_CONFIG_HOME=/x", "ANTHROPIC_API_KEY=k", "H2H_SESSION=a-1"}
	if !slices.Equal(got, want) {
		t.Errorf("Env() = %v\nwant %v", got, want)
	}
}

func TestAsk(t *testing.T) {
	h := config.Harness{Command: "sh", AskArgs: []string{"-c", `echo "got:$0"; echo "secret:$SECRET"`}}
	var out bytes.Buffer
	err := Ask(context.Background(), h, []string{"PATH=/bin:/usr/bin", "SECRET=x"}, t.TempDir(), "hello", &out, &out)
	if err != nil {
		t.Fatal(err)
	}
	if got := out.String(); !strings.Contains(got, "got:hello") || !strings.Contains(got, "secret:\n") {
		t.Errorf("Ask output = %q; want the prompt passed and SECRET withheld", got)
	}
}

func TestAskNeedsAskArgs(t *testing.T) {
	if err := Ask(context.Background(), config.Harness{Command: "sh"}, nil, t.TempDir(), "x", nil, nil); err == nil {
		t.Error("Ask without ask_args succeeded, want error")
	}
}

func TestVersion(t *testing.T) {
	environ := []string{"PATH=/bin:/usr/bin"}
	h := config.Harness{Command: "sh", VersionArgs: []string{"-c", "echo ' tool 1.2.3 '; echo more"}}
	if got, err := Version(context.Background(), h, environ); err != nil || got != "tool 1.2.3" {
		t.Errorf("Version() = %q, %v; want the first line", got, err)
	}
	h.VersionArgs = []string{"-c", "echo broken >&2; exit 3"}
	if _, err := Version(context.Background(), h, environ); err == nil || !strings.Contains(err.Error(), "broken") {
		t.Errorf("Version() of a failing program = %v; want an error carrying its output", err)
	}
	if _, err := Version(context.Background(), config.Harness{Command: "sh"}, environ); err == nil {
		t.Error("Version without version_args succeeded, want error")
	}
}

func TestNewer(t *testing.T) {
	tests := []struct {
		a, b string
		want bool
	}{
		{"2.1.340", "2.1.331 (Claude Code)", true},
		{"0.172.0", "codex-cli 0.172.0", false},
		{"1.9.0", "1.10.0", false},
		{"1.10.0", "1.9.0", true},
		{"2.0", "1.9.9", true},
		{"1.2", "1.2.0", false},
		{"1.2.1", "1.2", true},
		{"latest", "1.0.0", false},
		{"1.0.0", "", false},
	}
	for _, tt := range tests {
		if got := Newer(tt.a, tt.b); got != tt.want {
			t.Errorf("Newer(%q, %q) = %v, want %v", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestLatestAndInstall(t *testing.T) {
	ctx, environ := context.Background(), []string{"PATH=/bin:/usr/bin", "KEPT=yes"}
	h := config.Harness{
		LatestCommand:  []string{"sh", "-c", "echo 1.4.0; echo ignored"},
		InstallCommand: []string{"sh", "-c", "echo installing with $KEPT"},
	}
	if got, err := Latest(ctx, h, environ); err != nil || got != "1.4.0" {
		t.Errorf("Latest() = %q, %v; want 1.4.0", got, err)
	}
	var out bytes.Buffer
	if err := Install(ctx, h, environ, &out, &out); err != nil || !strings.Contains(out.String(), "installing with yes") {
		t.Errorf("Install() = %v, output %q; want it run with the user's environment", err, out.String())
	}
	h.InstallCommand = []string{"sh", "-c", "exit 4"}
	if err := Install(ctx, h, environ, &out, &out); err == nil {
		t.Error("Install of a failing command succeeded, want error")
	}
	if _, err := Latest(ctx, config.Harness{}, environ); err == nil {
		t.Error("Latest without latest_command succeeded, want error")
	}
	if err := Install(ctx, config.Harness{}, environ, &out, &out); err == nil {
		t.Error("Install without install_command succeeded, want error")
	}
}

func TestContext(t *testing.T) {
	h := config.Harness{
		ContextArgs: []string{"-c", "developer_instructions={roles}"},
		ContextEnv:  []string{"DIRS=/x,{roles_dir}"},
	}
	args, env := Context(h, "<r/>", "/state/roles")
	if want := []string{"-c", "developer_instructions=<r/>"}; !slices.Equal(args, want) {
		t.Errorf("args = %q, want %q", args, want)
	}
	if want := []string{"DIRS=/x,/state/roles"}; !slices.Equal(env, want) {
		t.Errorf("env = %q, want %q", env, want)
	}
	if args, env := Context(config.Harness{}, "<r/>", "/d"); args != nil || env != nil {
		t.Errorf("no context settings gave %q, %q", args, env)
	}
}

func TestAskStream(t *testing.T) {
	// The three shapes the shipped harnesses print: parts of a message
	// among other events, parts under another key, and whole messages.
	tests := []struct {
		name  string
		h     config.Harness
		lines string
		want  string
	}{
		{
			"parts of a message",
			config.Harness{StreamText: "event.delta.text"},
			`{"type":"system","subtype":"init"}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"thinking_delta","thinking":"hm"}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"one\n"}}}
{"type":"stream_event","event":{"type":"content_block_delta","delta":{"type":"text_delta","text":"two"}}}
{"type":"result","result":"one\ntwo"}`,
			"one\ntwo",
		},
		{
			"parts under another key",
			config.Harness{StreamText: "data.deltaContent"},
			`{"type":"assistant.message_delta","data":{"deltaContent":"one "}}
{"type":"assistant.message_delta","data":{"deltaContent":"two"}}
{"type":"assistant.message","data":{"content":"one two"}}`,
			"one two",
		},
		{
			"whole messages, only of one kind",
			config.Harness{StreamText: "item.text", StreamWhen: "item.type=agent_message", StreamWhole: true},
			`{"type":"item.completed","item":{"type":"reasoning","text":"thinking it over"}}
{"type":"item.completed","item":{"type":"agent_message","text":"first"}}
{"type":"item.completed","item":{"type":"error","message":"a warning"}}
{"type":"item.completed","item":{"type":"agent_message","text":"second"}}`,
			"first\nsecond\n",
		},
		{
			"whole parts, marked by the type of the line",
			config.Harness{StreamText: "part.text", StreamWhen: "type=text", StreamWhole: true},
			`{"type":"step_start","part":{"type":"step-start"}}
{"type":"text","part":{"type":"text","text":"OK"}}
{"type":"step_finish","part":{"type":"step-finish","reason":"stop"}}`,
			"OK\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tt.h.Command = "sh"
			tt.h.StreamArgs = []string{"-c", `printf '%s\n' "$LINES_OUT"; echo "asked: $0" >&2`}
			var out, errOut bytes.Buffer
			environ := []string{"PATH=/bin:/usr/bin", "LINES_OUT=" + tt.lines}
			tt.h.PassEnv = []string{"LINES_OUT"}
			if err := AskStream(context.Background(), tt.h, environ, t.TempDir(), "the prompt", &out, &errOut); err != nil {
				t.Fatal(err)
			}
			if out.String() != tt.want {
				t.Errorf("answer = %q, want %q", out.String(), tt.want)
			}
			if !strings.Contains(errOut.String(), "asked: the prompt") {
				t.Errorf("the prompt was not passed as the last argument: %q", errOut.String())
			}
		})
	}

	// What is not JSON is the harness saying something went wrong.
	h := config.Harness{Command: "sh", StreamArgs: []string{"-c", "echo not logged in; exit 1"}, StreamText: "x"}
	var out, errOut bytes.Buffer
	if err := AskStream(context.Background(), h, []string{"PATH=/bin:/usr/bin"}, t.TempDir(), "p", &out, &errOut); err == nil || !strings.Contains(errOut.String(), "not logged in") {
		t.Errorf("a failing run = %v, stderr %q; want an error and its reason", err, errOut.String())
	}
	// Without stream_args the answer comes from ask_args, whole.
	plain := config.Harness{Command: "sh", AskArgs: []string{"-c", "echo plain answer"}}
	out.Reset()
	if err := AskStream(context.Background(), plain, []string{"PATH=/bin:/usr/bin"}, t.TempDir(), "p", &out, &errOut); err != nil || out.String() != "plain answer\n" {
		t.Errorf("without stream_args: %q, %v", out.String(), err)
	}
}

func TestAskStreamPassesOnWhyTheHarnessFailed(t *testing.T) {
	tests := []struct {
		name, lines, want string
	}{
		{"a result marked as an error", `{"type":"system"}
{"type":"result","subtype":"error","is_error":true,"result":"You have hit your 5-hour limit. It resets at 4:10pm."}`, "You have hit your 5-hour limit. It resets at 4:10pm."},
		{"an error event", `{"type":"error","message":"Not logged in. Run codex login."}`, "Not logged in. Run codex login."},
		{"an error item", `{"type":"item.completed","item":{"type":"error","message":"usage limit reached"}}`, "usage limit reached"},
		{"an error object", `{"type":"session.failed","error":{"message":"quota exceeded","code":429}}`, "quota exceeded"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := config.Harness{Command: "sh", StreamArgs: []string{"-c", `printf '%s\n' "$LINES_OUT"; exit 1`}, StreamText: "event.delta.text", PassEnv: []string{"LINES_OUT"}}
			var out, errOut bytes.Buffer
			err := AskStream(context.Background(), h, []string{"PATH=/bin:/usr/bin", "LINES_OUT=" + tt.lines}, t.TempDir(), "p", &out, &errOut)
			if err == nil || !strings.Contains(errOut.String(), tt.want) || out.Len() != 0 {
				t.Errorf("err %v, answer %q, reason %q; want the reason %q passed on", err, out.String(), errOut.String(), tt.want)
			}
		})
	}
	// A warning in a run that succeeds is not a reason for anything.
	h := config.Harness{Command: "sh", StreamArgs: []string{"-c", `printf '%s\n' "$LINES_OUT"`}, StreamText: "item.text", StreamWhen: "item.type=agent_message", PassEnv: []string{"LINES_OUT"}}
	var out, errOut bytes.Buffer
	lines := `{"type":"item.completed","item":{"type":"error","message":"a hook flag is enabled"}}
{"type":"item.completed","item":{"type":"agent_message","text":"done"}}`
	if err := AskStream(context.Background(), h, []string{"PATH=/bin:/usr/bin", "LINES_OUT=" + lines}, t.TempDir(), "p", &out, &errOut); err != nil || out.String() != "done" || errOut.Len() != 0 {
		t.Errorf("a successful run: %v, answer %q, beside %q", err, out.String(), errOut.String())
	}
}
