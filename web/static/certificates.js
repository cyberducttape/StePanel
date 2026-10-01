(() => {
  'use strict';
  const form = document.querySelector('#certificateForm');
  if (!form) return;
  const status = document.querySelector('#certificateStatus');
  const button = form.querySelector('button[type="submit"]');

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    button.disabled = true;
    status.textContent = 'Requesting certificate…';
    try {
      const body = Object.fromEntries(new FormData(form));
      const data = await StepanelForms.submitJSON('/api/certificates/issue', body);
      status.textContent = 'Certificate request queued; follow it in Operations.';
      const job = await StepanelJobs.wait(data.job_id);
      if (job.state === 'completed') status.textContent = 'Certificate issued; verify the Apache site configuration.';
      else throw new Error(job.error || 'Certificate issuance failed');
    } catch (error) {
      status.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });
})();
