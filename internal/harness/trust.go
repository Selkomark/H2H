package harness

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Selkomark/H2H/internal/config"
)

// TrustPath returns the file in which the harness records the folders it
// trusts, with a leading ~ replaced by home.
func TrustPath(h config.Harness, home string) string {
	if rest, ok := strings.CutPrefix(h.TrustFile, "~/"); ok {
		return filepath.Join(home, rest)
	}
	return h.TrustFile
}

// Trusted reports whether the harness's own record says the user trusts
// the project at root, so a session there starts without a trust question.
// A trusted folder covers the folders inside it, so the folders above root
// count too. H2H only reads the record: giving trust is the user's act.
func Trusted(h config.Harness, root, home string) (bool, error) {
	if !h.ChecksTrust() {
		return false, errors.New("this harness has no trust_file in the config")
	}
	path := TrustPath(h, home)
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil // the harness has never recorded anything
	}
	if err != nil {
		return false, err
	}
	var record map[string]any
	if filepath.Ext(path) == ".toml" {
		err = toml.Unmarshal(data, &record)
	} else {
		err = json.Unmarshal(withoutComments(data), &record)
	}
	if err != nil {
		return false, fmt.Errorf("%s: %w", path, err)
	}

	for dir := filepath.Clean(root); ; dir = filepath.Dir(dir) {
		key := make([]string, len(h.TrustKey))
		for i, k := range h.TrustKey {
			key[i] = strings.ReplaceAll(k, config.ProjectPlaceholder, dir)
		}
		if holds(lookup(record, key), strings.ReplaceAll(h.TrustValue, config.ProjectPlaceholder, dir)) {
			return true, nil
		}
		if dir == filepath.Dir(dir) {
			return false, nil
		}
	}
}

// withoutComments drops the lines of a JSON file that are // comments,
// which some harnesses write at the top of theirs.
func withoutComments(data []byte) []byte {
	var kept [][]byte
	for _, line := range bytes.Split(data, []byte("\n")) {
		if !bytes.HasPrefix(bytes.TrimSpace(line), []byte("//")) {
			kept = append(kept, line)
		}
	}
	return bytes.Join(kept, []byte("\n"))
}

// lookup follows key through nested tables and returns what it leads to,
// or nil.
func lookup(record map[string]any, key []string) any {
	var at any = record
	for _, k := range key {
		table, ok := at.(map[string]any)
		if !ok {
			return nil
		}
		at = table[k]
	}
	return at
}

// holds reports whether a record is want, or a list with want in it.
func holds(record any, want string) bool {
	if list, ok := record.([]any); ok {
		for _, item := range list {
			if fmt.Sprint(item) == want {
				return true
			}
		}
		return false
	}
	return record != nil && fmt.Sprint(record) == want
}
