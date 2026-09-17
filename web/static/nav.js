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

  toggle.addEventListener('click', () => {
    if (sidebar.classList.contains('is-open')) close();
    else open();
  });
  backdrop.addEventListener('click', close);
  // Selecting a section should close the drawer instead of leaving it open
  // behind the content it just scrolled to.
  sidebar.querySelectorAll('a').forEach((link) => link.addEventListener('click', close));
  document.addEventListener('keydown', (event) => {
    if (event.key === 'Escape' && sidebar.classList.contains('is-open')) close();
  });
})();
