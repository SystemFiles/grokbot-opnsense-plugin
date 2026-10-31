---
name: opnsense
description: Inspect or change an OPNsense firewall through its HTTP API using the opnsense MCP tools. Use when the user asks about their OPNsense router/firewall - services, firmware/updates, interfaces, firewall rules, aliases, DHCP leases, DNS, VPN - or asks to change its config.
---

# OPNsense

Data shape: `OpnsenseCall = { method: GET|POST, path: "/api/<module>/<controller>/<command>[/params]", body? } -> json`; grid endpoints return `GridPage<Row> = { total, rowCount, current, rows: Row[] }`.

## When to call which tool

- Services up/down: `opnsense_services` (searchPhrase filters by name).
- Version / pending updates: `opnsense_firmware_status`.
- Interfaces: `opnsense_interfaces`.
- Firewall filter rules (MVC "Automation" rules only): `opnsense_firewall_rules`.
- Any other read: `opnsense_get` for plain GET endpoints, `opnsense_search` for endpoints named `search*` that return grid rows.
- Any write or action: `opnsense_post`.

Look up the module/controller/command in the official API reference (https://docs.opnsense.org/development/api.html) rather than guessing paths. A 404 usually means a wrong path or a plugin that isn't installed; a 403 means the API user lacks that privilege.

## Writes are dangerous

This is a live firewall; a bad write can drop the network or lock the user out of it.

1. Read the current state first.
2. Show the user the exact path and body and get an explicit yes for that call.
3. Only then call `opnsense_post` with `confirm: true`. Never set it pre-emptively.
4. Most config edits (`set`, `add_*`, `del_*`) take effect only after the module's apply/reconfigure call, which is a second write needing its own approval.

Never ask for, print, or echo the API key or secret; they come from the plugin variables.
