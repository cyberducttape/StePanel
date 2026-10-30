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
  let currentPreview;
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
    const report = { generated_at: new Date().toISOString(), phase: 'pre_migration', ...info };
    addReportDownloads(report, preview);
    renderTable(preview, 'Domain mapping', info.domain_mappings, [
      ['Source domain', 'source_domain'], ['Type', 'kind'], ['Source document root', 'source_document_root'], ['Destination plan', 'destination'], ['Status', 'status'], ['Notes', 'details'],
    ]);
    renderTable(preview, 'Database version checks', info.database_versions, [
      ['Database', 'database'], ['Source family/version', (item) => [item.source_family, item.source_version].filter(Boolean).join(' ') || 'unknown'], ['Destination version', 'destination_version'], ['Assessment', 'status'], ['Notes', 'details'],
    ]);
    renderTable(preview, 'Mailbox validation', info.mailbox_validation, [
      ['Mailbox', 'address'], ['Maildir layout', 'maildir_layout'], ['Messages', 'message_files'], ['Data size', (item) => formatBytes(item.bytes || 0)], ['Assessment', 'status'], ['Notes', 'details'],
    ]);
    renderTable(preview, 'DNS cutover checklist', info.dns_cutover_checklist, [
      ['Step', 'step'], ['Status', 'status'], ['Action', 'detail'],
    ]);
    if (info.source_scan_recommended) renderSourceScanForm(preview, report);
  };

  const renderTable = (parent, titleText, rows, columns) => {
    if (!rows || !rows.length) return;
    const section = document.createElement('section'); section.className = 'migration-preview';
    const title = document.createElement('h3'); title.textContent = titleText; section.append(title);
    const table = document.createElement('table'); table.className = 'migration-report-table';
    const thead = document.createElement('thead'); const head = document.createElement('tr');
    columns.forEach(([labelText]) => { const th = document.createElement('th'); th.scope = 'col'; th.textContent = labelText; head.append(th); });
    thead.append(head); table.append(thead);
    const tbody = document.createElement('tbody');
    rows.forEach((item) => {
      const row = document.createElement('tr');
      columns.forEach(([, value]) => { const cell = document.createElement('td'); const content = typeof value === 'function' ? value(item) : item[value]; cell.textContent = content == null || content === '' ? '—' : String(content); row.append(cell); });
      tbody.append(row);
    });
    table.append(tbody); section.append(table); parent.append(section);
  };

  const markdownReport = (report) => {
    const safeLine = (value) => String(value || '').replace(/[\r\n|]/g, ' ').trim();
    const lines = [`# StePanel migration evidence`, ``, `- Phase: ${safeLine(report.phase || 'pre_migration')}`, `- Generated: ${safeLine(report.generated_at || new Date().toISOString())}`, `- Archive: ${safeLine(report.source_archive || report.archive || 'uploaded cPanel archive')}`, `- SHA-256: ${safeLine(report.archive_sha256 || 'not calculated in preview')}`, ''];
    const addTable = (title, rows, columns) => {
      if (!rows || !rows.length) return;
      lines.push(`## ${title}`, '', `| ${columns.map(([, label]) => label).join(' | ')} |`, `| ${columns.map(() => '---').join(' | ')} |`);
      rows.forEach((row) => lines.push(`| ${columns.map(([key]) => String(typeof key === 'function' ? key(row) : (row[key] ?? '—')).replaceAll('|', '\\|').replaceAll('\n', ' ')).join(' | ')} |`));
      lines.push('');
    };
    addTable('Compatibility', report.compatibility, [['status', 'Status'], ['name', 'Check'], ['details', 'Details']]);
    addTable('Domain mapping', report.domain_mappings, [['source_domain', 'Source'], ['kind', 'Type'], ['source_document_root', 'Source root'], ['destination', 'Destination'], ['status', 'Status'], ['details', 'Notes']]);
    addTable('Database versions', report.database_versions, [[(r) => `${r.source_family || ''} ${r.source_version || 'unknown'}`.trim(), 'Source'], ['database', 'Database'], ['destination_version', 'Destination'], ['status', 'Status'], ['details', 'Notes']]);
    addTable('Mailbox validation', report.mailbox_validation, [['address', 'Mailbox'], ['maildir_layout', 'Layout'], ['message_files', 'Messages'], ['status', 'Status'], ['details', 'Notes']]);
    addTable('DNS cutover checklist', report.dns_cutover_checklist, [['step', 'Step'], ['status', 'Status'], ['detail', 'Action']]);
    addTable('Post-migration verification', report.post_migration_checks, [['status', 'Status'], ['name', 'Check'], ['details', 'Details']]);
    addTable('Component dispositions', report.components, [['category', 'Component'], ['source', 'Source'], ['target', 'Destination'], ['disposition', 'Disposition'], ['details', 'Notes']]);
    if (report.source_scan) {
      const source = report.source_scan.source_inventory || {};
      const destination = report.source_scan.destination_inventory || {};
      lines.push('## Live source scan', '', `- Source host: ${source.hostname || 'unknown'}`, `- Source OS: ${[source.os?.name, source.os?.version].filter(Boolean).join(' ') || 'unknown'}`, `- Source PHP CLI: ${source.php?.version || 'unknown'} (${(source.php?.extensions || []).join(', ') || 'no extensions reported'})`, `- Destination PHP CLI: ${destination.php?.version || 'unknown'} (${(destination.php?.extensions || []).join(', ') || 'no extensions reported'})`, `- PHP-FPM note: PHP CLI extension/version comparison does not establish per-site PHP-FPM configuration.`, `- Source database: ${[source.database?.type, source.database?.version].filter(Boolean).join(' ') || 'unknown'}; version evidence: ${source.database?.version_source || 'unknown'}`, `- Destination database configuration: ${[destination.database?.type, destination.database?.version].filter(Boolean).join(' ') || 'unknown'}`, `- Ready for migration: ${report.source_scan.ready_for_migration ? 'yes, based on collected checks' : 'no or unknown'}`, '');
      addTable('Source scan findings', [...(report.source_scan.blockers || []), ...(report.source_scan.warnings || [])], [['severity', 'Severity'], ['title', 'Finding'], ['description', 'Details'], ['solution', 'Suggested action']]);
    }
    if (report.application_rehearsal) lines.push('## Application rehearsal', '', `**${report.application_rehearsal.status}** — ${report.application_rehearsal.details || ''}`, '');
    return `${lines.join('\n')}\n`;
  };

  const addReportDownloads = (report, parent) => {
    const links = document.createElement('div'); links.className = 'migration-report-downloads';
    const stem = `stepanel-migration-${String(report.user || 'report').replace(/[^a-z0-9_-]/gi, '-')}-${new Date().toISOString().replaceAll(':', '-')}`;
    [[`${stem}.json`, JSON.stringify(report, null, 2) + '\n', 'application/json', 'Download evidence (JSON)'], [
      `${stem}.md`, markdownReport(report), 'text/markdown;charset=utf-8', 'Download evidence (Markdown)',
    ]].forEach(([filename, content, type, text]) => {
      const link = document.createElement('a'); link.className = 'quiet-action'; link.textContent = text;
      link.href = URL.createObjectURL(new Blob([content], { type })); link.download = filename;
      link.addEventListener('click', () => setTimeout(() => URL.revokeObjectURL(link.href), 60000), { once: true }); links.append(link);
    });
    parent.append(links);
  };

  const renderSourceScanForm = (parent, report) => {
    const section = document.createElement('section'); section.className = 'migration-preview migration-source-scan';
    const heading = document.createElement('h3'); heading.textContent = 'Optional live source compatibility scan'; section.append(heading);
    const note = document.createElement('p'); note.textContent = 'Read-only SSH scan compares source PHP version/extensions, database server/client versions, OS, resources, and WordPress roots. It requires administrator access and a trusted SSH host key. PHP CLI may differ from site PHP-FPM.'; section.append(note);
    const fields = document.createElement('div'); fields.className = 'migration-scan-fields';
    const addInput = (name, labelText, type = 'text', required = false, placeholder = '') => {
      const labelNode = document.createElement('label'); labelNode.textContent = labelText;
      const input = document.createElement('input'); input.name = name; input.type = type; input.required = required; input.placeholder = placeholder; labelNode.append(input); fields.append(labelNode); return input;
    };
    const host = addInput('source_ssh_host', 'Source SSH host', 'text', true, 'source.example.com');
    const port = addInput('source_ssh_port', 'SSH port', 'number', false, '22'); port.value = '22';
    const username = addInput('source_ssh_user', 'SSH user', 'text', false, 'root'); username.value = 'root';
    const key = addInput('source_ssh_key', 'Private key (optional)', 'password', false, 'Key is sent only to this panel request');
    const known = document.createElement('label'); known.textContent = 'Trusted known_hosts entry (required when panel host has no preinstalled host key)';
    const knownInput = document.createElement('textarea'); knownInput.name = 'source_ssh_known_hosts'; knownInput.rows = 2; knownInput.autocomplete = 'off'; known.append(knownInput); fields.append(known);
    const run = document.createElement('button'); run.type = 'button'; run.className = 'button-secondary'; run.textContent = 'Run read-only source scan';
    const scanStatus = document.createElement('p'); scanStatus.setAttribute('role', 'status');
    const scanOutput = document.createElement('div');
    run.addEventListener('click', async () => {
      if (!host.value.trim()) { scanStatus.textContent = 'Enter the source SSH host.'; return; }
      run.disabled = true; scanStatus.textContent = 'Queueing secure source scan…';
      try {
        const payload = { source_ssh_host: host.value.trim(), source_ssh_port: Number(port.value || 22), source_ssh_user: username.value.trim() || 'root', source_ssh_known_hosts: knownInput.value.trim() };
        if (key.value) payload.source_ssh_key = btoa(unescape(encodeURIComponent(key.value)));
        key.value = '';
        const queued = await StepanelAPI.request('/api/admin/migration-doctor', { method: 'POST', json: payload });
        payload.source_ssh_key = undefined;
        let response;
        for (let attempt = 0; attempt < 150; attempt++) {
          await new Promise((resolve) => setTimeout(resolve, 2000));
          response = await StepanelAPI.request(`/api/admin/migration-doctor/status?job_id=${encodeURIComponent(queued.job_id)}`);
          if (['completed', 'failed', 'dead-letter'].includes(response.state)) break;
        }
        if (!response || response.state !== 'completed' || !response.analysis) throw new Error(response?.error || 'Source scan did not complete before the 5-minute wait ended. Check Operations for its status.');
        report.source_scan = response.analysis;
        if (currentPreview) currentPreview.source_scan = response.analysis;
        preview.querySelectorAll('.migration-report-downloads').forEach((downloads) => downloads.remove());
        scanOutput.replaceChildren();
        renderTable(scanOutput, 'Live source comparison', response.analysis.readiness_checks || [], [['status', 'Status'], ['category', 'Area'], ['title', 'Finding'], ['detail', 'Evidence']]);
        const summary = document.createElement('p'); summary.textContent = `Scan completed ${response.analysis.source?.scan_time ? new Date(response.analysis.source.scan_time).toLocaleString() : ''}. ${response.analysis.blockers?.length || 0} blocker(s), ${response.analysis.warnings?.length || 0} warning(s).`; scanOutput.prepend(summary);
        addReportDownloads(report, scanOutput);
        scanStatus.textContent = 'Source compatibility scan completed. Review the findings and download the updated evidence.';
      } catch (error) { scanStatus.textContent = error.message; }
      finally { run.disabled = false; }
    });
    section.append(fields, run, scanStatus, scanOutput); parent.append(section);
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

  file.addEventListener('change', async () => {
    if (inspection) inspection.abort();
    inspection = null;
    uploadID = '';
    currentPreview = null;
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
      info.archive = chosen.name;
      currentPreview = info;
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
      addReportDownloads({ ...currentPreview, ...value, phase: 'post_migration', generated_at: new Date().toISOString() }, result);
    } catch (error) {
      StepanelUI.setStatus(result, error.message, 'error');
    } finally {
      button.disabled = false;
    }
  });
})();
