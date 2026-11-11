/**
 * @module Registration
 * @description Scans the DOM for HTMLeX-enabled elements and registers them.
 *
 * Module namespace objects are immutable, so this module exports a DOM update
 * wrapper for response paths that need sequential queue handling.
 */

import { Logger } from './logger.js';
import { registerSignalListener, emitSignal } from './signals.js';
import { handleAction } from './actions.js';
import { debounce, throttle } from './rateLimit.js';
import {
  dispatchHTMLeXDOMUpdated,
  hasHTMLeXMarkup,
  parseTargets,
  querySelectorAllSafe,
  querySelectorSafe,
  updateTarget as originalUpdateTarget
} from './dom.js';
import { handleWebSocket } from './websocket.js';

const METHOD_ATTRIBUTES = ['get', 'post', 'put', 'delete', 'patch'];
const REGISTRATION_ATTRIBUTES = [
  ...METHOD_ATTRIBUTES,
  'auto', 'poll', 'socket', 'subscribe', 'publish',
  'trigger', 'debounce', 'throttle', 'timer', 'sequential', 'repeat'
];
const REGISTRATION_SELECTORS = [
  '[get]', '[post]', '[put]', '[delete]', '[patch]',
  '[socket]', '[publish]', '[timer]'
];
const REGISTRATION_SELECTOR_STRING = REGISTRATION_SELECTORS.join(',');
const COMMON_ON_ATTRIBUTE_EVENTS = new Set([
  'click', 'submit', 'input', 'change', 'load', 'reset',
  'focus', 'blur', 'keydown', 'keyup', 'keypress',
  'mouseenter', 'mouseleave', 'mouseover', 'mouseout', 'mousemove',
  'mousedown', 'mouseup', 'dblclick',
  'pointerenter', 'pointerleave', 'pointermove', 'pointerdown', 'pointerup',
  'touchstart', 'touchmove', 'touchend', 'touchcancel'
]);
const ELEMENT_NODE_TYPE = 1;

function safeString(value, fallback = '') {
  try {
    return String(value ?? fallback);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to coerce registration value to string.', error);
    return fallback;
  }
}

function getObjectField(value, fieldName, fallback = undefined) {
  try {
    return value?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read registration field "${fieldName}".`, error);
    return fallback;
  }
}

function setObjectField(value, fieldName, fieldValue) {
  try {
    if (value && typeof value === 'object') {
      value[fieldName] = fieldValue;
      return true;
    }
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to set registration field "${fieldName}".`, error);
  }
  return false;
}

function deleteObjectField(value, fieldName) {
  try {
    if (value && typeof value === 'object') {
      delete value[fieldName];
    }
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to delete registration field "${fieldName}".`, error);
  }
}

function getArrayLength(value) {
  try {
    const length = value?.length;
    return Number.isSafeInteger(length) && length > 0 ? length : 0;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read registration array length.', error);
    return 0;
  }
}

function getArrayItem(value, index, label) {
  try {
    return value?.[index];
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read ${label}.`, error);
    return undefined;
  }
}

function appendArrayItem(value, item, label) {
  try {
    value[value.length] = item;
    return true;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to append ${label}.`, error);
    return false;
  }
}

function popArrayItem(value, label) {
  const length = getArrayLength(value);
  if (!length) return null;
  const item = getArrayItem(value, length - 1, label);
  try {
    value.length = length - 1;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to remove ${label}.`, error);
  }
  return item;
}

function clearArray(value, label) {
  if (!value) return;
  try {
    value.length = 0;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to clear ${label}.`, error);
  }
}

function getNonNegativeIntegerField(value, fieldName) {
  const fieldValue = getObjectField(value, fieldName, 0);
  return Number.isSafeInteger(fieldValue) && fieldValue > 0 ? fieldValue : 0;
}

function forEachIterableItem(value, label, callback) {
  try {
    if (!value) return;
    for (const item of value) {
      callback(item);
    }
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to iterate ${label}.`, error);
  }
}

function addSetItem(value, item, label) {
  try {
    value?.add?.(item);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to add ${label}.`, error);
  }
}

function deleteSetItem(value, item, label) {
  try {
    value?.delete?.(item);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to delete ${label}.`, error);
  }
}

function clearSetLike(value, label) {
  try {
    value?.clear?.();
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to clear ${label}.`, error);
  }
}

function getTargetSelector(target) {
  return safeString(getObjectField(target, 'selector', '')).trim();
}

function getElementNodeType() {
  return getObjectField(getObjectField(globalThis, 'Node'), 'ELEMENT_NODE', ELEMENT_NODE_TYPE);
}

function isElementNode(node) {
  return getObjectField(node, 'nodeType') === getElementNodeType();
}

function getRuntimeWindow() {
  return typeof window === 'undefined' ? globalThis.window : window;
}

function getRuntimeDocument() {
  return typeof document === 'undefined' ? globalThis.document : document;
}

function getDocumentBody(runtimeDocument = getRuntimeDocument()) {
  try {
    return runtimeDocument?.body || null;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read document body.', error);
    return null;
  }
}

function getWindowField(runtimeWindow, fieldName) {
  try {
    return runtimeWindow?.[fieldName];
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read window field "${fieldName}".`, error);
    return undefined;
  }
}

function setWindowField(runtimeWindow, fieldName, value) {
  try {
    if (runtimeWindow && typeof runtimeWindow === 'object') {
      runtimeWindow[fieldName] = value;
    }
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to set window field "${fieldName}".`, error);
  }
}

function addDocumentEventListener(runtimeDocument, eventName, listener) {
  try {
    if (typeof runtimeDocument?.addEventListener !== 'function') return false;
    runtimeDocument.addEventListener(eventName, listener);
    return true;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to add document listener "${eventName}".`, error);
    return false;
  }
}

function removeDocumentEventListener(runtimeDocument, eventName, listener) {
  try {
    runtimeDocument?.removeEventListener?.(eventName, listener);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to remove document listener "${eventName}".`, error);
  }
}

function getDOMUpdateRoot(event, fallbackRoot) {
  try {
    return event?.detail?.root || fallbackRoot;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read DOM update event root.', error);
    return fallbackRoot;
  }
}

function isElementLike(value) {
  if (!value || typeof value !== 'object') return false;

  const ElementConstructor = getObjectField(globalThis, 'Element');
  if (typeof ElementConstructor === 'function') {
    try {
      if (value instanceof ElementConstructor) return true;
    } catch {
      // Fall through to structural detection for hostile constructors.
    }
  }

  return isElementNode(value) && (
    typeof value.matches === 'function' ||
    typeof value.closest === 'function' ||
    typeof value.querySelectorAll === 'function'
  );
}

function createHTMLeXEvent(type) {
  const EventConstructor = getObjectField(globalThis, 'Event');
  if (typeof EventConstructor === 'function') {
    try {
      return new EventConstructor(type);
    } catch {
      // Fall through to a minimal event snapshot.
    }
  }

  return {
    type,
    target: null,
    currentTarget: null,
    defaultPrevented: false,
    preventDefault() {
      this.defaultPrevented = true;
    },
    stopPropagation() {},
    stopImmediatePropagation() {}
  };
}

function isElementConnected(element) {
  try {
    const body = getDocumentBody();
    if (typeof body?.contains !== 'function') return true;
    return body.contains(element);
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to determine whether an element is connected.', error);
    return false;
  }
}

function safeMatches(element, selector) {
  try {
    return typeof element?.matches === 'function' && element.matches(selector);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to match selector "${selector}" against an element.`, error);
    return false;
  }
}

function safeQuerySelectorAll(root, selector) {
  try {
    return typeof root?.querySelectorAll === 'function' ? root.querySelectorAll(selector) : [];
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to query selector "${selector}" within a DOM subtree.`, error);
    return [];
  }
}

function safeClosest(element, selector) {
  try {
    return typeof element?.closest === 'function' ? element.closest(selector) : null;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to resolve closest selector "${selector}".`, error);
    return null;
  }
}

function getElementTagName(element) {
  try {
    return safeString(getObjectField(element, 'tagName', '')).toLowerCase();
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to read element tagName.', error);
    return '';
  }
}

function hasElementAttribute(element, attributeName) {
  try {
    return Boolean(element?.hasAttribute?.(attributeName));
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read attribute "${attributeName}".`, error);
    return false;
  }
}

function getElementAttribute(element, attributeName) {
  try {
    return element?.getAttribute?.(attributeName) ?? null;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read attribute "${attributeName}".`, error);
    return null;
  }
}

function setElementAttribute(element, attributeName, value) {
  try {
    element?.setAttribute?.(attributeName, value);
    return true;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to set attribute "${attributeName}".`, error);
    return false;
  }
}

function removeElementAttribute(element, attributeName) {
  try {
    element?.removeAttribute?.(attributeName);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to remove attribute "${attributeName}".`, error);
  }
}

function getEventField(event, fieldName) {
  try {
    return event?.[fieldName];
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to read event field "${fieldName}".`, error);
    return undefined;
  }
}

function callEventMethod(event, methodName) {
  try {
    const method = getEventField(event, methodName);
    if (typeof method === 'function') method.call(event);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to call event method "${methodName}".`, error);
  }
}

function scheduleTimeout(callback, delayMs, label) {
  const runSafely = () => {
    try {
      callback();
    } catch (error) {
      Logger.system.error(`[HTMLeX] Scheduled ${label} callback failed.`, error);
    }
  };

  if (typeof globalThis.setTimeout !== 'function') {
    Logger.system.warn(`[HTMLeX] setTimeout is unavailable; ${label} cannot be delayed.`);
    if (delayMs <= 0) runSafely();
    return null;
  }

  try {
    return globalThis.setTimeout(runSafely, delayMs);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to schedule ${label}.`, error);
    if (delayMs <= 0) runSafely();
    return null;
  }
}

function clearScheduledTimeout(timerId, label = 'timer') {
  if (timerId === null || timerId === undefined) return;
  if (typeof globalThis.clearTimeout !== 'function') return;

  try {
    globalThis.clearTimeout(timerId);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to clear ${label}.`, error);
  }
}

function scheduleInterval(callback, intervalMs, label) {
  if (typeof globalThis.setInterval !== 'function') {
    Logger.system.warn(`[HTMLeX] setInterval is unavailable; ${label} cannot be scheduled.`);
    return null;
  }

  try {
    return globalThis.setInterval(callback, intervalMs);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to schedule ${label}.`, error);
    return null;
  }
}

function clearScheduledInterval(intervalId, label = 'interval') {
  if (intervalId === null || intervalId === undefined) return;
  if (typeof globalThis.clearInterval !== 'function') return;

  try {
    globalThis.clearInterval(intervalId);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to clear ${label}.`, error);
  }
}

function disconnectObserver(observer, label = 'observer') {
  try {
    observer?.disconnect?.();
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to disconnect ${label}.`, error);
  }
}

function addElementEventListener(element, eventName, listener) {
  try {
    if (typeof element?.addEventListener !== 'function') return false;
    element.addEventListener(eventName, listener);
    return true;
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to add "${eventName}" event listener.`, error);
    return false;
  }
}

function removeElementEventListener(element, eventName, listener) {
  try {
    element?.removeEventListener?.(eventName, listener);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to remove "${eventName}" event listener.`, error);
  }
}

function removeElementNode(element) {
  try {
    element?.remove?.();
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to remove element.', error);
  }
}

function setElementInnerHTML(element, html) {
  try {
    element.innerHTML = html;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to update element content.', error);
  }
}

function insertElementHTML(element, position, html) {
  try {
    element?.insertAdjacentHTML?.(position, html);
  } catch (error) {
    Logger.system.warn(`[HTMLeX] Failed to insert HTML at "${position}".`, error);
  }
}

function safeAbort(controller) {
  try {
    controller?.abort?.();
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to abort an in-flight request.', error);
  }
}

function createAbortController() {
  if (typeof globalThis.AbortController !== 'function') {
    Logger.system.warn('[HTMLeX] AbortController is unavailable; requests cannot be canceled by registration cleanup.');
    return { signal: null, abort() {} };
  }

  try {
    return new globalThis.AbortController();
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to create AbortController.', error);
    return { signal: null, abort() {} };
  }
}

function observeElementAttributes(element, callback, attributeFilter) {
  if (typeof globalThis.MutationObserver !== 'function') return null;
  try {
    const observer = new globalThis.MutationObserver(callback);
    observer.observe(element, { attributes: true, attributeFilter });
    return observer;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to observe element attributes.', error);
    return null;
  }
}

function observeBodyMutations(callback) {
  const body = getDocumentBody();
  if (typeof globalThis.MutationObserver !== 'function' || !body) {
    return null;
  }

  try {
    const observer = new globalThis.MutationObserver(callback);
    observer.observe(body, { childList: true, subtree: true });
    return observer;
  } catch (error) {
    Logger.system.warn('[HTMLeX] Failed to observe body mutations.', error);
    return null;
  }
}

function getElementIdentity(element) {
  try {
    const elementId = safeString(getObjectField(element, 'id', '')).trim();
    if (elementId) return `#${elementId}`;
    const name = getElementAttribute(element, 'name');
    if (name) return `[name="${name}"]`;
    return safeString(getObjectField(element, 'outerHTML', '<button>')).slice(0, 60) + '...';
  } catch {
    return '<button>...';
  }
}

function getQueuedCount(element, queueKey, cursorKey) {
  const queue = getObjectField(element, queueKey, null);
  const cursor = getNonNegativeIntegerField(element, cursorKey);
  return Math.max(getArrayLength(queue) - cursor, 0);
}

function dequeueQueuedItem(element, queueKey, cursorKey) {
  const queue = getObjectField(element, queueKey, null);
  const cursor = getNonNegativeIntegerField(element, cursorKey);
  if (!queue || cursor >= getArrayLength(queue)) return null;

  setObjectField(element, cursorKey, cursor + 1);
  return getArrayItem(queue, cursor, queueKey);
}

function resetElementQueue(element, queueKey, cursorKey) {
  clearArray(getObjectField(element, queueKey, null), queueKey);
  setObjectField(element, cursorKey, 0);
}

function notifyDOMUpdated(content, root) {
  if (hasHTMLeXMarkup(content)) {
    dispatchHTMLeXDOMUpdated(root);
  }
}

/**
 * patchedUpdateTarget
 *
 * When the target selector is empty or defaults to "this", this function ensures
 * that if multiple fragments are returned, the first fragment replaces the content
 * and subsequent fragments are appended.
 *
 * Additionally, if the element is in sequential mode (i.e. has the sequential attribute),
 * DOM updates are queued so they can later be inserted in FIFO order with a delay between each.
 *
 * @param {Object} target - The target object (with a .selector property).
 * @param {string} content - The HTML fragment content.
 * @param {Element} resolvedElement - The element to update.
 * @returns {Element|undefined}
 */
export function patchedUpdateTarget(target, content, resolvedElement, options = {}) {
  const contentString = safeString(content);
  Logger.system.debug("[HTMLeX] patchedUpdateTarget called with target:", target, "content length:", contentString.length);
  const forceResolvedElement = getObjectField(options, 'forceResolvedElement', false);
  const queueSequential = getObjectField(options, 'queueSequential', true);
  const selector = getTargetSelector(target).toLowerCase();

  if (queueSequential && resolvedElement._htmlexSequentialMode) {
    if (!resolvedElement._htmlexSequentialUpdates) {
      resolvedElement._htmlexSequentialUpdates = [];
      resolvedElement._htmlexSequentialUpdatesCursor = 0;
      Logger.system.debug("[HTMLeX] patchedUpdateTarget: Initialized sequential update queue.");
    }
    Logger.system.debug("[HTMLeX] patchedUpdateTarget: Queuing sequential update for target", getTargetSelector(target));
    appendArrayItem(resolvedElement._htmlexSequentialUpdates, { target, content: contentString }, 'sequential update');
    return;
  }
  if (selector === '' || selector === 'this') {
    if (!resolvedElement._htmlexDefaultUpdated) {
      resolvedElement._htmlexDefaultUpdated = true;
      Logger.system.debug("[HTMLeX] patchedUpdateTarget: First fragment - replacing content for target", getTargetSelector(target));
      setElementInnerHTML(resolvedElement, contentString);
      notifyDOMUpdated(contentString, resolvedElement);
      return resolvedElement;
    }

    Logger.system.debug("[HTMLeX] patchedUpdateTarget: Subsequent fragment - appending content for target", getTargetSelector(target));
    insertElementHTML(resolvedElement, 'beforeend', contentString);
    notifyDOMUpdated(contentString, resolvedElement);
    return resolvedElement;
  }
  Logger.system.debug("[HTMLeX] patchedUpdateTarget: Delegating update to originalUpdateTarget for target", getTargetSelector(target));
  return originalUpdateTarget(target, contentString, resolvedElement, { forceResolvedElement });
}

/**
 * processSequentialQueue
 *
 * Processes the FIFO queue of API calls for a sequential element.
 * Each API call is started only after the previous queued call has completed,
 * then its queued DOM updates are flushed before the configured delay is applied.
 *
 * @param {Element} element - The element with the sequential queue.
 */
async function processSequentialQueue(element) {
  element._htmlexSequentialProcessing = true;
  const delay = element._htmlexSequentialDelay || 0;
  Logger.system.debug("[HTMLeX] processSequentialQueue: Starting sequential processing with delay =", delay, "ms");

  const applyQueuedUpdate = (update) => {
    if (typeof update === 'function') {
      update();
      return;
    }

    let resolvedElement = update.resolvedElement;
    if (!resolvedElement) {
      if (getTargetSelector(update.target).toLowerCase() === "this") {
        resolvedElement = element;
      } else {
        resolvedElement = querySelectorSafe(getTargetSelector(update.target));
      }
    }
    if (!resolvedElement) {
      Logger.system.debug("[HTMLeX] processSequentialQueue: Target element not found for selector", getTargetSelector(update.target));
      return;
    }
    Logger.system.debug("[HTMLeX] processSequentialQueue: Applying update for target", getTargetSelector(update.target));
    originalUpdateTarget(update.target, update.content, resolvedElement);
    if (update.afterUpdate) update.afterUpdate();
  };

  try {
    while (
      getQueuedCount(element, '_htmlexSequentialQueue', '_htmlexSequentialQueueCursor') > 0 ||
      getQueuedCount(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor') > 0
    ) {
      Logger.system.debug(
        "[HTMLeX] processSequentialQueue: API queue length =",
        getQueuedCount(element, '_htmlexSequentialQueue', '_htmlexSequentialQueueCursor'),
        "Update queue length =",
        getQueuedCount(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor')
      );

      // If there is an API call queued, process it.
      if (getQueuedCount(element, '_htmlexSequentialQueue', '_htmlexSequentialQueueCursor') > 0) {
        const sequentialEntry = dequeueQueuedItem(element, '_htmlexSequentialQueue', '_htmlexSequentialQueueCursor');
        if (!sequentialEntry) continue;

        const { method, endpoint, updates = [], registrationToken, abortController, htmlexEvent } = sequentialEntry;
        if (
          abortController?.signal.aborted ||
          !isElementConnected(element) ||
          (registrationToken && element._htmlexRegistrationToken !== registrationToken)
        ) {
          Logger.system.debug("[HTMLeX] processSequentialQueue: Skipping stale sequential call for endpoint:", endpoint);
          clearArray(updates, 'stale sequential updates');
          continue;
        }
        Logger.system.debug("[HTMLeX] processSequentialQueue: Starting API call for endpoint:", endpoint);
        try {
          element._htmlexSequentialAbortControllers ||= new Set();
          addSetItem(element._htmlexSequentialAbortControllers, abortController, 'sequential abort controller');
          await handleAction(
            element,
            method,
            endpoint,
            {
              ...(abortController?.signal ? { signal: abortController.signal } : {}),
              htmlexSequentialEntry: sequentialEntry,
              htmlexEvent
            }
          );
          Logger.system.debug("[HTMLeX] processSequentialQueue: API call resolved for endpoint:", endpoint);
        } finally {
          deleteSetItem(element._htmlexSequentialAbortControllers, abortController, 'sequential abort controller');
        }
        if (
          abortController?.signal.aborted ||
          !isElementConnected(element) ||
          (registrationToken && element._htmlexRegistrationToken !== registrationToken)
        ) {
          Logger.system.debug("[HTMLeX] processSequentialQueue: Skipping stale sequential updates for endpoint:", endpoint);
          clearArray(updates, 'stale sequential updates');
          continue;
        }
        for (const update of updates) {
          applyQueuedUpdate(update);
        }
        clearArray(updates, 'applied sequential updates');
      }

      // If there is at least one queued update, flush one update.
      if (getQueuedCount(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor') > 0) {
        const update = dequeueQueuedItem(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor');
        if (update) {
          applyQueuedUpdate(update);
        }
      }
      Logger.system.debug("[HTMLeX] processSequentialQueue: Waiting for sequential delay of", delay, "ms before next flush.");
      await new Promise(resolve => {
        const timerId = scheduleTimeout(resolve, delay, 'sequential delay');
        if (timerId === null && delay > 0) resolve();
      });
    }
  } catch (error) {
    Logger.system.error("[HTMLeX] Sequential queue processing failed; resetting queue state.", error);
  } finally {
    resetElementQueue(element, '_htmlexSequentialQueue', '_htmlexSequentialQueueCursor');
    resetElementQueue(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor');
    element._htmlexSequentialProcessing = false;
    element._htmlexSequentialMode = false;
    Logger.system.debug("[HTMLeX] processSequentialQueue: Sequential processing complete.");
  }
}

/**
 * flushSequentialUpdates
 *
 * Immediately processes and applies all queued sequential updates for the given element.
 *
 * @param {Element} element - The element with queued sequential updates.
 */
export function flushSequentialUpdates(element) {
  if (getQueuedCount(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor') > 0) {
    Logger.system.debug(
      "[HTMLeX] flushSequentialUpdates: Flushing",
      getQueuedCount(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor'),
      "queued update(s) for element",
      element
    );

    while (getQueuedCount(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor') > 0) {
      const update = dequeueQueuedItem(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor');
      if (!update) continue;

      let resolvedElement = update.resolvedElement;
      if (!resolvedElement && getTargetSelector(update.target).toLowerCase() === "this") {
        resolvedElement = element;
      } else if (!resolvedElement) {
        resolvedElement = querySelectorSafe(getTargetSelector(update.target));
      }
      if (!resolvedElement) {
        Logger.system.debug("[HTMLeX] flushSequentialUpdates: Target element not found for selector", getTargetSelector(update.target));
      } else {
        Logger.system.debug("[HTMLeX] flushSequentialUpdates: Applying queued update for target", getTargetSelector(update.target));
        const result = originalUpdateTarget(update.target, update.content, resolvedElement);
        if (update.afterUpdate) update.afterUpdate();
        if (result && result.hasAttribute && result.hasAttribute('timer')) {
          Logger.system.debug("[HTMLeX] flushSequentialUpdates: Inserted element has timer attribute; ensure timer handling is applied.");
        }
      }
    }
    resetElementQueue(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor');
    Logger.system.debug("[HTMLeX] flushSequentialUpdates: All queued updates flushed.");
  } else {
    Logger.system.debug("[HTMLeX] flushSequentialUpdates: No queued updates to flush for element", element);
  }
}

/** @type {WeakSet<Element>} */
const registeredElements = new WeakSet();
const registrationRecords = new WeakMap();

function getRegistrationSignature(element) {
  return REGISTRATION_ATTRIBUTES
    .map(attributeName => `${attributeName}=${safeString(getElementAttribute(element, attributeName))}`)
    .join('|');
}

function cleanupElementRegistration(element, options = {}) {
  const record = registrationRecords.get(element);
  if (!record) return;

  for (const cleanup of record.cleanupFns) {
    try {
      cleanup(options);
    } catch (error) {
      Logger.system.error("[HTMLeX] Error cleaning up element registration:", error);
    }
  }
  registrationRecords.delete(element);
  registeredElements.delete(element);
  if (element._htmlexRegistrationToken === record.token) {
    deleteObjectField(element, '_htmlexRegistrationToken');
  }
  removeElementAttribute(element, 'data-htmlex-registered');
}

export function unregisterElement(element) {
  cleanupElementRegistration(element);
}

/**
 * Normalizes the trigger event name.
 * If the provided event name starts with "on", it strips the prefix.
 * @param {string} eventName - The raw event name from the attribute.
 * @returns {string} The normalized event name.
 */
function normalizeEvent(eventName, fallbackEvent = '') {
  if (!eventName) return fallbackEvent;
  eventName = safeString(eventName).trim();
  if (eventName.toLowerCase() === 'on') {
    Logger.system.warn(`[HTMLeX Warning] Ignoring invalid trigger "${eventName}". Falling back to "${fallbackEvent}".`);
    return fallbackEvent;
  }
  const shouldStripOnPrefix = (
    eventName.length > 2 &&
    eventName.toLowerCase().startsWith('on') &&
    (
      /^on[A-Z]/.test(eventName) ||
      COMMON_ON_ATTRIBUTE_EVENTS.has(eventName.slice(2).toLowerCase())
    )
  );
  const normalized = shouldStripOnPrefix
    ? eventName.slice(2)
    : eventName;
  const normalizedLowercase = normalized.toLowerCase();
  if (!normalizedLowercase) {
    Logger.system.warn(`[HTMLeX Warning] Ignoring invalid trigger "${eventName}". Falling back to "${fallbackEvent}".`);
    return fallbackEvent;
  }
  Logger.system.debug(`[HTMLeX] normalizeEvent: Raw="${eventName}" Normalized="${normalizedLowercase}"`);
  return normalizedLowercase;
}

function snapshotEvent(event) {
  if (!event) return null;
  return {
    type: getEventField(event, 'type'),
    target: getEventField(event, 'target'),
    currentTarget: getEventField(event, 'currentTarget'),
    defaultPrevented: getEventField(event, 'defaultPrevented'),
    preventDefault: () => callEventMethod(event, 'preventDefault'),
    stopPropagation: () => callEventMethod(event, 'stopPropagation'),
    stopImmediatePropagation: () => callEventMethod(event, 'stopImmediatePropagation')
  };
}

function getActionMethod(element) {
  return METHOD_ATTRIBUTES.find(methodAttribute => hasElementAttribute(element, methodAttribute));
}

function isSequentialEnabled(element) {
  return hasElementAttribute(element, 'sequential') &&
    safeString(getElementAttribute(element, 'sequential')).trim().toLowerCase() !== 'false';
}

function getDefaultTriggerEvent(element) {
  return getElementTagName(element) === 'form' ? 'submit' : 'click';
}

function parseNonNegativeInteger(value, defaultValue = 0) {
  const normalizedValue = safeString(value).trim();
  if (!/^\d+$/u.test(normalizedValue)) return defaultValue;

  const parsed = Number.parseInt(normalizedValue, 10);
  return Number.isSafeInteger(parsed) ? parsed : defaultValue;
}

function parseAutoDelay(value) {
  const normalizedValue = safeString(value).trim();
  if (normalizedValue.toLowerCase() === 'true') return 0;
  if (!normalizedValue || /^-\d+$/u.test(normalizedValue)) return 0;
  return parseNonNegativeInteger(normalizedValue, null);
}

function resolveTimerTargetElements(target, fallbackElement) {
  if (getTargetSelector(target).toLowerCase() === 'this') {
    return [fallbackElement];
  }

  return querySelectorAllSafe(getTargetSelector(target));
}

function cleanupSocket(element) {
  if (element._htmlexSocket) {
    try {
      element._htmlexSocket.disconnect?.();
    } catch (error) {
      Logger.system.warn('[HTMLeX] Failed to disconnect socket during cleanup.', error);
    }
    delete element._htmlexSocket;
  }
  if (element._htmlexSocketObserver) {
    disconnectObserver(element._htmlexSocketObserver, 'socket observer');
    delete element._htmlexSocketObserver;
  }
}

function cleanupRemovedTree(node) {
  if (!isElementNode(node) || isElementConnected(node)) return;

  cleanupElementRegistration(node, { preserveRequestState: true });
  try {
    for (const descendant of node.querySelectorAll?.('*') || []) {
      cleanupElementRegistration(descendant, { preserveRequestState: true });
    }
  } catch (error) {
    Logger.system.warn("[HTMLeX] Failed to clean up a removed subtree.", error);
  }
}

function runTimerTargetAction(element, targetAttribute) {
  if (!targetAttribute) {
    Logger.system.info("[TIMER] Timer triggered: No target attribute specified; removing the element.");
    removeElementNode(element);
    return;
  }

  for (const target of parseTargets(targetAttribute)) {
    const resolvedElements = resolveTimerTargetElements(target, element);
    if (!resolvedElements.length) {
      Logger.system.warn(`[TIMER] Timer triggered: No element found for selector "${getTargetSelector(target)}".`);
      continue;
    }

    for (const resolvedElement of resolvedElements) {
      if (getObjectField(target, 'strategy') === 'remove') {
        Logger.system.info(`[TIMER] Timer triggered: Removing element matching target "${getTargetSelector(target)}".`);
        removeElementNode(resolvedElement);
        continue;
      }

      Logger.system.info(`[TIMER] Timer triggered: Clearing content of element matching target "${getTargetSelector(target)}".`);
      setElementInnerHTML(resolvedElement, "");
    }
  }
}

function runTimerAction(element) {
  const methodAttribute = getActionMethod(element);
  if (methodAttribute) {
    Logger.system.info(`[TIMER] Timer triggered: Calling API with method ${methodAttribute.toUpperCase()}.`);
    handleAction(element, methodAttribute.toUpperCase(), getElementAttribute(element, methodAttribute), { htmlexEvent: createHTMLeXEvent('timer') })
      .catch(error => {
        Logger.system.error("[TIMER] Timer-triggered API action failed:", error);
      });
    return;
  }

  if (hasElementAttribute(element, 'publish')) {
    const publishSignal = getElementAttribute(element, 'publish');
    Logger.system.info(`[TIMER] Timer triggered: Emitting publish signal "${safeString(publishSignal)}".`);
    emitSignal(publishSignal);
    return;
  }

  runTimerTargetAction(element, getElementAttribute(element, 'target'));
}

function registerTimer(element, registrationToken, cleanupFns) {
  if (!hasElementAttribute(element, 'timer')) return;

  if (hasElementAttribute(element, 'data-timer-set')) {
    Logger.system.debug("[HTMLeX] Timer already set for element:", element);
    return;
  }

  const timerDelayMs = parseNonNegativeInteger(getElementAttribute(element, 'timer'), null);
  if (timerDelayMs === null) {
    Logger.system.warn(`[HTMLeX Warning] Ignoring invalid timer delay "${safeString(getElementAttribute(element, 'timer'))}".`);
    return;
  }

  Logger.system.info(`[HTMLeX INFO] Timer set for element with delay ${timerDelayMs}ms.`);
  const timerAttributeValue = getElementAttribute(element, 'timer');
  setElementAttribute(element, 'data-timer-set', 'true');
  let timerActive = true;
  let timerAttributeObserver = null;

  const timerId = scheduleTimeout(() => {
    timerActive = false;
    disconnectObserver(timerAttributeObserver, 'timer attribute observer');
    timerAttributeObserver = null;
    if (element._htmlexRegistrationToken !== registrationToken || !isElementConnected(element)) return;
    if (getElementAttribute(element, 'timer') !== timerAttributeValue) {
      Logger.system.debug("[TIMER] Skipping stale timer because its timer attribute changed or was removed:", element);
      return;
    }
    Logger.system.debug("[TIMER] Timer callback triggered for element:", element);
    runTimerAction(element);
  }, timerDelayMs, 'timer action');
  if (timerId === null && timerDelayMs > 0) {
    removeElementAttribute(element, 'data-timer-set');
    return;
  }

  const clearRegisteredTimer = () => {
    if (timerActive) {
      timerActive = false;
      clearScheduledTimeout(timerId, 'registered timer');
    }
    disconnectObserver(timerAttributeObserver, 'timer attribute observer');
    timerAttributeObserver = null;
    removeElementAttribute(element, 'data-timer-set');
  };

  timerAttributeObserver = observeElementAttributes(element, () => {
    if (getElementAttribute(element, 'timer') === timerAttributeValue) return;
    Logger.system.debug("[TIMER] Clearing timer because its timer attribute changed or was removed:", element);
    clearRegisteredTimer();
  }, ['timer']);

  appendArrayItem(cleanupFns, () => {
    clearRegisteredTimer();
  }, 'timer cleanup');
}

/**
 * Registers an individual element for HTMLeX behavior.
 * @param {Element} element - The element to register.
 */
export function registerElement(element) {
  // Warn if a <button> inside a form does not explicitly specify the type.
  if (
    getElementTagName(element) === 'button' &&
    !hasElementAttribute(element, 'type') &&
    safeClosest(element, 'form')
  ) {
    const identity = getElementIdentity(element);
    Logger.system.warn(
      `[HTMLeX Warning] A <button> element (${identity}) inside a form does not specify a type attribute. ` +
      `It defaults to 'submit', which may trigger the form's API/signal in addition to its own. ` +
      `Consider explicitly setting type="button" (or type="submit" if intended).`
    );
  }

  const registrationSignature = getRegistrationSignature(element);
  const existingRecord = registrationRecords.get(element);
  if (registeredElements.has(element) && existingRecord?.signature === registrationSignature) {
    Logger.system.debug("[HTMLeX] Element already registered:", element);
    return;
  }
  if (registeredElements.has(element)) {
    Logger.system.debug("[HTMLeX] Element registration-affecting attributes changed. Re-registering:", element);
    cleanupElementRegistration(element);
  }

  Logger.system.debug("[HTMLeX] Registering element:", element);
  registeredElements.add(element);
  setElementAttribute(element, 'data-htmlex-registered', 'true');
  const cleanupFns = [];
  const registrationToken = Symbol('htmlexRegistration');
  element._htmlexRegistrationToken = registrationToken;
  appendArrayItem(cleanupFns, ({ preserveRequestState = false } = {}) => {
    if (!preserveRequestState) {
      element._htmlexRequestId = (element._htmlexRequestId || 0) + 1;
      element._htmlexRequestPending = false;
      if (element._htmlexPendingCall) {
        clearScheduledTimeout(element._htmlexPendingCall, 'pending action call');
        element._htmlexPendingCall = null;
      }
      if (element._htmlexAbortController) {
        safeAbort(element._htmlexAbortController);
        element._htmlexAbortController = null;
      }
      if (element._htmlexSequentialAbortControllers) {
        forEachIterableItem(element._htmlexSequentialAbortControllers, 'sequential abort controllers', safeAbort);
        clearSetLike(element._htmlexSequentialAbortControllers, 'sequential abort controllers');
      }
      if (element._htmlexDelayedSignalTimers) {
        forEachIterableItem(element._htmlexDelayedSignalTimers, 'delayed signal timers', (timerId) => {
          clearScheduledTimeout(timerId, 'delayed signal timer');
        });
        clearSetLike(element._htmlexDelayedSignalTimers, 'delayed signal timers');
      }
      if (element._htmlexSequentialQueue) {
        resetElementQueue(element, '_htmlexSequentialQueue', '_htmlexSequentialQueueCursor');
      }
      if (element._htmlexSequentialUpdates) {
        resetElementQueue(element, '_htmlexSequentialUpdates', '_htmlexSequentialUpdatesCursor');
      }
      element._htmlexSequentialMode = false;
      element._htmlexSequentialProcessing = false;
    }
  }, 'registration cleanup');

  const methodAttribute = getActionMethod(element);
  const hasActionHandler = Boolean(methodAttribute || hasElementAttribute(element, 'publish'));

  const rawTrigger = getElementAttribute(element, 'trigger');
  const defaultTriggerEvent = getDefaultTriggerEvent(element);
  const triggerEvent = rawTrigger
    ? normalizeEvent(rawTrigger, defaultTriggerEvent)
    : defaultTriggerEvent;
  Logger.system.debug(`[HTMLeX] triggerEvent for element: ${triggerEvent}`);
  const actionSelector = '[get], [post], [put], [delete], [patch], [publish]';

  const wrappedHandler = async (event) => {
    const eventType = getEventField(event, 'type');
    const eventTarget = getEventField(event, 'target');
    Logger.system.debug(`[HTMLeX] Event triggered: type="${eventType}", currentTarget=`, getEventField(event, 'currentTarget'), "target=", eventTarget);

    if (element._htmlexRegistrationToken !== registrationToken) {
      Logger.system.debug("[HTMLeX] Ignoring event for stale element registration:", element);
      return;
    }

    if (!isElementConnected(element)) {
      Logger.system.debug("[HTMLeX] Ignoring event for removed element:", element);
      return;
    }

    const nestedAction = isElementLike(eventTarget)
      ? safeClosest(eventTarget, actionSelector)
      : null;
    if (
      (triggerEvent === 'click' || triggerEvent === 'submit') &&
      getElementTagName(element) !== 'form' &&
      nestedAction &&
      nestedAction !== element
    ) {
      Logger.system.debug("[HTMLeX] Ignoring event from nested HTMLeX action. triggerEvent:", triggerEvent);
      return;
    }

    Logger.system.debug(`[HTMLeX] Event accepted: triggerEvent: ${triggerEvent} on element:`, element);
    const htmlexEvent = snapshotEvent(event);

    if (methodAttribute) {
      if (triggerEvent === 'submit') callEventMethod(event, 'preventDefault');
      if (isSequentialEnabled(element)) {
        const sequentialDelay = parseNonNegativeInteger(getElementAttribute(element, 'sequential'), 0);
        element._htmlexSequentialMode = true;
        element._htmlexSequentialDelay = sequentialDelay;
        if (!element._htmlexSequentialQueue) {
          element._htmlexSequentialQueue = [];
          element._htmlexSequentialQueueCursor = 0;
          element._htmlexSequentialProcessing = false;
          Logger.system.debug("[HTMLeX] Initialized sequential queue with delay:", sequentialDelay, "ms");
        }
        const abortController = createAbortController();
        const sequentialEntry = {
          method: methodAttribute.toUpperCase(),
          endpoint: getElementAttribute(element, methodAttribute),
          updates: [],
          registrationToken,
          abortController,
          htmlexEvent
        };
        appendArrayItem(element._htmlexSequentialQueue, sequentialEntry, 'sequential API call');
        Logger.system.debug("[HTMLeX] Enqueued sequential API call. Queue length now:", getArrayLength(element._htmlexSequentialQueue));
        if (!element._htmlexSequentialProcessing) {
          processSequentialQueue(element);
        }
      } else {
        // Non-sequential: cancel pending or in-flight API calls.
        if (element._htmlexPendingCall) {
          clearScheduledTimeout(element._htmlexPendingCall, 'pending non-sequential action');
          Logger.system.debug("[HTMLeX] Cancelled previous pending non-sequential API call (timeout) for endpoint:", getElementAttribute(element, methodAttribute));
        }
        if (element._htmlexAbortController) {
          safeAbort(element._htmlexAbortController);
          Logger.system.debug("[HTMLeX] Aborted previous in-flight non-sequential API call for endpoint:", getElementAttribute(element, methodAttribute));
        }
        element._htmlexAbortController = createAbortController();
        Logger.system.debug("[HTMLeX] Created new AbortController for non-sequential API call for endpoint:", getElementAttribute(element, methodAttribute));
        element._htmlexPendingCall = scheduleTimeout(() => {
          Logger.system.debug("[HTMLeX] Executing non-sequential API call for endpoint:", getElementAttribute(element, methodAttribute));
          (async () => {
            try {
              const actionOptions = { htmlexEvent };
              if (element._htmlexAbortController?.signal) {
                actionOptions.signal = element._htmlexAbortController.signal;
              }
              await handleAction(element, methodAttribute.toUpperCase(), getElementAttribute(element, methodAttribute), actionOptions);
              Logger.system.debug("[HTMLeX] Non-sequential API call completed for endpoint:", getElementAttribute(element, methodAttribute));
            } catch (error) {
              Logger.system.debug("[HTMLeX] Non-sequential API call aborted or errored for endpoint:", getElementAttribute(element, methodAttribute), error);
            }
          })();
          element._htmlexPendingCall = null;
        }, 0, 'non-sequential action');
      }
    } else if (hasElementAttribute(element, 'publish')) {
      const publishSignal = getElementAttribute(element, 'publish');
      Logger.system.info(`[HTMLeX] Emitting publish signal "${safeString(publishSignal)}" on event "${triggerEvent}".`);
      emitSignal(publishSignal);
    }
  };

  let handler = wrappedHandler;
  if (hasActionHandler) {
    // Apply debounce/throttle if defined.
    const rateLimitCleanups = [];
    const debounceMs = parseNonNegativeInteger(getElementAttribute(element, 'debounce'), 0);
    if (debounceMs > 0) {
      const debouncedHandler = debounce(handler, debounceMs);
      appendArrayItem(rateLimitCleanups, () => debouncedHandler.cancel?.(), 'debounce cleanup');
      handler = debouncedHandler;
      Logger.system.debug(`[HTMLeX] Applied debounce of ${debounceMs}ms`);
    }
    const throttleMs = parseNonNegativeInteger(getElementAttribute(element, 'throttle'), 0);
    if (throttleMs > 0) {
      const throttledHandler = throttle(handler, throttleMs);
      appendArrayItem(rateLimitCleanups, () => throttledHandler.cancel?.(), 'throttle cleanup');
      handler = throttledHandler;
      Logger.system.debug(`[HTMLeX] Applied throttle of ${throttleMs}ms`);
    }
    if (rateLimitCleanups.length) {
      appendArrayItem(cleanupFns, () => {
        for (const cleanup of rateLimitCleanups) {
          cleanup();
        }
      }, 'rate limit cleanup');
    }

    const eventListener = (event) => {
      if (triggerEvent === 'submit' && typeof getEventField(event, 'preventDefault') === 'function') {
        callEventMethod(event, 'preventDefault');
      }
      return handler(event);
    };
    if (addElementEventListener(element, triggerEvent, eventListener)) {
      appendArrayItem(cleanupFns, () => removeElementEventListener(element, triggerEvent, eventListener), 'event listener cleanup');
    }
    Logger.system.info(`[HTMLeX INFO] Registered ${methodAttribute ? methodAttribute.toUpperCase() : 'publish'} action on element with event "${triggerEvent}" for endpoint "${methodAttribute ? safeString(getElementAttribute(element, methodAttribute)) : ''}".`);
  }

  // Revised polling code to respect the "repeat" attribute.
  if (hasActionHandler && hasElementAttribute(element, 'poll')) {
    const rawPollInterval = parseNonNegativeInteger(getElementAttribute(element, 'poll'), 0);
    const pollInterval = Number.isFinite(rawPollInterval) && rawPollInterval > 0
      ? Math.max(rawPollInterval, 100)
      : 0;
    if (pollInterval > 0) {
      const repeatLimit = parseNonNegativeInteger(getElementAttribute(element, 'repeat'), 0);
      let pollIterationCount = 0;
      let pollRemovalObserver = null;
      const clearPolling = () => {
        clearScheduledInterval(intervalId, 'poll interval');
        element._htmlexPollIntervalId = null;
        if (pollRemovalObserver) {
          disconnectObserver(pollRemovalObserver, 'poll removal observer');
          pollRemovalObserver = null;
        }
      };
      const intervalId = scheduleInterval(() => {
        if (!isElementConnected(element)) {
          Logger.system.info("[HTMLeX INFO] Polling element removed. Clearing interval.");
          clearPolling();
          return;
        }
        if (repeatLimit > 0 && pollIterationCount >= repeatLimit) {
          Logger.system.info(`[HTMLeX INFO] Polling reached maximum repeat limit (${repeatLimit}) for element. Clearing interval.`);
          clearPolling();
          return;
        }
        Logger.system.debug("[HTMLeX] Polling triggered for element:", element);
        handler(createHTMLeXEvent(triggerEvent));
        pollIterationCount += 1;
      }, pollInterval, 'poll interval');
      if (intervalId !== null) {
        element._htmlexPollIntervalId = intervalId;
        pollRemovalObserver = observeBodyMutations(() => {
          if (!isElementConnected(element)) {
            Logger.system.info("[HTMLeX INFO] Polling element removed. Clearing interval.");
            clearPolling();
          }
        });
        if (pollRemovalObserver) {
          element._htmlexPollRemovalObserver = pollRemovalObserver;
        }
        appendArrayItem(cleanupFns, clearPolling, 'poll cleanup');
        Logger.system.info(`[HTMLeX INFO] Set up polling every ${pollInterval}ms for element with repeat limit: ${repeatLimit || "unlimited"}.`);
      }
    }
  }

  // Auto-firing based on the "auto" attribute.
  if (hasActionHandler && hasElementAttribute(element, 'auto')) {
    const autoValue = safeString(getElementAttribute(element, 'auto')).trim();
    const autoMode = autoValue.toLowerCase();
    if (autoMode === 'false') {
      Logger.system.info("[HTMLeX INFO] Auto firing disabled for element.");
    } else if (autoMode === 'lazy') {
      if (typeof globalThis.IntersectionObserver !== 'function') {
        Logger.system.warn("[HTMLeX Warning] IntersectionObserver unavailable. Firing lazy auto action immediately.");
        handler(createHTMLeXEvent(triggerEvent));
      } else {
        let lazyRemovalObserver = null;
        const cleanupLazyObserver = (observer) => {
          disconnectObserver(observer, 'lazy observer');
          if (lazyRemovalObserver) {
            disconnectObserver(lazyRemovalObserver, 'lazy removal observer');
            lazyRemovalObserver = null;
          }
          element._htmlexLazyObserver = null;
          element._htmlexLazyRemovalObserver = null;
        };
        try {
          const observer = new globalThis.IntersectionObserver((entries, observer) => {
            for (const entry of entries) {
              if (!isElementConnected(element)) {
                cleanupLazyObserver(observer);
                return;
              }
              if (entry.isIntersecting) {
                Logger.system.debug("[HTMLeX] Lazy auto firing action for element:", element);
                handler(createHTMLeXEvent(triggerEvent));
                cleanupLazyObserver(observer);
              }
            }
          });
          observer.observe(element);
          lazyRemovalObserver = observeBodyMutations(() => {
            if (!isElementConnected(element)) {
              Logger.system.info("[HTMLeX INFO] Lazy auto element removed. Disconnecting observer.");
              cleanupLazyObserver(observer);
            }
          });
          element._htmlexLazyObserver = observer;
          element._htmlexLazyRemovalObserver = lazyRemovalObserver;
          appendArrayItem(cleanupFns, () => cleanupLazyObserver(observer), 'lazy observer cleanup');
          Logger.system.info("[HTMLeX INFO] Set up lazy auto firing for element.");
        } catch (error) {
          Logger.system.warn("[HTMLeX Warning] Failed to set up IntersectionObserver. Firing lazy auto action immediately.", error);
          try {
            handler(createHTMLeXEvent(triggerEvent));
          } catch (handlerError) {
            Logger.system.error("[HTMLeX ERROR] Lazy auto fallback failed for element:", element, handlerError);
          }
        }
      }
    } else if (autoMode === 'prefetch') {
      (async () => {
        try {
          await handler(createHTMLeXEvent(triggerEvent));
          Logger.system.info("[HTMLeX INFO] Prefetch completed for element:", element);
        } catch (error) {
          Logger.system.error("[HTMLeX ERROR] Prefetch failed for element:", element, error);
        }
      })();
    } else {
      const delay = parseAutoDelay(autoValue);
      if (delay === null) {
        Logger.system.warn(`[HTMLeX Warning] Ignoring invalid auto delay "${autoValue}".`);
      } else {
        const autoTimerId = scheduleTimeout(() => {
          if (!isElementConnected(element)) return;
          Logger.system.debug("[HTMLeX] Auto firing action for element after delay:", delay, "ms", element);
          handler(createHTMLeXEvent(triggerEvent));
        }, delay, 'auto action');
        if (autoTimerId !== null) {
          appendArrayItem(cleanupFns, () => clearScheduledTimeout(autoTimerId, 'auto action timer'), 'auto action cleanup');
          Logger.system.info(`[HTMLeX INFO] Auto firing set for element with delay ${delay}ms.`);
        }
      }
    }
  }

  // Publish-only element registration.
  if (!methodAttribute && hasElementAttribute(element, 'publish')) {
    Logger.system.info(`[HTMLeX INFO] Registered publish-only element for signal "${safeString(getElementAttribute(element, 'publish'))}" with event "${triggerEvent}".`);
  }

  // Handle subscriptions using the "subscribe" attribute.
  if (hasElementAttribute(element, 'subscribe')) {
    const signals = safeString(getElementAttribute(element, 'subscribe')).split(/\s+/).filter(Boolean);
    const unsubscribers = [];
    let subscribeRemovalObserver = null;
    const cleanupSubscriptions = () => {
      while (getArrayLength(unsubscribers)) {
        const unsubscribe = popArrayItem(unsubscribers, 'subscription cleanup');
        try {
          unsubscribe?.();
        } catch (error) {
          Logger.system.warn('[HTMLeX] Failed to unsubscribe signal listener.', error);
        }
      }
      if (subscribeRemovalObserver) {
        disconnectObserver(subscribeRemovalObserver, 'subscription removal observer');
        subscribeRemovalObserver = null;
      }
      element._htmlexSubscribeRemovalObserver = null;
    };

    for (const signalName of signals) {
      const unsubscribe = registerSignalListener(signalName, () => {
        if (element._htmlexRegistrationToken !== registrationToken) {
          cleanupSubscriptions();
          return;
        }
        if (!isElementConnected(element)) {
          cleanupSubscriptions();
          return;
        }
        Logger.system.debug(`[HTMLeX] Signal "${signalName}" triggered listener on element:`, element);
        const subscribedMethod = getActionMethod(element);
        if (subscribedMethod) {
          const endpoint = getElementAttribute(element, subscribedMethod);
          Logger.system.debug(`[HTMLeX] Handling subscribed signal with method ${subscribedMethod.toUpperCase()} for endpoint "${safeString(endpoint)}".`);
          return handleAction(element, subscribedMethod.toUpperCase(), endpoint, { htmlexEvent: createHTMLeXEvent('signal') })
            .catch(error => {
              Logger.system.error(`[HTMLeX] Subscribed signal action failed for "${signalName}".`, error);
            });
        }
        return undefined;
      });
      appendArrayItem(unsubscribers, unsubscribe, 'subscription cleanup');
      Logger.system.debug(`[HTMLeX] Registered subscriber for signal "${signalName}" on element:`, element);
    }

    if (getArrayLength(unsubscribers)) {
      subscribeRemovalObserver = observeBodyMutations(() => {
        if (!isElementConnected(element)) {
          cleanupSubscriptions();
        }
      });
      if (subscribeRemovalObserver) {
        element._htmlexSubscribeRemovalObserver = subscribeRemovalObserver;
      }
    }
    appendArrayItem(cleanupFns, cleanupSubscriptions, 'subscription cleanup');
  }

  // Handle WebSocket connections.
  if (hasElementAttribute(element, 'socket')) {
    const socketUrl = getElementAttribute(element, 'socket');
    Logger.system.debug("[HTMLeX] Setting up WebSocket connection for element:", element, "URL:", socketUrl);
    handleWebSocket(element, socketUrl);
    appendArrayItem(cleanupFns, () => cleanupSocket(element), 'socket cleanup');
  }

  registerTimer(element, registrationToken, cleanupFns);

  registrationRecords.set(element, {
    signature: registrationSignature,
    cleanupFns,
    token: registrationToken
  });
}

/**
 * Scans the DOM for HTMLeX-enabled elements and registers them.
 * Also sets up a MutationObserver to auto-register new elements as they are inserted,
 * ensuring progressive rendering (PR) elements get initialized.
 */
export function initHTMLeX() {
  Logger.system.info("[HTMLeX INFO] Initializing HTMLeX...");
  const runtimeWindow = getRuntimeWindow();
  const runtimeDocument = getRuntimeDocument();
  const documentBody = getDocumentBody(runtimeDocument);
  if (
    !runtimeWindow ||
    !runtimeDocument ||
    !documentBody ||
    typeof globalThis.MutationObserver !== 'function'
  ) {
    Logger.system.warn("[HTMLeX Warning] Browser DOM APIs are unavailable; skipping initialization.");
    return;
  }

  const existingObserver = getWindowField(runtimeWindow, '__htmlexObserver');
  if (existingObserver) {
    disconnectObserver(existingObserver, 'previous HTMLeX observer');
  }
  const existingDomUpdatedHandler = getWindowField(runtimeWindow, '__htmlexDomUpdatedHandler');
  if (existingDomUpdatedHandler) {
    removeDocumentEventListener(runtimeDocument, 'htmlex:dom-updated', existingDomUpdatedHandler);
  }

  const registerTree = (node) => {
    if (!isElementNode(node)) return;
    try {
      if (safeMatches(node, REGISTRATION_SELECTOR_STRING)) {
        Logger.system.debug("[HTMLeX] HTMLeX element found:", node);
        registerElement(node);
      }
      const newElements = safeQuerySelectorAll(node, REGISTRATION_SELECTOR_STRING);
      for (const el of newElements) {
        Logger.system.debug("[HTMLeX] Descendant HTMLeX element found:", el);
        registerElement(el);
      }
    } catch (error) {
      Logger.system.warn("[HTMLeX] Failed to register a DOM subtree.", error);
    }
  };

  // Register existing HTMLeX elements.
  const elements = safeQuerySelectorAll(runtimeDocument, REGISTRATION_SELECTOR_STRING);
  for (const el of elements) {
    registerElement(el);
  }
  Logger.system.info(`[HTMLeX INFO] Registered ${elements.length} element(s).`);

  const domUpdatedHandler = (event) => {
    registerTree(getDOMUpdateRoot(event, documentBody));
  };
  setWindowField(runtimeWindow, '__htmlexDomUpdatedHandler', domUpdatedHandler);
  if (addDocumentEventListener(runtimeDocument, 'htmlex:dom-updated', domUpdatedHandler)) {
    Logger.system.debug("[HTMLeX] Registered DOM update listener.");
  }

  // Observe for new elements added to the DOM.
  let observer;
  try {
    observer = new globalThis.MutationObserver(mutationsList => {
    for (const mutation of mutationsList) {
      if (mutation.type === 'attributes') {
        if (
          isElementNode(mutation.target) &&
          registrationRecords.has(mutation.target) &&
          !safeMatches(mutation.target, REGISTRATION_SELECTOR_STRING)
        ) {
          Logger.system.debug("[HTMLeX] HTMLeX attributes removed. Cleaning up registration:", mutation.target);
          cleanupElementRegistration(mutation.target);
          continue;
        }
        registerTree(mutation.target);
        continue;
      }
      if (mutation.type === 'childList') {
        for (const node of mutation.removedNodes || []) {
          cleanupRemovedTree(node);
        }
        for (const node of mutation.addedNodes || []) {
          registerTree(node);
        }
      }
    }
    });

    observer.observe(documentBody, {
      childList: true,
      subtree: true,
      attributes: true,
      attributeFilter: REGISTRATION_ATTRIBUTES
    });
  } catch (error) {
    Logger.system.warn("[HTMLeX] Failed to start mutation observer.", error);
    return;
  }
  setWindowField(runtimeWindow, '__htmlexObserver', observer);
  Logger.system.info("[HTMLeX INFO] HTMLeX is now observing for new elements.");
}
