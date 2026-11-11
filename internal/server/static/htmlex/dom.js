// src/dom.js
/**
 * @module DOM
 * @description Provides functions for diffing and updating the DOM.
 *
 * @typedef {Object} TargetInstruction
 * @property {string} selector - The CSS selector.
 * @property {string} strategy - The update strategy (e.g., "innerHTML", "append").
 */

import { Logger } from './logger.js';
const TARGET_STRATEGIES = 'innerHTML|outerHTML|append|prepend|before|after|remove';
export const HTMLEX_ATTRIBUTE_NAMES = [
  'get', 'post', 'put', 'delete', 'patch',
  'auto', 'poll', 'socket', 'subscribe', 'publish',
  'trigger', 'debounce', 'throttle', 'retry', 'timeout',
  'retrydelay', 'retry-delay', 'retrybackoff', 'retry-backoff', 'retrymaxdelay', 'retry-max-delay',
  'cache', 'timer', 'sequential', 'repeat', 'source', 'target',
  'loading', 'onerror', 'extras', 'push', 'pull', 'path',
  'history', 'onbefore', 'onbeforeswap', 'onafterswap', 'onafter'
];
const HTMLEX_MARKUP_PATTERN = new RegExp(
  `\\s(?:${HTMLEX_ATTRIBUTE_NAMES.map(escapeRegExp).join('|')})(?=[\\s=>/])`,
  'i'
);
const STRATEGY_BY_LOWERCASE = {
  innerhtml: 'innerHTML',
  outerhtml: 'outerHTML',
  append: 'append',
  prepend: 'prepend',
  before: 'before',
  after: 'after',
  remove: 'remove'
};
const ELEMENT_NODE_TYPE = 1;
const TEXT_NODE_TYPE = 3;

function safeString(value, fallback = '') {
  try {
    return String(value ?? fallback);
  } catch (error) {
    Logger.system.warn('[DOM] Failed to coerce value to string.', error);
    return fallback;
  }
}

function getRuntimeDocument() {
  try {
    return typeof document === 'undefined' ? globalThis.document : document;
  } catch (error) {
    Logger.system.warn('[DOM] Failed to read document.', error);
    return null;
  }
}

function getNodeField(node, fieldName, fallback = undefined) {
  try {
    return node?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to read node field "${fieldName}".`, error);
    return fallback;
  }
}

function getTargetField(target, fieldName, fallback = undefined) {
  try {
    return target?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to read target field "${fieldName}".`, error);
    return fallback;
  }
}

function setNodeField(node, fieldName, value) {
  try {
    node[fieldName] = value;
    return true;
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to set node field "${fieldName}".`, error);
    return false;
  }
}

function getNodeList(node, fieldName) {
  try {
    return [...(node?.[fieldName] || [])];
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to enumerate node ${fieldName}.`, error);
    return [];
  }
}

function getElementAttributes(element) {
  return getNodeList(element, 'attributes');
}

function getChildNodes(node) {
  return getNodeList(node, 'childNodes');
}

function hasElementAttribute(element, attributeName) {
  try {
    return Boolean(element?.hasAttribute?.(attributeName));
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to check attribute "${attributeName}".`, error);
    return false;
  }
}

function getElementAttribute(element, attributeName) {
  try {
    return element?.getAttribute?.(attributeName) ?? null;
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to read attribute "${attributeName}".`, error);
    return null;
  }
}

function setElementAttribute(element, attributeName, value) {
  try {
    element?.setAttribute?.(attributeName, value);
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to set attribute "${attributeName}".`, error);
  }
}

function removeElementAttribute(element, attributeName) {
  try {
    element?.removeAttribute?.(attributeName);
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to remove attribute "${attributeName}".`, error);
  }
}

function cloneNodeSafely(node, deep = false) {
  try {
    return node?.cloneNode?.(deep) || null;
  } catch (error) {
    Logger.system.warn('[DOM] Failed to clone node.', error);
    return null;
  }
}

function replaceNodeSafely(existingNode, replacementNode) {
  try {
    if (typeof existingNode?.replaceWith !== 'function') return false;
    existingNode.replaceWith(replacementNode);
    return true;
  } catch (error) {
    Logger.system.warn('[DOM] Failed to replace node.', error);
    return false;
  }
}

function removeNodeSafely(node) {
  try {
    node?.remove?.();
  } catch (error) {
    Logger.system.warn('[DOM] Failed to remove node.', error);
  }
}

function getInnerHTML(element) {
  return getNodeField(element, 'innerHTML', '');
}

function setInnerHTML(element, html) {
  return setNodeField(element, 'innerHTML', html);
}

function getNodeType(name, fallback) {
  try {
    return getTargetField(getTargetField(globalThis, 'Node'), name, fallback);
  } catch {
    return fallback;
  }
}

function escapeRegExp(value) {
  return safeString(value).replace(/[.*+?^${}()|[\]\\]/g, '\\$&');
}

function hasHTMLeXBehavior(node) {
  return isElementNode(node) && HTMLEX_ATTRIBUTE_NAMES.some(attributeName => {
    return hasElementAttribute(node, attributeName);
  });
}

function getHTMLeXBehaviorSignature(element) {
  return HTMLEX_ATTRIBUTE_NAMES
    .map(attributeName => {
      const value = getElementAttribute(element, attributeName);
      return `${attributeName}=${safeString(value)}`;
    })
    .join('|');
}

function isElementNode(node) {
  return getNodeField(node, 'nodeType') === getNodeType('ELEMENT_NODE', ELEMENT_NODE_TYPE);
}

function isTextNode(node) {
  return getNodeField(node, 'nodeType') === getNodeType('TEXT_NODE', TEXT_NODE_TYPE);
}

function getNodeKey(node) {
  if (!isElementNode(node)) return '';

  for (const attributeName of ['id', 'data-key', 'key', 'data-htmlex-key']) {
    let value;
    try {
      value = getElementAttribute(node, attributeName);
    } catch {
      value = null;
    }
    if (value) return `${attributeName}:${safeString(value)}`;
  }

  return '';
}

function getActiveElement() {
  try {
    return getRuntimeDocument()?.activeElement || null;
  } catch {
    return null;
  }
}

function getControlOptions(element) {
  try {
    return [...(element.options || [])];
  } catch {
    return [];
  }
}

function captureControlState(element) {
  const tagName = safeString(getNodeField(element, 'tagName', '')).toUpperCase();
  const isActive = getActiveElement() === element;

  if (tagName === 'INPUT' || tagName === 'TEXTAREA') {
    const state = {
      tagName,
      isActive,
      value: getNodeField(element, 'value'),
      checked: getNodeField(element, 'checked'),
    };
    const selectionStart = getNodeField(element, 'selectionStart');
    const selectionEnd = getNodeField(element, 'selectionEnd');
    if (isActive && typeof selectionStart === 'number' && typeof selectionEnd === 'number') {
      state.selectionStart = selectionStart;
      state.selectionEnd = selectionEnd;
      state.selectionDirection = getNodeField(element, 'selectionDirection');
    }
    return state;
  }

  if (tagName === 'SELECT') {
    const options = getControlOptions(element);
    return {
      tagName,
      isActive,
      selectedValues: options
        .filter(option => getNodeField(option, 'selected', false))
        .map(option => getNodeField(option, 'value', '')),
    };
  }

  if (tagName === 'VIDEO' || tagName === 'AUDIO') {
    return {
      tagName,
      currentTime: getNodeField(element, 'currentTime'),
      muted: getNodeField(element, 'muted'),
      paused: getNodeField(element, 'paused', true),
      playbackRate: getNodeField(element, 'playbackRate'),
      volume: getNodeField(element, 'volume'),
    };
  }

  return null;
}

function restoreControlState(element, state, newNode = null) {
  if (!state || safeString(getNodeField(element, 'tagName', '')).toUpperCase() !== state.tagName) return;

  const setControlProperty = (propertyName, value) => {
    try {
      element[propertyName] = value;
    } catch {
      // Some controls, such as file inputs, reject programmatic value assignment.
    }
  };

  if (state.tagName === 'INPUT' || state.tagName === 'TEXTAREA') {
    if (!state.isActive) {
      const nextValue = 'value' in (newNode || {}) ? getNodeField(newNode, 'value') : getElementAttribute(newNode, 'value');
      if (nextValue !== undefined && nextValue !== null && 'value' in (element || {})) {
        setControlProperty('value', nextValue);
      }
      const nextChecked = 'checked' in (newNode || {})
        ? getNodeField(newNode, 'checked')
        : hasElementAttribute(newNode, 'checked');
      if (nextChecked !== undefined && 'checked' in (element || {})) {
        setControlProperty('checked', Boolean(nextChecked));
      }
      return;
    }

    if ('value' in (element || {})) setControlProperty('value', state.value);
    if ('checked' in (element || {}) && state.checked !== undefined) setControlProperty('checked', state.checked);
    if (
      typeof element.setSelectionRange === 'function' &&
      typeof state.selectionStart === 'number' &&
      typeof state.selectionEnd === 'number'
    ) {
      try {
        element.setSelectionRange(state.selectionStart, state.selectionEnd, state.selectionDirection);
      } catch {
        // Some input types do not support selection ranges.
      }
    }
  }

  if (state.tagName === 'SELECT') {
    if (!state.isActive) return;

    const selectedValues = new Set(state.selectedValues || []);
    const options = getControlOptions(element);
    for (const option of options) {
      setNodeField(option, 'selected', selectedValues.has(getNodeField(option, 'value', '')));
    }
  }

  if (state.tagName === 'VIDEO' || state.tagName === 'AUDIO') {
    for (const [key, value] of Object.entries({
      currentTime: state.currentTime,
      muted: state.muted,
      playbackRate: state.playbackRate,
      volume: state.volume,
    })) {
      try {
        if (value !== undefined) element[key] = value;
      } catch {
        // Media properties can reject invalid values depending on ready state.
      }
    }
    if (!state.paused && typeof getNodeField(element, 'play') === 'function') {
      try {
        const playResult = element.play();
        playResult?.catch?.(() => {});
      } catch (error) {
        Logger.system.warn('[DOM] Failed to resume media playback.', error);
      }
    }
  }

  if (state.isActive && typeof getNodeField(element, 'focus') === 'function') {
    try {
      element.focus({ preventScroll: true });
    } catch (error) {
      Logger.system.warn('[DOM] Focus with preventScroll failed; retrying without options.', error);
      try {
        element.focus();
      } catch (fallbackError) {
        Logger.system.warn('[DOM] Failed to restore focus.', fallbackError);
      }
    }
  }
}

function findKeyedChild(parent, key, fromIndex) {
  if (!key) return null;
  const children = getChildNodes(parent);
  for (let index = fromIndex; index < children.length; index += 1) {
    if (getNodeKey(children[index]) === key) return children[index];
  }
  return null;
}

function moveChildBefore(parent, child, referenceNode) {
  if (child === referenceNode) return;
  if (typeof parent.insertBefore === 'function') {
    try {
      parent.insertBefore(child, referenceNode || null);
      return;
    } catch (error) {
      Logger.system.warn('[DOM] insertBefore failed while moving keyed child; falling back to childNodes splice.', error);
    }
  }

  const children = getNodeField(parent, 'childNodes');
  if (!children || typeof children.indexOf !== 'function' || typeof children.splice !== 'function') return;
  const currentIndex = children.indexOf(child);
  if (currentIndex < 0) return;
  children.splice(currentIndex, 1);
  const nextIndex = referenceNode ? children.indexOf(referenceNode) : children.length;
  children.splice(nextIndex < 0 ? children.length : nextIndex, 0, child);
}

export function dispatchHTMLeXDOMUpdated(root) {
  const runtimeDocument = getRuntimeDocument();
  if (typeof runtimeDocument?.dispatchEvent !== 'function') return;

  try {
    if (typeof CustomEvent === 'function') {
      runtimeDocument.dispatchEvent(new CustomEvent('htmlex:dom-updated', {
        detail: { root }
      }));
      return;
    }

    const event = typeof runtimeDocument.createEvent === 'function'
      ? runtimeDocument.createEvent('CustomEvent')
      : null;
    if (event?.initCustomEvent) {
      event.initCustomEvent('htmlex:dom-updated', false, false, { root });
      runtimeDocument.dispatchEvent(event);
    }
  } catch (error) {
    Logger.system.warn('[DOM] Failed to dispatch HTMLeX DOM update event.', error);
  }
}

export function hasHTMLeXMarkup(content) {
  return HTMLEX_MARKUP_PATTERN.test(safeString(content));
}

/**
 * Parses the target attribute into an array of target instructions.
 * @param {string} targetAttr - The target attribute string.
 * @returns {TargetInstruction[]} Array of parsed target instructions.
 */
export function parseTargets(targetAttr) {
  Logger.system.debug("[DOM] Parsing target attribute:", targetAttr);
  const input = safeString(targetAttr).trim();
  if (!input) {
    return [];
  }

  const targets = [];
  const targetPattern = new RegExp(`(.+?)\\((${TARGET_STRATEGIES})\\)(?:\\s+|$)`, 'gi');
  let match;
  while ((match = targetPattern.exec(input)) !== null) {
    const rawStrategy = match[2].trim();
    targets.push({
      selector: match[1].trim(),
      strategy: STRATEGY_BY_LOWERCASE[rawStrategy.toLowerCase()] || rawStrategy
    });
  }

  if (!targets.length) {
    targets.push({ selector: input, strategy: 'innerHTML' });
  }
  Logger.system.debug("[DOM] Parsed target instructions:", targets);
  return targets;
}

function getDefaultQueryRoot() {
  return getRuntimeDocument();
}

export function querySelectorSafe(selector, root = getDefaultQueryRoot()) {
  try {
    return root?.querySelector?.(selector) || null;
  } catch (error) {
    Logger.system.warn(`[DOM] Invalid selector "${selector}"`, error);
    return null;
  }
}

export function querySelectorAllResult(selector, root = getDefaultQueryRoot()) {
  try {
    return {
      matches: [...(root?.querySelectorAll?.(selector) || [])],
      valid: true,
    };
  } catch (error) {
    Logger.system.warn(`[DOM] Invalid selector "${selector}"`, error);
    return {
      matches: [],
      valid: false,
    };
  }
}

export function querySelectorAllSafe(selector, root = getDefaultQueryRoot()) {
  return querySelectorAllResult(selector, root).matches;
}

/**
 * Recursively diffs two DOM nodes and updates only parts that differ.
 * @param {Node} existingNode - The existing DOM node.
 * @param {Node} newNode - The new DOM node.
 */
export function diffAndUpdate(existingNode, newNode) {
  Logger.system.debug("[DOM] Diffing nodes:", existingNode, newNode);
  if (!existingNode || !newNode) return;
  if (
    getNodeField(existingNode, 'nodeType') !== getNodeField(newNode, 'nodeType') ||
    (isElementNode(existingNode) && getNodeField(existingNode, 'nodeName') !== getNodeField(newNode, 'nodeName'))
  ) {
    Logger.system.debug("[DOM] Nodes differ in type or tag; replacing node.");
    const clone = cloneNodeSafely(newNode, true);
    if (clone) replaceNodeSafely(existingNode, clone);
    return;
  }
  if (
    isElementNode(existingNode) &&
    (hasHTMLeXBehavior(existingNode) || hasHTMLeXBehavior(newNode)) &&
    getHTMLeXBehaviorSignature(existingNode) !== getHTMLeXBehaviorSignature(newNode)
  ) {
    Logger.system.debug("[DOM] HTMLeX behavior attributes changed; replacing node so it can be re-registered.");
    const clone = cloneNodeSafely(newNode, true);
    if (clone) replaceNodeSafely(existingNode, clone);
    return;
  }
  if (isTextNode(existingNode)) {
    const existingText = getNodeField(existingNode, 'textContent', '');
    const newText = getNodeField(newNode, 'textContent', '');
    if (existingText !== newText) {
      Logger.system.debug(
        `[DOM] Updating text from "${existingText}" to "${newText}"`
      );
      setNodeField(existingNode, 'textContent', newText);
    }
    return;
  }
  if (isElementNode(existingNode)) {
    const liveState = captureControlState(existingNode);
    Logger.system.debug("[DOM] Diffing attributes for element:", existingNode);
    const existingAttrs = getElementAttributes(existingNode);
    const newAttrs = getElementAttributes(newNode);
    for (let i = existingAttrs.length - 1; i >= 0; i--) {
      const attribute = existingAttrs[i];
      if (!attribute?.name) continue;
      if (!hasElementAttribute(newNode, attribute.name)) {
        Logger.system.debug(`[DOM] Removing attribute "${attribute.name}" from element:`, existingNode);
        removeElementAttribute(existingNode, attribute.name);
      }
    }
    for (let i = 0; i < newAttrs.length; i++) {
      const attribute = newAttrs[i];
      if (!attribute?.name) continue;
      if (getElementAttribute(existingNode, attribute.name) !== attribute.value) {
        Logger.system.debug(
          `[DOM] Updating attribute "${attribute.name}" to "${attribute.value}" on element:`,
          existingNode
        );
        setElementAttribute(existingNode, attribute.name, attribute.value);
      }
    }
    diffChildren(existingNode, newNode);
    restoreControlState(existingNode, liveState, newNode);
  }
}

/**
 * Recursively diffs the children of two elements.
 * @param {Element} existingParent - The current DOM element.
 * @param {Element} newParent - The new DOM element.
 */
export function diffChildren(existingParent, newParent) {
  Logger.system.debug(
    "[DOM] Diffing children of element:",
    existingParent,
    "with new element:",
    newParent
  );
  const newChildren = getChildNodes(newParent);
  for (let i = 0; i < newChildren.length; i++) {
    const newChild = newChildren[i];
    const newKey = getNodeKey(newChild);
    const keyedChild = findKeyedChild(existingParent, newKey, i);
    const existingChildren = getChildNodes(existingParent);
    if (keyedChild && keyedChild !== existingChildren[i]) {
      Logger.system.debug("[DOM] Moving keyed child into position:", newKey);
      moveChildBefore(existingParent, keyedChild, existingChildren[i] || null);
    }

    const existingChild = getChildNodes(existingParent)[i];
    if (!existingChild && newChild) {
      Logger.system.debug("[DOM] Appending new child:", newChild);
      const clone = cloneNodeSafely(newChild, true);
      if (clone) {
        try {
          existingParent.appendChild?.(clone);
        } catch (error) {
          Logger.system.warn('[DOM] Failed to append cloned child.', error);
        }
      }
    } else if (existingChild && newChild) {
      diffAndUpdate(existingChild, newChild);
    }
  }

  let existingChildren = getChildNodes(existingParent);
  while (existingChildren.length > newChildren.length) {
    const extraChild = existingChildren[existingChildren.length - 1];
    Logger.system.debug("[DOM] Removing extra child:", extraChild);
    removeNodeSafely(extraChild);
    const nextChildren = getChildNodes(existingParent);
    if (nextChildren.length === existingChildren.length) break;
    existingChildren = nextChildren;
  }
  Logger.system.debug("[DOM] Completed diffing children for element:", existingParent);
}

/**
 * Performs an innerHTML update on an element using a diff algorithm.
 * @param {Element} element - The element to update.
 * @param {string} newHTML - The new HTML content.
 */
export function performInnerHTMLUpdate(element, newHTML) {
  Logger.system.debug("[DOM] Performing innerHTML update on element:", element);
  const currentHTML = getInnerHTML(element);
  const newHTMLString = safeString(newHTML);
  if (currentHTML === newHTMLString) {
    Logger.system.debug("[DOM] No differences detected in innerHTML; skipping update.");
    return;
  }
  const runtimeDocument = getRuntimeDocument();
  if (typeof runtimeDocument?.createRange !== 'function') {
    Logger.system.debug("[DOM] Range API unavailable; falling back to direct innerHTML update.");
    setInnerHTML(element, newHTMLString);
    return;
  }
  Logger.system.debug("[DOM] Differences detected; performing partial update using diffing algorithm.");
  try {
    const range = runtimeDocument.createRange();
    range.selectNodeContents(element);
    const newFragment = range.createContextualFragment(newHTMLString);
    diffChildren(element, newFragment);
  } catch (error) {
    Logger.system.warn("[DOM] Diff update failed; falling back to direct innerHTML update.", error);
    setInnerHTML(element, newHTMLString);
    return;
  }
  if (getInnerHTML(element) !== newHTMLString) {
    Logger.system.debug("[DOM] Fallback: innerHTML mismatch after diffing; updating innerHTML directly.");
    setInnerHTML(element, newHTMLString);
  }
}

function getDocumentBodyFallback() {
  return getRuntimeDocument()?.body || null;
}

function replaceOuterHTML(targetElement, contentString) {
  const parent = getNodeField(targetElement, 'parentElement', null);
  const runtimeDocument = getRuntimeDocument();

  if (typeof runtimeDocument?.createRange === 'function') {
    try {
      const range = runtimeDocument.createRange();
      range.selectNode(targetElement);
      const fragment = range.createContextualFragment(contentString);
      const newNodes = getChildNodes(fragment);
      if (!newNodes.length) {
        Logger.system.warn("[DOM] outerHTML update failed: no new nodes generated from content.");
        return targetElement;
      }

      Logger.system.debug("[DOM] Replacing element with outerHTML strategy. New node count:", newNodes.length);
      if (!replaceNodeSafely(targetElement, fragment)) {
        throw new Error('replaceWith failed');
      }
      return parent || newNodes.find(isElementNode) || getDocumentBodyFallback() || targetElement;
    } catch (error) {
      Logger.system.warn("[DOM] Range outerHTML update failed; falling back to direct outerHTML update.", error);
    }
  }

  if ('outerHTML' in (targetElement || {})) {
    if (!setNodeField(targetElement, 'outerHTML', contentString)) {
      return targetElement;
    }
    return parent || getDocumentBodyFallback() || targetElement;
  }

  Logger.system.warn("[DOM] outerHTML update failed: no Range API or outerHTML fallback is available.");
  return targetElement;
}

function insertAdjacentHTMLSafely(targetElement, position, contentString) {
  try {
    targetElement?.insertAdjacentHTML?.(position, contentString);
    return true;
  } catch (error) {
    Logger.system.warn(`[DOM] Failed to insert adjacent HTML at "${position}".`, error);
    return false;
  }
}

/**
 * Updates target elements with new content based on the update strategy.
 * @param {TargetInstruction} target - The target instruction.
 * @param {string} content - The HTML content to update.
 * @param {Element|null} [resolvedElement=null] - Explicit element used for `this(...)` or forced resolved updates.
 * @param {object} [options={}]
 * @param {boolean} [options.forceResolvedElement=false] - Use the resolved element without querying the selector again.
 */
export function updateTarget(target, content, resolvedElement = null, options = {}) {
  const contentString = safeString(content);
  const selector = safeString(getTargetField(target, 'selector', '')).trim();
  const normalizedSelector = selector.toLowerCase();
  const strategy = safeString(getTargetField(target, 'strategy', 'innerHTML'), 'innerHTML') || 'innerHTML';
  Logger.system.debug(
    "[DOM] Updating target with instruction:",
    target,
    "and content length:",
    contentString.length
  );
  const useResolvedElement = resolvedElement && (
    getTargetField(options, 'forceResolvedElement', false) ||
    normalizedSelector === 'this'
  );
  const elements = useResolvedElement
    ? [resolvedElement]
    : querySelectorAllSafe(selector);
  for (const targetElement of elements) {
    let registrationRoot = targetElement;
    Logger.system.debug(
      `[DOM] Updating element(s) matching "${selector}" using strategy "${strategy}"`,
      targetElement
    );
    switch (strategy) {
      case 'innerHTML':
        performInnerHTMLUpdate(targetElement, contentString);
        break;
      case 'outerHTML':
        registrationRoot = replaceOuterHTML(targetElement, contentString);
        break;
      case 'append':
        Logger.system.debug("[DOM] Appending content to element:", targetElement);
        insertAdjacentHTMLSafely(targetElement, 'beforeend', contentString);
        break;
      case 'prepend':
        Logger.system.debug("[DOM] Prepending content to element:", targetElement);
        insertAdjacentHTMLSafely(targetElement, 'afterbegin', contentString);
        break;
      case 'before':
        Logger.system.debug("[DOM] Inserting content before element:", targetElement);
        insertAdjacentHTMLSafely(targetElement, 'beforebegin', contentString);
        registrationRoot = getNodeField(targetElement, 'parentElement', null) || getDocumentBodyFallback() || targetElement;
        break;
      case 'after':
        Logger.system.debug("[DOM] Inserting content after element:", targetElement);
        insertAdjacentHTMLSafely(targetElement, 'afterend', contentString);
        registrationRoot = getNodeField(targetElement, 'parentElement', null) || getDocumentBodyFallback() || targetElement;
        break;
      case 'remove':
        Logger.system.debug("[DOM] Removing element:", targetElement);
        removeNodeSafely(targetElement);
        registrationRoot = getDocumentBodyFallback() || targetElement;
        break;
      default:
        Logger.system.debug("[DOM] Default update strategy; updating innerHTML of element:", targetElement);
        setInnerHTML(targetElement, contentString);
    }
    if (hasHTMLeXMarkup(contentString)) {
      dispatchHTMLeXDOMUpdated(registrationRoot);
    }
  }
}
