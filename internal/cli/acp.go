package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"sync"

	"github.com/Selkomark/H2H/internal/acp"
	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/harness"
	"github.com/Selkomark/H2H/internal/policy"
	"github.com/muesli/cancelreader"
)

// acpCmd drives a harness over the Agent Client Protocol, in place of its
// terminal: H2H draws what the harness does and answers what it asks
// permission for, from the project's rules or by asking the user. It is a
// demo of driving harnesses that way; see docs/acp.md.
func (e *env) acpCmd(ctx context.Context, args []string) error {
	if len(args) > 1 {
		return errors.New("usage: acp [harness]")
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	var name string
	if len(args) == 1 {
		name = args[0]
	} else {
		// With no harness named, the default one if it speaks ACP, or
		// else the first that does.
		if n, h, err := cfg.Lookup(""); err == nil && h.SpeaksACP() {
			name = n
		}
		for _, n := range cfg.Names() {
			if name == "" && cfg.Harnesses[n].SpeaksACP() {
				name = n
			}
		}
		if name == "" {
			return errors.New("no harness in the config has acp_command")
		}
	}
	name, h, err := cfg.Lookup(name)
	if err != nil {
		return err
	}
	if !h.SpeaksACP() {
		return fmt.Errorf("%s has no acp_command, so it cannot be driven over ACP", name)
	}
	return e.ui.Foreground(func() error {
		out := e.tty
		if out == nil {
			out = os.Stdout
		}
		return e.drive(ctx, name, h, cfg.Permissions.Rules(), e.in, out)
	})
}

// acpView draws one ACP session and holds what a turn needs: the lines the
// user types and the permission answers they give while it runs.
type acpView struct {
	e     *env
	name  string
	rules policy.Rules
	out   io.Writer
	lines <-chan string

	mu        sync.Mutex // guards what follows, written from the agent's messages
	midLine   bool       // the last text written did not end a line
	thinking  bool       // the last text written was a thought
	titles    map[string]string
	used      int
	size      int
	cost      float64
	currency  string
	turn      context.Context
	stopAsked bool
	// refused lists what was refused in this turn, each with the
	// message the model would be given, since an ACP answer carries none.
	refused []string
}

func (e *env) drive(ctx context.Context, name string, h config.Harness, rules policy.Rules, in io.Reader, out io.Writer) error {
	// The reader is cancelled on the way out, so that it does not keep
	// taking the console's input after the demo returns to it.
	if r, err := cancelreader.NewReader(in); err == nil {
		defer r.Cancel()
		in = r
	}
	lines := make(chan string)
	go func() {
		sc := bufio.NewScanner(in)
		for sc.Scan() {
			lines <- sc.Text()
		}
		close(lines)
	}()
	v := &acpView{e: e, name: name, rules: rules, out: out, lines: lines, titles: map[string]string{}}

	ctx, stop := context.WithCancel(ctx)
	defer stop()
	env := harness.Env(h, e.environ, h.AcpEnv...)
	fmt.Fprintf(out, "Starting %s over ACP...\n", name)
	client, err := acp.Start(ctx, h.AcpCommand, env, e.root, io.Discard, acp.Handlers{Update: v.update, Permission: v.permission})
	if err != nil {
		return err
	}
	defer client.Close()
	sess, err := client.NewSession(ctx, e.root)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, e.st.title.Render(client.Agent)+e.st.dim.Render(" over ACP, session "+sess.ID+", in "+e.root))
	v.showOptions(sess.Options)
	fmt.Fprintln(out, e.st.dim.Render("Type a request. /model, /mode and /options change the session's settings; /quit leaves. Ctrl-C stops a turn."))

	interrupts := make(chan os.Signal, 1)
	signal.Notify(interrupts, os.Interrupt)
	defer signal.Stop(interrupts)

	for {
		fmt.Fprint(out, "\n"+e.st.accent.Render(name+" › "))
		var line string
		select {
		case l, ok := <-lines:
			if !ok {
				fmt.Fprintln(out)
				return nil
			}
			line = strings.TrimSpace(l)
		case <-interrupts:
			fmt.Fprintln(out)
			return nil
		}
		switch {
		case line == "":
			continue
		case line == "/quit" || line == "/exit":
			return nil
		case line == "/options":
			// Shown as they were last reported; setting one shows them anew.
			v.showOptions(sess.Options)
			continue
		case strings.HasPrefix(line, "/model") || strings.HasPrefix(line, "/mode"):
			id, value, _ := strings.Cut(strings.TrimPrefix(line, "/"), " ")
			value = strings.TrimSpace(value)
			if value == "" {
				v.showOptions(only(sess.Options, id))
				continue
			}
			opts, err := client.SetOption(ctx, sess.ID, id, value)
			if err != nil {
				fmt.Fprintln(out, e.st.bad.Render("Not changed: "+err.Error()))
				continue
			}
			sess.Options = opts
			v.showOptions(only(opts, id))
			continue
		case strings.HasPrefix(line, "/"):
			fmt.Fprintln(out, e.st.dim.Render("H2H knows /model, /mode, /options and /quit here. Anything else is sent to "+name+" as it is."))
		}
		if err := v.run(ctx, client, sess.ID, line, interrupts); err != nil {
			return err
		}
	}
}

// run sends one request and draws the turn until it ends. Ctrl-C asks the
// agent to stop it.
func (v *acpView) run(ctx context.Context, client *acp.Client, session, text string, interrupts <-chan os.Signal) error {
	turn, cancel := context.WithCancel(ctx)
	defer cancel()
	v.mu.Lock()
	v.turn, v.stopAsked, v.midLine, v.refused = turn, false, false, nil
	v.mu.Unlock()

	type result struct {
		reason string
		err    error
	}
	done := make(chan result, 1)
	go func() {
		r, err := client.Prompt(ctx, session, text)
		done <- result{r, err}
	}()
	for {
		select {
		case r := <-done:
			v.endTurn(r.reason)
			if r.err != nil {
				return r.err
			}
			// An agent may end its turn when a tool is refused, and ACP has
			// no way to tell it why. H2H then tells it in a request of its
			// own, as a permission hook does in its answer, unless the user
			// stopped the turn.
			v.mu.Lock()
			followUp := r.reason == "cancelled" && !v.stopAsked && len(v.refused) > 0
			refused := v.refused
			v.mu.Unlock()
			if !followUp {
				return nil
			}
			text = "H2H refused: " + strings.Join(refused, "; ") + ". Carry on with the rest of the request."
			fmt.Fprintln(v.out, v.e.st.dim.Render("H2H tells "+v.name+" what was refused, and to carry on:"))
			return v.run(ctx, client, session, text, interrupts)
		case <-interrupts:
			v.mu.Lock()
			v.stopAsked = true
			v.mu.Unlock()
			cancel() // a permission question that is open is withdrawn
			if err := client.Cancel(session); err != nil {
				return err
			}
		}
	}
}

func (v *acpView) endTurn(reason string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.newLine()
	st := v.e.st
	note := "turn ended: " + reason
	if v.size > 0 {
		note += fmt.Sprintf(" · context %s of %s tokens", kilo(v.used), kilo(v.size))
	}
	if v.currency != "" {
		note += fmt.Sprintf(" · cost so far %.4f %s", v.cost, v.currency)
	}
	fmt.Fprintln(v.out, st.dim.Render(note))
}

// update draws what the agent reports.
func (v *acpView) update(u acp.Update) {
	v.mu.Lock()
	defer v.mu.Unlock()
	st := v.e.st
	switch u.Kind {
	case "agent_message_chunk":
		if v.thinking {
			v.newLine()
			v.thinking = false
		}
		v.write(u.Text, u.Text)
	case "agent_thought_chunk":
		if !v.thinking {
			v.newLine()
			v.thinking = true
		}
		v.write(st.dim.Render(u.Text), u.Text)
	case "tool_call":
		v.titles[u.ToolCallID] = u.Title
		v.line(st.warn.Render("  ▸ ") + u.Title + st.dim.Render("  "+u.ToolKind+", "+u.Status))
	case "tool_call_update":
		title := v.titles[u.ToolCallID]
		if u.Title != "" && u.Title != title {
			title = u.Title
			v.titles[u.ToolCallID] = title
			v.line(st.warn.Render("  ▸ ") + title)
		}
		switch u.Status {
		case "completed":
			v.line(st.good.Render("  ✓ ") + st.dim.Render(title))
		case "failed":
			v.line(st.bad.Render("  ✗ ") + st.dim.Render(title))
		}
	case "plan":
		v.line(st.title.Render("  Plan"))
		for _, p := range u.Plan {
			mark := "○"
			switch p.Status {
			case "in_progress":
				mark = "◐"
			case "completed":
				mark = "●"
			}
			v.line("  " + mark + " " + p.Content)
		}
	case "usage_update":
		v.used, v.size, v.cost, v.currency = u.Used, u.Size, u.Cost, u.Currency
	}
}

// permission answers the agent's question about a tool: from the
// project's rules where one covers it, otherwise by asking the user. The
// answer comes back to the agent as one of the options it offered.
func (v *acpView) permission(p acp.Permission) string {
	subject := p.Command
	if subject == "" {
		tool := strings.ToUpper(p.ToolKind[:min(1, len(p.ToolKind))]) + p.ToolKind[min(1, len(p.ToolKind)):]
		subject = strings.TrimSpace(tool + " " + p.Path)
	}
	subject = portable(subject, v.e.root, v.e.root)

	v.mu.Lock()
	turn := v.turn
	st := v.e.st
	v.newLine()
	v.mu.Unlock()

	switch v.rules.Decide(subject) {
	case policy.Allow:
		fmt.Fprintln(v.out, st.good.Render("  allowed by rule: ")+subject)
		return pick(p.Options, "allow_once")
	case policy.Deny:
		fmt.Fprintln(v.out, st.bad.Render("  refused by rule: ")+subject)
		v.refuse(subject, deniedByRule)
		return pick(p.Options, "reject_once")
	}
	fmt.Fprintln(v.out, st.warn.Render("  "+v.name+" asks to: ")+subject)
	fmt.Fprint(v.out, st.dim.Render("  y allow once · a allow for this session · n refuse › "))
	for {
		select {
		case l, ok := <-v.lines:
			if !ok {
				return ""
			}
			switch strings.ToLower(strings.TrimSpace(l)) {
			case "y", "yes":
				return pick(p.Options, "allow_once")
			case "a":
				return pick(p.Options, "allow_always")
			case "n", "no":
				v.refuse(subject, deniedByUser)
				return pick(p.Options, "reject_once")
			}
			fmt.Fprint(v.out, st.dim.Render("  y, a or n › "))
		case <-turn.Done():
			fmt.Fprintln(v.out)
			return ""
		}
	}
}

func (v *acpView) refuse(subject, why string) {
	v.mu.Lock()
	defer v.mu.Unlock()
	v.refused = append(v.refused, "`"+subject+"`. "+why)
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

func (v *acpView) showOptions(opts []acp.ConfigOption) {
	for _, o := range opts {
		fmt.Fprintf(v.out, "%s %s %s\n", v.e.st.accent.Render(fmt.Sprintf("/%-6s", o.ID)), o.Current, v.e.st.dim.Render("of "+strings.Join(o.Values, ", ")))
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

// write, line and newLine draw text; the caller holds v.mu.
func (v *acpView) write(shown, text string) {
	if text == "" {
		return
	}
	fmt.Fprint(v.out, shown)
	v.midLine = !strings.HasSuffix(text, "\n")
}

func (v *acpView) line(s string) {
	v.newLine()
	fmt.Fprintln(v.out, s)
	v.thinking = false
}

func (v *acpView) newLine() {
	if v.midLine {
		fmt.Fprintln(v.out)
		v.midLine = false
	}
}

func kilo(n int) string {
	if n < 1000 {
		return fmt.Sprint(n)
	}
	return fmt.Sprintf("%.1fk", float64(n)/1000)
}
