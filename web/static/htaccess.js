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
      + `${warnings}${warnings ? '\n' : ''}\n${directives}`;
  };

  const submit = async (action) => {
    if (!form.reportValidity()) return;
    for (const button of buttons) button.disabled = true;
    output.textContent = action === 'apply' ? 'Validating and applying…' : 'Converting…';
    const values = Object.fromEntries(new FormData(form));
    try {
      render(await api.post('/api/caddy/htaccess', {
        site: values.site,
        domain: values.domain,
        content: values.content,
        action,
        allow_partial: values.allow_partial === 'on',
      }));
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
