package cli

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
)

// guideConf is a config whose default harness is the stand-in program
// that guideHarness puts on PATH.
const guideConf = "default_harness = \"guide\"\n[harness.guide]\ncommand = \"h2h-guide\"\nversion_args = [\"version\"]\nask_args = [\"ask\"]\n"

// guideHarness puts a stand-in for the default harness on PATH and returns
// the config that uses it. Asked, it is given the whole conversation as
// its prompt: it asks a question until the user has said "done", and then
// sends the config, with a domain added, after the marker line. With
// broken set, the first config it sends is not valid.
//
// The program is a file of its own, and not a script in the config,
// because the config is part of the prompt it matches on.
func guideHarness(t *testing.T, broken bool) string {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	bad := ""
	if broken {
		// A case of its own: once it has been told its file is not valid, it falls through to the good one.
		bad = `case "$2" in *"file is not valid"*) ;; *"User: done"*) printf 'Here it is.\nH2H-CONFIG-FOLLOWS\ndefault_harness = "nobody"\n'; exit ;; esac` + "\n"
	}
	script := "#!/bin/sh\n" +
		`[ "$1" = version ] && { echo guide 1.0; exit; }` + "\n" +
		bad + `case "$2" in *"User: done"*) printf 'All set.\nH2H-CONFIG-FOLLOWS\n%s[domain.ui]\nharness = "guide"\ndoes = "The web frontend"\n' "$CONF" ;; ` +
		`*"User: "*) echo "Anything else?" ;; *"single word OK"*) echo OK ;; *) echo "Which harness should own the UI?" ;; esac` + "\n"
	script = strings.Replace(script, `"$CONF"`, "'"+guideConf+"'", 1)
	if err := os.WriteFile(filepath.Join(bin, "h2h-guide"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return guideConf
}

func TestConfigGuide(t *testing.T) {
	e, out := doctorEnv(t, guideHarness(t, false))
	ui := &scriptedUI{yes: true, answers: []string{"the guide harness", "done"}}
	e.ui = ui

	if err := e.config(context.Background(), nil); err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	if len(ui.said) != 2 || !strings.Contains(ui.said[0], "Which harness should own the UI?") || !strings.Contains(ui.said[1], "Anything else?") {
		t.Errorf("the conversation was %q; want a question, then a follow-up", ui.said)
	}
	if !ui.closed {
		t.Error("the conversation was left open")
	}
	cfg, err := config.Load(e.root)
	if err != nil || cfg.Domains["ui"].Harness != "guide" {
		t.Fatalf("saved config = %+v, %v; want the ui domain the conversation produced", cfg.Domains, err)
	}
	// The file is checked by doctor, then shown as the map.
	for _, want := range []string{"Changes to h2h.toml", "+ [domain.ui]", "✓ h2h.toml", "│ ui", "The web frontend", "is in charge of this domain", "H2H is now configured."} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if doctor, configured := strings.Index(out.String(), "✓ h2h.toml"), strings.Index(out.String(), "H2H is now configured."); doctor > configured {
		t.Error("the confirmation came before doctor's check")
	}
}

func TestConfigGuideAsksAgainForAConfigThatIsNotValid(t *testing.T) {
	e, out := doctorEnv(t, guideHarness(t, true))
	ui := &scriptedUI{yes: true, answers: []string{"done"}}
	e.ui = ui
	if err := e.config(context.Background(), nil); err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	if len(ui.said) != 2 || !strings.Contains(ui.said[1], "was not valid") {
		t.Errorf("the conversation was %q; want the user told the first config was not valid", ui.said)
	}
	if cfg, err := config.Load(e.root); err != nil || len(cfg.Domains) != 1 {
		t.Errorf("saved config: %v, %d domains; want the corrected one", err, len(cfg.Domains))
	}
}

func TestConfigGuideLeavesTheFileAlone(t *testing.T) {
	tests := []struct {
		name    string
		ui      *scriptedUI
		want    string
		wantErr bool
	}{
		{"the user leaves the conversation", &scriptedUI{yes: true}, "Setup cancelled. The config is unchanged.", false},
		{"the user declines the change and then leaves", &scriptedUI{answers: []string{"done"}}, "Setup stopped. The config is unchanged.", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, out := doctorEnv(t, guideHarness(t, false))
			before, _ := os.ReadFile(config.Path(e.root))
			e.ui = tt.ui
			if err := e.config(context.Background(), nil); (err != nil) != tt.wantErr {
				t.Fatalf("config: %v", err)
			}
			after, _ := os.ReadFile(config.Path(e.root))
			if string(before) != string(after) || !strings.Contains(out.String(), tt.want) || strings.Contains(out.String(), "now configured") {
				t.Errorf("the file changed, or the output lacks %q:\n%s", tt.want, out)
			}
		})
	}
}

func TestConfigGuideNeedsAHarnessThatCanAnswer(t *testing.T) {
	tests := []struct{ name, conf, want string }{
		{"not installed", "default_harness = \"x\"\n[harness.x]\ncommand = \"h2h-no-such-program\"\nask_args = [\"-p\"]\n", "install x"},
		{"no ask_args", "default_harness = \"x\"\n[harness.x]\ncommand = \"sh\"\n", "no ask_args"},
	}
	for _, tt := range tests {
		e, _ := doctorEnv(t, tt.conf)
		ui := &scriptedUI{}
		e.ui = ui
		err := e.config(context.Background(), nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) || !strings.Contains(err.Error(), "config --manual") {
			t.Errorf("%s: config = %v; want an error naming %q and the manual way", tt.name, err, tt.want)
		}
	}
	e, _ := doctorEnv(t, guideHarness(t, false))
	if err := e.config(context.Background(), []string{"edit"}); err == nil {
		t.Error("the old \"config edit\" succeeded, want a usage error")
	}
}

func TestGuidePromptCarriesTheSchemaAndTheState(t *testing.T) {
	p := guidePrompt(config.Template(), false, nil, "- claude: installed", "")
	for _, want := range []string{"no config file yet", "context_args", "depends_on", configMarker, "- claude: installed", "type /done", "do not guess what it agrees to", "Do not summarise the setup in words"} {
		if !strings.Contains(p, want) {
			t.Errorf("the prompt lacks %q", want)
		}
	}
	if !strings.Contains(conversation(nil), "Send your first message") || !strings.Contains(conversation([]string{"You: hi", "User: yes"}), "User: yes") {
		t.Error("the conversation is not passed on as it stands")
	}
}

func TestConfigGuideSaysWhyTheHarnessCouldNotAnswer(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	script := "#!/bin/sh\necho \"You have hit your 5-hour limit. It resets at 4:10pm.\"\nexit 1\n"
	if err := os.WriteFile(filepath.Join(bin, "h2h-guide"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	e, _ := doctorEnv(t, guideConf)
	e.ui = &scriptedUI{}
	err := e.config(context.Background(), nil)
	if err == nil || !strings.Contains(err.Error(), "You have hit your 5-hour limit. It resets at 4:10pm.") || strings.Contains(err.Error(), "exit status") {
		t.Errorf("config = %v; want the harness's own reason, not its exit status", err)
	}

	// A harness that says nothing gets the command to try by hand.
	if err := os.WriteFile(filepath.Join(bin, "h2h-guide"), []byte("#!/bin/sh\nexit 3\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := e.config(context.Background(), nil); err == nil || !strings.Contains(err.Error(), "gave no reason") || !strings.Contains(err.Error(), "h2h-guide ask hello") {
		t.Errorf("config = %v; want the command to run by hand", err)
	}
}

func TestConfigGuideGoesBackToTheConversationAfterANo(t *testing.T) {
	e, out := doctorEnv(t, guideHarness(t, false))
	// The first proposal is turned down, the second applied.
	ui := &scriptedUI{confirms: []bool{false, true}, answers: []string{"done", "keep it as it was, done"}}
	e.ui = ui
	if err := e.config(context.Background(), nil); err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	asked := false
	for _, said := range ui.said {
		asked = asked || strings.Contains(said, "Not applied; the config is unchanged. What should be different?")
	}
	if !asked {
		t.Errorf("after the no, the conversation did not ask what to change: %q", ui.said)
	}
	if cfg, err := config.Load(e.root); err != nil || len(cfg.Domains) != 1 || !strings.Contains(out.String(), "H2H is now configured.") {
		t.Errorf("the second proposal was not applied: %v\n%s", err, out)
	}
}

func TestApplyShowsTheChangesAndThenTheMap(t *testing.T) {
	e, out := doctorEnv(t, guideConf)
	e.ui = &scriptedUI{}
	next := guideConf + "[domain.api]\nharness = \"guide\"\n[domain.ui]\nharness = \"guide\"\ndepends_on = [\"api\"]\n  [[domain.ui.subagent]]\n  role = \"coder\"\n  [[domain.ui.subagent]]\n  role = \"reviewer\"\n"
	if applied, err := e.configApply(context.Background(), guideConf, true, next); err != nil || applied {
		t.Fatalf("configApply = %v, %v", applied, err)
	}
	shown := out.String()
	changes, setup := strings.Index(shown, "Changes to h2h.toml"), strings.Index(shown, "The setup with this config")
	if changes < 0 || setup < changes {
		t.Fatalf("want the changes, then the map last:\n%s", shown)
	}
	// The map lists each domain's sub-agents in its box, and draws the flow.
	for _, want := range []string{"│ api", "│ ui", "· coder", "· reviewer", "▶", "+ [domain.ui]"} {
		if !strings.Contains(shown, want) {
			t.Errorf("the preview lacks %q:\n%s", want, shown)
		}
	}
	// Nothing else: no setup of each harness, no list of commands.
	for _, not := range []string{"Owner", "starts", "permission", "Running", "Harnesses ", "Commands"} {
		if strings.Contains(shown, not) {
			t.Errorf("the preview shows %q, which is not what is being confirmed:\n%s", not, shown)
		}
	}
	if e.ui.(*scriptedUI).asked != "Apply this config to h2h.toml?" {
		t.Errorf("asked %q", e.ui.(*scriptedUI).asked)
	}
}

func TestConfigGuideShowsTheSetupAndTakesDone(t *testing.T) {
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	// A stand-in that asks once, and sends the config when it is told the
	// user is finished, keeping the prompt that told it so.
	script := "#!/bin/sh\n[ \"$1\" = version ] && { echo guide 1.0; exit; }\n" +
		`case "$2" in *"User: DONE."*) printf '%s' "$2" > ` + bin + `/last-prompt; printf 'H2H-CONFIG-FOLLOWS\n%s' "$(cat ` + bin + `/next.toml)" ;; *"single word OK"*) echo OK ;; *) echo "What should change?" ;; esac` + "\n"
	if err := os.WriteFile(filepath.Join(bin, "h2h-guide"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	conf := guideConf + "[domain.api]\nharness = \"guide\"\ndoes = \"The API\"\npaths = [\"api/**\"]\n" +
		"[domain.ui]\nharness = \"guide\"\ndepends_on = [\"api\"]\n  [[domain.ui.subagent]]\n  role = \"coder\"\n"
	if err := os.WriteFile(filepath.Join(bin, "next.toml"), []byte(conf+"[domain.docs]\nharness = \"guide\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, out := doctorEnv(t, conf)
	ui := &scriptedUI{yes: true, answers: []string{"add a docs domain", "/done"}}
	e.ui = ui
	if err := e.config(context.Background(), nil); err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}

	// The conversation opens on the setup as a map and a table, then how to reply.
	if len(ui.shown) != 2 {
		t.Fatalf("drawn into the conversation: %d things, want the setup and how to reply", len(ui.shown))
	}
	for _, want := range []string{"The setup as it is now", "│ api", "│ ui", "· coder", "▶", "Domain", "Owner", "Sub-agents", "Files", "Starts after", "api/**", "The default harness is guide"} {
		if !strings.Contains(ui.shown[0], want) {
			t.Errorf("the picture of the setup lacks %q:\n%s", want, ui.shown[0])
		}
	}
	if !strings.Contains(ui.shown[1], "/done") || !strings.Contains(ui.shown[1], "nothing is written before you apply it") {
		t.Errorf("how to reply: %q", ui.shown[1])
	}
	// /done is passed on as one fixed message, whatever else was said.
	prompt, _ := os.ReadFile(filepath.Join(bin, "last-prompt"))
	if !strings.Contains(string(prompt), "User: add a docs domain") || !strings.Contains(string(prompt), "User: DONE. The user is finished") || strings.Contains(string(prompt), "User: /done") {
		t.Errorf("the harness was not told plainly that the user is done:\n%s", prompt)
	}
	if cfg, err := config.Load(e.root); err != nil || len(cfg.Domains) != 3 {
		t.Errorf("the config was not written: %v", err)
	}
}

func TestSetupPictureWithoutDomains(t *testing.T) {
	cfg, err := config.Parse(guideConf)
	if err != nil {
		t.Fatal(err)
	}
	got := setupPicture(cfg, plainStyles())(100)
	if !strings.Contains(got, "There are no domains yet") || !strings.Contains(got, "guide") || strings.Contains(got, "╭") {
		t.Errorf("a config with no domains is drawn as:\n%s", got)
	}
}

func TestConfigGuideAsksWhichHarnessHoldsIt(t *testing.T) {
	// A second harness that can answer, beside the default one.
	conf := guideHarness(t, false) + "[harness.other]\ncommand = \"sh\"\nask_args = [\"-c\", \"echo hi\"]\n[harness.gone]\ncommand = \"h2h-no-such-program\"\nask_args = [\"-p\"]\n"
	e, out := doctorEnv(t, conf)
	ui := &scriptedUI{yes: true, answers: []string{"done"}}
	e.ui = ui
	if err := e.config(context.Background(), nil); err != nil {
		t.Fatalf("config: %v\n%s", err, out)
	}
	if !strings.Contains(ui.question, "Which harness") {
		t.Fatalf("no harness was asked for; the last question was %q", ui.question)
	}
	want := []string{"guide (the default) · one run per message", "other · one run per message"}
	if strings.Join(ui.labels, "|") != strings.Join(want, "|") {
		t.Errorf("offered %q, want %q: the default first, and nothing that is not installed", ui.labels, want)
	}
	if cfg, err := config.Load(e.root); err != nil || cfg.Domains["ui"].Harness != "guide" {
		t.Errorf("the default harness, picked, did not hold the conversation: %v %+v", err, cfg.Domains)
	}
}

func TestConfigOffersTheFolderItRunsIn(t *testing.T) {
	e, _ := doctorEnv(t, guideConf)
	root, err := filepath.EvalSymlinks(e.root)
	if err != nil {
		t.Fatal(err)
	}
	e.root = root
	sub := filepath.Join(root, "teams", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}

	t.Chdir(root)
	ui := &scriptedUI{}
	e.ui = ui
	if got, err := e.configFolder(); err != nil || got != root || ui.question != "" {
		t.Errorf("at the project's top: %s, %v, asked %q; want the project, unasked", got, err, ui.question)
	}

	t.Chdir(sub)
	for pick, want := range map[int]string{0: root, 1: sub} {
		ui := &scriptedUI{pick: func(o []console.Option) ([]string, bool) { return []string{o[pick].ID}, true }}
		e.ui = ui
		got, err := e.configFolder()
		if err != nil || got != want || !strings.Contains(ui.question, "which folder") {
			t.Errorf("picking %d: %s, %v, asked %q; want %s", pick, got, err, ui.question, want)
		}
		if !strings.Contains(strings.Join(ui.labels, "|"), "teams/web") {
			t.Errorf("the folder was not offered by name: %q", ui.labels)
		}
	}
}
