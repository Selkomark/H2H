package console

import (
	"regexp"
	"strings"
)

var (
	// A terminal control sequence: ESC [ parameters, intermediates, final byte.
	csiRE = regexp.MustCompile(`\x1b\[[\x30-\x3f]*[\x20-\x2f]*[\x40-\x7e]`)
	// An operating system command, such as setting the window title.
	oscRE = regexp.MustCompile(`\x1b\][^\x07\x1b]*(\x07|\x1b\\)?`)
	// Any other escape sequence.
	escRE = regexp.MustCompile(`\x1b[^\[\]]?`)
	// Colour and text attributes, the only sequences the transcript keeps.
	sgrRE = regexp.MustCompile(`^\x1b\[[0-9;]*m$`)
)

// sanitize makes text safe to place in the transcript. Output from a
// harness can move the cursor or clear the screen, which would break the
// console's layout, so everything but colour is removed.
func sanitize(s string) string {
	s = oscRE.ReplaceAllString(s, "")
	s = csiRE.ReplaceAllStringFunc(s, func(seq string) string {
		if sgrRE.MatchString(seq) {
			return seq
		}
		return ""
	})
	// What is left of ESC is either a kept colour sequence or a stray.
	s = escRE.ReplaceAllStringFunc(s, func(seq string) string {
		if seq == "\x1b" { // followed by [ or ]: the start of a kept sequence, or the end of the text
			return seq
		}
		return ""
	})
	s = strings.ReplaceAll(s, "\r\n", "\n")
	s = strings.ReplaceAll(s, "\r", "\n")
	return strings.Map(func(r rune) rune {
		if r < 0x20 && r != '\n' && r != '\t' && r != 0x1b {
			return -1
		}
		return r
	}, s)
}

// match returns the suggestions whose text starts with prefix.
func match(all []Suggestion, prefix string) []Suggestion {
	var out []Suggestion
	for _, s := range all {
		if strings.HasPrefix(s.Text, prefix) {
			out = append(out, s)
		}
	}
	return out
}

// suggest works out what to offer for the line typed so far. It reports
// whether the offers are for an argument, as opposed to the command itself.
func suggest(line string, commands []Suggestion, arguments func(string) []Suggestion) (offers []Suggestion, forArgument bool) {
	if !strings.HasPrefix(line, "/") {
		return nil, false
	}
	name, rest, typingArgument := strings.Cut(line, " ")
	if !typingArgument {
		return match(commands, line), false
	}
	if arguments == nil || strings.Contains(strings.TrimLeft(rest, " "), " ") {
		return nil, true // only the first argument has suggestions
	}
	return match(arguments(name[1:]), strings.TrimLeft(rest, " ")), true
}

// accept returns the line after the user takes a suggestion.
func accept(line string, s Suggestion, forArgument bool) string {
	if !forArgument {
		return s.Text + " "
	}
	name, _, _ := strings.Cut(line, " ")
	return name + " " + s.Text + " "
}
