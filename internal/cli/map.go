package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss/tree"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/session"
)

// mapCmd prints how the config is wired up: the domains of responsibility,
// who owns each and with which sub-agents, how work flows between them, and
// how each harness is started. The structure comes only from the config;
// the sessions add state. See docs/responsibility-map.md.
func (e *env) mapCmd(_ context.Context, args []string) error {
	if len(args) > 0 {
		return fmt.Errorf("map takes no arguments")
	}
	cfg, err := config.Load(e.root)
	if err != nil {
		return err
	}
	infos, err := session.List(e.root)
	if err != nil {
		return err
	}
	fmt.Fprint(e.out, configMap(displayPath(e.root, os.Getenv("HOME"), 40), cfg, infos, e.st))
	return nil
}

// configMap draws the config as a tree with three branches: domains, the
// flow between them, and harnesses.
func configMap(project string, cfg config.Config, infos []session.Info, st styles) string {
	root := newTree(st, st.title.Render("H2H")+" "+st.dim.Render(project))
	if len(cfg.Domains) > 0 {
		root.Child(domainsTree(cfg, infos, st), flowTree(cfg, st))
	} else {
		root.Child(st.title.Render("Domains") + " " + st.dim.Render("none yet; add a [domain.<name>] table to "+config.FileName))
	}
	root.Child(harnessesTree(cfg, infos, st))
	return root.String() + "\n"
}

func domainsTree(cfg config.Config, infos []session.Info, st styles) *tree.Tree {
	t := newTree(st, st.title.Render("Domains"))
	for _, name := range cfg.DomainNames() {
		d := cfg.Domains[name]
		b := newTree(st, st.accent.Render(name)+" "+st.dim.Render("owned by")+" "+d.Harness)
		if d.Does != "" {
			b.Child(field(st, "does", d.Does))
		}
		if len(d.Paths) > 0 {
			b.Child(field(st, "paths", strings.Join(d.Paths, ", ")))
		}
		if len(d.Subagents) > 0 {
			subs := newTree(st, st.dim.Render("sub-agents"))
			for _, s := range d.Subagents {
				line := s.Role
				if s.Does != "" {
					line += " " + st.dim.Render(s.Does)
				}
				subs.Child(line)
			}
			b.Child(subs)
		}
		if s := sessionsTree(st, infos, func(i session.Info) bool { return i.Domain == name }); s != nil {
			b.Child(s)
		}
		t.Child(b)
	}
	return t
}

// flowTree shows how work moves between domains: one line per domain that
// waits for others, and the domains that stand alone.
func flowTree(cfg config.Config, st styles) *tree.Tree {
	t := newTree(st, st.title.Render("Flow"))
	var alone []string
	for _, name := range cfg.DomainNames() {
		d := cfg.Domains[name]
		if len(d.DependsOn) > 0 {
			deps := slices.Sorted(slices.Values(d.DependsOn))
			t.Child(strings.Join(deps, " + ") + st.dim.Render(" → ") + st.accent.Render(name))
		} else if len(cfg.Dependents(name)) == 0 {
			alone = append(alone, name)
		}
	}
	for _, name := range alone {
		t.Child(st.accent.Render(name) + " " + st.dim.Render("independent, runs in parallel"))
	}
	return t
}

func harnessesTree(cfg config.Config, infos []session.Info, st styles) *tree.Tree {
	t := newTree(st, st.title.Render("Harnesses"))
	for _, name := range cfg.Names() {
		h := cfg.Harnesses[name]
		label := st.accent.Render(name)
		if name == cfg.DefaultHarness {
			label += " " + st.dim.Render("(default: answers questions typed in the console)")
		}
		b := newTree(st, label)
		b.Child(field(st, "session", commandLine(h.Command, h.Args)))
		b.Child(field(st, "question", commandLine(h.Command, h.AskArgs)+" "+st.dim.Render("<prompt>")))
		roles := st.dim.Render("not passed (no context_args or context_env)")
		if h.PassesRoles() {
			roles = strings.Join(append(slices.Clone(h.ContextArgs), h.ContextEnv...), " ")
		}
		b.Child(field(st, "roles", roles))
		env := st.dim.Render("basic environment only")
		if len(h.PassEnv) > 0 {
			env = "basic + " + strings.Join(h.PassEnv, ", ")
		}
		b.Child(field(st, "env", env))
		if s := sessionsTree(st, infos, func(i session.Info) bool { return i.Harness == name }); s != nil {
			b.Child(s)
		} else {
			b.Child(field(st, "sessions", st.dim.Render("none")))
		}
		t.Child(b)
	}

	// Sessions whose harness is no longer in the config still run, so they
	// are shown rather than hidden.
	if s := sessionsTree(st, infos, func(i session.Info) bool { _, ok := cfg.Harnesses[i.Harness]; return !ok }); s != nil {
		s.Root(st.warn.Render("not in config"))
		t.Child(s)
	}
	return t
}

// sessionsTree lists the sessions that match, or returns nil when none do.
func sessionsTree(st styles, infos []session.Info, match func(session.Info) bool) *tree.Tree {
	var t *tree.Tree
	for _, i := range infos {
		if !match(i) {
			continue
		}
		if t == nil {
			t = newTree(st, st.dim.Render("sessions"))
		}
		t.Child(st.state(i).Render("● ") + i.ID + " " + st.dim.Render(i.Harness+", "+stateText(i)))
	}
	return t
}

func newTree(st styles, root string) *tree.Tree {
	return tree.Root(root).Enumerator(tree.RoundedEnumerator).EnumeratorStyle(st.dim.PaddingRight(1))
}

func field(st styles, name, value string) string {
	return st.dim.Render(fmt.Sprintf("%-9s", name)) + " " + value
}

func commandLine(command string, args []string) string {
	return strings.TrimSpace(command + " " + strings.Join(args, " "))
}
