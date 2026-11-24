// Package policy decides what a harness may do without asking the user.
//
// It holds the rules that must be exact, so it is pure: no files, no
// processes, nothing but the decision. See docs/permissions.md.
package policy

import (
	"slices"
	"strings"
)

// Decision is the answer to a harness that asks "may I do this?".
type Decision int

const (
	Ask   Decision = iota // no rule covers it: the user decides
	Allow                 // it may go ahead
	Deny                  // it is refused
)

// The modes: what happens to a request that no rule covers.
const (
	ModeAsk  = "ask"  // the user is asked
	ModeAuto = "auto" // it is allowed
)

// Rules are the permission rules of a project.
type Rules struct {
	// Mode is ModeAsk or ModeAuto. Empty means ModeAsk: a missing
	// setting never allows anything.
	Mode string
	// Allow and Deny are patterns matched against what the harness wants
	// to do: a shell command as it would be run, or for another tool its
	// name followed by what it acts on. A * stands for any text.
	Allow []string
	Deny  []string
}

// Decide answers a request. Deny wins over allow, and both win over the
// mode.
//
// A shell command can be several commands in one: "git status; rm -rf x".
// The rules are applied to each of them, so that a pattern for the first
// does not let the rest through. The request is denied if any command in
// it is denied, and allowed by rule only if every command in it is.
func (r Rules) Decide(subject string) Decision {
	subject = strings.TrimSpace(subject)
	parts := Commands(subject)
	for _, p := range r.Deny {
		if Match(p, subject) || slices.ContainsFunc(parts, func(part string) bool { return Match(p, part) }) {
			return Deny
		}
	}
	allowed := len(parts) > 0
	for _, part := range parts {
		// A command that writes to a file or runs another command inside
		// itself does more than its pattern says, so no pattern allows it.
		if !plain(part) || !slices.ContainsFunc(r.Allow, func(p string) bool { return Match(p, part) }) {
			allowed = false
			break
		}
	}
	if allowed || r.Mode == ModeAuto {
		return Allow
	}
	return Ask
}

// Commands splits a shell command into the commands it runs one after
// another or pipes together: at ; && || | & and line breaks, but not
// inside quotes. Text with none of these is one command.
func Commands(subject string) []string {
	var parts []string
	var cur strings.Builder
	quote := rune(0)
	flush := func() {
		if part := strings.TrimSpace(cur.String()); part != "" {
			parts = append(parts, part)
		}
		cur.Reset()
	}
	runes := []rune(subject)
	for i := 0; i < len(runes); i++ {
		c := runes[i]
		switch {
		case quote != 0:
			if c == quote {
				quote = 0
			}
			cur.WriteRune(c)
		case c == '\'' || c == '"':
			quote = c
			cur.WriteRune(c)
		case c == '\\' && i+1 < len(runes):
			cur.WriteRune(c)
			i++
			cur.WriteRune(runes[i])
		case c == ';' || c == '\n' || c == '|':
			// || and | both end a command.
			flush()
		case c == '&':
			// 2>&1 and &> are redirections, not the end of a command.
			if (i > 0 && runes[i-1] == '>') || (i+1 < len(runes) && runes[i+1] == '>') {
				cur.WriteRune(c)
				continue
			}
			flush()
		default:
			cur.WriteRune(c)
		}
	}
	flush()
	return parts
}

// harmless are the redirections that send output nowhere or join the two
// output streams. Any other redirection reads or writes a file.
var harmless = strings.NewReplacer("2>/dev/null", "", "&>/dev/null", "", ">/dev/null", "", "2>&1", "", "1>&2", "")

// plain reports whether a command is only itself: it redirects nothing
// to or from a file, and runs no other command inside it.
func plain(command string) bool {
	return !strings.ContainsAny(harmless.Replace(command), "<>`") && !strings.Contains(command, "$(")
}

// Match reports whether subject fits pattern, in which * stands for any
// text, including none. Everything else must be equal. A pattern without
// a * therefore matches only that exact subject.
func Match(pattern, subject string) bool {
	parts := strings.Split(strings.TrimSpace(pattern), "*")
	if len(parts) == 1 {
		return parts[0] != "" && parts[0] == subject
	}
	if !strings.HasPrefix(subject, parts[0]) {
		return false
	}
	rest := subject[len(parts[0]):]
	for _, part := range parts[1 : len(parts)-1] {
		at := strings.Index(rest, part)
		if at < 0 {
			return false
		}
		rest = rest[at+len(part):]
	}
	return strings.HasSuffix(rest, parts[len(parts)-1])
}
