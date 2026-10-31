# opnsense plugin

> **Unofficial community plugin.** Not affiliated with, endorsed by, or supported by OPNsense or Deciso B.V. "OPNsense" is a trademark of Deciso B.V.

`OpnsenseCall = { method: GET|POST, path: "/api/<module>/<controller>/<command>[/params]", body? } -> json` · `GridPage<Row> = { total, rowCount, current, rows: Row[] }`

Agent Plugin (with a Cursor Plugin manifest for variables) that wraps the [OPNsense HTTP API](https://docs.opnsense.org/development/api.html): a stdio MCP server written in Go using only the standard library, plus an `opnsense` skill. There is no official OPNsense Go client library, so the server talks to the API directly with `net/http`.

## Tools

| Tool | What | Writes? |
| --- | --- | --- |
| `opnsense_get(path)` | GET any `/api/...` path | no |
| `opnsense_search(path, searchPhrase?, rowCount?, current?)` | grid search endpoint -> GridPage | no |
| `opnsense_post(path, body?, confirm)` | create/update/delete/apply/actions; refuses unless `confirm` is boolean `true` | **yes** |
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

Secrets are never stored in this repo; `mcp.json` holds only `${VAR}` placeholders. Optional: `OPNSENSE_TIMEOUT_MS` (default 20000).

## Usage

1. In OPNsense, create an API key for a user (System > Access > Users > API keys) with only the privileges you need.
2. Install the plugin and set `OPNSENSE_URL`, `OPNSENSE_API_KEY`, `OPNSENSE_API_SECRET` (and `OPNSENSE_VERIFY_TLS` if you want certificate checks).
3. Ask the agent things like "show firmware status", "list services", or "search firewall rules for ssh". Any write goes through `opnsense_post` and only runs when you confirm it (`confirm: true`).

## How it runs

`mcp.json` starts `sh ./bin/opnsense-mcp`, a small POSIX launcher. It picks the prebuilt binary for your OS/arch (linux/amd64, linux/arm64, darwin/amd64, darwin/arm64, windows/amd64), checks it against `bin/SHA256SUMS`, unpacks it once into `~/.cache/opnsense-mcp/`, and runs it. Nothing else is needed: no Node, no Go, no network.

- Binaries ship gzipped (`bin/*.gz`, about 14 MB in total) to keep the repo small; they are reproducible from this source with the Go version in `bin/GOVERSION`, and `go test` checks that.
- No prebuilt binary for your platform? If `go` is installed, the launcher builds from source (`go build ./cmd/opnsense-mcp`, standard library only). Otherwise it exits with an error on stderr.
- Windows: the launcher needs `sh` on `PATH` (Git for Windows provides it). Without it, unpack `bin/opnsense-mcp-windows-amd64.exe.gz` and point `command` in `mcp.json` at that `.exe` with no `args`.

## Develop

Requires Go (see `go.mod`); no third-party modules.

```sh
go vet ./... && go test ./...   # unit tests + 15 end-to-end checks: real binary, JSON-RPC over stdio, mock OPNsense on https://[::1] (self-signed)
make build                      # cross-compile all targets into bin/ (raw binaries are git-ignored; commit the .gz + SHA256SUMS + GOVERSION)
node test/validate.js           # optional, dev-only: plugin.json / mcp.json against the Agent Plugins 1.0.0 schemas (needs Node + network)
```

Layout: `cmd/opnsense-mcp` (entry point and end-to-end tests), `internal/mcp` (JSON-RPC server and tool definitions), `internal/opnsense` (config, path guard, HTTP client).

Local install in Cursor: copy (not symlink) this folder to `~/.cursor/plugins/local/opnsense`, then Developer: Reload Window.

## License

MIT © 2026 Ben Sykes. See [LICENSE](LICENSE).
