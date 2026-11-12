// Announcing fragment swaps to screen readers and moving focus into opened panels are browser capabilities HTMLeX does not provide.
const live = document.getElementById('a11y-live');
const openers = new Map();
let pending = null;
let announceTimer = 0;

function finish(swap) {
  swap.observer.disconnect();
  clearTimeout(swap.timeout);
  if (pending === swap) pending = null;
}

function announce(message) {
  live.textContent = '';
  clearTimeout(announceTimer);
  announceTimer = setTimeout(() => { live.textContent = message; }, 60);
}

function managesFocus({ form, submitter }) {
  return [form, submitter].some((el) => el?.hasAttribute('data-focus-after-swap') || el?.hasAttribute('data-focus-return'));
}

function messageFor({ form, outer, label, pressed }, target) {
  const announced = target.querySelector('[data-a11y-announce]')?.dataset.a11yAnnounce || form.dataset.a11yAnnounce;
  if (announced) return announced;
  if (form.getAttribute('post') === '/reply/fragment') return 'Reply added to the thread.';
  const button = pressed !== null && [...target.querySelectorAll('button[aria-pressed]')].find((b) => b.getAttribute('aria-label') === label);
  if (button) return `${label} ${button.getAttribute('aria-pressed') === 'true' ? 'selected' : 'cleared'}`;
  const isLive = target.matches('[role=status], [role=alert], [aria-live]');
  const text = target.querySelector('[role=status], [role=alert]')?.textContent.trim() ?? '';
  return (outer || !isLive) && text.length > 0 && text.length <= 200 ? text : '';
}

function focusOpened(target, swap) {
  openers.set(swap.targetID, swap.submitter);
  const first = target.firstElementChild;
  const named = first?.hasAttribute('aria-label') || first?.hasAttribute('aria-labelledby');
  const choice = target.querySelector('[data-a11y-focus]') || target.querySelector('h1, h2, h3, h4') || (named ? first : null) || target.querySelector('button, a[href], input, textarea, select');
  if (!choice) return;
  if (!choice.matches('a[href], button, input, textarea, select, [tabindex]')) choice.tabIndex = -1;
  choice.focus({ preventScroll: true });
  choice.scrollIntoView({ block: 'nearest' });
}

function focusClosed({ targetID }) {
  const opener = openers.get(targetID);
  openers.delete(targetID);
  const control = opener?.isConnected ? opener : document.querySelector(`[aria-controls="${CSS.escape(targetID)}"]`);
  control?.focus({ preventScroll: true });
}

function settle(swap) {
  const target = document.getElementById(swap.targetID);
  if (!target) return;
  const message = messageFor(swap, target);
  if (message) announce(message);
  const active = document.activeElement;
  if (managesFocus(swap) || (active && active !== document.body && active.isConnected)) return;
  if (target.children.length) focusOpened(target, swap);
  else focusClosed(swap);
}

if (live) {
  document.addEventListener('submit', (event) => {
    if (pending) finish(pending);
    const form = event.target;
    if (!(form instanceof HTMLFormElement) || !form.hasAttribute('target') || !(form.hasAttribute('get') || form.hasAttribute('post'))) return;
    const target = form.getAttribute('target');
    const targetID = target.split('(')[0].replace(/^#/, '');
    if (targetID === 'viewer') return;
    const outer = target.includes('(outerHTML)');
    const element = document.getElementById(targetID);
    const root = outer ? element?.parentElement : element;
    if (!root) return;
    const submitter = event.submitter;
    const pressed = submitter?.getAttribute('aria-pressed') ?? null;
    const label = submitter?.getAttribute('aria-label') || submitter?.textContent.trim() || '';
    const swap = { form, submitter, targetID, outer, label, pressed, observer: new MutationObserver(() => {
      finish(swap);
      requestAnimationFrame(() => settle(swap));
    }), timeout: 0 };
    swap.observer.observe(root, { childList: true, subtree: true });
    swap.timeout = setTimeout(() => finish(swap), 10000);
    pending = swap;
  }, true);
}
