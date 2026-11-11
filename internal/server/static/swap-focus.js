// HTMLeX may replace a focused submit button during a fragment swap.
// Preserve focus only if the user has not focused or clicked somewhere else.
let pendingFocus = null;
const swapObserver = new MutationObserver(() => restoreFocus(pendingFocus?.root));
function clearPendingFocus() {
  pendingFocus = null;
  swapObserver.disconnect();
}
document.addEventListener('submit', event => {
  const form = event.target;
  const button = event.submitter;
  if (!(form instanceof HTMLFormElement) || !(button instanceof HTMLButtonElement) ||
      document.activeElement !== button) return;
  const vote = form.dataset.feedbackVote;
  if ((vote === 'up' || vote === 'down') && form.hasAttribute('post')) {
    const control = form.closest('[id^="feedback-"]');
    const root = control?.parentElement;
    if (root && control) {
      pendingFocus = { root, button, selector: `#${control.id} form[data-feedback-vote="${vote}"] button` };
      swapObserver.observe(root, { childList: true, subtree: true });
    }
    return;
  }
  if (form.hasAttribute('data-compose-submit') && form.hasAttribute('post')) {
    const root = form.closest('#compose-main')?.parentElement;
    if (root) {
      pendingFocus = { root, button, selector: '#compose-main [data-compose-result]' };
      swapObserver.observe(root, { childList: true, subtree: true });
    }
    return;
  }
  if (!form.hasAttribute('get')) return;
  const direction = form.dataset.galleryDirection;
  const threadTarget = form.dataset.threadFocus;
  if (direction === 'previous' || direction === 'next') {
    const root = form.closest('[id^="gallery-"]');
    if (root) pendingFocus = { root, button, selector: `form[data-gallery-direction="${direction}"] button` };
  } else if (form.hasAttribute('data-share-close')) {
    const root = form.closest('[id^="share-"]');
    const trigger = root?.parentElement?.querySelector('[data-share-trigger]');
    if (root && trigger) {
      pendingFocus = { root, button, trigger };
      swapObserver.observe(root, { childList: true, subtree: true });
    }
  } else if (threadTarget === 'open' || threadTarget === 'hide') {
    const root = form.closest('[id^="thread-"]');
    if (root) pendingFocus = { root, button, selector: `button[data-thread-control="${threadTarget}"]` };
  }
}, true);
document.addEventListener('focusin', event => {
  if (pendingFocus && event.target !== pendingFocus.button) clearPendingFocus();
});
document.addEventListener('pointerdown', event => {
  if (pendingFocus && !pendingFocus.button.contains(event.target)) clearPendingFocus();
});
function restoreFocus(root) {
  const pending = pendingFocus;
  if (!pending || root !== pending.root) return;
  if (pending.button.isConnected) return;
  if (document.activeElement !== document.body) {
    clearPendingFocus();
    return;
  }
  const next = pending.trigger || pending.root.querySelector(pending.selector);
  // Closing a thread uses an intermediate animation before its final control.
  if (!next || next.closest('.thread-collapsing')) return;
  clearPendingFocus();
  next.focus({ preventScroll: true });
}
document.addEventListener('htmlex:dom-updated', event => restoreFocus(event.detail?.root));

// Clipboard writes are a browser capability HTMLeX does not provide.
document.addEventListener('click', async event => {
  const button = event.target.closest('[data-copy-target]');
  const field = button && document.getElementById(button.dataset.copyTarget);
  const status = button?.closest('.share-panel')?.querySelector('[data-copy-status]');
  if (!(button instanceof HTMLButtonElement) || !(field instanceof HTMLInputElement || field instanceof HTMLTextAreaElement) || !status) return;
  status.textContent = '';
  try {
    await navigator.clipboard.writeText(field.value);
    status.textContent = 'Copied';
  } catch {
    field.focus();
    field.select();
    status.textContent = 'Select and copy the field';
  }
});
