package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/keeper"
	"github.com/Selkomark/H2H/internal/policy"
	"github.com/Selkomark/H2H/internal/session"
	"github.com/Selkomark/H2H/internal/workspace"
)

// A harness that is about to stop and ask "may I do this?" asks H2H first,
// through its own permission hook, which runs h2h-permit. H2H answers from
// the project's rules. What no rule covers is put to the user in the
// console, and the hook waits for that answer. See docs/permissions.md.

const (
	// permitCommand is the internal command a harness's hook runs.
	permitCommand = "permit"
	// permitWait is how long a request waits for the user. It stays under
	// the time limit the harness is given for its hook in permit_args,
	// which is ten minutes: past that limit the harness stops listening
	// and shows its own prompt.
	permitWait = 9 * time.Minute
	// maxSubject is how much of a request the user is shown.
	maxSubject = 600

	// The user's answers to a request.
	answerOnce   = "once"
	answerAlways = "always"
	answerDeny   = "deny"

	// What a harness is told when the answer is no. The model reads it
	// as an instruction, so it says what to do next.
	deniedByRule = "This is not allowed by the project's permission rules in h2h.toml. Do not try to do the same thing another way. Carry on with what you can do without it, and say in your report that it was not allowed."
	deniedByUser = "The user declined this. Do not try to do the same thing another way. Carry on with what you can do without it, and say in your report that the user declined it."
	notAnswered  = "The user has not answered this request yet. Do not retry it now. Carry on with what you can do without it, and say in your report that it is waiting for the user's approval."
)

// permitRequest is one request as it waits in the session's queue.
type permitRequest struct {
	Tool    string    `json:"tool"`
	Subject string    `json:"subject"` // what the rules are matched against
	Shown   string    `json:"shown"`   // the same as the harness wrote it, for the user
	Until   time.Time `json:"until"`   // when the hook stops waiting
}

// projectMark stands for the project's folder in what the rules are
// matched against, and in the rules themselves.
const projectMark = "{project}"

// portable rewrites the paths in a request so that the rules which match
// it do not depend on where the project was cloned. The harness's own
// working copy becomes "." and the project's folder becomes {project}:
// "Edit ./web/app.tsx", "git -C {project} status". A path anywhere else
// stays as it is.
//
// The working copy is replaced first, because it is inside the project.
func portable(subject, root, workspace string) string {
	for _, w := range pathForms(workspace) {
		subject = replacePath(subject, w, ".")
	}
	for _, r := range pathForms(root) {
		subject = replacePath(subject, r, projectMark)
	}
	return subject
}

// pathForms returns a folder as it may be written: as given, and with
// its symbolic links followed, which is how a harness often reports it.
func pathForms(path string) []string {
	if path == "" {
		return nil
	}
	forms := []string{filepath.Clean(path)}
	if real, err := filepath.EvalSymlinks(path); err == nil && real != forms[0] {
		forms = append(forms, real)
	}
	// The longer form first, so that one is not replaced inside the other.
	sort.Slice(forms, func(a, b int) bool { return len(forms[a]) > len(forms[b]) })
	return forms
}

// replacePath replaces a folder's path in text where it stands as a whole
// path or the start of one, and not where it is the start of a longer
// name: /p/app is replaced in "/p/app/x" and "cd /p/app", not in
// "/p/app-old".
func replacePath(text, path, with string) string {
	var out strings.Builder
	for {
		at := strings.Index(text, path)
		if at < 0 {
			return out.String() + text
		}
		after := text[at+len(path):]
		out.WriteString(text[:at])
		if after == "" || !strings.ContainsRune("abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789._-", rune(after[0])) {
			out.WriteString(with)
		} else {
			out.WriteString(path)
		}
		text = after
	}
}

// permitSubject reads what a harness wants to do out of its hook's input:
// the tool, and the text the rules are matched against. For the shell
// that is the command itself. For another tool it is the tool's name and
// what it acts on.
func permitSubject(input []byte) (tool, subject string) {
	var in struct {
		ToolName  string         `json:"tool_name"`
		ToolInput map[string]any `json:"tool_input"`
	}
	if err := json.Unmarshal(input, &in); err != nil || in.ToolName == "" {
		return "", ""
	}
	if command, ok := in.ToolInput["command"].(string); ok && command != "" {
		return in.ToolName, command
	}
	for _, key := range []string{"file_path", "path", "url", "pattern", "query"} {
		if v, ok := in.ToolInput[key].(string); ok && v != "" {
			return in.ToolName, in.ToolName + " " + v
		}
	}
	return in.ToolName, in.ToolName
}

// permit answers one permission request of a harness. It is run by the
// harness's hook inside its session, with the request as its input, and
// prints the answer the hook passes back.
//
// A request it cannot read, or a config it cannot load, is never allowed:
// it goes to the user. An error leaves the decision to the harness's own
// prompt, which is what would have happened without H2H.
func permit(in io.Reader, out io.Writer, getenv func(string) string) error {
	root, id := getenv("H2H_ROOT"), getenv("H2H_SESSION")
	if root == "" || !session.ValidID(id) {
		return errors.New("this command is for a harness started by H2H")
	}
	input, err := io.ReadAll(in)
	if err != nil {
		return err
	}
	tool, shown := permitSubject(input)
	if shown == "" {
		return errors.New("the request names no tool")
	}
	// The rules see paths as they are in any clone of the project.
	work := workspace.Path(root, id)
	if info, err := session.Load(root, id); err == nil && info.Workspace != "" {
		work = info.Workspace
	}
	subject := portable(shown, root, work)
	dir, err := session.PermitDir(root, id)
	if err != nil {
		return err
	}

	// Reporting back is what H2H asked the harness to do, so it needs no
	// rule and no question.
	if isReport(shown, keeper.ReportPath(root, id)) {
		return writeDecision(out, true, "")
	}
	decision := policy.Ask
	if cfg, err := config.Load(root); err == nil {
		decision = cfg.Permissions.Rules().Decide(subject)
	}
	// What the user allowed for the rest of this session counts as a rule.
	if decision == policy.Ask && slices.Contains(alwaysAllowed(dir), subject) {
		decision = policy.Allow
	}
	switch decision {
	case policy.Allow:
		return writeDecision(out, true, "")
	case policy.Deny:
		return writeDecision(out, false, deniedByRule)
	}

	switch answer := awaitAnswer(dir, permitRequest{Tool: tool, Subject: subject, Shown: shown, Until: time.Now().Add(permitWait)}); answer {
	case answerOnce, answerAlways:
		return writeDecision(out, true, "")
	case answerDeny:
		return writeDecision(out, false, deniedByUser)
	}
	return writeDecision(out, false, notAnswered)
}

// isReport reports whether a shell command is the harness sending its
// report, and nothing else: h2h-report by its name or its full path, with
// its answer as arguments or as a here-document. A command that goes on to
// do anything more is not taken for a report.
func isReport(command, path string) bool {
	first, rest, _ := strings.Cut(strings.TrimSpace(command), "\n")
	fields := strings.Fields(first)
	if len(fields) == 0 || (fields[0] != keeper.ReportCommand && fields[0] != path) {
		return false
	}
	// Nothing on the command's own line may start another command.
	line := first
	marker := ""
	if before, after, ok := strings.Cut(first, "<<"); ok {
		line, marker = before, strings.Trim(strings.TrimSpace(strings.TrimPrefix(after, "-")), `'"`)
	}
	if strings.ContainsAny(line, ";&|`$()<>") || (marker == "" && rest != "") {
		return false
	}
	if marker == "" {
		return true
	}
	// With a here-document, the command ends where the document does.
	lines := strings.Split(rest, "\n")
	for n, l := range lines {
		if strings.TrimSpace(l) == marker {
			return strings.TrimSpace(strings.Join(lines[n+1:], "")) == ""
		}
	}
	return false
}

// writeDecision prints an answer in the form a permission hook returns.
// Claude Code and Codex take the same form.
func writeDecision(out io.Writer, allow bool, message string) error {
	decision := map[string]any{"behavior": "deny", "message": message}
	if allow {
		decision = map[string]any{"behavior": "allow"}
	}
	return json.NewEncoder(out).Encode(map[string]any{
		"hookSpecificOutput": map[string]any{"hookEventName": "PermissionRequest", "decision": decision},
	})
}

// awaitAnswer puts a request in the queue and waits for the user's answer.
// It returns "" when none came in time.
func awaitAnswer(dir string, r permitRequest) string {
	n := 1
	for ; ; n++ {
		f, err := os.OpenFile(filepath.Join(dir, strconv.Itoa(n)+".ask"), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, os.ErrExist) {
			continue
		}
		if err != nil {
			return ""
		}
		err = json.NewEncoder(f).Encode(r)
		f.Close()
		if err != nil {
			return ""
		}
		break
	}
	ask, answer := filepath.Join(dir, strconv.Itoa(n)+".ask"), filepath.Join(dir, strconv.Itoa(n)+".answer")
	// The request leaves the queue when it is over, answered or not.
	defer os.Remove(ask)
	defer os.Remove(answer)
	for time.Now().Before(r.Until) {
		if data, err := os.ReadFile(answer); err == nil {
			return strings.TrimSpace(string(data))
		}
		time.Sleep(200 * time.Millisecond)
	}
	return ""
}

// alwaysAllowed returns what the user allowed for the rest of a session.
func alwaysAllowed(dir string) []string {
	data, err := os.ReadFile(filepath.Join(dir, "always.json"))
	if err != nil {
		return nil
	}
	var subjects []string
	_ = json.Unmarshal(data, &subjects)
	return subjects
}

// answerPermit records the user's answer to request n of a session.
func answerPermit(dir string, n int, subject, answer string) error {
	if answer == answerAlways {
		data, err := json.Marshal(append(alwaysAllowed(dir), subject))
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "always.json"), data, 0o600); err != nil {
			return err
		}
	}
	path := filepath.Join(dir, strconv.Itoa(n)+".answer")
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, []byte(answer), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// pendingPermit returns the oldest permission request that waits for the
// user, of any running harness, as a question for the console. It returns
// nil when nothing waits.
func (e *env) pendingPermit() *console.Request {
	infos, err := session.List(e.root)
	if err != nil {
		return nil
	}
	for _, info := range infos {
		if !info.Live() {
			continue
		}
		dir, err := session.PermitDir(e.root, info.ID)
		if err != nil {
			continue
		}
		entries, _ := os.ReadDir(dir)
		var waiting []int
		for _, entry := range entries {
			if name, ok := strings.CutSuffix(entry.Name(), ".ask"); ok {
				if n, err := strconv.Atoi(name); err == nil {
					waiting = append(waiting, n)
				}
			}
		}
		sort.Ints(waiting)
		for _, n := range waiting {
			var r permitRequest
			data, err := os.ReadFile(filepath.Join(dir, strconv.Itoa(n)+".ask"))
			if err != nil || json.Unmarshal(data, &r) != nil || time.Now().After(r.Until) {
				continue // being written, or its hook has stopped waiting
			}
			if _, err := os.Stat(filepath.Join(dir, strconv.Itoa(n)+".answer")); err == nil {
				continue // answered; the hook has yet to pick it up
			}
			// A shell command is shown as it would be run. Another tool
			// is shown by its name and what it acts on.
			what := "run this command"
			if r.Tool != "" && strings.HasPrefix(r.Subject, r.Tool) {
				what = "use this tool"
			}
			shown := r.Shown
			if shown == "" {
				shown = r.Subject
			}
			return &console.Request{
				ID:       info.ID + "/" + strconv.Itoa(n),
				Question: info.ID + " wants to " + what + ":\n\n" + displayBlock(shown, maxSubject),
				Options: []console.Option{
					{ID: answerOnce, Label: "Allow once"},
					{ID: answerAlways, Label: "Allow this for the rest of " + info.ID + "'s session"},
					{ID: answerDeny, Label: "Deny"},
				},
				Answer: func(choice string) {
					if choice == "" {
						choice = answerDeny // leaving the dialog agrees to nothing
					}
					_ = answerPermit(dir, n, r.Subject, choice)
				},
			}
		}
	}
	return nil
}

// displayBlock shortens text for a dialog, keeping its line breaks.
func displayBlock(text string, limit int) string {
	text = strings.TrimSpace(text)
	if r := []rune(text); len(r) > limit {
		text = string(r[:limit]) + " ..."
	}
	return text
}

// loadConfig reads the project's config.
func loadConfig(root string) (config.Config, error) { return config.Load(root) }
