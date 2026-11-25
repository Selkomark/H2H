package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

// Landing is bringing the work in a session's workspace onto the branch
// checked out in the project root. It is plain git, done from the project
// root, so it is the same whatever harness did the work.
//
// The workspace's commit is merged by its hash. A workspace starts on a
// detached HEAD and a harness may or may not have made a branch there, and
// a hash covers both without leaving a branch behind.

var (
	// ErrDetached means the project root has no branch checked out, so
	// there is nothing to land work on.
	ErrDetached = errors.New("the project is not on a branch")
	// ErrBusy means the project root is in the middle of a merge, rebase
	// or similar, which has to be finished first.
	ErrBusy = errors.New("the project is in the middle of another git operation")
	// ErrMoved means the project or the workspace changed between the
	// plan and the landing.
	ErrMoved = errors.New("the project or the workspace changed since the plan was made")
)

// ConflictError means the work could not be merged because it changes the
// same lines as the branch. The merge was undone.
type ConflictError struct {
	Files []string // the paths that conflict
}

func (e *ConflictError) Error() string {
	return "the work conflicts with the branch in: " + strings.Join(e.Files, ", ")
}

// RefusedError means git would not start the merge, which is what it does
// when files in the project root that are changed or untracked would be
// overwritten. Nothing was changed.
type RefusedError struct {
	Reason string // what git said
}

func (e *RefusedError) Error() string { return "git would not start the merge: " + e.Reason }

// Unlanded is what a workspace holds that its project's branch does not.
type Unlanded struct {
	Commits int      // commits in the workspace that the branch lacks
	Changes []string // uncommitted and untracked paths; ignored files are not among them
	Branch  string   // the branch the workspace is on, or "" on a detached HEAD
}

// Any reports whether there is anything to land.
func (u Unlanded) Any() bool { return u.Commits > 0 || len(u.Changes) > 0 }

// Plan is a landing worked out and not yet done.
type Plan struct {
	Target      string   // the branch in the project root
	Base        string   // the commit the branch is at
	Head        string   // the commit of the workspace that will be landed
	Commits     int      // how many commits that brings
	Files       []string // the paths those commits change
	Stat        string   // git's one-line summary of the change
	FastForward bool     // the branch has not moved, so no merge commit is needed
}

// Target returns the branch checked out in the project root, which is where
// work is landed.
func Target(ctx context.Context, root string) (string, error) {
	// In the middle of a rebase the root is on no branch, so this is asked
	// first: it is the more useful thing to tell the user.
	for _, mark := range []string{"MERGE_HEAD", "CHERRY_PICK_HEAD", "REVERT_HEAD", "rebase-merge", "rebase-apply"} {
		path, err := git(ctx, root, "rev-parse", "--git-path", mark)
		if err != nil {
			return "", err
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		if _, err := os.Stat(path); err == nil {
			return "", ErrBusy
		}
	}
	out, err := git(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	if err != nil {
		return "", ErrDetached
	}
	return strings.TrimSpace(out), nil
}

// Pending reports what the session's workspace holds that the branch in the
// project root does not.
func Pending(ctx context.Context, root, id string) (Unlanded, error) {
	changes, err := Changes(ctx, root, id)
	if err != nil {
		return Unlanded{}, err
	}
	u := Unlanded{Changes: changes}
	head, err := head(ctx, Path(root, id))
	if err != nil {
		return Unlanded{}, err
	}
	if u.Commits, err = count(ctx, root, "HEAD.."+head); err != nil {
		return Unlanded{}, err
	}
	if out, err := git(ctx, Path(root, id), "symbolic-ref", "--quiet", "--short", "HEAD"); err == nil {
		u.Branch = strings.TrimSpace(out)
	}
	return u, nil
}

// Commit records everything uncommitted in the session's workspace as one
// commit. Files that git ignores stay out of it.
func Commit(ctx context.Context, root, id, message string) error {
	ws := Path(root, id)
	if _, err := git(ctx, ws, "add", "--all"); err != nil {
		return fmt.Errorf("staging the workspace: %w", err)
	}
	if _, err := git(ctx, ws, "commit", "--quiet", "-m", message); err != nil {
		return fmt.Errorf("committing the workspace: %w", err)
	}
	return nil
}

// Ignored lists the paths in the session's workspace that git ignores,
// which landing therefore leaves behind. A folder is listed once, not file
// by file.
func Ignored(ctx context.Context, root, id string) ([]string, error) {
	out, err := git(ctx, Path(root, id), "status", "--porcelain", "--ignored")
	if err != nil {
		return nil, fmt.Errorf("checking workspace: %w", err)
	}
	var paths []string
	for _, line := range strings.Split(out, "\n") {
		if path, ok := strings.CutPrefix(line, "!! "); ok {
			paths = append(paths, path)
		}
	}
	return paths, nil
}

// PlanLanding works out what landing the session's committed work would do,
// without changing anything. Uncommitted work is not part of it.
func PlanLanding(ctx context.Context, root, id string) (Plan, error) {
	target, err := Target(ctx, root)
	if err != nil {
		return Plan{}, err
	}
	p := Plan{Target: target}
	if p.Base, err = head(ctx, root); err != nil {
		return Plan{}, err
	}
	if p.Head, err = head(ctx, Path(root, id)); err != nil {
		return Plan{}, err
	}
	if p.Commits, err = count(ctx, root, p.Base+".."+p.Head); err != nil {
		return Plan{}, err
	}
	// Three dots: what the workspace changed since it parted from the
	// branch, which is what landing brings, whatever the branch did since.
	names, err := git(ctx, root, "diff", "--name-only", p.Base+"..."+p.Head)
	if err != nil {
		return Plan{}, err
	}
	if names = strings.TrimSpace(names); names != "" {
		p.Files = strings.Split(names, "\n")
	}
	stat, err := git(ctx, root, "diff", "--shortstat", p.Base+"..."+p.Head)
	if err != nil {
		return Plan{}, err
	}
	p.Stat = strings.TrimSpace(stat)
	_, err = git(ctx, root, "merge-base", "--is-ancestor", p.Base, p.Head)
	p.FastForward = err == nil
	return p, nil
}

// Land carries out a plan in the project root: a fast-forward when the
// branch has not moved, and otherwise a merge commit with the given
// message. It does not push, and leaves the workspace as it is.
//
// When the merge cannot be done the project root is left exactly as it
// was: git either refuses before touching anything (RefusedError), or the
// merge is undone (ConflictError).
func Land(ctx context.Context, root, id string, p Plan, message string) error {
	// The plan is what the user agreed to. If anything moved since, it is
	// no longer what would happen.
	target, err := Target(ctx, root)
	if err != nil {
		return err
	}
	base, err := head(ctx, root)
	if err != nil {
		return err
	}
	now, err := head(ctx, Path(root, id))
	if err != nil {
		return err
	}
	if target != p.Target || base != p.Base || now != p.Head {
		return ErrMoved
	}

	args := []string{"merge", "--quiet", "--ff-only", p.Head}
	if !p.FastForward {
		args = []string{"merge", "--quiet", "--no-ff", "--no-edit", "-m", message, p.Head}
	}
	_, mergeErr := git(ctx, root, args...)
	if mergeErr == nil {
		return nil
	}
	// A merge that started and stopped leaves MERGE_HEAD behind. One that
	// git refused to start leaves nothing, and has changed nothing.
	if _, err := git(ctx, root, "rev-parse", "--quiet", "--verify", "MERGE_HEAD"); err != nil {
		return &RefusedError{Reason: strings.TrimPrefix(mergeErr.Error(), "git merge: ")}
	}
	conflicts, _ := git(ctx, root, "diff", "--name-only", "--diff-filter=U")
	if _, err := git(ctx, root, "merge", "--abort"); err != nil {
		return fmt.Errorf("the merge conflicted and could not be undone: %w", err)
	}
	return &ConflictError{Files: strings.Split(strings.TrimSpace(conflicts), "\n")}
}

// Short abbreviates a commit hash for display.
func Short(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// head returns the commit checked out in a working copy.
func head(ctx context.Context, dir string) (string, error) {
	out, err := git(ctx, dir, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

// count returns how many commits a range holds.
func count(ctx context.Context, dir, commits string) (int, error) {
	out, err := git(ctx, dir, "rev-list", "--count", commits)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(out))
}
