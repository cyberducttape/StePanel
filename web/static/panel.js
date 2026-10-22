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

  const siteMeta = (site) => el('div', { className: 'item-meta' }, [
    el('strong', {}, [site.site, site.kind ? el('span', { className: 'panel-kind' }, kindLabel[site.kind] || site.kind) : null]),
    el('small', { className: 'mono' }, site.doc_root),
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
    $('#panelSitesStatus').hidden = true;
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
      status.textContent = 'No domains are connected yet.';
      return;
    }
    status.hidden = true;
    table.hidden = false;
    rows.replaceChildren(...entries.map(({ site, domain }) => el('tr', {}, [
      el('td', {}, el('strong', {}, domain)),
      el('td', {}, site.site),
      el('td', { className: 'mono' }, site.doc_root),
      el('td', {}, el('div', { className: 'file-actions' }, [el('a', { href: `#site=${encodeURIComponent(site.site)}&tab=domains` }, 'Manage')])),
    ])));
  };

  const loadHosting = async () => {
    try {
      const [account, overview] = await Promise.all([
        api.get('/api/account/hosting', { errorMessage: 'Your hosting account is unavailable' }),
        api.get('/api/sites/overview', { errorMessage: 'Website details are unavailable' }).catch(() => ({ sites: [] })),
      ]);
      hosting = { ...account, sites: account.sites || [] };
      overviews = new Map((overview.sites || []).map((site) => [site.site, site]));
      renderSummaries();
      fileManager.setSites(hosting.sites.filter((site) => site.placed));
    } catch (error) {
      $('#panelSitesStatus').textContent = error.message;
      $('#panelDomainsStatus').textContent = error.message;
      fileManager.unavailable(error.message);
    }
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
    const MAX_WRITE = 4 * 1024 * 1024;
    const writable = can('files:write');

    let sites = [];
    let site = '';
    let path = '';
    let editing = null;
    let pendingName = null;
    let loaded = false;
    let token = 0;

    const base = () => `/api/sites/files/${encodeURIComponent(site)}`;
    const join = (dir, name) => (dir ? `${dir}/${name}` : name);
    const docRoot = () => (sites.find((entry) => entry.site === site) || {}).doc_root || '';
    const setStatus = (message, state = '') => ui.setStatus(status, message, state);

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

    const row = (entry) => {
      const target = join(path, entry.name);
      const nameButton = el('button', {
        type: 'button', className: 'file-name', 'data-type': entry.type,
        disabled: entry.type !== 'dir' && entry.type !== 'file',
        onClick: () => (entry.type === 'dir' ? open(target) : edit(target)),
      }, [el('span', { className: 'file-icon', 'aria-hidden': 'true' }, icon(entry.type)), entry.name]);
      const actions = [];
      if (entry.type === 'file') {
        actions.push(el('a', { href: `${base()}/content?path=${encodeURIComponent(target)}&download=1`, download: entry.name, 'aria-label': `Download ${entry.name}` }, 'Download'));
      }
      if (writable) {
        actions.push(el('button', { type: 'button', 'aria-label': `Rename ${entry.name}`, onClick: () => askName('rename', entry) }, 'Rename'));
        actions.push(el('button', { type: 'button', className: 'danger', 'aria-label': `Delete ${entry.name}`, onClick: () => remove(entry) }, 'Delete'));
      }
      return el('tr', {}, [
        el('td', {}, nameButton),
        el('td', {}, entry.type === 'dir' ? '—' : formatBytes(entry.size)),
        el('td', {}, el('time', { datetime: entry.mod_time }, new Date(entry.mod_time).toLocaleString())),
        el('td', { className: 'mono' }, entry.mode),
        el('td', {}, el('div', { className: 'file-actions' }, actions)),
      ]);
    };

    async function open(dir) {
      if (!site) return;
      path = dir;
      renderBreadcrumbs();
      hideNameForm();
      const current = ++token;
      setStatus('Loading folder…', 'pending');
      try {
        const listing = await api.get(`${base()}?path=${encodeURIComponent(dir)}`, { errorMessage: 'Could not list this folder' });
        if (current !== token) return;
        const entries = listing.entries || [];
        rows.replaceChildren(...entries.map(row));
        table.hidden = !entries.length;
        if (!entries.length) setStatus('This folder is empty.');
        else setStatus(listing.truncated ? `Showing the first ${entries.length} entries; use SFTP for the rest.` : ui.plural(entries.length, 'item'));
      } catch (error) {
        if (current !== token) return;
        rows.replaceChildren();
        table.hidden = true;
        setStatus(error.message, 'error');
      }
    }

    async function edit(target) {
      editing = null;
      editor.hidden = false;
      editorHeading.textContent = target;
      editorText.value = '';
      editorText.disabled = true;
      editorSave.disabled = true;
      ui.setStatus(editorStatus, 'Opening file…', 'pending');
      try {
        const file = await api.get(`${base()}/content?path=${encodeURIComponent(target)}`, { errorMessage: 'Could not open this file' });
        if (!file.text) {
          ui.setStatus(editorStatus, 'This is a binary file; download it to view it.', 'error');
          return;
        }
        editing = target;
        editorText.value = file.content;
        editorText.disabled = false;
        editorSave.disabled = !writable;
        ui.setStatus(editorStatus, `${formatBytes(file.size)} · ${writable ? 'saving replaces the file and keeps its permissions' : 'read only'}`);
        editorText.focus();
      } catch (error) {
        ui.setStatus(editorStatus, error.message, 'error');
      }
    }

    const closeEditor = () => {
      editing = null;
      editor.hidden = true;
      editorText.value = '';
    };

    const save = async () => {
      if (!editing) return;
      const body = new Blob([editorText.value], { type: 'application/octet-stream' });
      if (body.size > MAX_WRITE) {
        ui.setStatus(editorStatus, 'The file is larger than 4 MB; upload it over SFTP instead.', 'error');
        return;
      }
      editorSave.disabled = true;
      ui.setStatus(editorStatus, 'Saving…', 'pending');
      try {
        await api.request(`${base()}/content?path=${encodeURIComponent(editing)}`, { method: 'PUT', body, errorMessage: 'Could not save the file' });
        ui.setStatus(editorStatus, `Saved ${editing}.`, 'success');
        open(path);
      } catch (error) {
        ui.setStatus(editorStatus, error.message, 'error');
      } finally {
        editorSave.disabled = false;
      }
    };

    // The name form serves "new folder", "new file" and "rename".
    const askName = (mode, entry = null) => {
      pendingName = { mode, entry };
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
      const { mode, entry } = pendingName;
      nameSubmit.disabled = true;
      try {
        let done;
        if (mode === 'folder') {
          await api.post(`${base()}/mkdir`, { path: join(path, name) }, { errorMessage: 'Could not create the folder' });
          done = `Created ${name}.`;
        } else if (mode === 'file') {
          await api.request(`${base()}/content?path=${encodeURIComponent(join(path, name))}`, { method: 'PUT', body: new Blob([]), errorMessage: 'Could not create the file' });
          done = `Created ${name}.`;
        } else {
          await api.post(`${base()}/rename`, { path: join(path, entry.name), destination: join(path, name) }, { errorMessage: 'Could not rename' });
          done = `Renamed ${entry.name} to ${name}.`;
        }
        hideNameForm();
        await open(path);
        setStatus(done, 'success');
        if (mode === 'file') edit(join(path, name));
      } catch (error) {
        setStatus(error.message, 'error');
      } finally {
        nameSubmit.disabled = false;
      }
    });
    $('#fileNameCancel').addEventListener('click', hideNameForm);

    const remove = async (entry) => {
      const confirmed = await ui.confirmDangerous({
        title: `Delete ${entry.name}?`,
        message: entry.type === 'dir' ? 'The folder and everything in it are deleted permanently. Take a backup first if you may need them.' : 'The file is deleted permanently. Take a backup first if you may need it.',
        facts: [['Website', site], ['Path', join(path, entry.name)]],
        confirmText: entry.name,
        actionLabel: 'Delete',
      });
      if (!confirmed) return;
      try {
        await api.post(`${base()}/delete`, { path: join(path, entry.name) }, { errorMessage: 'Could not delete' });
        if (editing && (editing === join(path, entry.name) || editing.startsWith(`${join(path, entry.name)}/`))) closeEditor();
        await open(path);
        setStatus(`Deleted ${entry.name}.`, 'success');
      } catch (error) {
        setStatus(error.message, 'error');
      }
    };

    upload.addEventListener('change', async () => {
      const files = [...upload.files];
      upload.value = '';
      for (const file of files) {
        if (file.size > MAX_WRITE) {
          setStatus(`${file.name} is larger than 4 MB; upload it over SFTP instead.`, 'error');
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
          await api.request(`${base()}/content?path=${encodeURIComponent(join(path, file.name))}`, { method: 'PUT', body: file, errorMessage: `Could not upload ${file.name}` });
        } catch (error) {
          setStatus(error.message, 'error');
          open(path);
          return;
        }
      }
      await open(path);
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
      site = siteSelect.value;
      closeEditor();
      open('');
    });

    return {
      setSites(list) {
        sites = list;
        siteSelect.replaceChildren(...sites.map((entry) => el('option', { value: entry.site }, `${entry.site}${entry.kind ? ` · ${kindLabel[entry.kind] || entry.kind}` : ''}`)));
        if (!sites.length) {
          siteSelect.disabled = true;
          toolbar.hidden = true;
          setStatus('None of your websites are in your home directory yet, so there are no files to manage here.');
          return;
        }
        if (!sites.some((entry) => entry.site === site)) site = sites[0].site;
        siteSelect.value = site;
        if (currentView === 'files') this.activate();
      },
      // Opens a website's root the next time the Files view is shown.
      select(name) {
        if (!sites.some((entry) => entry.site === name)) return;
        site = name;
        siteSelect.value = name;
        closeEditor();
        loaded = false;
      },
      activate() {
        if (!sites.length || loaded) return;
        loaded = true;
        open('');
      },
      unavailable(message) {
        siteSelect.disabled = true;
        toolbar.hidden = true;
        setStatus(message, 'error');
      },
    };
  })();

  showView(viewForHash(location.hash) || 'home');
  loadHosting();
})();
