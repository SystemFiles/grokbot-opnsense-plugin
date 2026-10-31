package mcp

import (
	"encoding/json"
	"fmt"

	"github.com/SystemFiles/grokbot-opnsense-plugin/internal/opnsense"
)

// args is the decoded tools/call "arguments" object; values stay raw so that
// type checks match the original JavaScript semantics exactly.
type args map[string]json.RawMessage

func (a args) str(k string) any {
	var s string
	if r, ok := a[k]; ok && len(r) > 0 && r[0] == '"' && json.Unmarshal(r, &s) == nil {
		return s
	}
	return nil
}

func (a args) grid() opnsense.SearchArgs {
	return opnsense.SearchArgs{SearchPhrase: a["searchPhrase"], RowCount: a["rowCount"], Current: a["current"]}
}

type tool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations"`
	run         func(args) (json.RawMessage, error)
}

const (
	gridProps = `"searchPhrase":{"type":"string","description":"Free-text filter, same as the UI search box."},"rowCount":{"type":"integer","description":"Rows per page (default 50, -1 = all)."},"current":{"type":"integer","description":"Page number, 1-based (default 1)."}`
	ro        = `{"readOnlyHint":true,"destructiveHint":false,"openWorldHint":true}`
)

func raw(s string) json.RawMessage { return json.RawMessage(s) }

var tools = []tool{
	{
		Name:        "opnsense_get",
		Description: "Read-only GET of an OPNsense API path, e.g. /api/core/system/status or /api/firewall/alias/get. Returns the JSON response.",
		InputSchema: raw(`{"type":"object","properties":{"path":{"type":"string","description":"Path starting with /api/<module>/<controller>/<command>[/params]"}},"required":["path"],"additionalProperties":false}`),
		Annotations: raw(ro),
		run: func(a args) (json.RawMessage, error) {
			p, err := opnsense.CheckPath(a.str("path"))
			if err != nil {
				return nil, err
			}
			return opnsense.Request("GET", p, nil)
		},
	},
	{
		Name:        "opnsense_search",
		Description: "Query an OPNsense grid search endpoint (e.g. /api/firewall/alias/search_item, /api/kea/dhcpv4/search_lease) and return a GridPage {total,rowCount,current,rows}. Read-only.",
		InputSchema: raw(`{"type":"object","properties":{"path":{"type":"string","description":"Grid search path starting with /api/"},` + gridProps + `},"required":["path"],"additionalProperties":false}`),
		Annotations: raw(ro),
		run: func(a args) (json.RawMessage, error) {
			p, err := opnsense.CheckPath(a.str("path"))
			if err != nil {
				return nil, err
			}
			return opnsense.Search(p, a.grid())
		},
	},
	{
		Name:        "opnsense_post",
		Description: "POST to an OPNsense API path to create/update/delete config or run an action (apply, reconfigure, restart, reboot, firmware update). THIS CHANGES A LIVE FIREWALL: a bad write can cut network access or lock the owner out. Refuses unless confirm is exactly true. Only set confirm=true after the user has approved this exact path and body. Remember most config changes also need a separate apply/reconfigure call.",
		InputSchema: raw(`{"type":"object","properties":{"path":{"type":"string","description":"Path starting with /api/"},"body":{"type":"object","description":"JSON body (omit for bodyless actions; {} is sent)."},"confirm":{"type":"boolean","description":"Must be true, set only after the user approved this exact call."}},"required":["path","confirm"],"additionalProperties":false}`),
		Annotations: raw(`{"readOnlyHint":false,"destructiveHint":true,"idempotentHint":false,"openWorldHint":true}`),
		run: func(a args) (json.RawMessage, error) {
			p, err := opnsense.CheckPath(a.str("path"))
			if err != nil {
				return nil, err
			}
			// Only the JSON literal true counts: not "true", 1, or a missing value.
			if string(a["confirm"]) != "true" {
				return nil, &opnsense.Error{Msg: fmt.Sprintf("Refused: opnsense_post to %s needs confirm=true. This writes to a live firewall; show the user the exact path and body and get approval first.", p)}
			}
			body, ok := a["body"]
			if !ok {
				body = raw(`{}`)
			}
			return opnsense.Request("POST", p, body)
		},
	},
	{
		Name:        "opnsense_services",
		Description: "List services and whether each is running (GridPage of {id,name,description,running,locked}). Read-only; wraps /api/core/service/search.",
		InputSchema: raw(`{"type":"object","properties":{` + gridProps + `},"additionalProperties":false}`),
		Annotations: raw(ro),
		run:         func(a args) (json.RawMessage, error) { return opnsense.Search("/api/core/service/search", a.grid()) },
	},
	{
		Name:        "opnsense_firmware_status",
		Description: "Current firmware/product version and pending update status. Read-only; wraps GET /api/core/firmware/status.",
		InputSchema: raw(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: raw(ro),
		run:         func(args) (json.RawMessage, error) { return opnsense.Request("GET", "/api/core/firmware/status", nil) },
	},
	{
		Name:        "opnsense_firewall_rules",
		Description: `Search firewall filter rules managed by the MVC "Automation/Filter" module (GridPage). Read-only; wraps /api/firewall/filter/search_rule. Legacy rules from Firewall > Rules are not included.`,
		InputSchema: raw(`{"type":"object","properties":{` + gridProps + `},"additionalProperties":false}`),
		Annotations: raw(ro),
		run: func(a args) (json.RawMessage, error) {
			return opnsense.Search("/api/firewall/filter/search_rule", a.grid())
		},
	},
	{
		Name:        "opnsense_interfaces",
		Description: "Interfaces overview as reported by the OPNsense dashboard. Read-only; wraps GET /api/interfaces/overview/interfaces_info.",
		InputSchema: raw(`{"type":"object","properties":{},"additionalProperties":false}`),
		Annotations: raw(ro),
		run: func(args) (json.RawMessage, error) {
			return opnsense.Request("GET", "/api/interfaces/overview/interfaces_info", nil)
		},
	},
}

func findTool(name string) *tool {
	for i := range tools {
		if tools[i].Name == name {
			return &tools[i]
		}
	}
	return nil
}
