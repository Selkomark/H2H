package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/session"
)

func TestConfigMap(t *testing.T) {
	cfg, err := config.Parse(`
default_harness = "claude"

[harness.claude]
command = "claude"
ask_args = ["-p"]
pass_env = ["ANTHROPIC_API_KEY"]

[harness.codex]
command = "codex"
args = ["--full-auto"]
`)
	if err != nil {
		t.Fatal(err)
	}
	infos := []session.Info{
		{ID: "claude-1", Harness: "claude", State: session.Running, Started: time.Now()},
		{ID: "gone-1", Harness: "gone", State: session.Running, Started: time.Now()},
	}
	got := configMap("~/app", cfg, infos, plainStyles())
	want := `H2H ~/app
├── claude (default: answers questions typed in the console)
│   ├── session   claude
│   ├── question  claude -p <prompt>
│   ├── env       basic + ANTHROPIC_API_KEY
│   ╰── sessions 
│       ╰── ● claude-1 running
├── codex
│   ├── session   codex --full-auto
│   ├── question  codex <prompt>
│   ├── env       basic environment only
│   ╰── sessions  none
╰── not in config
    ╰── ● gone-1 gone, running
`
	if trimLines(got) != trimLines(want) {
		t.Errorf("configMap() =\n%s\nwant\n%s", got, want)
	}
}

// trimLines drops trailing spaces, which the tree renderer pads with.
func trimLines(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " ")
	}
	return strings.Join(lines, "\n")
}
