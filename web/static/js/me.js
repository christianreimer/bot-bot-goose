// /me — wire each per-decoy "Share report" button to the pre-built card.
// The card text is server-rendered into data-share-text; JS just reads it.
(function() {
  'use strict';
  const buttons = document.querySelectorAll('.btn-share[data-share-text]');
  buttons.forEach((btn) => {
    btn.addEventListener('click', async () => {
      const text = btn.dataset.shareText || '';
      if (!text) return;
      const result = await window.bbgShare(text, window.location.origin);
      flash(result === 'shared' ? 'Shared.' : 'Decoy report copied.');
    });
  });

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
