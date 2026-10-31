#!/usr/bin/env node
// End-to-end proof: spawn server/index.js, speak real JSON-RPC over stdio, against a
// mock OPNsense on https://[::1] with a throwaway self-signed cert and basic auth.
'use strict';
const { spawn, execFileSync } = require('node:child_process');
const https = require('node:https');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const assert = require('node:assert/strict');

const KEY = 'test-key-' + Math.random().toString(36).slice(2);
const SECRET = 'test-secret-' + Math.random().toString(36).slice(2);
const dir = fs.mkdtempSync(path.join(os.tmpdir(), 'opn-'));
execFileSync('openssl', ['req', '-x509', '-newkey', 'rsa:2048', '-nodes', '-days', '1', '-subj', '/CN=opnsense.mock',
  '-keyout', path.join(dir, 'k.pem'), '-out', path.join(dir, 'c.pem')], { stdio: 'ignore' });

const rows = [
  { id: 'unbound', locked: 0, running: 1, description: 'Unbound DNS', name: 'unbound' },
  { id: 'sshd', locked: 0, running: 0, description: 'Secure Shell Daemon', name: 'openssh' },
];
const seen = [];
const mock = https.createServer({ key: fs.readFileSync(path.join(dir, 'k.pem')), cert: fs.readFileSync(path.join(dir, 'c.pem')) }, (req, res) => {
  let body = '';
  req.on('data', (c) => (body += c));
  req.on('end', () => {
    seen.push({ method: req.method, url: req.url, body });
    const want = 'Basic ' + Buffer.from(`${KEY}:${SECRET}`).toString('base64');
    const json = (code, obj) => { res.writeHead(code, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(obj)); };
    if (req.headers.authorization !== want) return json(401, { status: 401, message: 'Authentication Failed' });
    if (req.url === '/api/core/service/search' && req.method === 'POST') {
      const q = JSON.parse(body || '{}');
      const r = rows.filter((x) => !q.searchPhrase || x.name.includes(q.searchPhrase));
      return json(200, { total: r.length, rowCount: r.length, current: q.current || 1, rows: r });
    }
    if (req.url === '/api/core/firmware/status' && req.method === 'GET') return json(200, { product_version: '26.1-mock', status: 'none' });
    if (req.url === '/api/core/service/restart/sshd' && req.method === 'POST') return json(200, { result: 'ok' });
    json(404, { errorMessage: 'Endpoint not found' });
  });
});

function client(env) {
  const p = spawn(process.execPath, [path.join(__dirname, '..', 'server', 'index.js')], { env: { PATH: process.env.PATH, ...env }, stdio: ['pipe', 'pipe', 'pipe'] });
  let buf = ''; const waiters = new Map(); let next = 1; let stdoutAll = '';
  p.stdout.on('data', (d) => {
    buf += d; stdoutAll += d;
    let i; while ((i = buf.indexOf('\n')) >= 0) {
      const line = buf.slice(0, i); buf = buf.slice(i + 1);
      const m = JSON.parse(line); // every stdout line must be valid JSON-RPC
      waiters.get(m.id)?.(m); waiters.delete(m.id);
    }
  });
  const rpc = (method, params) => new Promise((res, rej) => {
    const id = next++; waiters.set(id, res);
    setTimeout(() => rej(new Error('timeout ' + method)), 10000);
    p.stdin.write(JSON.stringify({ jsonrpc: '2.0', id, method, params }) + '\n');
  });
  const notify = (method) => p.stdin.write(JSON.stringify({ jsonrpc: '2.0', method }) + '\n');
  const call = async (name, args) => (await rpc('tools/call', { name, arguments: args })).result;
  return { rpc, notify, call, close: () => p.stdin.end(), out: () => stdoutAll };
}

const results = [];
async function check(name, fn) {
  try { await fn(); results.push(['PASS', name]); } catch (e) { results.push(['FAIL', name + ': ' + e.message]); }
}

mock.listen(0, '::1', async () => {
  const port = mock.address().port;
  const base = { OPNSENSE_URL: `https://[::1]:${port}`, OPNSENSE_API_KEY: KEY, OPNSENSE_API_SECRET: SECRET };
  const c = client({ ...base, OPNSENSE_VERIFY_TLS: 'false' });

  await check('initialize handshake', async () => {
    const r = await c.rpc('initialize', { protocolVersion: '2025-06-18', capabilities: {}, clientInfo: { name: 'e2e', version: '0' } });
    assert.equal(r.result.serverInfo.name, 'opnsense');
    assert.equal(r.result.protocolVersion, '2025-06-18');
    assert.ok(r.result.capabilities.tools);
    c.notify('notifications/initialized'); // must produce no response
  });
  await check('tools/list returns 7 tools with schemas', async () => {
    const r = await c.rpc('tools/list', {});
    const names = r.result.tools.map((t) => t.name).sort();
    assert.deepEqual(names, ['opnsense_firewall_rules', 'opnsense_firmware_status', 'opnsense_get', 'opnsense_interfaces', 'opnsense_post', 'opnsense_search', 'opnsense_services']);
    for (const t of r.result.tools) assert.equal(t.inputSchema.type, 'object');
    assert.match(r.result.tools.find((t) => t.name === 'opnsense_post').description, /confirm/);
  });
  await check('TLS verify off + IPv6 [::1]: opnsense_services returns GridPage', async () => {
    const r = await c.call('opnsense_services', { searchPhrase: 'unbound' });
    assert.equal(r.isError, false, r.content[0].text);
    assert.deepEqual(Object.keys(r.structuredContent).sort(), ['current', 'rowCount', 'rows', 'total']);
    assert.equal(r.structuredContent.total, 1);
    assert.equal(r.structuredContent.rows[0].name, 'unbound');
    const sent = JSON.parse(seen.at(-1).body);
    assert.deepEqual(Object.keys(sent).sort(), ['current', 'rowCount', 'searchPhrase', 'sort']);
  });
  await check('opnsense_search generic grid', async () => {
    const r = await c.call('opnsense_search', { path: '/api/core/service/search', rowCount: 7 });
    assert.equal(r.isError, false); assert.equal(r.structuredContent.total, 2);
  });
  await check('opnsense_get / firmware status', async () => {
    const r = await c.call('opnsense_firmware_status', {});
    assert.equal(r.isError, false); assert.equal(r.structuredContent.product_version, '26.1-mock');
  });
  await check('confirm guard refuses without confirm=true (no request sent)', async () => {
    const before = seen.length;
    for (const confirm of [false, 'true', 1, undefined]) {
      const r = await c.call('opnsense_post', { path: '/api/core/service/restart/sshd', confirm });
      assert.equal(r.isError, true); assert.match(r.content[0].text, /Refused/);
    }
    assert.equal(seen.length, before);
  });
  await check('confirm=true performs the POST', async () => {
    const r = await c.call('opnsense_post', { path: '/api/core/service/restart/sshd', confirm: true });
    assert.equal(r.isError, false); assert.equal(r.structuredContent.result, 'ok');
  });
  await check('path guard refuses scheme/host/traversal (no request sent)', async () => {
    const before = seen.length;
    for (const p of ['https://evil.example/api/x', '//evil.example/api/x', '/api/../../etc', 'api/core/x', '/api/core\r\nHost: x', '/other/x', '/api/x@evil.example', '@evil.example/api/x']) {
      const r = await c.call('opnsense_get', { path: p });
      assert.equal(r.isError, true, p); assert.match(r.content[0].text, /Refused path/, p);
    }
    assert.equal(seen.length, before);
  });
  await check('HTTP 404 surfaces as tool error with status', async () => {
    const r = await c.call('opnsense_get', { path: '/api/nope/nope/nope' });
    assert.equal(r.isError, true); assert.match(r.content[0].text, /HTTP 404/);
  });
  await check('non-grid response to opnsense_search is a tool error', async () => {
    const r = await c.call('opnsense_search', { path: '/api/core/firmware/status' });
    assert.equal(r.isError, true);
  });
  await check('unknown tool -> JSON-RPC error; unknown method -> -32601', async () => {
    assert.equal((await c.rpc('tools/call', { name: 'nope', arguments: {} })).error.code, -32602);
    assert.equal((await c.rpc('bogus/method', {})).error.code, -32601);
  });
  c.close();

  const bad = client({ ...base, OPNSENSE_API_SECRET: 'wrong-secret-xyz', OPNSENSE_VERIFY_TLS: 'false' });
  await check('bad credentials -> isError with HTTP 401, no secret echoed', async () => {
    await bad.rpc('initialize', {});
    const r = await bad.call('opnsense_firmware_status', {});
    assert.equal(r.isError, true); assert.match(r.content[0].text, /HTTP 401/);
    assert.ok(!bad.out().includes('wrong-secret-xyz'));
  });
  bad.close();

  const strict = client({ ...base, OPNSENSE_VERIFY_TLS: 'true' });
  await check('TLS verify on vs self-signed cert -> clean tool error', async () => {
    const r = await strict.call('opnsense_firmware_status', {});
    assert.equal(r.isError, true); assert.match(r.content[0].text, /SELF_SIGNED|self[- ]signed|certificate/i);
    assert.match(r.content[0].text, /OPNSENSE_VERIFY_TLS=false/);
  });
  strict.close();

  const unset = client({ OPNSENSE_URL: '${OPNSENSE_URL}', OPNSENSE_API_KEY: '${OPNSENSE_API_KEY}', OPNSENSE_API_SECRET: '' });
  await check('unexpanded ${VAR} placeholders treated as not configured', async () => {
    const r = await unset.call('opnsense_services', {});
    assert.equal(r.isError, true); assert.match(r.content[0].text, /not configured: set OPNSENSE_URL, OPNSENSE_API_KEY, OPNSENSE_API_SECRET/);
  });
  unset.close();

  await check('credentials never appear on any stdout', async () => {
    assert.ok(!c.out().includes(SECRET) && !c.out().includes(KEY));
    assert.ok(!strict.out().includes(SECRET));
  });

  mock.close(); fs.rmSync(dir, { recursive: true, force: true });
  for (const [s, n] of results) console.log(`${s}  ${n}`);
  const failed = results.filter((r) => r[0] === 'FAIL').length;
  console.log(`\n${results.length - failed}/${results.length} passed`);
  process.exit(failed ? 1 : 0);
});
