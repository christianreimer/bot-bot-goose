// Bot Bot Goose service worker — v1.
//
// Scope is intentionally minimal: cache the app shell + static assets, do
// NOT cache the play API (the whole anti-cheat story depends on no stale
// reveals). Push notifications hook is stubbed for when VAPID lands.

// Bump on any change to the cached set. The browser only swaps SWs after the
// old one's clients unload — bumping here is what unsticks a hot-fix for
// users who already installed.
const CACHE = 'bbg-shell-v3';

// IMPORTANT: never cache "/" or any /play/* page. Those carry server-rendered
// state (current round, play token, mode). Caching them would freeze a stale
// view and defeat the anti-cheat story.
//
// Static CSS/JS aren't pre-cached anymore: the server emits content-hashed
// URLs ("/static/css/app.css?v=<hash>"), so a content change produces a new
// URL that misses cache and falls through to fetch automatically. The fetch
// handler below still caches the hashed URL on first hit.
const SHELL = [
  '/manifest.json',
];

self.addEventListener('install', (event) => {
  event.waitUntil(caches.open(CACHE).then(c => c.addAll(SHELL).catch(() => {})));
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then(keys =>
      Promise.all(keys.filter(k => k !== CACHE).map(k => caches.delete(k)))
    )
  );
  self.clients.claim();
});

self.addEventListener('fetch', (event) => {
  const url = new URL(event.request.url);
  // Never intercept API, play pages, or the root. Labels and play state
  // must always be fresh from the server.
  if (url.pathname.startsWith('/api/') ||
      url.pathname.startsWith('/play/') ||
      url.pathname === '/') {
    return;
  }
  if (url.pathname.startsWith('/static/') || url.pathname === '/manifest.json') {
    event.respondWith(
      caches.match(event.request).then(r => r || fetch(event.request))
    );
  }
});

self.addEventListener('push', (event) => {
  if (!event.data) return;
  let payload = {};
  try { payload = event.data.json(); } catch (e) { payload = { body: event.data.text() }; }
  event.waitUntil(
    self.registration.showNotification(payload.title || 'Today\'s goose is loose 🪿', {
      body: payload.body || 'A fresh puzzle is waiting.',
      icon: '/static/icons/goose-192.png',
      badge: '/static/icons/goose-192.png',
      data: { url: payload.url || '/' },
    })
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  const target = (event.notification.data && event.notification.data.url) || '/';
  event.waitUntil(self.clients.openWindow(target));
});
