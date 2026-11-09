// CACHE_NAME is replaced at build time by the sw-cache-version vite plugin
// (see vite.config.ts). In dev the placeholder is fine — no real caching needed.
const CACHE_NAME = '__BUILD_HASH__';
const PRECACHE_URLS = ['/', '/index.html'];

self.addEventListener('install', (event) => {
  event.waitUntil(
    caches.open(CACHE_NAME).then((cache) => cache.addAll(PRECACHE_URLS))
  );
  self.skipWaiting();
});

self.addEventListener('activate', (event) => {
  event.waitUntil(
    caches.keys().then((keys) =>
      Promise.all(
        keys.filter((k) => k !== CACHE_NAME).map((k) => caches.delete(k))
      )
    )
  );
  self.clients.claim();
});

// Only a complete, same-origin success is worth keeping. A 404/5xx/opaque
// response stored here would be served back as a "successful" page the next
// time the network fails, and a poisoned /assets/ entry would keep the app
// from booting.
function isCacheable(response) {
  return response.status === 200 && response.type === 'basic';
}

// A failed fetch for an app asset or page may be the browser's HTTP cache
// replaying an old error (a deploy window can 404 a hashed file, and the
// server marks /assets/* immutable for a year). Retry ONCE, bypassing the HTTP
// cache; the successful answer replaces the poisoned entry. Bounded: one retry,
// no loop, and only for same-origin navigations and /assets/.
async function fetchWithRecovery(request) {
  let response = null;
  let failure = null;
  try {
    response = await fetch(request);
    if (response.ok) return response;
  } catch (err) {
    failure = err;
  }
  const url = new URL(request.url);
  const retriable =
    url.origin === self.location.origin &&
    (request.mode === 'navigate' || url.pathname.startsWith('/assets/'));
  if (retriable) {
    try {
      const fresh = await fetch(request.url, {
        cache: 'reload',
        credentials: 'same-origin',
      });
      if (fresh.ok) return fresh;
      if (!response) response = fresh;
    } catch (err) {
      failure = failure || err;
    }
  }
  if (response) return response;
  throw failure;
}

self.addEventListener('fetch', (event) => {
  if (event.request.method !== 'GET') return;
  // Network-first for API calls — never serve stale API responses
  if (event.request.url.includes('/api/')) return;

  event.respondWith(
    fetchWithRecovery(event.request)
      .then((response) => {
        if (isCacheable(response)) {
          const clone = response.clone();
          event.waitUntil(
            caches
              .open(CACHE_NAME)
              .then((cache) => cache.put(event.request, clone))
              .catch(() => {})
          );
        }
        return response;
      })
      .catch(() =>
        // Offline: serve a stored success, otherwise a plain network error —
        // never a stored failure and never `undefined`.
        caches
          .match(event.request)
          .then((hit) => (hit && hit.ok ? hit : Response.error()))
      )
  );
});

self.addEventListener('push', (event) => {
  if (!event.data) return;
  const data = event.data.json();
  event.waitUntil(
    self.registration.showNotification(data.title, {
      body: data.body,
      tag: data.tag,
      icon: data.icon || '/icons/icon-192.png',
      badge: '/icons/badge-72.png',
      data: { url: data.url },
    })
  );
});

self.addEventListener('notificationclick', (event) => {
  event.notification.close();
  event.waitUntil(
    self.clients.matchAll({ type: 'window' }).then((clients) => {
      const url = event.notification.data?.url || '/';
      for (const c of clients) {
        if (c.url.includes(url)) return c.focus();
      }
      return self.clients.openWindow(url);
    })
  );
});
