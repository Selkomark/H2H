package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
)

const addedMessage = "Added /.h2h/ and /h2h.toml.*.tmp to .gitignore, so H2H's own files stay out of the project."

func readGitignore(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, ".gitignore"))
	if err != nil {
		t.Fatalf("reading .gitignore: %v", err)
	}
	return string(data)
}

func TestApplyingTheConfigIgnoresH2HFiles(t *testing.T) {
	e, out := doctorEnv(t, "")
	e.ui = &scriptedUI{yes: true}
	conf := `default_harness = "good"` + workingHarness
	if applied, err := e.configApply(context.Background(), config.Template(), false, conf); err != nil || !applied {
		t.Fatalf("configApply = %v, %v", applied, err)
	}
	text := readGitignore(t, e.root)
	for _, want := range []string{ignoreComment + "\n", "/.h2h/\n", "/h2h.toml.*.tmp\n"} {
		if !strings.Contains(text, want) {
			t.Errorf(".gitignore lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(text, "\nh2h.toml\n") || strings.Contains(text, "/h2h.toml\n") {
		t.Errorf(".gitignore leaves out h2h.toml, which belongs in git:\n%s", text)
	}
	if !strings.Contains(out.String(), addedMessage) {
		t.Errorf("output does not say what was added:\n%s", out)
	}

	// Applying again changes nothing and says nothing about it.
	out.Reset()
	if applied, err := e.configApply(context.Background(), conf, true, conf+"\n"); err != nil || !applied {
		t.Fatalf("second configApply = %v, %v", applied, err)
	}
	if got := readGitignore(t, e.root); got != text {
		t.Errorf("the second apply changed .gitignore:\n%s", got)
	}
	if strings.Contains(out.String(), ".gitignore") {
		t.Errorf("the second apply mentioned .gitignore:\n%s", out)
	}
}

func TestStartingAHarnessIgnoresH2HFiles(t *testing.T) {
	e, out := doctorEnv(t, `default_harness = "good"`+workingHarness)
	// A file where the state directory would go makes the start fail just
	// after the point that keeps H2H's files out of git, so no harness is
	// spawned by the test.
	if err := os.WriteFile(filepath.Join(e.root, ".h2h"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := e.start(context.Background(), nil); err == nil {
		t.Fatal("start succeeded with a file in place of the state directory")
	}
	if text := readGitignore(t, e.root); !strings.Contains(text, "/.h2h/\n") {
		t.Errorf("start did not add /.h2h/ to .gitignore:\n%s", text)
	}
	if !strings.Contains(out.String(), addedMessage) {
		t.Errorf("output does not say what was added:\n%s", out)
	}
}

func TestH2HFilesStayOutOfGitStatus(t *testing.T) {
	e, _ := doctorEnv(t, "")
	e.keepOutOfGit(context.Background())
	// What H2H leaves in a project while it works.
	for _, path := range []string{".h2h/sessions/claude-1/terminal.log", ".h2h/counters.json", "h2h.toml.123.tmp"} {
		full := filepath.Join(e.root, path)
		if err := os.MkdirAll(filepath.Dir(full), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(config.Path(e.root), []byte(`default_harness = "good"`+workingHarness), 0o644); err != nil {
		t.Fatal(err)
	}
	status, err := exec.Command("git", "-C", e.root, "status", "--porcelain", "--untracked-files=all").Output()
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(status), "?? .gitignore\n?? h2h.toml\n"; got != want {
		t.Errorf("git status =\n%s\nwant only .gitignore and h2h.toml:\n%s", got, want)
	}
}

func TestDoctorChecksTheGitignore(t *testing.T) {
	e, out := doctorEnv(t, `default_harness = "good"`+workingHarness)
	if err := e.doctor(context.Background(), nil); err != nil && !strings.Contains(err.Error(), "doctor found") {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "! .gitignore") || !strings.Contains(out.String(), "does not leave out /.h2h/ and /h2h.toml.*.tmp") {
		t.Errorf("doctor did not warn about the missing entries:\n%s", out)
	}
	if _, err := os.Stat(filepath.Join(e.root, ".gitignore")); !os.IsNotExist(err) {
		t.Errorf("doctor wrote a .gitignore: %v", err)
	}

	e.keepOutOfGit(context.Background())
	out.Reset()
	if err := e.doctor(context.Background(), nil); err != nil && !strings.Contains(err.Error(), "doctor found") {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "✓ .gitignore") {
		t.Errorf("doctor did not pass the .gitignore once it was right:\n%s", out)
	}
}
