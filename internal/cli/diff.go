package cli

import "strings"

// diffContext is how many unchanged lines are shown around a change.
const diffContext = 2

// lineDiff describes how text a becomes text b, one line per row: "-" for a
// removed line, "+" for an added one, and a little unchanged context.
func lineDiff(a, b string) string {
	x, y := splitLines(a), splitLines(b)

	// lcs[i][j] is the length of the longest common subsequence of x[i:] and y[j:].
	lcs := make([][]int, len(x)+1)
	for i := range lcs {
		lcs[i] = make([]int, len(y)+1)
	}
	for i := len(x) - 1; i >= 0; i-- {
		for j := len(y) - 1; j >= 0; j-- {
			if x[i] == y[j] {
				lcs[i][j] = lcs[i+1][j+1] + 1
			} else {
				lcs[i][j] = max(lcs[i+1][j], lcs[i][j+1])
			}
		}
	}

	type row struct {
		mark byte
		text string
	}
	var rows []row
	i, j := 0, 0
	for i < len(x) || j < len(y) {
		switch {
		case i < len(x) && j < len(y) && x[i] == y[j]:
			rows = append(rows, row{' ', x[i]})
			i, j = i+1, j+1
		case i < len(x) && (j == len(y) || lcs[i+1][j] >= lcs[i][j+1]):
			rows = append(rows, row{'-', x[i]})
			i++
		default:
			rows = append(rows, row{'+', y[j]})
			j++
		}
	}

	// Keep changed rows and the context around them.
	keep := make([]bool, len(rows))
	for n, r := range rows {
		if r.mark == ' ' {
			continue
		}
		for k := max(0, n-diffContext); k <= min(len(rows)-1, n+diffContext); k++ {
			keep[k] = true
		}
	}
	var out strings.Builder
	skipped := false
	for n, r := range rows {
		if !keep[n] {
			skipped = true
			continue
		}
		if skipped && out.Len() > 0 {
			out.WriteString("  ...\n")
		}
		skipped = false
		out.WriteByte(r.mark)
		out.WriteByte(' ')
		out.WriteString(r.text)
		out.WriteByte('\n')
	}
	return out.String()
}

func splitLines(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(strings.TrimSuffix(s, "\n"), "\n")
}
