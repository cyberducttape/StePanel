(() => {
  'use strict';

  // Shared request layer for every panel script. It owns CSRF headers, JSON
  // encoding, timeouts, cancellation, and error classification so feature
  // modules never call fetch() directly.

  const MUTATING = new Set(['POST', 'PUT', 'PATCH', 'DELETE']);
  // Reads time out by default; mutations do not, because uploads, imports,
  // and job submissions can legitimately take minutes. Pass `timeout` to
  // bound a specific call.
  const DEFAULT_READ_TIMEOUT_MS = 30000;

  const csrf = () => {
    const match = document.cookie.match(/(?:^|; )stepanel_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  };

  const kindForStatus = (status) => {
    if (status === 400 || status === 422) return 'validation';
    if (status === 401) return 'unauthorized';
    if (status === 403) return 'forbidden';
    if (status === 404) return 'not_found';
    if (status === 409) return 'conflict';
    if (status === 429) return 'rate_limited';
    if (status >= 500) return 'server';
    return 'http';
  };

  // ApiError carries enough context for callers to react to the failure class
  // (kind) and for operators to quote the server's request ID in reports.
  class ApiError extends Error {
    constructor(message, { kind, status = 0, requestId = '', data = null } = {}) {
      super(message);
      this.name = 'ApiError';
      this.kind = kind;
      this.status = status;
      this.requestId = requestId;
      this.data = data;
    }
  }

  // request(url, options) accepts fetch options plus:
  //   json            value to send as a JSON body (sets Content-Type)
  //   csrf            send the CSRF header; defaults to true for mutations
  //   timeout         milliseconds before the request is aborted
  //   signal          AbortSignal for caller-initiated cancellation
  //   expectedStatus  status or list of statuses treated as success
  //                   (defaults to any 2xx)
  //   errorMessage    fallback message when the server gives none
  // It resolves with the parsed JSON body ({} when empty or not JSON) and
  // rejects with an ApiError.
  const request = async (url, options = {}) => {
    const { json, csrf: withCSRF, timeout, signal, expectedStatus, errorMessage, ...init } = options;
    init.method = (init.method || 'GET').toUpperCase();
    init.headers = { ...(init.headers || {}) };
    if (json !== undefined) {
      init.headers['Content-Type'] = 'application/json';
      init.body = JSON.stringify(json);
    }
    if ((withCSRF ?? MUTATING.has(init.method)) && !init.headers['X-CSRF-Token']) {
      init.headers['X-CSRF-Token'] = csrf();
    }

    const controller = new AbortController();
    let timedOut = false;
    const limit = timeout ?? (MUTATING.has(init.method) ? 0 : DEFAULT_READ_TIMEOUT_MS);
    const timer = limit > 0 ? setTimeout(() => { timedOut = true; controller.abort(); }, limit) : null;
    const forwardAbort = () => controller.abort();
    if (signal) {
      if (signal.aborted) controller.abort();
      else signal.addEventListener('abort', forwardAbort, { once: true });
    }
    init.signal = controller.signal;

    let response;
    let text;
    try {
      response = await fetch(url, init);
      text = await response.text();
    } catch (error) {
      if (timedOut) throw new ApiError('The request timed out. Check the panel connection and try again.', { kind: 'timeout' });
      if (controller.signal.aborted) throw new ApiError('The request was cancelled.', { kind: 'aborted' });
      throw new ApiError('Could not reach StePanel. Check the network connection and try again.', { kind: 'network' });
    } finally {
      if (timer) clearTimeout(timer);
      if (signal) signal.removeEventListener('abort', forwardAbort);
    }

    let data = {};
    let isJSON = false;
    try { data = text ? JSON.parse(text) : {}; isJSON = Boolean(text); } catch (_) { data = {}; }
    const requestId = response.headers.get('X-Request-ID') || data.request_id || '';
    const expected = expectedStatus === undefined ? null : [].concat(expectedStatus);
    const ok = expected ? expected.includes(response.status) : response.ok;
    if (ok) return data;

    const kind = kindForStatus(response.status);
    // Prefer the server's error field; plain-text bodies are shown as-is, but
    // a JSON body without an error field is not useful to an operator.
    let message = data.error || (isJSON ? '' : text.trim()) || errorMessage || `Request failed (${response.status})`;
    // Server errors are the ones operators escalate; give them the ID to quote.
    if (kind === 'server' && requestId) message += ` (request ID ${requestId})`;
    const error = new ApiError(message, { kind, status: response.status, requestId, data });
    if (kind === 'unauthorized') {
      // The session expired or was revoked; the shell decides how to re-auth.
      window.dispatchEvent(new CustomEvent('stepanel:unauthorized', { detail: { url } }));
    }
    throw error;
  };

  const withMethod = (method) => (url, json, options = {}) =>
    request(url, json === undefined ? { ...options, method } : { ...options, method, json });

  window.StepanelAPI = Object.freeze({
    ApiError,
    csrf,
    request,
    get: (url, options = {}) => request(url, { ...options, method: 'GET' }),
    post: withMethod('POST'),
    put: withMethod('PUT'),
    patch: withMethod('PATCH'),
    delete: (url, options = {}) => request(url, { ...options, method: 'DELETE' }),
  });
})();
