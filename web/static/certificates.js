(() => {
  'use strict';
  const form = document.querySelector('#certificateForm');
  if (!form) return;
  const status = document.querySelector('#certificateStatus');
  const button = form.querySelector('button[type="submit"]');

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    button.disabled = true;
    StepanelUI.setStatus(status, 'Preparing certificate change preview…', 'pending');
    try {
      const body = Object.fromEntries(new FormData(form));
      const preview = await StepanelForms.submitJSON('/api/certificates/preview', body);
      const confirmed = await StepanelUI.confirmDangerous({
        title: `Review TLS change for ${body.domain}`,
        message: 'Certificate issuance can update the Apache TLS virtual host and reload Apache. Public HTTP or DNS validation must reach this host.',
        facts: [
          ['Domain', body.domain],
          ['Certificate file', preview.proposed.certificate_path],
          ['Existing certificate', preview.before.exists ? `Present (${preview.before.bytes} bytes)` : 'None detected'],
          ['Proposed change', preview.effects.join(' ')],
          ['Recovery', preview.recovery_plan.join(' ')],
          ['Preview expires', new Date(preview.expires_at).toLocaleString()],
        ],
        confirmText: body.domain,
        actionLabel: 'Queue certificate issuance',
      });
      if (!confirmed) { StepanelUI.setStatus(status, 'Certificate request cancelled.'); return; }
      const data = await StepanelForms.submitJSON('/api/certificates/issue', { ...body, preview_token: preview.preview_token });
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
