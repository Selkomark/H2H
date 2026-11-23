// Package console is the interactive prompt of h2h: it reads lines with
// editing, history and completion, and hands each one to a handler.
//
// It knows nothing about what the lines mean. Commands live in package cli,
// so the console and the command line stay two views onto the same code.
package console

import (
	"bufio"
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"strings"

	"golang.org/x/term"
)

// Options configures a console.
type Options struct {
	In     *os.File
	Out    io.Writer
	Prompt string
	// Completions are the words offered when the user presses Tab on the
	// first word of a line.
	Completions []string
	// Handle runs one line and reports whether the console should close.
	// Its context is cancelled when the user presses Ctrl-C while it runs.
	Handle func(ctx context.Context, line string) (quit bool)
}

// Run reads lines until the handler asks to quit or the input ends.
func Run(o Options) error {
	fd := int(o.In.Fd())
	if !term.IsTerminal(fd) {
		return runPlain(o)
	}
	t := term.NewTerminal(struct {
		io.Reader
		io.Writer
	}{o.In, o.Out}, o.Prompt)
	t.AutoCompleteCallback = func(line string, pos int, key rune) (string, int, bool) {
		if key != '\t' || pos != len(line) {
			return "", 0, false
		}
		done, ok := complete(line, o.Completions)
		return done, len(done), ok
	}
	for {
		// The terminal is raw only while a line is being edited. A command
		// then runs with an ordinary terminal, so it can print normally,
		// start an editor or attach to a session.
		old, err := term.MakeRaw(fd)
		if err != nil {
			return err
		}
		if w, h, err := term.GetSize(fd); err == nil && w > 0 && h > 0 {
			_ = t.SetSize(w, h)
		}
		line, err := t.ReadLine()
		_ = term.Restore(fd, old)
		if errors.Is(err, io.EOF) { // Ctrl-D or Ctrl-C at the prompt
			return nil
		}
		if err != nil {
			return err
		}
		if handle(o, line) {
			return nil
		}
	}
}

// runPlain serves input that is not a terminal, such as a pipe or a script.
func runPlain(o Options) error {
	sc := bufio.NewScanner(o.In)
	for sc.Scan() {
		if handle(o, sc.Text()) {
			return nil
		}
	}
	return sc.Err()
}

func handle(o Options, line string) (quit bool) {
	line = strings.TrimSpace(line)
	if line == "" {
		return false
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	return o.Handle(ctx, line)
}

// complete extends line to the only completion it is a prefix of, or to the
// longest prefix shared by all that match.
func complete(line string, words []string) (string, bool) {
	var matches []string
	for _, w := range words {
		if strings.HasPrefix(w, line) {
			matches = append(matches, w)
		}
	}
	switch len(matches) {
	case 0:
		return "", false
	case 1:
		return matches[0] + " ", true
	}
	common := matches[0]
	for _, m := range matches[1:] {
		for !strings.HasPrefix(m, common) {
			common = common[:len(common)-1]
		}
	}
	return common, len(common) > len(line)
}
