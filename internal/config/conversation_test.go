package config

import (
	"strings"
	"testing"
)

func TestConversationSettings(t *testing.T) {
	const head = "default_harness = \"a\"\n[harness.a]\ncommand = \"a\"\nstream_args = [\"ask\"]\nstream_text = \"text\"\n"
	const (
		named   = `conversation_args = ["--new", "{conversation}"]` + "\n"
		key     = `conversation_key = "thread_id"` + "\n"
		resumes = `resume_args = ["--resume", "{conversation}"]` + "\n"
	)
	tests := []struct {
		name, settings, wantErr string
	}{
		{"chosen by H2H", named + resumes, ""},
		{"reported by the harness", key + resumes, ""},
		{"none", "", ""},
		{"no resume_args", named, "need resume_args"},
		{"key without resume_args", key, "need resume_args"},
		{"resume_args without the ID", named + `resume_args = ["--resume"]` + "\n", "resume_args must contain {conversation}"},
		{"resume_args alone", resumes, "need either conversation_args"},
		{"both ways", named + key + resumes, "need either conversation_args"},
		{"conversation_args without the ID", `conversation_args = ["--new"]` + "\n" + resumes, "conversation_args must contain {conversation}"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := Parse(head + tt.settings)
			switch {
			case tt.wantErr == "" && err != nil:
				t.Fatalf("Parse: %v", err)
			case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
				t.Fatalf("Parse error = %v, want it to contain %q", err, tt.wantErr)
			case tt.wantErr == "" && tt.settings != "" && !c.Harnesses["a"].Remembers():
				t.Error("a harness with resume_args does not remember")
			}
		})
	}

	// The ID is read from JSON output, which only stream_args give.
	_, err := Parse("default_harness = \"a\"\n[harness.a]\ncommand = \"a\"\nask_args = [\"ask\"]\n" + key + resumes)
	if err == nil || !strings.Contains(err.Error(), "conversation_key needs stream_args") {
		t.Errorf("conversation_key without stream_args: %v", err)
	}
}

func TestEveryShippedHarnessCarriesAConversationOn(t *testing.T) {
	c, err := Parse(Template())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"claude", "copilot", "codex", "opencode", "grok"} {
		h, ok := c.Harnesses[name]
		if !ok || !h.Remembers() {
			t.Errorf("the shipped %s cannot carry a conversation on", name)
		}
	}
}
