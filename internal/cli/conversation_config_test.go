package cli

import (
	"context"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/session"
)

func TestTheConfigIsRepeatedOnlyWhenItChanges(t *testing.T) {
	dir := talkingHarness(t)
	replies(t, dir, "OK.", "OK.", "OK.")
	e := consoleEnv(t, namingConfig)
	if err := e.converse(context.Background(), "hello"); err != nil {
		t.Fatal(err)
	}

	// A harness starting changes the list of harnesses, not the config.
	if _, err := session.Create(e.root, "namer"); err != nil {
		t.Fatal(err)
	}
	if err := e.converse(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	if p := readRun(t, dir, "prompt", 2); strings.Contains(p, "The config has changed") || !strings.Contains(p, "namer-1") {
		t.Errorf("a started harness was taken for a changed config, or not listed:\n%s", p)
	}

	writeFile(t, config.Path(e.root), namingConfig+"[domain.ui]\nharness = \"namer\"\ndoes = \"The web frontend\"\n")
	if err := e.converse(context.Background(), "and now?"); err != nil {
		t.Fatal(err)
	}
	if p := readRun(t, dir, "prompt", 3); !strings.Contains(p, "The config has changed") || !strings.Contains(p, "The web frontend") {
		t.Errorf("the changed config was not shown:\n%s", p)
	}
}
