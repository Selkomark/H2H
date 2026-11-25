package config

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"
)

// Upgrade brings the text of a config written for an earlier H2H up to
// date with this one, and says what it changed, one line per change. It
// only removes lines, so the comments and layout a person wrote stay as
// they were. A config that is already up to date comes back unchanged with
// no changes.
//
// Each step is safe to run on any config, any number of times. A change
// to the config format that an older config needs in order to keep working,
// such as a renamed setting, is added here as a step, so "doctor" can offer
// it to every project.
func Upgrade(text string) (string, []string) {
	var changes []string
	for _, step := range upgradeSteps {
		next, change := step(text)
		if change != "" {
			text = next
			changes = append(changes, change)
		}
	}
	return text, changes
}

// upgradeSteps run in order, each on what the one before left.
var upgradeSteps = []func(text string) (string, string){
	dropRetired,
	dropShipped,
}

// retired are values H2H once shipped for a setting of one of its harnesses
// and ships no longer. A project that still has one copied an older default
// and did not choose it, so the line goes and the current default applies.
// When a shipped value changes, the old one is added here.
var retired = []struct {
	harness, key string
	value        any
}{
	{"claude", "context_args", []any{"--append-system-prompt"}},
}

// dropRetired removes settings whose value is one H2H no longer ships.
func dropRetired(text string) (string, string) {
	var dropped []string
	next := removeSettings(text, func(harness, key string, value any) bool {
		for _, r := range retired {
			if r.harness == harness && r.key == key && reflect.DeepEqual(r.value, value) {
				dropped = append(dropped, harness+"."+key)
				return true
			}
		}
		return false
	})
	if len(dropped) == 0 {
		return text, ""
	}
	return next, fmt.Sprintf("Removed %s that H2H used to ship and has since replaced, so the current %s: %s",
		pluralise(len(dropped), "setting"), pick(len(dropped), "default applies", "defaults apply"), strings.Join(dropped, ", "))
}

// dropShipped removes settings that hold exactly what H2H ships for that
// harness, so the entry says only how the project differs, and what later
// versions of H2H change reaches it. What the config sets up stays the same.
func dropShipped(text string) (string, string) {
	shipped := shippedRaw()
	count := map[string]int{}
	next := removeSettings(text, func(harness, key string, value any) bool {
		if key == "command" { // what ties the entry to the shipped one
			return false
		}
		if want, ok := shipped[harness][key]; ok && reflect.DeepEqual(want, value) {
			count[harness]++
			return true
		}
		return false
	})
	if len(count) == 0 {
		return text, ""
	}
	// Nothing may change in what the config sets up. If it would, keep the
	// text as it was.
	if before, err := Parse(text); err == nil {
		if after, err := Parse(next); err != nil || !reflect.DeepEqual(before.Harnesses, after.Harnesses) {
			return text, ""
		}
	}
	names := make([]string, 0, len(count))
	for name := range count {
		names = append(names, fmt.Sprintf("%s (%d)", name, count[name]))
	}
	sort.Strings(names)
	return next, "Removed settings that are the same as what H2H ships, so later updates reach them: " + strings.Join(names, ", ")
}

// shippedRaw returns the shipped harness settings as TOML values.
func shippedRaw() map[string]map[string]any {
	var doc struct {
		Harness map[string]map[string]any `toml:"harness"`
	}
	_, _ = toml.Decode(defaultTOML, &doc) // a test keeps the shipped config valid
	return doc.Harness
}

// removeSettings returns text without the setting lines, in tables of
// harnesses H2H ships and run by the same program, for which drop is true.
// A setting whose value spans several lines goes whole.
func removeSettings(text string, drop func(harness, key string, value any) bool) string {
	var doc struct {
		Harness map[string]map[string]any `toml:"harness"`
	}
	if _, err := toml.Decode(text, &doc); err != nil {
		return text // nothing is removed from a file that does not read
	}
	shipped := shippedRaw()
	lines := strings.Split(text, "\n")
	var out []string
	harness := "" // the shipped harness whose table this is, or ""
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "[") {
			harness = ""
			if name, ok := strings.CutPrefix(strings.Trim(trimmed, "[] "), "harness."); ok && !strings.HasPrefix(trimmed, "[[") {
				if mine, theirs := doc.Harness[name]["command"], shipped[name]["command"]; theirs != nil && reflect.DeepEqual(mine, theirs) {
					harness = name
				}
			}
			out = append(out, line)
			continue
		}
		key, _, isSetting := strings.Cut(trimmed, "=")
		key = strings.TrimSpace(key)
		if harness == "" || !isSetting || strings.HasPrefix(trimmed, "#") || key == "" {
			out = append(out, line)
			continue
		}
		// The setting may continue over the next lines: take lines until
		// they read as one whole setting.
		end, value, ok := i, any(nil), false
		for ; end < len(lines); end++ {
			var v map[string]any
			if _, err := toml.Decode(strings.Join(lines[i:end+1], "\n"), &v); err == nil {
				value, ok = v[key], true
				break
			}
		}
		if !ok || !drop(harness, key, value) {
			out = append(out, line)
			continue
		}
		i = end
	}
	return strings.Join(out, "\n")
}

func pluralise(n int, noun string) string {
	if n == 1 {
		return "1 " + noun
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

func pick(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
