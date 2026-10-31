// Package opnsense is a small stdlib-only client for the OPNsense HTTP API.
//
// Data shape: OpnsenseCall = { method, path:"/api/<module>/<controller>/<command>[/params]", body? } -> json
//
//	GridPage<Row> = { total, rowCount, current, rows: Row[] }
package opnsense

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxBody = 4 * 1024 * 1024

// Error is a user-facing tool error; its message is safe to show (credentials scrubbed).
type Error struct{ Msg string }

func (e *Error) Error() string { return e.Msg }

func errorf(format string, a ...any) error { return &Error{Msg: fmt.Sprintf(format, a...)} }

// Config is read from the environment on every call, like the original server.
type Config struct {
	Base      string // scheme://host[:port][/basePath] without trailing slash
	Key       string
	Secret    string
	VerifyTLS bool
}

var placeholder = regexp.MustCompile(`^\$\{[^}]*\}$`)

// EnvVar returns the variable, treating empty and unexpanded "${VAR}" placeholders as unset.
func EnvVar(name string) (string, bool) {
	v, ok := os.LookupEnv(name)
	if !ok || v == "" || placeholder.MatchString(v) {
		return "", false
	}
	return v, true
}

// LoadConfig validates OPNSENSE_URL / _API_KEY / _API_SECRET / _VERIFY_TLS.
func LoadConfig() (*Config, error) {
	rawURL, okURL := EnvVar("OPNSENSE_URL")
	key, okKey := EnvVar("OPNSENSE_API_KEY")
	secret, okSecret := EnvVar("OPNSENSE_API_SECRET")
	verifyRaw, ok := EnvVar("OPNSENSE_VERIFY_TLS")
	if !ok {
		verifyRaw = "false"
	}
	var missing []string
	if !okURL {
		missing = append(missing, "OPNSENSE_URL")
	}
	if !okKey {
		missing = append(missing, "OPNSENSE_API_KEY")
	}
	if !okSecret {
		missing = append(missing, "OPNSENSE_API_SECRET")
	}
	if len(missing) > 0 {
		return nil, errorf("Plugin not configured: set %s in the plugin variables.", strings.Join(missing, ", "))
	}
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme == "" || u.Host == "" {
		return nil, errorf("OPNSENSE_URL is not a valid URL (expected e.g. https://[2001:db8::1]).")
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return nil, errorf("OPNSENSE_URL must be http(s).")
	}
	if u.User != nil {
		return nil, errorf("Put credentials in OPNSENSE_API_KEY/SECRET, not in OPNSENSE_URL.")
	}
	switch strings.ToLower(verifyRaw) {
	case "1", "true", "yes", "on":
		ok = true
	default:
		ok = false
	}
	return &Config{
		Base:      u.Scheme + "://" + u.Host + strings.TrimRight(u.EscapedPath(), "/"),
		Key:       key,
		Secret:    secret,
		VerifyTLS: ok,
	}, nil
}

var pathRE = regexp.MustCompile(`^/api/[A-Za-z0-9_.~%\-]+(/[A-Za-z0-9_.~%:\-]+)*/?(\?[A-Za-z0-9_.~%=&+\-]*)?$`)

// CheckPath allows only relative /api/... paths: no scheme, host, "..", or "//".
func CheckPath(p any) (string, error) {
	s, ok := p.(string)
	if !ok {
		return "", errorf("path must be a string like /api/core/service/search")
	}
	if !pathRE.MatchString(s) || strings.Contains(s, "..") || strings.Contains(s, "//") {
		r := []rune(s)
		if len(r) > 200 {
			r = r[:200]
		}
		return "", errorf("Refused path %s: must be a relative OPNsense API path starting with /api/ (no scheme, host, \"..\", or \"//\").", JSONString(string(r)))
	}
	return s, nil
}

// JSONString encodes s like JavaScript's JSON.stringify (no HTML escaping).
func JSONString(s string) string {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}

func scrub(s string, c *Config) string {
	for _, v := range []string{c.Key, c.Secret} {
		if v != "" {
			s = strings.ReplaceAll(s, v, "[redacted]")
		}
	}
	return s
}

func truncRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

func timeout() time.Duration {
	if v, err := strconv.Atoi(os.Getenv("OPNSENSE_TIMEOUT_MS")); err == nil && v > 0 {
		return time.Duration(v) * time.Millisecond
	}
	return 20 * time.Second
}

var (
	clientsMu sync.Mutex
	clients   = map[bool]*http.Client{}
)

func httpClient(verify bool) *http.Client {
	clientsMu.Lock()
	defer clientsMu.Unlock()
	if c, ok := clients[verify]; ok {
		return c
	}
	c := &http.Client{
		Timeout: timeout(),
		Transport: &http.Transport{
			Proxy:             nil,                                      // requests only ever go to OPNSENSE_URL
			TLSClientConfig:   &tls.Config{InsecureSkipVerify: !verify}, // #nosec G402 -- opt-in via OPNSENSE_VERIFY_TLS=false for self-signed firewalls
			ForceAttemptHTTP2: true,
			IdleConnTimeout:   30 * time.Second,
		},
		// Never follow redirects: credentials must not leave OPNSENSE_URL.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	clients[verify] = c
	return c
}

var tlsWords = regexp.MustCompile(`(?i)self[- ]signed|certificate|x509|CERT_|UNABLE_TO_VERIFY`)

// Request performs one API call. body == nil means no body. It returns the
// response JSON as raw bytes (normalised: empty -> {}, non-JSON -> {"raw": ...}).
func Request(method, path string, body json.RawMessage) (json.RawMessage, error) {
	cfg, err := LoadConfig()
	if err != nil {
		return nil, err
	}
	if _, err := CheckPath(path); err != nil {
		return nil, err
	}
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequest(method, cfg.Base+path, rdr)
	if err != nil {
		return nil, errorf("OPNsense %s %s failed: %s", method, path, scrub(err.Error(), cfg))
	}
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(cfg.Key, cfg.Secret)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := httpClient(cfg.VerifyTLS).Do(req)
	if err != nil {
		var ue *url.Error
		inner := err
		if errors.As(err, &ue) {
			inner = ue.Err
		}
		msg := scrub(inner.Error(), cfg)
		var ne interface{ Timeout() bool }
		if errors.As(err, &ne) && ne.Timeout() {
			msg = fmt.Sprintf("timed out after %d ms", timeout().Milliseconds())
		}
		hint := ""
		if tlsWords.MatchString(msg) {
			hint = " (TLS verification failed; set OPNSENSE_VERIFY_TLS=false for a self-signed cert)"
		}
		return nil, errorf("OPNsense %s %s failed: %s%s", method, path, msg, hint)
	}
	defer res.Body.Close()
	data, err := io.ReadAll(io.LimitReader(res.Body, maxBody+1))
	if err == nil && len(data) > maxBody {
		err = errors.New("response larger than 4 MB")
	}
	if err != nil {
		return nil, errorf("OPNsense %s %s failed: %s", method, path, scrub(err.Error(), cfg))
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		hint := ""
		switch res.StatusCode {
		case 401:
			hint = " (check OPNSENSE_API_KEY/SECRET)"
		case 403:
			hint = " (API user lacks privilege for this endpoint)"
		}
		return nil, errorf("OPNsense %s %s -> HTTP %d%s: %s", method, path, res.StatusCode, hint,
			truncRunes(strings.TrimSpace(scrub(string(data), cfg)), 500))
	}
	if strings.TrimSpace(string(data)) == "" {
		return json.RawMessage(`{}`), nil
	}
	if !json.Valid(data) {
		raw, _ := json.Marshal(map[string]string{"raw": truncRunes(string(data), 20000)})
		return raw, nil
	}
	return bytes.TrimSpace(data), nil
}

// GridPage is the normalised result of a grid search endpoint.
type GridPage struct {
	Total    json.RawMessage   `json:"total"`
	RowCount json.RawMessage   `json:"rowCount"`
	Current  json.RawMessage   `json:"current"`
	Rows     []json.RawMessage `json:"rows"`
}

func present(r json.RawMessage) bool { return len(r) > 0 && string(r) != "null" }

// ToGridPage checks that data is {rows:[...]} and fills total/rowCount/current defaults.
func ToGridPage(data json.RawMessage, path string) (json.RawMessage, error) {
	var g struct {
		Total, RowCount, Current json.RawMessage
		Rows                     json.RawMessage
	}
	var rows []json.RawMessage
	if json.Unmarshal(data, &g) != nil || len(g.Rows) == 0 || g.Rows[0] != '[' || json.Unmarshal(g.Rows, &rows) != nil {
		return nil, errorf("%s did not return a grid ({total,rowCount,current,rows}); use opnsense_get or opnsense_post instead.", path)
	}
	if rows == nil {
		rows = []json.RawMessage{}
	}
	n := json.RawMessage(strconv.Itoa(len(rows)))
	out := GridPage{Total: n, RowCount: n, Current: json.RawMessage("1"), Rows: rows}
	if present(g.Total) {
		out.Total = g.Total
	}
	if present(g.RowCount) {
		out.RowCount = g.RowCount
	}
	if present(g.Current) {
		out.Current = g.Current
	}
	return json.Marshal(out)
}

// SearchArgs are the optional grid arguments, already decoded from tool input.
type SearchArgs struct {
	SearchPhrase json.RawMessage
	RowCount     json.RawMessage
	Current      json.RawMessage
}

func integer(r json.RawMessage) (float64, bool) {
	if len(r) == 0 || (r[0] != '-' && (r[0] < '0' || r[0] > '9')) { // JSON numbers only, like Number.isInteger
		return 0, false
	}
	var f float64
	if json.Unmarshal(r, &f) != nil || math.IsInf(f, 0) || math.Trunc(f) != f {
		return 0, false
	}
	return f, true
}

// Search POSTs {current,rowCount,sort:{},searchPhrase} and returns a GridPage.
func Search(path string, a SearchArgs) (json.RawMessage, error) {
	type body struct {
		Current      json.Number `json:"current"`
		RowCount     json.Number `json:"rowCount"`
		Sort         struct{}    `json:"sort"`
		SearchPhrase string      `json:"searchPhrase"`
	}
	b := body{Current: "1", RowCount: "50"}
	if f, ok := integer(a.Current); ok && f > 0 {
		b.Current = json.Number(strconv.FormatFloat(f, 'f', -1, 64))
	}
	if f, ok := integer(a.RowCount); ok {
		b.RowCount = json.Number(strconv.FormatFloat(f, 'f', -1, 64))
	}
	if len(a.SearchPhrase) > 0 && a.SearchPhrase[0] == '"' {
		_ = json.Unmarshal(a.SearchPhrase, &b.SearchPhrase)
	}
	payload, _ := json.Marshal(b)
	data, err := Request("POST", path, payload)
	if err != nil {
		return nil, err
	}
	return ToGridPage(data, path)
}
