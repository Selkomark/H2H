// Package config reads and validates the H2H configuration file.
//
// The file is the single source of truth for which harnesses exist and which
// one is the default. The CLI and the console are views onto it.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// FileName is the name of the config file at the project root.
const FileName = "h2h.toml"

//go:embed default.toml
var defaultTOML string

var (
	nameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)
	envRE  = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)
)

// Config is the declared setup of a project.
type Config struct {
	DefaultHarness string             `toml:"default_harness"`
	Harnesses      map[string]Harness `toml:"harness"`
}

// Harness says how to start one harness product.
type Harness struct {
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	AskArgs []string `toml:"ask_args"`
	PassEnv []string `toml:"pass_env"`
}

// Template returns the text of the config H2H ships with.
func Template() string { return defaultTOML }

// Path returns where the config file of the project at root lives.
func Path(root string) string { return filepath.Join(root, FileName) }

// Parse decodes and validates config text.
func Parse(text string) (Config, error) {
	var c Config
	md, err := toml.Decode(text, &c)
	if err != nil {
		return Config{}, err
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return Config{}, fmt.Errorf("unknown setting: %s", strings.Join(keys, ", "))
	}
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// Load reads the project's config file. A project without one gets the
// shipped defaults.
func Load(root string) (Config, error) {
	text, err := os.ReadFile(Path(root))
	if errors.Is(err, fs.ErrNotExist) {
		return Parse(defaultTOML)
	}
	if err != nil {
		return Config{}, err
	}
	c, err := Parse(string(text))
	if err != nil {
		return Config{}, fmt.Errorf("%s: %w", FileName, err)
	}
	return c, nil
}

// Lookup returns the harness called name, or the default harness when name
// is empty. The returned name is the one that was resolved.
func (c Config) Lookup(name string) (string, Harness, error) {
	if name == "" {
		name = c.DefaultHarness
	}
	h, ok := c.Harnesses[name]
	if !ok {
		return "", Harness{}, fmt.Errorf("no harness named %q (configured: %s)", name, strings.Join(c.Names(), ", "))
	}
	return name, h, nil
}

// Names returns the configured harness names in order.
func (c Config) Names() []string {
	names := make([]string, 0, len(c.Harnesses))
	for n := range c.Harnesses {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

func (c Config) validate() error {
	if len(c.Harnesses) == 0 {
		return errors.New("no harness is configured")
	}
	for _, name := range c.Names() {
		h := c.Harnesses[name]
		if !nameRE.MatchString(name) {
			return fmt.Errorf("harness name %q: use lower-case letters, digits and hyphens", name)
		}
		if h.Command == "" {
			return fmt.Errorf("harness %q has no command", name)
		}
		for _, v := range h.PassEnv {
			if !envRE.MatchString(v) {
				return fmt.Errorf("harness %q: %q is not an environment variable name", name, v)
			}
		}
	}
	if c.DefaultHarness == "" {
		return errors.New("default_harness is not set")
	}
	if _, ok := c.Harnesses[c.DefaultHarness]; !ok {
		return fmt.Errorf("default_harness %q is not a configured harness (configured: %s)", c.DefaultHarness, strings.Join(c.Names(), ", "))
	}
	return nil
}
