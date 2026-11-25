package keeper

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/session"
)

func TestStateHooksAreWrittenIntoTheSessionsOwnFolder(t *testing.T) {
	root := t.TempDir()
	h := config.Harness{
		Command:   "x",
		StateArgs: []string{"--plugin-dir", "{state_dir}", "-c", `hooks.Stop=[{command="'{state}' go"}]`},
		StateEnv:  []string{"HOOKS_DIR={state_dir}"},
		StateFiles: map[string]string{
			"hooks/hooks.json": `{"command":"'{state}' wait"}`,
		},
	}
	args, env, err := stateHooks(root, "x-1", h)
	if err != nil {
		t.Fatal(err)
	}
	dir, program := StateDir(root, "x-1"), StatePath(root, "x-1")
	if args[1] != dir {
		t.Errorf("a bare {state_dir} = %q, want the folder as it is: %q", args[1], dir)
	}
	if want := `hooks.Stop=[{command="'` + program + `' go"}]`; args[3] != want {
		t.Errorf("an argument with {state} in it = %q, want %q", args[3], want)
	}
	if len(env) != 1 || env[0] != "HOOKS_DIR="+dir {
		t.Errorf("env = %q", env)
	}
	data, err := os.ReadFile(filepath.Join(dir, "hooks", "hooks.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"command":"'` + program + `' wait"}`; string(data) != want {
		t.Errorf("hooks file = %s, want %s", data, want)
	}
	if !strings.HasPrefix(dir, session.Dir(root, "x-1")) {
		t.Errorf("the state folder %s is not in the session's own directory", dir)
	}

	// A harness without state settings gets nothing.
	if args, env, err := stateHooks(root, "y-1", config.Harness{Command: "y"}); args != nil || env != nil || err != nil {
		t.Errorf("no state settings: %v, %v, %v", args, env, err)
	}
}

func TestShimsIncludeTheStateProgram(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(session.Dir(root, "x-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := reportShim(root, "x-1"); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(StatePath(root, "x-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), " state \"$@\"") || !strings.Contains(string(data), "H2H_SESSION='x-1'") {
		t.Errorf("h2h-state = %s; want it to run this binary's state command for its session", data)
	}
}
