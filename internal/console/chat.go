package console

import (
	"slices"
	"strings"

	"github.com/charmbracelet/bubbles/textarea"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

const (
	chatHint     = "enter send · shift+enter new line · wheel, pgup/pgdn scroll · esc cancel"
	chatWaitHint = "wheel, pgup/pgdn scroll · esc cancel"
	chatMaxWidth = 100 // columns of text, so long lines stay readable
	chatMinRows  = 3   // rows of conversation, on the smallest screen
)

// Chat is a conversation with the user in a box over the page: the other
// side speaks, the user answers, until it is closed. A command drives it.
type Chat interface {
	// Say adds what the other side said.
	Say(text string)
	// Show adds something drawn for the width of the box, such as a table
	// or a diagram, which must not be wrapped like prose.
	Show(draw func(width int) string)
	// Wait shows that the other side is working, with a note saying on what.
	Wait(note string)
	// Ask waits for the user's next line. It returns false if the user
	// cancelled the conversation.
	Ask() (string, bool)
	// Close takes the box off the page.
	Close()
}

// Messages that drive the chat box.
type (
	chatOpenMsg  struct{ title string }
	chatSayMsg   struct{ text string }
	chatShowMsg  struct{ draw func(width int) string }
	chatWaitMsg  struct{ note string }
	chatAskMsg   struct{ reply chan<- chatAnswer }
	chatCloseMsg struct{}
	chatAnswer   struct {
		text string
		ok   bool
	}
)

// programChat is the Chat a command sees in the console. Each call becomes
// a message to the running program.
type programChat struct{ box *mailbox }

func (c programChat) Say(text string)  { c.box.send(chatSayMsg{text: sanitize(text)}) }
func (c programChat) Wait(note string) { c.box.send(chatWaitMsg{note: note}) }

func (c programChat) Show(draw func(width int) string) { c.box.send(chatShowMsg{draw: draw}) }
func (c programChat) Close()                           { c.box.send(chatCloseMsg{}) }

func (c programChat) Ask() (string, bool) {
	reply := make(chan chatAnswer, 1)
	c.box.send(chatAskMsg{reply: reply})
	a := <-reply
	return a.text, a.ok
}

// chatBox is the conversation on screen.
type chatBox struct {
	title  string
	pieces []chatPiece // the conversation so far
	view   viewport.Model
	input  textarea.Model
	note   string            // what the other side is working on, while it is
	asking chan<- chatAnswer // where the user's next line goes, while one is awaited
	width  int               // columns of text inside the box
	height int               // rows the page gives the box
}

func newChatBox(title string, th Theme) *chatBox {
	return &chatBox{title: title, input: newInput(th, "Type your answer"), view: viewport.New(0, 0)}
}

// resize fits the box into an area of the page.
func (c *chatBox) resize(width, height int) {
	c.width, c.height = max(10, min(chatMaxWidth, width-2*dialogContentX-2)), height
	c.fit()
}

// fit sizes the input to its text and gives the conversation the rows
// that are left. Around the conversation are the border and padding, the
// title and a blank line above, and a blank line and the input below.
func (c *chatBox) fit() {
	fitInput(&c.input, c.width)
	c.view.Width, c.view.Height = c.width, max(chatMinRows, c.height-2*dialogContentY-3-c.input.Height())
	c.wrap()
}

// breakLine starts a new line in the answer being typed.
func (c *chatBox) breakLine() {
	if c.asking != nil {
		c.input.InsertString("\n")
		c.fit()
	}
}

// chatPiece is one part of a conversation: text that wraps to the box, or
// something that draws itself for the box's width.
type chatPiece struct {
	text string
	draw func(width int) string
}

func (c *chatBox) wrap() {
	prose := lipgloss.NewStyle().Width(c.width)
	parts := make([]string, len(c.pieces))
	for n, p := range c.pieces {
		if p.draw != nil {
			// Drawn for this width, and cut to it should it be wider.
			lines := strings.Split(strings.TrimRight(sanitize(p.draw(c.width)), "\n"), "\n")
			for i, l := range lines {
				lines[i] = clip(l, c.width)
			}
			parts[n] = strings.Join(lines, "\n")
			continue
		}
		parts[n] = prose.Render(p.text)
	}
	c.view.SetContent(strings.Join(parts, "\n\n"))
	c.view.GotoBottom()
}

// add puts one message at the end of the conversation.
func (c *chatBox) add(text string) {
	c.pieces = append(c.pieces, chatPiece{text: strings.TrimSpace(text)})
	c.wrap()
}

// show puts something that draws itself at the end of the conversation.
func (c *chatBox) show(draw func(width int) string) {
	c.pieces = append(c.pieces, chatPiece{draw: draw})
	c.wrap()
	// What is drawn before anyone has spoken is there to be read from
	// its top: a picture taller than the box starts at its first row.
	if !slices.ContainsFunc(c.pieces, func(p chatPiece) bool { return p.draw == nil }) {
		c.view.GotoTop()
	}
}

// answer sends the user's line, or their wish to stop, to whoever asked.
func (c *chatBox) answer(a chatAnswer) {
	if c.asking != nil {
		c.asking <- a
		c.asking = nil
	}
}

// key applies a key press. It reports whether the user cancelled.
func (c *chatBox) key(k tea.KeyMsg, th Theme) (cancelled bool) {
	switch k.Type {
	case tea.KeyEsc, tea.KeyCtrlC:
		c.answer(chatAnswer{})
		return true
	case tea.KeyPgUp:
		c.view.HalfPageUp()
	case tea.KeyPgDown:
		c.view.HalfPageDown()
	case tea.KeyShiftUp:
		c.view.SetYOffset(c.view.YOffset - 1)
	case tea.KeyShiftDown:
		c.view.SetYOffset(c.view.YOffset + 1)
	case tea.KeyEnter, tea.KeyCtrlJ:
		// Enter sends. With Alt, as Ctrl+J, or after a backslash, it
		// breaks the line instead.
		if breaksLine(k) {
			c.breakLine()
			break
		}
		if text, ok := endsWithBackslash(c.input.Value()); ok && c.asking != nil {
			c.input.SetValue(text + "\n")
			c.fit()
			break
		}
		if line := strings.TrimSpace(c.input.Value()); line != "" && c.asking != nil {
			c.add(th.Accent.Render(promptText + strings.ReplaceAll(line, "\n", "\n"+continuation)))
			c.input.SetValue("")
			c.fit()
			c.answer(chatAnswer{text: line, ok: true})
		}
	default:
		if c.asking != nil {
			before := c.input.Value()
			c.input, _ = c.input.Update(k)
			if c.input.Value() != before {
				c.fit()
			}
		}
	}
	return false
}

func (c *chatBox) hint() string {
	if c.asking != nil {
		return chatHint
	}
	return chatWaitHint
}

// render draws the box. spinner is shown while the other side works.
func (c *chatBox) render(th Theme, spinner string) string {
	last := c.input.View()
	if c.asking == nil {
		last = spinner + " " + th.Dim.Render(c.note)
	}
	if c.asking == nil {
		last = clip(last, c.width)
	}
	body := strings.Join([]string{th.Accent.Bold(true).Render(c.title), "", c.view.View(), "", last}, "\n")
	return lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(th.Frame).Padding(dialogPadY, dialogPadX).
		Width(c.width + 2*dialogPadX).Render(body)
}
