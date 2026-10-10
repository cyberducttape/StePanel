(() => {
  'use strict';

  const storageKey = 'stepanel.appearance.v1';
  const themes = {
    default: { label: 'StePanel Dark', description: 'Modern control-room blue with quiet green status accents.', colors: ['#080e17', '#151f2c', '#68d391'] },
    'classic-green': { label: 'Classic Green', description: 'VT220 phosphor green for a focused terminal feel.', colors: ['#001408', '#002b12', '#00e676'] },
    'classic-amber': { label: 'Classic Amber', description: 'Warm amber monochrome inspired by early workstations.', colors: ['#160d00', '#2b1b00', '#ffbf00'] },
    'classic-white': { label: 'Classic White', description: 'Crisp white-on-black monochrome with maximum simplicity.', colors: ['#101010', '#242424', '#f4f4f4'] },
    'retro-neon': { label: 'Retro 80s Neon', description: 'Synthwave magenta and cyan for a high-energy workspace.', colors: ['#090018', '#210044', '#ff4fd8'] },
    'high-contrast': { label: 'High Contrast', description: 'Bold black, white, yellow, and cyan for visual clarity.', colors: ['#000', '#111', '#ffff00'] },
    'terminal-blue': { label: 'Terminal Blue', description: 'IBM 3270-inspired blue for dense operational work.', colors: ['#00101f', '#002b4a', '#55c7ff'] },
    commodore64: { label: 'Commodore 64', description: 'Deep blue and soft cyan from the 8-bit home-computer era.', colors: ['#40318c', '#7869c4', '#a8ffea'] },
    windows95: { label: 'Windows 95', description: 'Teal desktop chrome, beveled panels, and navy actions.', colors: ['#008080', '#c0c0c0', '#000080'] },
    windows31: { label: 'Windows 3.1', description: 'A sharper classic desktop treatment with hard-edged depth.', colors: ['#008080', '#c0c0c0', '#000080'] },
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
    const description = document.querySelector('#appearanceThemeDescription');
    if (select) select.value = state.theme;
    if (dark) dark.checked = state.dark;
    if (scale) scale.value = String(state.scale);
    if (scaleValue) scaleValue.textContent = `${state.scale}%`;
    if (darkRow) darkRow.hidden = state.theme !== 'default';
    if (description) description.textContent = themes[state.theme].description;
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
    const preview = document.createElement('span');
    preview.className = 'appearance-swatch-colors';
    theme.colors.forEach((color) => {
      const chip = document.createElement('i');
      chip.style.backgroundColor = color;
      chip.setAttribute('aria-hidden', 'true');
      preview.append(chip);
    });
    const label = document.createElement('span');
    label.className = 'appearance-swatch-label';
    label.textContent = theme.label;
    button.append(preview, label);
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
