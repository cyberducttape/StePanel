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
  const preview = document.createElement('section');
  preview.className = 'migration-preview';
  preview.setAttribute('aria-label', 'Migration preview');
  status.after(preview);

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

  const summary = (info) => [
    info.has_home ? 'website files found' : 'no website files detected',
    window.StepanelUI.plural(info.database_count || 0, 'database dump'),
    window.StepanelUI.plural(info.mailbox_count || 0, 'mailbox', 'mailboxes'),
    window.StepanelUI.plural(info.dns_zone_count || 0, 'DNS zone file'),
    window.StepanelUI.plural(info.cron_job_count || 0, 'cron schedule'),
  ].join(', ');

  const renderMigrationEvidence = (items) => {
    const section = document.createElement('section');
    section.className = 'migration-evidence';
    const heading = document.createElement('h3');
    heading.textContent = 'Migration evidence';
    section.append(heading);
    (items || []).forEach((item) => {
      const article = document.createElement('article');
      article.className = `migration-evidence-item migration-evidence-${item.status || 'unknown'}`;
      const title = document.createElement('strong');
      title.textContent = `${String(item.category || 'component').replaceAll('_', ' ')} · ${item.status || 'unknown'}`;
      article.append(title);
      [['Source', item.source_items], ['Imported', item.imported_items], ['Skipped', item.skipped_items]].forEach(([labelText, values]) => {
        if (!values || !values.length) return;
        const labelNode = document.createElement('small');
        labelNode.textContent = `${labelText}:`;
        const list = document.createElement('ul');
        values.forEach((value) => {
          const row = document.createElement('li');
          row.textContent = value;
          list.append(row);
        });
        article.append(labelNode, list);
      });
      if (item.omitted_item_count) {
        const omitted = document.createElement('small');
        omitted.textContent = `${item.omitted_item_count} additional item(s) omitted from this bounded job summary.`;
        article.append(omitted);
      }
      if (item.details) {
        const detail = document.createElement('p');
        detail.textContent = item.details;
        article.append(detail);
      }
      section.append(article);
    });
    return section;
  };

  const renderPreview = (info) => {
    preview.replaceChildren();
    const heading = document.createElement('h3'); heading.textContent = 'Transfer plan'; preview.append(heading);
    const checks = document.createElement('ul'); checks.className = 'migration-checks';
    (info.compatibility || []).forEach((check) => {
      const row = document.createElement('li'); row.dataset.state = check.status || 'unknown';
      const title = document.createElement('strong'); title.textContent = `${check.status || 'unknown'} · ${check.name}`;
      const detail = document.createElement('span'); detail.textContent = check.details || '';
      row.append(title, detail); checks.append(row);
    });
    preview.append(checks);
    const list = document.createElement('ul'); list.className = 'migration-plan';
    (info.components || []).forEach((component) => {
      const row = document.createElement('li'); row.dataset.disposition = component.planned_disposition;
      const name = document.createElement('strong'); name.textContent = `${component.planned_disposition} · ${component.category.replaceAll('_', ' ')}`;
      const source = document.createElement('span'); source.textContent = component.source;
      const details = document.createElement('small'); details.textContent = component.details || '';
      row.append(name, source, details); list.append(row);
    });
    preview.append(list);
  };

  const renderReportComponents = (items) => {
    const section = document.createElement('section'); section.className = 'migration-evidence';
    const heading = document.createElement('h3'); heading.textContent = 'Component disposition'; section.append(heading);
    const table = document.createElement('table'); table.className = 'migration-report-table';
    const head = document.createElement('tr');
    ['Component', 'Source', 'Destination', 'Disposition', 'Notes'].forEach((labelText) => { const th = document.createElement('th'); th.scope = 'col'; th.textContent = labelText; head.append(th); });
    const thead = document.createElement('thead'); thead.append(head); table.append(thead);
    const tbody = document.createElement('tbody');
    (items || []).forEach((item) => {
      const row = document.createElement('tr'); row.dataset.disposition = item.disposition || 'failed';
      [String(item.category || '').replaceAll('_', ' '), item.source || '', item.target || '—', item.disposition || 'failed', item.details || ''].forEach((value) => { const td = document.createElement('td'); td.textContent = value; row.append(td); });
      tbody.append(row);
    });
    table.append(tbody); section.append(table); return section;
  };

  const renderReportChecks = (checks) => {
    const section = document.createElement('section'); section.className = 'migration-preview migration-report-checks';
    const heading = document.createElement('h3'); heading.textContent = 'Compatibility and cutover checks'; section.append(heading);
    const list = document.createElement('ul'); list.className = 'migration-checks';
    (checks || []).forEach((check) => {
      const row = document.createElement('li'); row.dataset.state = check.status || 'unknown';
      const title = document.createElement('strong'); title.textContent = `${check.status || 'unknown'} · ${check.name}`;
      const detail = document.createElement('span'); detail.textContent = check.details || '';
      row.append(title, detail); list.append(row);
    });
    section.append(list); return section;
  };

  const addReportDownload = (report) => {
    const link = document.createElement('a'); link.className = 'quiet-action migration-report-download';
    link.textContent = 'Download detailed migration report (JSON)';
    const blob = new Blob([`${JSON.stringify(report, null, 2)}\n`], { type: 'application/json' });
    link.href = URL.createObjectURL(blob);
    link.download = `stepanel-migration-${String(report.user || 'report').replace(/[^a-z0-9_-]/gi, '-')}-${new Date().toISOString().replaceAll(':', '-')}.json`;
    link.addEventListener('click', () => setTimeout(() => URL.revokeObjectURL(link.href), 60000), { once: true });
    result.append(link);
  };

  file.addEventListener('change', async () => {
    if (inspection) inspection.abort();
    inspection = null;
    uploadID = '';
    preview.replaceChildren();
    StepanelUI.setStatus(result, '');
    const chosen = file.files && file.files[0];
    if (!chosen) {
      file.required = true;
      label.textContent = 'Choose .tar.gz backup';
      StepanelUI.setStatus(status, 'Choose a backup to inspect it before restoring.');
      return;
    }
    const controller = new AbortController();
    inspection = controller;
    file.value = '';
    file.required = false;
    button.disabled = true;
    cancelUpload.hidden = false;
    label.textContent = chosen.name;
    StepanelUI.setStatus(status, `Uploading ${formatBytes(chosen.size)} backup…`, 'pending');
    const body = new FormData(); body.append('backup', chosen);
    try {
      const info = await StepanelAPI.request('/api/cpmove/inspect', {
        method: 'POST', body, signal: controller.signal,
        onUploadProgress: (progress) => {
          if (progress.complete) {
            StepanelUI.setStatus(status, 'Upload received; inspecting backup contents…', 'pending');
          } else if (progress.percent !== null) {
            StepanelUI.setStatus(status, `Uploading backup… ${progress.percent}% (${formatBytes(progress.loaded)} of ${formatBytes(progress.total)})`, 'pending');
          } else {
            StepanelUI.setStatus(status, `Uploading backup… ${formatBytes(progress.loaded)} transferred`, 'pending');
          }
        },
      });
      uploadID = info.upload_id;
      if (info.database_count > 0) database.checked = true;
      renderPreview(info);
      StepanelUI.setStatus(status, `Ready: ${summary(info)}. DNS records and scheduled task commands are not imported; the result will show observed schedules and cutover work.`, 'warning');
    } catch (error) {
      file.required = true;
      if (error.kind === 'aborted') {
        StepanelUI.setStatus(status, 'Upload cancelled. Select the backup again to retry.', 'warning');
      } else {
        StepanelUI.setStatus(status, `${error.message} Select the backup again to retry.`, 'error');
      }
    } finally {
      if (inspection === controller) {
        inspection = null;
        cancelUpload.hidden = true;
        button.disabled = false;
      }
    }
  });

  cancelUpload.addEventListener('click', () => inspection?.abort());

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
      const value = job.result || {};
      const evidence = value.migration_evidence || [];
      const dispositions = value.components || [];
      const needsReview = job.state !== 'completed' || dispositions.some((item) => ['skipped', 'unsupported', 'failed'].includes(item.disposition)) || (value.application_rehearsal && value.application_rehearsal.status !== 'passed');
      const databaseTotal = value.databases_restored_total ?? (value.databases_restored || []).length;
      const mailboxTotal = value.mailboxes_staged_total ?? (value.mailboxes_staged || []).length;
      StepanelUI.setStatus(result, `${job.state === 'completed' ? 'Import completed' : `Import ${job.state}`}${needsReview ? ' · review the component dispositions and cutover findings' : ''}: files ${value.files_restored ? 'restored' : 'not present'}, ${window.StepanelUI.plural(databaseTotal, 'database')}, ${window.StepanelUI.plural(mailboxTotal, 'mailbox', 'mailboxes')} staged.`, needsReview ? 'warning' : 'success');
      if (value.source_archive) {
        const summaryLine = document.createElement('p'); summaryLine.className = 'migration-report-summary';
        summaryLine.textContent = `Source: ${value.source_archive} · ${formatBytes(value.archive_bytes)} compressed / ${formatBytes(value.expanded_bytes)} expanded · SHA-256 ${value.archive_sha256 || 'not recorded'}${value.finished_at ? ` · finished ${new Date(value.finished_at).toLocaleString()}` : ''}`;
        result.append(summaryLine);
      }
      if (value.compatibility) result.append(renderReportChecks(value.compatibility));
      if (job.state === 'completed') result.append(renderMigrationEvidence(evidence));
      if (dispositions.length) result.append(renderReportComponents(dispositions));
      if (value.application_rehearsal) {
        const rehearsal = document.createElement('p'); rehearsal.className = 'migration-rehearsal';
        rehearsal.textContent = `Application rehearsal: ${value.application_rehearsal.status}. ${value.application_rehearsal.details || ''}${value.application_rehearsal.duration_ms ? ` (${Math.ceil(value.application_rehearsal.duration_ms / 1000)} seconds)` : ''}`;
        result.append(rehearsal);
      }
      addReportDownload(value);
    } catch (error) {
      StepanelUI.setStatus(result, error.message, 'error');
    } finally {
      button.disabled = false;
    }
  });
})();
