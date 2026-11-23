'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');

const css = fs.readFileSync(path.join(__dirname, '../../web/static/workspace.css'), 'utf8');

const declarations = (selector) => {
  const escaped = selector.replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
  const match = css.match(new RegExp(`${escaped}\\s*\\{([^}]+)\\}`));
  return Object.fromEntries([...(match?.[1] || '').matchAll(/--([\w-]+):\s*(#[\da-fA-F]{3,8})\s*;/g)].map(([, name, value]) => [name, value]));
};

const channel = (value) => {
  const hex = value.slice(1);
  const expanded = hex.length === 3 ? [...hex].map((digit) => digit + digit).join('') : hex.slice(0, 6);
  const normalized = [0, 2, 4].map((offset) => parseInt(expanded.slice(offset, offset + 2), 16) / 255);
  return normalized.map((component) => component <= 0.04045 ? component / 12.92 : ((component + 0.055) / 1.055) ** 2.4);
};

const contrast = (foreground, background) => {
  const luminance = (color) => {
    const [r, g, b] = channel(color);
    return 0.2126 * r + 0.7152 * g + 0.0722 * b;
  };
  const values = [luminance(foreground), luminance(background)].sort((a, b) => b - a);
  return (values[0] + 0.05) / (values[1] + 0.05);
};

test('every appearance theme has readable primary-action text and no first-paint color tween', () => {
  const base = declarations('body[data-theme]');
  const themes = [
    'default', 'classic-green', 'classic-amber', 'classic-white', 'retro-neon',
    'high-contrast', 'terminal-blue', 'commodore64', 'windows95', 'windows31',
  ];
  for (const theme of themes) {
    const selected = theme === 'default' ? {} : declarations(`body[data-theme="${theme}"]`);
    const foreground = selected['appearance-action-text'] || base['appearance-action-text'];
    const background = selected['appearance-accent'] || base['appearance-accent'];
    assert.ok(foreground && background, `${theme} must define primary-action colors`);
    assert.ok(contrast(foreground, background) >= 4.5, `${theme} primary action contrast is below 4.5:1`);
  }

  const lightDefault = declarations('body[data-theme="default"][data-dark-mode="false"]');
  assert.ok(contrast(lightDefault['appearance-action-text'], lightDefault['appearance-accent']) >= 4.5,
    'light default primary action contrast is below 4.5:1');
  assert.match(css, /body\[data-theme\]\s+\.primary-action\s*\{[^}]*transition:\s*none\s*;/s,
    'the theme switch must not animate the primary action through unreadable intermediate colors');
});
