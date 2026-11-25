package cli

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/Selkomark/H2H/internal/console"
)

func TestConsoleSessionsAreSavedListedAndRemoved(t *testing.T) {
	e, out := doctorEnv(t, "")
	at := time.Date(2026, 11, 24, 11, 50, 37, 0, time.Local)

	first := newConsoleSession(e.root, at)
	if first.ID != "20261124-115037" {
		t.Fatalf("ID = %q", first.ID)
	}
	first.Transcript, first.History, first.Starts, first.Updated = "> /ps\nnothing\n\n", []string{"/ps"}, []int{0}, at
	if err := saveConsoleSession(e.root, first); err != nil {
		t.Fatal(err)
	}
	// A second session started in the same second gets its own ID.
	second := newConsoleSession(e.root, at)
	if second.ID != "20261124-115037-2" {
		t.Fatalf("second ID = %q, want a number added", second.ID)
	}
	second.History, second.Updated = []string{"what is running?", "/map"}, at.Add(time.Hour)
	if err := saveConsoleSession(e.root, second); err != nil {
		t.Fatal(err)
	}
	if info, err := os.Stat(consolePath(e.root, first.ID)); err != nil || info.Mode().Perm() != 0o600 {
		t.Errorf("session file: %v, mode %v; want private to the user", err, info.Mode().Perm())
	}

	got, err := loadConsoleSession(e.root, first.ID)
	if err != nil || got.Transcript != first.Transcript || len(got.History) != 1 || len(got.Starts) != 1 {
		t.Errorf("loaded %+v, %v; want what was saved", got, err)
	}
	saved, err := listConsoleSessions(e.root)
	if err != nil || len(saved) != 2 || saved[0].ID != second.ID {
		t.Fatalf("list = %v, %v; want both, the latest used first", saved, err)
	}

	if err := e.sessions(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{first.ID, second.ID, "what is running?", "h2h --resume"} {
		if !strings.Contains(out.String(), want) {
			t.Errorf("sessions output lacks %q:\n%s", want, out)
		}
	}

	// The only session left is resumed without asking.
	if err := e.sessions(context.Background(), []string{"rm", second.ID}); err != nil {
		t.Fatal(err)
	}
	if s, ok, err := e.sessionToResume(""); err != nil || !ok || s.ID != first.ID {
		t.Errorf("sessionToResume = %v, %v, %v; want the remaining session", s.ID, ok, err)
	}
	if s, ok, err := e.sessionToResume(first.ID); err != nil || !ok || s.History[0] != "/ps" {
		t.Errorf("sessionToResume by ID = %+v, %v, %v", s, ok, err)
	}

	// Removing all asks first.
	ui := &scriptedUI{}
	e.ui = ui
	if err := e.sessions(context.Background(), []string{"rm", "all"}); err != nil {
		t.Fatal(err)
	}
	if saved, _ := listConsoleSessions(e.root); len(saved) != 1 {
		t.Errorf("rm all without a yes removed sessions: %d left", len(saved))
	}
	if err := removeConsoleSession(e.root, first.ID); err != nil {
		t.Fatal(err)
	}
	if _, _, err := e.sessionToResume(""); err == nil {
		t.Error("resuming with nothing saved succeeded, want error")
	}
}

func TestConsoleSessionIDsCannotLeaveTheirFolder(t *testing.T) {
	e, _ := doctorEnv(t, "")
	for _, id := range []string{"../session", "claude-1", "", "20261124-115037/../../x", "20261124"} {
		if _, err := loadConsoleSession(e.root, id); err == nil {
			t.Errorf("loading %q succeeded, want it refused", id)
		}
		if err := removeConsoleSession(e.root, id); err == nil {
			t.Errorf("removing %q succeeded, want it refused", id)
		}
		if err := saveConsoleSession(e.root, consoleSession{ID: id}); err == nil {
			t.Errorf("saving %q succeeded, want it refused", id)
		}
	}
	if err := e.sessions(context.Background(), []string{"delete"}); err == nil {
		t.Error("an unknown sessions argument succeeded, want usage error")
	}
}

func TestResumeSwitchesTheConsoleSession(t *testing.T) {
	e, _ := doctorEnv(t, "")
	at := time.Date(2026, 11, 24, 9, 0, 0, 0, time.Local)
	older := newConsoleSession(e.root, at)
	older.Transcript, older.History, older.Starts, older.Updated = "> /map\nthe map\n\n", []string{"/map"}, []int{0}, at
	if err := saveConsoleSession(e.root, older); err != nil {
		t.Fatal(err)
	}

	// From the shell there is no page to switch.
	if err := e.resume(context.Background(), []string{older.ID}); err == nil || !strings.Contains(err.Error(), "h2h --resume "+older.ID) {
		t.Errorf("resume from the shell = %v; want it to point at h2h --resume", err)
	}

	ui := &scriptedUI{}
	e.ui = ui
	e.here = &openSession{root: e.root, current: newConsoleSession(e.root, at.Add(time.Hour))}
	started := e.hereID()
	e.here.save(console.State{Transcript: "> /ps\n\n", History: []string{"/ps"}, Starts: []int{0}})

	for _, args := range [][]string{nil, {started}, {"20200101-000000"}, {"../x"}, {older.ID, "extra"}} {
		if err := e.resume(context.Background(), args); err == nil || ui.loaded != nil {
			t.Errorf("resume %v = %v, loaded %v; want an error and the page left alone", args, err, ui.loaded)
		}
	}

	if err := e.resume(context.Background(), []string{older.ID}); err != nil {
		t.Fatal(err)
	}
	if ui.loaded == nil || ui.loaded.Transcript != older.Transcript || ui.loaded.History[0] != "/map" || !strings.Contains(ui.note, "Resumed session "+older.ID) {
		t.Fatalf("loaded %+v with note %q; want the older session's page and a note", ui.loaded, ui.note)
	}
	if e.hereID() != older.ID {
		t.Errorf("after resume the session is %q, want %q", e.hereID(), older.ID)
	}
	// What is entered next is saved to the session switched to, and the
	// one left behind keeps what it had.
	e.here.save(console.State{Transcript: older.Transcript + "> /help\n\n", History: []string{"/map", "/help"}, Starts: []int{0, 17}})
	if s, _ := loadConsoleSession(e.root, older.ID); len(s.History) != 2 {
		t.Errorf("the resumed session has %d lines, want the new one added", len(s.History))
	}
	if s, _ := loadConsoleSession(e.root, started); len(s.History) != 1 || s.History[0] != "/ps" {
		t.Errorf("the session left behind changed: %v", s.History)
	}

	// The tab lists both without their pages and marks the one on screen.
	view := e.consoleSessionsView()
	if len(view.Rows) != 2 || view.Rows[0].ID != older.ID || !strings.Contains(view.Rows[0].Text, "this one") || strings.Contains(view.Rows[1].Text, "this one") {
		t.Errorf("view rows = %+v; want the resumed session first and marked", view.Rows)
	}
	if again := e.consoleSessionsView(); len(again.Rows) != 2 || len(e.here.index) != 2 {
		t.Errorf("a second listing gave %d rows from an index of %d", len(again.Rows), len(e.here.index))
	}

	// The session on screen cannot be removed from inside it.
	if err := e.sessions(context.Background(), []string{"rm", older.ID}); err == nil {
		t.Error("removing the session on screen succeeded, want it refused")
	}
	if err := e.sessions(context.Background(), []string{"rm", started}); err != nil {
		t.Fatal(err)
	}
	if view := e.consoleSessionsView(); len(view.Rows) != 1 {
		t.Errorf("after removing one, the view has %d rows", len(view.Rows))
	}
}

func TestCls(t *testing.T) {
	e, _ := doctorEnv(t, "")
	ui := &scriptedUI{}
	e.ui = ui
	if err := e.cls(context.Background(), nil); err == nil || ui.cleared {
		t.Errorf("cls from the shell = %v, cleared %v; want an error", err, ui.cleared)
	}
	e.here = &openSession{root: e.root, current: newConsoleSession(e.root, time.Now())}
	e.conv = e.here
	e.keepTalk(talk{Harness: "claude", ID: "conversation-1"})
	if err := e.cls(context.Background(), []string{"all"}); err == nil || ui.cleared {
		t.Errorf("cls with an argument = %v; want a usage error", err)
	}
	if err := e.cls(context.Background(), nil); err != nil || !ui.cleared {
		t.Errorf("cls in the console = %v, cleared %v", err, ui.cleared)
	}
	if got := e.currentTalk().ID; got != "conversation-1" {
		t.Errorf("after cls the conversation is %q; want it to go on", got)
	}
	for _, name := range []string{"cls", "clear"} {
		if ok, _, _ := permitted([]string{name}); ok {
			t.Errorf("the default harness may run %s", name)
		}
	}
}
