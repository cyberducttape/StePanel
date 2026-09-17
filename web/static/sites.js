(() => {
  'use strict';

  const grid = document.querySelector('#siteOverview');
  const gridStatus = document.querySelector('#siteOverviewStatus');
  const detail = document.querySelector('#siteDetail');
  if (!grid || !gridStatus || !detail) return;

  const isAdministrator = document.body.dataset.isAdministrator === 'true';
  const webserver = document.body.dataset.webserver || 'caddy';

  // ---------------------------------------------------------------------
  // Shared request helpers
  // ---------------------------------------------------------------------

  const csrfToken = () => {
    const match = document.cookie.match(/(?:^|; )stepanel_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  };

  const readResponse = async (response) => {
    const body = await response.text();
    let data = {};
    try { data = body ? JSON.parse(body) : {}; } catch (error) { /* not JSON */ }
    if (!response.ok) throw new Error(data.error || body || `Request failed (${response.status})`);
    return data;
  };

  const getJSON = (path) => fetch(path).then(readResponse);

  const mutate = (method, path, body) => fetch(path, {
    method,
    headers: body !== undefined
      ? { 'Content-Type': 'application/json', 'X-CSRF-Token': csrfToken() }
      : { 'X-CSRF-Token': csrfToken() },
    body: body !== undefined ? JSON.stringify(body) : undefined,
  }).then(readResponse);

  const postJSON = (path, body) => mutate('POST', path, body ?? {});
  const putJSON = (path, body) => mutate('PUT', path, body ?? {});
  const patchJSON = (path, body) => mutate('PATCH', path, body ?? {});
  const deleteJSON = (path) => mutate('DELETE', path);

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

  // A typed-confirmation dialog for destructive actions. Shows the object's
  // context (what it is, what it's used by, when it was last verified)
  // instead of a bare "are you sure?" prompt. When `extraField` is given
  // (e.g. collecting a staging domain), the dialog resolves to
  // { confirmed, value } instead of a plain boolean so the caller can read
  // what was typed without a separate native prompt() breaking the flow.
  const confirmDangerous = ({ title, message, facts = [], confirmText, actionLabel = 'Confirm', extraField = null }) => new Promise((resolve) => {
    const dialog = el('dialog', { className: 'confirm-dialog' });
    const inputId = `confirm-input-${Math.random().toString(36).slice(2, 8)}`;
    const input = el('input', { id: inputId, type: 'text', autocomplete: 'off', required: true });
    const confirmButton = el('button', { type: 'submit', className: 'confirm-dialog-danger', disabled: true }, actionLabel);
    let extraInput = null;
    const updateConfirmState = () => {
      confirmButton.disabled = input.value !== confirmText || (extraField ? !extraInput.value.trim() : false);
    };
    input.addEventListener('input', updateConfirmState);
    const extraFieldNode = extraField ? (() => {
      const extraId = `confirm-extra-${Math.random().toString(36).slice(2, 8)}`;
      extraInput = el('input', { id: extraId, type: extraField.type || 'text', placeholder: extraField.placeholder || '', required: true });
      extraInput.addEventListener('input', updateConfirmState);
      return el('div', { className: 'field' }, [
        el('label', { for: extraId }, extraField.label),
        extraInput,
        extraField.hint ? el('small', { className: 'hint' }, extraField.hint) : null,
      ]);
    })() : null;
    const form = el('form', { method: 'dialog', onSubmit: () => { dialog.close('confirm'); } }, [
      el('div', { className: 'confirm-dialog-body' }, [
        el('h3', {}, title),
        el('p', {}, message),
        facts.length ? el('div', { className: 'confirm-dialog-facts' }, facts.map(([label, value]) => el('div', {}, [label, el('strong', {}, value)]))) : null,
        extraFieldNode,
        el('div', { className: 'field' }, [el('label', { for: inputId }, `Type ${confirmText} to confirm`), input]),
      ]),
      el('div', { className: 'confirm-dialog-actions' }, [
        el('button', { type: 'button', className: 'confirm-dialog-cancel', onClick: () => dialog.close('cancel') }, 'Cancel'),
        confirmButton,
      ]),
    ]);
    dialog.append(form);
    dialog.addEventListener('close', () => {
      const confirmed = dialog.returnValue === 'confirm';
      resolve(extraField ? { confirmed, value: extraInput ? extraInput.value.trim() : '' } : confirmed);
      dialog.remove();
    });
    document.body.append(dialog);
    dialog.showModal();
    input.focus();
  });

  // ---------------------------------------------------------------------
  // Site workspace: tabbed detail view
  // ---------------------------------------------------------------------

  const TABS = [
    { id: 'overview', label: 'Overview', render: renderOverviewTab },
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
      try {
        await TABS[index].render(site, panel, { isAdministrator, webserver, getJSON, postJSON, putJSON, patchJSON, deleteJSON, field, button, badge, formatAge, formatBytes, el, confirmDangerous, statusOutput });
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
    } else if (!detail.hidden) {
      detail.hidden = true;
      detail.replaceChildren();
    }
  });

  // ---------------------------------------------------------------------
  // Overview tab
  // ---------------------------------------------------------------------

  async function renderOverviewTab(site, panel, ctx) {
    const [overview, backups, deploymentList, usage, php] = await Promise.all([
      ctx.getJSON(`/api/sites/overview/${encodeURIComponent(site)}`),
      ctx.getJSON(`/api/backups?site=${encodeURIComponent(site)}&limit=500`).catch(() => ({ backups: [] })),
      ctx.getJSON(`/api/deployments?site=${encodeURIComponent(site)}`).catch(() => ({ deployments: [] })),
      ctx.getJSON(`/api/sites/usage/${encodeURIComponent(site)}`).catch(() => null),
      ctx.getJSON(`/api/sites/php/${encodeURIComponent(site)}`).catch(() => null),
    ]);

    const routes = overview.routes || [];
    const apps = overview.applications || [];
    const backup = latestBackup(backups.backups, site);
    const deployments = (deploymentList.deployments || []).slice().sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
    const lastDeployment = deployments[0];

    const running = apps.some((app) => app.state === 'applied' || app.state === 'running');
    const statusBadge = apps.length === 0
      ? ctx.badge('No application deployed', 'off')
      : running ? ctx.badge('Running', 'ok') : ctx.badge('Needs attention', 'warn');

    const stats = [
      ['Status', statusBadge, apps.length ? `${apps.length} managed application(s)` : 'Site files only, or PHP served directly'],
      ['Domains', routes.length ? routes.map((r) => r.domain).join(', ') : 'No domain connected', `${routes.length} route(s)`],
      ['PHP runtime', php && php.configured ? `PHP ${php.profile.version}` : 'Not configured', php && php.configured ? `OPcache ${php.profile.opcache ? 'on' : 'off'}` : 'Configure in Runtime'],
      ['Last verified backup', backup ? ctx.formatAge(backup.verified_at) : 'None yet', backup ? `${backup.databases ? backup.databases.length : 0} database(s) included` : 'Create one from the Backups tab'],
      ['Last deployment', lastDeployment ? `${lastDeployment.stage} · ${lastDeployment.state}` : 'None recorded', lastDeployment ? ctx.formatAge(lastDeployment.created_at) : 'Use the Deployments tab'],
      ['Disk usage', usage ? ctx.formatBytes(usage.bytes) : 'Unavailable', usage ? `${usage.files} files${usage.complete ? '' : ' (partial scan)'}` : ''],
    ];

    const grid = el('div', { className: 'overview-grid' }, stats.map(([label, value, note]) => el('article', { className: 'overview-stat' }, [
      el('span', { className: 'stat-label' }, label),
      value && value.nodeType ? value : el('strong', {}, value),
      note ? el('small', {}, note) : null,
    ])));

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'A quick read on whether this site is healthy: what is deployed, when it last backed up, and what changed most recently.'),
      grid,
    );

    if (ctx.isAdministrator) {
      try {
        const activity = await ctx.getJSON(`/api/audit/events?target=${encodeURIComponent(site)}&limit=8`);
        const events = activity.events || [];
        panel.append(
          el('h4', {}, 'Recent activity'),
          events.length
            ? el('ul', { className: 'resource-list' }, events.map((event) => el('li', { className: 'resource-list-item' }, [
              el('div', { className: 'item-meta' }, [el('strong', {}, event.action), el('small', {}, event.detail || '')]),
              el('small', {}, new Date(event.time).toLocaleString()),
            ])))
            : el('p', { className: 'empty-state' }, 'No recorded activity yet.'),
        );
      } catch (error) { /* audit trail is administrator-only; skip quietly for customers */ }
    }
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

    const list = el('ul', { className: 'resource-list' }, routes.length ? routes.map((route) => el('li', { className: 'resource-list-item' }, [
      el('div', { className: 'item-meta' }, [el('strong', {}, route.domain), el('small', {}, 'Active route')]),
      el('div', { className: 'item-actions' }, [ctx.button('Remove', async () => {
        const confirmed = await ctx.confirmDangerous({
          title: `Remove ${route.domain}?`,
          message: 'The route stops serving this domain immediately. DNS still points here until you update it elsewhere.',
          facts: [['Domain', route.domain], ['Site', site]],
          confirmText: route.domain,
          actionLabel: 'Remove domain',
        });
        if (!confirmed) return;
        try {
          await ctx.deleteJSON(`/api/sites/${encodeURIComponent(vhostConfigName(site, route.domain))}`);
          output.textContent = `${route.domain} removed.`;
          renderDomainsTab(site, panel, ctx);
        } catch (error) { output.textContent = error.message; }
      }, { className: 'danger' })]),
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
            await ctx.postJSON('/api/sites/deploy', { site, domain });
            output.textContent = 'Route added.';
            renderDomainsTab(site, panel, ctx);
          } catch (error) { output.textContent = error.message; }
        },
      }, [domainInput, el('button', { type: 'submit', className: 'primary-action' }, 'Add domain'), output]));
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
                  await ctx.postJSON('/api/sites/deploy', { site, domain: claim.domain });
                  output.textContent = 'Domain verified and route added.';
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
    const php = await ctx.getJSON(`/api/sites/php/${encodeURIComponent(site)}`).catch(() => null);
    const phpOutput = ctx.statusOutput();
    if (php) {
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
          phpOutput.textContent = 'Applying PHP runtime…';
          try {
            await ctx.putJSON(`/api/sites/php/${encodeURIComponent(site)}`, {
              version: versionField.querySelector('select').value,
              memory_limit: memoryField.querySelector('input').value,
              max_execution_time: Number(timeoutField.querySelector('input').value),
              upload_max_filesize: uploadField.querySelector('input').value,
              post_max_size: current.post_max_size,
              max_input_vars: current.max_input_vars,
              opcache: opcacheField.querySelector('input').checked,
              display_errors: current.display_errors,
              error_reporting: current.error_reporting,
            });
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
      panel.append(form);
    }

    // Node application status (read-only here; initial deployment happens
    // from the administrator "Deploy a Node app" panel).
    const overview = await ctx.getJSON(`/api/sites/overview/${encodeURIComponent(site)}`).catch(() => ({ applications: [] }));
    const nodeApp = (overview.applications || [])[0];
    panel.append(el('h4', {}, 'Node.js'), nodeApp
      ? el('div', { className: 'overview-grid' }, [el('article', { className: 'overview-stat' }, [
        el('span', { className: 'stat-label' }, 'Application'),
        el('strong', {}, `Node ${nodeApp.node_version}`),
        el('small', {}, `${nodeApp.domain} · ${nodeApp.state}`),
      ])])
      : el('p', { className: 'empty-state' }, ctx.isAdministrator ? 'No Node application deployed. Use "Deploy a Node app" below to add one.' : 'No Node application deployed for this site.'));

    if (nodeApp) {
      const toolingOutput = ctx.statusOutput();
      panel.append(el('div', { className: 'workspace-panel-actions' }, [
        ctx.button('npm install', async () => { toolingOutput.textContent = 'Installing dependencies…'; try { await ctx.postJSON('/api/node/tooling', { site, action: 'install', package_manager: 'npm' }); toolingOutput.textContent = 'Dependencies installed.'; } catch (error) { toolingOutput.textContent = error.message; } }),
        ctx.button('npm run build', async () => { toolingOutput.textContent = 'Building…'; try { await ctx.postJSON('/api/node/tooling', { site, action: 'build', package_manager: 'npm' }); toolingOutput.textContent = 'Build completed.'; } catch (error) { toolingOutput.textContent = error.message; } }),
      ]), toolingOutput);
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
          await ctx.postJSON('/api/python/deploy', { site, version: data.version, entrypoint: data.entrypoint, port: Number(data.port), workers: Number(data.workers) });
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
    panel.append(pyForm);

    // Composer
    const composerOutput = ctx.statusOutput();
    panel.append(el('h4', {}, 'Composer'), el('div', { className: 'workspace-panel-actions' }, [
      ctx.button('Run composer install', async () => {
        composerOutput.textContent = 'Running composer install…';
        try { await ctx.postJSON(`/api/composer/${encodeURIComponent(site)}`, { development: false, optimize: true }); composerOutput.textContent = 'Composer install completed.'; } catch (error) { composerOutput.textContent = error.message; }
      }),
    ]), composerOutput);
  }

  // ---------------------------------------------------------------------
  // Deployments tab
  // ---------------------------------------------------------------------

  async function renderDeploymentsTab(site, panel, ctx) {
    const [history, keyStatus] = await Promise.all([
      ctx.getJSON(`/api/deployments?site=${encodeURIComponent(site)}`).catch(() => ({ deployments: [] })),
      ctx.getJSON(`/api/sites/git-key/${encodeURIComponent(site)}`).catch(() => ({ configured: false })),
    ]);
    const deployments = (history.deployments || []).slice().sort((a, b) => new Date(b.created_at) - new Date(a.created_at)).slice(0, 20);

    const output = ctx.statusOutput();
    const keyOutput = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Deploy from a Git repository, review release history, and roll back if a release breaks the site.'),
      el('h4', {}, 'Deploy key'),
      keyStatus.configured
        ? el('div', {}, [el('p', { className: 'import-note' }, 'A deploy key is configured for this site.'), ctx.button('Retire deploy key', async () => {
          keyOutput.textContent = 'Retiring…';
          try { await ctx.deleteJSON(`/api/sites/git-key/${encodeURIComponent(site)}`); keyOutput.textContent = 'Deploy key retired.'; renderDeploymentsTab(site, panel, ctx); } catch (error) { keyOutput.textContent = error.message; }
        })])
        : el('div', {}, [ctx.button('Generate deploy key', async () => {
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
        })]),
      keyOutput,
    );

    if (ctx.isAdministrator) {
      const repoField = field({ label: 'Repository', name: 'repository', placeholder: 'git@github.com:org/repo.git or https://…', required: true });
      const refField = field({ label: 'Branch or tag', name: 'ref', value: 'main', placeholder: 'main' });
      const deployForm = el('form', {
        className: 'import-form', onSubmit: async (event) => {
          event.preventDefault();
          const repository = repoField.querySelector('input').value.trim();
          const ref = refField.querySelector('input').value.trim() || 'main';
          output.textContent = 'Deploying…';
          try {
            await ctx.postJSON('/api/sites/git-deploy', { site, repository, ref });
            output.textContent = 'Deployment completed.';
            renderDeploymentsTab(site, panel, ctx);
          } catch (error) { output.textContent = error.message; }
        },
      }, [el('h4', {}, 'Deploy from Git'), repoField, refField, el('button', { type: 'submit', className: 'primary-action' }, 'Deploy'), output]);
      panel.append(deployForm);

      panel.append(el('h4', {}, 'Roll back'), ctx.button('Roll back to previous release', async () => {
        const confirmed = await ctx.confirmDangerous({
          title: `Roll back ${site}?`,
          message: 'The previous release is restored atomically. The current release is kept for forensics.',
          confirmText: `ROLLBACK ${site}`,
          actionLabel: 'Roll back',
        });
        if (!confirmed) return;
        output.textContent = 'Rolling back…';
        try { await ctx.postJSON('/api/sites/git-rollback', { site, confirm: `ROLLBACK ${site}` }); output.textContent = 'Rolled back.'; } catch (error) { output.textContent = error.message; }
      }, { className: 'danger' }));
    }

    panel.append(
      el('h4', {}, 'Release history'),
      deployments.length
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
    const all = await ctx.getJSON('/api/databases').catch(() => ({ databases: [] }));
    const owned = (all.databases || []).filter((db) => db.site === site);
    const output = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, "This site's managed databases. StePanel never displays stored passwords or runs arbitrary SQL." ),
      el('ul', { className: 'resource-list' }, owned.length ? owned.map((db) => el('li', { className: 'resource-list-item' }, [
        el('div', { className: 'item-meta' }, [el('strong', {}, db.name), el('small', {}, `${ctx.formatBytes(db.bytes)}${db.user ? ` · user ${db.user}` : ''}`)]),
        el('div', { className: 'item-actions' }, [
          ctx.button('Delete', async () => {
            const confirmed = await ctx.confirmDangerous({
              title: `Delete ${db.name}?`,
              message: 'A safety backup is taken automatically before the database is dropped, but the live database will be gone immediately.',
              facts: [['Database', db.name], ['Size', ctx.formatBytes(db.bytes)], ['Owning site', site]],
              confirmText: `DROP ${db.name}`,
              actionLabel: 'Delete database',
            });
            if (!confirmed) return;
            output.textContent = 'Deleting…';
            try {
              await mutate('DELETE', `/api/databases/${encodeURIComponent(db.name)}`, { user: db.user, confirm: `DROP ${db.name}` });
              output.textContent = 'Database deleted.';
              renderDatabasesTab(site, panel, ctx);
            } catch (error) { output.textContent = error.message; }
          }, { className: 'danger' }),
        ]),
      ])) : [el('p', { className: 'empty-state' }, 'No databases for this site yet.')]),
      output,
    );

    const nameField = field({ label: 'Database name', name: 'name', pattern: '[a-z0-9_]{1,63}', required: true });
    const userField = field({ label: 'Database user', name: 'user', pattern: '[a-z][a-z0-9_]{0,31}', required: true, hint: 'Must start with a lowercase letter.' });
    const passwordField = withGenerateButton(field({ label: 'Password', name: 'password', type: 'password', minlength: 20, required: true, hint: '20+ characters.' }));
    const createOutput = ctx.statusOutput();
    panel.append(el('h4', {}, 'Create a database'), el('form', {
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
    }, [nameField, userField, passwordField, el('button', { type: 'submit', className: 'primary-action' }, 'Create database'), createOutput]));
  }

  // ---------------------------------------------------------------------
  // Backups tab
  // ---------------------------------------------------------------------

  async function renderBackupsTab(site, panel, ctx) {
    const data = await ctx.getJSON(`/api/backups?site=${encodeURIComponent(site)}&limit=100`).catch(() => ({ backups: [] }));
    const backups = (data.backups || []).sort((a, b) => new Date(b.created_at || b.verified_at) - new Date(a.created_at || a.verified_at));
    const output = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Backups are checksummed and, when a signing key is configured, cryptographically signed and verified before they are trusted for restore.'),
      el('div', { className: 'workspace-panel-actions' }, [
        ctx.button('Create verified backup', async () => {
          output.textContent = 'Creating a verified backup…';
          try {
            const result = await ctx.postJSON('/api/backups', { site, include_databases: true });
            output.textContent = `Backup queued (job ${result.job_id}). Refresh in a moment to see it listed.`;
          } catch (error) { output.textContent = error.message; }
        }),
      ]),
      output,
      el('ul', { className: 'resource-list' }, backups.length ? backups.map((backup) => el('li', { className: 'resource-list-item' }, [
        el('div', { className: 'item-meta' }, [
          el('strong', {}, backup.name || backup.path || 'Backup'),
          el('small', {}, backup.verified_at ? `Verified ${ctx.formatAge(backup.verified_at)}` : 'Not yet verified'),
        ]),
        el('div', { className: 'item-actions' }, [
          ctx.button('Verify', async () => {
            output.textContent = 'Verifying…';
            try { await ctx.postJSON('/api/backups/verify', { site, backup: backup.name }); output.textContent = 'Backup verified.'; } catch (error) { output.textContent = error.message; }
          }),
          ctx.button('Restore to staging', async () => {
            const { confirmed, value: domain } = await ctx.confirmDangerous({
              title: 'Restore to staging?',
              message: 'Files are restored into an isolated, no-index staging route. The live site is not touched.',
              facts: [['Backup', backup.name], ['Site', site]],
              confirmText: `RESTORE ${site}`,
              actionLabel: 'Restore to staging',
              extraField: { label: 'Staging domain to activate', placeholder: 'staging.example.com', hint: 'Must already resolve to this server.' },
            });
            if (!confirmed) return;
            output.textContent = 'Restoring to staging…';
            try {
              await ctx.postJSON('/api/backups/restore-to-staging', { site, backup: backup.name, domain });
              output.textContent = 'Restore to staging queued.';
            } catch (error) { output.textContent = error.message; }
          }),
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
      el('p', { className: 'panel-intro' }, 'The most recent lines from this site’s logs.'),
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
    const data = await ctx.getJSON(`/api/workers/${encodeURIComponent(site)}`).catch(() => ({ workers: [] }));
    const workers = data.workers || [];
    const output = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Background queue workers for this site (Laravel queues, Horizon, Celery, RQ, or a Node worker process).'),
      el('ul', { className: 'resource-list' }, workers.length ? workers.map((worker) => el('li', { className: 'resource-list-item' }, [
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
  }

  // ---------------------------------------------------------------------
  // Tasks tab (scheduled jobs)
  // ---------------------------------------------------------------------

  const TASK_RUNTIMES = ['php', 'node', 'python', 'shell'];

  async function renderTasksTab(site, panel, ctx) {
    const data = await ctx.getJSON(`/api/tasks/${encodeURIComponent(site)}`).catch(() => ({ tasks: [] }));
    const tasks = data.tasks || [];
    const output = ctx.statusOutput();

    panel.replaceChildren(
      el('p', { className: 'panel-intro' }, 'Scheduled commands run as systemd timers under the site’s isolated identity.'),
      el('ul', { className: 'resource-list' }, tasks.length ? tasks.map((task) => el('li', { className: 'resource-list-item' }, [
        el('div', { className: 'item-meta' }, [el('strong', {}, task.name), el('small', {}, `${task.on_calendar} · ${task.runtime} · ${task.enabled ? 'enabled' : 'disabled'}`)]),
        el('div', { className: 'item-actions' }, [
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
            enabled: enabledField.querySelector('input').checked,
          });
          createOutput.textContent = 'Task created.';
          renderTasksTab(site, panel, ctx);
        } catch (error) { createOutput.textContent = error.message; }
      },
    }, [nameField, el('div', { className: 'field-row' }, [runtimeField, calendarField, timeoutField]), commandField, enabledField, el('button', { type: 'submit', className: 'primary-action' }, 'Add task'), createOutput]));
  }

  // ---------------------------------------------------------------------
  // Security tab (SSH/SFTP access, keys, malware scan)
  // ---------------------------------------------------------------------

  async function renderSecurityTab(site, panel, ctx) {
    const access = await ctx.getJSON(`/api/sites/access/${encodeURIComponent(site)}`).catch(() => ({ sftp_enabled: false, shell_enabled: false, keys: [] }));
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

    if (ctx.isAdministrator) {
      const scanOutput = ctx.statusOutput();
      panel.append(el('h4', {}, 'Malware scan'), el('div', { className: 'workspace-panel-actions' }, [
        ctx.button('Scan site files', async () => {
          scanOutput.textContent = 'Scanning (this can take a while for large sites)…';
          try {
            const result = await ctx.postJSON('/api/security/scan', { site, quarantine: false });
            scanOutput.textContent = `Scan complete: ${(result.findings || []).length} finding(s).`;
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
    const env = await ctx.getJSON(`/api/sites/environment/${encodeURIComponent(site)}`).catch(() => ({ environment: {} }));
    const entries = Object.entries(env.environment || {});
    const rows = el('div', {}, entries.map(([name, value]) => {
      const secret = typeof value === 'object' && value !== null && value.secret;
      return el('div', { className: 'field-row' }, [
        field({ label: 'Name', name: `env-name`, value: name }),
        field({ label: secret ? 'Value (secret, hidden)' : 'Value', name: `env-value`, value: secret ? '' : value, type: secret ? 'password' : 'text', placeholder: secret ? 'Unchanged' : '' }),
      ]);
    }));
    const addRowButton = el('button', {
      type: 'button', className: 'quiet-action', onClick: () => {
        rows.append(el('div', { className: 'field-row' }, [field({ label: 'Name', name: 'env-name' }), field({ label: 'Value', name: 'env-value' })]));
      },
    }, '+ Add variable');
    panel.append(el('h4', {}, 'Environment variables'), env.environment ? el('div', {}, [rows, addRowButton, el('div', { className: 'workspace-panel-actions' }, [el('button', {
      type: 'button', className: 'primary-action', onClick: async () => {
        const payload = {};
        rows.querySelectorAll('.field-row').forEach((row) => {
          const inputs = row.querySelectorAll('input');
          const name = inputs[0]?.value.trim();
          const value = inputs[1]?.value;
          if (name) payload[name] = { value: value || '', secret: inputs[1]?.type === 'password' };
        });
        envOutput.textContent = 'Saving…';
        try { await ctx.putJSON(`/api/sites/environment/${encodeURIComponent(site)}`, payload); envOutput.textContent = 'Environment applied.'; } catch (error) { envOutput.textContent = error.message; }
      },
    }, 'Save environment')]), envOutput]) : el('p', { className: 'import-note' }, 'Environment management requires STEPANEL_ENVIRONMENT_KEY to be configured.'));

    // Resources (administrator only — the enforced side of a plan)
    if (ctx.isAdministrator) {
      const resources = await ctx.getJSON(`/api/sites/resources/${encodeURIComponent(site)}`).catch(() => null);
      if (resources) {
        panel.append(
          el('h4', {}, 'Resource profile'),
          el('div', { className: 'overview-grid' }, [
            el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'CPU'), el('strong', {}, `${resources.cpu_percent}%`)]),
            el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Memory'), el('strong', {}, `${resources.memory_mb} MB`)]),
            el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'Tasks'), el('strong', {}, String(resources.tasks_max))]),
            el('article', { className: 'overview-stat' }, [el('span', { className: 'stat-label' }, 'PHP workers'), el('strong', {}, String(resources.php_workers))]),
          ]),
        );
      }
    }

    // Danger zone
    if (ctx.isAdministrator) {
      const dangerOutput = ctx.statusOutput();
      panel.append(
        el('h4', {}, 'Danger zone'),
        el('p', { className: 'import-note' }, 'Terminating a site removes its files, databases, routes, and services after taking a final verified backup. This cannot be undone from the panel.'),
        ctx.button('Terminate site', async () => {
          const confirmed = await ctx.confirmDangerous({
            title: `Terminate ${site}?`,
            message: 'A final verified backup is taken first, then the site is permanently removed.',
            facts: [['Site', site]],
            confirmText: `DELETE ${site}`,
            actionLabel: 'Terminate site',
          });
          if (!confirmed) return;
          dangerOutput.textContent = 'Termination queued…';
          try {
            await ctx.postJSON('/api/sites/terminate', { site, confirmation: `DELETE ${site}` });
            dangerOutput.textContent = 'Termination queued. Track progress in Activity.';
          } catch (error) { dangerOutput.textContent = error.message; }
        }, { className: 'danger' }),
        dangerOutput,
      );
    }
  }

  // ---------------------------------------------------------------------
  // Site grid (list view)
  // ---------------------------------------------------------------------

  async function loadGrid() {
    const [siteData, backupData] = await Promise.all([
      getJSON('/api/sites/overview'),
      getJSON('/api/backups').catch(() => ({ backups: [] })),
    ]);
    grid.replaceChildren();
    let domains = 0;
    for (const site of siteData.sites || []) {
      domains += (site.routes || []).length;
      const backup = latestBackup(backupData.backups, site.site);
      const running = (site.applications || []).some((app) => app.state === 'applied' || app.state === 'running');
      const card = el('article', { className: 'site-card' }, [
        el('div', { className: 'section-heading' }, [el('h3', {}, site.site), (site.applications || []).length ? badge(running ? 'Running' : 'Needs attention', running ? 'ok' : 'warn') : null]),
        el('code', {}, (site.routes || [])[0]?.domain || 'No domain route'),
        el('p', {}, `${(site.applications || []).length} app · ${site.database_count || 0} database(s) · backup ${backup ? formatAge(backup.verified_at) : 'never'}`),
        el('div', { className: 'site-actions' }, [button('Manage site →', () => openWorkspace(site.site))]),
      ]);
      grid.append(card);
    }
    const setCount = (id, value) => { const node = document.querySelector(id); if (node) node.textContent = String(value); };
    setCount('#siteCount', (siteData.sites || []).length);
    setCount('#domainCount', domains);
    const latest = latestBackup(backupData.backups);
    setCount('#backupFreshness', latest ? formatAge(latest.verified_at) : 'Never');
    gridStatus.textContent = siteData.sites && siteData.sites.length
      ? `${siteData.sites.length} managed site(s). Select one to open its workspace.`
      : 'No managed sites yet. Start by migrating a cPanel backup or deploying a site.';
  }

  loadGrid()
    .then(() => {
      const initial = parseWorkspaceHash();
      if (initial) openWorkspace(initial.site, initial.tab, { pushHistory: false });
    })
    .catch((error) => { gridStatus.textContent = error.message; });
})();
