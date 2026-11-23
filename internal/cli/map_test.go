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
context_args = ["--append-system-prompt"]

[harness.codex]
command = "codex"
args = ["--full-auto"]

[domain.ui]
harness = "claude"
does = "Web frontend"
paths = ["web/**"]
  [[domain.ui.subagent]]
  role = "coder"
  does = "Writes the code"
  [[domain.ui.subagent]]
  role = "reviewer"

[domain.backend]
harness = "codex"

[domain.e2e]
harness = "codex"
depends_on = ["ui", "backend"]

[domain.finance]
harness = "claude"
`)
	if err != nil {
		t.Fatal(err)
	}
	infos := []session.Info{
		{ID: "ui-1", Harness: "claude", Domain: "ui", State: session.Running, Started: time.Now()},
		{ID: "gone-1", Harness: "gone", State: session.Running, Started: time.Now()},
	}
	got := configMap("~/app", cfg, infos, plainStyles())
	want := `H2H ~/app
├── Domains
│   ├── backend owned by codex
│   ├── e2e owned by codex
│   ├── finance owned by claude
│   ╰── ui owned by claude
│       ├── does      Web frontend
│       ├── paths     web/**
│       ├── sub-agents
│       │   ├── coder Writes the code
│       │   ╰── reviewer
│       ╰── sessions
│           ╰── ● ui-1 claude, running
├── Flow
│   ├── backend + ui → e2e
│   ╰── finance independent, runs in parallel
╰── Harnesses
    ├── claude (default: answers questions typed in the console)
    │   ├── session   claude
    │   ├── question  claude -p <prompt>
    │   ├── roles     --append-system-prompt <roles as XML>
    │   ├── env       basic + ANTHROPIC_API_KEY
    │   ╰── sessions
    │       ╰── ● ui-1 claude, running
    ├── codex
    │   ├── session   codex --full-auto
    │   ├── question  codex <prompt>
    │   ├── roles     not passed (no context_args)
    │   ├── env       basic environment only
    │   ╰── sessions  none
    ╰── not in config
        ╰── ● gone-1 gone, running
`
	if trimLines(got) != trimLines(want) {
		t.Errorf("configMap() =\n%s\nwant\n%s", got, want)
	}
}

func TestConfigMapWithoutDomains(t *testing.T) {
	cfg, err := config.Parse(config.Template())
	if err != nil {
		t.Fatal(err)
	}
	got := configMap("~/app", cfg, nil, plainStyles())
	if !strings.Contains(got, "Domains none yet") || strings.Contains(got, "Flow") {
		t.Errorf("configMap() without domains =\n%s", got)
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
