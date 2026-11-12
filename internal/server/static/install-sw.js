// Offline fallback and static asset caching are browser capabilities HTMLeX does not provide.
// Bumping instCache discards the copies kept by earlier versions of this worker.
const instCache = 'foryou-install-v1';
const instPrecache = ['/offline', '/static/tailwind.css', '/install/icon.svg'];
const instStaticScript = /^\/static\/.+\.(css|js)$/;

self.addEventListener('install', event => {
  event.waitUntil(caches.open(instCache).then(cache => cache.addAll(instPrecache)).then(() => self.skipWaiting()));
});

self.addEventListener('activate', event => {
  event.waitUntil(
    caches.keys()
      .then(names => Promise.all(names.filter(name => name.startsWith('foryou-install-') && name !== instCache).map(name => caches.delete(name))))
      .then(() => self.clients.claim())
  );
});

self.addEventListener('fetch', event => {
  const { request } = event;
  const url = new URL(request.url);
  if (request.method !== 'GET' || url.origin !== self.location.origin) return;
  if (request.mode === 'navigate') {
    event.respondWith(fetch(request).catch(() => caches.match('/offline')));
    return;
  }
  const staticAsset = instStaticScript.test(url.pathname) || (url.pathname.startsWith('/install/') && /\.(svg|png)$/.test(url.pathname));
  if (!staticAsset) return;
  event.respondWith(
    fetch(request)
      .then(response => {
        if (response.ok && response.type === 'basic') {
          const copy = response.clone();
          caches.open(instCache).then(cache => cache.put(request, copy));
        }
        return response;
      })
      .catch(() => caches.match(request))
  );
});
