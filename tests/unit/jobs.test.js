// Exercise the job feed's durable snapshot fallback and its ordering against
// process-local SSE events without requiring a browser or a running panel.
const test = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');

const source = fs.readFileSync(path.join(__dirname, '../../web/static/jobs.js'), 'utf8');

class FakeElement {
  constructor() { this.children = []; this.attributes = {}; }
  append(...children) { this.children.push(...children); }
  replaceChildren(...children) { this.children = [...children]; }
  setAttribute(name, value) { this.attributes[name] = value; }
  addEventListener() {}
}

function deferred() {
  let resolve;
  const promise = new Promise((done) => { resolve = done; });
  return { promise, resolve };
}

async function flush() {
  await new Promise((resolve) => setImmediate(resolve));
}

function loadJobs(respond) {
  const intervals = [];
  const requests = [];
  let eventSource;
  class FakeEventSource {
    constructor(url) { this.url = url; this.listeners = new Map(); eventSource = this; }
    addEventListener(name, listener) { this.listeners.set(name, listener); }
    close() {}
  }
  const feed = new FakeElement();
  const document = {
    visibilityState: 'visible',
    querySelector: (selector) => selector === '#jobFeed' ? feed : null,
    createElement: () => new FakeElement(),
    addEventListener() {},
  };
  const window = { EventSource: FakeEventSource };
  const context = {
    window,
    document,
    EventSource: FakeEventSource,
    setInterval: (callback, delay) => { intervals.push({ callback, delay }); return intervals.length; },
    clearInterval() {},
    setTimeout() { return 1; },
    clearTimeout() {},
    StepanelAPI: {
      request: (url) => {
        requests.push(url);
        return respond(url, requests.length);
      },
    },
  };
  window.StepanelAPI = context.StepanelAPI;
  vm.runInNewContext(source, context);
  return { jobs: window.StepanelJobs, intervals, requests, feed, get eventSource() { return eventSource; } };
}

const job = (state) => ({ id: 'job-1', kind: 'site.backup', user: 'site-a', state, started_at: '2026-10-12T00:00:00Z' });

test('durable snapshot polling surfaces a separate worker completion', async () => {
  const { jobs, intervals, requests } = loadJobs((_url, requestNumber) => Promise.resolve({ jobs: [job(requestNumber === 1 ? 'running' : 'completed')] }));
  await flush();
  assert.equal(jobs.get('job-1').state, 'running');
  assert.equal(intervals[0].delay, 30000);

  intervals[0].callback();
  await flush();
  assert.equal(jobs.get('job-1').state, 'completed');
  assert.deepEqual(requests, ['/api/jobs', '/api/jobs']);
});

test('an older snapshot cannot roll back a newer SSE event', async () => {
  const stale = deferred();
  const harness = loadJobs((_url, requestNumber) => requestNumber === 1
    ? Promise.resolve({ jobs: [job('running')] })
    : stale.promise);
  await flush();
  const { jobs, intervals, eventSource } = harness;
  assert.ok(eventSource);

  intervals[0].callback();
  eventSource.listeners.get('job')({ data: JSON.stringify(job('completed')) });
  stale.resolve({ jobs: [job('running')] });
  await flush();
  assert.equal(jobs.get('job-1').state, 'completed');
});

test('a failed refresh labels the retained snapshot as potentially stale', async () => {
  const harness = loadJobs((_url, requestNumber) => {
    if (requestNumber === 2) return Promise.reject(new Error('database unavailable'));
    return Promise.resolve({ jobs: [job(requestNumber === 1 ? 'running' : 'completed')] });
  });
  await flush();
  harness.intervals[0].callback();
  await flush();

  assert.equal(harness.jobs.get('job-1').state, 'running');
  let warning = harness.feed.children.find((child) => child.attributes.role === 'status');
  assert.ok(warning);
  assert.match(warning.textContent, /last available snapshot/);

  harness.intervals[0].callback();
  await flush();
  assert.equal(harness.jobs.get('job-1').state, 'completed');
  warning = harness.feed.children.find((child) => child.attributes.role === 'status');
  assert.equal(warning, undefined);
});
