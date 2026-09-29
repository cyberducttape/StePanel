(() => {
  'use strict';
  const panel = document.querySelector('.account-panel');
  if (!panel) return;
  const status = document.querySelector('#accountStatus');
  const actionStatus = document.querySelector('#accountActionStatus');
  const csrf = () => {
    const match = document.cookie.match(/(?:^|; )stepanel_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  };
  const set = (id, value) => { const node = document.querySelector(`#${id}`); if (node) node.textContent = value; };
  const load = async () => {
    try {
      const response = await fetch('/api/account/me');
      const data = await response.json();
      if (!response.ok) throw new Error(data.error || 'Account details unavailable');
      set('accountPlan', data.plan);
      set('accountCreated', `Created ${new Date(data.created_at).toLocaleDateString()}`);
      set('accountSites', `${data.sites_used} / ${data.site_limit}`);
      set('accountSitesLimit', `${data.site_limit} site${data.site_limit === 1 ? '' : 's'} included`);
      set('accountDatabases', `${data.databases_used} / ${data.database_limit}`);
      set('accountDatabasesLimit', `${data.database_limit} database${data.database_limit === 1 ? '' : 's'} included`);
      const sitesBar = document.querySelector('#accountSitesBar');
      const databasesBar = document.querySelector('#accountDatabasesBar');
      if (sitesBar) sitesBar.style.width = `${Math.min(100, data.sites_percent || 0)}%`;
      if (databasesBar) databasesBar.style.width = `${Math.min(100, data.databases_percent || 0)}%`;
      set('accountMFA', data.mfa_enabled ? 'MFA enabled' : 'MFA setup needed');
      set('accountSecurityNote', data.password_reset_required ? 'Password update required.' : (data.mfa_enrollment_required ? 'MFA enrollment required.' : 'Credentials are scoped to this tenant.'));
      const mfaSetup = document.querySelector('#accountMFASetup');
      if (mfaSetup && data.mfa_enabled && !data.mfa_enrollment_required) mfaSetup.hidden = true;
      if (status) status.textContent = data.suspended ? 'Account suspended' : 'Account active';
    } catch (error) { if (status) status.textContent = error.message; }
  };
  const tokenStatus = document.querySelector('#accountTokenStatus');
  const tokenList = document.querySelector('#accountTokenList');
  const activityList = document.querySelector('#accountActivityList');
  const renderTokens = (items) => {
    if (!tokenList) return;
    tokenList.replaceChildren();
    if (!items.length) { tokenList.textContent = 'No automation tokens have been created.'; return; }
    items.forEach((token) => {
      const row = document.createElement('div'); row.className = 'token-row';
      const detail = document.createElement('span'); detail.textContent = `${token.name} · ${token.prefix}… · ${(token.scopes || []).join(', ') || 'legacy'}`;
      const revoke = document.createElement('button'); revoke.type = 'button'; revoke.className = 'quiet-action danger'; revoke.textContent = 'Revoke';
      revoke.addEventListener('click', async () => {
        revoke.disabled = true;
        try {
          const response = await fetch(`/api/account/tokens/${encodeURIComponent(token.id)}`, { method: 'DELETE', headers: { 'X-CSRF-Token': csrf() } });
          if (!response.ok) throw new Error((await response.text()) || 'Could not revoke token');
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
  if (tokenForm) tokenForm.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = tokenForm.querySelector('button[type="submit"]'); button.disabled = true;
    if (tokenStatus) tokenStatus.textContent = 'Creating token…';
    try {
      const form = new FormData(tokenForm);
      const response = await fetch('/api/account/tokens', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify({ name: form.get('name'), scopes: form.getAll('scope') }) });
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
      if (!response.ok) throw new Error((await response.text()) || 'Could not change password');
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
      if (!response.ok) throw new Error((await response.text()) || 'Could not configure MFA');
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
      if (!response.ok) throw new Error((await response.text()) || 'Could not revoke sessions');
      if (actionStatus) actionStatus.textContent = 'Other sessions signed out.';
    } catch (error) { if (actionStatus) actionStatus.textContent = error.message; }
    finally { revoke.disabled = false; }
  });
  load();
  loadActivity();
})();
