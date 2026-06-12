// Standalone decoy-share page — the share button hands the same pre-built
// card text that /me's row buttons use, so a screenshot from anywhere
// produces the same artifact.
(function() {
  'use strict';
  const btn = document.getElementById('shareBtn');
  if (!btn) return;
  btn.addEventListener('click', async () => {
    const text = btn.dataset.shareText || '';
    if (!text) return;
    const result = await window.bbgShare(text, window.location.href);
    const t = document.getElementById('toast');
    if (!t) return;
    t.textContent = result === 'shared' ? 'Shared.' : 'Report copied.';
    t.classList.add('show');
    setTimeout(() => t.classList.remove('show'), 2200);
  });
})();
