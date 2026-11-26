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

// askConfirmed makes request n of a session, made at the given time, for a
// harness whose hooks say when it takes a request.
func askConfirmed(t *testing.T, root, id string, n int, at time.Time) string {
	t.Helper()
	ask(t, root, id, n, at, false)
	dir, err := ReportDir(root, id)
	if err != nil {
		t.Fatal(err)
	}
	asked := filepath.Join(dir, strconv.Itoa(n)+".asked")
	if err := os.WriteFile(asked, []byte(confirmMark+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(asked, at, at); err != nil {
		t.Fatal(err)
	}
	return dir
}

// A request typed into a harness whose hooks confirm a submitted prompt,
// and never confirmed, did not arrive: H2H says so instead of that the
// harness may be waiting.
func TestNotTakenWhenTheHookNeverConfirms(t *testing.T) {
	now := time.Now()
	silent := now.Add(-TakeAfter - time.Second)
	drawing := now.Add(-TakeAfter / 4)

	// Typed, unconfirmed, and the terminal silent since.
	root, info := running(t, silent)
	askConfirmed(t, root, info.ID, 1, silent)
	if a := attend(root, info, now); a.Kind != NotTaken || a.Request != 1 {
		t.Errorf("unconfirmed and silent: %+v, want not taken, request 1", a)
	}

	// Unconfirmed, but the harness is drawing: it may be busy with earlier
	// work and hold the request until it is done.
	root, info = running(t, drawing)
	askConfirmed(t, root, info.ID, 1, silent)
	if a := attend(root, info, now); a.Kind != NoAttention {
		t.Errorf("unconfirmed while drawing: %+v, want no attention", a)
	}

	// Confirmed: it took the request, and silence means what it did.
	root, info = running(t, silent)
	askConfirmed(t, root, info.ID, 1, silent)
	if err := Took(root, info.ID, 1); err != nil {
		t.Fatal(err)
	}
	if a := attend(root, info, now); a.Kind != NoAttention {
		t.Errorf("taken and silent for less than %s: %+v, want no attention", QuietAfter, a)
	}
	long := now.Add(-QuietAfter - time.Second)
	root, info = running(t, long)
	askConfirmed(t, root, info.ID, 1, long)
	if err := Took(root, info.ID, 1); err != nil {
		t.Fatal(err)
	}
	if a := attend(root, info, now); a.Kind != Quiet {
		t.Errorf("taken and silent for %s: %+v, want quiet", QuietAfter, a)
	}

	// A harness whose hooks say nothing of prompts is never said not to
	// have taken a request.
	root, info = running(t, long)
	ask(t, root, info.ID, 1, long, false)
	if a := attend(root, info, now); a.Kind != Quiet {
		t.Errorf("unconfirmable request, silent: %+v, want quiet", a)
	}

	// A hook outranks it.
	root, info = running(t, silent)
	askConfirmed(t, root, info.ID, 1, silent)
	if err := SetWait(root, info.ID, "Permission needed"); err != nil {
		t.Fatal(err)
	}
	if a := attend(root, info, now); a.Kind != Waiting {
		t.Errorf("unconfirmed with a wait: %+v, want waiting", a)
	}
}

func TestNewRequestMarksWhatHooksConfirmAndTookIgnoresOthers(t *testing.T) {
	root, info := running(t, time.Now())
	dir, err := ReportDir(root, info.ID)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	for want, confirmed := range []bool{false, true} {
		n, err := NewRequest(dir, confirmed)
		if err != nil || n != want+1 {
			t.Fatalf("NewRequest = %d, %v; want %d", n, err, want+1)
		}
		if req, _ := openRequest(root, info.ID); req.n != n || req.confirmed != confirmed || req.taken {
			t.Errorf("request %d: %+v; want confirmed %v and not taken", n, req, confirmed)
		}
	}
	if err := Took(root, info.ID, 9); err != nil || Taken(dir, 9) {
		t.Errorf("Took for a request never made: %v, taken %v; want it ignored", err, Taken(dir, 9))
	}
	if err := Took(root, info.ID, 2); err != nil || !Taken(dir, 2) {
		t.Errorf("Took(2): %v, taken %v", err, Taken(dir, 2))
	}
	// A request number is not reused after a take.
	if n, err := NewRequest(dir, true); err != nil || n != 3 {
		t.Errorf("NewRequest after a take = %d, %v; want 3", n, err)
	}
}
