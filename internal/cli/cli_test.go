package cli

import (
	"errors"
	"slices"
	"testing"
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
