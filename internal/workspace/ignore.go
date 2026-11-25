package workspace

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// IgnoreFile is the file at the top of a project that tells git what to
// leave out.
const IgnoreFile = ".gitignore"

// IgnoreEntry is a line wanted in a project's .gitignore.
type IgnoreEntry struct {
	// Pattern is the line itself, such as "/.h2h".
	Pattern string
	// Sample is a path the pattern covers, relative to the project root,
	// such as ".h2h/". Git is asked about it to find out whether something
	// already ignores it.
	Sample string
}

// MissingIgnores returns the entries the project's .gitignore does not
// list and git does not already ignore by another rule. It changes
// nothing. Outside a git repository nothing is missing.
func MissingIgnores(ctx context.Context, root string, entries []IgnoreEntry) ([]IgnoreEntry, error) {
	if _, err := git(ctx, root, "rev-parse", "--show-toplevel"); err != nil {
		return nil, nil // not a repository: there is no .gitignore to keep
	}
	listed, err := ignoreLines(root)
	if err != nil {
		return nil, err
	}
	var missing []IgnoreEntry
	for _, entry := range entries {
		if listed[entry.Pattern] {
			continue
		}
		ignored, err := isIgnored(ctx, root, entry.Sample)
		if err != nil {
			return nil, err
		}
		if !ignored {
			missing = append(missing, entry)
		}
	}
	return missing, nil
}

// Ignore adds the missing entries to the project's .gitignore, under the
// comment, and returns the patterns it added. It creates the file if there
// is none, and otherwise only appends: lines already there are never
// changed or moved. It neither stages nor commits.
func Ignore(ctx context.Context, root, comment string, entries []IgnoreEntry) ([]string, error) {
	missing, err := MissingIgnores(ctx, root, entries)
	if err != nil || len(missing) == 0 {
		return nil, err
	}
	path := filepath.Join(root, IgnoreFile)
	old, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}

	var block strings.Builder
	if len(old) > 0 {
		if old[len(old)-1] != '\n' {
			block.WriteByte('\n') // end the last line the file has
		}
		block.WriteByte('\n') // and keep H2H's lines apart from it
	}
	block.WriteString(comment + "\n")
	added := make([]string, len(missing))
	for n, entry := range missing {
		block.WriteString(entry.Pattern + "\n")
		added[n] = entry.Pattern
	}

	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND|os.O_CREATE, 0o644)
	if err != nil {
		return nil, err
	}
	if _, err := f.WriteString(block.String()); err != nil {
		f.Close()
		return nil, err
	}
	return added, f.Close()
}

// ignoreLines returns the lines of the project's .gitignore, trimmed.
func ignoreLines(root string) (map[string]bool, error) {
	data, err := os.ReadFile(filepath.Join(root, IgnoreFile))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	lines := map[string]bool{}
	for _, line := range strings.Split(string(data), "\n") {
		lines[strings.TrimSpace(line)] = true
	}
	return lines, nil
}

// isIgnored asks git whether one of its rules ignores a path: the
// project's .gitignore files, .git/info/exclude or the user's global
// excludes file. The path need not exist.
//
// A rule kept inside the directory the path names does not count. H2H's
// state directory carries a .gitignore of its own that ignores everything
// in it, and that must not stand in for an entry in the project's file.
//
// This does not go through git(), because git check-ignore answers "not
// ignored" with exit status 1, which git() can only report as a failure.
func isIgnored(ctx context.Context, root, path string) (bool, error) {
	// -z, which keeps a path or pattern with a colon in it readable, needs
	// the path on standard input.
	cmd := exec.CommandContext(ctx, "git", "-C", root, "check-ignore", "--verbose", "-z", "--no-index", "--stdin")
	cmd.Stdin = strings.NewReader(path + "\x00")
	out, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case errors.As(err, &exit) && exit.ExitCode() == 1:
		return false, nil
	case errors.As(err, &exit):
		return false, fmt.Errorf("git check-ignore %s: %s", path, strings.TrimSpace(string(exit.Stderr)))
	case err != nil:
		return false, fmt.Errorf("git check-ignore %s: %w", path, err)
	}
	// With -z each match is: source, line number, pattern, path.
	fields := strings.Split(string(out), "\x00")
	if len(fields) < 3 {
		return false, fmt.Errorf("git check-ignore %s: unexpected output %q", path, out)
	}
	source, pattern := fields[0], fields[2]
	inside := strings.TrimSuffix(path, "/") + "/"
	if strings.HasPrefix(pattern, "!") || strings.HasPrefix(source, inside) {
		return false, nil // a rule that keeps the path, or the directory's own
	}
	return true, nil
}
