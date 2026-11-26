package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
)

func post(t *testing.T, s *Server, token, body string) (int, map[string]any) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, s.URL(), bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer res.Body.Close()
	var out map[string]any
	json.NewDecoder(res.Body).Decode(&out)
	return res.StatusCode, out
}

func TestServer(t *testing.T) {
	var got string
	s, err := Start("h2h", "test", []Tool{{
		Name: "start", Description: "Start a harness", Param: "arguments", ParamHelp: "harness or domain",
		Call: func(_ context.Context, in string) (string, bool) { got = in; return "started " + in, in == "bad" },
	}})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()

	if !strings.HasPrefix(s.URL(), "http://127.0.0.1:") {
		t.Errorf("URL = %s, want the loopback interface", s.URL())
	}
	if code, _ := post(t, s, "", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != http.StatusUnauthorized {
		t.Errorf("without the token: %d", code)
	}
	if code, _ := post(t, s, "wrong", `{"jsonrpc":"2.0","id":1,"method":"ping"}`); code != http.StatusUnauthorized {
		t.Errorf("with a wrong token: %d", code)
	}

	_, init := post(t, s, s.Token(), `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-03-26"}}`)
	if v := init["result"].(map[string]any)["protocolVersion"]; v != "2025-03-26" {
		t.Errorf("protocol version = %v", v)
	}
	if code, _ := post(t, s, s.Token(), `{"jsonrpc":"2.0","method":"notifications/initialized"}`); code != http.StatusAccepted {
		t.Errorf("a notification: %d", code)
	}

	_, list := post(t, s, s.Token(), `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	tools := list["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "start" {
		t.Fatalf("tools = %v", tools)
	}

	_, call := post(t, s, s.Token(), `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"start","arguments":{"arguments":"ui"}}}`)
	res := call["result"].(map[string]any)
	if got != "ui" || res["isError"] != false || res["content"].([]any)[0].(map[string]any)["text"] != "started ui" {
		t.Errorf("call = %v, input %q", call, got)
	}
	_, call = post(t, s, s.Token(), `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"start","arguments":{"arguments":"bad"}}}`)
	if call["result"].(map[string]any)["isError"] != true {
		t.Errorf("a failed call = %v", call)
	}
	_, call = post(t, s, s.Token(), `{"jsonrpc":"2.0","id":5,"method":"tools/call","params":{"name":"nope"}}`)
	if call["error"] == nil {
		t.Errorf("an unknown tool = %v", call)
	}
}
