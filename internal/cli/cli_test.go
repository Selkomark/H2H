package cli

import (
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/lipgloss"
)

func TestLineDiff(t *testing.T) {
	tests := []struct {
		name, a, b, want string
	}{
		{"same", "a\nb\n", "a\nb\n", ""},
		{"change one line", "a\nb\nc\n", "a\nB\nc\n", "  a\n- b\n+ B\n  c\n"},
		{"add at end", "a\n", "a\nb\n", "  a\n+ b\n"},
		{"remove first", "a\nb\n", "b\n", "- a\n  b\n"},
		{"from empty", "", "a\n", "+ a\n"},
		{
			"context is limited",
			"1\n2\n3\n4\n5\n6\n7\n8\n9\n", "1\n2\n3\n4\n5\n6\n7\n8\nX\n",
			"  7\n  8\n- 9\n+ X\n",
		},
		{
			"gap between changes",
			"a\n1\n2\n3\n4\n5\n6\nz\n", "A\n1\n2\n3\n4\n5\n6\nZ\n",
			"- a\n+ A\n  1\n  2\n  ...\n  5\n  6\n- z\n+ Z\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lineDiff(tt.a, tt.b); got != tt.want {
				t.Errorf("lineDiff(%q, %q) =\n%s\nwant\n%s", tt.a, tt.b, got, tt.want)
			}
		})
	}
}

func TestDisplayPath(t *testing.T) {
	tests := []struct {
		path, home string
		limit      int
		want       string
	}{
		{"/home/u/code/app", "/home/u", 60, "~/code/app"},
		{"/home/u", "/home/u", 60, "~"},
		{"/home/user2/app", "/home/u", 60, "/home/user2/app"},
		{"/srv/app", "", 60, "/srv/app"},
		{"/a/very/long/path/to/project", "", 12, "...o/project"},
	}
	for _, tt := range tests {
		if got := displayPath(tt.path, tt.home, tt.limit); got != tt.want {
			t.Errorf("displayPath(%q, %q, %d) = %q, want %q", tt.path, tt.home, tt.limit, got, tt.want)
		}
	}
}

func TestChangeSummary(t *testing.T) {
	tests := []struct {
		name    string
		changes []string
		want    string
	}{
		{"one", []string{"a.txt"}, "The workspace of claude-1 has 1 uncommitted change:\n  a.txt\n"},
		{"a few", []string{"a", "b/"}, "The workspace of claude-1 has 2 uncommitted changes:\n  a\n  b/\n"},
		{
			"more than are listed", []string{"1", "2", "3", "4", "5", "6", "7"},
			"The workspace of claude-1 has 7 uncommitted changes:\n  1\n  2\n  3\n  4\n  5\n  and 2 more\n",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := changeSummary("claude-1", tt.changes); got != tt.want {
				t.Errorf("changeSummary() =\n%s\nwant\n%s", got, tt.want)
			}
		})
	}
}

func TestReadLineLeavesTheRest(t *testing.T) {
	in := strings.NewReader("/rm a\ny\nlast")
	for _, want := range []string{"/rm a", "y", "last"} {
		if got, ok := readLine(in); !ok || got != want {
			t.Errorf("readLine() = %q, %v; want %q, true", got, ok, want)
		}
	}
	if got, ok := readLine(in); ok {
		t.Errorf("readLine() at the end = %q, true; want false", got)
	}
}

func TestUnfence(t *testing.T) {
	tests := []struct {
		name, reply, want string
	}{
		{"plain", "a = 1\nb = 2\n", "a = 1\nb = 2\n"},
		{"fenced", "Here you go:\n```toml\na = 1\n```\nDone.", "a = 1\n"},
		{"fence never closed", "```\na = 1\nb = 2", "a = 1\nb = 2\n"},
		{"surrounding space", "\n\n a = 1 \n\n", "a = 1\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := unfence(tt.reply); got != tt.want {
				t.Errorf("unfence(%q) = %q, want %q", tt.reply, got, tt.want)
			}
		})
	}
}

func TestEditorCommand(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }
	found := func(names ...string) func(string) (string, error) {
		return func(n string) (string, error) {
			if slices.Contains(names, n) {
				return "/usr/bin/" + n, nil
			}
			return "", errors.New("not found")
		}
	}
	tests := []struct {
		name    string
		env     map[string]string
		found   []string
		want    []string
		wantErr bool
	}{
		{"VISUAL wins", map[string]string{"VISUAL": "code -w", "EDITOR": "vi"}, nil, []string{"code", "-w"}, false},
		{"EDITOR", map[string]string{"EDITOR": "vim"}, nil, []string{"vim"}, false},
		{"falls back to nano", nil, []string{"nano", "vi"}, []string{"nano"}, false},
		{"falls back to vi", nil, []string{"vi"}, []string{"vi"}, false},
		{"none", nil, nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := editorCommand(env(tt.env), found(tt.found...))
			if (err != nil) != tt.wantErr || !slices.Equal(got, tt.want) {
				t.Errorf("editorCommand() = %v, %v; want %v, error %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestMix(t *testing.T) {
	a, b := rgb{0, 100, 200}, rgb{100, 200, 0}
	tests := []struct {
		t    float64
		want rgb
	}{
		{0, a}, {1, b}, {0.5, rgb{50, 150, 100}}, {-1, a}, {2, b},
	}
	for _, tt := range tests {
		if got := mix(a, b, tt.t); got != tt.want {
			t.Errorf("mix(a, b, %v) = %v, want %v", tt.t, got, tt.want)
		}
	}
}

func TestParseHex(t *testing.T) {
	if got := parseHex("#4DEB9A"); got != (rgb{0x4D, 0xEB, 0x9A}) {
		t.Errorf("parseHex = %v", got)
	}
	if got := (rgb{0x4D, 0xEB, 0x9A}).color(); string(got) != "#4DEB9A" {
		t.Errorf("color() = %q", got)
	}
}

func TestBarColorSweeps(t *testing.T) {
	// With the light off the bar, the ends are the plain gradient.
	if got := barColor(0, 0); got != barStart {
		t.Errorf("left end at the start of a sweep = %v, want %v", got, barStart)
	}
	if got := barColor(1, 0); got != barEnd {
		t.Errorf("right end at the start of a sweep = %v, want %v", got, barEnd)
	}
	// Half way through, the middle is lit and the ends are not.
	mid, plain := barColor(0.5, 0.5), mix(barStart, barEnd, 0.5)
	if mid == plain || mid.r <= plain.r {
		t.Errorf("middle at half sweep = %v; want it lighter than the plain %v", mid, plain)
	}
	if got := barColor(0, 0.5); got != barStart {
		t.Errorf("left end at half sweep = %v, want it unlit: %v", got, barStart)
	}
	// The light has left by the end, so the next sweep starts without a jump.
	if got := barColor(1, 1); got != barEnd {
		t.Errorf("right end at the end of a sweep = %v, want %v", got, barEnd)
	}
}

func TestBarFillsTheWidth(t *testing.T) {
	st := newStyles()
	for _, width := range []int{0, 1, 10, 80, 200} {
		for _, since := range []time.Duration{0, time.Second, 2500 * time.Millisecond} {
			got := st.bar(" H2H  claude-1   ctrl+\\ return to h2h", width, since)
			if w := lipgloss.Width(got); w != width {
				t.Errorf("bar at width %d after %v is %d wide", width, since, w)
			}
		}
	}
	if got := st.bar(" H2H  claude-1", 40, 0); !strings.Contains(got, "claude-1") && !strings.Contains(stripColour(got), "claude-1") {
		t.Errorf("bar lost its text: %q", got)
	}
}

func TestGradientKeepsTheText(t *testing.T) {
	if got := stripColour(newStyles().gradient("H2H")); got != "H2H" {
		t.Errorf("gradient text = %q, want H2H", got)
	}
	if got := plainStyles().gradient("H2H"); got != "H2H" {
		t.Errorf("plain gradient = %q, want it unchanged", got)
	}
}

// stripColour removes colour codes, so text can be compared.
func stripColour(s string) string {
	return regexp.MustCompile(`\x1b\[[0-9;]*m`).ReplaceAllString(s, "")
}
