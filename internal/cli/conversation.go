package cli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"

	"github.com/Selkomark/H2H/internal/config"
)

// A console session has one conversation with the default harness, which
// the harness keeps itself: each message is a run of the harness that
// continues the conversation by its ID, so the harness remembers what was
// said before as it would in its own terminal. H2H never reads a
// harness's screen; it keeps only the ID. /clear ends the conversation,
// and the next message starts another in the same console session.

// talk is a console session's conversation with the default harness.
type talk struct {
	// Harness is the harness that holds the conversation. When the default
	// harness changes, the next message starts a conversation with the new
	// one.
	Harness string `json:"harness"`
	// ID is what the harness knows the conversation by.
	ID string `json:"id"`
	// Wiring is a digest of the config as the harness was last shown it,
	// so a later message repeats the config only when it has changed.
	Wiring string `json:"wiring,omitempty"`
	// Gen is which conversation of the console session this is, so that
	// an answer that ends after /clear does not bring the old one back.
	// It is not saved.
	Gen int `json:"-"`
}

// talks keeps the conversation of the console session a line runs in.
type talks interface {
	talk() talk
	setTalk(talk)
	// endTalk ends the conversation, so the next message starts another.
	endTalk()
}

func (o *openSession) talk() talk {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.current.Talk == nil {
		return talk{Gen: o.talks}
	}
	t := *o.current.Talk
	t.Gen = o.talks
	return t
}

// setTalk keeps the conversation with the session, which is saved when the
// line that changed it finishes. An empty one ends it. A message is
// answered while the user may type commands, so the conversation may have
// been ended, or the session switched, since the message began: what it
// keeps then belongs to no conversation and is dropped.
func (o *openSession) setTalk(t talk) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if t.Gen != o.talks {
		return
	}
	if t.ID == "" {
		o.current.Talk = nil
		return
	}
	t.Gen = 0
	o.current.Talk = &t
}

func (o *openSession) endTalk() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.current.Talk = nil
	o.talks++
}

// scriptTalk keeps the conversation of lines read from a pipe, which are
// not saved.
type scriptTalk struct{ t talk }

func (s *scriptTalk) talk() talk     { return s.t }
func (s *scriptTalk) setTalk(t talk) { s.t = t }
func (s *scriptTalk) endTalk()       { s.t = talk{} }

// currentTalk is the conversation the line runs in; outside the console,
// where there is none, it is always a new one.
func (e *env) currentTalk() talk {
	if e.conv == nil {
		return talk{}
	}
	return e.conv.talk()
}

func (e *env) keepTalk(t talk) {
	if e.conv != nil {
		e.conv.setTalk(t)
	}
}

// digest stands for a text without repeating it.
func digest(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:8])
}

// clearTalk ends the console session's conversation with the default
// harness. The page stays as it is; the next message starts a new
// conversation, which knows nothing of this one.
func (e *env) clearTalk(_ context.Context, args []string) error {
	switch {
	case len(args) > 0:
		return errors.New("usage: clear")
	case e.conv == nil:
		return errors.New("clear starts a new conversation with the default harness in the console; there is none outside it")
	}
	name := "the default harness"
	if cfg, err := config.Load(e.root); err == nil {
		name = cfg.DefaultHarness
	}
	// Ended even when none was kept yet, since a first message may be
	// being answered: the conversation it starts is not kept either.
	had := e.currentTalk().ID != ""
	e.conv.endTalk()
	if !had {
		fmt.Fprintf(e.out, "There is no conversation with %s yet. Your next message starts one.\n", name)
		return nil
	}
	fmt.Fprintf(e.out, "Cleared the conversation with %s. Your next message starts a new one.\n", name)
	return nil
}
