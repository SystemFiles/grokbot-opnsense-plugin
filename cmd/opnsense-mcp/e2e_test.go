package main

// End-to-end proof (port of the original test/e2e.js): build the real binary,
// speak JSON-RPC over stdio, against a mock OPNsense on https://[::1] with a
// throwaway self-signed cert (httptest) and basic auth.
// Set OPNSENSE_MCP_BIN to test a prebuilt binary instead of building one.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"
)

var binPath string

func TestMain(m *testing.M) {
	binPath = os.Getenv("OPNSENSE_MCP_BIN")
	if binPath == "" {
		dir, err := os.MkdirTemp("", "opnsense-mcp-test-")
		if err != nil {
			panic(err)
		}
		binPath = filepath.Join(dir, "opnsense-mcp")
		if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
			fmt.Fprintf(os.Stderr, "build failed: %v\n%s", err, out)
			os.Exit(1)
		}
		defer os.RemoveAll(dir)
	}
	code := m.Run()
	os.Exit(code)
}

func randHex() string { b := make([]byte, 8); _, _ = rand.Read(b); return hex.EncodeToString(b) }

type seenReq struct{ Method, URL, Body string }

type mock struct {
	*httptest.Server
	mu   sync.Mutex
	seen []seenReq
}

func (m *mock) count() int    { m.mu.Lock(); defer m.mu.Unlock(); return len(m.seen) }
func (m *mock) last() seenReq { m.mu.Lock(); defer m.mu.Unlock(); return m.seen[len(m.seen)-1] }

func newMock(t *testing.T, key, secret string) *mock {
	type row struct {
		ID          string `json:"id"`
		Locked      int    `json:"locked"`
		Running     int    `json:"running"`
		Description string `json:"description"`
		Name        string `json:"name"`
	}
	rows := []row{{"unbound", 0, 1, "Unbound DNS", "unbound"}, {"sshd", 0, 0, "Secure Shell Daemon", "openssh"}}
	m := &mock{}
	m.Server = httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		m.mu.Lock()
		m.seen = append(m.seen, seenReq{r.Method, r.URL.RequestURI(), string(b)})
		m.mu.Unlock()
		js := func(code int, v any) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(v)
		}
		if u, p, ok := r.BasicAuth(); !ok || u != key || p != secret {
			js(401, map[string]any{"status": 401, "message": "Authentication Failed"})
			return
		}
		switch {
		case r.URL.RequestURI() == "/api/core/service/search" && r.Method == "POST":
			var q struct {
				SearchPhrase string `json:"searchPhrase"`
				Current      int    `json:"current"`
			}
			_ = json.Unmarshal(b, &q)
			out := []row{}
			for _, x := range rows {
				if q.SearchPhrase == "" || strings.Contains(x.Name, q.SearchPhrase) {
					out = append(out, x)
				}
			}
			if q.Current == 0 {
				q.Current = 1
			}
			js(200, map[string]any{"total": len(out), "rowCount": len(out), "current": q.Current, "rows": out})
		case r.URL.RequestURI() == "/api/core/firmware/status" && r.Method == "GET":
			js(200, map[string]any{"product_version": "26.1-mock", "status": "none"})
		case r.URL.RequestURI() == "/api/core/service/restart/sshd" && r.Method == "POST":
			js(200, map[string]any{"result": "ok"})
		default:
			js(404, map[string]any{"errorMessage": "Endpoint not found"})
		}
	}))
	ln, err := net.Listen("tcp", "[::1]:0")
	if err != nil {
		t.Skipf("no IPv6 loopback: %v", err)
	}
	m.Listener = ln
	m.Config.ErrorLog = log.New(io.Discard, "", 0) // expected handshake failure in check 13
	m.StartTLS()                                   // self-signed certificate
	t.Cleanup(m.Close)
	return m
}

type client struct {
	t      *testing.T
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	mu     sync.Mutex
	next   int
	wait   map[int]chan map[string]any
	stdout strings.Builder
	done   chan struct{}
}

func startClient(t *testing.T, env ...string) *client {
	t.Helper()
	cmd := exec.Command(binPath)
	cmd.Env = append([]string{"PATH=" + os.Getenv("PATH")}, env...)
	stdin, _ := cmd.StdinPipe()
	stdout, _ := cmd.StdoutPipe()
	cmd.Stderr = io.Discard
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	c := &client{t: t, cmd: cmd, stdin: stdin, next: 1, wait: map[int]chan map[string]any{}, done: make(chan struct{})}
	go func() {
		defer close(c.done)
		sc := bufio.NewScanner(stdout)
		sc.Buffer(make([]byte, 1<<20), 1<<24)
		for sc.Scan() {
			line := sc.Text()
			c.mu.Lock()
			c.stdout.WriteString(line + "\n")
			var m map[string]any
			if err := json.Unmarshal([]byte(line), &m); err != nil { // every stdout line must be JSON-RPC
				c.stdout.WriteString("NON-JSON-LINE\n")
			} else if id, ok := m["id"].(float64); ok {
				if ch := c.wait[int(id)]; ch != nil {
					ch <- m
				}
			}
			c.mu.Unlock()
		}
	}()
	t.Cleanup(c.close)
	return c
}

func (c *client) write(v any) {
	b, _ := json.Marshal(v)
	_, _ = c.stdin.Write(append(b, '\n'))
}

func (c *client) rpc(method string, params any) map[string]any {
	c.t.Helper()
	c.mu.Lock()
	id := c.next
	c.next++
	ch := make(chan map[string]any, 1)
	c.wait[id] = ch
	c.mu.Unlock()
	c.write(map[string]any{"jsonrpc": "2.0", "id": id, "method": method, "params": params})
	select {
	case m := <-ch:
		return m
	case <-time.After(10 * time.Second):
		c.t.Fatalf("timeout waiting for %s", method)
		return nil
	}
}

func (c *client) notify(method string) { c.write(map[string]any{"jsonrpc": "2.0", "method": method}) }

type callResult struct {
	IsError           bool           `json:"isError"`
	StructuredContent map[string]any `json:"structuredContent"`
	Content           []struct{ Text string }
}

func (c *client) call(name string, args map[string]any) callResult {
	c.t.Helper()
	m := c.rpc("tools/call", map[string]any{"name": name, "arguments": args})
	b, _ := json.Marshal(m["result"])
	var r callResult
	if err := json.Unmarshal(b, &r); err != nil || len(r.Content) == 0 {
		c.t.Fatalf("bad tools/call result: %s", b)
	}
	return r
}

func (c *client) close() {
	_ = c.stdin.Close()
	select {
	case <-c.done:
	case <-time.After(5 * time.Second):
		_ = c.cmd.Process.Kill()
	}
	_ = c.cmd.Wait()
}

func (c *client) out() string { c.mu.Lock(); defer c.mu.Unlock(); return c.stdout.String() }

func TestE2E(t *testing.T) {
	key, secret := "test-key-"+randHex(), "test-secret-"+randHex()
	srv := newMock(t, key, secret)
	_, port, _ := net.SplitHostPort(srv.Listener.Addr().String())
	base := []string{"OPNSENSE_URL=https://[::1]:" + port, "OPNSENSE_API_KEY=" + key}
	good := append(append([]string{}, base...), "OPNSENSE_API_SECRET="+secret)
	c := startClient(t, append(good, "OPNSENSE_VERIFY_TLS=false")...)

	t.Run("01 initialize handshake", func(t *testing.T) {
		r := c.rpc("initialize", map[string]any{"protocolVersion": "2025-06-18", "capabilities": map[string]any{}, "clientInfo": map[string]any{"name": "e2e", "version": "0"}})
		res := r["result"].(map[string]any)
		if res["serverInfo"].(map[string]any)["name"] != "opnsense" || res["protocolVersion"] != "2025-06-18" || res["capabilities"].(map[string]any)["tools"] == nil {
			t.Fatalf("bad initialize: %v", r)
		}
		c.notify("notifications/initialized") // must produce no response
	})
	t.Run("02 tools/list returns 7 tools with schemas", func(t *testing.T) {
		r := c.rpc("tools/list", map[string]any{})
		var names []string
		for _, x := range r["result"].(map[string]any)["tools"].([]any) {
			tl := x.(map[string]any)
			names = append(names, tl["name"].(string))
			if tl["inputSchema"].(map[string]any)["type"] != "object" {
				t.Errorf("%s schema type", tl["name"])
			}
			if tl["name"] == "opnsense_post" && !strings.Contains(tl["description"].(string), "confirm") {
				t.Error("post description lacks confirm")
			}
		}
		sort.Strings(names)
		want := "opnsense_firewall_rules opnsense_firmware_status opnsense_get opnsense_interfaces opnsense_post opnsense_search opnsense_services"
		if strings.Join(names, " ") != want {
			t.Fatalf("tools = %v", names)
		}
	})
	t.Run("03 TLS verify off + IPv6 [::1]: opnsense_services returns GridPage", func(t *testing.T) {
		r := c.call("opnsense_services", map[string]any{"searchPhrase": "unbound"})
		if r.IsError {
			t.Fatal(r.Content[0].Text)
		}
		if keys := sortedKeys(r.StructuredContent); keys != "current rowCount rows total" {
			t.Fatalf("keys %s", keys)
		}
		if r.StructuredContent["total"] != float64(1) || r.StructuredContent["rows"].([]any)[0].(map[string]any)["name"] != "unbound" {
			t.Fatalf("got %v", r.StructuredContent)
		}
		var sent map[string]any
		_ = json.Unmarshal([]byte(srv.last().Body), &sent)
		if keys := sortedKeys(sent); keys != "current rowCount searchPhrase sort" {
			t.Fatalf("sent body keys %s", keys)
		}
	})
	t.Run("04 opnsense_search generic grid", func(t *testing.T) {
		r := c.call("opnsense_search", map[string]any{"path": "/api/core/service/search", "rowCount": 7})
		if r.IsError || r.StructuredContent["total"] != float64(2) {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("05 opnsense_get / firmware status", func(t *testing.T) {
		r := c.call("opnsense_firmware_status", map[string]any{})
		if r.IsError || r.StructuredContent["product_version"] != "26.1-mock" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("06 confirm guard refuses without confirm=true (no request sent)", func(t *testing.T) {
		before := srv.count()
		for _, confirm := range []any{false, "true", 1, nil, "missing"} {
			args := map[string]any{"path": "/api/core/service/restart/sshd"}
			if confirm != "missing" {
				args["confirm"] = confirm
			}
			r := c.call("opnsense_post", args)
			if !r.IsError || !strings.Contains(r.Content[0].Text, "Refused") {
				t.Errorf("confirm=%#v not refused: %+v", confirm, r)
			}
		}
		if srv.count() != before {
			t.Fatal("a request reached the firewall without confirm=true")
		}
	})
	t.Run("07 confirm=true performs the POST", func(t *testing.T) {
		r := c.call("opnsense_post", map[string]any{"path": "/api/core/service/restart/sshd", "confirm": true})
		if r.IsError || r.StructuredContent["result"] != "ok" {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("08 path guard refuses scheme/host/traversal (no request sent)", func(t *testing.T) {
		before := srv.count()
		for _, p := range []string{"https://evil.example/api/x", "//evil.example/api/x", "/api/../../etc", "api/core/x", "/api/core\r\nHost: x", "/other/x", "/api/x@evil.example", "@evil.example/api/x"} {
			r := c.call("opnsense_get", map[string]any{"path": p})
			if !r.IsError || !strings.Contains(r.Content[0].Text, "Refused path") {
				t.Errorf("path %q not refused: %+v", p, r)
			}
		}
		if srv.count() != before {
			t.Fatal("a refused path reached the firewall")
		}
	})
	t.Run("09 HTTP 404 surfaces as tool error with status", func(t *testing.T) {
		r := c.call("opnsense_get", map[string]any{"path": "/api/nope/nope/nope"})
		if !r.IsError || !strings.Contains(r.Content[0].Text, "HTTP 404") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("10 non-grid response to opnsense_search is a tool error", func(t *testing.T) {
		if r := c.call("opnsense_search", map[string]any{"path": "/api/core/firmware/status"}); !r.IsError {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("11 unknown tool -> -32602; unknown method -> -32601", func(t *testing.T) {
		code := func(m map[string]any) any { e, _ := m["error"].(map[string]any); return e["code"] }
		if v := code(c.rpc("tools/call", map[string]any{"name": "nope", "arguments": map[string]any{}})); v != float64(-32602) {
			t.Errorf("unknown tool code %v", v)
		}
		if v := code(c.rpc("bogus/method", map[string]any{})); v != float64(-32601) {
			t.Errorf("unknown method code %v", v)
		}
	})
	c.close()

	t.Run("12 bad credentials -> isError with HTTP 401, no secret echoed", func(t *testing.T) {
		bad := startClient(t, append(base, "OPNSENSE_API_SECRET=wrong-secret-xyz", "OPNSENSE_VERIFY_TLS=false")...)
		bad.rpc("initialize", map[string]any{})
		r := bad.call("opnsense_firmware_status", map[string]any{})
		if !r.IsError || !strings.Contains(r.Content[0].Text, "HTTP 401") {
			t.Fatalf("got %+v", r)
		}
		if strings.Contains(bad.out(), "wrong-secret-xyz") {
			t.Fatal("secret on stdout")
		}
	})
	var strictOut string
	t.Run("13 TLS verify on vs self-signed cert -> clean tool error", func(t *testing.T) {
		strict := startClient(t, append(good, "OPNSENSE_VERIFY_TLS=true")...)
		r := strict.call("opnsense_firmware_status", map[string]any{})
		strict.close()
		strictOut = strict.out()
		if !r.IsError || !regexp.MustCompile(`(?i)SELF_SIGNED|self[- ]signed|certificate`).MatchString(r.Content[0].Text) || !strings.Contains(r.Content[0].Text, "OPNSENSE_VERIFY_TLS=false") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("14 unexpanded ${VAR} placeholders treated as not configured", func(t *testing.T) {
		unset := startClient(t, "OPNSENSE_URL=${OPNSENSE_URL}", "OPNSENSE_API_KEY=${OPNSENSE_API_KEY}", "OPNSENSE_API_SECRET=")
		r := unset.call("opnsense_services", map[string]any{})
		if !r.IsError || !strings.Contains(r.Content[0].Text, "not configured: set OPNSENSE_URL, OPNSENSE_API_KEY, OPNSENSE_API_SECRET") {
			t.Fatalf("got %+v", r)
		}
	})
	t.Run("15 credentials never appear on any stdout; stdout is JSON only", func(t *testing.T) {
		for _, o := range []string{c.out(), strictOut} {
			if strings.Contains(o, secret) || strings.Contains(o, key) || strings.Contains(o, "NON-JSON-LINE") {
				t.Fatal("credential or non-JSON line on stdout")
			}
		}
		if c.out() == "" || strictOut == "" {
			t.Fatal("captured no stdout")
		}
	})
}

func sortedKeys(m map[string]any) string {
	var k []string
	for x := range m {
		k = append(k, x)
	}
	sort.Strings(k)
	return strings.Join(k, " ")
}
