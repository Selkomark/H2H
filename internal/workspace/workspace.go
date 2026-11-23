// Package workspace gives each session its own working copy of the project,
// as a git worktree inside the project's state directory.
package workspace

import (
	"context"
	"errors"
	"fmt"
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

// Remove deletes the session's worktree. Git refuses when it holds changes
// that are not committed, and that refusal is returned as is.
func Remove(ctx context.Context, root, id string) error {
	if _, err := git(ctx, root, "worktree", "remove", Path(root, id)); err != nil {
		return fmt.Errorf("removing workspace: %w", err)
	}
	return nil
}

func git(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, "git", append([]string{"-C", dir}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("git %s: %s", args[0], strings.TrimSpace(string(out)))
	}
	return string(out), nil
}
