(() => {
  'use strict';
  const form = document.querySelector('#certificateForm');
  if (!form) return;
  const status = document.querySelector('#certificateStatus');
  const button = form.querySelector('button[type="submit"]');

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    button.disabled = true;
    StepanelUI.setStatus(status, 'Requesting certificate…', 'pending');
    try {
      const body = Object.fromEntries(new FormData(form));
      const data = await StepanelForms.submitJSON('/api/certificates/issue', body);
      StepanelUI.setStatus(status, 'Certificate request queued; follow it in Operations.', 'pending');
      const job = await StepanelJobs.wait(data.job_id);
      if (job.state === 'completed') StepanelUI.setStatus(status, 'Certificate issued; verify the Apache site configuration.', 'success');
      else throw new Error(job.error || 'Certificate issuance failed');
    } catch (error) {
      StepanelUI.setStatus(status, error.message, 'error');
    } finally {
      button.disabled = false;
    }
  });
})();
