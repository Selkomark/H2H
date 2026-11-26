package cli

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

// land brings the work in a harness's workspace onto the branch checked out
// in the project root. It shows what would happen and asks before anything
// in the project changes. It does not push, stop the harness or remove its
// workspace.
func (e *env) land(ctx context.Context, args []string) error {
	info, err := e.pick(args, "land [id]")
	if err != nil {
		return err
	}
	if info.State == session.Starting {
		return fmt.Errorf("%s is still starting; try again in a moment", info.ID)
	}
	if !workspace.Exists(e.root, info.ID) {
		return fmt.Errorf("%s has no workspace any more, so there is nothing to land", info.ID)
	}
	target, err := workspace.Target(ctx, e.root)
	switch {
	case errors.Is(err, workspace.ErrDetached):
		return errors.New("the project is not on a branch, so there is nowhere to land work; check a branch out in the project first")
	case errors.Is(err, workspace.ErrBusy):
		return errors.New("the project is in the middle of a merge, rebase or similar; finish or abort it, then land again")
	case err != nil:
		return err
	}
	pending, err := workspace.Pending(ctx, e.root, info.ID)
	if err != nil {
		return err
	}
	if !pending.Any() {
		fmt.Fprintf(e.out, "%s has nothing to land: its workspace holds no work that %s lacks.\n", info.ID, target)
		return nil
	}

	nothing := func() error {
		fmt.Fprintf(e.out, "Nothing was landed. The work is still in the workspace of %s.\n", info.ID)
		return nil
	}
	if info.State == session.Running &&
		!e.ui.Confirm(fmt.Sprintf("%s is still running and may be in the middle of a change. Land what it has so far?", info.ID)) {
		return nothing()
	}
	// Uncommitted work is neither dropped nor committed without a word.
	if len(pending.Changes) > 0 {
		fmt.Fprint(e.out, changeSummary(info.ID, pending.Changes))
		question := fmt.Sprintf("Only committed work can be landed. Commit %s in the workspace of %s first? Files that git ignores stay out.",
			theseChanges(len(pending.Changes)), info.ID)
		if !e.ui.Confirm(question) {
			fmt.Fprintf(e.out, "Nothing was landed. Commit or discard those changes in the workspace of %s, then land again.\n", info.ID)
			return nil
		}
		if err := workspace.Commit(ctx, e.root, info.ID, commitMessage(info)); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Committed %s in the workspace.\n", theseChanges(len(pending.Changes)))
	}

	plan, err := workspace.PlanLanding(ctx, e.root, info.ID)
	if err != nil {
		return err
	}
	if len(plan.Outside) > 0 {
		// The project is one folder of a larger repository, and the rest
		// of it is not the project's to change.
		return fmt.Errorf("the work of %s changes files outside this project's folder, so nothing was landed: %s\n"+
			"Have the harness undo those changes in its workspace, then land again", info.ID, listed(plan.Outside))
	}
	fmt.Fprint(e.out, e.landingPlan(info.ID, plan))
	if !e.ui.Confirm(fmt.Sprintf("Land this on %s?", plan.Target)) {
		return nothing()
	}

	err = workspace.Land(ctx, e.root, info.ID, plan, "Merge work of "+info.ID)
	var conflict *workspace.ConflictError
	var refused *workspace.RefusedError
	switch {
	case errors.As(err, &conflict):
		return fmt.Errorf("the work of %s conflicts with %s in: %s\n"+
			"The merge was undone, so the project is as it was.\n"+
			"Have the harness bring its workspace up to date with %s and settle the conflicts there, then land again",
			info.ID, plan.Target, listed(conflict.Files), plan.Target)
	case errors.As(err, &refused):
		return fmt.Errorf("git would not start the merge, so nothing in the project was changed. It said:\n%s\n"+
			"This is how git protects changed or untracked files in the project that the work would overwrite. Commit or move them, then land again",
			refused.Reason)
	case errors.Is(err, workspace.ErrMoved):
		return fmt.Errorf("%s or the workspace of %s changed after the plan was shown, so nothing was landed; land again for a new plan", plan.Target, info.ID)
	case err != nil:
		return err
	}

	fmt.Fprintf(e.out, "%s %s on %s: %s, %s..%s.\n", e.st.good.Render("Landed"), info.ID, plan.Target,
		plural(plan.Commits, "commit"), workspace.Short(plan.Base), workspace.Short(plan.Head))
	if ignored, err := workspace.Ignored(ctx, e.root, info.ID); err == nil && len(ignored) > 0 {
		fmt.Fprintf(e.out, "Left in the workspace, because git ignores them: %s.\n", listed(ignored))
	}
	fmt.Fprintf(e.out, "Nothing was pushed. %s and its workspace are still there; remove them with: %s\n", info.ID, e.st.accent.Render("rm "+info.ID))
	return nil
}

// landingPlan describes a landing before it is done.
func (e *env) landingPlan(id string, p workspace.Plan) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s\n", e.st.title.Render(fmt.Sprintf("Landing %s on %s", id, p.Target)))
	fmt.Fprintf(&b, "  %s: %s\n", plural(p.Commits, "commit"), p.Stat)
	if p.FastForward {
		fmt.Fprintf(&b, "  as a fast-forward: %s has not moved since the work began\n", p.Target)
	} else {
		fmt.Fprintf(&b, "  with a merge commit: %s has moved on since the work began\n", p.Target)
	}
	for n, path := range p.Files {
		if n == maxListedChanges {
			fmt.Fprintf(&b, "    and %d more\n", len(p.Files)-n)
			break
		}
		fmt.Fprintf(&b, "    %s\n", path)
	}
	return b.String()
}

// commitMessage is the message of the commit H2H makes of a harness's
// uncommitted work.
func commitMessage(info session.Info) string {
	who := info.ID + " (" + info.Harness + ")"
	if info.Domain != "" {
		who = info.ID + ", owner of " + info.Domain + " (" + info.Harness + ")"
	}
	return "Work of " + who + "\n\nCommitted by H2H when the work was landed."
}

// theseChanges names a number of uncommitted changes that were just listed.
func theseChanges(n int) string {
	if n == 1 {
		return "this 1 change"
	}
	return fmt.Sprintf("these %d changes", n)
}

// listed names the first few paths of a list and counts the rest.
func listed(paths []string) string {
	if len(paths) <= maxListedChanges {
		return strings.Join(paths, ", ")
	}
	return fmt.Sprintf("%s and %d more", strings.Join(paths[:maxListedChanges], ", "), len(paths)-maxListedChanges)
}

// landable describes what a harness has to land, or returns "" when there
// is nothing or it cannot be told.
func (e *env) landable(ctx context.Context, id string) string {
	if !workspace.Exists(e.root, id) {
		return ""
	}
	pending, err := workspace.Pending(ctx, e.root, id)
	if err != nil || !pending.Any() {
		return ""
	}
	var parts []string
	if pending.Commits > 0 {
		parts = append(parts, plural(pending.Commits, "commit"))
	}
	if n := len(pending.Changes); n > 0 {
		parts = append(parts, plural(n, "uncommitted change"))
	}
	return strings.Join(parts, ", ")
}
