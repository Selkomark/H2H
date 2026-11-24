package console

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

var testGraph = Graph{
	Nodes: []Node{
		{ID: "infra", Title: "infra", Lines: []string{"codex"}, Detail: text("does  Local infrastructure\n\nSub-agents\n  llm  Writes the client")},
		{ID: "development", Title: "development", Lines: []string{"claude", "2 sub-agents"}, Detail: text("does  The API and the web app"), More: text("paths  services/api/**")},
		{ID: "e2e", Title: "e2e", Lines: []string{"codex-low"}, Detail: text("does  Runs the tests")},
		{ID: "docs", Title: "docs", Lines: []string{"claude"}, Detail: text("does  The documentation")},
	},
	Edges: []Edge{{"infra", "development"}, {"development", "e2e"}, {"infra", "e2e"}},
}

// text is a node detail that reads the same at every width.
func text(s string) func(int) string { return func(int) string { return s } }

func plainTheme() Theme {
	p := lipgloss.NewStyle()
	return Theme{Accent: p, Dim: p, Frame: lipgloss.NoColor{}}
}

func columnOf(d drawing, id string) int {
	for _, s := range d.spots {
		if s.node != nil && s.node.ID == id {
			return s.column
		}
	}
	return -1
}

func TestGraphLayout(t *testing.T) {
	d := layoutGraph(testGraph)
	// Every arrow points right: a node is one column past the last of
	// those it comes after.
	for id, want := range map[string]int{"infra": 0, "docs": 0, "development": 1, "e2e": 2} {
		if got := columnOf(d, id); got != want {
			t.Errorf("%s is in column %d, want %d", id, got, want)
		}
	}
	// The arrow from infra to e2e skips a column, and gets a place in it.
	passes := 0
	for _, s := range d.spots {
		if s.node == nil && s.column == 1 && s.from == "infra" && s.to == "e2e" {
			passes++
		}
	}
	if passes != 1 {
		t.Errorf("the arrow that skips a column passes through it %d times, want once", passes)
	}
	// No two things overlap.
	for a := range d.spots {
		for b := a + 1; b < len(d.spots); b++ {
			s, o := d.spots[a], d.spots[b]
			if s.x < o.x+max(1, o.w) && o.x < s.x+max(1, s.w) && s.y < o.y+o.h && o.y < s.y+s.h {
				t.Errorf("spots %d and %d overlap: %+v, %+v", a, b, s, o)
			}
		}
	}

	out := d.render(plainTheme(), "")
	for _, want := range []string{"│ infra", "│ development", "│ e2e", "│ docs", "▶", "2 sub-agents"} {
		if !strings.Contains(out, want) {
			t.Errorf("the drawing lacks %q:\n%s", want, out)
		}
	}
	if strings.Count(out, "▶") != 2 {
		t.Errorf("want an arrowhead at each of the two nodes that are pointed at:\n%s", out)
	}
	for _, line := range strings.Split(out, "\n") {
		if lipgloss.Width(line) > d.width {
			t.Errorf("a row is wider than the drawing: %q", line)
		}
	}

	// Edges that name no node, repeat, or loop are left out; a circle
	// does not hang the layout.
	odd := Graph{Nodes: testGraph.Nodes[:2], Edges: []Edge{{"infra", "nobody"}, {"infra", "infra"}, {"infra", "development"}, {"infra", "development"}, {"development", "infra"}}}
	if got := layoutGraph(odd).render(plainTheme(), ""); !strings.Contains(got, "│ infra") || !strings.Contains(got, "│ development") {
		t.Errorf("a graph with odd edges did not draw its nodes:\n%s", got)
	}
	if got := layoutGraph(Graph{}).render(plainTheme(), ""); got != "" {
		t.Errorf("an empty graph drew %q", got)
	}
}

func TestGraphNeighbour(t *testing.T) {
	d := layoutGraph(testGraph)
	tests := []struct {
		from   string
		dx, dy int
		want   string
	}{
		{"", 0, 0, "infra"},            // nothing selected: the first
		{"infra", 1, 0, "development"}, // right, to the next column
		{"development", 1, 0, "e2e"},   // and on
		{"e2e", 1, 0, "e2e"},           // nothing further right
		{"e2e", -1, 0, "development"},  // back
		{"infra", 0, 1, "docs"},        // down the column
		{"docs", 0, -1, "infra"},       // and up
		{"docs", 0, 1, "docs"},         // nothing below
		{"development", 0, 1, "development"},
	}
	for _, tt := range tests {
		if got := d.neighbour(tt.from, tt.dx, tt.dy); got != tt.want {
			t.Errorf("neighbour(%q, %d, %d) = %q, want %q", tt.from, tt.dx, tt.dy, got, tt.want)
		}
	}
}

// graphModel is a console whose only view is a graph.
func graphModel(g *Graph) *model {
	m := newModel(Options{
		Theme:  plainTheme(),
		Title:  "H2H",
		Views:  []View{{Name: "Map", Command: "/map", Table: func() Table { return Table{Graph: g, Empty: "No domains yet."} }}},
		Handle: func(_ context.Context, _ UI, _ string) bool { return false },
	}, fakeUI{})
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	return m
}

func TestGraphViewSelectAndInspect(t *testing.T) {
	g := testGraph
	m := graphModel(&g)
	typeText(m, "/map")
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v := m.View()
	if m.tab != 1 || !strings.Contains(v, "│ development") || !strings.Contains(v, "▶") || !strings.Contains(v, "enter inspect") {
		t.Fatalf("/map did not open the drawing:\n%s", v)
	}
	if rows := len(strings.Split(v, "\n")); rows != 30 {
		t.Errorf("the page is %d rows", rows)
	}

	// The arrows move between the boxes, and Enter inspects the one chosen.
	if d, _ := m.graph(); m.graphSelection(d) != "infra" {
		t.Errorf("the selection starts at %q, want where the flow starts", m.graphSelection(d))
	}
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.dialog == nil || m.dialog.question != "development" || !strings.Contains(m.View(), "The API and the web app") || !strings.Contains(m.View(), "Close") {
		t.Fatalf("inspecting did not show the selected node's detail:\n%s", m.View())
	}
	if !strings.Contains(m.View(), "│ infra") {
		t.Errorf("the drawing is hidden behind the detail:\n%s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.dialog != nil {
		t.Fatal("esc did not close the detail")
	}

	// A click selects a box, and a click on the selected one inspects it.
	x, y := find(t, m, "docs")
	click(m, x, y)
	if m.dialog != nil || m.selectedID[0] != "docs" {
		t.Fatalf("a first click: dialog %v, selected %q; want docs selected only", m.dialog != nil, m.selectedID[0])
	}
	click(m, x, y)
	if m.dialog == nil || !strings.Contains(m.View(), "The documentation") {
		t.Fatalf("a click on the selected box did not inspect it:\n%s", m.View())
	}
	x, y = find(t, m, "Close")
	click(m, x, y)
	if m.dialog != nil {
		t.Error("clicking Close did not close the detail")
	}
	// A click between the boxes selects nothing new.
	click(m, 98, 20)
	if m.selectedID[0] != "docs" {
		t.Errorf("a click on empty space changed the selection to %q", m.selectedID[0])
	}

	// With no nodes the view says so.
	empty := Graph{}
	e := graphModel(&empty)
	e.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	e.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !strings.Contains(e.View(), "No domains yet.") || e.dialog != nil {
		t.Errorf("an empty graph view:\n%s", e.View())
	}
}

func TestInspectShowsMoreOnRequest(t *testing.T) {
	g := testGraph
	m := graphModel(&g)
	m.Update(tea.KeyMsg{Type: tea.KeyShiftTab})
	m.Update(tea.KeyMsg{Type: tea.KeyRight}) // development, which has more to show
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v := m.View()
	if !strings.Contains(v, "The API and the web app") || strings.Contains(v, "services/api/**") || !strings.Contains(v, "Show more") {
		t.Fatalf("want the first part and a button for the rest:\n%s", v)
	}
	// Enter on the focused button opens the rest, and again closes it.
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	v = m.View()
	if m.dialog == nil || !strings.Contains(v, "services/api/**") || !strings.Contains(v, "Show less") || len(strings.Split(v, "\n")) != 30 {
		t.Fatalf("enter did not open the rest, or the page changed size:\n%s", v)
	}
	typeText(m, "m")
	if strings.Contains(m.View(), "services/api/**") {
		t.Errorf("m did not close the rest:\n%s", m.View())
	}
	// By mouse: the first button opens the rest, Close closes the box.
	x, y := find(t, m, "Show more")
	click(m, x, y)
	if m.dialog == nil || !strings.Contains(m.View(), "services/api/**") {
		t.Fatalf("clicking Show more did not open the rest:\n%s", m.View())
	}
	x, y = find(t, m, "Close")
	click(m, x, y)
	if m.dialog != nil {
		t.Error("clicking Close did not close the box")
	}
	// A node with nothing more has only Close, and Enter closes it.
	m.Update(tea.KeyMsg{Type: tea.KeyRight})
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if strings.Contains(m.View(), "Show more") || !strings.Contains(m.View(), "Runs the tests") {
		t.Fatalf("a node with nothing more:\n%s", m.View())
	}
	m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.dialog != nil {
		t.Error("enter did not close a box that has only Close")
	}
}
