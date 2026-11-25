package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// A harness that stops to ask the user something, above all whether it
// may do a thing, waits in its own terminal where nobody may be looking.
// H2H learns of it in one of two ways, and shows it as the session's
// attention:
//
//   - The harness says so, through its own hooks, which run the session's
//     h2h-state program: "wait" when it stops for the user and "go" when
//     it carries on. This is certain.
//   - The harness has a request from H2H open and its terminal has been
//     silent for a while. A harness at work keeps drawing, so this is a
//     strong hint but not certain, and it is shown as such.

// Attention says whether a running session needs the user.
type Attention struct {
	Kind  AttentionKind
	Since time.Time // when it started
	Why   string    // what the harness said it waits for, if it said
}

// AttentionKind is how sure H2H is that a session waits for the user.
type AttentionKind int

const (
	// NoAttention: nothing suggests the session waits for the user.
	NoAttention AttentionKind = iota
	// Quiet: a request is open and the terminal has been silent for
	// QuietAfter. The harness may be waiting for the user.
	Quiet
	// Waiting: the harness said, through its hooks, that it waits for the
	// user.
	Waiting
)

// QuietAfter is how long a running session with a request open may be
// silent before H2H says it may be waiting for the user. A harness at work
// keeps its terminal busy, even while a slow command runs (findings.md,
// spike 01), so twenty seconds of silence is rare when it is working and
// short enough that the user is not left in the dark for long.
const QuietAfter = 20 * time.Second

// maxWhy is how much of what a harness says it waits for is kept.
const maxWhy = 300

// Wait is the record a harness's hook leaves while it waits for the user.
type Wait struct {
	Since time.Time `json:"since"`
	Why   string    `json:"why,omitempty"`
}

// waitPath is where a session's wait record is. It sits beside the
// session's socket, like its reports, where a hook may write even when the
// harness confines what it runs.
func waitPath(root, id string) (string, error) {
	sock, err := SocketPath(root, id)
	if err != nil {
		return "", err
	}
	return strings.TrimSuffix(sock, ".sock") + ".wait", nil
}

// SetWait records that a session's harness waits for the user. A wait
// already recorded keeps the time it began.
func SetWait(root, id, why string) error {
	path, err := waitPath(root, id)
	if err != nil {
		return err
	}
	w := Wait{Since: time.Now(), Why: shorten(why, maxWhy)}
	if old, ok := readWait(path); ok {
		w.Since = old.Since
	}
	data, err := json.Marshal(w)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ClearWait records that a session's harness carries on.
func ClearWait(root, id string) error {
	path, err := waitPath(root, id)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

func readWait(path string) (Wait, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Wait{}, false
	}
	var w Wait
	if json.Unmarshal(data, &w) != nil || w.Since.IsZero() {
		return Wait{}, false
	}
	return w, true
}

// attend works out whether a running session needs the user, at now.
func attend(root string, info Info, now time.Time) Attention {
	if info.State != Running {
		return Attention{}
	}
	if path, err := waitPath(root, info.ID); err == nil {
		if w, ok := readWait(path); ok {
			return Attention{Kind: Waiting, Since: w.Since, Why: w.Why}
		}
	}
	asked, open := openRequest(root, info.ID)
	if !open {
		return Attention{}
	}
	// Silent since the later of the request and the last output.
	quietSince := asked
	if fi, err := os.Stat(LogPath(root, info.ID)); err == nil && fi.ModTime().After(quietSince) {
		quietSince = fi.ModTime()
	}
	if now.Sub(quietSince) < QuietAfter {
		return Attention{}
	}
	return Attention{Kind: Quiet, Since: quietSince}
}

// openRequest returns when the latest request H2H gave a session was made,
// if it has not been reported on yet. It creates nothing.
func openRequest(root, id string) (time.Time, bool) {
	sock, err := SocketPath(root, id)
	if err != nil {
		return time.Time{}, false
	}
	dir := strings.TrimSuffix(sock, ".sock") + ".reports"
	entries, err := os.ReadDir(dir)
	if err != nil {
		return time.Time{}, false
	}
	last := 0
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "%d.asked", &n); err == nil && strings.HasSuffix(e.Name(), ".asked") && n > last {
			last = n
		}
	}
	if last == 0 {
		return time.Time{}, false
	}
	if _, err := os.Stat(ReportPath(dir, last)); err == nil {
		return time.Time{}, false // reported
	}
	fi, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%d.asked", last)))
	if err != nil {
		return time.Time{}, false
	}
	return fi.ModTime(), true
}

// shorten keeps the first line-broken words of text, up to limit runes.
func shorten(text string, limit int) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > limit {
		return string(r[:limit]) + "..."
	}
	return text
}
