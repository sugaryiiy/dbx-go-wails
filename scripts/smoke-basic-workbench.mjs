#!/usr/bin/env node
/**
 * Real HTTP smoke test; Node >= 22.13, no browser or database mocks.
 * Start a separate DBX backend with a disposable DBX_DATA_DIR on this machine.
 * Do not point this at a shared/user instance, even if it listens on localhost.
 *
 * DBX_SMOKE_ISOLATED=1 DBX_SMOKE_PASSWORD_FILE=/path/to/password \
 *   node scripts/smoke-basic-workbench.mjs
 * Optional: DBX_SMOKE_BASE_URL=http://127.0.0.1:4224 (subpaths supported).
 * DBX_SMOKE_PASSWORD is also accepted; never put a password on the command line.
 * Preconfigure authentication in the backend; this script never sets/resets it.
 * The backend and script must share the local filesystem (not a container).
 * Tests own a unique temporary SQLite file, profile, saved SQL and history entry.
 * Pagination here exercises SQL LIMIT/OFFSET, not an agent cursor protocol.
 */
import assert from 'node:assert/strict';
import { randomUUID } from 'node:crypto';
import { mkdtemp, readFile, rm, writeFile } from 'node:fs/promises';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const base = new URL(process.env.DBX_SMOKE_BASE_URL || 'http://127.0.0.1:4224');
let password = '';
let cookie = '';
let temporaryDirectory;
let connectionRegistered = false;
let connectionAttempted = false;
let savedSqlAttempted = false;
let historyAttempted = false;
const connectionId = `smoke-${randomUUID()}`;
const savedSqlId = `smoke-sql-${randomUUID()}`;
const historyId = `smoke-history-${randomUUID()}`;
const sources = new Set();
const exportsInFlight = new Set();
const importsInFlight = new Set();
const expectedColumns = ['id', 'name', 'amount', 'note'];
const expectedRows = [[1, '中文测试', 12.5, null], [2, '第二行', -2.75, '含逗号,引号"和换行\n尾行']];

function safeMessage(error) {
  let message = error instanceof Error ? error.message : String(error);
  for (const secret of [password, cookie]) if (secret) message = message.replaceAll(secret, '[REDACTED]');
  return message.slice(0, 1500);
}

function url(path) {
  return `${base.origin}${base.pathname.replace(/\/$/, '')}/api${path}`;
}

async function request(path, { method = 'GET', body, raw = false, allowError = false, signal } = {}) {
  const headers = { Accept: raw ? '*/*' : 'application/json' };
  if (cookie) headers.Cookie = cookie;
  if (body !== undefined && !(body instanceof FormData)) headers['Content-Type'] = 'application/json';
  const response = await fetch(url(path), {
    method,
    headers,
    body: body instanceof FormData ? body : body === undefined ? undefined : JSON.stringify(body),
    redirect: 'error',
    signal: signal || AbortSignal.timeout(30_000),
  });
  // Never log response headers, credentials, or successful authentication payloads.
  const sessionCookie = response.headers.getSetCookie().find((value) => value.startsWith('dbx_session='));
  if (sessionCookie) cookie = sessionCookie.split(';', 1)[0];
  if (raw && response.ok) return response;
  const text = await response.text();
  let data;
  try { data = text ? JSON.parse(text) : null; } catch { data = null; }
  if (!response.ok && !allowError) {
    const detail = path.startsWith('/auth/') ? 'authentication failed' : data?.detail || data?.error || text.slice(0, 500);
    throw new Error(`${method} ${path}: HTTP ${response.status}: ${typeof detail === 'string' ? detail : JSON.stringify(detail)}`);
  }
  if (allowError) return { status: response.status, data };
  assert.ok(data !== null || text === 'null' || !text, `${path}: expected JSON response`);
  return data;
}

const post = (path, body) => request(path, { method: 'POST', body });
const remove = (path) => request(path, { method: 'DELETE' });
const query = (sql, options = {}) => post('/query/execute', {
  connectionId, database: 'main', sql, timeoutSecs: 15, ...options,
});
const schema = (kind, table) => request(`/schema/${kind}?${new URLSearchParams({
  connection_id: connectionId, database: 'main', schema: '', ...(table ? { table } : {}),
})}`);

async function check(label, test) {
  await test();
  console.log(`PASS ${label}`);
}

function assertRows(result) {
  assert.deepEqual(result.columns, expectedColumns);
  assert.deepEqual(result.rows, expectedRows);
}

/** Subscribe before starting a broadcast export so fast jobs cannot lose 'done'. */
async function progress(path) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(new Error(`Progress timed out: ${path}`)), 45_000);
  let response;
  try {
    response = await request(path, { raw: true, signal: controller.signal });
    assert.match(response.headers.get('content-type') || '', /text\/event-stream/);
  } catch (error) {
    clearTimeout(timeout);
    throw error;
  }
  const done = (async () => {
    const reader = response.body.getReader();
    const decoder = new TextDecoder();
    let buffered = '';
    try {
      while (true) {
        const { value, done } = await reader.read();
        if (done) throw new Error(`Progress closed without terminal success: ${path}`);
        buffered += decoder.decode(value, { stream: true }).replaceAll('\r\n', '\n');
        let boundary;
        while ((boundary = buffered.indexOf('\n\n')) !== -1) {
          const frame = buffered.slice(0, boundary);
          buffered = buffered.slice(boundary + 2);
          const payload = frame.split('\n').filter((line) => line.startsWith('data:'))
            .map((line) => line.slice(5).trimStart()).join('\n');
          if (!payload) continue;
          const event = JSON.parse(payload);
          // Import status is camelCase; export's Rust enum serializes PascalCase.
          const status = String(event.status).toLowerCase();
          if (status === 'error' || status === 'cancelled') {
            throw new Error(`${path}: ${event.errorMessage || event.error || event.status}`);
          }
          if (status === 'done') return event;
        }
      }
    } finally {
      clearTimeout(timeout);
      await reader.cancel().catch(() => {});
      controller.abort();
    }
  })();
  // Attach a rejection handler immediately while the start request is running.
  done.catch(() => {});
  return { done, stop: () => controller.abort() };
}

async function roundTrip(format) {
  const exportId = randomUUID();
  const stream = await progress(`/export/query-result/progress/${exportId}`);
  exportsInFlight.add(exportId);
  let bytes;
  try {
    const sql = 'SELECT id, name, amount, note FROM smoke_rows ORDER BY id';
    const started = await post('/export/query-result', { request: {
      exportId, connectionId, database: 'main', databaseType: 'sqlite', sql, queryBaseSql: sql,
      filePath: `smoke.${format}`, format, pageSize: 100, nullLiteral: '\\N',
    } });
    assert.equal(started.exportId, exportId);
    const finished = await stream.done;
    assert.equal(finished.rowsExported, 2);
    const download = await request(`/export/query-result/download/${exportId}`, { raw: true });
    assert.match(download.headers.get('content-type') || '', format === 'csv' ? /text\/csv/ : /spreadsheetml/);
    bytes = Buffer.from(await download.arrayBuffer());
    exportsInFlight.delete(exportId);
    if (format === 'csv') {
      const text = bytes.toString('utf8');
      assert.ok(text.includes('中文测试') && text.includes('12.5') && text.includes('\\N'));
      assert.ok(text.includes('含逗号,引号""和换行\n尾行'));
    } else {
      assert.equal(bytes.subarray(0, 4).toString('hex'), '504b0304', 'XLSX must be a ZIP workbook');
    }
  } finally {
    stream.stop();
  }

  const parseOptions = { hasHeader: true, emptyStringAsNull: true, nullLiteral: '\\N' };
  const form = new FormData();
  form.set('file', new Blob([bytes]), `smoke.${format}`);
  form.set('connectionId', connectionId);
  form.set('database', 'main');
  form.set('sourceFormat', format === 'xlsx' ? 'excel' : 'csv');
  form.set('parseOptions', JSON.stringify(parseOptions));
  const preview = await post('/import/preview', form);
  if (preview.sourceRef) sources.add(preview.sourceRef);
  assert.equal(typeof preview.sourceRef, 'string');
  assert.deepEqual(preview.columns, expectedColumns);
  assert.equal(preview.totalRows, 2);
  assert.equal(preview.rows[0][1], '中文测试');
  assert.equal(Number(preview.rows[0][2]), 12.5);
  assert.equal(preview.rows[0][3], null);
  const table = `smoke_${format}_roundtrip`;
  await query(`CREATE TABLE ${table} (id INTEGER PRIMARY KEY, name TEXT NOT NULL, amount REAL, note TEXT)`);
  const importId = randomUUID();
  importsInFlight.add(importId);
  const started = await post('/import/execute', { request: {
    importId, connectionId, database: 'main', schema: '', table, filePath: preview.filePath,
    sourceRef: preview.sourceRef, sourceFormat: format === 'xlsx' ? 'excel' : 'csv', parseOptions,
    mappings: expectedColumns.map((column) => ({ sourceColumn: column, targetColumn: column })),
    mode: 'append', batchSize: 100, retainSource: false,
  } });
  assert.equal(started.importId, importId);
  const imported = await progress(`/import/progress/${importId}`);
  try {
    const finished = await imported.done;
    assert.equal(finished.rowsImported, 2);
    importsInFlight.delete(importId);
  } finally {
    imported.stop();
  }
  assertRows(await query(`SELECT id, name, amount, note FROM ${table} ORDER BY id`));
}

async function run() {
  if (process.env.DBX_SMOKE_ISOLATED !== '1') {
    throw new Error('Use a disposable local backend with a separate DBX_DATA_DIR; explicitly set DBX_SMOKE_ISOLATED=1.');
  }
  if (!['127.0.0.1', 'localhost', '[::1]'].includes(base.hostname)
      || !['http:', 'https:'].includes(base.protocol) || base.username || base.password || base.search || base.hash) {
    throw new Error('DBX_SMOKE_BASE_URL must be a local HTTP(S) URL without credentials, query or fragment.');
  }
  password = process.env.DBX_SMOKE_PASSWORD || '';
  if (process.env.DBX_SMOKE_PASSWORD_FILE) {
    if (password) throw new Error('Set only one of DBX_SMOKE_PASSWORD and DBX_SMOKE_PASSWORD_FILE.');
    password = (await readFile(process.env.DBX_SMOKE_PASSWORD_FILE, 'utf8')).replace(/\r?\n$/, '');
  }
  await check('authentication', async () => {
    const state = await request('/auth/check');
    assert.equal(state.setup_required, false, 'Preconfigure this disposable backend password before running smoke.');
    if (state.required && !state.authenticated) {
      assert.ok(password, 'Provide DBX_SMOKE_PASSWORD or DBX_SMOKE_PASSWORD_FILE.');
      await post('/auth/login', { password });
      assert.equal((await request('/auth/check')).authenticated, true);
    }
  });

  temporaryDirectory = await mkdtemp(join(tmpdir(), 'dbx-basic-smoke-'));
  const databaseFile = join(temporaryDirectory, 'smoke.sqlite');
  await writeFile(databaseFile, '', { mode: 0o600 });
  const config = {
    id: connectionId, name: `基础功能测试 ${connectionId}`, db_type: 'sqlite', host: databaseFile,
    port: 0, username: '', password: '', database: 'main', save_password: false,
  };
  await check('save / list / connect SQLite', async () => {
    connectionRegistered = true;
    await post('/connection/save', { configs: [config] });
    const saved = (await request('/connection/list')).find((item) => item.id === connectionId);
    assert.equal(saved?.name, config.name);
    assert.equal(saved?.host, databaseFile);
    assert.equal(saved?.db_type, 'sqlite');
    connectionAttempted = true;
    await post('/connection/connect', { config });
    assert.equal(await post('/connection/is-open', { connectionId }), true);
    await post('/connection/check-health', { connectionId });
  });
  await check('create / insert / select Chinese, NULL and decimals', async () => {
    await query('CREATE TABLE smoke_rows (id INTEGER PRIMARY KEY, name TEXT NOT NULL, amount REAL, note TEXT)');
    const values = expectedRows.map((row) => `(${row.map((value) => value === null ? 'NULL'
      : typeof value === 'number' ? String(value) : `'${value.replaceAll("'", "''")}'`).join(',')})`).join(',');
    assert.equal((await query(`INSERT INTO smoke_rows VALUES ${values}`)).affected_rows, 2);
    assertRows(await query('SELECT id, name, amount, note FROM smoke_rows ORDER BY id'));
  });
  await check('databases / tables / columns / DDL', async () => {
    assert.ok((await schema('databases')).some((item) => item.name === 'main'));
    assert.ok((await schema('tables')).some((item) => item.name === 'smoke_rows'));
    const columns = await schema('columns', 'smoke_rows');
    assert.deepEqual(columns.map((column) => column.name), expectedColumns);
    assert.equal(columns[0].is_primary_key, true);
    assert.equal(columns[1].is_nullable, false);
    const ddl = await schema('ddl', 'smoke_rows');
    assert.match(ddl, /CREATE TABLE/i);
    assert.match(ddl, /smoke_rows/);
    assert.match(ddl, /PRIMARY KEY/i);
  });
  await check('SQL LIMIT/OFFSET pagination and result limit', async () => {
    const first = await query('SELECT id FROM smoke_rows ORDER BY id LIMIT 1 OFFSET 0');
    const second = await query('SELECT id FROM smoke_rows ORDER BY id LIMIT 1 OFFSET 1');
    const last = await query('SELECT id FROM smoke_rows ORDER BY id LIMIT 1 OFFSET 2');
    assert.deepEqual(first.rows, [[1]]);
    assert.deepEqual(second.rows, [[2]]);
    assert.deepEqual(last.rows, []);
    const limited = await query('SELECT id FROM smoke_rows ORDER BY id', { maxRows: 1 });
    assert.deepEqual(limited.rows, [[1]]);
    assert.equal(limited.truncated, true);
  });
  await check('update / delete and SQL error recovery', async () => {
    assert.equal((await query("UPDATE smoke_rows SET name='修改后' WHERE id=2")).affected_rows, 1);
    assert.deepEqual((await query('SELECT name FROM smoke_rows WHERE id=2')).rows, [['修改后']]);
    await query("UPDATE smoke_rows SET name='第二行' WHERE id=2");
    await query("INSERT INTO smoke_rows VALUES (3, '删除行', 0, NULL)");
    assert.equal((await query('DELETE FROM smoke_rows WHERE id=3')).affected_rows, 1);
    assert.deepEqual((await query('SELECT COUNT(*) FROM smoke_rows')).rows, [[2]]);
    const bad = await request('/query/execute', { method: 'POST', allowError: true,
      body: { connectionId, database: 'main', sql: 'SELEC invalid syntax' } });
    assert.ok(bad.status >= 400, 'Invalid SQL must return an HTTP error');
    // AppError returns the shared BackendError v1 envelope, not { error: ... }.
    assert.equal(bad.data?.version, 1, 'Invalid SQL must return the backend error envelope');
    assert.match(bad.data?.code || '', /^DBX-[A-Z]+-\d+$/, 'Invalid SQL must have a catalog error code');
    assert.match(bad.data?.messageKey || '', /^backendErrors\./);
    assert.equal(typeof bad.data?.detail, 'string', 'Invalid SQL must include its diagnostic detail');
    assert.match(bad.data.detail, /syntax|SELEC/i, 'Error detail must describe the invalid SQL');
    assertRows(await query('SELECT id, name, amount, note FROM smoke_rows ORDER BY id'));
  });
  await check('saved SQL save / load / delete', async () => {
    const now = new Date().toISOString();
    const file = { id: savedSqlId, connectionId, name: '中文查询.sql', database: 'main',
      sql: "SELECT '中文测试', NULL, 12.5", createdAt: now, updatedAt: now };
    savedSqlAttempted = true;
    await post('/saved-sql', file);
    assert.ok((await request('/saved-sql')).files.some((item) => item.id === savedSqlId));
    assert.equal((await request(`/saved-sql/${savedSqlId}`)).sql, file.sql);
    await remove(`/saved-sql/${savedSqlId}`);
    assert.equal(await request(`/saved-sql/${savedSqlId}`), null);
    savedSqlAttempted = false;
  });
  await check('history save / search / delete', async () => {
    const entry = { id: historyId, connection_id: connectionId, connection_name: config.name,
      database: 'main', sql: "SELECT '历史记录', NULL, -2.75", executed_at: new Date().toISOString(),
      // Retention is enforced per source on every save. An isolated source
      // prevents this test from evicting an existing SQL history entry.
      execution_time_ms: 1, success: true, error: null, source: connectionId };
    historyAttempted = true;
    await post('/history/save', { entry });
    assert.equal((await request('/history?limit=100')).find((item) => item.id === historyId)?.sql, entry.sql);
    const search = () => post('/history/search', {
      connections: [{ connection_id: connectionId }], source: connectionId, limit: 100,
    });
    assert.equal((await search()).entries.find((item) => item.id === historyId)?.sql, entry.sql);
    await remove(`/history/${historyId}`);
    assert.ok(!(await search()).entries.some((item) => item.id === historyId));
    historyAttempted = false;
  });
  await check('CSV export / upload preview / import round trip', () => roundTrip('csv'));
  await check('Excel export / upload preview / import round trip', () => roundTrip('xlsx'));
  await check('disconnect / reconnect persists SQLite data', async () => {
    await post('/connection/disconnect', { connectionId });
    assert.equal(await post('/connection/is-open', { connectionId }), false);
    await post('/connection/connect', { config });
    assertRows(await query('SELECT id, name, amount, note FROM smoke_rows ORDER BY id'));
  });
}

async function cleanup() {
  const failures = [];
  const attempt = async (label, task) => {
    try { await task(); } catch (error) { failures.push(`${label}: ${safeMessage(error)}`); }
  };
  for (const importId of importsInFlight) await attempt('cancel own import', () => post('/import/cancel', { importId }));
  for (const exportId of exportsInFlight) {
    await attempt('cancel own export', () => post('/export/query-result/cancel', { exportId }));
    // Downloads consume their temporary file. A completed export can remain if
    // an assertion failed before download; an already removed export is fine.
    await attempt('release own export', async () => {
      const response = await request(`/export/query-result/download/${exportId}`, { raw: true, allowError: true });
      if (response instanceof Response) await response.arrayBuffer();
    });
  }
  for (const sourceRef of sources) await attempt('release own upload', () => post('/import/source/release', { sourceRef }));
  if (savedSqlAttempted) await attempt('delete own SQL', () => remove(`/saved-sql/${savedSqlId}`));
  if (historyAttempted) await attempt('delete own history', () => remove(`/history/${historyId}`));
  if (connectionAttempted) await attempt('disconnect own connection', () => post('/connection/disconnect', { connectionId }));
  if (connectionRegistered) {
    await attempt('remove own profile', async () => {
      await post('/connection/save', { configs: [], removedIds: [connectionId] });
      assert.ok(!(await request('/connection/list')).some((item) => item.id === connectionId));
    });
  }
  if (temporaryDirectory) await attempt('remove own SQLite directory', () => rm(temporaryDirectory, { recursive: true, force: true }));
  if (cookie) await attempt('logout test session', () => post('/auth/logout'));
  if (failures.length) throw new Error(`Cleanup failed: ${failures.join('; ')}`);
}

try {
  await run();
} catch (error) {
  console.error(`FAIL ${safeMessage(error)}`);
  process.exitCode = 1;
} finally {
  try {
    await cleanup();
    if (temporaryDirectory) console.log('PASS cleanup (only this run’s resources)');
  } catch (error) {
    console.error(`FAIL ${safeMessage(error)}`);
    process.exitCode = 1;
  }
}
if (!process.exitCode) console.log('PASS basic workbench smoke completed');
