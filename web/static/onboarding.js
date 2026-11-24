(() => {
  'use strict';

  const list = document.querySelector('#onboardingChecklist');
  const progress = document.querySelector('#onboardingProgress');
  const refreshButton = document.querySelector('#refreshOnboarding');
  const primaryAction = document.querySelector('#primarySetupAction');
  if (!list || !progress || !window.StepanelAPI) return;

  const isAdmin = document.body.dataset.isAdministrator === 'true';
  let requestGeneration = 0;
  let controller;

  const text = (tag, className, value) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    node.textContent = value;
    return node;
  };
  const message = (error) => error && error.message ? error.message : 'request failed';
  const formatBytes = (value) => {
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let size = Math.max(0, value);
    let unit = 0;
    while (size >= 1024 && unit < units.length - 1) {
      size /= 1024;
      unit += 1;
    }
    return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
  };
  const readinessFailure = (name, check) => {
    const labels = {
      backup_capacity: 'Backup storage',
      import_capacity: 'Import staging storage',
      job_capacity: 'Job state storage',
      recovery_capacity: 'Recovery journal storage',
      root_broker: 'Privileged helper',
      dead_letter_jobs: 'Jobs needing review',
      security_audit_outcomes: 'Audit reconciliation',
      capacity_ledger: 'Resource admission',
      control_plane_integrity: 'Control-plane database integrity',
    };
    const label = labels[name] || name.replace(/_/g, ' ').replace(/^./, (letter) => letter.toUpperCase());
    const detail = typeof check?.detail === 'string' ? check.detail : '';
    if (name === 'operator_notifications') {
      if (detail.includes('no SMTP or webhook alert destination is configured')) {
        return 'Operator alerts: configure one destination in /etc/ste-panel.env. SMTP needs STEPANEL_ALERT_EMAIL_TO, STEPANEL_ALERT_EMAIL_FROM, and STEPANEL_ALERT_SMTP_HOST; a webhook needs STEPANEL_ALERT_WEBHOOK_URL and STEPANEL_ALERT_WEBHOOK_SECRET (32+ characters). Restart stepanel and stepanel-worker, then refresh this check.';
      }
      if (detail.includes('delivery failure')) {
        return `Operator alerts: ${detail}. Check the alert destination settings in /etc/ste-panel.env, then refresh this check.`;
      }
      if (detail.includes('await delivery')) {
        return `Operator alerts: ${detail}. Check that stepanel-worker is running and can reach its configured destination, then refresh this check.`;
      }
      return `${label}: ${detail || 'needs attention'}. Review alert delivery settings in /etc/ste-panel.env and verify stepanel-worker is running.`;
    }
    const capacity = detail.match(/^(\d+) bytes free; minimum is (\d+)$/);
    if (capacity) {
      return `${label}: ${formatBytes(Number(capacity[1]))} free; ${formatBytes(Number(capacity[2]))} required. Free space on this filesystem or expand it, then refresh the host checks.`;
    }
    return `${label}: ${detail || 'needs attention'}`;
  };
  const showHealthDetails = (event) => {
    const toggle = document.querySelector('#controlPlaneDetailsToggle');
    const details = document.querySelector('#controlPlaneDetails');
    if (!toggle || !details) return;
    event.preventDefault();
    details.hidden = false;
    toggle.setAttribute('aria-expanded', 'true');
    details.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  };
  const selectSetupTemplate = (template) => {
    if (template !== 'wordpress') return;
    const selector = document.querySelector('#siteCreateTemplate');
    if (!selector || selector.value === template) return;
    selector.value = template;
    selector.dispatchEvent(new Event('change', { bubbles: true }));
  };
  if (primaryAction) {
    primaryAction.addEventListener('click', () => {
      selectSetupTemplate(primaryAction.dataset.setupTemplate);
    });
  }

  const load = async () => {
    if (controller) controller.abort();
    controller = new AbortController();
    const signal = controller.signal;
    const generation = ++requestGeneration;
    list.replaceChildren(text('li', 'empty-state', 'Checking your workspace…'));
    progress.textContent = 'Checking your workspace…';

    const [sitesResult, backupsResult, readinessResult] = await Promise.allSettled([
      StepanelAPI.get('/api/sites/overview', { cache: 'no-store', signal }),
      StepanelAPI.get('/api/backups?limit=100', { cache: 'no-store', signal }),
      isAdmin
        ? StepanelAPI.get('/api/health/operational', { expectedStatus: [200, 503], cache: 'no-store', signal })
        : Promise.resolve(null),
    ]);
    if (generation !== requestGeneration) return;

    const sites = sitesResult.status === 'fulfilled' && Array.isArray(sitesResult.value.sites)
      ? sitesResult.value.sites.filter((site) => !site.deleted)
      : null;
    const backups = backupsResult.status === 'fulfilled' && Array.isArray(backupsResult.value.backups)
      ? backupsResult.value.backups
      : null;
    let recoverySite = null;
    let recoveryResult = null;
    if (isAdmin && sites && sites.length > 0) {
      const verifiedSiteNames = new Set((backups || [])
        .filter((backup) => backup.verified_at && Number.isFinite(Date.parse(backup.verified_at)))
        .map((backup) => backup.site));
      recoverySite = sites.find((site) => verifiedSiteNames.has(site.site)) || sites[0];
      [recoveryResult] = await Promise.allSettled([
        StepanelAPI.get(`/api/sites/recovery/${encodeURIComponent(recoverySite.site)}`, { cache: 'no-store', signal }),
      ]);
    }
    if (generation !== requestGeneration) return;
    const steps = [];

    if (isAdmin) {
      if (readinessResult.status !== 'fulfilled') {
        steps.push({
          title: 'Check host and job health',
          detail: `Operational health could not be checked: ${message(readinessResult.reason)}.`,
          href: '#controlPlaneDetailsToggle', label: 'Show health details', state: 'unknown',
        });
      } else {
        const readiness = readinessResult.value;
        const checks = readiness.checks && typeof readiness.checks === 'object' && !Array.isArray(readiness.checks)
          ? Object.entries(readiness.checks)
          : [];
        const failed = checks.filter(([, check]) => !check || !check.ready);
        const ready = readiness.operational === true && checks.length > 0 && failed.length === 0;
        const details = failed.slice(0, 2).map(([name, check]) => readinessFailure(name, check));
        steps.push({
          title: 'Check host and job health',
          detail: ready
            ? 'The broker, durable queue, storage, audit, and required operational checks are passing.'
            : details.length
              ? details.join(' · ')
              : checks.length === 0
                ? 'Health check details were not returned, so the host cannot be marked ready. Refresh or inspect the health details.'
                : 'The readiness endpoint responded, but did not report a ready state.',
          href: '#controlPlaneDetailsToggle', label: ready ? 'Review health checks' : checks.length ? 'Show what needs attention' : 'Show health details', state: ready ? 'done' : checks.length ? 'attention' : 'unknown',
        });
      }
    }

    if (sites === null) {
      steps.push({
        title: isAdmin ? 'Create or migrate your first site' : 'Open your assigned site',
        detail: `Site inventory could not be loaded: ${message(sitesResult.reason)}. Your sites may still exist.`,
        href: '#sites', label: 'View site status', state: 'unknown',
      });
    } else if (sites.length === 0) {
      steps.push({
        title: isAdmin ? 'Create or migrate your first site' : 'Open your assigned site',
        detail: isAdmin
          ? 'Create a WordPress site or inspect a cPanel archive before importing it.'
          : 'No sites are assigned to this account yet. Ask your hosting operator to assign one.',
        href: isAdmin ? '#sites' : '#jobs', label: isAdmin ? 'Create WordPress site' : 'View activity', state: 'next',
        template: isAdmin ? 'wordpress' : undefined,
      });
    } else {
      steps.push({
        title: isAdmin ? 'Manage your websites' : 'Open your assigned site',
        detail: `${sites.length} ${sites.length === 1 ? 'site is' : 'sites are'} available. Open a site workspace to check its domain, runtime, and application health.`,
        href: '#sites', label: 'Open sites', state: 'done',
      });
    }

    if (backups === null) {
      steps.push({
        title: 'Create a verified recovery point',
        detail: `Backup status could not be loaded: ${message(backupsResult.reason)}.`,
        href: '#sites', label: 'View backup status', state: 'unknown',
      });
    } else {
      const siteNames = sites === null ? null : new Set(sites.map((site) => site.site));
      const verified = siteNames
        ? backups.filter((backup) => siteNames.has(backup.site) && backup.verified_at && Number.isFinite(Date.parse(backup.verified_at)))
        : [];
      const latest = verified.sort((a, b) => Date.parse(b.verified_at) - Date.parse(a.verified_at))[0];
      const verifiedSites = new Set(verified.map((backup) => backup.site));
      const missingSites = sites ? sites.filter((site) => !verifiedSites.has(site.site)) : [];
      const backupHistoryMayBeTruncated = backups.length >= 100;
      const backupState = sites === null || (missingSites.length > 0 && backupHistoryMayBeTruncated)
        ? 'unknown'
        : missingSites.length > 0 || sites.length === 0 ? 'next' : 'done';
      const firstMissing = missingSites[0];
      steps.push({
        title: 'Create a verified recovery point',
        detail: sites === null
            ? 'Site inventory is unavailable, so backup status cannot be matched to an active site.'
          : firstMissing
            ? backupHistoryMayBeTruncated
              ? `No integrity-verified backup for ${firstMissing.site} appears in the latest 100 results. Check that site's full backup history before treating it as missing.`
              : `No integrity-verified backup is recorded for ${firstMissing.site}. Create one before making high-risk changes.`
            : latest
              ? `Every active site has an integrity-verified backup. The newest was verified ${new Date(latest.verified_at).toLocaleString()}. Archive integrity does not prove application recovery.`
              : 'After creating a site, make an encrypted backup and confirm its integrity here.',
        href: firstMissing ? `#site=${encodeURIComponent(firstMissing.site)}&tab=backups` : '#sites',
        label: firstMissing ? `Review ${firstMissing.site} backups` : latest ? 'Review backups' : 'Review site backups',
        state: backupState,
      });
    }

    if (isAdmin && recoverySite) {
      const recovery = recoveryResult?.status === 'fulfilled' ? recoveryResult.value : null;
      const latestBackup = recovery?.last_backup;
      const latestPassed = recovery?.last_passed_rehearsal;
      const latestAttempt = recovery?.last_rehearsal;
      const applicationProven = Boolean(
        latestBackup && latestPassed && latestPassed.outcome === 'passed' &&
        latestPassed.level === 'application' && latestPassed.backup === latestBackup.name,
      );
      let state = 'next';
      let detail = 'No application-level restore proof is recorded. Create a verified backup, then rehearse its restore and require a database-backed HTTP check.';
      if (!recovery) {
        state = 'unknown';
        detail = `Restore evidence for ${recoverySite.site} could not be checked: ${message(recoveryResult?.reason)}.`;
      } else if (applicationProven) {
        state = 'done';
        detail = `The newest recovery point for ${recoverySite.site} passed application-level restore proof in ${Math.max(0, Math.round((latestPassed.duration_ms || 0) / 1000))} seconds. This is measured evidence, not an RTO guarantee.`;
      } else if (!latestBackup) {
        detail = `${recoverySite.site} has no recovery point yet. Create and verify a backup before running a restore rehearsal.`;
      } else if (latestAttempt?.backup === latestBackup.name && latestAttempt.outcome === 'failed') {
        state = 'attention';
        detail = `The latest restore rehearsal for ${latestBackup.name} failed: ${latestAttempt.error || 'failure detail unavailable'}. Resolve the reported issue and run the rehearsal again.`;
      } else if (latestPassed?.backup === latestBackup.name && latestPassed.level !== 'application') {
        detail = `${latestBackup.name} passed archive-level rehearsal, but database import and application HTTP recovery are not proven. Run the application-level restore rehearsal.`;
      } else if (latestPassed) {
        detail = `The newest recovery point ${latestBackup.name} has no matching application-level proof. The last passing proof was for ${latestPassed.backup}; rehearse the newest backup.`;
      }
      steps.push({
        title: `Prove a full restore for ${recoverySite.site}`,
        detail,
        href: `#site=${encodeURIComponent(recoverySite.site)}&tab=backups`,
        label: applicationProven ? 'Review restore proof' : `Rehearse ${recoverySite.site} restore`,
        state,
      });
    }

    list.replaceChildren();
    steps.forEach((step, index) => {
      const item = document.createElement('li');
      item.className = `setup-step setup-step-${step.state}`;
      const marker = text('span', 'setup-step-marker', step.state === 'done' ? '✓' : String(index + 1));
      marker.setAttribute('aria-hidden', 'true');
      const body = document.createElement('div');
      body.className = 'setup-step-copy';
      body.append(text('strong', '', step.title), text('p', '', step.detail));
      const action = document.createElement('a');
      action.className = 'setup-step-action';
      action.href = step.href;
      action.textContent = step.label;
      if (step.href === '#controlPlaneDetailsToggle') {
        action.addEventListener('click', showHealthDetails);
      }
      if (step.template) action.addEventListener('click', () => selectSetupTemplate(step.template));
      item.append(marker, body, action);
      list.append(item);
    });

    const completed = steps.filter((step) => step.state === 'done').length;
    const attention = steps.filter((step) => step.state === 'attention' || step.state === 'unknown').length;
    if (primaryAction) {
      const next = steps.find((step) => step.state !== 'done') || { href: '#sites', label: 'Open sites' };
      primaryAction.href = next.href;
      primaryAction.textContent = next.label;
      if (next.template) primaryAction.dataset.setupTemplate = next.template;
      else delete primaryAction.dataset.setupTemplate;
      primaryAction.removeEventListener('click', showHealthDetails);
      if (next.href === '#controlPlaneDetailsToggle') primaryAction.addEventListener('click', showHealthDetails);
    }
    progress.textContent = attention
      ? `${completed} of ${steps.length} essentials complete · ${attention} need attention or could not be checked.`
      : `${completed} of ${steps.length} essentials complete.`;
  };

  let refreshTimer;
  const start = () => {
    load();
    if (refreshTimer) window.clearInterval(refreshTimer);
    refreshTimer = window.setInterval(load, 60000);
  };
  const stop = () => {
    if (refreshTimer) window.clearInterval(refreshTimer);
    refreshTimer = undefined;
    if (controller) controller.abort();
    requestGeneration++;
  };
  if (refreshButton) refreshButton.addEventListener('click', load);
  document.addEventListener('visibilitychange', () => {
    if (document.hidden) stop();
    else start();
  });
  window.addEventListener('pageshow', (event) => {
    if (event.persisted) start();
  });
  start();
})();
