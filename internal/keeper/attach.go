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
	"time"

	"golang.org/x/term"
)

// DetachKey leaves an attached session without stopping it: Ctrl-\.
const DetachKey = 0x1c

// resetTerminal undoes what a full-screen program may have left switched
// on: a scroll region, alternate screen, hidden cursor, mouse reporting,
// bracketed paste, extended keyboard mode and colours. It ends on a clean
// last row, where the bar was.
const resetTerminal = "\x1b[r\x1b[?1049l\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?2004l\x1b[<u\x1b[0m\x1b[999;1H\x1b[2K"

// barDelay is how long the session must be quiet before the bar is drawn
// again. Drawing between two pieces of output could split a control
// sequence the harness is in the middle of sending.
const barDelay = 80 * time.Millisecond

// Attach connects the user's terminal to a session until the user presses
// DetachKey or the session ends. It reports whether the session ended.
//
// The bottom row of the terminal is kept for a bar that says how to leave:
// the harness is told the terminal is one row shorter. bar returns the
// bar's text for a given width; with a nil bar the harness gets every row.
func Attach(sock string, in *os.File, out io.Writer, bar func(width int) string) (ended bool, err error) {
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

	var omu sync.Mutex // guards out
	write := func(s string) {
		omu.Lock()
		defer omu.Unlock()
		_, _ = io.WriteString(out, s)
	}
	defer func() {
		_ = term.Restore(fd, old)
		write(resetTerminal)
	}()

	var wmu sync.Mutex // guards writes to conn
	send := func(kind byte, payload []byte) error {
		wmu.Lock()
		defer wmu.Unlock()
		head := []byte{kind, 0, 0}
		binary.BigEndian.PutUint16(head[1:], uint16(len(payload)))
		_, err := conn.Write(append(head, payload...))
		return err
	}

	// drawBar paints the bar on the last row without moving the cursor.
	drawBar := func() {
		cols, rows, err := term.GetSize(fd)
		if bar == nil || err != nil || rows < 3 {
			return
		}
		write(fmt.Sprintf("\x1b7\x1b[%d;1H\x1b[0m\x1b[2K%s\x1b[0m\x1b8", rows, bar(cols)))
	}
	// resize gives the harness the rows above the bar, and keeps its
	// scrolling out of the bar's row.
	resize := func() {
		cols, rows, err := term.GetSize(fd)
		if err != nil {
			return
		}
		if bar != nil && rows >= 3 {
			rows--
			write(fmt.Sprintf("\x1b[1;%dr\x1b[H", rows))
		}
		var p [4]byte
		binary.BigEndian.PutUint16(p[:], uint16(rows))
		binary.BigEndian.PutUint16(p[2:], uint16(cols))
		_ = send(frameResize, p[:])
		drawBar()
	}
	write("\x1b[2J\x1b[H") // a clean screen for the harness to draw on
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
		defer close(remoteDone)
		// The harness may clear the screen or scroll the bar away, so it is
		// drawn again whenever the output pauses.
		redraw := time.AfterFunc(time.Hour, drawBar)
		defer redraw.Stop()
		buf := make([]byte, 32*1024)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				write(string(buf[:n]))
				redraw.Reset(barDelay)
			}
			if err != nil {
				return
			}
		}
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
		write("\r\n[session ended - press any key]")
		<-inputDone
		return true, nil
	}
}
