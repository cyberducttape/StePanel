(() => {
  'use strict';
  const label = document.querySelector('#controlPlaneLabel');
  const pulse = document.querySelector('#controlPlanePulse');
  const toggle = document.querySelector('#controlPlaneDetailsToggle');
  const details = document.querySelector('#controlPlaneDetails');
  if (!label || !pulse || !toggle || !details) return;

  const isAdmin = document.body.dataset.isAdministrator === 'true';
  const read = async (url) => {
    const response = await fetch(url, { headers: { Accept: 'application/json' } });
    const text = await response.text();
    let data = {};
    try { data = JSON.parse(text); } catch (_) { /* handled below */ }
    if (!response.ok) throw new Error(data.error || text || `Request failed (${response.status})`);
    return data;
  };
  const serviceState = (services, names) => {
    for (const name of names) if (services && services[name]) return services[name];
    return null;
  };
  const status = (value) => {
    if (value === true || value === 'active' || value === 'healthy') return ['Healthy', 'ok'];
    if (value === false || value === 'failed' || value === 'degraded') return ['Degraded', 'warn'];
    return [value || 'Unknown', 'unknown'];
  };
  const add = (name, value, detail = '') => {
    const item = document.createElement('li');
    const [text, tone] = status(value);
    item.className = `control-plane-${tone}`;
    const strong = document.createElement('strong');
    strong.textContent = name;
    const state = document.createElement('span');
    state.textContent = text;
    item.append(strong, state);
    if (detail) {
      const note = document.createElement('small');
      note.textContent = detail;
      item.append(note);
    }
    details.append(item);
    return tone;
  };

  toggle.addEventListener('click', () => {
    details.hidden = !details.hidden;
    toggle.setAttribute('aria-expanded', String(!details.hidden));
  });

  (async () => {
    try {
      const health = await read('/api/health');
      const operational = isAdmin ? await read('/api/health/operational').catch((error) => ({ error })) : null;
      const checks = operational && !operational.error ? operational.checks || {} : {};
      const services = health.services || {};
      const tones = [];
      tones.push(add('API', health.ok === true, 'HTTP health endpoint responded'));
      if (isAdmin) {
        const worker = checks.durable_worker;
        tones.push(add('Worker', worker ? worker.ready : true, worker ? worker.detail : 'In-process job execution'));
        const queue = checks.dead_letter_jobs;
        tones.push(add('Job queue', queue ? queue.ready : true, queue ? queue.detail : 'Queue health is not exposed'));
        tones.push(add('Database', serviceState(services, ['mariadb', 'mysql', 'postgresql']), 'Database service probe'));
        tones.push(add('Host broker', 'Not probed', 'No safe read-only broker probe is configured'));
        try {
          const backups = await read('/api/backups?limit=1');
          const latest = (backups.backups || [])[0];
          tones.push(add('Backups', latest ? true : 'No verified backups', latest ? `Last verified ${new Date(latest.verified_at).toLocaleString()}` : 'Create and verify a backup'));
        } catch (error) {
          tones.push(add('Backups', 'Unavailable', error.message));
        }
        const offsite = checks.offsite_backup;
        tones.push(add('Offsite', offsite ? offsite.ready : 'Not configured', offsite ? offsite.detail : 'No required offsite target configured'));
        const mail = serviceState(services, ['dovecot', 'exim', 'exim4', 'postfix']);
        tones.push(add('Mail', mail || 'Not configured', mail ? 'Mail service probe' : 'No mail service detected'));
      }
      const degraded = tones.some((tone) => tone !== 'ok');
      label.textContent = isAdmin
        ? (degraded ? 'Control plane needs attention' : 'Control plane healthy')
        : (tones[0] === 'ok' ? 'Control plane reachable' : 'Control plane unavailable');
      pulse.classList.toggle('control-plane-warning', degraded);
    } catch (error) {
      label.textContent = 'Control plane unavailable';
      pulse.classList.add('control-plane-warning');
      details.replaceChildren();
      add('API', 'Unavailable', error.message);
    }
  })();
})();
