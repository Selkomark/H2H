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
	"slices"
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
	Domains        map[string]Domain  `toml:"domain"`
}

// Harness says how to start one harness product.
type Harness struct {
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	AskArgs []string `toml:"ask_args"`
	PassEnv []string `toml:"pass_env"`
	// ContextArgs are the arguments that give the harness extra
	// instructions at the start of a session; the instructions follow as
	// the next argument. Without them a session started for a domain is
	// not told its role.
	ContextArgs []string `toml:"context_args"`
}

// Domain is an area of responsibility, owned by one harness. Its
// sub-agents are the specialists the harness is asked to use inside it.
// H2H passes them to the harness as instructions and cannot enforce them.
type Domain struct {
	Harness   string     `toml:"harness"`
	Does      string     `toml:"does"`
	Paths     []string   `toml:"paths"`
	DependsOn []string   `toml:"depends_on"`
	Subagents []Subagent `toml:"subagent"`
}

// Subagent is one specialist role inside a domain.
type Subagent struct {
	Role string `toml:"role"`
	Does string `toml:"does"`
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

// DomainNames returns the configured domain names in order.
func (c Config) DomainNames() []string {
	names := make([]string, 0, len(c.Domains))
	for n := range c.Domains {
		names = append(names, n)
	}
	sort.Strings(names)
	return names
}

// Dependents returns the domains that depend on name, in order.
func (c Config) Dependents(name string) []string {
	var out []string
	for _, n := range c.DomainNames() {
		if slices.Contains(c.Domains[n].DependsOn, name) {
			out = append(out, n)
		}
	}
	return out
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
	return c.validateDomains()
}

func (c Config) validateDomains() error {
	for _, name := range c.DomainNames() {
		d := c.Domains[name]
		if !nameRE.MatchString(name) {
			return fmt.Errorf("domain name %q: use lower-case letters, digits and hyphens", name)
		}
		if _, ok := c.Harnesses[name]; ok {
			return fmt.Errorf("domain %q has the same name as a harness; \"start %s\" would be ambiguous", name, name)
		}
		if d.Harness == "" {
			return fmt.Errorf("domain %q has no harness", name)
		}
		if _, ok := c.Harnesses[d.Harness]; !ok {
			return fmt.Errorf("domain %q: harness %q is not configured (configured: %s)", name, d.Harness, strings.Join(c.Names(), ", "))
		}
		for _, dep := range d.DependsOn {
			if dep == name {
				return fmt.Errorf("domain %q depends on itself", name)
			}
			if _, ok := c.Domains[dep]; !ok {
				return fmt.Errorf("domain %q depends on %q, which is not a domain", name, dep)
			}
		}
		roles := map[string]bool{}
		for _, s := range d.Subagents {
			if strings.TrimSpace(s.Role) == "" {
				return fmt.Errorf("domain %q has a sub-agent with no role", name)
			}
			if roles[s.Role] {
				return fmt.Errorf("domain %q has the sub-agent role %q twice", name, s.Role)
			}
			roles[s.Role] = true
		}
	}
	if cycle := c.dependencyCycle(); cycle != nil {
		return fmt.Errorf("domains depend on each other in a circle: %s", strings.Join(cycle, " -> "))
	}
	return nil
}

// dependencyCycle returns a circle of depends_on links, or nil when there
// is none. Work could never start in a domain on such a circle.
func (c Config) dependencyCycle() []string {
	const (
		unseen = iota
		open
		done
	)
	mark := map[string]int{}
	var path []string
	var visit func(string) []string
	visit = func(n string) []string {
		switch mark[n] {
		case open:
			i := slices.Index(path, n)
			return append(slices.Clone(path[i:]), n)
		case done:
			return nil
		}
		mark[n] = open
		path = append(path, n)
		deps := slices.Sorted(slices.Values(c.Domains[n].DependsOn))
		for _, d := range deps {
			if cycle := visit(d); cycle != nil {
				return cycle
			}
		}
		path = path[:len(path)-1]
		mark[n] = done
		return nil
	}
	for _, n := range c.DomainNames() {
		if cycle := visit(n); cycle != nil {
			return cycle
		}
	}
	return nil
}
