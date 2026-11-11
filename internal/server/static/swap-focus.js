// Focus restoration uses a MutationObserver because HTMLeX does not restore focus after a swap.
let pendingFocus = null;
const swapObserver = new MutationObserver(() => restoreFocus(pendingFocus?.root));

function clearPendingFocus() {
  pendingFocus = null;
  swapObserver.disconnect();
}

document.addEventListener('submit', event => {
  const form = event.target;
  const control = event.submitter;
  if (!(form instanceof HTMLFormElement) || !(control instanceof HTMLElement) || document.activeElement !== control) return;
  const selector = form.dataset.focusAfterSwap || control.dataset.focusAfterSwap;
  const returnFocus = control.hasAttribute('data-focus-return');
  if (!selector && !returnFocus) return;
  const target = form.getAttribute('target');
  const targetSelector = target?.match(/^([^ (]+)/)?.[1];
  const targetElement = targetSelector && document.querySelector(targetSelector);
  const outerSwap = target?.includes('(outerHTML)');
  const root = outerSwap ? targetElement?.parentElement : targetElement;
  if (!root) return;
  pendingFocus = { control, root, selector, returnFocus };
  swapObserver.observe(root, { childList: true, subtree: true });
}, true);

document.addEventListener('focusin', event => {
  if (pendingFocus && event.target !== pendingFocus.control) clearPendingFocus();
});

document.addEventListener('pointerdown', event => {
  if (pendingFocus && !pendingFocus.control.contains(event.target)) clearPendingFocus();
});

function restoreFocus(root) {
  const pending = pendingFocus;
  if (!pending || root !== pending.root || pending.control.isConnected) return;
  if (document.activeElement !== document.body) {
    clearPendingFocus();
    return;
  }
  let next;
  if (pending.returnFocus) {
    const controlID = pending.control.getAttribute('aria-controls');
    next = controlID && document.querySelector(`[aria-controls="${CSS.escape(controlID)}"]`);
  } else {
    next = pending.root.querySelector(pending.selector);
  }
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
