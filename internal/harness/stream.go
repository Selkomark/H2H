package harness

import (
	"bufio"
	"context"
	"encoding/json"
	"io"
	"os/exec"
	"strings"

	"github.com/Selkomark/H2H/internal/config"
)

// AskStream runs the harness once to answer prompt, like Ask, and writes
// the answer to stdout while it is being written. A harness that has no
// stream_args is run with Ask, and its answer arrives when it is complete.
//
// With stream_args the harness prints JSON, one object per line. The text
// is picked out of the lines that carry it, as its config entry says, and
// everything else the harness reports there (tool use, usage, its own
// thinking) is left out.
func AskStream(ctx context.Context, h config.Harness, environ []string, dir, prompt string, stdout, stderr io.Writer) error {
	if !h.Streams() {
		return Ask(ctx, h, environ, dir, prompt, stdout, stderr)
	}
	_, err := stream(ctx, h, environ, dir, h.StreamArgs, prompt, stdout, stderr)
	return err
}

// stream runs the harness with args and the prompt after them, and writes
// the text of its JSON output to stdout as it arrives. It returns the
// conversation ID the output reports at the harness's conversation_key,
// or "" when there is none.
func stream(ctx context.Context, h config.Harness, environ []string, dir string, args []string, prompt string, stdout, stderr io.Writer) (string, error) {
	cmd := exec.CommandContext(ctx, h.Command, append(append([]string{}, args...), prompt)...)
	cmd.Dir = dir
	cmd.Env = Env(h, environ)
	cmd.Stderr = stderr
	// Stdin stays closed, as in Ask.
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	if err := cmd.Start(); err != nil {
		return "", err
	}
	lines := bufio.NewReader(pipe)
	// Output that is not what was asked for is most likely the harness
	// saying why it cannot run. It is passed on once the run is over:
	// until then stderr belongs to the process.
	var stray strings.Builder
	// A harness that fails says why in one of its events, not in the
	// answer: a usage limit reached, a login that expired.
	var failures []string
	var id string
	for {
		line, readErr := lines.ReadString('\n')
		if text, isJSON := streamPiece(h, line); text != "" {
			if h.StreamWhole && !strings.HasSuffix(text, "\n") {
				text += "\n"
			}
			_, _ = io.WriteString(stdout, text)
		} else if !isJSON && strings.TrimSpace(line) != "" {
			stray.WriteString(line)
		} else if why := streamFailure(line); why != "" {
			failures = append(failures, why)
		}
		if id == "" && h.ConversationKey != "" {
			id = streamField(line, h.ConversationKey)
		}
		if readErr != nil {
			break
		}
	}
	err = cmd.Wait()
	if stray.Len() > 0 {
		_, _ = io.WriteString(stderr, stray.String())
	}
	if err != nil {
		for _, why := range failures {
			_, _ = io.WriteString(stderr, why+"\n")
		}
	}
	return id, err
}

// streamField returns the text at a path of keys in one line of JSON
// output, or "" when the line has none there.
func streamField(line, path string) string {
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return ""
	}
	text, _ := lookupPath(event, path).(string)
	return strings.TrimSpace(text)
}

// streamFailure returns what a line of a harness's output says went
// wrong, or "" when it reports no failure. The harnesses name their
// events differently, so this looks for what they have in common: an
// event marked as an error, with its text under one of a few keys.
func streamFailure(line string) string {
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return ""
	}
	kind, _ := event["type"].(string)
	failed, _ := event["is_error"].(bool)
	item, _ := event["item"].(map[string]any)
	if itemKind, _ := item["type"].(string); itemKind == "error" {
		failed, event = true, item
	}
	if !failed && !strings.Contains(strings.ToLower(kind), "error") && event["error"] == nil {
		return ""
	}
	for _, key := range []string{"result", "message", "error"} {
		switch v := event[key].(type) {
		case string:
			if strings.TrimSpace(v) != "" {
				return strings.TrimSpace(v)
			}
		case map[string]any:
			if m, _ := v["message"].(string); strings.TrimSpace(m) != "" {
				return strings.TrimSpace(m)
			}
		}
	}
	return ""
}

// streamPiece returns the text one line of a harness's output carries, or
// "" when it carries none. isJSON is false for a line that is not a JSON
// object at all.
func streamPiece(h config.Harness, line string) (text string, isJSON bool) {
	var event map[string]any
	if err := json.Unmarshal([]byte(line), &event); err != nil {
		return "", false
	}
	if path, want, ok := strings.Cut(h.StreamWhen, "="); ok {
		if got, _ := lookupPath(event, path).(string); got != want {
			return "", true
		}
	}
	text, _ = lookupPath(event, h.StreamText).(string)
	return text, true
}

// lookupPath follows keys joined by dots through nested objects.
func lookupPath(event map[string]any, path string) any {
	return lookup(event, strings.Split(path, "."))
}
