package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

// landFixture is a project with one stopped harness that has a workspace.
type landFixture struct {
	t    *testing.T
	root string
	ws   string
	id   string
	out  bytes.Buffer
	ui   *scriptedUI
	e    *env
}

func newLandFixture(t *testing.T) *landFixture {
	t.Helper()
	f := &landFixture{t: t, root: t.TempDir(), ui: &scriptedUI{}}
	f.git(f.root, "init", "-q")
	f.git(f.root, "config", "user.name", "test")
	f.git(f.root, "config", "user.email", "test@example.com")
	f.write(f.root, "a.txt", "one\n")
	f.git(f.root, "add", ".")
	f.git(f.root, "commit", "-q", "-m", "init")
	// The root as git reports it, which on macOS differs from TempDir's.
	root, err := workspace.Root(context.Background(), f.root, "h2h.toml")
	if err != nil {
		t.Fatal(err)
	}
	f.root = root

	info, err := session.Create(f.root, "dev")
	if err != nil {
		t.Fatal(err)
	}
	if info.Workspace, err = workspace.Create(context.Background(), f.root, info.ID); err != nil {
		t.Fatal(err)
	}
	info.Harness, info.State = "fake", session.Exited
	if err := session.Save(f.root, info); err != nil {
		t.Fatal(err)
	}
	f.ws, f.id = info.Workspace, info.ID
	f.e = &env{root: f.root, out: &f.out, errw: &f.out, ui: f.ui, st: plainStyles()}
	return f
}

func (f *landFixture) git(dir string, args ...string) string {
	f.t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		f.t.Fatalf("git %v: %v\n%s", args, err, out)
	}
	return strings.TrimSpace(string(out))
}

func (f *landFixture) write(dir, name, text string) {
	f.t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

// commit commits everything in the workspace, as the harness would.
func (f *landFixture) commit(message string) {
	f.t.Helper()
	f.git(f.ws, "add", "--all")
	f.git(f.ws, "commit", "-q", "-m", message)
}

// land runs the command with the given answers to its questions, in order.
func (f *landFixture) land(answers ...bool) error {
	f.out.Reset()
	f.ui.asked, f.ui.confirms, f.ui.yes = "", answers, false
	return f.e.land(context.Background(), []string{f.id})
}

func (f *landFixture) inProject(name string) bool {
	_, err := os.Stat(filepath.Join(f.root, name))
	return err == nil
}

func TestLandNothing(t *testing.T) {
	f := newLandFixture(t)
	if err := f.land(); err != nil {
		t.Fatal(err)
	}
	if f.ui.asked != "" || !strings.Contains(f.out.String(), "has nothing to land") {
		t.Errorf("with nothing to land: asked %q, printed %q; want a plain message and no question", f.ui.asked, f.out.String())
	}
}

func TestLandCommittedWork(t *testing.T) {
	f := newLandFixture(t)
	branch := f.git(f.root, "symbolic-ref", "--short", "HEAD")
	f.write(f.ws, "src/app.go", "package app\n")
	f.write(f.ws, ".gitignore", ".env\n")
	f.write(f.ws, ".env", "SECRET=1\n")
	f.commit("add the app")

	// Declining the plan leaves the project as it was.
	if err := f.land(false); err != nil {
		t.Fatal(err)
	}
	if f.ui.asked != "Land this on "+branch+"?" {
		t.Errorf("asked %q, want to be asked about landing on %s", f.ui.asked, branch)
	}
	for _, want := range []string{"Landing dev-1 on " + branch, "1 commit: 2 files changed", "as a fast-forward", "src/app.go", "Nothing was landed"} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, f.out.String())
		}
	}
	if f.inProject("src/app.go") {
		t.Fatal("the work was landed although the user declined")
	}

	if err := f.land(true); err != nil {
		t.Fatal(err)
	}
	if !f.inProject("src/app.go") {
		t.Error("the work is not in the project after landing")
	}
	if f.inProject(".env") {
		t.Error("an ignored file was landed")
	}
	for _, want := range []string{"Landed dev-1 on " + branch + ": 1 commit", "because git ignores them: .env", "Nothing was pushed", "rm dev-1"} {
		if !strings.Contains(f.out.String(), want) {
			t.Errorf("output lacks %q:\n%s", want, f.out.String())
		}
	}
	if _, err := session.Load(f.root, f.id); err != nil || !workspace.Exists(f.root, f.id) {
		t.Errorf("landing removed the harness or its workspace: %v", err)
	}

	// A second landing finds nothing left.
	if err := f.land(); err != nil || !strings.Contains(f.out.String(), "has nothing to land") {
		t.Errorf("landing again: %v, %q", err, f.out.String())
	}
}

func TestLandUncommittedWork(t *testing.T) {
	f := newLandFixture(t)
	f.write(f.ws, "new.txt", "x\n")
	f.write(f.ws, "a.txt", "two\n")

	// Declining the commit changes nothing anywhere.
	if err := f.land(false); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(f.ui.asked, "Commit these 2 changes in the workspace of dev-1 first?") {
		t.Errorf("asked %q, want to be asked about committing the 2 changes", f.ui.asked)
	}
	if !strings.Contains(f.out.String(), "The workspace of dev-1 has 2 uncommitted changes") || !strings.Contains(f.out.String(), "Nothing was landed") {
		t.Errorf("output:\n%s", f.out.String())
	}
	if got := f.git(f.ws, "status", "--porcelain"); got == "" {
		t.Fatal("the changes were committed although the user declined")
	}

	// Agreeing to the commit and to the plan lands them.
	if err := f.land(true, true); err != nil {
		t.Fatal(err)
	}
	if !f.inProject("new.txt") {
		t.Error("the work is not in the project after landing")
	}
	if got := f.git(f.root, "log", "-1", "--format=%s"); got != "Work of dev-1 (fake)" {
		t.Errorf("the commit H2H made is called %q", got)
	}
	if got := f.git(f.root, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("the project has uncommitted changes after landing:\n%s", got)
	}
}

func TestLandConflictLeavesTheProjectAlone(t *testing.T) {
	f := newLandFixture(t)
	f.write(f.ws, "a.txt", "the harness's line\n")
	f.commit("harness work")
	f.write(f.root, "a.txt", "the user's line\n")
	f.git(f.root, "commit", "-q", "-am", "user work")
	before := f.git(f.root, "rev-parse", "HEAD")

	err := f.land(true)
	if err == nil || !strings.Contains(err.Error(), "conflicts with") || !strings.Contains(err.Error(), "a.txt") || !strings.Contains(err.Error(), "The merge was undone") {
		t.Fatalf("landing conflicting work: %v; want an error that names the file and says the merge was undone", err)
	}
	if !strings.Contains(f.out.String(), "with a merge commit") {
		t.Errorf("the plan did not say a merge commit was needed:\n%s", f.out.String())
	}
	if got := f.git(f.root, "rev-parse", "HEAD"); got != before {
		t.Errorf("the branch moved from %s to %s", before, got)
	}
	if got := f.git(f.root, "status", "--porcelain", "--untracked-files=no"); got != "" {
		t.Errorf("the project is not clean after the failed landing:\n%s", got)
	}
}

func TestLandNeedsABranch(t *testing.T) {
	f := newLandFixture(t)
	f.write(f.ws, "new.txt", "x\n")
	f.commit("harness work")
	f.git(f.root, "checkout", "-q", "--detach")
	if err := f.land(true); err == nil || !strings.Contains(err.Error(), "not on a branch") {
		t.Errorf("landing with the project on no branch: %v", err)
	}
	if f.ui.asked != "" {
		t.Errorf("asked %q before finding out there is nowhere to land", f.ui.asked)
	}
}

func TestRmWarnsAboutUnlandedCommits(t *testing.T) {
	f := newLandFixture(t)
	f.write(f.ws, "new.txt", "x\n")
	f.commit("harness work")

	// The workspace is clean, yet its commit is on no branch.
	f.ui.confirms = []bool{false}
	if err := f.e.rm(context.Background(), []string{f.id}); err != nil {
		t.Fatal(err)
	}
	if f.ui.asked != "dev-1 has 1 unlanded commit that will be lost. Delete it anyway?" {
		t.Errorf("rm asked %q", f.ui.asked)
	}
	if !strings.Contains(f.out.String(), "land dev-1") {
		t.Errorf("rm did not point at land:\n%s", f.out.String())
	}
	if !workspace.Exists(f.root, f.id) {
		t.Fatal("the workspace was removed although the user declined")
	}

	// Once landed, nothing is at risk and rm does not ask.
	if err := f.land(true); err != nil {
		t.Fatal(err)
	}
	f.ui.asked, f.ui.confirms = "", nil
	if err := f.e.rm(context.Background(), []string{f.id}); err != nil {
		t.Fatal(err)
	}
	if f.ui.asked != "" || workspace.Exists(f.root, f.id) {
		t.Errorf("rm after landing: asked %q, workspace exists = %v; want no question and the workspace gone", f.ui.asked, workspace.Exists(f.root, f.id))
	}
	if !f.inProject("new.txt") {
		t.Error("the landed work left the project when its workspace was removed")
	}
}

func TestAtRisk(t *testing.T) {
	tests := []struct {
		pending workspace.Unlanded
		want    string
	}{
		{workspace.Unlanded{}, ""},
		{workspace.Unlanded{Changes: []string{"a"}}, "1 uncommitted change"},
		{workspace.Unlanded{Commits: 2}, "2 unlanded commits"},
		{workspace.Unlanded{Commits: 2, Branch: "dev-1"}, ""}, // the branch keeps them
		{workspace.Unlanded{Commits: 1, Changes: []string{"a", "b"}}, "2 uncommitted changes and 1 unlanded commit"},
	}
	for _, tt := range tests {
		if got := atRisk(tt.pending); got != tt.want {
			t.Errorf("atRisk(%+v) = %q, want %q", tt.pending, got, tt.want)
		}
	}
}

func TestLandSuggestsOnlyHarnessesWithWork(t *testing.T) {
	f := newLandFixture(t)
	if got := f.e.argumentSuggestions("land"); len(got) != 0 {
		t.Errorf("suggestions with nothing to land = %v", got)
	}
	f.write(f.ws, "new.txt", "x\n")
	f.commit("harness work")
	f.write(f.ws, "more.txt", "y\n")
	want := []console.Suggestion{{Text: "dev-1", Help: "1 commit, 1 uncommitted change"}}
	if got := f.e.argumentSuggestions("land"); len(got) != 1 || got[0] != want[0] {
		t.Errorf("suggestions = %v, want %v", got, want)
	}
}

func TestListed(t *testing.T) {
	if got := listed([]string{"a", "b"}); got != "a, b" {
		t.Errorf("listed of two = %q", got)
	}
	if got := listed([]string{"a", "b", "c", "d", "e", "f", "g"}); got != "a, b, c, d, e and 2 more" {
		t.Errorf("listed of seven = %q", got)
	}
}

func TestLandAsksBeforeTakingFromARunningHarness(t *testing.T) {
	root, id, done := runningSession(t)
	ws := workspace.Path(root, id)
	if err := os.WriteFile(filepath.Join(ws, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"config", "user.name", "test"}, {"config", "user.email", "test@example.com"}} {
		if out, err := exec.Command("git", append([]string{"-C", root}, args...)...).CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	var out bytes.Buffer
	ui := &scriptedUI{confirms: []bool{false}}
	e := &env{root: root, out: &out, errw: &out, ui: ui, st: plainStyles()}

	if err := e.land(context.Background(), []string{id}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(ui.asked, id+" is still running") {
		t.Errorf("asked %q, want to be asked about landing from a running harness first", ui.asked)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Fatal("the work was landed although the user declined")
	}

	// Yes to all three questions: running, commit, plan.
	ui.confirms = []bool{true, true, true}
	if err := e.land(context.Background(), []string{id}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err != nil {
		t.Errorf("the work is not in the project after landing: %v", err)
	}
	if got, err := session.Load(root, id); err != nil || got.State != session.Running {
		t.Errorf("after landing: %+v, %v; want the harness still running", got, err)
	}

	if err := (&env{root: root, out: &out, errw: &out, ui: &scriptedUI{yes: true}, st: plainStyles()}).stop(context.Background(), []string{id}); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatalf("keeper: %v", err)
	}
}
