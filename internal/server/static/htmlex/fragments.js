/**
 * @module Fragments
 * @description Processes a streaming HTTP response that uses <fragment> tags,
 * extracting their contents and updating the DOM.
 *
 * As complete <fragment> blocks are found in the stream, each is parsed and
 * its inner content is immediately inserted into its target using the specified
 * replacement strategy. Inserted HTMLeX nodes are registered by the shared DOM
 * update notification path.
 */

import { Logger } from './logger.js';
import { parseTargets, querySelectorAllResult } from './dom.js';
import { patchedUpdateTarget } from './registration.js';

function safeString(value, fallback = '') {
  try {
    return String(value ?? fallback);
  } catch (error) {
    Logger.system.warn('[FRAG] Failed to coerce value to string.', error);
    return fallback;
  }
}

function getRuntimeDocument() {
  try {
    return typeof document === 'undefined' ? globalThis.document : document;
  } catch (error) {
    Logger.system.warn('[FRAG] Failed to read document.', error);
    return null;
  }
}

function getObjectField(value, fieldName, fallback = undefined) {
  try {
    return value?.[fieldName] ?? fallback;
  } catch (error) {
    Logger.system.warn(`[FRAG] Failed to read ${fieldName}.`, error);
    return fallback;
  }
}

function setObjectField(value, fieldName, nextValue) {
  try {
    value[fieldName] = nextValue;
    return true;
  } catch (error) {
    Logger.system.warn(`[FRAG] Failed to set ${fieldName}.`, error);
    return false;
  }
}

function getOrCreateUpdateQueue(owner, fieldName) {
  let queue = getObjectField(owner, fieldName, null);
  if (Array.isArray(queue)) return queue;

  queue = [];
  return setObjectField(owner, fieldName, queue) ? queue : null;
}

function pushQueuedUpdate(owner, fieldName, updateFn) {
  const queue = getOrCreateUpdateQueue(owner, fieldName);
  if (!queue) return false;

  try {
    queue.push(updateFn);
    return true;
  } catch (error) {
    Logger.system.warn(`[FRAG] Failed to queue update in ${fieldName}.`, error);
    return false;
  }
}

function getElementAttribute(element, attributeName) {
  try {
    return element?.getAttribute?.(attributeName) ?? null;
  } catch (error) {
    Logger.system.warn(`[FRAG] Failed to read ${attributeName} attribute.`, error);
    return null;
  }
}

function hasElementAttribute(element, attributeName) {
  try {
    return Boolean(element?.hasAttribute?.(attributeName));
  } catch (error) {
    Logger.system.warn(`[FRAG] Failed to check ${attributeName} attribute.`, error);
    return false;
  }
}

function parseFragmentStatus(value) {
  const normalizedValue = safeString(value).trim();
  if (!/^\d{3}$/u.test(normalizedValue)) return null;

  const statusCode = Number.parseInt(normalizedValue, 10);
  return statusCode >= 100 && statusCode <= 599 ? statusCode : null;
}

function parseFragmentElement(fragmentHtml) {
  const runtimeDocument = getRuntimeDocument();
  if (typeof runtimeDocument?.createElement !== 'function') {
    Logger.system.warn('[FRAG] Document template parsing is unavailable. Skipping fragment.');
    return null;
  }

  try {
    const template = runtimeDocument.createElement('template');
    template.innerHTML = fragmentHtml;
    return getObjectField(getObjectField(template, 'content', null), 'firstElementChild', null);
  } catch (error) {
    Logger.system.warn('[FRAG] Failed to parse fragment HTML. Skipping fragment.', error);
    return null;
  }
}

function parseTargetsSafely(targetAttribute, context) {
  try {
    return parseTargets(targetAttribute);
  } catch (error) {
    Logger.system.warn(`[FRAG] Failed to parse ${context} targets.`, error);
    return [];
  }
}

function getTargetSelector(target) {
  return safeString(getObjectField(target, 'selector', '')).trim();
}

function isThisTarget(target) {
  return getTargetSelector(target).toLowerCase() === 'this';
}

function createAfterUpdateCallback(swapLifecycle) {
  try {
    return swapLifecycle?.createUpdateCallback?.() || null;
  } catch (error) {
    Logger.system.warn('[FRAG] Failed to create swap lifecycle callback.', error);
    return null;
  }
}

function completeAfterUpdate(afterUpdate) {
  try {
    afterUpdate?.();
  } catch (error) {
    Logger.system.warn('[FRAG] Swap lifecycle callback failed.', error);
  }
}

function applyFragmentUpdate(target, content, targetElement, options, afterUpdate) {
  try {
    patchedUpdateTarget(target, content, targetElement, options);
  } catch (error) {
    Logger.system.error('[FRAG] Fragment target update failed.', error);
  } finally {
    completeAfterUpdate(afterUpdate);
  }
}

function getResolvedTargetElements(target, triggeringElement) {
  const selector = getTargetSelector(target);
  if (selector.toLowerCase() === 'this') {
    return triggeringElement ? [triggeringElement] : [];
  }

  const targetResult = querySelectorAllResult(selector);
  if (!targetResult.valid) {
    Logger.system.warn(`[FRAG] Invalid fragment target selector "${selector}". Skipping update.`);
    return [];
  }

  if (targetResult.matches.length > 0) {
    return targetResult.matches;
  }

  Logger.system.debug(`[FRAG] No elements found for selector "${selector}". Falling back to triggering element.`);
  return triggeringElement ? [triggeringElement] : [];
}

/**
 * Processes the current buffer by extracting complete <fragment> blocks.
 * For each complete fragment found, it extracts the inner content and updates the DOM.
 * If the triggering element is in streaming mode (as indicated by _htmlexStreaming),
 * updates are applied immediately (bypassing sequential queuing). Otherwise, if the triggering
 * element is in sequential mode, the update is queued; if neither, the update is applied immediately.
 * After insertion, the code scans each target element for any child elements with a timer attribute
 * (that have not yet been processed) and sets up their timers.
 * Returns the buffer with all complete fragments removed.
 *
 * @param {string} buffer - The current accumulated buffer from the stream.
 * @param {Element} [triggeringElement=null] - The element that triggered the API call.
 * @param {object|null} [sequentialEntry=null] - Request-specific queue for sequential updates.
 * @param {object|null} [swapLifecycle=null] - Lifecycle tracker notified after each DOM update.
 * @returns {string} The buffer with complete fragments removed.
 */
export function processFragmentBuffer(buffer, triggeringElement = null, sequentialEntry = null, swapLifecycle = null) {
  const bufferString = safeString(buffer);
  Logger.system.debug("[FRAG] Processing fragment buffer. Buffer length:", bufferString.length);
  const fragmentRegex = /<fragment\b[^>]*>[\s\S]*?<\/fragment>/gi;
  let match;

  while ((match = fragmentRegex.exec(bufferString)) !== null) {
    if (triggeringElement) {
      setObjectField(triggeringElement, '_htmlexFragmentsProcessed', true);
    }
    const fragmentHtml = match[0];
    Logger.system.debug("[FRAG] Found fragment HTML:", fragmentHtml);

    const fragmentElement = parseFragmentElement(fragmentHtml);
    if (!fragmentElement) {
      Logger.system.debug("[FRAG] No valid fragment element found in parsed HTML.");
      continue;
    }

    const statusCode = parseFragmentStatus(getElementAttribute(fragmentElement, 'status'));
    if (triggeringElement && statusCode !== null && statusCode >= 400) {
      const currentStatus = parseFragmentStatus(getObjectField(triggeringElement, '_htmlexFragmentErrorStatus'));
      setObjectField(triggeringElement, '_htmlexFragmentErrorStatus', Number.isFinite(currentStatus)
        ? Math.max(currentStatus, statusCode)
        : statusCode);
      Logger.system.warn(`[FRAG] Fragment reported error status ${statusCode}.`);
    }

    let fragmentTargetAttribute = getElementAttribute(fragmentElement, 'target');
    if (!fragmentTargetAttribute) {
      Logger.system.warn("[FRAG] Fragment found without target attribute. Defaulting to 'this(innerHTML)'.");
      fragmentTargetAttribute = "this(innerHTML)";
    }
    Logger.system.debug("[FRAG] Fragment target attribute:", fragmentTargetAttribute);

    // Extract raw inner content so context-sensitive markup such as <tr> survives
    // until the target element can parse it in the right DOM context.
    const content = fragmentHtml
      .replace(/^<fragment\b[^>]*>/i, '')
      .replace(/<\/fragment>\s*$/i, '');
    Logger.system.debug("[FRAG] Extracted fragment content:", content);

    const fragmentTargets = parseTargetsSafely(fragmentTargetAttribute, 'fragment');
    Logger.system.debug("[FRAG] Parsed fragment targets:", fragmentTargets);

    for (const fragmentTarget of fragmentTargets) {
      let targetsToApply = [fragmentTarget];
      if (isThisTarget(fragmentTarget)) {
        Logger.system.debug("[FRAG] Fragment target selector is 'this'. Checking triggering element for an overriding target.");
        if (triggeringElement && hasElementAttribute(triggeringElement, "target")) {
          const callerTargets = parseTargetsSafely(getElementAttribute(triggeringElement, "target"), 'caller override');
          if (callerTargets.length > 0) {
            Logger.system.debug("[FRAG] Overriding fragment target with caller target(s):", callerTargets);
            targetsToApply = callerTargets;
          } else {
            Logger.system.debug("[FRAG] Triggering element has no valid target attribute. Using triggering element as target.");
            targetsToApply = [{ ...fragmentTarget, selector: "this" }];
          }
        } else {
          Logger.system.debug("[FRAG] No overriding target on triggering element. Using triggering element as target.");
        }
      }

      for (const target of targetsToApply) {
        const targetElements = getResolvedTargetElements(target, triggeringElement);

        if (!targetElements || targetElements.length === 0) {
          Logger.system.warn("[FRAG] No elements resolved for fragment target:", getTargetSelector(target));
          continue;
        }

        for (const targetElement of targetElements) {
          const afterUpdate = createAfterUpdateCallback(swapLifecycle);
          if (triggeringElement && getObjectField(triggeringElement, '_htmlexStreaming', false)) {
            Logger.system.debug("[FRAG] Streaming active: updating fragment immediately.");
            applyFragmentUpdate(target, content, targetElement, { forceResolvedElement: true }, afterUpdate);
          } else if (triggeringElement && getObjectField(triggeringElement, '_htmlexSequentialMode', false)) {
            Logger.system.debug("[FRAG] Queuing fragment update because triggering element is sequential.");
            if (sequentialEntry) {
              pushQueuedUpdate(sequentialEntry, 'updates', () => {
                applyFragmentUpdate(target, content, targetElement, { forceResolvedElement: true, queueSequential: false }, afterUpdate);
              });
            } else {
              if (!getObjectField(triggeringElement, '_htmlexSequentialUpdates', null)) {
                setObjectField(triggeringElement, '_htmlexSequentialUpdatesCursor', 0);
              }
              pushQueuedUpdate(triggeringElement, '_htmlexSequentialUpdates', () => {
                applyFragmentUpdate(target, content, targetElement, { forceResolvedElement: true, queueSequential: false }, afterUpdate);
              });
            }
          } else {
            applyFragmentUpdate(target, content, targetElement, { forceResolvedElement: true }, afterUpdate);
          }
        }
      }
    }
  }

  const newBuffer = bufferString.replace(fragmentRegex, '');
  Logger.system.debug("[FRAG] Buffer after removing processed fragments. New buffer length:", newBuffer.length);
  return newBuffer;
}
