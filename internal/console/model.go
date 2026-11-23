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
	maxSuggestions = 10
	maxTranscript  = 400_000 // bytes kept in the transcript
	statusInterval = 2 * time.Second
	promptText     = "> "
	transcriptTab  = "Console"

	// Rows the page spends outside the middle section: the top border, the
	// title and the tabs; the rule above the middle; the rule above the
	// input, the input and the bottom border; and the hint under the page.
	chromeHeight = 3 + 1 + 3 + 1
	// Columns the frame and its padding take on each line.
	chromeWidth = 4

	idleHint        = "enter send · tab complete · shift+tab switch view · ↑↓ history · pgup/pgdn scroll"
	busyHint        = "working · ctrl+c to cancel"
	confirmHint     = "y to confirm · any other key to decline"
	exitHint        = "press ctrl+c again to exit"
	suggestionsHint = "↑↓ choose · tab complete · enter run · esc hide"
)

// Messages from a running command and from timers.
type (
	outputMsg string
	doneMsg   struct{ quit bool }
	statusMsg struct {
		status string
		views  []string
	}
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
	tab           int      // 0 is the transcript, n is Views[n-1]
	views         []string // the last rendering of each view

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
	m.apply(m.snapshot())
	return m
}

// snapshot reads the live state shown outside the transcript. It may touch
// the disk, so it runs off the interface's own loop where it can.
func (m *model) snapshot() statusMsg {
	var s statusMsg
	if m.o.Status != nil {
		s.status = m.o.Status()
	}
	for _, v := range m.o.Views {
		s.views = append(s.views, v.Render())
	}
	return s
}

func (m *model) apply(s statusMsg) {
	m.status, m.views = s.status, s.views
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textinput.Blink, m.pollStatus())
}

func (m *model) pollStatus() tea.Cmd {
	return tea.Tick(statusInterval, func(time.Time) tea.Msg { return m.snapshot() })
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
		m.apply(msg)
		return m, m.pollStatus()

	case doneMsg:
		m.busy, m.cancel = false, nil
		m.apply(m.snapshot()) // a command usually changes what the views show
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
	case tea.KeyShiftTab:
		m.tab = (m.tab + 1) % (len(m.o.Views) + 1)
		m.apply(m.snapshot())
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
		m.remember(line)
		m.setLine("")
		// A view's command shows the view. Anything else runs, with the
		// transcript in front so its output is seen.
		for n, v := range m.o.Views {
			if line == v.Command {
				m.tab = n + 1
				m.apply(m.snapshot())
				return m, nil
			}
		}
		m.tab = 0
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
	m.print(m.o.Theme.Accent.Render(promptText+line) + "\n")

	ctx, cancel := context.WithCancel(context.Background())
	m.busy, m.cancel = true, cancel
	run := func() tea.Msg {
		defer cancel()
		return doneMsg{quit: m.o.Handle(ctx, m.ui, line)}
	}
	return tea.Batch(m.spin.Tick, run)
}

// remember adds a line to the history.
func (m *model) remember(line string) {
	if len(m.history) == 0 || m.history[len(m.history)-1] != line {
		m.history = append(m.history, line)
	}
	m.histPos = len(m.history)
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

// middleHeight is how many rows the section between the header and the
// input has.
func (m *model) middleHeight() int {
	return max(1, m.height-chromeHeight)
}

// layout sizes the transcript to the space the other parts leave.
func (m *model) layout() {
	if m.width == 0 {
		return
	}
	inner := max(1, m.width-chromeWidth)
	m.input.Width = max(1, inner-lipgloss.Width(promptText)-1)
	m.view.Width = inner
	m.view.Height = max(1, m.middleHeight()-len(m.shown()))
	m.view.SetContent(lipgloss.NewStyle().Width(inner).Render(strings.TrimRight(m.transcript, "\n")))
}

// The page is three framed sections stacked into one box. The lower two
// start with a rule that joins the sides, which is their top border.
var (
	topFrame = lipgloss.RoundedBorder()
	midFrame = lipgloss.Border{Top: "─", Left: "│", Right: "│", TopLeft: "├", TopRight: "┤"}
	endFrame = lipgloss.Border{Top: "─", Bottom: "─", Left: "│", Right: "│", TopLeft: "├", TopRight: "┤", BottomLeft: "╰", BottomRight: "╯"}
)

func (m *model) View() string {
	if m.width == 0 {
		return ""
	}
	th := m.o.Theme
	inner := max(1, m.width-chromeWidth)
	section := lipgloss.NewStyle().BorderForeground(th.Frame).Padding(0, 1).Width(m.width - 2)

	header := section.Border(topFrame, true, true, false, true).
		Render(clip(m.o.Title, inner) + "\n" + m.tabRow(inner))

	shown := m.shown()
	rows := m.suggestionRows(shown, inner)
	var middle []string
	if m.tab == 0 {
		middle = append(middle, m.view.View())
	} else {
		middle = append(middle, fit(m.views[m.tab-1], inner, m.middleHeight()-len(rows)))
	}
	middle = append(middle, rows...)
	body := section.Border(midFrame, true, true, false, true).Render(strings.Join(middle, "\n"))

	var line, hint string
	switch {
	case m.confirm != nil:
		line, hint = th.Accent.Render(m.confirm.question)+" [y/N]", confirmHint
	case m.busy:
		line, hint = m.spin.View()+" "+th.Dim.Render("working"), busyHint
	default:
		line, hint = m.input.View(), idleHint
		if len(shown) > 0 {
			hint = suggestionsHint
		}
		if m.exitArmed {
			hint = exitHint
		}
	}
	input := section.Border(endFrame).Render(clip(line, inner))

	return strings.Join([]string{header, body, input, th.Dim.Render(clip(" "+hint, m.width))}, "\n")
}

// tabRow shows the tabs on the left and the status on the right.
func (m *model) tabRow(width int) string {
	th := m.o.Theme
	names := []string{transcriptTab}
	for _, v := range m.o.Views {
		names = append(names, v.Name)
	}
	for n, name := range names {
		if n == m.tab {
			names[n] = th.Accent.Underline(true).Render(name)
		} else {
			names[n] = th.Dim.Render(name)
		}
	}
	tabs := strings.Join(names, "   ")
	if gap := width - lipgloss.Width(tabs) - lipgloss.Width(m.status); gap >= 3 {
		return tabs + strings.Repeat(" ", gap) + m.status
	}
	return clip(tabs, width)
}

// suggestionRows draws the suggestions on screen, one per row.
func (m *model) suggestionRows(shown []Suggestion, width int) []string {
	th := m.o.Theme
	nameWidth := 0
	for _, s := range shown {
		nameWidth = max(nameWidth, lipgloss.Width(strings.TrimSpace(s.Text+" "+s.Usage)))
	}
	offset := 0
	if len(m.offers) > maxSuggestions {
		offset = min(max(0, m.selected-maxSuggestions+1), len(m.offers)-maxSuggestions)
	}
	rows := make([]string, len(shown))
	for i, s := range shown {
		name := strings.TrimSpace(s.Text + " " + s.Usage)
		name += strings.Repeat(" ", nameWidth-lipgloss.Width(name)+2)
		row := "  " + name + th.Dim.Render(s.Help)
		if i+offset == m.selected {
			row = th.Accent.Render("› "+name) + s.Help
		}
		rows[i] = clip(row, width)
	}
	return rows
}

// clip cuts a line to a width, so it can never wrap and push the layout
// out of place.
func clip(s string, width int) string {
	return lipgloss.NewStyle().MaxWidth(width).Render(s)
}

// fit makes text exactly height rows of at most width columns.
func fit(text string, width, height int) string {
	lines := strings.Split(strings.TrimRight(text, "\n"), "\n")
	for len(lines) < height {
		lines = append(lines, "")
	}
	lines = lines[:max(0, height)]
	for n, l := range lines {
		lines[n] = clip(l, width)
	}
	return strings.Join(lines, "\n")
}
