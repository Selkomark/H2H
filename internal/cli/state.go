package cli

import (
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/Selkomark/H2H/internal/session"
)

// A harness's state hooks run h2h-state in its session: "wait" when it
// stops for the user, at a permission prompt or a question, and "go" when
// it carries on. The "go" of a submitted prompt also tells H2H that a
// request it typed arrived: the request's head is in the hook's input.
// See docs/runtime.md.

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
		err := session.ClearWait(root, id)
		if n := requestIn(input); n > 0 {
			err = errors.Join(err, session.Took(root, id, n))
		}
		return err
	case stateWait:
		kind, why := hookNotice(input)
		if kinds := args[1:]; len(kinds) > 0 && kind != "" && !slices.Contains(kinds, kind) {
			return nil
		}
		return session.SetWait(root, id, why)
	}
	return errors.New("usage: state wait [kind ...] | state go")
}

// requestRE finds the head of a request H2H typed, in the text a harness
// submitted.
var requestRE = regexp.MustCompile(strings.Replace(regexp.QuoteMeta(requestHead), "%d", `(\d+)`, 1))

// requestIn returns the number of the request whose head is in a hook's
// input, or 0. Harnesses name the field of a submitted prompt differently,
// so every text in the input is searched.
func requestIn(input []byte) int {
	var in any
	texts := []string{string(input)}
	if json.Unmarshal(input, &in) == nil {
		texts = stringsIn(in, nil)
	}
	for _, t := range texts {
		if m := requestRE.FindStringSubmatch(t); m != nil {
			n, _ := strconv.Atoi(m[1])
			return n
		}
	}
	return 0
}

// stringsIn appends every string in a decoded JSON value to texts.
func stringsIn(v any, texts []string) []string {
	switch v := v.(type) {
	case string:
		texts = append(texts, v)
	case []any:
		for _, item := range v {
			texts = stringsIn(item, texts)
		}
	case map[string]any:
		for _, item := range v {
			texts = stringsIn(item, texts)
		}
	}
	return texts
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
