package config

import (
	"os"
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
	if got, want := strings.Join(c.Names(), ","), "claude,codex,copilot"; got != want {
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
