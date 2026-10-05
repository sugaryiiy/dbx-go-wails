#!/usr/bin/env node
/** Exercise the backend and static assets inside a real .app using disposable data. */
import assert from 'node:assert/strict';
import { spawn } from 'node:child_process';
import { randomBytes } from 'node:crypto';
import { access, mkdtemp, realpath, rm } from 'node:fs/promises';
import { createServer } from 'node:net';
import { tmpdir } from 'node:os';
import { dirname, join, resolve, sep } from 'node:path';
import { setTimeout as delay } from 'node:timers/promises';
import { fileURLToPath } from 'node:url';

const root = resolve(dirname(fileURLToPath(import.meta.url)), '..');
const options = { app: join(root, 'target/macos-mcp/Database Workbench.app'), json: false };
const report = { ok: false, app: '', checks: [], toolCount: null, sampleRows: null };
const controller = new AbortController();
const password = randomBytes(32).toString('hex');
const token = randomBytes(32).toString('hex');
let dataDirectory, child, childClosed, childResult, origin, cookie = '', session = '', protocol = '';
let logTail = '', nextId = 1;

function safe(value) {
  let text = value instanceof Error ? value.message : String(value);
  for (const secret of [password, token, cookie, session]) {
    if (secret) text = text.replaceAll(secret, '[REDACTED]');
  }
  return text.slice(-1800);
}

function parseArguments() {
  const args = process.argv.slice(2);
  for (let i = 0; i < args.length; i++) {
    if (args[i] === '--help' || args[i] === '-h') return false;
    if (args[i] === '--json') options.json = true;
    else if (args[i] === '--app' && args[i + 1] && !args[i + 1].startsWith('--')) options.app = resolve(args[++i]);
    else throw new Error(`Unknown or incomplete argument: ${args[i]}; use --help.`);
  }
  return true;
}

async function check(name, run) {
  controller.signal.throwIfAborted();
  await run();
  report.checks.push(name);
  console.error(`PASS ${name}`);
}

async function availablePort() {
  const server = createServer();
  await new Promise((resolveListen, reject) => {
    server.once('error', reject);
    server.listen(0, '127.0.0.1', resolveListen);
  });
  const port = server.address().port;
  await new Promise((resolveClose, reject) => server.close((error) => error ? reject(error) : resolveClose()));
  return port;
}

async function request(path, { method = 'GET', body, timeout = 15_000 } = {}) {
  const response = await fetch(`${origin}/api${path}`, {
    method, redirect: 'error',
    headers: { Accept: 'application/json', ...(cookie ? { Cookie: cookie } : {}), ...(body === undefined ? {} : { 'Content-Type': 'application/json' }) },
    body: body === undefined ? undefined : JSON.stringify(body),
    signal: AbortSignal.any([controller.signal, AbortSignal.timeout(timeout)]),
  });
  const sessionCookie = response.headers.getSetCookie().find((value) => value.startsWith('dbx_session='));
  if (sessionCookie) cookie = sessionCookie.split(';', 1)[0];
  const text = await response.text();
  let result;
  try { result = text ? JSON.parse(text) : null; } catch { throw new Error(`${path}: expected JSON (HTTP ${response.status})`); }
  if (!response.ok) throw new Error(`${method} ${path}: HTTP ${response.status}: ${path.startsWith('/auth/') ? 'authentication failed' : safe(result?.detail || result?.error || result?.messageKey || '')}`);
  return result;
}

async function waitForBackend() {
  const deadline = Date.now() + 60_000;
  while (Date.now() < deadline) {
    controller.signal.throwIfAborted();
    if (childResult) throw new Error(`Packed backend exited before readiness (${childResult.code ?? childResult.signal ?? childResult.error})`);
    try { return await request('/auth/check', { timeout: 1000 }); } catch { /* startup can briefly refuse connections */ }
    await delay(150, undefined, { signal: controller.signal });
  }
  throw new Error('Packed backend did not become ready within 60 seconds');
}

function mcpHeaders(authenticated = true) {
  return {
    'Content-Type': 'application/json', Accept: 'application/json, text/event-stream',
    ...(authenticated ? { Authorization: `Bearer ${token}` } : {}),
    ...(session ? { 'Mcp-Session-Id': session } : {}),
    ...(protocol ? { 'MCP-Protocol-Version': protocol } : {}),
  };
}

/** A POST may stream SSE indefinitely; stop as soon as our JSON-RPC response arrives. */
async function readRpc(response, id) {
  if (!response.headers.get('content-type')?.includes('text/event-stream')) {
    const result = await response.json();
    assert.equal(result.id, id, 'JSON-RPC response id');
    return result;
  }
  const reader = response.body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  try {
    while (true) {
      const { value, done } = await reader.read();
      buffer += decoder.decode(value, { stream: !done }).replaceAll('\r\n', '\n');
      assert.ok(buffer.length < 2_000_000, 'MCP response is unexpectedly large');
      let boundary;
      while ((boundary = buffer.indexOf('\n\n')) !== -1) {
        const frame = buffer.slice(0, boundary);
        buffer = buffer.slice(boundary + 2);
        const payload = frame.split('\n').filter((line) => line.startsWith('data:')).map((line) => line.slice(5).trimStart()).join('\n');
        if (!payload) continue;
        const message = JSON.parse(payload);
        if (message.id === id) return message;
      }
      if (done) throw new Error(`MCP stream closed without response ${id}`);
    }
  } finally { await reader.cancel().catch(() => {}); }
}

async function rpc(method, params, notification = false) {
  const id = notification ? undefined : nextId++;
  const response = await fetch(`${origin}/mcp`, {
    method: 'POST', headers: mcpHeaders(), redirect: 'error',
    body: JSON.stringify({ jsonrpc: '2.0', ...(notification ? {} : { id }), method, ...(params === undefined ? {} : { params }) }),
    signal: AbortSignal.any([controller.signal, AbortSignal.timeout(30_000)]),
  });
  assert.ok(response.ok, `${method}: HTTP ${response.status}`);
  session = response.headers.get('mcp-session-id') || session;
  if (notification) { await response.body?.cancel(); return; }
  const message = await readRpc(response, id);
  if (message.error) {
    const error = new Error(`${method}: ${safe(message.error.message)}`);
    error.rpcError = true;
    throw error;
  }
  assert.ok(message.result, `${method}: missing result`);
  return message.result;
}

function toolText(result) {
  return result.content?.filter((item) => item.type === 'text').map((item) => item.text).join('\n') || '';
}

async function callTool(name, args = {}) {
  const result = await rpc('tools/call', { name, arguments: args });
  const text = toolText(result);
  assert.notEqual(result.isError, true, `${name}: ${safe(text)}`);
  assert.ok(text.length > 0, `${name}: empty result`);
  return text;
}

async function callJsonTool(name, args) {
  const result = await rpc('tools/call', { name, arguments: args });
  const text = toolText(result);
  assert.notEqual(result.isError, true, `${name}: ${safe(text)}`);
  const data = JSON.parse(text);
  assert.deepEqual(result.structuredContent, data, `${name}: text and structured content must agree`);
  return data;
}

async function expectToolError(name, args, code) {
  let result;
  try { result = await rpc('tools/call', { name, arguments: args }); }
  catch (error) {
    // Do not mistake a network failure or assertion failure for an expected rejection.
    if (!error.rpcError) throw error;
    assert.ok(error.message.includes(code), `${name}: expected ${code}, got ${safe(error)}`);
    return;
  }
  assert.equal(result.isError, true, `${name}: expected rejection ${code}`);
  assert.ok(toolText(result).includes(`[${code}]`), `${name}: expected ${code}, got ${safe(toolText(result))}`);
}

function hasType(schema, type, rootSchema = schema) {
  if (schema?.$ref?.startsWith('#/')) {
    const referenced = schema.$ref.slice(2).split('/').reduce((value, key) => value?.[key.replaceAll('~1', '/').replaceAll('~0', '~')], rootSchema);
    return hasType(referenced, type, rootSchema);
  }
  return schema?.type === type || (Array.isArray(schema?.type) && schema.type.includes(type)) || schema?.anyOf?.some((entry) => hasType(entry, type, rootSchema));
}

async function boundedExit(milliseconds) {
  let timer;
  try {
    return await Promise.race([childClosed, new Promise((_, reject) => {
      timer = setTimeout(() => reject(new Error(`Backend shutdown exceeded ${milliseconds / 1000} seconds`)), milliseconds);
    })]);
  } finally { clearTimeout(timer); }
}

async function closeSession() {
  if (!session || childResult) return;
  const response = await fetch(`${origin}/mcp`, {
    method: 'DELETE', headers: mcpHeaders(), redirect: 'error', signal: AbortSignal.timeout(3000),
  });
  await response.body?.cancel();
  assert.ok(response.ok, `MCP session cleanup: HTTP ${response.status}`);
  session = '';
}

function interrupted(signal) {
  controller.abort(new Error(`Interrupted by ${signal}`));
  child?.stdin.end();
}
const onInterrupt = () => interrupted('SIGINT');
const onTerminate = () => interrupted('SIGTERM');

async function main() {
  if (!parseArguments()) {
    console.log('Usage: node scripts/smoke-macos-mcp.mjs [--app "path/Database Workbench.app"] [--json]\nRuns the packaged backend with temporary data and a random loopback port. No running workbench instance is used.');
    return;
  }
  report.app = options.app;
  const backend = join(options.app, 'Contents/Resources/dbx-web');
  const staticDirectory = join(options.app, 'Contents/Resources/web-dist');
  try { await access(backend); await access(join(staticDirectory, 'index.html')); }
  catch { throw new Error(`App resources not found at ${options.app}. Run pnpm build:macos first.`); }
  process.on('SIGINT', onInterrupt);
  process.on('SIGTERM', onTerminate);
  dataDirectory = await realpath(await mkdtemp(join(tmpdir(), 'dbx-macos-mcp-smoke-')));
  const port = await availablePort();
  origin = `http://127.0.0.1:${port}`;
  const env = Object.fromEntries(Object.entries(process.env).filter(([key]) => !key.startsWith('DBX_')));
  Object.assign(env, {
    DBX_BIND_ADDR: '127.0.0.1', DBX_PORT: String(port), DBX_DATA_DIR: dataDirectory,
    DBX_STATIC_DIR: staticDirectory, DBX_PASSWORD: password, DBX_DISABLE_PASSWORD: '0', DBX_DEMO_MODE: '0',
    DBX_PUBLIC_BASE_PATH: '', DBX_WEB_MCP_TOKEN: token, DBX_WEB_MCP_ALLOWED_HOSTS: `127.0.0.1:${port}`,
    DBX_WEB_MCP_ALLOWED_ORIGINS: origin, DBX_MCP_PLUGIN_TOOLS: 'lazy', DBX_EXIT_ON_STDIN_CLOSE: '1',
  });
  child = spawn(backend, [], { cwd: dataDirectory, env, stdio: ['pipe', 'pipe', 'pipe'] });
  child.stdin.on('error', () => {});
  childClosed = new Promise((resolveExit) => {
    child.once('error', (error) => { childResult = { error: error.message }; resolveExit(childResult); });
    child.once('close', (code, signal) => { childResult = { code, signal }; resolveExit(childResult); });
  });
  for (const stream of [child.stdout, child.stderr]) stream.on('data', (chunk) => { logTail = (logTail + chunk.toString()).slice(-6000); });
  let sample;
  await check('packed backend, authentication and migration', async () => {
    const auth = await waitForBackend();
    assert.equal(auth.required, true);
    assert.equal(auth.authenticated, false);
    assert.equal(auth.setup_required, false, 'DBX_PASSWORD must configure authentication');
    await request('/auth/login', { method: 'POST', body: { password } });
    assert.ok(cookie, 'login must set a session cookie');
    let migration = await request('/migration/status');
    if (migration.needsMigration) {
      await request('/migration/start', { method: 'POST', timeout: 60_000 });
      migration = await request('/migration/status');
    }
    assert.equal(migration.needsMigration, false);
    assert.ok(['succeeded', 'not_required'].includes(migration.state), `migration state: ${migration.state}`);
    const index = await fetch(origin, { redirect: 'error', signal: AbortSignal.timeout(5000) });
    assert.equal(index.status, 200);
    assert.match(await index.text(), /<html[\s>]/i);
  });
  await check('system fonts available for desktop font selection', async () => {
    const fonts = await request('/system/fonts');
    assert.ok(Array.isArray(fonts) && fonts.length > 0, 'packed backend must enumerate installed fonts');
    assert.ok(fonts.every((font) => typeof font === 'string' && font.trim().length > 0));
    assert.equal(new Set(fonts).size, fonts.length, 'font family list must not contain duplicates');
    report.fontCount = fonts.length;
  });
  await check('sample workspace and disposable read-only policy', async () => {
    sample = await request('/workspace/sample', { method: 'POST', body: {} });
    assert.equal(sample.connection.db_type, 'sqlite');
    assert.ok(resolve(sample.connection.host).startsWith(dataDirectory + sep), 'sample database must stay inside owned temporary data');
    assert.match(sample.sql, /join/i);
    await request('/app-settings/mcp-policy', { method: 'PUT', body: {
      readOnly: true, allowDangerousSql: false, allowedConnectionIds: [sample.connection.id], allowedToolNames: null,
    } });
  });
  await check('MCP Bearer and Origin enforcement', async () => {
    for (const [authenticated, invalidOrigin, expected] of [[false, false, 401], [true, true, 403]]) {
      const response = await fetch(`${origin}/mcp`, {
        method: 'POST', headers: { ...mcpHeaders(authenticated), ...(invalidOrigin ? { Origin: 'https://invalid.example' } : {}) },
        body: JSON.stringify({ jsonrpc: '2.0', id: 999, method: 'tools/list', params: {} }),
        redirect: 'error', signal: AbortSignal.any([controller.signal, AbortSignal.timeout(5000)]),
      });
      await response.body?.cancel();
      assert.equal(response.status, expected, invalidOrigin ? 'invalid Origin' : 'missing Bearer');
    }
  });
  await check('MCP initialize and tool schemas', async () => {
    const initialized = await rpc('initialize', { protocolVersion: '2025-03-26', capabilities: {}, clientInfo: { name: 'dbx-macos-smoke', version: '1.0.0' } });
    protocol = initialized.protocolVersion;
    assert.equal(typeof protocol, 'string');
    assert.ok(session, 'stateful MCP session id');
    await rpc('notifications/initialized', undefined, true);
    const listing = await rpc('tools/list', {});
    assert.ok(Array.isArray(listing.tools));
    report.toolCount = listing.tools.length;
    const tools = new Map(listing.tools.map((tool) => [tool.name, tool]));
    for (const name of ['dbx_list_connections', 'dbx_list_databases', 'dbx_list_tables', 'dbx_describe_table', 'dbx_execute_query', 'dbx_plugin_list', 'dbx_plugin_tools', 'dbx_plugin_call', 'dbx_list_saved_sql', 'dbx_get_saved_sql', 'dbx_save_sql', 'dbx_search_history']) assert.ok(tools.has(name), `missing tool: ${name}`);
    for (const name of ['dbx_open_table', 'dbx_execute_and_show', 'dbx_peek_messages', 'dbx_send_message']) assert.ok(!tools.has(name), `unsupported web/basic tool: ${name}`);
    const query = tools.get('dbx_execute_query').inputSchema;
    assert.ok(hasType(query.properties?.sql, 'string'));
    assert.ok(hasType(query.properties?.max_rows, 'integer'));
    assert.ok(hasType(tools.get('dbx_describe_table').inputSchema.properties?.table, 'string'));
    assert.ok(hasType(tools.get('dbx_save_sql').inputSchema.properties?.sql, 'string'));
    assert.ok(hasType(tools.get('dbx_get_saved_sql').inputSchema.properties?.id, 'string'));
    assert.ok(hasType(tools.get('dbx_list_saved_sql').inputSchema.properties?.limit, 'integer'));
    const historySchema = tools.get('dbx_search_history').inputSchema;
    assert.ok(hasType(historySchema.properties?.cursor, 'object', historySchema));
    assert.equal(report.toolCount, 28, 'current basic build with lazy plugin discovery');
  });
  const connection = { connection_id: sample.connection.id };
  const database = { ...connection, database: 'main', schema: '' };
  await check('MCP connections, databases, tables and schema', async () => {
    assert.ok((await callTool('dbx_list_connections')).includes(sample.connection.id));
    assert.match(await callTool('dbx_list_databases', connection), /main/);
    const tables = await callTool('dbx_list_tables', database);
    for (const table of ['customers', 'products', 'orders', 'order_items']) assert.ok(tables.includes(table), `missing sample table: ${table}`);
    const description = await callTool('dbx_describe_table', { ...database, table: 'products' });
    for (const column of ['id', 'name', 'price', 'stock']) assert.ok(description.includes(column), `missing column: ${column}`);
    assert.match(description, /NUMERIC/i);
    assert.match(description, /INTEGER/i);
  });
  await check('MCP sample JOIN returns 8 rows, Chinese, NULL and decimals', async () => {
    const result = await callTool('dbx_execute_query', { ...connection, database: 'main', sql: sample.sql, max_rows: 20 });
    assert.match(result, /\(8 rows, [\d.]+ms\)/);
    assert.match(result, /林晓/);
    assert.match(result, /239\.9/);
    assert.match(result, /\bNULL\b/);
    const lines = result.split('\n').filter((line) => line.startsWith('|') && !/^\|[-| :]+\|$/.test(line));
    const columns = lines[0].split('|').slice(1, -1).map((value) => value.trim());
    assert.deepEqual(columns, ['order_id', 'customer', 'city', 'product', 'quantity', 'unit_price', 'subtotal', 'status', 'ordered_at', 'note']);
    report.sampleRows = lines.length - 1;
    assert.equal(report.sampleRows, 8);
    const typed = await request('/query/execute', { method: 'POST', body: { connectionId: sample.connection.id, database: 'main', sql: sample.sql } });
    assert.deepEqual(typed.columns, columns);
    assert.equal(typed.rows.length, 8);
    assert.equal(typed.rows[0][1], '林晓');
    assert.equal(typed.rows[0][6], 239.9);
    assert.ok(typed.rows.every((row) => Number.isInteger(row[0]) && Number.isInteger(row[4]) && typeof row[5] === 'number'));
    assert.ok(typed.rows.some((row) => row[9] === null));
  });
  const scoped = { ...connection, database: 'main' };
  const marker = `mcp-smoke-${randomBytes(8).toString('hex')}`;
  await check('MCP SQL create/list/get agrees with HTTP library without executing SQL', async () => {
    const sql = "CREATE TABLE dbx_mcp_save_must_not_execute (note TEXT DEFAULT '中文收藏');";
    const name = `${marker}-中文.sql`;
    const saved = await callJsonTool('dbx_save_sql', { ...scoped, name, sql });
    const duplicate = await callJsonTool('dbx_save_sql', { ...scoped, name, sql });
    for (const file of [saved, duplicate]) {
      assert.equal(typeof file.id, 'string');
      assert.ok(file.id.length > 0);
      assert.equal(file.name, name);
      assert.equal(file.connection_id, sample.connection.id);
      assert.equal(file.database, 'main');
      assert.ok(!Object.hasOwn(file, 'sql'), 'save returns metadata, not a second SQL copy');
    }
    assert.notEqual(saved.id, duplicate.id, 'save must create a new ID instead of overwriting');
    const listing = await callJsonTool('dbx_list_saved_sql', { ...scoped, filter: marker });
    assert.equal(listing.limit, 20);
    assert.equal(listing.total, 2);
    assert.deepEqual(new Set(listing.files.map((file) => file.id)), new Set([saved.id, duplicate.id]));
    assert.ok(listing.files.every((file) => !Object.hasOwn(file, 'sql')), 'list only returns metadata');
    const first = await callJsonTool('dbx_list_saved_sql', { ...scoped, filter: marker.toUpperCase(), limit: 1 });
    assert.equal(first.files.length, 1);
    assert.equal(first.offset, 0);
    assert.equal(first.next_offset, 1);
    const second = await callJsonTool('dbx_list_saved_sql', { ...scoped, filter: marker, limit: 1, offset: first.next_offset });
    assert.equal(second.files.length, 1);
    assert.equal(second.offset, 1);
    assert.equal(second.total, 2);
    assert.equal(second.next_offset, null);
    assert.notEqual(first.files[0].id, second.files[0].id);
    assert.deepEqual(new Set([first.files[0].id, second.files[0].id]), new Set([saved.id, duplicate.id]));
    const loaded = await callJsonTool('dbx_get_saved_sql', { ...scoped, id: saved.id });
    assert.equal(loaded.id, saved.id);
    assert.equal(loaded.sql, sql);
    await expectToolError('dbx_get_saved_sql', { ...scoped, database: 'outside-selected-database', id: saved.id }, 'SAVED_SQL_NOT_FOUND');
    const library = await request('/saved-sql');
    for (const id of [saved.id, duplicate.id]) assert.ok(library.files.some((file) => file.id === id && file.name === name), 'HTTP SQL library must see MCP-created files');
    const httpFile = await request(`/saved-sql/${encodeURIComponent(saved.id)}`);
    assert.equal(httpFile.sql, sql);
    assert.equal(httpFile.connectionId, sample.connection.id);
    const untouched = await request('/query/execute', { method: 'POST', body: {
      connectionId: sample.connection.id, database: 'main',
      sql: "SELECT COUNT(*) AS table_count FROM sqlite_master WHERE type = 'table' AND name = 'dbx_mcp_save_must_not_execute'",
    } });
    assert.deepEqual(untouched.rows, [[0]], 'saving SQL must not execute its DDL');
  });
  await check('HTTP query history is searchable through MCP with cursor and safe fields', async () => {
    const sql = `SELECT '${marker}-历史查询' AS marker, NULL AS note, 12.5 AS amount`;
    const result = await request('/query/execute', { method: 'POST', body: { connectionId: sample.connection.id, database: 'main', sql } });
    assert.deepEqual(result.rows, [[`${marker}-历史查询`, null, 12.5]]);
    const executedAt = new Date().toISOString();
    const entry = {
      connection_id: sample.connection.id, connection_name: sample.connection.name, database: 'main', sql,
      executed_at: executedAt, execution_time_ms: result.execution_time_ms, success: true, error: null,
      activity_kind: 'query', source: 'sql', affected_rows: null,
      mcp_tool_name: 'private-mcp-tool', mcp_request_json: 'private-mcp-request',
      mcp_response_json: 'private-mcp-response', mcp_session_id: 'private-mcp-session',
      details_json: 'private-history-details', rollback_sql: 'private-history-rollback',
    };
    const ids = [`${marker}-history-a`, `${marker}-history-b`];
    // Mirror the GUI's query + history/save workflow, using only this disposable backend.
    for (const id of ids) await request('/history/save', { method: 'POST', body: { entry: { ...entry, id } } });
    await request('/history/save', { method: 'POST', body: { entry: { ...entry, id: `${marker}-mcp-only`, source: 'mcp' } } });
    const args = { ...scoped, search_text: marker, success: true, limit: 1 };
    const first = await callJsonTool('dbx_search_history', args);
    assert.equal(first.total, 2, 'history search must select SQL history, excluding MCP audit entries');
    assert.equal(first.entries.length, 1);
    assert.deepEqual(first.next_cursor, { executed_at: first.entries[0].executed_at, id: first.entries[0].id }, 'cursor must identify the last emitted row');
    const second = await callJsonTool('dbx_search_history', { ...args, cursor: first.next_cursor });
    assert.equal(second.entries.length, 1);
    assert.equal(second.total, 2, 'filtered total must not shrink after applying a cursor');
    assert.equal(second.next_cursor, null);
    assert.equal(second.entries[0].executed_at, first.entries[0].executed_at);
    assert.ok(second.entries[0].id < first.entries[0].id, 'equal timestamps must use descending ID order');
    const entries = [...first.entries, ...second.entries];
    assert.deepEqual(new Set(entries.map((item) => item.id)), new Set(ids), 'equal timestamps must paginate without duplicates or gaps');
    for (const item of entries) {
      assert.equal(item.sql, sql);
      assert.equal(item.connection_id, sample.connection.id);
      assert.equal(item.database, 'main');
      assert.equal(item.success, true);
      assert.equal(typeof item.execution_time_ms, 'number');
      assert.equal(item.sql_truncated, false);
      assert.equal(item.sql_bytes, Buffer.byteLength(sql));
      assert.equal(item.error_truncated, false);
      for (const key of Object.keys(item)) assert.ok(!key.startsWith('mcp_') && !['details_json', 'rollback_sql'].includes(key), `internal history field leaked: ${key}`);
      assert.ok(!JSON.stringify(item).includes('private-'), 'internal history values must be omitted');
    }
    const longSql = `-- ${marker}-limited-history\nSELECT '${'历史'.repeat(3000)}';`;
    const longError = '错误说明'.repeat(500);
    const longId = `${marker}-history-long`;
    await request('/history/save', { method: 'POST', body: { entry: {
      ...entry, id: longId, sql: longSql, success: false, error: longError,
    } } });
    const limited = await callJsonTool('dbx_search_history', { ...scoped, search_text: `${marker}-limited-history`, success: false });
    assert.equal(limited.entries.length, 1);
    const bounded = limited.entries[0];
    assert.equal(bounded.id, longId);
    assert.equal(bounded.sql_truncated, true);
    assert.equal(bounded.sql_bytes, Buffer.byteLength(longSql));
    assert.ok(Buffer.byteLength(bounded.sql) <= 16 * 1024);
    assert.ok(bounded.sql.length > 0 && longSql.startsWith(bounded.sql), 'SQL truncation must preserve UTF-8 boundaries');
    assert.equal(bounded.error_truncated, true);
    assert.ok(Buffer.byteLength(bounded.error) <= 2 * 1024);
    assert.ok(bounded.error.length > 0 && longError.startsWith(bounded.error), 'error truncation must preserve UTF-8 boundaries');
    const fullHistory = await request('/history/search', { method: 'POST', body: {
      search_text: `${marker}-limited-history`, connections: [{ connection_id: sample.connection.id }], source: 'sql', limit: 20,
    } });
    assert.equal(fullHistory.entries.find((item) => item.id === longId)?.sql, longSql, 'HTTP history must retain the full SQL text');
  });
  await check('saved SQL and history reject missing IDs and invalid input limits', async () => {
    await expectToolError('dbx_get_saved_sql', { ...scoped, id: `${marker}-missing` }, 'SAVED_SQL_NOT_FOUND');
    await expectToolError('dbx_get_saved_sql', { ...scoped, id: '' }, 'INVALID_ID');
    for (const limit of [0, 101]) {
      await expectToolError('dbx_list_saved_sql', { ...scoped, limit }, 'INVALID_LIMIT');
      await expectToolError('dbx_search_history', { ...scoped, limit }, 'INVALID_LIMIT');
    }
    await expectToolError('dbx_save_sql', { ...scoped, name: '', sql: 'SELECT 1' }, 'INVALID_NAME');
    await expectToolError('dbx_save_sql', { ...scoped, name: 'empty.sql', sql: '' }, 'INVALID_SQL');
    await expectToolError('dbx_save_sql', { ...scoped, name: 'oversize.sql', sql: 'x'.repeat(65_537) }, 'SQL_TOO_LARGE');
    await expectToolError('dbx_search_history', { ...scoped, cursor: { executed_at: 'not-a-date', id: 'invalid-cursor' } }, 'INVALID_CURSOR');
  });
  await check('parent stdin EOF stops the packed backend', async () => {
    await closeSession();
    child.stdin.end();
    const result = await boundedExit(15_000);
    assert.equal(result.code, 0, `backend exit: ${JSON.stringify(result)}`);
    await assert.rejects(fetch(`${origin}/api/auth/check`, { signal: AbortSignal.timeout(1500) }), 'backend listener must close');
  });
  report.ok = true;
}

try { await main(); }
catch (error) {
  report.error = safe(error);
  console.error(`FAIL ${report.error}`);
  if (logTail) console.error(`Backend log tail: ${safe(logTail)}`);
  process.exitCode = 1;
} finally {
  try {
    if (child && !childResult) {
      await closeSession().catch(() => {});
      child.stdin.end();
      try { await boundedExit(3000); }
      catch {
        child.kill('SIGTERM');
        try { await boundedExit(5000); }
        catch { child.kill('SIGKILL'); await boundedExit(3000); }
      }
    }
    if (dataDirectory && (!child || childResult)) await rm(dataDirectory, { recursive: true, force: true });
  } catch (error) {
    report.ok = false;
    report.cleanupError = safe(error);
    console.error(`FAIL cleanup: ${report.cleanupError}`);
    process.exitCode = 1;
  }
  process.removeListener('SIGINT', onInterrupt);
  process.removeListener('SIGTERM', onTerminate);
  if (options.json) console.log(JSON.stringify(report));
  else if (report.ok) console.log(`Passed ${report.checks.length} checks; ${report.toolCount} MCP tools; ${report.sampleRows} sample rows; temporary data removed.`);
}
