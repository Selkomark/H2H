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
	// Confirm asks a yes-or-no question and waits for the answer.
	Confirm(question string) bool
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
}

// View is a page of live information with its own tab, beside the
// transcript.
type View struct {
	// Name is the label of the tab.
	Name string
	// Command, when typed, switches to this view instead of running anything.
	Command string
	// Render returns the content of the view. It is called every few
	// seconds while the console is open.
	Render func() string
}

// Options configures a console.
type Options struct {
	In    *os.File
	Out   *os.File
	Theme Theme
	// Title is shown at the top of the screen.
	Title string
	// Intro is the first text in the transcript.
	Intro string
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
	p := tea.NewProgram(newModel(o, ui), tea.WithAltScreen(), tea.WithInput(o.In), tea.WithOutput(o.Out))
	ui.p = p
	_, err := p.Run()
	return err
}

// programUI is the UI a command sees. Each call becomes a message to the
// running program, so commands never touch the screen themselves.
type programUI struct {
	p *tea.Program
}

func (u *programUI) Write(b []byte) (int, error) {
	u.p.Send(outputMsg(sanitize(string(b))))
	return len(b), nil
}

func (u *programUI) Confirm(question string) bool {
	reply := make(chan bool, 1)
	u.p.Send(confirmMsg{question: question, reply: reply})
	return <-reply
}

func (u *programUI) Foreground(fn func() error) error {
	reply := make(chan error, 1)
	u.p.Send(foregroundMsg{fn: fn, reply: reply})
	return <-reply
}

// foreground adapts a function to what Bubble Tea runs with the terminal
// released. The function uses the real terminal itself.
type foreground struct{ fn func() error }

func (f foreground) Run() error          { return f.fn() }
func (f foreground) SetStdin(io.Reader)  {}
func (f foreground) SetStdout(io.Writer) {}
func (f foreground) SetStderr(io.Writer) {}
