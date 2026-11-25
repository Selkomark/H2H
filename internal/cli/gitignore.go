package cli

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

// ignoreComment heads the lines H2H adds to a project's .gitignore, so a
// person reading the file can tell where they came from.
const ignoreComment = "# H2H: its runtime state, and the file it writes while saving " + config.FileName + ". " +
	config.FileName + " itself belongs in git."

// projectIgnores are the paths H2H creates inside a project that do not
// belong in it:
//
//   - The state directory: harness sessions and their logs, the
//     workspaces, saved console sessions, permission requests and the
//     session counters.
//   - The file the config is written to before it replaces h2h.toml, which
//     is left behind only if H2H stops in between.
//
// h2h.toml is not here: it is the declared config, meant to be committed.
func projectIgnores(root string) []workspace.IgnoreEntry {
	state, err := filepath.Rel(root, session.StateDir(root))
	if err != nil {
		state = ".h2h"
	}
	state = filepath.ToSlash(state)
	return []workspace.IgnoreEntry{
		// Without a trailing slash, so the line names .h2h itself and not
		// only what is under it.
		{Pattern: "/" + state, Sample: state + "/"},
		// Matches the name configApply gives the file: h2h.toml.<pid>.tmp.
		{Pattern: "/" + config.FileName + ".*.tmp", Sample: config.FileName + ".0.tmp"},
	}
}

// keepOutOfGit makes sure the project's .gitignore keeps H2H's own files
// out of the project, and says so when it changed the file. It runs when a
// project is set up for H2H: when the config is applied and when a harness
// is started. A failure is reported and does not stop either.
func (e *env) keepOutOfGit(ctx context.Context) {
	if e.root == "" {
		return
	}
	added, err := workspace.Ignore(ctx, e.root, ignoreComment, projectIgnores(e.root))
	if err != nil {
		fmt.Fprintln(e.errw, e.st.warn.Render("h2h:"), "could not update "+workspace.IgnoreFile+":", err)
		return
	}
	if len(added) > 0 {
		fmt.Fprintf(e.out, "Added %s to %s, so H2H's own files stay out of the project.\n",
			strings.Join(added, " and "), workspace.IgnoreFile)
	}
}
