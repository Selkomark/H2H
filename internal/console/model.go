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
	"github.com/charmbracelet/x/ansi"
)

const (
	maxSuggestions = 10
	maxTranscript  = 400_000 // bytes kept in the transcript
	statusInterval = 2 * time.Second
	// mouseInterval is how often the console asks the terminal for the
	// mouse again, in case nothing else told it the mouse was lost.
	mouseInterval = 10 * time.Second
	promptText    = "> "
	transcriptTab = "Console"

	// Rows the page spends outside the middle section: the top border and
	// the header; the rule above the middle; the rule above the input, the
	// input and the bottom border; and the hint under the page.
	chromeHeight = 2 + 1 + 3 + 1
	// Columns the frame and its padding take on each line.
	chromeWidth = 4

	tabSep = " "
	// tabPad is the space inside a tab's label, on each side of its name.
	tabPad = 1

	idleHint    = "enter send · shift+enter new line · tab complete · shift+tab switch view · ↑↓ history · wheel, pgup/pgdn, shift+↑↓ scroll · end latest · drag to copy"
	busyHint    = "enter send · / commands run beside it · ctrl+c cancel"
	talkingHint = "enter queue a message · / commands run now · ctrl+c cancel"
	queuedHint  = "%d queued, sent when the answer ends · ↑ edit · esc drop · ctrl+c cancel"
	dialogLine  = "answer in the box above"
	// maxQueuedRows is how many queued messages are shown above the input.
	maxQueuedRows   = 3
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
		views   []Table
		request *Request // a question waiting for the user, if any
		notices []string // lines for the transcript, from Heed
		alert   string   // from Heed
	}
	// confirmMsg asks the user a question. The answer goes to reply when a
	// command asked, or to then when the console itself did.
	confirmMsg struct {
		from     int // the command that asked, or 0
		question string
		reply    chan<- bool
		then     func() tea.Cmd
	}
	// chooseMsg asks the user to tick options, for a command.
	chooseMsg struct {
		from     int // the command that asked
		question string
		options  []Option
		reply    chan<- choice
	}
	// pickMsg asks the user to choose one option, for a command.
	pickMsg struct {
		from     int // the command that asked
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
	// glowFor is how long the glow of the frame has been running.
	glowFor time.Duration

	width, height int
	transcript    string
	lines         []string           // the transcript as wrapped for the screen
	starts        []int              // where each message starts in the transcript, in bytes
	owners        []int              // the command whose line and output each message is, or 0; one per start
	sections      []int              // where each message starts in lines
	hoverRow      int                // the row of the transcript under the mouse, or -1
	sel           *selection         // text dragged over in the transcript
	unseen        int                // lines printed below the view since the user scrolled away from the end
	flash         string             // a short note in the hint line, until the next key
	copy          func(string) error // puts text on the clipboard
	alert         string             // shown in place of "working", from Heed
	tab           int                // 0 is the transcript, n is Views[n-1]
	views         []Table            // the last content of each view
	selectedID    []string           // the selected row of each view
	notice        string             // what the last action on a view printed
	noticeOf      int                // the number of the command whose output becomes the notice, or 0
	noticing      strings.Builder    // what that command has printed so far
	commands      int                // how many commands have been started; the last one's number

	offers      []Suggestion
	forArgument bool
	selected    int
	hidden      bool // suggestions dismissed until the line changes

	history     []string
	lastCommand string
	histPos     int // index into history; len(history) means the line being typed

	// busy says a command is running, and cancel stops every one that
	// is. More than one can run: the input stays open while one works, and
	// a command typed or chosen on a view runs beside it.
	busy    bool
	cancel  context.CancelFunc
	running map[int]bool // the numbers of the commands running
	// talking is the number of the message to the default harness being
	// answered, or 0. A message typed meanwhile waits in queued, and the
	// queued ones are sent together, as one message, when the answer ends.
	talking int
	queued  []string

	dialog    *dialog                 // the question on screen, if any
	waiting   []func()                // questions that open, in turn, once it is answered
	chat      *chatBox                // the conversation on screen, if any
	answered  func(d *dialog) tea.Cmd // what to do with its answer
	exitArmed bool
}

func newModel(o Options, ui UI) *model {
	in := newInput(o.Theme, "Type / for commands, or ask a question")

	sp := newSpinner(o.Theme)

	m := &model{o: o, ui: ui, input: in, view: viewport.New(0, 0), spin: sp, transcript: o.Intro}
	m.copy = systemClipboard(o.Out)
	m.starts, m.owners, m.hoverRow = []int{0}, []int{0}, -1
	m.running = map[int]bool{}
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
	m.owners = make([]int, len(m.starts)) // what was restored belongs to no command running now
	m.history = append([]string{}, s.History...)
	m.lastCommand = s.LastCommand
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
	m.o.Save(State{Transcript: m.transcript, History: append([]string{}, history...), Starts: append([]int{}, m.starts...), LastCommand: m.lastCommand})
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
	for _, v := range m.o.Views {
		s.views = append(s.views, v.Table())
	}
	if m.o.Requests != nil {
		s.request = m.o.Requests()
	}
	if m.o.Heed != nil {
		s.notices, s.alert = m.o.Heed()
	}
	return s
}

func (m *model) apply(s statusMsg) {
	m.views, m.alert = s.views, s.alert
	for _, n := range s.notices {
		// A message of its own, even in the middle of an answer.
		m.begin(0)
		m.print(n + "\n\n")
	}
	// A question from outside is asked when nothing else is being asked.
	if r := s.request; r != nil && m.dialog == nil && len(m.waiting) == 0 && m.chat == nil && m.width > 0 {
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
			m.begin(0)
			m.print(m.o.Theme.Accent.Render(r.Question) + "\n" + label + "\n\n")
			r.Answer(picked)
			return nil
		}
		m.layout()
		m.view.GotoBottom()
	}
}

func (m *model) Init() tea.Cmd {
	return tea.Batch(textarea.Blink, m.pollStatus(), m.pollMouse())
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
		return m, claimMouse

	case tea.FocusMsg:
		return m, claimMouse

	case tea.BlurMsg:
		// Whatever the mouse was doing, the console will not hear how it
		// ended.
		m.dropMouse()
		return m, nil

	case mouseTickMsg:
		return m, tea.Batch(claimMouse, m.pollMouse())

	case foregroundDoneMsg:
		return m, claimMouse

	case outputMsg:
		m.printFor(msg.from, msg.text)
		if msg.from != 0 && msg.from == m.noticeOf {
			m.noticing.WriteString(msg.text)
		}
		return m, nil

	case statusMsg:
		m.apply(msg)
		return m, m.pollStatus()

	case doneMsg:
		delete(m.running, msg.from)
		if len(m.running) == 0 {
			m.busy, m.cancel = false, nil
		}
		// The notice under a view is what its own action printed, and
		// not what another command printed in the meantime.
		if msg.from != 0 && msg.from == m.noticeOf {
			m.notice, m.noticeOf = strings.TrimSpace(m.noticing.String()), 0
			m.noticing.Reset()
		}
		// Leave one blank line after a command's output, however it ended.
		// A page that was just cleared stays empty.
		if at := m.sectionOf(msg.from); at >= 0 {
			if text := m.sectionText(at); text != "" {
				end := "\n"
				if !strings.HasSuffix(text, "\n") {
					end = "\n\n"
				}
				m.printFor(msg.from, end)
			}
		}
		m.apply(m.snapshot()) // a command usually changes what the views show
		defer m.save()
		if msg.quit {
			return m, tea.Quit
		}
		// The messages typed while the default harness answered go to it
		// now, together, as the next message.
		if msg.from != 0 && msg.from == m.talking {
			m.talking = 0
			if len(m.queued) > 0 {
				line := strings.Join(m.queued, "\n")
				m.queued = nil
				m.layout()
				return m, m.submit(line)
			}
		}
		return m, nil

	case confirmMsg:
		m.openDialog(func() { m.ask(msg) })
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
			m.nextDialog()
		}
		return m, nil

	case clearMsg:
		// A clean page for the same session: what was entered before can
		// still be recalled, and nothing that pointed into the old page
		// still holds.
		m.transcript, m.starts, m.owners = "", []int{0}, []int{0}
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

	case pickMsg:
		m.openDialog(func() {
			m.dialog = newChoice(msg.question, msg.options)
			m.dialog.single = true
			m.answered = func(d *dialog) tea.Cmd {
				picked, label := []string(nil), "none"
				if d.accepted {
					picked = d.chosen()[:1]
					label = d.options[d.cursor].Label
				}
				m.printFor(msg.from, m.o.Theme.Accent.Render(msg.question)+" "+label+"\n")
				msg.reply <- choice{ids: picked, ok: d.accepted}
				return nil
			}
			m.layout()
			m.view.GotoBottom()
		})
		return m, nil

	case chooseMsg:
		m.openDialog(func() {
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
				m.printFor(msg.from, m.o.Theme.Accent.Render(msg.question)+" "+picked+"\n")
				msg.reply <- choice{ids: ids, ok: d.accepted}
				return nil
			}
			m.layout()
			m.view.GotoBottom()
		})
		return m, nil

	case foregroundMsg:
		return m, tea.Exec(foreground{fn: msg.fn, out: m.out()}, foregroundDone(msg.reply))

	case spinner.TickMsg:
		if !m.busy {
			return m, nil
		}
		var cmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.glowFor += m.spin.Spinner.FPS
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
		default:
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

	// On a view, with nothing typed, the keys belong to the list, while a
	// command runs as well as when none does.
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
		if m.busy {
			// Stopping the answer also stops the messages queued behind
			// it, which come back to the input to be sent or dropped.
			m.unqueue()
			if m.cancel != nil {
				m.cancel()
			}
			return m, nil
		}
		if m.exitArmed {
			return m, tea.Quit
		}
		m.exitArmed = true
		return m, nil

	case tea.KeyCtrlD:
		if m.input.Value() == "" && !m.busy {
			return m, tea.Quit
		}

	case tea.KeyEsc:
		if len(m.shown()) == 0 && len(m.queued) > 0 {
			n := len(m.queued)
			m.queued = nil
			m.flash = fmt.Sprintf("dropped %d queued %s", n, plural(n, "message", "messages"))
			m.layout()
			return m, nil
		}
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
		// Up on an empty line takes the queued messages back to edit.
		if step < 0 && m.input.Value() == "" && len(m.queued) > 0 {
			m.unqueue()
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
		if isMessage(line) && m.talking != 0 {
			// The default harness is still answering: the message waits.
			m.queued = append(m.queued, line)
			m.layout()
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
	cmd := answered(d)
	m.nextDialog()
	return cmd
}

// openDialog puts a question on screen with open, or, while another is
// being answered, once that one is. Commands running side by side may ask
// at the same moment, and each gets its own answer.
func (m *model) openDialog(open func()) {
	if m.dialog == nil && m.chat == nil {
		open()
		return
	}
	m.waiting = append(m.waiting, open)
}

// nextDialog opens the question that waited longest, if nothing is on
// screen.
func (m *model) nextDialog() {
	if m.dialog != nil || m.chat != nil || len(m.waiting) == 0 {
		return
	}
	open := m.waiting[0]
	m.waiting = m.waiting[1:]
	open()
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
			m.printFor(c.from, m.o.Theme.Accent.Render(c.question)+" "+answer+"\n")
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
	m.commands++
	number, ui := m.commands, m.ui
	m.begin(number)
	m.print(m.o.Theme.Accent.Render(promptText+strings.ReplaceAll(line, "\n", "\n"+continuation)) + "\n")
	if !isMessage(line) {
		m.lastCommand = line
		m.save()
	}

	// The command gets a UI of its own, which marks what it prints.
	if p, ok := m.ui.(*programUI); ok {
		ui = &programUI{box: p.box, command: number}
	}
	if isMessage(line) {
		m.talking = number
	}
	ctx, cancel := context.WithCancel(context.Background())
	already, others := m.busy, m.cancel
	m.running[number] = true
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
	if !already {
		m.glowFor = 0 // the glow fades in afresh
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
	m.openDialog(func() { m.ask(confirmMsg{question: fmt.Sprintf(a.Confirm, id), then: run}) })
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
	if m.hidden || m.dialog != nil || m.chat != nil {
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
	m.place(len(m.transcript), text)
}

// place puts text into the transcript at a byte offset, moving the
// messages after it down. Like print, it keeps a user who was reading the
// latest output there.
func (m *model) place(at int, text string) {
	follow, before := m.view.AtBottom(), len(m.lines)
	m.transcript = m.transcript[:at] + text + m.transcript[at:]
	for n, s := range m.starts {
		if s >= at && n > 0 && at < len(m.transcript)-len(text) {
			m.starts[n] = s + len(text)
		}
	}
	if at < len(m.transcript)-len(text) {
		m.sel = nil // the lines under it have moved
	}
	m.trim()
	m.layout()
	if follow {
		m.jump()
		return
	}
	// What arrived below a view that stays where it is, for the jump label.
	m.unseen += max(0, len(m.lines)-before)
}

// trim drops the oldest text, from a line boundary, when the transcript
// has grown past its limit.
func (m *model) trim() {
	over := len(m.transcript) - maxTranscript
	if over <= 0 {
		return
	}
	cut := strings.IndexByte(m.transcript[over:], '\n')
	if cut < 0 {
		return
	}
	drop := over + cut + 1
	m.transcript = m.transcript[drop:]
	m.sel = nil // its lines have moved
	// The message the cut falls in starts the transcript now.
	starts, owners := []int{0}, []int{0}
	for n, s := range m.starts {
		if s -= drop; s > 0 {
			starts, owners = append(starts, s), append(owners, m.owners[n])
		} else {
			owners[0] = m.owners[n]
		}
	}
	m.starts, m.owners = starts, owners
}

// isMessage says whether a line is a message for the default harness,
// rather than a command.
func isMessage(line string) bool { return !strings.HasPrefix(line, "/") }

// begin starts a message at the end of the transcript, holding the line
// and output of the command numbered owner, or of none when it is 0.
func (m *model) begin(owner int) {
	if len(m.transcript) > 0 {
		m.starts, m.owners = append(m.starts, len(m.transcript)), append(m.owners, 0)
	}
	m.owners[len(m.owners)-1] = owner
}

// sectionOf returns the index of the message of the command numbered
// owner, or -1 when it has none on the page, as after the page was cleared.
func (m *model) sectionOf(owner int) int {
	if owner == 0 {
		return -1
	}
	return slices.Index(m.owners, owner)
}

// sectionEnd is where the message at index n ends, in bytes.
func (m *model) sectionEnd(n int) int {
	if n+1 < len(m.starts) {
		return m.starts[n+1]
	}
	return len(m.transcript)
}

func (m *model) sectionText(n int) string {
	return m.transcript[m.starts[n]:m.sectionEnd(n)]
}

// printFor adds what the command numbered from printed to its own message,
// so that commands running side by side never mix their lines. A message
// that grows while the ones after it are finished moves to the end of the
// page first, where the user is looking; while one after it still runs,
// it grows where it is.
func (m *model) printFor(from int, text string) {
	at := m.sectionOf(from)
	if at < 0 || at == len(m.starts)-1 {
		m.print(text)
		return
	}
	if !slices.ContainsFunc(m.owners[at+1:], func(o int) bool { return m.running[o] }) {
		m.moveToEnd(at)
		m.print(text)
		return
	}
	m.place(m.sectionEnd(at), text)
}

// moveToEnd moves the message at index n to the end of the transcript.
func (m *model) moveToEnd(n int) {
	text, owner := m.sectionText(n), m.owners[n]
	m.transcript = m.transcript[:m.starts[n]] + m.transcript[m.sectionEnd(n):]
	for k := n + 1; k < len(m.starts); k++ {
		m.starts[k] -= len(text)
	}
	m.starts, m.owners = slices.Delete(m.starts, n, n+1), slices.Delete(m.owners, n, n+1)
	m.starts[0] = 0 // the first message, whichever it is now, starts the page
	m.starts, m.owners = append(m.starts, len(m.transcript)), append(m.owners, owner)
	m.transcript += text
	m.sel = nil
}

// unqueue takes the queued messages back into the input, to be edited and
// sent again, or dropped.
func (m *model) unqueue() {
	if len(m.queued) == 0 {
		return
	}
	text := strings.Join(m.queued, "\n")
	if line := m.input.Value(); line != "" {
		text += "\n" + line
	}
	m.queued = nil
	m.setLine(text)
	m.hidden = true
	m.layout()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// middleHeight is how many rows the section between the header and the
// input has.
func (m *model) middleHeight() int {
	// The input takes one row, and more as its text grows.
	return max(1, m.height-chromeHeight-(m.inputHeight()-1))
}

// inputHeight is how many rows the input at the bottom of the page takes,
// with the rows above it that say what is running and what is queued.
// While a question is open, a single line stands there.
func (m *model) inputHeight() int {
	if m.dialog != nil || m.chat != nil || m.width == 0 {
		return 1
	}
	return len(m.activityRows(max(1, m.width-chromeWidth))) + inputRows(m.input.Value(), max(1, m.width-chromeWidth))
}

// activityRows are the rows above the input while a command runs: what it
// is doing, and the messages queued behind the default harness's answer,
// each cut to one row.
func (m *model) activityRows(width int) []string {
	if !m.busy && len(m.queued) == 0 {
		return nil
	}
	th := m.o.Theme
	var rows []string
	switch {
	case !m.busy:
	case th.Glow != nil:
		// The glow says it is working; only a warning needs a row.
		if m.alert != "" {
			rows = append(rows, clip(m.alert, width))
		}
	case m.alert != "":
		rows = append(rows, clip(m.spin.View()+" "+m.alert, width))
	default:
		rows = append(rows, clip(m.spin.View()+" "+th.Dim.Render("working"), width))
	}
	for n, q := range m.queued {
		if n == maxQueuedRows-1 && len(m.queued) > maxQueuedRows {
			rows = append(rows, th.Dim.Render(fmt.Sprintf("  … and %d more queued", len(m.queued)-n)))
			break
		}
		first, _, more := strings.Cut(q, "\n")
		if more {
			first += " …"
		}
		rows = append(rows, clip(th.Dim.Render("  queued: "+first), width))
	}
	return rows
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
		Render(m.headerRow(inner))

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
	default:
		typing = true
		line, hint = m.input.View(), idleHint
		if rows := m.activityRows(inner); len(rows) > 0 {
			line = strings.Join(rows, "\n") + "\n" + line
		}
		switch {
		case len(m.queued) > 0 && m.input.Value() == "":
			hint = fmt.Sprintf(queuedHint, len(m.queued))
		case m.talking != 0 && m.tab == 0 && m.input.Value() == "":
			hint = talkingHint
		case m.busy && m.tab == 0 && m.input.Value() == "":
			hint = busyHint
		}
		if m.tab > 0 && m.input.Value() == "" {
			hint = m.viewHint()
			if m.busy {
				hint += hintSep + "ctrl+c cancel"
			}
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
	page := strings.Join([]string{header, body, input}, "\n")
	under := th.Dim.Render(clip(" "+hint, m.width)) // the hint under the page
	if m.glowing() {
		side := lipgloss.NewStyle().Foreground(th.Frame).Render("│")
		return strings.Join(th.Glow.glowFrameOf(strings.Split(page, "\n"), m.width, side, under, m.glowFor), "\n")
	}

	return page + "\n" + under
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
		// The result of the action that emptied the list is still shown.
		if m.notice != "" {
			return t.Empty + "\n\n" + m.notice
		}
		return t.Empty
	}
	lines := []string{"  " + t.Header}
	at := m.selection()
	for n, r := range t.Rows {
		text := m.rowText(r)
		if n == at {
			lines = append(lines, th.Accent.Render("› ")+text)
		} else {
			lines = append(lines, "  "+text)
		}
	}
	if m.notice != "" {
		lines = append(lines, "", m.notice)
	}
	return strings.Join(lines, "\n")
}

// rowText shares the available space between a label and its detail.
func (m *model) rowText(r Row) string {
	if r.Label == "" && r.Detail == "" {
		return r.Text
	}
	room := max(0, m.width-chromeWidth-2-ansi.StringWidth(r.Text))
	label := r.Label
	if r.Detail == "" || room < 12 {
		return r.Text + ansi.Truncate(label, room, ellipsis)
	}
	const separator = " · "
	const prefix = "last: "
	available := room - ansi.StringWidth(separator+prefix)
	labelWidth := min(ansi.StringWidth(label), (available+1)/2)
	detailWidth := min(ansi.StringWidth(r.Detail), available-labelWidth)
	labelWidth = min(ansi.StringWidth(label), available-detailWidth)
	if detailWidth < 1 || labelWidth < 1 {
		return r.Text + ansi.Truncate(label, room, ellipsis)
	}
	return r.Text + ansi.Truncate(label, labelWidth, ellipsis) +
		m.o.Theme.Dim.Render(separator+prefix+ansi.Truncate(r.Detail, detailWidth, ellipsis))
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
