package console

import (
	"context"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/muesli/termenv"
)

const (
	testTitle = "H2H 0.1.0-dev"
	testPath  = "~/projects/selkomark/h2h-playground"
)

// headerModel is a console with the tabs h2h has, on a screen of a width.
// The Harnesses view's content is read from harnesses, so a test can
// change what it counts.
func headerModel(width int, path string, harnesses *Table) *model {
	plain := lipgloss.NewStyle()
	view := func(name string) View {
		return View{Name: name, Command: "/" + strings.ToLower(name), Table: func() Table { return Table{} }}
	}
	middle := view("Harnesses")
	middle.Table = func() Table { return *harnesses }
	m := newModel(Options{
		Theme:  Theme{Accent: plain, Dim: plain, Warn: plain, Frame: lipgloss.NoColor{}},
		Title:  testTitle,
		Path:   path,
		Views:  []View{view("Map"), middle, view("Sessions")},
		Handle: func(context.Context, UI, string) bool { return false },
	}, fakeUI{})
	m.Update(tea.WindowSizeMsg{Width: width, Height: 24})
	return m
}

// headerText is the header as it is on screen, inside the frame.
func headerText(m *model) string {
	row := []rune(ansi.Strip(strings.Split(m.View(), "\n")[tabRowY]))
	return strings.TrimRight(string(row[contentX:len(row)-contentX]), " ")
}

func TestHeaderGivesWayAsTheScreenNarrows(t *testing.T) {
	const tabs = " Console   Map   Harnesses (3/4)   Sessions "
	// For each screen width: the column inside the frame where the title
	// starts, or -1 when it is not shown, and the path as shown.
	tests := []struct {
		width   int
		titleAt int
		path    string
	}{
		{200, 91, testPath},
		{140, 61, testPath},
		{120, 51, testPath},
		// The middle would touch the tabs, so the title moves right of
		// them, and then the path loses directories.
		{110, 47, testPath},
		{101, 47, "…/selkomark/h2h-playground"},
		{90, 47, "…/h2h-playground"},
		// The title goes, and the path takes its room.
		{77, -1, "…/selkomark/h2h-playground"},
		{68, -1, "…/h2h-playground"},
		{66, -1, "h2h-playground"},
		{63, -1, "…-playground"},
		// Too little is left of the folder's name to tell what it is, and
		// the title fits where the path does not.
		{60, -1, ""},
		{48, -1, ""},
	}
	for _, tt := range tests {
		m := headerModel(tt.width, testPath, &Table{Count: "3/4"})
		got := headerText(m)
		t.Logf("%3d |%s|", tt.width, got)
		inner := tt.width - chromeWidth
		if !strings.HasPrefix(got, strings.TrimRight(tabs[:min(len(tabs), inner)], " ")) {
			t.Errorf("width %d: the tabs are not at the left: %q", tt.width, got)
		}
		at := -1
		if i := strings.Index(got, testTitle); i >= 0 {
			at = ansi.StringWidth(got[:i])
		}
		if at != tt.titleAt {
			t.Errorf("width %d: the title is at column %d, want %d: %q", tt.width, at, tt.titleAt, got)
		}
		rest := strings.TrimSpace(strings.Replace(strings.TrimPrefix(got, strings.TrimRight(tabs, " ")), testTitle, "", 1))
		if inner >= len(tabs) && rest != tt.path {
			t.Errorf("width %d: the path is %q, want %q", tt.width, rest, tt.path)
		}
		if tt.path != "" && ansi.StringWidth(got) != inner {
			t.Errorf("width %d: the path ends at column %d of %d: %q", tt.width, ansi.StringWidth(got), inner, got)
		}
	}
}

// At every width the header is one row that stays inside the frame, with a
// gap between its parts.
func TestHeaderNeverOverlapsOrWraps(t *testing.T) {
	for _, path := range []string{testPath, "", "/", "~", "~/项目/very-long-项目名称-that-goes-on-and-on-and-on", "~/café/projét", "~/line\nbreak/app"} {
		for width := 20; width <= 220; width++ {
			m := headerModel(width, path, &Table{Count: "3/4"})
			// Colour codes must not count as columns.
			m.o.Title = "\x1b[1;32mH2H\x1b[0m \x1b[2m0.1.0-dev\x1b[0m"
			lines := strings.Split(m.View(), "\n")
			if len(lines) != 24 {
				t.Fatalf("path %q, width %d: the page is %d rows, want 24:\n%s", path, width, len(lines), m.View())
			}
			for y, l := range lines[:len(lines)-1] {
				if w := ansi.StringWidth(l); w != width {
					t.Fatalf("path %q, width %d: row %d is %d columns: %q", path, width, y, w, l)
				}
			}
			row := m.headerRow(width - chromeWidth)
			if w := ansi.StringWidth(row); w > width-chromeWidth || strings.Contains(row, "\n") {
				t.Fatalf("path %q, width %d: the header is %d columns: %q", path, width, w, row)
			}
			text := ansi.Strip(row)
			if i := strings.Index(text, testTitle); i >= 0 && !strings.HasSuffix(text[:i], strings.Repeat(" ", headerGap)) {
				t.Fatalf("path %q, width %d: the title touches the tabs: %q", path, width, text)
			}
			if _, after, ok := strings.Cut(text, testTitle); ok && after != "" && !strings.HasPrefix(after, strings.Repeat(" ", headerGap)) {
				t.Fatalf("path %q, width %d: the title touches the path: %q", path, width, text)
			}
		}
	}
}

func TestShortenPath(t *testing.T) {
	tests := []struct {
		path  string
		width int
		want  string
	}{
		{testPath, 35, testPath},
		{testPath, 34, "…/selkomark/h2h-playground"},
		{testPath, 26, "…/selkomark/h2h-playground"},
		{testPath, 25, "…/h2h-playground"},
		{testPath, 16, "…/h2h-playground"},
		{testPath, 15, "h2h-playground"},
		{testPath, 14, "h2h-playground"},
		{testPath, 13, "…h-playground"},
		{testPath, 12, "…-playground"},
		{testPath, 11, ""},
		{testPath, -3, ""},
		{"/srv/www/app", 11, "…/www/app"},
		{"/srv/www/app", 3, "app"},
		{"/a/b/", 4, "…/b"},
		{"~", 1, "~"},
		// A wide character takes two columns, and is not cut in half.
		{"~/code/项目名称项目名称项目", 27, "~/code/项目名称项目名称项目"},
		{"~/code/项目名称项目名称项目", 26, "…/项目名称项目名称项目"},
		{"~/项目名称项目名称项目", 14, "…项目名称项目"},
	}
	for _, tt := range tests {
		got := shortenPath(tt.path, tt.width)
		if w := ansi.StringWidth(got); got != "" && w > tt.width {
			t.Errorf("shortenPath(%q, %d) = %q, which is %d columns", tt.path, tt.width, got, w)
		}
		if got != tt.want {
			t.Errorf("shortenPath(%q, %d) = %q, want %q", tt.path, tt.width, got, tt.want)
		}
	}
}

func TestHeaderFollowsResizes(t *testing.T) {
	m := headerModel(200, testPath, &Table{Count: "3/4"})
	for _, step := range []struct {
		width int
		want  string
	}{{90, "…/h2h-playground"}, {200, testPath}} {
		m.Update(tea.WindowSizeMsg{Width: step.width, Height: 24})
		if got := headerText(m); !strings.HasSuffix(got, " "+step.want) {
			t.Errorf("resized to %d: %q, want it to end in %q", step.width, got, step.want)
		}
	}
}

func TestHarnessesTabCountsAndFollowsRefreshes(t *testing.T) {
	harnesses := Table{}
	m := headerModel(120, testPath, &harnesses)
	if got := headerText(m); !strings.Contains(got, " Harnesses   Sessions ") || strings.Contains(got, "(") {
		t.Errorf("with nothing to count: %q", got)
	}
	harnesses = Table{Count: "1/12"}
	m.Update(m.snapshot())
	if got := headerText(m); !strings.Contains(got, " Harnesses (1/12)   Sessions ") {
		t.Errorf("after a refresh: %q", got)
	}

	// Clicks land on the tabs as drawn, with the count part of its tab,
	// whichever tab is on screen.
	for _, tt := range []struct {
		on  string
		off int
		tab int
	}{
		{"Sessions", 0, 3}, {"Sessions", 8, 3}, {"(1/12)", 5, 2}, {"Harnesses", 0, 2},
		{"Map", 0, 1}, {"Console", 0, 0},
	} {
		x, y := find(t, m, tt.on)
		if y != tabRowY {
			t.Fatalf("%q is on row %d, want %d", tt.on, y, tabRowY)
		}
		m.tab = (tt.tab + 1) % 4
		click(m, x+tt.off, y)
		if m.tab != tt.tab {
			t.Errorf("clicking column %d of %q: tab %d, want %d", tt.off, tt.on, m.tab, tt.tab)
		}
	}
	// The title and the path are not buttons.
	for _, text := range []string{testTitle, testPath} {
		x, y := find(t, m, text)
		click(m, x+1, y)
		if m.tab != 0 {
			t.Errorf("clicking %q switched to tab %d", text, m.tab)
		}
	}
}

func TestHarnessesCountWarnsWhenOneNeedsTheUser(t *testing.T) {
	// Colour is forced on, as the tests otherwise run without any.
	r := lipgloss.DefaultRenderer()
	before := r.ColorProfile()
	r.SetColorProfile(termenv.ANSI256)
	defer r.SetColorProfile(before)

	harnesses := Table{Count: "3/4"}
	m := headerModel(120, testPath, &harnesses)
	m.o.Theme.Warn = lipgloss.NewStyle().Foreground(lipgloss.Color("214"))
	const warned = "38;5;214"
	for _, tab := range []int{0, 2} { // the tab in the background, and on screen
		m.tab = tab
		harnesses.Attention = false
		m.Update(m.snapshot())
		if row := m.tabRow(); strings.Contains(row, warned) {
			t.Errorf("tab %d: nothing needs the user, but the tabs warn: %q", tab, row)
		}
		harnesses.Attention = true
		m.Update(m.snapshot())
		row := m.tabRow()
		_, after, ok := strings.Cut(row, warned)
		if !ok {
			t.Fatalf("tab %d: a harness needs the user, but the count is not in the warning colour: %q", tab, row)
		}
		if got, _, _ := strings.Cut(after, "\x1b[0m"); !strings.HasSuffix(got, "(3/4)") {
			t.Errorf("tab %d: the warning colour is on %q, want the count: %q", tab, got, row)
		}
		if got, want := ansi.Strip(row), " Console   Map   Harnesses (3/4)   Sessions "; got != want {
			t.Errorf("tab %d: the tabs read %q, want %q", tab, got, want)
		}
	}
}
