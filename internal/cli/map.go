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

// mapCmd prints how the config is wired up: which harnesses exist, how each
// is started, what it may see, and which sessions run it now. The structure
// comes only from the config; the sessions add state. See
// docs/responsibility-map.md.
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

// configMap draws the config as a tree: the project at the root, one
// branch per harness, and under each harness the way it is started, the
// environment it is given and its sessions.
func configMap(project string, cfg config.Config, infos []session.Info, st styles) string {
	byHarness := map[string][]session.Info{}
	for _, i := range infos {
		byHarness[i.Harness] = append(byHarness[i.Harness], i)
	}

	root := tree.Root(st.title.Render("H2H") + " " + st.dim.Render(project)).
		Enumerator(tree.RoundedEnumerator).
		EnumeratorStyle(st.dim.PaddingRight(1))

	for _, name := range cfg.Names() {
		h := cfg.Harnesses[name]
		label := st.accent.Render(name)
		if name == cfg.DefaultHarness {
			label += " " + st.dim.Render("(default: answers questions typed in the console)")
		}

		branch := tree.Root(label).Enumerator(tree.RoundedEnumerator).EnumeratorStyle(st.dim.PaddingRight(1))
		branch.Child(field(st, "session", commandLine(h.Command, h.Args)))
		branch.Child(field(st, "question", commandLine(h.Command, h.AskArgs)+" "+st.dim.Render("<prompt>")))
		env := st.dim.Render("basic environment only")
		if len(h.PassEnv) > 0 {
			env = "basic + " + strings.Join(h.PassEnv, ", ")
		}
		branch.Child(field(st, "env", env))

		sessions := byHarness[name]
		delete(byHarness, name)
		if len(sessions) == 0 {
			branch.Child(field(st, "sessions", st.dim.Render("none")))
		} else {
			list := tree.Root(st.dim.Render(fmt.Sprintf("%-9s", "sessions"))).Enumerator(tree.RoundedEnumerator).EnumeratorStyle(st.dim.PaddingRight(1))
			for _, i := range sessions {
				list.Child(st.state(i).Render("● ") + i.ID + " " + st.dim.Render(stateText(i)))
			}
			branch.Child(list)
		}
		root.Child(branch)
	}

	// Sessions whose harness is no longer in the config still run, so they
	// are shown rather than hidden.
	if len(byHarness) > 0 {
		orphans := tree.Root(st.warn.Render("not in config")).Enumerator(tree.RoundedEnumerator).EnumeratorStyle(st.dim.PaddingRight(1))
		names := make([]string, 0, len(byHarness))
		for n := range byHarness {
			names = append(names, n)
		}
		slices.Sort(names)
		for _, n := range names {
			for _, i := range byHarness[n] {
				orphans.Child(st.state(i).Render("● ") + i.ID + " " + st.dim.Render(i.Harness+", "+stateText(i)))
			}
		}
		root.Child(orphans)
	}
	return root.String() + "\n"
}

func field(st styles, name, value string) string {
	return st.dim.Render(fmt.Sprintf("%-9s", name)) + " " + value
}

func commandLine(command string, args []string) string {
	return strings.TrimSpace(command + " " + strings.Join(args, " "))
}
