package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/harness"
	"github.com/Selkomark/H2H/internal/keeper"
	"github.com/Selkomark/H2H/internal/session"
)

const (
	// runMarker starts a line in which the default harness asks H2H to
	// run a command.
	runMarker = "H2H-RUN:"
	// maxRounds is how many times in one answer the default harness may
	// run commands and look at the results.
	maxRounds = 6
	// maxResult is how much of a command's output the harness is shown.
	maxResult = 6000
	// streamPause is the longest that text which has arrived waits to be
	// shown. Showing every piece by itself would redraw the page for each.
	streamPause = 60 * time.Millisecond
)

// The commands the default harness may run for the user. The value says
// whether the command changes something, in which case the user is asked
// first. The config has its own flow and is not here, and neither is what
// needs the terminal or belongs to the console itself.
//
// land changes the project and is not asked about here, because it always
// asks the user itself, with the plan of what it would do in front of
// them. A second question before that one would say less.
var assistantCommands = map[string]bool{
	"ps": false, "map": false, "logs": false, "sessions": false, "doctor": false, "ask": false,
	"land":  false,
	"start": true, "stop": true, "rm": true, "install": true,
}

// converse answers text typed into the console with the default harness,
// which can also run H2H's commands: it asks for them in its reply, H2H
// runs them and shows it the results, and it carries on from there.
//
// Every run continues the console session's conversation with the
// harness, so it remembers what was said before. The first message of a
// conversation carries everything the harness needs to know; a later one
// carries only the message and what has changed since.
func (e *env) converse(ctx context.Context, text string) error {
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	name, h, err := cfg.Lookup("")
	if err != nil {
		return err
	}
	t := e.currentTalk()
	if t.ID != "" && t.Harness != name {
		fmt.Fprintln(e.out, e.st.dim.Render("The default harness is now "+name+", so this starts a new conversation with it."))
		t = talk{Gen: t.Gen}
		e.keepTalk(t)
	}
	var turns []string // what the harness and H2H said after the message, for a harness that does not remember
	result := ""       // what H2H's commands printed in the last round
	for round := 1; ; round++ {
		note := "Asking " + name + "..."
		if round > 1 {
			note = name + " is looking at the results..."
		}
		fmt.Fprintln(e.out, e.st.dim.Render(note))

		reply, next, err := e.turn(ctx, cfg, name, h, t, text, turns, result, round)
		// A conversation the harness no longer has, because its history was
		// removed, cannot be continued. It is started again, once, from the
		// beginning, but only if the turn said nothing: one that failed half
		// way through is not repeated.
		if err != nil && ctx.Err() == nil && t.ID != "" && strings.TrimSpace(reply) == "" {
			fmt.Fprintln(e.out, e.st.dim.Render("The earlier conversation with "+name+" could not be continued, so this starts a new one."))
			reply, next, err = e.turn(ctx, cfg, name, h, talk{}, text, turns, result, round)
		}
		switch {
		case ctx.Err() != nil:
			// A conversation that had begun goes on; one whose first turn
			// was cut short may not exist, so the next message starts anew.
			return fmt.Errorf("%s was interrupted", name)
		case err != nil:
			return err
		}
		next.Gen = t.Gen
		t = next
		e.keepTalk(t)

		calls := runLines(reply)
		if len(calls) == 0 {
			return nil
		}
		if round == maxRounds {
			fmt.Fprintln(e.out, e.st.dim.Render(fmt.Sprintf("Stopped after %d rounds of commands.", maxRounds)))
			return nil
		}
		result = e.runFor(ctx, name, calls)
		turns = append(turns, "You: "+strings.TrimSpace(reply), "H2H: "+result)
	}
}

// turn runs one turn of the conversation t, which is new when its ID is
// "". It returns the harness's reply and the conversation as it goes on.
func (e *env) turn(ctx context.Context, cfg config.Config, name string, h config.Harness, t talk, text string, turns []string, result string, round int) (string, talk, error) {
	prompt, wiring, err := e.turnPrompt(cfg, t, text, turns, result, round)
	if err != nil {
		return "", t, err
	}
	// What the harness prints beside its answer (progress, warnings) is
	// kept off the page, and shown only if the run fails.
	var reply, beside bytes.Buffer
	shown := &streamWriter{out: e.out, hide: runMarker}
	actx, cancel := context.WithTimeout(ctx, askTimeout)
	id, err := harness.Turn(actx, h, e.environ, e.root, t.ID, prompt, io.MultiWriter(&reply, shown), &beside)
	cancel()
	shown.Close()
	if err != nil {
		return reply.String(), t, couldNotAnswer(name, h, err, beside.String())
	}
	return reply.String(), talk{Harness: name, ID: id, Wiring: wiring}, nil
}

// turnPrompt is what one turn tells the harness, and the digest of the
// config it shows. A new conversation, or a harness that does not
// remember, is told everything. A conversation that goes on is told the
// message and how things stand now, or, after commands it asked for,
// only what they printed.
func (e *env) turnPrompt(cfg config.Config, t talk, text string, turns []string, result string, round int) (prompt, wiring string, err error) {
	state, wiringText, err := e.assistantState(cfg)
	if err != nil {
		return "", "", err
	}
	// The map also shows which harnesses run, which the harness is told
	// anyway; only a change to the config itself makes it worth repeating.
	wiring = digest(configMap(e.root, cfg, nil, plainStyles()) + cfg.RolesXML(""))
	switch {
	case t.ID == "":
		return assistantRules + "How the config is wired up:\n" + wiringText + "\n" + "Harnesses right now:\n" + state + "\n" +
			"Message from the user: " + text + "\n" + rounds(turns), wiring, nil
	case round > 1:
		return "What H2H's commands printed:\n\n" + result + "\n\nContinue: run more, or give the user your answer.\n", t.Wiring, nil
	}
	update := ""
	if wiring != t.Wiring {
		update = "The config has changed since you last saw it. How it is wired up now:\n" + wiringText + "\n"
	}
	return update + "Harnesses right now:\n" + state + "\n" + "Message from the user: " + text + "\n", wiring, nil
}

// assistantState is how things stand, as the default harness is shown
// them: the harnesses that were started, and how the config is wired up.
// They are the same views the user has, without styling.
func (e *env) assistantState(cfg config.Config) (state, wiring string, err error) {
	infos, err := session.List(e.root)
	if err != nil {
		return "", "", err
	}
	state = "No harness has been started.\n"
	if len(infos) > 0 {
		state = sessionTable(e.root, infos, plainStyles())
	}
	wiring = configMap(e.root, cfg, infos, plainStyles())
	if roles := cfg.RolesXML(""); roles != "" {
		wiring += "\nThe domains in detail:\n" + roles
	}
	return state, wiring, nil
}

// assistantRules tell the default harness where it is and how to act. A
// conversation is told them once, at its start.
const assistantRules = "You are the assistant in the console of H2H, a tool that runs coding-agent harnesses as a team " +
	"for the project in the current directory: each harness owns a domain and works in its own session and workspace. " +
	"The user types messages into the console, and each comes to you in this conversation, with how things stand at that moment. " +
	"Answer each one briefly, in plain text for a terminal: no Markdown. You remember the earlier messages, " +
	"so when the user says to go ahead with something, it is what was discussed before. " +
	"The conversation lasts until the user starts a new one with /clear.\n\n" +
	"You can run H2H's commands yourself. To run one, put it on a line of its own in your reply, after " + runMarker + ", for example:\n" +
	runMarker + " start backend\n" +
	"H2H runs the commands of your reply in order once the reply ends, then shows you what they printed, and you continue: " +
	"run more, or give the user your answer. Say in a few words what you are about to do before the commands, " +
	"and do not claim a result before you have seen it. When you have nothing more to run, reply without any such line.\n\n" +
	"The commands you can run:\n" +
	"  ps                          list the harnesses that were started, with their state\n" +
	"  start <harness | domain>    start a harness in its own workspace; a domain starts its owner, told its role\n" +
	"  stop <id>                   stop a running harness\n" +
	"  rm <id>                     remove a stopped harness and its workspace\n" +
	"  land <id>                   bring a harness's work from its workspace onto the branch the project is on:\n" +
	"                              H2H shows the user what would land and asks them before it changes the project;\n" +
	"                              it offers to commit work the harness left uncommitted, and merges without pushing\n" +
	"  logs <id>                   the terminal output of a harness so far\n" +
	"  ask <id | harness | domain> <question>\n" +
	"                              hand a request to a running harness: it is typed into that harness's own session,\n" +
	"                              where the user can watch, and you get its report when it is done. Name the id, or the\n" +
	"                              domain or harness it runs for. This is how work and questions are delegated: start a\n" +
	"                              domain's owner first, then ask it. Only if none is running for that name is the\n" +
	"                              harness run once, read-only, without a session\n" +
	"  map                         how the config is wired up\n" +
	"  sessions                    the saved console sessions\n" +
	"  doctor                      check that everything H2H needs is in place\n" +
	"  install [--update] [harness ...]\n" +
	"                              install missing harnesses, or update installed ones\n" +
	"The user is asked before start, stop, rm and install are run, and may decline. " +
	"A harness works in its own workspace, a separate copy of the project, so its work is not in the project until it is landed. " +
	"When the user accepts a harness's work, or wants it in the project, the code base or their branch, run land for that harness. " +
	"Do not tell the user to run git for this, and do not ask the harness to merge, push or switch branches: land does it, and it asks the user itself. " +
	"You cannot change the config: for that, tell the user to run /config. " +
	"You cannot attach to a harness: the user does that with /attach <id>. " +
	"A harness whose state is \"waiting for you\" has stopped at a prompt in its own terminal, such as a permission prompt, and only the user can answer it: " +
	"say so first, and tell them to answer it with /attach <id>. Do not ask it anything until then, since what is typed would land in that prompt. " +
	"\"may be waiting for you\" means it has been silent with a request open, which usually means the same. " +
	"When the user wants something done or found out in a domain, do not do it or answer it yourself: delegate it to that domain's harness with ask, and pass on what it reports. " +
	"Do not change any file yourself.\n\n"

// rounds is what has happened since the user's message: each reply of the
// harness and what its commands printed.
func rounds(turns []string) string {
	if len(turns) == 0 {
		return ""
	}
	return "\nWhat has happened since:\n\n" + strings.Join(turns, "\n\n") + "\n\nContinue.\n"
}

// runLines returns the commands a reply asks for, each split into words.
func runLines(reply string) [][]string {
	var calls [][]string
	for _, line := range strings.Split(reply, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), runMarker)
		if !ok {
			continue
		}
		if fields := strings.Fields(strings.TrimPrefix(strings.TrimSpace(rest), "/")); len(fields) > 0 {
			calls = append(calls, fields)
		}
	}
	return calls
}

// permitted says whether the default harness may run a command, and
// whether the user is asked first. When it may not, why says so.
func permitted(call []string) (ok, asks bool, why string) {
	changes, known := assistantCommands[call[0]]
	switch {
	case call[0] == "config":
		return false, false, "you cannot change the config; tell the user to run /config"
	case !known:
		return false, false, "this is not a command you can run"
	case call[0] == "logs" && slices.Contains(call, "-f"):
		return false, false, "you cannot follow a log; run logs without -f"
	case call[0] == "sessions" && len(call) > 1:
		return true, true, "" // removing saved sessions
	}
	return true, changes, ""
}

// runFor runs the commands the default harness asked for, showing the
// user each one and what it prints, and returns the results as the
// harness is shown them.
func (e *env) runFor(ctx context.Context, name string, calls [][]string) string {
	var asking []string
	for _, call := range calls {
		if ok, asks, _ := permitted(call); ok && asks {
			asking = append(asking, "  /"+strings.Join(call, " "))
		}
	}
	agreed := true
	if len(asking) > 0 {
		question := "Run them?"
		if len(asking) == 1 {
			question = "Run it?"
		}
		agreed = e.ui.Confirm(name + " wants to run:\n" + strings.Join(asking, "\n") + "\n" + question)
	}

	var results []string
	for _, call := range calls {
		line := "/" + strings.Join(call, " ")
		ok, asks, why := permitted(call)
		switch {
		case !ok:
			results = append(results, "Result of "+line+": not run: "+why+".")
			continue
		case asks && !agreed:
			results = append(results, "Result of "+line+": not run: the user declined.")
			continue
		case ctx.Err() != nil:
			results = append(results, "Result of "+line+": not run: the user interrupted.")
			continue
		}
		fmt.Fprintln(e.out, e.st.accent.Render(promptMark+line))
		var printed bytes.Buffer
		sub := *e
		sub.out, sub.errw = io.MultiWriter(e.out, &printed), io.MultiWriter(e.errw, &printed)
		if err := sub.dispatch(ctx, call[0], call[1:]); err != nil {
			sub.fail(err)
		}
		results = append(results, "Result of "+line+":\n"+forHarness(printed.String()))
	}
	return strings.Join(results, "\n\n")
}

// promptMark precedes a command that H2H runs for the default harness, as
// the console's own prompt precedes one the user typed.
const promptMark = "> "

var (
	controlRE = regexp.MustCompile(`\x1b\[[0-9;?]*[ -/]*[@-~]|\x1b\][^\x07\x1b]*(\x07|\x1b\\)?|\x1b.`)
	blankRE   = regexp.MustCompile(`\n{3,}`)
)

// forHarness makes a command's output fit to show a harness: without the
// terminal's control sequences, and only the end of it when it is long.
func forHarness(text string) string {
	text = controlRE.ReplaceAllString(text, "")
	text = strings.Map(func(r rune) rune {
		if r == '\r' || (r < 0x20 && r != '\n' && r != '\t') {
			return -1
		}
		return r
	}, text)
	text = strings.TrimSpace(blankRE.ReplaceAllString(text, "\n\n"))
	if text == "" {
		return "(it printed nothing)"
	}
	if r := []rune(text); len(r) > maxResult {
		text = "(the start is left out)\n" + string(r[len(r)-maxResult:])
	}
	return text
}

// reportCommand is the internal command a harness's h2h-report runs.
const reportCommand = "report"

// requestHead begins every request typed into a session, with its number.
// The session's hooks find it in the prompt the harness submits, and so
// learn which request it took.
const requestHead = "Request %d, from the user of H2H through its console"

// startSettle is how long after its start a harness is left alone before
// a request is typed into it.
const startSettle = 10 * time.Second

// reportWait is how long ask waits for a running harness to report.
const reportWait = 30 * time.Minute

// report stores a harness's answer to a request. It is run by the harness
// inside its session: the session and the project come from the
// environment its keeper gave it, and the answer from the arguments after
// the request number, or from the input when there are none.
func report(args []string, in io.Reader, getenv func(string) string) error {
	root, id := getenv("H2H_ROOT"), getenv("H2H_SESSION")
	if root == "" || !session.ValidID(id) {
		return errors.New("this command is for a harness started by H2H; it reports to the request it was given")
	}
	if len(args) == 0 {
		return errors.New("usage: h2h-report <request number>, with the answer as its input")
	}
	n, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("%q is not a request number", args[0])
	}
	text := strings.Join(args[1:], " ")
	if text == "" {
		data, err := io.ReadAll(in)
		if err != nil {
			return err
		}
		text = string(data)
	}
	if strings.TrimSpace(text) == "" {
		return errors.New("the answer is empty; pass it as the command's input")
	}
	dir, err := session.ReportDir(root, id)
	if err != nil {
		return err
	}
	return session.Report(dir, n, text)
}

// askHarness hands a request to a harness and prints its answer.
//
// A running harness is asked in its own session: the request is typed
// into its terminal, where the user can attach and watch, and its answer
// comes back through h2h-report. Naming a domain or a harness asks the
// one running for it. When none is running, the harness is run once,
// read-only, and answers from the project alone.
func (e *env) askHarness(ctx context.Context, args []string) error {
	if len(args) < 2 {
		return errors.New("usage: ask <id | harness | domain> <question>")
	}
	question := strings.Join(args[1:], " ")
	infos, err := session.List(e.root)
	if err != nil {
		return err
	}
	var running []session.Info
	for _, i := range infos {
		if i.State != session.Running {
			continue
		}
		if i.ID == args[0] {
			return e.askRunning(ctx, i, question)
		}
		// A domain's name matches its owner's sessions; a harness's name
		// matches the sessions started for it and no domain.
		if i.Domain == args[0] || (i.Domain == "" && i.Harness == args[0]) {
			running = append(running, i)
		}
	}
	switch len(running) {
	case 0:
		return e.askFresh(ctx, args[0], question)
	case 1:
		return e.askRunning(ctx, running[0], question)
	}
	ids := make([]string, len(running))
	for n, i := range running {
		ids[n] = i.ID
	}
	return fmt.Errorf("several harnesses are running for %s; name one: %s", args[0], strings.Join(ids, ", "))
}

// safeToType says whether a request may be typed into a session. Typing
// ends with Enter. In a harness that has not been told to trust the
// project, that Enter would answer its trust question, which is the user's
// to answer, and may answer it with yes. So a request is typed only where
// H2H knows there is no such question: anything it cannot check is a no.
// It returns the harness's config entry.
func (e *env) safeToType(info session.Info) (config.Harness, error) {
	refuse := func(why string) (config.Harness, error) {
		return config.Harness{}, fmt.Errorf("%s, so H2H will not type into %s: it could answer a trust question for you. Give it the request yourself: attach %s", why, info.ID, info.ID)
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		return refuse("the config cannot be read (" + err.Error() + ")")
	}
	h, ok := cfg.Harnesses[info.Harness]
	switch {
	case !ok:
		return refuse(info.Harness + " is not in the config")
	case h.NoTrustQuestion:
		return h, nil
	case !h.ChecksTrust():
		return refuse("the config does not say how to check that " + info.Harness + " trusts this project")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return refuse("the home directory is unknown")
	}
	trusted, err := harness.Trusted(h, e.root, home)
	if err != nil {
		return refuse("what " + info.Harness + " trusts cannot be read (" + err.Error() + ")")
	}
	if !trusted {
		return refuse(info.Harness + " has not been told to trust this project")
	}
	return h, nil
}

// askRunning types a request into a running harness's terminal and waits
// for its report.
func (e *env) askRunning(ctx context.Context, info session.Info, question string) error {
	h, err := e.safeToType(info)
	if err != nil {
		return err
	}
	// A harness that has only just started is still drawing its screen
	// and would lose what is typed. There is no signal yet for when it is
	// ready, so it is given a moment.
	if wait := time.Until(info.Started.Add(startSettle)); wait > 0 {
		select {
		case <-ctx.Done():
			return errors.New("interrupted before the request was sent")
		case <-time.After(wait):
		}
	}
	// A harness stopped at its own prompt would take what is typed as the
	// answer to it.
	if now, err := session.Load(e.root, info.ID); err == nil && now.Attention.Kind == session.Waiting {
		return fmt.Errorf("%s is waiting for your answer in its own terminal, and a request typed now would land in that prompt; answer it first: attach %s", info.ID, info.ID)
	}
	dir, err := session.ReportDir(e.root, info.ID)
	if err != nil {
		return err
	}
	// A harness whose hooks run "go" when a prompt is submitted says when
	// it takes the request; see state.
	confirmed := h.TellsWaiting()
	n, err := session.NewRequest(dir, confirmed)
	if err != nil {
		return err
	}
	request := fmt.Sprintf(requestHead+":\n\n%s\n\n"+
		"When you have the answer, send it back by running this shell command, with the answer in plain text as its input:\n\n"+
		"%s %d <<'H2H_ANSWER'\nyour answer\nH2H_ANSWER\n\n"+
		"Run that command by itself, with nothing added before or after it. "+
		"If the shell does not find that command, use its full path instead: %s\n\n"+
		"The user is not looking at this terminal and sees only what you send that way.",
		n, question, keeper.ReportCommand, n, keeper.ReportPath(e.root, info.ID))
	if err := keeper.Type(info.Socket, request); err != nil {
		if errors.Is(err, keeper.ErrAttached) {
			return fmt.Errorf("%s is attached, here or in another terminal, so the request cannot be typed into it; leave it with Ctrl-\\ and ask again", info.ID)
		}
		return err
	}
	fmt.Fprintln(e.out, e.st.dim.Render("Asked "+info.ID+" in its own session (request "+strconv.Itoa(n)+"). Waiting for its report; watch it work with: ")+e.st.accent.Render("attach "+info.ID))

	deadline := time.Now().Add(reportWait)
	var told time.Time // the wait last told about, outside the full-screen console
	took := false
	for {
		if data, err := os.ReadFile(session.ReportPath(dir, n)); err == nil {
			fmt.Fprintln(e.out, strings.TrimSpace(string(data)))
			return nil
		}
		if confirmed && !took && session.Taken(dir, n) {
			took = true
			fmt.Fprintln(e.out, e.st.dim.Render(info.ID+" took the request and is working on it."))
		}
		now, err := session.Load(e.root, info.ID)
		// The full-screen console tells the user itself; anywhere else
		// the wait is told here.
		if err == nil && e.heeded == nil && now.Attention.Kind != session.NoAttention && !now.Attention.Since.Equal(told) {
			told = now.Attention.Since
			fmt.Fprintln(e.out, e.waitNotice(now))
		}
		switch {
		case err != nil || !now.Live():
			return fmt.Errorf("%s ended before it reported; see what it did with: logs %s", info.ID, info.ID)
		case time.Now().After(deadline):
			return fmt.Errorf("%s has not reported within %s; it may still be working, or waiting for you to approve something: attach %s", info.ID, reportWait, info.ID)
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("stopped waiting for %s; the request is still with it, and it may yet act on it: attach %s", info.ID, info.ID)
		case <-time.After(300 * time.Millisecond):
		}
	}
}

// askFresh runs a harness once, read-only, and prints its answer as it
// arrives. Naming a domain asks its owner, which is told its role there.
func (e *env) askFresh(ctx context.Context, target, question string) error {
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	asked, domain := target, ""
	if d, ok := cfg.Domains[target]; ok {
		domain, target = target, d.Harness
	}
	name, h, err := cfg.Lookup(target)
	if err != nil {
		return fmt.Errorf("%q is not a running harness, a harness or a domain (harnesses: %s; domains: %s)", asked,
			strings.Join(cfg.Names(), ", "), strings.Join(cfg.DomainNames(), ", "))
	}
	if _, err := exec.LookPath(h.Command); err != nil {
		return fmt.Errorf("harness %s: the program %q is not installed or not on PATH", name, h.Command)
	}
	prompt := "You are asked a question inside H2H, a tool that runs coding-agent harnesses as a team for the project in the current directory. " +
		"Answer briefly, in plain text for a terminal: no Markdown. You may read the project to answer. Do not change any file.\n\n"
	who := name
	if domain != "" {
		// The role goes in the prompt, which every harness takes, instead
		// of through the option each one has for standing instructions.
		prompt += "You are asked as the owner of the " + domain + " domain. Answer for that domain.\n\n" + cfg.RolesXML(domain) + "\n"
		who = name + ", owner of " + domain
	}
	fmt.Fprintln(e.out, e.st.dim.Render("No harness is running for "+asked+", so "+who+" answers in a fresh read-only run. Start it first to ask it in its own session."))
	var beside bytes.Buffer
	shown := &streamWriter{out: e.out}
	actx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	err = harness.AskStream(actx, h, e.environ, e.root, prompt+"Question: "+question+"\n", shown, &beside)
	shown.Close()
	switch {
	case ctx.Err() != nil:
		return fmt.Errorf("%s was interrupted", name)
	case err != nil:
		return couldNotAnswer(name, h, err, beside.String())
	}
	return nil
}

// couldNotAnswer is the error for a harness whose one-shot run failed. It
// leads with what the harness itself said, which is the real reason: a
// usage limit reached, a login that expired. The exit status alone says
// nothing a user can act on.
func couldNotAnswer(name string, h config.Harness, err error, said string) error {
	if why := lastLines(said, 5); why != "" {
		return fmt.Errorf("%s could not answer. It said:\n%s", name, why)
	}
	return fmt.Errorf("%s could not answer (%w) and gave no reason. Run it yourself to see why: %s", name, err, strings.Join(append([]string{h.Command}, h.AskArgs...), " ")+" hello")
}

// lastLines returns the last n lines of text that are not blank, which is
// where a program that failed says why.
func lastLines(text string, n int) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	var lines []string
	for _, l := range strings.Split(forHarness(text), "\n") {
		if strings.TrimSpace(l) != "" {
			lines = append(lines, l)
		}
	}
	return strings.Join(lines[max(0, len(lines)-n):], "\n")
}

// streamWriter shows an answer while it arrives. It leaves out the lines
// that start with hide, which are for H2H and not for the user, and it
// passes text on in small batches instead of one piece at a time.
type streamWriter struct {
	out  io.Writer
	hide string // "" hides nothing

	line    []byte // the line being received
	sent    int    // how much of it has been passed on
	pending bytes.Buffer
	flushed time.Time
}

func (w *streamWriter) Write(p []byte) (int, error) {
	n := len(p)
	for len(p) > 0 {
		end := bytes.IndexByte(p, '\n') + 1
		whole := end > 0
		if !whole {
			end = len(p)
		}
		w.line = append(w.line, p[:end]...)
		p = p[end:]
		// A line is passed on as soon as it cannot be one to hide.
		if w.showable(whole) {
			w.pending.Write(w.line[w.sent:])
			w.sent = len(w.line)
		}
		if whole {
			w.line, w.sent = w.line[:0], 0
		}
	}
	if time.Since(w.flushed) >= streamPause {
		w.flush()
	}
	return n, nil
}

// showable reports whether the line received so far is known not to be
// one to hide. An unfinished line that could still become one is held.
func (w *streamWriter) showable(whole bool) bool {
	if w.hide == "" {
		return true
	}
	start := strings.TrimLeft(string(w.line), " \t")
	if strings.HasPrefix(start, w.hide) {
		return false
	}
	return whole || !strings.HasPrefix(w.hide, strings.TrimRight(start, "\n"))
}

func (w *streamWriter) flush() {
	if w.pending.Len() > 0 {
		_, _ = w.out.Write(w.pending.Bytes())
		w.pending.Reset()
	}
	w.flushed = time.Now()
}

// Close passes on what is left, and ends an unfinished last line.
func (w *streamWriter) Close() {
	if len(w.line) > 0 && w.showable(true) {
		w.pending.Write(w.line[w.sent:])
		w.pending.WriteByte('\n')
	}
	w.line, w.sent = nil, 0
	w.flush()
}
