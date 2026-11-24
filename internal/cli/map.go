package cli

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/lipgloss"
	lgtable "github.com/charmbracelet/lipgloss/table"
	"github.com/charmbracelet/lipgloss/tree"

	"github.com/Selkomark/H2H/internal/config"
	"github.com/Selkomark/H2H/internal/console"
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
	fmt.Fprint(e.out, mapText(displayPath(e.root, os.Getenv("HOME"), 40), cfg, infos, e.st))
	return nil
}

// mapGraph draws the config as a graph for the console: a box for each
// domain, with its owner and what is running for it, and an arrow from a
// domain to each domain that waits for its work. A box's detail is how
// the domain is set up inside.
func mapGraph(cfg config.Config, infos []session.Info, st styles) console.Graph {
	var g console.Graph
	for _, name := range cfg.DomainNames() {
		d := cfg.Domains[name]
		lines := []string{d.Harness}
		if n := len(d.Subagents); n > 0 {
			lines = append(lines, plural(n, "sub-agent"))
		}
		for _, i := range infos {
			if i.Domain == name && i.Live() {
				lines = append(lines, "▸ "+i.ID+" "+stateText(i))
			}
		}
		g.Nodes = append(g.Nodes, console.Node{ID: name, Title: name, Lines: lines, Detail: domainAgents(cfg, name, st), More: domainSetup(cfg, name, infos, st)})
		for _, dep := range d.DependsOn {
			g.Edges = append(g.Edges, console.Edge{From: dep, To: name})
		}
	}
	return g
}

// proposedMap draws a config that is not in force yet, for the user to
// check before they apply it. Nobody can select a box here to look inside
// it, so each box lists its sub-agents itself.
func proposedMap(cfg config.Config, st styles) string {
	if len(cfg.Domains) == 0 {
		return st.dim.Render("No domains: every harness is started by its own name.")
	}
	var g console.Graph
	for _, name := range cfg.DomainNames() {
		d := cfg.Domains[name]
		lines := []string{d.Harness}
		for _, s := range d.Subagents {
			lines = append(lines, "· "+s.Role)
		}
		g.Nodes = append(g.Nodes, console.Node{ID: name, Title: name, Lines: lines})
		for _, dep := range d.DependsOn {
			g.Edges = append(g.Edges, console.Edge{From: dep, To: name})
		}
	}
	return console.DrawGraph(g, console.Theme{Accent: st.accent, Dim: st.dim, Frame: frameColor}) + "\n" +
		st.dim.Render("Each box is a domain: its owner, then its sub-agents. An arrow points to the domain that waits for the work.")
}

// maxDetailWidth keeps the tables of a domain's detail from stretching
// across a wide terminal, where long rows are hard to follow.
const maxDetailWidth = 110

// grid draws rows under headers as a table with a line between every two
// rows, so each row reads as one thing. It is as wide as its content needs
// and no wider than width, where long cells wrap. The first column names
// what the row is about, so it stands out.
func grid(st styles, width int, headers []string, rows [][]string) string {
	t := lgtable.New().
		Border(lipgloss.RoundedBorder()).
		BorderStyle(st.dim).
		BorderRow(true).
		Headers(headers...).
		Rows(rows...).
		StyleFunc(func(row, col int) lipgloss.Style {
			cell := lipgloss.NewStyle().Padding(0, 1)
			switch {
			case row == lgtable.HeaderRow:
				return cell.Inherit(st.title)
			case col == 0:
				return cell.Inherit(st.accent)
			}
			return cell
		})
	// Only a table too wide for the box is given a width, which makes
	// its cells wrap. One that fits keeps the size of its content.
	if width = min(width, maxDetailWidth); lipgloss.Width(t.String()) > width {
		t.Width(width)
	}
	return t.String()
}

// domainAgents is the first thing shown about a domain: what it is for,
// and who does what in it. One sentence says who is in charge, and the
// table gives each sub-agent its job.
func domainAgents(cfg config.Config, name string, st styles) func(width int) string {
	d := cfg.Domains[name]
	return func(width int) string {
		var b strings.Builder
		wrap := lipgloss.NewStyle().Width(min(width, maxDetailWidth))
		if d.Does != "" {
			fmt.Fprintln(&b, wrap.Render(d.Does))
			fmt.Fprintln(&b)
		}
		in := st.accent.Render(d.Harness) + " is in charge of this domain"
		if len(d.Subagents) == 0 {
			fmt.Fprint(&b, wrap.Render(in+" and does the work itself. It has no sub-agents."))
			return b.String()
		}
		fmt.Fprintln(&b, wrap.Render(in+" and hands the work to "+plural(len(d.Subagents), "sub-agent")+":"))
		fmt.Fprintln(&b)
		var rows [][]string
		for _, s := range d.Subagents {
			does := s.Does
			if does == "" {
				does = "No job written down for it"
			}
			rows = append(rows, []string{s.Role, does})
		}
		fmt.Fprint(&b, grid(st, width, []string{"Sub-agent", "Its job"}, rows))
		return b.String()
	}
}

// domainSetup is the rest of what there is to know about a domain, as
// questions a user has about it and answers in plain words: is it
// running, what it works on, where it stands in the flow, and how its
// harness is started and treated.
func domainSetup(cfg config.Config, name string, infos []session.Info, st styles) func(width int) string {
	d := cfg.Domains[name]
	h := cfg.Harnesses[d.Harness]

	var live []string
	for _, i := range infos {
		if i.Domain == name && i.Live() {
			live = append(live, i.ID)
		}
	}
	running := "No. Start it with /start " + name
	if len(live) > 0 {
		running = "Yes, as " + strings.Join(live, ", ") + ". Open it with /attach " + live[0]
	}
	files := "No files are assigned to it"
	if len(d.Paths) > 0 {
		files = strings.Join(d.Paths, ", ")
	}
	after := "Right away. It does not wait for another domain"
	if len(d.DependsOn) > 0 {
		after = "After " + strings.Join(d.DependsOn, " and ") + " has done its work"
	}
	next := "Nowhere. No other domain waits for it"
	if dependents := cfg.Dependents(name); len(dependents) > 0 {
		next = "To " + strings.Join(dependents, " and ") + ", which waits for it"
	}
	role := "No. " + d.Harness + " has no way set up to be told (context_args or context_env in the config)"
	if h.PassesRoles() {
		role = "Yes. It is told what this domain covers and which sub-agents to use, every time it starts"
	}
	permission := "It stops and asks in its own terminal. Open it with /attach to answer"
	if h.AsksH2H() {
		permission = "H2H answers from the [permissions] rules, and asks you in this console for anything they do not cover"
	}
	rows := [][]string{
		{"Is it running?", running},
		{"Which files does it work on?", files},
		{"When can it start?", after},
		{"Where does its work go?", next},
		{"How is " + d.Harness + " started?", "With the command: " + commandLine(h.Command, h.Args)},
		{"Does it know its role?", role},
		{"What if it needs permission?", permission},
	}
	return func(width int) string {
		return grid(st, width, []string{"More about " + name, ""}, rows)
	}
}

// setupPicture draws a config for someone about to change it: the map,
// with each domain's sub-agents in its box, and under it a table with one
// row per domain. It is what there is, seen at a glance, in place of a
// description to read.
func setupPicture(cfg config.Config, st styles) func(width int) string {
	return func(width int) string {
		if len(cfg.Domains) == 0 {
			return st.dim.Render("There are no domains yet: every harness is started by its own name.") + "\n" +
				st.dim.Render("Harnesses: ") + strings.Join(cfg.Names(), ", ") + st.dim.Render("; the default is ") + cfg.DefaultHarness
		}
		or := func(value, otherwise string) string {
			if value == "" {
				return otherwise
			}
			return value
		}
		var rows [][]string
		for _, name := range cfg.DomainNames() {
			d := cfg.Domains[name]
			var roles []string
			for _, s := range d.Subagents {
				roles = append(roles, s.Role)
			}
			rows = append(rows, []string{name, d.Harness, or(strings.Join(roles, ", "), "none"),
				or(strings.Join(d.Paths, ", "), "none"), or(strings.Join(d.DependsOn, ", "), "nothing")})
		}
		return proposedMap(cfg, st) + "\n\n" +
			grid(st, width, []string{"Domain", "Owner", "Sub-agents", "Files", "Starts after"}, rows) + "\n" +
			st.dim.Render("The default harness is ") + cfg.DefaultHarness + st.dim.Render(". Harnesses: ") + strings.Join(cfg.Names(), ", ")
	}
}

// textWidth is the width the map's tables are drawn for when they are
// printed, where there is no box to fit them to.
const textWidth = 100

// mapText is the map as text, for output that cannot be moved around in:
// the drawing, then each domain's setup under it.
func mapText(project string, cfg config.Config, infos []session.Info, st styles) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s\n\n", st.title.Render("H2H"), st.dim.Render(project))
	if len(cfg.Domains) == 0 {
		fmt.Fprintln(&b, st.dim.Render("No domains yet. Set them up with: ")+st.accent.Render("/config"))
	} else {
		g := mapGraph(cfg, infos, st)
		fmt.Fprintln(&b, console.DrawGraph(g, console.Theme{Accent: st.accent, Dim: st.dim, Frame: frameColor}))
		for _, n := range g.Nodes {
			fmt.Fprintf(&b, "\n%s\n%s\n%s\n", st.accent.Bold(true).Render(n.Title), n.Detail(textWidth), n.More(textWidth))
		}
	}
	names := cfg.Names()
	for n, name := range names {
		if name == cfg.DefaultHarness {
			names[n] += " (default)"
		}
	}
	fmt.Fprintf(&b, "\n%s %s\n", st.title.Render("Harnesses"), strings.Join(names, ", "))
	return b.String()
}

// mapView is the content of the console's Map tab.
func (e *env) mapView() console.Table {
	cfg, err := config.Load(e.root)
	if err != nil {
		return console.Table{Empty: e.st.bad.Render(err.Error())}
	}
	infos, _ := session.List(e.root)
	g := mapGraph(cfg, infos, e.st)
	return console.Table{
		Graph: &g,
		Empty: e.st.dim.Render("No domains yet. Set them up with ") + e.st.accent.Render("/config") +
			e.st.dim.Render(", and they are drawn here with the flow of work between them."),
	}
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
