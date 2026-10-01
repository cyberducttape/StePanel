(() => {
  'use strict';
  const updateMetric = async (elementID, endpoint, transform) => {
    const element = document.getElementById(elementID);
    if (!element) return;
    try {
      element.classList.add('loading');
      const value = transform(await StepanelAPI.request(endpoint));
      element.textContent = value;
      element.classList.remove('loading', 'unavailable');
    } catch (error) {
      element.textContent = `Unavailable (${error.message})`;
      element.classList.remove('loading');
      element.classList.add('unavailable');
    }
  };
  const age = (date) => {
    const seconds = Math.max(0, Math.floor((Date.now() - date.getTime()) / 1000));
    if (seconds < 60) return 'Just now';
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    if (seconds < 604800) return `${Math.floor(seconds / 86400)}d ago`;
    return date.toLocaleDateString();
  };
  const refresh = () => {
    updateMetric('siteCount', '/api/sites/overview', (data) => (data.sites || []).filter((site) => !site.deleted).length || '—');
    updateMetric('backupFreshness', '/api/backups?limit=1', (data) => data.backups?.length ? age(new Date(data.backups[0].created_at)) : 'No backups created yet');
  };
  let timer;
  const start = () => { refresh(); timer = window.setInterval(refresh, 30000); };
  const stop = () => { if (timer) window.clearInterval(timer); timer = undefined; };
  document.addEventListener('visibilitychange', () => { if (document.hidden) stop(); else start(); });
  start();
  window.dashboardUpdater = Object.freeze({ refresh, destroy: stop });
})();
