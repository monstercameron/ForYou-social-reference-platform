// Service worker registration is a browser capability HTMLeX does not provide.
if ('serviceWorker' in navigator) navigator.serviceWorker.register('/sw.js', { scope: '/' }).catch(() => {});
