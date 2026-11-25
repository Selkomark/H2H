package cli

import (
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"

	"github.com/Selkomark/H2H/internal/session"
)

// A harness's state hooks run h2h-state in its session: "wait" when it
// stops for the user, at a permission prompt or a question, and "go" when
// it carries on. See docs/runtime.md.

const (
	// stateCommand is the internal command the state hooks run.
	stateCommand = "state"
	stateWait    = "wait"
	stateGo      = "go"
	// maxHookInput is as much of a hook's input as is read.
	maxHookInput = 1 << 20
)

// state records what a harness's hook says. "wait" may name the kinds of
// notification that mean waiting, for a harness whose hook fires for
// others too: an event of another kind is then left alone. It prints
// nothing, since a harness may read what a hook prints, and the caller
// exits 0 whatever happens, since a hook that fails may stop the harness.
func state(args []string, in io.Reader, getenv func(string) string) error {
	root, id := getenv("H2H_ROOT"), getenv("H2H_SESSION")
	if root == "" || !session.ValidID(id) {
		return errors.New("this command is for a harness started by H2H")
	}
	input, _ := io.ReadAll(io.LimitReader(in, maxHookInput))
	if len(args) == 0 {
		return errors.New("usage: state wait [kind ...] | state go")
	}
	switch args[0] {
	case stateGo:
		return session.ClearWait(root, id)
	case stateWait:
		kind, why := hookNotice(input)
		if kinds := args[1:]; len(kinds) > 0 && kind != "" && !slices.Contains(kinds, kind) {
			return nil
		}
		return session.SetWait(root, id, why)
	}
	return errors.New("usage: state wait [kind ...] | state go")
}

// hookNotice reads, from a hook's input, the kind of notification and what
// the harness says it waits for. Harnesses name these fields differently,
// and any of them may be missing.
func hookNotice(input []byte) (kind, why string) {
	var in map[string]any
	if json.Unmarshal(input, &in) != nil {
		return "", ""
	}
	text := func(keys ...string) string {
		for _, k := range keys {
			if v, ok := in[k].(string); ok && strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		}
		return ""
	}
	kind = text("notification_type", "notificationType")
	title, message := text("title"), text("message")
	switch {
	case title != "" && message != "" && !strings.Contains(message, title):
		why = title + ": " + message
	case message != "":
		why = message
	default:
		why = title
	}
	if why == "" {
		// A permission hook names the tool it is about to ask for.
		if tool, subject := permitSubject(input); tool != "" {
			why = "Permission needed: " + subject
		}
	}
	return kind, why
}
