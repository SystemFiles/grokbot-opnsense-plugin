#!/usr/bin/env node
// OPNsense MCP server: zero-dependency, JSON-RPC 2.0 over stdio (newline-delimited).
// Data shape: OpnsenseCall = { method, path:"/api/<module>/<controller>/<command>[/params]", body? } -> json
//             GridPage<Row> = { total, rowCount, current, rows: Row[] }
'use strict';
const https = require('node:https');
const http = require('node:http');
const readline = require('node:readline');

const SERVER_INFO = { name: 'opnsense', version: '0.1.0' };
const DEFAULT_PROTOCOL = '2025-06-18';
const MAX_BODY = 4 * 1024 * 1024;
const TIMEOUT_MS = Number(process.env.OPNSENSE_TIMEOUT_MS) || 20000;

// ---------- config ----------
function envVar(name) {
  const v = process.env[name];
  // A placeholder the host did not expand (e.g. "${OPNSENSE_URL}") counts as unset.
  if (v == null || v === '' || /^\$\{[^}]*\}$/.test(v)) return undefined;
  return v;
}
function loadConfig() {
  const url = envVar('OPNSENSE_URL');
  const key = envVar('OPNSENSE_API_KEY');
  const secret = envVar('OPNSENSE_API_SECRET');
  const verifyRaw = (envVar('OPNSENSE_VERIFY_TLS') || 'false').toLowerCase();
  const missing = [];
  if (!url) missing.push('OPNSENSE_URL');
  if (!key) missing.push('OPNSENSE_API_KEY');
  if (!secret) missing.push('OPNSENSE_API_SECRET');
  if (missing.length) throw new ToolError(`Plugin not configured: set ${missing.join(', ')} in the plugin variables.`);
  let u;
  try { u = new URL(url); } catch { throw new ToolError('OPNSENSE_URL is not a valid URL (expected e.g. https://[2001:db8::1]).'); }
  if (u.protocol !== 'https:' && u.protocol !== 'http:') throw new ToolError('OPNSENSE_URL must be http(s).');
  if (u.username || u.password) throw new ToolError('Put credentials in OPNSENSE_API_KEY/SECRET, not in OPNSENSE_URL.');
  return {
    protocol: u.protocol,
    hostname: u.hostname.replace(/^\[|\]$/g, ''), // node wants IPv6 literals without brackets
    port: u.port || undefined,
    basePath: u.pathname.replace(/\/+$/, ''),
    key, secret,
    verifyTls: ['1', 'true', 'yes', 'on'].includes(verifyRaw),
  };
}

class ToolError extends Error {}

// ---------- path guard ----------
const PATH_RE = /^\/api\/[A-Za-z0-9_.~%\-]+(\/[A-Za-z0-9_.~%:\-]+)*\/?(\?[A-Za-z0-9_.~%=&+\-]*)?$/;
function checkPath(p) {
  if (typeof p !== 'string') throw new ToolError('path must be a string like /api/core/service/search');
  if (!PATH_RE.test(p) || p.includes('..') || p.includes('//')) {
    throw new ToolError(`Refused path ${JSON.stringify(p.slice(0, 200))}: must be a relative OPNsense API path starting with /api/ (no scheme, host, "..", or "//").`);
  }
  return p;
}

// ---------- HTTP ----------
function scrub(text, cfg) {
  let s = String(text);
  for (const v of [cfg && cfg.key, cfg && cfg.secret]) if (v) s = s.split(v).join('[redacted]');
  return s;
}
function request(method, path, body) {
  const cfg = loadConfig();
  checkPath(path);
  const lib = cfg.protocol === 'https:' ? https : http;
  const payload = body === undefined ? undefined : JSON.stringify(body);
  const headers = {
    Accept: 'application/json',
    Authorization: 'Basic ' + Buffer.from(`${cfg.key}:${cfg.secret}`).toString('base64'),
  };
  if (payload !== undefined) { headers['Content-Type'] = 'application/json'; headers['Content-Length'] = Buffer.byteLength(payload); }
  return new Promise((resolve, reject) => {
    const req = lib.request({
      method, hostname: cfg.hostname, port: cfg.port, path: cfg.basePath + path, headers,
      rejectUnauthorized: cfg.verifyTls, timeout: TIMEOUT_MS,
    }, (res) => {
      const chunks = []; let size = 0;
      res.on('data', (c) => { size += c.length; if (size > MAX_BODY) { req.destroy(new Error('response larger than 4 MB')); } else chunks.push(c); });
      res.on('end', () => {
        const text = Buffer.concat(chunks).toString('utf8');
        if (res.statusCode < 200 || res.statusCode >= 300) {
          const hint = res.statusCode === 401 ? ' (check OPNSENSE_API_KEY/SECRET)' : res.statusCode === 403 ? ' (API user lacks privilege for this endpoint)' : '';
          return reject(new ToolError(`OPNsense ${method} ${path} -> HTTP ${res.statusCode}${hint}: ${scrub(text, cfg).trim().slice(0, 500)}`));
        }
        try { resolve(text.trim() === '' ? {} : JSON.parse(text)); }
        catch { resolve({ raw: text.slice(0, 20000) }); }
      });
    });
    req.on('timeout', () => req.destroy(new Error(`timed out after ${TIMEOUT_MS} ms`)));
    req.on('error', (e) => {
      const tlsHint = /self[- ]signed|certificate|CERT_|UNABLE_TO_VERIFY/i.test(e.code + ' ' + e.message)
        ? ' (TLS verification failed; set OPNSENSE_VERIFY_TLS=false for a self-signed cert)' : '';
      reject(new ToolError(`OPNsense ${method} ${path} failed: ${e.code ? e.code + ' ' : ''}${scrub(e.message, cfg)}${tlsHint}`));
    });
    if (payload !== undefined) req.write(payload);
    req.end();
  });
}

function toGridPage(data, path) {
  if (!data || !Array.isArray(data.rows)) throw new ToolError(`${path} did not return a grid ({total,rowCount,current,rows}); use opnsense_get or opnsense_post instead.`);
  return { total: data.total ?? data.rows.length, rowCount: data.rowCount ?? data.rows.length, current: data.current ?? 1, rows: data.rows };
}
async function search(path, a = {}) {
  const body = {
    current: Number.isInteger(a.current) && a.current > 0 ? a.current : 1,
    rowCount: Number.isInteger(a.rowCount) ? a.rowCount : 50,
    sort: {},
    searchPhrase: typeof a.searchPhrase === 'string' ? a.searchPhrase : '',
  };
  return toGridPage(await request('POST', path, body), path);
}

// ---------- tools ----------
const gridArgs = {
  searchPhrase: { type: 'string', description: 'Free-text filter, same as the UI search box.' },
  rowCount: { type: 'integer', description: 'Rows per page (default 50, -1 = all).' },
  current: { type: 'integer', description: 'Page number, 1-based (default 1).' },
};
const RO = { readOnlyHint: true, destructiveHint: false, openWorldHint: true };
const TOOLS = [
  {
    name: 'opnsense_get',
    description: 'Read-only GET of an OPNsense API path, e.g. /api/core/system/status or /api/firewall/alias/get. Returns the JSON response.',
    inputSchema: { type: 'object', properties: { path: { type: 'string', description: 'Path starting with /api/<module>/<controller>/<command>[/params]' } }, required: ['path'], additionalProperties: false },
    annotations: RO,
    run: (a) => request('GET', checkPath(a.path)),
  },
  {
    name: 'opnsense_search',
    description: 'Query an OPNsense grid search endpoint (e.g. /api/firewall/alias/search_item, /api/kea/dhcpv4/search_lease) and return a GridPage {total,rowCount,current,rows}. Read-only.',
    inputSchema: { type: 'object', properties: { path: { type: 'string', description: 'Grid search path starting with /api/' }, ...gridArgs }, required: ['path'], additionalProperties: false },
    annotations: RO,
    run: (a) => search(checkPath(a.path), a),
  },
  {
    name: 'opnsense_post',
    description: 'POST to an OPNsense API path to create/update/delete config or run an action (apply, reconfigure, restart, reboot, firmware update). THIS CHANGES A LIVE FIREWALL: a bad write can cut network access or lock the owner out. Refuses unless confirm is exactly true. Only set confirm=true after the user has approved this exact path and body. Remember most config changes also need a separate apply/reconfigure call.',
    inputSchema: {
      type: 'object',
      properties: {
        path: { type: 'string', description: 'Path starting with /api/' },
        body: { type: 'object', description: 'JSON body (omit for bodyless actions; {} is sent).' },
        confirm: { type: 'boolean', description: 'Must be true, set only after the user approved this exact call.' },
      },
      required: ['path', 'confirm'], additionalProperties: false,
    },
    annotations: { readOnlyHint: false, destructiveHint: true, idempotentHint: false, openWorldHint: true },
    run: (a) => {
      checkPath(a.path);
      if (a.confirm !== true) throw new ToolError(`Refused: opnsense_post to ${a.path} needs confirm=true. This writes to a live firewall; show the user the exact path and body and get approval first.`);
      return request('POST', a.path, a.body === undefined ? {} : a.body);
    },
  },
  {
    name: 'opnsense_services',
    description: 'List services and whether each is running (GridPage of {id,name,description,running,locked}). Read-only; wraps /api/core/service/search.',
    inputSchema: { type: 'object', properties: gridArgs, additionalProperties: false },
    annotations: RO,
    run: (a) => search('/api/core/service/search', a),
  },
  {
    name: 'opnsense_firmware_status',
    description: 'Current firmware/product version and pending update status. Read-only; wraps GET /api/core/firmware/status.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    annotations: RO,
    run: () => request('GET', '/api/core/firmware/status'),
  },
  {
    name: 'opnsense_firewall_rules',
    description: 'Search firewall filter rules managed by the MVC "Automation/Filter" module (GridPage). Read-only; wraps /api/firewall/filter/search_rule. Legacy rules from Firewall > Rules are not included.',
    inputSchema: { type: 'object', properties: gridArgs, additionalProperties: false },
    annotations: RO,
    run: (a) => search('/api/firewall/filter/search_rule', a),
  },
  {
    name: 'opnsense_interfaces',
    description: 'Interfaces overview as reported by the OPNsense dashboard. Read-only; wraps GET /api/interfaces/overview/interfaces_info.',
    inputSchema: { type: 'object', properties: {}, additionalProperties: false },
    annotations: RO,
    run: () => request('GET', '/api/interfaces/overview/interfaces_info'),
  },
];
const TOOL_MAP = new Map(TOOLS.map((t) => [t.name, t]));

// ---------- JSON-RPC ----------
function send(msg) { process.stdout.write(JSON.stringify(msg) + '\n'); }
function reply(id, result) { send({ jsonrpc: '2.0', id, result }); }
function fail(id, code, message) { send({ jsonrpc: '2.0', id, error: { code, message } }); }
function textResult(data, isError = false) {
  const text = typeof data === 'string' ? data : JSON.stringify(data, null, 2);
  const out = { content: [{ type: 'text', text }], isError };
  if (!isError && data && typeof data === 'object' && !Array.isArray(data)) out.structuredContent = data;
  return out;
}

async function handle(msg) {
  if (!msg || msg.jsonrpc !== '2.0' || typeof msg.method !== 'string') {
    if (msg && msg.id !== undefined) fail(msg.id, -32600, 'Invalid Request');
    return;
  }
  const isNotification = msg.id === undefined || msg.id === null;
  if (isNotification) return; // notifications/initialized, cancelled, etc.
  const { id, method, params = {} } = msg;
  switch (method) {
    case 'initialize':
      return reply(id, {
        protocolVersion: typeof params.protocolVersion === 'string' ? params.protocolVersion : DEFAULT_PROTOCOL,
        capabilities: { tools: { listChanged: false } },
        serverInfo: SERVER_INFO,
        instructions: 'OPNsense firewall API. Prefer read tools; opnsense_post changes the live firewall and requires confirm=true after explicit user approval.',
      });
    case 'ping':
      return reply(id, {});
    case 'tools/list':
      return reply(id, { tools: TOOLS.map(({ run, ...t }) => t) });
    case 'tools/call': {
      const tool = TOOL_MAP.get(params.name);
      if (!tool) return fail(id, -32602, `Unknown tool: ${params.name}`);
      try {
        return reply(id, textResult(await tool.run(params.arguments || {})));
      } catch (e) {
        const m = e instanceof ToolError ? e.message : `Unexpected error: ${e && e.message}`;
        return reply(id, textResult(m, true));
      }
    }
    default:
      return fail(id, -32601, `Method not found: ${method}`);
  }
}

let inflight = 0, closed = false;
function track(p) {
  inflight++;
  p.catch((e) => process.stderr.write(String(e) + '\n')).finally(() => { inflight--; if (closed && inflight === 0) process.exit(0); });
}
const rl = readline.createInterface({ input: process.stdin, crlfDelay: Infinity });
rl.on('line', (line) => {
  if (!line.trim()) return;
  let msg;
  try { msg = JSON.parse(line); } catch { return send({ jsonrpc: '2.0', id: null, error: { code: -32700, message: 'Parse error' } }); }
  (Array.isArray(msg) ? msg : [msg]).forEach((m) => track(handle(m)));
});
rl.on('close', () => { closed = true; if (inflight === 0) process.exit(0); });
