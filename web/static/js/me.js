// /me — wires:
//   - per-decoy "Share report" buttons to the pre-built card text
//   - the sign-in form (POST /api/auth/magic/request)
//   - the handle picker (PATCH /api/me/handle)
//   - the anonymous toggle (PATCH /api/me/anonymous)
//
// Everything posts JSON with the CSRF cookie value as the X-CSRF-Token
// header so the double-submit middleware lets it through.
(function() {
  'use strict';

  // ---- per-decoy share buttons ------------------------------------------
  document.querySelectorAll('.btn-share[data-share-text]').forEach((btn) => {
    btn.addEventListener('click', async () => {
      const text = btn.dataset.shareText || '';
      if (!text) return;
      const result = await window.bbgShare(text, window.location.origin);
      flash(result === 'shared' ? 'Shared.' : 'Decoy report copied.');
    });
  });

  // ---- "just signed in" one-time toast ----------------------------------
  if (new URLSearchParams(window.location.search).get('signed_in') === '1') {
    flash('Signed in. Welcome back 🪿');
    // Scrub the query string so a reload doesn't re-flash.
    history.replaceState({}, '', window.location.pathname);
  }

  // ---- magic-link request -----------------------------------------------
  const magicForm = document.getElementById('magicForm');
  if (magicForm) {
    magicForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const email = document.getElementById('magicEmail').value.trim();
      const hint = document.getElementById('magicHint');
      if (!email || !email.includes('@')) {
        if (hint) { hint.textContent = 'enter a valid email'; hint.classList.add('warn'); }
        return;
      }
      const r = await postJSON('/api/auth/magic/request', { email });
      if (hint) {
        hint.classList.remove('warn');
        hint.textContent = "if that email is on file, we just sent a sign-in link. check your inbox.";
      }
      magicForm.querySelector('button').disabled = true;
    });
  }

  // ---- handle picker ----------------------------------------------------
  const handleForm = document.getElementById('handleForm');
  if (handleForm) {
    handleForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const value = document.getElementById('handleInput').value.trim();
      const hint = document.getElementById('handleHint');
      const r = await fetch('/api/me/handle', {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('bbg_csrf_v1') || '' },
        body: JSON.stringify({ handle: value }),
      });
      let body = {};
      try { body = await r.json(); } catch (_) {}
      if (r.ok) {
        if (hint) { hint.textContent = '✓ saved as ' + body.handle; hint.classList.remove('warn'); }
        flash('Handle saved.');
      } else {
        const msg = ({
          'bad_handle': '3–20 chars, letters/digits/_/- only — and not starting with _ or -',
          'reserved': "that one's reserved, try another",
          'handle_taken': 'taken — try another',
        })[body.code] || ('error: ' + (body.code || r.statusText));
        if (hint) { hint.textContent = msg; hint.classList.add('warn'); }
      }
    });
  }

  // ---- anonymous toggle -------------------------------------------------
  const anonToggle = document.getElementById('anonToggle');
  if (anonToggle) {
    anonToggle.addEventListener('change', async () => {
      // checked = "show as handle"; unchecked = anonymous.
      const anon = !anonToggle.checked;
      const r = await fetch('/api/me/anonymous', {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('bbg_csrf_v1') || '' },
        body: JSON.stringify({ anonymous: anon }),
      });
      if (r.ok) {
        const label = anonToggle.parentElement.querySelector('span');
        if (label) label.textContent = anon ? 'anonymous' : 'as my handle';
        flash(anon ? 'Showing as anonymous.' : 'Showing as your handle.');
      } else {
        anonToggle.checked = !anonToggle.checked; // revert
        flash('Could not update — please retry');
      }
    });
  }

  // ---- logout (this device + everywhere) --------------------------------
  document.getElementById('logoutBtn')?.addEventListener('click', () => doLogout('/api/auth/logout'));
  document.getElementById('logoutAllBtn')?.addEventListener('click', () => {
    if (!confirm('Sign out of EVERY device where this account is signed in?')) return;
    doLogout('/api/auth/logout-all');
  });

  async function doLogout(url) {
    try {
      await fetch(url, {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'X-CSRF-Token': readCookie('bbg_csrf_v1') || '' },
      });
    } catch (_) { /* even on failure, drop the cookie client-side and reload */ }
    // Cookie is cleared by the response's Set-Cookie. Navigate home; the
    // session middleware will mint a fresh anonymous identity on landing.
    window.location.href = '/';
  }

  // ---- helpers ----------------------------------------------------------
  async function postJSON(url, body) {
    const r = await fetch(url, {
      method: 'POST',
      credentials: 'same-origin',
      headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('bbg_csrf_v1') || '' },
      body: JSON.stringify(body),
    });
    try { return await r.json(); } catch (_) { return {}; }
  }

  function readCookie(name) {
    return document.cookie.split('; ')
      .map(c => c.split('='))
      .filter(p => p[0] === name)
      .map(p => decodeURIComponent(p[1]))[0];
  }

  let toastTimer;
  function flash(msg) {
    const t = document.getElementById('toast');
    if (!t) return;
    t.textContent = msg;
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), 2200);
  }
})();
