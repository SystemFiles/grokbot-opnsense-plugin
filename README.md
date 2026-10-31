# opnsense plugin

> **Unofficial community plugin.** Not affiliated with, endorsed by, or supported by OPNsense or Deciso B.V. "OPNsense" is a trademark of Deciso B.V.

`OpnsenseCall = { method: GET|POST, path: "/api/<module>/<controller>/<command>[/params]", body? } -> json` · `GridPage<Row> = { total, rowCount, current, rows: Row[] }`

Agent Plugin (with a Cursor Plugin manifest for variables) that wraps the [OPNsense HTTP API](https://docs.opnsense.org/development/api.html): a zero-dependency Node stdio MCP server plus an `opnsense` skill.

## Tools

| Tool | What | Writes? |
| --- | --- | --- |
| `opnsense_get(path)` | GET any `/api/...` path | no |
| `opnsense_search(path, searchPhrase?, rowCount?, current?)` | grid search endpoint -> GridPage | no |
| `opnsense_post(path, body?, confirm)` | create/update/delete/apply/actions; refuses unless `confirm === true` | **yes** |
| `opnsense_services(...)` | `/api/core/service/search` | no |
| `opnsense_firmware_status()` | `GET /api/core/firmware/status` | no |
| `opnsense_firewall_rules(...)` | `/api/firewall/filter/search_rule` | no |
| `opnsense_interfaces()` | `GET /api/interfaces/overview/interfaces_info` | no |

Paths must start with `/api/` and contain no scheme, host, `..` or `//`; requests only go to `OPNSENSE_URL`.

## Configuration (plugin variables)

| Variable | Notes |
| --- | --- |
| `OPNSENSE_URL` | e.g. `https://192.168.1.1` or `https://[2001:db8::1]` (IPv6 literals OK) |
| `OPNSENSE_API_KEY` / `OPNSENSE_API_SECRET` | System > Access > Users > API keys; sent as HTTP basic auth |
| `OPNSENSE_VERIFY_TLS` | `false` (default) accepts a self-signed cert; `true` verifies |

Secrets are never stored in this repo; `mcp.json` holds only `${VAR}` placeholders. The server needs Node 18+.

## Usage

1. In OPNsense, create an API key for a user (System > Access > Users > API keys) with only the privileges you need.
2. Install the plugin and set `OPNSENSE_URL`, `OPNSENSE_API_KEY`, `OPNSENSE_API_SECRET` (and `OPNSENSE_VERIFY_TLS` if you want certificate checks).
3. Ask the agent things like "show firmware status", "list services", or "search firewall rules for ssh". Any write goes through `opnsense_post` and only runs when you confirm it (`confirm: true`).

## Develop

```sh
node test/e2e.js        # JSON-RPC over stdio vs a mock OPNsense on https://[::1] (self-signed)
node test/validate.js   # plugin.json / mcp.json against the Agent Plugins 1.0.0 schemas
```

Local install in Cursor: copy (not symlink) this folder to `~/.cursor/plugins/local/opnsense`, then Developer: Reload Window.

## License

MIT © 2026 Ben Sykes. See [LICENSE](LICENSE).
