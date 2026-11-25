package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/keeper"
	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

// runningSession starts a keeper in this process, holding a shell that
// waits for input, in a worktree of a new temporary repository.
func runningSession(t *testing.T) (root, id string, done <-chan error) {
	t.Helper()
	root = t.TempDir()
	for _, args := range [][]string{
		{"init", "-q"},
		{"-c", "user.name=t", "-c", "user.email=t@example.com", "commit", "-q", "--allow-empty", "-m", "init"},
	} {
		cmd := exec.Command("git", append([]string{"-C", root}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	conf := "default_harness = \"fake\"\n[harness.fake]\ncommand = \"sh\"\nno_trust_question = true\nargs = [\"-c\", \"read line\"]\n"
	if err := os.WriteFile(config.Path(root), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := session.Create(root, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if info.Workspace, err = workspace.Create(context.Background(), root, info.ID); err != nil {
		t.Fatal(err)
	}
	if err := session.Save(root, info); err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 1)
	go func() { ch <- keeper.Run(root, info.ID) }()
	deadline := time.Now().Add(10 * time.Second)
	for {
		got, err := session.Load(root, info.ID)
		if err == nil && got.State == session.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("session did not start: %+v, %v", got, err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	return root, info.ID, ch
}

func TestRmOffersToStopARunningSession(t *testing.T) {
	root, id, done := runningSession(t)
	var out bytes.Buffer
	ui := &scriptedUI{confirms: []bool{false}}
	e := &env{root: root, out: &out, errw: &out, ui: ui, st: plainStyles()}

	// Declining leaves everything as it was.
	if err := e.rm(context.Background(), []string{id}); err != nil {
		t.Fatalf("rm, declined: %v", err)
	}
	if !strings.Contains(ui.asked, id+" is running. Stop it and delete it?") {
		t.Errorf("question = %q; want to be asked about stopping the running session", ui.asked)
	}
	if got, err := session.Load(root, id); err != nil || got.State != session.Running {
		t.Fatalf("after declining: %+v, %v; want the session still running", got, err)
	}
	if !strings.Contains(out.String(), "Left "+id+" running.") {
		t.Errorf("output = %q; want it to say the session was left running", out.String())
	}

	// Agreeing stops it and removes it with its workspace.
	out.Reset()
	ui.confirms = []bool{true}
	if err := e.rm(context.Background(), []string{id}); err != nil {
		t.Fatalf("rm, confirmed: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("keeper: %v", err)
	}
	if _, err := session.Load(root, id); err == nil {
		t.Error("the session still exists after a confirmed rm")
	}
	if workspace.Exists(root, id) {
		t.Error("the workspace still exists after a confirmed rm")
	}
	if got := out.String(); !strings.Contains(got, "Stopped "+id+".") || !strings.Contains(got, "Removed "+id+".") {
		t.Errorf("output = %q; want it to report the stop and the removal", got)
	}
}

func TestStopAllAndRmAll(t *testing.T) {
	root, id, done := runningSession(t)
	// A second harness that has already ended, with work left in its workspace.
	old, err := session.Create(root, "fake")
	if err != nil {
		t.Fatal(err)
	}
	if old.Workspace, err = workspace.Create(context.Background(), root, old.ID); err != nil {
		t.Fatal(err)
	}
	old.State = session.Exited
	if err := session.Save(root, old); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(old.Workspace+"/notes.txt", []byte("unsaved"), 0o644); err != nil {
		t.Fatal(err)
	}

	var out bytes.Buffer
	ui := &scriptedUI{}
	e := &env{root: root, out: &out, errw: &out, ui: ui, st: plainStyles()}
	ctx := context.Background()

	// Declining either question changes nothing.
	if err := e.stop(ctx, []string{"all"}); err != nil {
		t.Fatalf("stop all, declined: %v", err)
	}
	if ui.asked != "Stop the 1 running harness?" {
		t.Errorf("stop all asked %q", ui.asked)
	}
	if err := e.rm(ctx, []string{"all"}); err != nil {
		t.Fatalf("rm all, declined: %v", err)
	}
	want := "Delete all 2 harnesses and their workspaces? 1 is running and will be stopped. 1 has uncommitted changes that will be lost."
	if ui.asked != want {
		t.Errorf("rm all asked\n %q\nwant\n %q", ui.asked, want)
	}
	if got, err := session.Load(root, id); err != nil || got.State != session.Running {
		t.Fatalf("after declining both: %+v, %v; want the harness still running", got, err)
	}
	if infos, _ := session.List(root); len(infos) != 2 {
		t.Fatalf("after declining both: %d harnesses, want 2", len(infos))
	}

	// Stop all stops the running one and leaves both in the list.
	ui.yes = true
	if err := e.stop(ctx, []string{"all"}); err != nil {
		t.Fatalf("stop all: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("keeper: %v", err)
	}
	if got, err := session.Load(root, id); err != nil || got.Live() {
		t.Errorf("after stop all: %+v, %v; want it stopped", got, err)
	}
	out.Reset()
	if err := e.stop(ctx, []string{"all"}); err != nil || !strings.Contains(out.String(), "No harness is running.") {
		t.Errorf("stop all with nothing running: %v, %q", err, out.String())
	}

	// Rm all removes both, including the workspace with unsaved work.
	if err := e.rm(ctx, []string{"all"}); err != nil {
		t.Fatalf("rm all: %v", err)
	}
	if infos, _ := session.List(root); len(infos) != 0 {
		t.Errorf("after rm all: %d harnesses left, want none", len(infos))
	}
	if workspace.Exists(root, id) || workspace.Exists(root, old.ID) {
		t.Error("after rm all: a workspace still exists")
	}
}

func TestAllOf(t *testing.T) {
	tests := []struct {
		n    int
		noun string
		want string
	}{
		{1, "harness", "the 1 harness"},
		{3, "harness", "all 3 harnesses"},
		{2, "running harness", "all 2 running harnesses"},
	}
	for _, tt := range tests {
		if got := allOf(tt.n, tt.noun); got != tt.want {
			t.Errorf("allOf(%d, %q) = %q, want %q", tt.n, tt.noun, got, tt.want)
		}
	}
}
