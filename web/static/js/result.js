// Result page — bind the share button to the embedded share-card string.
(function() {
  const stateEl = document.getElementById('bbg-state');
  if (!stateEl) return;
  const state = JSON.parse(stateEl.textContent);

  const verdictEl = document.getElementById('verdict');
  if (verdictEl) verdictEl.textContent = verdictFor(state.outcomes, state.mode);

  const btn = document.getElementById('shareBtn');
  if (btn) {
    // Share the public /r/<short> URL so chat clients unfurl the og:image
    // (the grid PNG) into a preview card. The short text is just the
    // bubble copy; the URL is the part that unfurls.
    const url = btn.dataset.shareUrl || window.location.origin;
    const pct = scorePctFromOutcomes(state.outcomes);
    const label = state.mode === 'find_the_human' ? 'Human-Dar' : 'Bot-Dar';
    const bubble = `${label} ${pct}% · Bot Bot Goose`;
    btn.onclick = async () => {
      const result = await window.bbgShare(bubble, url);
      const t = document.getElementById('toast');
      if (!t) return;
      t.textContent = result === 'shared' ? 'Shared.' : 'Link copied.';
      t.classList.add('show');
      setTimeout(() => t.classList.remove('show'), 2200);
    };
  }

  function scorePctFromOutcomes(outs) {
    if (!outs || !outs.length) return 0;
    const caught = outs.filter(o => o === 'green' || o === 'yellow').length;
    return Math.round((caught * 100) / outs.length);
  }

  // ---- decoy submission (You vs the Room — design §4) -------------------
  const decoyForm = document.getElementById('decoyForm');
  if (decoyForm) {
    decoyForm.addEventListener('submit', async (e) => {
      e.preventDefault();
      const ta = document.getElementById('decoyText');
      const submitBtn = document.getElementById('decoySubmit');
      const value = (ta.value || '').trim();
      if (value.length < 4) {
        flash('Give the goose something to hide behind');
        return;
      }
      submitBtn.disabled = true;
      const csrf = readCookie('bbg_csrf_v1') || '';
      let r, body;
      try {
        r = await fetch('/api/decoy/submit', {
          method: 'POST',
          credentials: 'same-origin',
          headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
          body: JSON.stringify({ prompt_id: decoyForm.dataset.promptId, text: value }),
        });
        try { body = await r.json(); } catch (_) { body = {}; }
      } catch (err) {
        submitBtn.disabled = false;
        flash('Network error — please retry');
        return;
      }
      if (r.ok) {
        // Replace the form with a confirmation. The plan's payoff loop §4:
        // "Decoy locked. 🪶 It goes live in tomorrow's puzzle..."
        ta.disabled = true;
        const meta = document.getElementById('decoyHint');
        if (meta) meta.textContent = 'queued for review';
        const shareUrl = body && body.share_url ? body.share_url : '/me';
        submitBtn.outerHTML =
          '<div class="ok">✓ Planted. <a href="' + shareUrl + '" style="color:var(--reed);">See your decoy ▸</a></div>';
        flash('Decoy planted 🪶');
        return;
      }

      submitBtn.disabled = false;
      // Specific friendly handling for the codes the server emits.
      const code = (body && body.code) || '';
      if (code === 'already_submitted') {
        const ex = (body && body.existing) || {};
        const link = ex.share_url || '/me';
        ta.disabled = true;
        const meta = document.getElementById('decoyHint');
        if (meta) meta.textContent = 'one decoy per prompt — design rule';
        submitBtn.outerHTML =
          '<div class="ok">🪶 You’ve already planted one here. <a href="' + link + '" style="color:var(--reed);">See it ▸</a></div>';
        flash('Already planted here.');
        return;
      }
      if (code === 'rate_limited') {
        const secs = (body && body.retry_after_sec) || 0;
        const mins = Math.max(1, Math.round(secs / 60));
        flash('Slow down — try again in ' + mins + ' min');
        return;
      }
      if (code === 'bad_text') {
        flash('Give the goose something to hide behind (4–280 chars)');
        return;
      }
      flash('Submit failed: ' + (code || r.statusText || 'unknown'));
    });
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

  function verdictFor(outs, mode) {
    const caught = (outs || []).filter(o => o !== 'red').length;
    const perfect = (outs || []).every(o => o === 'green');
    const targetWord = mode === 'find_the_human' ? 'human' : 'goose';
    if (perfect) return `Flawless. The ${targetWord === 'human' ? 'bots' : 'bots'} fear you.`;
    if (caught === 3) return `Caught every ${targetWord}. A hint or two slipped in.`;
    if (caught === 2) return `Two of three. The ${targetWord === 'human' ? 'humans' : 'bots'} are getting good.`;
    if (caught === 1) return `One catch. Honk back harder tomorrow.`;
    return `Swept. It happens to everyone once.`;
  }
})();
