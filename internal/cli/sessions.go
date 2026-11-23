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
		fmt.Fprintln(e.out, e.st.dim.Render("No sessions. Start one with: ")+e.st.accent.Render("start [harness]"))
		return nil
	}
	fmt.Fprint(e.out, sessionTable(e.root, infos, e.st))
	return nil
}

// sessionTable lists sessions, one per row.
func sessionTable(root string, infos []session.Info, st styles) string {
	const stateCol = 2
	var rows [][]string
	for _, i := range infos {
		state := stateText(i)
		ws, err := filepath.Rel(root, i.Workspace)
		if err != nil {
			ws = i.Workspace
		}
		rows = append(rows, []string{i.ID, i.Harness, state, age(time.Since(i.Started)), ws})
	}
	return table([]string{"SESSION", "HARNESS", "STATE", "AGE", "WORKSPACE"}, rows, st.dim,
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
		return errors.New("usage: start [harness]")
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	var want string
	if len(args) == 1 {
		want = args[0]
	}
	name, h, err := cfg.Lookup(want)
	if err != nil {
		return err
	}
	if _, err := exec.LookPath(h.Command); err != nil {
		return fmt.Errorf("harness %s: the program %q is not installed or not on PATH", name, h.Command)
	}

	info, err := session.Create(e.root, name)
	if err != nil {
		return err
	}
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
	fmt.Fprintf(e.out, "%s %s. Open it with: %s\n", e.st.good.Render("Started"), info.ID, e.st.accent.Render("attach "+info.ID))
	return nil
}

func (e *env) attach(_ context.Context, args []string) error {
	info, err := e.pick(args, "attach [session]")
	if err != nil {
		return err
	}
	if info.State != session.Running {
		return fmt.Errorf("session %s is %s, not running; see its output with: logs %s", info.ID, info.State, info.ID)
	}
	if !term.IsTerminal(int(e.in.Fd())) {
		return errors.New("attach needs a terminal")
	}
	fmt.Fprintf(e.out, "Attaching to %s. Press %s to leave it running.\n", info.ID, e.st.accent.Render("Ctrl-\\"))
	var ended bool
	err = e.ui.Foreground(func() error {
		var err error
		ended, err = keeper.Attach(info.Socket, e.in, e.tty, func(width int) string {
			return e.st.bar(" H2H  "+info.ID+"   ctrl+\\ return to h2h, the session keeps running", width)
		})
		return err
	})
	if err != nil {
		return err
	}
	if ended {
		fmt.Fprintf(e.out, "Session %s ended.\n", info.ID)
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
	info, err := e.pick(rest, "logs [-f] [session]")
	if err != nil {
		return err
	}
	f, err := os.Open(session.LogPath(e.root, info.ID))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("session %s has produced no output", info.ID)
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
	info, err := e.pick(args, "stop [session]")
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
	info, err := e.pick(args, "rm [session]")
	if err != nil {
		return err
	}
	if info.State == session.Running || info.State == session.Starting {
		return fmt.Errorf("session %s is %s; stop it first", info.ID, info.State)
	}
	if _, err := os.Stat(workspace.Path(e.root, info.ID)); err == nil {
		if err := workspace.Remove(ctx, e.root, info.ID); err != nil {
			return fmt.Errorf("%w\nThe session was kept. Commit or discard the changes in its workspace, then remove it again", err)
		}
	}
	if err := session.Remove(e.root, info.ID); err != nil {
		return err
	}
	fmt.Fprintf(e.out, "Removed %s.\n", info.ID)
	return nil
}

// sessionsView is the content of the console's Sessions tab.
func (e *env) sessionsView() string {
	infos, err := session.List(e.root)
	if err != nil {
		return e.st.bad.Render(err.Error())
	}
	if len(infos) == 0 {
		return e.st.dim.Render("No sessions. Start one with ") + e.st.accent.Render("/start [harness]")
	}
	return sessionTable(e.root, infos, e.st) + "\n" +
		e.st.dim.Render("Open one with ") + e.st.accent.Render("/attach <session>") +
		e.st.dim.Render(", stop it with ") + e.st.accent.Render("/stop") +
		e.st.dim.Render(", remove it with ") + e.st.accent.Render("/rm") + e.st.dim.Render(".")
}

// statusLine summarises the sessions in one line, for the top of the console.
func (e *env) statusLine() string {
	infos, err := session.List(e.root)
	if err != nil {
		return e.st.bad.Render(err.Error())
	}
	if len(infos) == 0 {
		return e.st.dim.Render("no sessions")
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
	case "config":
		out = []console.Suggestion{
			{Text: "show", Help: "Print the config"},
			{Text: "edit", Help: "Open the config in your editor"},
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
			return session.Info{}, errors.New("there are no sessions")
		case 1:
			return infos[0], nil
		}
		ids := make([]string, len(infos))
		for i, info := range infos {
			ids[i] = info.ID
		}
		return session.Info{}, fmt.Errorf("which session? %s", strings.Join(ids, ", "))
	}
	return session.Info{}, fmt.Errorf("usage: %s", usage)
}
