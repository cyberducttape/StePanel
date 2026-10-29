(() => {
  'use strict';
  const form = document.querySelector('#wpressForm');
  if (!form) return;
  const status = document.querySelector('#wpressStatus');
  const button = form.querySelector('button[type="submit"]');
  const file = form.querySelector('input[name="backup"]');
  let upload;
  const cancelUpload = document.createElement('button');
  cancelUpload.type = 'button';
  cancelUpload.className = 'import-upload-cancel';
  cancelUpload.textContent = 'Cancel upload';
  cancelUpload.hidden = true;
  file.closest('.file-input').after(cancelUpload);

  const formatBytes = (bytes) => {
    if (!Number.isFinite(bytes) || bytes < 0) return 'unknown size';
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let value = bytes;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit++; }
    return `${value.toFixed(unit ? 1 : 0)} ${units[unit]}`;
  };

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
    if (upload) upload.abort();
    const controller = new AbortController();
    upload = controller;
    button.disabled = true;
    cancelUpload.hidden = false;
    StepanelUI.setStatus(status, 'Uploading WordPress archive…', 'pending');
    try {
      // The server validates every field before accepting archive bytes,
      // so the archive must be the last multipart part.
      const body = new FormData(form);
      const archive = body.get('backup');
      body.delete('backup');
      if (archive) body.append('backup', archive);
      const data = await StepanelAPI.request('/api/wpress/import', {
        method: 'POST', body, signal: controller.signal,
        onUploadProgress: (progress) => {
          if (progress.complete) {
            StepanelUI.setStatus(status, 'Upload received; validating archive…', 'pending');
          } else if (progress.percent !== null) {
            StepanelUI.setStatus(status, `Uploading WordPress archive… ${progress.percent}% (${formatBytes(progress.loaded)} of ${formatBytes(progress.total)})`, 'pending');
          } else {
            StepanelUI.setStatus(status, `Uploading WordPress archive… ${formatBytes(progress.loaded)} transferred`, 'pending');
          }
        },
      });
      StepanelUI.setStatus(status, 'Restore queued; follow it in Operations.', 'pending');
      const job = await StepanelJobs.wait(data.job_id);
      if (job.state === 'completed') StepanelUI.setStatus(status, 'WordPress restore completed; verify the site before switching traffic.', 'success');
      else throw new Error(job.error || 'WordPress restore failed');
    } catch (error) {
      if (error.kind === 'aborted') StepanelUI.setStatus(status, 'Upload cancelled. Submit again to retry.', 'warning');
      else StepanelUI.setStatus(status, `${error.message} Submit again to retry.`, 'error');
    } finally {
      if (upload === controller) {
        upload = null;
        cancelUpload.hidden = true;
        button.disabled = false;
      }
    }
  });

  cancelUpload.addEventListener('click', () => upload?.abort());
})();
