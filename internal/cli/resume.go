package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/lipgloss"

	"github.com/Selkomark/H2H/internal/console"
	"github.com/Selkomark/H2H/internal/session"
)

// A console session is one sitting in the h2h console: what was on the
// page and the lines that were entered. It is saved in the project's
// state directory so the console can be closed and resumed. It is not a
// running harness, which the console lists under Harnesses.

// maxPicked is how many sessions the resume dialog offers.
const maxPicked = 15

// consoleSession is the saved record of one console session.
type consoleSession struct {
	ID         string    `json:"id"`
	Started    time.Time `json:"started"`
	Updated    time.Time `json:"updated"`
	Transcript string    `json:"transcript"`
	History    []string  `json:"history"`
	Starts     []int     `json:"starts"`
	// Talk is the session's conversation with the default harness, so
	// resuming the session continues it. Nil until the first message.
	Talk *talk `json:"conversation,omitempty"`
}

// IDs are the time the session started, with a number added when two
// start in the same second. They become file names, so anything else is
// refused.
var consoleIDRE = regexp.MustCompile(`^[0-9]{8}-[0-9]{6}(-[0-9]{1,3})?$`)

func consoleDir(root string) string { return filepath.Join(session.StateDir(root), "console") }

func consolePath(root, id string) string { return filepath.Join(consoleDir(root), id+".json") }

// newConsoleSession reserves an ID for a session that starts now. Nothing
// is written until the session has something worth keeping.
func newConsoleSession(root string, now time.Time) consoleSession {
	id := now.Format("20060102-150405")
	for n := 2; ; n++ {
		if _, err := os.Stat(consolePath(root, id)); errors.Is(err, fs.ErrNotExist) || n > 999 {
			break
		}
		id = fmt.Sprintf("%s-%d", now.Format("20060102-150405"), n)
	}
	return consoleSession{ID: id, Started: now}
}

// saveConsoleSession writes the record atomically, private to the user.
func saveConsoleSession(root string, s consoleSession) error {
	if !consoleIDRE.MatchString(s.ID) {
		return fmt.Errorf("%q is not a session ID", s.ID)
	}
	if err := session.Init(root); err != nil {
		return err
	}
	if err := os.MkdirAll(consoleDir(root), 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(s)
	if err != nil {
		return err
	}
	path := consolePath(root, s.ID)
	tmp := fmt.Sprintf("%s.%d.tmp", path, os.Getpid())
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func loadConsoleSession(root, id string) (consoleSession, error) {
	if !consoleIDRE.MatchString(id) {
		return consoleSession{}, fmt.Errorf("%q is not a session ID; list them with: sessions", id)
	}
	data, err := os.ReadFile(consolePath(root, id))
	if errors.Is(err, fs.ErrNotExist) {
		return consoleSession{}, fmt.Errorf("no saved session %q; list them with: sessions", id)
	}
	if err != nil {
		return consoleSession{}, err
	}
	var s consoleSession
	if err := json.Unmarshal(data, &s); err != nil {
		return consoleSession{}, fmt.Errorf("session %s: %w", id, err)
	}
	s.ID = id // the file name decides, whatever the file says
	return s, nil
}

// listConsoleSessions returns the saved sessions, the latest used first.
func listConsoleSessions(root string) ([]consoleSession, error) {
	entries, err := os.ReadDir(consoleDir(root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []consoleSession
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		if !ok || !consoleIDRE.MatchString(id) {
			continue
		}
		if s, err := loadConsoleSession(root, id); err == nil {
			out = append(out, s) // a file that cannot be read is not a session
		}
	}
	sort.Slice(out, func(a, b int) bool { return out[a].Updated.After(out[b].Updated) })
	return out, nil
}

func removeConsoleSession(root, id string) error {
	if !consoleIDRE.MatchString(id) {
		return fmt.Errorf("%q is not a session ID", id)
	}
	err := os.Remove(consolePath(root, id))
	if errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no saved session %q", id)
	}
	return err
}

// summary is the first line the user entered in the session, which is
// what they will know it by.
func (s consoleSession) summary() string {
	if len(s.History) == 0 {
		return ""
	}
	return s.History[0]
}

// sessionToResume finds the session "h2h --resume" should open: the one
// named, the only one there is, or the one the user picks.
func (e *env) sessionToResume(id string) (consoleSession, bool, error) {
	if id != "" {
		s, err := loadConsoleSession(e.root, id)
		return s, err == nil, err
	}
	saved, err := listConsoleSessions(e.root)
	if err != nil {
		return consoleSession{}, false, err
	}
	switch len(saved) {
	case 0:
		return consoleSession{}, false, errors.New("there is no saved session to resume in this project")
	case 1:
		return saved[0], true, nil
	}
	question := "Resume which session?"
	if len(saved) > maxPicked {
		question = fmt.Sprintf("Resume which session? These are the latest %d of %d; see all with: h2h sessions", maxPicked, len(saved))
		saved = saved[:maxPicked]
	}
	options := make([]console.Option, len(saved))
	for n, s := range saved {
		options[n] = console.Option{ID: s.ID, Label: fmt.Sprintf("%s  %-9s %s", s.Updated.Local().Format("2006-01-02 15:04"),
			plural(len(s.History), "line"), displayLine(s.summary(), 50))}
	}
	picked, ok := console.Pick(e.in, e.tty, e.theme(), question, options)
	if !ok {
		return consoleSession{}, false, nil
	}
	s, err := loadConsoleSession(e.root, picked)
	return s, err == nil, err
}

// displayLine shortens a line for a list.
func displayLine(line string, limit int) string {
	line = strings.Join(strings.Fields(line), " ")
	if r := []rune(line); len(r) > limit {
		line = string(r[:limit-3]) + "..."
	}
	return line
}

// sessions lists the saved console sessions of the project, or removes
// one or all of them.
func (e *env) sessions(_ context.Context, args []string) error {
	const usage = "usage: sessions [rm <session> | rm all]"
	saved, err := listConsoleSessions(e.root)
	if err != nil {
		return err
	}
	here := e.hereID()
	switch {
	case len(args) == 0:
		if len(saved) == 0 {
			fmt.Fprintln(e.out, e.st.dim.Render("No saved sessions. One is saved each time you use the console."))
			return nil
		}
		var rows [][]string
		for _, s := range saved {
			rows = append(rows, []string{s.ID, age(time.Since(s.Updated)) + " ago", fmt.Sprint(len(s.History)), displayLine(s.summary(), 60)})
		}
		lines := table([]string{"SESSION", "LAST USED", "LINES", "FIRST LINE"}, rows, e.st.dim, func(_, col int) lipgloss.Style {
			if col == 0 {
				return e.st.accent
			}
			return lipgloss.NewStyle()
		})
		fmt.Fprintln(e.out, strings.Join(lines, "\n"))
		fmt.Fprintln(e.out, "\n"+e.st.dim.Render("Resume one with: ")+e.st.accent.Render("h2h --resume <session>")+
			e.st.dim.Render(", or pick from a list with: ")+e.st.accent.Render("h2h --resume"))
		return nil
	case len(args) == 2 && args[0] == "rm" && args[1] == "all":
		if len(saved) == 0 {
			fmt.Fprintln(e.out, "There are no saved sessions.")
			return nil
		}
		if !e.ui.Confirm(fmt.Sprintf("Remove all %s? Running harnesses are not affected.", plural(len(saved), "saved session"))) {
			fmt.Fprintln(e.out, "Nothing was removed.")
			return nil
		}
		removed := 0
		for _, s := range saved {
			if s.ID == here {
				continue // it would be saved again when this line finishes
			}
			if err := removeConsoleSession(e.root, s.ID); err != nil {
				return err
			}
			removed++
		}
		fmt.Fprintf(e.out, "Removed %s.\n", plural(removed, "saved session"))
		if removed < len(saved) {
			fmt.Fprintln(e.out, "The session you are in was kept.")
		}
		return nil
	case len(args) == 2 && args[0] == "rm" && args[1] == here:
		return errors.New("that is the session you are in; switch to another one first, or remove it from the shell")
	case len(args) == 2 && args[0] == "rm":
		if err := removeConsoleSession(e.root, args[1]); err != nil {
			return err
		}
		fmt.Fprintf(e.out, "Removed session %s.\n", args[1])
		return nil
	}
	return errors.New(usage)
}

// openSession is the console session on screen. The console saves it from
// its own loop while a command may switch it, so it is guarded.
type openSession struct {
	root    string
	mu      sync.Mutex
	current consoleSession
	saved   bool // written at least once, so it can be resumed
	// index remembers each saved session by its file's time and size, so
	// the Sessions tab, which is redrawn every few seconds, reads a file
	// again only after it has changed.
	index map[string]indexed
	// talks counts the conversations ended from outside a message, by
	// /clear or by switching sessions. A message still being answered
	// then keeps its conversation to itself; see setTalk.
	talks int
}

// indexed is a saved session without its page, as a list shows it.
type indexed struct {
	modified time.Time
	size     int64
	session  consoleSession
}

// listed returns the saved sessions without their pages, the latest used
// first.
func (o *openSession) listed() ([]consoleSession, error) {
	entries, err := os.ReadDir(consoleDir(o.root))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	fresh := map[string]indexed{}
	var out []consoleSession
	for _, entry := range entries {
		id, ok := strings.CutSuffix(entry.Name(), ".json")
		info, err := entry.Info()
		if !ok || err != nil || !consoleIDRE.MatchString(id) {
			continue
		}
		known, ok := o.index[id]
		if !ok || !known.modified.Equal(info.ModTime()) || known.size != info.Size() {
			s, err := loadConsoleSession(o.root, id)
			if err != nil {
				continue
			}
			s.Transcript = ""
			known = indexed{modified: info.ModTime(), size: info.Size(), session: s}
		}
		fresh[id] = known
		out = append(out, known.session)
	}
	o.index = fresh
	sort.Slice(out, func(a, b int) bool { return out[a].Updated.After(out[b].Updated) })
	return out, nil
}

// save writes the session once the user has entered something, and again
// after every line, so closing the terminal loses nothing.
func (o *openSession) save(s console.State) {
	if len(s.History) == 0 {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.current.Transcript, o.current.History, o.current.Starts, o.current.Updated = s.Transcript, s.History, s.Starts, time.Now()
	if saveConsoleSession(o.root, o.current) == nil {
		o.saved = true
	}
}

// id returns the session's ID and whether it has been saved.
func (o *openSession) id() (string, bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.current.ID, o.saved
}

// switchTo makes another saved session the one on screen. The one left
// behind was saved when its last line finished.
func (o *openSession) switchTo(s consoleSession) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.current, o.saved = s, true
	o.talks++
}

// hereID is the ID of the console session a command runs in, or "" for a
// command run from the shell.
func (e *env) hereID() string {
	if e.here == nil {
		return ""
	}
	id, _ := e.here.id()
	return id
}

// resumedNote is the line that follows a resumed session's page.
func (e *env) resumedNote(s consoleSession) string {
	return e.st.dim.Render("Resumed session "+s.ID+", last used "+s.Updated.Local().Format("2006-01-02 15:04")+".") + "\n\n"
}

// resume switches the console to a saved session: its page and the lines
// entered in it replace the ones on screen.
func (e *env) resume(_ context.Context, args []string) error {
	switch {
	case len(args) > 1:
		return errors.New("usage: resume [session]")
	case e.here == nil && len(args) == 1:
		return fmt.Errorf("resume switches sessions inside the console; from the shell use: h2h --resume %s", args[0])
	case e.here == nil:
		return errors.New("resume switches sessions inside the console; from the shell use: h2h --resume")
	case len(args) == 0:
		return errors.New("which session? Pick one in the Sessions tab: /sessions")
	case args[0] == e.hereID():
		return errors.New("that is the session you are in")
	}
	s, err := loadConsoleSession(e.root, args[0])
	if err != nil {
		return err
	}
	e.here.switchTo(s)
	e.ui.Load(console.State{Transcript: s.Transcript, History: s.History, Starts: s.Starts}, e.resumedNote(s))
	return nil
}

// consoleSessionsView is the content of the console's Sessions tab: the
// saved sessions, the latest used first, with the one on screen marked.
func (e *env) consoleSessionsView() console.Table {
	saved, err := e.here.listed()
	if err != nil {
		return console.Table{Empty: e.st.bad.Render(err.Error())}
	}
	here := e.hereID()
	var rows [][]string
	for _, s := range saved {
		used := age(time.Since(s.Updated)) + " ago"
		if s.ID == here {
			used = "this one"
		}
		rows = append(rows, []string{s.ID, used, fmt.Sprint(len(s.History)), displayLine(s.summary(), 60)})
	}
	lines := table([]string{"SESSION", "LAST USED", "LINES", "FIRST LINE"}, rows, e.st.dim, func(row, col int) lipgloss.Style {
		switch {
		case col == 0:
			return e.st.accent
		case col == 1 && saved[row].ID == here:
			return e.st.good
		}
		return lipgloss.NewStyle()
	})
	t := console.Table{
		Header: lines[0],
		Empty:  e.st.dim.Render("No saved sessions yet. This one is saved once you enter a line."),
	}
	for n, s := range saved {
		t.Rows = append(t.Rows, console.Row{ID: s.ID, Text: lines[n+1]})
	}
	return t
}

// cls empties the console's page. The session stays the same one, with
// the lines entered in it still in reach of the Up key and its
// conversation with the default harness going on, and is saved that way:
// resuming it later shows the clean page, not the old one.
func (e *env) cls(_ context.Context, args []string) error {
	switch {
	case len(args) > 0:
		return errors.New("usage: cls")
	case e.here == nil:
		return errors.New("cls empties the page of the console; there is no page outside it")
	}
	e.ui.Clear()
	return nil
}
