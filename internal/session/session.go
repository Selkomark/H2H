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

// Create reserves a new session ID for a harness and writes its first record.
func Create(root, harness string) (Info, error) {
	if err := Init(root); err != nil {
		return Info{}, err
	}
	for n := 1; n < 10000; n++ {
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
		info := Info{ID: id, Harness: harness, State: Starting, Started: time.Now()}
		return info, Save(root, info)
	}
	return Info{}, errors.New("too many sessions")
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

// Remove deletes a session's record and logs.
func Remove(root, id string) error {
	if !ValidID(id) {
		return fmt.Errorf("%q is not a session ID", id)
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

func alive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
}
