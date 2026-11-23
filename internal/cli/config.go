package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"strings"
	"time"

	"golang.org/x/term"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/harness"
	"github.com/Selkomark/H2H/internal/session"
)

// askTimeout bounds every non-interactive harness run: a harness can finish
// its work and still not exit.
const askTimeout = 5 * time.Minute

func (e *env) config(ctx context.Context, args []string) error {
	switch {
	case len(args) == 0 || (len(args) == 1 && args[0] == "show"):
		return e.configShow()
	case len(args) == 1 && args[0] == "edit":
		return e.configEdit()
	}
	return e.configDescribe(ctx, strings.Join(args, " "))
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
	fmt.Fprintln(e.out, "\n"+e.st.dim.Render("Change it with: ")+e.st.accent.Render("config edit")+e.st.dim.Render(", or ")+e.st.accent.Render("config <what you want changed>"))
	return nil
}

// configEdit opens the config in the user's editor, on a copy, and applies
// the result only if it is valid and the user confirms.
func (e *env) configEdit() error {
	if !term.IsTerminal(int(e.in.Fd())) {
		return errors.New("config edit needs a terminal")
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
			if !e.confirm("Edit it again?") {
				fmt.Fprintln(e.out, "Discarded. The config is unchanged.")
				return nil
			}
			continue
		}
		return e.configApply(current, exists, draft)
	}
}

// configDescribe asks the default harness to rewrite the config to match a
// description. The proposal goes through the same validation and
// confirmation as a manual edit.
func (e *env) configDescribe(ctx context.Context, description string) error {
	current, exists, err := e.configText()
	if err != nil {
		return err
	}
	// The harness comes from the shipped defaults if the file is broken, so
	// a bad config can still be repaired in words.
	cfg, err := config.Load(e.root)
	if err != nil {
		if cfg, err = config.Parse(config.Template()); err != nil {
			return err
		}
	}
	name, h, err := cfg.Lookup("")
	if err != nil {
		return err
	}
	prompt := "You are editing the configuration file of H2H, a tool that supervises coding-agent harness sessions. " +
		"Reply with the complete new contents of the file and nothing else: no explanation and no code fence. " +
		"Keep the existing comments. Change only what the request asks for. Do not read or change any file.\n\n" +
		"The file is TOML. The comments in it explain each setting.\n\n" +
		"Current file:\n\n" + current + "\n\nRequested change: " + description + "\n"

	fmt.Fprintln(e.out, e.st.dim.Render("Asking "+name+" for a new config..."))
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	var reply, errOut bytes.Buffer
	if err := harness.Ask(ctx, h, e.environ, e.root, prompt, &reply, &errOut); err != nil {
		return fmt.Errorf("%s could not answer: %w\n%s", name, err, strings.TrimSpace(errOut.String()))
	}
	proposal := unfence(reply.String())
	if _, err := config.Parse(proposal); err != nil {
		return fmt.Errorf("%s proposed a config that is not valid (%v); nothing was changed", name, err)
	}
	if proposal == current && exists {
		fmt.Fprintln(e.out, "No changes proposed.")
		return nil
	}
	return e.configApply(current, exists, proposal)
}

// configApply is the plan-and-apply step: show the difference, ask, write.
func (e *env) configApply(current string, exists bool, next string) error {
	if exists {
		fmt.Fprintf(e.out, "%s\n\n%s\n", e.st.title.Render("Changes to "+config.FileName), e.st.diff(lineDiff(current, next)))
	} else if next == current {
		fmt.Fprintf(e.out, "This creates %s with the shipped defaults.\n", config.FileName)
	} else {
		fmt.Fprintf(e.out, "%s\n\n%s\n", e.st.title.Render("This creates "+config.FileName+". Compared with the shipped defaults:"), e.st.diff(lineDiff(current, next)))
	}
	if !e.confirm("Apply?") {
		fmt.Fprintln(e.out, "Not applied. The config is unchanged.")
		return nil
	}
	path := config.Path(e.root)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(next), 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	fmt.Fprintln(e.out, e.st.good.Render("Applied.")+" Running sessions keep the settings they started with.")
	return nil
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
	cmd.Stdin, cmd.Stdout, cmd.Stderr = e.in, e.out, e.errw
	if err := cmd.Run(); err != nil {
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

// confirm asks a yes-or-no question. Anything but yes is no.
func (e *env) confirm(question string) bool {
	fmt.Fprintf(e.out, "%s [y/N] ", question)
	// One byte at a time, so nothing typed after the answer is swallowed.
	var line []byte
	b := make([]byte, 1)
	for {
		n, err := e.in.Read(b)
		if n == 0 || err != nil || b[0] == '\n' {
			break
		}
		line = append(line, b[0])
	}
	answer := strings.ToLower(strings.TrimSpace(string(line)))
	return answer == "y" || answer == "yes"
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

// ask sends text typed into the console to the default harness and prints
// its answer.
func (e *env) ask(ctx context.Context, text string) error {
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	name, h, err := cfg.Lookup("")
	if err != nil {
		return err
	}
	infos, err := session.List(e.root)
	if err != nil {
		return err
	}
	// The harness gets the same views the user has, without styling.
	state := "There are no sessions.\n"
	if len(infos) > 0 {
		state = sessionTable(e.root, infos, plainStyles())
	}
	var help strings.Builder
	helper := *e
	helper.out, helper.st = &help, plainStyles()
	_ = helper.help(ctx, nil)

	prompt := "You are answering a message typed into the console of H2H, a tool that supervises coding-agent harness sessions " +
		"for the project in the current directory. Answer briefly in plain text. Do not change any file. " +
		"If the user wants something a console command does, tell them the command.\n\n" +
		"Sessions right now:\n" + state + "\n" + help.String() + "\nMessage: " + text + "\n"

	fmt.Fprintln(e.out, e.st.dim.Render("Asking "+name+"..."))
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()
	if err := harness.Ask(ctx, h, e.environ, e.root, prompt, e.out, e.errw); err != nil {
		if ctx.Err() != nil {
			return fmt.Errorf("%s was interrupted", name)
		}
		return fmt.Errorf("%s could not answer: %w", name, err)
	}
	return nil
}
