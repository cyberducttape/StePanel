(() => {
  'use strict';

  const grid = document.querySelector('#siteOverview');
  const gridStatus = document.querySelector('#siteOverviewStatus');
  const detail = document.querySelector('#siteDetail');
  if (!grid || !gridStatus || !detail) return;

  const isAdministrator = document.body.dataset.isAdministrator === 'true';
  const accountRole = document.body.dataset.accountRole || 'owner';
  const webserver = document.body.dataset.webserver || 'caddy';
  const roleScopes = {
    developer: new Set(['site:read', 'site:deploy', 'deploy:write', 'environment:read', 'environment:write', 'logs:read', 'backup:read', 'backup:create', 'database:read', 'redis:read', 'files:read', 'files:write']),
    viewer: new Set(['site:read', 'environment:read', 'backup:read', 'database:read', 'redis:read', 'logs:read', 'files:read']),
  };
  const can = (scope) => isAdministrator || accountRole === 'owner' || accountRole === 'manager' || (roleScopes[accountRole] || new Set()).has(scope);

  // ---------------------------------------------------------------------
  // Shared request helpers
  // ---------------------------------------------------------------------

  const api = window.StepanelAPI;
  const getJSON = (path) => api.get(path);
  // Mutations without a payload send an empty JSON object, as the handlers
  // expect a body.
  const postJSON = (path, body) => api.post(path, body ?? {});
  const putJSON = (path, body) => api.put(path, body ?? {});
  const patchJSON = (path, body) => api.patch(path, body ?? {});
  const deleteJSON = (path, body) => api.delete(path, body === undefined ? {} : { json: body });
  // Builds, deployments, dependency installs, and staging restores answer
  // with a durable job. Follow it to the end and return the operation's
  // result, or throw its error, so callers read like a synchronous request.
  // The job keeps running if this page is closed; Activity shows it.
  const runOperation = async (path, body, onQueued) => {
    const queued = await postJSON(path, body);
    if (!queued || !queued.job_id) return queued;
    if (onQueued) onQueued(queued.job_id);
    const done = new Set(['completed', 'failed', 'cancelled', 'dead-letter']);
    for (let delay = 1000; ; delay = Math.min(delay * 1.5, 5000)) {
      await new Promise((resolve) => setTimeout(resolve, delay));
      const job = await getJSON(`/api/jobs/${encodeURIComponent(queued.job_id)}`);
      if (!done.has(job.state)) continue;
      if (job.state === 'completed') return job.output || {};
      throw new Error(job.error || `The operation was ${job.state}.`);
    }
  };

  // ---------------------------------------------------------------------
  // Small DOM helpers
  // ---------------------------------------------------------------------

  // Disables the clicked button for the duration of an async handler so a
  // slow request (or an impatient double-click) cannot fire the same
  // mutation twice. Re-enabling is skipped if the handler already replaced
  // or removed the button from the document.
  const withBusyClick = (node, onClick) => async (event) => {
    if (node.disabled) return;
    node.disabled = true;
    try {
      await onClick(event);
    } finally {
      if (node.isConnected) node.disabled = false;
    }
  };

  // Same guard for form submissions: disables the form's own submit button
  // rather than the form itself, so labels/inputs stay interactive.
  const withBusySubmit = (onSubmit) => async (event) => {
    const submitButton = event.currentTarget.querySelector('button[type="submit"]');
    if (submitButton && submitButton.disabled) { event.preventDefault(); return; }
    if (submitButton) submitButton.disabled = true;
    try {
      await onSubmit(event);
    } finally {
      if (submitButton && submitButton.isConnected) submitButton.disabled = false;
    }
  };

  const el = (tag, props = {}, children = []) => {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
      if (key === 'className') node.className = value;
      else if (key === 'dataset') Object.assign(node.dataset, value);
      else if (key === 'onClick' && typeof value === 'function') node.addEventListener('click', withBusyClick(node, value));
      else if (key === 'onSubmit' && typeof value === 'function') node.addEventListener('submit', withBusySubmit(value));
      else if (key.startsWith('on') && typeof value === 'function') node.addEventListener(key.slice(2).toLowerCase(), value);
      else if (value !== undefined && value !== null) node.setAttribute(key, value);
    }
    for (const child of [].concat(children)) {
      if (child === null || child === undefined) continue;
      node.append(child.nodeType ? child : document.createTextNode(String(child)));
    }
    return node;
  };

  const button = (text, onClick, props = {}) => el('button', { type: 'button', onClick, ...props }, text);

  const statusOutput = () => el('output', { role: 'status', 'aria-live': 'polite' });

  const errorState = (label, error) => el('p', { className: 'error-state', role: 'alert' }, `${label} unavailable: ${error?.message || 'request failed'}`);

  // A high-entropy password so a 20+ character requirement isn't something
  // the person has to type by hand. crypto.getRandomValues works without a
  // secure context, unlike crypto.subtle, so this has no HTTPS dependency.
  const generateSecret = (length = 28) => {
    const alphabet = 'ABCDEFGHJKLMNPQRSTUVWXYZabcdefghijkmnopqrstuvwxyz23456789!@#%^&*-_=+';
    const bytes = new Uint8Array(length);
    crypto.getRandomValues(bytes);
    return Array.from(bytes, (b) => alphabet[b % alphabet.length]).join('');
  };

  // Adds a "Generate" button next to a password field created by field(),
  // filling it with generateSecret() and switching it to plain text so the
  // person can see (and copy) what was generated.
  const withGenerateButton = (passwordFieldNode) => {
    const input = passwordFieldNode.querySelector('input');
    passwordFieldNode.append(button('Generate', () => {
      input.value = generateSecret();
      input.type = 'text';
    }, { className: 'quiet-action' }));
    return passwordFieldNode;
  };

  // A labeled field: replaces placeholder-only inputs so the field's purpose
  // stays visible once the user has typed a value.
  const field = ({ label, tag = 'input', name, type = 'text', value = '', placeholder = '', hint = '', required = false, pattern, min, max, minlength, options, checked, rows }) => {
    const wrap = el('div', { className: tag === 'checkbox-field' ? 'field field-check' : 'field' });
    const id = `field-${name}-${Math.random().toString(36).slice(2, 8)}`;
    let input;
    if (tag === 'select') {
      input = el('select', { id, name });
      for (const option of options || []) {
        input.append(el('option', { value: option.value }, option.label));
      }
      if (value) input.value = value;
    } else if (tag === 'textarea') {
      input = el('textarea', { id, name, placeholder, required: required || undefined, rows: rows || 6 }, value);
    } else if (tag === 'checkbox-field') {
      input = el('input', { id, name, type: 'checkbox', checked: checked || undefined });
      wrap.append(input, el('label', { for: id }, label));
      if (hint) wrap.append(el('small', { className: 'hint' }, hint));
      return wrap;
    } else {
      input = el('input', {
        id, name, type, value, placeholder,
        required: required || undefined,
        pattern, min, max, minlength,
      });
    }
    wrap.append(el('label', { for: id }, label));
    wrap.append(input);
    if (hint) wrap.append(el('small', { className: 'hint' }, hint));
    return wrap;
  };

  const badge = (text, kind = 'off') => el('span', { className: `badge badge-${kind}` }, [el('i', { 'aria-hidden': 'true' }), text]);

  const plural = window.StepanelUI.plural;

  const formatAge = (value) => {
    if (!value) return 'Never';
    const age = Math.max(0, Date.now() - new Date(value).getTime());
    const minutes = Math.floor(age / 60000);
    if (minutes < 1) return 'Just now';
    if (minutes < 60) return `${minutes}m ago`;
    const hours = Math.floor(minutes / 60);
    if (hours < 48) return `${hours}h ago`;
    return `${Math.floor(hours / 24)}d ago`;
  };

  const formatBytes = (value) => {
    if (!value) return '0 B';
    const units = ['B', 'KB', 'MB', 'GB', 'TB'];
    let size = value, unit = 0;
    while (size >= 1024 && unit < units.length - 1) { size /= 1024; unit += 1; }
    return `${size.toFixed(unit === 0 ? 0 : 1)} ${units[unit]}`;
  };

  const latestBackup = (backups, site) => {
    const matching = (backups || []).filter((item) => item.verified_at && (!site || item.site === site));
    return matching.sort((a, b) => new Date(b.verified_at) - new Date(a.verified_at))[0];
  };

  // Shared typed-confirmation dialog for destructive actions (web/static/ui.js).
  const confirmDangerous = window.StepanelUI.confirmDangerous;

  // ---------------------------------------------------------------------
  // Site workspace: tabbed detail view
  // ---------------------------------------------------------------------

  const TABS = [
    { id: 'overview', label: 'Overview', render: renderOverviewTab },
    { id: 'journey', label: 'Guided workflow', render: renderLifecycleTab },
    { id: 'domains', label: 'Domains', render: renderDomainsTab },
    { id: 'runtime', label: 'Runtime', render: renderRuntimeTab },
    { id: 'deployments', label: 'Deployments', render: renderDeploymentsTab },
    { id: 'databases', label: 'Databases', render: renderDatabasesTab },
    { id: 'backups', label: 'Backups', render: renderBackupsTab },
    { id: 'logs', label: 'Logs', render: renderLogsTab },
    { id: 'workers', label: 'Workers', render: renderWorkersTab },
    { id: 'tasks', label: 'Tasks', render: renderTasksTab },
    { id: 'security', label: 'Security', render: renderSecurityTab },
    { id: 'settings', label: 'Settings', render: renderSettingsTab },
  ];

  // The open workspace's site/tab is reflected in the URL hash so a refresh,
  // a bookmark, or the browser back/forward buttons behave the way they do
  // for any other page instead of always dropping back to the site grid.
  const workspaceHash = (site, tab) => `#site=${encodeURIComponent(site)}&tab=${encodeURIComponent(tab)}`;

  const parseWorkspaceHash = () => {
    if (!location.hash.startsWith('#site=')) return null;
    const params = new URLSearchParams(location.hash.slice(1));
    const site = params.get('site');
    if (!site) return null;
    return { site, tab: params.get('tab') || TABS[0].id };
  };

  function openWorkspace(site, initialTabId, { pushHistory = true } = {}) {
    detail.hidden = false;
    detail.replaceChildren();

    const heading = el('h3', {}, `${site} workspace`);
    const closeButton = button('Close', () => {
      detail.hidden = true;
      detail.replaceChildren();
      history.pushState(null, '', location.pathname + location.search);
    }, { className: 'quiet-action' });
    const headingRow = el('div', { className: 'section-heading' }, [heading, closeButton]);

    const tablist = el('div', { className: 'workspace-tablist', role: 'tablist', 'aria-label': `${site} sections` });
    const panel = el('div', { className: 'workspace-tabpanel', role: 'tabpanel', tabindex: '0' });
    const tabButtons = [];
    const startIndex = Math.max(0, TABS.findIndex((tab) => tab.id === initialTabId));
    let activeIndex = startIndex;
    let requestToken = 0;

    const activate = async (index) => {
      activeIndex = index;
      tabButtons.forEach((tabButton, i) => {
        const selected = i === index;
        tabButton.setAttribute('aria-selected', String(selected));
        tabButton.tabIndex = selected ? 0 : -1;
      });
      panel.setAttribute('aria-labelledby', `tab-${TABS[index].id}`);
      history.replaceState({ site, tab: TABS[index].id }, '', workspaceHash(site, TABS[index].id));
      const token = ++requestToken;
      panel.replaceChildren(el('p', { className: 'import-note' }, 'Loading…'));
      const renderPanel = document.createElement('div');
      try {
        await TABS[index].render(site, renderPanel, { isAdministrator, accountRole, can, webserver, getJSON, postJSON, runOperation, putJSON, patchJSON, deleteJSON, field, button, badge, formatAge, formatBytes, el, confirmDangerous, statusOutput });
        if (token !== requestToken) return;
        panel.replaceChildren(...renderPanel.childNodes);
      } catch (error) {
        if (token === requestToken) panel.replaceChildren(el('p', { className: 'import-note' }, error.message));
      }
    };

    TABS.forEach((tab, index) => {
      const tabButton = el('button', {
        type: 'button',
        role: 'tab',
        className: 'workspace-tab',
        id: `tab-${tab.id}`,
        'aria-selected': String(index === startIndex),
        'aria-controls': 'workspace-panel',
        tabindex: index === startIndex ? '0' : '-1',
        onClick: () => activate(index),
        onKeydown: (event) => {
          let target = null;
          if (event.key === 'ArrowRight') target = (index + 1) % TABS.length;
          else if (event.key === 'ArrowLeft') target = (index - 1 + TABS.length) % TABS.length;
          else if (event.key === 'Home') target = 0;
          else if (event.key === 'End') target = TABS.length - 1;
          if (target === null) return;
          event.preventDefault();
          tabButtons[target].focus();
          activate(target);
        },
      }, tab.label);
      tabButtons.push(tabButton);
      tablist.append(tabButton);
    });
    panel.id = 'workspace-panel';

    detail.append(headingRow, tablist, panel);
    if (pushHistory) {
      history.pushState({ site, tab: TABS[startIndex].id }, '', workspaceHash(site, TABS[startIndex].id));
    }
    activate(startIndex);
    detail.scrollIntoView({ behavior: 'smooth', block: 'start' });
  }

  window.addEventListener('popstate', (event) => {
    if (event.state && event.state.site) {
      openWorkspace(event.state.site, event.state.tab, { pushHistory: false });
    } else if (!detail.hidden && !parseWorkspaceHash()) {
      detail.hidden = true;
      detail.replaceChildren();
    }
  });
  // A followed #site=NAME&tab=TAB link (the customer panel links straight
  // to a site's tab) opens that workspace.
  window.addEventListener('hashchange', () => {
    const target = parseWorkspaceHash();
    if (target) openWorkspace(target.site, target.tab, { pushHistory: false });
  });

  // A site lifecycle guide that brings the existing migration, deployment,
  // diagnostics, TLS, recovery, staging, and termination workflows together.
  // Every status is based on a named API observation; unavailable evidence is
  // rendered as unknown instead of being treated as a successful check.
  async function renderLifecycleTab(site, panel, ctx) {
    const observe = (promise) => promise.then((value) => ({ value }), (error) => ({ error }));
    const checks = await Promise.all([
      observe(ctx.getJSON(`/api/sites/overview/${encodeURIComponent(site)}`)),
      observe(ctx.getJSON(`/api/sites/diagnostics/${encodeURIComponent(site)}`)),
      observe(ctx.getJSON(`/api/sites/php/${encodeURIComponent(site)}`)),
      observe(ctx.getJSON(`/api/sites/resources/${encodeURIComponent(site)}`)),
      ctx.can('backup:read') ? observe(ctx.getJSON(`/api/sites/recovery/${encodeURIComponent(site)}`)) : Promise.resolve(null),
      ctx.can('backup:read') ? observe(ctx.getJSON(`/api/backups?site=${encodeURIComponent(site)}&limit=100`)) : Promise.resolve(null),
      observe(ctx.getJSON(`/api/wordpress/status/${encodeURIComponent(site)}`)),
      ctx.can('logs:read') ? observe(ctx.getJSON(`/api/sites/logs/${encodeURIComponent(site)}?source=error&lines=40`)) : Promise.resolve(null),
      ctx.can('logs:read') ? observe(ctx.getJSON(`/api/sites/logs/${encodeURIComponent(site)}?source=php-fpm&lines=40`)) : Promise.resolve(null),
      observe(ctx.getJSON(`/api/deployments?site=${encodeURIComponent(site)}`)),
    ]);
    const [overviewResult, diagnosticResult, phpResult, resourceResult, recoveryResult, backupResult, wordpressResult, webLogResult, phpLogResult, deploymentResult] = checks;
    const overview = overviewResult.value || {};
    const diagnostics = diagnosticResult.value || {};
    const php = phpResult.value || {};
    const resources = resourceResult.value || {};
    const recovery = recoveryResult?.value || null;
    const backups = backupResult?.value?.backups || [];
    const wordpress = wordpressResult.value || {};
    const tls = diagnostics.tls || [];
    const routes = overview.routes || [];
    const apps = overview.applications || [];
    const deployments = (deploymentResult.value?.deployments || []).slice().sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
    const latest = backups.filter((item) => item.verified_at).sort((a, b) => new Date(b.verified_at) - new Date(a.verified_at))[0];
    const fpm = resources.fpm_status || {};
    const profiles = php.profiles || (php.profile ? [php.profile] : []);
    const logLines = [...(webLogResult?.value?.lines || []), ...(phpLogResult?.value?.lines || [])].map((line) => typeof line === 'string' ? line : (line.message || line.line || JSON.stringify(line)));
    const notableLogs = logLines.filter((line) => /error|critical|fatal|failed|denied|timeout|refused|upstream|database connection|ssl|tls/i.test(line)).slice(-3);
    const nav = (tab) => workspaceHash(site, tab);
    const link = (label, href, className = 'quiet-action') => el('a', { className, href }, label);
    const statePill = (state, label) => el('span', { className: `lifecycle-state lifecycle-state-${state}`, role: 'status' }, label);
    const wordpressOutput = el('output', { id: 'lifecycleWordPressOutput', className: 'lifecycle-command-output', role: 'status', 'aria-live': 'polite' });
    const step = (number, title, state, stateLabel, description, actions = []) => el('article', { className: `lifecycle-step lifecycle-step-${state}` }, [
      el('div', { className: 'lifecycle-step-marker', 'aria-hidden': 'true' }, String(number)),
      el('div', { className: 'lifecycle-step-content' }, [
        el('div', { className: 'lifecycle-step-heading' }, [el('h3', {}, title), statePill(state, stateLabel)]),
        el('p', {}, description),
        actions.length ? el('div', { className: 'workspace-panel-actions' }, actions) : null,
      ]),
    ]);
    // StePanel has service and WP-CLI checks here, but no universal live HTTP
    // probe for every site's public route. Keep application health explicitly
    // unverified even when a process or wp-config.php is present.
    const applicationState = apps.length || wordpress.installed ? 'attention' : 'unknown';
    const tlsProblem = tls.some((item) => ['expired', 'expiring', 'missing', 'hostname_mismatch', 'failed'].includes(item.status));
    const tlsUnknown = tls.some((item) => item.status === 'unknown') || !tls.length;
    const tlsState = tlsProblem ? 'attention' : tlsUnknown ? 'unknown' : 'done';
    const recoveryState = recovery ? (recovery.confidence === 'high' || recovery.confidence === 'verified' ? 'done' : latest ? 'attention' : 'next') : 'unknown';
    const phpPools = fpm.pools || [];
    const phpIssue = profiles.some((profile) => profile.state === 'pending' || profile.last_error) || phpPools.some((pool) => pool.state !== 'available');

    if (overviewResult.error) {
      panel.replaceChildren(el('p', { className: 'error-state', role: 'alert' }, `Site workflow is unavailable: ${overviewResult.error.message || 'site overview request failed'}`));
      return;
    }

    const steps = [
      step(1, 'Import or create the site', 'done', 'Site registered', wordpress.installed
        ? 'WordPress files are detected. The origin and exact set of imported cPanel components are not inferred from the current site record.'
        : 'This site is registered in StePanel. Import history is not available as a lifecycle status, so confirm the intended files and data before cutover.', [
        ctx.isAdministrator ? link('Open migration center', '/#migrations', 'primary-action') : link('Site setup guidance', '/panel#websites', 'quiet-action'),
        wordpress.installed ? ctx.badge('WordPress detected', 'ok') : ctx.badge('WordPress not detected', 'off'),
      ]),
      step(2, 'Validate files, database, and application', diagnosticResult.error ? 'unknown' : diagnostics.filesystem?.status === 'failed' || diagnostics.database?.available === false ? 'attention' : 'done', diagnosticResult.error ? 'Evidence unavailable' : diagnostics.filesystem?.status === 'failed' || diagnostics.database?.available === false ? 'Needs review' : 'Host checks observed', diagnosticResult.error
        ? 'Site diagnostics did not return. Retry before relying on filesystem or database status.'
        : `${diagnostics.filesystem?.detail || 'Filesystem evidence is unavailable.'} ${diagnostics.database?.detail || 'Database evidence is unavailable.'} Database inventory does not prove the application can authenticate or query its database.`, [
        link('Review health and diagnostics', nav('overview')),
        wordpressResult.error ? ctx.badge('WordPress check unavailable', 'warn') : ctx.badge(wordpress.installed ? (wordpress.wp_cli ? 'WordPress + WP-CLI' : 'WordPress; WP-CLI missing') : 'No wp-config.php detected', wordpress.installed && wordpress.wp_cli ? 'ok' : 'warn'),
      ]),
      step(3, 'Deploy and verify the application', applicationState, applicationState === 'attention' ? 'Live application check needed' : 'No application detected', apps.length
        ? `${apps.length} managed application(s); latest deployment ${deployments[0] ? `${deployments[0].state} (${ctx.formatAge(deployments[0].created_at)})` : 'not recorded'}. Service state is not an HTTP health check.`
        : wordpress.installed ? 'WordPress configuration is present. Run WP-CLI status and verify a database-backed page over HTTPS after import or deployment.' : 'No managed application or WordPress configuration was detected. Choose the deployment workflow that matches the site.', [
        link('Deployments and release history', nav('deployments')),
        wordpress.installed && ctx.can('site:deploy') ? ctx.button('Run WordPress status', async (event) => {
          wordpressOutput.textContent = 'Running WP-CLI status as the site identity…';
          try {
            const result = await ctx.postJSON(`/api/wordpress/${encodeURIComponent(site)}`, { action: 'status' });
            wordpressOutput.textContent = result.output || 'WP-CLI completed without status output.';
          } catch (error) { wordpressOutput.textContent = error.message; }
        }) : null,
      ]),
      step(4, 'Secure the domain and troubleshoot HTTPS', tlsState, tlsState === 'done' ? 'TLS metadata looks good' : tlsState === 'attention' ? 'TLS needs attention' : 'TLS not verified', routes.length
        ? tls.map((item) => `${item.domain}: ${String(item.status || 'unknown').replaceAll('_', ' ')}${item.detail ? ` — ${item.detail}` : ''}`).join(' · ') || 'TLS status was not returned for connected routes.'
        : 'No web route is connected. Add the domain and confirm its DNS points to this host.', [
        link('Domains and TLS', nav('domains')),
        el('small', { className: 'lifecycle-guidance' }, ctx.webserver === 'caddy'
          ? 'For Caddy, check A/AAAA records, inbound ports 80/443, route status, and Caddy logs. This panel does not claim a live external TLS handshake.'
          : 'For Apache, confirm DNS and virtual-host routing, then use the certificate workflow. Check certificate hostname/expiry and test HTTPS externally.'),
      ]),
      step(5, 'Create and prove a recovery point', recoveryState, recovery ? (recovery.last_passed_rehearsal ? `${recovery.last_passed_rehearsal.level || 'archive'} rehearsal recorded` : 'No passing rehearsal') : 'Recovery evidence unavailable', recovery
        ? `${recovery.last_backup ? `Newest backup: ${recovery.last_backup.name} (${ctx.formatAge(recovery.last_backup.created_at)}).` : 'No backup recorded.'} ${recovery.last_passed_rehearsal ? `Latest passing proof: ${recovery.last_passed_rehearsal.level || 'archive'} for ${recovery.last_passed_rehearsal.backup}.` : 'An archive verification alone does not establish that the restored application works.'}`
        : 'Recovery status could not be loaded or this role cannot view it. Do not assume a backup or restore proof exists.', [
        link('Backups, restore rehearsals, and staging clones', nav('backups')),
        latest ? ctx.badge(`Latest integrity-verified backup ${ctx.formatAge(latest.verified_at)}`, 'ok') : ctx.badge(backupResult?.error ? 'Backup inventory unavailable' : 'No verified backup found', 'warn'),
      ]),
      step(6, 'Clone to staging and promote safely', 'next', 'Optional before cutover', 'Create an isolated clone, validate WordPress and database-backed behavior there, and use the guarded promotion preview when ready. Staging is not a substitute for an independent backup.', [
        link('Open staging and clone actions', nav('backups')),
      ]),
      ...(ctx.isAdministrator ? [step(7, 'Retire the site safely', 'next', 'Destructive action', 'The settings workflow first requests a termination preview, checks prerequisites, and requires an explicit confirmation. Review the final backup and affected resources before queueing retirement.', [link('Review site settings and termination preview', nav('settings'), 'danger')])] : []),
    ];

    const diagnosticCards = [
      el('article', { className: 'lifecycle-diagnostic' }, [el('strong', {}, 'WordPress'), el('span', {}, wordpressResult.error ? 'Status unavailable' : wordpress.installed ? `Detected${wordpress.wp_cli ? ' · WP-CLI available' : ' · WP-CLI unavailable'}` : 'Not detected'), el('small', {}, 'Presence check uses wp-config.php; application login and HTTP behavior are not tested.')]),
      el('article', { className: 'lifecycle-diagnostic' }, [el('strong', {}, 'PHP-FPM'), el('span', {}, phpResult.error ? 'Diagnostics unavailable' : profiles.length ? `${profiles.map((profile) => `PHP ${profile.version} ${profile.state || 'configured'}`).join(' · ')}${phpIssue ? ' · attention' : ''}` : 'No PHP profile reported'), el('small', {}, phpPools.length ? phpPools.map((pool) => `${pool.php_version || 'default'}: ${pool.active_processes || 0}/${pool.max_children || '?'} active, queue ${pool.listen_queue || 0}, ${pool.slow_requests || 0} slow request(s)`).join(' · ') : (profiles.find((profile) => profile.last_error)?.last_error || 'Live worker counters unavailable; open Runtime for host-level details.'))]),
      el('article', { className: 'lifecycle-diagnostic' }, [el('strong', {}, 'Recent error logs'), el('span', {}, webLogResult?.error || phpLogResult?.error ? 'Log query unavailable' : `${notableLogs.length} notable line(s) in recent sample`), el('small', {}, notableLogs.length ? notableLogs.map((line) => line.slice(0, 240)).join(' · ') : 'No matching error indicators in this bounded sample; this is not proof that requests succeed.')]),
      el('article', { className: 'lifecycle-diagnostic' }, [el('strong', {}, 'Recent deployment'), el('span', {}, deploymentResult.error ? 'History unavailable' : deployments[0] ? `${deployments[0].stage || 'Deployment'} · ${deployments[0].state}` : 'No deployment recorded'), el('small', {}, deployments[0] ? `Updated ${ctx.formatAge(deployments[0].created_at)}. Compare with application and web-server logs.` : 'Use deployment history to publish a release or inspect migration activation.')]),
    ];

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'A practical path from import to retirement. Each step links to the existing site workflow; evidence marked unknown has not been verified.'),
      el('div', { className: 'lifecycle-summary' }, [
        el('div', {}, [el('p', { className: 'eyebrow' }, 'Website lifecycle'), el('h2', {}, site), el('p', {}, `${routes.length} domain route(s) · ${apps.length} managed app(s) · ${wordpress.installed ? 'WordPress detected' : 'WordPress not detected'}`)]),
        link('Open full incident overview', nav('overview'), 'primary-action'),
      ]),
      el('ol', { className: 'lifecycle-steps', 'aria-label': 'Website lifecycle steps' }, steps.map((item) => el('li', {}, item))),
      el('section', { className: 'lifecycle-diagnostics', 'aria-labelledby': 'lifecycle-diagnostics-heading' }, [
        el('div', { className: 'section-heading' }, [el('div', {}, [el('p', { className: 'eyebrow' }, 'Practical diagnostics'), el('h2', { id: 'lifecycle-diagnostics-heading' }, 'WordPress, PHP, and logs')]), link('Open PHP diagnostics', nav('runtime'))]),
        el('div', { className: 'lifecycle-diagnostic-grid' }, diagnosticCards),
        ctx.can('logs:read') ? link('Inspect site logs', nav('logs')) : el('p', { className: 'import-note' }, 'Your role does not have permission to read site logs.'),
        wordpressOutput,
      ]),
      el('p', { className: 'import-note' }, 'A green service state is not equivalent to an application-level restore proof. For a production cutover, require a verified recovery point and validate a database-backed page over the target HTTPS route.'),
    );
  }

  // ---------------------------------------------------------------------
  // Overview tab
  // ---------------------------------------------------------------------

  async function renderOverviewTab(site, panel, ctx) {
    const observe = (promise) => promise.then((value) => ({ ok: true, value }), (error) => ({ ok: false, error }));
    const [overview, backups, deploymentList, usage, php, recoveryResult, resourceResult, webLogResult, phpLogResult, accessLogResult, activityResult, databaseResult, siteDiagnosticResult] = await Promise.all([
      ctx.getJSON(`/api/sites/overview/${encodeURIComponent(site)}`),
      observe(ctx.getJSON(`/api/backups?site=${encodeURIComponent(site)}&limit=500`)),
      observe(ctx.getJSON(`/api/deployments?site=${encodeURIComponent(site)}`)),
      observe(ctx.getJSON(`/api/sites/usage/${encodeURIComponent(site)}`)),
      observe(ctx.getJSON(`/api/sites/php/${encodeURIComponent(site)}`)),
      ctx.can('backup:read') ? observe(ctx.getJSON(`/api/sites/recovery/${encodeURIComponent(site)}`)) : Promise.resolve(null),
      observe(ctx.getJSON(`/api/sites/resources/${encodeURIComponent(site)}`)),
      ctx.can('logs:read') ? observe(ctx.getJSON(`/api/sites/logs/${encodeURIComponent(site)}?source=error&lines=100`)) : Promise.resolve(null),
      ctx.can('logs:read') ? observe(ctx.getJSON(`/api/sites/logs/${encodeURIComponent(site)}?source=php-fpm&lines=100`)) : Promise.resolve(null),
      ctx.can('logs:read') ? observe(ctx.getJSON(`/api/sites/logs/${encodeURIComponent(site)}?source=access&lines=100`)) : Promise.resolve(null),
      ctx.isAdministrator ? observe(ctx.getJSON(`/api/audit/events?target=${encodeURIComponent(site)}&limit=8`)) : Promise.resolve(null),
      ctx.isAdministrator ? observe(ctx.getJSON('/api/database/diagnostics')) : Promise.resolve(null),
      observe(ctx.getJSON(`/api/sites/diagnostics/${encodeURIComponent(site)}`)),
    ]);

    const routes = overview.routes || [];
    const apps = overview.applications || [];
    const backupData = backups.ok ? backups.value : null;
    const deploymentData = deploymentList.ok ? deploymentList.value : null;
    const usageData = usage.ok ? usage.value : null;
    const phpData = php.ok ? php.value : null;
    const resourceData = resourceResult.ok ? resourceResult.value : null;
    const backup = latestBackup(backupData?.backups, site);
    const deployments = (deploymentData?.deployments || []).slice().sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
    const lastDeployment = deployments[0];

    const running = apps.some((app) => app.state === 'applied' || app.state === 'running');
    const statusBadge = apps.length === 0
      ? ctx.badge('No application deployed', 'off')
      : running ? ctx.badge('Running', 'ok') : ctx.badge('Needs attention', 'warn');
    const unavailable = (message) => ctx.badge(message, 'warn');
    const phpValue = !php.ok
      ? unavailable('Unavailable')
      : phpData && phpData.configured ? `PHP ${phpData.profile.version}` : 'Not configured';
    const phpNote = !php.ok
      ? 'PHP runtime API unavailable; retry to determine health.'
      : phpData && phpData.configured ? `OPcache ${phpData.profile.opcache ? 'on' : 'off'}` : 'Configure in Runtime';
    const backupValue = !backups.ok ? unavailable('Unavailable') : backup ? ctx.formatAge(backup.verified_at) : 'None yet';
    const backupNote = !backups.ok
      ? 'Backup API unavailable; retry to determine backup state.'
      : backup ? `${plural(backup.databases ? backup.databases.length : 0, 'database')} included` : 'Create one from the Backups tab';
    const deploymentValue = !deploymentList.ok
      ? unavailable('Unavailable')
      : lastDeployment ? `${lastDeployment.stage} · ${lastDeployment.state}` : 'None recorded';
    const deploymentNote = !deploymentList.ok
      ? 'Deployment history unavailable; retry to determine state.'
      : lastDeployment ? ctx.formatAge(lastDeployment.created_at) : 'Use the Deployments tab';
    const usageValue = !usage.ok ? unavailable('Unavailable') : usageData ? ctx.formatBytes(usageData.bytes) : 'Unavailable';
    const usageNote = !usage.ok
      ? 'Usage scan unavailable; retry to determine disk state.'
      : usageData ? `${usageData.files} files${usageData.complete ? '' : ' (partial scan)'}` : 'No usage data reported.';

    const stats = [
      ['Status', statusBadge, apps.length ? plural(apps.length, 'managed application') : 'Site files only, or PHP served directly'],
      ['Domains', routes.length ? routes.map((r) => r.domain).join(', ') : 'No domain connected', plural(routes.length, 'route')],
      ['PHP runtime', phpValue, phpNote],
      ['Last verified backup', backupValue, backupNote],
      ...(recoveryResult ? [[
        'Recovery',
        recoveryResult.ok ? recoveryBadge(recoveryResult.value, ctx) : unavailable('Unavailable'),
        recoveryResult.ok
          ? (recoveryResult.value.last_passed_rehearsal ? `Last proven ${ctx.formatAge(recoveryResult.value.last_passed_rehearsal.at)}` : 'Not yet proven by a rehearsal')
          : 'Recovery status unavailable; retry to determine state.',
      ]] : []),
      ['Last deployment', deploymentValue, deploymentNote],
      ['Disk usage', usageValue, usageNote],
    ];

    const grid = el('div', { className: 'overview-grid' }, stats.map(([label, value, note]) => el('article', { className: 'overview-stat' }, [
      el('span', { className: 'stat-label' }, label),
      value && value.nodeType ? value : el('strong', {}, value),
      note ? el('small', {}, note) : null,
    ])));

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'A quick read on whether this site is healthy: what is deployed, when it last backed up, and what changed most recently.'),
      el('div', { className: 'workspace-panel-actions' }, [el('a', { className: 'primary-action', href: workspaceHash(site, 'journey') }, 'Open guided site workflow')]),
      grid,
      renderIncidentAssessment({ overview, phpData, phpAvailable: php.ok, resourceData, resourceAvailable: resourceResult.ok, usageData, usageAvailable: usage.ok, recovery: recoveryResult && recoveryResult.ok ? recoveryResult.value : null, recoveryAvailable: Boolean(recoveryResult && recoveryResult.ok), lastDeployment, webLogResult, phpLogResult, accessLogResult, canReadLogs: ctx.can('logs:read'), latestChange: activityResult && activityResult.ok ? (activityResult.value.events || [])[0] : null, databaseDiagnostics: databaseResult && databaseResult.ok ? databaseResult.value : null, databaseDiagnosticsAvailable: databaseResult && databaseResult.ok, siteDiagnostics: siteDiagnosticResult && siteDiagnosticResult.ok ? siteDiagnosticResult.value : null, siteDiagnosticsAvailable: siteDiagnosticResult && siteDiagnosticResult.ok }),
    );

    if (ctx.isAdministrator) {
      if (activityResult && activityResult.ok) {
        const events = activityResult.value.events || [];
        panel.append(
          el('h4', {}, 'Recent activity'),
          events.length
            ? el('ul', { className: 'resource-list' }, events.map((event) => el('li', { className: 'resource-list-item' }, [
              el('div', { className: 'item-meta' }, [el('strong', {}, event.action), el('small', {}, event.detail || '')]),
              el('small', {}, new Date(event.time).toLocaleString()),
            ])))
            : el('p', { className: 'empty-state' }, 'No recorded activity yet.'),
        );
      } else {
        panel.append(el('h4', {}, 'Recent activity'), errorState('Audit trail', activityResult && activityResult.error));
      }
    }
  }

  function renderIncidentAssessment(signals) {
    const findings = [];
    const add = (level, title, evidence, next) => findings.push({ level, title, evidence, next });
    const { overview, phpData, phpAvailable, resourceData, resourceAvailable, usageData, usageAvailable, recovery, recoveryAvailable, lastDeployment, webLogResult, phpLogResult, accessLogResult, canReadLogs, latestChange, databaseDiagnostics, databaseDiagnosticsAvailable, siteDiagnostics, siteDiagnosticsAvailable } = signals;
    const applications = overview.applications || [];
    const unhealthy = applications.filter((app) => !['applied', 'running'].includes(String(app.state || '').toLowerCase()));
    if (unhealthy.length) add('critical', 'Application is not reported as running', unhealthy.map((app) => `${app.domain}: ${app.state || 'unknown state'}`).join('; '), 'Review the deployment history and application logs before retrying activation.');
    if (phpAvailable && phpData && phpData.configured && phpData.profile && (phpData.profile.state === 'pending' || phpData.profile.last_error)) {
      add('warning', 'PHP runtime changes need attention', phpData.profile.last_error || `Runtime profile is ${phpData.profile.state}.`, 'Open Runtime and reconcile the pending PHP-FPM configuration.');
    } else if (!phpAvailable) {
      add('unknown', 'PHP runtime state is unavailable', 'The site PHP profile endpoint did not return a usable result.', 'Retry the site overview and inspect the Runtime tab if this persists.');
    }
    if (resourceAvailable && resourceData) {
      const profile = resourceData.profile || {};
      const observed = resourceData.observed || {};
      if (resourceData.configured === false || profile.state === 'pending' || profile.filesystem_quota_state === 'apply-pending' || profile.filesystem_quota_state === 'clear-pending' || observed.state === 'failed' || observed.state === 'unavailable') {
        add('warning', 'Resource enforcement needs verification', `Desired profile: ${profile.state || 'not configured'}; observed unit: ${observed.state || 'unknown'}; quota: ${profile.filesystem_quota_state || 'unknown'}.`, 'Review the resource profile and host quota state before increasing workload.');
      }
      if (usageAvailable && usageData && Number.isFinite(Number(profile.disk_mb)) && Number(profile.disk_mb) > 0) {
        const limit = Number(profile.disk_mb) * 1024 * 1024;
        if (usageData.bytes >= limit * 0.9) add('warning', 'Site is near its configured disk allowance', `${formatBytes(usageData.bytes)} of ${formatBytes(limit)} in regular-file content (${Math.min(100, Math.round(usageData.bytes / limit * 100))}%).`, 'Review old releases, uploads, and backup policy before the site reaches its configured limit. Do not raise the limit until host free space and inode headroom are checked.');
        if (profile.filesystem_quota_state !== 'enforced') add('unknown', 'Disk quota enforcement is not confirmed', `Configured allowance is ${formatBytes(limit)}, but quota state is ${profile.filesystem_quota_state || 'unknown'}.`, 'Ask an administrator to verify the filesystem quota from the host; measured tree size alone does not prove enforcement.');
      }
      if (usageAvailable && usageData && usageData.quota) {
        const quota = usageData.quota;
        const byteHeadroom = Number.isFinite(Number(quota.estimated_remaining_bytes)) ? `${formatBytes(Number(quota.tree_bytes || 0))} used; up to ${formatBytes(Number(quota.estimated_remaining_bytes))} estimated bytes remain.` : 'Byte headroom is unknown.';
        const inodeHeadroom = Number.isFinite(Number(quota.estimated_remaining_inodes)) ? ` ${Number(quota.tree_inodes || 0).toLocaleString()} inodes measured; up to ${Number(quota.estimated_remaining_inodes).toLocaleString()} estimated account inodes remain.` : '';
        if (Number.isFinite(Number(quota.estimated_remaining_bytes)) || inodeHeadroom) add('guidance', 'Measured filesystem usage and quota estimate', `${byteHeadroom}${inodeHeadroom} ${quota.enforced ? 'Kernel quota is marked enforced.' : 'Kernel enforcement is not confirmed.'} ${quota.scope_note || ''}`, 'Treat byte and inode headroom as upper bounds because other content owned by the hosting account can consume the same quota. Verify kernel quota usage before capacity changes.');
      }
      for (const key of ['Memory', 'Tasks']) {
        const current = Number(observed[`${key}Current`]);
        const maximum = Number(observed[`${key}Max`]);
        if (Number.isFinite(current) && Number.isFinite(maximum) && maximum > 0) {
          const ratio = current / maximum;
          if (ratio >= 0.9) add(ratio >= 1 ? 'critical' : 'warning', `Site ${key.toLowerCase()} limit ${ratio >= 1 ? 'reached' : 'near capacity'}`, `${key === 'Memory' ? formatBytes(current) : current.toLocaleString()} of ${key === 'Memory' ? formatBytes(maximum) : maximum.toLocaleString()} (${Math.min(100, Math.round(ratio * 100))}%) observed in the site systemd cgroup.`, "Review the site's measured workload and account plan. Check host headroom before changing an enforced limit.");
        }
      }
      const fpm = resourceData.fpm_status || {};
      const pools = Array.isArray(fpm.pools) ? fpm.pools : [];
      const livePools = pools.filter((pool) => pool.state === 'available');
      if (!fpm.available || livePools.length === 0) {
        add('unknown', 'Live PHP-FPM pool status is unavailable', fpm.detail || 'No current pool counters were returned.', 'Check the PHP-FPM service, managed pool configuration, and root-broker status probe.');
      } else {
        const saturated = livePools.filter((pool) => pool.saturated);
        if (saturated.length) {
          add('critical', 'PHP-FPM pool is currently saturated', saturated.map((pool) => `PHP ${pool.php_version || 'default'}: ${pool.active_processes}/${pool.max_children} active, queue ${pool.listen_queue}`).join('; '), 'Review slow requests and workload, then adjust the pool only after checking host memory headroom.');
        }
        const atCapacity = livePools.filter((pool) => pool.at_capacity && !pool.saturated);
        if (atCapacity.length) {
          add('warning', 'PHP-FPM pool is at its worker limit', atCapacity.map((pool) => `PHP ${pool.php_version || 'default'}: ${pool.active_processes}/${pool.max_children} active with no queued requests at the sample time.`).join('; '), 'Check nearby samples and request latency; reaching the worker limit alone does not prove requests are waiting.');
        }
        const historical = livePools.filter((pool) => Number(pool.max_children_reached) > 0);
        if (historical.length) {
          add('warning', 'PHP-FPM has reached its worker limit since pool start', historical.map((pool) => `PHP ${pool.php_version || 'default'}: ${pool.max_children_reached} limit event(s), ${pool.slow_requests || 0} slow request(s)`).join('; '), 'This counter is historical, not proof of current saturation. Correlate it with traffic and recent changes.');
        }
        if (fpm.warnings && fpm.warnings.length) {
          add('unknown', 'Some PHP-FPM pools could not be sampled', fpm.warnings.join('; '), 'Check the affected PHP-FPM pool and socket before drawing conclusions from the available versions.');
        }
        (Array.isArray(fpm.performance_guidance) ? fpm.performance_guidance : []).forEach((guidance) => {
          add('guidance', guidance.title,
            `${guidance.php_version ? `PHP ${guidance.php_version} · ` : ''}${guidance.evidence} ${guidance.recommendation}`,
            guidance.operator_approval_needed ? 'Review the evidence and approve any PHP worker change separately. StePanel has not changed the pool.' : 'No configuration change was applied.');
        });
        livePools.filter((pool) => pool.worker_memory).forEach((pool) => {
          const memory = pool.worker_memory;
          const maxObservedProjection = Number(memory.projected_max_rss_at_max_children_bytes)
            || Number(memory.max_rss_bytes || 0) * Number(pool.max_children || 0);
          add('guidance', `Measured PHP-FPM memory · PHP ${pool.php_version || 'default'}`,
            `${memory.sampled_workers} workers sampled: mean ${formatBytes(memory.mean_rss_bytes)}, P95 ${formatBytes(memory.p95_rss_bytes)}, maximum ${formatBytes(memory.max_rss_bytes)}. At max_children ${pool.max_children}, estimated pool RSS is ${formatBytes(memory.projected_rss_at_max_children_bytes)} at P95 and ${formatBytes(maxObservedProjection)} using the largest observed worker; one more worker adds about ${formatBytes(memory.one_additional_worker_bytes)} at sampled P95. ${memory.estimate || ''}`,
            `${memory.estimated_worker_ceiling ? `A conservative planning ceiling is about ${memory.estimated_worker_ceiling} workers using current host headroom; ${memory.worker_ceiling_basis || ''} ` : ''}Compare multiple peak-period samples before adjusting PHP workers. RSS includes shared pages and is not an enforced per-site memory limit.`);
        });
      }
    } else if (!resourceAvailable) {
      add('unknown', 'Resource enforcement could not be inspected', 'The site resource status endpoint was unavailable.', 'Check the host broker and resource reconciliation status.');
    }
    if (lastDeployment && ['failed', 'dead-letter'].includes(String(lastDeployment.state || '').toLowerCase())) {
      add('warning', 'Most recent deployment failed', `${lastDeployment.stage || 'Deployment'} · ${lastDeployment.state}`, 'Open deployment history and inspect the associated durable job before retrying.');
    }
    if (siteDiagnosticsAvailable && siteDiagnostics) {
      for (const storage of (siteDiagnostics.host_storage || [])) {
        if (storage.status === 'pressure') add('critical', `${storage.label || 'Host filesystem'} is under capacity pressure`, `${formatBytes(storage.available_bytes)} of ${formatBytes(storage.total_bytes)} bytes and ${(Number(storage.available_inodes) || 0).toLocaleString()} of ${(Number(storage.total_inodes) || 0).toLocaleString()} inodes are available. ${storage.detail || ''}`, 'Free or expand the affected filesystem using the host runbook. Check both bytes and inodes before starting restores or deployments.');
        else if (storage.status === 'unknown') add('unknown', `${storage.label || 'Host filesystem'} capacity not measured`, storage.detail || 'The filesystem capacity probe did not return measurements.', 'Check the configured storage path and panel permissions; do not infer capacity from site tree size.');
      }
      const siteDB = siteDiagnostics.database || {};
      if (!siteDB.available) {
        add('warning', 'Site database inventory could not be verified', siteDB.detail || 'The managed database probe did not complete.', 'Check the database service and helper status. After the engine is healthy, verify the application credentials with a read-only application health check.');
      } else if (!(siteDB.databases || []).length) {
        add('warning', 'No managed database is assigned to this site', `The ${siteDB.engine || 'database'} engine responded, but its inventory contains no database owned by this site.`, 'If this application needs a database, verify its intended database ownership and import state before changing wp-config.php.');
      }
      const dbConnection = siteDiagnostics.database_connectivity || {};
      if (dbConnection.status === 'unavailable') add('critical', 'Database engine connectivity probe failed', `${dbConnection.engine || 'Database'} diagnostic query did not complete. ${dbConnection.detail || ''} Probe time: ${dbConnection.collected_at ? new Date(dbConnection.collected_at).toLocaleString() : 'unknown'}.`, "Check database service state, socket/host configuration, and restricted helper logs. This probe does not test the site's own credentials.");
      else if (dbConnection.status === 'connected') add('guidance', 'Database engine connectivity verified', `${dbConnection.engine || 'Database'} answered the restricted host diagnostic query at ${dbConnection.collected_at ? new Date(dbConnection.collected_at).toLocaleString() : 'an unknown time'}. Application credentials and query latency are not established.`, 'If the application reports connection errors, validate its protected credential configuration and inspect database logs.');
      const appDB = siteDiagnostics.application_database_connectivity || {};
      if (appDB.status === 'failed') add('critical', 'WordPress database connection failed', `${appDB.detail || 'WP-CLI db check failed with the site-configured credentials.'} Probe time: ${appDB.collected_at ? new Date(appDB.collected_at).toLocaleString() : 'unknown'}.`, 'Verify the database host and managed username/password, confirm the target database exists, and rerun the read-only WordPress database check after correcting the cause.');
      else if (appDB.status === 'unknown') add('unknown', 'WordPress database connection not verified', `${appDB.detail || 'The application database probe did not complete.'} Probe time: ${appDB.collected_at ? new Date(appDB.collected_at).toLocaleString() : 'unknown'}.`, 'Check that WP-CLI and the site identity are available; retry diagnostics. This result is not evidence that application credentials work.');
      else if (appDB.status === 'connected') add('guidance', 'WordPress database connection verified', `${appDB.detail || 'WP-CLI db check succeeded.'} Probe time: ${appDB.collected_at ? new Date(appDB.collected_at).toLocaleString() : 'unknown'}.`, 'This confirms the configured credential path at the sample time; it does not measure ongoing database latency or application HTTP health.');
      const schedule = siteDiagnostics.backup_schedule || {};
      if (schedule.enabled) {
        const expectedMs = Number(schedule.interval_minutes) * 2 * 60 * 1000;
        const lastSuccess = schedule.last_success ? new Date(schedule.last_success) : null;
        const ageMs = lastSuccess && Number.isFinite(lastSuccess.getTime()) ? Date.now() - lastSuccess.getTime() : Infinity;
        if (Number(schedule.consecutive_failures) > 0) add('warning', 'Scheduled backups are failing', `${schedule.consecutive_failures} consecutive failure(s); last attempt ${schedule.last_run ? new Date(schedule.last_run).toLocaleString() : 'time not recorded'}; latest success ${lastSuccess ? lastSuccess.toLocaleString() : 'none recorded'}. ${schedule.has_error_detail ? 'Failure detail is retained in the authorized backup job history.' : 'No failure detail was retained.'}`, 'Inspect the latest backup job and destination capacity/connectivity, then confirm a successful backup and restore rehearsal.');
        if (!lastSuccess || (expectedMs > 0 && ageMs > expectedMs)) add('critical', 'Scheduled backup recovery point is stale', `Schedule interval ${schedule.interval_minutes} minutes; last successful backup ${lastSuccess ? `${Math.floor(ageMs / 3600000)} hours ago (${lastSuccess.toLocaleString()})` : 'has never succeeded'}. Stale threshold is twice the configured interval.`, 'Resolve the backup failure, produce a new verified backup, and run a restore rehearsal before treating this site as recoverable.');
      }
      if (recoveryAvailable && recovery && recovery.last_backup && recovery.last_backup.created_at && recovery.last_backup.verified_at) {
        add('guidance', 'Latest backup integrity evidence', `Backup ${recovery.last_backup.name || 'unnamed'} created ${new Date(recovery.last_backup.created_at).toLocaleString()}; verified ${new Date(recovery.last_backup.verified_at).toLocaleString()}.`, 'A verified archive does not by itself prove that the application can be restored; review the latest rehearsal result.');
      }
      const filesystem = siteDiagnostics.filesystem || {};
      if (filesystem.status === 'failed') add('critical', 'Site document root is unavailable', filesystem.detail, 'Restore the document root from a verified recovery point or correct its path through the managed site workflow. Do not run recursive chown as a first response.');
      else if (filesystem.status === 'observed') add('guidance', 'Filesystem permission evidence', `Document-root mode ${filesystem.mode}; owner UID ${filesystem.owner_uid ?? 'unknown'}, GID ${filesystem.owner_gid ?? 'unknown'}. ${filesystem.detail || ''}`, 'If logs show permission denied, compare these values with the configured web/PHP service identity and parent-directory traversal. Use the site permission repair workflow rather than broad recursive permission changes.');
      for (const cert of (siteDiagnostics.tls || [])) {
        if (cert.renewal === 'failed' || cert.renewal === 'dead-letter') add('critical', `Certificate renewal failed for ${cert.domain}`, `${cert.renewal_evidence || 'A certificate job failed.'} Started ${cert.renewal_at ? new Date(cert.renewal_at).toLocaleString() : 'at an unknown time'}.`, 'Open the certificate job for its diagnostic output. Check DNS validation, port 80/443 reachability, and web-server configuration before retrying.');
        else if (cert.renewal === 'unknown') add('unknown', `Certificate renewal history unavailable for ${cert.domain}`, cert.renewal_evidence || 'The durable certificate job history could not be checked.', 'Check control-plane database health and retry diagnostics; do not interpret missing job evidence as a successful renewal.');
        if (['expired', 'expiring', 'missing', 'hostname_mismatch', 'failed'].includes(cert.status)) {
          add(cert.status === 'expired' || cert.status === 'hostname_mismatch' ? 'critical' : 'warning', `TLS ${cert.status.replace('_', ' ')} for ${cert.domain}`, `${cert.detail || ''}${cert.not_after ? ` Expires ${new Date(cert.not_after).toLocaleString()}.` : ''}`, 'Confirm DNS points to this host and the route is applied, then use the certificate workflow to renew or issue the certificate. Avoid deleting certificate files manually.');
        } else if (cert.status === 'unknown') add('unknown', `TLS status not verified for ${cert.domain}`, cert.detail || 'Certificate status is unavailable.', 'Check the live site over HTTPS from an external probe; this panel does not claim a successful handshake from local certificate metadata.');
      }
    } else {
      add('unknown', 'Site-specific host diagnostics are unavailable', 'Database ownership, document-root metadata, and certificate evidence could not be collected.', 'Check the panel service and managed database helper, then refresh this snapshot.');
    }
    if (databaseDiagnosticsAvailable && databaseDiagnostics && databaseDiagnostics.available) {
      const values = databaseDiagnostics.values || {};
      if (Number(values.blocked_sessions) > 0 || Number(values.long_transactions) > 0) {
        add('warning', 'Database contention is present on this host', `${values.blocked_sessions || 0} blocked session(s); ${values.long_transactions || 0} transaction(s) older than five minutes. This is host-wide evidence, not attribution to this site.`, 'Review database sessions and correlate their database names and start times with this site’s traffic.');
      }
    } else if (databaseDiagnosticsAvailable === false) {
      add('unknown', 'Database diagnostics are unavailable', 'The host database diagnostic probe did not return usable measurements.', 'Check database service health and its restricted diagnostic helper.');
    }
    const excerpts = [];
    const safeExcerpt = (line) => String(line)
      .replace(/((?:password|passwd|token|secret|api[_-]?key)\s*[=:]\s*)[^\s&,;]+/ig, '$1[redacted]')
      .replace(/(authorization\s*[=:]\s*)[^,;]+/ig, '$1[redacted]')
      .slice(0, 260);
    const inspectLog = (result, label, saturation) => {
      if (!canReadLogs) return;
      if (!result || !result.ok) {
        add('unknown', `${label} log signal unavailable`, 'The bounded recent-log request did not complete.', 'Open Logs to inspect availability and retry.');
        return;
      }
      const lines = result.value.lines || [];
      const pattern = /\b(error|critical|crit|fatal|emerg|failed|denied|no space|timed? ?out|refused|unavailable|upstream|database connection|certificate|ssl|tls)\b|max_children|reached pm\.max_children|listen queue/i;
      const matches = lines.filter((line) => pattern.test(line));
      if (matches.length) {
        const evidence = matches.slice(-4).map(safeExcerpt);
        excerpts.push({ label, lines: evidence });
        const joined = matches.join('\n').toLowerCase();
        const pressure = saturation && /max_children|listen queue/.test(joined);
        let next = 'Compare the log timestamps with the latest configuration change and request failures; inspect the full bounded log tail before applying a change.';
        if (/no space|disk quota|quota exceeded|inode/.test(joined)) next = 'Check filesystem free bytes, inodes, and this site’s enforced quota. Free or archive known data safely; do not delete files based only on log messages.';
        else if (/permission denied|access denied/.test(joined)) next = 'Verify document-root ownership, directory traversal bits, ACLs, and the PHP/web service identity. Apply a narrow site-level permission repair; avoid recursive world-writable changes.';
        else if (/could not connect|connection refused|sqlstate|error establishing a database connection|too many connections/.test(joined)) next = 'Check database service availability and site database ownership/credentials. Test with the application’s protected credential path; never put passwords in a command line or diagnostic report.';
        else if (/certificate|ssl|tls|handshake/.test(joined)) next = 'Check route application, DNS, certificate hostname and expiry, then validate the live HTTPS handshake before renewing or changing TLS settings.';
        else if (/upstream|connect\(\) to unix|php-fpm|primary script unknown/.test(joined)) next = 'Check PHP-FPM pool state, socket path/permissions, and the selected PHP runtime. Validate the pool configuration before restarting the service.';
        add(pressure ? 'warning' : 'critical', pressure ? 'PHP-FPM log reports worker pressure' : `${label} log contains actionable errors`, `${matches.length} relevant line(s) in the latest ${lines.length}. Excerpts below preserve the observed message.`, next);
      }
    };
    inspectLog(webLogResult, 'Web-server', false);
    inspectLog(phpLogResult, 'PHP-FPM', true);
    if (accessLogResult && accessLogResult.ok) {
      const failures = (accessLogResult.value.lines || []).filter((line) => /"\s*[45]\d\d\s/.test(line));
      if (failures.length) excerpts.push({ label: 'Recent HTTP 4xx/5xx requests', lines: failures.slice(-4).map(safeExcerpt) });
    }
    const unknowns = ['Application-level database login and query latency are not tested with the customer’s credentials. The database inventory confirms helper access and ownership only.'];
    if (!canReadLogs) unknowns.push('Recent web-server and PHP-FPM logs are hidden because this account lacks logs:read.');
    if (!resourceAvailable || !resourceData || !resourceData.fpm_status || !resourceData.fpm_status.available) unknowns.push('Live PHP-FPM counters are unavailable unless the installed root broker can safely query a managed pool.');
    const levelLabel = { critical: 'Action needed', warning: 'Review', unknown: 'Not verified', guidance: 'Performance guidance' };
    return el('section', { className: 'incident-assessment', 'aria-label': 'Site incident assessment' }, [
      el('div', { className: 'section-heading' }, [el('h3', {}, 'Why this site may be failing'), el('small', {}, `Snapshot · ${siteDiagnostics && siteDiagnostics.collected_at ? new Date(siteDiagnostics.collected_at).toLocaleString() : new Date().toLocaleTimeString()}`)]),
      findings.length ? el('ul', { className: 'incident-findings' }, findings.map((finding) => el('li', { className: `incident-finding incident-${finding.level}` }, [
        el('strong', {}, `${levelLabel[finding.level]} · ${finding.title}`),
        el('p', {}, finding.evidence),
        el('small', {}, finding.next),
      ]))) : el('p', { className: 'incident-clear' }, 'No failing signal was found in the available snapshot. This does not establish application health by itself.'),
      excerpts.length ? el('div', { className: 'incident-evidence' }, [
        el('h4', {}, 'Relevant log evidence'),
        ...excerpts.map((group) => el('section', {}, [el('strong', {}, group.label), el('pre', {}, group.lines.join('\n'))])),
      ]) : null,
      latestChange ? el('div', { className: 'incident-recent-change' }, [
        el('strong', {}, 'Most recent recorded change'),
        el('span', {}, `${latestChange.action || 'Change'} · ${latestChange.detail || 'No additional detail recorded'}`),
        el('small', {}, latestChange.time ? new Date(latestChange.time).toLocaleString() : 'Timestamp unavailable'),
      ]) : null,
      el('details', { className: 'incident-limits' }, [
        el('summary', {}, 'Signals not established by this snapshot'),
        el('ul', {}, unknowns.map((item) => el('li', {}, item))),
      ]),
    ]);
  }

  // ---------------------------------------------------------------------
  // Domains tab
  // ---------------------------------------------------------------------

  function vhostConfigName(site, domain) {
    const extension = webserver === 'caddy' ? '.caddy' : '.conf';
    return `site-${site}-${domain.toLowerCase().replace(/\./g, '_')}${extension}`;
  }

  async function renderDomainsTab(site, panel, ctx) {
    const overview = await ctx.getJSON(`/api/sites/overview/${encodeURIComponent(site)}`);
    const routes = overview.routes || [];
    const output = ctx.statusOutput();

    const previewAndConnect = async (domain) => {
      const preview = await ctx.postJSON('/api/sites/deploy', { site, domain });
      const confirmed = await ctx.confirmDangerous({
        title: `Review domain change for ${domain}`,
        message: 'This writes a virtual-host configuration and reloads the web server. DNS is not changed by StePanel.',
        facts: [
          ['Site', site],
          ['Domain', domain],
          ['Vhost file', preview.proposed.vhost_path],
          ['Effects', preview.effects.join(' ')],
          ['Recovery', preview.recovery_plan.join(' ')],
          ['Preview expires', new Date(preview.expires_at).toLocaleString()],
        ],
        confirmText: domain,
        actionLabel: 'Apply domain change',
      });
      if (!confirmed) return false;
      return await ctx.postJSON('/api/sites/deploy', { site, domain, preview_token: preview.preview_token });
    };

    const list = el('ul', { className: 'resource-list' }, routes.length ? routes.map((route) => el('li', { className: 'resource-list-item' }, [
      el('div', { className: 'item-meta' }, [el('strong', {}, route.domain), el('small', {}, 'Active route')]),
      ctx.can('site:deploy') ? el('div', { className: 'item-actions' }, [ctx.button('Remove', async () => {
        try {
          const name = vhostConfigName(site, route.domain);
          const preview = await ctx.postJSON('/api/sites/route-delete-preview', { name });
          const confirmed = await ctx.confirmDangerous({
            title: `Review removal of ${route.domain}`,
            message: 'The route will stop serving this domain and the web server will reload. DNS, site files, databases, and mail are not changed.',
            facts: [
              ['Domain', route.domain], ['Site', site],
              ['Vhost file', preview.proposed.vhost_path],
              ['Effects', preview.effects.join(' ')],
              ['Recovery', preview.recovery_plan.join(' ')],
              ['Preview expires', new Date(preview.expires_at).toLocaleString()],
            ],
            confirmText: route.domain,
            actionLabel: 'Remove domain route',
          });
          if (!confirmed) return;
          const result = await ctx.deleteJSON(`/api/sites/${encodeURIComponent(name)}`, { preview_token: preview.preview_token });
          output.textContent = `${route.domain} removed.${result.recovery_snapshot ? ` Recovery snapshot: ${result.recovery_snapshot}` : ''}`;
          renderDomainsTab(site, panel, ctx);
        } catch (error) { output.textContent = error.message; }
      }, { className: 'danger' })]) : null,
    ])) : [el('p', { className: 'empty-state' }, 'No domains connected yet.')]);

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Connect a domain to this site. Point its DNS A/AAAA record at this server; StePanel does not manage DNS zones.'),
      list,
    );

    if (ctx.isAdministrator) {
      const domainInput = field({ label: 'Domain', name: 'domain', placeholder: 'www.example.com', required: true });
      panel.append(el('form', {
        className: 'import-form', onSubmit: async (event) => {
          event.preventDefault();
          const domain = domainInput.querySelector('input').value.trim();
          output.textContent = 'Adding domain route…';
          try {
            const result = await previewAndConnect(domain);
            if (!result) { output.textContent = 'Domain change cancelled.'; return; }
            output.textContent = `Route added.${result.recovery_snapshot ? ` Recovery snapshot: ${result.recovery_snapshot}` : ''}`;
            renderDomainsTab(site, panel, ctx);
          } catch (error) { output.textContent = error.message; }
        },
      }, [domainInput, el('button', { type: 'submit', className: 'primary-action' }, 'Add domain'), output]));
      return;
    }

    if (!ctx.can('site:deploy')) {
      panel.append(el('p', { className: 'import-note' }, 'Your role can review domains but cannot change routes. Ask a tenant owner or manager to make this change.'));
      return;
    }

    // Customers must prove domain ownership with a DNS TXT record before a
    // route can activate, matching the server-side enforcement in siteDeploy.
    let claim = null;
    const claimOutput = el('div', {});
    const domainInput = field({ label: 'Domain', name: 'domain', placeholder: 'www.example.com', required: true, hint: "You'll need to add a DNS TXT record to prove you control it." });
    const claimButton = el('button', { type: 'submit', className: 'primary-action' }, 'Start domain verification');
    const claimForm = el('form', {
      className: 'import-form', onSubmit: async (event) => {
        event.preventDefault();
        const domain = domainInput.querySelector('input').value.trim();
        output.textContent = 'Requesting a verification token…';
        try {
          claim = await ctx.postJSON('/api/sites/domains/claim', { site, domain });
          claimOutput.replaceChildren(
            el('div', { className: 'confirm-dialog-facts' }, [
              el('div', {}, ['Add a TXT record named', el('strong', {}, claim.txt_name)]),
              el('div', {}, ['with the value', el('strong', {}, claim.txt_value)]),
            ]),
            el('button', {
              type: 'button', className: 'primary-action', onClick: async () => {
                output.textContent = 'Checking DNS…';
                try {
                  await ctx.postJSON('/api/sites/domains/verify', { site, domain: claim.domain });
                  const result = await previewAndConnect(claim.domain);
                  if (!result) { output.textContent = 'Domain route change cancelled.'; return; }
                  output.textContent = `Domain verified and route added.${result.recovery_snapshot ? ` Recovery snapshot: ${result.recovery_snapshot}` : ''}`;
                  renderDomainsTab(site, panel, ctx);
                } catch (error) { output.textContent = error.message; }
              },
            }, "I've added the record — verify and connect"),
          );
          output.textContent = '';
        } catch (error) { output.textContent = error.message; }
      },
    }, [domainInput, claimButton, claimOutput, output]);
    panel.append(claimForm);
  }

  // ---------------------------------------------------------------------
  // Runtime tab (PHP, Node, Python, Composer)
  // ---------------------------------------------------------------------

  async function renderRuntimeTab(site, panel, ctx) {
    panel.replaceChildren(el('p', { className: 'panel-intro' }, 'The application runtime serving this site.'));

    // PHP
    const php = await ctx.getJSON(`/api/sites/php/${encodeURIComponent(site)}`).catch((error) => ({ __error: error }));
    const phpOutput = ctx.statusOutput();
    if (php.__error) {
      panel.append(errorState('PHP runtime status', php.__error));
    } else if (php) {
      const versionOptions = (php.versions && php.versions.length ? php.versions : ['8.3']).map((v) => ({ value: v, label: `PHP ${v}` }));
      const current = php.configured ? php.profile : { version: versionOptions[0]?.value || '8.3', memory_limit: '256M', max_execution_time: 30, upload_max_filesize: '64M', post_max_size: '64M', max_input_vars: 3000, opcache: true, display_errors: false, error_reporting: 'E_ALL & ~E_DEPRECATED' };
      const versionField = field({ label: 'Version', tag: 'select', name: 'version', value: current.version, options: versionOptions });
      const memoryField = field({ label: 'Memory limit', name: 'memory_limit', value: current.memory_limit, pattern: '[1-9][0-9]{0,4}M', hint: 'e.g. 256M' });
      const timeoutField = field({ label: 'Max execution seconds', name: 'max_execution_time', type: 'number', value: current.max_execution_time, min: 1, max: 3600 });
      const uploadField = field({ label: 'Upload max filesize', name: 'upload_max_filesize', value: current.upload_max_filesize, pattern: '[1-9][0-9]{0,4}M' });
      const opcacheField = field({ label: 'OPcache enabled', tag: 'checkbox-field', name: 'opcache', checked: current.opcache });
      const form = el('form', {
        className: 'import-form', onSubmit: async (event) => {
          event.preventDefault();
          phpOutput.textContent = 'Preparing PHP change preview…';
          try {
            const change = {
              version: versionField.querySelector('select').value,
              memory_limit: memoryField.querySelector('input').value,
              max_execution_time: Number(timeoutField.querySelector('input').value),
              upload_max_filesize: uploadField.querySelector('input').value,
              post_max_size: current.post_max_size,
              max_input_vars: current.max_input_vars,
              opcache: opcacheField.querySelector('input').checked,
              display_errors: current.display_errors,
              error_reporting: current.error_reporting,
            };
            const preview = await ctx.postJSON(`/api/sites/php/${encodeURIComponent(site)}`, change);
            const confirmed = await ctx.confirmDangerous({
              title: 'Review PHP runtime change',
              message: 'StePanel will update the PHP-FPM pool and reload its service. Requests may briefly fail during activation.',
              facts: [
                ['Current PHP', preview.before.configured ? `PHP ${preview.before.profile.version}` : 'No saved profile'],
                ['Proposed PHP', `PHP ${preview.proposed.version}`],
              ['Profile changes', `memory ${preview.proposed.memory_limit}; execution ${preview.proposed.max_execution_time}s; upload ${preview.proposed.upload_max_filesize}; post ${preview.proposed.post_max_size}; input vars ${preview.proposed.max_input_vars}; OPcache ${preview.proposed.opcache ? 'on' : 'off'}; display errors ${preview.proposed.display_errors ? 'on' : 'off'}; reporting ${preview.proposed.error_reporting}`],
              ['Host changes', preview.effects.join(' ')],
                ['Recovery', preview.recovery_plan.join(' ')],
                ['Preview expires', new Date(preview.expires_at).toLocaleString()],
              ],
              confirmText: site,
              actionLabel: 'Apply PHP change',
            });
            if (!confirmed) { phpOutput.textContent = 'PHP change cancelled.'; return; }
            await ctx.putJSON(`/api/sites/php/${encodeURIComponent(site)}`, { ...change, preview_token: preview.preview_token });
            phpOutput.textContent = 'PHP runtime applied.';
          } catch (error) { phpOutput.textContent = error.message; }
        },
      }, [
        el('h4', {}, 'PHP'),
        el('div', { className: 'field-row' }, [versionField, memoryField, timeoutField, uploadField]),
        opcacheField,
        el('button', { type: 'submit', className: 'primary-action' }, 'Apply PHP runtime'),
        phpOutput,
      ]);
      if (!ctx.can('site:deploy')) form.querySelectorAll('input, select, button').forEach((node) => { node.disabled = true; });
      panel.append(form);
    }

    // Node application status (read-only here; initial deployment happens
    // from the administrator "Deploy a Node app" panel).
    const overview = await ctx.getJSON(`/api/sites/overview/${encodeURIComponent(site)}`).catch((error) => ({ __error: error, applications: [] }));
    const nodeApp = (overview.applications || [])[0];
    panel.append(el('h4', {}, 'Node.js'), overview.__error ? errorState('Node application status', overview.__error) : nodeApp
      ? el('div', { className: 'overview-grid' }, [el('article', { className: 'overview-stat' }, [
        el('span', { className: 'stat-label' }, 'Application'),
        el('strong', {}, `Node ${nodeApp.node_version}`),
        el('small', {}, `${nodeApp.domain} · ${nodeApp.state}`),
      ])])
      : el('p', { className: 'empty-state' }, ctx.isAdministrator ? 'No Node application deployed. Use "Deploy a Node app" below to add one.' : 'No Node application deployed for this site.'));

    if (nodeApp) {
      const toolingOutput = ctx.statusOutput();
      panel.append(el('div', { className: 'workspace-panel-actions' }, ctx.can('site:deploy') ? [
        ctx.button('npm install', async () => { toolingOutput.textContent = 'Installing dependencies…'; try { await ctx.runOperation('/api/node/tooling', { site, action: 'install', package_manager: 'npm' }); toolingOutput.textContent = 'Dependencies installed.'; } catch (error) { toolingOutput.textContent = error.message; } }),
        ctx.button('npm run build', async () => { toolingOutput.textContent = 'Building…'; try { await ctx.runOperation('/api/node/tooling', { site, action: 'build', package_manager: 'npm' }); toolingOutput.textContent = 'Build completed.'; } catch (error) { toolingOutput.textContent = error.message; } }),
      ] : [el('span', { className: 'import-note' }, 'Your role can view this application but cannot run build commands.')]), toolingOutput);
    }

    // Python
    const pythonOutput = ctx.statusOutput();
    const pyManifest = { version: '3.13', entrypoint: 'app:app', port: 8001, workers: 2 };
    const pyForm = el('form', {
      className: 'import-form', onSubmit: async (event) => {
        event.preventDefault();
        const data = Object.fromEntries(new FormData(pyForm));
        pythonOutput.textContent = 'Deploying Python application…';
        try {
          await ctx.runOperation('/api/python/deploy', { site, version: data.version, entrypoint: data.entrypoint, port: Number(data.port), workers: Number(data.workers) });
          pythonOutput.textContent = 'Python application deployed.';
        } catch (error) { pythonOutput.textContent = error.message; }
      },
    }, [
      el('h4', {}, 'Python'),
      el('div', { className: 'field-row' }, [
        field({ label: 'Version', tag: 'select', name: 'version', value: pyManifest.version, options: [{ value: '3.12', label: 'Python 3.12' }, { value: '3.13', label: 'Python 3.13' }] }),
        field({ label: 'Entrypoint (module:app)', name: 'entrypoint', value: pyManifest.entrypoint, required: true }),
        field({ label: 'Port', name: 'port', type: 'number', value: pyManifest.port, min: 1024, max: 65535 }),
        field({ label: 'Gunicorn workers', name: 'workers', type: 'number', value: pyManifest.workers, min: 1, max: 64 }),
      ]),
      el('div', { className: 'workspace-panel-actions' }, [
        el('button', { type: 'submit', className: 'primary-action' }, 'Deploy / redeploy'),
        ctx.button('Restart', async () => { pythonOutput.textContent = 'Restarting…'; try { await ctx.postJSON(`/api/python/${encodeURIComponent(site)}/restart`); pythonOutput.textContent = 'Restarted.'; } catch (error) { pythonOutput.textContent = error.message; } }),
        ctx.button('Stop', async () => { pythonOutput.textContent = 'Stopping…'; try { await ctx.postJSON(`/api/python/${encodeURIComponent(site)}/stop`); pythonOutput.textContent = 'Stopped.'; } catch (error) { pythonOutput.textContent = error.message; } }),
      ]),
      pythonOutput,
    ]);
    if (!ctx.can('site:deploy')) pyForm.querySelectorAll('input, select, button').forEach((node) => { node.disabled = true; });
    panel.append(pyForm);

    // Composer
    const composerOutput = ctx.statusOutput();
    panel.append(el('h4', {}, 'Composer'), el('div', { className: 'workspace-panel-actions' }, ctx.can('site:deploy') ? [
      ctx.button('Run composer install', async () => {
        composerOutput.textContent = 'Running composer install…';
        try { await ctx.runOperation(`/api/composer/${encodeURIComponent(site)}/install`, { development: false, optimize_autoloader: true }); composerOutput.textContent = 'Composer install completed.'; } catch (error) { composerOutput.textContent = error.message; }
      }),
      ] : [el('span', { className: 'import-note' }, 'Your role can view this site but cannot run Composer operations.')]), composerOutput);
  }

  // ---------------------------------------------------------------------
  // Deployments tab
  // ---------------------------------------------------------------------

  async function renderDeploymentsTab(site, panel, ctx) {
    const [history, keyStatus] = await Promise.all([
      ctx.getJSON(`/api/deployments?site=${encodeURIComponent(site)}`).catch((error) => ({ __error: error, deployments: [] })),
      ctx.getJSON(`/api/sites/git-key/${encodeURIComponent(site)}`).catch((error) => ({ __error: error, configured: false })),
    ]);
    const deployments = (history.deployments || []).slice().sort((a, b) => new Date(b.created_at) - new Date(a.created_at)).slice(0, 20);

    const output = ctx.statusOutput();
    const keyOutput = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Deploy from a Git repository, review release history, and roll back if a release breaks the site.'),
      el('h4', {}, 'Deploy key'),
      keyStatus.__error ? errorState('Deploy key status', keyStatus.__error) : keyStatus.configured
        ? el('div', {}, [el('p', { className: 'import-note' }, 'A deploy key is configured for this site.'), ctx.can('ssh:write') ? ctx.button('Retire deploy key', async () => {
          keyOutput.textContent = 'Retiring…';
          try { await ctx.deleteJSON(`/api/sites/git-key/${encodeURIComponent(site)}`); keyOutput.textContent = 'Deploy key retired.'; renderDeploymentsTab(site, panel, ctx); } catch (error) { keyOutput.textContent = error.message; }
        }) : el('span', { className: 'import-note' }, 'Your role can view deployment history but cannot manage deploy keys.')])
        : el('div', {}, [ctx.can('ssh:write') ? ctx.button('Generate deploy key', async () => {
          keyOutput.textContent = 'Generating…';
          try {
            const key = await ctx.postJSON(`/api/sites/git-key/${encodeURIComponent(site)}`);
            const copyStatus = ctx.statusOutput();
            keyOutput.replaceChildren(
              el('p', {}, 'Add this public key to your Git provider as a read-only deploy key:'),
              el('pre', { className: 'log-viewer' }, key.public_key),
              el('div', { className: 'workspace-panel-actions' }, [ctx.button('Copy to clipboard', async () => {
                if (!navigator.clipboard) { copyStatus.textContent = 'Clipboard access is unavailable here — select the text above manually.'; return; }
                try { await navigator.clipboard.writeText(key.public_key); copyStatus.textContent = 'Copied.'; } catch (error) { copyStatus.textContent = 'Could not copy automatically — select the text above manually.'; }
              })]),
              copyStatus,
            );
          } catch (error) { keyOutput.textContent = error.message; }
        }) : el('span', { className: 'import-note' }, 'Your role can view deployment history but cannot manage deploy keys.')]),
      keyOutput,
    );

    if (ctx.isAdministrator || ctx.can('deploy:write')) {
      const repoField = field({ label: 'Repository', name: 'repository', placeholder: 'git@github.com:org/repo.git or https://…', required: true });
      const refField = field({ label: 'Branch or tag', name: 'ref', value: 'main', placeholder: 'main' });
      const deployForm = el('form', {
        className: 'import-form', onSubmit: async (event) => {
          event.preventDefault();
          const repository = repoField.querySelector('input').value.trim();
          const ref = refField.querySelector('input').value.trim() || 'main';
          output.textContent = 'Deploying…';
          try {
            await ctx.runOperation('/api/sites/git-deploy', { site, repository, ref });
            output.textContent = 'Deployment completed.';
            renderDeploymentsTab(site, panel, ctx);
          } catch (error) { output.textContent = error.message; }
        },
      }, [el('h4', {}, 'Deploy from Git'), repoField, refField, el('button', { type: 'submit', className: 'primary-action' }, 'Deploy'), output]);
      panel.append(deployForm);

      if (ctx.isAdministrator) panel.append(el('h4', {}, 'Roll back'), ctx.button('Preview rollback', async () => {
        output.textContent = 'Loading rollback preview…';
        try {
          const preview = await ctx.getJSON(`/api/sites/git-rollback/preview?site=${encodeURIComponent(site)}`);
          const formatBytes = (bytes) => `${(bytes / (1024 * 1024)).toFixed(1)} MiB`;
          const previewMessage = [
            `Current release: ${preview.current_release} (${preview.current_files} files, ${formatBytes(preview.current_bytes)})`,
            `Target release: ${preview.target_release} (${preview.target_files} files, ${formatBytes(preview.target_bytes)})`,
            `Filesystem change: replace ${preview.document_root} with ${preview.target_release}.`,
            preview.service_effect,
            preview.preserved_release,
          ].join('\n\n');
          const confirmed = await ctx.confirmDangerous({
            title: `Roll back ${site}?`,
            message: previewMessage,
            confirmText: `ROLLBACK ${site}`,
            actionLabel: 'Roll back',
          });
          if (!confirmed) { output.textContent = 'Rollback cancelled.'; return; }
          output.textContent = 'Rolling back…';
          await ctx.postJSON('/api/sites/git-rollback', { site, confirm: `ROLLBACK ${site}`, preview_token: preview.preview_token });
          output.textContent = 'Rollback completed.';
          renderDeploymentsTab(site, panel, ctx);
        } catch (error) { output.textContent = error.message; }
      }, { className: 'danger' }));
    }

    panel.append(
      el('h4', {}, 'Release history'),
      history.__error ? errorState('Deployment history', history.__error) : deployments.length
        ? el('ul', { className: 'resource-list' }, deployments.map((item) => el('li', { className: 'resource-list-item' }, [
          el('div', { className: 'item-meta' }, [el('strong', {}, `${item.stage} · ${item.state}`), el('small', {}, [item.repository, item.ref, item.commit ? item.commit.slice(0, 10) : ''].filter(Boolean).join(' · '))]),
          el('small', {}, ctx.formatAge(item.created_at)),
        ]))) : el('p', { className: 'empty-state' }, 'No deployments recorded yet.'),
    );
  }

  // ---------------------------------------------------------------------
  // Databases tab (site-scoped view of the managed database lifecycle)
  // ---------------------------------------------------------------------

  async function renderDatabasesTab(site, panel, ctx) {
    const all = await ctx.getJSON('/api/databases').catch((error) => ({ __error: error, databases: [] }));
    const owned = (all.databases || []).filter((db) => db.site === site);
    const output = ctx.statusOutput();
    const databaseList = all.__error ? errorState('Database inventory', all.__error) : el('ul', { className: 'resource-list' }, owned.length ? owned.map((db) => el('li', { className: 'resource-list-item' }, [
      el('div', { className: 'item-meta' }, [el('strong', {}, db.name), el('small', {}, `${ctx.formatBytes(db.bytes)}${db.user ? ` · user ${db.user}` : ''}`)]),
      ctx.can('database:write') ? el('div', { className: 'item-actions' }, [
        ctx.button('Delete', async () => {
          output.textContent = 'Loading deletion preview…';
          let preview;
          try {
            preview = await ctx.getJSON(`/api/databases/${encodeURIComponent(db.name)}/deletion-preview`);
          } catch (error) {
            output.textContent = error.message;
            return;
          }
          const confirmed = await ctx.confirmDangerous({
            title: `Delete ${db.name}?`,
            message: [preview.database_effect, preview.backup_effect, preview.service_effect].join('\n\n'),
            facts: [['Database', preview.database.name], ['Engine', preview.engine], ['Size', ctx.formatBytes(preview.database.bytes)], ['Owning site', preview.database.site], ['Database user', preview.database.user]],
            confirmText: `DROP ${db.name}`,
            actionLabel: 'Delete database',
          });
          if (!confirmed) return;
          output.textContent = 'Deleting…';
          try {
            await api.request(`/api/databases/${encodeURIComponent(db.name)}`, { method: 'DELETE', json: { user: db.user, confirm: `DROP ${db.name}`, preview_token: preview.preview_token } });
            output.textContent = 'Database deleted.';
            renderDatabasesTab(site, panel, ctx);
          } catch (error) { output.textContent = error.message; }
        }, { className: 'danger' }),
      ]) : null,
    ])) : el('p', { className: 'empty-state' }, 'No databases for this site yet.'));

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, "This site's managed databases. StePanel never displays stored passwords or runs arbitrary SQL." ),
      databaseList,
      output,
    );

    const nameField = field({ label: 'Database name', name: 'name', pattern: '[a-z0-9_]{1,63}', required: true });
    const userField = field({ label: 'Database user', name: 'user', pattern: '[a-z][a-z0-9_]{0,31}', required: true, hint: 'Must start with a lowercase letter.' });
    const passwordField = withGenerateButton(field({ label: 'Password', name: 'password', type: 'password', minlength: 20, required: true, hint: '20+ characters.' }));
    const createOutput = ctx.statusOutput();
    const createForm = el('form', {
      className: 'import-form', onSubmit: async (event) => {
        event.preventDefault();
        createOutput.textContent = 'Creating…';
        try {
          await ctx.postJSON('/api/databases', {
            name: nameField.querySelector('input').value,
            user: userField.querySelector('input').value,
            site,
            password: passwordField.querySelector('input').value,
          });
          createOutput.textContent = 'Database created.';
          renderDatabasesTab(site, panel, ctx);
        } catch (error) { createOutput.textContent = error.message; }
      },
    }, [nameField, userField, passwordField, el('button', { type: 'submit', className: 'primary-action' }, 'Create database'), createOutput]);
    panel.append(...(ctx.can('database:write')
      ? [el('h4', {}, 'Create a database'), createForm]
      : [el('p', { className: 'import-note' }, 'Your role can inspect databases but cannot create or delete them.')]));
  }

  // ---------------------------------------------------------------------
  // Recovery status (shared by the Overview and Backups tabs)
  // ---------------------------------------------------------------------

  const confidenceBadges = {
    verified: ['Verified', 'ok'],
    degraded: ['Degraded', 'warn'],
    unverified: ['Unverified', 'warn'],
    failing: ['Failing', 'danger'],
    no_backup: ['No backup', 'danger'],
  };
  const offsiteLabels = {
    verified: 'Upload confirmed',
    pending: 'Not yet uploaded',
    untracked: 'Not tracked',
    not_configured: 'Not configured',
  };

  const formatDuration = (ms) => {
    if ((ms || 0) < 1000) return 'Under 1s';
    const seconds = Math.round(ms / 1000);
    if (seconds < 60) return `${seconds}s`;
    return `${Math.floor(seconds / 60)}m ${seconds % 60}s`;
  };

  const recoveryBadge = (status, ctx) => {
    const [label, tone] = confidenceBadges[status.confidence] || ['Unknown', 'off'];
    return ctx.badge(label, tone);
  };

  const recoveryProcedure = (status) => {
    const backup = status.last_backup;
    if (!backup) return [
      'No recovery point is available. Create a verified backup that includes the site files and required database dumps.',
      'Configure and verify an offsite destination before relying on this host as the only copy.',
      'Run a restore rehearsal and an application HTTP check before declaring the site recoverable.',
    ];
    const passed = status.last_passed_rehearsal;
    const latestBackupProven = Boolean(passed && passed.backup === backup.name);
    const applicationProvenForLatest = latestBackupProven && passed.level === 'application';
    const steps = [
      `Select recovery point ${backup.name}, created ${new Date(backup.created_at).toLocaleString()}.`,
      status.offsite?.state === 'verified'
        ? 'Retrieve the confirmed offsite copy; retain the local copy until recovery is verified.'
        : 'Use the local backup. No confirmed offsite copy is recorded for the newest recovery point.',
      backup.integrity === 'verified'
        ? 'Verify the signed manifest and archive checksums, then decrypt with the configured backup key if encrypted.'
        : 'Validate the unsigned backup manifest and archive carefully; its authenticity is not established by a signature.',
      'Restore the site files and import the required database dump into the destination database service.',
      applicationProvenForLatest
        ? 'Recreate the tested PHP-FPM and virtual-host configuration, then run the application HTTP probe.'
        : 'Recreate PHP-FPM and virtual-host configuration, verify database credentials, and run an application HTTP probe; this application recovery has not been proven by the latest passing rehearsal.',
      'Review DNS, mail routing, cron commands, and external integrations before cutover; these may require separate migration steps.',
    ];
    return steps;
  };

  const recoveryEstimate = (status) => {
    const passed = status.last_passed_rehearsal;
    if (!passed) return 'No successful rehearsal is available to estimate recovery time.';
    const duration = formatDuration(status.measured_recovery_ms);
    if (passed.level === 'application' && passed.backup === status.last_backup?.name) return `Latest recovery point passed application-level proof in ${duration} (${ctxAgeLabel(passed.at)}). This is an observed run, not an RTO guarantee.`;
    if (passed.level === 'application') return `A previous backup passed application-level proof in ${duration} (${ctxAgeLabel(passed.at)}), but the latest recovery point differs; its full-site recovery time is unknown.`;
    return `Last archive-level rehearsal: ${duration}. This excludes database import, service activation, and application HTTP verification, so a full-site recovery estimate is unknown.`;
  };

  const ctxAgeLabel = (timestamp) => {
    const date = new Date(timestamp);
    return Number.isNaN(date.getTime()) ? 'time unavailable' : date.toLocaleString();
  };

  const automaticRehearsalNote = (status) => {
    if (!status.automatic_rehearsals_enabled) return 'Automatic rehearsals are turned off on this server.';
    if (!status.scheduled_backups) return 'Automatic rehearsals start once scheduled backups are enabled for this site.';
    return `Rehearsed automatically after scheduled backups, at most every ${status.rehearsal_interval_hours}h.`;
  };

  function renderRecoveryStatus(status, ctx) {
    const last = status.last_backup;
    const latest = status.last_rehearsal;
    const passed = status.last_passed_rehearsal;
    const passingProofMatchesLatest = Boolean(last && passed && passed.backup === last.name);
    const latestAttemptMatchesLatest = Boolean(last && latest && latest.backup === last.name);
    let restoreability = 'Unknown — no passing restore test is recorded.';
    let restoreabilityDetail = 'The presence of a backup does not prove it can be restored.';
    if (passingProofMatchesLatest) {
      restoreability = passed.level === 'application'
        ? 'Yes — application recovery passed'
        : 'Yes — archive recovery passed';
      restoreabilityDetail = `${last.name} passed ${passed.level || 'archive'}-level recovery on ${ctx.formatAge(passed.at)}. ${passed.level_description || ''}`;
      if (latestAttemptMatchesLatest && latest.outcome === 'failed') {
        restoreabilityDetail += ` A later attempt failed: ${latest.error || 'failure details unavailable'}.`;
      }
    } else if (latestAttemptMatchesLatest && latest.outcome === 'failed') {
      restoreability = 'Not proven — latest restore test failed';
      restoreabilityDetail = `${last.name}: ${latest.error || 'failure details unavailable'}`;
    } else if (passed) {
      restoreability = 'Newest backup is untested';
      restoreabilityDetail = `The newest backup is ${last?.name || 'unavailable'}; the most recent proven recovery point is ${passed.backup} (${passed.level || 'archive'} level).`;
    }
    const facts = [
      ['Last successful backup', last ? ctx.formatAge(last.verified_at || last.created_at) : 'None',
        last ? `${last.name} · ${last.integrity === 'verified' ? 'signed' : 'unsigned manifest'} · archive verified${last.encrypted ? ' · encrypted' : ''}` : 'No validated backup is available'],
      ['Can this backup be restored?', last ? restoreability : 'No — no successful backup', restoreabilityDetail],
      ['Offsite copy', offsiteLabels[status.offsite.state] || status.offsite.state,
        status.offsite.uploaded_at ? `Uploaded ${ctx.formatAge(status.offsite.uploaded_at)}; remote access is not being probed live.` : 'No confirmed upload for this backup.',
      ],
      ['Last successful remote restore', status.offsite.restore_verified_at ? ctx.formatAge(status.offsite.restore_verified_at) : 'Not recorded',
        status.offsite.restore_verified_at ? 'A files or database restore completed from the offsite copy; this does not by itself prove application health.' : 'No successful restore from this offsite copy is recorded.'],
      ['Last restore rehearsal', latest ? ctx.formatAge(latest.at) : 'Never',
        latest ? `${latest.outcome === 'passed' ? 'Passed' : 'Failed'} · ${latest.trigger} · ${latest.backup} · ${latest.level || 'level unavailable'}` : 'Use “Rehearse restore” on a backup below'],
      ['Measured recovery time', passed ? formatDuration(status.measured_recovery_ms) : '—',
        passed ? (passed.level === 'application' && status.last_backup?.name === passed.backup ? 'Application proof for the newest backup' : passed.level === 'application' ? 'Proof was for a different backup' : 'Archive-only rehearsal; full-site time unknown') : 'No passing rehearsal yet'],
      ['Proven recovery point', passed ? passed.backup : 'None proven',
        passed ? `${passed.level || 'archive'}-level recovery passed ${ctx.formatAge(passed.at)}. ${passed.level_description || ''}${last && passed.backup !== last.name ? ` Newest backup ${last.name} is not covered by this proof.` : ''}` : 'No backup has passed a restore rehearsal.'],
      ['Encryption key', status.encryption_key === 'healthy' ? 'Proven' : 'Not proven',
        status.encryption_key === 'healthy' ? 'A rehearsal decrypted an encrypted backup' : 'No encrypted backup has been rehearsed'],
    ];
    const proof = (passed || latest || {}).level_description;
    return el('section', { className: 'recovery-status', 'aria-label': 'Recovery status' }, [
      el('div', { className: 'section-heading' }, [el('h3', {}, 'Recovery status'), recoveryBadge(status, ctx)]),
      el('p', { className: 'panel-intro' }, status.summary),
      el('dl', { className: 'recovery-facts' }, facts.map(([label, value, note]) => el('div', {}, [
        el('dt', {}, label),
        el('dd', {}, [el('strong', {}, value), note ? el('small', {}, note) : null]),
      ]))),
      status.reasons.length ? el('ul', { className: 'resource-list', 'aria-label': 'Recovery findings' }, status.reasons.map((reason) => el('li', { className: 'resource-list-item' }, [
        el('div', { className: 'item-meta' }, [el('small', {}, reason)]),
      ]))) : null,
      el('details', { className: 'recovery-procedure' }, [
        el('summary', {}, 'Estimated recovery procedure'),
        el('p', { className: 'import-note' }, recoveryEstimate(status)),
        el('ol', {}, recoveryProcedure(status).map((step) => el('li', {}, step))),
      ]),
      proof ? el('p', { className: 'import-note' }, `${passed && status.last_backup?.name !== passed.backup ? `An earlier backup passed a rehearsal. ${passed.level_description}` : `What the passing rehearsal proves: ${proof}`}`) : null,
      el('p', { className: 'import-note' }, automaticRehearsalNote(status)),
    ]);
  }

  // ---------------------------------------------------------------------
  // Backups tab
  // ---------------------------------------------------------------------

  async function renderBackupsTab(site, panel, ctx) {
    const [data, recoveryStatus] = await Promise.all([
      ctx.getJSON(`/api/backups?site=${encodeURIComponent(site)}&limit=100`).catch((error) => ({ __error: error, backups: [] })),
      ctx.can('backup:read') ? ctx.getJSON(`/api/sites/recovery/${encodeURIComponent(site)}`).catch((error) => ({ __error: error })) : Promise.resolve(null),
    ]);
    const backups = (data.backups || []).sort((a, b) => new Date(b.created_at || b.verified_at) - new Date(a.created_at || a.verified_at));
    const output = ctx.statusOutput();
    const cloneToStaging = async () => {
      try {
        const [overview, inventory] = await Promise.all([
          ctx.getJSON(`/api/sites/overview/${encodeURIComponent(site)}`),
          ctx.getJSON('/api/databases'),
        ]);
        const databases = (inventory.databases || []).filter((item) => item.site === site);
        if (!databases.length) throw new Error('No managed production database is registered to this site.');
        const confirmation = await ctx.confirmDangerous({
          title: `Clone ${site} to staging?`,
          message: 'StePanel creates a separate site identity and database, rewrites WordPress URLs, protects the route with Basic Auth and noindex, and records the production origin for later guarded promotion. Production files and database are read-only during cloning.',
          facts: [['Source site', site], ['Production databases', databases.map((item) => item.name).join(', ')]],
          confirmText: `CLONE ${site}`,
          actionLabel: 'Create staging clone',
          extraFields: [
            { name: 'staging_site', label: 'New staging site name', placeholder: `${site}-stage` },
            { name: 'domain', label: 'Staging domain', placeholder: 'staging.example.com' },
            { name: 'source_database', label: 'Production database', options: databases.map((item) => ({ value: item.name, label: item.name })) },
            { name: 'target_database', label: 'New staging database name', placeholder: `${site}_stage` },
            { name: 'target_user', label: 'New staging database user', placeholder: `${site}_stage` },
            { name: 'target_password', label: 'Staging database password', type: 'password', placeholder: '20+ supported characters' },
            { name: 'auth_user', label: 'Staging Basic Auth username', placeholder: 'reviewer' },
            { name: 'auth_password', label: 'Staging Basic Auth password', type: 'password', placeholder: '12+ characters' },
          ],
        });
        if (!confirmation.confirmed) return;
        const values = confirmation.values;
        const stagingSite = values.staging_site.toLowerCase();
        if (!/^[a-z0-9_-]{1,32}$/.test(stagingSite)) throw new Error('Staging site name must use lowercase letters, digits, dashes, or underscores.');
        output.textContent = 'Creating an isolated staging site and PHP-FPM pool…';
        const created = await ctx.postJSON('/api/sites', {
          site: stagingSite,
          template: 'php',
          account: overview.account || '',
          kind: overview.account ? 'addon' : '',
          domain: values.domain.toLowerCase(),
        });
        let siteJob;
        for (let attempt = 0; attempt < 120; attempt += 1) {
          await new Promise((resolve) => setTimeout(resolve, 1500));
          siteJob = await ctx.getJSON(`/api/jobs/${encodeURIComponent(created.job_id)}`);
          if (['completed', 'failed', 'cancelled', 'dead-letter'].includes(siteJob.state)) break;
        }
        if (!siteJob || siteJob.state !== 'completed') throw new Error(siteJob?.error || `Staging site setup is still running (${created.job_id}).`);
        output.textContent = 'Copying WordPress files and cloning the database into the isolated staging account…';
        const result = await ctx.runOperation('/api/staging', {
          source: site,
          site: stagingSite,
          domain: values.domain.toLowerCase(),
          files: true,
          environment: true,
          database: true,
          source_database: values.source_database,
          target_database: values.target_database.toLowerCase(),
          target_user: values.target_user.toLowerCase(),
          target_password: values.target_password,
          no_index: true,
          basic_auth: true,
          auth_user: values.auth_user,
          auth_password: values.auth_password,
        });
        output.textContent = result.wordpress_promotion_ready
          ? `Staging is live at https://${result.domain}. Basic Auth and noindex are enabled. Its database is isolated, and promotion back to ${site} is available with a pre-promotion backup and retained prior database.`
          : `Staging site created at https://${result.domain}.`;
      } catch (error) {
        output.textContent = error.message;
      }
    };
    const promoteFromStaging = async () => {
      const sourceInput = await ctx.confirmDangerous({
        title: `Prepare promotion to ${site}`,
        message: 'Enter the StePanel staging site name. The preview checks the recorded production relationship and both managed database ownership records.',
        facts: [['Production site', site]],
        confirmText: site,
        actionLabel: 'Build promotion preview',
        extraFields: [{ name: 'source', label: 'Staging site name', placeholder: `${site}-stage` }],
      });
      if (!sourceInput.confirmed) return;
      const source = sourceInput.values.source.toLowerCase();
      output.textContent = 'Checking staging relationship and database ownership…';
      try {
        const preview = await ctx.postJSON('/api/staging/promote/preview', { source, site });
        const confirm = await ctx.confirmDangerous({
          title: `Promote ${source} to ${site}?`,
          message: `${preview.effects.join(' ')} ${preview.recovery_plan.join(' ')}`,
          facts: [['Production domain', preview.proposed.production_domain], ['Staging database', preview.proposed.source_database], ['Current production database', preview.before.active_production_database]],
          confirmText: `PROMOTE ${site}`,
          actionLabel: 'Queue guarded promotion',
        });
        if (!confirm.confirmed) return;
        output.textContent = 'Creating the production safety backup, candidate database, and transactional file cutover…';
        const result = await ctx.runOperation('/api/staging/promote', { source, site, preview_token: preview.preview_token });
        output.textContent = `Promotion completed for https://${result.domain}. Database ${result.database} passed WP-CLI validation. Previous database ${result.previous_database_retained} remains intact; safety backup ${result.safety_backup} is retained. Validate the public site before removing either recovery copy.`;
      } catch (error) {
        output.textContent = error.message;
      }
    };
    const restoreToStaging = async (backup, includeDatabase) => {
      const backupName = backup.name || backup.path;
      const databaseNames = (backup.databases || []).filter((name) => /^[A-Za-z0-9_]+$/.test(name));
      const fields = [
        { name: 'domain', label: 'Staging domain to activate', placeholder: 'staging.example.com', hint: 'Must already resolve to this server.' },
      ];
      if (includeDatabase) {
        fields.push(
          { name: 'database', label: 'Database dump', tag: 'select', options: databaseNames.map((name) => ({ value: name, label: name })), hint: 'Select the verified dump to import.' },
          { name: 'target_database', label: 'New database name', placeholder: `${site}_staging` },
          { name: 'target_user', label: 'New database user', placeholder: `${site}_staging` },
          { name: 'target_password', label: 'New database password', type: 'password', placeholder: 'At least 20 supported characters' },
        );
      }
      const confirmation = await ctx.confirmDangerous({
        title: includeDatabase ? 'Restore files and database to staging?' : 'Restore to staging?',
        message: 'Files are restored into an isolated, no-index staging route. The live site is not touched.',
        facts: [['Backup', backupName], ['Site', site]],
        confirmText: `RESTORE ${site}`,
        actionLabel: includeDatabase ? 'Restore files + database' : 'Restore to staging',
        extraFields: fields,
      });
      if (!confirmation.confirmed) return;
      const values = confirmation.values || {};
      output.textContent = includeDatabase ? 'Restoring files and database to staging…' : 'Restoring to staging…';
      try {
        const result = await ctx.runOperation('/api/backups/restore-to-staging', {
          site, backup: backupName, domain: values.domain,
          ...(includeDatabase ? {
            database: values.database,
            target_database: values.target_database,
            target_user: values.target_user,
            target_password: values.target_password,
          } : {}),
        });
        output.textContent = `Staging restore is ready at ${result.domain || values.domain}. Production was not changed.`;
      } catch (error) { output.textContent = error.message; }
    };

    panel.replaceChildren(
      recoveryStatus && recoveryStatus.__error ? errorState('Recovery status', recoveryStatus.__error) : recoveryStatus ? renderRecoveryStatus(recoveryStatus, ctx) : null,
      el('p', { className: 'panel-intro' }, 'Backups are checksummed and, when a signing key is configured, cryptographically signed and verified before they are trusted for restore.'),
      el('div', { className: 'workspace-panel-actions' }, [
        ctx.can('site:deploy') ? ctx.button('Clone WordPress to staging', cloneToStaging) : null,
        ctx.can('site:deploy') ? ctx.button('Promote staging to production', promoteFromStaging, { className: 'danger' }) : null,
        ctx.can('backup:create') ? ctx.button('Create verified backup', async () => {
          output.textContent = 'Creating a verified backup…';
          try {
            const result = await ctx.postJSON('/api/backups', { site, include_databases: true });
            output.textContent = `Backup queued (job ${result.job_id}). Refresh in a moment to see it listed.`;
          } catch (error) { output.textContent = error.message; }
        }) : null,
      ]),
      output,
      data.__error ? errorState('Backup inventory', data.__error) : el('ul', { className: 'resource-list' }, backups.length ? backups.map((backup) => el('li', { className: 'resource-list-item' }, [
        el('div', { className: 'item-meta' }, [
          el('strong', {}, backup.name || (backup.path || '').split('/').pop() || 'Backup'),
          el('small', {}, backup.verified_at ? `Verified ${ctx.formatAge(backup.verified_at)}` : 'Not yet verified'),
        ]),
        el('div', { className: 'item-actions' }, [
          ctx.can('backup:read') ? ctx.button('Verify', async () => {
            output.textContent = 'Verifying…';
            try { await ctx.postJSON('/api/backups/verify', { site, backup: backup.name || backup.path }); output.textContent = 'Backup verified.'; } catch (error) { output.textContent = error.message; }
          }) : null,
          ctx.can('backup:restore') ? ctx.button('Rehearse restore', async () => {
            output.textContent = 'Rehearsing restore…';
            try {
              const result = await ctx.postJSON('/api/backups/rehearse', { site, backup: backup.name || backup.path });
              output.textContent = `Restore rehearsal queued (job ${result.job_id}). The live site was not changed.`;
            } catch (error) { output.textContent = error.message; }
          }) : null,
          ctx.can('backup:restore') ? ctx.button('Restore to staging', () => restoreToStaging(backup, false)) : null,
          (backup.databases || []).length && ctx.can('backup:restore') ? ctx.button('Restore files + database', () => restoreToStaging(backup, true)) : null,
        ]),
      ])) : [el('p', { className: 'empty-state' }, 'No backups yet. Create one above.')]),
    );
  }

  // ---------------------------------------------------------------------
  // Logs tab
  // ---------------------------------------------------------------------

  const LOG_SOURCES = ['access', 'error', 'php-fpm', 'php', 'application', 'deployment', 'build', 'cron', 'worker'];

  async function renderLogsTab(site, panel, ctx) {
    const sourceField = field({ label: 'Source', tag: 'select', name: 'source', value: 'error', options: LOG_SOURCES.map((s) => ({ value: s, label: s })) });
    const filterField = field({ label: 'Filter (optional)', name: 'filter', placeholder: 'search text' });
    const viewer = el('pre', { className: 'log-viewer' });
    const output = ctx.statusOutput();

    const load = async () => {
      const source = sourceField.querySelector('select').value;
      const filter = filterField.querySelector('input').value.trim();
      output.textContent = 'Loading…';
      try {
        const params = new URLSearchParams({ source, lines: '200' });
        if (filter) params.set('filter', filter);
        const data = await ctx.getJSON(`/api/sites/logs/${encodeURIComponent(site)}?${params}`);
        viewer.textContent = data.available ? (data.lines || []).join('\n') : 'This log has not been written yet.';
        output.textContent = '';
      } catch (error) { output.textContent = error.message; }
    };

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, "The most recent lines from this site's logs."),
      el('div', { className: 'log-controls' }, [sourceField, filterField, el('button', { type: 'button', className: 'primary-action', onClick: load }, 'Load')]),
      output,
      viewer,
    );
    load();
  }

  // ---------------------------------------------------------------------
  // Workers tab
  // ---------------------------------------------------------------------

  const WORKER_TYPES = ['laravel', 'horizon', 'node', 'celery', 'rq'];

  async function renderWorkersTab(site, panel, ctx) {
    const data = await ctx.getJSON(`/api/workers/${encodeURIComponent(site)}`).catch((error) => ({ __error: error, workers: [] }));
    const workers = data.workers || [];
    const output = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Background queue workers for this site (Laravel queues, Horizon, Celery, RQ, or a Node worker process).'),
      data.__error ? errorState('Worker inventory', data.__error) : el('ul', { className: 'resource-list' }, workers.length ? workers.map((worker) => el('li', { className: 'resource-list-item' }, [
        el('div', { className: 'item-meta' }, [el('strong', {}, worker.name), el('small', {}, `${worker.type} · ${worker.processes} process(es) · ${worker.state || 'unknown'}`)]),
        el('div', { className: 'item-actions' }, [
          ctx.button('Restart', async () => { output.textContent = 'Restarting…'; try { await ctx.postJSON(`/api/workers/${encodeURIComponent(site)}/${encodeURIComponent(worker.name)}/restart`); output.textContent = 'Restarted.'; } catch (error) { output.textContent = error.message; } }),
          ctx.button('Stop', async () => { output.textContent = 'Stopping…'; try { await ctx.postJSON(`/api/workers/${encodeURIComponent(site)}/${encodeURIComponent(worker.name)}/stop`); output.textContent = 'Stopped.'; } catch (error) { output.textContent = error.message; } }),
          ctx.button('Delete', async () => {
            const confirmed = await ctx.confirmDangerous({ title: `Delete worker ${worker.name}?`, message: 'The worker service is stopped and removed.', confirmText: worker.name, actionLabel: 'Delete worker' });
            if (!confirmed) return;
            output.textContent = 'Deleting…';
            try { await ctx.deleteJSON(`/api/workers/${encodeURIComponent(site)}/${encodeURIComponent(worker.name)}`); output.textContent = 'Deleted.'; renderWorkersTab(site, panel, ctx); } catch (error) { output.textContent = error.message; }
          }, { className: 'danger' }),
        ]),
      ])) : [el('p', { className: 'empty-state' }, 'No workers configured for this site.')]),
      output,
    );

    const nameField = field({ label: 'Name', name: 'name', pattern: '[A-Za-z0-9_-]{1,32}', required: true });
    const typeField = field({ label: 'Type', tag: 'select', name: 'type', value: 'laravel', options: WORKER_TYPES.map((t) => ({ value: t, label: t })) });
    const processesField = field({ label: 'Processes', name: 'processes', type: 'number', value: 1, min: 1, max: 64 });
    const memoryField = field({ label: 'Memory (MB)', name: 'memory_mb', type: 'number', value: 256, min: 64, max: 65536 });
    const retriesField = field({ label: 'Retries', name: 'retries', type: 'number', value: 3, min: 0, max: 20 });
    const createOutput = ctx.statusOutput();
    panel.append(el('h4', {}, 'Add a worker'), el('form', {
      className: 'import-form', onSubmit: async (event) => {
        event.preventDefault();
        const name = nameField.querySelector('input').value;
        createOutput.textContent = 'Saving…';
        try {
          await ctx.putJSON(`/api/workers/${encodeURIComponent(site)}/${encodeURIComponent(name)}`, {
            type: typeField.querySelector('select').value,
            processes: Number(processesField.querySelector('input').value),
            memory_mb: Number(memoryField.querySelector('input').value),
            retries: Number(retriesField.querySelector('input').value),
          });
          createOutput.textContent = 'Worker created.';
          renderWorkersTab(site, panel, ctx);
        } catch (error) { createOutput.textContent = error.message; }
      },
    }, [el('div', { className: 'field-row' }, [nameField, typeField, processesField, memoryField, retriesField]), el('button', { type: 'submit', className: 'primary-action' }, 'Add worker'), createOutput]));
    if (!ctx.can('site:deploy')) panel.querySelectorAll('button').forEach((node) => { node.disabled = true; });
  }

  // ---------------------------------------------------------------------
  // Tasks tab (scheduled jobs)
  // ---------------------------------------------------------------------

  const TASK_RUNTIMES = ['php', 'node', 'python', 'shell'];

  async function renderTasksTab(site, panel, ctx) {
    let data;
    let apiError = null;
    try {
      data = await ctx.getJSON(`/api/tasks/${encodeURIComponent(site)}`);
    } catch (error) {
      apiError = error;
      data = { tasks: [] };
    }
    const tasks = data.tasks || [];
    const output = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Scheduled commands run as systemd timers under the site\'s isolated identity.'),
      apiError ? el('div', { className: 'error-message' }, [
        el('strong', {}, 'Unable to load scheduled tasks'),
        el('p', {}, `API error: ${apiError.message}. Retry when the API becomes available.`)
      ]) : null,
      el('ul', { className: 'resource-list' }, tasks.length ? tasks.map((task) => el('li', { className: 'resource-list-item' }, [
        el('div', { className: 'item-meta' }, [el('strong', {}, task.name), el('small', {}, `${task.on_calendar} · ${task.runtime} · ${task.enabled ? 'enabled' : 'disabled'} · ${task.current_run_count ? 'running' : 'idle'} · ${task.consecutive_failures || 0} consecutive failures · timeout ${task.timeout_sec}s · min interval ${task.min_interval_seconds || 60}s`)]),
        el('div', { className: 'item-actions' }, [
          ctx.button('History', async () => {
            try {
              const details = await ctx.getJSON(`/api/tasks/${encodeURIComponent(site)}/${encodeURIComponent(task.name)}`);
              const executions = details.executions || [];
              output.textContent = executions.length
                ? `${plural(details.consecutive_failures || 0, 'consecutive failure')}; ${executions.map((run) => `${new Date(run.started_at * 1000).toLocaleString()} · ${run.result} (${run.duration_seconds}s)`).join('\n')}`
                : 'No recorded executions yet.';
            } catch (error) { output.textContent = error.message; }
          }),
          ctx.button('Stop run', async () => {
            try {
              const details = await ctx.getJSON(`/api/tasks/${encodeURIComponent(site)}/${encodeURIComponent(task.name)}`);
              if (!details.current_run_count) { output.textContent = 'Task is not currently running.'; return; }
              const confirmed = await ctx.confirmDangerous({ title: `Stop task ${task.name}?`, message: 'The active task process will receive SIGTERM.', confirmText: task.name, actionLabel: 'Stop task' });
              if (!confirmed) return;
              await ctx.postJSON(`/api/tasks/${encodeURIComponent(site)}/${encodeURIComponent(task.name)}/kill`);
              output.textContent = 'Stop requested.';
            } catch (error) { output.textContent = error.message; }
          }, { className: 'danger' }),
          ctx.button('Delete', async () => {
            const confirmed = await ctx.confirmDangerous({ title: `Delete task ${task.name}?`, message: 'The scheduled timer is removed immediately.', confirmText: task.name, actionLabel: 'Delete task' });
            if (!confirmed) return;
            output.textContent = 'Deleting…';
            try { await ctx.deleteJSON(`/api/tasks/${encodeURIComponent(site)}/${encodeURIComponent(task.name)}`); output.textContent = 'Deleted.'; renderTasksTab(site, panel, ctx); } catch (error) { output.textContent = error.message; }
          }, { className: 'danger' }),
        ]),
      ])) : [el('p', { className: 'empty-state' }, 'No scheduled tasks yet.')]),
      output,
    );

    const nameField = field({ label: 'Name', name: 'name', pattern: '[A-Za-z0-9_-]{1,32}', required: true });
    const runtimeField = field({ label: 'Runtime', tag: 'select', name: 'runtime', value: 'shell', options: TASK_RUNTIMES.map((r) => ({ value: r, label: r })) });
    const calendarField = field({ label: 'Schedule (systemd OnCalendar)', name: 'on_calendar', value: '*-*-* *:00:00', hint: 'e.g. *-*-* *:00:00 for hourly.' });
    const commandField = field({ label: 'Command', tag: 'textarea', name: 'command', required: true, hint: 'Runs as a single shell command, e.g. php artisan schedule:run.', rows: 3 });
    const timeoutField = field({ label: 'Timeout (seconds)', name: 'timeout_sec', type: 'number', value: 300, min: 1, max: 86400 });
    const minIntervalField = field({ label: 'Minimum interval (seconds)', name: 'min_interval_seconds', type: 'number', value: 60, min: 60, max: 31536000, hint: 'The calendar schedule must not run more frequently than this.' });
    const missedPolicyField = field({ label: 'Missed-run policy', tag: 'select', name: 'missed_run_policy', value: 'run_once', options: [{ value: 'run_once', label: 'Run once after downtime' }, { value: 'skip', label: 'Skip missed runs' }] });
    const cpuField = field({ label: 'CPU quota (%)', name: 'cpu_percent', type: 'number', value: 100, min: 25, max: 6400 });
    const memoryField = field({ label: 'Memory limit (MiB)', name: 'memory_mb', type: 'number', value: 1024, min: 64, max: 1048576 });
    const tasksField = field({ label: 'Process limit', name: 'tasks_max', type: 'number', value: 256, min: 16, max: 100000 });
    const webhookField = field({ label: 'Completion webhook (HTTPS)', name: 'notify_webhook', type: 'url', hint: 'Receives a small JSON result notification after each run.' });
    const enabledField = field({ label: 'Enabled', tag: 'checkbox-field', name: 'enabled', checked: true });
    const createOutput = ctx.statusOutput();
    panel.append(el('h4', {}, 'Add a scheduled task'), el('form', {
      className: 'import-form', onSubmit: async (event) => {
        event.preventDefault();
        const name = nameField.querySelector('input').value;
        createOutput.textContent = 'Saving…';
        try {
          await ctx.putJSON(`/api/tasks/${encodeURIComponent(site)}/${encodeURIComponent(name)}`, {
            runtime: runtimeField.querySelector('select').value,
            on_calendar: calendarField.querySelector('input').value,
            command: commandField.querySelector('textarea').value,
            timeout_sec: Number(timeoutField.querySelector('input').value),
            min_interval_seconds: Number(minIntervalField.querySelector('input').value),
            max_concurrent_runs: 1,
            missed_run_policy: missedPolicyField.querySelector('select').value,
            cpu_percent: Number(cpuField.querySelector('input').value),
            memory_mb: Number(memoryField.querySelector('input').value),
            tasks_max: Number(tasksField.querySelector('input').value),
            notify_webhook: webhookField.querySelector('input').value,
            enabled: enabledField.querySelector('input').checked,
          });
          createOutput.textContent = 'Task created.';
          renderTasksTab(site, panel, ctx);
        } catch (error) { createOutput.textContent = error.message; }
      },
    }, [nameField, el('div', { className: 'field-row' }, [runtimeField, calendarField, timeoutField]), el('div', { className: 'field-row' }, [minIntervalField, missedPolicyField]), el('div', { className: 'field-row' }, [cpuField, memoryField, tasksField]), commandField, webhookField, enabledField, el('button', { type: 'submit', className: 'primary-action' }, 'Add task'), createOutput]));
    if (!ctx.can('site:deploy')) panel.querySelectorAll('button').forEach((node) => { node.disabled = true; });
  }

  // ---------------------------------------------------------------------
  // Security tab (SSH/SFTP access, keys, suspicious PHP heuristic scan)
  // ---------------------------------------------------------------------

  async function renderSecurityTab(site, panel, ctx) {
    const access = await ctx.getJSON(`/api/sites/access/${encodeURIComponent(site)}`).catch((error) => ({ __error: error, sftp_enabled: false, shell_enabled: false, keys: [] }));
    if (access.__error) {
      panel.replaceChildren(errorState('SSH access status', access.__error));
      return;
    }
    const output = ctx.statusOutput();

    const sftpField = field({ label: 'SFTP access', tag: 'checkbox-field', name: 'sftp_enabled', checked: access.sftp_enabled });
    const shellField = field({ label: 'Shell (SSH) access', tag: 'checkbox-field', name: 'shell_enabled', checked: access.shell_enabled });
    const accessForm = el('form', {
      className: 'import-form', onSubmit: async (event) => {
        event.preventDefault();
        output.textContent = 'Updating access policy…';
        try {
          await ctx.patchJSON(`/api/sites/access/${encodeURIComponent(site)}`, {
            sftp_enabled: sftpField.querySelector('input').checked,
            shell_enabled: shellField.querySelector('input').checked,
          });
          output.textContent = 'Access policy updated.';
        } catch (error) { output.textContent = error.message; }
      },
    }, [el('h4', {}, 'SFTP & shell access'), sftpField, shellField, el('button', { type: 'submit', className: 'primary-action' }, 'Save access policy'), output]);

    const keyOutput = ctx.statusOutput();
    const keys = access.keys || [];
    const keyList = el('ul', { className: 'resource-list' }, keys.length ? keys.map((key) => el('li', { className: 'resource-list-item' }, [
      el('div', { className: 'item-meta' }, [el('strong', {}, key.label), el('small', {}, key.fingerprint)]),
      el('div', { className: 'item-actions' }, [ctx.button('Remove', async () => {
        const confirmed = await ctx.confirmDangerous({
          title: `Remove key "${key.label}"?`,
          message: 'Anyone who authenticates with this key immediately loses SFTP/shell access to this site.',
          facts: [['Key', key.label], ['Fingerprint', key.fingerprint], ['Site', site]],
          confirmText: key.label,
          actionLabel: 'Remove key',
        });
        if (!confirmed) return;
        keyOutput.textContent = 'Removing…';
        try { await ctx.deleteJSON(`/api/sites/access/${encodeURIComponent(site)}/${encodeURIComponent(key.label)}`); keyOutput.textContent = 'Removed.'; renderSecurityTab(site, panel, ctx); } catch (error) { keyOutput.textContent = error.message; }
      }, { className: 'danger' })]),
    ])) : [el('p', { className: 'empty-state' }, 'No SSH keys added yet.')]);

    const labelField = field({ label: 'Key label', name: 'label', pattern: '[A-Za-z0-9_.-]{1,64}', required: true });
    const keyField = field({ label: 'Public key', tag: 'textarea', name: 'public_key', required: true, hint: 'ssh-ed25519 AAAA… (DSA keys are not permitted).', rows: 3 });
    const keyForm = el('form', {
      className: 'import-form', onSubmit: async (event) => {
        event.preventDefault();
        keyOutput.textContent = 'Adding key…';
        try {
          await ctx.postJSON(`/api/sites/access/${encodeURIComponent(site)}`, { label: labelField.querySelector('input').value, public_key: keyField.querySelector('textarea').value });
          keyOutput.textContent = 'Key added.';
          renderSecurityTab(site, panel, ctx);
        } catch (error) { keyOutput.textContent = error.message; }
      },
    }, [labelField, keyField, el('button', { type: 'submit', className: 'primary-action' }, 'Add SSH key'), keyOutput]);

    panel.replaceChildren(accessForm, el('h4', {}, 'SSH keys'), keyList, keyForm);

    if (!ctx.can('ssh:write')) panel.querySelectorAll('button, input').forEach((node) => { node.disabled = true; });

    if (ctx.isAdministrator) {
      const scanOutput = ctx.statusOutput();
      panel.append(el('h4', {}, 'Suspicious PHP / webshell heuristic scan'), el('p', { className: 'panel-intro' }, 'Defense-in-depth review only; this is not authoritative malware detection.'), el('div', { className: 'workspace-panel-actions' }, [
        ctx.button('Scan site files', async () => {
          scanOutput.textContent = 'Scanning PHP files for suspicious webshell indicators…';
          try {
            const result = await ctx.postJSON('/api/security/scan', { site, quarantine: false });
            const scan = result.scan || {};
            scanOutput.textContent = `Heuristic review complete: ${plural((result.findings || []).length, 'finding')}, ${plural(scan.large_files_skipped || 0, 'large file')} skipped.`;
          } catch (error) { scanOutput.textContent = error.message; }
        }),
      ]), scanOutput);
    }
  }

  // ---------------------------------------------------------------------
  // Settings tab (environment variables, resources, Redis, danger zone)
  // ---------------------------------------------------------------------

  async function renderSettingsTab(site, panel, ctx) {
    panel.replaceChildren(el('p', { className: 'panel-intro' }, 'Environment variables, resource limits, and site-wide settings.'));

    // Environment variables
    const envOutput = ctx.statusOutput();
    const env = await ctx.getJSON(`/api/sites/environment/${encodeURIComponent(site)}`).catch((error) => ({ __error: error, environment: {} }));
    // Each row keeps an explicit model: its original name and secrecy come from
    // the API, and secrecy is an explicit checkbox, never inferred from the
    // input type. Redacted secrets are sent as "preserve", removals as
    // "delete"; a blank secret never implies deletion.
    const envRows = [];
    const rows = el('div', {});
    const addEnvRow = (name = null, value = '') => {
      const existingSecret = name !== null && typeof value === 'object' && value !== null && value.secret === true;
      const nameField = field({ label: 'Name', name: 'env-name', value: name || '' });
      const valueField = field({
        label: 'Value', name: 'env-value',
        value: existingSecret ? '' : String(value ?? ''),
        type: existingSecret ? 'password' : 'text',
        placeholder: existingSecret ? 'Unchanged (leave blank to keep)' : '',
      });
      const secretField = field({ tag: 'checkbox-field', label: 'Secret', name: 'env-secret', checked: existingSecret });
      const row = {
        original: name,
        originalSecret: existingSecret,
        removed: false,
        nameInput: nameField.querySelector('input'),
        valueInput: valueField.querySelector('input'),
        secretInput: secretField.querySelector('input'),
      };
      row.secretInput.addEventListener('change', () => { row.valueInput.type = row.secretInput.checked ? 'password' : 'text'; });
      const node = el('div', { className: 'field-row' }, [nameField, valueField, secretField, el('button', {
        type: 'button', className: 'quiet-action', onClick: () => { row.removed = true; node.remove(); },
      }, 'Remove')]);
      envRows.push(row);
      rows.append(node);
    };
    Object.entries(env.environment || {}).forEach(([name, value]) => addEnvRow(name, value));
    const buildEnvironmentPayload = () => {
      const payload = {};
      const put = (name, update) => {
        if (payload[name] && payload[name].operation !== 'delete') throw new Error(`Variable ${name} is listed more than once.`);
        payload[name] = update;
      };
      // Deletions first so a rename onto a removed name is not a duplicate.
      envRows.forEach((row) => {
        const name = row.nameInput.value.trim();
        if (row.original !== null && (row.removed || name !== row.original)) payload[row.original] = { operation: 'delete' };
      });
      envRows.forEach((row) => {
        if (row.removed) return;
        const name = row.nameInput.value.trim();
        const value = row.valueInput.value;
        const secret = row.secretInput.checked;
        if (!name) {
          if (row.original === null && value) throw new Error('Every variable with a value needs a name.');
          return;
        }
        const redacted = row.originalSecret && value === '';
        if (redacted && name === row.original && secret) {
          put(name, { operation: 'preserve', secret: true });
        } else if (redacted) {
          throw new Error(`Enter a new value for ${row.original} to rename it or make it non-secret.`);
        } else {
          put(name, { operation: 'set', value, secret });
        }
      });
      return payload;
    };
    const addRowButton = el('button', {
      type: 'button', className: 'quiet-action', onClick: () => addEnvRow(),
    }, '+ Add variable');
    panel.append(el('h4', {}, 'Environment variables'), env.__error ? errorState('Environment status', env.__error) : env.environment ? el('div', {}, [rows, addRowButton, el('div', { className: 'workspace-panel-actions' }, [el('button', {
      type: 'button', className: 'primary-action', onClick: async () => {
        let payload;
        try { payload = buildEnvironmentPayload(); } catch (error) { envOutput.textContent = error.message; return; }
        envOutput.textContent = 'Saving…';
        try {
          await ctx.putJSON(`/api/sites/environment/${encodeURIComponent(site)}`, payload);
          envOutput.textContent = 'Environment applied.';
          renderSettingsTab(site, panel, ctx);
        } catch (error) { envOutput.textContent = error.message; }
      },
    }, 'Save environment')]), envOutput]) : el('p', { className: 'import-note' }, 'Environment management requires STEPANEL_ENVIRONMENT_KEY to be configured.'));

    // Resource profiles are administrator-configured but tenant-readable so
    // customers can see the envelope their plan is actually enforcing.
    const [resources, usage] = await Promise.all([
      ctx.getJSON(`/api/sites/resources/${encodeURIComponent(site)}`).catch((error) => ({ __error: error })),
      ctx.getJSON(`/api/sites/usage/${encodeURIComponent(site)}`).catch((error) => ({ __error: error })),
    ]);
    if (resources.__error) {
      panel.append(errorState('Resource profile', resources.__error));
    } else if (resources) {
      const profile = resources.profile || resources;
      const configured = resources.configured !== false;
      const value = (key, suffix = '') => profile[key] === undefined || profile[key] === null ? '—' : `${profile[key]}${suffix}`;
      const observedState = resources.observed?.state || 'unavailable';
      panel.append(
        el('h4', {}, ctx.isAdministrator ? 'Resource profile' : 'Plan resource envelope'),
        el('p', { className: 'import-note' }, configured
          ? `Host enforcement: ${observedState}. These limits are managed by your plan.`
          : 'No resource profile has been applied yet; the administrator will reconcile it.'),
        el('div', { className: 'overview-grid' }, [
          el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'CPU'), el('strong', {}, value('cpu_percent', '%'))]),
          el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Memory'), el('strong', {}, value('memory_mb', ' MB'))]),
          el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Tasks'), el('strong', {}, value('tasks_max'))]),
          el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'PHP workers'), el('strong', {}, value('php_workers'))]),
        ]),
      );
      const observedValue = (key) => resources.observed?.[key] || 'unknown';
      const memoryCurrent = Number(observedValue('MemoryCurrent'));
      const memoryMax = Number(observedValue('MemoryMax'));
      const cpuNS = Number(observedValue('CPUUsageNSec'));
      const tasksCurrent = observedValue('TasksCurrent');
      const fpm = resources.fpm_status || {};
      const memoryCards = [];
      if (ctx.isAdministrator && fpm.host_memory && Number.isFinite(Number(fpm.host_memory.available_bytes))) {
        memoryCards.push(el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Host memory available'), el('strong', {}, formatBytes(fpm.host_memory.available_bytes)), el('small', {}, `Host-wide MemAvailable sample${fpm.host_memory.collected_at ? ` · ${new Date(fpm.host_memory.collected_at).toLocaleTimeString()}` : ''}; other workloads can consume it.`)]));
      }
      if (Number.isFinite(memoryCurrent) && memoryCurrent >= 0) {
        memoryCards.push(el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Managed service memory now'), el('strong', {}, formatBytes(memoryCurrent)), el('small', {}, Number.isFinite(memoryMax) && memoryMax > 0 ? `of ${formatBytes(memoryMax)} cgroup limit; PHP-FPM is reported separately below` : 'cgroup limit unavailable; PHP-FPM is reported separately below')]));
      }
      if (tasksCurrent !== 'unknown') memoryCards.push(el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Tasks now'), el('strong', {}, `${tasksCurrent} / ${observedValue('TasksMax')}`)]));
      if (Number.isFinite(cpuNS) && cpuNS >= 0) memoryCards.push(el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Managed service CPU time'), el('strong', {}, `${(cpuNS / 1e9).toFixed(1)} s`), el('small', {}, 'cumulative since slice start; not a utilization percentage')]));
      if (memoryCards.length) panel.append(el('h5', {}, 'Observed site resource use'), el('div', { className: 'overview-grid' }, memoryCards));
      if (usage && !usage.__error && usage.quota) {
        const quota = usage.quota;
          const quotaDetails = `${Number.isFinite(Number(quota.estimated_remaining_bytes)) ? `${formatBytes(Number(quota.tree_bytes || 0))} used · up to ${formatBytes(Number(quota.estimated_remaining_bytes))} estimated bytes remain` : `${formatBytes(Number(usage.bytes || 0))} measured · byte quota headroom unknown`}${Number.isFinite(Number(quota.estimated_remaining_inodes)) ? ` · ${(Number(quota.tree_inodes || 0)).toLocaleString()} inodes measured · up to ${Number(quota.estimated_remaining_inodes).toLocaleString()} estimated inodes remain` : ''}.`;
        panel.append(el('h5', {}, 'Filesystem quota headroom'), el('article', { className: 'resource-list-item' }, [
          el('div', { className: 'item-meta' }, [
            el('strong', {}, quota.enforced ? 'Kernel quota marked enforced' : 'Quota enforcement unconfirmed'),
            el('small', {}, quotaDetails),
          ]),
          el('small', {}, `${quota.scope_note || 'The measurement covers this site tree only.'}${usage.complete === false ? ' The tree scan reached its safety bound, so measured usage is incomplete.' : ''}`),
        ]));
      } else if (usage && usage.__error) {
        panel.append(el('p', { className: 'import-note' }, `Filesystem usage could not be measured: ${usage.__error.message || 'request failed'}. Quota headroom is unknown.`));
      }
      const pools = (fpm.pools || []).filter((pool) => pool.state === 'available');
      if (pools.length) {
        const sampledAt = fpm.collected_at && Number.isFinite(Date.parse(fpm.collected_at))
          ? new Date(fpm.collected_at).toLocaleString()
          : 'time unavailable';
        panel.append(el('h5', {}, 'PHP-FPM measurements'), el('p', { className: 'import-note' }, `Live pool counters and process RSS sampled ${sampledAt}. Worker RSS includes shared mappings and is an instantaneous estimate, not private-memory accounting or a sustained-load forecast. Host MemAvailable is sampled separately and is not reserved for this site.`), el('div', { className: 'resource-list' }, pools.map((pool) => {
          const memory = pool.worker_memory;
          const maxObservedProjection = memory && (Number(memory.projected_max_rss_at_max_children_bytes)
            || Number(memory.max_rss_bytes || 0) * Number(pool.max_children || 0));
          const details = memory
            ? `${memory.sampled_workers} worker(s) sampled · current summed RSS ${formatBytes(memory.total_rss_bytes)} · mean ${formatBytes(memory.mean_rss_bytes)} · P95 ${formatBytes(memory.p95_rss_bytes)} · projected ${formatBytes(memory.projected_rss_at_max_children_bytes)} at P95 or ${formatBytes(maxObservedProjection)} using the largest observed worker, at max_children ${pool.max_children}${memory.estimated_worker_ceiling ? ` · conservative planning ceiling ${memory.estimated_worker_ceiling} workers from current host headroom` : ''}`
            : 'Worker RSS unavailable; FPM queue and slow-request counters remain available.';
          const slowThreshold = pool.slow_request_threshold || 'not configured';
          return el('article', { className: 'resource-list-item' }, [el('div', { className: 'item-meta' }, [el('strong', {}, `PHP ${pool.php_version || 'default'} · ${pool.active_processes} active / ${pool.idle_processes} idle / ${pool.max_children} max`), el('small', {}, details)]), el('small', {}, `Request queue now ${pool.listen_queue} (historical peak ${pool.max_listen_queue || 0}); slow requests since FPM start ${pool.slow_requests} (threshold ${slowThreshold}); max-children events ${pool.max_children_reached}. A nonzero historical counter is evidence to investigate, not proof of current pressure. ${memory?.worker_ceiling_basis || ''}`)]);
        })));
      }
      const guidance = Array.isArray(fpm.performance_guidance) ? fpm.performance_guidance : [];
      if (guidance.length) {
        panel.append(el('h5', {}, 'PHP-FPM recommendations'), el('div', { className: 'resource-list' }, guidance.map((item) => el('article', { className: 'resource-list-item' }, [
          el('div', { className: 'item-meta' }, [
            el('strong', {}, `${item.php_version ? `PHP ${item.php_version} · ` : ''}${item.title}`),
            el('small', {}, item.evidence || 'No supporting sample was returned.'),
          ]),
          el('small', {}, `${item.recommendation || 'Review the pool measurements.'}${item.operator_approval_needed ? ' Any configuration change requires administrator approval and a separate preview.' : ''}`),
        ]))));
      }
      if (!fpm.available) panel.append(el('p', { className: 'import-note' }, fpm.detail || 'Live PHP-FPM measurements are unavailable.'));
      if (ctx.isAdministrator && configured) {
        const workers = field({ label: 'PHP-FPM max children', name: 'php-workers', type: 'number', min: 1, max: 512, value: profile.php_workers, hint: 'Preview includes measured worker RSS where available.' });
        const workerOutput = ctx.statusOutput();
        const workerForm = el('form', {
          className: 'import-form',
          onSubmit: async (event) => {
            event.preventDefault();
            const desiredWorkers = Number(workers.querySelector('input').value);
            if (!Number.isInteger(desiredWorkers) || desiredWorkers < 1 || desiredWorkers > 512) {
              workerOutput.textContent = 'Enter a whole number from 1 to 512.';
              return;
            }
            const change = {
              cpu_percent: Number(profile.cpu_percent), cpu_weight: Number(profile.cpu_weight),
              memory_high_mb: Number(profile.memory_high_mb), memory_mb: Number(profile.memory_mb),
              io_weight: Number(profile.io_weight), tasks_max: Number(profile.tasks_max),
              php_workers: desiredWorkers, disk_mb: Number(profile.disk_mb || 0), inodes: Number(profile.inodes || 0),
            };
            workerOutput.textContent = 'Measuring current PHP-FPM workers and preparing a preview…';
            try {
              const preview = await ctx.postJSON(`/api/sites/resources/${encodeURIComponent(site)}`, change);
              const confirmed = await ctx.confirmDangerous({
                title: 'Review PHP-FPM worker change',
                message: 'The change updates this site’s PHP-FPM max_children and reapplies its resource profile. The RSS projection is an estimate, not reserved memory.',
                facts: [
                  ['Current PHP workers', String(profile.php_workers)],
                  ['Proposed PHP workers', String(desiredWorkers)],
                  ...((preview.effects || []).map((effect, index) => [`Effect ${index + 1}`, effect])),
                  ...((preview.recovery_plan || []).map((step, index) => [`Recovery ${index + 1}`, step])),
                  ['Preview expires', new Date(preview.expires_at).toLocaleString()],
                ],
                confirmText: site,
                actionLabel: 'Apply worker change',
              });
              if (!confirmed) { workerOutput.textContent = 'PHP-FPM worker change cancelled.'; return; }
              workerOutput.textContent = 'Applying the reviewed PHP-FPM worker change…';
              await ctx.putJSON(`/api/sites/resources/${encodeURIComponent(site)}`, { ...change, preview_token: preview.preview_token });
              workerOutput.textContent = 'PHP-FPM worker limit applied.';
              renderSettingsTab(site, panel, ctx);
            } catch (error) { workerOutput.textContent = error.message; }
          },
        }, [el('h5', {}, 'Tune PHP-FPM workers'), workers, el('button', { type: 'submit', className: 'primary-action' }, 'Preview worker change'), workerOutput]);
        panel.append(workerForm);
      }
    }

    // Danger zone
    if (ctx.isAdministrator) {
      const dangerOutput = ctx.statusOutput();
      panel.append(
        el('h4', {}, 'Danger zone'),
        el('p', { className: 'import-note' }, 'Terminating a site removes its files, databases, routes, and services after taking a final verified backup. This cannot be undone from the panel.'),
        ctx.button('Terminate site', async () => {
          try {
            dangerOutput.textContent = 'Inspecting proposed changes…';
            const previewResponse = await ctx.postJSON('/api/sites/terminate', { site, confirmation: `DELETE ${site}`, dry_run: true });
            const plan = previewResponse?.plan;
            if (!plan) throw new Error('The server did not return a termination preview.');
            const blockers = (plan.preconditions || []).filter((check) => check.status === 'blocker');
            if (!plan.ready || blockers.length) {
              dangerOutput.textContent = `Termination is blocked: ${blockers.map((check) => `${check.name}: ${check.detail}`).join('; ') || 'preconditions are not satisfied'}.`;
              return;
            }
            const facts = (plan.changes || []).map((change) => {
              const targets = (change.targets || []).join(', ') || 'none found';
              return [change.resource, `${change.action}: ${targets}${change.detail ? ` (${change.detail})` : ''}`];
            });
            facts.push(['Rollback', plan.rollback || 'No rollback guidance is available.']);
            const warning = (plan.preconditions || []).filter((check) => check.status === 'warning').map((check) => `${check.name}: ${check.detail}`).join(' ');
            const confirmed = await ctx.confirmDangerous({
              title: `Review termination of ${site}`,
              message: `${plan.preview_note || 'Review the proposed changes before continuing.'}${warning ? ` ${warning}` : ''}`,
              facts,
              confirmText: `DELETE ${site}`,
              actionLabel: 'Queue termination',
            });
            if (!confirmed) { dangerOutput.textContent = 'Termination cancelled; no changes were made.'; return; }
            dangerOutput.textContent = 'Termination queued…';
            await ctx.postJSON('/api/sites/terminate', { site, confirmation: `DELETE ${site}` });
            dangerOutput.textContent = 'Termination queued. Track progress in Activity.';
          } catch (error) { dangerOutput.textContent = error.message; }
        }, { className: 'danger' }),
        dangerOutput,
      );
    }
    if (!ctx.can('environment:write')) panel.querySelectorAll('button, input, textarea, select').forEach((node) => { node.disabled = true; });
  }

  // ---------------------------------------------------------------------
  // Site grid (list view)
  // ---------------------------------------------------------------------

  async function loadGrid() {
    const [siteData, backupData] = await Promise.all([
      getJSON('/api/sites/overview'),
      getJSON('/api/backups').catch((error) => ({ __error: error, backups: [] })),
    ]);
    grid.replaceChildren();
    let domains = 0;
    for (const site of siteData.sites || []) {
      domains += (site.routes || []).length;
      const backup = latestBackup(backupData.backups, site.site);
      const backupLabel = backupData.__error ? 'Backup status unavailable' : backup ? `Last backup ${formatAge(backup.verified_at).toLowerCase()}` : 'No backup yet';
      const running = (site.applications || []).some((app) => app.state === 'applied' || app.state === 'running');
      const appCount = (site.applications || []).length;
      const extraDomains = Math.max((site.routes || []).length - 1, 0);
      const card = el('article', { className: 'site-card' }, [
        el('div', { className: 'section-heading' }, [el('h3', {}, site.site), appCount ? badge(running ? 'Running' : 'Needs attention', running ? 'ok' : 'warn') : null]),
        el('code', {}, (site.routes || [])[0]?.domain ? `${site.routes[0].domain}${extraDomains ? ` +${extraDomains} more` : ''}` : 'No domain connected'),
        el('p', {}, `${plural(appCount, 'app')} · ${plural(site.database_count || 0, 'database')}`),
        el('p', { className: backup && !backupData.__error ? 'site-card-backup' : 'site-card-backup warning' }, backupLabel),
        el('div', { className: 'site-actions' }, [button('Manage site →', () => openWorkspace(site.site))]),
      ]);
      grid.append(card);
    }
    const setCount = (id, value) => { const node = document.querySelector(id); if (node) node.textContent = String(value); };
    setCount('#siteCount', (siteData.sites || []).length);
    setCount('#domainCount', domains);
    const latest = latestBackup(backupData.backups);
    setCount('#backupFreshness', backupData.__error ? 'Unavailable' : latest ? formatAge(latest.verified_at) : 'Never');
    gridStatus.textContent = siteData.sites && siteData.sites.length
      ? `${plural(siteData.sites.length, 'managed site')}. Select one to open its workspace.`
      : isAdministrator ? 'No managed sites yet. Create a site above, or migrate a cPanel backup.' : 'No sites are assigned to this account yet.';
  }

  // Authorized administrators and tenant owners create sites through a
  // durable job; follow it until it finishes, then show the new site in the
  // grid. The API remains the authority for plan and role enforcement.
  const createForm = document.querySelector('#siteCreateForm');
  if (createForm) {
    const createStatus = createForm.querySelector('#siteCreateStatus');
    const templateSelect = createForm.elements.template;
    const wordpressFields = createForm.querySelector('#wordpressCreateFields');
    const domainInput = createForm.elements.domain;
    const wordpressInputs = ['title', 'admin_user', 'admin_email', 'admin_password'].map((name) => createForm.elements[name]).filter(Boolean);
    const updateTemplateFields = () => {
      const wordpress = templateSelect.value === 'wordpress';
      wordpressFields.hidden = !wordpress;
      domainInput.required = wordpress;
      wordpressInputs.forEach((input) => { input.required = wordpress; });
    };
    templateSelect.addEventListener('change', updateTemplateFields);
    updateTemplateFields();
    const terminal = new Set(['completed', 'failed', 'cancelled', 'dead-letter']);
    createForm.addEventListener('submit', withBusySubmit(async (event) => {
      event.preventDefault();
      const site = createForm.elements.site.value.trim();
      const template = createForm.elements.template.value;
      // Where the site lives in its hosting account (ADR-0007).
      const optional = (name) => (createForm.elements[name] ? createForm.elements[name].value.trim() : '');
      const placement = { account: optional('account'), kind: optional('kind'), domain: optional('domain').toLowerCase() };
      const wordpress = template === 'wordpress' ? {
        title: optional('title'),
        admin_user: optional('admin_user'),
        admin_email: optional('admin_email'),
        admin_password: createForm.elements.admin_password.value,
      } : {};
      if (template === 'wordpress') {
        const confirmed = await confirmDangerous({
          title: `Create WordPress site ${site}?`,
          message: `StePanel will create a dedicated PHP-FPM pool and database for ${placement.domain}, publish its route, configure HTTPS, and create the first database-inclusive recovery point. DNS must already point to this server for public certificate issuance.`,
          confirmText: site,
          actionLabel: 'Create WordPress site',
        });
        if (!confirmed) return;
      }
      createStatus.textContent = `Queuing ${site}…`;
      try {
        const queued = await postJSON('/api/sites', { site, template, ...placement, ...wordpress });
        createStatus.textContent = `Creating ${site}: preparing its account and PHP pool…`;
        for (let attempt = 0; attempt < 90; attempt += 1) {
          await new Promise((resolve) => setTimeout(resolve, 2000));
          const job = await getJSON(`/api/jobs/${encodeURIComponent(queued.job_id)}`);
          if (!terminal.has(job.state)) continue;
          if (job.state === 'completed') {
            const result = job.output || {};
            createStatus.textContent = template === 'wordpress'
              ? `${site} is ready at ${result.domain || placement.domain}. ${result.https || 'HTTPS configuration is active.'} Database: ${result.database || 'created'}. Integrity-verified recovery point: ${result.recovery_point || 'check Backups'}${result.recovery_verified_at ? ` (${new Date(result.recovery_verified_at).toLocaleString()})` : ''}. ${result.recovery_evidence || ''} Backups: ${result.backup_schedule || 'check schedule status'}.`
              : `${site} is ready. Connect a domain from its workspace.`;
            createForm.reset();
            updateTemplateFields();
            await loadGrid();
            openWorkspace(site, 'journey');
          } else {
            createStatus.textContent = `${site} was not created: ${job.error || job.state}. Nothing was left behind.`;
          }
          return;
        }
        createStatus.textContent = `${site} is still being created; follow job ${queued.job_id} under Activity.`;
      } catch (error) {
        createStatus.textContent = error.message;
      }
    }));
  }

  loadGrid()
    .then(() => {
      const initial = parseWorkspaceHash();
      if (initial) openWorkspace(initial.site, initial.tab, { pushHistory: false });
    })
    .catch((error) => { gridStatus.textContent = error.message; });
})();
