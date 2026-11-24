package console

import (
	"slices"
	"sort"
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// Graph is a view drawn as boxes joined by arrows, left to right: what
// comes first on the left, and what depends on it to its right. The user
// moves between the boxes and inspects one to read its Detail.
type Graph struct {
	Nodes []Node
	Edges []Edge
}

// Node is one box of a graph.
type Node struct {
	ID    string   // what the selection follows; stable between refreshes
	Title string   // the first line in the box
	Lines []string // further lines in the box, drawn fainter
	// Detail returns what is shown when the node is inspected, drawn for
	// a width in columns: the part worth reading first.
	Detail func(width int) string
	// More, if set, returns the rest, which is shown under Detail when
	// the user asks for it.
	More func(width int) string
}

// Edge is an arrow from one node to another: To comes after From.
type Edge struct{ From, To string }

// Sizes of the drawing, in cells.
const (
	graphRowGap   = 1 // blank rows between two boxes of a column
	graphLane     = 2 // columns each line takes in the gap between two columns of boxes
	graphGapSides = 3 // columns of a gap beside its lines: one to leave a box, two to reach the arrow
	graphMinGap   = 4 // the gap between two columns of boxes that no line crosses
	boxPadding    = 4 // the border and one space on each side of a box's text
)

// How a cell of the drawing is styled.
const (
	inkPlain = iota
	inkFaint
	inkTitle
	inkSelected
)

// The sides of a cell a line leaves it by.
const (
	north = 1 << iota
	east
	south
	west
)

// lineRunes is the character for a cell, by the sides its lines leave by.
// Corners are rounded, as the boxes are.
var lineRunes = map[int]rune{
	east: '─', west: '─', east | west: '─',
	north: '│', south: '│', north | south: '│',
	east | south: '╭', west | south: '╮', north | east: '╰', north | west: '╯',
	north | east | south: '├', north | west | south: '┤',
	east | south | west: '┬', north | east | west: '┴',
	north | east | south | west: '┼',
}

// spot is one thing that takes a place in a column: a node's box, or the
// point where an arrow that skips the column passes through it.
type spot struct {
	node   *Node  // nil for a pass-through
	from   string // for a pass-through: the ends of the arrow it belongs to
	to     string
	column int
	x, y   int // top-left cell
	w, h   int
}

// row is the row an arrow meets the spot at.
func (s spot) row() int { return s.y + s.h/2 }

// link is one step of an arrow, between spots in neighbouring columns.
type link struct {
	a, b     int    // indices of its spots
	from, to string // the nodes the whole arrow joins
}

// drawing is a graph laid out on a grid of cells.
type drawing struct {
	spots         []spot
	links         []link
	width, height int
}

// layoutGraph places the nodes in columns and the arrows between them.
//
// A node goes in the column after the last of the nodes it comes after, so
// every arrow points right. An arrow that skips a column is given a place
// in it to pass through, which keeps it clear of that column's boxes.
func layoutGraph(g Graph) drawing {
	index := map[string]int{}
	for n := range g.Nodes {
		index[g.Nodes[n].ID] = n
	}
	var edges []Edge
	for _, e := range g.Edges {
		_, a := index[e.From]
		_, b := index[e.To]
		if a && b && e.From != e.To && !slices.Contains(edges, e) {
			edges = append(edges, e)
		}
	}

	// The column of each node: one past its latest predecessor. The loop
	// is bounded, so arrows that run in a circle cannot hang it.
	column := make([]int, len(g.Nodes))
	for range g.Nodes {
		for _, e := range edges {
			if a, b := index[e.From], index[e.To]; column[b] < column[a]+1 {
				column[b] = min(column[a]+1, len(g.Nodes)-1)
			}
		}
	}

	var d drawing
	at := map[string]int{} // node ID to its spot
	for n := range g.Nodes {
		node := &g.Nodes[n]
		w := lipgloss.Width(node.Title)
		for _, l := range node.Lines {
			w = max(w, lipgloss.Width(l))
		}
		at[node.ID] = len(d.spots)
		d.spots = append(d.spots, spot{node: node, column: column[n], w: w + boxPadding, h: 3 + len(node.Lines)})
	}
	for _, e := range edges {
		// Arrows that run in a circle leave one that would point back.
		// It is not drawn: every arrow here points right.
		if column[index[e.To]] <= column[index[e.From]] {
			continue
		}
		prev := at[e.From]
		for c := column[index[e.From]] + 1; c < column[index[e.To]]; c++ {
			d.spots = append(d.spots, spot{from: e.From, to: e.To, column: c, h: 1})
			d.links = append(d.links, link{a: prev, b: len(d.spots) - 1, from: e.From, to: e.To})
			prev = len(d.spots) - 1
		}
		d.links = append(d.links, link{a: prev, b: at[e.To], from: e.From, to: e.To})
	}

	// The spots of each column, top to bottom. A spot is placed near the
	// ones its arrows come from, which keeps the arrows from crossing more
	// than they have to.
	columns := 0
	for _, s := range d.spots {
		columns = max(columns, s.column+1)
	}
	order := make([][]int, columns)
	for n, s := range d.spots {
		order[s.column] = append(order[s.column], n)
	}
	position := make([]float64, len(d.spots))
	for c := range order {
		if c > 0 {
			for _, n := range order[c] {
				sum, count := 0.0, 0
				for _, l := range d.links {
					if l.b == n {
						sum += position[l.a]
						count++
					}
				}
				if count > 0 {
					position[n] = sum / float64(count)
				}
			}
			sort.SliceStable(order[c], func(i, j int) bool { return position[order[c][i]] < position[order[c][j]] })
		}
		for row, n := range order[c] {
			position[n] = float64(row)
		}
	}

	// Sizes and places. The boxes of a column are equally wide, and each
	// column is centred on the tallest.
	heights := make([]int, columns)
	widths := make([]int, columns)
	for c, spots := range order {
		for _, n := range spots {
			widths[c] = max(widths[c], d.spots[n].w)
			heights[c] += d.spots[n].h + graphRowGap
		}
		heights[c] -= graphRowGap
		d.height = max(d.height, heights[c])
	}
	x := 0
	for c, spots := range order {
		y := (d.height - heights[c]) / 2
		for _, n := range spots {
			s := &d.spots[n]
			s.x, s.y, s.w = x, y, widths[c]
			y += s.h + graphRowGap
		}
		x += widths[c]
		d.width = x
		if c+1 < columns {
			x += d.gap(c, order)
		}
	}
	return d
}

// sources returns the spots of a column that arrows leave, top to bottom.
// Each gets a lane of its own in the gap to the next column.
func (d drawing) sources(c int, order [][]int) []int {
	var out []int
	for _, n := range order[c] {
		if slices.ContainsFunc(d.links, func(l link) bool { return l.a == n }) {
			out = append(out, n)
		}
	}
	return out
}

// gap is the width of the space between column c and the next.
func (d drawing) gap(c int, order [][]int) int {
	return max(graphMinGap, graphGapSides+graphLane*len(d.sources(c, order)))
}

// columnsOf returns the spots of each column, top to bottom.
func (d drawing) columnsOf() [][]int {
	var order [][]int
	for n, s := range d.spots {
		for len(order) <= s.column {
			order = append(order, nil)
		}
		order[s.column] = append(order[s.column], n)
	}
	for c := range order {
		sort.SliceStable(order[c], func(i, j int) bool { return d.spots[order[c][i]].y < d.spots[order[c][j]].y })
	}
	return order
}

// glyph is one character of the drawing.
type glyph struct {
	r     rune
	sides int // for a line: the sides it leaves by
	ink   int
}

// render draws the laid-out graph. The selected node, and the arrows into
// and out of it, stand out: they are its lineage.
func (d drawing) render(th Theme, selected string) string {
	if len(d.spots) == 0 {
		return ""
	}
	grid := make([][]glyph, d.height)
	for y := range grid {
		grid[y] = make([]glyph, d.width)
	}
	set := func(x, y int, r rune, ink int) {
		if y >= 0 && y < d.height && x >= 0 && x < d.width {
			grid[y][x] = glyph{r: r, ink: ink}
		}
	}
	// join draws a line from one cell to a neighbouring one.
	join := func(x1, y1, x2, y2, ink int) {
		mark := func(x, y, side int) {
			if y < 0 || y >= d.height || x < 0 || x >= d.width {
				return
			}
			c := &grid[y][x]
			c.sides |= side
			c.ink = max(c.ink, ink)
		}
		switch {
		case x2 > x1:
			mark(x1, y1, east)
			mark(x2, y2, west)
		case x2 < x1:
			mark(x1, y1, west)
			mark(x2, y2, east)
		case y2 > y1:
			mark(x1, y1, south)
			mark(x2, y2, north)
		case y2 < y1:
			mark(x1, y1, north)
			mark(x2, y2, south)
		}
	}
	// path draws a line through corner points, cell by cell.
	path := func(ink int, points ...[2]int) {
		for n := 1; n < len(points); n++ {
			x, y := points[n-1][0], points[n-1][1]
			for x != points[n][0] || y != points[n][1] {
				nx, ny := x+sign(points[n][0]-x), y+sign(points[n][1]-y)
				join(x, y, nx, ny, ink)
				x, y = nx, ny
			}
		}
	}

	order := d.columnsOf()
	lanes := map[int]int{} // a source spot to the column of its lane
	for c := range order {
		for n, s := range d.sources(c, order) {
			lanes[s] = d.spots[s].x + d.spots[s].w + 1 + graphLane*n
		}
	}
	for _, l := range d.links {
		a, b := d.spots[l.a], d.spots[l.b]
		ink := inkFaint
		if selected != "" && (l.from == selected || l.to == selected) {
			ink = inkSelected
		}
		start, end := a.x+a.w, b.x-1
		// Out of the source, along its lane to the target's row, and in.
		path(ink, [2]int{start, a.row()}, [2]int{lanes[l.a], a.row()}, [2]int{lanes[l.a], b.row()}, [2]int{end, b.row()})
		join(start, a.row(), start-1, a.row(), ink) // it starts at the box
		if a.row() < d.height && start < d.width {
			grid[a.row()][start].sides &^= west // without running into it
		}
		if b.node != nil {
			set(end, b.row(), '▶', ink)
		} else {
			// Through a column it skips, straight across.
			path(ink, [2]int{end, b.row()}, [2]int{b.x + b.w, b.row()})
		}
	}
	for y := range grid {
		for x := range grid[y] {
			if c := &grid[y][x]; c.sides != 0 && c.r == 0 {
				c.r = lineRunes[c.sides]
			}
		}
	}

	for _, s := range d.spots {
		if s.node == nil {
			continue
		}
		border, title := inkFaint, inkTitle
		if s.node.ID == selected {
			border, title = inkSelected, inkSelected
		}
		for x := s.x + 1; x < s.x+s.w-1; x++ {
			set(x, s.y, '─', border)
			set(x, s.y+s.h-1, '─', border)
		}
		for y := s.y + 1; y < s.y+s.h-1; y++ {
			set(s.x, y, '│', border)
			set(s.x+s.w-1, y, '│', border)
			for x := s.x + 1; x < s.x+s.w-1; x++ {
				set(x, y, ' ', inkPlain)
			}
		}
		set(s.x, s.y, '╭', border)
		set(s.x+s.w-1, s.y, '╮', border)
		set(s.x, s.y+s.h-1, '╰', border)
		set(s.x+s.w-1, s.y+s.h-1, '╯', border)
		write := func(row int, text string, ink int) {
			x := s.x + 2
			for _, r := range text {
				set(x, s.y+row, r, ink)
				x += lipgloss.Width(string(r))
			}
		}
		write(1, s.node.Title, title)
		for n, l := range s.node.Lines {
			write(2+n, l, inkFaint)
		}
	}

	inks := map[int]lipgloss.Style{
		inkPlain:    lipgloss.NewStyle(),
		inkFaint:    th.Dim,
		inkTitle:    lipgloss.NewStyle().Bold(true),
		inkSelected: th.Accent.Bold(true),
	}
	var out []string
	for _, row := range grid {
		var line, run strings.Builder
		ink := inkPlain
		flush := func() {
			if run.Len() > 0 {
				line.WriteString(inks[ink].Render(run.String()))
				run.Reset()
			}
		}
		for _, c := range row {
			r := c.r
			if r == 0 {
				r = ' '
			}
			if c.ink != ink {
				flush()
				ink = c.ink
			}
			run.WriteRune(r)
		}
		flush()
		out = append(out, strings.TrimRight(line.String(), " "))
	}
	return strings.Join(out, "\n")
}

func sign(n int) int {
	switch {
	case n > 0:
		return 1
	case n < 0:
		return -1
	}
	return 0
}

// nodeAt returns the node whose box is at a cell of the drawing.
func (d drawing) nodeAt(x, y int) (string, bool) {
	for _, s := range d.spots {
		if s.node != nil && x >= s.x && x < s.x+s.w && y >= s.y && y < s.y+s.h {
			return s.node.ID, true
		}
	}
	return "", false
}

// neighbour returns the node to move the selection to from one node, in a
// direction: dx of -1 or 1 for the column before or after, where the
// nearest node by row is taken, and dy for the node above or below. It
// returns the node itself when there is none that way.
func (d drawing) neighbour(id string, dx, dy int) string {
	at := 0
	var here *spot
	for n := range d.spots {
		if s := &d.spots[n]; s.node != nil && s.node.ID == id {
			here = s
		}
	}
	if here == nil {
		// Nothing was selected: start where the flow starts, at the top
		// of the first column.
		first := id
		for n, s := range d.spots {
			if s.node == nil {
				continue
			}
			if first == id || s.column < d.spots[at].column || (s.column == d.spots[at].column && s.y < d.spots[at].y) {
				first, at = s.node.ID, n
			}
		}
		return first
	}
	best, bestScore := id, 1<<30
	for _, s := range d.spots {
		if s.node == nil || s.node.ID == id {
			continue
		}
		var score int
		switch {
		case dx != 0 && sign(s.column-here.column) == dx:
			// The nearest column that way, then the nearest row.
			score = abs(s.column-here.column)*1000 + abs(s.row()-here.row())
		case dy != 0 && s.column == here.column && sign(s.y-here.y) == dy:
			score = abs(s.y - here.y)
		default:
			continue
		}
		if score < bestScore {
			best, bestScore = s.node.ID, score
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// DrawGraph draws a graph as text, with nothing selected, for output that
// is not a view of the console.
func DrawGraph(g Graph, th Theme) string {
	return layoutGraph(g).render(th, "")
}
