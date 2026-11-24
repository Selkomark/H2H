package keeper

import (
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"strings"
	"time"
)

const (
	// Bracketed paste: a terminal program takes what is between these as
	// pasted text, so line breaks in it do not submit it early.
	pasteStart, pasteEnd = "\x1b[200~", "\x1b[201~"
	// typeSettle is how long the harness is given to take in the pasted
	// text before Enter is pressed.
	typeSettle = 500 * time.Millisecond
	// typeGreeting is how long Type waits to learn that the session is
	// attached elsewhere.
	typeGreeting = 300 * time.Millisecond
	maxFrame     = 60_000 // a frame's length is two bytes
)

// ErrAttached means someone else is driving the session.
var ErrAttached = errors.New("the session is attached elsewhere; a session has one driver at a time")

// Type enters text into a session's terminal as if the user had pasted it
// and pressed Enter. It is how a request is handed to a running harness.
func Type(sock, text string) error {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return fmt.Errorf("connecting to session: %w", err)
	}
	defer conn.Close()

	// A keeper that already has a client says so and hangs up.
	greeting := make([]byte, 256)
	_ = conn.SetReadDeadline(time.Now().Add(typeGreeting))
	if n, _ := conn.Read(greeting); strings.Contains(string(greeting[:n]), "already attached elsewhere") {
		return ErrAttached
	}
	// From here on the harness's output is of no interest, but it has to
	// be read, or the keeper would find this client stalled and drop it.
	go func() {
		_ = conn.SetReadDeadline(time.Time{})
		buf := make([]byte, 32*1024)
		for {
			if _, err := conn.Read(buf); err != nil {
				return
			}
		}
	}()

	send := func(payload string) error {
		for len(payload) > 0 {
			part := payload[:min(len(payload), maxFrame)]
			payload = payload[len(part):]
			head := []byte{frameInput, 0, 0}
			binary.BigEndian.PutUint16(head[1:], uint16(len(part)))
			if _, err := conn.Write(append(head, part...)); err != nil {
				return fmt.Errorf("typing into the session: %w", err)
			}
		}
		return nil
	}
	if err := send(pasteStart + text + pasteEnd); err != nil {
		return err
	}
	time.Sleep(typeSettle)
	if err := send("\r"); err != nil {
		return err
	}
	time.Sleep(typeSettle) // let the keeper pass it on before the line drops
	return nil
}
