(() => {
  'use strict';

  const csrf = () => {
    const match = document.cookie.match(/(?:^|; )stepanel_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  };

  const request = async (url, options = {}) => {
    const init = { ...options, headers: { ...(options.headers || {}) } };
    if (!init.headers['X-CSRF-Token'] && ['POST', 'PUT', 'PATCH', 'DELETE'].includes((init.method || 'GET').toUpperCase())) {
      init.headers['X-CSRF-Token'] = csrf();
    }
    const response = await fetch(url, init);
    const text = await response.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (_) { data = {}; }
    if (!response.ok) throw new Error(data.error || text || `Request failed (${response.status})`);
    return data;
  };

  window.StepanelAPI = Object.freeze({ csrf, request });
})();
