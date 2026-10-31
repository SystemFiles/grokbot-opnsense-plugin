package opnsense

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestCheckPath(t *testing.T) {
	ok := []string{"/api/core/service/search", "/api/core/service/restart/sshd", "/api/firewall/alias/get/", "/api/a/b/c?x=1&y=2", "/api/kea/dhcpv4/get_lease/aa:bb"}
	bad := []any{nil, 5, "", "api/core", "/api/", "/api/../x", "/api//x", "https://h/api/x", "/api/x@h", "/api/x y", "/api/core\r\nHost: x", "/other/x"}
	for _, p := range ok {
		if _, err := CheckPath(p); err != nil {
			t.Errorf("%q refused: %v", p, err)
		}
	}
	for _, p := range bad {
		if _, err := CheckPath(p); err == nil {
			t.Errorf("%#v allowed", p)
		}
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("OPNSENSE_URL", "https://[2001:db8::1]:8443/")
	t.Setenv("OPNSENSE_API_KEY", "k")
	t.Setenv("OPNSENSE_API_SECRET", "s")
	t.Setenv("OPNSENSE_VERIFY_TLS", "${OPNSENSE_VERIFY_TLS}")
	c, err := LoadConfig()
	if err != nil || c.Base != "https://[2001:db8::1]:8443" || c.VerifyTLS {
		t.Fatalf("%+v %v", c, err)
	}
	for _, v := range []string{"1", "TRUE", "yes", "on"} {
		t.Setenv("OPNSENSE_VERIFY_TLS", v)
		if c, _ := LoadConfig(); !c.VerifyTLS {
			t.Errorf("%s should verify", v)
		}
	}
	for url, want := range map[string]string{"ftp://h": "must be http(s)", "https://u:p@h": "Put credentials", "nope": "not a valid URL"} {
		t.Setenv("OPNSENSE_URL", url)
		if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v", url, err)
		}
	}
}

func TestToGridPage(t *testing.T) {
	out, err := ToGridPage(json.RawMessage(`{"rows":[{"a":1}],"total":null}`), "/api/x")
	if err != nil || string(out) != `{"total":1,"rowCount":1,"current":1,"rows":[{"a":1}]}` {
		t.Fatalf("%s %v", out, err)
	}
	for _, in := range []string{`{}`, `[]`, `{"rows":{}}`, `"x"`} {
		if _, err := ToGridPage(json.RawMessage(in), "/api/x"); err == nil {
			t.Errorf("%s accepted", in)
		}
	}
}

func TestInteger(t *testing.T) {
	for in, want := range map[string]bool{"5": true, "5.0": true, "-1": true, "5.5": false, `"5"`: false, "true": false, "": false, "null": false} {
		if _, got := integer(json.RawMessage(in)); got != want {
			t.Errorf("%s: got %v", in, got)
		}
	}
}
