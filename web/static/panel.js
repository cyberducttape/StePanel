(() => {
  'use strict';

  // Customer panel (web/panel.html): one view per area, chosen by the URL
  // hash, plus the account summary and the file manager. The per-site
  // workspace (domains, PHP, deployments, databases, backups, logs, tasks)
  // is sites.js, opened with #site=NAME&tab=TAB.

  const api = window.StepanelAPI;
  const ui = window.StepanelUI;
  if (!api || !document.body.classList.contains('customer-panel')) return;

  const accountRole = document.body.dataset.accountRole || 'owner';
  const siteCreationState = document.body.dataset.siteCreationState || 'unavailable';
  const siteLimit = Number(document.body.dataset.siteLimit || 0);
  const roleScopes = {
    developer: new Set(['site:read', 'site:deploy', 'deploy:write', 'environment:read', 'environment:write', 'logs:read', 'backup:read', 'backup:create', 'database:read', 'redis:read', 'files:read', 'files:write']),
    viewer: new Set(['site:read', 'environment:read', 'backup:read', 'database:read', 'redis:read', 'logs:read', 'files:read']),
  };
  const can = (scope) => accountRole === 'owner' || accountRole === 'manager' || (roleScopes[accountRole] || new Set()).has(scope);

  const el = (tag, props = {}, children = []) => {
    const node = document.createElement(tag);
    for (const [key, value] of Object.entries(props)) {
      if (key === 'className') node.className = value;
      else if (key.startsWith('on') && typeof value === 'function') node.addEventListener(key.slice(2).toLowerCase(), value);
      else if (value === true) node.setAttribute(key, '');
      else if (value !== undefined && value !== null && value !== false) node.setAttribute(key, value);
    }
    for (const child of [].concat(children)) {
      if (child === null || child === undefined) continue;
      node.append(child.nodeType ? child : document.createTextNode(String(child)));
    }
    return node;
  };
  const $ = (selector) => document.querySelector(selector);
  const workspaceLink = (site, tab, label) => el('a', { className: 'quiet-action', href: `#site=${encodeURIComponent(site)}&tab=${encodeURIComponent(tab)}` }, label);
  const kindLabel = { main: 'Main domain', addon: 'Addon domain', subdomain: 'Subdomain' };
  const formatBytes = (bytes) => {
    if (bytes < 1024) return `${bytes} B`;
    const units = ['KB', 'MB', 'GB', 'TB'];
    let value = bytes / 1024;
    let unit = 0;
    while (value >= 1024 && unit < units.length - 1) { value /= 1024; unit += 1; }
    return `${value.toFixed(value < 10 ? 1 : 0)} ${units[unit]}`;
  };

  // ---------------------------------------------------------------------
  // Views
  // ---------------------------------------------------------------------

  const views = [...document.querySelectorAll('.panel-view')];
  const navLinks = [...document.querySelectorAll('.workspace-nav a[href^="#"]')];
  const contextTitle = $('#workspaceContextTitle');
  let currentView = '';

  const viewForHash = (hash) => {
    if (hash.startsWith('#site=')) return 'websites';
    const id = hash.slice(1);
    return views.some((view) => view.id === id) ? id : null;
  };

  // Each view is a page of its own: following a view link shows it from the
  // top instead of the browser's jump to the section anchor. A #site= link
  // is left to sites.js, which scrolls to the opened workspace.
  const showView = (id) => {
    if (!id) return;
    currentView = id;
    views.forEach((view) => { view.hidden = view.id !== id; });
    navLinks.forEach((link) => {
      const active = link.getAttribute('href') === `#${id}`;
      link.classList.toggle('is-active', active);
      if (active) {
        link.setAttribute('aria-current', 'page');
        if (contextTitle) contextTitle.textContent = link.textContent.trim();
      } else link.removeAttribute('aria-current');
    });
    if (!location.hash.startsWith('#site=')) window.scrollTo({ top: 0, behavior: 'instant' });
    if (id === 'files') fileManager.activate();
  };

  const route = () => showView(viewForHash(location.hash));
  // The browser's initial jump to the anchor happens after this script runs.
  window.addEventListener('load', () => { if (!location.hash.startsWith('#site=')) window.scrollTo({ top: 0, behavior: 'instant' }); });
  window.addEventListener('hashchange', route);
  window.addEventListener('popstate', route);

  // ---------------------------------------------------------------------
  // Account summary and per-area site lists
  // ---------------------------------------------------------------------

  let hosting = { sites: [] };
  let overviews = new Map();
  let hostingLoaded = false;
  let hostingError = null;
  let overviewError = null;
  let backups = [];
  let backupsLoaded = false;
  let backupsError = null;

  const retryNotice = (message) => el('span', {}, [
    document.createTextNode(`${message} `),
    el('button', { type: 'button', className: 'quiet-action', onClick: () => loadHosting() }, 'Retry'),
  ]);

  const selectSetupTemplate = (template) => {
    if (template !== 'wordpress') return;
    const selector = $('#siteCreateTemplate');
    if (!selector || selector.value === template) return;
    selector.value = template;
    selector.dispatchEvent(new Event('change', { bubbles: true }));
  };
  $('#panelPrimaryAction')?.addEventListener('click', (event) => {
    selectSetupTemplate(event.currentTarget.dataset.setupTemplate);
  });

  const renderSetupChecklist = () => {
    const list = $('#panelOnboardingList');
    const progress = $('#panelOnboardingProgress');
    const primary = $('#panelPrimaryAction');
    if (!list || !progress) return;

    const steps = [];
    if (!hostingLoaded || hostingError) {
      steps.push({
        title: 'Open your websites',
        detail: hostingLoaded
          ? `Could not refresh website assignments: ${hostingError}. The list may be out of date.`
          : `Website assignments could not be checked: ${hostingError || 'still loading'}. Your sites may still exist.`,
        href: '#websites', label: 'Review website status', state: 'unknown',
      });
    } else if (hosting.sites.length) {
      steps.push({
        title: 'Open your website workspace',
        detail: `${ui.plural(hosting.sites.length, 'website')} available. Manage its domain, files, PHP, databases, and application health from one workspace.`,
        href: '#websites', label: 'Open websites', state: 'done',
      });
    } else if (siteCreationState === 'limit') {
      steps.push({
        title: 'Your plan’s website limit is reached',
        detail: `Your plan includes ${siteLimit} website${siteLimit === 1 ? '' : 's'}, and all slots are in use. Contact your hosting operator to review plan capacity or remove an unused website before creating another.`,
        href: '#account', label: 'Review plan usage', state: 'attention',
      });
    } else if (siteCreationState === 'suspended') {
      steps.push({
        title: 'Website creation is unavailable',
        detail: 'This hosting account is suspended. Contact your hosting operator to restore access before creating a website.',
        href: '#account', label: 'Review account access', state: 'attention',
      });
    } else if (siteCreationState === 'unavailable') {
      steps.push({
        title: 'Website eligibility could not be checked',
        detail: 'StePanel could not confirm your plan or site assignments. Refresh this page to retry; contact your hosting operator if this continues.',
        href: '/panel', label: 'Retry status check', state: 'unknown',
      });
    } else if ($('#siteCreateForm')) {
      steps.push({
        title: 'Create your first WordPress site',
        detail: 'StePanel provisions the database, site-owned PHP-FPM pool, HTTPS route, security defaults, and first recovery point.',
        href: '#websites', label: 'Create WordPress site', state: 'next', template: 'wordpress',
      });
    } else {
      steps.push({
        title: 'Get a website assigned',
        detail: 'No websites are assigned to this login. Ask your account owner to add a website or grant access.',
        href: '#account', label: 'View account access', state: 'next',
      });
    }

    if (!hostingLoaded || hostingError || overviewError) {
      steps.push({
        title: 'Connect your domain',
        detail: `Domain routes could not be checked: ${overviewError || hostingError || 'website assignments are still loading'}. A configured route also does not prove public DNS or HTTPS is working.`,
        href: '#websites', label: 'Review domain status', state: 'unknown',
      });
    } else if (!hosting.sites.length && siteCreationState === 'limit') {
      steps.push({
        title: 'Connect your domain',
        detail: 'After the plan has an available website slot, create the site and then check its DNS and HTTPS in the workspace.',
        href: '#account', label: 'Review plan usage', state: 'next',
      });
    } else if (!hosting.sites.length && siteCreationState === 'suspended') {
      steps.push({
        title: 'Connect your domain',
        detail: 'Domain and HTTPS setup will be available after the hosting operator restores account access.',
        href: '#account', label: 'Review account access', state: 'next',
      });
    } else if (!hosting.sites.length && siteCreationState === 'unavailable') {
      steps.push({
        title: 'Connect your domain',
        detail: 'Domain and HTTPS steps will appear after StePanel can confirm your account and site eligibility.',
        href: '/panel', label: 'Retry status check', state: 'unknown',
      });
    } else if (!hosting.sites.length) {
      steps.push({
        title: 'Connect your domain',
        detail: 'After adding a website, connect its domain and check DNS and HTTPS in the site workspace.',
        href: '#websites', label: 'Add a website', state: 'next',
      });
    } else {
      const siteWithoutRoute = hosting.sites.find((site) => {
        const overview = overviews.get(site.site);
        return !overview || !Array.isArray(overview.routes) || overview.routes.length === 0;
      });
      if (siteWithoutRoute) {
        const overview = overviews.get(siteWithoutRoute.site);
        const state = !overview || !Array.isArray(overview.routes) ? 'unknown' : 'next';
        steps.push({
          title: 'Connect your domain',
          detail: state === 'unknown'
            ? `Domain status for ${siteWithoutRoute.site} could not be confirmed. Review the site workspace before assuming it has no domain.`
            : `${siteWithoutRoute.site} has no configured domain route. Connect the domain, then verify DNS and HTTPS in its workspace.`,
          href: `#site=${encodeURIComponent(siteWithoutRoute.site)}&tab=domains`,
          label: state === 'unknown' ? 'Review domain status' : 'Connect a domain',
          state,
        });
      } else {
        const firstSite = hosting.sites[0];
        steps.push({
          title: 'Verify DNS and HTTPS',
          detail: 'Every website has a configured route, but this does not prove public DNS resolves or HTTPS works. Review DNS and certificate evidence in each website workspace.',
          href: `#site=${encodeURIComponent(firstSite.site)}&tab=domains`,
          label: 'Review DNS & HTTPS', state: 'next',
        });
      }
    }

    if (!backupsLoaded) {
      steps.push({
        title: 'Check your recovery point',
        detail: `Backup status could not be checked: ${backupsError || 'still loading'}.`,
        href: '#data', label: 'View backup status', state: 'unknown',
      });
    } else if (!hostingLoaded || hostingError) {
      steps.push({
        title: 'Create a verified backup',
        detail: 'Current website assignments are unavailable, so StePanel cannot safely match backups to your sites.',
        href: '#data', label: 'View backup status', state: 'unknown',
      });
    } else if (!hosting.sites.length) {
      steps.push({
        title: 'Create a verified backup',
        detail: 'After a website is assigned, create a backup and confirm its integrity before risky changes.',
        href: '#websites', label: 'Open websites', state: 'next',
      });
    } else {
      const siteNames = new Set(hosting.sites.map((site) => site.site));
      const verified = backups
        .filter((backup) => siteNames.has(backup.site) && backup.verified_at && Number.isFinite(Date.parse(backup.verified_at)));
      const latest = verified.sort((a, b) => Date.parse(b.verified_at) - Date.parse(a.verified_at))[0];
      const verifiedSites = new Set(verified.map((backup) => backup.site));
      const missingSites = hosting.sites.filter((site) => !verifiedSites.has(site.site));
      const truncated = backups.length >= 100;
      const firstMissing = missingSites[0];
      steps.push({
        title: 'Create a verified backup',
        detail: firstMissing
          ? truncated
            ? `No integrity-verified backup for ${firstMissing.site} appears in the latest 100 results. Check that website's full backup history before treating it as missing.`
            : `No integrity-verified backup is recorded for ${firstMissing.site}. Create one before making high-risk changes.`
          : `Every assigned website has an integrity-verified backup. The newest was verified ${new Date(latest.verified_at).toLocaleString()}. This proves archive integrity, not a complete application restore.`,
        href: firstMissing ? `#site=${encodeURIComponent(firstMissing.site)}&tab=backups` : '#data',
        label: firstMissing ? `Review ${firstMissing.site} backups` : 'Review backups',
        state: firstMissing ? truncated ? 'unknown' : 'next' : 'done',
      });
    }

    list.replaceChildren(...steps.map((step, index) => {
      const action = el('a', { href: step.href }, step.label);
      if (step.template) action.addEventListener('click', () => selectSetupTemplate(step.template));
      return el('li', { className: `panel-setup-step panel-setup-${step.state}` }, [
        el('span', { className: 'panel-setup-marker', 'aria-hidden': 'true' }, step.state === 'done' ? '✓' : String(index + 1)),
        el('div', { className: 'panel-setup-copy' }, [el('strong', {}, step.title), el('p', {}, step.detail)]),
        action,
      ]);
    }));
    const completed = steps.filter((step) => step.state === 'done').length;
    const unresolved = steps.filter((step) => step.state === 'unknown').length;
    const attention = steps.filter((step) => step.state === 'attention').length;
    const pending = [
      attention ? `${attention} need attention` : '',
      unresolved ? `${unresolved} could not be checked` : '',
    ].filter(Boolean).join(' · ');
    progress.textContent = `${completed} of ${steps.length} essentials complete${pending ? ` · ${pending}` : ''}.`;
    if (primary) {
      const next = steps.find((step) => step.state !== 'done') || { href: '#websites', label: 'Open my websites' };
      primary.href = next.href;
      primary.textContent = next.label;
      if (next.template) primary.dataset.setupTemplate = next.template;
      else delete primary.dataset.setupTemplate;
    }
  };

  const siteMeta = (site) => el('div', { className: 'item-meta' }, [
    el('strong', {}, [site.site, site.kind ? el('span', { className: 'panel-kind' }, kindLabel[site.kind] || site.kind) : null]),
    el('small', { className: 'mono' }, site.directory || site.doc_root),
  ]);

  const renderSiteList = (container, actions, empty) => {
    if (!container) return;
    if (!hosting.sites.length) {
      container.replaceChildren(el('li', { className: 'empty-state' }, empty));
      return;
    }
    container.replaceChildren(...hosting.sites.map((site) => el('li', { className: 'resource-list-item' }, [
      siteMeta(site),
      el('div', { className: 'item-actions' }, actions(site)),
    ])));
  };

  const renderSummaries = () => {
    $('#panelAccount').textContent = hosting.account || '—';
    $('#panelUnixUser').textContent = hosting.unix_user || 'Not yet created';
    $('#panelHome').textContent = hosting.home || '—';
    const siteStatus = $('#panelSitesStatus');
    const issues = [];
    if (hostingError) issues.push(`Website assignments could not be refreshed: ${hostingError}`);
    if (overviewError) issues.push(`Domain and database details could not be refreshed: ${overviewError}`);
    siteStatus.hidden = !issues.length;
    siteStatus.replaceChildren(...(issues.length ? [retryNotice(issues.join(' '))] : []));
    renderSiteList($('#panelSiteList'), (site) => [
      site.placed ? el('a', { className: 'quiet-action', href: `#files`, onClick: () => fileManager.select(site.site) }, 'Files') : null,
      workspaceLink(site.site, 'domains', 'Domains'),
      workspaceLink(site.site, 'overview', 'Manage'),
    ], 'You have no websites yet. Add one from Websites.');
    renderSiteList($('#panelDomainSites'), (site) => [workspaceLink(site.site, 'domains', `Connect a domain to ${site.site}`)], 'Add a website first.');
    renderSiteList($('#panelDataSites'), (site) => {
      const overview = overviews.get(site.site);
      return [
        overview ? el('span', { className: 'muted' }, ui.plural(overview.database_count || 0, 'database')) : null,
        workspaceLink(site.site, 'databases', 'Databases'),
        workspaceLink(site.site, 'backups', 'Backups'),
      ];
    }, 'Add a website first.');
    renderSiteList($('#panelRuntimeSites'), (site) => [
      workspaceLink(site.site, 'runtime', 'PHP'),
      workspaceLink(site.site, 'tasks', 'Cron tasks'),
      workspaceLink(site.site, 'logs', 'Logs'),
    ], 'Add a website first.');
    renderDomains();
    renderSetupChecklist();
  };

  const renderDomains = () => {
    const table = $('#panelDomainTable');
    const rows = $('#panelDomainRows');
    const status = $('#panelDomainsStatus');
    const entries = [];
    hosting.sites.forEach((site) => {
      (overviews.get(site.site)?.routes || []).forEach((route) => entries.push({ site, domain: route.domain }));
    });
    entries.sort((a, b) => a.domain.localeCompare(b.domain));
    if (!entries.length) {
      table.hidden = true;
      status.hidden = false;
      if (overviewError) status.replaceChildren(retryNotice(`Domain details are unavailable (${overviewError}); this is not an empty-domain result.`));
      else status.textContent = 'No domains are connected yet.';
      return;
    }
    status.hidden = !overviewError;
    if (overviewError) status.replaceChildren(retryNotice(`Showing the last loaded domain details. Refresh failed: ${overviewError}`));
    table.hidden = false;
    rows.replaceChildren(...entries.map(({ site, domain }) => el('tr', {}, [
      el('td', {}, el('strong', {}, domain)),
      el('td', {}, site.site),
      el('td', { className: 'mono' }, site.directory || site.doc_root),
      el('td', {}, el('div', { className: 'file-actions' }, [el('a', { href: `#site=${encodeURIComponent(site.site)}&tab=domains` }, 'Manage')])),
    ])));
  };

  const loadHosting = async () => {
    const serverRenderedSiteCount = Number(document.body.dataset.accountSiteCount || 0);
    let [accountResult, overviewResult, backupResult] = await Promise.allSettled([
      api.get('/api/account/hosting', { errorMessage: 'Your hosting account is unavailable' }),
      serverRenderedSiteCount > 0
        ? api.get('/api/sites/overview', { errorMessage: 'Website details are unavailable' })
        : Promise.resolve(null),
      serverRenderedSiteCount > 0
        ? api.get('/api/backups?limit=100', { errorMessage: 'Backup status is unavailable' })
        : Promise.resolve(null),
    ]);
    if (accountResult.status === 'fulfilled') {
      hosting = { ...accountResult.value, sites: accountResult.value.sites || [] };
      hostingLoaded = true;
      hostingError = null;
      fileManager.setSites(hosting.sites.filter((site) => site.placed));
      if (hosting.sites.length > 0 && serverRenderedSiteCount === 0) {
        [overviewResult, backupResult] = await Promise.allSettled([
          api.get('/api/sites/overview', { errorMessage: 'Website details are unavailable' }),
          api.get('/api/backups?limit=100', { errorMessage: 'Backup status is unavailable' }),
        ]);
      }
    } else {
      hostingError = accountResult.reason?.message || 'request failed';
    }
    if (!overviewResult || overviewResult.value === null) {
      overviews = new Map();
      overviewError = null;
    } else if (overviewResult.status === 'fulfilled') {
      overviews = new Map((overviewResult.value.sites || []).map((site) => [site.site, site]));
      overviewError = null;
    } else {
      overviewError = overviewResult.reason?.message || 'request failed';
    }
    if (!backupResult || backupResult.value === null) {
      backups = [];
      backupsLoaded = true;
      backupsError = null;
    } else if (backupResult.status === 'fulfilled') {
      backups = Array.isArray(backupResult.value.backups) ? backupResult.value.backups : [];
      backupsLoaded = true;
      backupsError = null;
    } else {
      backupsError = backupResult.reason?.message || 'request failed';
    }
    if (!hostingLoaded) {
      const message = hostingError || 'Your hosting account is unavailable';
      $('#panelSitesStatus').hidden = false;
      $('#panelSitesStatus').replaceChildren(retryNotice(`Website assignments are unavailable: ${message}`));
      $('#panelDomainsStatus').hidden = false;
      $('#panelDomainsStatus').replaceChildren(retryNotice(`Domain inventory is unavailable: ${message}`));
      fileManager.unavailable(message);
      renderSetupChecklist();
      return;
    }
    renderSummaries();
  };

  // ---------------------------------------------------------------------
  // File manager
  // ---------------------------------------------------------------------

  const fileManager = (() => {
    const siteSelect = $('#fileSite');
    const breadcrumbs = $('#fileBreadcrumbs');
    const toolbar = $('#fileToolbar');
    const status = $('#fileStatus');
    const table = $('#fileTable');
    const rows = $('#fileRows');
    const nameForm = $('#fileNameForm');
    const nameInput = $('#fileNameInput');
    const nameLabel = $('#fileNameLabel');
    const nameSubmit = $('#fileNameSubmit');
    const upload = $('#fileUpload');
    const editor = $('#fileEditor');
    const editorHeading = $('#fileEditorHeading');
    const editorText = $('#fileEditorText');
    const editorStatus = $('#fileEditorStatus');
    const editorSave = $('#fileEditorSave');
    const editorReload = $('#fileEditorReload');
    const sftpGuide = $('#fileSftpGuide');
    const sftpUsername = $('#fileSftpUsername');
    const sftpRoot = $('#fileSftpRoot');
    const sftpSettings = $('#fileSftpSettings');
    const MAX_WRITE = 4 * 1024 * 1024;
    const MAX_RESUMABLE_UPLOAD = 100 * 1024 * 1024;
    const UPLOAD_CHUNK = 4 * 1024 * 1024;

    const sha256File = async (file) => {
      const bytes = await file.arrayBuffer();
      const digest = await crypto.subtle.digest('SHA-256', bytes);
      return [...new Uint8Array(digest)].map((value) => value.toString(16).padStart(2, '0')).join('');
    };

    const resumableFileUpload = async (file, context) => {
      if (file.size > MAX_RESUMABLE_UPLOAD) throw new Error('Files larger than 100 MiB are not supported by the browser file manager.');
      if (!crypto?.subtle) throw new Error('This browser cannot verify file checksums. Use a current browser over HTTPS.');
      const digest = await sha256File(file);
      const target = join(context.path, file.name);
      const resumeKey = `stepanel-upload:${context.site}:${target}:${file.size}:${file.lastModified}`;
      let session = null;
      const previous = sessionStorage.getItem(resumeKey);
      if (previous) {
        try {
          const candidate = await api.get(`${base(context.site)}/uploads/${encodeURIComponent(previous)}`);
          if (candidate.path === target && candidate.size === file.size && candidate.sha256 === digest) session = candidate;
          else sessionStorage.removeItem(resumeKey);
        } catch (_) { sessionStorage.removeItem(resumeKey); }
      }
      if (!session) {
        session = await api.request(`${base(context.site)}/uploads`, {
          method: 'POST', json: { path: target, size: file.size, sha256: digest },
          errorMessage: `Could not start upload of ${file.name}`,
        });
        sessionStorage.setItem(resumeKey, session.id);
      }
      while (session.offset < file.size) {
        if (!isCurrent(context)) throw new Error('Website or folder changed. The upload is saved and can resume when you select the same file again.');
        const start = session.offset;
        const end = Math.min(start + UPLOAD_CHUNK, file.size);
        const chunk = file.slice(start, end);
        setStatus(`Uploading ${file.name}: ${Math.floor(start * 100 / file.size)}% (${ui.formatBytes ? ui.formatBytes(start) : Math.round(start / 1048576) + ' MB'} of ${Math.ceil(file.size / 1048576)} MB)…`, 'pending');
        try {
          session = await api.request(`${base(context.site)}/uploads/${encodeURIComponent(session.id)}`, {
            method: 'PUT', body: chunk, headers: { 'Upload-Offset': String(start) },
            errorMessage: `Could not upload ${file.name}`,
          });
        } catch (error) {
          // A chunk can be stored even when its response is lost. Resync with
          // the server's durable offset before treating the failure as fatal.
          let current = null;
          try { current = await api.get(`${base(context.site)}/uploads/${encodeURIComponent(session.id)}`); } catch (_) { /* report the original failure */ }
          if (!current || current.id !== session.id || current.offset <= start) throw error;
          session = current;
        }
      }
      await api.request(`${base(context.site)}/uploads/${encodeURIComponent(session.id)}/complete`, {
        method: 'POST', json: {}, errorMessage: `Could not finish ${file.name}`,
      });
      sessionStorage.removeItem(resumeKey);
    };
    const writable = can('files:write');

    let sites = [];
    let site = '';
    let path = '';
    let editing = null;
    let pendingName = null;
    let loaded = false;
    let token = 0;
    let editorRequest = 0;
    let contextGeneration = 0;

    const base = (siteName = site) => `/api/sites/files/${encodeURIComponent(siteName)}`;
    const join = (dir, name) => (dir ? `${dir}/${name}` : name);
    const docRoot = () => (sites.find((entry) => entry.site === site) || {}).doc_root || '';
    const updateSFTPGuide = () => {
      const selected = sites.find((entry) => entry.site === site);
      const username = String(hosting.unix_user || '').trim();
      if (!sftpGuide) return;
      sftpGuide.hidden = !selected || !username;
      if (!selected || !username) return;
      sftpUsername.textContent = username;
      sftpRoot.textContent = selected.doc_root || '';
      sftpSettings.href = `#site=${encodeURIComponent(selected.site)}&tab=security`;
    };
    const revealSFTPGuide = () => {
      updateSFTPGuide();
      if (sftpGuide && !sftpGuide.hidden) {
        sftpGuide.open = true;
        sftpGuide.scrollIntoView({ block: 'nearest' });
      }
    };
    const setStatus = (message, state = '') => ui.setStatus(status, message, state);
    const isCurrent = (context) => Boolean(context && context.token === token && context.generation === contextGeneration && context.site === site && context.path === path);
    const isCurrentEditor = (session) => Boolean(session && session.generation === contextGeneration && session.site === site && session.directory === path);

    if (!writable) {
      ['#fileNewFolder', '#fileNewFile'].forEach((selector) => { $(selector).hidden = true; });
      $('.file-upload-label').hidden = true;
      editorSave.hidden = true;
      editorText.readOnly = true;
    }

    const renderBreadcrumbs = () => {
      const parts = path ? path.split('/') : [];
      const crumbs = [el('button', { type: 'button', onClick: () => open(''), 'aria-current': parts.length ? null : 'location' }, docRoot() || '/')];
      parts.forEach((part, index) => {
        crumbs.push(el('span', { className: 'separator', 'aria-hidden': 'true' }, '/'));
        const target = parts.slice(0, index + 1).join('/');
        crumbs.push(el('button', { type: 'button', onClick: () => open(target), 'aria-current': index === parts.length - 1 ? 'location' : null }, part));
      });
      breadcrumbs.replaceChildren(...crumbs);
    };

    const icon = (type) => ({ dir: '▸', file: '·', link: '↪' }[type] || '?');

    const row = (entry, context) => {
      const target = join(context.path, entry.name);
      const nameButton = el('button', {
        type: 'button', className: 'file-name', 'data-type': entry.type,
        disabled: entry.type !== 'dir' && entry.type !== 'file',
        onClick: () => {
          if (!isCurrent(context)) return;
          if (entry.type === 'dir') open(target, context.site);
          else edit(target, context);
        },
      }, [el('span', { className: 'file-icon', 'aria-hidden': 'true' }, icon(entry.type)), entry.name]);
      const actions = [];
      if (entry.type === 'file') {
        actions.push(el('a', { href: `${base(context.site)}/content?path=${encodeURIComponent(target)}&download=1`, download: entry.name, 'aria-label': `Download ${entry.name}` }, 'Download'));
      }
      if (writable) {
        actions.push(el('button', { type: 'button', 'aria-label': `Rename ${entry.name}`, onClick: () => askName('rename', entry, context) }, 'Rename'));
        actions.push(el('button', { type: 'button', className: 'danger', 'aria-label': `Delete ${entry.name}`, onClick: () => remove(entry, context) }, 'Delete'));
      }
      return el('tr', {}, [
        el('td', {}, nameButton),
        el('td', {}, entry.type === 'dir' ? '—' : formatBytes(entry.size)),
        el('td', {}, el('time', { datetime: entry.mod_time }, new Date(entry.mod_time).toLocaleString())),
        el('td', { className: 'mono' }, entry.mode),
        el('td', {}, el('div', { className: 'file-actions' }, actions)),
      ]);
    };

    async function open(dir, targetSite = site) {
      if (!targetSite) return;
      const changed = targetSite !== site || dir !== path;
      if (changed) {
        contextGeneration++;
        closeEditor();
      }
      site = targetSite;
      path = dir;
      renderBreadcrumbs();
      hideNameForm();
      const current = ++token;
      const context = { site: targetSite, path: dir, token: current, generation: contextGeneration };
      rows.replaceChildren();
      table.hidden = true;
      toolbar.inert = true;
      toolbar.setAttribute('aria-busy', 'true');
      setStatus('Loading folder…', 'pending');
      try {
        const listing = await api.get(`${base(targetSite)}?path=${encodeURIComponent(dir)}`, { errorMessage: 'Could not list this folder' });
        if (!isCurrent(context)) return;
        const entries = listing.entries || [];
        rows.replaceChildren(...entries.map((entry) => row(entry, context)));
        table.hidden = !entries.length;
        loaded = true;
        if (!entries.length) setStatus('This folder is empty.');
        else setStatus(listing.truncated ? `Showing the first ${entries.length} entries; use SFTP for the rest.` : ui.plural(entries.length, 'item'));
      } catch (error) {
        if (!isCurrent(context)) return;
        // Keep the error visible in this view, but let the next visit retry
        // automatically instead of requiring the user to discover Refresh.
        loaded = false;
        rows.replaceChildren();
        table.hidden = true;
        setStatus(error.message, 'error');
      } finally {
        if (isCurrent(context)) {
          toolbar.inert = false;
          toolbar.removeAttribute('aria-busy');
        }
      }
    }

    async function edit(target, context = { site, path, token, generation: contextGeneration }) {
      if (!isCurrent(context)) return;
      const request = ++editorRequest;
      editing = null;
      editor.hidden = false;
      editorHeading.textContent = target;
      editorText.value = '';
      editorText.disabled = true;
      editorSave.disabled = true;
      editorReload.hidden = true;
      ui.setStatus(editorStatus, 'Opening file…', 'pending');
      try {
        const file = await api.get(`${base(context.site)}/content?path=${encodeURIComponent(target)}`, { errorMessage: 'Could not open this file' });
        if (request !== editorRequest || !isCurrent(context)) return;
        if (!file.text) {
          ui.setStatus(editorStatus, 'This is a binary file; download it to view it.', 'error');
          return;
        }
        editing = { site: context.site, path: target, directory: context.path, generation: context.generation, revision: file.revision || '' };
        editorText.value = file.content;
        editorText.disabled = false;
        editorSave.disabled = !writable;
        ui.setStatus(editorStatus, `${formatBytes(file.size)} · ${writable ? 'saving replaces the file and keeps its permissions' : 'read only'}`);
        editorText.focus();
      } catch (error) {
        if (request !== editorRequest || !isCurrent(context)) return;
        ui.setStatus(editorStatus, error.message, 'error');
      }
    }

    const closeEditor = () => {
      editorRequest++;
      editing = null;
      editor.hidden = true;
      editorText.value = '';
      editorReload.hidden = true;
    };

    const save = async () => {
      if (!editing) return;
      const session = editing;
      if (!isCurrentEditor(session)) {
        closeEditor();
        ui.setStatus(editorStatus, 'The website or folder changed. Reopen the file before saving.', 'error');
        return;
      }
      const body = new Blob([editorText.value], { type: 'application/octet-stream' });
      if (body.size > MAX_WRITE) {
        revealSFTPGuide();
        ui.setStatus(
          editorStatus,
          sftpGuide && !sftpGuide.hidden
            ? 'The file is larger than 4 MiB. SFTP details are shown below.'
            : 'The file is larger than 4 MiB. Contact your hosting administrator to arrange SFTP access.',
          'error',
        );
        return;
      }
      editorSave.disabled = true;
      ui.setStatus(editorStatus, 'Saving…', 'pending');
      try {
        await api.request(`${base(session.site)}/content?path=${encodeURIComponent(session.path)}`, {
          method: 'PUT', body, headers: session.revision ? { 'If-Match': session.revision } : {},
          errorMessage: 'Could not save the file',
        });
        ui.setStatus(editorStatus, `Saved ${session.path}.`, 'success');
        if (isCurrentEditor(session)) open(session.directory, session.site);
      } catch (error) {
        ui.setStatus(editorStatus, error.message, 'error');
        editorReload.hidden = error.kind !== 'conflict' || !isCurrentEditor(session);
      } finally {
        editorSave.disabled = false;
      }
    };

    editorReload.addEventListener('click', () => {
      const session = editing;
      if (!isCurrentEditor(session)) return;
      edit(session.path, { site: session.site, path: session.directory, token, generation: session.generation });
    });

    // The name form serves "new folder", "new file" and "rename".
    const askName = (mode, entry = null, context = { site, path, token, generation: contextGeneration }) => {
      if (!isCurrent(context)) return;
      pendingName = { mode, entry, context: { ...context } };
      nameForm.hidden = false;
      nameLabel.textContent = { folder: 'New folder name', file: 'New file name', rename: `New name for ${entry?.name}` }[mode];
      nameSubmit.textContent = mode === 'rename' ? 'Rename' : 'Create';
      nameInput.value = entry ? entry.name : '';
      nameInput.focus();
      nameInput.select();
    };
    function hideNameForm() {
      pendingName = null;
      nameForm.hidden = true;
    }

    const validName = (name) => name && name !== '.' && name !== '..' && !name.includes('/') && !/[\u0000-\u001f\u007f]/.test(name);

    nameForm.addEventListener('submit', async (event) => {
      event.preventDefault();
      if (!pendingName) return;
      const name = nameInput.value.trim();
      if (!validName(name)) {
        setStatus('Use a name without slashes or control characters.', 'error');
        return;
      }
      const { mode, entry, context } = pendingName;
      if (!isCurrent(context)) {
        hideNameForm();
        setStatus('The website or folder changed. Choose the action again.', 'error');
        return;
      }
      nameSubmit.disabled = true;
      try {
        let done;
        if (mode === 'folder') {
          await api.post(`${base(context.site)}/mkdir`, { path: join(context.path, name) }, { errorMessage: 'Could not create the folder' });
          done = `Created ${name}.`;
        } else if (mode === 'file') {
          await api.request(`${base(context.site)}/content?path=${encodeURIComponent(join(context.path, name))}`, { method: 'PUT', body: new Blob([]), errorMessage: 'Could not create the file' });
          done = `Created ${name}.`;
        } else {
          await api.post(`${base(context.site)}/rename`, { path: join(context.path, entry.name), destination: join(context.path, name) }, { errorMessage: 'Could not rename' });
          done = `Renamed ${entry.name} to ${name}.`;
        }
        hideNameForm();
        if (isCurrent(context)) {
          await open(context.path, context.site);
          setStatus(done, 'success');
          if (mode === 'file') edit(join(context.path, name));
        }
      } catch (error) {
        setStatus(error.message, 'error');
      } finally {
        nameSubmit.disabled = false;
      }
    });
    $('#fileNameCancel').addEventListener('click', hideNameForm);

    const remove = async (entry, context) => {
      if (!isCurrent(context)) return;
      const confirmed = await ui.confirmDangerous({
        title: `Delete ${entry.name}?`,
        message: entry.type === 'dir' ? 'The folder and everything in it are deleted permanently. Take a backup first if you may need them.' : 'The file is deleted permanently. Take a backup first if you may need it.',
        facts: [['Website', context.site], ['Path', join(context.path, entry.name)]],
        confirmText: entry.name,
        actionLabel: 'Delete',
      });
      if (!confirmed) return;
      if (!isCurrent(context)) {
        setStatus('The website or folder changed. Choose the action again.', 'error');
        return;
      }
      try {
        await api.post(`${base(context.site)}/delete`, { path: join(context.path, entry.name) }, { errorMessage: 'Could not delete' });
        if (editing && (editing.site === context.site && (editing.path === join(context.path, entry.name) || editing.path.startsWith(`${join(context.path, entry.name)}/`)))) closeEditor();
        if (isCurrent(context)) await open(context.path, context.site);
        setStatus(`Deleted ${entry.name}.`, 'success');
      } catch (error) {
        setStatus(error.message, 'error');
      }
    };

    upload.addEventListener('change', async () => {
      const files = [...upload.files];
      const uploadContext = { site, path, token, generation: contextGeneration };
      upload.value = '';
      for (const file of files) {
        if (file.size > MAX_RESUMABLE_UPLOAD) {
          revealSFTPGuide();
          setStatus(
            sftpGuide && !sftpGuide.hidden
              ? `${file.name} exceeds the 100 MiB browser upload limit. SFTP details are shown below.`
              : `${file.name} exceeds the 100 MiB browser upload limit. Contact your hosting administrator to arrange SFTP access.`,
            'error',
          );
          return;
        }
        if (!validName(file.name)) {
          setStatus(`${file.name} has a name the file manager cannot use.`, 'error');
          return;
        }
      }
      for (const [index, file] of files.entries()) {
        setStatus(`Uploading ${file.name} (${index + 1} of ${files.length})…`, 'pending');
        try {
          if (file.size <= MAX_WRITE) {
            await api.request(`${base(uploadContext.site)}/content?path=${encodeURIComponent(join(uploadContext.path, file.name))}`, { method: 'PUT', body: file, errorMessage: `Could not upload ${file.name}` });
          } else {
            await resumableFileUpload(file, uploadContext);
          }
        } catch (error) {
          setStatus(error.message, 'error');
          if (isCurrent(uploadContext)) open(uploadContext.path, uploadContext.site);
          return;
        }
      }
      if (isCurrent(uploadContext)) await open(uploadContext.path, uploadContext.site);
      if (files.length) setStatus(`Uploaded ${ui.plural(files.length, 'file')}.`, 'success');
    });

    $('#fileNewFolder').addEventListener('click', () => askName('folder'));
    $('#fileNewFile').addEventListener('click', () => askName('file'));
    $('#fileRefresh').addEventListener('click', () => open(path));
    editorSave.addEventListener('click', save);
    $('#fileEditorClose').addEventListener('click', closeEditor);
    editorText.addEventListener('keydown', (event) => {
      if ((event.ctrlKey || event.metaKey) && event.key === 's') {
        event.preventDefault();
        save();
      }
    });
    siteSelect.addEventListener('change', () => {
      open('', siteSelect.value);
      updateSFTPGuide();
    });

    return {
      setSites(list) {
        sites = list;
        siteSelect.replaceChildren(...sites.map((entry) => el('option', { value: entry.site }, `${entry.site}${entry.kind ? ` · ${kindLabel[entry.kind] || entry.kind}` : ''}`)));
        if (!sites.length) {
          if (site) contextGeneration++;
          token++;
          site = '';
          path = '';
          closeEditor();
          hideNameForm();
          rows.replaceChildren();
          table.hidden = true;
          siteSelect.disabled = true;
          toolbar.hidden = true;
          setStatus('None of your websites are in your home directory yet, so there are no files to manage here.');
          updateSFTPGuide();
          return;
        }
        if (!sites.some((entry) => entry.site === site)) {
          site = sites[0].site;
          path = '';
          contextGeneration++;
          token++;
          closeEditor();
          hideNameForm();
          rows.replaceChildren();
          table.hidden = true;
        }
        siteSelect.disabled = false;
        toolbar.hidden = false;
        siteSelect.value = site;
        updateSFTPGuide();
        if (currentView === 'files') this.activate();
      },
      // Opens a website's root the next time the Files view is shown.
      select(name) {
        if (!sites.some((entry) => entry.site === name)) return;
        site = name;
        path = '';
        contextGeneration++;
        token++;
        siteSelect.value = name;
        updateSFTPGuide();
        closeEditor();
        hideNameForm();
        rows.replaceChildren();
        table.hidden = true;
        loaded = false;
      },
      activate() {
        if (!sites.length || loaded) return;
        loaded = true;
        open(path);
      },
      unavailable(message) {
        token++;
        closeEditor();
        rows.replaceChildren();
        table.hidden = true;
        siteSelect.disabled = true;
        toolbar.hidden = true;
        setStatus(message, 'error');
      },
    };
  })();

  showView(viewForHash(location.hash) || 'home');
  $('#panelOnboardingRefresh')?.addEventListener('click', loadHosting);
  window.addEventListener('hashchange', () => {
    if (viewForHash(location.hash) === 'home') loadHosting();
  });
  loadHosting();
})();
