package session

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// running returns a running session's record, with a terminal log that
// last changed at logged.
func running(t *testing.T, logged time.Time) (string, Info) {
	t.Helper()
	root := t.TempDir()
	info, err := Create(root, "fake")
	if err != nil {
		t.Fatal(err)
	}
	info.State = Running
	if err := os.WriteFile(LogPath(root, info.ID), []byte("screen"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(LogPath(root, info.ID), logged, logged); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ClearWait(root, info.ID) })
	return root, info
}

// ask makes request n of a session, as made at the given time, and reports
// on it if reported is set.
func ask(t *testing.T, root, id string, n int, at time.Time, reported bool) {
	t.Helper()
	dir, err := ReportDir(root, id)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	asked := filepath.Join(dir, strconv.Itoa(n)+".asked")
	if err := os.WriteFile(asked, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(asked, at, at); err != nil {
		t.Fatal(err)
	}
	if reported {
		if err := Report(dir, n, "done"); err != nil {
			t.Fatal(err)
		}
	}
}

func TestWaitIsRecordedAndCleared(t *testing.T) {
	now := time.Now()
	root, info := running(t, now)
	if a := attend(root, info, now); a.Kind != NoAttention {
		t.Fatalf("before any hook: %+v, want no attention", a)
	}
	if err := SetWait(root, info.ID, "Permission needed:   Edit file\n /p/a.txt"); err != nil {
		t.Fatal(err)
	}
	a := attend(root, info, now)
	if a.Kind != Waiting || a.Why != "Permission needed: Edit file /p/a.txt" {
		t.Fatalf("after wait: %+v; want waiting, with what for on one line", a)
	}
	// A second wait for the same stop keeps the time it began.
	time.Sleep(10 * time.Millisecond)
	if err := SetWait(root, info.ID, "again"); err != nil {
		t.Fatal(err)
	}
	if b := attend(root, info, now); !b.Since.Equal(a.Since) || b.Why != "again" {
		t.Errorf("second wait: %+v; want since %v kept and the new reason", b, a.Since)
	}
	// Only a running harness waits.
	stopped := info
	stopped.State = Exited
	if b := attend(root, stopped, now); b.Kind != NoAttention {
		t.Errorf("an exited session: %+v, want no attention", b)
	}
	if err := ClearWait(root, info.ID); err != nil {
		t.Fatal(err)
	}
	if err := ClearWait(root, info.ID); err != nil {
		t.Errorf("clearing twice: %v", err)
	}
	if b := attend(root, info, now); b.Kind != NoAttention {
		t.Errorf("after go: %+v, want no attention", b)
	}

	// Removing a session removes a wait it left behind.
	if err := SetWait(root, info.ID, "left behind"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, info.ID); err != nil {
		t.Fatal(err)
	}
	if path, _ := waitPath(root, info.ID); fileExists(path) {
		t.Errorf("the wait record %s is still there after Remove", path)
	}
}

func fileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func TestQuietOnlyWithAnOpenRequestAndASilentTerminal(t *testing.T) {
	now := time.Now()
	long := now.Add(-QuietAfter - time.Second)
	recent := now.Add(-QuietAfter / 2)

	// Silent, but nothing was asked of it: it is idle, not waiting.
	root, info := running(t, long)
	if a := attend(root, info, now); a.Kind != NoAttention {
		t.Errorf("silent with no request: %+v, want no attention", a)
	}

	// A request open, and silent long enough.
	ask(t, root, info.ID, 1, long, false)
	if a := attend(root, info, now); a.Kind != Quiet {
		t.Errorf("silent with a request open: %+v, want quiet", a)
	}

	// Output while the request is open: it is at work.
	root, info = running(t, recent)
	ask(t, root, info.ID, 1, long, false)
	if a := attend(root, info, now); a.Kind != NoAttention {
		t.Errorf("output %s ago: %+v, want no attention", QuietAfter/2, a)
	}

	// Asked only just now: give it time to start.
	root, info = running(t, long)
	ask(t, root, info.ID, 1, recent, false)
	if a := attend(root, info, now); a.Kind != NoAttention {
		t.Errorf("asked %s ago: %+v, want no attention", QuietAfter/2, a)
	}

	// Reported: nothing is open.
	root, info = running(t, long)
	ask(t, root, info.ID, 1, long, true)
	if a := attend(root, info, now); a.Kind != NoAttention {
		t.Errorf("request reported: %+v, want no attention", a)
	}

	// The hook outranks the silence.
	root, info = running(t, long)
	ask(t, root, info.ID, 1, long, false)
	if err := SetWait(root, info.ID, "Permission needed"); err != nil {
		t.Fatal(err)
	}
	if a := attend(root, info, now); a.Kind != Waiting {
		t.Errorf("hook and silence: %+v, want waiting", a)
	}
}

func TestLoadWorksOutAttention(t *testing.T) {
	root, info := running(t, time.Now())
	info.KeeperPID = os.Getpid() // a live keeper, so the session stays running
	if err := Save(root, info); err != nil {
		t.Fatal(err)
	}
	if err := SetWait(root, info.ID, "Permission needed"); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root, info.ID)
	if err != nil || got.Attention.Kind != Waiting {
		t.Errorf("Load = %+v, %v; want the session waiting", got.Attention, err)
	}
}
