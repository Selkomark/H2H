// Package cli holds the commands of h2h and wires them to the command line
// and to the interactive console. "h2h ps" and "/ps" run the same function.
package cli

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/term"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/keeper"
	"github.com/Selkomark/H2H/internal/workspace"
)

// Version is the version of this build.
const Version = "0.1.0-dev"

// env is what a command may use: the project it acts on and the ways to
// reach the user.
type env struct {
	root    string
	in      *os.File   // the terminal's input, for programs given the whole terminal
	tty     *os.File   // the terminal's output, likewise
	out     io.Writer  // where a command's output goes
	errw    io.Writer  // where its errors go
	ui      console.UI // questions to the user, and handing over the terminal
	environ []string
	st      styles
}

// shellUI serves a command run from the shell or from piped input, where
// the terminal is used directly.
type shellUI struct {
	in  *os.File
	out io.Writer
}

func (u shellUI) Write(b []byte) (int, error) { return u.out.Write(b) }

func (u shellUI) Foreground(fn func() error) error { return fn() }

// Confirm asks a yes-or-no question. Anything but yes is no.
func (u shellUI) Confirm(question string) bool {
	fmt.Fprintf(u.out, "%s [y/N] ", question)
	// One byte at a time, so nothing typed after the answer is swallowed.
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := u.in.Read(b)
		if n == 0 || err != nil || b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	answer := strings.ToLower(strings.TrimSpace(string(line)))
	return answer == "y" || answer == "yes"
}

type command struct {
	name  string
	usage string
	help  string
	run   func(e *env, ctx context.Context, args []string) error
}

func commands() []command {
	return []command{
		{"ps", "", "List sessions", (*env).ps},
		{"start", "[harness]", "Start a session; the default harness if none is named", (*env).start},
		{"attach", "[session]", "Open a session's terminal; leave it with Ctrl-\\", (*env).attach},
		{"logs", "[-f] [session]", "Print a session's terminal log; -f follows it", (*env).logs},
		{"stop", "[session]", "Stop a running session", (*env).stop},
		{"rm", "[session]", "Remove a stopped session and its workspace", (*env).rm},
		{"config", "[show | edit | <text>]", "Show the config, edit it, or describe a change in words", (*env).config},
		{"help", "", "Show this help", (*env).help},
	}
}

// Run executes h2h with the given arguments and returns its exit status.
func Run(args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case keeper.Command:
			if len(args) != 3 {
				fmt.Fprintln(os.Stderr, "h2h: keeper is an internal command")
				return 2
			}
			if err := keeper.Run(args[1], args[2]); err != nil {
				fmt.Fprintln(os.Stderr, "h2h keeper:", err)
				return 1
			}
			return 0
		case "version", "--version", "-v":
			fmt.Println("h2h", Version)
			return 0
		case "-h", "--help":
			args[0] = "help"
		}
	}

	e := &env{in: os.Stdin, tty: os.Stdout, out: os.Stdout, errw: os.Stderr, environ: os.Environ(), st: newStyles()}
	e.ui = shellUI{in: e.in, out: e.out}
	if len(args) > 0 && args[0] == "help" {
		_ = e.help(context.Background(), nil)
		return 0
	}

	cwd, err := os.Getwd()
	if err == nil {
		e.root, err = workspace.Root(context.Background(), cwd)
	}
	if err != nil {
		e.fail(err)
		return 1
	}

	if len(args) == 0 {
		if err := e.console(); err != nil {
			e.fail(err)
			return 1
		}
		return 0
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	if err := e.dispatch(ctx, args[0], args[1:]); err != nil {
		e.fail(err)
		return 1
	}
	return 0
}

// displayPath shortens a path for display: the home directory becomes ~,
// and a path longer than limit keeps its end, which names the project.
func displayPath(path, home string, limit int) string {
	if home != "" && (path == home || strings.HasPrefix(path, home+"/")) {
		path = "~" + strings.TrimPrefix(path, home)
	}
	if r := []rune(path); len(r) > limit {
		path = "..." + string(r[len(r)-limit+3:])
	}
	return path
}

// fail reports an error to the user.
func (e *env) fail(err error) {
	fmt.Fprintln(e.errw, e.st.bad.Render("h2h:"), err)
}

func (e *env) dispatch(ctx context.Context, name string, args []string) error {
	for _, c := range commands() {
		if c.name == name {
			return c.run(e, ctx, args)
		}
	}
	return fmt.Errorf("unknown command %q; see help", name)
}

// console opens the full-screen console. A line starting with "/" is a
// command, and anything else is a question for the default harness.
func (e *env) console() error {
	if !term.IsTerminal(int(e.in.Fd())) || !term.IsTerminal(int(e.tty.Fd())) {
		return e.script()
	}
	harnessName := "the default harness"
	cfg, cfgErr := config.Load(e.root)
	if cfgErr == nil {
		harnessName = cfg.DefaultHarness
	}
	intro := "Type " + e.st.accent.Render("/") + " to see the commands, or type a question for " + harnessName + ".\n" +
		e.st.dim.Render("Sessions keep running after you leave.") + "\n\n"
	if cfgErr != nil {
		// A broken config must not lock the user out of /config edit.
		intro = e.st.bad.Render("h2h: ") + cfgErr.Error() + "\nFix it with " + e.st.accent.Render("/config edit") + ".\n\n"
	}

	suggestions := []console.Suggestion{}
	for _, c := range commands() {
		suggestions = append(suggestions, console.Suggestion{Text: "/" + c.name, Usage: c.usage, Help: c.help})
	}
	suggestions = append(suggestions, console.Suggestion{Text: "/exit", Help: "Leave the console; sessions keep running"})

	return console.Run(console.Options{
		In:    e.in,
		Out:   e.tty,
		Theme: console.Theme{Accent: e.st.accent, Dim: e.st.dim, Frame: frameColor},
		Title: e.st.accent.Render("H2H") + " " + e.st.dim.Render(Version+"  ·  ") + displayPath(e.root, os.Getenv("HOME"), 40) +
			e.st.dim.Render("  ·  default harness ") + harnessName,
		Intro:     intro,
		Status:    e.statusLine,
		Commands:  suggestions,
		Arguments: e.argumentSuggestions,
		Handle: func(ctx context.Context, ui console.UI, line string) bool {
			// The command writes to the console, not to the terminal.
			inConsole := *e
			inConsole.out, inConsole.errw, inConsole.ui = ui, ui, ui
			return inConsole.line(ctx, line)
		},
	})
}

// script reads lines from input that is not a terminal, such as a pipe.
func (e *env) script() error {
	sc := bufio.NewScanner(e.in)
	for sc.Scan() {
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		quit := e.line(ctx, sc.Text())
		cancel()
		if quit {
			return nil
		}
	}
	return sc.Err()
}

// line runs one line typed into the console and reports whether to leave.
func (e *env) line(ctx context.Context, line string) (quit bool) {
	line = strings.TrimSpace(line)
	var err error
	switch {
	case line == "":
		return false
	case line == "/exit" || line == "/quit":
		return true
	case strings.HasPrefix(line, "/"):
		fields := strings.Fields(line[1:])
		if len(fields) == 0 {
			fields = []string{"help"}
		}
		err = e.dispatch(ctx, fields[0], fields[1:])
	default:
		err = e.ask(ctx, line)
	}
	if err != nil {
		e.fail(err)
	}
	return false
}

func (e *env) help(_ context.Context, _ []string) error {
	fmt.Fprintln(e.out, e.st.title.Render("Commands")+e.st.dim.Render("  In the h2h console put a / in front, for example /ps."))
	fmt.Fprintln(e.out)
	for _, c := range commands() {
		fmt.Fprintf(e.out, "  %s %s %s\n", e.st.accent.Render(fmt.Sprintf("%-7s", c.name)), e.st.dim.Render(fmt.Sprintf("%-30s", c.usage)), c.help)
	}
	fmt.Fprintln(e.out)
	fmt.Fprintln(e.out, "Run h2h with no command to open the console. There, text without a / is")
	fmt.Fprintln(e.out, "sent to the default harness as a question, and /exit leaves.")
	return nil
}
