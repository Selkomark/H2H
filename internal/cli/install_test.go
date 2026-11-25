package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
)

// scriptedUI answers a Choose with a fixed pick and records what was asked.
type scriptedUI struct {
	pick     func(options []console.Option) ([]string, bool)
	question string
	labels   []string
	yes      bool     // the answer to every yes-or-no question
	asked    string   // the last yes-or-no question
	confirms []bool   // answers to yes-or-no questions, in order, before yes applies
	answers  []string // the lines typed into a conversation, in order
	said     []string // what the other side said in it
	closed   bool
	shown    []string       // what was drawn into a conversation
	cleared  bool           // the page was emptied
	loaded   *console.State // what a command put on the page
	note     string
}

func (u *scriptedUI) Write(b []byte) (int, error) { return len(b), nil }
func (u *scriptedUI) Confirm(question string) bool {
	u.asked = question
	if len(u.confirms) > 0 { // answers given one by one come first
		answer := u.confirms[0]
		u.confirms = u.confirms[1:]
		return answer
	}
	return u.yes
}
func (u *scriptedUI) Foreground(fn func() error) error  { return fn() }
func (u *scriptedUI) Load(s console.State, note string) { u.loaded, u.note = &s, note }
func (u *scriptedUI) Chat(string) console.Chat          { return u }
func (u *scriptedUI) Say(text string)                   { u.said = append(u.said, text) }
func (u *scriptedUI) Wait(string)                       {}
func (u *scriptedUI) Close()                            { u.closed = true }
func (u *scriptedUI) Ask() (string, bool) {
	if len(u.answers) == 0 {
		return "", false
	}
	answer := u.answers[0]
	u.answers = u.answers[1:]
	return answer, true
}
func (u *scriptedUI) Choose(question string, options []console.Option) ([]string, bool) {
	u.question = question
	for _, o := range options {
		u.labels = append(u.labels, o.Label)
	}
	return u.pick(options)
}

func takeAll(options []console.Option) ([]string, bool) {
	var ids []string
	for _, o := range options {
		if o.Checked {
			ids = append(ids, o.ID)
		}
	}
	return ids, true
}

func takeNone([]console.Option) ([]string, bool) { return nil, false }

// fakeTool describes a harness whose program is a script in bin that
// prints the version kept in a file beside it. Its install_command writes
// the program, at the version its latest_command reports.
func fakeTool(bin, name, latest string) string {
	program, version := filepath.Join(bin, name), filepath.Join(bin, name+".version")
	install := "echo " + latest + " > " + version + "; printf '#!/bin/sh\\ncat " + version + "\\n' > " + program + "; chmod +x " + program
	return "[harness." + name + "]\ncommand = \"" + name + "\"\nversion_args = [\"--version\"]\n" +
		"install_command = [\"sh\", \"-c\", \"" + install + "\"]\n" +
		"latest_command = [\"sh\", \"-c\", \"echo " + latest + "\"]\n"
}

// installOld puts version 1.0.0 of a fake tool on PATH.
func installOld(t *testing.T, bin, name string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(bin, name+".version"), []byte("1.0.0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\ncat " + filepath.Join(bin, name+".version") + "\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

// installEnv is doctorEnv with a private directory first on PATH and a
// scripted user.
func installEnv(t *testing.T, conf func(bin string) string, pick func([]console.Option) ([]string, bool)) (*env, *scriptedUI, string, func() string) {
	t.Helper()
	bin := t.TempDir()
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	e, out := doctorEnv(t, conf(bin))
	ui := &scriptedUI{pick: pick}
	e.ui = ui
	return e, ui, bin, out.String
}

func TestInstallMissingHarness(t *testing.T) {
	conf := func(bin string) string {
		return "default_harness = \"alpha\"\n" + fakeTool(bin, "alpha", "2.0.0") + fakeTool(bin, "beta", "3.1.0")
	}
	e, ui, bin, output := installEnv(t, conf, takeAll)
	installOld(t, bin, "alpha")

	if err := e.install(context.Background(), nil); err != nil {
		t.Fatalf("install: %v\n%s", err, output())
	}
	if ui.question != "Install these harnesses?" || len(ui.labels) != 1 || !strings.Contains(ui.labels[0], "beta") {
		t.Errorf("asked %q about %q; want only the missing beta", ui.question, ui.labels)
	}
	for _, want := range []string{"alpha      ✓ already installed 1.0.0", "✓ beta now 3.1.0"} {
		if !strings.Contains(output(), want) {
			t.Errorf("output lacks %q:\n%s", want, output())
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "beta")); err != nil {
		t.Errorf("beta was not installed: %v", err)
	}
}

// installBroken puts a program on PATH that is found but does not run, like
// a terminal's stand-in for a harness that is not installed.
func installBroken(t *testing.T, bin, name string) {
	t.Helper()
	script := "#!/bin/sh\necho 'Error: " + name + " not found in PATH' >&2\nexit 127\n"
	if err := os.WriteFile(filepath.Join(bin, name), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
}

func TestInstallOffersAHarnessThatDoesNotRun(t *testing.T) {
	conf := func(bin string) string { return "default_harness = \"alpha\"\n" + fakeTool(bin, "alpha", "2.0.0") }
	e, ui, bin, output := installEnv(t, conf, takeAll)
	installBroken(t, bin, "alpha")

	if err := e.install(context.Background(), nil); err != nil {
		t.Fatalf("install: %v\n%s", err, output())
	}
	if len(ui.labels) != 1 || !strings.Contains(ui.labels[0], "does not run") {
		t.Errorf("offered %q; want the harness that does not run", ui.labels)
	}
	for _, want := range []string{"installed but does not run: exit status 127: Error: alpha not found in PATH", "✓ alpha now 2.0.0"} {
		if !strings.Contains(output(), want) {
			t.Errorf("output lacks %q:\n%s", want, output())
		}
	}
	if strings.Contains(output(), "already installed") {
		t.Errorf("a harness that does not run is called installed:\n%s", output())
	}
}

func TestInstallUpdateSaysAHarnessDoesNotRun(t *testing.T) {
	conf := func(bin string) string { return "default_harness = \"alpha\"\n" + fakeTool(bin, "alpha", "2.0.0") }
	e, ui, bin, output := installEnv(t, conf, takeAll)
	installBroken(t, bin, "alpha")

	if err := e.install(context.Background(), []string{updateFlag}); err != nil {
		t.Fatalf("install --update: %v\n%s", err, output())
	}
	for _, want := range []string{"installed but does not run: exit status 127", "reinstall it with: install alpha"} {
		if !strings.Contains(output(), want) {
			t.Errorf("output lacks %q:\n%s", want, output())
		}
	}
	if strings.Contains(output(), "version_args") || len(ui.labels) != 0 {
		t.Errorf("offered %q, with output:\n%s", ui.labels, output())
	}
}

func TestInstallDeclined(t *testing.T) {
	conf := func(bin string) string { return "default_harness = \"beta\"\n" + fakeTool(bin, "beta", "3.1.0") }
	e, _, bin, output := installEnv(t, conf, takeNone)
	if err := e.install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(bin, "beta")); err == nil {
		t.Error("beta was installed although the user declined")
	}
	if !strings.Contains(output(), "Nothing was changed.") {
		t.Errorf("output does not say nothing changed:\n%s", output())
	}
}

func TestInstallUpdate(t *testing.T) {
	conf := func(bin string) string {
		return "default_harness = \"alpha\"\n" + fakeTool(bin, "alpha", "2.0.0") + fakeTool(bin, "beta", "1.0.0") + fakeTool(bin, "gamma", "9.0.0")
	}
	// The user unticks nothing: only alpha has a newer version to offer.
	e, ui, bin, output := installEnv(t, conf, takeAll)
	installOld(t, bin, "alpha")
	installOld(t, bin, "beta")

	if err := e.install(context.Background(), []string{"--update"}); err != nil {
		t.Fatalf("install --update: %v\n%s", err, output())
	}
	if ui.question != "Would you like to update?" || len(ui.labels) != 1 || !strings.Contains(ui.labels[0], "1.0.0 → 2.0.0") {
		t.Errorf("asked %q about %q; want only alpha, from 1.0.0 to 2.0.0", ui.question, ui.labels)
	}
	for _, want := range []string{"beta       ✓ up to date 1.0.0", "gamma      - not installed", "✓ alpha now 2.0.0"} {
		if !strings.Contains(output(), want) {
			t.Errorf("output lacks %q:\n%s", want, output())
		}
	}
	if _, err := os.Stat(filepath.Join(bin, "gamma")); err == nil {
		t.Error("--update installed gamma, which was not installed")
	}
}

func TestInstallUpdateOnlyWhatIsTicked(t *testing.T) {
	conf := func(bin string) string {
		return "default_harness = \"alpha\"\n" + fakeTool(bin, "alpha", "2.0.0") + fakeTool(bin, "beta", "2.0.0")
	}
	e, ui, bin, output := installEnv(t, conf, func([]console.Option) ([]string, bool) { return []string{"beta"}, true })
	installOld(t, bin, "alpha")
	installOld(t, bin, "beta")

	if err := e.install(context.Background(), []string{"--update"}); err != nil {
		t.Fatalf("install --update: %v\n%s", err, output())
	}
	if len(ui.labels) != 2 {
		t.Errorf("offered %q, want both", ui.labels)
	}
	got, _ := os.ReadFile(filepath.Join(bin, "alpha.version"))
	if strings.TrimSpace(string(got)) != "1.0.0" || !strings.Contains(output(), "✓ beta now 2.0.0") {
		t.Errorf("alpha is %q; want it left alone and beta updated:\n%s", got, output())
	}
}

func TestInstallReportsAnUpdateThatDidNotTake(t *testing.T) {
	conf := func(bin string) string {
		return "default_harness = \"alpha\"\n[harness.alpha]\ncommand = \"alpha\"\nversion_args = [\"--version\"]\n" +
			"install_command = [\"true\"]\nlatest_command = [\"echo\", \"2.0.0\"]\n"
	}
	e, _, bin, output := installEnv(t, conf, takeAll)
	installOld(t, bin, "alpha")
	err := e.install(context.Background(), []string{"--update"})
	if err == nil || !strings.Contains(output(), "another copy of it comes first on PATH") {
		t.Errorf("install --update = %v; want the unchanged version reported:\n%s", err, output())
	}
}

func TestInstallCannot(t *testing.T) {
	tests := []struct {
		name, conf, want string
		args             []string
		wantErr          bool
	}{
		{"no install_command", "[harness.x]\ncommand = \"h2h-no-such-program\"\n", "has no install_command", nil, false},
		{"installer missing", "[harness.x]\ncommand = \"h2h-no-such-program\"\ninstall_command = [\"h2h-no-such-installer\"]\n", "which is not installed or not on PATH", nil, false},
		{"unknown harness", "[harness.x]\ncommand = \"sh\"\n", "", []string{"nobody"}, true},
		{"unknown flag", "[harness.x]\ncommand = \"sh\"\n", "", []string{"--force"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, ui, _, output := installEnv(t, func(string) string { return "default_harness = \"x\"\n" + tt.conf }, takeAll)
			err := e.install(context.Background(), tt.args)
			if (err != nil) != tt.wantErr || !strings.Contains(output(), tt.want) || ui.question != "" {
				t.Errorf("install = %v, asked %q; want error %v, nothing asked, and %q in:\n%s", err, ui.question, tt.wantErr, tt.want, output())
			}
		})
	}
}

func TestPickByNumber(t *testing.T) {
	options := []console.Option{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	tests := []struct {
		line   string
		want   []string
		wantOK bool
	}{
		{"", nil, false},
		{"all", []string{"a", "b", "c"}, true},
		{"2", []string{"b"}, true},
		{"3, 1", []string{"a", "c"}, true},
		{"1 4", nil, false},
		{"yes", nil, false},
	}
	for _, tt := range tests {
		got, ok := pickByNumber(tt.line, options)
		if !slices.Equal(got, tt.want) || ok != tt.wantOK {
			t.Errorf("pickByNumber(%q) = %v, %v; want %v, %v", tt.line, got, ok, tt.want, tt.wantOK)
		}
	}
}

func (u *scriptedUI) Clear() { u.cleared = true }

func (u *scriptedUI) Show(draw func(width int) string) { u.shown = append(u.shown, draw(100)) }

func TestInstallKnowsHarnessesTheConfigLacks(t *testing.T) {
	conf := func(bin string) string { return "default_harness = \"alpha\"\n" + fakeTool(bin, "alpha", "2.0.0") }
	// Only the stand-in directory and the system's own are on PATH, so no
	// real harness or installer is found and nothing real can be installed.
	e, ui, bin, output := installEnv(t, conf, takeNone)
	t.Setenv("PATH", bin+string(os.PathListSeparator)+"/bin:/usr/bin")
	installOld(t, bin, "alpha")

	// Listing everything names what H2H supports beyond the config.
	if err := e.install(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if out := output(); !strings.Contains(out, "opencode   - supported, not in h2h.toml; not installed; install it with: install opencode") {
		t.Errorf("the supported harnesses are not listed:\n%s", out)
	}
	// Naming one uses the entry H2H ships for it, and is not refused.
	err := e.install(context.Background(), []string{"opencode"})
	if out := output(); err != nil || !strings.Contains(out, "is installed with npm, which is not installed or not on PATH") || !strings.Contains(out, "opencode is not in h2h.toml yet. Add it with: /config") {
		t.Errorf("install opencode = %v:\n%s", err, out)
	}
	if len(ui.labels) != 0 {
		t.Errorf("something was offered although its installer is missing: %q", ui.labels)
	}
	// A name H2H does not know is refused, and says what it does support.
	if err := e.install(context.Background(), []string{"nothing"}); err == nil || !strings.Contains(err.Error(), "H2H also supports: claude, codex, copilot, grok, opencode") {
		t.Errorf("install nothing = %v", err)
	}
}

func TestDoctorAndGuideNameSupportedHarnesses(t *testing.T) {
	t.Setenv("PATH", "/bin:/usr/bin")
	e, out := doctorEnv(t, "default_harness = \"mine\"\n[harness.mine]\ncommand = \"sh\"\n")
	_ = e.doctor(context.Background(), nil)
	for _, want := range []string{"Also supported", "- opencode", "not installed, not in h2h.toml", "To use it: install it with install opencode, then add it with /config"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("doctor lacks %q:\n%s", want, out)
		}
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		t.Fatal(err)
	}
	p := guidePrompt("x", true, nil, "- mine: installed", supportedHarnesses(cfg))
	for _, want := range []string{"also supports the harnesses below", "opencode (not installed):\n[harness.opencode]\ncommand = \"opencode\"", `install_command = ["npm", "install", "-g", "@opencode/cli@latest"]`, "/install <name>"} {
		if !strings.Contains(p, want) {
			t.Errorf("the guide's prompt lacks %q", want)
		}
	}
	if got := supportedHarnesses(config.Shipped()); got != "" {
		t.Errorf("a config with every harness is told of more: %q", got)
	}
}
