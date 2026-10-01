(() => {
  'use strict';
  const api = () => window.StepanelAPI;
  window.StepanelForms = Object.freeze({
    submitJSON: (url, value) => api().request(url, {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(value),
    }),
  });
})();
