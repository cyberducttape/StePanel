// Dashboard real-time updates and enhancements
// Provides live metric refresh, loading states, and better UX

class DashboardUpdater {
  constructor() {
    this.updateInterval = 30000; // 30 seconds
    this.refreshTimers = new Map();
    this.init();
  }

  init() {
    // Refresh key metrics periodically
    this.startMetricUpdates();

    // Handle visibility changes (pause updates when tab is hidden)
    document.addEventListener('visibilitychange', () => {
      if (document.hidden) {
        this.pauseUpdates();
      } else {
        this.resumeUpdates();
      }
    });
  }

  startMetricUpdates() {
    // Update site count
    this.updateMetric('siteCount', '/api/sites/overview', data => {
      if (data.sites) {
        return data.sites.filter(s => !s.deleted).length;
      }
      return '—';
    });

    // Update domain count
    this.updateMetric('domainCount', '/api/domains', data => {
      if (data.domains) {
        return data.domains.filter(d => d.managed).length;
      }
      return '—';
    });

    // Update backup freshness
    this.updateMetric('backupFreshness', '/api/backups?limit=1', data => {
      if (data.backups && data.backups.length > 0) {
        const backup = data.backups[0];
        return this.formatTimeAgo(new Date(backup.verified_at));
      }
      return 'No backups yet';
    });

    // Set up periodic refresh
    Object.keys(this.refreshTimers).forEach(key => {
      clearInterval(this.refreshTimers.get(key));
    });

    this.refreshTimers.set('sites',
      setInterval(() => this.updateAllMetrics(), this.updateInterval)
    );
  }

  async updateMetric(elementId, endpoint, transform) {
    const element = document.getElementById(elementId);
    if (!element) return;

    try {
      // Show loading state
      element.classList.add('loading');

      const response = await fetch(endpoint);
      if (!response.ok) throw new Error(`HTTP ${response.status}`);

      const data = await response.json();
      const value = transform(data);

      // Update with animation
      element.textContent = value;
      element.classList.remove('loading');

      // Flash animation on change
      element.style.animation = 'bounce-in 0.3s ease';
      setTimeout(() => {
        element.style.animation = '';
      }, 300);
    } catch (error) {
      console.error(`Failed to update ${elementId}:`, error);
      element.classList.remove('loading');
    }
  }

  async updateAllMetrics() {
    this.updateMetric('siteCount', '/api/sites/overview', data => {
      if (data.sites) {
        return data.sites.filter(s => !s.deleted).length;
      }
      return '—';
    });

    this.updateMetric('backupFreshness', '/api/backups?limit=1', data => {
      if (data.backups && data.backups.length > 0) {
        const backup = data.backups[0];
        return this.formatTimeAgo(new Date(backup.verified_at));
      }
      return 'No backups yet';
    });
  }

  formatTimeAgo(date) {
    const now = new Date();
    const seconds = Math.floor((now - date) / 1000);

    if (seconds < 60) return 'Just now';
    if (seconds < 3600) return `${Math.floor(seconds / 60)}m ago`;
    if (seconds < 86400) return `${Math.floor(seconds / 3600)}h ago`;
    if (seconds < 604800) return `${Math.floor(seconds / 86400)}d ago`;

    return date.toLocaleDateString();
  }

  pauseUpdates() {
    this.refreshTimers.forEach(timer => clearInterval(timer));
    this.refreshTimers.clear();
  }

  resumeUpdates() {
    this.startMetricUpdates();
  }

  destroy() {
    this.pauseUpdates();
  }
}

// Status indicator helper
class StatusIndicator {
  static create(state, label) {
    const indicator = document.createElement('span');
    indicator.className = `status-indicator ${state}`;
    indicator.textContent = label;
    indicator.setAttribute('aria-label', `Status: ${label}`);
    return indicator;
  }

  static states = {
    live: 'Live',
    pending: 'Pending',
    error: 'Error'
  };
}

// Activity feed updater
class ActivityUpdater {
  constructor(feedElement) {
    this.feed = feedElement;
    this.updateInterval = 20000; // 20 seconds
  }

  start() {
    this.timer = setInterval(() => this.refresh(), this.updateInterval);
  }

  async refresh() {
    try {
      const response = await fetch('/api/jobs?limit=5');
      if (!response.ok) throw new Error(`HTTP ${response.status}`);

      const data = await response.json();
      this.renderJobs(data.jobs || []);
    } catch (error) {
      console.error('Failed to refresh activity:', error);
    }
  }

  renderJobs(jobs) {
    if (!this.feed) return;

    const html = jobs.map(job => `
      <a class="activity-item activity-link" href="/api/jobs/${job.id}">
        <span class="activity-dot ${this.getStatusClass(job.state)}" aria-hidden="true"></span>
        <div>
          <strong>${this.escapeHtml(job.kind)}</strong>
          <p>${this.escapeHtml(job.user)} · ${job.state}</p>
        </div>
        <time class="activity-time" datetime="${job.started_at}">
          ${this.formatTime(new Date(job.started_at))}
        </time>
      </a>
    `).join('');

    this.feed.innerHTML = html || '<p class="empty-state">No jobs recorded yet.</p>';
  }

  getStatusClass(state) {
    switch (state) {
      case 'completed': return 'green';
      case 'failed': return 'amber';
      default: return 'blue';
    }
  }

  formatTime(date) {
    return date.toLocaleDateString('en-US', {
      month: 'short',
      day: 'numeric',
      hour: '2-digit',
      minute: '2-digit'
    });
  }

  escapeHtml(text) {
    const div = document.createElement('div');
    div.textContent = text;
    return div.innerHTML;
  }

  stop() {
    if (this.timer) clearInterval(this.timer);
  }

  destroy() {
    this.stop();
  }
}

// Initialize on page load
if (document.readyState === 'loading') {
  document.addEventListener('DOMContentLoaded', initDashboard);
} else {
  initDashboard();
}

function initDashboard() {
  // Start dashboard updates
  const updater = new DashboardUpdater();
  window.dashboardUpdater = updater;

  // Start activity feed updates
  const feedElement = document.getElementById('jobFeed');
  if (feedElement) {
    const activityUpdater = new ActivityUpdater(feedElement);
    activityUpdater.start();
    window.activityUpdater = activityUpdater;
  }

  // Clean up on page unload
  window.addEventListener('beforeunload', () => {
    if (window.dashboardUpdater) {
      window.dashboardUpdater.destroy();
    }
    if (window.activityUpdater) {
      window.activityUpdater.destroy();
    }
  });
}
