(() => {
  'use strict';

  // Minimal element builder for shared components. Event props (onClick,
  // onSubmit, ...) attach plain listeners.
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

  // A typed-confirmation dialog for destructive actions. Shows the object's
  // context (what it is, what it's used by, when it was last verified)
  // instead of a bare "are you sure?" prompt. When `extraField` is given
  // (e.g. collecting a staging domain), the dialog resolves to
  // { confirmed, value } instead of a plain boolean. Multiple fields resolve
  // to { confirmed, values } and are used for explicit database restore
  // parameters without falling back to native prompt() dialogs.
  const confirmDangerous = ({ title, message, facts = [], confirmText, actionLabel = 'Confirm', extraField = null, extraFields = null }) => new Promise((resolve) => {
    const dialog = el('dialog', { className: 'confirm-dialog' });
    const inputId = `confirm-input-${Math.random().toString(36).slice(2, 8)}`;
    const input = el('input', { id: inputId, type: 'text', autocomplete: 'off', required: true });
    const confirmButton = el('button', { type: 'submit', className: 'confirm-dialog-danger', disabled: true }, actionLabel);
    const definitions = extraFields || (extraField ? [{ name: 'value', ...extraField }] : []);
    const extraInputs = {};
    const updateConfirmState = () => {
      const missing = definitions.some((definition) => definition.required !== false && !(extraInputs[definition.name]?.value || '').trim());
      confirmButton.disabled = input.value !== confirmText || missing;
    };
    input.addEventListener('input', updateConfirmState);
    const extraFieldNodes = definitions.map((definition) => {
      const extraId = `confirm-extra-${Math.random().toString(36).slice(2, 8)}`;
      const tag = definition.tag || (definition.options ? 'select' : 'input');
      const extraInput = el(tag, { id: extraId, type: definition.type || 'text', placeholder: definition.placeholder || '', required: definition.required !== false });
      if (definition.options) {
        extraInput.append(...definition.options.map((option) => el('option', { value: option.value }, option.label)));
      }
      extraInputs[definition.name] = extraInput;
      extraInput.addEventListener('input', updateConfirmState);
      extraInput.addEventListener('change', updateConfirmState);
      return el('div', { className: 'field' }, [
        el('label', { for: extraId }, definition.label),
        extraInput,
        definition.hint ? el('small', { className: 'hint' }, definition.hint) : null,
      ]);
    });
    const form = el('form', { method: 'dialog', onSubmit: () => { dialog.close('confirm'); } }, [
      el('div', { className: 'confirm-dialog-body' }, [
        el('h3', {}, title),
        el('p', {}, message),
        facts.length ? el('div', { className: 'confirm-dialog-facts' }, facts.map(([label, value]) => el('div', {}, [label, el('strong', {}, value)]))) : null,
        ...extraFieldNodes,
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
      if (extraFields) {
        const values = {};
        definitions.forEach((definition) => { values[definition.name] = (extraInputs[definition.name]?.value || '').trim(); });
        resolve({ confirmed, values });
      } else if (extraField) {
        resolve({ confirmed, value: extraInputs.value ? extraInputs.value.value.trim() : '' });
      } else resolve(confirmed);
      dialog.remove();
    });
    document.body.append(dialog);
    dialog.showModal();
    input.focus();
  });

  window.StepanelUI = Object.freeze({
    setStatus(element, message, state = '') {
      if (!element) return;
      element.textContent = message;
      if (state) element.dataset.state = state;
      else delete element.dataset.state;
    },
    confirmDangerous,
    // "1 site", "3 sites": counts in prose read better than "site(s)".
    plural: (count, noun, pluralNoun = `${noun}s`) => `${count} ${count === 1 ? noun : pluralNoun}`,
  });
})();
