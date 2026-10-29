// Unit tests for web/static/api.js, the shared request layer. Run with
// `node --test tests/unit`. The module is loaded into a minimal browser-like
// context with a scripted fetch, so no server or browser is needed.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../../web/static/api.js'), 'utf8');

function loadAPI(respond, { cookie = 'other=1; stepanel_csrf=tok%20en', XMLHttpRequest: XHR } = {}) {
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
  if (XHR) context.XMLHttpRequest = XHR;
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

test('reads, mutations, and uploads have absolute default timeouts', async () => {
  const { api, timers } = loadAPI(() => response(200, {}));
  await api.get('/api/x');
  assert.deepEqual(timers, [30000]);
  await api.post('/api/x', {});
  assert.deepEqual(timers, [30000, 120000]);
  await api.request('/api/upload', { method: 'POST', body: new FormData() });
  await api.request('/api/upload', { method: 'POST', body: new Blob(['archive']) });
  assert.deepEqual(timers, [30000, 120000, api.DEFAULT_UPLOAD_TIMEOUT_MS, api.DEFAULT_UPLOAD_TIMEOUT_MS]);
  await api.post('/api/x', {}, { timeout: 0 });
  await api.post('/api/x', {}, { timeout: 5000 });
  assert.deepEqual(timers, [30000, 120000, api.DEFAULT_UPLOAD_TIMEOUT_MS, api.DEFAULT_UPLOAD_TIMEOUT_MS, 5000]);
});

test('XHR uploads report progress, attach CSRF, and recognize upload completion', async () => {
  const calls = [];
  class FakeXHR {
    constructor() { this.upload = {}; this.headers = {}; }
    open(method, url) { this.method = method; this.url = url; }
    setRequestHeader(name, value) { this.headers[name] = value; }
    getResponseHeader(name) { return name === 'X-Request-ID' ? 'upload-1' : null; }
    send(body) {
      calls.push({ url: this.url, method: this.method, headers: this.headers, body });
      this.upload.onprogress({ loaded: 4, total: 10, lengthComputable: true });
      this.upload.onload({ loaded: 10, total: 10, lengthComputable: true });
      this.status = 201;
      this.responseText = JSON.stringify({ upload_id: 'upload-1' });
      this.onload();
    }
    abort() { if (this.onabort) this.onabort(); }
  }
  const progresses = [];
  const { api } = loadAPI(() => { throw new Error('fetch must not handle browser uploads'); }, { XMLHttpRequest: FakeXHR });
  const body = new FormData();
  body.append('backup', new Blob(['archive']));
  const result = await api.request('/api/cpmove/inspect', {
    method: 'POST', body, onUploadProgress: (progress) => progresses.push(progress),
  });
  assert.equal(result.upload_id, 'upload-1');
  assert.equal(calls.length, 1);
  assert.equal(calls[0].headers['X-CSRF-Token'], 'tok en');
  assert.equal(calls[0].body, body);
  assert.equal(progresses[0].percent, 40);
  assert.equal(progresses[1].complete, true);
});

test('XHR uploads abort after a configurable no-progress interval', async () => {
  class StalledXHR {
    constructor() { this.upload = {}; }
    open() {}
    setRequestHeader() {}
    send() {}
    abort() { if (this.onabort) this.onabort(); }
  }
  const { api } = loadAPI(() => { throw new Error('fetch must not handle browser uploads'); }, { XMLHttpRequest: StalledXHR });
  await assert.rejects(api.request('/api/upload', {
    method: 'POST', body: new FormData(), timeout: 1000, stallTimeout: 10,
  }), (error) => error.kind === 'upload_stalled' && /no progress/.test(error.message));
});

test('a malformed CSRF cookie does not throw out of the request layer', async () => {
  const { api, calls } = loadAPI(() => response(200, {}), { cookie: 'stepanel_csrf=%' });
  assert.equal(api.csrf(), '');
  await api.post('/api/x', {});
  assert.equal(calls[0].init.headers['X-CSRF-Token'], '');
});

test('synchronous long operations wait longer than the server deadline', async () => {
  const { api, timers } = loadAPI(() => response(200, {}));
  await api.post('/api/deployments/run', {});
  await api.post('/api/composer/shop', {});
  await api.post('/api/backups/restore-to-staging?x=1', {});
  await api.post('/api/deployments', {});
  assert.deepEqual(timers, [120000, 120000, 120000, 120000]);
});
