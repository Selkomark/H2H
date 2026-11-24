package console

import (
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	confirmHint = "←→ choose · enter select · y yes · n no · esc cancel · or click · wheel, pgup/pgdn scroll the page"
	chooseHint  = "↑↓ move · space tick · a all · enter confirm · esc cancel · or click"
	pickHint    = "↑↓ move · enter choose · esc cancel"
	infoHint    = "enter or esc close · or click"
	moreHint    = "m more or less · ←→ choose · enter select · esc close · or click"

	// Where a dialog's content starts inside its box: past the border and
	// the padding.
	dialogPadX, dialogPadY = 3, 1
	dialogContentX         = 1 + dialogPadX
	dialogContentY         = 1 + dialogPadY
	// Columns of padding on each side of a button's label, and between buttons.
	buttonPad, buttonGap = 3, 2
)

// Option is one entry the user can tick in a Choose dialog.
type Option struct {
	ID      string // what Choose returns for it
	Label   string // what the user reads
	Checked bool   // ticked when the dialog opens
}

// dialog is a question in a box: yes or no, or a list of options to tick.
// The console draws it over the page, and the shell draws it on its own.
type dialog struct {
	question string
	options  []Option // nil for a yes-or-no question
	single   bool     // the options are a list to pick one from, not to tick
	// For a box that only informs: the text under its title, drawn for a
	// width, and more of it that the user can open and close.
	body     func(width int) string
	more     func(width int) string
	expanded bool
	cursor   int // the focused option; for yes or no, 0 is Yes and 1 is No
	done     bool
	accepted bool // the answer was yes, or the ticks were confirmed
}

// newConfirm asks a yes-or-no question. No has the focus, so Enter alone
// never agrees to anything.
func newConfirm(question string) *dialog {
	return &dialog{question: question, cursor: 1}
}

// newInfo shows text to read, under a title, with a button to close it.
// With more, a second button opens and closes the rest of the text.
func newInfo(title string, body, more func(width int) string) *dialog {
	return &dialog{question: title, body: body, more: more}
}

// toggle opens or closes the rest of an info box's text.
func (d *dialog) toggle() {
	if d.more != nil {
		d.expanded = !d.expanded
	}
}

func newChoice(question string, options []Option) *dialog {
	return &dialog{question: question, options: append([]Option{}, options...)}
}

// key applies one key press. The dialog is over once done is set.
func (d *dialog) key(k tea.KeyMsg) {
	char := ""
	if k.Type == tea.KeyRunes {
		char = strings.ToLower(string(k.Runes))
	}
	if k.Type == tea.KeyEsc || k.Type == tea.KeyCtrlC {
		d.done = true
		return
	}
	if d.body != nil {
		// Nothing to answer. The buttons open the rest of the text, or
		// close the box.
		closeAt := len(d.buttons()) - 1
		switch {
		case char == "m":
			d.toggle()
		case char == "q":
			d.done = true
		case k.Type == tea.KeyLeft || k.Type == tea.KeyRight || k.Type == tea.KeyTab:
			d.cursor = (d.cursor + 1) % len(d.buttons())
		case k.Type == tea.KeyEnter || k.Type == tea.KeySpace || char == " ":
			if d.cursor == closeAt {
				d.done = true
			} else {
				d.toggle()
			}
		}
		return
	}
	if d.options == nil {
		switch {
		case char == "y":
			d.done, d.accepted = true, true
		case char == "n":
			d.done = true
		case k.Type == tea.KeyLeft || k.Type == tea.KeyRight || k.Type == tea.KeyTab:
			d.cursor = 1 - d.cursor
		case k.Type == tea.KeyEnter:
			d.done, d.accepted = true, d.cursor == 0
		}
		return
	}
	n := len(d.options)
	switch {
	case k.Type == tea.KeyUp || char == "k":
		d.cursor = (d.cursor - 1 + n) % n
	case k.Type == tea.KeyDown || char == "j":
		d.cursor = (d.cursor + 1) % n
	case k.Type == tea.KeyEnter:
		d.done, d.accepted = true, true
	case d.single: // nothing to tick
	case k.Type == tea.KeySpace || char == " ":
		d.options[d.cursor].Checked = !d.options[d.cursor].Checked
	case char == "a":
		all := len(d.chosen()) == n
		for i := range d.options {
			d.options[i].Checked = !all
		}
	}
}

// buttons are the labels of the dialog's buttons, left to right. The first
// one accepts.
func (d *dialog) buttons() []string {
	switch {
	case d.body != nil && d.more != nil && d.expanded:
		return []string{"Show less", "Close"}
	case d.body != nil && d.more != nil:
		return []string{"Show more", "Close"}
	case d.body != nil:
		return []string{"Close"}
	case d.options == nil:
		return []string{"Yes", "No"}
	case d.single:
		return nil // picking an option is the answer
	}
	return []string{"Confirm", "Cancel"}
}

// questionText is the question, wrapped to the width the box allows.
func (d *dialog) questionText(width int) string {
	inner := max(1, width-2*dialogContentX)
	return lipgloss.NewStyle().Bold(true).Width(min(inner, lipgloss.Width(d.question))).Render(d.question)
}

// bodyText is the text of an info box, drawn for and cut to the width the
// box allows, with the rest of it when that is open.
func (d *dialog) bodyText(width int) string {
	inner := max(1, width-2*dialogContentX)
	text := strings.TrimRight(d.body(inner), "\n")
	if d.expanded && d.more != nil {
		text += "\n\n" + strings.TrimRight(d.more(inner), "\n")
	}
	lines := strings.Split(text, "\n")
	for n, l := range lines {
		lines[n] = clip(l, inner)
	}
	return strings.Join(lines, "\n")
}

// click applies a mouse click at a position inside the box, counted from
// its top-left corner. Options are ticked, and buttons answer.
func (d *dialog) click(x, y, width int) {
	x, y = x-dialogContentX, y-dialogContentY
	row := y - lipgloss.Height(d.questionText(width)) - 1 // the rows under the question and a blank line
	if d.body != nil {
		row -= lipgloss.Height(d.bodyText(width)) + 1 // the text, and a blank line under it
	}
	if x < 0 || row < 0 {
		return
	}
	if d.options != nil {
		if row < len(d.options) {
			d.cursor = row
			if d.single {
				d.done, d.accepted = true, true
			} else {
				d.options[row].Checked = !d.options[row].Checked
			}
			return
		}
		row -= len(d.options) + 1 // a blank line, then the buttons
	}
	if row != 0 {
		return
	}
	at := 0
	for n, label := range d.buttons() {
		if w := lipgloss.Width(label) + 2*buttonPad; x >= at && x < at+w {
			if d.body != nil && n < len(d.buttons())-1 {
				d.cursor = n
				d.toggle() // the button that is not Close
				return
			}
			d.done, d.accepted = true, n == 0
			return
		} else {
			at += w + buttonGap
		}
	}
}

// chosen returns the IDs of the ticked options, in order. For a list to
// pick one from, it is the option with the focus.
func (d *dialog) chosen() []string {
	if d.single {
		return []string{d.options[d.cursor].ID}
	}
	var ids []string
	for _, o := range d.options {
		if o.Checked {
			ids = append(ids, o.ID)
		}
	}
	return ids
}

func (d *dialog) hint() string {
	switch {
	case d.body != nil && d.more != nil:
		return moreHint
	case d.body != nil:
		return infoHint
	case d.options == nil:
		return confirmHint
	case d.single:
		return pickHint
	}
	return chooseHint
}

// view draws the dialog as a box no wider than width.
func (d *dialog) view(th Theme, width int) string {
	inner := max(1, width-2*dialogContentX)
	lines := []string{d.questionText(width), ""}
	if d.body != nil {
		lines = append(lines, d.bodyText(width), "")
	}
	for n, o := range d.options {
		box := "[ ] "
		if o.Checked {
			box = th.Accent.Render("[x]") + " "
		}
		if d.single {
			box = ""
		}
		pointer := "  "
		if n == d.cursor {
			pointer = th.Accent.Render("› ")
		}
		lines = append(lines, clip(pointer+box+o.Label, inner))
	}
	if d.single {
		return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.Frame).Padding(dialogPadY, dialogPadX).
			Render(strings.Join(lines, "\n"))
	}
	if d.options != nil {
		lines = append(lines, "")
	}
	// The button Enter would press stands out: the focused one of Yes and
	// No, or Confirm.
	focused := 0
	if d.options == nil {
		focused = min(d.cursor, len(d.buttons())-1)
	}
	var row []string
	for n, label := range d.buttons() {
		style := th.Dim
		if n == focused {
			style = th.Accent.Reverse(true).Bold(true)
		}
		row = append(row, style.Padding(0, buttonPad).Render(label))
	}
	lines = append(lines, strings.Join(row, strings.Repeat(" ", buttonGap)))
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.Frame).Padding(dialogPadY, dialogPadX).
		Render(strings.Join(lines, "\n"))
}

// prompt shows one dialog on a terminal that the console does not hold,
// for a command run from the shell.
type prompt struct {
	d     *dialog
	th    Theme
	width int
}

func (p *prompt) Init() tea.Cmd { return nil }

func (p *prompt) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		if msg.Width > 0 { // a terminal that does not say its size keeps the default
			p.width = msg.Width
		}
	case tea.KeyMsg:
		p.d.key(msg)
		if p.d.done {
			return p, tea.Quit
		}
	}
	return p, nil
}

func (p *prompt) View() string {
	if p.d.done {
		return "" // leave nothing behind; the caller records the answer
	}
	// A dialog outside the console does not know where on the screen it
	// is, so it takes the keyboard only.
	hint := strings.TrimSuffix(p.d.hint(), " · or click")
	return p.d.view(p.th, p.width) + "\n" + p.th.Dim.Render(clip(" "+hint, p.width)) + "\n"
}

func (p *prompt) run(in, out *os.File) bool {
	if _, err := tea.NewProgram(p, tea.WithInput(in), tea.WithOutput(out)).Run(); err != nil {
		return false
	}
	return p.d.accepted
}

// Confirm asks a yes-or-no question in a dialog on the terminal, outside
// the console.
func Confirm(in, out *os.File, th Theme, question string) bool {
	return (&prompt{d: newConfirm(question), th: th, width: 80}).run(in, out)
}

// Choose asks the user to tick options in a dialog on the terminal, outside
// the console. It returns the IDs ticked, and false if the user cancelled.
func Choose(in, out *os.File, th Theme, question string, options []Option) ([]string, bool) {
	p := &prompt{d: newChoice(question, options), th: th, width: 80}
	ok := p.run(in, out)
	return p.d.chosen(), ok
}

// Pick asks the user to choose one option in a dialog on the terminal,
// outside the console. It returns the option's ID, and false if the user
// cancelled or there was nothing to choose from.
func Pick(in, out *os.File, th Theme, question string, options []Option) (string, bool) {
	if len(options) == 0 {
		return "", false
	}
	d := newChoice(question, options)
	d.single = true
	p := &prompt{d: d, th: th, width: 80}
	if !p.run(in, out) {
		return "", false
	}
	return d.chosen()[0], true
}
