package console

import (
	"strings"
	"unicode"

	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

const (
	// headerGap is the least space between two parts of the header.
	headerGap = 3
	// minCutPath is the fewest columns a path is shown in once the name
	// of its folder has to be cut too. Less is a fragment that names
	// nothing, so the path is left out instead.
	minCutPath = 12

	ellipsis = "…"
)

// headerRow is the one row at the top of the page, exactly as wide as it
// is given or narrower: the tabs on the left, the title in the middle and
// the path on the right.
//
// When they do not all fit, they give way in this order. The path loses
// directories from its start. The title then leaves the middle for the
// room between the tabs and the path, and after that goes. Last, the name
// of the path's folder is cut from its start, and then the path goes. The
// tabs stay, and are cut only when the row cannot hold them alone.
func (m *model) headerRow(width int) string {
	tabs := m.tabRow()
	tabsWidth := lipgloss.Width(tabs)
	if tabsWidth >= width {
		return clip(tabs, width)
	}
	title, titleWidth := m.o.Title, lipgloss.Width(m.o.Title)
	path := printable(m.o.Path)

	// What is free for the title and the path starts here.
	free := tabsWidth + headerGap
	middle := (width - titleWidth) / 2
	titleAt, shown := -1, ""
	// The room for a path beside a title that starts as far left as it can.
	beside := width - free - titleWidth - headerGap
	switch {
	case path == "":
		if titleWidth > 0 && free+titleWidth <= width {
			titleAt = max(free, middle)
		}
	case titleWidth > 0 && middle >= free && wholeName(path, width-middle-titleWidth-headerGap) != "":
		titleAt, shown = middle, wholeName(path, width-middle-titleWidth-headerGap)
	case titleWidth > 0 && wholeName(path, beside) != "":
		shown = wholeName(path, beside)
		// As near the middle as the tabs and the path allow.
		titleAt = min(max(free, middle), width-lipgloss.Width(shown)-headerGap-titleWidth)
	default:
		shown = shortenPath(path, width-free)
		// With no room for any of the path, the title may still fit.
		if shown == "" && titleWidth > 0 && free+titleWidth <= width {
			titleAt = max(free, min(middle, width-titleWidth))
		}
	}

	row, at := tabs, tabsWidth
	put := func(x int, text string) {
		row += strings.Repeat(" ", x-at) + text
		at = x + lipgloss.Width(text)
	}
	if titleAt >= 0 {
		put(titleAt, title)
	}
	if shown != "" {
		put(width-lipgloss.Width(shown), shown)
	}
	return row
}

// tabRow draws the tabs.
func (m *model) tabRow() string {
	th := m.o.Theme
	pad := strings.Repeat(" ", tabPad)
	names := m.tabNames()
	tabs := make([]string, len(names))
	// The tab on screen is a filled label; the others are padded to the
	// same width, so switching never shifts the row.
	for n, name := range names {
		style, warn := th.Dim, th.Warn
		if n == m.tab {
			style, warn = th.Accent.Reverse(true).Bold(true), th.Warn.Reverse(true).Bold(true)
		}
		count, attention := m.tabCount(n)
		switch {
		case count == "":
			tabs[n] = style.Render(pad + name + pad)
		case attention:
			tabs[n] = style.Render(pad+name+" ") + warn.Render(countLabel(count)) + style.Render(pad)
		default:
			tabs[n] = style.Render(pad + name + " " + countLabel(count) + pad)
		}
	}
	return strings.Join(tabs, tabSep)
}

// tabNames are the names of the tabs, left to right.
func (m *model) tabNames() []string {
	names := []string{transcriptTab}
	for _, v := range m.o.Views {
		names = append(names, v.Name)
	}
	return names
}

// tabLabels are the texts of the tabs as drawn, without their padding:
// each name, with its count if its view has one.
func (m *model) tabLabels() []string {
	labels := m.tabNames()
	for n := range labels {
		if count, _ := m.tabCount(n); count != "" {
			labels[n] += " " + countLabel(count)
		}
	}
	return labels
}

// tabCount is what the view of tab n counts, and whether any of it needs
// the user.
func (m *model) tabCount(n int) (count string, attention bool) {
	if n == 0 || n > len(m.views) {
		return "", false
	}
	return m.views[n-1].Count, m.views[n-1].Attention
}

func countLabel(count string) string { return "(" + count + ")" }

// wholeName shortens a path to a width by leaving out directories from its
// start, and returns "" when that cannot keep the folder's own name whole.
func wholeName(path string, width int) string {
	if ansi.StringWidth(path) <= width {
		return path
	}
	parts := strings.Split(strings.TrimRight(path, "/"), "/")
	for from := 1; from < len(parts); from++ {
		if short := ellipsis + "/" + strings.Join(parts[from:], "/"); ansi.StringWidth(short) <= width {
			return short
		}
	}
	if name := parts[len(parts)-1]; name != "" && ansi.StringWidth(name) <= width {
		return name
	}
	return ""
}

// shortenPath shortens a path to a width, keeping its end, which names the
// project: whole directories go first, and then the start of the folder's
// name. It returns "" when too little would be left to tell what it is.
func shortenPath(path string, width int) string {
	if short := wholeName(path, width); short != "" || width < minCutPath {
		return short
	}
	// Widths are of the screen, where a character may take two columns, so
	// more is cut until what is left fits.
	total := ansi.StringWidth(path)
	for cut := total - width + 1; cut < total; cut++ {
		if short := ansi.TruncateLeft(path, cut, ellipsis); ansi.StringWidth(short) <= width {
			return short
		}
	}
	return ""
}

// printable drops control characters, which a directory's name may hold
// and which would break the row.
func printable(s string) string {
	return strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
}
