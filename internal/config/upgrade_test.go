package config

import (
	"reflect"
	"strings"
	"testing"
)

// copied is a config as /config wrote it for an earlier H2H: whole copies
// of the shipped claude entry, with comments, a retired value, and a
// setting of the project's own.
const copied = `# The team for this project.
default_harness = "claude"

# Claude, as shipped.
[harness.claude]
command = "claude"
ask_args = ["--permission-mode", "manual", "--tools", "Read,Glob,Grep", "--strict-mcp-config", "-p"]
stream_args = ["--permission-mode", "manual", "--tools", "Read,Glob,Grep", "--strict-mcp-config", "-p",
  "--output-format", "stream-json", "--include-partial-messages", "--verbose"]
context_args = ["--append-system-prompt"]
pass_env = ["ANTHROPIC_API_KEY", "MY_PROXY_TOKEN"] # ours

[harness.mine]
command = "my-agent"
ask_args = ["--permission-mode", "plan", "-p"]

[domain.ui]
harness = "claude"
does = "The web frontend"
`

func TestUpgradeRemovesCopiesAndRetiredValues(t *testing.T) {
	next, changes := Upgrade(copied)
	want := `# The team for this project.
default_harness = "claude"

# Claude, as shipped.
[harness.claude]
command = "claude"
pass_env = ["ANTHROPIC_API_KEY", "MY_PROXY_TOKEN"] # ours

[harness.mine]
command = "my-agent"
ask_args = ["--permission-mode", "plan", "-p"]

[domain.ui]
harness = "claude"
does = "The web frontend"
`
	if next != want {
		t.Errorf("Upgrade =\n%s\nwant\n%s", next, want)
	}
	if len(changes) != 2 || !strings.Contains(changes[0], "claude.context_args") || !strings.Contains(changes[1], "claude (2)") {
		t.Errorf("changes = %q", changes)
	}

	// A config holding a retired value may not load at all: this one
	// does not, because context_args now needs {roles}. The upgrade makes it
	// load, and otherwise sets up the same as before.
	if _, err := Parse(copied); err == nil || !strings.Contains(err.Error(), "{roles}") {
		t.Fatalf("Parse(copied) = %v; want it rejected for the retired value", err)
	}
	after, err := Parse(next)
	if err != nil {
		t.Fatalf("the upgraded config does not parse: %v", err)
	}
	before, err := Parse(strings.Replace(copied, "context_args = [\"--append-system-prompt\"]\n", "", 1))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before.Harnesses, after.Harnesses) || !reflect.DeepEqual(before.Domains, after.Domains) {
		t.Errorf("the upgrade changed what the config sets up:\nbefore %+v\nafter  %+v", before.Harnesses, after.Harnesses)
	}
}

func TestUpgradeIsIdempotent(t *testing.T) {
	once, _ := Upgrade(copied)
	twice, changes := Upgrade(once)
	if twice != once || len(changes) != 0 {
		t.Errorf("a second upgrade changed %q:\n%s", changes, twice)
	}
	if text, changes := Upgrade(Template()); len(changes) != 1 || !strings.Contains(changes[0], "same as what H2H ships") {
		t.Errorf("upgrading the shipped config itself: %q\n%s", changes, text)
	}
}

func TestUpgradeLeavesAnotherProgramAlone(t *testing.T) {
	text := "default_harness = \"claude\"\n[harness.claude]\ncommand = \"my-claude\"\nask_args = [\"--permission-mode\", \"plan\", \"-p\"]\n"
	if next, changes := Upgrade(text); next != text || len(changes) != 0 {
		t.Errorf("an entry for another program was changed: %q\n%s", changes, next)
	}
}

func TestUpgradeLeavesAnUnreadableFileAlone(t *testing.T) {
	text := "default_harness = \n[harness.claude]\ncommand = \"claude\"\n"
	if next, changes := Upgrade(text); next != text || len(changes) != 0 {
		t.Errorf("a file that does not read was changed: %q", changes)
	}
}

// A project that copied the plan-mode arguments must not stay stuck with them.
func TestUpgradeDropsThePlanModeArguments(t *testing.T) {
	text := "default_harness = \"claude\"\n[harness.claude]\ncommand = \"claude\"\n" +
		"ask_args = [\"--permission-mode\", \"plan\", \"-p\"]\n" +
		"resume_args = [\"--permission-mode\", \"plan\", \"-p\", \"--output-format\", \"stream-json\", \"--include-partial-messages\", \"--verbose\", \"--resume\", \"{conversation}\"]\n"
	next, changes := Upgrade(text)
	if len(changes) == 0 || strings.Contains(next, "plan") {
		t.Errorf("Upgrade = %q, %q; want both lines gone", next, changes)
	}
}
