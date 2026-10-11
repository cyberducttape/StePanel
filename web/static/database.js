(() => {
  'use strict';
  const form = document.querySelector('#databaseForm');
  const inventory = document.querySelector('#databaseInventory');
  const status = document.querySelector('#databaseStatus');
  if (!form || !inventory || !status || form.querySelector('button').disabled) return;
  const api = window.StepanelAPI;

  const formatBytes = (value) => {
    const units = ['B', 'KiB', 'MiB', 'GiB', 'TiB'];
    let size = Number(value) || 0;
    let index = 0;
    while (size >= 1024 && index < units.length - 1) {
      size /= 1024;
      index++;
    }
    return `${size.toFixed(index ? 1 : 0)} ${units[index]}`;
  };

  const field = (type, name, placeholder) => {
    const input = document.createElement('input');
    input.type = type;
    input.name = name;
    input.placeholder = placeholder;
    input.required = true;
    if (type === 'password') input.minLength = 20;
    return input;
  };

  // An inline form with one input and a submit button that stays disabled
  // while its request runs.
  const inlineForm = (input, label, className, onSubmit) => {
    const inline = document.createElement('form');
    inline.className = 'database-inline';
    const button = document.createElement('button');
    button.type = 'submit';
    button.className = className;
    button.textContent = label;
    inline.append(input, button);
    inline.addEventListener('submit', async (event) => {
      event.preventDefault();
      button.disabled = true;
      try {
        await onSubmit(new FormData(inline));
      } catch (error) {
        status.textContent = error.message;
      } finally {
        button.disabled = false;
      }
    });
    return inline;
  };

  const load = async () => {
    const data = await api.get('/api/databases');
    inventory.replaceChildren();
    for (const db of data.databases) {
      const row = document.createElement('article');
      row.className = 'database-record';
      const summary = document.createElement('div');
      const title = document.createElement('strong');
      title.textContent = db.name;
      const detail = document.createElement('small');
      detail.textContent = `${db.site} · ${db.user || 'import-managed'} · ${formatBytes(db.bytes)} · ${db.encoding}`;
      summary.append(title, detail);
      row.append(summary);
      if (db.user) {
        const rotate = inlineForm(field('password', 'password', 'New password (20+ characters)'), 'Rotate', 'quiet-action', async (values) => {
          await api.patch(`/api/databases/${encodeURIComponent(db.name)}/credentials`, { user: db.user, password: values.get('password') });
          rotate.reset();
          status.textContent = `Credentials rotated for ${db.name}. Update the application secret now.`;
        });
        const drop = inlineForm(field('text', 'confirm', `DROP ${db.name}`), 'Delete', 'quiet-action danger', async (values) => {
          await api.request(`/api/databases/${encodeURIComponent(db.name)}`, {
            method: 'DELETE',
            json: { user: db.user, confirm: values.get('confirm') },
          });
          status.textContent = `Deleted ${db.name} and its managed user.`;
          await load();
        });
        row.append(rotate, drop);
      }
      inventory.append(row);
    }
    status.textContent = data.databases.length ? `${window.StepanelUI.plural(data.databases.length, 'managed database')}.` : 'No managed databases yet.';
  };

  form.addEventListener('submit', async (event) => {
    event.preventDefault();
    const button = form.querySelector('button');
    button.disabled = true;
    status.textContent = 'Creating database and least-privilege user…';
    try {
      await api.post('/api/databases', Object.fromEntries(new FormData(form)));
      form.reset();
      status.textContent = 'Database created. Store the credential in the application secret manager.';
      await load();
    } catch (error) {
      status.textContent = error.message;
    } finally {
      button.disabled = false;
    }
  });

  load().catch((error) => {
    status.textContent = error.message;
  });
})();
