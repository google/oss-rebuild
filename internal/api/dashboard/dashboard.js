// Copyright 2026 Google LLC
// SPDX-License-Identifier: Apache-2.0

// Client behavior shared across dashboard pages: sidebar navigation state,
// page-transition indicator, and session candidate drawers and popouts.
(() => {
  // A package's own pages sit under Packages.
  const also = {'/packages': ['/package/', '/attempt/', '/session/']};
  for (const a of document.querySelectorAll('.secs a')) {
    const p = a.getAttribute('href'), at = location.pathname;
    if (at === p || (p !== '/' && at.startsWith(p + '/')) || (also[p] || []).some(q => at.startsWith(q))) a.setAttribute('aria-current', 'page');
  }
  // Pages render server-side and can take seconds, so a same-origin
  // link click shows a progress bar until the next page replaces this
  // one. pageshow clears it when the back-forward cache restores a page.
  addEventListener('click', e => {
    const a = e.target.closest('a[href]');
    if (!a || e.defaultPrevented || e.button || e.metaKey || e.ctrlKey || e.shiftKey || e.altKey || a.target || a.origin !== location.origin) return;
    if (a.hash && a.pathname === location.pathname && a.search === location.search) return;
    document.documentElement.classList.add('navigating');
  });
  addEventListener('pageshow', () => document.documentElement.classList.remove('navigating'));

  // Drawers and popouts are native toggles. This wires the controls that
  // reach across rows: expand all, a review row opening its popout, a
  // diffr link opening the drawer it points into.
  for (const b of document.querySelectorAll('[data-drawers]')) b.addEventListener('click', () => {
    for (const t of document.querySelectorAll('.drawer-toggle')) t.checked = b.dataset.drawers === 'open';
  });
  for (const b of document.querySelectorAll('[data-pop]')) b.addEventListener('click', e => {
    e.stopPropagation();
    const d = document.getElementById(b.dataset.pop);
    if (d) { d.open = true; d.scrollIntoView({block: 'nearest'}); }
  });
  for (const a of document.querySelectorAll('[data-drawer]')) a.addEventListener('click', () => {
    const t = document.getElementById(a.dataset.drawer);
    if (t) t.checked = true;
    a.closest('details')?.removeAttribute('open');
  });
  for (const b of document.querySelectorAll('.pop-close')) b.addEventListener('click', () => b.closest('details').removeAttribute('open'));
  document.addEventListener('click', e => {
    for (const d of document.querySelectorAll('details.pop[open]')) if (!d.contains(e.target)) d.removeAttribute('open');
  });
})();
