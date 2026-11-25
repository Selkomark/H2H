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
	if !h.AsksH2H() {
		fmt.Fprintln(e.out, e.st.dim.Render("When "+name+" needs permission it stops at a prompt in its own terminal, which H2H cannot answer for it; attach to answer."))
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
		ended, err = keeper.Attach(info.Socket, e.in, e.tty, func(width int) string {
			return e.st.bar(" H2H  "+info.ID+"   ctrl+\\ return to h2h, the harness keeps running", width)
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

func (e *env) stop(_ context.Context, args []string) error {
	info, err := e.pick(args, "stop [id]")
	if err != nil {
		return err
	}
	if err := keeper.Stop(e.root, info); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Stopped %s.\n", info.ID)
	return nil
}

func (e *env) rm(ctx context.Context, args []string) error {
	info, err := e.pick(args, "rm [id]")
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
	// A workspace with uncommitted work is deleted only if the user says so.
	force := false
	if workspace.Exists(e.root, info.ID) {
		changes, err := workspace.Changes(ctx, e.root, info.ID)
		question := ""
		switch {
		case errors.Is(err, workspace.ErrNotWorktree):
			question = fmt.Sprintf("The workspace of %s is no longer a git worktree. Delete the folder and everything in it?", info.ID)
		case err != nil:
			return err
		case len(changes) > 0:
			fmt.Fprint(e.out, changeSummary(info.ID, changes))
			question = fmt.Sprintf("%s has %s that will be lost. Delete it anyway?", info.ID, plural(len(changes), "uncommitted change"))
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

// plural returns a count with its noun, such as "1 change" or "3 changes".
func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("1 %s", noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// sessionsView is the content of the console's Sessions tab.
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
	for n, i := range infos {
		t.Rows = append(t.Rows, console.Row{ID: i.ID, Text: lines[n+1]})
	}
	return t
}

// statusLine summarises the sessions in one line, for the top of the console.
func (e *env) statusLine() string {
	infos, err := session.List(e.root)
	if err != nil {
		return e.st.bad.Render(err.Error())
	}
	if len(infos) == 0 {
		return e.st.dim.Render("no harnesses running")
	}
	parts := make([]string, len(infos))
	for n, i := range infos {
		parts[n] = e.st.state(i).Render("● ") + i.ID + " " + e.st.dim.Render(stateText(i))
	}
	return strings.Join(parts, "   ")
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
			if command == "rm" && i.Live() {
				continue
			}
			out = append(out, console.Suggestion{Text: i.ID, Help: stateText(i)})
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
