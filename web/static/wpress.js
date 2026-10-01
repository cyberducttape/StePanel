(() => {
  'use strict';
  const form = document.querySelector('#wpressForm');
  if (!form) return;
  const status = document.querySelector('#wpressStatus');
  const button = form.querySelector('button[type="submit"]');

  StepanelAPI.request('/api/wpress/preflight').then((data) => {
    if (!data.ready) {
      button.disabled = true;
      const missing = Object.entries(data.checks || {}).filter(([, ready]) => !ready).map(([name]) => name.replaceAll('_', ' '));
      status.textContent = `Unavailable: install ${missing.join(', ')}.`;
    }
  }).catch(() => {
    button.disabled = true;
    status.textContent = 'Unable to verify WordPress restore dependencies.';
  });

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    button.disabled = true;
    status.textContent = 'Queueing WordPress restore…';
    try {
      const data = await StepanelAPI.request('/api/wpress/import', { method: 'POST', body: new FormData(form) });
      status.textContent = 'Restore queued; follow it in Operations.';
      const job = await StepanelJobs.wait(data.job_id);
      if (job.state === 'completed') status.textContent = 'WordPress restore completed; verify the site before switching traffic.';
      else throw new Error(job.error || 'WordPress restore failed');
    } catch (error) {
      status.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });
})();
