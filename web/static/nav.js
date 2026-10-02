(() => {
  'use strict';
  const toggle = document.querySelector('#navToggle');
  const sidebar = document.querySelector('#workspaceSidebar');
  const backdrop = document.querySelector('#navBackdrop');
  if (!toggle || !sidebar || !backdrop) return;

  const background = [document.querySelector('.topbar'), document.querySelector('main'), document.querySelector('#jobCenterToggle'), document.querySelector('#jobCenter')].filter(Boolean);
  const focusable = () => [...sidebar.querySelectorAll('a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])')];
  let previouslyFocused = null;

  const close = () => {
    if (!sidebar.classList.contains('is-open')) return;
    sidebar.classList.remove('is-open');
    backdrop.hidden = true;
    toggle.setAttribute('aria-expanded', 'false');
    toggle.setAttribute('aria-label', 'Open navigation');
    background.forEach((node) => { node.inert = false; node.removeAttribute('inert'); });
    if (previouslyFocused && document.contains(previouslyFocused)) previouslyFocused.focus();
    previouslyFocused = null;
  };
  const open = () => {
    previouslyFocused = document.activeElement;
    sidebar.classList.add('is-open');
    backdrop.hidden = false;
    toggle.setAttribute('aria-expanded', 'true');
    toggle.setAttribute('aria-label', 'Close navigation');
    background.forEach((node) => { node.inert = true; node.setAttribute('inert', ''); });
    requestAnimationFrame(() => focusable()[0]?.focus());
  };

  const links = [...sidebar.querySelectorAll('a[href^="#"]')];
  const setActive = (hash) => {
    const target = hash || '#main';
    links.forEach((link) => {
      const active = link.getAttribute('href') === target;
      link.classList.toggle('is-active', active);
      if (active) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
    });
  };

  toggle.addEventListener('click', () => {
    if (sidebar.classList.contains('is-open')) close();
    else open();
  });
  backdrop.addEventListener('click', close);
  setActive(window.location.hash);
  window.addEventListener('hashchange', () => setActive(window.location.hash));
  // Selecting a section should close the drawer instead of leaving it open
  // behind the content it just scrolled to.
  links.forEach((link) => link.addEventListener('click', () => {
    setActive(link.getAttribute('href'));
    close();
  }));
  document.addEventListener('keydown', (event) => {
    if (!sidebar.classList.contains('is-open')) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
      return;
    }
    if (event.key !== 'Tab') return;
    const items = focusable();
    if (!items.length) return;
    const first = items[0];
    const last = items[items.length - 1];
    if (event.shiftKey && document.activeElement === first) {
      event.preventDefault();
      last.focus();
    } else if (!event.shiftKey && document.activeElement === last) {
      event.preventDefault();
      first.focus();
    }
  });
})();
