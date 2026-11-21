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
  const showHealthDetails = (event) => {
    const toggle = document.querySelector('#controlPlaneDetailsToggle');
    const details = document.querySelector('#controlPlaneDetails');
    if (!toggle || !details) return;
    event.preventDefault();
    details.hidden = false;
    toggle.setAttribute('aria-expanded', 'true');
    details.scrollIntoView({ behavior: 'smooth', block: 'nearest' });
  };

  const load = async () => {
    if (controller) controller.abort();
    controller = new AbortController();
    const signal = controller.signal;
    const generation = ++requestGeneration;
    list.replaceChildren(text('p', 'empty-state', 'Checking your workspace…'));
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
        const failed = Object.entries(readiness.checks || {}).filter(([, check]) => !check || !check.ready);
        const ready = readiness.operational === true && failed.length === 0;
        const details = failed.slice(0, 2).map(([name, check]) => `${name.replace(/_/g, ' ')}: ${check.detail || 'needs attention'}`);
        steps.push({
          title: 'Check host and job health',
          detail: ready
            ? 'The broker, durable queue, storage, audit, and required operational checks are passing.'
            : details.length
              ? details.join(' · ')
              : 'The readiness endpoint responded, but did not report a ready state.',
          href: '#controlPlaneDetailsToggle', label: ready ? 'Review health checks' : 'Show what needs attention', state: ready ? 'done' : 'attention',
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
        href: isAdmin ? '#sites' : '#jobs', label: isAdmin ? 'Set up a site' : 'View activity', state: 'next',
      });
    } else {
      steps.push({
        title: isAdmin ? 'Create or migrate your first site' : 'Open your assigned site',
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
      const latest = siteNames
        ? backups.filter((backup) => siteNames.has(backup.site) && backup.verified_at)
          .sort((a, b) => new Date(b.verified_at) - new Date(a.verified_at))[0]
        : null;
      const backupHistoryMayBeTruncated = backups.length >= 100;
      const backupState = latest ? 'done' : sites === null || (sites.length > 0 && backupHistoryMayBeTruncated) ? 'unknown' : 'next';
      steps.push({
        title: 'Create a verified recovery point',
        detail: latest
          ? `Latest integrity-verified backup: ${new Date(latest.verified_at).toLocaleString()}. An archive check does not prove application recovery.`
          : sites === null
            ? 'Site inventory is unavailable, so backup status cannot be matched to an active site.'
          : sites && sites.length
            ? backupHistoryMayBeTruncated
              ? 'No verified backup for an active site appears in the latest 100 results. Check the site workspace for its exact backup history.'
              : 'No verified backup is recorded for an active site yet. Create one before making high-risk changes.'
            : 'After creating a site, make an encrypted backup and confirm its integrity here.',
        href: '#sites', label: latest ? 'Review backups' : 'Review site backups', state: backupState,
      });
    }

    list.replaceChildren();
    steps.forEach((step, index) => {
      const item = document.createElement('article');
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
      item.append(marker, body, action);
      list.append(item);
    });

    const completed = steps.filter((step) => step.state === 'done').length;
    const attention = steps.filter((step) => step.state === 'attention' || step.state === 'unknown').length;
    if (primaryAction) {
      const next = steps.find((step) => step.state !== 'done') || { href: '#sites', label: 'Open sites' };
      primaryAction.href = next.href;
      primaryAction.textContent = next.label;
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
