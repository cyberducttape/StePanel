(() => {
  'use strict';
  const panel = document.querySelector('.account-panel');
  if (!panel) return;
  const status = document.querySelector('#accountStatus');
  const actionStatus = document.querySelector('#accountActionStatus');
  // Reads the API error envelope ({error, code, request_id}) and falls back
  // to the raw body or a default message.
  const errorMessage = async (response, fallback) => {
    const text = await response.text();
    try {
      const data = JSON.parse(text);
      if (data && data.error) return data.error;
    } catch (_) { /* plain-text body */ }
    return text || fallback;
  };

  const csrf = () => {
    const match = document.cookie.match(/(?:^|; )stepanel_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  };
  const set = (id, value) => { const node = document.querySelector(`#${id}`); if (node) node.textContent = value; };
  const renderPlanResources = (data) => {
    const container = document.querySelector('#accountPlanResources');
    if (!container) return;
    const resources = [
      ['CPU', `${data.cpu_percent}%`],
      ['Memory', `${data.memory_mb} MB`],
      ['Disk', `${data.disk_mb} MB`],
      ['Inodes', Number(data.inodes || 0).toLocaleString()],
      ['Tasks', Number(data.tasks_max || 0).toLocaleString()],
      ['Redis memory', `${data.redis_memory_mb} MB`],
    ];
    container.replaceChildren(...resources.map(([label, value]) => {
      const card = document.createElement('article'); card.className = 'account-resource-card';
      const labelNode = document.createElement('span'); labelNode.className = 'metric-label'; labelNode.textContent = label;
      const valueNode = document.createElement('strong'); valueNode.textContent = value;
      card.append(labelNode, valueNode);
      return card;
    }));
  };
  const load = async () => {
    try {
      const response = await fetch('/api/account/me');
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Account details unavailable');
      set('accountPlan', data.plan);
      set('accountRole', `Tenant ${data.role || 'owner'}`);
      const roleScopes = {
        developer: new Set(['site:read', 'site:deploy', 'deploy:write', 'environment:read', 'environment:write', 'logs:read', 'backup:read', 'backup:create', 'database:read', 'redis:read']),
        viewer: new Set(['site:read', 'environment:read', 'backup:read', 'database:read', 'redis:read', 'logs:read']),
      }[data.role];
      if (roleScopes) document.querySelectorAll('#accountTokenForm input[name="scope"]').forEach((input) => {
        input.disabled = !roleScopes.has(input.value);
        if (input.disabled) input.checked = false;
      });
      set('accountCreated', `Created ${new Date(data.created_at).toLocaleDateString()}`);
      set('accountSites', `${data.sites_used} / ${data.site_limit}`);
      set('accountSitesLimit', `${data.site_limit} site${data.site_limit === 1 ? '' : 's'} included · ${data.sites_percent || 0}% used`);
      set('accountDatabases', `${data.databases_used} / ${data.database_limit}`);
      set('accountDatabasesLimit', `${data.database_limit} database${data.database_limit === 1 ? '' : 's'} included · ${data.databases_percent || 0}% used`);
      const sitesBar = document.querySelector('#accountSitesBar');
      const databasesBar = document.querySelector('#accountDatabasesBar');
      const markUsage = (bar, percent) => {
        if (!bar) return;
        const value = Math.min(100, percent || 0);
        bar.style.width = `${value}%`;
        bar.parentElement.classList.toggle('warning', value >= (data.warning_threshold_percent || 80));
        bar.parentElement.classList.toggle('critical', value >= (data.critical_threshold_percent || 95));
        bar.parentElement.setAttribute('role', 'progressbar');
        bar.parentElement.setAttribute('aria-valuenow', String(value));
        bar.parentElement.setAttribute('aria-valuemin', '0');
        bar.parentElement.setAttribute('aria-valuemax', '100');
      };
      markUsage(sitesBar, data.sites_percent);
      markUsage(databasesBar, data.databases_percent);
      renderPlanResources(data);
      set('accountMFA', data.mfa_enabled ? 'MFA enabled' : 'MFA setup needed');
      set('accountSecurityNote', data.password_reset_required ? 'Password update required.' : (data.mfa_enrollment_required ? 'MFA enrollment required.' : 'Credentials are scoped to this tenant.'));
      if (team && data.role === 'owner') { team.hidden = false; loadMembers(); }
      const mfaSetup = document.querySelector('#accountMFASetup');
      if (mfaSetup && data.mfa_enabled && !data.mfa_enrollment_required) mfaSetup.hidden = true;
      if (status) status.textContent = data.suspended ? 'Account suspended' : 'Account active';
    } catch (error) { if (status) status.textContent = error.message; }
  };
  const tokenStatus = document.querySelector('#accountTokenStatus');
  const tokenList = document.querySelector('#accountTokenList');
  const activityList = document.querySelector('#accountActivityList');
  const team = document.querySelector('#accountTeam');
  const memberList = document.querySelector('#accountMemberList');
  const memberStatus = document.querySelector('#accountMemberStatus');
  const securityStatus = document.querySelector('#accountSecurityStatus');
  const loadMembers = async () => {
    if (!memberList) return;
    try {
      const response = await fetch('/api/account/members');
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Team members unavailable');
      memberList.replaceChildren();
      if (!(data.members || []).length) { memberList.textContent = 'No team members have been added.'; return; }
      data.members.forEach((member) => {
        const row = document.createElement('div'); row.className = 'token-row';
        const detail = document.createElement('span'); detail.textContent = `${member.username} · ${member.role} · ${member.mfa_enabled ? 'MFA enabled' : 'MFA required'}${member.suspended ? ' · suspended' : ''}`;
        const actions = document.createElement('span'); actions.className = 'account-inventory-actions';
        const role = document.createElement('select'); role.className = 'member-role'; role.setAttribute('aria-label', `Role for ${member.username}`);
        ['manager', 'developer', 'viewer'].forEach((value) => {
          const option = document.createElement('option'); option.value = value; option.textContent = value[0].toUpperCase() + value.slice(1); option.selected = value === member.role; role.append(option);
        });
        const saveRole = document.createElement('button'); saveRole.type = 'button'; saveRole.className = 'quiet-action'; saveRole.textContent = 'Save role';
        saveRole.addEventListener('click', async () => {
          saveRole.disabled = true;
          try {
            const response = await fetch(`/api/account/members/${encodeURIComponent(member.username)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify({ role: role.value }) });
            if (!response.ok) throw new Error(await errorMessage(response, 'Could not update member role'));
            await loadMembers();
          } catch (error) { if (memberStatus) memberStatus.textContent = error.message; }
          finally { saveRole.disabled = false; }
        });
        const suspend = document.createElement('button'); suspend.type = 'button'; suspend.className = 'quiet-action'; suspend.textContent = member.suspended ? 'Unsuspend' : 'Suspend';
        suspend.addEventListener('click', async () => {
          suspend.disabled = true;
          try {
            const response = await fetch(`/api/account/members/${encodeURIComponent(member.username)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify({ suspended: !member.suspended }) });
            if (!response.ok) throw new Error(await errorMessage(response, 'Could not update member'));
            await loadMembers();
          } catch (error) { if (memberStatus) memberStatus.textContent = error.message; }
          finally { suspend.disabled = false; }
        });
        const remove = document.createElement('button'); remove.type = 'button'; remove.className = 'quiet-action danger'; remove.textContent = 'Remove';
        remove.addEventListener('click', async () => {
          const confirmed = await window.StepanelUI.confirmDangerous({
            title: `Remove ${member.username}?`,
            message: 'The member immediately loses access to every site in this tenant. Their sessions and API tokens stop working.',
            facts: [['Member', member.username], ['Role', member.role || 'member']],
            confirmText: member.username,
            actionLabel: 'Remove member',
          });
          if (!confirmed) return;
          remove.disabled = true;
          try {
            const response = await fetch(`/api/account/members/${encodeURIComponent(member.username)}`, { method: 'DELETE', headers: { 'X-CSRF-Token': csrf() } });
            if (!response.ok) throw new Error(await errorMessage(response, 'Could not remove member'));
            await loadMembers();
          } catch (error) { if (memberStatus) memberStatus.textContent = error.message; }
          finally { remove.disabled = false; }
        });
        actions.append(role, saveRole, suspend, remove); row.append(detail, actions); memberList.append(row);
      });
    } catch (error) { memberList.textContent = error.message; }
  };
  const loadSecurity = async () => {
    if (!securityStatus) return;
    try {
      const response = await fetch('/api/account/security');
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Token security unavailable');
      if (data.has_legacy_tokens) {
        securityStatus.textContent = `${data.legacy_count} legacy token${data.legacy_count === 1 ? '' : 's'} need regeneration.`;
        securityStatus.classList.add('warning');
      } else {
        securityStatus.textContent = 'Token security is compliant.';
      }
    } catch (error) { securityStatus.textContent = error.message; }
  };
  const reviewSecurity = document.querySelector('#reviewTokenSecurity');
  if (reviewSecurity) reviewSecurity.addEventListener('click', () => {
    const tokenDetails = document.querySelector('.account-tokens');
    if (tokenDetails) tokenDetails.open = true;
    document.querySelector('.account-tokens')?.scrollIntoView({ behavior: 'smooth', block: 'center' });
    loadTokens();
  });
  const renderTokens = (items) => {
    if (!tokenList) return;
    tokenList.replaceChildren();
    if (!items.length) { tokenList.textContent = 'No automation tokens have been created.'; return; }
    items.forEach((token) => {
      const row = document.createElement('div'); row.className = 'token-row';
      const expiry = token.expires_at ? ` · expires ${new Date(token.expires_at * 1000).toLocaleDateString()}` : ' · no expiry';
      const state = token.revoked_at ? ' · revoked' : '';
      const detail = document.createElement('span'); detail.textContent = `${token.name} · ${token.prefix}… · ${(token.scopes || []).join(', ') || 'legacy'}${expiry}${state}`;
      const revoke = document.createElement('button'); revoke.type = 'button'; revoke.className = 'quiet-action danger'; revoke.textContent = 'Revoke';
      revoke.addEventListener('click', async () => {
        revoke.disabled = true;
        try {
          const response = await fetch(`/api/account/tokens/${encodeURIComponent(token.id)}`, { method: 'DELETE', headers: { 'X-CSRF-Token': csrf() } });
          if (!response.ok) throw new Error(await errorMessage(response, 'Could not revoke token'));
          await loadTokens();
        } catch (error) { if (tokenStatus) tokenStatus.textContent = error.message; }
        finally { revoke.disabled = false; }
      });
      row.append(detail, revoke); tokenList.append(row);
    });
  };
  const loadTokens = async () => {
    if (!tokenList) return;
    try {
      const response = await fetch('/api/account/tokens');
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Token list unavailable');
      renderTokens(data.tokens || []);
    } catch (error) { tokenList.textContent = error.message; }
  };
  const loadActivity = async () => {
    if (!activityList) return;
    try {
      const response = await fetch('/api/account/activity?limit=8');
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Activity unavailable');
      activityList.replaceChildren();
      if (!(data.events || []).length) { activityList.textContent = 'No tenant activity recorded yet.'; return; }
      data.events.slice().reverse().forEach((event) => {
        const row = document.createElement('div'); row.className = 'token-row';
        const when = new Date(event.time).toLocaleString();
        row.textContent = `${when} · ${event.action} · ${event.target}`;
        activityList.append(row);
      });
    } catch (error) { activityList.textContent = error.message; }
  };
  const tokenForm = document.querySelector('#accountTokenForm');
  const expiryInput = document.querySelector('#accountTokenExpiry');
  if (expiryInput) expiryInput.min = new Date().toISOString().slice(0, 10);
  if (tokenForm) tokenForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = tokenForm.querySelector('button[type="submit"]'); button.disabled = true;
    if (tokenStatus) tokenStatus.textContent = 'Creating token…';
    try {
      const form = new FormData(tokenForm);
      const expiry = form.get('expires_at');
      const expiresAt = expiry ? Math.floor(new Date(`${expiry}T23:59:59Z`).getTime() / 1000) : null;
      const response = await fetch('/api/account/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify({ name: form.get('name'), expires_at: expiresAt, scopes: form.getAll('scope') }) });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Could not create token');
      tokenForm.reset();
      if (tokenStatus) tokenStatus.textContent = `Copy this token now; it will not be shown again: ${data.token}`;
      await loadTokens();
    } catch (error) { if (tokenStatus) tokenStatus.textContent = error.message; }
    finally { button.disabled = false; }
  });
  const tokenDetails = document.querySelector('.account-tokens');
  if (tokenDetails) tokenDetails.addEventListener('toggle', () => { if (tokenDetails.open) loadTokens(); });
  const memberForm = document.querySelector('#accountMemberForm');
  if (memberForm) memberForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = memberForm.querySelector('button[type="submit"]'); button.disabled = true;
    if (memberStatus) memberStatus.textContent = 'Adding team member…';
    try {
      const values = Object.fromEntries(new FormData(memberForm));
      const response = await fetch('/api/account/members', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify(values) });
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Could not add team member');
      memberForm.reset();
      if (memberStatus) memberStatus.textContent = `${data.username} added as ${data.role}.`;
      await loadMembers();
    } catch (error) { if (memberStatus) memberStatus.textContent = error.message; }
    finally { button.disabled = false; }
  });
  const passwordForm = document.querySelector('#accountPasswordForm');
  if (passwordForm) passwordForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = passwordForm.querySelector('button[type="submit"]');
    const output = document.querySelector('#accountPasswordStatus');
    button.disabled = true;
    if (output) output.textContent = 'Changing password…';
    try {
      const password = new FormData(passwordForm).get('password');
      const response = await fetch('/api/account/password', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify({ password }) });
      if (!response.ok) throw new Error(await errorMessage(response, 'Could not change password'));
      passwordForm.reset();
      if (output) output.textContent = 'Password changed. Sign in again with the new password.';
    } catch (error) { if (output) output.textContent = error.message; }
    finally { button.disabled = false; }
  });
  const mfaForm = document.querySelector('#accountMFAForm');
  if (mfaForm) mfaForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = mfaForm.querySelector('button[type="submit"]');
    const output = document.querySelector('#accountMFAStatus');
    button.disabled = true;
    if (output) output.textContent = 'Saving MFA configuration…';
    try {
      const totpSecret = new FormData(mfaForm).get('totp_secret');
      const response = await fetch('/api/account/mfa', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify({ totp_secret: totpSecret }) });
      if (!response.ok) throw new Error(await errorMessage(response, 'Could not configure MFA'));
      mfaForm.reset();
      if (output) output.textContent = 'MFA saved. Sign in again with an authenticator code.';
    } catch (error) { if (output) output.textContent = error.message; }
    finally { button.disabled = false; }
  });
  const revoke = document.querySelector('#revokeOtherSessions');
  if (revoke) revoke.addEventListener('click', async () => {
    revoke.disabled = true;
    if (actionStatus) actionStatus.textContent = 'Signing out other sessions…';
    try {
      const response = await fetch('/api/account/sessions/revoke', { method: 'POST', headers: { 'X-CSRF-Token': csrf() } });
      if (!response.ok) throw new Error(await errorMessage(response, 'Could not revoke sessions'));
      if (actionStatus) actionStatus.textContent = 'Other sessions signed out.';
    } catch (error) { if (actionStatus) actionStatus.textContent = error.message; }
    finally { revoke.disabled = false; }
  });
  load();
  loadSecurity();
  loadActivity();
})();
