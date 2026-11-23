package console

import "testing"

func TestComplete(t *testing.T) {
	words := []string{"/attach", "/config", "/logs", "/ps", "/stop", "/start"}
	tests := []struct {
		line, want string
		ok         bool
	}{
		{"/a", "/attach ", true},
		{"/p", "/ps ", true},
		{"/s", "/st", true},
		{"/st", "", false},
		{"/x", "", false},
		{"hello", "", false},
	}
	for _, tt := range tests {
		got, ok := complete(tt.line, words)
		if ok != tt.ok || (ok && got != tt.want) {
			t.Errorf("complete(%q) = %q, %v; want %q, %v", tt.line, got, ok, tt.want, tt.ok)
		}
	}
}
