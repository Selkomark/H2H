package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/harness"
)

// askTimeout bounds every non-interactive harness run: a harness can finish
// its work and still not exit.
const askTimeout = 5 * time.Minute

// manualFlag makes config open the file in the user's editor.
const manualFlag = "--manual"

// config sets the config up in a conversation with the default harness.
// "show" prints it, and --manual opens it in the user's editor instead.
func (e *env) config(ctx context.Context, args []string) error {
	switch {
	case len(args) == 1 && args[0] == "show":
		return e.configShow()
	case len(args) == 0, len(args) == 1 && args[0] == manualFlag:
	default:
		return errors.New("usage: config [show | " + manualFlag + "]")
	}
	folder, err := e.configFolder()
	if err != nil || folder == "" {
		return err // nil when the user cancelled the choice
	}
	in := *e
	in.root = folder
	if len(args) == 0 {
		err = in.configGuide(ctx)
	} else {
		err = in.configEdit(ctx)
	}
	if _, statErr := os.Stat(config.Path(folder)); err == nil && folder != e.root && statErr == nil {
		fmt.Fprintln(e.out, e.st.dim.Render("This folder is now a project of its own. Run h2h from it, or a folder inside it, to work on it."))
	}
	return err
}

// configFolder is the folder whose config is set up: the project's, or,
// when h2h runs in a folder below the project, that folder if the user
// picks it. A folder with its own config is a project of its own, such as
// a team's part of a monorepo; see workspace.Root.
func (e *env) configFolder() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return e.root, nil
	}
	if real, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = real
	}
	rel, err := filepath.Rel(e.root, cwd)
	if err != nil || rel == "." || strings.HasPrefix(rel, "..") {
		return e.root, nil
	}
	options := []console.Option{
		{ID: e.root, Label: "The whole project: " + tildePath(e.root, os.Getenv("HOME"))},
		{ID: cwd, Label: "Only this folder, as a project of its own: " + rel},
	}
	folder, ok := e.ui.Pick("Set up H2H for which folder?", options)
	if !ok {
		fmt.Fprintln(e.out, "Setup cancelled. The config is unchanged.")
		return "", nil
	}
	return folder, nil
}

// configText returns the config as it stands: the file, or the shipped
// defaults when the project has none yet.
func (e *env) configText() (text string, exists bool, err error) {
	data, err := os.ReadFile(config.Path(e.root))
	if errors.Is(err, fs.ErrNotExist) {
		return config.Template(), false, nil
	}
	return string(data), err == nil, err
}

func (e *env) configShow() error {
	text, exists, err := e.configText()
	if err != nil {
		return err
	}
	if exists {
		fmt.Fprintln(e.out, e.st.dim.Render(config.Path(e.root))+"\n")
	} else {
		fmt.Fprintln(e.out, e.st.dim.Render("No "+config.FileName+" yet. These are the defaults H2H ships with.")+"\n")
	}
	fmt.Fprint(e.out, text)
	fmt.Fprintln(e.out, "\n"+e.st.dim.Render("Set it up in a conversation with: ")+e.st.accent.Render("config")+e.st.dim.Render(", or edit it yourself with: ")+e.st.accent.Render("config "+manualFlag))
	return nil
}

// configEdit opens the config in the user's editor, on a copy, and applies
// the result only if it is valid and the user confirms. A project without
// a config starts from the one H2H ships with.
func (e *env) configEdit(ctx context.Context) error {
	if !term.IsTerminal(int(e.in.Fd())) {
		return errors.New("config " + manualFlag + " needs a terminal")
	}
	current, exists, err := e.configText()
	if err != nil {
		return err
	}
	draft := current
	for {
		draft, err = e.editText(draft)
		if err != nil {
			return err
		}
		if draft == current && exists {
			fmt.Fprintln(e.out, "No changes.")
			return nil
		}
		if _, err := config.Parse(draft); err != nil {
			fmt.Fprintf(e.out, "%s %v\n", e.st.bad.Render("That config is not valid:"), err)
			if !e.ui.Confirm("Edit it again?") {
				fmt.Fprintln(e.out, "Discarded. The config is unchanged.")
				return nil
			}
			continue
		}
		applied, err := e.configApply(ctx, current, exists, draft)
		if err != nil || applied || !e.ui.Confirm("Edit it again?") {
			return err
		}
		// Turned down, and not done: back to the editor with the draft.
	}
}

// configMarker is the line the harness puts before the finished config,
// which is how H2H tells the file from a question.
const configMarker = "H2H-CONFIG-FOLLOWS"

// doneCommand is what the user types to end the conversation and see the
// result. doneMessage is what the harness is told when they do.
const (
	doneCommand = "/done"
	doneMessage = "DONE. The user is finished and wants to see the result. Send the marker line and the file now."
)

// maxInvalidConfigs is how often the harness may send a config that does
// not validate before the conversation is given up.
const maxInvalidConfigs = 2

// configGuide sets the config up in a conversation: the default harness
// asks the user how they want their team arranged, and once the user says
// they are done it writes the file. The result goes through the same
// validation and confirmation as a manual edit, then doctor checks it and
// the map shows what was set up.
func (e *env) configGuide(ctx context.Context) error {
	current, exists, err := e.configText()
	if err != nil {
		return err
	}
	// The harness comes from the shipped defaults if the file is broken,
	// so a bad config can still be repaired in a conversation.
	cfg, loadErr := config.Load(e.root)
	if loadErr != nil {
		if cfg, err = config.Parse(config.Template()); err != nil {
			return err
		}
	}
	name, h, err := e.configHarness(cfg)
	if err != nil || name == "" {
		return err // nil when the user cancelled the choice
	}
	base := guidePrompt(current, exists, loadErr, e.installedHarnesses(cfg), supportedHarnesses(cfg))
	talk, hangUp, err := e.configTalker(ctx, name, h, base)
	if err != nil {
		return err
	}
	defer hangUp()

	// The box is closed while the user looks at a proposal, and opened
	// again if they turn it down and want to keep editing.
	var chat console.Chat
	open := false
	openChat := func() {
		if !open {
			chat = e.ui.Chat("Set up H2H with " + name)
			open = true
		}
	}
	closeChat := func() {
		if open {
			chat.Close()
			open = false
		}
	}
	openChat()
	defer closeChat()

	// First what is there, as a picture, so nobody has to read a
	// description of it: the map and a table of the domains. Then how to
	// reply, which stays the same through the conversation.
	if loadErr == nil {
		chat.Show(func(width int) string {
			return e.st.title.Render("The setup as it is now") + "\n\n" + setupPicture(cfg, e.st)(width)
		})
	}
	chat.Show(func(int) string {
		return e.st.dim.Render("Say in plain words what to set up or change, or ask for a suggestion.") + "\n" +
			e.st.dim.Render("When you are happy, type ") + e.st.accent.Render(doneCommand) +
			e.st.dim.Render(": you are shown the changes and a map of the result, and nothing is written before you apply it.")
	})

	var turns []string
	invalid := 0
	for {
		chat.Wait(name + " is thinking")
		reply, err := talk(ctx, turns)
		switch {
		case ctx.Err() != nil:
			closeChat()
			fmt.Fprintln(e.out, "Setup cancelled. The config is unchanged.")
			return nil
		case err != nil:
			return err
		}

		said, file, done := strings.Cut(reply, configMarker)
		if !done {
			turns = append(turns, "You: "+strings.TrimSpace(said))
			chat.Say(said)
			answer, ok := chat.Ask()
			if !ok {
				closeChat()
				fmt.Fprintln(e.out, "Setup cancelled. The config is unchanged.")
				return nil
			}
			// The one reply H2H reads itself, so that "finished" is never
			// a matter of how the harness takes a "yes" or an "ok".
			if strings.EqualFold(strings.TrimSpace(answer), doneCommand) {
				answer = doneMessage
			}
			turns = append(turns, "User: "+answer)
			continue
		}

		proposal := unfence(file)
		if _, err := config.Parse(proposal); err != nil {
			if invalid++; invalid > maxInvalidConfigs {
				return fmt.Errorf("%s kept writing a config that is not valid (%v); nothing was changed. Edit it yourself with: config %s", name, err, manualFlag)
			}
			chat.Say(fmt.Sprintf("That config was not valid (%v). Asking %s to correct it.", err, name))
			turns = append(turns, "You: "+strings.TrimSpace(reply),
				fmt.Sprintf("H2H: that file is not valid: %v. Correct it, and send the marker line and the complete file again.", err))
			continue
		}

		closeChat()
		if proposal != current || !exists {
			applied, err := e.configApply(ctx, current, exists, proposal)
			if err != nil {
				return err
			}
			if !applied {
				// Turned down is not the end: back to the conversation,
				// to say what should be different.
				openChat()
				chat.Say("Not applied; the config is unchanged. What should be different? Leave with Esc to stop here.")
				answer, ok := chat.Ask()
				if !ok {
					closeChat()
					fmt.Fprintln(e.out, "Setup stopped. The config is unchanged.")
					return nil
				}
				turns = append(turns, "You: "+strings.TrimSpace(reply),
					"H2H: the user looked at that config and the map of it, and did not apply it.", "User: "+answer)
				continue
			}
		} else {
			fmt.Fprintln(e.out, "The config already says that. Nothing was changed.")
		}
		if err := e.doctor(ctx, nil); err != nil {
			return fmt.Errorf("the config was saved, but %w", err)
		}
		fmt.Fprintln(e.out)
		if err := e.mapCmd(ctx, nil); err != nil {
			return err
		}
		fmt.Fprintln(e.out, "\n"+e.st.good.Render("H2H is now configured."))
		return nil
	}
}

// configHarness asks which harness should hold the setup conversation,
// from those that are installed and can hold one, the default first. It
// asks only when there is a choice. It returns "" when the user cancelled.
func (e *env) configHarness(cfg config.Config) (string, config.Harness, error) {
	def, defH, err := cfg.Lookup("")
	if err != nil {
		return "", config.Harness{}, err
	}
	can := func(h config.Harness) bool {
		_, err := exec.LookPath(h.Command)
		return err == nil && (h.SpeaksACP() || len(h.AskArgs) > 0)
	}
	var options []console.Option
	for _, name := range append([]string{def}, cfg.Names()...) {
		h := cfg.Harnesses[name]
		if !can(h) || slices.ContainsFunc(options, func(o console.Option) bool { return o.ID == name }) {
			continue
		}
		label := name
		if name == def {
			label += " (the default)"
		}
		if h.SpeaksACP() {
			label += " · over ACP, in one session"
		} else {
			label += " · one run per message"
		}
		options = append(options, console.Option{ID: name, Label: label})
	}
	switch {
	case len(options) == 0:
		if _, err := exec.LookPath(defH.Command); err != nil {
			return "", config.Harness{}, fmt.Errorf("the default harness %s is not installed, so it cannot hold this conversation; install it with: install %s, or edit the config yourself with: config %s", def, def, manualFlag)
		}
		return "", config.Harness{}, fmt.Errorf("the default harness %s has no ask_args, so it cannot hold this conversation; edit the config yourself with: config %s", def, manualFlag)
	case len(options) == 1:
		return options[0].ID, cfg.Harnesses[options[0].ID], nil
	}
	name, ok := e.ui.Pick("Which harness should set up the config with you?", options)
	if !ok {
		fmt.Fprintln(e.out, "Setup cancelled. The config is unchanged.")
		return "", config.Harness{}, nil
	}
	return name, cfg.Harnesses[name], nil
}

// configTalker returns how to get the harness's next message in the setup
// conversation, given the conversation so far, and how to end it. A
// harness that speaks ACP holds it in one session and is sent only what is
// new; any other is run once per message and shown all of it.
func (e *env) configTalker(ctx context.Context, name string, h config.Harness, base string) (func(context.Context, []string) (string, error), func(), error) {
	if !h.SpeaksACP() {
		return func(ctx context.Context, turns []string) (string, error) {
			actx, cancel := context.WithTimeout(ctx, askTimeout)
			defer cancel()
			var reply, errOut bytes.Buffer
			if err := harness.Ask(actx, h, e.environ, e.root, base+conversation(turns), &reply, &errOut); err != nil {
				if ctx.Err() != nil {
					return "", ctx.Err()
				}
				// A harness run this way prints its reason as its answer.
				return "", couldNotAnswer(name, h, err, errOut.String()+"\n"+reply.String())
			}
			return reply.String(), nil
		}, func() {}, nil
	}
	t, err := e.startACPTalk(ctx, h)
	if err != nil {
		return nil, nil, fmt.Errorf("%s over ACP: %w", name, err)
	}
	sent := 0
	return func(ctx context.Context, turns []string) (string, error) {
		text := base + acpTalkNote + conversation(turns)
		if sent > 0 {
			// Its own messages it has already; the rest is news to it.
			var news []string
			for _, turn := range turns[sent:] {
				if !strings.HasPrefix(turn, "You: ") {
					news = append(news, turn)
				}
			}
			text = strings.Join(news, "\n\n") + "\n\nSend your next message.\n"
		}
		sent = len(turns)
		reply, err := t.say(ctx, text)
		if err != nil && ctx.Err() == nil {
			err = fmt.Errorf("%s over ACP: %w", name, err)
		}
		return reply, err
	}, t.close, nil
}

// acpTalkNote corrects, for a harness in one ACP session, what the guide
// says about seeing the whole conversation each time.
const acpTalkNote = "This conversation is one session: after this message you are sent only what is new, " +
	"so remember what was said. Read the project only with your tools for reading files; do not run commands, which H2H refuses here.\n"

// installedHarnesses says which of the configured harnesses are on PATH,
// so the conversation does not hand a domain to one that cannot start.
func (e *env) installedHarnesses(cfg config.Config) string {
	var lines []string
	for _, name := range cfg.Names() {
		state := "installed"
		if _, err := exec.LookPath(cfg.Harnesses[name].Command); err != nil {
			state = "not installed"
		}
		lines = append(lines, "- "+name+": "+state)
	}
	return strings.Join(lines, "\n")
}

// supportedHarnesses tells the harness which harnesses this H2H supports
// that the config does not have, each with the entry to add for it. A
// config written for an earlier H2H cannot say this itself.
func supportedHarnesses(cfg config.Config) string {
	extra := cfg.Unconfigured()
	if len(extra) == 0 {
		return ""
	}
	shipped := config.Shipped()
	text := "This version of H2H also supports the harnesses below, which the config does not have. " +
		"Tell the user about them when they ask what they can use, or when one would suit a domain. " +
		"To add one, copy its entry into the config exactly as it is here, with the other harnesses. " +
		"One that is not installed is installed by the user with \"/install <name>\" in H2H; say so when you add it.\n"
	for _, name := range extra {
		state := "installed"
		if _, err := exec.LookPath(shipped.Harnesses[name].Command); err != nil {
			state = "not installed"
		}
		text += "\n" + name + " (" + state + "):\n" + config.ShippedEntry(name) + "\n"
	}
	return text + "\n"
}

// guidePrompt is what the harness is told before the conversation starts:
// what H2H is for, the config as it stands, its schema, and how to hold
// the conversation.
func guidePrompt(current string, exists bool, broken error, installed, supported string) string {
	state := "The project has no config file yet. This is the one H2H ships with, which is in force until one is written:"
	switch {
	case broken != nil:
		state = "The project's config file is not valid (" + broken.Error() + "). This is the file as it stands:"
	case exists:
		state = "This is the project's config file as it stands:"
	}
	return "You are setting up H2H for the project in the current directory, in a conversation with its user.\n\n" +
		"What H2H is for: it lets complete coding-agent harnesses (such as Claude Code, Copilot CLI and Codex CLI) work as a team. " +
		"Each harness owns a domain of responsibility, for example the web frontend, the backend, or end-to-end testing, " +
		"works in its own session and workspace, and can be told which sub-agent roles to use as specialists. " +
		"Domains can depend on each other, so one waits for the work of another, and a domain without dependencies runs in parallel. " +
		"One harness is the default: it starts when none is named and answers the user in the H2H console. " +
		"All of this is declared in one TOML file, " + config.FileName + ", which is what you are writing.\n\n" +
		state + "\n\n" + current + "\n\n" +
		"The comments in that file are the schema: they explain every setting H2H accepts. Settings not described there are rejected. " +
		"Keep the comments, and keep the settings of the harnesses that H2H ships with unless the user asks to change them.\n\n" +
		"Which of the configured harnesses are installed on this machine:\n" + installed + "\n\n" + supported +
		"How to hold the conversation:\n" +
		"- You see the whole conversation so far each time, and reply with your next message only.\n" +
		"- The user has just been shown the setup as it stands, as a map and a table, and how to reply. " +
		"Do not explain what H2H is and do not list the setup again. " +
		"Your first message is one or two sentences and a single question: what they want to set up or change. " +
		"Mention something only if it looks wrong, such as a path in the config that does not exist in the project.\n" +
		"- Ask one question at a time and keep each message short. Find out which harness should be the default, " +
		"which domains of responsibility the project should have, which harness owns each, which paths each covers, " +
		"which sub-agent roles each should use, and which domains depend on which.\n" +
		"- You may read the project's files to suggest domains that fit it. Do not change any file and do not run anything that changes the project.\n" +
		"- Do not give a domain to a harness that is not installed without telling the user.\n" +
		"- Write plain text for a terminal: no Markdown, no headings, no code fences.\n" +
		"- Do not summarise the setup in words, and do not ask the user in words to confirm it. " +
		"When the user says they are done, or their message starts with DONE, reply with a line containing exactly " + configMarker +
		" and after it the complete new contents of " + config.FileName + ", and nothing else. " +
		"H2H then shows the user the changes and a map of the result, and asks them to apply it or go back to you: that is the confirmation.\n" +
		"- If a reply does not tell you what to do, such as a bare \"yes\" or \"ok\" that answers no question of yours, do not guess what it agrees to. " +
		"Ask them to either say what should change or type " + doneCommand + " to see the result.\n" +
		"- After a change is asked for and you have no question left, say in one sentence what you changed and that they can ask for more or type " + doneCommand + ".\n"
}

// conversation is the exchange so far, as the harness is shown it.
func conversation(turns []string) string {
	if len(turns) == 0 {
		return "\nThe conversation has not started. Send your first message.\n"
	}
	return "\nThe conversation so far:\n\n" + strings.Join(turns, "\n\n") + "\n\nSend your next message.\n"
}

// configApply is the plan-and-apply step: show the difference, ask, write.
// It reports whether the config was written.
func (e *env) configApply(ctx context.Context, current string, exists bool, next string) (bool, error) {
	// Two things and no more: the lines that change, and then, last and
	// so right above the question, the map of what the config sets up.
	// The map is what the user checks their intent against.
	if exists {
		fmt.Fprintf(e.out, "%s\n\n%s\n", e.st.title.Render("Changes to "+config.FileName), e.st.diff(lineDiff(current, next)))
	} else if next == current {
		fmt.Fprintf(e.out, "This creates %s with the shipped defaults.\n\n", config.FileName)
	} else {
		fmt.Fprintf(e.out, "%s\n\n%s\n", e.st.title.Render("This creates "+config.FileName+". Compared with the shipped defaults:"), e.st.diff(lineDiff(current, next)))
	}
	if cfg, err := config.Parse(next); err == nil {
		fmt.Fprintf(e.out, "%s\n\n%s\n", e.st.title.Render("The setup with this config"), proposedMap(cfg, e.st))
	}
	if !e.ui.Confirm("Apply this config to " + config.FileName + "?") {
		fmt.Fprintln(e.out, "Not applied. The config is unchanged.")
		return false, nil
	}
	path := config.Path(e.root)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(next), 0o644); err != nil {
		return false, err
	}
	if err := os.Rename(tmp, path); err != nil {
		return false, err
	}
	fmt.Fprintln(e.out, e.st.good.Render("Applied.")+" Harnesses that are running keep the settings they started with.")
	e.keepOutOfGit(ctx)
	return true, nil
}

// editText lets the user edit text in their own editor and returns the result.
func (e *env) editText(text string) (string, error) {
	f, err := os.CreateTemp("", "h2h-*.toml")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.WriteString(text); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}

	argv, err := editorCommand(os.Getenv, exec.LookPath)
	if err != nil {
		return "", err
	}
	// Not tied to a context: Ctrl-C belongs to the editor while it runs.
	cmd := exec.Command(argv[0], append(argv[1:], f.Name())...)
	cmd.Stdin, cmd.Stdout, cmd.Stderr = e.in, e.tty, e.tty
	if err := e.ui.Foreground(cmd.Run); err != nil {
		return "", fmt.Errorf("editor %s: %w", argv[0], err)
	}
	data, err := os.ReadFile(f.Name())
	return string(data), err
}

// editorCommand picks the user's editor: $VISUAL, then $EDITOR, then the
// first of nano and vi that is installed.
func editorCommand(getenv func(string) string, lookPath func(string) (string, error)) ([]string, error) {
	for _, name := range []string{"VISUAL", "EDITOR"} {
		if argv := strings.Fields(getenv(name)); len(argv) > 0 {
			return argv, nil
		}
	}
	for _, name := range []string{"nano", "vi"} {
		if _, err := lookPath(name); err == nil {
			return []string{name}, nil
		}
	}
	return nil, errors.New("no editor found; set the EDITOR environment variable")
}

// unfence returns the body of the first fenced code block in a reply, or
// the whole reply when it has none. Models often add a fence when told not to.
func unfence(reply string) string {
	lines := strings.Split(strings.TrimSpace(reply), "\n")
	start := -1
	for i, l := range lines {
		if !strings.HasPrefix(strings.TrimSpace(l), "```") {
			continue
		}
		if start < 0 {
			start = i + 1
			continue
		}
		lines = lines[start:i]
		start = -2
		break
	}
	if start >= 0 { // an opening fence with no closing one
		lines = lines[start:]
	}
	return strings.TrimSpace(strings.Join(lines, "\n")) + "\n"
}
