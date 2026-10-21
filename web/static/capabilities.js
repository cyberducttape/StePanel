(() => {
  'use strict';

  const panel = document.querySelector('#capabilityCenter');
  if (!panel || document.body.dataset.isAdministrator !== 'true') return;

  // Readable names for the capability keys reported by /api/capabilities.
  // Unknown keys fall back to a title built from the key itself.
  const labels = {
    'archive.import.database_restore': 'Restore databases from imported archives',
    'archive.import.extract': 'Extract imported archives',
    'archive.import.inspect': 'Inspect imported archives',
    'auth.legacy_token_notifications': 'Legacy token notifications',
    'auth.mfa': 'Administrator two-factor sign-in',
    'backup.offsite': 'Offsite backups',
    'broker.app.rollback': 'Application rollback through the broker',
    'broker.git.verify_key': 'Git deploy key verification',
    'database.mariadb.create': 'Create MariaDB databases',
    'database.mariadb.restore': 'Restore MariaDB databases',
    'database.mysql.create': 'Create MySQL databases',
    'database.mysql.restore': 'Restore MySQL databases',
    'database.postgresql.create': 'Create PostgreSQL databases',
    'database.postgresql.restore': 'Restore PostgreSQL databases',
    'deployment.builds': 'Sandboxed builds',
    'deployment.git': 'Git deployments',
    'dns.management': 'DNS management',
    'filesystem.quotas': 'Filesystem quotas',
    'mail.integration': 'Mail integration',
    'restore.database_only': 'Database-only restore',
    'restore.to_staging': 'Restore to staging',
    'restore.verified_file': 'Verified file restore',
    'runner.network_isolation': 'Build network isolation',
    'runner.registry_allowlist': 'Build image registry allowlist',
    'site.lifecycle.create': 'Site creation',
    'site.lifecycle.delete': 'Site deletion',
    'site.lifecycle.resume': 'Site resume',
    'site.lifecycle.suspend': 'Site suspension',
  };

  const areas = {
    archive: 'Migrations',
    auth: 'Sign-in and tokens',
    backup: 'Backups',
    broker: 'Privileged broker',
    database: 'Databases',
    deployment: 'Deployments',
    dns: 'DNS',
    filesystem: 'Filesystem',
    mail: 'Mail',
    restore: 'Restores',
    runner: 'Builds',
    site: 'Sites',
  };

  // Modes from capabilities.go, worded for an operator deciding what to do.
  const modes = {
    unsupported: { label: 'Needs setup', tone: 'warn' },
    partial: { label: 'Partly available', tone: 'info' },
    configured: { label: 'Not yet verified', tone: 'info' },
    locally_validated: { label: 'Checked locally', tone: 'info' },
  };

  const title = (value) => {
    const text = String(value || '').replaceAll('_', ' ').replaceAll('.', ' ');
    return text.charAt(0).toUpperCase() + text.slice(1);
  };

  const element = (tag, className, text) => {
    const node = document.createElement(tag);
    if (className) node.className = className;
    if (text !== undefined) node.textContent = text;
    return node;
  };

  const render = (capabilities) => {
    panel.replaceChildren();
    const entries = Object.entries(capabilities || {})
      .filter(([, capability]) => capability && !capability.available);
    if (!entries.length) {
      panel.hidden = true;
      return;
    }
    panel.hidden = false;

    const heading = element('div', 'section-heading capability-heading');
    const titleBlock = element('div');
    const h = element('h2', '', 'Why can’t I do this?');
    h.id = 'capabilityCenterHeading';
    titleBlock.append(h, element('p', 'import-copy', 'Each unavailable operation shows what the host is missing and the next step to enable it.'));
    const needsSetup = entries.filter(([, capability]) => capability.mode === 'unsupported').length;
    const summary = element('p', 'capability-summary');
    summary.append(element('strong', '', String(entries.length)), ` unavailable · ${needsSetup} need setup`);
    heading.append(titleBlock, summary);
    panel.append(heading);

    const groups = new Map();
    entries.forEach(([key, capability]) => {
      const area = areas[key.split('.')[0]] || title(key.split('.')[0]);
      if (!groups.has(area)) groups.set(area, []);
      groups.get(area).push([key, capability]);
    });

    [...groups.keys()].sort((a, b) => a.localeCompare(b)).forEach((area) => {
      const group = element('section', 'capability-group');
      const groupHeading = element('h3', 'capability-group-title', area);
      groupHeading.id = `capability-area-${area.toLowerCase().replace(/[^a-z0-9]+/g, '-')}`;
      group.setAttribute('aria-labelledby', groupHeading.id);
      const list = element('ul', 'capability-list');
      groups.get(area)
        .sort(([a], [b]) => (labels[a] || title(a)).localeCompare(labels[b] || title(b)))
        .forEach(([key, capability]) => {
          const mode = modes[capability.mode] || { label: title(capability.mode || 'unavailable'), tone: 'info' };
          const item = element('li', `capability-explanation capability-${mode.tone}`);
          const top = element('div', 'capability-explanation-head');
          top.append(element('strong', '', labels[key] || title(key)), element('span', `capability-pill capability-pill-${mode.tone}`, mode.label));
          const reason = element('p', '', capability.reason || 'The host has not provided enough evidence to enable this operation.');
          item.append(top, reason);
          const match = (capability.reason || '').match(/(STEPANEL_[A-Z0-9_]+)/);
          if (match) {
            const fix = element('p', 'capability-fix');
            fix.append('Next step: set ', element('code', '', match[1]), ' in /etc/ste-panel.env and restart StePanel.');
            item.append(fix);
          }
          list.append(item);
        });
      group.append(groupHeading, list);
      panel.append(group);
    });
  };

  fetch('/api/capabilities', { headers: { Accept: 'application/json' } })
    .then((response) => response.ok ? response.json() : Promise.reject(new Error('capability report unavailable')))
    .then((report) => render(report.capabilities))
    .catch(() => {
      panel.hidden = false;
      panel.textContent = 'Capability explanations are temporarily unavailable. Check readiness and try again.';
    });
})();
