package console

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/spinner"
	"github.com/charmbracelet/bubbles/textarea"
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

	tabSep = "   "

	idleHint        = "enter send · shift+enter new line · tab complete · shift+tab switch view · ↑↓ history · wheel, pgup/pgdn, shift+↑↓ scroll · end latest · drag to copy"
	busyHint        = "working · ctrl+c to cancel"
	dialogLine      = "answer in the box above"
	exitHint        = "press ctrl+c again to exit"
	suggestionsHint = "↑↓ choose · tab or click complete · enter run · esc hide"
)

// Messages from a running command and from timers.
type (
	// outputMsg is text printed by the command numbered from.
	outputMsg struct {
		from int
		text string
	}
	// doneMsg says the command numbered from has finished.
	doneMsg struct {
		quit bool
		from int
	}
	statusMsg struct {
		status  string
		views   []Table
		request *Request // a question waiting for the user, if any
	}
	// confirmMsg asks the user a question. The answer goes to reply when a
	// command asked, or to then when the console itself did.
	confirmMsg struct {
		question string
		reply    chan<- bool
		then     func() tea.Cmd
	}
	// chooseMsg asks the user to tick options, for a command.
	chooseMsg struct {
		question string
		options  []Option
		reply    chan<- choice
	}
	choice struct {
		ids []string
		ok  bool
	}
	// clearMsg empties the page.
	clearMsg struct{}
	// loadMsg replaces the session on the page with another one.
	loadMsg struct {
		state State
		note  string
	}
	foregroundMsg struct {
		fn    func() error
		reply chan<- error
	}
)

type model struct {
	o  Options
	ui UI

	input textarea.Model
	view  viewport.Model
	spin  spinner.Model

	width, height int
	transcript    string
	lines         []string           // the transcript as wrapped for the screen
	starts        []int              // where each message starts in the transcript, in bytes
	sections      []int              // where each message starts in lines
	hoverRow      int                // the row of the transcript under the mouse, or -1
	sel           *selection         // text dragged over in the transcript
	unseen        int                // lines printed below the view since the user scrolled away from the end
	flash         string             // a short note in the hint line, until the next key
	copy          func(string) error // puts text on the clipboard
	status        string
	tab           int             // 0 is the transcript, n is Views[n-1]
	views         []Table         // the last content of each view
	selectedID    []string        // the selected row of each view
	notice        string          // what the last action on a view printed
	noticeOf      int             // the number of the command whose output becomes the notice, or 0
	noticing      strings.Builder // what that command has printed so far
	commands      int             // how many commands have been started; the last one's number

	offers      []Suggestion
	forArgument bool
	selected    int
	hidden      bool // suggestions dismissed until the line changes

	history []string
	histPos int // index into history; len(history) means the line being typed

	// busy says a command is running, and cancel stops every one that
	// is. More than one can run: while the first works, the views stay
	// usable, and an action chosen there runs beside it.
	busy      bool
	cancel    context.CancelFunc
	running   int
	dialog    *dialog                 // the question on screen, if any
	chat      *chatBox                // the conversation on screen, if any
	answered  func(d *dialog) tea.Cmd // what to do with its answer
	exitArmed bool
}

func newModel(o Options, ui UI) *model {
	in := newInput(o.Theme, "Type / for commands, or ask a question")

	sp := spinner.New()
	sp.Spinner = spinner.MiniDot
	sp.Style = o.Theme.Accent

	m := &model{o: o, ui: ui, input: in, view: viewport.New(0, 0), spin: sp, transcript: o.Intro}
	m.copy = systemClipboard(o.Out)
	m.starts, m.hoverRow = []int{0}, -1
	if r := o.Restore; r != nil {
		m.restore(*r, o.Intro)
	}
	m.histPos = len(m.history)
	m.selectedID = make([]string, len(o.Views))
	m.apply(m.snapshot())
	return m
}

// maxHistory is how many entered lines a session keeps.
const maxHistory = 1000

// restore puts an earlier session on the page, followed by note.
func (m *model) restore(s State, note string) {
	m.transcript = s.Transcript
	if m.transcript != "" && !strings.HasSuffix(m.transcript, "\n") {
		m.transcript += "\n"
	}
	// Message starts that do not fit the transcript are dropped rather
	// than trusted: the file may have been edited.
	m.starts = []int{0}
	for _, at := range s.Starts {
		if at > m.starts[len(m.starts)-1] && at < len(m.transcript) {
			m.starts = append(m.starts, at)
		}
	}
	if note != "" && m.transcript != "" {
		m.starts = append(m.starts, len(m.transcript))
	}
	m.transcript += note
	m.history = append([]string{}, s.History...)
	m.histPos = len(m.history)
}

// save hands the session's state to whoever keeps it.
func (m *model) save() {
	if m.o.Save == nil {
		return
	}
	history := m.history
	if len(history) > maxHistory {
		history = history[len(history)-maxHistory:]
	}
	m.o.Save(State{Transcript: m.transcript, History: append([]string{}, history...), Starts: append([]int{}, m.starts...)})
}

// out is the terminal the console draws on, or nil when it has none, as
// in a test.
func (m *model) out() io.Writer {
	if m.o.Out == nil {
		return nil
	}
	return m.o.Out
}

// snapshot reads the live state shown outside the transcript. It may touch
// the disk, so it runs off the interface's own loop where it can.
func (m *model) snapshot() statusMsg {
	var s statusMsg
	if m.o.Status != nil {
		s.status = m.o.Status()
	}
	for _, v := range m.o.Views {
		s.views = append(s.views, v.Table())
	}
	if m.o.Requests != nil {
		s.request = m.o.Requests()
	}
	return s
}

func (m *model) apply(s statusMsg) {
	m.status, m.views = s.status, s.views
	// A question from outside is asked when nothing else is being asked.
	if r := s.request; r != nil && m.dialog == nil && m.chat == nil && m.width > 0 {
		m.dialog = newChoice(r.Question, r.Options)
		m.dialog.single = true
		// The focus starts on the last option, which by convention is the
		// one that agrees to nothing: a question that appears while the
		// user is typing must not be answered yes by their next Enter.
		m.dialog.cursor = len(r.Options) - 1
		m.answered = func(d *dialog) tea.Cmd {
			picked, label := "", "dismissed"
			if d.accepted {
				picked = d.chosen()[0]
				label = d.options[d.cursor].Label
			}
			m.print(m.o.Theme.Accent.Render(r.Question) + "\n" + label + "\n\n")
			r.Answer(picked)
			return nil
		}
		m.layout()
		m.view.GotoBottom()
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.pollStatus())
}

func (m *model) pollStatus() tea.Cmd {
	return tea.Tick(statusInterval, func(time.Time) tea.Msg { return m.snapshot() })
}

func (m *model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Whatever brought the view back to the bottom, nothing is unseen there.
	defer m.settle()
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.sel = nil // the text wraps differently now
		m.layout()
		if m.chat != nil {
			m.chat.resize(max(1, m.width-chromeWidth), m.middleHeight())
		}
		m.view.GotoBottom()
		return m, nil

	case outputMsg:
		m.print(msg.text)
		if msg.from != 0 && msg.from == m.noticeOf {
			m.noticing.WriteString(msg.text)
		}
		return m, nil

	case statusMsg:
		m.apply(msg)
		return m, m.pollStatus()

	case doneMsg:
		if m.running--; m.running <= 0 {
			m.running, m.busy, m.cancel = 0, false, nil
		}
		// The notice under a view is what its own action printed, and
		// not what another command printed in the meantime.
		if msg.from != 0 && msg.from == m.noticeOf {
			m.notice, m.noticeOf = strings.TrimSpace(m.noticing.String()), 0
			m.noticing.Reset()
		}
		m.apply(m.snapshot()) // a command usually changes what the views show
		defer m.save()
		if msg.quit {
			return m, tea.Quit
		}
		// Leave one blank line after a command's output, however it ended.
		// A page that was just cleared stays empty.
		if m.transcript != "" {
			if !strings.HasSuffix(m.transcript, "\n") {
				m.print("\n")
			}
			m.print("\n")
		}
		return m, nil

	case confirmMsg:
		m.ask(msg)
		return m, nil

	case chatOpenMsg:
		m.chat = newChatBox(msg.title, m.o.Theme)
		m.chat.input.Focus()
		m.chat.resize(max(1, m.width-chromeWidth), m.middleHeight())
		return m, nil

	case chatSayMsg:
		if m.chat != nil {
			m.chat.add(msg.text)
		}
		return m, nil

	case chatShowMsg:
		if m.chat != nil {
			m.chat.show(msg.draw)
		}
		return m, nil

	case chatWaitMsg:
		if m.chat != nil {
			m.chat.note = msg.note
		}
		return m, nil

	case chatAskMsg:
		if m.chat == nil {
			msg.reply <- chatAnswer{} // the box is gone: nobody can answer
			return m, nil
		}
		m.chat.asking = msg.reply
		return m, nil

	case chatCloseMsg:
		if m.chat != nil {
			m.chat.answer(chatAnswer{})
			m.chat = nil
		}
		return m, nil

	case clearMsg:
		// A clean page for the same session: what was entered before can
		// still be recalled, and nothing that pointed into the old page
		// still holds.
		m.transcript, m.starts = "", []int{0}
		m.sel, m.notice, m.noticeOf, m.unseen, m.hoverRow = nil, "", 0, 0, -1
		m.noticing.Reset()
		m.layout()
		m.view.GotoTop()
		return m, nil

	case loadMsg:
		// The page is another session's now: nothing that pointed into
		// the old one still holds.
		m.restore(msg.state, msg.note)
		m.sel, m.notice, m.noticeOf, m.tab = nil, "", 0, 0
		m.noticing.Reset()
		m.layout()
		m.view.GotoBottom()
		return m, nil

	case chooseMsg:
		m.dialog = newChoice(msg.question, msg.options)
		m.answered = func(d *dialog) tea.Cmd {
			ids := d.chosen()
			if !d.accepted {
				ids = nil
			}
			picked := "none"
			if len(ids) > 0 {
				picked = strings.Join(ids, ", ")
			}
			m.print(m.o.Theme.Accent.Render(msg.question) + " " + picked + "\n")
			msg.reply <- choice{ids: ids, ok: d.accepted}
			return nil
		}
		m.layout()
		m.view.GotoBottom()
		return m, nil

	case foregroundMsg:
		return m, tea.Exec(foreground{fn: msg.fn, out: m.out()}, func(err error) tea.Msg {
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

	case tea.MouseMsg:
		return m.mouse(msg)
	}

	if shiftEnter(msg) {
		// A new line in whichever input the user is typing into.
		switch {
		case m.dialog != nil:
		case m.chat != nil:
			m.chat.breakLine()
		case !m.busy:
			m.input.InsertString("\n")
			m.refresh()
		}
		return m, nil
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg) // cursor blink
	return m, cmd
}

func (m *model) key(k tea.KeyMsg) (tea.Model, tea.Cmd) {
	if k.Type != tea.KeyCtrlC {
		m.exitArmed = false
	}
	m.flash = ""
	if m.chat != nil && m.dialog == nil {
		// Leaving the conversation also stops the command behind it.
		if m.chat.key(k, m.o.Theme) && m.cancel != nil {
			m.cancel()
		}
		return m, nil
	}
	// End belongs to the line being typed, and to the dialog, when there is
	// one. Ctrl+End is the transcript's alone.
	if k.Type == tea.KeyCtrlEnd || (k.Type == tea.KeyEnd && m.dialog == nil && m.input.Value() == "") {
		m.jump()
		return m, nil
	}
	switch k.Type {
	case tea.KeyShiftUp:
		m.view.SetYOffset(m.view.YOffset - 1)
		return m, nil
	case tea.KeyShiftDown:
		m.view.SetYOffset(m.view.YOffset + 1)
		return m, nil
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

	if m.dialog != nil {
		m.dialog.key(k)
		cmd := m.settleDialog()
		m.layout() // a box that opened more of its text takes more rows
		return m, cmd
	}

	if m.busy {
		// The page's input waits for the command, but a view does not:
		// its rows can be chosen and acted on while the command works.
		if m.tab > 0 {
			if cmd, handled := m.viewKey(k); handled {
				return m, cmd
			}
		}
		if k.Type == tea.KeyCtrlC && m.cancel != nil {
			m.cancel()
		}
		return m, nil
	}

	// On a view, with nothing typed, the keys belong to the list.
	if m.tab > 0 && m.input.Value() == "" {
		if cmd, handled := m.viewKey(k); handled {
			return m, cmd
		}
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
		// In text of several lines the arrows move through the lines, and
		// reach the history only from its first and last line.
		if at := m.input.Line(); (step < 0 && at > 0) || (step > 0 && at < m.input.LineCount()-1) {
			var cmd tea.Cmd
			m.input, cmd = m.input.Update(k)
			return m, cmd
		}
		m.recall(step)
		return m, nil

	case tea.KeyTab:
		if shown := m.shown(); len(shown) > 0 {
			m.setLine(accept(m.input.Value(), shown[m.selected], m.forArgument))
		}
		return m, nil

	case tea.KeyEnter, tea.KeyCtrlJ:
		// Enter sends. With Alt, as Ctrl+J, or after a backslash, it
		// breaks the line instead.
		if breaksLine(k) {
			m.input.InsertString("\n")
			m.refresh()
			return m, nil
		}
		if text, ok := endsWithBackslash(m.input.Value()); ok {
			m.input.SetValue(text + "\n")
			m.refresh()
			return m, nil
		}
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

// settleDialog takes the dialog off screen once it has its answer, and
// passes the answer on.
func (m *model) settleDialog() tea.Cmd {
	d := m.dialog
	if d == nil || !d.done {
		return nil
	}
	answered := m.answered
	m.dialog, m.answered = nil, nil
	m.layout()
	return answered(d)
}

// ask puts a yes-or-no question on screen. The answer goes to the command
// that asked, or runs then when the console itself asked.
func (m *model) ask(c confirmMsg) {
	m.dialog = newConfirm(c.question)
	m.answered = func(d *dialog) tea.Cmd {
		if c.reply != nil {
			answer := "no"
			if d.accepted {
				answer = "yes"
			}
			m.print(m.o.Theme.Accent.Render(c.question) + " " + answer + "\n")
			c.reply <- d.accepted
			return nil
		}
		if d.accepted {
			return c.then()
		}
		return nil
	}
	m.layout()
	m.view.GotoBottom() // the question is about what was printed last
}

// submit runs a line through the handler, off the interface's own loop.
func (m *model) submit(line string) tea.Cmd {
	// A line and everything it prints are one message.
	if len(m.transcript) > 0 {
		m.starts = append(m.starts, len(m.transcript))
	}
	m.print(m.o.Theme.Accent.Render(promptText+strings.ReplaceAll(line, "\n", "\n"+continuation)) + "\n")

	// The command gets a UI of its own, which marks what it prints.
	m.commands++
	number, ui := m.commands, m.ui
	if p, ok := m.ui.(*programUI); ok {
		ui = &programUI{p: p.p, command: number}
	}
	ctx, cancel := context.WithCancel(context.Background())
	already, others := m.busy, m.cancel
	m.running++
	m.busy, m.cancel = true, func() {
		cancel()
		if others != nil {
			others()
		}
	}
	run := func() tea.Msg {
		defer cancel()
		return doneMsg{quit: m.o.Handle(ctx, ui, line), from: number}
	}
	if already {
		return run // the spinner is already turning
	}
	return tea.Batch(m.spin.Tick, run)
}

// viewKey handles a key for the list on screen. It reports whether the key
// was one of the list's.
func (m *model) viewKey(k tea.KeyMsg) (tea.Cmd, bool) {
	v := m.o.Views[m.tab-1]
	if d, ok := m.graph(); ok {
		// The arrows move between the boxes, and Enter inspects one.
		at := m.graphSelection(d)
		switch k.Type {
		case tea.KeyLeft:
			m.selectedID[m.tab-1] = d.neighbour(at, -1, 0)
		case tea.KeyRight:
			m.selectedID[m.tab-1] = d.neighbour(at, 1, 0)
		case tea.KeyUp:
			m.selectedID[m.tab-1] = d.neighbour(at, 0, -1)
		case tea.KeyDown:
			m.selectedID[m.tab-1] = d.neighbour(at, 0, 1)
		case tea.KeyEnter:
			m.inspect()
		default:
			return nil, false
		}
		return nil, true
	}
	switch k.Type {
	case tea.KeyUp:
		m.moveRow(-1, true)
		return nil, true
	case tea.KeyDown:
		m.moveRow(1, true)
		return nil, true
	}
	key := ""
	switch {
	case k.Type == tea.KeyEnter:
		key = "enter"
	case k.Type == tea.KeyRunes && len(k.Runes) == 1:
		key = string(k.Runes)
	}
	for _, a := range v.Actions {
		if a.Key == key {
			return m.runAction(a), true
		}
	}
	return nil, false
}

// moveRow moves the selection of the view on screen by step rows. The
// keyboard wraps around the ends, and the wheel stops at them.
func (m *model) moveRow(step int, wrap bool) {
	rows := m.views[m.tab-1].Rows
	if len(rows) == 0 {
		return
	}
	at := m.selection() + step
	if wrap {
		at = (at + len(rows)) % len(rows)
	}
	m.selectedID[m.tab-1] = rows[min(max(0, at), len(rows)-1)].ID
	m.notice = ""
}

// runAction applies an action to the selected row of the view on screen,
// asking first if the action says to.
func (m *model) runAction(a Action) tea.Cmd {
	rows := m.views[m.tab-1].Rows
	if len(rows) == 0 {
		return nil
	}
	id := rows[m.selection()].ID
	run := func() tea.Cmd {
		cmd := m.submit(a.Line(id))
		m.notice, m.noticeOf = "", m.commands
		m.noticing.Reset()
		return cmd
	}
	if a.Confirm == "" {
		return run()
	}
	m.ask(confirmMsg{question: fmt.Sprintf(a.Confirm, id), then: run})
	return nil
}

// selection returns the index of the selected row of the view on screen.
// The selection follows a row by its ID, so a refresh does not move it.
func (m *model) selection() int {
	for n, r := range m.views[m.tab-1].Rows {
		if r.ID == m.selectedID[m.tab-1] {
			return n
		}
	}
	return 0
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
	if m.hidden || m.busy || m.dialog != nil || m.chat != nil {
		return nil
	}
	if len(m.offers) > maxSuggestions {
		// Keep the selected entry in view.
		start := m.suggestionOffset()
		return m.offers[start : start+maxSuggestions]
	}
	return m.offers
}

// print adds text to the transcript, staying at the bottom if the user has
// not scrolled away from it.
func (m *model) print(text string) {
	follow, before := m.view.AtBottom(), len(m.lines)
	m.transcript += text
	if over := len(m.transcript) - maxTranscript; over > 0 {
		// Drop the oldest text, from a line boundary.
		if cut := strings.IndexByte(m.transcript[over:], '\n'); cut >= 0 {
			m.transcript = m.transcript[over+cut+1:]
			m.sel = nil // its lines have moved
			starts := []int{0}
			for _, s := range m.starts {
				if s -= over + cut + 1; s > 0 {
					starts = append(starts, s)
				}
			}
			m.starts = starts
		}
	}
	m.layout()
	if follow {
		m.jump()
		return
	}
	// What arrived below a view that stays where it is, for the jump label.
	m.unseen += max(0, len(m.lines)-before)
}

// middleHeight is how many rows the section between the header and the
// input has.
func (m *model) middleHeight() int {
	// The input takes one row, and more as its text grows.
	return max(1, m.height-chromeHeight-(m.inputHeight()-1))
}

// inputHeight is how many rows the input at the bottom of the page takes.
// While a command runs or a question is open, a single line stands there.
func (m *model) inputHeight() int {
	if m.busy || m.dialog != nil || m.chat != nil || m.width == 0 {
		return 1
	}
	return inputRows(m.input.Value(), max(1, m.width-chromeWidth))
}

// layout sizes the transcript to the space the other parts leave.
func (m *model) layout() {
	if m.width == 0 {
		return
	}
	inner := max(1, m.width-chromeWidth)
	// The rows the transcript has change with what is under it: the input
	// growing over several lines, suggestions, a dialog. A user who was
	// reading the latest output is kept there, instead of being left
	// short of the end without having scrolled.
	follow := m.view.AtBottom()
	fitInput(&m.input, inner)
	m.view.Width = inner
	m.view.Height = max(1, m.middleHeight()-len(m.shown()))
	if m.dialog != nil {
		// A dialog at the bottom of the page leaves the rows above it.
		if _, _, top, docked := m.dialogPlace(inner); docked {
			m.view.Height = top
		}
	}
	// Each message is wrapped by itself, so the line it starts on is known.
	wrap := lipgloss.NewStyle().Width(inner)
	// A blank line above the first message and below the last gives every
	// message a line on each side for its hover background to spill into.
	blank := strings.Repeat(" ", inner)
	m.lines, m.sections = append(m.lines[:0], blank), m.sections[:0]
	for n, start := range m.starts {
		text := strings.TrimRight(m.transcript[start:], "\n")
		if n+1 < len(m.starts) {
			text = strings.TrimSuffix(m.transcript[start:m.starts[n+1]], "\n")
		}
		m.sections = append(m.sections, len(m.lines))
		m.lines = append(m.lines, strings.Split(wrap.Render(text), "\n")...)
	}
	m.lines = append(m.lines, blank)
	m.view.SetContent(strings.Join(m.lines, "\n"))
	if follow {
		m.view.GotoBottom()
	}
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
	if m.dialog != nil {
		// The question takes the middle of the page until it is answered.
		box, left, top, docked := m.dialogPlace(inner)
		if docked {
			// What the page showed stays above the question.
			if m.tab == 0 {
				middle = append(middle, m.transcriptView())
			} else {
				middle = append(middle, gutter(fit(m.viewText(), inner, top)))
			}
			pad := strings.Repeat(" ", left)
			middle = append(middle, gutter(fit(pad+strings.ReplaceAll(box, "\n", "\n"+pad), inner, m.middleHeight()-top)))
		} else {
			box, _, _ = centered(box, inner, m.middleHeight())
			middle = append(middle, gutter(fit(box, inner, m.middleHeight())))
		}
	} else if m.chat != nil {
		box, _, _ := centered(m.chat.render(th, m.spin.View()), inner, m.middleHeight())
		middle = append(middle, gutter(fit(box, inner, m.middleHeight())))
	} else if m.tab == 0 {
		middle = append(middle, m.transcriptView())
	} else {
		middle = append(middle, gutter(fit(m.viewText(), inner, m.middleHeight()-len(rows))))
	}
	for _, r := range rows {
		middle = append(middle, gutter(r))
	}
	// The middle section pads its own lines; see gutter.
	body := section.Padding(0).Border(midFrame, true, true, false, true).Render(strings.Join(middle, "\n"))

	var line, hint string
	typing := false // the input itself is on screen
	switch {
	case m.dialog != nil:
		line, hint = th.Dim.Render(dialogLine), m.dialog.hint()
	case m.chat != nil:
		line, hint = th.Dim.Render(dialogLine), m.chat.hint()
	case m.busy:
		line, hint = m.spin.View()+" "+th.Dim.Render("working"), busyHint
		if m.tab > 0 {
			hint = m.viewHint() + hintSep + "ctrl+c cancel"
		}
	default:
		typing = true
		line, hint = m.input.View(), idleHint
		if m.tab > 0 && m.input.Value() == "" {
			hint = m.viewHint()
		}
		if len(shown) > 0 {
			hint = suggestionsHint
		}
		if m.exitArmed {
			hint = exitHint
		}
	}
	if m.flash != "" {
		hint = m.flash
	}
	// The input draws its own rows at the right width. Anything else
	// that stands in its place is one line, cut so it cannot wrap.
	if !typing {
		line = clip(line, inner)
	}
	input := section.Border(endFrame).Render(line)

	return strings.Join([]string{header, body, input, th.Dim.Render(clip(" "+hint, m.width))}, "\n")
}

// graphOrigin is where a graph view's drawing starts inside the middle
// section: a blank row above it, and two columns to its left.
const graphLeft, graphTop = 2, 1

// graph returns the graph of the view on screen, laid out, and whether
// the view is one.
func (m *model) graph() (drawing, bool) {
	if m.tab == 0 || m.views[m.tab-1].Graph == nil {
		return drawing{}, false
	}
	return layoutGraph(*m.views[m.tab-1].Graph), true
}

// graphSelection is the selected node of the graph on screen: the one the
// user chose, or the first when that one is gone.
func (m *model) graphSelection(d drawing) string {
	g := m.views[m.tab-1].Graph
	if slices.ContainsFunc(g.Nodes, func(n Node) bool { return n.ID == m.selectedID[m.tab-1] }) {
		return m.selectedID[m.tab-1]
	}
	return d.neighbour("", 0, 0)
}

// inspect opens a box with the detail of the selected node of the graph
// on screen.
func (m *model) inspect() {
	d, ok := m.graph()
	if !ok {
		return
	}
	id := m.graphSelection(d)
	for _, n := range m.views[m.tab-1].Graph.Nodes {
		if n.ID == id && n.Detail != nil {
			m.dialog = newInfo(n.Title, n.Detail, n.More)
			m.answered = func(*dialog) tea.Cmd { return nil }
			m.layout()
		}
	}
}

// viewText draws the list of the view on screen.
func (m *model) viewText() string {
	th, t := m.o.Theme, m.views[m.tab-1]
	if d, ok := m.graph(); ok {
		if len(t.Graph.Nodes) == 0 {
			return t.Empty
		}
		pad := strings.Repeat(" ", graphLeft)
		return strings.Repeat("\n", graphTop) + pad + strings.ReplaceAll(d.render(th, m.graphSelection(d)), "\n", "\n"+pad)
	}
	if len(t.Rows) == 0 {
		return t.Empty
	}
	lines := []string{"  " + t.Header}
	at := m.selection()
	for n, r := range t.Rows {
		if n == at {
			lines = append(lines, th.Accent.Render("› ")+r.Text)
		} else {
			lines = append(lines, "  "+r.Text)
		}
	}
	if m.notice != "" {
		lines = append(lines, "", m.notice)
	}
	return strings.Join(lines, "\n")
}

// viewHint lists the keys of the view on screen.
func (m *model) viewHint() string {
	if _, ok := m.graph(); ok {
		return strings.Join([]string{"←↑↓→ select", "enter inspect", "/ command", "shift+tab switch view"}, hintSep)
	}
	parts := []string{"↑↓ select"}
	for _, a := range m.o.Views[m.tab-1].Actions {
		parts = append(parts, a.Key+" "+a.Label)
	}
	return strings.Join(append(parts, "/ command", "shift+tab switch view"), hintSep)
}

// tabRow shows the tabs on the left and the status on the right.
func (m *model) tabRow(width int) string {
	th := m.o.Theme
	names := m.tabNames()
	for n, name := range names {
		if n == m.tab {
			names[n] = th.Accent.Underline(true).Render(name)
		} else {
			names[n] = th.Dim.Render(name)
		}
	}
	tabs := strings.Join(names, tabSep)
	if gap := width - lipgloss.Width(tabs) - lipgloss.Width(m.status); gap >= 3 {
		return tabs + strings.Repeat(" ", gap) + m.status
	}
	return clip(tabs, width)
}

// tabNames are the labels of the tabs, left to right.
func (m *model) tabNames() []string {
	names := []string{transcriptTab}
	for _, v := range m.o.Views {
		names = append(names, v.Name)
	}
	return names
}

// suggestionOffset is the index of the first suggestion on screen.
func (m *model) suggestionOffset() int {
	if len(m.offers) > maxSuggestions {
		return min(max(0, m.selected-maxSuggestions+1), len(m.offers)-maxSuggestions)
	}
	return 0
}

// suggestionRows draws the suggestions on screen, one per row.
func (m *model) suggestionRows(shown []Suggestion, width int) []string {
	th := m.o.Theme
	nameWidth := 0
	for _, s := range shown {
		nameWidth = max(nameWidth, lipgloss.Width(strings.TrimSpace(s.Text+" "+s.Usage)))
	}
	offset := m.suggestionOffset()
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
