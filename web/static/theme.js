(() => {
  'use strict';

  const storageKey = 'stepanel.appearance.v1';
  const themes = {
    default: { label: 'StePanel Dark', colors: ['#080e17', '#151f2c', '#68d391'] },
    'classic-green': { label: 'Classic Green', colors: ['#001408', '#002b12', '#00e676'] },
    'classic-amber': { label: 'Classic Amber', colors: ['#160d00', '#2b1b00', '#ffbf00'] },
    'classic-white': { label: 'Classic White', colors: ['#101010', '#242424', '#f4f4f4'] },
    'retro-neon': { label: 'Retro 80s Neon', colors: ['#090018', '#210044', '#ff4fd8'] },
    'high-contrast': { label: 'High Contrast', colors: ['#000', '#111', '#ffff00'] },
    'terminal-blue': { label: 'Terminal Blue', colors: ['#00101f', '#002b4a', '#55c7ff'] },
    commodore64: { label: 'Commodore 64', colors: ['#40318c', '#7869c4', '#a8ffea'] },
    windows95: { label: 'Windows 95', colors: ['#008080', '#c0c0c0', '#000080'] },
    windows31: { label: 'Windows 3.1', colors: ['#008080', '#c0c0c0', '#000080'] },
  };

  const readState = () => {
    const defaults = { theme: 'default', dark: true, scale: 100 };
    try {
      const saved = JSON.parse(localStorage.getItem(storageKey) || '{}');
      return {
        theme: themes[saved.theme] ? saved.theme : defaults.theme,
        dark: saved.dark !== false,
        scale: Math.min(150, Math.max(90, Number(saved.scale) || defaults.scale)),
      };
    } catch (_) {
      return defaults;
    }
  };

  const state = readState();
  const saveState = () => {
    try { localStorage.setItem(storageKey, JSON.stringify(state)); } catch (_) { /* private browsing */ }
  };

  const apply = () => {
    document.body.dataset.theme = state.theme;
    document.body.style.setProperty('--ui-scale', String(state.scale / 100));
    document.body.dataset.darkMode = state.dark ? 'true' : 'false';
    const select = document.querySelector('#appearanceTheme');
    const dark = document.querySelector('#appearanceDark');
    const scale = document.querySelector('#appearanceScale');
    const scaleValue = document.querySelector('#appearanceScaleValue');
    const darkRow = document.querySelector('#appearanceDarkRow');
    if (select) select.value = state.theme;
    if (dark) dark.checked = state.dark;
    if (scale) scale.value = String(state.scale);
    if (scaleValue) scaleValue.textContent = `${state.scale}%`;
    if (darkRow) darkRow.hidden = state.theme !== 'default';
    document.querySelectorAll('.appearance-swatch').forEach((swatch) => {
      swatch.setAttribute('aria-pressed', swatch.dataset.theme === state.theme ? 'true' : 'false');
    });
  };

  const trigger = document.querySelector('#appearanceTrigger');
  const dialog = document.querySelector('#appearanceDialog');
  if (!dialog || !trigger) { apply(); return; }

  const swatches = document.querySelector('#appearanceSwatches');
  Object.entries(themes).forEach(([key, theme]) => {
    const button = document.createElement('button');
    button.type = 'button';
    button.className = 'appearance-swatch';
    button.dataset.theme = key;
    button.title = theme.label;
    button.setAttribute('aria-label', `Use ${theme.label}`);
    theme.colors.forEach((color) => {
      const chip = document.createElement('i');
      chip.style.backgroundColor = color;
      chip.setAttribute('aria-hidden', 'true');
      button.append(chip);
    });
    button.addEventListener('click', () => {
      state.theme = key;
      apply();
      saveState();
    });
    swatches.append(button);
  });

  trigger.addEventListener('click', () => dialog.showModal());
  document.querySelector('#appearanceClose').addEventListener('click', () => dialog.close());
  document.querySelector('#appearanceTheme').addEventListener('change', (event) => {
    state.theme = event.target.value;
    apply();
    saveState();
  });
  document.querySelector('#appearanceDark').addEventListener('change', (event) => {
    state.dark = event.target.checked;
    apply();
    saveState();
  });
  document.querySelector('#appearanceScale').addEventListener('input', (event) => {
    state.scale = Number(event.target.value);
    apply();
    saveState();
  });
  document.querySelector('#appearanceReset').addEventListener('click', () => {
    Object.assign(state, { theme: 'default', dark: true, scale: 100 });
    apply();
    saveState();
  });

  apply();
})();
