package cli

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/Selkomark/H2H/internal/config"
)

// TestRealHarnessesRemember checks, with the harnesses that are installed
// and signed in, that the console's conversation carries from one message
// to the next. It makes real model calls, so it runs only when asked:
//
//	H2H_REAL_HARNESSES=1 go test -run TestRealHarnessesRemember ./internal/cli/
//
// Each harness is a subtest, so the result is reported per harness.
func TestRealHarnessesRemember(t *testing.T) {
	if os.Getenv("H2H_REAL_HARNESSES") == "" {
		t.Skip("makes real model calls; set H2H_REAL_HARNESSES=1 to run it")
	}
	shipped, err := config.Parse(config.Template())
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range shipped.Names() {
		t.Run(name, func(t *testing.T) {
			if _, err := exec.LookPath(shipped.Harnesses[name].Command); err != nil {
				t.Skipf("%s is not installed", name)
			}
			conf := strings.Replace(config.Template(), `default_harness = "claude"`, `default_harness = "`+name+`"`, 1)
			e := consoleEnv(t, conf)
			e.environ = os.Environ()
			var out bytes.Buffer
			e.out, e.errw = &out, &out
			ctx := context.Background()
			if err := e.converse(ctx, "Remember the word heron. Reply OK."); err != nil {
				t.Fatalf("first message: %v\n%s", err, out.String())
			}
			id := e.currentTalk().ID
			if id == "" {
				t.Fatalf("no conversation was kept after the first message:\n%s", out.String())
			}
			out.Reset()
			if err := e.converse(ctx, "What word did I ask you to remember? One word."); err != nil {
				t.Fatalf("second message: %v\n%s", err, out.String())
			}
			if got := e.currentTalk().ID; got != id {
				t.Errorf("the conversation changed from %s to %s", id, got)
			}
			if !strings.Contains(strings.ToLower(out.String()), "heron") {
				t.Errorf("the second answer does not remember the word:\n%s", out.String())
			}
			t.Logf("%s, conversation %s, answered: %s", name, id, strings.TrimSpace(out.String()))
		})
	}
}
