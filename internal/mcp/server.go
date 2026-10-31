// Package mcp is a minimal MCP server: JSON-RPC 2.0 over newline-delimited stdio.
// stdout carries protocol messages only; diagnostics go to the log writer (stderr).
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"

	"github.com/SystemFiles/grokbot-opnsense-plugin/internal/opnsense"
)

const (
	defaultProtocol = "2025-06-18"
	serverName      = "opnsense"
	// Version is the server version reported in initialize.
	Version = "0.2.0"
)

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

type toolResult struct {
	Content           []content       `json:"content"`
	IsError           bool            `json:"isError"`
	StructuredContent json.RawMessage `json:"structuredContent,omitempty"`
}

type server struct {
	mu  sync.Mutex
	out io.Writer
	log io.Writer
}

func (s *server) send(r response) {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(r); err != nil {
		fmt.Fprintln(s.log, "encode error:", err)
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, _ = s.out.Write(b.Bytes())
}

func (s *server) reply(id json.RawMessage, result any) {
	s.send(response{JSONRPC: "2.0", ID: id, Result: result})
}

func (s *server) fail(id json.RawMessage, code int, msg string) {
	s.send(response{JSONRPC: "2.0", ID: id, Error: &rpcError{code, msg}})
}

// textResult mirrors the original: errors are plain text; successes are the
// pretty-printed JSON plus structuredContent when the result is a JSON object.
func textResult(data json.RawMessage) toolResult {
	var s string
	if len(data) > 0 && data[0] == '"' && json.Unmarshal(data, &s) == nil {
		return toolResult{Content: []content{{"text", s}}}
	}
	var b bytes.Buffer
	if json.Indent(&b, data, "", "  ") != nil {
		b.Reset()
		b.Write(data)
	}
	r := toolResult{Content: []content{{"text", b.String()}}}
	if len(data) > 0 && data[0] == '{' {
		r.StructuredContent = data
	}
	return r
}

func errorResult(msg string) toolResult {
	return toolResult{Content: []content{{"text", msg}}, IsError: true}
}

func (s *server) callTool(t *tool, a args) (res toolResult) {
	defer func() {
		if p := recover(); p != nil {
			fmt.Fprintln(s.log, "panic in tool", t.Name, ":", p)
			res = errorResult(fmt.Sprintf("Unexpected error: %v", p))
		}
	}()
	data, err := t.run(a)
	if err != nil {
		var te *opnsense.Error
		if errors.As(err, &te) {
			return errorResult(te.Msg)
		}
		return errorResult("Unexpected error: " + err.Error())
	}
	return textResult(data)
}

func (s *server) handle(raw json.RawMessage) {
	var msg struct {
		JSONRPC any             `json:"jsonrpc"`
		ID      json.RawMessage `json:"id"`
		Method  any             `json:"method"`
		Params  json.RawMessage `json:"params"`
	}
	if raw[0] != '{' || json.Unmarshal(raw, &msg) != nil {
		return
	}
	method, ok := msg.Method.(string)
	if msg.JSONRPC != "2.0" || !ok {
		if msg.ID != nil {
			s.fail(msg.ID, -32600, "Invalid Request")
		}
		return
	}
	if msg.ID == nil || string(msg.ID) == "null" {
		return // notifications/initialized, cancelled, etc.
	}
	id := msg.ID
	var params map[string]json.RawMessage
	_ = json.Unmarshal(msg.Params, &params)
	switch method {
	case "initialize":
		pv := defaultProtocol
		if r := params["protocolVersion"]; len(r) > 0 && r[0] == '"' {
			_ = json.Unmarshal(r, &pv)
		}
		s.reply(id, map[string]any{
			"protocolVersion": pv,
			"capabilities":    map[string]any{"tools": map[string]any{"listChanged": false}},
			"serverInfo":      map[string]any{"name": serverName, "version": Version},
			"instructions":    "OPNsense firewall API. Prefer read tools; opnsense_post changes the live firewall and requires confirm=true after explicit user approval.",
		})
	case "ping":
		s.reply(id, map[string]any{})
	case "tools/list":
		s.reply(id, map[string]any{"tools": tools})
	case "tools/call":
		name := "undefined"
		if r := params["name"]; len(r) > 0 {
			var n string
			if json.Unmarshal(r, &n) == nil {
				name = n
			} else {
				name = string(r)
			}
		}
		t := findTool(name)
		if t == nil {
			s.fail(id, -32602, "Unknown tool: "+name)
			return
		}
		var a args
		_ = json.Unmarshal(params["arguments"], &a)
		if a == nil {
			a = args{}
		}
		s.reply(id, s.callTool(t, a))
	default:
		s.fail(id, -32601, "Method not found: "+method)
	}
}

// Serve reads JSON-RPC messages (one per line, batches allowed) until EOF,
// handles them concurrently, and returns once every in-flight call has replied.
func Serve(in io.Reader, out, log io.Writer) error {
	s := &server{out: out, log: log}
	var wg sync.WaitGroup
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		if !json.Valid(line) {
			s.send(response{JSONRPC: "2.0", ID: json.RawMessage("null"), Error: &rpcError{-32700, "Parse error"}})
			continue
		}
		var msgs []json.RawMessage
		if line[0] == '[' {
			_ = json.Unmarshal(line, &msgs)
		} else {
			msgs = []json.RawMessage{append([]byte(nil), line...)}
		}
		for _, m := range msgs {
			wg.Add(1)
			go func(m json.RawMessage) {
				defer wg.Done()
				s.handle(m)
			}(m)
		}
	}
	wg.Wait()
	return sc.Err()
}
