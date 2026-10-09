// Unit tests for web/static/api.js, the shared request layer. Run with
// `node --test tests/unit`. The module is loaded into a minimal browser-like
// context with a scripted fetch, so no server or browser is needed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../../web/static/api.js'), 'utf8');

function loadAPI(respond, { cookie = 'other=1; stepanel_csrf=tok%20en' } = {}) {
  const calls = [];
  const events = [];
  const timers = [];
  const window = {
    dispatchEvent: (event) => events.push(event),
  };
  const context = {
    window,
    document: { cookie },
    AbortController,
    FormData,
    Blob,
    CustomEvent: class { constructor(type, init) { this.type = type; this.detail = init && init.detail; } },
    setTimeout: (fn, ms) => { timers.push(ms); return setTimeout(fn, ms); },
    clearTimeout,
    fetch: async (url, init) => {
      calls.push({ url, init });
      return respond(url, init);
    },
  };
  vm.runInNewContext(source, context);
  return { api: window.StepanelAPI, calls, events, timers };
}

function response(status, body, headers = {}) {
  const text = typeof body === 'string' ? body : JSON.stringify(body);
  return {
    status,
    ok: status >= 200 && status < 300,
    text: async () => text,
    headers: { get: (name) => headers[name] || null },
  };
}

test('GET returns parsed JSON without a CSRF header', async () => {
  const { api, calls } = loadAPI(() => response(200, { ok: true }));
  assert.deepEqual({ ...(await api.get('/api/x')) }, { ok: true });
  assert.equal(calls[0].init.method, 'GET');
  assert.equal(calls[0].init.headers['X-CSRF-Token'], undefined);
});

test('mutations send JSON and the decoded CSRF token', async () => {
  const { api, calls } = loadAPI(() => response(200, {}));
  await api.post('/api/x', { a: 1 });
  await api.delete('/api/x');
  await api.request('/api/x', { method: 'DELETE', json: { confirm: 'yes' } });
  assert.equal(calls[0].init.headers['X-CSRF-Token'], 'tok en');
  assert.equal(calls[0].init.headers['Content-Type'], 'application/json');
  assert.equal(calls[0].init.body, '{"a":1}');
  assert.equal(calls[1].init.method, 'DELETE');
  assert.equal(calls[1].init.body, undefined);
  assert.equal(calls[2].init.body, '{"confirm":"yes"}');
});

test('FormData-style bodies pass through untouched', async () => {
  const { api, calls } = loadAPI(() => response(200, {}));
  const body = { formData: true };
  await api.request('/api/upload', { method: 'POST', body });
  assert.equal(calls[0].init.body, body);
  assert.equal(calls[0].init.headers['Content-Type'], undefined);
  assert.equal(calls[0].init.headers['X-CSRF-Token'], 'tok en');
});

test('errors are classified and carry the server message and data', async () => {
  const cases = [
    [400, 'validation'], [422, 'validation'], [401, 'unauthorized'], [403, 'forbidden'],
    [404, 'not_found'], [409, 'conflict'], [429, 'rate_limited'], [503, 'server'], [418, 'http'],
  ];
  for (const [status, kind] of cases) {
    const { api } = loadAPI(() => response(status, { error: 'nope', caddy_directives: 'x' }));
    await assert.rejects(api.get('/api/x'), (error) => {
      assert.equal(error.name, 'ApiError');
      assert.equal(error.kind, kind);
      assert.equal(error.status, status);
      assert.equal(error.data.caddy_directives, 'x');
      assert.match(error.message, /^nope/);
      return true;
    });
  }
});

test('server errors include the request ID for escalation', async () => {
  const { api } = loadAPI(() => response(500, { error: 'boom' }, { 'X-Request-ID': 'req-123' }));
  await assert.rejects(api.get('/api/x'), (error) => error.requestId === 'req-123' && error.message === 'boom (request ID req-123)');
});

test('message falls back to plain text, then the caller fallback, then the status', async () => {
  let api = loadAPI(() => response(502, 'Bad gateway from proxy')).api;
  await assert.rejects(api.get('/api/x'), /Bad gateway from proxy/);
  api = loadAPI(() => response(409, {})).api;
  await assert.rejects(api.get('/api/x', { errorMessage: 'Could not save' }), (error) => error.message === 'Could not save');
  api = loadAPI(() => response(409, {})).api;
  await assert.rejects(api.get('/api/x'), (error) => error.message === 'Request failed (409)');
});

test('401 notifies the shell so it can re-authenticate', async () => {
  const { api, events } = loadAPI(() => response(401, { error: 'session expired' }));
  await assert.rejects(api.get('/api/x'));
  assert.equal(events.length, 1);
  assert.equal(events[0].type, 'stepanel:unauthorized');
});

test('expectedStatus accepts a non-2xx status as success', async () => {
  const { api } = loadAPI(() => response(404, { missing: true }));
  assert.equal((await api.get('/api/x', { expectedStatus: [200, 404] })).missing, true);
});

test('network failures, timeouts, and cancellation are distinguished', async () => {
  let api = loadAPI(() => { throw new TypeError('Failed to fetch'); }).api;
  await assert.rejects(api.get('/api/x'), (error) => error.kind === 'network');

  const hang = (url, init) => new Promise((resolve, reject) => {
    init.signal.addEventListener('abort', () => reject(new Error('aborted')));
  });
  api = loadAPI(hang).api;
  await assert.rejects(api.get('/api/x', { timeout: 20 }), (error) => error.kind === 'timeout');

  api = loadAPI(hang).api;
  const controller = new AbortController();
  const pending = api.get('/api/x', { signal: controller.signal });
  controller.abort();
  await assert.rejects(pending, (error) => error.kind === 'aborted');
});

test('reads and mutations time out by default; uploads do not', async () => {
  const { api, timers } = loadAPI(() => response(200, {}));
  await api.get('/api/x');
  assert.deepEqual(timers, [30000]);
  await api.post('/api/x', {});
  assert.deepEqual(timers, [30000, 120000]);
  await api.request('/api/upload', { method: 'POST', body: new FormData() });
  await api.request('/api/upload', { method: 'POST', body: new Blob(['archive']) });
  assert.deepEqual(timers, [30000, 120000], 'uploads must not get the default mutation timeout');
  await api.post('/api/x', {}, { timeout: 0 });
  await api.post('/api/x', {}, { timeout: 5000 });
  assert.deepEqual(timers, [30000, 120000, 5000]);
});

test('a malformed CSRF cookie does not throw out of the request layer', async () => {
  const { api, calls } = loadAPI(() => response(200, {}), { cookie: 'stepanel_csrf=%' });
  assert.equal(api.csrf(), '');
  await api.post('/api/x', {});
  assert.equal(calls[0].init.headers['X-CSRF-Token'], '');
});
