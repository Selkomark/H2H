package workspace

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

var testEntries = []IgnoreEntry{
	{Pattern: "/.h2h", Sample: ".h2h/"},
	{Pattern: "/h2h.toml.*.tmp", Sample: "h2h.toml.0.tmp"},
}

const testComment = "# H2H"

func readIgnore(t *testing.T, root string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, IgnoreFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func writeIgnore(t *testing.T, root, text string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, IgnoreFile), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestIgnoreCreatesTheFile(t *testing.T) {
	root := newRepo(t)
	added, err := Ignore(context.Background(), root, testComment, testEntries)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"/.h2h", "/h2h.toml.*.tmp"}; !slices.Equal(added, want) {
		t.Errorf("added = %q, want %q", added, want)
	}
	if got, want := readIgnore(t, root), "# H2H\n/.h2h\n/h2h.toml.*.tmp\n"; got != want {
		t.Errorf(".gitignore = %q, want %q", got, want)
	}
	fi, err := os.Stat(filepath.Join(root, IgnoreFile))
	if err != nil || fi.Mode().Perm() != 0o644 {
		t.Errorf("mode = %v, %v; want 0644", fi.Mode().Perm(), err)
	}
}

func TestIgnoreAppendsAndLeavesExistingLinesAlone(t *testing.T) {
	tests := []struct {
		name, before, want string
	}{
		{"with a final newline", "node_modules/\n*.log\n", "node_modules/\n*.log\n\n# H2H\n/.h2h\n/h2h.toml.*.tmp\n"},
		{"without a final newline", "node_modules/\n*.log", "node_modules/\n*.log\n\n# H2H\n/.h2h\n/h2h.toml.*.tmp\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			root := newRepo(t)
			writeIgnore(t, root, tt.before)
			if _, err := Ignore(context.Background(), root, testComment, testEntries); err != nil {
				t.Fatal(err)
			}
			if got := readIgnore(t, root); got != tt.want {
				t.Errorf(".gitignore =\n%q\nwant\n%q", got, tt.want)
			}
		})
	}
}

func TestIgnoreTwiceAddsNothing(t *testing.T) {
	root := newRepo(t)
	if _, err := Ignore(context.Background(), root, testComment, testEntries); err != nil {
		t.Fatal(err)
	}
	first := readIgnore(t, root)
	added, err := Ignore(context.Background(), root, testComment, testEntries)
	if err != nil || len(added) != 0 {
		t.Errorf("second run added %q, %v; want nothing", added, err)
	}
	if got := readIgnore(t, root); got != first {
		t.Errorf("second run changed .gitignore to %q", got)
	}
}

func TestIgnoreSkipsWhatIsAlreadyListed(t *testing.T) {
	// A project set up before the line lost its trailing slash still has
	// the older form, which covers the same directory.
	root := newRepo(t)
	writeIgnore(t, root, "/.h2h/\n")
	added, err := Ignore(context.Background(), root, testComment, testEntries)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"/h2h.toml.*.tmp"}) {
		t.Errorf("added = %q; want only the entry that was missing", added)
	}
	if got, want := readIgnore(t, root), "/.h2h/\n\n# H2H\n/h2h.toml.*.tmp\n"; got != want {
		t.Errorf(".gitignore = %q, want %q", got, want)
	}
}

func TestIgnoreSkipsWhatGitAlreadyIgnores(t *testing.T) {
	root := newRepo(t)
	// Broader patterns that already cover both entries.
	writeIgnore(t, root, ".*\n*.tmp\n")
	added, err := Ignore(context.Background(), root, testComment, testEntries)
	if err != nil || len(added) != 0 {
		t.Errorf("added = %q, %v; want nothing, git already ignores both", added, err)
	}
	if got := readIgnore(t, root); got != ".*\n*.tmp\n" {
		t.Errorf(".gitignore changed to %q", got)
	}

	// The repository's own exclude file counts as well.
	root = newRepo(t)
	if err := os.WriteFile(filepath.Join(root, ".git", "info", "exclude"), []byte(".h2h/\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	added, err = Ignore(context.Background(), root, testComment, testEntries)
	if err != nil || !slices.Equal(added, []string{"/h2h.toml.*.tmp"}) {
		t.Errorf("with .h2h/ excluded: added = %q, %v; want only the other entry", added, err)
	}
}

func TestIgnoreDoesNotCountTheStateDirectorysOwnFile(t *testing.T) {
	// The state directory has a .gitignore of its own that ignores what is
	// inside it. The project's file still gets the entry, so the directory
	// itself is ignored and anyone reading .gitignore can see why.
	root := newRepo(t)
	if err := os.MkdirAll(filepath.Join(root, ".h2h"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, ".h2h", ".gitignore"), []byte("*\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := Ignore(context.Background(), root, testComment, testEntries)
	if err != nil || !slices.Contains(added, "/.h2h") {
		t.Errorf("added = %q, %v; want /.h2h added", added, err)
	}
}

func TestIgnoreOutsideARepositoryDoesNothing(t *testing.T) {
	dir := t.TempDir()
	added, err := Ignore(context.Background(), dir, testComment, testEntries)
	if err != nil || len(added) != 0 {
		t.Errorf("Ignore outside a repository = %q, %v; want nothing and no error", added, err)
	}
	if _, err := os.Stat(filepath.Join(dir, IgnoreFile)); !os.IsNotExist(err) {
		t.Errorf("a .gitignore was created outside a repository: %v", err)
	}
}

func TestMissingIgnoresChangesNothing(t *testing.T) {
	root := newRepo(t)
	missing, err := MissingIgnores(context.Background(), root, testEntries)
	if err != nil || len(missing) != 2 {
		t.Errorf("MissingIgnores = %v, %v; want both entries", missing, err)
	}
	if _, err := os.Stat(filepath.Join(root, IgnoreFile)); !os.IsNotExist(err) {
		t.Errorf("MissingIgnores created a .gitignore: %v", err)
	}
}

func TestIgnoreSkipsTheSameLine(t *testing.T) {
	root := newRepo(t)
	writeIgnore(t, root, "/.h2h\n")
	added, err := Ignore(context.Background(), root, testComment, testEntries)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(added, []string{"/h2h.toml.*.tmp"}) {
		t.Errorf("added = %q; want only the entry that was missing", added)
	}
}
