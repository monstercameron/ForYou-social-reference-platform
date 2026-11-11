import { hooks, initHTMLeX } from './htmlex.js';

const lifecycleMarkers = {
  'todo:create:before': 'beforeTodoCreate',
  'todo:create:after': 'afterTodoCreate',
  'todo:create:before-swap': 'beforeTodoSwap',
  'todo:create:after-swap': 'afterTodoSwap',
  'chat:send:before': 'beforeChatSend',
  'chat:send:after': 'afterChatSend'
};

function getRuntimeWindow() {
  return typeof window === 'undefined' ? globalThis.window : window;
}

function getRuntimeDocument() {
  return typeof document === 'undefined' ? globalThis.document : document;
}

function markLifecycle(marker) {
  const runtimeWindow = getRuntimeWindow();
  if (!runtimeWindow || typeof runtimeWindow !== 'object') return;

  try {
    runtimeWindow.__htmlexLifecycle = marker;
  } catch {
    // Lifecycle markers are demo diagnostics only; hook execution should not fail the action.
  }
}

for (const [name, marker] of Object.entries(lifecycleMarkers)) {
  hooks.register(name, () => {
    markLifecycle(marker);
  }, { owner: 'main', replace: true });
}

const runtimeDocument = getRuntimeDocument();
if (runtimeDocument) {
  const initialize = () => initHTMLeX();
  try {
    if (runtimeDocument.readyState && runtimeDocument.readyState !== 'loading') {
      initialize();
    } else if (typeof runtimeDocument.addEventListener === 'function') {
      runtimeDocument.addEventListener('DOMContentLoaded', initialize, { once: true });
    }
  } catch {
    // Importing the browser entrypoint in non-DOM runtimes should be a no-op.
  }
}
