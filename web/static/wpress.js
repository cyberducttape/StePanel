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
      StepanelUI.setStatus(status, `Unavailable: install ${missing.join(', ')}.`, 'warning');
    }
  }).catch(() => {
    button.disabled = true;
    StepanelUI.setStatus(status, 'Unable to verify WordPress restore dependencies.', 'warning');
  });

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    button.disabled = true;
    StepanelUI.setStatus(status, 'Queueing WordPress restore…', 'pending');
    try {
      // The server validates every field before accepting archive bytes,
      // so the archive must be the last multipart part.
      const body = new FormData(form);
      const archive = body.get('backup');
      body.delete('backup');
      if (archive) body.append('backup', archive);
      const data = await StepanelAPI.request('/api/wpress/import', { method: 'POST', body });
      StepanelUI.setStatus(status, 'Restore queued; follow it in Operations.', 'pending');
      const job = await StepanelJobs.wait(data.job_id);
      if (job.state === 'completed') StepanelUI.setStatus(status, 'WordPress restore completed; verify the site before switching traffic.', 'success');
      else throw new Error(job.error || 'WordPress restore failed');
    } catch (error) {
      StepanelUI.setStatus(status, error.message, 'error');
    } finally {
      button.disabled = false;
    }
  });
})();
