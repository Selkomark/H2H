package keeper

import (
	"encoding/binary"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/session"
)

// startSession runs a keeper in this process with the given shell script as
// its harness, and returns once the session is running.
func startSession(t *testing.T, script string) (root string, id string, done <-chan error) {
	t.Helper()
	root = t.TempDir()
	conf := "default_harness = \"fake\"\n[harness.fake]\ncommand = \"sh\"\nargs = [\"-c\", '''" + script + "''']\n"
	if err := os.WriteFile(config.Path(root), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := session.Create(root, "fake")
	if err != nil {
		t.Fatal(err)
	}
	info.Workspace = root
	if err := session.Save(root, info); err != nil {
		t.Fatal(err)
	}
	ch := make(chan error, 1)
	go func() { ch <- Run(root, info.ID) }()
	waitFor(t, func() bool {
		got, err := session.Load(root, info.ID)
		return err == nil && got.State != session.Starting
	})
	return root, info.ID, ch
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for condition")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func frame(kind byte, payload []byte) []byte {
	head := []byte{kind, 0, 0}
	binary.BigEndian.PutUint16(head[1:], uint16(len(payload)))
	return append(head, payload...)
}

func TestKeeperRunsLogsAndRecordsExit(t *testing.T) {
	root, id, done := startSession(t, `echo "session=$H2H_SESSION"; exit 3`)
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
	info, err := session.Load(root, id)
	if err != nil {
		t.Fatal(err)
	}
	if info.State != session.Exited || info.ExitCode == nil || *info.ExitCode != 3 {
		t.Errorf("after exit: State = %q, ExitCode = %v; want exited, 3", info.State, info.ExitCode)
	}
	log, err := os.ReadFile(session.LogPath(root, id))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(log), "session="+id) {
		t.Errorf("terminal log = %q; want it to contain the harness output", log)
	}
	if _, err := os.Stat(info.Socket); err == nil {
		t.Errorf("socket %s still exists after the session ended", info.Socket)
	}
}

func TestKeeperAttachInputResizeAndSingleDriver(t *testing.T) {
	// The harness prints its terminal size on demand and echoes a line back.
	root, id, done := startSession(t, `read line; echo "heard:$line"; stty size; read wait`)
	info, err := session.Load(root, id)
	if err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(info.Socket)
	if err != nil || fi.Mode().Perm() != 0o600 {
		t.Fatalf("socket mode = %v, %v; want 0600", fi.Mode().Perm(), err)
	}

	conn, err := net.Dial("unix", info.Socket)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()

	second, err := net.Dial("unix", info.Socket)
	if err != nil {
		t.Fatal(err)
	}
	refusal := make([]byte, 256)
	_ = second.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, _ := second.Read(refusal)
	second.Close()
	if !strings.Contains(string(refusal[:n]), "already attached") {
		t.Errorf("second client got %q; want a refusal", refusal[:n])
	}

	size := make([]byte, 4)
	binary.BigEndian.PutUint16(size, 33)
	binary.BigEndian.PutUint16(size[2:], 77)
	if _, err := conn.Write(frame(frameResize, size)); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.Write(frame(frameInput, []byte("hello\n"))); err != nil {
		t.Fatal(err)
	}

	var got strings.Builder
	buf := make([]byte, 1024)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	for !strings.Contains(got.String(), "33 77") {
		n, err := conn.Read(buf)
		if err != nil {
			t.Fatalf("reading session output: %v (got %q)", err, got.String())
		}
		got.Write(buf[:n])
	}
	if !strings.Contains(got.String(), "heard:hello") {
		t.Errorf("output = %q; want the typed line echoed by the harness", got.String())
	}

	if err := Stop(root, info); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("Run: %v", err)
	}
}

func TestKeeperRecordsStartFailure(t *testing.T) {
	root := t.TempDir()
	conf := "default_harness = \"none\"\n[harness.none]\ncommand = \"" + filepath.Join(root, "no-such-program") + "\"\n"
	if err := os.WriteFile(config.Path(root), []byte(conf), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := session.Create(root, "none")
	if err != nil {
		t.Fatal(err)
	}
	info.Workspace = root
	if err := session.Save(root, info); err != nil {
		t.Fatal(err)
	}
	if err := Run(root, info.ID); err == nil {
		t.Fatal("Run with a missing program succeeded, want error")
	}
	got, err := session.Load(root, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	if got.State != session.Exited || got.Error == "" {
		t.Errorf("after a failed start: State = %q, Error = %q; want exited with a reason", got.State, got.Error)
	}
}

func TestCheck(t *testing.T) {
	root := t.TempDir()
	if err := Check(root); err != nil {
		t.Fatalf("Check: %v", err)
	}
	sock, err := session.SocketPath(root, checkID)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(sock); err == nil {
		t.Errorf("Check left its socket behind at %s", sock)
	}
}
