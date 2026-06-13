// /me — wires:
//   - per-decoy "Share report" buttons to the pre-built card text
//   - the sign-in form (POST /api/auth/magic/request)
//   - the handle picker (PATCH /api/me/handle)
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

  // ---- arrived via /me#signin → focus the email input ------------------
  // The result-page CTA links to /me#signin so the browser auto-scrolls
  // to the form. We additionally pull focus into the email input so the
  // user can start typing without a second tap. Guard on the form
  // actually being present — if the user is already signed in, the
  // hash points to an element that doesn't exist and we skip.
  if (window.location.hash === '#signin') {
    const emailInput = document.getElementById('magicEmail');
    if (emailInput) {
      // requestAnimationFrame lets the browser's hash-anchor scroll
      // settle before we steal focus (otherwise mobile Safari sometimes
      // re-scrolls to the input and overshoots the section header).
      requestAnimationFrame(() => emailInput.focus({ preventScroll: true }));
    }
  }

  // ---- "just signed in" one-time toast ----------------------------------
  // Guard on #logoutBtn — only rendered when the server confirms a signed-in
  // user. Without this guard, sharing the /me?signed_in=1 URL to a device
  // without the auth cookie would flash a misleading "welcome back" even
  // though the page actually shows the sign-in form.
  if (new URLSearchParams(window.location.search).get('signed_in') === '1') {
    if (document.getElementById('logoutBtn')) {
      flash('Signed in. Welcome back 🪿');
    }
    // Scrub the query string either way so a reload doesn't re-trigger the
    // check (and so the misleading URL doesn't linger in history).
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
  // Two modes:
  //   display — read-only "@handle  edit" row (initial state if .Handle is set)
  //   edit    — input + save + cancel (initial state on first sign-in)
  // After a successful save we drop back to display mode so the picker
  // disappears until the user actively asks to change it.
  const handleForm    = document.getElementById('handleForm');
  const handleInput   = document.getElementById('handleInput');
  const handleDisplay = document.getElementById('handleDisplay');
  const handleValueEl = document.getElementById('handleValue');
  const handleEditBtn = document.getElementById('handleEditBtn');
  const handleCancel  = document.getElementById('handleCancelBtn');
  const handleHint    = document.getElementById('handleHint');
  const handleHintDefault = '3–20 chars · letters, digits, _ -';

  function showHandleEdit() {
    if (handleDisplay) handleDisplay.hidden = true;
    if (handleForm)    handleForm.hidden    = false;
    if (handleHint) {
      handleHint.hidden      = false;
      handleHint.textContent = handleHintDefault;
      handleHint.classList.remove('warn');
    }
    if (handleInput) { handleInput.focus(); handleInput.select(); }
  }
  function showHandleDisplay(newValue) {
    if (newValue && handleValueEl) handleValueEl.textContent = newValue;
    if (handleDisplay) handleDisplay.hidden = false;
    if (handleForm)    handleForm.hidden    = true;
    if (handleHint)    handleHint.hidden    = true;
    if (handleCancel)  handleCancel.hidden  = false; // once there's a value to revert to
  }

  if (handleEditBtn) handleEditBtn.addEventListener('click', showHandleEdit);
  if (handleCancel) handleCancel.addEventListener('click', () => {
    if (handleInput && handleValueEl) handleInput.value = handleValueEl.textContent.trim();
    showHandleDisplay();
  });

  if (handleForm) {
    handleForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const value = (handleInput?.value || '').trim();
      const r = await fetch('/api/me/handle', {
        method: 'PATCH',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': readCookie('bbg_csrf_v1') || '' },
        body: JSON.stringify({ handle: value }),
      });
      let body = {};
      try { body = await r.json(); } catch (_) {}
      if (r.ok) {
        showHandleDisplay(body.handle);
        flash('Handle saved.');
      } else {
        const msg = ({
          'bad_handle': '3–20 chars, letters/digits/_/- only. Not starting with _ or -.',
          'reserved':   "That one's reserved. Try another.",
          'handle_taken': 'Taken. Try another.',
        })[body.code] || ('error: ' + (body.code || r.statusText));
        if (handleHint) { handleHint.textContent = msg; handleHint.classList.add('warn'); }
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

  // The toast's visual lifecycle is driven by the CSS @keyframes toastShow
  // animation (see app.css). JS only adds `.show` to trigger it. The
  // setTimeout below is cleanup-only (drops the class so subsequent
  // flashes restart cleanly); it is NOT what makes the chip disappear.
  let toastTimer;
  function flash(msg) {
    const t = document.getElementById('toast');
    if (!t) return;
    t.textContent = msg;
    // Re-trigger the animation by removing, reflowing, then re-adding.
    t.classList.remove('show');
    void t.offsetWidth;
    t.classList.add('show');
    clearTimeout(toastTimer);
    toastTimer = setTimeout(() => t.classList.remove('show'), 2700);
  }
})();
