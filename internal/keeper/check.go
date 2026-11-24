package keeper

import (
	"fmt"
	"net"
	"os"

	"github.com/creack/pty"

	"github.com/Selkomark/H2H/internal/session"
)

// checkID names the socket Check listens on. It has no number, so it can
// never be the ID of a session.
const checkID = "check"

// Check reports whether this system gives a keeper what it needs: a way to
// start itself, a pseudo-terminal for the harness, and a local socket for
// attaching. It leaves nothing behind.
func Check(root string) error {
	if _, err := os.Executable(); err != nil {
		return fmt.Errorf("h2h cannot find its own program to start a keeper: %w", err)
	}
	ptmx, tty, err := pty.Open()
	if err != nil {
		return fmt.Errorf("opening a pseudo-terminal: %w", err)
	}
	tty.Close()
	ptmx.Close()

	sock, err := session.SocketPath(root, checkID)
	if err != nil {
		return err
	}
	_ = os.Remove(sock) // left behind by a check that was interrupted
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fmt.Errorf("listening on a local socket: %w", err)
	}
	ln.Close()
	_ = os.Remove(sock)
	return nil
}
