(() => {
  'use strict';
  const toggle = document.querySelector('#navToggle');
  const sidebar = document.querySelector('#workspaceSidebar');
  const backdrop = document.querySelector('#navBackdrop');
  if (!toggle || !sidebar || !backdrop) return;

  const close = () => {
    sidebar.classList.remove('is-open');
    backdrop.hidden = true;
    toggle.setAttribute('aria-expanded', 'false');
  };
  const open = () => {
    sidebar.classList.add('is-open');
    backdrop.hidden = false;
    toggle.setAttribute('aria-expanded', 'true');
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
    if (event.key === 'Escape' && sidebar.classList.contains('is-open')) close();
  });
})();
