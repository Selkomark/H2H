package cli

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/session"
)

func TestConfigMap(t *testing.T) {
	cfg, err := config.Parse(`
default_harness = "claude"

[harness.claude]
command = "claude"
ask_args = ["-p"]
pass_env = ["ANTHROPIC_API_KEY"]
context_args = ["--append-system-prompt", "{roles}"]

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
    │   ├── roles     --append-system-prompt {roles}
    │   ├── env       basic + ANTHROPIC_API_KEY
    │   ╰── sessions
    │       ╰── ● ui-1 claude, running
    ├── codex
    │   ├── session   codex --full-auto
    │   ├── question  codex <prompt>
    │   ├── roles     not passed (no context_args or context_env)
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

func TestDomainDetailIsTwoTables(t *testing.T) {
	cfg, err := config.Parse(`
default_harness = "claude"
[harness.claude]
command = "claude"
context_args = ["--append-system-prompt", "{roles}"]
[harness.codex]
command = "codex"
[domain.api]
harness = "codex"
[domain.ui]
harness = "claude"
does = "The web frontend, which is a long description that has to wrap when the box it is shown in is narrow"
paths = ["web/**"]
depends_on = ["api"]
  [[domain.ui.subagent]]
  role = "coder"
  does = "Writes the components"
  [[domain.ui.subagent]]
  role = "reviewer"
`)
	if err != nil {
		t.Fatal(err)
	}
	infos := []session.Info{{ID: "ui-1", Harness: "claude", Domain: "ui", State: session.Starting, Started: time.Now()}}
	g := mapGraph(cfg, infos, plainStyles())
	var ui, api console.Node
	for _, n := range g.Nodes {
		if n.ID == "ui" {
			ui = n
		}
		if n.ID == "api" {
			api = n
		}
	}

	// First, what the domain is for and who does what in it.
	first := ui.Detail(100)
	for _, want := range []string{"The web frontend", "claude is in charge of this domain and hands the work to 2 sub-agents:", "Sub-agent", "Its job", "coder", "Writes the components", "reviewer", "No job written down for it"} {
		if !strings.Contains(first, want) {
			t.Errorf("the first part lacks %q:\n%s", want, first)
		}
	}
	for _, not := range []string{"web/**", "append-system-prompt", "permission", "ui-1"} {
		if strings.Contains(first, not) {
			t.Errorf("the first part shows %q, which belongs behind Show more:\n%s", not, first)
		}
	}
	// Then the rest, on request, as questions with answers in plain words.
	more := ui.More(100)
	for _, want := range []string{"More about ui", "Is it running?", "Yes, as ui-1", "/attach ui-1", "Which files does it work on?", "web/**",
		"When can it start?", "After api has done its work", "Where does its work go?", "No other domain waits for it",
		"How is claude started?", "With the command: claude", "Does it know its role?", "Yes.", "What if it needs permission?", "asks in its own terminal"} {
		if !strings.Contains(more, want) {
			t.Errorf("the second part lacks %q:\n%s", want, more)
		}
	}
	// A narrow box wraps long text instead of running off its edge.
	for _, part := range []string{ui.Detail(60), ui.More(60)} {
		for _, line := range strings.Split(part, "\n") {
			if lipgloss.Width(line) > 60 {
				t.Errorf("a row is %d columns in a box of 60: %q", lipgloss.Width(line), line)
			}
		}
	}
	// Wrapping loses no words, wherever the lines break.
	for _, word := range strings.Fields("The web frontend, which is a long description that has to wrap when the box it is shown in is narrow") {
		if !strings.Contains(ui.Detail(60), word) {
			t.Errorf("the wrapped text lost %q:\n%s", word, ui.Detail(60))
		}
	}
	// A domain with no sub-agents and nothing before it says so plainly.
	if d := api.Detail(100); !strings.Contains(d, "codex is in charge of this domain and does the work itself. It has no sub-agents.") || strings.Contains(d, "Sub-agent │") {
		t.Errorf("a domain with no sub-agents:\n%s", d)
	}
	if m := api.More(100); !strings.Contains(m, "No. Start it with /start api") || !strings.Contains(m, "Right away. It does not wait for another domain") ||
		!strings.Contains(m, "To ui, which waits for it") || !strings.Contains(m, "No files are assigned to it") || !strings.Contains(m, "No. codex has no way set up to be told") {
		t.Errorf("the setup of a domain that starts the flow:\n%s", m)
	}
}
