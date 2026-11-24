package policy

import "testing"

func TestMatch(t *testing.T) {
	tests := []struct {
		pattern, subject string
		want             bool
	}{
		{"git status", "git status", true},
		{"git status", "git status --short", false},
		{"git status*", "git status --short", true},
		{"git status*", "git status", true},
		{"git status*", "git stash", false},
		{"*", "anything at all", true},
		{"git * --force*", "git push --force origin main", true},
		{"git * --force*", "git push origin main", false},
		{"*rm -rf*", "cd /tmp && rm -rf build", true},
		{"npm test", "npm test && rm -rf /", false},
		{"Edit web/*", "Edit web/app.tsx", true},
		{"Edit web/*", "Edit api/server.go", false},
		{"a*a", "a", false}, // the two ends may not share a character
		{"a*a", "aa", true},
		{"", "", false},
		{"", "x", false},
		{"  git status* ", "git status", true},
	}
	for _, tt := range tests {
		if got := Match(tt.pattern, tt.subject); got != tt.want {
			t.Errorf("Match(%q, %q) = %v, want %v", tt.pattern, tt.subject, got, tt.want)
		}
	}
}

func TestDecide(t *testing.T) {
	ask := Rules{Allow: []string{"git status*", "npm *"}, Deny: []string{"git push*", "npm publish*"}}
	auto := Rules{Mode: ModeAuto, Deny: []string{"git push*"}}
	tests := []struct {
		name    string
		rules   Rules
		subject string
		want    Decision
	}{
		{"allowed by a rule", ask, "git status --short", Allow},
		{"denied by a rule", ask, "git push origin main", Deny},
		{"deny wins over allow", ask, "npm publish", Deny},
		{"no rule, the user is asked", ask, "make deploy", Ask},
		{"no rules at all", Rules{}, "ls", Ask},
		{"an unknown mode asks", Rules{Mode: "yes"}, "ls", Ask},
		{"auto allows what no rule covers", auto, "make deploy", Allow},
		{"auto still denies", auto, "git push --force", Deny},
		{"space around the subject", ask, "  git status\n", Allow},
	}
	for _, tt := range tests {
		if got := tt.rules.Decide(tt.subject); got != tt.want {
			t.Errorf("%s: Decide(%q) = %v, want %v", tt.name, tt.subject, got, tt.want)
		}
	}
}

func TestCommands(t *testing.T) {
	tests := []struct {
		subject string
		want    []string
	}{
		{"git status", []string{"git status"}},
		{"git status --short | head; git log -1 && ls || pwd", []string{"git status --short", "head", "git log -1", "ls", "pwd"}},
		{"grep -n -E 'status|stopped_at' STATE.md | head -5", []string{"grep -n -E 'status|stopped_at' STATE.md", "head -5"}},
		{`echo "a; b" ; echo c`, []string{`echo "a; b"`, "echo c"}},
		{"ls .planning 2>/dev/null | head -20", []string{"ls .planning 2>/dev/null", "head -20"}},
		{"make build 2>&1 | tail", []string{"make build 2>&1", "tail"}},
		{"sleep 5 & rm -rf x", []string{"sleep 5", "rm -rf x"}},
		{"git status\nrm -rf x", []string{"git status", "rm -rf x"}},
		{`echo a\;b`, []string{`echo a\;b`}},
		{"Edit web/app.tsx", []string{"Edit web/app.tsx"}},
		{"  ", nil},
	}
	for _, tt := range tests {
		got := Commands(tt.subject)
		if len(got) != len(tt.want) {
			t.Errorf("Commands(%q) = %q, want %q", tt.subject, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("Commands(%q) = %q, want %q", tt.subject, got, tt.want)
				break
			}
		}
	}
}

func TestDecideLooksAtEveryCommandInARequest(t *testing.T) {
	r := Rules{
		Allow: []string{"git status*", "git log*", "head*", "grep *", "ls*", "cat *", "echo *"},
		Deny:  []string{"git push*", "*rm -rf*", "sudo *"},
	}
	tests := []struct {
		name    string
		subject string
		want    Decision
	}{
		{"every command allowed", "git status --short | head; git log --oneline -1 main; grep -n -E 'a|b' f | head -5; ls x 2>/dev/null | head -20", Allow},
		{"an allowed start does not carry the rest", "git status; make deploy", Ask},
		{"a denied command anywhere denies", "git status && git push origin main", Deny},
		{"a denied command after a pipe", "ls | sudo tee /etc/hosts", Deny},
		{"deny matched across the whole request", "git status; rm -rf build", Deny},
		{"writing to a file is not what the pattern allowed", "echo x > ~/.zshrc", Ask},
		{"appending too", "cat a >> b", Ask},
		{"a command inside a command", "git status $(make deploy)", Ask},
		{"backticks too", "echo `make deploy`", Ask},
		{"reading from a file into it", "cat < /etc/passwd", Ask},
		{"sending output nowhere is fine", "ls missing 2>/dev/null", Allow},
		{"joining the output streams is fine", "git status 2>&1 | head", Allow},
		{"an operator inside quotes is text", `grep "a && b" f`, Allow},
	}
	for _, tt := range tests {
		if got := r.Decide(tt.subject); got != tt.want {
			t.Errorf("%s: Decide(%q) = %v, want %v", tt.name, tt.subject, got, tt.want)
		}
	}
	// Auto mode allows what is not denied, compound or not.
	auto := Rules{Mode: ModeAuto, Deny: []string{"git push*"}}
	if got := auto.Decide("make build > log.txt; make deploy"); got != Allow {
		t.Errorf("auto: got %v, want Allow", got)
	}
	if got := auto.Decide("make build; git push"); got != Deny {
		t.Errorf("auto with a denied command: got %v, want Deny", got)
	}
}
