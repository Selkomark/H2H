package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
)

// doctorEnv makes a git repository with one commit and the given config,
// and returns an env whose output is captured.
func doctorEnv(t *testing.T, conf string) (*env, *bytes.Buffer) {
	t.Helper()
	root := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=test", "-c", "user.email=test@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	if conf != "" {
		if err := os.WriteFile(config.Path(root), []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	return &env{root: root, out: &out, errw: &out, environ: os.Environ(), st: plainStyles()}, &out
}

// A harness that runs, and answers when asked.
const workingHarness = `
[harness.good]
command = "sh"
version_args = ["-c", "echo good 1.0"]
ask_args = ["-c", "echo OK"]
`

func TestDoctor(t *testing.T) {
	tests := []struct {
		name    string
		conf    string
		args    []string
		wantErr bool
		want    []string // each must appear in the output
	}{
		{
			name: "all in place",
			conf: `default_harness = "good"` + workingHarness,
			want: []string{"✓ git", "✓ first commit", "✓ state directory", "✓ sessions", "✓ good (default)", "good 1.0",
				"logged in and answering", "\nPermission\n"},
		},
		{
			name:    "flags are gone",
			conf:    `default_harness = "good"` + workingHarness,
			args:    []string{"--login"},
			wantErr: true,
		},
		{
			name:    "login refused",
			conf:    "default_harness = \"good\"\n[harness.good]\ncommand = \"sh\"\nask_args = [\"-c\", \"echo please log in >&2; exit 1\"]\n",
			wantErr: true,
			want:    []string{"✗ good (default)", "could not answer: please log in"},
		},
		{
			name:    "default harness missing",
			conf:    "default_harness = \"gone\"\n[harness.gone]\ncommand = \"h2h-no-such-program\"\n",
			wantErr: true,
			want:    []string{"✗ gone (default)", "not installed or not on PATH", "it is the default harness"},
		},
		{
			name: "unused harness missing only warns",
			conf: `default_harness = "good"` + workingHarness + "[harness.spare]\ncommand = \"h2h-no-such-program\"\n",
			want: []string{"! spare", "H2H can run."},
		},
		{
			name:    "domain owner missing",
			conf:    `default_harness = "good"` + workingHarness + "[harness.spare]\ncommand = \"h2h-no-such-program\"\n[domain.ui]\nharness = \"spare\"\n",
			wantErr: true,
			want:    []string{"✗ spare", "it owns the domain ui"},
		},
		{
			name:    "installed but broken",
			conf:    "default_harness = \"bad\"\n[harness.bad]\ncommand = \"sh\"\nversion_args = [\"-c\", \"echo cannot start >&2; exit 2\"]\n",
			wantErr: true,
			want:    []string{"✗ bad (default)", "installed but does not run", "cannot start"},
		},
		{
			name:    "config not valid",
			conf:    "default_harness = \"nobody\"\n[harness.good]\ncommand = \"sh\"\n",
			wantErr: true,
			want:    []string{"✗ h2h.toml", `default_harness "nobody" is not a configured harness`, "/config --manual"},
		},
		{
			name:    "config does not parse",
			conf:    "default_harness = \n",
			wantErr: true,
			want:    []string{"✗ h2h.toml", "line 1", "/config --manual"},
		},
		{
			name:    "config has an unknown setting",
			conf:    `default_harness = "good"` + workingHarness + "colour = \"blue\"\n",
			wantErr: true,
			want:    []string{"✗ h2h.toml", "unknown setting: harness.good.colour"},
		},
		{
			name:    "unknown argument",
			conf:    `default_harness = "good"` + workingHarness,
			args:    []string{"--everything"},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e, out := doctorEnv(t, tt.conf)
			err := e.doctor(context.Background(), tt.args)
			if (err != nil) != tt.wantErr {
				t.Errorf("doctor() error = %v, want error %v\n%s", err, tt.wantErr, out)
			}
			for _, w := range tt.want {
				if !strings.Contains(out.String(), w) {
					t.Errorf("output lacks %q:\n%s", w, out)
				}
			}
		})
	}
}

func TestDoctorPermission(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	conf := `default_harness = "good"` + workingHarness +
		"trust_file = \"~/good.json\"\ntrust_key = [\"trustedFolders\"]\ntrust_value = \"{project}\"\n" +
		"[harness.plain]\ncommand = \"sh\"\n[harness.gone]\ncommand = \"h2h-no-such-program\"\n" +
		"trust_file = \"~/gone.json\"\ntrust_key = [\"trustedFolders\"]\ntrust_value = \"{project}\"\n"

	e, out := doctorEnv(t, conf)
	// The section is matched as a whole line: the test's own name is in the project's path.
	const heading = "\nPermission\n"
	if err := e.doctor(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{heading, "! good", "has not been told to trust this project", "! plain", "no trust_file in the config"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if _, section, _ := strings.Cut(out.String(), heading); strings.Contains(section, "gone") {
		t.Errorf("a harness that is not installed is listed under Permission too:\n%s", out)
	}

	record := `{"trustedFolders": ["` + e.root + `"]}`
	if err := os.WriteFile(filepath.Join(home, "good.json"), []byte(record), 0o600); err != nil {
		t.Fatal(err)
	}
	out.Reset()
	if err := e.doctor(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "✓ good               trusts this project") || !strings.Contains(out.String(), "logged in and answering") {
		t.Errorf("want good trusted and logged in:\n%s", out)
	}
}

func TestDoctorWarnsWithoutAConfig(t *testing.T) {
	// The shipped harnesses stand in; keep the real ones off PATH.
	t.Setenv("PATH", "/usr/bin:/bin")
	e, out := doctorEnv(t, "")
	_ = e.doctor(context.Background(), nil)
	if !strings.Contains(out.String(), "! h2h.toml") || !strings.Contains(out.String(), "set it up with /config") {
		t.Errorf("want a warning that points at /config:\n%s", out)
	}
}

func TestDoctorOutsideAProject(t *testing.T) {
	// Outside a project the shipped harnesses are checked. Keep the real
	// ones off PATH, so the test never asks a real harness for an answer.
	t.Setenv("PATH", "/usr/bin:/bin")
	var out bytes.Buffer
	e := &env{out: &out, errw: &out, environ: os.Environ(), st: plainStyles()}
	if err := e.doctor(context.Background(), nil); err == nil {
		t.Error("doctor outside a project succeeded, want error")
	}
	if !strings.Contains(out.String(), "✗ project") || !strings.Contains(out.String(), "Harnesses") {
		t.Errorf("want the project reported and the harnesses still checked:\n%s", out.String())
	}
}

func TestDoctorChangesNothing(t *testing.T) {
	e, _ := doctorEnv(t, `default_harness = "good"`+workingHarness)
	if err := e.doctor(context.Background(), nil); err != nil && !strings.Contains(err.Error(), "doctor found") {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(e.root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != ".git" && entry.Name() != config.FileName {
			t.Errorf("doctor left %s in the project", filepath.Join(e.root, entry.Name()))
		}
	}
}

func TestDoctorNamesSettingsAnOlderConfigLacks(t *testing.T) {
	t.Setenv("PATH", "/usr/bin:/bin") // keep the real harness off PATH
	// A config as an earlier H2H wrote it, for the shipped claude program:
	// what it does not name comes from the shipped entry, so nothing is
	// missing. Another program under the name claude gets nothing, and
	// doctor names what it lacks.
	e, out := doctorEnv(t, "default_harness = \"claude\"\n[harness.claude]\ncommand = \"claude\"\nask_args = [\"--permission-mode\", \"plan\", \"-p\"]\n[harness.mine]\ncommand = \"sh\"\n")
	_ = e.doctor(context.Background(), nil)
	if strings.Contains(out.String(), "that H2H ships for it") {
		t.Errorf("settings an older config inherits were reported as missing:\n%s", out)
	}

	e, out = doctorEnv(t, "default_harness = \"claude\"\n[harness.claude]\ncommand = \"my-claude\"\nask_args = [\"-p\"]\n")
	_ = e.doctor(context.Background(), nil)
	for _, want := range []string{"! claude", "that H2H ships for it", `report_args = ["--allowedTools", "Bash(h2h-report:*)"]`, `stream_text = "event.delta.text"`, "permit_args = [\"--settings\","} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, out)
		}
	}
	if strings.Contains(out.String(), "ask_args = ") {
		t.Errorf("a setting that is there was reported:\n%s", out)
	}
	// The lines shown are valid config.
	shipped, _ := config.Parse(config.Template())
	lines := strings.Join(config.Harness{Command: "claude"}.Lacks(shipped.Harnesses["claude"]), "\n")
	if cfg, err := config.Parse("default_harness = \"claude\"\n[harness.claude]\ncommand = \"claude\"\n" + lines + "\n"); err != nil || !cfg.Harnesses["claude"].AsksH2H() {
		t.Errorf("the lines doctor shows do not parse back: %v\n%s", err, lines)
	}
}
