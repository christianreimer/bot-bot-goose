// /harvest — Phase-0 collection-campaign deck.
//
// 21 face-down cards in a fixed 3×7 grid. Tapping a card opens a centered overlay
// "on top of" the grid (the grid does NOT move; the original card stays
// in place, dimly highlighted). On Plant ▸ we POST to /api/harvest/submit
// (writes to pre_launch_submissions, NEVER decoy_submissions). The
// overlay closes; the original card stays in its grid slot with the
// user's answer rendered in place of the feather.
(function () {
  'use strict';

  const grid = document.getElementById('cardGrid');
  const counterEl = document.getElementById('counter');
  const totalEl = document.getElementById('total');
  const done = document.getElementById('harvestDone');
  const plantedCount = document.getElementById('plantedCount');
  const overlay = document.getElementById('cardOverlay');
  const overlayScrim = document.getElementById('overlayScrim');
  const overlayClose = document.getElementById('overlayClose');
  const overlayPrompt = document.getElementById('overlayPrompt');
  const overlayForm = document.getElementById('overlayForm');
  const overlayInput = document.getElementById('overlayInput');
  const overlayPlant = document.getElementById('overlayPlant');
  const overlayHint = document.getElementById('overlayHint');
  if (!grid || !overlay) return;

  const csrf = readCookie('bbg_csrf_v1') || '';
  let planted = 0;
  let total = totalEl ? parseInt(totalEl.textContent || '0', 10) : 0;
  let activeCard = null;

  // Tap a card → open the overlay populated from this card's data attrs.
  grid.querySelectorAll('.card').forEach((card) => {
    const face = card.querySelector('.card-face');
    if (!face) return;
    face.addEventListener('click', () => {
      if (card.classList.contains('planted')) return; // inert
      openCard(card);
    });
  });

  // Close affordances: × button, scrim tap, Escape key.
  overlayClose.addEventListener('click', closeOverlay);
  overlayScrim.addEventListener('click', closeOverlay);
  document.addEventListener('keydown', (e) => {
    if (e.key === 'Escape' && !overlay.hidden) closeOverlay();
  });

  overlayForm.addEventListener('submit', async (e) => {
    e.preventDefault();
    if (!activeCard) return;
    const value = (overlayInput.value || '').trim();
    if (value.length < 4) return flashHint('Give the goose something to hide behind.');

    overlayPlant.disabled = true;
    const promptID = activeCard.dataset.promptId;
    let r, body = {};
    try {
      r = await fetch('/api/harvest/submit', {
        method: 'POST',
        credentials: 'same-origin',
        headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': csrf },
        body: JSON.stringify({ prompt_id: promptID, text: value }),
      });
      try { body = await r.json(); } catch (_) { body = {}; }
    } catch (err) {
      overlayPlant.disabled = false;
      return flashHint('Network error. Try again.');
    }

    if (r.ok || (body && body.code === 'already_submitted')) {
      planted++;
      if (counterEl) counterEl.textContent = String(planted);
      markPlanted(activeCard, value);
      closeOverlay();
      if (planted >= total) showDone();
      return;
    }

    overlayPlant.disabled = false;
    const code = (body && body.code) || '';
    if (code === 'rate_limited') {
      const mins = Math.max(1, Math.round((body.retry_after_sec || 60) / 60));
      return flashHint('Slow down. Try again in ' + mins + ' min.');
    }
    if (code === 'bad_text') return flashHint('4 to 280 characters.');
    flashHint('Submit failed: ' + (code || r.statusText || 'unknown'));
  });

  function openCard(card) {
    activeCard = card;
    overlayPrompt.textContent = card.dataset.promptText || '';
    overlayInput.value = '';
    overlayHint.textContent = '4–280 characters · one answer per question per device';
    overlayHint.classList.remove('warn');
    overlayHint.classList.add('muted');
    overlayPlant.disabled = false;
    card.classList.add('active'); // subtle highlight on the source card
    overlay.hidden = false;
    document.body.style.overflow = 'hidden'; // prevent scroll behind the modal
    window.setTimeout(() => overlayInput.focus(), 60);
  }

  function closeOverlay() {
    overlay.hidden = true;
    document.body.style.overflow = '';
    if (activeCard) activeCard.classList.remove('active');
    activeCard = null;
  }

  // markPlanted converts the source card to its persistent "planted" state
  // in place: same grid position, no reflow, no dismiss. The card's face
  // is replaced with the user's own answer text.
  function markPlanted(card, answerText) {
    card.classList.add('planted');
    const face = card.querySelector('.card-face');
    if (face) {
      face.disabled = true;
      face.replaceChildren();
      const span = document.createElement('span');
      span.className = 'card-answer';
      span.textContent = answerText;
      face.appendChild(span);
    }
    card.style.pointerEvents = 'none';
  }

  function showDone() {
    if (done) done.hidden = false;
    if (plantedCount) plantedCount.textContent = String(planted);
  }

  function flashHint(msg) {
    if (!overlayHint) return;
    overlayHint.textContent = msg;
    overlayHint.classList.remove('muted');
    overlayHint.classList.add('warn');
  }

  function readCookie(name) {
    return document.cookie.split('; ')
      .map((c) => c.split('='))
      .filter((p) => p[0] === name)
      .map((p) => decodeURIComponent(p[1]))[0];
  }
})();
