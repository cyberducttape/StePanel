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
    StepanelUI.setStatus(result, '');
    const chosen = file.files && file.files[0];
    if (!chosen) {
      label.textContent = 'Choose .tar.gz backup';
      StepanelUI.setStatus(status, 'Choose a backup to inspect it before restoring.');
      return;
    }
    label.textContent = chosen.name;
    StepanelUI.setStatus(status, 'Inspecting backup…', 'pending');
    const body = new FormData(); body.append('backup', chosen);
    try {
      const info = await StepanelAPI.request('/api/cpmove/inspect', { method: 'POST', body, signal: inspection.signal });
      uploadID = info.upload_id;
      if (info.database_count > 0) database.checked = true;
      StepanelUI.setStatus(status, `Ready: ${summary(info)}.`, 'success');
    } catch (error) {
      if (error.name !== 'AbortError') StepanelUI.setStatus(status, error.message, 'error');
    }
  });

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    if (!uploadID) { StepanelUI.setStatus(result, 'Inspect the selected backup before importing.', 'warning'); return; }
    button.disabled = true;
    StepanelUI.setStatus(result, 'Queueing import…', 'pending');
    const body = new FormData(form);
    body.delete('backup'); body.append('upload_id', uploadID);
    try {
      const data = await StepanelAPI.request('/api/cpmove/import', { method: 'POST', body });
      StepanelUI.setStatus(result, 'Import queued; follow it in Operations.', 'pending');
      const job = await StepanelJobs.wait(data.job_id);
      if (job.state !== 'completed') throw new Error(job.error || 'Import failed');
      const value = job.result || {};
      StepanelUI.setStatus(result, `Import completed: files ${value.files_restored ? 'restored' : 'not present'}, ${window.StepanelUI.plural((value.databases_restored || []).length, 'database')}, ${window.StepanelUI.plural((value.mailboxes_staged || []).length, 'mailbox', 'mailboxes')} staged.`, 'success');
    } catch (error) {
      StepanelUI.setStatus(result, error.message, 'error');
    } finally {
      button.disabled = false;
    }
  });
})();
