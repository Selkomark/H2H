package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// landRepo makes a repository with one commit, an identity to commit with,
// and a state directory that git ignores, as H2H sets one up. It returns the
// root and the workspace of a session "s-1".
func landRepo(t *testing.T) (root, ws string) {
	t.Helper()
	root = newRepo(t)
	gitIn(t, root, "config", "user.name", "test")
	gitIn(t, root, "config", "user.email", "test@example.com")
	write(t, filepath.Join(root, ".h2h", ".gitignore"), "*\n")
	ws, err := Create(context.Background(), root, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	return root, ws
}

// gitIn runs git in a directory and returns what it printed.
func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	out, err := exec.Command("git", append([]string{"-C", dir}, args...)...).CombinedOutput()
	if err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
	return strings.TrimSpace(string(out))
}

func write(t *testing.T, path, text string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// commitIn commits everything in a working copy.
func commitIn(t *testing.T, dir, message string) {
	t.Helper()
	gitIn(t, dir, "add", "--all")
	gitIn(t, dir, "commit", "-q", "-m", message)
}

// land plans and lands the session's committed work.
func land(t *testing.T, root string) (Plan, error) {
	t.Helper()
	ctx := context.Background()
	p, err := PlanLanding(ctx, root, "s-1")
	if err != nil {
		t.Fatalf("PlanLanding: %v", err)
	}
	return p, Land(ctx, root, "s-1", p, "Merge work of s-1")
}

func TestTarget(t *testing.T) {
	ctx := context.Background()
	root, _ := landRepo(t)
	branch := gitIn(t, root, "symbolic-ref", "--short", "HEAD")
	if got, err := Target(ctx, root); err != nil || got != branch {
		t.Errorf("Target = %q, %v; want %q", got, err, branch)
	}

	gitIn(t, root, "checkout", "-q", "--detach")
	if _, err := Target(ctx, root); !errors.Is(err, ErrDetached) {
		t.Errorf("Target on a detached HEAD: %v, want ErrDetached", err)
	}
	gitIn(t, root, "checkout", "-q", branch)

	// A merge left unfinished in the project root.
	gitIn(t, root, "checkout", "-q", "-b", "other")
	write(t, filepath.Join(root, "a.txt"), "other\n")
	commitIn(t, root, "other")
	gitIn(t, root, "checkout", "-q", branch)
	write(t, filepath.Join(root, "a.txt"), "mine\n")
	commitIn(t, root, "mine")
	if err := exec.Command("git", "-C", root, "merge", "other").Run(); err == nil {
		t.Fatal("the test's merge did not conflict")
	}
	if _, err := Target(ctx, root); !errors.Is(err, ErrBusy) {
		t.Errorf("Target in the middle of a merge: %v, want ErrBusy", err)
	}
}

func TestNothingToLand(t *testing.T) {
	ctx := context.Background()
	root, _ := landRepo(t)
	u, err := Pending(ctx, root, "s-1")
	if err != nil || u.Any() {
		t.Errorf("Pending in a new workspace = %+v, %v; want nothing", u, err)
	}
	p, err := PlanLanding(ctx, root, "s-1")
	if err != nil || p.Commits != 0 || len(p.Files) != 0 {
		t.Errorf("PlanLanding in a new workspace = %+v, %v; want no commits and no files", p, err)
	}
}

func TestLandFastForwardFromDetachedHead(t *testing.T) {
	ctx := context.Background()
	root, ws := landRepo(t)
	write(t, filepath.Join(ws, "src", "main.go"), "package main\n")
	commitIn(t, ws, "add main")
	write(t, filepath.Join(ws, "a.txt"), "two\n")
	commitIn(t, ws, "change a")

	u, err := Pending(ctx, root, "s-1")
	if err != nil || u.Commits != 2 || len(u.Changes) != 0 || u.Branch != "" {
		t.Fatalf("Pending = %+v, %v; want 2 commits, no changes, no branch", u, err)
	}
	branch := gitIn(t, root, "symbolic-ref", "--short", "HEAD")
	p, err := land(t, root)
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	slices.Sort(p.Files)
	if p.Target != branch || p.Commits != 2 || !p.FastForward || !slices.Equal(p.Files, []string{"a.txt", "src/main.go"}) || !strings.Contains(p.Stat, "2 files changed") {
		t.Errorf("plan = %+v; want a fast-forward of 2 commits and 2 files onto %s", p, branch)
	}
	if got := gitIn(t, root, "rev-parse", "HEAD"); got != p.Head {
		t.Errorf("the branch is at %s, want the workspace's commit %s", got, p.Head)
	}
	if got := read(t, filepath.Join(root, "src", "main.go")); got != "package main\n" {
		t.Errorf("the landed file holds %q", got)
	}
	if got := gitIn(t, root, "symbolic-ref", "--short", "HEAD"); got != branch {
		t.Errorf("the project is on %q after landing, want %q", got, branch)
	}
	if got := gitIn(t, root, "branch", "--format=%(refname:short)"); got != branch {
		t.Errorf("branches after landing = %q; want only %q, none left behind", got, branch)
	}
	if u, err := Pending(ctx, root, "s-1"); err != nil || u.Any() {
		t.Errorf("Pending after landing = %+v, %v; want nothing", u, err)
	}
	if !Exists(root, "s-1") {
		t.Error("landing removed the workspace")
	}
}

func TestLandFromTheWorkspacesOwnBranch(t *testing.T) {
	ctx := context.Background()
	root, ws := landRepo(t)
	// What a harness does when asked to commit its work on a branch.
	gitIn(t, ws, "switch", "-q", "-c", "development-1")
	write(t, filepath.Join(ws, "new.txt"), "x\n")
	commitIn(t, ws, "add new")

	if u, err := Pending(ctx, root, "s-1"); err != nil || u.Commits != 1 || u.Branch != "development-1" {
		t.Fatalf("Pending = %+v, %v; want 1 commit on development-1", u, err)
	}
	if _, err := land(t, root); err != nil {
		t.Fatalf("Land: %v", err)
	}
	if got := read(t, filepath.Join(root, "new.txt")); got != "x\n" {
		t.Errorf("the landed file holds %q", got)
	}
}

func TestLandMergesWhenTheBranchMovedOn(t *testing.T) {
	root, ws := landRepo(t)
	write(t, filepath.Join(ws, "new.txt"), "from the harness\n")
	commitIn(t, ws, "harness work")
	write(t, filepath.Join(root, "other.txt"), "from the user\n")
	commitIn(t, root, "user work")
	before := gitIn(t, root, "rev-parse", "HEAD")

	p, err := land(t, root)
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	if p.FastForward || p.Commits != 1 || !slices.Equal(p.Files, []string{"new.txt"}) {
		t.Errorf("plan = %+v; want a merge of 1 commit changing only new.txt", p)
	}
	if got := gitIn(t, root, "log", "-1", "--format=%s"); got != "Merge work of s-1" {
		t.Errorf("the merge commit's message is %q", got)
	}
	if got := gitIn(t, root, "rev-list", "--parents", "-1", "HEAD"); !strings.Contains(got, before) || !strings.Contains(got, p.Head) {
		t.Errorf("the merge commit's parents are %q; want the branch %s and the work %s", got, before, p.Head)
	}
	for _, name := range []string{"new.txt", "other.txt"} {
		if _, err := os.Stat(filepath.Join(root, name)); err != nil {
			t.Errorf("%s is missing after the merge: %v", name, err)
		}
	}
}

func TestCommitTakesUncommittedWorkAndLeavesIgnoredFilesOut(t *testing.T) {
	ctx := context.Background()
	root, ws := landRepo(t)
	write(t, filepath.Join(ws, ".gitignore"), ".env\nbuild/\n")
	write(t, filepath.Join(ws, ".env"), "SECRET=1\n")
	write(t, filepath.Join(ws, "build", "out.bin"), "binary\n")
	write(t, filepath.Join(ws, "src", "app.py"), "print(1)\n")
	write(t, filepath.Join(ws, "a.txt"), "two\n")

	u, err := Pending(ctx, root, "s-1")
	if err != nil || u.Commits != 0 || len(u.Changes) != 3 {
		t.Fatalf("Pending = %+v, %v; want no commits and 3 changes (.gitignore, a.txt, src/)", u, err)
	}
	ignored, err := Ignored(ctx, root, "s-1")
	slices.Sort(ignored)
	if err != nil || !slices.Equal(ignored, []string{".env", "build/"}) {
		t.Errorf("Ignored = %q, %v; want .env and build/", ignored, err)
	}

	if err := Commit(ctx, root, "s-1", "Work of s-1"); err != nil {
		t.Fatalf("Commit: %v", err)
	}
	if u, err := Pending(ctx, root, "s-1"); err != nil || u.Commits != 1 || len(u.Changes) != 0 {
		t.Fatalf("Pending after Commit = %+v, %v; want 1 commit and no changes", u, err)
	}
	p, err := land(t, root)
	if err != nil {
		t.Fatalf("Land: %v", err)
	}
	slices.Sort(p.Files)
	if want := []string{".gitignore", "a.txt", "src/app.py"}; !slices.Equal(p.Files, want) {
		t.Errorf("landed files = %q, want %q", p.Files, want)
	}
	for _, name := range []string{".env", "build"} {
		if _, err := os.Stat(filepath.Join(root, name)); err == nil {
			t.Errorf("the ignored %s was landed", name)
		}
	}
	if _, err := os.Stat(filepath.Join(ws, ".env")); err != nil {
		t.Errorf("the ignored file was removed from the workspace: %v", err)
	}
}

func TestConflictIsUndone(t *testing.T) {
	root, ws := landRepo(t)
	write(t, filepath.Join(ws, "a.txt"), "the harness's line\n")
	commitIn(t, ws, "harness work")
	write(t, filepath.Join(root, "a.txt"), "the user's line\n")
	commitIn(t, root, "user work")
	before := gitIn(t, root, "rev-parse", "HEAD")

	_, err := land(t, root)
	var conflict *ConflictError
	if !errors.As(err, &conflict) || !slices.Equal(conflict.Files, []string{"a.txt"}) {
		t.Fatalf("Land of conflicting work: %v; want a ConflictError naming a.txt", err)
	}
	if got := gitIn(t, root, "rev-parse", "HEAD"); got != before {
		t.Errorf("the branch moved from %s to %s", before, got)
	}
	if got := gitIn(t, root, "status", "--porcelain"); got != "" {
		t.Errorf("the project is not clean after the merge was undone:\n%s", got)
	}
	if got := read(t, filepath.Join(root, "a.txt")); got != "the user's line\n" {
		t.Errorf("a.txt holds %q after the merge was undone", got)
	}
	if _, err := Target(context.Background(), root); err != nil {
		t.Errorf("the project is left in the middle of a merge: %v", err)
	}
}

func TestLandRefusesToOverwriteWorkInTheProject(t *testing.T) {
	tests := []struct {
		name string
		edit func(t *testing.T, root string) // what the user has, uncommitted, in the project
	}{
		{"a changed file", func(t *testing.T, root string) { write(t, filepath.Join(root, "a.txt"), "the user's edit\n") }},
		{"an untracked file", func(t *testing.T, root string) { write(t, filepath.Join(root, "new.txt"), "the user's file\n") }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root, ws := landRepo(t)
			write(t, filepath.Join(ws, "a.txt"), "two\n")
			write(t, filepath.Join(ws, "new.txt"), "x\n")
			commitIn(t, ws, "harness work")
			tt.edit(t, root)
			before, status := gitIn(t, root, "rev-parse", "HEAD"), gitIn(t, root, "status", "--porcelain")

			_, err := land(t, root)
			var refused *RefusedError
			if !errors.As(err, &refused) || refused.Reason == "" {
				t.Fatalf("Land over the user's uncommitted work: %v; want a RefusedError with git's reason", err)
			}
			if got := gitIn(t, root, "rev-parse", "HEAD"); got != before {
				t.Errorf("the branch moved from %s to %s", before, got)
			}
			if got := gitIn(t, root, "status", "--porcelain"); got != status {
				t.Errorf("the project's status changed from\n%s\nto\n%s", status, got)
			}
		})
	}
}

func TestLandKeepsUnrelatedWorkInTheProject(t *testing.T) {
	root, ws := landRepo(t)
	write(t, filepath.Join(ws, "new.txt"), "x\n")
	commitIn(t, ws, "harness work")
	write(t, filepath.Join(root, "a.txt"), "the user's edit\n")

	if _, err := land(t, root); err != nil {
		t.Fatalf("Land beside unrelated uncommitted work: %v", err)
	}
	if got := read(t, filepath.Join(root, "a.txt")); got != "the user's edit\n" {
		t.Errorf("the user's uncommitted edit became %q", got)
	}
}

func TestLandRefusesAStalePlan(t *testing.T) {
	ctx := context.Background()
	root, ws := landRepo(t)
	write(t, filepath.Join(ws, "new.txt"), "x\n")
	commitIn(t, ws, "harness work")
	p, err := PlanLanding(ctx, root, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	// The harness commits again after the user saw the plan.
	write(t, filepath.Join(ws, "more.txt"), "y\n")
	commitIn(t, ws, "more harness work")
	if err := Land(ctx, root, "s-1", p, "Merge work of s-1"); !errors.Is(err, ErrMoved) {
		t.Errorf("Land with a plan that is out of date: %v, want ErrMoved", err)
	}
	if _, err := os.Stat(filepath.Join(root, "new.txt")); err == nil {
		t.Error("a stale plan was landed")
	}
}
