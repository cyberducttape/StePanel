(() => {
  'use strict';
  const form = document.querySelector('#deployForm');
  const version = document.querySelector('#nodeVersion');
  const status = document.querySelector('#deployStatus');
  if (!form) return;
  const api = window.StepanelAPI;
  const button = form.querySelector('button[type="submit"]');

  const option = (value, label) => {
    const item = document.createElement('option');
    item.value = value;
    item.textContent = label;
    return item;
  };

  api.get('/api/node/versions')
    .then((data) => {
      version.replaceChildren();
      for (const value of data.versions || []) version.append(option(value, value));
      if (!version.children.length) version.append(option('', 'No NVM versions installed'));
    })
    .catch(() => version.replaceChildren(option('', 'Unable to load versions')));

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    button.disabled = true;
    status.textContent = 'Deploying…';
    const data = Object.fromEntries(new FormData(form));
    data.node_version = data.version;
    delete data.version;
    data.port = Number(data.port);
    try {
      await api.post('/api/deployments', data);
      status.textContent = 'Deployment queued; monitor its durable job for completion.';
    } catch (error) {
      status.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });
})();
