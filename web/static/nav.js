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
  const contextTitle = document.querySelector('#workspaceContextTitle');
  const defaultTitle = contextTitle ? contextTitle.textContent : '';
  // Link text without its decorative aria-hidden icon.
  const sectionName = (link) => [...link.childNodes]
    .filter((node) => !(node.nodeType === Node.ELEMENT_NODE && node.getAttribute('aria-hidden') === 'true'))
    .map((node) => node.textContent).join('').trim();
  const setActive = (hash) => {
    const target = hash || '#main';
    links.forEach((link) => {
      const active = link.getAttribute('href') === target;
      link.classList.toggle('is-active', active);
      if (active) link.setAttribute('aria-current', 'page');
      else link.removeAttribute('aria-current');
      // The topbar names the section in view; the overview keeps the
      // role-specific title rendered by the server.
      if (active && contextTitle) contextTitle.textContent = target === '#main' ? defaultTitle : sectionName(link);
    });
  };

  // Scroll spy: the dashboard is one long page, so the highlighted section
  // follows what is on screen rather than only the last link clicked.
  const sections = links
    .map((link) => ({ hash: link.getAttribute('href'), node: link.getAttribute('href') === '#main' ? null : document.querySelector(link.getAttribute('href')) }))
    .filter((entry) => entry.hash === '#main' || entry.node);
  let spyLockedUntil = 0;
  let spyFrame = 0;
  const spy = () => {
    spyFrame = 0;
    if (Date.now() < spyLockedUntil) return;
    const topbar = document.querySelector('.topbar');
    const threshold = (topbar ? topbar.getBoundingClientRect().bottom : 0) + 96;
    let current = '#main';
    let currentTop = -Infinity;
    for (const { hash, node } of sections) {
      if (!node || node.offsetParent === null) continue;
      const top = node.getBoundingClientRect().top;
      if (top <= threshold && top > currentTop) { current = hash; currentTop = top; }
    }
    // A short final section can never reach the threshold; at the bottom of
    // the page, prefer the last visible section instead.
    if (window.innerHeight + window.scrollY >= document.documentElement.scrollHeight - 4) {
      const visible = sections.filter(({ node }) => node && node.offsetParent !== null && node.getBoundingClientRect().top < window.innerHeight);
      if (visible.length) current = visible.reduce((a, b) => (a.node.getBoundingClientRect().top > b.node.getBoundingClientRect().top ? a : b)).hash;
    }
    setActive(current);
  };
  window.addEventListener('scroll', () => { if (!spyFrame) spyFrame = requestAnimationFrame(spy); }, { passive: true });
  window.addEventListener('scrollend', () => { spyLockedUntil = 0; spy(); });

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
    // Hold the clicked section while smooth scrolling passes over others.
    spyLockedUntil = Date.now() + 1200;
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
