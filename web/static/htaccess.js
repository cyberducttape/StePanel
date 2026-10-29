(() => {
  'use strict';
  const form = document.querySelector('#htaccessForm');
  if (!form) return;
  const api = window.StepanelAPI;
  const output = document.querySelector('#htaccessResult');
  const buttons = form.querySelectorAll('button[data-action]');

  const render = (data) => {
    const warnings = (data.warnings || []).map((value) => `Warning: ${value}`).join('\n');
    const directives = data.caddy_directives || '# No Caddy directives were required.';
    output.textContent = `${data.applied ? 'Applied successfully.' : 'Preview only.'}\n`
      + `Supported directives: ${data.supported_directives || 0}\n`
      + `${warnings}${warnings ? '\n' : ''}\n${directives}`
      + (data.effects ? `\nEffects: ${data.effects.join(' ')}` : '')
      + (data.recovery_plan ? `\nRecovery: ${data.recovery_plan.join(' ')}` : '')
      + (data.recovery_snapshot ? `\nRecovery snapshot: ${data.recovery_snapshot}` : '');
  };

  const submit = async (action) => {
    if (!form.reportValidity()) return;
    for (const button of buttons) button.disabled = true;
    output.textContent = action === 'apply' ? 'Preparing configuration change preview…' : 'Converting…';
    const values = Object.fromEntries(new FormData(form));
    try {
      const body = {
        site: values.site,
        domain: values.domain,
        content: values.content,
        allow_partial: values.allow_partial === 'on',
      };
      if (action === 'preview') {
        render(await api.post('/api/caddy/htaccess', { ...body, action: 'preview' }));
        return;
      }
      const preview = await api.post('/api/caddy/htaccess', { ...body, action: 'preview' });
      render(preview);
      if ((preview.warnings || []).length && !body.allow_partial) {
        throw new Error('Unsupported directives are present. Review the warnings and explicitly enable partial application before continuing.');
      }
      const confirmed = await window.StepanelUI.confirmDangerous({
        title: `Review Caddy changes for ${body.domain}`,
        message: 'StePanel will replace the managed Caddy site configuration with the supported translation and reload Caddy.',
        facts: [
          ['Site', body.site], ['Domain', body.domain],
          ['Vhost file', preview.proposed.vhost_path],
          ['Translated directives', String(preview.supported_directives || 0)],
          ['Effects', (preview.effects || []).join(' ')],
          ['Recovery', (preview.recovery_plan || []).join(' ')],
          ['Preview expires', new Date(preview.expires_at).toLocaleString()],
        ],
        confirmText: body.domain,
        actionLabel: 'Apply Caddy configuration',
      });
      if (!confirmed) { output.textContent += '\nChange cancelled.'; return; }
      render(await api.post('/api/caddy/htaccess', { ...body, action: 'apply', preview_token: preview.preview_token }));
    } catch (error) {
      // A rejected conversion still returns the partial translation and its
      // warnings; show them instead of only the error.
      if (error.data && error.data.caddy_directives !== undefined) render(error.data);
      else output.textContent = error.message;
    } finally {
      for (const button of buttons) button.disabled = false;
    }
  };

  for (const button of buttons) button.addEventListener('click', () => submit(button.dataset.action));
})();
