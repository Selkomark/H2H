package config

import (
	"strings"
	"testing"
)

func TestShippedHarnessesTellWhenTheyWait(t *testing.T) {
	// Every shipped harness either tells H2H through its hooks that it
	// waits for the user, or says in its entry that it cannot.
	for name, h := range Shipped().Harnesses {
		if h.TellsWaiting() == h.NoWaitSignal {
			t.Errorf("harness %s: state hooks %v, no_wait_signal %v; want exactly one", name, h.TellsWaiting(), h.NoWaitSignal)
		}
	}
}

func TestStateSettingsAreValidated(t *testing.T) {
	base := "default_harness = \"x\"\n[harness.x]\ncommand = \"x\"\n"
	tests := []struct {
		name, extra, want string
	}{
		{"no program", "state_args = [\"--hooks\", \"on\"]\n", "{state}"},
		{"files nowhere", "state_files = { \"h.json\" = \"{state} wait\" }\n", "{state_dir}"},
		{"a path out of the folder", "state_args = [\"--plugin-dir\", \"{state_dir}\"]\nstate_files = { \"../h.json\" = \"{state} wait\" }\n", "plain path"},
		{"an absolute path", "state_args = [\"--plugin-dir\", \"{state_dir}\"]\nstate_files = { \"/h.json\" = \"{state} wait\" }\n", "plain path"},
		{"env not NAME=value", "state_env = [\"{state}\"]\n", "NAME=value"},
		{"both", "state_args = [\"{state}\"]\nno_wait_signal = true\n", "no_wait_signal"},
	}
	for _, tt := range tests {
		if _, err := Parse(base + tt.extra); err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: Parse = %v; want an error about %q", tt.name, err, tt.want)
		}
	}
	if _, err := Parse(base + "state_args = [\"--plugin-dir\", \"{state_dir}\"]\nstate_files = { \"hooks/h.json\" = \"'{state}' wait\" }\n"); err != nil {
		t.Errorf("a valid plugin folder: %v", err)
	}
}

func TestAProjectEntryInheritsTheStateHooks(t *testing.T) {
	// A project written before the state hooks existed gets them.
	c, err := Parse("default_harness = \"copilot\"\n[harness.copilot]\ncommand = \"copilot\"\nask_args = [\"-p\"]\n")
	if err != nil {
		t.Fatal(err)
	}
	if h := c.Harnesses["copilot"]; !h.TellsWaiting() || len(h.StateFiles) != len(Shipped().Harnesses["copilot"].StateFiles) {
		t.Errorf("copilot state settings = %q, %d files; want the shipped ones", h.StateArgs, len(h.StateFiles))
	}
}
