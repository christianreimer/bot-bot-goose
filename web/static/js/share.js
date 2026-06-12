// Share — mobile native sheet via navigator.share, desktop fallback to clipboard.
window.bbgShare = async function (text, url) {
  if (navigator.share) {
    try {
      await navigator.share({ title: 'Bot Bot Goose', text, url });
      return 'shared';
    } catch (e) {
      // user cancelled; fall through to clipboard fallback
    }
  }
  try {
    await navigator.clipboard.writeText(text);
    return 'copied';
  } catch (e) {
    // Last-ditch contentEditable workaround for legacy browsers.
    const ta = document.createElement('textarea');
    ta.value = text;
    document.body.appendChild(ta);
    ta.select();
    try { document.execCommand('copy'); } catch (e) {}
    ta.remove();
    return 'copied';
  }
};
