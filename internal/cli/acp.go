package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/Selkomark/H2H/internal/acp"
	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/harness"
	"github.com/Selkomark/H2H/internal/mcp"
	"github.com/Selkomark/H2H/internal/policy"
	"github.com/charmbracelet/lipgloss"
)

// The ACP demo: the console's conversation runs over the Agent Client
// Protocol instead of a run of the default harness per message. The
// harness stays running for the console's lifetime, its work is drawn in
// the transcript as it arrives, its permission questions are answered from
// the rules or in the console's dialog, and H2H's commands are offered to
// it as tools through an MCP server of H2H's own. See docs/acp.md.

// acpRules are what a harness driven over ACP is told at the start of a
// session: the same as the default harness in the console, except that it
// runs H2H's commands as tools.
const acpRules = assistantAbout +
	"You can run H2H's commands yourself, with the tools of the MCP server named h2h: each tool is one of the commands below, " +
	"and takes the command's arguments as one string, as they would be typed after it. " +
	"Say in a few words what you are about to do before you use them, and do not claim a result before you have seen it.\n\n" +
	assistantCommandList + assistantPolicy

// acpLink is the console's connection to a harness over ACP. want is the
// harness the user chose with /acp; the agent, its session and the tools
// are started when they are first needed, and stay until the console
// closes, the user leaves ACP or starts a new conversation.
type acpLink struct {
	// starting is held while the agent starts. mu is never held while
	// waiting for the agent, since its messages are read by a goroutine
	// that needs mu to find the turn they belong to.
	starting sync.Mutex
	mu       sync.Mutex
	want     string // "" while ACP is off
	name     string // the harness running
	client   *acp.Client
	session  acp.Session
	tools    *mcp.Server
	stop     context.CancelFunc
	wiring   string // the config as the session was last told it
	turn     *acpTurn
}

func (l *acpLink) on() bool {
	if l == nil {
		return false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.want != ""
}

func (l *acpLink) current() *acpTurn {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.turn
}

// close stops the agent and the tools. The choice of harness stays, so
// the next message starts a new session with it.
func (l *acpLink) close() {
	if l == nil {
		return
	}
	l.mu.Lock()
	client, tools, stop := l.client, l.tools, l.stop
	l.client, l.tools, l.stop, l.wiring, l.name = nil, nil, nil, "", ""
	l.mu.Unlock()
	if client != nil {
		client.Close()
		tools.Close()
		stop()
	}
}

// acpCmd chooses the harness the console talks to over ACP, or with "off"
// goes back to the default harness. From the shell it opens the console
// talking to that harness.
func (e *env) acpCmd(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: acp [harness | off]")
	}
	if e.link == nil {
		// From the shell: the console is opened with ACP on.
		name, err := e.acpHarness(args)
		if err != nil {
			return err
		}
		e.link = &acpLink{want: name}
		return e.console(false, "")
	}
	if len(args) == 1 && args[0] == "off" {
		e.link.close()
		e.link.mu.Lock()
		e.link.want = ""
		e.link.mu.Unlock()
		fmt.Fprintln(e.out, "ACP is off. Text you type goes to the default harness again, one run per message.")
		return nil
	}
	name, err := e.acpHarness(args)
	if err != nil {
		return err
	}
	e.link.mu.Lock()
	same := e.link.want == name
	e.link.mu.Unlock()
	if !same {
		e.link.close()
		e.link.mu.Lock()
		e.link.want = name
		e.link.mu.Unlock()
	}
	if err := e.acpStart(ctx); err != nil {
		return err
	}
	fmt.Fprintln(e.out, "Text you type now goes to "+e.st.accent.Render(name)+" over ACP. "+e.st.dim.Render("/acp off goes back."))
	return nil
}

// acpHarness is the harness named, or with none, the default harness if
// it speaks ACP, or else the first that does.
func (e *env) acpHarness(args []string) (string, error) {
	cfg, err := config.Load(e.root)
	if err != nil {
		return "", err
	}
	if len(args) == 1 {
		name, h, err := cfg.Lookup(args[0])
		if err != nil {
			return "", err
		}
		if !h.SpeaksACP() {
			return "", fmt.Errorf("%s has no acp_command, so it cannot be driven over ACP", name)
		}
		return name, nil
	}
	if name, h, err := cfg.Lookup(""); err == nil && h.SpeaksACP() {
		return name, nil
	}
	for _, name := range cfg.Names() {
		if cfg.Harnesses[name].SpeaksACP() {
			return name, nil
		}
	}
	return "", errors.New("no harness in the config has acp_command")
}

// acpStart starts the chosen harness, its session and H2H's tools, if
// they are not running, and says what it started.
func (e *env) acpStart(ctx context.Context) error {
	l := e.link
	l.starting.Lock()
	defer l.starting.Unlock()
	l.mu.Lock()
	running, want := l.client != nil, l.want
	l.mu.Unlock()
	if running {
		return nil
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	name, h, err := cfg.Lookup(want)
	if err != nil {
		return err
	}
	tools, err := mcp.Start("h2h", Version, e.acpTools(l))
	if err != nil {
		return fmt.Errorf("could not serve H2H's tools: %w", err)
	}
	fmt.Fprintln(e.out, e.st.dim.Render("Starting "+name+" over ACP..."))
	// The agent outlives the message that started it.
	actx, stop := context.WithCancel(context.Background())
	client, err := acp.Start(actx, h.AcpCommand, harness.Env(h, e.environ, h.AcpEnv...), e.root, io.Discard, acp.Handlers{
		Update: func(u acp.Update) {
			if t := l.current(); t != nil {
				t.update(u)
			}
		},
		Permission: func(p acp.Permission) string {
			if t := l.current(); t != nil {
				return t.permission(p)
			}
			return "" // nobody is there to answer: refused
		},
	})
	if err != nil {
		stop()
		tools.Close()
		return err
	}
	session, err := client.NewSession(ctx, e.root, acp.MCPServer{Name: "h2h", URL: tools.URL(), Token: tools.Token()})
	if err != nil {
		client.Close()
		stop()
		tools.Close()
		return err
	}
	l.mu.Lock()
	l.name, l.client, l.session, l.tools, l.stop = name, client, session, tools, stop
	l.mu.Unlock()
	fmt.Fprintln(e.out, e.st.title.Render(client.Agent)+e.st.dim.Render(" over ACP · session "+session.ID+" · H2H's commands offered as tools"))
	e.showOptions(session.Options)
	return nil
}

// acpTools are H2H's commands as the harness may run them: those the
// console's default harness may run, through the same code, so the user is
// asked first where it would be.
func (e *env) acpTools(l *acpLink) []mcp.Tool {
	var tools []mcp.Tool
	for _, c := range commands() {
		if _, ok := assistantCommands[c.name]; !ok {
			continue
		}
		name := c.name
		tools = append(tools, mcp.Tool{
			Name:        name,
			Description: c.help + ". Arguments: " + c.usage,
			Param:       "arguments",
			ParamHelp:   "What follows the command, as typed: " + c.usage,
			Call: func(ctx context.Context, input string) (string, bool) {
				t := l.current()
				if t == nil {
					return "H2H runs a command only while it is answering the user.", true
				}
				call := append([]string{name}, strings.Fields(input)...)
				t.pause()
				return t.e.runFor(t.ctx, t.name, [][]string{call}), false
			},
		})
	}
	return tools
}

// showOptions lists the settings the session offers, such as its model.
func (e *env) showOptions(opts []acp.ConfigOption) {
	for _, o := range opts {
		fmt.Fprintf(e.out, "  %s %s %s\n", e.st.accent.Render(fmt.Sprintf("/%-6s", o.ID)), o.Current, e.st.dim.Render("of "+strings.Join(o.Values, ", ")))
	}
}

// acpOption shows or changes one setting of the ACP session, for /model
// and /mode.
func acpOption(id string) func(*env, context.Context, []string) error {
	return func(e *env, ctx context.Context, args []string) error {
		if !e.link.on() {
			return fmt.Errorf("%s changes the session of a harness driven over ACP; start one with /acp", id)
		}
		if err := e.acpStart(ctx); err != nil {
			return err
		}
		l := e.link
		l.mu.Lock()
		client, session := l.client, l.session
		l.mu.Unlock()
		if len(args) == 0 {
			e.showOptions(only(session.Options, id))
			return nil
		}
		opts, err := client.SetOption(ctx, session.ID, id, strings.Join(args, " "))
		if err != nil {
			return fmt.Errorf("%s was not changed: %w", id, err)
		}
		l.mu.Lock()
		l.session.Options = opts
		l.mu.Unlock()
		e.showOptions(only(opts, id))
		return nil
	}
}

func only(opts []acp.ConfigOption, id string) []acp.ConfigOption {
	for _, o := range opts {
		if o.ID == id {
			return []acp.ConfigOption{o}
		}
	}
	return nil
}

// acpConverse sends text typed into the console to the harness over ACP,
// and draws its turn in the transcript. Ctrl-C stops the turn.
func (e *env) acpConverse(ctx context.Context, text string) error {
	if err := e.acpStart(ctx); err != nil {
		return err
	}
	l := e.link
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	l.mu.Lock()
	busy := l.turn != nil
	known := talk{ID: l.session.ID, Wiring: l.wiring}
	if l.wiring == "" {
		known = talk{} // the session has not been told anything yet
	}
	l.mu.Unlock()
	if busy {
		return fmt.Errorf("%s is still answering; wait for it, or stop it with Ctrl-C", l.name)
	}
	prompt, wiring, err := e.turnPrompt(cfg, acpRules, known, text, nil, "", 1)
	if err != nil {
		return err
	}
	l.mu.Lock()
	l.wiring = wiring
	l.mu.Unlock()
	return e.acpTurn(ctx, prompt)
}

// acpTurn sends one prompt and draws the turn until it ends.
func (e *env) acpTurn(ctx context.Context, prompt string) error {
	l := e.link
	l.mu.Lock()
	name, client, session := l.name, l.client, l.session.ID
	t := &acpTurn{e: e, ctx: ctx, name: name, rules: policyRules(e.root), titles: map[string]string{}, stream: &streamWriter{out: e.out}}
	l.turn = t
	l.mu.Unlock()
	defer func() {
		l.mu.Lock()
		l.turn = nil
		l.mu.Unlock()
	}()

	fmt.Fprintln(e.out, e.st.dim.Render("Asking "+name+" over ACP..."))
	type result struct {
		reason string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		// Not ctx: Ctrl-C cancels the turn, and the answer to that is
		// still to be read.
		r, err := client.Prompt(context.Background(), session, prompt)
		done <- result{r, err}
	}()
	var r result
	select {
	case r = <-done:
	case <-ctx.Done():
		t.mu.Lock()
		t.stopAsked = true
		t.mu.Unlock()
		client.Cancel(session)
		r = <-done
	}
	t.end(r.reason)
	if r.err != nil {
		// A harness that stopped is started again for the next message.
		l.close()
		return fmt.Errorf("%s over ACP: %w", name, r.err)
	}
	// An agent may end its turn when a tool is refused, and ACP has no way
	// to tell it why. H2H then tells it in a prompt of its own, as a
	// permission hook does in its answer, unless the user stopped the turn.
	t.mu.Lock()
	followUp := r.reason == "cancelled" && !t.stopAsked && len(t.refused) > 0
	refused := t.refused
	t.mu.Unlock()
	if !followUp || ctx.Err() != nil {
		return nil
	}
	fmt.Fprintln(e.out, e.st.dim.Render("H2H tells "+name+" what was refused, and to carry on."))
	return e.acpTurn(ctx, "H2H refused: "+strings.Join(refused, "; ")+". Carry on with the rest of the request.")
}

func policyRules(root string) policy.Rules {
	cfg, err := config.Load(root)
	if err != nil {
		return policy.Rules{} // nothing is allowed by rule
	}
	return cfg.Permissions.Rules()
}

// acpTurn is one turn being drawn: what the agent sends arrives on the
// goroutine that reads its messages, and is written to the line's
// transcript.
type acpTurn struct {
	e      *env
	ctx    context.Context
	name   string
	rules  policy.Rules
	stream *streamWriter // the text of messages and thoughts, in batches

	mu        sync.Mutex
	midLine   bool // the text written last did not end its line
	thinking  bool // the text written last was a thought
	titles    map[string]string
	used      int
	size      int
	cost      float64
	currency  string
	stopAsked bool
	// refused lists what was refused, each with the message the model
	// would have been given, since an ACP answer carries none.
	refused []string
}

func (t *acpTurn) update(u acp.Update) {
	t.mu.Lock()
	defer t.mu.Unlock()
	st := t.e.st
	switch u.Kind {
	case "agent_message_chunk":
		if t.thinking {
			t.breakLine()
			t.thinking = false
		}
		t.text(u.Text, lipgloss.NewStyle())
	case "agent_thought_chunk":
		if !t.thinking {
			t.breakLine()
			t.thinking = true
		}
		t.text(u.Text, st.dim)
	case "tool_call":
		t.titles[u.ToolCallID] = u.Title
		t.line(st.warn.Render("  ▸ ") + u.Title + st.dim.Render("  "+u.ToolKind))
	case "tool_call_update":
		title := t.titles[u.ToolCallID]
		if u.Title != "" && u.Title != title {
			title = u.Title
			t.titles[u.ToolCallID] = title
			t.line(st.warn.Render("  ▸ ") + title)
		}
		switch u.Status {
		case "completed":
			t.line(st.good.Render("  ✓ ") + st.dim.Render(title))
		case "failed":
			t.line(st.bad.Render("  ✗ ") + st.dim.Render(title))
		}
	case "plan":
		t.line(st.title.Render("  Plan"))
		for _, p := range u.Plan {
			mark := "○"
			switch p.Status {
			case "in_progress":
				mark = "◐"
			case "completed":
				mark = "●"
			}
			t.line("  " + mark + " " + p.Content)
		}
	case "usage_update":
		t.used, t.size, t.cost, t.currency = u.Used, u.Size, u.Cost, u.Currency
	}
}

// permission answers the agent's question about a tool: from the
// project's rules where one covers it, otherwise in the console's dialog.
func (t *acpTurn) permission(p acp.Permission) string {
	subject := p.Command
	switch {
	case subject != "":
	case p.ToolKind == "edit" && p.Path != "":
		subject = "Edit " + p.Path
	default:
		subject = p.Title
	}
	subject = portable(subject, t.e.root, t.e.root)
	st := t.e.st
	switch t.rules.Decide(subject) {
	case policy.Allow:
		t.say(st.good.Render("  allowed by rule: ") + subject)
		return pick(p.Options, "allow_once")
	case policy.Deny:
		t.say(st.bad.Render("  refused by rule: ") + subject)
		t.refuse(subject, deniedByRule)
		return pick(p.Options, "reject_once")
	}
	t.pause()
	if t.e.ui.Confirm(t.name + " asks to:\n  " + subject + "\nAllow it?") {
		t.say(st.good.Render("  allowed by you: ") + subject)
		return pick(p.Options, "allow_once")
	}
	t.say(st.bad.Render("  refused by you: ") + subject)
	t.refuse(subject, deniedByUser)
	return pick(p.Options, "reject_once")
}

// pick returns the ID of the first option of the kind, or "" if the agent
// offered none, which cancels the request: never an allow it did not offer.
func pick(options []acp.Option, kind string) string {
	for _, o := range options {
		if o.Kind == kind {
			return o.ID
		}
	}
	return ""
}

func (t *acpTurn) refuse(subject, why string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.refused = append(t.refused, "`"+subject+"`. "+why)
}

// pause ends the text so far, before something else is written to the
// transcript, such as a dialog's question or a command H2H runs.
func (t *acpTurn) pause() {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.breakLine()
}

func (t *acpTurn) say(s string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.line(s)
}

func (t *acpTurn) end(reason string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.breakLine()
	note := "turn ended: " + reason
	if t.size > 0 {
		note += fmt.Sprintf(" · context %s of %s tokens", kilo(t.used), kilo(t.size))
	}
	if t.currency != "" {
		note += fmt.Sprintf(" · cost so far %.4f %s", t.cost, t.currency)
	}
	fmt.Fprintln(t.e.out, t.e.st.dim.Render(note))
}

// text, line and breakLine write to the transcript; the caller holds t.mu.

// text writes a piece of a message. Each line of it is styled by itself,
// since a style given text of several lines pads them to one width.
func (t *acpTurn) text(s string, style lipgloss.Style) {
	if s == "" {
		return
	}
	parts := strings.Split(s, "\n")
	for n, p := range parts {
		if p != "" {
			parts[n] = style.Render(p)
		}
	}
	t.stream.Write([]byte(strings.Join(parts, "\n")))
	t.midLine = !strings.HasSuffix(s, "\n")
}

func (t *acpTurn) line(s string) {
	t.breakLine()
	fmt.Fprintln(t.e.out, s)
	t.thinking = false
}

func (t *acpTurn) breakLine() {
	t.stream.flush()
	if t.midLine {
		fmt.Fprintln(t.e.out)
		t.midLine = false
	}
}

func kilo(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
