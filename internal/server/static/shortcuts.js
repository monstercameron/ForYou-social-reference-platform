// Keyboard shortcuts are a browser capability HTMLeX does not provide; every key presses a control or follows a link already on the page.
const OFF = /(?:^|;\s*)foryou_shortcuts=off(?:;|$)/;
const FIELD = 'input, textarea, select, [contenteditable], [role=textbox]';
const HIDDEN = '[aria-hidden="true"], .thread-collapsing, [hidden], details:not([open])';
const ITEM_KEYS = new Set(['o', 'Enter', 'l', 'r', 's']);
const CHORDS = new Map([['h', '/'], ['e', '/explore'], ['w', '/wallet'], ['s', '/settings']]);

const rendered = el => el.getClientRects().length > 0;
const field = el => el instanceof Element && el.closest(FIELD);
const current = () => document.querySelector('[data-sc-current]');
const own = (item, selector) => [...item.querySelectorAll(selector)].filter(el => el.closest('article') === item && rendered(el));
const pick = (item, ...selectors) => selectors.map(selector => own(item, selector)[0]).find(Boolean);

document.body.insertAdjacentHTML('beforeend', '<div id="sc-status" class="sc-sr-only" role="status" aria-live="polite"></div><div id="sc-chord" class="sc-chord" aria-hidden="true" hidden><kbd class="sc-kbd">g</kbd> … <kbd class="sc-kbd">h</kbd> Home · <kbd class="sc-kbd">e</kbd> Explore · <kbd class="sc-kbd">w</kbd> Wallet · <kbd class="sc-kbd">s</kbd> Settings</div>');
const region = document.getElementById('sc-status');
const chip = document.getElementById('sc-chord');

function announce(message) {
  region.textContent = '';
  requestAnimationFrame(() => region.textContent = message);
}

function select(item) {
  current()?.removeAttribute('data-sc-current');
  item.setAttribute('data-sc-current', '');
  if (!item.hasAttribute('tabindex')) item.setAttribute('tabindex', '-1');
  item.focus({ preventScroll: true });
  item.scrollIntoView({ block: 'nearest', behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth' });
}

function move(step) {
  const list = [...document.querySelectorAll('main article')].filter(el => rendered(el) && !el.closest(HIDDEN));
  if (!list.length) return announce('No posts here');
  const index = list.indexOf(current());
  if (index < 0) {
    const below = list.findIndex(el => el.getBoundingClientRect().top >= 0);
    return select(list[below < 0 ? list.length - 1 : below]);
  }
  const next = index + step;
  if (next < 0) return announce('Top of the list');
  if (next >= list.length) {
    const more = [...document.querySelectorAll('.load-more button')].find(rendered);
    if (more) more.focus({ preventScroll: true });
    else announce('No more posts loaded here');
    return;
  }
  select(list[next]);
}

function openCurrent() {
  const link = own(current(), 'a[href^="/posts/"], a[href^="/thread?"]')[0];
  if (link) location.assign(link.href);
  else announce('This item is already open');
}

function press(message, ...selectors) {
  const button = pick(current(), ...selectors);
  if (button) button.click();
  else announce(message);
}

const like = () => press('Like is not available here', '[data-shortcut="like"]', 'button[aria-label="Like"]');
const save = () => press('Save is not available here', '[data-shortcut="save"]', 'button[aria-label="Save"]', 'button[aria-label="Saved"]');

function focusField(item) {
  const textarea = own(item, 'textarea')[0];
  return textarea ? (textarea.focus(), true) : false;
}

function reply() {
  const item = current();
  const closed = own(item, 'details:not([open])').find(d => d.querySelector(':scope > summary')?.textContent.trim() === 'Reply');
  if (closed) {
    closed.open = true;
    focusField(item);
    return;
  }
  if (focusField(item)) return;
  const toggle = pick(item, 'button[data-thread-control="open"]', 'button.thread-toggle[aria-expanded="false"]');
  if (!toggle) return announce('No reply box for this item');
  const stop = () => {
    clearTimeout(timer);
    document.removeEventListener('htmlex:dom-updated', onUpdate);
  };
  const onUpdate = event => {
    if (!(event.detail?.root instanceof Element) || !item.contains(event.detail.root)) return;
    stop();
    focusField(item);
  };
  const timer = setTimeout(stop, 5000);
  document.addEventListener('htmlex:dom-updated', onUpdate);
  toggle.click();
}

function search() {
  const input = [...document.querySelectorAll('main input[type="search"]')].find(rendered);
  if (!input) return location.assign('/explore#search');
  input.focus();
  input.select();
}

function escape() {
  const open = document.activeElement?.closest?.('details[open]');
  if (open) {
    open.open = false;
    open.querySelector(':scope > summary')?.focus();
    return;
  }
  const item = current();
  const hide = item && pick(item, 'button[aria-expanded="true"][data-thread-control="hide"]');
  if (hide) return hide.click();
  const close = [...document.querySelectorAll('main [data-panel-close], main button[aria-label^="Close"]')].find(rendered);
  if (close) return close.click();
  current()?.removeAttribute('data-sc-current');
}

let chordTimer = 0;
function endChord() {
  clearTimeout(chordTimer);
  chordTimer = 0;
  chip.hidden = true;
}
function startChord() {
  endChord();
  chip.hidden = false;
  chordTimer = setTimeout(endChord, 1500);
}

let help = null;
let helpOpener = null;
async function loadHelp(dialog) {
  try {
    const response = await fetch('/shortcuts/dialog', { credentials: 'same-origin', signal: AbortSignal.timeout(5000) });
    if (!response.ok) throw new Error(`status ${response.status}`);
    dialog.innerHTML = await response.text();
  } catch {
    dialog.innerHTML = '<p role="alert">Could not load the list. <a href="/shortcuts">Open the shortcuts page</a>.</p><button type="button" class="button button-secondary" data-sc-close>Close</button>';
  }
  if (dialog.open) dialog.querySelector('[data-sc-close]')?.focus();
}
function openHelp() {
  if (!help) {
    help = document.createElement('dialog');
    help.id = 'sc-help';
    help.className = 'sc-dialog';
    help.setAttribute('aria-labelledby', 'sc-help-title');
    help.addEventListener('click', event => {
      if (event.target instanceof Element && event.target.closest('[data-sc-close]')) help.close();
    });
    help.addEventListener('close', () => { helpOpener?.focus?.({ preventScroll: true }); helpOpener = null; });
    document.body.append(help);
  }
  helpOpener = document.activeElement;
  help.innerHTML = '<p class="sc-status" role="status">Loading shortcuts…</p>';
  help.showModal();
  loadHelp(help);
}

const ACTIONS = new Map([
  ['j', () => move(1)],
  ['k', () => move(-1)],
  ['o', openCurrent],
  ['Enter', openCurrent],
  ['l', like],
  ['r', reply],
  ['s', save],
  ['n', () => location.assign('/compose')],
  ['/', search],
  ['g', startChord],
  ['?', openHelp],
  ['Escape', escape],
]);

document.addEventListener('keydown', event => {
  if (OFF.test(document.cookie) || event.defaultPrevented || event.isComposing) return;
  if (event.ctrlKey || event.metaKey || event.altKey) return;
  if (event.shiftKey && event.key !== '?') return;
  if (field(event.target) || field(document.activeElement)) return;
  if (document.querySelector('#viewer [data-viewer-close], dialog[open]:not(#sc-help)') || help?.open) return;
  if (chordTimer) {
    const destination = CHORDS.get(event.key);
    endChord();
    if (destination) {
      event.preventDefault();
      location.assign(destination);
      return;
    }
  }
  if (event.key === 'Enter' && document.activeElement !== current()) return;
  const action = ACTIONS.get(event.key);
  if (!action) return;
  event.preventDefault();
  if (ITEM_KEYS.has(event.key) && !current()) announce('Select a post first with j');
  else action();
});

if (location.hash === '#search') {
  [...document.querySelectorAll('main input[type="search"]')].find(rendered)?.focus();
}
