package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/charmbracelet/lipgloss"
	"golang.org/x/term"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/keeper"
	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

func (e *env) ps(_ context.Context, _ []string) error {
	infos, err := session.List(e.root)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Fprintln(e.out, e.st.dim.Render("No harnesses running. Start one with: ")+e.st.accent.Render("start [harness]"))
		return nil
	}
	fmt.Fprint(e.out, sessionTable(e.root, infos, e.st))
	return nil
}

// sessionTable lists sessions, one per row, under a header.
func sessionTable(root string, infos []session.Info, st styles) string {
	return strings.Join(sessionLines(root, infos, st), "\n") + "\n"
}

// sessionLines returns the header of the session list followed by one line
// per session.
func sessionLines(root string, infos []session.Info, st styles) []string {
	const stateCol = 2
	var rows [][]string
	for _, i := range infos {
		ws, err := filepath.Rel(root, i.Workspace)
		if err != nil {
			ws = i.Workspace
		}
		rows = append(rows, []string{i.ID, i.Harness, stateText(i), age(time.Since(i.Started)), ws})
	}
	return table([]string{"ID", "HARNESS", "STATE", "AGE", "WORKSPACE"}, rows, st.dim,
		func(row, col int) lipgloss.Style {
			switch col {
			case 0:
				return st.accent
			case stateCol:
				return st.state(infos[row])
			}
			return lipgloss.NewStyle()
		})
}

// stateText is the state of a session as shown to the user.
func stateText(i session.Info) string {
	switch {
	case i.State == session.Running && i.Attention.Kind == session.Waiting:
		return "waiting for you"
	case i.State == session.Running && i.Attention.Kind == session.Quiet:
		return "may be waiting for you"
	case i.State == session.Exited && i.Error != "":
		return "failed"
	case i.State == session.Exited && i.ExitCode != nil && *i.ExitCode < 0:
		return "stopped" // ended by a signal
	case i.State == session.Exited && i.ExitCode != nil:
		return fmt.Sprintf("exited (%d)", *i.ExitCode)
	}
	return string(i.State)
}

func age(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	}
	return fmt.Sprintf("%dd", int(d.Hours()/24))
}

func (e *env) start(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: start [harness | domain]")
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	var want string
	if len(args) == 1 {
		want = args[0]
	}
	// A domain starts its own harness, and the session is named after the
	// domain so the user can tell who owns it.
	idBase, domain := "", ""
	if d, ok := cfg.Domains[want]; ok {
		idBase, domain, want = want, want, d.Harness
	}
	name, h, err := cfg.Lookup(want)
	if err != nil && len(cfg.Domains) > 0 {
		return fmt.Errorf("no harness or domain named %q (harnesses: %s; domains: %s)", want,
			strings.Join(cfg.Names(), ", "), strings.Join(cfg.DomainNames(), ", "))
	}
	if err != nil {
		return err
	}
	if idBase == "" {
		idBase = name
	}
	if _, err := exec.LookPath(h.Command); err != nil {
		return fmt.Errorf("harness %s: the program %q is not installed or not on PATH", name, h.Command)
	}

	e.keepOutOfGit(ctx)
	info, err := session.Create(e.root, idBase)
	if err != nil {
		return err
	}
	info.Harness, info.Domain = name, domain
	info.Workspace, err = workspace.Create(ctx, e.root, info.ID)
	if err == nil {
		err = session.Save(e.root, info)
	}
	if err != nil {
		_ = session.Remove(e.root, info.ID)
		return err
	}
	if err := keeper.Spawn(e.root, info.ID); err != nil {
		return fmt.Errorf("session %s failed to start: %w", info.ID, err)
	}
	if domain != "" && !h.PassesRoles() {
		fmt.Fprintln(e.out, e.st.warn.Render("Note:")+" harness "+name+" has no context_args or context_env in the config, so the session was not told its role in "+domain+".")
	}
	fmt.Fprintf(e.out, "%s %s. Open it with: %s\n", e.st.good.Render("Started"), info.ID, e.st.accent.Render("attach "+info.ID))
	switch {
	case h.AsksH2H():
		// Its permission requests come to the console, as a dialog.
	case h.TellsWaiting():
		fmt.Fprintln(e.out, e.st.dim.Render("When "+name+" stops to ask you something, such as permission, it asks in its own terminal. H2H tells you here when it does; answer it with ")+e.st.accent.Render("attach "+info.ID))
	default:
		fmt.Fprintln(e.out, e.st.dim.Render(fmt.Sprintf("When %s stops to ask you something, such as permission, it asks in its own terminal and cannot tell H2H. H2H says it may be waiting once it has been quiet for %s with a request open; answer it with ", name, session.QuietAfter))+e.st.accent.Render("attach "+info.ID))
	}
	return nil
}

func (e *env) attach(_ context.Context, args []string) error {
	info, err := e.pick(args, "attach [id]")
	if err != nil {
		return err
	}
	if info.State != session.Running {
		return fmt.Errorf("%s is %s, not running; see its output with: logs %s", info.ID, info.State, info.ID)
	}
	if !term.IsTerminal(int(e.in.Fd())) {
		return errors.New("attach needs a terminal")
	}
	fmt.Fprintf(e.out, "Attaching to %s. Press %s to leave it running.\n", info.ID, e.st.accent.Render("Ctrl-\\"))
	var ended bool
	err = e.ui.Foreground(func() error {
		var err error
		up := time.Now()
		ended, err = keeper.Attach(info.Socket, e.in, e.tty, func(width int) string {
			return e.st.bar(" H2H  "+info.ID+"   ctrl+\\ return to h2h, the harness keeps running", width, time.Since(up))
		})
		return err
	})
	if err != nil {
		return err
	}
	if ended {
		fmt.Fprintf(e.out, "%s ended.\n", info.ID)
	} else {
		fmt.Fprintf(e.out, "Left %s running.\n", info.ID)
	}
	return nil
}

func (e *env) logs(ctx context.Context, args []string) error {
	follow := false
	var rest []string
	for _, a := range args {
		if a == "-f" {
			follow = true
		} else {
			rest = append(rest, a)
		}
	}
	info, err := e.pick(rest, "logs [-f] [id]")
	if err != nil {
		return err
	}
	f, err := os.Open(session.LogPath(e.root, info.ID))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("%s has produced no output", info.ID)
	}
	if err != nil {
		return err
	}
	defer f.Close()
	for {
		if _, err := io.Copy(e.out, f); err != nil {
			return err
		}
		if !follow {
			break
		}
		// At the end of the file: stop once the session is over, otherwise
		// wait for more.
		if now, err := session.Load(e.root, info.ID); err != nil || !now.Live() {
			_, err := io.Copy(e.out, f)
			return err
		}
		select {
		case <-ctx.Done():
			fmt.Fprintln(e.out)
			return nil
		case <-time.After(200 * time.Millisecond):
		}
	}
	fmt.Fprintln(e.out)
	return nil
}

// everything is the word that stands for every harness in stop and rm.
const everything = "all"

func (e *env) stop(_ context.Context, args []string) error {
	if len(args) == 1 && args[0] == everything {
		return e.stopAll()
	}
	info, err := e.pick(args, "stop [id | all]")
	if err != nil {
		return err
	}
	if err := keeper.Stop(e.root, info); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Stopped %s.\n", info.ID)
	return nil
}

// stopAll stops every running harness, after one question.
func (e *env) stopAll() error {
	infos, err := session.List(e.root)
	if err != nil {
		return err
	}
	var running []session.Info
	for _, i := range infos {
		if i.State == session.Running {
			running = append(running, i)
		}
	}
	if len(running) == 0 {
		fmt.Fprintln(e.out, "No harness is running.")
		return nil
	}
	if !e.ui.Confirm(fmt.Sprintf("Stop %s?", allOf(len(running), "running harness"))) {
		fmt.Fprintln(e.out, "Nothing was stopped.")
		return nil
	}
	var failed []error
	for _, i := range running {
		if err := keeper.Stop(e.root, i); err != nil {
			failed = append(failed, fmt.Errorf("%s: %w", i.ID, err))
			continue
		}
		fmt.Fprintf(e.out, "Stopped %s.\n", i.ID)
	}
	return errors.Join(failed...)
}

// rmAll removes every harness and its workspace, after one question that
// says what is lost: running harnesses are stopped, and uncommitted work in
// a workspace is discarded.
func (e *env) rmAll(ctx context.Context) error {
	infos, err := session.List(e.root)
	if err != nil {
		return err
	}
	if len(infos) == 0 {
		fmt.Fprintln(e.out, "There is no harness to remove.")
		return nil
	}
	running, unsaved := 0, 0
	for _, i := range infos {
		if i.State == session.Running {
			running++
		}
		if !workspace.Exists(e.root, i.ID) {
			continue
		}
		// A workspace git no longer knows cannot be checked, so it counts
		// as holding work that would be lost.
		if changes, err := workspace.Changes(ctx, e.root, i.ID); err != nil || len(changes) > 0 {
			unsaved++
		}
	}
	question := fmt.Sprintf("Delete %s and %s?", allOf(len(infos), "harness"), possessive(len(infos))+" "+nouns(len(infos), "workspace"))
	if running > 0 {
		question += fmt.Sprintf(" %s running and will be stopped.", isAre(running))
	}
	if unsaved > 0 {
		question += fmt.Sprintf(" %s uncommitted changes that will be lost.", hasHave(unsaved))
	}
	if !e.ui.Confirm(question) {
		fmt.Fprintln(e.out, "Nothing was removed.")
		return nil
	}
	var failed []error
	for _, i := range infos {
		if err := e.rmOne(ctx, i); err != nil {
			failed = append(failed, fmt.Errorf("%s was kept: %w", i.ID, err))
			continue
		}
		fmt.Fprintf(e.out, "Removed %s.\n", i.ID)
	}
	return errors.Join(failed...)
}

// rmOne stops a harness if it runs and removes it with its workspace,
// whatever the workspace holds. The caller has asked the user.
func (e *env) rmOne(ctx context.Context, info session.Info) error {
	switch info.State {
	case session.Starting:
		return errors.New("it is still starting")
	case session.Running:
		if err := keeper.Stop(e.root, info); err != nil {
			return err
		}
	}
	if err := workspace.Remove(ctx, e.root, info.ID, true); err != nil {
		return err
	}
	return session.Remove(e.root, info.ID)
}

func (e *env) rm(ctx context.Context, args []string) error {
	if len(args) == 1 && args[0] == everything {
		return e.rmAll(ctx)
	}
	info, err := e.pick(args, "rm [id | all]")
	if err != nil {
		return err
	}
	switch info.State {
	case session.Starting:
		return fmt.Errorf("%s is still starting; try again in a moment", info.ID)
	case session.Running:
		// Deleting a running harness means stopping it, which the user
		// decides here instead of being sent away to do it by hand.
		if !e.ui.Confirm(fmt.Sprintf("%s is running. Stop it and delete it?", info.ID)) {
			fmt.Fprintf(e.out, "Left %s running.\n", info.ID)
			return nil
		}
		if err := keeper.Stop(e.root, info); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Stopped %s.\n", info.ID)
	}
	// A workspace holding work that exists nowhere else is deleted only if
	// the user says so.
	force := false
	if workspace.Exists(e.root, info.ID) {
		pending, err := workspace.Pending(ctx, e.root, info.ID)
		question := ""
		switch {
		case errors.Is(err, workspace.ErrNotWorktree):
			question = fmt.Sprintf("The workspace of %s is no longer a git worktree. Delete the folder and everything in it?", info.ID)
		case err != nil:
			return err
		default:
			if len(pending.Changes) > 0 {
				fmt.Fprint(e.out, changeSummary(info.ID, pending.Changes))
			}
			if lost := atRisk(pending); lost != "" {
				fmt.Fprintln(e.out, e.st.dim.Render("To keep this work, bring it onto the project's branch first with: ")+e.st.accent.Render("land "+info.ID))
				question = fmt.Sprintf("%s has %s that will be lost. Delete it anyway?", info.ID, lost)
			}
		}
		if question != "" {
			if !e.ui.Confirm(question) {
				fmt.Fprintf(e.out, "Kept %s and its workspace.\n", info.ID)
				return nil
			}
			force = true
		}
	}
	if err := workspace.Remove(ctx, e.root, info.ID, force); err != nil {
		return fmt.Errorf("%w\n%s was kept", err, info.ID)
	}
	if err := session.Remove(e.root, info.ID); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Removed %s.\n", info.ID)
	return nil
}

// atRisk names the work that removing a workspace would lose: what is not
// committed, and commits that are on no branch and were never landed. It
// returns "" when nothing would be lost.
func atRisk(pending workspace.Unlanded) string {
	var parts []string
	if n := len(pending.Changes); n > 0 {
		parts = append(parts, plural(n, "uncommitted change"))
	}
	if pending.Commits > 0 && pending.Branch == "" {
		parts = append(parts, plural(pending.Commits, "unlanded commit"))
	}
	return strings.Join(parts, " and ")
}

// maxListedChanges is how many uncommitted paths are named before the rest
// are only counted.
const maxListedChanges = 5

// changeSummary tells the user what in a session's workspace is not
// committed.
func changeSummary(id string, changes []string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "The workspace of %s has %s:\n", id, plural(len(changes), "uncommitted change"))
	for n, path := range changes {
		if n == maxListedChanges {
			fmt.Fprintf(&b, "  and %d more\n", len(changes)-n)
			break
		}
		fmt.Fprintf(&b, "  %s\n", path)
	}
	return b.String()
}

// allOf names a whole group by its size: "the 1 harness", "all 3 harnesses".
func allOf(n int, noun string) string {
	if n == 1 {
		return "the 1 " + noun
	}
	return fmt.Sprintf("all %d %s", n, nouns(n, noun))
}

// nouns returns the noun in the form that fits a count.
func nouns(n int, noun string) string {
	switch {
	case n == 1:
		return noun
	case strings.HasSuffix(noun, "s"):
		return noun + "es"
	}
	return noun + "s"
}

func possessive(n int) string {
	if n == 1 {
		return "its"
	}
	return "their"
}

func isAre(n int) string {
	if n == 1 {
		return "1 is"
	}
	return fmt.Sprintf("%d are", n)
}

func hasHave(n int) string {
	if n == 1 {
		return "1 has"
	}
	return fmt.Sprintf("%d have", n)
}

// plural returns a count with its noun, such as "1 change" or "3 changes".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// sessionsView is the content of the console's Harnesses tab. Its tab
// counts the harnesses running out of those listed, and counts nothing
// when none is listed.
func (e *env) sessionsView() console.Table {
	infos, err := session.List(e.root)
	if err != nil {
		return console.Table{Empty: e.st.bad.Render(err.Error())}
	}
	lines := sessionLines(e.root, infos, e.st)
	t := console.Table{
		Header: lines[0],
		Empty:  e.st.dim.Render("No harnesses running. Start one with ") + e.st.accent.Render("/start [harness]"),
	}
	running := 0
	for n, i := range infos {
		t.Rows = append(t.Rows, console.Row{ID: i.ID, Text: lines[n+1]})
		if i.State == session.Running {
			running++
			// Loading a session sets its attention only while it runs.
			t.Attention = t.Attention || i.Attention.Kind != session.NoAttention
		}
	}
	if len(infos) > 0 {
		t.Count = fmt.Sprintf("%d/%d", running, len(infos))
	}
	return t
}

// argumentSuggestions offers what can follow a command in the console.
func (e *env) argumentSuggestions(command string) []console.Suggestion {
	var out []console.Suggestion
	switch command {
	case "attach", "logs", "stop", "rm":
		infos, err := session.List(e.root)
		if err != nil {
			return nil
		}
		for _, i := range infos {
			if (command == "attach" || command == "stop") && i.State != session.Running {
				continue
			}
			out = append(out, console.Suggestion{Text: i.ID, Help: stateText(i)})
		}
		if len(out) > 0 && (command == "stop" || command == "rm") {
			out = append(out, console.Suggestion{Text: everything, Help: "Every harness, after one question"})
		}
	case "land":
		// Only the harnesses that have something to land.
		infos, err := session.List(e.root)
		if err != nil {
			return nil
		}
		for _, i := range infos {
			if what := e.landable(context.Background(), i.ID); what != "" {
				out = append(out, console.Suggestion{Text: i.ID, Help: what})
			}
		}
	case "start":
		cfg, err := config.Load(e.root)
		if err != nil {
			return nil
		}
		for _, name := range cfg.Names() {
			help := cfg.Harnesses[name].Command
			if name == cfg.DefaultHarness {
				help += " (default)"
			}
			out = append(out, console.Suggestion{Text: name, Help: help})
		}
		for _, name := range cfg.DomainNames() {
			out = append(out, console.Suggestion{Text: name, Help: "domain, on " + cfg.Domains[name].Harness})
		}
	case "sessions":
		out = []console.Suggestion{{Text: "rm", Help: "Remove a saved session by its ID, or all of them with: rm all"}}
	case "resume":
		saved, err := listConsoleSessions(e.root)
		if err != nil {
			return nil
		}
		for _, s := range saved {
			if s.ID != e.hereID() {
				out = append(out, console.Suggestion{Text: s.ID, Help: displayLine(s.summary(), 60)})
			}
		}
	case "install":
		out = []console.Suggestion{{Text: updateFlag, Help: "Check the installed harnesses for a newer version, and choose which to update"}}
		if cfg, err := config.Load(e.root); err == nil {
			for _, name := range cfg.Names() {
				out = append(out, console.Suggestion{Text: name, Help: strings.Join(cfg.Harnesses[name].InstallCommand, " ")})
			}
			// And the ones H2H supports that the config does not have yet.
			shipped := config.Shipped()
			for _, name := range cfg.Unconfigured() {
				out = append(out, console.Suggestion{Text: name, Help: strings.Join(shipped.Harnesses[name].InstallCommand, " ") + "  (not in " + config.FileName + " yet)"})
			}
		}
	case "config":
		out = []console.Suggestion{
			{Text: "show", Help: "Print the config"},
			{Text: manualFlag, Help: "Open the config in your editor"},
		}
	}
	return out
}

// pick returns the session a command names. With no name it picks the only
// session there is, and otherwise asks the user to choose.
func (e *env) pick(args []string, usage string) (session.Info, error) {
	switch len(args) {
	case 1:
		return session.Load(e.root, args[0])
	case 0:
		infos, err := session.List(e.root)
		if err != nil {
			return session.Info{}, err
		}
		switch len(infos) {
		case 0:
			return session.Info{}, errors.New("no harness has been started")
		case 1:
			return infos[0], nil
		}
		ids := make([]string, len(infos))
		for i, info := range infos {
			ids[i] = info.ID
		}
		return session.Info{}, fmt.Errorf("which one? %s", strings.Join(ids, ", "))
	}
	return session.Info{}, fmt.Errorf("usage: %s", usage)
}
