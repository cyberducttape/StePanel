(() => {
  'use strict';

  const panel = document.querySelector('#capabilityCenter');
  if (!panel || document.body.dataset.isAdministrator !== 'true') return;

  const labels = {
    'runner.registry_allowlist': 'Build environment',
    'deployment.builds': 'Sandboxed builds',
    'site.lifecycle.create': 'Site creation',
    'site.lifecycle.suspend': 'Site suspension',
    'backup.offsite': 'Offsite backups',
    'mail.integration': 'Mail integration',
    'dns.management': 'DNS management',
  };

  const title = (value) => {
    const text = String(value || '').replaceAll('_', ' ');
    return text.charAt(0).toUpperCase() + text.slice(1);
  };

  const render = (capabilities) => {
    panel.replaceChildren();
    const entries = Object.entries(capabilities || {})
      .filter(([, capability]) => capability && !capability.available)
      .sort(([a], [b]) => (labels[a] || a).localeCompare(labels[b] || b));
    if (!entries.length) {
      panel.hidden = true;
      return;
    }
    panel.hidden = false;
    const heading = document.createElement('div');
    heading.className = 'section-heading';
    const h = document.createElement('h2');
    h.id = 'capabilityCenterHeading';
    h.textContent = 'Why can’t I do this?';
    heading.append(h);
    panel.append(heading);
    const intro = document.createElement('p');
    intro.className = 'import-copy';
    intro.textContent = 'Capabilities reflect verified host prerequisites. An unavailable operation includes the reason and the next operator action.';
    panel.append(intro);
    entries.forEach(([key, capability]) => {
      const article = document.createElement('article');
      article.className = 'capability-explanation';
      const name = document.createElement('strong');
      name.textContent = labels[key] || title(key);
      const state = document.createElement('span');
      state.className = `status status-${capability.mode === 'unsupported' ? 'warn' : 'ok'}`;
      state.textContent = capability.mode;
      const reason = document.createElement('p');
      reason.textContent = capability.reason || 'The host has not provided enough evidence to enable this operation.';
      article.append(name, state, reason);
      const match = (capability.reason || '').match(/(STEPANEL_[A-Z0-9_]+)/);
      if (match) {
        const fix = document.createElement('code');
        fix.textContent = `Configure ${match[1]} and reload capabilities.`;
        article.append(fix);
      }
      panel.append(article);
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
