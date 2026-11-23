package cli

import (
	"strings"

	"github.com/charmbracelet/lipgloss"

	"github.com/Selkomark/H2H/internal/session"
)

// styles is the visual vocabulary of h2h's output. It uses the terminal's
// own sixteen colours, so it follows the user's theme, light or dark.
// lipgloss drops all styling when the output is not a terminal or NO_COLOR
// is set.
type styles struct {
	title  lipgloss.Style // headings
	accent lipgloss.Style // names the user can type
	dim    lipgloss.Style // secondary text
	good   lipgloss.Style
	warn   lipgloss.Style
	bad    lipgloss.Style
	box    lipgloss.Style // the console's welcome panel
}

func newStyles() styles {
	return styles{
		title:  lipgloss.NewStyle().Bold(true),
		accent: lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("6")),
		dim:    lipgloss.NewStyle().Faint(true),
		good:   lipgloss.NewStyle().Foreground(lipgloss.Color("2")),
		warn:   lipgloss.NewStyle().Foreground(lipgloss.Color("3")),
		bad:    lipgloss.NewStyle().Foreground(lipgloss.Color("1")),
		box:    lipgloss.NewStyle().Border(lipgloss.RoundedBorder()).BorderForeground(lipgloss.Color("6")).Padding(0, 1),
	}
}

// plainStyles renders text unchanged, for output that is not shown to a
// person, such as the context given to a harness.
func plainStyles() styles {
	s := lipgloss.NewStyle()
	return styles{title: s, accent: s, dim: s, good: s, warn: s, bad: s, box: s}
}

// state picks the style for a session state.
func (st styles) state(i session.Info) lipgloss.Style {
	switch {
	case i.State == session.Running:
		return st.good
	case i.State == session.Starting:
		return st.warn
	case i.State == session.Exited && i.ExitCode != nil && *i.ExitCode == 0 && i.Error == "":
		return st.dim
	}
	return st.bad
}

// diff colours the rows of a lineDiff.
func (st styles) diff(d string) string {
	lines := strings.Split(strings.TrimSuffix(d, "\n"), "\n")
	for n, l := range lines {
		switch {
		case strings.HasPrefix(l, "+"):
			lines[n] = st.good.Render(l)
		case strings.HasPrefix(l, "-"):
			lines[n] = st.bad.Render(l)
		default:
			lines[n] = st.dim.Render(l)
		}
	}
	return strings.Join(lines, "\n") + "\n"
}

// table lays out rows in aligned columns. Cells are padded before they are
// styled, so colour codes never disturb the alignment.
func table(header []string, rows [][]string, headStyle lipgloss.Style, cellStyle func(row, col int) lipgloss.Style) string {
	width := make([]int, len(header))
	for c, h := range header {
		width[c] = lipgloss.Width(h)
	}
	for _, r := range rows {
		for c, cell := range r {
			width[c] = max(width[c], lipgloss.Width(cell))
		}
	}
	pad := func(s string, c int) string {
		if c == len(header)-1 {
			return s
		}
		return s + strings.Repeat(" ", width[c]-lipgloss.Width(s)+2)
	}
	var out strings.Builder
	for c, h := range header {
		out.WriteString(headStyle.Render(pad(h, c)))
	}
	out.WriteByte('\n')
	for r, row := range rows {
		for c, cell := range row {
			out.WriteString(cellStyle(r, c).Render(pad(cell, c)))
		}
		out.WriteByte('\n')
	}
	return out.String()
}
