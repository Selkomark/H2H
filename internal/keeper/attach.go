package keeper

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sync"
	"syscall"

	"golang.org/x/term"
)

// DetachKey leaves an attached session without stopping it: Ctrl-\.
const DetachKey = 0x1c

// resetTerminal undoes what a full-screen program may have left switched
// on: alternate screen, hidden cursor, mouse reporting, bracketed paste,
// extended keyboard mode and colours.
const resetTerminal = "\x1b[?1049l\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[<u\x1b[0m\r\n"

// Attach connects the user's terminal to a session until the user presses
// DetachKey or the session ends. It reports whether the session ended.
func Attach(sock string, in *os.File, out io.Writer) (ended bool, err error) {
	conn, err := net.Dial("unix", sock)
	if err != nil {
		return false, fmt.Errorf("connecting to session: %w", err)
	}
	defer conn.Close()

	fd := int(in.Fd())
	old, err := term.MakeRaw(fd)
	if err != nil {
		return false, err
	}
	defer func() {
		_ = term.Restore(fd, old)
		_, _ = io.WriteString(out, resetTerminal)
	}()

	var wmu sync.Mutex
	send := func(kind byte, payload []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		head := []byte{kind, 0, 0}
		binary.BigEndian.PutUint16(head[1:], uint16(len(payload)))
		_, err := conn.Write(append(head, payload...))
		return err
	}
	resize := func() {
		cols, rows, err := term.GetSize(fd)
		if err != nil {
			return
		}
		var p [4]byte
		binary.BigEndian.PutUint16(p[:], uint16(rows))
		binary.BigEndian.PutUint16(p[2:], uint16(cols))
		_ = send(frameResize, p[:])
	}
	resize()

	winch := make(chan os.Signal, 1)
	signal.Notify(winch, syscall.SIGWINCH)
	defer signal.Stop(winch)
	go func() {
		for range winch {
			resize()
		}
	}()

	remoteDone := make(chan struct{})
	go func() {
		_, _ = io.Copy(out, conn)
		close(remoteDone)
	}()

	// The reader owns the user's input until it returns. It returns on the
	// detach key, or on the first key pressed after the connection is gone.
	inputDone := make(chan bool, 1) // true when the user asked to detach
	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := in.Read(buf)
			if err != nil {
				inputDone <- false
				return
			}
			data, detach := buf[:n], false
			if i := bytes.IndexByte(data, DetachKey); i >= 0 {
				data, detach = data[:i], true
			}
			if len(data) > 0 && send(frameInput, data) != nil {
				inputDone <- false
				return
			}
			if detach {
				inputDone <- true
				return
			}
		}
	}()

	select {
	case <-inputDone:
		return false, nil
	case <-remoteDone:
		// The reader is still waiting for a key. Take that key here, so it
		// is not stolen from whatever reads the terminal next.
		_, _ = io.WriteString(out, "\r\n[session ended - press any key]")
		<-inputDone
		return true, nil
	}
}
