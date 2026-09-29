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
      if (status) status.textContent = data.suspended ? 'Account suspended' : 'Account active';
    } catch (error) { if (status) status.textContent = error.message; }
  };
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
})();
