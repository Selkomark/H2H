package config

import (
	"slices"
	"strings"
	"testing"
)

const pool = "default_harness = \"claude\"\n[harness.claude]\ncommand = \"claude\"\n[harness.codex]\ncommand = \"codex\"\n"

func TestDomains(t *testing.T) {
	c, err := Parse(pool + `
[domain.ui]
harness = "claude"
does = "Web <frontend> & styles"
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
`)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.DomainNames(); !slices.Equal(got, []string{"backend", "e2e", "ui"}) {
		t.Errorf("DomainNames() = %v", got)
	}
	if got := c.Dependents("ui"); !slices.Equal(got, []string{"e2e"}) {
		t.Errorf("Dependents(ui) = %v, want [e2e]", got)
	}
	if got := c.Dependents("e2e"); got != nil {
		t.Errorf("Dependents(e2e) = %v, want none", got)
	}

	x := c.RolesXML("ui")
	for _, want := range []string{
		`<domain name="ui" harness="claude" yours="true">`,
		`<responsibility>Web &lt;frontend&gt; &amp; styles</responsibility>`,
		`<subagent role="coder">Writes the code</subagent>`,
		"<paths>\n      <path>web/**</path>",
		`You are the owner of the ui domain`,
	} {
		if !strings.Contains(x, want) {
			t.Errorf("RolesXML(ui) lacks %q:\n%s", want, x)
		}
	}
	if strings.Contains(c.RolesXML("ui"), "<subagents></subagents>") || strings.Contains(c.RolesXML("ui"), "<paths></paths>") {
		t.Errorf("RolesXML writes empty lists:\n%s", c.RolesXML("ui"))
	}
	if strings.Contains(c.RolesXML(""), "yours=") || strings.Contains(c.RolesXML(""), "You are the owner") {
		t.Errorf("RolesXML(\"\") marks a domain as yours:\n%s", c.RolesXML(""))
	}
}

func TestNoDomainsNoXML(t *testing.T) {
	c, err := Parse(pool)
	if err != nil {
		t.Fatal(err)
	}
	if x := c.RolesXML(""); x != "" {
		t.Errorf("RolesXML() = %q, want empty", x)
	}
}

func TestDomainRejects(t *testing.T) {
	tests := []struct{ name, text, wantErr string }{
		{"no harness", "[domain.ui]\ndoes = \"x\"", "has no harness"},
		{"unknown harness", "[domain.ui]\nharness = \"gemini\"", `harness "gemini" is not configured`},
		{"unknown dependency", "[domain.ui]\nharness = \"claude\"\ndepends_on = [\"api\"]", `depends on "api", which is not a domain`},
		{"self", "[domain.ui]\nharness = \"claude\"\ndepends_on = [\"ui\"]", "depends on itself"},
		{"cycle", "[domain.a]\nharness = \"claude\"\ndepends_on = [\"b\"]\n[domain.b]\nharness = \"claude\"\ndepends_on = [\"c\"]\n[domain.c]\nharness = \"claude\"\ndepends_on = [\"a\"]", "a -> b -> c -> a"},
		{"same name as harness", "[domain.codex]\nharness = \"claude\"", "same name as a harness"},
		{"bad name", "[domain.UI]\nharness = \"claude\"", "lower-case"},
		{"role missing", "[domain.ui]\nharness = \"claude\"\n[[domain.ui.subagent]]\ndoes = \"x\"", "no role"},
		{"role twice", "[domain.ui]\nharness = \"claude\"\n[[domain.ui.subagent]]\nrole = \"a\"\n[[domain.ui.subagent]]\nrole = \"a\"", "twice"},
		{"unknown key", "[domain.ui]\nharness = \"claude\"\nafter = [\"x\"]", "unknown setting: domain.ui.after"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse(pool + tt.text)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

func TestContextRejects(t *testing.T) {
	tests := []struct{ name, text, wantErr string }{
		{"args without placeholder", `context_args = ["--append-system-prompt"]`, "must contain {roles}"},
		{"env not NAME=value", `context_env = ["{roles_dir}"]`, "not NAME=value"},
		{"env without placeholder", `context_env = ["DIRS=/tmp"]`, "must contain {roles_dir}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse("default_harness = \"a\"\n[harness.a]\ncommand = \"a\"\n" + tt.text)
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("Parse() error = %v, want it to contain %q", err, tt.wantErr)
			}
		})
	}
}

// Every shipped harness must be able to receive its role. See the
// harness-parity skill.
func TestTrustSettings(t *testing.T) {
	base := "default_harness = \"a\"\n[harness.a]\ncommand = \"a\"\n"
	tests := []struct {
		name, settings string
		wantErr        bool
	}{
		{"none", "", false},
		{"complete", "trust_file = \"~/.a.json\"\ntrust_key = [\"projects\", \"{project}\"]\ntrust_value = \"true\"\n", false},
		{"placeholder in the value", "trust_file = \"~/.a.toml\"\ntrust_key = [\"folders\"]\ntrust_value = \"{project}\"\n", false},
		{"only the file", "trust_file = \"~/.a.json\"\n", true},
		{"unknown format", "trust_file = \"~/.a.yaml\"\ntrust_key = [\"{project}\"]\ntrust_value = \"true\"\n", true},
		{"no placeholder", "trust_file = \"~/.a.json\"\ntrust_key = [\"trusted\"]\ntrust_value = \"true\"\n", true},
	}
	for _, tt := range tests {
		if _, err := Parse(base + tt.settings); (err != nil) != tt.wantErr {
			t.Errorf("%s: Parse error = %v, want error %v", tt.name, err, tt.wantErr)
		}
	}
}

func TestShippedHarnessesReportVersion(t *testing.T) {
	c, err := Parse(Template())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range c.Names() {
		if len(c.Harnesses[name].VersionArgs) == 0 {
			t.Errorf("shipped harness %q has no version_args, so doctor cannot check that it runs", name)
		}
		if len(c.Harnesses[name].InstallCommand) == 0 || len(c.Harnesses[name].LatestCommand) == 0 {
			t.Errorf("shipped harness %q lacks install_command or latest_command, so install cannot install or update it", name)
		}
		if !c.Harnesses[name].Streams() {
			t.Errorf("shipped harness %q has no stream_args, so its answers cannot be shown as they arrive", name)
		}
		if !c.Harnesses[name].ChecksTrust() {
			t.Errorf("shipped harness %q has no trust settings, so doctor cannot check its permission", name)
		}
		if len(c.Harnesses[name].AskArgs) == 0 {
			t.Errorf("shipped harness %q has no ask_args, so doctor cannot check its login", name)
		}
	}
}

func TestShippedHarnessesPassRoles(t *testing.T) {
	c, err := Parse(Template())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range c.Names() {
		if !c.Harnesses[name].PassesRoles() {
			t.Errorf("shipped harness %q has no way to receive its role", name)
		}
	}
}
