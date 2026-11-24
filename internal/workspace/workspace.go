// Package workspace gives each session its own working copy of the project,
// as a git worktree inside the project's state directory.
package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Root returns the top of the git repository that contains dir.
func Root(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errors.New("h2h must be run inside a git repository")
	}
	return strings.TrimSpace(out), nil
}

// The oldest git that has every worktree command H2H uses.
const minGitMajor, minGitMinor = 2, 17

// GitVersion returns the version of the installed git, and an error when
// git is missing or too old to manage worktrees the way H2H does.
func GitVersion(ctx context.Context) (string, error) {
	out, err := exec.CommandContext(ctx, "git", "version").Output()
	if err != nil {
		return "", errors.New("git is not installed or not on PATH")
	}
	version := strings.TrimPrefix(strings.TrimSpace(string(out)), "git version ")
	var major, minor int
	if _, err := fmt.Sscanf(version, "%d.%d", &major, &minor); err != nil {
		return version, nil // an unusual version string is not a reason to refuse
	}
	if major < minGitMajor || (major == minGitMajor && minor < minGitMinor) {
		return version, fmt.Errorf("git %s is too old; H2H needs %d.%d or later", version, minGitMajor, minGitMinor)
	}
	return version, nil
}

// Ready reports whether a workspace can be created for the project. A
// workspace is a copy of the current commit, so there has to be one.
func Ready(ctx context.Context, root string) error {
	if _, err := git(ctx, root, "rev-parse", "--verify", "--quiet", "HEAD^{commit}"); err != nil {
		return errors.New("the repository has no commit yet")
	}
	return nil
}

// Path is where the workspace of a session lives.
func Path(root, id string) string {
	return filepath.Join(root, ".h2h", "workspaces", id)
}

// Create adds a worktree for the session at the project's current commit.
func Create(ctx context.Context, root, id string) (string, error) {
	path := Path(root, id)
	if _, err := git(ctx, root, "worktree", "add", "--detach", path); err != nil {
		return "", fmt.Errorf("creating workspace: %w", err)
	}
	return path, nil
}

// ErrNotWorktree means the workspace folder exists but git no longer
// knows it as a worktree of the project.
var ErrNotWorktree = errors.New("the workspace is not a git worktree of this project")

// Exists reports whether the session's workspace folder is there.
func Exists(root, id string) bool {
	_, err := os.Stat(Path(root, id))
	return err == nil
}

// Changes lists what in the session's workspace is not committed: modified,
// staged and untracked paths, relative to the workspace. An empty result
// means removing the workspace loses nothing.
func Changes(ctx context.Context, root, id string) ([]string, error) {
	ok, err := registered(ctx, root, id)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrNotWorktree
	}
	out, err := git(ctx, Path(root, id), "status", "--porcelain")
	if err != nil {
		return nil, fmt.Errorf("checking workspace: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		// Each line is a two-letter status, a space and the path.
		if len(line) > 3 {
			paths = append(paths, line[3:])
		}
	}
	return paths, nil
}

// Remove deletes the session's workspace. Without force it refuses a
// workspace that holds uncommitted work, or one that is no longer a
// worktree. With force the folder is deleted whatever it holds.
func Remove(ctx context.Context, root, id string, force bool) error {
	path := Path(root, id)
	if !Exists(root, id) {
		// Nothing to delete. Let git forget a worktree whose folder is gone.
		_, _ = git(ctx, root, "worktree", "prune")
		return nil
	}
	ok, err := registered(ctx, root, id)
	if err != nil {
		return err
	}
	if !ok {
		if !force {
			return ErrNotWorktree
		}
		return os.RemoveAll(path)
	}
	args := []string{"worktree", "remove", path}
	if force {
		args = []string{"worktree", "remove", "--force", path}
	}
	if _, err := git(ctx, root, args...); err != nil {
		return fmt.Errorf("removing workspace: %w", err)
	}
	return nil
}

// registered reports whether git lists the session's workspace as a
// worktree of the project.
func registered(ctx context.Context, root, id string) (bool, error) {
	out, err := git(ctx, root, "worktree", "list", "--porcelain")
	if err != nil {
		return false, fmt.Errorf("listing worktrees: %w", err)
	}
	want := resolve(Path(root, id))
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "worktree "); ok && resolve(path) == want {
			return true, nil
		}
	}
	return false, nil
}

// resolve follows symbolic links, so the same folder always compares equal
// however it was reached.
func resolve(path string) string {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return real
	}
	return filepath.Clean(path)
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
