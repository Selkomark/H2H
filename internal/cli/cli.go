// Package cli holds the commands of h2h and wires them to the command line
// and to the interactive console. "h2h ps" and "/ps" run the same function.
package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"time"

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
	root string
	in   *os.File   // the terminal's input, for programs given the whole terminal
	tty  *os.File   // the terminal's output, likewise
	out  io.Writer  // where a command's output goes
	errw io.Writer  // where its errors go
	ui   console.UI // questions to the user, and handing over the terminal
	// here is the console session the command runs in, or nil for a
	// command run from the shell.
	here *openSession
	// conv keeps the conversation with the default harness that the line
	// belongs to: the console session's, or one for lines read from a
	// pipe. It is nil for a command run from the shell.
	conv talks
	// heeded is what the full-screen console has told the user about
	// harnesses that wait for them, or nil where nothing tells them, as
	// for a command run from the shell.
	heeded  *heeded
	environ []string
	st      styles
	// link is the console's conversation over ACP, a demo; see acp.go.
	// It is nil for a command run from the shell.
	link *acpLink
}

// shellUI serves a command run from the shell or from piped input, where
// the terminal is used directly.
type shellUI struct {
	in    *os.File
	tty   *os.File // the terminal's output, where a dialog is drawn
	out   io.Writer
	theme console.Theme
}

func (u shellUI) Write(b []byte) (int, error) { return u.out.Write(b) }

func (u shellUI) Foreground(fn func() error) error { return fn() }

// Chat holds a conversation as lines: what the other side says is printed,
// and each answer is the next line of input.
func (u shellUI) Chat(title string) console.Chat {
	fmt.Fprintln(u.out, title)
	fmt.Fprintln(u.out, "Answer each question on a line of its own. An empty line, or the end of the input, cancels.")
	return lineChat{in: u.in, out: u.out}
}

type lineChat struct {
	in  *os.File
	out io.Writer
}

func (c lineChat) Say(text string)  { fmt.Fprintf(c.out, "\n%s\n", strings.TrimSpace(text)) }
func (c lineChat) Wait(note string) { fmt.Fprintf(c.out, "\n(%s)\n", note) }

func (c lineChat) Show(draw func(width int) string) { fmt.Fprintf(c.out, "\n%s\n", draw(textWidth)) }
func (c lineChat) Close()                           {}

func (c lineChat) Ask() (string, bool) {
	fmt.Fprint(c.out, "> ")
	line, _ := readLine(c.in)
	line = strings.TrimSpace(line)
	return line, line != ""
}

// Clear does nothing: outside the console there is no page to empty.
func (u shellUI) Clear() {}

// Load does nothing: outside the console there is no page to replace.
func (u shellUI) Load(console.State, string) {}

// interactive reports whether a dialog can be shown: both ends are a
// terminal. Otherwise questions are asked and answered as lines.
func (u shellUI) interactive() bool {
	return term.IsTerminal(int(u.in.Fd())) && term.IsTerminal(int(u.tty.Fd()))
}

// Confirm asks a yes-or-no question. Anything but yes is no.
func (u shellUI) Confirm(question string) bool {
	if u.interactive() {
		yes := console.Confirm(u.in, u.tty, u.theme, question)
		answer := "no"
		if yes {
			answer = "yes"
		}
		fmt.Fprintf(u.out, "%s %s\n", question, answer) // the dialog leaves nothing on screen
		return yes
	}
	fmt.Fprintf(u.out, "%s [y/N] ", question)
	line, _ := readLine(u.in)
	answer := strings.ToLower(strings.TrimSpace(line))
	return answer == "y" || answer == "yes"
}

// Choose asks which options to take. Without a terminal the answer is a
// line: "all", or the numbers of the options, and anything else is none.
func (u shellUI) Choose(question string, options []console.Option) ([]string, bool) {
	if u.interactive() {
		ids, ok := console.Choose(u.in, u.tty, u.theme, question, options)
		picked := "none"
		if ok && len(ids) > 0 {
			picked = strings.Join(ids, ", ")
		}
		fmt.Fprintf(u.out, "%s %s\n", question, picked)
		return ids, ok
	}
	fmt.Fprintln(u.out, question)
	for n, o := range options {
		fmt.Fprintf(u.out, "  %d. %s\n", n+1, o.Label)
	}
	fmt.Fprint(u.out, "Type the numbers to take, or all [none] ")
	line, _ := readLine(u.in)
	fmt.Fprintln(u.out)
	return pickByNumber(line, options)
}

// Pick asks which one option to take. Without a terminal the answer is a
// line with the option's number; an empty line takes the first.
func (u shellUI) Pick(question string, options []console.Option) (string, bool) {
	if len(options) == 0 {
		return "", false
	}
	if u.interactive() {
		id, ok := console.Pick(u.in, u.tty, u.theme, question, options)
		if ok {
			fmt.Fprintf(u.out, "%s %s\n", question, id)
		}
		return id, ok
	}
	fmt.Fprintln(u.out, question)
	for n, o := range options {
		fmt.Fprintf(u.out, "  %d. %s\n", n+1, o.Label)
	}
	fmt.Fprint(u.out, "Type a number [1] ")
	line, _ := readLine(u.in)
	line = strings.TrimSpace(line)
	if line == "" {
		return options[0].ID, true
	}
	n, err := strconv.Atoi(line)
	if err != nil || n < 1 || n > len(options) {
		return "", false
	}
	return options[n-1].ID, true
}

// pickByNumber reads an answer typed as a line: "all", or option numbers
// separated by spaces or commas. An answer it cannot read takes nothing.
func pickByNumber(line string, options []console.Option) ([]string, bool) {
	fields := strings.FieldsFunc(strings.ToLower(line), func(r rune) bool { return r == ' ' || r == ',' })
	if len(fields) == 0 {
		return nil, false
	}
	taken := make([]bool, len(options))
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		switch {
		case f == "all":
			for i := range taken {
				taken[i] = true
			}
		case err != nil || n < 1 || n > len(options):
			return nil, false
		default:
			taken[n-1] = true
		}
	}
	var ids []string
	for i, o := range options {
		if taken[i] {
			ids = append(ids, o.ID)
		}
	}
	return ids, true
}

// readLine reads one line from in, a byte at a time, so that nothing after
// the line is taken from whoever reads next. It reports false at the end
// of the input.
func readLine(in io.Reader) (string, bool) {
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := in.Read(b)
		if n == 0 || err != nil {
			return string(line), len(line) > 0
		}
		if b[0] == '\n' {
			return string(line), true
		}
		line = append(line, b[0])
	}
}

type command struct {
	name  string
	usage string
	help  string
	run   func(e *env, ctx context.Context, args []string) error
}

func commands() []command {
	return []command{
		{"ps", "", "List the harnesses that were started", (*env).ps},
		{"start", "[harness | domain]", "Start a harness; the default one if none is named", (*env).start},
		{"attach", "[id]", "Open a running harness's terminal; leave it with Ctrl-\\", (*env).attach},
		{"logs", "[-f] [id]", "Print a harness's terminal log; -f follows it", (*env).logs},
		{"stop", "[id | all]", "Stop a running harness, or all of them", (*env).stop},
		{"rm", "[id | all]", "Remove a harness and its workspace, or all of them", (*env).rm},
		{"land", "[id]", "Bring a harness's work from its workspace onto the project's branch", (*env).land},
		{"map", "", "Draw the domains and the flow of work between them", (*env).mapCmd},
		{"ask", "<id | harness | domain> <question>", "Hand a request to a running harness and wait for its report", (*env).askHarness},
		{"acp", "[harness | off]", "Demo: talk to a harness over ACP; it can run H2H's commands as tools", (*env).acpCmd},
		{"model", "[model]", "Demo, with ACP: show or change the model of the session", acpOption("model")},
		{"mode", "[mode]", "Demo, with ACP: show or change the mode of the session", acpOption("mode")},
		{"sessions", "[rm <session> | rm all]", "List the saved console sessions, or remove them", (*env).sessions},
		{"resume", "[session]", "In the console, switch to a saved session", (*env).resume},
		{"cls", "", "In the console, empty the page and carry on in the same session and conversation", (*env).cls},
		{"clear", "", "In the console, start a new conversation with the default harness; the page stays", (*env).clearTalk},
		{"install", "[--update] [harness ...]", "Install missing harnesses; --update offers newer versions", (*env).install},
		{"doctor", "", "Check that everything H2H needs is in place", (*env).doctor},
		{"config", "[show | --manual]", "Set the config up in a conversation; --manual opens it in your editor", (*env).config},
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
		case stateCommand:
			// Run by a harness's state hooks, through h2h-state. It never
			// fails the hook: a harness may stop on a hook that fails.
			if err := state(args[1:], os.Stdin, os.Getenv); err != nil {
				fmt.Fprintln(os.Stderr, "h2h-state:", err)
			}
			return 0
		case permitCommand:
			// Run by a harness's permission hook, through h2h-permit.
			if err := permit(os.Stdin, os.Stdout, os.Getenv); err != nil {
				fmt.Fprintln(os.Stderr, "h2h-permit:", err)
				return 1
			}
			return 0
		case reportCommand:
			// Run by a harness inside its session, through h2h-report.
			if err := report(args[1:], os.Stdin, os.Getenv); err != nil {
				fmt.Fprintln(os.Stderr, "h2h-report:", err)
				return 1
			}
			fmt.Println("Reported. The user has your answer.")
			return 0
		case "version", "--version", "-v":
			fmt.Println("h2h", Version)
			return 0
		case "-h", "--help":
			args[0] = "help"
		}
	}

	// "h2h --resume" opens the console on an earlier session.
	resume, resumeID := false, ""
	if len(args) > 0 && (args[0] == "-r" || args[0] == "--resume") {
		if len(args) > 2 {
			fmt.Fprintln(os.Stderr, "h2h: usage: h2h --resume [session]")
			return 2
		}
		resume = true
		if len(args) == 2 {
			resumeID = args[1]
		}
		args = nil
	}

	e := &env{in: os.Stdin, tty: os.Stdout, out: os.Stdout, errw: os.Stderr, environ: os.Environ(), st: newStyles()}
	e.ui = shellUI{in: e.in, tty: e.tty, out: e.out, theme: e.theme()}
	if len(args) > 0 && args[0] == "help" {
		_ = e.help(context.Background(), nil)
		return 0
	}

	cwd, err := os.Getwd()
	if err == nil {
		e.root, err = workspace.Root(context.Background(), cwd)
	}
	// doctor runs anywhere: saying what is missing is its job.
	if err != nil && !(len(args) > 0 && args[0] == "doctor") {
		e.fail(err)
		return 1
	}

	if len(args) == 0 {
		if err := e.console(resume, resumeID); err != nil {
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

// tildePath writes the home directory at the start of a path as ~.
func tildePath(path, home string) string {
	if home != "" && (path == home || strings.HasPrefix(path, home+"/")) {
		return "~" + strings.TrimPrefix(path, home)
	}
	return path
}

// displayPath shortens a path for display: the home directory becomes ~,
// and a path longer than limit keeps its end, which names the project.
func displayPath(path, home string, limit int) string {
	path = tildePath(path, home)
	if r := []rune(path); len(r) > limit {
		path = "..." + string(r[len(r)-limit+3:])
	}
	return path
}

// theme is the look the console and its dialogs share.
func (e *env) theme() console.Theme {
	return console.Theme{Accent: e.st.accent, Dim: e.st.dim, Warn: e.st.warn, Frame: frameColor, Hover: hoverColor, Glow: e.st.glow()}
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
// command, and anything else is a question for the default harness. With
// resume it carries on an earlier session: the one named, or one the user
// picks.
func (e *env) console(resume bool, resumeID string) error {
	if !term.IsTerminal(int(e.in.Fd())) || !term.IsTerminal(int(e.tty.Fd())) {
		if resume {
			return errors.New("--resume needs a terminal")
		}
		return e.script()
	}
	if e.link == nil {
		e.link = &acpLink{}
	}
	defer e.link.close()
	e.here = &openSession{root: e.root, current: newConsoleSession(e.root, time.Now())}
	e.conv = e.here
	e.heeded = &heeded{} // the console tells the user of a harness that waits
	var restore *console.State
	if resume {
		saved, ok, err := e.sessionToResume(resumeID)
		if err != nil || !ok {
			return err // nil when the user cancelled the choice
		}
		e.here.current = saved
		restore = &console.State{Transcript: saved.Transcript, History: saved.History, Starts: saved.Starts, LastCommand: saved.latestCommand()}
	}
	harnessName := "the default harness"
	cfg, cfgErr := config.Load(e.root)
	if cfgErr == nil {
		harnessName = cfg.DefaultHarness
	}
	intro := "Type " + e.st.accent.Render("/") + " to see the commands, or type a question for " + harnessName + ".\n" +
		e.st.dim.Render("Harnesses keep running after you leave.") + "\n\n"
	if cfgErr != nil {
		// A broken config must not lock the user out of /config.
		intro = e.st.bad.Render("h2h: ") + cfgErr.Error() + "\nCorrect it with " + e.st.accent.Render("/config "+manualFlag) + ", or have it rewritten with " + e.st.accent.Render("/config") + ".\n\n"
	}

	if restore != nil && cfgErr == nil {
		intro = e.resumedNote(e.here.current)
	}
	title := e.st.gradient("H2H") + " " + e.st.dim.Render(Version)
	if e.link.on() {
		intro = "Text you type goes to " + e.st.accent.Render(e.link.want) + " over ACP, a demo. It can run H2H's commands as tools.\n" +
			e.st.dim.Render("/model and /mode change its session, Ctrl-C stops a turn, /acp off goes back to the default harness.") + "\n\n"
		title += e.st.dim.Render(" · ACP")
	}

	suggestions := []console.Suggestion{}
	for _, c := range commands() {
		suggestions = append(suggestions, console.Suggestion{Text: "/" + c.name, Usage: c.usage, Help: c.help})
	}
	suggestions = append(suggestions, console.Suggestion{Text: "/exit", Help: "Leave the console; harnesses keep running, and the session is saved"})

	err := console.Run(console.Options{
		In:       e.in,
		Out:      e.tty,
		Theme:    e.theme(),
		Title:    title,
		Path:     tildePath(e.root, os.Getenv("HOME")),
		Intro:    intro,
		Restore:  restore,
		Save:     e.here.save,
		Requests: e.pendingPermit,
		Heed:     e.heed,
		Views: []console.View{{
			Name:    "Map",
			Command: "/map",
			Table:   e.mapView,
		}, {
			Name:    "Harnesses",
			Command: "/ps",
			Table:   e.sessionsView,
			Actions: []console.Action{
				{Key: "enter", Label: "attach", Line: func(id string) string { return "/attach " + id }},
				{Key: "s", Label: "stop", Line: func(id string) string { return "/stop " + id }, Confirm: "Stop %s?"},
				{Key: "d", Label: "delete", Line: func(id string) string { return "/rm " + id }, Confirm: "Delete %s and its workspace?"},
				// Landing shows its plan and asks for itself.
				{Key: "l", Label: "land", Line: func(id string) string { return "/land " + id }},
				// The commands for all of them ask for themselves, with the counts.
				{Key: "S", Label: "stop all", Line: func(string) string { return "/stop all" }},
				{Key: "D", Label: "delete all", Line: func(string) string { return "/rm all" }},
			},
		}, {
			Name:    "Sessions",
			Command: "/sessions",
			Table:   e.consoleSessionsView,
			Actions: []console.Action{
				{Key: "enter", Label: "resume", Line: func(id string) string { return "/resume " + id }},
				{Key: "d", Label: "delete", Line: func(id string) string { return "/sessions rm " + id }, Confirm: "Delete session %s?"},
			},
		}},
		Commands:  suggestions,
		Arguments: e.argumentSuggestions,
		Handle: func(ctx context.Context, ui console.UI, line string) bool {
			// The command writes to the console, not to the terminal.
			inConsole := *e
			inConsole.out, inConsole.errw, inConsole.ui = ui, ui, ui
			return inConsole.line(ctx, line)
		},
	})
	if id, saved := e.here.id(); saved {
		fmt.Fprintln(e.out, e.st.dim.Render("Resume this session with: ")+e.st.accent.Render("h2h --resume "+id))
	}
	return err
}

// script reads lines from input that is not a terminal, such as a pipe.
// A command that asks a question reads its answer from the next line.
func (e *env) script() error {
	e.conv = &scriptTalk{}
	if e.link == nil {
		e.link = &acpLink{}
	}
	defer e.link.close()
	for {
		text, ok := readLine(e.in)
		if !ok {
			return nil
		}
		ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
		quit := e.line(ctx, text)
		cancel()
		if quit {
			return nil
		}
	}
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
	case e.link.on():
		err = e.acpConverse(ctx, line)
	default:
		err = e.converse(ctx, line)
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
		fmt.Fprintf(e.out, "  %s %s %s\n", e.st.accent.Render(fmt.Sprintf("%-8s", c.name)), e.st.dim.Render(fmt.Sprintf("%-34s", c.usage)), c.help)
	}
	fmt.Fprintln(e.out)
	fmt.Fprintln(e.out, "Run h2h with no command to open the console. There, text without a / is")
	fmt.Fprintln(e.out, "sent to the default harness as a question, and /exit leaves.")
	fmt.Fprintln(e.out)
	fmt.Fprintln(e.out, "The console saves each session. Open an earlier one, with its page and the")
	fmt.Fprintln(e.out, "lines you entered, with: "+e.st.accent.Render("h2h --resume [session]")+" (or -r).")
	return nil
}
