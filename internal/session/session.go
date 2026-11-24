// Package session models a supervised harness session and keeps its record
// in the project's state directory.
//
// Every command reads sessions from disk, so listing and logs keep working
// whichever process started a session.
package session

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"
)

// State is where a session is in its life.
type State string

const (
	Starting State = "starting" // record written, keeper not up yet
	Running  State = "running"  // keeper holds a live harness
	Exited   State = "exited"   // the harness ended, or never started
	Lost     State = "lost"     // the record says running but its keeper is gone
)

// startGrace is how long a session may stay in Starting before it counts as lost.
const startGrace = 15 * time.Second

// Info is the on-disk record of one session.
type Info struct {
	ID         string    `json:"id"`
	Harness    string    `json:"harness"`
	Domain     string    `json:"domain,omitempty"`
	Workspace  string    `json:"workspace"`
	Socket     string    `json:"socket,omitempty"`
	KeeperPID  int       `json:"keeper_pid,omitempty"`
	HarnessPID int       `json:"harness_pid,omitempty"`
	State      State     `json:"state"`
	ExitCode   *int      `json:"exit_code,omitempty"`
	Error      string    `json:"error,omitempty"`
	Started    time.Time `json:"started"`
	Ended      time.Time `json:"ended,omitzero"`
}

// Live reports whether the session has, or is about to have, a running harness.
func (i Info) Live() bool { return i.State == Starting || i.State == Running }

var idRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

// ValidID reports whether id is safe to use as a session ID. IDs become
// directory names, so anything else is refused.
func ValidID(id string) bool { return idRE.MatchString(id) }

// StateDir is the directory under the project root that holds H2H's runtime state.
func StateDir(root string) string { return filepath.Join(root, ".h2h") }

// Dir is the directory holding the record and logs of one session.
func Dir(root, id string) string { return filepath.Join(StateDir(root), "sessions", id) }

// LogPath is the file the session's terminal output is written to.
func LogPath(root, id string) string { return filepath.Join(Dir(root, id), "terminal.log") }

func infoPath(root, id string) string { return filepath.Join(Dir(root, id), "session.json") }

// Init creates the state directory, private to the user and ignored by git.
func Init(root string) error {
	dir := StateDir(root)
	if err := os.MkdirAll(filepath.Join(dir, "sessions"), 0o700); err != nil {
		return err
	}
	ignore := filepath.Join(dir, ".gitignore")
	if _, err := os.Stat(ignore); errors.Is(err, fs.ErrNotExist) {
		return os.WriteFile(ignore, []byte("*\n"), 0o600)
	}
	return nil
}

// Writable reports whether H2H can keep its state in the project. It
// leaves nothing behind, and does not create the state directory.
func Writable(root string) error {
	dir := StateDir(root)
	if _, err := os.Stat(dir); errors.Is(err, fs.ErrNotExist) {
		dir = root // where the state directory would be created
	}
	f, err := os.CreateTemp(dir, ".h2h-check-*")
	if err != nil {
		return err
	}
	f.Close()
	return os.Remove(f.Name())
}

// Create reserves a new session ID for a harness and writes its first record.
//
// IDs are the harness name and a number. A number is never used twice, even
// after its session is removed, so an ID always means the same session in
// logs, in the user's memory and on screen.
func Create(root, harness string) (Info, error) {
	if err := Init(root); err != nil {
		return Info{}, err
	}
	last, err := loadCounters(root)
	if err != nil {
		return Info{}, err
	}
	for n := last[harness] + 1; n < last[harness]+10000; n++ {
		id := fmt.Sprintf("%s-%d", harness, n)
		if !ValidID(id) {
			return Info{}, fmt.Errorf("cannot make a session ID from harness name %q", harness)
		}
		err := os.Mkdir(Dir(root, id), 0o700)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return Info{}, err
		}
		if err := saveCounter(root, harness, n); err != nil {
			return Info{}, err
		}
		info := Info{ID: id, Harness: harness, State: Starting, Started: time.Now()}
		return info, Save(root, info)
	}
	return Info{}, errors.New("too many sessions")
}

func countersPath(root string) string { return filepath.Join(StateDir(root), "counters.json") }

// loadCounters reads the highest number given out so far for each harness.
func loadCounters(root string) (map[string]int, error) {
	last := map[string]int{}
	data, err := os.ReadFile(countersPath(root))
	if errors.Is(err, fs.ErrNotExist) {
		return last, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(data, &last); err != nil {
		return nil, fmt.Errorf("%s: %w", countersPath(root), err)
	}
	return last, nil
}

// saveCounter records that harness has been given number n. It rereads the
// file first and never lowers a number, so two h2h processes starting
// sessions at once cannot make one of them reusable.
func saveCounter(root, harness string, n int) error {
	last, err := loadCounters(root)
	if err != nil {
		return err
	}
	if last[harness] >= n {
		return nil
	}
	last[harness] = n
	data, err := json.Marshal(last)
	if err != nil {
		return err
	}
	tmp := fmt.Sprintf("%s.%d.tmp", countersPath(root), os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, countersPath(root))
}

// Save writes the record atomically, so a reader never sees half a file.
func Save(root string, info Info) error {
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return err
	}
	path := infoPath(root, info.ID)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads one session's record and checks it against reality: a session
// recorded as live whose keeper is gone is reported as Lost.
func Load(root, id string) (Info, error) {
	if !ValidID(id) {
		return Info{}, fmt.Errorf("%q is not a session ID", id)
	}
	data, err := os.ReadFile(infoPath(root, id))
	if errors.Is(err, fs.ErrNotExist) {
		return Info{}, fmt.Errorf("no session %q", id)
	}
	if err != nil {
		return Info{}, err
	}
	var info Info
	if err := json.Unmarshal(data, &info); err != nil {
		return Info{}, fmt.Errorf("session %s: %w", id, err)
	}
	switch {
	case info.State == Running && !alive(info.KeeperPID):
		info.State = Lost
	case info.State == Starting && time.Since(info.Started) > startGrace:
		info.State = Lost
	}
	return info, nil
}

// List returns every session of the project, oldest first.
func List(root string) ([]Info, error) {
	entries, err := os.ReadDir(filepath.Join(StateDir(root), "sessions"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var infos []Info
	for _, e := range entries {
		if !e.IsDir() || !ValidID(e.Name()) {
			continue
		}
		info, err := Load(root, e.Name())
		if err != nil {
			continue // a directory without a readable record is not a session
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(a, b int) bool { return infos[a].Started.Before(infos[b].Started) })
	return infos, nil
}

// Remove deletes a session's record, logs and reports.
func Remove(root, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not a session ID", id)
	}
	if sock, err := SocketPath(root, id); err == nil {
		_ = os.RemoveAll(strings.TrimSuffix(sock, ".sock") + ".reports")
		_ = os.RemoveAll(strings.TrimSuffix(sock, ".sock") + ".permits")
	}
	return os.RemoveAll(Dir(root, id))
}

// SocketPath returns where the keeper of a session listens.
//
// Unix socket paths are limited to about 100 bytes, which a path under a
// deep project directory can exceed. Sockets therefore live in a short
// per-user directory that only that user can enter.
func SocketPath(root, id string) (string, error) {
	if !ValidID(id) {
		return "", fmt.Errorf("%q is not a session ID", id)
	}
	uid := os.Getuid()
	dir := fmt.Sprintf("/tmp/h2h-%d", uid)
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return "", err
	}
	st, ok := fi.Sys().(*syscall.Stat_t)
	if !fi.IsDir() || fi.Mode().Perm() != 0o700 || !ok || int(st.Uid) != uid {
		return "", fmt.Errorf("%s is not a private directory owned by this user; refusing to use it", dir)
	}
	sum := sha256.Sum256([]byte(root))
	return filepath.Join(dir, fmt.Sprintf("%x-%s.sock", sum[:6], id)), nil
}

// ReportDir returns the directory in which the harness of a session
// leaves its reports, creating it if needed. It sits beside the session's
// socket, in the per-user directory under /tmp, and not in the state
// directory: a harness that confines its own commands still lets them
// write under /tmp, and may not let them write elsewhere in the project.
func ReportDir(root, id string) (string, error) {
	sock, err := SocketPath(root, id)
	if err != nil {
		return "", err
	}
	dir := strings.TrimSuffix(sock, ".sock") + ".reports"
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	return dir, nil
}

// ReportPath is the file that holds the report to request number n.
func ReportPath(dir string, n int) string { return filepath.Join(dir, fmt.Sprintf("%d.txt", n)) }

// NewRequest reserves the next request number of a session, so that the
// report to it cannot be mistaken for the report to another.
func NewRequest(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	last := 0
	for _, e := range entries {
		var n int
		if _, err := fmt.Sscanf(e.Name(), "%d.", &n); err == nil && n > last {
			last = n
		}
	}
	for n := last + 1; ; n++ {
		f, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%d.asked", n)), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		return n, f.Close()
	}
}

// Report stores the report to request n, atomically, so a reader never
// sees half of it. A request that was not made is refused.
func Report(dir string, n int, text string) error {
	if _, err := os.Stat(filepath.Join(dir, fmt.Sprintf("%d.asked", n))); err != nil {
		return fmt.Errorf("there is no request %d to report on", n)
	}
	tmp := fmt.Sprintf("%s.%d.tmp", ReportPath(dir, n), os.Getpid())
	if err := os.WriteFile(tmp, []byte(text), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, ReportPath(dir, n))
}

// PermitDir returns the directory in which a session's permission
// requests wait for their answers, creating it if needed. Like ReportDir
// it sits beside the session's socket.
func PermitDir(root, id string) (string, error) {
	sock, err := SocketPath(root, id)
	if err != nil {
		return "", err
	}
	dir := strings.TrimSuffix(sock, ".sock") + ".permits"
	if err := os.Mkdir(dir, 0o700); err != nil && !errors.Is(err, fs.ErrExist) {
		return "", err
	}
	return dir, nil
}

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
