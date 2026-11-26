package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"slices"
	"testing"
	"time"
)

// The test binary stands in for an agent when this variable is set.
const fakeAgentEnv = "H2H_ACP_FAKE_AGENT"

func TestMain(m *testing.M) {
	if os.Getenv(fakeAgentEnv) == "1" {
		fakeAgent(os.Stdin, os.Stdout)
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// fakeAgent answers as an ACP agent does: a prompt gets a message, a tool
// call that needs permission, and the tool's result if it was allowed.
func fakeAgent(in io.Reader, out io.Writer) {
	enc := json.NewEncoder(out)
	send := func(m map[string]any) { m["jsonrpc"] = "2.0"; enc.Encode(m) }
	update := func(u map[string]any) {
		send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": "s1", "update": u}})
	}
	model := map[string]any{"id": "model", "name": "Model", "currentValue": "small", "options": []any{map[string]string{"value": "small"}, map[string]string{"value": "large"}}}
	sc := bufio.NewScanner(in)
	var prompt *int
	for sc.Scan() {
		var m struct {
			ID     *int            `json:"id"`
			Method string          `json:"method"`
			Params json.RawMessage `json:"params"`
			Result struct {
				Outcome struct {
					Outcome  string `json:"outcome"`
					OptionID string `json:"optionId"`
				} `json:"outcome"`
			} `json:"result"`
		}
		json.Unmarshal(sc.Bytes(), &m)
		switch m.Method {
		case "initialize":
			send(map[string]any{"id": *m.ID, "result": map[string]any{"protocolVersion": 1, "agentInfo": map[string]string{"name": "Fake", "version": "1.0"}}})
		case "session/new":
			send(map[string]any{"id": *m.ID, "result": map[string]any{"sessionId": "s1", "configOptions": []any{model}}})
		case "session/set_config_option":
			var p struct{ Value string }
			json.Unmarshal(m.Params, &p)
			model["currentValue"] = p.Value
			send(map[string]any{"id": *m.ID, "result": map[string]any{"configOptions": []any{model}}})
		case "session/prompt":
			prompt = m.ID
			update(map[string]any{"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "Working on it."}})
			update(map[string]any{"sessionUpdate": "tool_call", "toolCallId": "t1", "title": "shell", "kind": "execute", "status": "pending"})
			send(map[string]any{"id": 900, "method": "session/request_permission", "params": map[string]any{
				"sessionId": "s1",
				"toolCall":  map[string]any{"toolCallId": "t1", "title": "echo hi", "kind": "execute", "rawInput": map[string]string{"command": "echo hi"}},
				"options":   []any{map[string]string{"optionId": "yes", "kind": "allow_once", "name": "Allow"}, map[string]string{"optionId": "no", "kind": "reject_once", "name": "Reject"}},
			}})
		case "session/cancel":
			send(map[string]any{"id": *prompt, "result": map[string]string{"stopReason": "cancelled"}})
		case "":
			if m.ID != nil && *m.ID == 900 { // the answer to the permission request
				status := "failed"
				if m.Result.Outcome.OptionID == "yes" {
					status = "completed"
				}
				update(map[string]any{"sessionUpdate": "tool_call_update", "toolCallId": "t1", "status": status})
				send(map[string]any{"id": *prompt, "result": map[string]string{"stopReason": "end_turn"}})
			}
		default:
			if m.ID != nil {
				send(map[string]any{"id": *m.ID, "error": map[string]any{"code": -32601, "message": "method not found"}})
			}
		}
	}
}

func startFake(t *testing.T, h Handlers) *Client {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)
	c, err := Start(ctx, []string{self}, append(os.Environ(), fakeAgentEnv+"=1"), t.TempDir(), os.Stderr, h)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

func TestTurn(t *testing.T) {
	for _, allow := range []bool{true, false} {
		t.Run(fmt.Sprint("allow=", allow), func(t *testing.T) {
			var updates []Update
			var asked Permission
			c := startFake(t, Handlers{
				Update: func(u Update) { updates = append(updates, u) },
				Permission: func(p Permission) string {
					asked = p
					if allow {
						return "yes"
					}
					return "no"
				},
			})
			if c.Agent != "Fake 1.0" {
				t.Errorf("agent = %q", c.Agent)
			}
			ctx := context.Background()
			s, err := c.NewSession(ctx, t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			reason, err := c.Prompt(ctx, s.ID, "do it")
			if err != nil || reason != "end_turn" {
				t.Fatalf("Prompt = %q, %v", reason, err)
			}
			if asked.Command != "echo hi" || asked.ToolKind != "execute" || len(asked.Options) != 2 {
				t.Errorf("permission request = %+v", asked)
			}
			kinds := []string{}
			for _, u := range updates {
				kinds = append(kinds, u.Kind)
			}
			if !slices.Equal(kinds, []string{"agent_message_chunk", "tool_call", "tool_call_update"}) {
				t.Fatalf("updates = %v", kinds)
			}
			if updates[0].Text != "Working on it." {
				t.Errorf("message = %q", updates[0].Text)
			}
			want := map[bool]string{true: "completed", false: "failed"}[allow]
			if got := updates[2].Status; got != want {
				t.Errorf("tool status = %q, want %q", got, want)
			}
		})
	}
}

func TestCancel(t *testing.T) {
	asked := make(chan bool)
	var c *Client
	c = startFake(t, Handlers{Permission: func(Permission) string {
		asked <- true
		<-asked // held open until the test has cancelled
		return ""
	}})
	ctx := context.Background()
	s, err := c.NewSession(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan string)
	go func() {
		r, _ := c.Prompt(ctx, s.ID, "do it")
		done <- r
	}()
	<-asked
	if err := c.Cancel(s.ID); err != nil {
		t.Fatal(err)
	}
	asked <- true
	if r := <-done; r != "cancelled" {
		t.Errorf("stop reason = %q", r)
	}
}

func TestSetOption(t *testing.T) {
	c := startFake(t, Handlers{})
	ctx := context.Background()
	s, err := c.NewSession(ctx, t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if len(s.Options) != 1 || s.Options[0].Current != "small" || !slices.Equal(s.Options[0].Values, []string{"small", "large"}) {
		t.Fatalf("options = %+v", s.Options)
	}
	opts, err := c.SetOption(ctx, s.ID, "model", "large")
	if err != nil || len(opts) != 1 || opts[0].Current != "large" {
		t.Fatalf("SetOption = %+v, %v", opts, err)
	}
}
