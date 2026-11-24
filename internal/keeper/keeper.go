// Package keeper holds one harness in a pseudo-terminal.
//
// A keeper is the h2h binary started in an internal mode, detached from
// whatever launched it, so the harness survives the console exiting. It does
// three things and nothing else: hold the terminal, write the session's
// state and log, and let one client at a time attach.
package keeper

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/harness"
	"github.com/Selkomark/H2H/internal/session"
)

// Frames a client sends to the keeper: one type byte, a two-byte big-endian
// length, then the payload. The keeper answers with raw terminal output.
const (
	frameInput  = 'i' // payload: bytes for the harness's terminal
	frameResize = 'r' // payload: rows and columns, two bytes each
)

const (
	defaultRows, defaultCols = 40, 120
	stopGrace                = 5 * time.Second
	clientWriteTimeout       = 2 * time.Second
	redrawNudge              = 150 * time.Millisecond
)

type keeper struct {
	ptmx *os.File

	mu     sync.Mutex
	client net.Conn
}

// Run starts the harness of an existing session record and blocks until the
// harness ends. It is the whole life of a keeper process.
func Run(root, id string) error {
	info, err := session.Load(root, id)
	if err != nil {
		return err
	}
	fail := func(err error) error {
		info.State, info.Error, info.Ended = session.Exited, err.Error(), time.Now()
		if saveErr := session.Save(root, info); saveErr != nil {
			return errors.Join(err, saveErr)
		}
		return err
	}

	cfg, err := config.Load(root)
	if err != nil {
		return fail(err)
	}
	_, h, err := cfg.Lookup(info.Harness)
	if err != nil {
		return fail(err)
	}

	logf, err := os.OpenFile(session.LogPath(root, id), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fail(err)
	}
	defer logf.Close()

	sock, err := session.SocketPath(root, id)
	if err != nil {
		return fail(err)
	}
	_ = os.Remove(sock) // left behind by a keeper that died
	ln, err := net.Listen("unix", sock)
	if err != nil {
		return fail(err)
	}
	defer os.Remove(sock)
	defer ln.Close()
	if err := os.Chmod(sock, 0o600); err != nil {
		return fail(err)
	}

	// The harness answers a delegated request with h2h-report, a small
	// program on its PATH that calls this binary. It has a name of its
	// own so that a harness can be allowed to run it and nothing else.
	shimDir, err := reportShim(root, id)
	if err != nil {
		return fail(err)
	}
	args := append(append([]string{}, h.Args...), h.ReportArgs...)
	for _, a := range h.PermitArgs {
		// The path goes inside a quoted string of JSON or TOML, so what
		// those would read as an escape is escaped.
		quoted := strings.Trim(strconv.Quote(PermitPath(root, id)), `"`)
		args = append(args, strings.ReplaceAll(a, config.PermitPlaceholder, quoted))
	}
	extraEnv := []string{"H2H_SESSION=" + id, "H2H_ROOT=" + root, "PATH=" + shimDir + string(os.PathListSeparator) + os.Getenv("PATH")}
	if info.Domain != "" && h.PassesRoles() {
		roles := cfg.RolesXML(info.Domain)
		// The roles file lives in the session's state directory, never in
		// the workspace, so nothing H2H writes ends up in the project.
		rolesDir := filepath.Join(session.Dir(root, id), "roles")
		if len(h.ContextEnv) > 0 {
			if err := os.MkdirAll(rolesDir, 0o700); err != nil {
				return fail(err)
			}
			if err := os.WriteFile(filepath.Join(rolesDir, harness.RolesFile), []byte(roles), 0o600); err != nil {
				return fail(err)
			}
		}
		ctxArgs, ctxEnv := harness.Context(h, roles, rolesDir)
		args = append(args, ctxArgs...)
		extraEnv = append(extraEnv, ctxEnv...)
	}
	cmd := exec.Command(h.Command, args...)
	cmd.Dir = info.Workspace
	cmd.Env = harness.Env(h, os.Environ(), extraEnv...)
	ptmx, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: defaultRows, Cols: defaultCols})
	if err != nil {
		return fail(fmt.Errorf("starting %s: %w", h.Command, err))
	}
	defer ptmx.Close()

	info.State, info.Socket = session.Running, sock
	info.KeeperPID, info.HarnessPID = os.Getpid(), cmd.Process.Pid
	if err := session.Save(root, info); err != nil {
		stop(cmd.Process.Pid)
		return err
	}

	k := &keeper{ptmx: ptmx}
	go k.accept(ln)
	exited := make(chan struct{})
	go forwardStop(cmd.Process.Pid, exited)

	k.pump(logf) // returns when the harness closes its terminal
	waitErr := cmd.Wait()
	close(exited)
	k.drop(nil)

	code := cmd.ProcessState.ExitCode()
	info.State, info.ExitCode, info.Ended = session.Exited, &code, time.Now()
	var exitErr *exec.ExitError
	if waitErr != nil && !errors.As(waitErr, &exitErr) {
		info.Error = waitErr.Error()
	}
	return session.Save(root, info)
}

// pump copies the harness's terminal output to the log and to the attached
// client, until the terminal closes.
func (k *keeper) pump(logf io.Writer) {
	buf := make([]byte, 32*1024)
	for {
		n, err := k.ptmx.Read(buf)
		if n > 0 {
			_, _ = logf.Write(buf[:n])
			k.mu.Lock()
			c := k.client
			k.mu.Unlock()
			if c != nil {
				// A stalled client must never block the harness.
				_ = c.SetWriteDeadline(time.Now().Add(clientWriteTimeout))
				if _, werr := c.Write(buf[:n]); werr != nil {
					k.drop(c)
				}
			}
		}
		if err != nil {
			return
		}
	}
}

// accept admits one client at a time: a session has one driver.
func (k *keeper) accept(ln net.Listener) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		k.mu.Lock()
		busy := k.client != nil
		if !busy {
			k.client = conn
		}
		k.mu.Unlock()
		if busy {
			_, _ = conn.Write([]byte("h2h: this session is already attached elsewhere\r\n"))
			conn.Close()
			continue
		}
		go k.serve(conn)
	}
}

// serve applies the frames of one attached client.
func (k *keeper) serve(conn net.Conn) {
	defer k.drop(conn)
	var head [3]byte
	for {
		if _, err := io.ReadFull(conn, head[:]); err != nil {
			return
		}
		payload := make([]byte, binary.BigEndian.Uint16(head[1:]))
		if _, err := io.ReadFull(conn, payload); err != nil {
			return
		}
		switch head[0] {
		case frameInput:
			if _, err := k.ptmx.Write(payload); err != nil {
				return
			}
		case frameResize:
			if len(payload) != 4 {
				return
			}
			rows, cols := binary.BigEndian.Uint16(payload), binary.BigEndian.Uint16(payload[2:])
			if rows == 0 || cols == 0 {
				continue
			}
			// A harness redraws only when it sees its size change, and a
			// newly attached client needs a full screen even when its
			// terminal matches the last one. So an unchanged size is first
			// set one column narrower, and held there long enough for the
			// harness to notice: two changes in quick succession reach it
			// as one signal, after which it finds the size it already had.
			if cur, err := pty.GetsizeFull(k.ptmx); err == nil && cur.Rows == rows && cur.Cols == cols && cols > 1 {
				_ = pty.Setsize(k.ptmx, &pty.Winsize{Rows: rows, Cols: cols - 1})
				time.Sleep(redrawNudge)
			}
			_ = pty.Setsize(k.ptmx, &pty.Winsize{Rows: rows, Cols: cols})
		default:
			return
		}
	}
}

// drop closes the attached client. With a non-nil conn it only acts if conn
// is still the attached one.
func (k *keeper) drop(conn net.Conn) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.client == nil || (conn != nil && k.client != conn) {
		if conn != nil {
			conn.Close()
		}
		return
	}
	k.client.Close()
	k.client = nil
}

// forwardStop ends the harness when the keeper is asked to stop. It gives
// up once the harness has exited, so a late signal never reaches a process
// that has since taken over the harness's ID.
func forwardStop(pid int, exited <-chan struct{}) {
	signal.Ignore(syscall.SIGHUP)
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	defer signal.Stop(sig)
	select {
	case <-sig:
		stop(pid)
	case <-exited:
	}
}

// stop asks the harness and everything it started to end, and insists after
// a grace period. The harness leads its own process group.
func stop(pid int) {
	_ = syscall.Kill(-pid, syscall.SIGTERM)
	time.AfterFunc(stopGrace, func() { _ = syscall.Kill(-pid, syscall.SIGKILL) })
}

// ReportCommand is the name of the program a harness reports with.
const ReportCommand = "h2h-report"

// ReportPath is where a session's ReportCommand is, for a harness whose
// shell does not keep the PATH it was started with.
func ReportPath(root, id string) string {
	return filepath.Join(session.Dir(root, id), "bin", ReportCommand)
}

// reportShim writes ReportCommand into the session's state directory and
// returns the directory, to be put on the harness's PATH. The program
// names its session and project itself, and does not rely on the
// environment: a harness may run its commands in a fresh shell.
func reportShim(root, id string) (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	path := ReportPath(root, id)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return "", err
	}
	for name, command := range map[string]string{ReportCommand: "report", PermitCommand: "permit"} {
		script := "#!/bin/sh\nH2H_ROOT=" + shellQuote(root) + " H2H_SESSION=" + shellQuote(id) + " exec " + shellQuote(exe) + " " + command + " \"$@\"\n"
		if err := os.WriteFile(filepath.Join(filepath.Dir(path), name), []byte(script), 0o700); err != nil {
			return "", err
		}
	}
	return filepath.Dir(path), nil
}

// PermitCommand is the name of the program a harness's permission hook
// runs to ask H2H.
const PermitCommand = "h2h-permit"

// PermitPath is where a session's PermitCommand is.
func PermitPath(root, id string) string {
	return filepath.Join(session.Dir(root, id), "bin", PermitCommand)
}

// shellQuote quotes s as one word for sh.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
