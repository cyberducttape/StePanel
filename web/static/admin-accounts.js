(() => {
  'use strict';
  const inventory = document.querySelector('#accountInventory');
  if (!inventory) return;
  const status = document.querySelector('#adminAccountStatus');
  const csrf = () => {
    const match = document.cookie.match(/(?:^|; )stepanel_csrf=([^;]+)/);
    return match ? decodeURIComponent(match[1]) : '';
  };
  const request = async (url, options = {}) => {
    const response = await fetch(url, options);
    const text = await response.text();
    let data = {};
    try { data = text ? JSON.parse(text) : {}; } catch (_) { data = { error: text }; }
    if (!response.ok) throw new Error(data.error || `Request failed (${response.status})`);
    return data;
  };
  const render = (accounts) => {
    inventory.replaceChildren();
    if (!accounts.length) { inventory.textContent = 'No customer accounts provisioned.'; return; }
    accounts.forEach((account) => {
      const row = document.createElement('article'); row.className = 'account-inventory-row';
      const info = document.createElement('div');
      const title = document.createElement('strong'); title.textContent = account.username;
      const usage = account.usage ? ` · ${account.usage.sites_used}/${account.usage.site_limit} sites · ${account.usage.databases_used}/${account.usage.database_limit} databases` : '';
      const membership = account.role && account.role !== 'owner' ? ` · ${account.role} member of ${account.tenant_id || 'tenant'}` : '';
      const detail = document.createElement('small'); detail.textContent = `${account.plan}${usage}${membership} · ${account.mfa_enabled ? 'MFA enabled' : 'MFA required'}`;
      info.append(title, detail);
      const actions = document.createElement('div'); actions.className = 'account-inventory-actions';
      const state = document.createElement('span'); state.className = account.suspended ? 'status status-warn' : 'status status-ok'; state.textContent = account.suspended ? 'Suspended' : 'Active';
      const button = document.createElement('button'); button.type = 'button'; button.className = 'quiet-action'; button.textContent = account.suspended ? 'Unsuspend' : 'Suspend';
      button.addEventListener('click', async () => {
        button.disabled = true;
        try {
          const endpoint = account.suspended ? '/api/admin/unsuspend' : '/api/admin/suspend';
          const body = account.suspended ? { username: account.username } : { username: account.username, reason: 'Operator action from tenant inventory', permanent: false };
          await request(endpoint, { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify(body) });
          await load();
        } catch (error) { if (status) status.textContent = error.message; }
        finally { button.disabled = false; }
      });
      if (!account.role || account.role === 'owner') {
        const edit = document.createElement('details'); edit.className = 'account-edit';
        const editSummary = document.createElement('summary'); editSummary.textContent = 'Edit plan and sites'; edit.append(editSummary);
        const editForm = document.createElement('form'); editForm.className = 'account-edit-form';
        const plan = document.createElement('select'); plan.name = 'plan';
        ['starter', 'professional', 'agency'].forEach((value) => { const option = document.createElement('option'); option.value = value; option.textContent = value; option.selected = value === account.plan; plan.append(option); });
        const sites = document.createElement('input'); sites.name = 'sites'; sites.value = (account.sites || []).join(', '); sites.placeholder = 'Assigned sites'; sites.pattern = '[a-z0-9_-]*(,\\s*[a-z0-9_-]+)*';
        const save = document.createElement('button'); save.type = 'submit'; save.className = 'quiet-action'; save.textContent = 'Save assignment';
        const output = document.createElement('output'); output.setAttribute('role', 'status');
        editForm.append(plan, sites, save, output); edit.append(editForm);
        editForm.addEventListener('submit', async (event) => {
          event.preventDefault(); save.disabled = true; output.textContent = 'Saving…';
          try {
            await request(`/api/accounts/${encodeURIComponent(account.username)}`, { method: 'PATCH', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify({ plan: plan.value, sites: sites.value.split(',').map((site) => site.trim()).filter(Boolean) }) });
            output.textContent = 'Assignment saved.'; await load();
          } catch (error) { output.textContent = error.message; }
          finally { save.disabled = false; }
        });
        actions.append(edit);
      }
      actions.prepend(state, button); row.append(info, actions); inventory.append(row);
    });
  };
  const load = async () => {
    try {
      const data = await request('/api/accounts');
      const accounts = await Promise.all((data.accounts || []).map(async (account) => {
        try { account.usage = await request(`/api/admin/plan-status?account=${encodeURIComponent(account.username)}`); } catch (_) { /* inventory remains useful if one usage query fails */ }
        return account;
      }));
      render(accounts);
      if (status) status.textContent = `${accounts.length} customer account(s)`;
    }
    catch (error) { if (status) status.textContent = error.message; inventory.textContent = 'Account inventory unavailable.'; }
  };
  const form = document.querySelector('#accountCreateForm');
  if (form) form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = form.querySelector('button[type="submit"]'); const output = document.querySelector('#accountCreateStatus');
    button.disabled = true; if (output) output.textContent = 'Creating account…';
    try {
      const values = Object.fromEntries(new FormData(form));
      values.sites = values.sites.split(',').map((site) => site.trim()).filter(Boolean);
      await request('/api/accounts', { method: 'POST', headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf() }, body: JSON.stringify(values) });
      form.reset(); if (output) output.textContent = 'Customer account created.'; await load();
    } catch (error) { if (output) output.textContent = error.message; }
    finally { button.disabled = false; }
  });
  load();
})();
