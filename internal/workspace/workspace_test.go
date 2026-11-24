package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"testing"
)

// newRepo makes a git repository with one commit and returns its root.
func newRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir, "-c", "user.name=test", "-c", "user.email=test@example.com"}, args...)...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	run("init", "-q")
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-q", "-m", "init")
	root, err := Root(context.Background(), dir)
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestGitVersion(t *testing.T) {
	if v, err := GitVersion(context.Background()); err != nil || v == "" {
		t.Errorf("GitVersion() = %q, %v", v, err)
	}
}

func TestReady(t *testing.T) {
	ctx := context.Background()
	if err := Ready(ctx, newRepo(t)); err != nil {
		t.Errorf("Ready with a commit: %v", err)
	}
	empty := t.TempDir()
	if out, err := exec.Command("git", "-C", empty, "init", "-q").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	if err := Ready(ctx, empty); err == nil {
		t.Error("Ready without a commit succeeded, want error")
	}
}

func TestRootOutsideARepository(t *testing.T) {
	if _, err := Root(context.Background(), t.TempDir()); err == nil {
		t.Error("Root outside a repository succeeded, want error")
	}
}

func TestCreateAndRemoveClean(t *testing.T) {
	ctx, root := context.Background(), newRepo(t)
	path, err := Create(ctx, root, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if path != Path(root, "s-1") || !Exists(root, "s-1") {
		t.Fatalf("Create returned %q, Exists = %v; want the workspace at %q", path, Exists(root, "s-1"), Path(root, "s-1"))
	}
	if _, err := os.Stat(filepath.Join(path, "a.txt")); err != nil {
		t.Errorf("the workspace does not hold the project's files: %v", err)
	}
	changes, err := Changes(ctx, root, "s-1")
	if err != nil || len(changes) != 0 {
		t.Errorf("Changes in a new workspace = %v, %v; want none", changes, err)
	}
	if err := Remove(ctx, root, "s-1", false); err != nil {
		t.Fatalf("Remove of a clean workspace: %v", err)
	}
	if Exists(root, "s-1") {
		t.Error("the workspace still exists after Remove")
	}
}

func TestDirtyWorkspaceNeedsForce(t *testing.T) {
	ctx, root := context.Background(), newRepo(t)
	path, err := Create(ctx, root, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	// One modified file, one new file and one new folder.
	if err := os.WriteFile(filepath.Join(path, "a.txt"), []byte("two\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "new.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(path, "dir"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "dir", "f"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	changes, err := Changes(ctx, root, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	slices.Sort(changes)
	if want := []string{"a.txt", "dir/", "new.txt"}; !slices.Equal(changes, want) {
		t.Errorf("Changes = %q, want %q", changes, want)
	}

	if err := Remove(ctx, root, "s-1", false); err == nil {
		t.Fatal("Remove without force deleted a workspace with uncommitted changes")
	}
	if _, err := os.Stat(filepath.Join(path, "new.txt")); err != nil {
		t.Fatalf("a refused Remove damaged the workspace: %v", err)
	}
	if err := Remove(ctx, root, "s-1", true); err != nil {
		t.Fatalf("Remove with force: %v", err)
	}
	if Exists(root, "s-1") {
		t.Error("the workspace still exists after a forced Remove")
	}
}

func TestRemoveWhenFolderIsGone(t *testing.T) {
	ctx, root := context.Background(), newRepo(t)
	path, err := Create(ctx, root, "s-1")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.RemoveAll(path); err != nil {
		t.Fatal(err)
	}
	if err := Remove(ctx, root, "s-1", false); err != nil {
		t.Errorf("Remove of a workspace whose folder is gone: %v", err)
	}
	// Git has forgotten it, so the same ID can be used again.
	if _, err := Create(ctx, root, "s-1"); err != nil {
		t.Errorf("Create after the folder was deleted by hand: %v", err)
	}
}

func TestFolderThatIsNotAWorktree(t *testing.T) {
	ctx, root := context.Background(), newRepo(t)
	path := Path(root, "s-1")
	if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(path, "f"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Changes(ctx, root, "s-1"); !errors.Is(err, ErrNotWorktree) {
		t.Errorf("Changes = %v, want ErrNotWorktree", err)
	}
	if err := Remove(ctx, root, "s-1", false); !errors.Is(err, ErrNotWorktree) {
		t.Errorf("Remove without force = %v, want ErrNotWorktree", err)
	}
	if !Exists(root, "s-1") {
		t.Fatal("a refused Remove deleted the folder")
	}
	if err := Remove(ctx, root, "s-1", true); err != nil || Exists(root, "s-1") {
		t.Errorf("Remove with force = %v, Exists = %v; want the folder gone", err, Exists(root, "s-1"))
	}
}
