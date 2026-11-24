// Package console is the full-screen interface of h2h: one framed page with
// a header and tabs at the top, a transcript or a live view in the middle,
// an input at the bottom, and suggestions that appear as the user types.
//
// It knows nothing about what a line means. Commands live in package cli,
// which hands the console its suggestions and a handler, so the console and
// the command line stay two views onto the same code.
package console

import (
	"context"
	"io"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Suggestion is one entry offered while the user types.
type Suggestion struct {
	Text  string // what is inserted, such as "/ps" or a session ID
	Usage string // arguments it takes, if any
	Help  string // one line on what it is
}

// UI is how a running command talks to the user through the console.
type UI interface {
	// Write adds text to the transcript.
	io.Writer
	// Confirm asks a yes-or-no question in a dialog and waits for the answer.
	Confirm(question string) bool
	// Choose shows options to tick in a dialog. It returns the IDs of those
	// ticked, and false if the user cancelled.
	Choose(question string, options []Option) ([]string, bool)
	// Chat opens a conversation in a box over the page.
	Chat(title string) Chat
	// Clear empties the page. The lines the user entered stay in reach of
	// the Up key.
	Clear()
	// Load replaces the page and the entered lines with those of another
	// session, followed by note. It is how a command switches sessions.
	Load(s State, note string)
	// Foreground gives fn the whole terminal, for an editor or an attached
	// session, and takes the screen back when fn returns.
	Foreground(fn func() error) error
}

// Theme is the look of the console, supplied by the caller so the colours
// are defined in one place.
type Theme struct {
	Accent lipgloss.Style         // the prompt, selected entries, typed lines
	Dim    lipgloss.Style         // hints and secondary text
	Frame  lipgloss.TerminalColor // the frame around the page and the rules inside it
	Hover  lipgloss.TerminalColor // the background of the message under the mouse; nil for none
}

// View is a live list with its own tab, beside the transcript. The user
// moves through its rows with the arrow keys and acts on the selected one.
type View struct {
	// Name is the label of the tab.
	Name string
	// Command, when typed, switches to this view instead of running anything.
	Command string
	// Table returns the content of the view. It is called every few
	// seconds while the console is open.
	Table func() Table
	// Actions are what the user can do to the selected row.
	Actions []Action
}

// Table is the content of a view at one moment.
type Table struct {
	Header string
	Rows   []Row
	// Empty is shown in place of the rows when there are none.
	Empty string
	// Graph, if set, makes the view a drawing of boxes and arrows instead
	// of rows. The user moves between the boxes and inspects one.
	Graph *Graph
}

// Row is one selectable line of a view.
type Row struct {
	ID   string // what an action is applied to; stable between refreshes
	Text string
}

// Action is a key that acts on the selected row of a view, by running a
// line exactly as if the user had typed it.
type Action struct {
	// Key is "enter" or a single character.
	Key string
	// Label names the action in the hint line.
	Label string
	// Line returns the line to run for the row with the given ID.
	Line func(id string) string
	// Confirm, if set, is asked first. %s stands for the row's ID.
	Confirm string
}

// State is what a console session consists of: enough to close the
// console and carry on later where it was left.
type State struct {
	Transcript string   // everything on the page, with its colours
	History    []string // the lines the user entered, oldest first
	Starts     []int    // where each message starts in the transcript, in bytes
}

// Request is a question that no command of the user's asked: something
// outside the console needs an answer, and the console puts it to the
// user as soon as it can.
type Request struct {
	ID       string // tells one request from another
	Question string
	Options  []Option // the answers to pick one from
	// Answer is given the ID of the option picked, or "" when the user
	// left the dialog without picking.
	Answer func(option string)
}

// Options configures a console.
type Options struct {
	In    *os.File
	Out   *os.File
	Theme Theme
	// Title is shown at the top of the screen.
	Title string
	// Intro is the first text in the transcript, or with Restore, the text
	// added after what was restored.
	Intro string
	// Restore, if set, is an earlier session to carry on from.
	Restore *State
	// Save, if set, is given the session's state after each line has run
	// and when the console closes.
	Save func(State)
	// Requests, if set, returns a question waiting for the user, or nil.
	// It is called every few seconds.
	Requests func() *Request
	// Status returns the line under the title. It is called every few
	// seconds, so it can show live state.
	Status func() string
	// Views are the tabs that follow the transcript's own tab.
	Views []View
	// Commands are offered while the first word of a line is typed.
	Commands []Suggestion
	// Arguments returns what to offer after a command, given its name
	// without the slash.
	Arguments func(command string) []Suggestion
	// Handle runs one line and reports whether the console should close.
	// Its context is cancelled when the user presses Ctrl-C while it runs.
	Handle func(ctx context.Context, ui UI, line string) (quit bool)
}

// Run shows the console until the user leaves it.
func Run(o Options) error {
	ui := &programUI{}
	p := tea.NewProgram(newModel(o, ui), tea.WithAltScreen(), tea.WithMouseAllMotion(), tea.WithInput(o.In), tea.WithOutput(o.Out))
	ui.p = p
	// Ask the terminal to tell Shift+Enter from Enter while the console
	// has it, and put that back when it does not.
	if o.Out != nil {
		setShiftEnter(o.Out, true)
		defer setShiftEnter(o.Out, false)
	}
	final, err := p.Run()
	if m, ok := final.(*model); ok {
		m.save()
	}
	return err
}

// programUI is the UI a command sees. Each call becomes a message to the
// running program, so commands never touch the screen themselves.
type programUI struct {
	p *tea.Program
	// command is the number of the command this UI was handed to, so
	// that what it prints can be told from what another one prints.
	command int
}

func (u *programUI) Write(b []byte) (int, error) {
	u.p.Send(outputMsg{from: u.command, text: sanitize(string(b))})
	return len(b), nil
}

func (u *programUI) Confirm(question string) bool {
	reply := make(chan bool, 1)
	u.p.Send(confirmMsg{question: question, reply: reply})
	return <-reply
}

func (u *programUI) Choose(question string, options []Option) ([]string, bool) {
	reply := make(chan choice, 1)
	u.p.Send(chooseMsg{question: question, options: options, reply: reply})
	c := <-reply
	return c.ids, c.ok
}

func (u *programUI) Chat(title string) Chat {
	u.p.Send(chatOpenMsg{title: title})
	return programChat{p: u.p}
}

func (u *programUI) Clear() { u.p.Send(clearMsg{}) }

func (u *programUI) Load(s State, note string) {
	u.p.Send(loadMsg{state: s, note: note})
}

func (u *programUI) Foreground(fn func() error) error {
	reply := make(chan error, 1)
	u.p.Send(foregroundMsg{fn: fn, reply: reply})
	return <-reply
}

// foreground adapts a function to what Bubble Tea runs with the terminal
// released. The function uses the real terminal itself, so the console's
// own key reporting is switched off for as long as it runs.
type foreground struct {
	fn  func() error
	out io.Writer
}

func (f foreground) Run() error {
	setShiftEnter(f.out, false)
	defer setShiftEnter(f.out, true)
	return f.fn()
}
func (f foreground) SetStdin(io.Reader)  {}
func (f foreground) SetStdout(io.Writer) {}
func (f foreground) SetStderr(io.Writer) {}
