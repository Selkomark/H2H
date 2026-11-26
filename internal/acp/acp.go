// Package acp drives a harness over the Agent Client Protocol (ACP), in
// place of its terminal: H2H is the client, the harness is the agent, and
// they exchange JSON-RPC messages over the harness's standard input and
// output. See docs/acp.md.
//
// Only what H2H uses is implemented: starting a session, sending a prompt,
// following what the agent does, answering its permission requests,
// cancelling a turn and changing a session's settings, such as its model.
// The client offers the agent no file system or terminal of its own, so the
// agent uses its own tools, as it does in its terminal.
package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"sync"
)

// protocolVersion is the version of ACP this client speaks.
const protocolVersion = 1

// Update is something the agent reports during a turn. Kind is ACP's
// sessionUpdate, such as "agent_message_chunk", "agent_thought_chunk",
// "tool_call", "tool_call_update", "plan" or "usage_update". The other
// fields are filled in where the kind carries them.
type Update struct {
	Kind string
	// Text is the text of a message or thought chunk.
	Text string
	// The tool call a "tool_call" or "tool_call_update" is about.
	ToolCallID string
	Title      string
	ToolKind   string // read, edit, execute, fetch and so on
	Status     string // pending, in_progress, completed, failed
	// Plan holds the entries of a "plan", each as its status and content.
	Plan []PlanEntry
	// Used and Size are the tokens in the context and its size, and Cost
	// what the session has cost so far, from a "usage_update".
	Used, Size int
	Cost       float64
	Currency   string
}

// PlanEntry is one step of the agent's plan.
type PlanEntry struct {
	Content string
	Status  string
}

// Permission is the agent asking whether it may use a tool.
type Permission struct {
	Title    string
	ToolKind string
	// Command is the shell command, for a tool that runs one.
	Command string
	// Path is the file or folder the tool acts on, if it names one.
	Path    string
	Options []Option
}

// Option is one answer the agent offers to a permission request. Kind is
// allow_once, allow_always, reject_once or reject_always.
type Option struct {
	ID   string
	Name string
	Kind string
}

// Handlers receive what the agent sends. They are called one at a time, in
// the order the messages arrive, from the goroutine that reads them.
type Handlers struct {
	Update func(Update)
	// Permission returns the ID of the chosen option, or "" to cancel the
	// request, which the agent takes as a refusal.
	Permission func(Permission) string
}

// ConfigOption is a setting of a session that the client may change, such
// as "model" or "mode", with its current value and the values it takes.
type ConfigOption struct {
	ID      string
	Name    string
	Current string
	Values  []string
}

// Session is a conversation with the agent.
type Session struct {
	ID      string
	Options []ConfigOption
}

// Client is a running agent and the connection to it.
type Client struct {
	// Agent is the name and version the agent gave, such as "OpenCode 2.4.1".
	Agent string

	cmd      *exec.Cmd
	in       io.WriteCloser
	handlers Handlers

	mu      sync.Mutex // guards next, pending and writes to in
	next    int
	pending map[int]chan response
	done    chan struct{} // closed when the agent's output ends
	err     error         // why it ended, set before done is closed
}

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (e *rpcError) Error() string { return fmt.Sprintf("%s (%d)", e.Message, e.Code) }

type response struct {
	result json.RawMessage
	err    error
}

// Start runs the agent, given as a whole command line, program first, in
// dir with env, and opens the connection. What the agent writes to its
// standard error goes to stderr. The agent stops when ctx ends or Close is
// called.
func Start(ctx context.Context, argv, env []string, dir string, stderr io.Writer, h Handlers) (*Client, error) {
	if len(argv) == 0 {
		return nil, errors.New("no agent command")
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir, cmd.Env, cmd.Stderr = dir, env, stderr
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	c := &Client{cmd: cmd, in: in, handlers: h, pending: map[int]chan response{}, done: make(chan struct{})}
	go c.read(out)

	var init struct {
		ProtocolVersion int `json:"protocolVersion"`
		AgentInfo       struct {
			Name    string `json:"name"`
			Version string `json:"version"`
		} `json:"agentInfo"`
	}
	params := map[string]any{
		"protocolVersion": protocolVersion,
		"clientCapabilities": map[string]any{
			"fs":       map[string]bool{"readTextFile": false, "writeTextFile": false},
			"terminal": false,
		},
		"clientInfo": map[string]string{"name": "h2h"},
	}
	if err := c.call(ctx, "initialize", params, &init); err != nil {
		c.Close()
		return nil, fmt.Errorf("the agent did not start: %w", err)
	}
	if init.ProtocolVersion != protocolVersion {
		c.Close()
		return nil, fmt.Errorf("the agent speaks ACP version %d, and H2H speaks %d", init.ProtocolVersion, protocolVersion)
	}
	c.Agent = init.AgentInfo.Name
	if init.AgentInfo.Version != "" {
		c.Agent += " " + init.AgentInfo.Version
	}
	return c, nil
}

// NewSession starts a conversation in dir.
func (c *Client) NewSession(ctx context.Context, dir string) (Session, error) {
	var res struct {
		SessionID     string          `json:"sessionId"`
		ConfigOptions json.RawMessage `json:"configOptions"`
	}
	if err := c.call(ctx, "session/new", map[string]any{"cwd": dir, "mcpServers": []any{}}, &res); err != nil {
		return Session{}, err
	}
	return Session{ID: res.SessionID, Options: configOptions(res.ConfigOptions)}, nil
}

// Prompt sends text to the session and waits for the turn to end. It
// returns why it ended, such as "end_turn" or "cancelled". Updates and
// permission requests arrive through the handlers meanwhile.
func (c *Client) Prompt(ctx context.Context, session, text string) (string, error) {
	var res struct {
		StopReason string `json:"stopReason"`
	}
	params := map[string]any{"sessionId": session, "prompt": []any{map[string]string{"type": "text", "text": text}}}
	if err := c.call(ctx, "session/prompt", params, &res); err != nil {
		return "", err
	}
	return res.StopReason, nil
}

// Cancel asks the agent to stop the session's turn. Prompt then returns
// with the reason "cancelled".
func (c *Client) Cancel(session string) error {
	return c.send(message{Method: "session/cancel", Params: mustJSON(map[string]string{"sessionId": session})})
}

// SetOption changes a setting of the session, such as its model, and
// returns the settings as they are afterwards.
func (c *Client) SetOption(ctx context.Context, session, id, value string) ([]ConfigOption, error) {
	var res struct {
		ConfigOptions json.RawMessage `json:"configOptions"`
	}
	params := map[string]string{"sessionId": session, "configId": id, "value": value}
	if err := c.call(ctx, "session/set_config_option", params, &res); err != nil {
		return nil, err
	}
	return configOptions(res.ConfigOptions), nil
}

// Close stops the agent.
func (c *Client) Close() error {
	c.in.Close()
	c.cmd.Process.Kill()
	<-c.done // Wait closes the output, so reading it must be over first
	return c.cmd.Wait()
}

func (c *Client) call(ctx context.Context, method string, params, result any) error {
	c.mu.Lock()
	c.next++
	id := c.next
	ch := make(chan response, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
	}()
	if err := c.send(message{ID: &id, Method: method, Params: mustJSON(params)}); err != nil {
		return err
	}
	select {
	case r := <-ch:
		if r.err != nil {
			return r.err
		}
		if result == nil || len(r.result) == 0 {
			return nil
		}
		return json.Unmarshal(r.result, result)
	case <-c.done:
		return fmt.Errorf("the agent stopped: %w", c.err)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *Client) send(m message) error {
	m.JSONRPC = "2.0"
	b, err := json.Marshal(m)
	if err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	_, err = c.in.Write(append(b, '\n'))
	return err
}

// read takes the agent's messages, one per line, until its output ends.
func (c *Client) read(out io.Reader) {
	sc := bufio.NewScanner(out)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		var m message
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			continue // not a message: an agent may print other lines
		}
		switch {
		case m.Method != "" && m.ID != nil:
			c.answer(m)
		case m.Method != "":
			c.notice(m)
		case m.ID != nil:
			c.mu.Lock()
			ch := c.pending[*m.ID]
			c.mu.Unlock()
			if ch != nil {
				r := response{result: m.Result}
				if m.Error != nil {
					r.err = m.Error
				}
				select {
				case ch <- r:
				default: // a second answer to the same request is dropped
				}
			}
		}
	}
	c.err = sc.Err()
	if c.err == nil {
		c.err = io.EOF
	}
	close(c.done)
}

// answer replies to a request from the agent. Only permission requests are
// served; the client offered nothing else.
func (c *Client) answer(m message) {
	if m.Method != "session/request_permission" {
		c.send(message{ID: m.ID, Error: &rpcError{Code: -32601, Message: "method not found"}})
		return
	}
	choice := ""
	if c.handlers.Permission != nil {
		choice = c.handlers.Permission(permission(m.Params))
	}
	outcome := map[string]string{"outcome": "cancelled"}
	if choice != "" {
		outcome = map[string]string{"outcome": "selected", "optionId": choice}
	}
	c.send(message{ID: m.ID, Result: mustJSON(map[string]any{"outcome": outcome})})
}

func (c *Client) notice(m message) {
	if m.Method != "session/update" || c.handlers.Update == nil {
		return
	}
	var p struct {
		Update json.RawMessage `json:"update"`
	}
	if json.Unmarshal(m.Params, &p) == nil {
		c.handlers.Update(update(p.Update))
	}
}

func update(raw json.RawMessage) Update {
	var u struct {
		SessionUpdate string          `json:"sessionUpdate"`
		Content       json.RawMessage `json:"content"`
		ToolCallID    string          `json:"toolCallId"`
		Title         string          `json:"title"`
		Kind          string          `json:"kind"`
		Status        string          `json:"status"`
		Entries       []struct {
			Content string `json:"content"`
			Status  string `json:"status"`
		} `json:"entries"`
		Used int `json:"used"`
		Size int `json:"size"`
		Cost struct {
			Amount   float64 `json:"amount"`
			Currency string  `json:"currency"`
		} `json:"cost"`
	}
	json.Unmarshal(raw, &u)
	out := Update{
		Kind: u.SessionUpdate, ToolCallID: u.ToolCallID, Title: u.Title, ToolKind: u.Kind, Status: u.Status,
		Used: u.Used, Size: u.Size, Cost: u.Cost.Amount, Currency: u.Cost.Currency,
	}
	// A chunk's content is one block; a tool call's is a list of them.
	var block struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(u.Content, &block) == nil && block.Type == "text" {
		out.Text = block.Text
	}
	for _, e := range u.Entries {
		out.Plan = append(out.Plan, PlanEntry{Content: e.Content, Status: e.Status})
	}
	return out
}

func permission(raw json.RawMessage) Permission {
	var p struct {
		ToolCall struct {
			Title     string `json:"title"`
			Kind      string `json:"kind"`
			Locations []struct {
				Path string `json:"path"`
			} `json:"locations"`
			RawInput map[string]any `json:"rawInput"`
		} `json:"toolCall"`
		Options []struct {
			OptionID string `json:"optionId"`
			Name     string `json:"name"`
			Kind     string `json:"kind"`
		} `json:"options"`
	}
	json.Unmarshal(raw, &p)
	out := Permission{Title: p.ToolCall.Title, ToolKind: p.ToolCall.Kind}
	if s, ok := p.ToolCall.RawInput["command"].(string); ok {
		out.Command = s
	}
	for _, key := range []string{"filePath", "file_path", "path"} {
		if s, ok := p.ToolCall.RawInput[key].(string); ok && s != "" {
			out.Path = s
			break
		}
	}
	if out.Path == "" && out.Command == "" && len(p.ToolCall.Locations) > 0 {
		out.Path = p.ToolCall.Locations[0].Path
	}
	for _, o := range p.Options {
		out.Options = append(out.Options, Option{ID: o.OptionID, Name: o.Name, Kind: o.Kind})
	}
	return out
}

func configOptions(raw json.RawMessage) []ConfigOption {
	var opts []struct {
		ID           string `json:"id"`
		Name         string `json:"name"`
		CurrentValue string `json:"currentValue"`
		Options      []struct {
			Value string `json:"value"`
		} `json:"options"`
	}
	json.Unmarshal(raw, &opts)
	var out []ConfigOption
	for _, o := range opts {
		c := ConfigOption{ID: o.ID, Name: o.Name, Current: o.CurrentValue}
		for _, v := range o.Options {
			c.Values = append(c.Values, v.Value)
		}
		out = append(out, c)
	}
	return out
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err) // only values built in this package are encoded
	}
	return b
}
