(() => {
  'use strict';
  const form = document.querySelector('#cpmoveForm');
  if (!form) return;
  const status = document.querySelector('#cpmoveStatus');
  const result = document.querySelector('#cpmoveResult');
  const label = document.querySelector('#cpmoveFileLabel');
  const button = form.querySelector('button[type="submit"]');
  const file = form.querySelector('input[name="backup"]');
  const user = form.querySelector('input[name="username"]');
  const database = form.querySelector('input[name="restore_databases"]');
  let inspection;
  let uploadID = '';

  const summary = (info) => [
    info.has_home ? 'website files found' : 'no website files detected',
    window.StepanelUI.plural(info.database_count || 0, 'database dump'),
    window.StepanelUI.plural(info.mailbox_count || 0, 'mailbox', 'mailboxes'),
  ].join(', ');

  file.addEventListener('change', async () => {
    if (inspection) inspection.abort();
    inspection = new AbortController();
    uploadID = '';
    result.textContent = '';
    const chosen = file.files && file.files[0];
    if (!chosen) {
      label.textContent = 'Choose .tar.gz backup';
      status.textContent = 'Choose a backup to inspect it before restoring.';
      return;
    }
    label.textContent = chosen.name;
    status.textContent = 'Inspecting backup…';
    const body = new FormData(); body.append('backup', chosen);
    try {
      const info = await StepanelAPI.request('/api/cpmove/inspect', { method: 'POST', body, signal: inspection.signal });
      uploadID = info.upload_id;
      if (info.database_count > 0) database.checked = true;
      status.textContent = `Ready: ${summary(info)}.`;
    } catch (error) {
      if (error.name !== 'AbortError') status.textContent = error.message;
    }
  });

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!uploadID) { result.textContent = 'Inspect the selected backup before importing.'; return; }
    button.disabled = true;
    result.textContent = 'Queueing import…';
    const body = new FormData(form);
    body.delete('backup'); body.append('upload_id', uploadID);
    try {
      const data = await StepanelAPI.request('/api/cpmove/import', { method: 'POST', body });
      result.textContent = 'Import queued; follow it in Operations.';
      const job = await StepanelJobs.wait(data.job_id);
      if (job.state !== 'completed') throw new Error(job.error || 'Import failed');
      const value = job.result || {};
      result.textContent = `Import completed: files ${value.files_restored ? 'restored' : 'not present'}, ${window.StepanelUI.plural((value.databases_restored || []).length, 'database')}, ${window.StepanelUI.plural((value.mailboxes_staged || []).length, 'mailbox', 'mailboxes')} staged.`;
    } catch (error) {
      result.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });
})();
