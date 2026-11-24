package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestValidID(t *testing.T) {
	tests := []struct {
		id   string
		want bool
	}{
		{"claude-1", true},
		{"a", true},
		{"", false},
		{"../x", false},
		{"a/b", false},
		{"-a", false},
		{"Claude-1", false},
		{"a b", false},
		{strings.Repeat("a", 65), false},
	}
	for _, tt := range tests {
		if got := ValidID(tt.id); got != tt.want {
			t.Errorf("ValidID(%q) = %v, want %v", tt.id, got, tt.want)
		}
	}
}

func TestCreateSaveLoadList(t *testing.T) {
	root := t.TempDir()
	a, err := Create(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	b, err := Create(root, "claude")
	if err != nil {
		t.Fatal(err)
	}
	if a.ID != "claude-1" || b.ID != "claude-2" {
		t.Fatalf("IDs = %q, %q; want claude-1, claude-2", a.ID, b.ID)
	}

	a.State, a.KeeperPID = Running, os.Getpid()
	if err := Save(root, a); err != nil {
		t.Fatal(err)
	}
	got, err := Load(root, a.ID)
	if err != nil || got.State != Running || got.KeeperPID != os.Getpid() {
		t.Errorf("Load = %+v, %v; want running under this process", got, err)
	}

	list, err := List(root)
	if err != nil || len(list) != 2 || list[0].ID != "claude-1" {
		t.Errorf("List = %+v, %v; want two sessions, oldest first", list, err)
	}

	fi, err := os.Stat(StateDir(root))
	if err != nil || fi.Mode().Perm() != 0o700 {
		t.Errorf("state dir mode = %v, %v; want 0700", fi.Mode().Perm(), err)
	}

	if err := Remove(root, a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(root, a.ID); err == nil {
		t.Error("Load after Remove succeeded, want error")
	}
}

func TestCreateNeverReusesANumber(t *testing.T) {
	root := t.TempDir()
	var ids []string
	create := func(harness string) {
		t.Helper()
		info, err := Create(root, harness)
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, info.ID)
	}
	create("claude")  // claude-1
	create("copilot") // copilot-1
	create("claude")  // claude-2
	if err := Remove(root, "claude-1"); err != nil {
		t.Fatal(err)
	}
	create("claude") // must not take the freed claude-1
	if err := Remove(root, "claude-2"); err != nil {
		t.Fatal(err)
	}
	if err := Remove(root, "claude-3"); err != nil {
		t.Fatal(err)
	}
	create("claude") // nor start again from 1 when none are left
	create("copilot")

	want := "claude-1,copilot-1,claude-2,claude-3,claude-4,copilot-2"
	if got := strings.Join(ids, ","); got != want {
		t.Errorf("IDs given out = %s\nwant            %s", got, want)
	}
}

func TestCreateSkipsSessionsOlderThanTheCounter(t *testing.T) {
	// A state directory from before numbers were recorded has sessions and
	// no counter file.
	root := t.TempDir()
	if err := os.MkdirAll(Dir(root, "claude-1"), 0o700); err != nil {
		t.Fatal(err)
	}
	info, err := Create(root, "claude")
	if err != nil || info.ID != "claude-2" {
		t.Errorf("Create = %q, %v; want claude-2", info.ID, err)
	}
}

func TestLoadReportsLost(t *testing.T) {
	root := t.TempDir()
	dead, err := Create(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	dead.State, dead.KeeperPID = Running, 1<<30 // no such process
	if err := Save(root, dead); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(root, dead.ID); got.State != Lost {
		t.Errorf("running session with a dead keeper: State = %q, want %q", got.State, Lost)
	}

	stuck, err := Create(root, "x")
	if err != nil {
		t.Fatal(err)
	}
	stuck.Started = time.Now().Add(-time.Minute)
	if err := Save(root, stuck); err != nil {
		t.Fatal(err)
	}
	if got, _ := Load(root, stuck.ID); got.State != Lost {
		t.Errorf("session stuck in starting: State = %q, want %q", got.State, Lost)
	}
}

func TestLoadRejectsBadID(t *testing.T) {
	if _, err := Load(t.TempDir(), "../../etc"); err == nil {
		t.Error("Load with a path as ID succeeded, want error")
	}
}

func TestSocketPathIsShort(t *testing.T) {
	root := "/" + strings.Repeat("deep/", 60)
	p, err := SocketPath(root, "claude-1")
	if err != nil {
		t.Fatal(err)
	}
	if len(p) > 100 {
		t.Errorf("SocketPath length = %d, want at most 100: %s", len(p), p)
	}
}

func TestWritable(t *testing.T) {
	root := t.TempDir()
	if err := Writable(root); err != nil {
		t.Errorf("Writable: %v", err)
	}
	if entries, _ := os.ReadDir(root); len(entries) != 0 {
		t.Errorf("Writable left %d entries behind", len(entries))
	}
	if err := Writable(filepath.Join(root, "missing")); err == nil {
		t.Error("Writable in a missing directory succeeded, want error")
	}
}
