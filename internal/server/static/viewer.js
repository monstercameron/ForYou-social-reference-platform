// The image viewer opens over the page. Moving focus into it, returning focus
// to the picture and closing with the keyboard are browser capabilities HTMLeX
// does not provide.
const viewer = document.getElementById('viewer');
let opener = null;
let direction = '';
if (viewer) {
  document.addEventListener('submit', event => {
    const form = event.target;
    if (!(form instanceof HTMLFormElement)) return;
    if (form.matches('.gallery-open, .image-open')) opener = event.submitter;
    direction = viewer.contains(form) ? form.dataset.galleryDirection || '' : '';
  }, true);
  new MutationObserver(() => {
    const close = viewer.querySelector('[data-viewer-close]');
    if (close) {
      const next = direction && viewer.querySelector(`[data-gallery-direction="${direction}"] button`);
      if (!viewer.contains(document.activeElement)) (next || close).focus({ preventScroll: true });
    } else if (opener?.isConnected) {
      opener.focus({ preventScroll: true });
      opener = null;
    }
  }).observe(viewer, { childList: true });
  const keys = {
    Escape: '[data-viewer-close]',
    ArrowLeft: '[data-gallery-direction="previous"] button',
    ArrowRight: '[data-gallery-direction="next"] button',
  };
  document.addEventListener('keydown', event => {
    const control = keys[event.key] && viewer.querySelector(keys[event.key]);
    if (!control) return;
    event.preventDefault();
    control.click();
  });
}
