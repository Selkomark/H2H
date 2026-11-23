package console

import (
	"context"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	maxSuggestions  = 10
	maxTranscript   = 400_000 // bytes kept in the transcript
	statusInterval  = 2 * time.Second
	headerHeight    = 3 // title, status, rule
	inputHeight     = 3 // framed input
	footerHeight    = 1
	promptText      = "> "
	idleHint        = "enter send · tab complete · ↑↓ history · pgup/pgdn scroll · ctrl+c clear or exit"
	busyHint        = "working · ctrl+c to cancel"
	confirmHint     = "y to confirm · any other key to decline"
	exitHint        = "press ctrl+c again to exit"
	suggestionsHint = "↑↓ choose · tab complete · enter run · esc hide"
)

// Messages from a running command and from timers.
type (
	outputMsg  string
	doneMsg    struct{ quit bool }
	statusMsg  string
	confirmMsg struct {
		question string
		reply    chan<- bool
	}
	foregroundMsg struct {
		fn    func() error
		reply chan<- error
	}
)

type model struct {
	o  Options
	ui UI

	input textinput.Model
	view  viewport.Model
	spin  spinner.Model

	width, height int
	transcript    string
	status        string

	offers      []Suggestion
	forArgument bool
	selected    int
	hidden      bool // suggestions dismissed until the line changes

	history []string
	histPos int // index into history; len(history) means the line being typed

	busy      bool
	cancel    context.CancelFunc
	confirm   *confirmMsg
	exitArmed bool
}

func newModel(o Options, ui UI) *model {
	in := textinput.New()
	in.Prompt = promptText
	in.PromptStyle = o.Theme.Accent
	in.Placeholder = "Type / for commands, or ask a question"
	in.Focus()

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = o.Theme.Accent

	m := &model{o: o, ui: ui, input: in, view: viewport.New(0, 0), spin: sp, transcript: o.Intro}
	if o.Status != nil {
		m.status = o.Status()
	}
	return m
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.pollStatus())
}

func (m *model) pollStatus() tea.Cmd {
	if m.o.Status == nil {
		return nil
	}
	return tea.Tick(statusInterval, func(time.Time) tea.Msg { return statusMsg(m.o.Status()) })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.layout()
		m.view.GotoBottom()
		return m, nil

	case outputMsg:
		m.print(string(msg))
		return m, nil

	case statusMsg:
		m.status = string(msg)
		return m, m.pollStatus()

	case doneMsg:
		m.busy, m.cancel = false, nil
		if m.o.Status != nil {
			m.status = m.o.Status() // a command usually changes what the status shows
		}
		if msg.quit {
			return m, tea.Quit
		}
		// Leave one blank line after a command's output, however it ended.
		if !strings.HasSuffix(m.transcript, "\n") {
			m.print("\n")
		}
		m.print("\n")
		return m, nil

	case confirmMsg:
		m.confirm = &msg
		return m, nil

	case foregroundMsg:
		return m, tea.Exec(foreground{msg.fn}, func(err error) tea.Msg {
			msg.reply <- err
			return nil
		})

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		return m, cmd

	case tea.KeyMsg:
		return m.key(msg)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg) // cursor blink
	return m, cmd
}

func (m *model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type != tea.KeyCtrlC {
		m.exitArmed = false
	}
	switch k.Type {
	case tea.KeyPgUp:
		m.view.HalfPageUp()
		return m, nil
	case tea.KeyPgDown:
		m.view.HalfPageDown()
		return m, nil
	}

	if m.confirm != nil {
		yes := k.Type == tea.KeyRunes && strings.EqualFold(string(k.Runes), "y")
		answer := "no"
		if yes {
			answer = "yes"
		}
		m.print(m.o.Theme.Accent.Render(m.confirm.question) + " " + answer + "\n")
		m.confirm.reply <- yes
		m.confirm = nil
		return m, nil
	}

	if m.busy {
		if k.Type == tea.KeyCtrlC && m.cancel != nil {
			m.cancel()
		}
		return m, nil
	}

	switch k.Type {
	case tea.KeyCtrlC:
		if m.input.Value() != "" {
			m.setLine("")
			return m, nil
		}
		if m.exitArmed {
			return m, tea.Quit
		}
		m.exitArmed = true
		return m, nil

	case tea.KeyCtrlD:
		if m.input.Value() == "" {
			return m, tea.Quit
		}

	case tea.KeyEsc:
		m.hidden = true
		m.layout()
		return m, nil

	case tea.KeyUp, tea.KeyDown:
		step := 1
		if k.Type == tea.KeyUp {
			step = -1
		}
		if n := len(m.shown()); n > 0 {
			m.selected = (m.selected + step + n) % n
			return m, nil
		}
		m.recall(step)
		return m, nil

	case tea.KeyTab:
		if shown := m.shown(); len(shown) > 0 {
			m.setLine(accept(m.input.Value(), shown[m.selected], m.forArgument))
		}
		return m, nil

	case tea.KeyEnter:
		line := strings.TrimSpace(m.input.Value())
		// Enter on a half-typed command runs the highlighted one.
		if shown := m.shown(); len(shown) > 0 && !m.forArgument {
			line = shown[m.selected].Text
		}
		if line == "" {
			return m, nil
		}
		return m, m.submit(line)
	}

	before := m.input.Value()
	var cmd tea.Cmd
	m.input, cmd = m.input.Update(k)
	if m.input.Value() != before {
		m.refresh()
	}
	return m, cmd
}

// submit runs a line through the handler, off the interface's own loop.
func (m *model) submit(line string) tea.Cmd {
	if len(m.history) == 0 || m.history[len(m.history)-1] != line {
		m.history = append(m.history, line)
	}
	m.histPos = len(m.history)
	m.setLine("")
	m.print(m.o.Theme.Accent.Render(promptText+line) + "\n")

	ctx, cancel := context.WithCancel(context.Background())
	m.busy, m.cancel = true, cancel
	run := func() tea.Msg {
		defer cancel()
		return doneMsg{quit: m.o.Handle(ctx, m.ui, line)}
	}
	return tea.Batch(m.spin.Tick, run)
}

// recall moves through the lines entered before.
func (m *model) recall(step int) {
	pos := m.histPos + step
	if pos < 0 || pos > len(m.history) {
		return
	}
	m.histPos = pos
	line := ""
	if pos < len(m.history) {
		line = m.history[pos]
	}
	m.input.SetValue(line)
	m.input.CursorEnd()
	m.hidden = true // recalling a line is not asking for suggestions
	m.layout()
}

func (m *model) setLine(line string) {
	m.input.SetValue(line)
	m.input.CursorEnd()
	m.refresh()
}

// refresh recomputes the suggestions for the current line.
func (m *model) refresh() {
	m.offers, m.forArgument = suggest(m.input.Value(), m.o.Commands, m.o.Arguments)
	m.selected, m.hidden = 0, false
	m.layout()
}

// shown returns the suggestions on screen.
func (m *model) shown() []Suggestion {
	if m.hidden || m.busy || m.confirm != nil {
		return nil
	}
	if len(m.offers) > maxSuggestions {
		// Keep the selected entry in view.
		start := min(max(0, m.selected-maxSuggestions+1), len(m.offers)-maxSuggestions)
		return m.offers[start : start+maxSuggestions]
	}
	return m.offers
}

// print adds text to the transcript, staying at the bottom if the user has
// not scrolled away from it.
func (m *model) print(text string) {
	follow := m.view.AtBottom()
	m.transcript += text
	if over := len(m.transcript) - maxTranscript; over > 0 {
		// Drop the oldest text, from a line boundary.
		if cut := strings.IndexByte(m.transcript[over:], '\n'); cut >= 0 {
			m.transcript = m.transcript[over+cut+1:]
		}
	}
	m.layout()
	if follow {
		m.view.GotoBottom()
	}
}

// layout sizes the transcript to the space the other parts leave.
func (m *model) layout() {
	if m.width == 0 {
		return
	}
	m.input.Width = max(1, m.width-4-lipgloss.Width(promptText)-1)
	m.view.Width = m.width
	m.view.Height = max(1, m.height-headerHeight-inputHeight-footerHeight-len(m.shown()))
	m.view.SetContent(lipgloss.NewStyle().Width(m.width).Render(strings.TrimRight(m.transcript, "\n")))
}

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	th := m.o.Theme
	rule := lipgloss.NewStyle().Foreground(th.Frame).Render(strings.Repeat("─", m.width))
	parts := []string{clip(m.o.Title, m.width), clip(m.status, m.width), rule, m.view.View()}

	shown := m.shown()
	nameWidth := 0
	for _, s := range shown {
		nameWidth = max(nameWidth, lipgloss.Width(strings.TrimSpace(s.Text+" "+s.Usage)))
	}
	offset := 0
	if len(m.offers) > maxSuggestions {
		offset = min(max(0, m.selected-maxSuggestions+1), len(m.offers)-maxSuggestions)
	}
	for i, s := range shown {
		name := s.Text
		if s.Usage != "" {
			name += " " + s.Usage
		}
		name += strings.Repeat(" ", nameWidth-lipgloss.Width(name)+2)
		row := "  " + name + th.Dim.Render(s.Help)
		if i+offset == m.selected {
			row = th.Accent.Render("› "+name) + s.Help
		}
		parts = append(parts, clip(row, m.width))
	}

	var body, hint string
	switch {
	case m.confirm != nil:
		body, hint = th.Accent.Render(m.confirm.question)+" [y/N]", confirmHint
	case m.busy:
		body, hint = m.spin.View()+" "+th.Dim.Render("working"), busyHint
	default:
		body, hint = m.input.View(), idleHint
		if len(shown) > 0 {
			hint = suggestionsHint
		}
		if m.exitArmed {
			hint = exitHint
		}
	}
	box := lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.Frame).Padding(0, 1).Width(m.width - 2)
	parts = append(parts, box.Render(body), th.Dim.Render(clip(" "+hint, m.width)))
	return strings.Join(parts, "\n")
}

// clip cuts a line to the width of the screen, so it can never wrap and
// push the layout out of place.
func clip(s string, width int) string {
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}
