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
	"reflect"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/Selkomark/H2H/internal/policy"
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
	Permissions    Permissions        `toml:"permissions"`
}

// Harness says how to start one harness product.
type Harness struct {
	Command string   `toml:"command"`
	Args    []string `toml:"args"`
	AskArgs []string `toml:"ask_args"`
	// StreamArgs are used instead of AskArgs to get the answer while it is
	// being written: the harness prints JSON, one object per line.
	// StreamText is where the text is in such a line, as keys joined by
	// dots. StreamWhen, if set, is "keys.joined.by.dots=value": only lines
	// where that holds carry text. StreamWhole says each piece is a whole
	// message and not part of one, so a line break follows it.
	StreamArgs  []string `toml:"stream_args"`
	StreamText  string   `toml:"stream_text"`
	StreamWhen  string   `toml:"stream_when"`
	StreamWhole bool     `toml:"stream_whole"`
	// ConversationArgs, ResumeArgs and ConversationKey let the harness
	// carry one conversation on across runs, so the console's default
	// harness remembers what was said before. Each list replaces the
	// arguments of an answer: stream_args, or ask_args for a harness that
	// does not stream. ConversationArgs start a conversation whose ID H2H
	// chooses, and ResumeArgs continue one; {conversation} in them is the
	// ID. A harness that chooses its own ID reports it in its JSON output,
	// at ConversationKey, instead of taking ConversationArgs.
	ConversationArgs []string `toml:"conversation_args"`
	ResumeArgs       []string `toml:"resume_args"`
	ConversationKey  string   `toml:"conversation_key"`
	PassEnv          []string `toml:"pass_env"`
	// VersionArgs make the harness print its version and exit. "doctor"
	// runs them to prove the program works, not only that it is on PATH.
	VersionArgs []string `toml:"version_args"`
	// InstallCommand is the whole command, program first, that installs the
	// latest version of the harness or updates it. LatestCommand is the
	// whole command that prints the latest version there is. "install" runs
	// them; neither is run without the user choosing to.
	InstallCommand []string `toml:"install_command"`
	LatestCommand  []string `toml:"latest_command"`
	// TrustFile, TrustKey and TrustValue say where the harness records that
	// the user trusts a folder, so "doctor" can read it. The
	// file is JSON or TOML, by its extension, and a leading ~ is the home
	// directory. TrustKey is the path of keys to the record, and TrustValue
	// is what it holds when the folder is trusted: the record equals it, or
	// is a list that contains it. {project} in either is the project's
	// path. H2H only reads the file.
	TrustFile  string   `toml:"trust_file"`
	TrustKey   []string `toml:"trust_key"`
	TrustValue string   `toml:"trust_value"`
	// TrustCoversSubfolders says that trust given to a folder holds for
	// every folder inside it, repositories of their own included, so a
	// record for a folder above the project counts. Without it only a
	// record for the project itself does.
	TrustCoversSubfolders bool `toml:"trust_covers_subfolders"`
	// NoTrustQuestion says the harness starts in any folder without
	// asking whether the user trusts it, so there is nothing to check.
	NoTrustQuestion bool `toml:"no_trust_question"`
	// ContextArgs and ContextEnv tell a session started for a domain what
	// its role is. In ContextArgs, {roles} is replaced by the roles as XML.
	// In ContextEnv, entries are NAME=value and {roles_dir} is replaced by
	// a directory holding the roles as AGENTS.md, for harnesses that read
	// instructions only from files. A harness with neither is not told its
	// role. See docs/responsibility-map.md.
	ContextArgs []string `toml:"context_args"`
	ContextEnv  []string `toml:"context_env"`
	// ReportArgs are added to an interactive session so the harness may
	// run the reporting command, h2h-report, without asking the user each
	// time. A harness without them still can, after its own prompt.
	ReportArgs []string `toml:"report_args"`
	// PermitArgs are added to an interactive session so the harness asks
	// H2H, through its permission hook, before it stops to ask the user.
	// {permit} in them is the path of the program the hook runs. A
	// harness without them asks in its own terminal, as it does alone.
	PermitArgs []string `toml:"permit_args"`
	// StateArgs, StateEnv and StateFiles give an interactive session the
	// harness's own hooks that tell H2H when it stops to wait for the user
	// and when it carries on. A hook runs {state} with "wait" or "go"; see
	// docs/runtime.md. StateFiles are written, with {state} filled in, into
	// a directory of the session's own that {state_dir} names, for a
	// harness that reads hooks only from files, such as a plugin. They go
	// in H2H's state directory, never in the workspace.
	StateArgs  []string          `toml:"state_args"`
	StateEnv   []string          `toml:"state_env"`
	StateFiles map[string]string `toml:"state_files"`
	// NoWaitSignal says the harness has no hook that tells H2H it waits for
	// the user. H2H then goes by a silent terminal while a request is open.
	NoWaitSignal bool `toml:"no_wait_signal"`
}

// Permissions say what a harness may do without stopping to ask. They
// apply to harnesses that have permit_args. See docs/permissions.md.
type Permissions struct {
	// Mode is what happens to a request no rule covers: "ask" puts it to
	// the user in the H2H console, "auto" allows it. Empty means "ask".
	Mode string `toml:"mode"`
	// Allow and Deny are patterns for what may run without asking and
	// what is always refused. Deny wins. A * stands for any text.
	Allow []string `toml:"allow"`
	Deny  []string `toml:"deny"`
}

// Rules returns the permissions as the policy package takes them.
func (p Permissions) Rules() policy.Rules {
	return policy.Rules{Mode: p.Mode, Allow: p.Allow, Deny: p.Deny}
}

// PermitPlaceholder stands for the hook program in a harness's permit_args.
const PermitPlaceholder = "{permit}"

// AsksH2H reports whether the harness asks H2H before it asks the user.
func (h Harness) AsksH2H() bool { return len(h.PermitArgs) > 0 }

// StatePlaceholder stands for the program a harness's state hooks run, and
// StateDirPlaceholder for the directory its state_files are written to.
const (
	StatePlaceholder    = "{state}"
	StateDirPlaceholder = "{state_dir}"
)

// TellsWaiting reports whether the harness tells H2H, through its hooks,
// when it stops to wait for the user.
func (h Harness) TellsWaiting() bool {
	return len(h.StateArgs) > 0 || len(h.StateEnv) > 0 || len(h.StateFiles) > 0
}

func (h Harness) validateState() error {
	if !h.TellsWaiting() {
		return nil
	}
	uses := func(placeholder string) bool {
		in := func(list []string) bool {
			return slices.ContainsFunc(list, func(a string) bool { return strings.Contains(a, placeholder) })
		}
		files := false
		for _, content := range h.StateFiles {
			files = files || strings.Contains(content, placeholder)
		}
		return in(h.StateArgs) || in(h.StateEnv) || files
	}
	if !uses(StatePlaceholder) {
		return fmt.Errorf("state_args, state_env or state_files must contain %s, the program its hooks run", StatePlaceholder)
	}
	if len(h.StateFiles) > 0 && !slices.ContainsFunc(append(append([]string{}, h.StateArgs...), h.StateEnv...), func(a string) bool { return strings.Contains(a, StateDirPlaceholder) }) {
		return fmt.Errorf("state_files are written to %s, so state_args or state_env must name it", StateDirPlaceholder)
	}
	for name := range h.StateFiles {
		if name == "" || filepath.IsAbs(name) || filepath.Clean(name) != name || name == ".." || strings.HasPrefix(name, "../") {
			return fmt.Errorf("state_files: %q must be a plain path inside the state directory", name)
		}
	}
	for _, kv := range h.StateEnv {
		if name, _, ok := strings.Cut(kv, "="); !ok || !envRE.MatchString(name) {
			return fmt.Errorf("state_env entry %q is not NAME=value", kv)
		}
	}
	if h.NoWaitSignal {
		return errors.New("no_wait_signal says the harness has no state hooks, but it has state settings")
	}
	return nil
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

// Lacks returns the settings, by their names in the file, that shipped
// has and h does not, each with its shipped value as it is written in the
// file. A config written by an earlier version of H2H keeps working
// without them, and silently goes without what they switch on.
func (h Harness) Lacks(shipped Harness) []string {
	var lines []string
	mine, theirs := reflect.ValueOf(h), reflect.ValueOf(shipped)
	for i := 0; i < mine.NumField(); i++ {
		if !unset(mine.Field(i)) || unset(theirs.Field(i)) {
			continue
		}
		name := mine.Type().Field(i).Tag.Get("toml")
		switch v := theirs.Field(i).Interface().(type) {
		case string:
			lines = append(lines, name+" = "+strconv.Quote(v))
		case bool:
			lines = append(lines, name+" = "+strconv.FormatBool(v))
		case []string:
			quoted := make([]string, len(v))
			for n, s := range v {
				quoted[n] = strconv.Quote(s)
			}
			lines = append(lines, name+" = ["+strings.Join(quoted, ", ")+"]")
		}
	}
	return lines
}

// Template returns the text of the config H2H ships with.
func Template() string { return defaultTOML }

// Path returns where the config file of the project at root lives.
func Path(root string) string { return filepath.Join(root, FileName) }

// Parse decodes and validates config text.
func Parse(text string) (Config, error) {
	c, md, err := decode(text)
	if err != nil {
		return Config{}, err
	}
	c.inherit(md, shippedHarnesses())
	if err := c.validate(); err != nil {
		return Config{}, err
	}
	return c, nil
}

// decode reads config text without filling anything in or checking it.
func decode(text string) (Config, toml.MetaData, error) {
	var c Config
	md, err := toml.Decode(text, &c)
	if err != nil {
		return Config{}, md, err
	}
	if extra := md.Undecoded(); len(extra) > 0 {
		keys := make([]string, len(extra))
		for i, k := range extra {
			keys[i] = k.String()
		}
		return Config{}, md, fmt.Errorf("unknown setting: %s", strings.Join(keys, ", "))
	}
	return c, md, nil
}

// shippedHarnesses returns the harness entries H2H ships with, as written,
// with nothing inherited.
func shippedHarnesses() map[string]Harness {
	c, _, _ := decode(defaultTOML) // a test keeps the shipped config valid
	return c.Harnesses
}

// conversationSettings are filled in together or not at all: a harness
// either names its conversations or reports their IDs, and mixing a
// project's choice with the shipped one would leave it with both.
var conversationSettings = []string{"conversation_args", "resume_args", "conversation_key"}

// inherit fills in, for each harness that has the name and program of a
// shipped one, every setting the file does not name with the shipped value.
// A project's entry then says only how it differs, and a project written
// for an earlier H2H gets what later versions add without being edited. A
// setting the file names, even as an empty list, is kept as written.
func (c *Config) inherit(md toml.MetaData, shipped map[string]Harness) {
	for name, h := range c.Harnesses {
		base, ok := shipped[name]
		if !ok || base.Command != h.Command {
			continue // a harness H2H does not ship, or another program under its name
		}
		named := func(key string) bool { return md.IsDefined("harness", name, key) }
		conversationNamed := false
		for _, key := range conversationSettings {
			conversationNamed = conversationNamed || named(key)
		}
		mine, theirs := reflect.ValueOf(&h).Elem(), reflect.ValueOf(base)
		for i := 0; i < mine.NumField(); i++ {
			key := mine.Type().Field(i).Tag.Get("toml")
			if named(key) || (conversationNamed && slices.Contains(conversationSettings, key)) {
				continue
			}
			mine.Field(i).Set(theirs.Field(i))
		}
		c.Harnesses[name] = h
	}
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
		// A config that only fails because it was written for an earlier
		// H2H is one "doctor" can put right, and the error says so.
		if next, changes := Upgrade(string(text)); len(changes) > 0 {
			if _, upgraded := Parse(next); upgraded == nil {
				return Config{}, fmt.Errorf("%s: %w; it was written for an earlier H2H, and doctor can bring it up to date", FileName, err)
			}
		}
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
		for _, argv := range [][]string{h.InstallCommand, h.LatestCommand} {
			if len(argv) > 0 && strings.TrimSpace(argv[0]) == "" {
				return fmt.Errorf("harness %q: install_command and latest_command must start with the program to run", name)
			}
		}
		if h.AsksH2H() && !slices.ContainsFunc(h.PermitArgs, func(a string) bool { return strings.Contains(a, PermitPlaceholder) }) {
			return fmt.Errorf("harness %q: permit_args must contain %s where the hook program goes", name, PermitPlaceholder)
		}
		if err := h.validateStream(); err != nil {
			return fmt.Errorf("harness %q: %w", name, err)
		}
		if err := h.validateConversation(); err != nil {
			return fmt.Errorf("harness %q: %w", name, err)
		}
		if err := h.validateTrust(); err != nil {
			return fmt.Errorf("harness %q: %w", name, err)
		}
		if err := h.validateContext(); err != nil {
			return fmt.Errorf("harness %q: %w", name, err)
		}
		if err := h.validateState(); err != nil {
			return fmt.Errorf("harness %q: %w", name, err)
		}
	}
	if m := c.Permissions.Mode; m != "" && m != policy.ModeAsk && m != policy.ModeAuto {
		return fmt.Errorf("permissions: mode %q is not %q or %q", m, policy.ModeAsk, policy.ModeAuto)
	}
	for _, p := range slices.Concat(c.Permissions.Allow, c.Permissions.Deny) {
		if strings.TrimSpace(p) == "" {
			return errors.New("permissions: an empty pattern would match nothing; remove it")
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

// Streams reports whether the harness can give its answer while it is
// being written.
func (h Harness) Streams() bool { return len(h.StreamArgs) > 0 }

func (h Harness) validateStream() error {
	switch {
	case !h.Streams() && (h.StreamText != "" || h.StreamWhen != "" || h.StreamWhole):
		return errors.New("stream_text, stream_when and stream_whole need stream_args")
	case h.Streams() && h.StreamText == "":
		return errors.New("stream_args needs stream_text, which says where the text is in a line of output")
	case h.StreamWhen != "" && !strings.Contains(h.StreamWhen, "="):
		return fmt.Errorf("stream_when %q is not keys=value", h.StreamWhen)
	}
	return nil
}

// ConversationPlaceholder stands for a conversation's ID in a harness's
// conversation_args and resume_args.
const ConversationPlaceholder = "{conversation}"

// Remembers reports whether the harness can carry a conversation on from
// one run to the next.
func (h Harness) Remembers() bool { return len(h.ResumeArgs) > 0 }

// NamesConversation reports whether H2H chooses the ID of a new
// conversation, as opposed to the harness reporting the one it chose.
func (h Harness) NamesConversation() bool { return len(h.ConversationArgs) > 0 }

func (h Harness) validateConversation() error {
	has := func(args []string) bool {
		return slices.ContainsFunc(args, func(a string) bool { return strings.Contains(a, ConversationPlaceholder) })
	}
	switch {
	case !h.Remembers() && (h.NamesConversation() || h.ConversationKey != ""):
		return errors.New("conversation_args and conversation_key need resume_args, which continue the conversation")
	case !h.Remembers():
		return nil
	case !has(h.ResumeArgs):
		return fmt.Errorf("resume_args must contain %s where the conversation's ID goes", ConversationPlaceholder)
	case h.NamesConversation() == (h.ConversationKey != ""):
		return errors.New("resume_args need either conversation_args, to start a conversation with an ID H2H chooses, or conversation_key, where the harness reports the ID it chose")
	case h.NamesConversation() && !has(h.ConversationArgs):
		return fmt.Errorf("conversation_args must contain %s where the conversation's ID goes", ConversationPlaceholder)
	case h.ConversationKey != "" && !h.Streams():
		return errors.New("conversation_key needs stream_args: the ID is read from the harness's JSON output")
	}
	return nil
}

// ProjectPlaceholder stands for the project's path in a harness's trust
// settings.
const ProjectPlaceholder = "{project}"

// ChecksTrust reports whether the config says where the harness records
// the folders it trusts.
func (h Harness) ChecksTrust() bool { return h.TrustFile != "" }

func (h Harness) validateTrust() error {
	if h.NoTrustQuestion && h.ChecksTrust() {
		return errors.New("no_trust_question says there is no trust record to read, so it cannot be set with trust_file")
	}
	if h.TrustFile == "" && len(h.TrustKey) == 0 && h.TrustValue == "" {
		return nil
	}
	if h.TrustFile == "" || len(h.TrustKey) == 0 || h.TrustValue == "" {
		return errors.New("trust_file, trust_key and trust_value are set together or not at all")
	}
	if ext := filepath.Ext(h.TrustFile); ext != ".json" && ext != ".toml" {
		return fmt.Errorf("trust_file %q must end in .json or .toml", h.TrustFile)
	}
	if !strings.Contains(strings.Join(h.TrustKey, "")+h.TrustValue, ProjectPlaceholder) {
		return fmt.Errorf("trust_key or trust_value must contain %s where the project's path goes", ProjectPlaceholder)
	}
	return nil
}

// The placeholders a harness's context settings may use.
const (
	RolesPlaceholder    = "{roles}"
	RolesDirPlaceholder = "{roles_dir}"
)

// PassesRoles reports whether the harness is told its role when a session
// is started for a domain.
func (h Harness) PassesRoles() bool { return len(h.ContextArgs) > 0 || len(h.ContextEnv) > 0 }

func (h Harness) validateContext() error {
	if len(h.ContextArgs) > 0 && !slices.ContainsFunc(h.ContextArgs, func(a string) bool { return strings.Contains(a, RolesPlaceholder) }) {
		return fmt.Errorf("context_args must contain %s where the roles go", RolesPlaceholder)
	}
	for _, kv := range h.ContextEnv {
		name, value, ok := strings.Cut(kv, "=")
		if !ok || !envRE.MatchString(name) {
			return fmt.Errorf("context_env entry %q is not NAME=value", kv)
		}
		if !strings.Contains(value, RolesDirPlaceholder) {
			return fmt.Errorf("context_env entry %q must contain %s", kv, RolesDirPlaceholder)
		}
	}
	return nil
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

// Shipped returns the config H2H ships with. Its harnesses are the ones
// this version of H2H supports, whatever a project's own config lists.
func Shipped() Config {
	c, _ := Parse(defaultTOML) // a test keeps the shipped config valid
	return c
}

// Unconfigured returns the names of the shipped harnesses that c does not
// have: neither under their name nor, for a harness the user renamed,
// under their program. They are the harnesses a config written for an
// earlier H2H does not know about.
func (c Config) Unconfigured() []string {
	shipped := Shipped()
	var names []string
	for _, name := range shipped.Names() {
		_, named := c.Harnesses[name]
		program := shipped.Harnesses[name].Command
		used := false
		for _, h := range c.Harnesses {
			used = used || h.Command == program
		}
		if !named && !used {
			names = append(names, name)
		}
	}
	return names
}

// ShippedEntry returns the shipped config's table for one harness, as the
// text to add to a config file, or "" when H2H ships none of that name.
func ShippedEntry(name string) string {
	var entry []string
	for _, line := range strings.Split(defaultTOML, "\n") {
		switch {
		case line == "[harness."+name+"]":
			entry = []string{line}
		case len(entry) > 0 && (strings.TrimSpace(line) == "" || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "#")):
			return strings.Join(entry, "\n")
		case len(entry) > 0:
			entry = append(entry, line)
		}
	}
	return strings.Join(entry, "\n")
}

// unset reports whether a setting was left out or given no value: an
// empty list, as "args = []" writes it, sets nothing either.
func unset(v reflect.Value) bool {
	return v.IsZero() || (v.Kind() == reflect.Slice && v.Len() == 0)
}
