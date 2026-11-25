package config

import (
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestShippedDefault(t *testing.T) {
	c, err := Parse(Template())
	if err != nil {
		t.Fatalf("Parse(Template()) failed: %v", err)
	}
	if c.DefaultHarness != "claude" {
		t.Errorf("DefaultHarness = %q, want %q", c.DefaultHarness, "claude")
	}
	if got, want := strings.Join(c.Names(), ","), "claude,codex,copilot,grok,opencode"; got != want {
		t.Errorf("Names() = %q, want %q", got, want)
	}
}

func TestParseRejects(t *testing.T) {
	tests := []struct {
		name, text, wantErr string
	}{
		{"no harness", `default_harness = "claude"`, "no harness"},
		{"default missing from pool", "default_harness = \"codex\"\n[harness.claude]\ncommand = \"claude\"", "not a configured harness"},
		{"default unset", "[harness.claude]\ncommand = \"claude\"", "default_harness is not set"},
		{"no command", "default_harness = \"claude\"\n[harness.claude]\nargs = []", "no command"},
		{"unknown key", "default_harness = \"claude\"\ncolour = 1\n[harness.claude]\ncommand = \"claude\"", "unknown setting: colour"},
		{"bad name", "default_harness = \"Bad_Name\"\n[harness.Bad_Name]\ncommand = \"x\"", "lower-case"},
		{"bad env", "default_harness = \"a\"\n[harness.a]\ncommand = \"x\"\npass_env = [\"A=B\"]", "not an environment variable"},
		{"syntax", "default_harness = ", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(tt.text)
			if err == nil {
				t.Fatalf("Parse(%q) succeeded, want error", tt.text)
			}
			if !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse(%q) error = %q, want it to contain %q", tt.text, err, tt.wantErr)
			}
		})
	}
}

func TestLookup(t *testing.T) {
	c, err := Parse("default_harness = \"codex\"\n[harness.codex]\ncommand = \"codex\"\n[harness.claude]\ncommand = \"claude\"")
	if err != nil {
		t.Fatal(err)
	}
	if name, h, err := c.Lookup(""); err != nil || name != "codex" || h.Command != "codex" {
		t.Errorf("Lookup(\"\") = %q, %+v, %v; want the default, codex", name, h, err)
	}
	if name, _, err := c.Lookup("claude"); err != nil || name != "claude" {
		t.Errorf("Lookup(claude) = %q, %v", name, err)
	}
	if _, _, err := c.Lookup("gemini"); err == nil {
		t.Error("Lookup(gemini) succeeded, want error")
	}
}

func TestLoad(t *testing.T) {
	root := t.TempDir()
	c, err := Load(root)
	if err != nil || c.DefaultHarness != "claude" {
		t.Fatalf("Load without a file = %+v, %v; want shipped defaults", c, err)
	}
	if err := os.WriteFile(Path(root), []byte("default_harness = \"x\"\n[harness.x]\ncommand = \"sh\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c, err = Load(root)
	if err != nil || c.DefaultHarness != "x" {
		t.Fatalf("Load with a file = %+v, %v; want default x", c, err)
	}
}

func TestUnconfigured(t *testing.T) {
	// A config written before a harness was added does not have it.
	c, err := Parse("default_harness = \"cc\"\n[harness.cc]\ncommand = \"claude\"\n[harness.codex]\ncommand = \"codex\"\n[harness.codex-low]\ncommand = \"codex\"\n")
	if err != nil {
		t.Fatal(err)
	}
	// claude is there under another name, so it is not offered again.
	if got, want := strings.Join(c.Unconfigured(), ","), "copilot,grok,opencode"; got != want {
		t.Errorf("Unconfigured() = %q, want %q", got, want)
	}
	if got := Shipped().Unconfigured(); len(got) != 0 {
		t.Errorf("the shipped config lacks its own harnesses: %v", got)
	}
}

func TestShippedEntryParsesBack(t *testing.T) {
	for _, name := range Shipped().Names() {
		entry := ShippedEntry(name)
		if !strings.HasPrefix(entry, "[harness."+name+"]\ncommand = ") {
			t.Errorf("ShippedEntry(%q) =\n%s", name, entry)
		}
		c, err := Parse("default_harness = \"" + name + "\"\n" + entry + "\n")
		if err != nil || !reflect.DeepEqual(c.Harnesses[name], Shipped().Harnesses[name]) {
			t.Errorf("the entry of %q does not parse back to the shipped harness: %v\n%s", name, err, entry)
		}
	}
	if got := ShippedEntry("nothing"); got != "" {
		t.Errorf("ShippedEntry of an unknown harness = %q", got)
	}
}

func TestLacksCountsAnEmptyListAsUnset(t *testing.T) {
	shipped := Harness{Command: "x", Args: []string{"--standalone"}}
	if got := (Harness{Command: "x", Args: []string{}}).Lacks(shipped); len(got) != 1 || got[0] != `args = ["--standalone"]` {
		t.Errorf("Lacks() = %q; want the shipped args offered", got)
	}
	if got := (Harness{Command: "x", Args: []string{}}).Lacks(Harness{Command: "x", Args: []string{}}); len(got) != 0 {
		t.Errorf("Lacks() of a setting H2H ships empty = %q", got)
	}
}

func TestProjectHarnessInheritsShippedSettings(t *testing.T) {
	// A project written before conversations existed: its claude entry
	// names a few settings and none of the conversation ones.
	text := "default_harness = \"claude\"\n" +
		"[harness.claude]\ncommand = \"claude\"\nargs = []\nask_args = [\"-p\"]\n" +
		"[harness.codex]\ncommand = \"codex\"\nask_args = [\"exec\"]\n" +
		"[harness.mine]\ncommand = \"my-agent\"\nask_args = [\"--once\"]\n"
	c, err := Parse(text)
	if err != nil {
		t.Fatal(err)
	}
	shipped := shippedHarnesses()

	claude := c.Harnesses["claude"]
	if !slices.Equal(claude.AskArgs, []string{"-p"}) {
		t.Errorf("claude ask_args = %q; a setting the file names must be kept", claude.AskArgs)
	}
	if claude.Args == nil || len(claude.Args) != 0 {
		t.Errorf("claude args = %q; an empty list the file names must stay empty", claude.Args)
	}
	if !slices.Equal(claude.ResumeArgs, shipped["claude"].ResumeArgs) || !claude.Remembers() {
		t.Errorf("claude resume_args = %q; want the shipped ones inherited", claude.ResumeArgs)
	}
	if !slices.Equal(claude.StreamArgs, shipped["claude"].StreamArgs) {
		t.Errorf("claude stream_args = %q; want the shipped ones inherited", claude.StreamArgs)
	}
	if codex := c.Harnesses["codex"]; codex.ConversationKey != shipped["codex"].ConversationKey || !codex.Remembers() {
		t.Errorf("codex conversation settings = %q, %q; want the shipped ones", codex.ConversationKey, codex.ResumeArgs)
	}
	if mine := c.Harnesses["mine"]; mine.Remembers() || len(mine.StreamArgs) > 0 {
		t.Errorf("a harness H2H does not ship inherited settings: %+v", mine)
	}
}

func TestInheritSkipsAnotherProgramUnderAShippedName(t *testing.T) {
	c, err := Parse("default_harness = \"claude\"\n[harness.claude]\ncommand = \"my-claude-wrapper\"\n")
	if err != nil {
		t.Fatal(err)
	}
	if h := c.Harnesses["claude"]; h.Remembers() || len(h.AskArgs) > 0 {
		t.Errorf("another program named claude inherited claude's settings: %+v", h)
	}
}

func TestConversationSettingsAreInheritedTogether(t *testing.T) {
	// A project that set its own way of naming conversations keeps it whole,
	// without the shipped conversation_key for the same harness.
	text := "default_harness = \"codex\"\n[harness.codex]\ncommand = \"codex\"\n" +
		"stream_args = [\"exec\", \"--json\"]\nstream_text = \"item.text\"\n" +
		"conversation_args = [\"exec\", \"--id\", \"{conversation}\"]\nresume_args = [\"exec\", \"resume\", \"{conversation}\"]\n"
	c, err := Parse(text)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if h := c.Harnesses["codex"]; h.ConversationKey != "" || !h.NamesConversation() {
		t.Errorf("codex = key %q, names %v; want the project's own settings only", h.ConversationKey, h.NamesConversation())
	}
}
