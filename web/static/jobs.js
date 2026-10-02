(() => {
  'use strict';
  const api = () => window.StepanelAPI;
  const jobs = new Map();
  const waiters = new Map();
  let stream;
  let reconnectTimer;

  const isActive = (job) => ['queued', 'running'].includes(job.state);
  const terminal = (job) => ['completed', 'failed', 'cancelled', 'dead-letter'].includes(job.state);
  const label = (job) => ({
    'cpmove.restore': 'Restore',
    'site.backup': 'Backup',
    'certificate.issue': 'Certificate',
    'wordpress.restore': 'WordPress restore',
    'backup.restore': 'Backup restore',
    'node.deployment': 'Deployment',
    'migration.analysis': 'Migration analysis',
  }[job.kind] || job.kind.replaceAll('.', ' '));
  const duration = (job) => {
    const start = new Date(job.started_at);
    const end = job.finished_at ? new Date(job.finished_at) : new Date();
    const seconds = Math.max(0, Math.floor((end - start) / 1000));
    if (seconds < 60) return `${seconds}s elapsed`;
    return `${Math.floor(seconds / 60)}m ${seconds % 60}s elapsed`;
  };
  const record = (job) => {
    jobs.set(job.id, job);
    const listeners = waiters.get(job.id) || [];
    listeners.forEach((resolve) => { if (terminal(job)) resolve(job); });
    if (terminal(job)) waiters.delete(job.id);
  };
  const notify = (job) => {
    record(job);
    render();
  };
  // A snapshot is the server's authoritative view (recent jobs plus every
  // active job), so it replaces the local map instead of being merged into it;
  // jobs pruned on the server disappear here too. Only jobs someone is still
  // waiting on are kept until their own status request settles.
  const replaceAll = (list) => {
    const previous = new Map(jobs);
    jobs.clear();
    for (const [id, job] of previous) if (waiters.has(id)) jobs.set(id, job);
    list.forEach(record);
    render();
  };
  const load = async () => {
    const data = await api().request('/api/jobs');
    replaceAll(data.jobs || []);
  };
  const connect = () => {
    if (!window.EventSource) return;
    if (stream) stream.close();
    stream = new EventSource('/api/jobs/events');
    stream.addEventListener('snapshot', (event) => {
      const data = JSON.parse(event.data);
      replaceAll(data.jobs || []);
    });
    stream.addEventListener('job', (event) => notify(JSON.parse(event.data)));
    stream.onerror = () => {
      stream.close();
      clearTimeout(reconnectTimer);
      reconnectTimer = setTimeout(connect, 3000);
    };
  };
  const cancel = async (id) => {
    await api().request(`/api/jobs/${encodeURIComponent(id)}`, { method: 'POST' });
    const job = jobs.get(id);
    if (job) notify({ ...job, cancel_requested: true });
  };
  // Operator actions never fail silently: the button reflects the request
  // while it is in flight and any failure is shown next to it.
  const requestCancel = (job, button, status) => {
    button.disabled = true;
    button.textContent = 'Requesting cancellation…';
    status.textContent = '';
    cancel(job.id).catch((error) => {
      button.disabled = false;
      button.textContent = 'Cancel';
      status.textContent = `Cancellation failed: ${error.message || 'request was not accepted'}`;
    });
  };
  const wait = (id) => new Promise((resolve) => {
    const current = jobs.get(id);
    if (current && terminal(current)) { resolve(current); return; }
    waiters.set(id, [...(waiters.get(id) || []), resolve]);
    api().request(`/api/jobs/${encodeURIComponent(id)}`).then(notify).catch(() => {});
  });
  const render = () => {
    const list = document.querySelector('#jobCenterList');
    const count = document.querySelector('#jobCenterCount');
    const all = [...jobs.values()];
    // Count over every known job, not just the rows that fit on screen.
    const active = all.filter(isActive).length;
    if (count) count.textContent = active;
    const visible = all.sort((a, b) => new Date(b.started_at) - new Date(a.started_at)).slice(0, 8);
    const feed = document.querySelector('#jobFeed');
    if (feed) {
      feed.replaceChildren();
      visible.slice(0, 5).forEach((job) => {
        const row = document.createElement('div'); row.className = 'activity-item';
        const link = document.createElement('a'); link.className = 'activity-link'; link.href = `/api/jobs/${encodeURIComponent(job.id)}`;
        const dot = document.createElement('span'); dot.className = `activity-dot ${job.state === 'completed' ? 'green' : job.state === 'failed' ? 'amber' : 'blue'}`; dot.setAttribute('aria-hidden', 'true');
        const text = document.createElement('div'); const strong = document.createElement('strong'); strong.textContent = job.kind; const detail = document.createElement('p'); detail.textContent = `${job.user} · ${job.state}`; text.append(strong, detail);
        const time = document.createElement('time'); time.className = 'activity-time'; time.dateTime = job.started_at; time.textContent = new Date(job.started_at).toLocaleDateString('en-US', { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
        link.append(dot, text, time); row.append(link);
        if (isActive(job)) {
          const button = document.createElement('button'); button.type = 'button'; button.className = 'quiet-action danger activity-cancel';
          button.textContent = job.cancel_requested ? 'Cancellation requested' : 'Cancel';
          button.disabled = Boolean(job.cancel_requested);
          const status = document.createElement('small'); status.className = 'job-center-error'; status.setAttribute('role', 'status');
          button.addEventListener('click', () => requestCancel(job, button, status)); row.append(button, status);
        }
        feed.append(row);
      });
      if (!visible.length) { const empty = document.createElement('p'); empty.className = 'empty-state'; empty.textContent = 'No jobs have been recorded yet.'; feed.append(empty); }
    }
    if (!list) return;
    list.replaceChildren();
    if (!visible.length) {
      const empty = document.createElement('p');
      empty.className = 'job-center-empty';
      empty.textContent = 'No operations yet.';
      list.append(empty);
      return;
    }
    visible.forEach((job) => {
      const item = document.createElement('article');
      item.className = 'job-center-item';
      const heading = document.createElement('div');
      heading.className = 'job-center-heading';
      const title = document.createElement('strong');
      title.textContent = `${label(job)}${job.user ? ` · ${job.user}` : ''}`;
      const state = document.createElement('span');
      state.className = `job-center-state ${job.state}`;
      state.textContent = job.state === 'completed' ? 'Complete ✓' : job.state;
      heading.append(title, state);
      item.append(heading);
      if (job.state === 'running' || job.state === 'queued') {
        const progress = document.createElement('progress');
        progress.max = 100; progress.value = job.progress || 0;
        progress.setAttribute('aria-label', `${label(job)} progress`);
        item.append(progress);
        const meta = document.createElement('small');
        meta.textContent = `${job.progress || 0}% · ${duration(job)}`;
        item.append(meta);
        const cancelButton = document.createElement('button');
        cancelButton.type = 'button'; cancelButton.className = 'quiet-action danger';
        cancelButton.textContent = job.cancel_requested ? 'Cancellation requested' : 'Cancel';
        cancelButton.disabled = Boolean(job.cancel_requested);
        const cancelStatus = document.createElement('small'); cancelStatus.className = 'job-center-error'; cancelStatus.setAttribute('role', 'status');
        cancelButton.addEventListener('click', () => requestCancel(job, cancelButton, cancelStatus));
        item.append(cancelButton, cancelStatus);
      } else if (job.error) {
        const error = document.createElement('small');
        error.className = 'job-center-error'; error.textContent = job.error;
        item.append(error);
      }
      const details = document.createElement('a');
      details.className = 'quiet-action';
      details.href = `/api/jobs/${encodeURIComponent(job.id)}`;
      details.textContent = 'View details';
      item.append(details);
      list.append(item);
    });
  };

  const init = async () => {
    try { await load(); } catch (_) { /* the page can still render its server snapshot */ }
    connect();
    const toggle = document.querySelector('#jobCenterToggle');
    const drawer = document.querySelector('#jobCenter');
    // The drawer is non-modal, so focus is not trapped; opening moves focus
    // into it, and Escape or Close returns focus to the control that opened it.
    const openDrawer = () => {
      if (!drawer) return;
      drawer.removeAttribute('hidden');
      toggle?.setAttribute('aria-expanded', 'true');
      document.querySelector('#jobCenterHeading')?.focus();
    };
    const closeDrawer = ({ restoreFocus = true } = {}) => {
      if (!drawer || drawer.hasAttribute('hidden')) return;
      drawer.setAttribute('hidden', '');
      toggle?.setAttribute('aria-expanded', 'false');
      if (restoreFocus) toggle?.focus();
    };
    toggle?.addEventListener('click', () => { if (drawer?.hasAttribute('hidden')) openDrawer(); else closeDrawer(); });
    document.querySelector('#jobCenterClose')?.addEventListener('click', () => closeDrawer());
    drawer?.addEventListener('keydown', (event) => {
      if (event.key === 'Escape') { event.stopPropagation(); closeDrawer(); }
    });
    render();
  };
  window.StepanelJobs = Object.freeze({ init, load, wait, get: (id) => jobs.get(id), render });
  init();
})();
