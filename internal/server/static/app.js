import { initHTMLeX } from './htmlex/htmlex.js';
import './swap-focus.js';
import './viewer.js';

initHTMLeX();

// Text-fit detection is a browser capability that HTMLeX does not provide.
// Keep native disclosures usable without JavaScript and observe only new posts.
const observed = new WeakSet();
function updateFold(text) {
  const control = text.parentElement?.querySelector('.fold-control');
  if (control && !control.open) control.hidden = text.scrollHeight <= text.clientHeight + 1;
}
const sizeObserver = 'ResizeObserver' in window ? new ResizeObserver(entries => {
  for (const { target } of entries) updateFold(target);
}) : null;
function observeFolds(root) {
  if (!(root instanceof Element)) return;
  if (root.matches('.text-fold .post-text') && !observed.has(root)) {
    observed.add(root);
    sizeObserver?.observe(root);
  }
  for (const text of root.querySelectorAll('.text-fold .post-text')) {
    if (!observed.has(text)) {
      observed.add(text);
      sizeObserver?.observe(text);
    }
  }
}
observeFolds(document.body);
function unobserveFolds(root) {
  if (!(root instanceof Element) || root.isConnected) return;
  if (root.matches('.text-fold .post-text')) {
    sizeObserver?.unobserve(root);
    observed.delete(root);
  }
  for (const text of root.querySelectorAll('.text-fold .post-text')) {
    sizeObserver?.unobserve(text);
    observed.delete(text);
  }
}
new MutationObserver(records => {
  for (const record of records) {
    for (const node of record.removedNodes) unobserveFolds(node);
    for (const node of record.addedNodes) observeFolds(node);
  }
}).observe(document.body, { childList: true, subtree: true });
// A document restored from the back/forward cache may not receive a resize.
window.addEventListener('pageshow', event => {
  if (event.persisted) document.querySelectorAll('.text-fold .post-text').forEach(updateFold);
});
