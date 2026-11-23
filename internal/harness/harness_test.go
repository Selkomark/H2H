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
