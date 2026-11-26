// Package mcp serves tools to a harness over the Model Context Protocol
// (MCP), on its HTTP transport: each request is one JSON-RPC message
// posted to the server, answered with one JSON response.
//
// It is how H2H offers its own commands to a harness it drives over ACP;
// see docs/acp.md. Only tools are served: no resources, prompts or
// notifications from the server.
package mcp

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"slices"
)

// protocolVersions are the versions of MCP this server answers in, newest
// first. A client that asks for one of them gets it; any other gets the
// newest, as MCP says.
var protocolVersions = []string{"2025-06-18", "2025-03-26", "2024-11-05"}

// Tool is one tool the server offers. Its input is one string, named by
// Param, which the harness fills in as the tool's description says.
type Tool struct {
	Name        string
	Description string
	Param       string // the name of the input, such as "arguments"
	ParamHelp   string
	// Call runs the tool. It returns what the harness is shown, and
	// whether the call failed.
	Call func(ctx context.Context, input string) (text string, failed bool)
}

// Server serves tools on a local port, to clients that present its token.
type Server struct {
	name, version string
	tools         []Tool
	token         string
	ln            net.Listener
	srv           *http.Server
}

// Start serves the tools on a port of the loopback interface, chosen by
// the system, until Close is called. Only a client that sends the token
// it was given, as a bearer token, is served.
func Start(name, version string, tools []Tool) (*Server, error) {
	key := make([]byte, 24)
	if _, err := rand.Read(key); err != nil {
		return nil, err
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	s := &Server{name: name, version: version, tools: tools, token: hex.EncodeToString(key), ln: ln}
	s.srv = &http.Server{Handler: s}
	go s.srv.Serve(ln)
	return s, nil
}

// URL is where the server answers.
func (s *Server) URL() string { return "http://" + s.ln.Addr().String() + "/mcp" }

// Token is what a client sends, as "Authorization: Bearer <token>".
func (s *Server) Token() string { return s.token }

// Close stops the server. Calls under way are cancelled.
func (s *Server) Close() error { return s.srv.Close() }

type message struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	want := "Bearer " + s.token
	if subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte(want)) != 1 {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	if r.URL.Path != "/mcp" {
		http.NotFound(w, r)
		return
	}
	switch r.Method {
	case http.MethodPost:
	case http.MethodDelete:
		w.WriteHeader(http.StatusOK) // the client ends its session; there is nothing to end
		return
	default:
		// No stream of messages from the server: it sends none.
		w.Header().Set("Allow", "POST, DELETE")
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var m message
	if err := json.Unmarshal(body, &m); err != nil {
		writeJSON(w, message{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{Code: -32700, Message: "parse error"}})
		return
	}
	if len(m.ID) == 0 {
		w.WriteHeader(http.StatusAccepted) // a notification or a response: nothing to answer
		return
	}
	result, rerr := s.handle(r.Context(), m)
	writeJSON(w, message{JSONRPC: "2.0", ID: m.ID, Result: result, Error: rerr})
}

func (s *Server) handle(ctx context.Context, m message) (any, *rpcError) {
	switch m.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(m.Params, &p)
		version := protocolVersions[0]
		if slices.Contains(protocolVersions, p.ProtocolVersion) {
			version = p.ProtocolVersion
		}
		return map[string]any{
			"protocolVersion": version,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]string{"name": s.name, "version": s.version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := make([]any, len(s.tools))
		for n, t := range s.tools {
			tools[n] = map[string]any{
				"name":        t.Name,
				"description": t.Description,
				"inputSchema": map[string]any{
					"type":       "object",
					"properties": map[string]any{t.Param: map[string]string{"type": "string", "description": t.ParamHelp}},
				},
			}
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &p); err != nil {
			return nil, &rpcError{Code: -32602, Message: "invalid params"}
		}
		i := slices.IndexFunc(s.tools, func(t Tool) bool { return t.Name == p.Name })
		if i < 0 {
			return nil, &rpcError{Code: -32602, Message: "unknown tool " + p.Name}
		}
		t := s.tools[i]
		input, _ := p.Arguments[t.Param].(string)
		text, failed := t.Call(ctx, input)
		return map[string]any{"content": []any{map[string]string{"type": "text", "text": text}}, "isError": failed}, nil
	}
	return nil, &rpcError{Code: -32601, Message: "method not found"}
}

func writeJSON(w http.ResponseWriter, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		http.Error(w, errors.New("could not encode the answer").Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Write(b)
}
