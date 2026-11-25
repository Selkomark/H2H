package harness

import (
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"strings"

	"github.com/Selkomark/H2H/internal/config"
)

// Turn runs one turn of a conversation with the harness: it answers
// prompt, as AskStream does, as part of the conversation with the ID
// given, or as the start of a new one when id is "".
//
// It returns the ID the conversation goes on under. For a harness that
// takes the ID from H2H that is one H2H made up, and for one that chooses
// its own it is the one its output reported. It is "" when the harness
// cannot carry a conversation on, or reported no ID: the next turn then
// starts a new conversation.
func Turn(ctx context.Context, h config.Harness, environ []string, dir, id, prompt string, stdout, stderr io.Writer) (string, error) {
	switch {
	case !h.Remembers():
		return "", AskStream(ctx, h, environ, dir, prompt, stdout, stderr)
	case id != "":
		return id, answer(ctx, h, environ, dir, withConversation(h.ResumeArgs, id), prompt, stdout, stderr)
	case h.NamesConversation():
		id = newID()
		return id, answer(ctx, h, environ, dir, withConversation(h.ConversationArgs, id), prompt, stdout, stderr)
	}
	// The harness chooses the ID, and says which in its output.
	return stream(ctx, h, environ, dir, h.StreamArgs, prompt, stdout, stderr)
}

// answer runs the harness with args in place of those it answers with.
func answer(ctx context.Context, h config.Harness, environ []string, dir string, args []string, prompt string, stdout, stderr io.Writer) error {
	if h.Streams() {
		_, err := stream(ctx, h, environ, dir, args, prompt, stdout, stderr)
		return err
	}
	h.AskArgs = args
	return Ask(ctx, h, environ, dir, prompt, stdout, stderr)
}

// withConversation puts a conversation's ID into a harness's arguments.
func withConversation(args []string, id string) []string {
	out := make([]string, len(args))
	for n, a := range args {
		out[n] = strings.ReplaceAll(a, config.ConversationPlaceholder, id)
	}
	return out
}

// newID makes up a conversation ID: a random UUID, which is the form the
// harnesses that take one expect.
func newID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40 // version 4
	b[8] = b[8]&0x3f | 0x80 // the RFC 4122 variant
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}
